package admin

// entry_protocol_stats.go answers the protocols screen: how each protocol and
// each machine under it lived over the window. Every figure is a series on the
// window's buckets, because the console draws data as history, never as a
// number of the moment (the owner's rule).
//
// Life, subscribers and cost are rebuilt from Postgres, by reading each row's
// interval at the end of every bucket. Pass rate, traffic and connections come
// from the dataset. A bucket nobody measured is null, drawn as a gap: a relay
// that did not pulse carried an unknown amount, not zero. Without the dataset
// those three are named in `missing`.
//
// Two readings are approximate, and only these two:
//   - A relay counts from its creation if it was ever admitted: signed_at moves
//     on every re-signing, so the moment of admission is not kept. Admission
//     takes minutes, and the narrowest bucket is an hour.
//   - A subscription counts until its current expiry and revocation. A renewal
//     rewrites the expiry, and a period's traffic running out leaves no date.

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
)

// verdictSkew bounds a probe row's date: a vantage's clock is a little off, and
// a row from the future must not count (the detector's bound, detector.go).
const verdictSkew = 2 * time.Minute

type entryProtocolStats struct {
	Window        string `json:"window"`
	BucketSeconds int    `json:"bucketSeconds"`
	// Buckets is the one time axis every series below is drawn on.
	Buckets   []string         `json:"buckets"`
	Protocols []protocolSeries `json:"protocols"`
	// Nodes is every machine that had an admitted relay in the window, the
	// ones handed back since included: their history is still history.
	Nodes   []protocolSeries `json:"nodes"`
	Missing []missingFigure  `json:"missing"`
}

// protocolSeries is one protocol or one machine, one value per bucket.
type protocolSeries struct {
	Key     string `json:"key"`
	Proto   string `json:"proto"`
	Domain  string `json:"domain,omitempty"`
	Retired bool   `json:"retired,omitempty"`
	// Live is machines alive for a protocol, relays alive for a machine.
	Live []*float64 `json:"live"`
	// PassPct is the share of the vantage's probes that got through; probes
	// that could not judge are not in it.
	PassPct      []*float64 `json:"passPct"`
	TrafficBytes []*float64 `json:"trafficBytes"`
	Conns        []*float64 `json:"conns"`
	Subscribers  []*float64 `json:"subscribers"`
	CostCents    []*float64 `json:"costCents"`
}

// historyBox is one machine's life, as the bucket walk reads it.
type historyBox struct {
	name, proto, domain string
	createdAt           time.Time
	retiredAt           *time.Time
	monthlyCents        *int
	relays              []historyRelay
}

type historyRelay struct {
	name      string
	createdAt time.Time
	diedAt    *time.Time
	admitted  bool
}

// historyGrant is one account's hold on one relay: the grant's interval cut by
// its subscription's.
type historyGrant struct {
	account, relay, box, proto string
	from                       time.Time
	until                      *time.Time
}

// relaySeries is what the dataset says of each relay, bucket by bucket.
type relaySeries struct {
	probes  map[string]map[int64]probeCount
	traffic map[string]map[int64]float64
	conns   map[string]map[int64]float64
}

type probeCount struct{ ok, bad uint64 }

