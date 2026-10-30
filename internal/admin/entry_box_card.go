package admin

// entry_box_card.go answers the machine card on the fleet screen (plan 42
// П9.3, П9.9 §6): one request for one machine — its life from birth to death
// and what it cost, and what its relays say about themselves in the pulse.
//
// The list of the fleet (entry_fleet.go) never shows an address. The card is
// where the owner looks at one machine on purpose, and it is the one place the
// address is opened from its seal.
//
// Nothing is stored for this screen. The lifetime, the rent over the life,
// the rates and the people are each computed by the query that reads them.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// entryBoxCard is the whole card.
type entryBoxCard struct {
	Window        string `json:"window"`
	BucketSeconds int    `json:"bucketSeconds"`

	Box     boxCardInfo    `json:"box"`
	History boxHistory     `json:"history"`
	Relays  []boxCardRelay `json:"relays"`
	// Stats is the pulse of the box's relays over the window; nil when none
	// of them pulsed in it.
	Stats *boxStats `json:"stats"`
	// People is how many distinct accounts' sockets came through this box in
	// the window (entry_user_traffic); nil when none did.
	People *int64 `json:"people"`

	Missing []missingFigure `json:"missing"`
}

// boxCardInfo is the machine as the hub recorded it.
type boxCardInfo struct {
	Name string `json:"name"`
	// Address is opened from the seal for this card only; empty when the
	// server key cannot open it, which AddressUnreadable then says.
	Address           string `json:"address,omitempty"`
	AddressUnreadable bool   `json:"addressUnreadable,omitempty"`
	Provider          string `json:"provider"`
	Location          string `json:"location"`
	Region            string `json:"region,omitempty"`
	ProviderRegion    string `json:"providerRegion,omitempty"`
	Plan              string `json:"plan,omitempty"`
	Proto             string `json:"proto"`
	Arch              string `json:"arch,omitempty"`
	EdgeSHA           string `json:"edgeSha,omitempty"`
	Domain            string `json:"domain,omitempty"`
	MonthlyCents      *int   `json:"monthlyCents,omitempty"`
}

// boxHistory is the machine's life.
type boxHistory struct {
	BornAt time.Time `json:"bornAt"`
	// AdmittedAt is when its first relay was signed into the pool; nil for a
	// machine none of whose relays ever was.
	AdmittedAt    *time.Time `json:"admittedAt"`
	RetiredAt     *time.Time `json:"retiredAt"`
	RetiredReason string     `json:"retiredReason,omitempty"`
	// LifetimeSec runs from birth to retirement, or to now for a live one.
	LifetimeSec int64 `json:"lifetimeSec"`
	// RentCents is the rent over that life, at the machine's monthly price
	// over thirty days — the same day rate the provider comparison uses. nil
	// for a machine rented by hand, which carries no price.
	RentCents *int64 `json:"rentCents"`
	// RepairCents is what the repairs of its incidents spent.
	RepairCents int64         `json:"repairCents"`
	Incidents   []boxIncident `json:"incidents"`
}

type boxIncident struct {
	ID       string     `json:"id"`
	Cause    string     `json:"cause"`
	Status   string     `json:"status"`
	Playbook string     `json:"playbook,omitempty"`
	Outcome  string     `json:"outcome,omitempty"`
	Cost     int        `json:"costCents"`
	OpenedAt time.Time  `json:"openedAt"`
	ClosedAt *time.Time `json:"closedAt,omitempty"`
}

// boxCardRelay is one relay of the machine, with what its last pulse said.
type boxCardRelay struct {
	fleetRelay
	DiedReason string `json:"diedReason,omitempty"`
	// LastPulseAt, Version and UptimeS are the relay's latest pulse; absent
	// when it never pulsed in the window.
	LastPulseAt *time.Time `json:"lastPulseAt,omitempty"`
	Version     string     `json:"version,omitempty"`
	UptimeS     *int64     `json:"uptimeS,omitempty"`
}

// boxStats is the pulse, summed over the box's relays bucket by bucket.
type boxStats struct {
	Series []boxStatsPoint `json:"series"`
}

