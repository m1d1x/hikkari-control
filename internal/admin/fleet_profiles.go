package admin

// fleet_profiles.go answers the fleet screen: every profile a subscriber is
// handed right now, one card each, with what it is (protocol, hoster, place,
// price, the plan's traffic and how much of it is spent, the domain it hides
// behind) and how it lived over the window (latency and throughput). A profile
// is a logical node (entry_nodes): the name a subscription puts on a link,
// kept across the relay behind it being replaced.
//
// The series are the current relay's. A node rebound to a fresh relay starts
// its lines again: the old relay's numbers were another machine's.
//
// The lines are history on the window's buckets (the owner's rule: the console
// draws data as history, never as a number of the moment). A bucket nobody
// measured is null and drawn as a gap.
//
//   - loss: per prober, the share of the call-sized datagrams it sent through
//     the relay that never came back (entry_probes xudp_sent/xudp_got: 100 a
//     round, to the relay's loopback echo). A tunnel that opened and carried
//     nothing back loses them all. Only relays that carry calls have lines.
//   - failPct: the share of the probers' rounds in which the relay did not
//     carry traffic: banned, its tunnel opened and stayed silent, or not one
//     datagram came back. A round that could not judge (network_down,
//     unknown: the prober's own network) is not in it. This replaces the
//     pass rate of verdicts, which only told a ban from no ban and read 100%
//     on a relay whose tunnel was silent (owner, 14.11).
//   - retransPct: of the TCP segments the relay's box sent, the share it had
//     to send again (the relay's pulse, /proc/net/snmp of the box). On a TCP
//     protocol that is the loss users feel, as stalls rather than gaps. It is
//     the box's: probes and the hub's own traffic are in it too, and a bucket
//     with too few segments to tell is a gap.
//   - rttMs: the median time of the RU probers' check through the relay
//     (entry_probes.reality_rtt_ms), a check that did not complete left out.
//   - speedMbps: the download speed the RU probers measured through the
//     profile (the stand's capacity runs, relay_capacity_history), the
//     median of the points in a bucket. This is the profile's speed; it is
//     what subscribers are handed with their links (capacity.Store.Metrics).
//     speedNormMbps is the node's own usual speed, the median of the same
//     measurements over the week before now, drawn beside the line: a node
//     is slow against itself, not against a figure picked for all.
//   - carriedMbps: what the relay passed for subscribers, the busier
//     direction, counted on the relay itself and sent in its pulses
//     (analytics.EntryRelayDeltas). Load, not speed: on an idle relay it is
//     a fraction of a megabit. Nothing here comes from a device.
//
// The traffic line of the card is the box's spend against its plan: bytes in
// and out of all its relays since the start of the calendar month (UTC), or
// since the box was born if later, against the plan's allowance from the
// hoster's catalog. Hosters count their own month from the rental date, so
// this is the month's shape, not their invoice.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/knobcheck"
)

type fleetProfiles struct {
	Window        string         `json:"window"`
	BucketSeconds int            `json:"bucketSeconds"`
	Buckets       []string       `json:"buckets"`
	Profiles      []fleetProfile `json:"profiles"`
	// Cascades are the trunk entries with their two legs (fleet_cascades.go).
	Cascades []fleetCascade  `json:"cascades"`
	Missing  []missingFigure `json:"missing"`
}

