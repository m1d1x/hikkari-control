package admin

// entry_pressure.go answers the detection screen's top card (plan 42 К3): how
// hard the censor is pushing, split by what it is burning.
//
// # What this replaced
//
// The chart existed before this file and was the worst thing in the console:
// it took the real probe verdicts, added a sine wobble and a Gaussian spike in
// a hard-coded bucket, and labelled that spike "волна" unconditionally. The
// headline figures were then averaged out of the poisoned series, with a
// `Math.max(…, 12)` under the ban count. Everywhere else the console showed
// "нет данных" when it had none; here it showed an invention on top of
// measurements, which is worse than either.
//
// # What a ban is, exactly
//
// Not "a probe said banned" — that counts the same dead relay once per probe
// round for as long as it stays dead, which turns one ban into a slope. A ban
// is a transition: a relay whose verdict from one vantage was something else
// and is now a blocking cause. Counted per (relay, vantage), so two vantages
// reporting the same relay are not two bans.
//
// A relay that was already banned before the window opened is not a ban inside
// it: the first row of each relay's history in the window is skipped, because
// its "previous" verdict is outside what we are looking at.
//
// # The wave is read, not detected
//
// The screen used to draw a wave wherever the generator put one. A wave is an
// entity now (К2): `entry_cause_pauses`, written by the fuse in the mechanic's
// rails when one cause breaks several machines inside the window. So the
// markers on this chart are the pauses that fell inside the span — real
// moments when the autopilot stood down — or there are none.

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// The causes that count as pressure from the censor. `network_down` is the
// vantage's own outage and is not one of them; `unknown` is the absence of
// evidence; `clean` and `reality_wins` are the thing working.
var pressureCauses = clickhouse.ArraySet{"domain_blocked", "ip_banned", "fingerprint"}

// entryPressure is the card: the series, the rates, the waves, and what the
// hub could not compute.
type entryPressure struct {
	Range string `json:"range"`
	// BucketSeconds is the width of one point, so the screen can label the
	// axis without re-deriving it from the range.
	BucketSeconds int             `json:"bucketSeconds"`
	Points        []pressurePoint `json:"points"`
	// Rate is bans per hour over the window, by cause, and Before is the same
	// over the window immediately preceding it — the trend arrow's only
	// honest source. Before is null when the dataset does not reach that far.
	Rate   pressureRate  `json:"rate"`
	Before *pressureRate `json:"before"`
	// Bans is the count itself over the window: a rate per hour on a 15
	// minute window is a figure nobody can check against the chart.
	Bans pressureCount `json:"bans"`
	// Waves are the fuse's standing and lifted pauses that fell inside the
	// window. Not detected here — read from where the autopilot wrote them.
	Waves []pressureWave `json:"waves"`
	// Vantages are the observation points that reported in the window, with
	// what each of them did. One of them is why the two-vantage rule on this
	// screen is unanswerable — and a vantage that has gone quiet is the
	// reason the whole screen would read as calm, so it is said here rather
	// than left to be noticed.
	Vantages []pressureVantage `json:"vantages"`
	Missing  []missingFigure   `json:"missing"`
}