// EntryProtocolStats answers GET /api/v1/admin/entry/protocols.
func (h *Handlers) EntryProtocolStats(w http.ResponseWriter, r *http.Request) {
	window, label, ok := overviewRange(r.URL.Query().Get("window"))
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	since := now.Add(-window)
	bucket := seriesBucket(window)
	starts := bucketStarts(since, now, bucket)

	boxes, err := h.historyBoxes(ctx, starts[0])
	if err != nil {
		log.Printf("admin entry protocols: boxes: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	grants, err := h.historyGrants(ctx, starts[0])
	if err != nil {
		log.Printf("admin entry protocols: grants: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	out := entryProtocolStats{
		Window: label, BucketSeconds: int(bucket.Seconds()),
		Buckets: make([]string, len(starts)), Missing: []missingFigure{},
	}
	for i, at := range starts {
		out.Buckets[i] = at.Format(time.RFC3339)
	}
	data := relaySeries{}
	if h.analytics == nil {
		why := "analytics is not configured on this hub"
		out.Missing = append(out.Missing,
			missingFigure{Figure: "passPct", Why: why},
			missingFigure{Figure: "trafficBytes", Why: why},
			missingFigure{Figure: "conns", Why: why})
	} else if data, err = h.relaySeries(ctx, starts[0], now, bucket); err != nil {
		log.Printf("admin entry protocols: dataset: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	out.Protocols, out.Nodes = protocolHistory(starts, bucket, now, boxes, grants, data)
	if n := unpricedBoxes(boxes); n > 0 {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "costCents", Why: fmt.Sprintf("машин без цены: %d, стоимость занижена на их аренду", n),
		})
	}
	writeJSONResponse(w, out)
}

func unpricedBoxes(boxes []historyBox) int {
	n := 0
	for _, b := range boxes {
		if b.monthlyCents == nil {
			n++
		}
	}
	return n
}

// historyBoxes reads every machine in service at some moment since `since`.
func (h *Handlers) historyBoxes(ctx context.Context, since time.Time) ([]historyBox, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT name, proto, domain_ct, created_at, retired_at, monthly_cents
		FROM entry_boxes
		WHERE retired_at IS NULL OR retired_at >= $1
		ORDER BY name`, since)
	if err != nil {
		return nil, fmt.Errorf("boxes: %w", err)
	}
	defer rows.Close()
	boxes := []historyBox{}
	index := map[string]int{}
	for rows.Next() {
		var b historyBox
		var domainCT []byte
		if err := rows.Scan(&b.name, &b.proto, &domainCT, &b.createdAt, &b.retiredAt, &b.monthlyCents); err != nil {
			return nil, fmt.Errorf("scan box: %w", err)
		}
		if domainCT != nil {
			// Unreadable, the row stays: the domain is only a label here.
			if d, err := cryptobox.Decrypt(domainCT); err == nil {
				b.domain = string(d)
			}
		}
		index[b.name] = len(boxes)
		boxes = append(boxes, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	relays, err := h.pool.Query(ctx, `
		SELECT name, box, created_at, died_at, signed_at IS NOT NULL
		FROM entry_relays
		WHERE died_at IS NULL OR died_at >= $1
		ORDER BY name`, since)
	if err != nil {
		return nil, fmt.Errorf("relays: %w", err)
	}
	defer relays.Close()
	for relays.Next() {
		var r historyRelay
		var box string
		if err := relays.Scan(&r.name, &box, &r.createdAt, &r.diedAt, &r.admitted); err != nil {
			return nil, fmt.Errorf("scan relay: %w", err)
		}
		if i, ok := index[box]; ok {
			boxes[i].relays = append(boxes[i].relays, r)
		}
	}
	return boxes, relays.Err()
}

// historyGrants reads who held what since `since`. A grant counts while its
// subscription does, so a test link revoked an hour ago is nobody's now.
func (h *Handlers) historyGrants(ctx context.Context, since time.Time) ([]historyGrant, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT g.account_id::text, r.name, r.box, b.proto,
		       greatest(g.granted_at, s.created_at),
		       nullif(least(coalesce(g.revoked_at, 'infinity'), coalesce(s.revoked_at, 'infinity'),
		                    coalesce(s.expires_at, 'infinity')), 'infinity')
		FROM entry_grants g
		JOIN entry_relays r ON r.id = g.relay_id
		JOIN entry_boxes b ON b.name = r.box
		JOIN hub_subscriptions s ON s.account_id = g.account_id
		WHERE least(coalesce(g.revoked_at, 'infinity'), coalesce(s.revoked_at, 'infinity'),
		            coalesce(s.expires_at, 'infinity')) >= $1`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []historyGrant{}
	for rows.Next() {
		var g historyGrant
		if err := rows.Scan(&g.account, &g.relay, &g.box, &g.proto, &g.from, &g.until); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// relaySeries reads the dataset's three figures per relay and bucket.
func (h *Handlers) relaySeries(ctx context.Context, since, now time.Time, bucket time.Duration) (relaySeries, error) {
	out := relaySeries{
		probes:  map[string]map[int64]probeCount{},
		traffic: map[string]map[int64]float64{},
		conns:   map[string]map[int64]float64{},
	}
	seconds := int64(bucket.Seconds())

	probes, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toString(verdict), count()
		FROM entry_probes WHERE ts >= ? AND ts <= ?
		GROUP BY relay_name, bucket, verdict`, seconds, since, now.Add(verdictSkew))
	if err != nil {
		return out, fmt.Errorf("probes: %w", err)
	}
	defer probes.Close()
	for probes.Next() {
		var relay, verdict string
		var at time.Time
		var n uint64
		if err := probes.Scan(&relay, &at, &verdict, &n); err != nil {
			return out, fmt.Errorf("scan probes: %w", err)
		}
		if out.probes[relay] == nil {
			out.probes[relay] = map[int64]probeCount{}
		}
		c := out.probes[relay][at.Unix()]
		switch verdict {
		case entry.VerdictClean, entry.VerdictRealityWins:
			c.ok += n
		case entry.VerdictIPBanned, entry.VerdictFingerprint, entry.CauseDomainBlocked:
			c.bad += n
		}
		out.probes[relay][at.Unix()] = c
	}
	if err := probes.Err(); err != nil {
		return out, err
	}

	traffic, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       sum(d_up) + sum(d_down)
		FROM (`+analytics.EntryRelayDeltas+`)
		GROUP BY relay_name, bucket`, seconds, since)
	if err != nil {
		return out, fmt.Errorf("traffic: %w", err)
	}
	defer traffic.Close()
	for traffic.Next() {
		var relay string
		var at time.Time
		var bytes uint64
		if err := traffic.Scan(&relay, &at, &bytes); err != nil {
			return out, fmt.Errorf("scan traffic: %w", err)
		}
		if out.traffic[relay] == nil {
			out.traffic[relay] = map[int64]float64{}
		}
		out.traffic[relay][at.Unix()] = float64(bytes)
	}
	if err := traffic.Err(); err != nil {
		return out, err
	}

	conns, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket, avg(conns)
		FROM entry_relay_stats WHERE ts >= ?
		GROUP BY relay_name, bucket`, seconds, since)
	if err != nil {
		return out, fmt.Errorf("conns: %w", err)
	}
	defer conns.Close()
	for conns.Next() {
		var relay string
		var at time.Time
		var avg float64
		if err := conns.Scan(&relay, &at, &avg); err != nil {
			return out, fmt.Errorf("scan conns: %w", err)
		}
		if out.conns[relay] == nil {
			out.conns[relay] = map[int64]float64{}
		}
		out.conns[relay][at.Unix()] = avg
	}
	return out, conns.Err()
}

// aliveAt is whether the moment falls in [from, until).
func aliveAt(t, from time.Time, until *time.Time) bool {
	return !from.After(t) && (until == nil || until.After(t))
}

// protocolHistory walks the buckets. Each life figure is read at the bucket's
// end, the current bucket's at now. A protocol is at zero when nothing lives,
// which is a fact about it; a machine that does not exist yet has no point, so
// its line starts when it does.
func protocolHistory(
	starts []time.Time, bucket time.Duration, now time.Time,
	boxes []historyBox, grants []historyGrant, data relaySeries,
) (protocols, nodes []protocolSeries) {
	n := len(starts)
	newSeries := func(key, proto string) *protocolSeries {
		return &protocolSeries{
			Key: key, Proto: proto,
			Live: make([]*float64, n), PassPct: make([]*float64, n), TrafficBytes: make([]*float64, n),
			Conns: make([]*float64, n), Subscribers: make([]*float64, n), CostCents: make([]*float64, n),
		}
	}
	byProto := map[string]*protocolSeries{}
	protoProbes := map[string][]probeCount{}
	seriesOf := func(proto string) *protocolSeries {
		if byProto[proto] == nil {
			byProto[proto] = newSeries(proto, proto)
			protoProbes[proto] = make([]probeCount, n)
			// A fresh protocol reads zero everywhere it has a meaning of zero.
			for i := 0; i < n; i++ {
				byProto[proto].Live[i] = num(0)
				byProto[proto].Subscribers[i] = num(0)
				byProto[proto].CostCents[i] = num(0)
			}
		}
		return byProto[proto]
	}

	for _, b := range boxes {
		proto := seriesOf(b.proto)
		node := newSeries(b.name, b.proto)
		node.Domain, node.Retired = b.domain, b.retiredAt != nil
		everAlive := false
		for i, start := range starts {
			at := start.Add(bucket)
			if at.After(now) {
				at = now
			}
			if !aliveAt(at, b.createdAt, b.retiredAt) && !aliveAt(start, b.createdAt, b.retiredAt) {
				// Only a bucket wholly outside its life is skipped: what a
				// machine carried in the hour it died still counts.
				continue
			}
			var probes probeCount
			var bytes, conns float64
			var hasBytes, hasConns bool
			live := 0
			for _, r := range b.relays {
				if r.admitted && aliveAt(at, r.createdAt, r.diedAt) && aliveAt(at, b.createdAt, b.retiredAt) {
					live++
				}
				key := start.Unix()
				if c, ok := data.probes[r.name][key]; ok {
					probes.ok += c.ok
					probes.bad += c.bad
				}
				if v, ok := data.traffic[r.name][key]; ok {
					bytes += v
					hasBytes = true
				}
				if v, ok := data.conns[r.name][key]; ok {
					conns += v
					hasConns = true
				}
			}
			node.Live[i] = num(float64(live))
			if live > 0 {
				everAlive = true
				*proto.Live[i]++
			}
			if aliveAt(at, b.createdAt, b.retiredAt) && b.monthlyCents != nil {
				node.CostCents[i] = num(float64(*b.monthlyCents))
				*proto.CostCents[i] += float64(*b.monthlyCents)
			}
			if probes.ok+probes.bad > 0 {
				node.PassPct[i] = passPct(probes)
				c := protoProbes[b.proto][i]
				c.ok += probes.ok
				c.bad += probes.bad
				protoProbes[b.proto][i] = c
			}
			if hasBytes {
				node.TrafficBytes[i] = num(bytes)
				proto.TrafficBytes[i] = addTo(proto.TrafficBytes[i], bytes)
			}
			if hasConns {
				node.Conns[i] = num(conns)
				proto.Conns[i] = addTo(proto.Conns[i], conns)
			}
		}
		if everAlive {
			nodes = append(nodes, *node)
		}
	}
	// A protocol's pass rate is over its probes, not an average of its machines'.
	for proto, counts := range protoProbes {
		for i, c := range counts {
			if c.ok+c.bad > 0 {
				byProto[proto].PassPct[i] = passPct(c)
			}
		}
	}

	// Subscribers are distinct at each level: one subscription on two machines
	// of a protocol is one subscriber of the protocol and one of each machine.
	for i, start := range starts {
		at := start.Add(bucket)
		if at.After(now) {
			at = now
		}
		onBox, onProto := map[string]map[string]bool{}, map[string]map[string]bool{}
		for _, g := range grants {
			if !aliveAt(at, g.from, g.until) {
				continue
			}
			if onBox[g.box] == nil {
				onBox[g.box] = map[string]bool{}
			}
			if onProto[g.proto] == nil {
				onProto[g.proto] = map[string]bool{}
			}
			onBox[g.box][g.account] = true
			onProto[g.proto][g.account] = true
		}
		for proto, accounts := range onProto {
			if s := byProto[proto]; s != nil {
				s.Subscribers[i] = num(float64(len(accounts)))
			}
		}
		for j := range nodes {
			if nodes[j].Live[i] != nil {
				nodes[j].Subscribers[i] = num(float64(len(onBox[nodes[j].Key])))
			}
		}
	}

	protocols = make([]protocolSeries, 0, len(byProto))
	for _, s := range byProto {
		protocols = append(protocols, *s)
	}
	sort.Slice(protocols, func(i, j int) bool { return protocols[i].Key < protocols[j].Key })
	if nodes == nil {
		nodes = []protocolSeries{}
	}
	return protocols, nodes
}

func num(v float64) *float64 { return &v }

func addTo(sum *float64, v float64) *float64 {
	if sum == nil {
		return num(v)
	}
	return num(*sum + v)
}

func passPct(c probeCount) *float64 {
	return num(round2(float64(c.ok) / float64(c.ok+c.bad) * 100))
}