// boxStatsPoint is one bucket. A bucket no relay pulsed in has every field
// nil: the line breaks there rather than dropping to zero.
type boxStatsPoint struct {
	Bucket string `json:"bucket"`
	// MBUp is what clients sent through the box, MBDown what they received.
	MBUp   *float64 `json:"mbUp"`
	MBDown *float64 `json:"mbDown"`
	// ConnsPerSec is new connections accepted per second over the bucket.
	ConnsPerSec *float64 `json:"connsPerSec"`
	// CPUPct is the box's mean load and MemMB its peak memory in the bucket;
	// every relay of a box reports the same machine.
	CPUPct *float64 `json:"cpuPct"`
	MemMB  *float64 `json:"memMb"`
}

// boxPulseQ differences each relay's cumulative counters pulse by pulse, the
// way [analytics.EntryRelayDeltas] does for the overview, connections included, and sums the
// box's relays per bucket. Parameters: the bucket width in hours, the start of
// the span, the relay names.
const boxPulseQ = `
	SELECT
		toStartOfInterval(ts, toIntervalHour(?)) AS bucket,
		sum(d_up)                                AS up,
		sum(d_down)                              AS down,
		sum(d_conns)                             AS conns,
		toFloat64(avg(cpu_pct))                  AS cpu,
		toFloat64(max(mem_mb))                   AS mem
	FROM (
		SELECT
			ts, cpu_pct, mem_mb,
			if(seq = 1, 0, if(uptime_s >= prev_uptime AND bytes_up >= prev_up, toUInt64(bytes_up - prev_up), bytes_up))           AS d_up,
			if(seq = 1, 0, if(uptime_s >= prev_uptime AND bytes_down >= prev_down, toUInt64(bytes_down - prev_down), bytes_down)) AS d_down,
			if(seq = 1, 0, if(uptime_s >= prev_uptime AND conns >= prev_conns, toUInt64(conns - prev_conns), conns))             AS d_conns
		FROM (
			SELECT
				ts, relay_name, uptime_s, bytes_up, bytes_down, conns, cpu_pct, mem_mb,
				lagInFrame(uptime_s) OVER w   AS prev_uptime,
				lagInFrame(bytes_up) OVER w   AS prev_up,
				lagInFrame(bytes_down) OVER w AS prev_down,
				lagInFrame(conns) OVER w      AS prev_conns,
				row_number() OVER w           AS seq
			FROM entry_relay_stats
			WHERE ts >= ? AND relay_name IN ?
			WINDOW w AS (PARTITION BY relay_name ORDER BY ts ASC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
		)
	)
	GROUP BY bucket
`

// boxLastPulseQ is each relay's latest pulse in the span. Parameters: the
// start of the span, the relay names.
const boxLastPulseQ = `
	SELECT relay_name, max(ts), argMax(version, ts), toInt64(argMax(uptime_s, ts))
	FROM entry_relay_stats
	WHERE ts >= ? AND relay_name IN ?
	GROUP BY relay_name
`

// boxPeopleQ counts the accounts whose socket came through the box.
// Parameters: the start of the span, the box.
const boxPeopleQ = `
	SELECT uniqExact(account) FROM entry_user_traffic WHERE ts >= ? AND box = ?
`

