package admin

// entry_protocol_stats.go answers the protocols screen: for each protocol and
// each machine under it, is it alive, what did the vantage say and when, how
// much does it carry, for how many subscribers, at what cost.
//
// Unlike the overview (entry_overview.go), this screen is an inventory on
// purpose: it is what the owner opens to see in ten seconds which protocol
// lives and how much it carries. Machines handed back are not on it.
//
// Liveness, subscribers and cost come from Postgres; verdicts and traffic from
// the dataset. Without the dataset those figures are absent and named in
// `missing`, the same rule as the overview: never a plausible default.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

// connsFreshFor is how old a relay's last pulse may be for its connection
// count to still read as "now". The edge pulses every half minute, and
// EntryRelayDeltas calls a gap past two minutes a silence.
const connsFreshFor = 2 * time.Minute

// verdictSkew bounds a probe row's date: a vantage's clock is a little off,
// and a row from the future would stay the "latest verdict" for as long as
// the dataset keeps it (the detector's bound, detector.go).
const verdictSkew = 2 * time.Minute

// Health of a relay, a machine or a protocol, as the console colours it.
const (
	healthOK      = "ok"      // the vantage gets through
	healthWarn    = "warn"    // some of it does, some does not
	healthBad     = "bad"     // the vantage does not get through
	healthUnknown = "unknown" // no verdict to go by
)

type entryProtocolStats struct {
	Window        string          `json:"window"`
	BucketSeconds int             `json:"bucketSeconds"`
	Protocols     []protocolStats `json:"protocols"`
	Missing       []missingFigure `json:"missing"`
}

// protocolStats is one protocol's row with its machines under it. Figures are
// sums over its machines, except Subscribers: one subscription on two of its
// machines is one subscriber.
type protocolStats struct {
	Proto        string `json:"proto"`
	BoxesLive    int    `json:"boxesLive"`
	BoxesTotal   int    `json:"boxesTotal"`
	RelaysAlive  int    `json:"relaysAlive"`
	RelaysTotal  int    `json:"relaysTotal"`
	Health       string `json:"health"`
	BytesUp      uint64 `json:"bytesUp"`
	BytesDown    uint64 `json:"bytesDown"`
	Conns        uint64 `json:"conns"`
	Subscribers  int    `json:"subscribers"`
	MonthlyCents int    `json:"monthlyCents"`
	// BoxesUnpriced is how many machines carry no price: MonthlyCents
	// understates by their rent, and that is said rather than hidden.
	BoxesUnpriced int             `json:"boxesUnpriced"`
	Boxes         []protocolBoxes `json:"boxes"`
}