// pressureVantage is one observation point slot over the window: the slot's
// Postgres record (title, machine box/host, provider, status, createdAt)
// joined with what ClickHouse saw from its prober (probes, relays, lastSeen,
// perBucket). A registered slot whose prober stopped reporting stays in the
// list with Silent = true and a PerBucket line at zero rather than vanishing.
type pressureVantage struct {
	Region     string     `json:"region"`
	Title      string     `json:"title,omitempty"`
	Box        string     `json:"box,omitempty"`
	Host       string     `json:"host,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	Status     string     `json:"status,omitempty"`
	CreatedAt  *time.Time `json:"createdAt,omitempty"`
	RetiredAt  *time.Time `json:"retiredAt,omitempty"`
	Unreadable bool       `json:"unreadable,omitempty"`
	Silent     bool       `json:"silent"`
	Probes     int        `json:"probes"`
	Relays     int        `json:"relays"`
	// LastSeen is unix seconds of its newest probe row (0 when never seen).
	LastSeen int64 `json:"lastSeen"`
	// LastVerdict is the verdict of that newest row, whatever the window:
	// what the point said last, so a silent point still shows its last word.
	LastVerdict string `json:"lastVerdict,omitempty"`
	// PerBucket is one count per point of the series, in the same order.
	PerBucket []int `json:"perBucket"`
}

// pressurePoint is one bucket. A bucket nobody probed is absent from the probe
// data and reported as zero bans here on purpose: unlike a median, a count of
// events genuinely is zero when nothing happened — and the point carries
// `probes` so a gap in observation reads as a gap rather than as calm.
type pressurePoint struct {
	TS            int64 `json:"ts"`
	DomainBlocked int   `json:"domainBlocked"`
	IPBanned      int   `json:"ipBanned"`
	Fingerprint   int   `json:"fingerprint"`
	// Probes is how many probe rows landed in the bucket at all. Zero means
	// nobody looked, which is not the same as nothing happening.
	Probes int `json:"probes"`
}

type pressureCount struct {
	Total         int `json:"total"`
	DomainBlocked int `json:"domainBlocked"`
	IPBanned      int `json:"ipBanned"`
	Fingerprint   int `json:"fingerprint"`
}

type pressureRate struct {
	Total         float64 `json:"total"`
	DomainBlocked float64 `json:"domainBlocked"`
	IPBanned      float64 `json:"ipBanned"`
	Fingerprint   float64 `json:"fingerprint"`
}

// pressureWave is one blown fuse: a cause the autopilot stood down on.
type pressureWave struct {
	Cause    string `json:"cause"`
	Reason   string `json:"reason"`
	Boxes    int    `json:"boxes"`
	PausedAt int64  `json:"pausedAt"`
	// Lifted says whether a person has since told the autopilot to carry on.
	Lifted bool `json:"lifted"`
}

// EntryPressure answers GET /api/v1/admin/entry/pressure?range=6h.
//
// Ranges: 15m | 1h | 6h | 24h (default 6h), the same four the rest of the
// detection screen uses.
func (h *Handlers) EntryPressure(w http.ResponseWriter, r *http.Request) {
	if h.analytics == nil {
		http.Error(w, "analytics store not configured", http.StatusServiceUnavailable)
		return
	}
	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "6h"
	}
	since, bucket, ok := rangeAndBucket(rangeStr)
	if !ok {
		http.Error(w, "range is 15m, 1h, 6h or 24h", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	span := now.Sub(since)

	out := entryPressure{
		Range:         rangeStr,
		BucketSeconds: int(bucket.Seconds()),
		Points:        []pressurePoint{},
		Waves:         []pressureWave{},
		Vantages:      []pressureVantage{},
		Missing:       []missingFigure{},
	}

	buckets, counts, err := h.pressureSeries(ctx, since, now, bucket)
	if err != nil {
		log.Printf("admin entry pressure: series: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	out.Points = buckets
	out.Bans = counts
	out.Rate = ratePerHour(counts, span)

	// The window before this one, for the trend. Asked as a count and not as
	// a series: what the arrow compares is the rate, and a second chart nobody
	// draws would be a second query for nothing.
	beforePoints, beforeCounts, err := h.pressureSeries(ctx, since.Add(-span), since, bucket)
	if err != nil {
		log.Printf("admin entry pressure: previous window: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	// A previous window the dataset never saw a probe in is not a rate of
	// zero: it is the absence of a comparison, and an arrow drawn from it
	// would read as "the censor just started". The trend is absent until
	// there is something to compare against.
	if totalProbes(beforePoints) > 0 {
		before := ratePerHour(beforeCounts, span)
		out.Before = &before
	}

	if err := h.fillPressureWaves(ctx, &out, since); err != nil {
		log.Printf("admin entry pressure: waves: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillPressureVantages(ctx, &out, since, now, bucket); err != nil {
		log.Printf("admin entry pressure: vantages: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	// The screen draws a "confirm a ban from two observation points" switch,
	// and this is the figure under it. The second RU vantage does not exist —
	// it waits on a machine — so the figure is absent and says so, rather than
	// being filled with the one vantage's own numbers.
	if len(out.Vantages) < 2 {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "vantages.second",
			Why: "в сетке одна точка наблюдения: подтверждение бана из двух точек невозможно проверить, " +
				"пока у второй РФ-точки нет машины",
		})
	}
	// What the mock showed under the thresholds — "порог помягче дал бы +6
	// ложных выселений" — is a counterfactual: it needs the probe history
	// replayed against a policy that was never in force. The hub keeps probe
	// rows, not the decisions it would have made, so the figure is named
	// instead of guessed.
	out.Missing = append(out.Missing, missingFigure{
		Figure: "thresholds.counterfactual",
		Why: "«сколько выселений дал бы другой порог» — это прогон истории проб через политику, которой не было; " +
			"хаб хранит пробы, а не решения, которых не принимал",
	})

	writeJSONResponse(w, out)
}

// pressureSeries counts ban transitions per bucket and in total over a span.
//
// The window function is the whole point: a ban is a change of verdict, and
// without `prev` the query would count a relay that stays banned once per
// probe round. `seq > 1` drops each relay's first row in the span, whose
// previous verdict lies outside it.
func (h *Handlers) pressureSeries(
	ctx context.Context,
	from, to time.Time,
	bucket time.Duration,
) ([]pressurePoint, pressureCount, error) {
	const q = `
		SELECT
			bucket,
			verdict,
			count(DISTINCT relay_name) AS bans
		FROM (
			SELECT
				toStartOfInterval(ts, toIntervalSecond(?)) AS bucket,
				relay_name,
				toString(verdict)                          AS verdict,
				lagInFrame(toString(verdict)) OVER w       AS prev,
				row_number() OVER w                        AS seq
			FROM entry_probes
			WHERE ts >= ? AND ts < ?
			WINDOW w AS (
				PARTITION BY relay_name, source_region
				ORDER BY ts ASC
				ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
			)
		)
		WHERE verdict IN (?) AND seq > 1 AND prev != verdict
		GROUP BY bucket, verdict
		ORDER BY bucket
	`
	secs := int64(bucket.Seconds())
	rows, err := h.analytics.Query(ctx, q, secs, from, to, pressureCauses)
	if err != nil {
		return nil, pressureCount{}, err
	}

	byBucket := map[time.Time]*pressurePoint{}
	var counts pressureCount
	for rows.Next() {
		var at time.Time
		var verdict string
		var bans uint64
		if err := rows.Scan(&at, &verdict, &bans); err != nil {
			rows.Close()
			return nil, pressureCount{}, err
		}
		p := byBucket[at.UTC()]
		if p == nil {
			p = &pressurePoint{TS: at.UTC().Unix()}
			byBucket[at.UTC()] = p
		}
		n := int(bans)
		switch verdict {
		case "domain_blocked":
			p.DomainBlocked += n
			counts.DomainBlocked += n
		case "ip_banned":
			p.IPBanned += n
			counts.IPBanned += n
		case "fingerprint":
			p.Fingerprint += n
			counts.Fingerprint += n
		}
		counts.Total += n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, pressureCount{}, err
	}
	rows.Close()

	// How many probes landed in each bucket, so a bucket nobody looked at can
	// be told apart from a quiet one. Separate query because the ban count is
	// over transitions and this is over rows.
	const probesQ = `
		SELECT toStartOfInterval(ts, toIntervalSecond(?)) AS bucket, count() AS probes
		FROM entry_probes
		WHERE ts >= ? AND ts < ?
		GROUP BY bucket
	`
	prows, err := h.analytics.Query(ctx, probesQ, secs, from, to)
	if err != nil {
		return nil, pressureCount{}, err
	}
	for prows.Next() {
		var at time.Time
		var probes uint64
		if err := prows.Scan(&at, &probes); err != nil {
			prows.Close()
			return nil, pressureCount{}, err
		}
		p := byBucket[at.UTC()]
		if p == nil {
			p = &pressurePoint{TS: at.UTC().Unix()}
			byBucket[at.UTC()] = p
		}
		p.Probes = int(probes)
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return nil, pressureCount{}, err
	}
	prows.Close()

	// Every bucket of the span, in order, so the chart's x axis is time and
	// not "whatever we have rows for".
	points := []pressurePoint{}
	for at := from.Truncate(bucket); at.Before(to); at = at.Add(bucket) {
		if p, ok := byBucket[at.UTC()]; ok {
			points = append(points, *p)
			continue
		}
		points = append(points, pressurePoint{TS: at.UTC().Unix()})
	}
	return points, counts, nil
}

// fillPressureWaves reads the fuse's own record — the pauses that fell inside
// the window, standing or lifted.
func (h *Handlers) fillPressureWaves(ctx context.Context, out *entryPressure, since time.Time) error {
	rows, err := h.pool.Query(ctx, `
		SELECT cause, reason, boxes, paused_at, lifted_at IS NOT NULL
		FROM entry_cause_pauses
		WHERE paused_at >= $1
		ORDER BY paused_at
	`, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			wv       pressureWave
			pausedAt time.Time
		)
		if err := rows.Scan(&wv.Cause, &wv.Reason, &wv.Boxes, &pausedAt, &wv.Lifted); err != nil {
			return err
		}
		wv.PausedAt = pausedAt.UTC().Unix()
		out.Waves = append(out.Waves, wv)
	}
	return rows.Err()
}

// fillPressureVantages joins the registered observation point slots from
// Postgres (`entry_vantages`, migration 068) with the window's probe counts
// from ClickHouse (`entry_probes`).
//
// The slot list comes from Postgres so a slot whose prober went quiet remains
// on the screen with `Silent: true` and `Probes: 0` instead of disappearing
// when its last row ages past the chart window. Slots marked `retired` in
// Postgres are excluded even while their recent ClickHouse rows age out; any
// unrecorded `source_region` that reported in the window still rides along.
func (h *Handlers) fillPressureVantages(
	ctx context.Context,
	out *entryPressure,
	since, to time.Time,
	bucket time.Duration,
) error {
	byRegion := map[string]*pressureVantage{}
	order := []string{}
	retired := map[string]bool{}

	if h.pool != nil {
		pgRows, err := h.pool.Query(ctx, `
			SELECT v.slot, v.title, COALESCE(v.box, ''),
			       COALESCE(v.host_ct, b.address_ct),
			       CASE WHEN v.provider <> '' THEN v.provider ELSE COALESCE(b.provider, '') END,
			       v.status, v.created_at, v.retired_at
			FROM entry_vantages v
			LEFT JOIN entry_boxes b ON b.name = v.box
			ORDER BY v.created_at ASC, v.slot ASC
		`)
		if err != nil {
			return err
		}
		for pgRows.Next() {
			var (
				slot, title, box, provider, status string
				hostCT                             []byte
				createdAt                          time.Time
				retiredAt                          *time.Time
			)
			if err := pgRows.Scan(&slot, &title, &box, &hostCT, &provider, &status, &createdAt, &retiredAt); err != nil {
				pgRows.Close()
				return err
			}
			if status == "retired" {
				retired[slot] = true
				continue
			}
			ca := createdAt.UTC()
			pv := &pressureVantage{
				Region:    slot,
				Title:     title,
				Box:       box,
				Provider:  provider,
				Status:    status,
				CreatedAt: &ca,
				RetiredAt: retiredAt,
				PerBucket: make([]int, len(out.Points)),
			}
			if len(hostCT) > 0 {
				if plain, err := cryptobox.Decrypt(hostCT); err != nil {
					pv.Unreadable = true
				} else {
					pv.Host = string(plain)
				}
			}
			byRegion[slot] = pv
			order = append(order, slot)
		}
		if err := pgRows.Err(); err != nil {
			pgRows.Close()
			return err
		}
		pgRows.Close()
	}

	const q = `
		SELECT
			source_region,
			toStartOfInterval(ts, toIntervalSecond(?)) AS slot,
			count()                                    AS probes,
			count(DISTINCT relay_name)                 AS relays,
			max(ts)                                    AS last_seen
		FROM entry_probes
		WHERE ts >= ? AND ts < ? AND source_region != ''
		GROUP BY source_region, slot
		ORDER BY source_region, slot
	`
	rows, err := h.analytics.Query(ctx, q, int64(bucket.Seconds()), since, to)
	if err != nil {
		return err
	}
	defer rows.Close()

	slots := map[int64]int{}
	for i, p := range out.Points {
		slots[p.TS] = i
	}

	relaysSeen := map[string]int{}
	for rows.Next() {
		var (
			region   string
			slot     time.Time
			probes   uint64
			relays   uint64
			lastSeen time.Time
		)
		if err := rows.Scan(&region, &slot, &probes, &relays, &lastSeen); err != nil {
			return err
		}
		if retired[region] {
			continue
		}
		v := byRegion[region]
		if v == nil {
			v = &pressureVantage{
				Region:    region,
				Status:    "active",
				PerBucket: make([]int, len(out.Points)),
			}
			byRegion[region] = v
			order = append(order, region)
		}
		v.Probes += int(probes)
		if last := lastSeen.UTC().Unix(); last > v.LastSeen {
			v.LastSeen = last
		}
		// Relays are distinct per bucket, so the window's figure is the
		// widest round rather than a sum that counts the same relay again
		// every time it was probed.
		if int(relays) > relaysSeen[region] {
			relaysSeen[region] = int(relays)
		}
		if i, ok := slots[slot.UTC().Unix()]; ok {
			v.PerBucket[i] = int(probes)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// The newest row of every point, whatever the window: when a slot that
	// was quiet through the window last reported, and what each point said
	// last — the status line the slot's card shows.
	const lastQ = `
		SELECT source_region, max(ts) AS last_seen, argMax(toString(verdict), ts) AS last_verdict
		FROM entry_probes
		WHERE source_region != ''
		GROUP BY source_region
	`
	lrows, err := h.analytics.Query(ctx, lastQ)
	if err != nil {
		return err
	}
	for lrows.Next() {
		var (
			region, verdict string
			lastSeen        time.Time
		)
		if err := lrows.Scan(&region, &lastSeen, &verdict); err != nil {
			lrows.Close()
			return err
		}
		v := byRegion[region]
		if v == nil {
			continue
		}
		v.LastVerdict = verdict
		if v.LastSeen == 0 && lastSeen.Year() > 1970 {
			v.LastSeen = lastSeen.UTC().Unix()
		}
	}
	if err := lrows.Err(); err != nil {
		lrows.Close()
		return err
	}
	lrows.Close()

	// A slot is silent when it sent no probes in the window or has not been
	// heard from in the last ten minutes.
	silentCutoff := to.Add(-10 * time.Minute).Unix()
	for _, region := range order {
		v := byRegion[region]
		v.Relays = relaysSeen[region]
		v.Silent = v.Probes == 0 || v.LastSeen < silentCutoff
		out.Vantages = append(out.Vantages, *v)
	}
	return nil
}

// ratePerHour turns the window's counts into a rate. The span is the real one,
// so a 15 minute window reads as bans per hour at that pace rather than as the
// count itself.
func ratePerHour(c pressureCount, span time.Duration) pressureRate {
	hours := span.Hours()
	if hours <= 0 {
		return pressureRate{}
	}
	return pressureRate{
		Total:         round2(float64(c.Total) / hours),
		DomainBlocked: round2(float64(c.DomainBlocked) / hours),
		IPBanned:      round2(float64(c.IPBanned) / hours),
		Fingerprint:   round2(float64(c.Fingerprint) / hours),
	}
}

// totalProbes is how many probe rows the span holds at all.
func totalProbes(points []pressurePoint) int {
	n := 0
	for _, p := range points {
		n += p.Probes
	}
	return n
}
