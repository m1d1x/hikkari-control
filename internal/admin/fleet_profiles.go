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
//   - availPct: the share of the RU probers' verdicts on the relay that got
//     through, the protocol screen's pass rate (entry_protocol_stats.go):
//     a verdict that could not judge (network_down, unknown) is not in it.
//   - rttMs: the median time of the RU probers' check through the relay
//     (entry_probes.reality_rtt_ms), a check that did not complete left out.
//   - carriedMbps: what the relay passed for subscribers, the busier
//     direction, counted on the relay itself and sent in its pulses
//     (analytics.EntryRelayDeltas). Nothing here comes from a device.
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
	Window        string          `json:"window"`
	BucketSeconds int             `json:"bucketSeconds"`
	Buckets       []string        `json:"buckets"`
	Profiles      []fleetProfile  `json:"profiles"`
	Missing       []missingFigure `json:"missing"`
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

	AvailPct    []*float64 `json:"availPct"`
	RTTMs       []*float64 `json:"rttMs"`
	CarriedMbps []*float64 `json:"carriedMbps"`

	providerRegion string
	boxSince       time.Time
}

// relayFigures is what the dataset says of each relay, keyed by bucket start,
// and of each box over the month.
type relayFigures struct {
	probes  map[string]map[int64]probeCount
	rtt     map[string]map[int64]float64
	carried map[string]map[int64]float64
	covers  map[string]string
	boxGB   map[string]float64
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
			missingFigure{Figure: "availPct", Why: why},
			missingFigure{Figure: "rttMs", Why: why},
			missingFigure{Figure: "carriedMbps", Why: why},
			missingFigure{Figure: "trafficUsedGb", Why: why})
	} else if figures, err = h.relayFigures(ctx, starts[0], now, bucket, month); err != nil {
		log.Printf("admin fleet profiles: dataset: %v", err)
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
		probes:  map[string]map[int64]probeCount{},
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

	verdicts, err := h.analytics.Query(ctx, `
		SELECT relay_name, toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
		       toString(verdict), count()
		FROM entry_probes WHERE ts >= ? AND ts <= ?
		GROUP BY relay_name, bucket, verdict`, seconds, since, now.Add(verdictSkew))
	if err != nil {
		return out, fmt.Errorf("verdicts: %w", err)
	}
	defer verdicts.Close()
	for verdicts.Next() {
		var relay, verdict string
		var at time.Time
		var n uint64
		if err := verdicts.Scan(&relay, &at, &verdict, &n); err != nil {
			return out, fmt.Errorf("scan verdicts: %w", err)
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
	if err := verdicts.Err(); err != nil {
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
		p.AvailPct = make([]*float64, len(starts))
		for j, at := range starts {
			// A bucket where no verdict judged is a gap, not 0%: the probers
			// were down, not the relay.
			if c := f.probes[p.Relay][at.Unix()]; c.ok+c.bad > 0 {
				p.AvailPct[j] = passPct(c)
			}
		}
		p.RTTMs = line(f.rtt, p.Relay)
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