type protocolBoxes struct {
	Name         string          `json:"name"`
	Location     string          `json:"location"`
	Domain       string          `json:"domain,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	MonthlyCents *int            `json:"monthlyCents"`
	Health       string          `json:"health"`
	Subscribers  int             `json:"subscribers"`
	Relays       []protocolRelay `json:"relays"`
}

type protocolRelay struct {
	Name      string     `json:"name"`
	Port      int        `json:"port"`
	Status    string     `json:"status"`
	Health    string     `json:"health"`
	Verdict   string     `json:"verdict"`
	VerdictAt *time.Time `json:"verdictAt,omitempty"`
	// History is one point per bucket of the window that holds a probe,
	// oldest first; a bucket nobody probed is absent, not clean.
	History   []verdictPoint `json:"history"`
	BytesUp   uint64         `json:"bytesUp"`
	BytesDown uint64         `json:"bytesDown"`
	// Conns is the last pulse's count, nil when that pulse is too old to
	// read as now.
	Conns       *uint64    `json:"conns"`
	PulseAt     *time.Time `json:"pulseAt,omitempty"`
	Subscribers int        `json:"subscribers"`
}

// verdictPoint is one bucket of probes against one relay: how many got
// through, how many did not, and the worst thing said.
type verdictPoint struct {
	Bucket time.Time `json:"bucket"`
	OK     uint64    `json:"ok"`
	Bad    uint64    `json:"bad"`
	Other  uint64    `json:"other"`
	Worst  string    `json:"worst"`
}

// verdictHealth is what one verdict says about getting through. `reality_wins`
// is a pass: admission reads it the same way (provision.go).
func verdictHealth(v string) string {
	switch v {
	case entry.VerdictClean, entry.VerdictRealityWins:
		return healthOK
	case entry.VerdictIPBanned, entry.VerdictFingerprint, entry.CauseDomainBlocked:
		return healthBad
	}
	return healthUnknown
}

// foldHealth is the health of several things together. Unjudged ones say
// nothing, so they do not lower the others; nothing but unjudged is unknown.
func foldHealth(hs []string) string {
	var ok, bad bool
	for _, h := range hs {
		switch h {
		case healthOK:
			ok = true
		case healthBad:
			bad = true
		case healthWarn:
			ok, bad = true, true
		}
	}
	switch {
	case ok && bad:
		return healthWarn
	case ok:
		return healthOK
	case bad:
		return healthBad
	}
	return healthUnknown
}

// EntryProtocolStats answers GET /api/v1/admin/entry/protocols.
func (h *Handlers) EntryProtocolStats(w http.ResponseWriter, r *http.Request) {
	window, label, ok := overviewRange(r.URL.Query().Get("window"))
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	bucket := verdictBucket(window)
	out := entryProtocolStats{
		Window: label, BucketSeconds: int(bucket.Seconds()),
		Protocols: []protocolStats{}, Missing: []missingFigure{},
	}

	boxes, protoOf, err := h.protocolBoxes(ctx)
	if err != nil {
		log.Printf("admin entry protocols: boxes: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	subs, err := h.protocolSubscribers(ctx)
	if err != nil {
		log.Printf("admin entry protocols: subscribers: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	var data relayFigures
	if h.analytics == nil {
		why := "analytics is not configured on this hub"
		out.Missing = append(out.Missing,
			missingFigure{Figure: "verdicts", Why: why}, missingFigure{Figure: "traffic", Why: why})
	} else {
		since := now.Add(-window)
		if data, err = h.relayFigures(ctx, since, now, bucket); err != nil {
			log.Printf("admin entry protocols: dataset: %v", err)
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
	}

	out.Protocols = rollupProtocols(boxes, protoOf, subs, data, now)
	writeJSONResponse(w, out)
}

// verdictBucket keeps a relay's history strip to a few dozen points.
func verdictBucket(window time.Duration) time.Duration {
	switch {
	case window <= 24*time.Hour:
		return time.Hour
	case window <= 7*24*time.Hour:
		return 6 * time.Hour
	}
	return 24 * time.Hour
}

// protocolBoxes reads the machines in service with their relays, and each
// machine's protocol.
func (h *Handlers) protocolBoxes(ctx context.Context) ([]protocolBoxes, map[string]string, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT name, proto, location, created_at, monthly_cents, domain_ct
		FROM entry_boxes WHERE retired_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, nil, fmt.Errorf("boxes: %w", err)
	}
	defer rows.Close()
	boxes := []protocolBoxes{}
	protoOf := map[string]string{}
	index := map[string]int{}
	for rows.Next() {
		var b protocolBoxes
		var proto string
		var domainCT []byte
		if err := rows.Scan(&b.Name, &proto, &b.Location, &b.CreatedAt, &b.MonthlyCents, &domainCT); err != nil {
			return nil, nil, fmt.Errorf("scan box: %w", err)
		}
		if domainCT != nil {
			// Unreadable, the row stays: the domain is a label here.
			if d, err := cryptobox.Decrypt(domainCT); err == nil {
				b.Domain = string(d)
			}
		}
		b.Relays = []protocolRelay{}
		index[b.Name] = len(boxes)
		protoOf[b.Name] = proto
		boxes = append(boxes, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	relayRows, err := h.pool.Query(ctx, `
		SELECT r.name, r.box, r.status, r.identity_ct FROM entry_relays r
		JOIN entry_boxes b ON b.name = r.box
		WHERE b.retired_at IS NULL ORDER BY r.name`)
	if err != nil {
		return nil, nil, fmt.Errorf("relays: %w", err)
	}
	defer relayRows.Close()
	for relayRows.Next() {
		var r protocolRelay
		var box string
		var identityCT []byte
		if err := relayRows.Scan(&r.Name, &box, &r.Status, &identityCT); err != nil {
			return nil, nil, fmt.Errorf("scan relay: %w", err)
		}
		var face fleetRelay
		openRelayFace(&face, identityCT)
		r.Port = face.Port
		if i, ok := index[box]; ok {
			boxes[i].Relays = append(boxes[i].Relays, r)
		}
	}
	return boxes, protoOf, relayRows.Err()
}

// relaySubscriber says one live subscription holds a grant on one relay.
type relaySubscriber struct {
	account, relay, box, proto string
}

// protocolSubscribers reads who holds what: live subscriptions with a standing
// grant, live as the fleet's own sizing counts them (subscribers.go). A revoked
// or lapsed subscription's grant is nobody's traffic.
func (h *Handlers) protocolSubscribers(ctx context.Context) ([]relaySubscriber, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT DISTINCT g.account_id::text, r.name, r.box, b.proto
		FROM entry_grants g
		JOIN entry_relays r ON r.id = g.relay_id
		JOIN entry_boxes b ON b.name = r.box
		JOIN hub_subscriptions s ON s.account_id = g.account_id
		WHERE g.revoked_at IS NULL AND b.retired_at IS NULL
		  AND `+subscription.LiveTerms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []relaySubscriber{}
	for rows.Next() {
		var s relaySubscriber
		if err := rows.Scan(&s.account, &s.relay, &s.box, &s.proto); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// relayFigures is what the dataset says of each relay over the window.
type relayFigures struct {
	verdict map[string]lastVerdict
	history map[string][]verdictPoint
	traffic map[string]relayTraffic
	pulse   map[string]relayPulse
}

type lastVerdict struct {
	verdict string
	at      time.Time
}

type relayTraffic struct{ up, down uint64 }

type relayPulse struct {
	conns uint64
	at    time.Time
}

func (h *Handlers) relayFigures(ctx context.Context, since, now time.Time, bucket time.Duration) (relayFigures, error) {
	out := relayFigures{
		verdict: map[string]lastVerdict{}, history: map[string][]verdictPoint{},
		traffic: map[string]relayTraffic{}, pulse: map[string]relayPulse{},
	}
	until := now.Add(verdictSkew)

	last, err := h.analytics.Query(ctx, `
		SELECT relay_name, toString(argMax(verdict, ts)), max(ts)
		FROM entry_probes WHERE ts >= ? AND ts <= ?
		GROUP BY relay_name`, since, until)
	if err != nil {
		return out, fmt.Errorf("last verdicts: %w", err)
	}
	defer last.Close()
	for last.Next() {
		var relay string
		var v lastVerdict
		if err := last.Scan(&relay, &v.verdict, &v.at); err != nil {
			return out, fmt.Errorf("scan last verdict: %w", err)
		}
		out.verdict[relay] = v
	}
	if err := last.Err(); err != nil {
		return out, err
	}

	hist, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toString(verdict), count()
		FROM entry_probes WHERE ts >= ? AND ts <= ?
		GROUP BY relay_name, bucket, verdict
		ORDER BY relay_name, bucket`, int64(bucket.Seconds()), since, until)
	if err != nil {
		return out, fmt.Errorf("verdict history: %w", err)
	}
	defer hist.Close()
	for hist.Next() {
		var relay, verdict string
		var at time.Time
		var n uint64
		if err := hist.Scan(&relay, &at, &verdict, &n); err != nil {
			return out, fmt.Errorf("scan verdict history: %w", err)
		}
		out.history[relay] = addVerdict(out.history[relay], at, verdict, n)
	}
	if err := hist.Err(); err != nil {
		return out, err
	}

	traffic, err := h.analytics.Query(ctx, `
		SELECT relay_name, sum(d_up), sum(d_down)
		FROM (`+analytics.EntryRelayDeltas+`)
		GROUP BY relay_name`, since)
	if err != nil {
		return out, fmt.Errorf("traffic: %w", err)
	}
	defer traffic.Close()
	for traffic.Next() {
		var relay string
		var t relayTraffic
		if err := traffic.Scan(&relay, &t.up, &t.down); err != nil {
			return out, fmt.Errorf("scan traffic: %w", err)
		}
		out.traffic[relay] = t
	}
	if err := traffic.Err(); err != nil {
		return out, err
	}

	pulses, err := h.analytics.Query(ctx, `
		SELECT relay_name, argMax(conns, ts), max(ts)
		FROM entry_relay_stats WHERE ts >= ?
		GROUP BY relay_name`, since)
	if err != nil {
		return out, fmt.Errorf("pulses: %w", err)
	}
	defer pulses.Close()
	for pulses.Next() {
		var relay string
		var p relayPulse
		if err := pulses.Scan(&relay, &p.conns, &p.at); err != nil {
			return out, fmt.Errorf("scan pulse: %w", err)
		}
		out.pulse[relay] = p
	}
	return out, pulses.Err()
}

// addVerdict folds one (bucket, verdict, count) row into a relay's history.
// Rows come ordered by bucket, so a bucket is the last point or a new one.
func addVerdict(points []verdictPoint, at time.Time, verdict string, n uint64) []verdictPoint {
	if len(points) == 0 || !points[len(points)-1].Bucket.Equal(at) {
		points = append(points, verdictPoint{Bucket: at})
	}
	p := &points[len(points)-1]
	switch verdictHealth(verdict) {
	case healthOK:
		p.OK += n
	case healthBad:
		p.Bad += n
	default:
		p.Other += n
	}
	if p.Worst == "" || verdictRank(verdict) > verdictRank(p.Worst) {
		p.Worst = verdict
	}
	return points
}

// verdictRank orders verdicts by how bad they are: a bucket with one ban in it
// is a bucket with a ban, however many clean rounds surround it.
func verdictRank(v string) int {
	switch verdictHealth(v) {
	case healthBad:
		return 2
	case healthUnknown:
		return 1
	}
	return 0
}

// rollupProtocols joins the machines with what the dataset and the grants say,
// and sums each protocol. A machine counts as live with one relay alive; a
// protocol's health reads only relays alive, since a dead relay is already
// replaced or owed and its old verdict says nothing about the protocol now.
func rollupProtocols(boxes []protocolBoxes, protoOf map[string]string, subs []relaySubscriber, data relayFigures, now time.Time) []protocolStats {
	onRelay, onBox, onProto := map[string]int{}, map[string]int{}, map[string]int{}
	seenBox, seenProto := map[string]bool{}, map[string]bool{}
	for _, s := range subs {
		onRelay[s.relay]++
		if k := s.account + "\x00" + s.box; !seenBox[k] {
			seenBox[k] = true
			onBox[s.box]++
		}
		if k := s.account + "\x00" + s.proto; !seenProto[k] {
			seenProto[k] = true
			onProto[s.proto]++
		}
	}

	byProto := map[string]*protocolStats{}
	for _, b := range boxes {
		proto := protoOf[b.Name]
		p := byProto[proto]
		if p == nil {
			p = &protocolStats{Proto: proto, Subscribers: onProto[proto], Boxes: []protocolBoxes{}}
			byProto[proto] = p
		}
		p.BoxesTotal++
		if b.MonthlyCents == nil {
			p.BoxesUnpriced++
		} else {
			p.MonthlyCents += *b.MonthlyCents
		}
		b.Subscribers = onBox[b.Name]
		alive := 0
		health := make([]string, 0, len(b.Relays))
		for i := range b.Relays {
			r := &b.Relays[i]
			r.Subscribers = onRelay[r.Name]
			r.Verdict, r.Health = "", healthUnknown
			if v, ok := data.verdict[r.Name]; ok {
				at := v.at
				r.Verdict, r.Health, r.VerdictAt = v.verdict, verdictHealth(v.verdict), &at
			}
			r.History = data.history[r.Name]
			if r.History == nil {
				r.History = []verdictPoint{}
			}
			t := data.traffic[r.Name]
			r.BytesUp, r.BytesDown = t.up, t.down
			p.BytesUp += t.up
			p.BytesDown += t.down
			if pulse, ok := data.pulse[r.Name]; ok {
				at := pulse.at
				r.PulseAt = &at
				if now.Sub(at) <= connsFreshFor {
					conns := pulse.conns
					r.Conns = &conns
					p.Conns += conns
				}
			}
			p.RelaysTotal++
			if r.Status == "alive" {
				alive++
				p.RelaysAlive++
				health = append(health, r.Health)
			}
		}
		if alive > 0 {
			p.BoxesLive++
		}
		b.Health = foldHealth(health)
		p.Boxes = append(p.Boxes, b)
	}

	out := make([]protocolStats, 0, len(byProto))
	for _, p := range byProto {
		boxHealth := make([]string, 0, len(p.Boxes))
		for _, b := range p.Boxes {
			boxHealth = append(boxHealth, b.Health)
		}
		p.Health = foldHealth(boxHealth)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Proto < out[j].Proto })
	return out
}