// fleetProfile is one card of the fleet screen.
type fleetProfile struct {
	// Label is the name the subscriber reads; the slug when none is set yet.
	Label string `json:"label"`
	Slug  string `json:"slug"`
	Relay string `json:"relay"`
	Box   string `json:"box"`
	Proto string `json:"proto"`
	// Provider is the hoster's API kind (vdsina, openstack, …); the console
	// turns it into the service's name.
	Provider       string `json:"provider"`
	Location       string `json:"location"`
	ProviderRegion string `json:"providerRegion,omitempty"`
	Plan           string `json:"plan,omitempty"`
	MonthlyCents   *int   `json:"monthlyCents,omitempty"`
	// Cover is the site the profile passes for: the domain an XHTTP box
	// serves, otherwise the cover the probers last reached the relay under.
	Cover string `json:"cover,omitempty"`
	// Fingerprint is the browser build whose ClientHello the client sends on
	// this profile (chrome_133, firefox_148): the node's own fingerprint, or
	// the link's default, read as the release's core reads it. Empty for a
	// protocol with no TLS hello of its own (Hysteria2, AmneziaWG).
	Fingerprint string `json:"fingerprint,omitempty"`
	// BoundAt is when the profile started being handed out on this relay.
	BoundAt time.Time `json:"boundAt"`
	// TrafficUsedGB is the box's bytes this month; TrafficQuotaGB its plan's
	// allowance. Either is absent when it could not be read.
	TrafficUsedGB  *float64  `json:"trafficUsedGb,omitempty"`
	TrafficQuotaGB *int      `json:"trafficQuotaGb,omitempty"`
	TrafficSince   time.Time `json:"trafficSince"`

	Loss          []fleetLossLine `json:"loss"`
	FailPct       []*float64      `json:"failPct"`
	RetransPct    []*float64      `json:"retransPct"`
	RTTMs         []*float64      `json:"rttMs"`
	SpeedMbps     []*float64      `json:"speedMbps"`
	SpeedNormMbps *float64        `json:"speedNormMbps,omitempty"`
	CarriedMbps   []*float64      `json:"carriedMbps"`

	providerRegion string
	boxSince       time.Time
}

// fleetLossLine is one prober's datagram loss through a relay, per bucket.
type fleetLossLine struct {
	Source string     `json:"source"`
	Pct    []*float64 `json:"pct"`
}

// datagramCount is what one prober sent through a relay in a bucket and what
// came back.
type datagramCount struct{ sent, got uint64 }

// roundCount is a relay's judged rounds in a bucket and those in which it
// carried no traffic.
type roundCount struct{ judged, failed uint64 }

// segmentCount is a box's TCP segments sent in a bucket and those sent again.
type segmentCount struct{ out, retrans uint64 }

// minRetransSegments is how many segments a bucket needs before its share of
// retransmits is drawn: below it, one lost probe reads as a percent.
const minRetransSegments = 2000

// relayFigures is what the dataset says of each relay, keyed by bucket start,
// and of each box over the month.
type relayFigures struct {
	// loss is keyed by relay, then prober.
	loss    map[string]map[string]map[int64]datagramCount
	rounds  map[string]map[int64]roundCount
	segs    map[string]map[int64]segmentCount
	rtt     map[string]map[int64]float64
	carried map[string]map[int64]float64
	// speed and speedNorm are keyed by node slug, not relay: a measurement
	// names the node.
	speed     map[string]map[int64]float64
	speedNorm map[string]float64
	covers    map[string]string
	boxGB     map[string]float64
}

// PlanCatalog reads a hoster's offer in one region: where a plan's traffic
// allowance comes from. entry.Service.ProviderCatalogIn in the hub.
type PlanCatalog func(ctx context.Context, kind, region string) (entry.ProviderCatalog, error)

// SetPlanCatalog wires the hoster catalogs into the fleet screen. Without it
// the cards carry no allowance.
func (h *Handlers) SetPlanCatalog(c PlanCatalog) {
	h.quotas = &planQuotas{read: c, cache: map[string]quotaEntry{}}
}

// planQuotas caches each hoster region's allowances: the catalog is a call to
// the hoster, and the fleet screen refreshes every minute.
type planQuotas struct {
	read  PlanCatalog
	mu    sync.Mutex
	cache map[string]quotaEntry
}

type quotaEntry struct {
	plans map[string]int
	until time.Time
}

const (
	quotaFresh   = time.Hour
	quotaRetry   = 5 * time.Minute
	quotaTimeout = 5 * time.Second
)