// EntryBoxCard answers GET /api/v1/admin/entry/machines/{name}
func (h *Handlers) EntryBoxCard(w http.ResponseWriter, r *http.Request) {
	window, label, ok := overviewRange(r.URL.Query().Get("window"))
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()
	bucket := seriesBucket(window)
	out := entryBoxCard{
		Window:        label,
		BucketSeconds: int(bucket.Seconds()),
		Relays:        []boxCardRelay{},
		Missing:       []missingFigure{},
	}

	if err := h.fillBoxInfo(ctx, &out, name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "no such box", http.StatusNotFound)
			return
		}
		log.Printf("admin entry box card: box: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillBoxIncidents(ctx, &out, name); err != nil {
		log.Printf("admin entry box card: incidents: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillBoxRelays(ctx, &out, name); err != nil {
		log.Printf("admin entry box card: relays: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if h.analytics == nil {
		out.Missing = append(out.Missing, missingFigure{Figure: "stats", Why: "analytics is not configured on this hub"})
	} else if err := h.fillBoxStats(ctx, &out, name, window, bucket); err != nil {
		log.Printf("admin entry box card: stats: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	// The pulse carries counters of the box, not of its sessions: the edge
	// keeps no session list (plan 42 §0.1), and a refused handshake never
	// reaches the counters at all.
	out.Missing = append(out.Missing,
		missingFigure{
			Figure: "stats.activeSessions",
			Why:    "the relay's pulse carries accepted connections, not the ones open now",
		},
		missingFigure{
			Figure: "stats.handshakeFailures",
			Why:    "the relay's pulse does not count refused handshakes",
		},
	)
	writeJSONResponse(w, out)
}

func (h *Handlers) fillBoxInfo(ctx context.Context, out *entryBoxCard, name string) error {
	var addrCT, domainCT []byte
	b := &out.Box
	hist := &out.History
	err := h.pool.QueryRow(ctx, `
		SELECT
			b.name, b.address_ct, b.provider, b.location, coalesce(b.region, ''),
			coalesce(b.provider_region, ''), coalesce(b.plan, ''), b.proto,
			coalesce(b.arch, ''), coalesce(b.edge_sha, ''), b.domain_ct, b.monthly_cents,
			b.created_at, b.retired_at, coalesce(b.retired_reason, ''),
			(SELECT min(signed_at) FROM entry_relays WHERE box = b.name),
			EXTRACT(EPOCH FROM (coalesce(b.retired_at, now()) - b.created_at))::bigint,
			(b.monthly_cents / 30.0
				* EXTRACT(EPOCH FROM (coalesce(b.retired_at, now()) - b.created_at)) / 86400.0)::bigint,
			(SELECT coalesce(sum(cost_cents), 0) FROM entry_incidents WHERE box = b.name)::bigint
		FROM entry_boxes b
		WHERE b.name = $1
	`, name).Scan(
		&b.Name, &addrCT, &b.Provider, &b.Location, &b.Region,
		&b.ProviderRegion, &b.Plan, &b.Proto,
		&b.Arch, &b.EdgeSHA, &domainCT, &b.MonthlyCents,
		&hist.BornAt, &hist.RetiredAt, &hist.RetiredReason,
		&hist.AdmittedAt, &hist.LifetimeSec, &hist.RentCents, &hist.RepairCents,
	)
	if err != nil {
		return err
	}
	if plain, err := cryptobox.Decrypt(addrCT); err == nil {
		b.Address = string(plain)
	} else {
		b.AddressUnreadable = true
	}
	if len(domainCT) > 0 {
		if plain, err := cryptobox.Decrypt(domainCT); err == nil {
			b.Domain = string(plain)
		}
	}
	if hist.RentCents == nil {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "history.rentCents",
			Why:    "the machine was rented by hand and carries no price",
		})
	}
	return nil
}

func (h *Handlers) fillBoxIncidents(ctx context.Context, out *entryBoxCard, name string) error {
	rows, err := h.pool.Query(ctx, `
		SELECT id::text, cause, status, playbook, outcome, cost_cents, opened_at, closed_at
		FROM entry_incidents
		WHERE box = $1
		ORDER BY opened_at DESC
	`, name)
	if err != nil {
		return err
	}
	defer rows.Close()
	out.History.Incidents = []boxIncident{}
	for rows.Next() {
		var i boxIncident
		if err := rows.Scan(&i.ID, &i.Cause, &i.Status, &i.Playbook, &i.Outcome, &i.Cost, &i.OpenedAt, &i.ClosedAt); err != nil {
			return err
		}
		out.History.Incidents = append(out.History.Incidents, i)
	}
	return rows.Err()
}

func (h *Handlers) fillBoxRelays(ctx context.Context, out *entryBoxCard, name string) error {
	rows, err := h.pool.Query(ctx, `
		SELECT r.name, r.kind, r.status, r.signed_at, r.died_at, coalesce(r.died_reason, ''), r.identity_ct,
		       (SELECT count(*) FROM entry_grants g WHERE g.relay_id = r.id AND g.revoked_at IS NULL)
		FROM entry_relays r
		WHERE r.box = $1
		ORDER BY r.name
	`, name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r boxCardRelay
		var identityCT []byte
		if err := rows.Scan(&r.Name, &r.Kind, &r.Status, &r.SignedAt, &r.DiedAt, &r.DiedReason, &identityCT, &r.Grants); err != nil {
			return err
		}
		openRelayFace(&r.fleetRelay, identityCT)
		out.Relays = append(out.Relays, r)
	}
	return rows.Err()
}

func (h *Handlers) fillBoxStats(ctx context.Context, out *entryBoxCard, name string, window, bucket time.Duration) error {
	now := time.Now().UTC()
	starts := bucketStarts(now.Add(-window), now, bucket)
	names := make([]string, 0, len(out.Relays))
	for _, r := range out.Relays {
		names = append(names, r.Name)
	}

	if len(names) > 0 {
		if err := h.fillBoxLastPulse(ctx, out, starts[0], names); err != nil {
			return err
		}
		rows, err := h.analytics.Query(ctx, boxPulseQ, int64(bucket/time.Hour), starts[0], names)
		if err != nil {
			return fmt.Errorf("box pulse: %w", err)
		}
		type pulse struct {
			up, down, conns uint64
			cpu, mem        float64
		}
		by := map[time.Time]pulse{}
		for rows.Next() {
			var at time.Time
			var p pulse
			if err := rows.Scan(&at, &p.up, &p.down, &p.conns, &p.cpu, &p.mem); err != nil {
				rows.Close()
				return fmt.Errorf("scan box pulse: %w", err)
			}
			by[at.UTC()] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(by) > 0 {
			const mb = 1024 * 1024
			stats := &boxStats{Series: make([]boxStatsPoint, 0, len(starts))}
			for _, at := range starts {
				pt := boxStatsPoint{Bucket: at.Format(time.RFC3339)}
				if p, ok := by[at]; ok {
					up, down := round2(float64(p.up)/mb), round2(float64(p.down)/mb)
					rate := round2(float64(p.conns) / bucketElapsed(at, now, bucket).Seconds())
					cpu, mem := round2(p.cpu), p.mem
					pt.MBUp, pt.MBDown, pt.ConnsPerSec, pt.CPUPct, pt.MemMB = &up, &down, &rate, &cpu, &mem
				}
				stats.Series = append(stats.Series, pt)
			}
			out.Stats = stats
		}
	}
	if out.Stats == nil {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "stats",
			Why:    "no relay of this machine pulsed in the window",
		})
	}

	var people uint64
	if err := h.analytics.QueryRow(ctx, &people, boxPeopleQ, starts[0], name); err != nil {
		return fmt.Errorf("box people: %w", err)
	}
	if people > 0 {
		n := int64(people)
		out.People = &n
	} else {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "people",
			Why:    "no signed-in socket came through this machine in the window",
		})
	}
	return nil
}