// bandwidthGB is the plan's monthly allowance, false when the hoster does not
// name one or could not be asked.
func (q *planQuotas) bandwidthGB(ctx context.Context, kind, region, plan string, now time.Time) (int, bool) {
	if q == nil || plan == "" {
		return 0, false
	}
	key := kind + "|" + region
	q.mu.Lock()
	e, ok := q.cache[key]
	q.mu.Unlock()
	if !ok || now.After(e.until) {
		e = quotaEntry{plans: map[string]int{}, until: now.Add(quotaFresh)}
		cctx, cancel := context.WithTimeout(ctx, quotaTimeout)
		cat, err := q.read(cctx, kind, region)
		cancel()
		if err != nil {
			// A hoster that does not answer is asked again soon, not on every
			// refresh of every open screen.
			log.Printf("admin fleet profiles: %s catalog: %v", kind, err)
			e.until = now.Add(quotaRetry)
		}
		for _, p := range cat.Plans {
			if p.BandwidthGB > 0 {
				e.plans[p.ID] = p.BandwidthGB
			}
		}
		q.mu.Lock()
		q.cache[key] = e
		q.mu.Unlock()
	}
	gb, ok := e.plans[plan]
	return gb, ok
}

// FleetProfiles answers GET /api/v1/admin/fleet/profiles.
func (h *Handlers) FleetProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("window")
	if q == "" {
		q = "24h"
	}
	window, label, ok := overviewRange(q)
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	bucket := seriesBucket(window)
	starts := bucketStarts(now.Add(-window), now, bucket)
	month := monthStart(now)

	profiles, err := h.servedProfiles(ctx)
	if err != nil {
		log.Printf("admin fleet profiles: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	out := fleetProfiles{
		Window: label, BucketSeconds: int(bucket.Seconds()),
		Buckets: make([]string, len(starts)), Missing: []missingFigure{},
	}
	for i, at := range starts {
		out.Buckets[i] = at.Format(time.RFC3339)
	}
	figures := relayFigures{}
	if h.analytics == nil {
		why := "analytics is not configured on this hub"
		out.Missing = append(out.Missing,
			missingFigure{Figure: "loss", Why: why},
			missingFigure{Figure: "failPct", Why: why},
			missingFigure{Figure: "retransPct", Why: why},
			missingFigure{Figure: "rttMs", Why: why},
			missingFigure{Figure: "carriedMbps", Why: why},
			missingFigure{Figure: "trafficUsedGb", Why: why})
	} else if figures, err = h.relayFigures(ctx, starts[0], now, bucket, month); err != nil {
		log.Printf("admin fleet profiles: dataset: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if figures.speed, err = h.probedSpeed(ctx, starts[0], bucket); err != nil {
		log.Printf("admin fleet profiles: speed: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if figures.speedNorm, err = h.probedSpeedNorm(ctx, now.Add(-speedNormWindow)); err != nil {
		log.Printf("admin fleet profiles: speed norm: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if h.quotas == nil {
		out.Missing = append(out.Missing, missingFigure{Figure: "trafficQuotaGb", Why: "hoster catalogs are not wired on this hub"})
	}
	for i := range profiles {
		p := &profiles[i]
		if gb, ok := h.quotas.bandwidthGB(ctx, p.Provider, p.providerRegion, p.Plan, now); ok {
			p.TrafficQuotaGB = &gb
		}
	}
	out.Profiles = foldProfiles(profiles, starts, month, figures)
	if out.Cascades, err = h.cascades(ctx, starts, now, bucket); err != nil {
		// The profiles stand without them: the cascade's legs are a card more.
		log.Printf("admin fleet profiles: %v", err)
		out.Cascades = []fleetCascade{}
		out.Missing = append(out.Missing, missingFigure{Figure: "cascades", Why: "the trunk entries' legs could not be read"})
	}
	writeJSONResponse(w, out)
}

// monthStart is the first moment of the calendar month, UTC.
func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// servedProfiles reads every node whose relay is serving now, with its box.
func (h *Handlers) servedProfiles(ctx context.Context) ([]fleetProfile, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT coalesce(n.label, n.slug), n.slug, r.name, b.name, b.proto,
		       b.provider, b.location, b.provider_region, b.plan, b.monthly_cents, b.domain_ct,
		       coalesce(n.fingerprint, ''), n.bound_at, b.created_at
		FROM entry_nodes n
		JOIN entry_relays r ON r.id = n.relay_id
		JOIN entry_boxes b ON b.name = r.box
		WHERE r.status = 'alive' AND b.retired_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("profiles: %w", err)
	}
	defer rows.Close()
	out := []fleetProfile{}
	for rows.Next() {
		var p fleetProfile
		var domainCT []byte
		if err := rows.Scan(&p.Label, &p.Slug, &p.Relay, &p.Box, &p.Proto,
			&p.Provider, &p.Location, &p.providerRegion, &p.Plan, &p.MonthlyCents, &domainCT,
			&p.Fingerprint, &p.BoundAt, &p.boxSince); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		if domainCT != nil {
			// Unreadable, the card stays: the domain is only a label here.
			if d, err := cryptobox.Decrypt(domainCT); err == nil {
				p.Cover = string(d)
			}
		}
		p.Fingerprint = helloBuildOf(p.Proto, p.Fingerprint)
		out = append(out, p)
	}
	return out, rows.Err()
}

// helloBuildOf is the browser build a profile's client imitates. A VLESS link
// asks for "chrome" unless its node names another fingerprint
// (subscription/links.go, namedLink); other protocols ask for none.
func helloBuildOf(proto, nodeFP string) string {
	if proto != entry.ProtoVlessRealityVision && proto != entry.ProtoVlessXhttp {
		return ""
	}
	if nodeFP == "" {
		nodeFP = "chrome"
	}
	return knobcheck.HelloBuild(nodeFP)
}

// relayFigures reads the dataset: the two lines per relay and bucket, the
// cover each relay was last probed under, and each box's bytes this month.
func (h *Handlers) relayFigures(ctx context.Context, since, now time.Time, bucket time.Duration, month time.Time) (relayFigures, error) {
	out := relayFigures{
		loss:    map[string]map[string]map[int64]datagramCount{},
		rounds:  map[string]map[int64]roundCount{},
		segs:    map[string]map[int64]segmentCount{},
		rtt:     map[string]map[int64]float64{},
		carried: map[string]map[int64]float64{},
		covers:  map[string]string{},
		boxGB:   map[string]float64{},
	}
	seconds := int64(bucket.Seconds())
	put := func(m map[string]map[int64]float64, relay string, at time.Time, v float64) {
		if m[relay] == nil {
			m[relay] = map[int64]float64{}
		}
		m[relay][at.Unix()] = v
	}

	// A round judged the relay unless the prober's own network was down or it
	// could not tell. Judged, the relay failed it when it was banned, when
	// its tunnel opened and stayed silent (k3), or when not one of the
	// round's datagrams came back.
	rounds, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toUInt64(count()),
		       toUInt64(countIf(
		           verdict IN ('ip_banned', 'fingerprint', 'domain_blocked')
		           OR position(detail, 'upstream silent') > 0
		           OR (xudp_sent > 0 AND xudp_got = 0)
		           OR (dup_sent > 0 AND dup_got = 0)))
		FROM entry_probes
		WHERE ts >= ? AND ts <= ? AND verdict NOT IN ('network_down', 'unknown')
		GROUP BY relay_name, bucket`, seconds, since, now.Add(verdictSkew))
	if err != nil {
		return out, fmt.Errorf("rounds: %w", err)
	}
	defer rounds.Close()
	for rounds.Next() {
		var relay string
		var at time.Time
		var c roundCount
		if err := rounds.Scan(&relay, &at, &c.judged, &c.failed); err != nil {
			return out, fmt.Errorf("scan rounds: %w", err)
		}
		if out.rounds[relay] == nil {
			out.rounds[relay] = map[int64]roundCount{}
		}
		out.rounds[relay][at.Unix()] = c
	}
	if err := rounds.Err(); err != nil {
		return out, err
	}

	loss, err := h.analytics.Query(ctx, `
		SELECT relay_name, toString(source_region), toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toUInt64(sum(xudp_sent)), toUInt64(sum(xudp_got))
		FROM entry_probes
		WHERE ts >= ? AND ts <= ? AND xudp_sent > 0
		GROUP BY relay_name, source_region, bucket`, seconds, since, now.Add(verdictSkew))
	if err != nil {
		return out, fmt.Errorf("loss: %w", err)
	}
	defer loss.Close()
	for loss.Next() {
		var relay, source string
		var at time.Time
		var c datagramCount
		if err := loss.Scan(&relay, &source, &at, &c.sent, &c.got); err != nil {
			return out, fmt.Errorf("scan loss: %w", err)
		}
		if out.loss[relay] == nil {
			out.loss[relay] = map[string]map[int64]datagramCount{}
		}
		if out.loss[relay][source] == nil {
			out.loss[relay][source] = map[int64]datagramCount{}
		}
		out.loss[relay][source][at.Unix()] = c
	}
	if err := loss.Err(); err != nil {
		return out, err
	}

	// Pulses carry the box's totals since boot; a total that went down is a
	// reboot, and that pair is no delta. The pulse before the window gives
	// the window's first delta its base.
	segs, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toUInt64(sum(d_out)), toUInt64(sum(d_re))
		FROM (
			SELECT relay_name, ts,
			       if(p_out > 0 AND tcp_out_segs >= p_out AND tcp_retrans_segs >= p_re, tcp_out_segs - p_out, 0) AS d_out,
			       if(p_out > 0 AND tcp_out_segs >= p_out AND tcp_retrans_segs >= p_re, tcp_retrans_segs - p_re, 0) AS d_re
			FROM (
				SELECT relay_name, ts, tcp_out_segs, tcp_retrans_segs,
				       lagInFrame(tcp_out_segs) OVER w AS p_out,
				       lagInFrame(tcp_retrans_segs) OVER w AS p_re
				FROM entry_relay_stats
				WHERE ts >= ? AND ts <= ? AND tcp_out_segs > 0
				WINDOW w AS (PARTITION BY relay_name ORDER BY ts ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)
			)
		)
		WHERE ts >= ?
		GROUP BY relay_name, bucket`, seconds, since.Add(-10*time.Minute), now, since)
	if err != nil {
		return out, fmt.Errorf("segments: %w", err)
	}
	defer segs.Close()
	for segs.Next() {
		var relay string
		var at time.Time
		var c segmentCount
		if err := segs.Scan(&relay, &at, &c.out, &c.retrans); err != nil {
			return out, fmt.Errorf("scan segments: %w", err)
		}
		if out.segs[relay] == nil {
			out.segs[relay] = map[int64]segmentCount{}
		}
		out.segs[relay][at.Unix()] = c
	}
	if err := segs.Err(); err != nil {
		return out, err
	}

	rtt, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toFloat64(quantile(0.5)(reality_rtt_ms))
		FROM entry_probes
		WHERE ts >= ? AND ts <= ? AND reality_rtt_ms > 0
		GROUP BY relay_name, bucket`, seconds, since, now.Add(verdictSkew))
	if err != nil {
		return out, fmt.Errorf("rtt: %w", err)
	}
	defer rtt.Close()
	for rtt.Next() {
		var relay string
		var at time.Time
		var ms float64
		if err := rtt.Scan(&relay, &at, &ms); err != nil {
			return out, fmt.Errorf("scan rtt: %w", err)
		}
		put(out.rtt, relay, at, ms)
	}
	if err := rtt.Err(); err != nil {
		return out, err
	}

	// Only measured deltas carry a rate (see EntryRelayDeltas): the bytes of
	// a delta over a long silence are an average over minutes nobody watched.
	carried, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toUInt64(sumIf(d_up, measured)), toUInt64(sumIf(d_down, measured)), toUInt64(sumIf(dt_s, measured))
		FROM (`+analytics.EntryRelayDeltas+`)
		GROUP BY relay_name, bucket`, seconds, since)
	if err != nil {
		return out, fmt.Errorf("carried: %w", err)
	}
	defer carried.Close()
	for carried.Next() {
		var relay string
		var at time.Time
		var up, down, secs uint64
		if err := carried.Scan(&relay, &at, &up, &down, &secs); err != nil {
			return out, fmt.Errorf("scan carried: %w", err)
		}
		if mbps, ok := carriedMbps(up, down, secs); ok {
			put(out.carried, relay, at, mbps)
		}
	}
	if err := carried.Err(); err != nil {
		return out, err
	}

	covers, err := h.analytics.Query(ctx, `
		SELECT relay_name, argMax(cover, ts)
		FROM entry_probes
		WHERE ts >= ? AND cover != ''
		GROUP BY relay_name`, now.Add(-24*time.Hour))
	if err != nil {
		return out, fmt.Errorf("covers: %w", err)
	}
	defer covers.Close()
	for covers.Next() {
		var relay, cover string
		if err := covers.Scan(&relay, &cover); err != nil {
			return out, fmt.Errorf("scan covers: %w", err)
		}
		out.covers[relay] = cover
	}
	if err := covers.Err(); err != nil {
		return out, err
	}

	// Every delta counts toward the month, measured or not: the bytes were
	// carried even when nobody watched the minutes they took. A relay is
	// named <box>-<n>, so its box is the part before the dash, and relays
	// already gone from a box still count toward its month.
	spent, err := h.analytics.Query(ctx, `
		SELECT splitByChar('-', relay_name)[1] AS box, toUInt64(sum(d_up) + sum(d_down))
		FROM (`+analytics.EntryRelayDeltas+`)
		GROUP BY box`, month)
	if err != nil {
		return out, fmt.Errorf("month traffic: %w", err)
	}
	defer spent.Close()
	for spent.Next() {
		var box string
		var bytes uint64
		if err := spent.Scan(&box, &bytes); err != nil {
			return out, fmt.Errorf("scan month traffic: %w", err)
		}
		out.boxGB[box] = float64(bytes) / 1e9
	}
	return out, spent.Err()
}