func (h *Handlers) fillBoxLastPulse(ctx context.Context, out *entryBoxCard, since time.Time, names []string) error {
	rows, err := h.analytics.Query(ctx, boxLastPulseQ, since, names)
	if err != nil {
		return fmt.Errorf("last pulse: %w", err)
	}
	defer rows.Close()
	type last struct {
		at      time.Time
		version string
		uptime  int64
	}
	by := map[string]last{}
	for rows.Next() {
		var relay string
		var l last
		if err := rows.Scan(&relay, &l.at, &l.version, &l.uptime); err != nil {
			return fmt.Errorf("scan last pulse: %w", err)
		}
		by[relay] = l
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range out.Relays {
		if l, ok := by[out.Relays[i].Name]; ok {
			at, up := l.at, l.uptime
			out.Relays[i].LastPulseAt, out.Relays[i].Version, out.Relays[i].UptimeS = &at, l.version, &up
		}
	}
	return nil
}

// openRelayFace fills a relay's port, protocol and cover from its sealed
// identity, and its verdict from its status. A seal the server key cannot
// open says so in Unreadable and shows none of the three rather than a
// plausible default.
func openRelayFace(r *fleetRelay, identityCT []byte) {
	if face, err := entry.OpenRelayFace(identityCT); err != nil {
		r.Unreadable = true
	} else {
		r.Proto, r.Port = face.Proto, face.Port
		if len(face.Covers) > 0 {
			r.Cover = face.Covers[0]
		}
	}
	switch r.Status {
	case "dead":
		r.Verdict = "bad"
	case "alive":
		r.Verdict = "ok"
	default:
		r.Verdict = "warn"
	}
}