// probedSpeed is each node's measured download speed per bucket: the median
// of the successful samples the RU observation points sent in it. A sample
// joins its node through relay_capacity_nodes; one whose key no capacity run
// was handed with a node name is left out.
func (h *Handlers) probedSpeed(ctx context.Context, since time.Time, bucket time.Duration) (map[string]map[int64]float64, error) {
	out := map[string]map[int64]float64{}
	rows, err := h.pool.Query(ctx, `
		SELECT n.node_slug,
		       floor(extract(epoch FROM c.measured_at) / $2)::bigint * $2 AS bucket,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY (c.sample->>'mbps')::float8)
		FROM relay_capacity_history c
		JOIN relay_capacity_nodes n ON n.config_key = c.config_key
		JOIN entry_vantages v ON v.slot = c.slot
		WHERE v.in_russia AND c.measured_at >= $1
		  AND coalesce(c.sample->>'error', '') = '' AND (c.sample->>'mbps')::float8 > 0
		GROUP BY 1, 2`, since, int64(bucket.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var at int64
		var mbps float64
		if err := rows.Scan(&slug, &at, &mbps); err != nil {
			return nil, err
		}
		if out[slug] == nil {
			out[slug] = map[int64]float64{}
		}
		out[slug][at] = mbps
	}
	return out, rows.Err()
}

// speedNormWindow is how far back a node's usual speed is taken from.
const speedNormWindow = 7 * 24 * time.Hour

// probedSpeedNorm is each node's usual speed: the median of its successful
// RU measurements since since.
func (h *Handlers) probedSpeedNorm(ctx context.Context, since time.Time) (map[string]float64, error) {
	out := map[string]float64{}
	rows, err := h.pool.Query(ctx, `
		SELECT n.node_slug,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY (c.sample->>'mbps')::float8)
		FROM relay_capacity_history c
		JOIN relay_capacity_nodes n ON n.config_key = c.config_key
		JOIN entry_vantages v ON v.slot = c.slot
		WHERE v.in_russia AND c.measured_at >= $1
		  AND coalesce(c.sample->>'error', '') = '' AND (c.sample->>'mbps')::float8 > 0
		GROUP BY 1`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var mbps float64
		if err := rows.Scan(&slug, &mbps); err != nil {
			return nil, err
		}
		out[slug] = mbps
	}
	return out, rows.Err()
}

// lossPct is the share of a bucket's datagrams that did not come back.
func lossPct(c datagramCount) *float64 {
	if c.sent == 0 {
		return nil
	}
	got := min(c.got, c.sent)
	return num(float64(c.sent-got) * 100 / float64(c.sent))
}

// failPct is the share of a bucket's judged rounds the relay failed.
func failPct(c roundCount) *float64 {
	if c.judged == 0 {
		return nil
	}
	return num(float64(min(c.failed, c.judged)) * 100 / float64(c.judged))
}

// retransPct is the share of a bucket's TCP segments sent again; a gap below
// minRetransSegments.
func retransPct(c segmentCount) *float64 {
	if c.out < minRetransSegments {
		return nil
	}
	return num(float64(min(c.retrans, c.out)) * 100 / float64(c.out))
}

// carriedMbps is a relay's rate in a bucket: the busier direction's bytes over
// the seconds they took. The link is full duplex, so the two directions are
// not added. No measured second is no rate, not zero.
func carriedMbps(up, down, secs uint64) (float64, bool) {
	if secs == 0 {
		return 0, false
	}
	busier := up
	if down > busier {
		busier = down
	}
	return float64(busier) * 8 / float64(secs) / 1e6, true
}

// foldProfiles lays each relay's figures on the window's buckets, fills the
// card's month and cover, and orders the cards by protocol, then by name, so
// a protocol's profiles stand together.
func foldProfiles(profiles []fleetProfile, starts []time.Time, month time.Time, f relayFigures) []fleetProfile {
	line := func(m map[string]map[int64]float64, relay string) []*float64 {
		s := make([]*float64, len(starts))
		for i, at := range starts {
			if v, ok := m[relay][at.Unix()]; ok {
				s[i] = num(v)
			}
		}
		return s
	}
	for i := range profiles {
		p := &profiles[i]
		// A bucket nobody judged or measured is a gap, not 0%: the probers
		// were down, not the relay.
		p.FailPct = make([]*float64, len(starts))
		p.RetransPct = make([]*float64, len(starts))
		for j, at := range starts {
			p.FailPct[j] = failPct(f.rounds[p.Relay][at.Unix()])
			p.RetransPct[j] = retransPct(f.segs[p.Relay][at.Unix()])
		}
		p.Loss = []fleetLossLine{}
		sources := make([]string, 0, len(f.loss[p.Relay]))
		for source := range f.loss[p.Relay] {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		for _, source := range sources {
			l := fleetLossLine{Source: source, Pct: make([]*float64, len(starts))}
			for j, at := range starts {
				l.Pct[j] = lossPct(f.loss[p.Relay][source][at.Unix()])
			}
			p.Loss = append(p.Loss, l)
		}
		if v, ok := f.speedNorm[p.Slug]; ok {
			p.SpeedNormMbps = num(v)
		}
		p.RTTMs = line(f.rtt, p.Relay)
		p.SpeedMbps = line(f.speed, p.Slug)
		p.CarriedMbps = line(f.carried, p.Relay)
		if p.Cover == "" {
			p.Cover = f.covers[p.Relay]
		}
		p.TrafficSince = month
		if p.boxSince.After(month) {
			p.TrafficSince = p.boxSince
		}
		if f.boxGB != nil {
			gb := f.boxGB[p.Box]
			p.TrafficUsedGB = &gb
		}
	}
	sort.SliceStable(profiles, func(a, b int) bool {
		if profiles[a].Proto != profiles[b].Proto {
			return profiles[a].Proto < profiles[b].Proto
		}
		return profiles[a].Label < profiles[b].Label
	})
	return profiles
}
