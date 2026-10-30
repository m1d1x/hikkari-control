package admin

// entry_overview_series.go draws the overview's history: one point per bucket
// of the window for each card — reach, money, traffic, survival.
//
// # Why the bucket follows the window
//
// The cards used to be bucketed by calendar day whatever the window was, and
// three of them drew a generated curve until there were six days of points. A
// stand deployed an hour ago had a day of nothing to look at, and a week of a
// fabricated shape after that. The bucket now comes from the window — an hour
// for 24h, six hours for 7d, a day for 30d — so the first hour of data is a
// point on the 24h chart, and every chart on the screen shares one axis.
//
// # A bucket nobody measured is null
//
// Not zero. A relay that did not pulse, an hour nobody connected, a day no
// machine died: the value is absent and the chart breaks the line there. The
// one exception is money, which genuinely is zero for a bucket nothing was
// spent in — a count of cents, unlike a median, has a true zero.

import (
	"context"
	"fmt"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// seriesBucket is the width of one point for a window.
func seriesBucket(window time.Duration) time.Duration {
	switch {
	case window <= 24*time.Hour:
		return time.Hour
	case window <= 7*24*time.Hour:
		return 6 * time.Hour
	}
	return 24 * time.Hour
}

// bucketStarts lists every bucket of the span, oldest first. Truncation is
// against the Unix epoch, which is midnight UTC: an hour, six hours and a day
// all divide a day, so the buckets line up with ClickHouse's
// toStartOfInterval on the same widths.
func bucketStarts(since, now time.Time, bucket time.Duration) []time.Time {
	var out []time.Time
	for at := since.Truncate(bucket); !at.After(now); at = at.Add(bucket) {
		out = append(out, at.UTC())
	}
	return out
}

// bucketElapsed is how much of a bucket has happened by now: the whole width,
// except for the current one. A rate over the current bucket is taken over
// its elapsed part, or the last point of every chart would read as a fall.
func bucketElapsed(at, now time.Time, bucket time.Duration) time.Duration {
	end := at.Add(bucket)
	if end.After(now) {
		end = now
	}
	if d := end.Sub(at); d > 0 {
		return d
	}
	return time.Second
}

type reachPoint struct {
	Bucket   string `json:"bucket"`
	Attempts int64  `json:"attempts"`
	// Null when nobody attempted in the bucket.
	AnyPct   *float64 `json:"anyPct"`
	FirstPct *float64 `json:"firstPct"`
}

// moneyPoint is what one bucket cost, as a day's rate.
type moneyPoint struct {
	Bucket string `json:"bucket"`
	// MachinesPerDayCents is the rent of the machines in service during the
	// bucket, as a daily rate.
	MachinesPerDayCents int64 `json:"machinesPerDayCents"`
	// DomainsCents is what was paid for names inside the bucket. Not scaled to
	// a day: a purchase is an event, and a $5 name bought within an hour is
	// $5, not $120 a day.
	DomainsCents int64 `json:"domainsCents"`
	TotalCents   int64 `json:"totalCents"`
	// People is how many distinct accounts reached in the 24 hours ending at
	// the bucket's end; PerUserMonthCents is the bucket's rate over them,
	// run out to a month. Both null when nobody reached.
	People            *int64 `json:"people"`
	PerUserMonthCents *int64 `json:"perUserMonthCents"`
}

// trafficPoint is what the relays carried inside one bucket.
type trafficPoint struct {
	Bucket string `json:"bucket"`
	// Null when no relay pulsed twice inside the bucket.
	GBUp   *float64 `json:"gbUp"`
	GBDown *float64 `json:"gbDown"`
	// GBPerPersonDay is the bucket's traffic as a daily rate over the people
	// of the 24 hours ending at the bucket's end; null when nobody reached.
	GBPerPersonDay *float64 `json:"gbPerPersonDay"`
}

// survivalPoint is what the censor burned inside one bucket, and how long it
// had lived. Medians are over the deaths in the bucket and null without one.
type survivalPoint struct {
	Bucket           string   `json:"bucket"`
	Deaths           int      `json:"deaths"`
	BoxMedianDays    *float64 `json:"boxMedianDays"`
	RelayMedianDays  *float64 `json:"relayMedianDays"`
	DomainMedianDays *float64 `json:"domainMedianDays"`
}

// fillSeries adds every card's history to the overview.
func (h *Handlers) fillSeries(ctx context.Context, out *entryOverview, since time.Time, window time.Duration) error {
	now := time.Now().UTC()
	bucket := seriesBucket(window)
	starts := bucketStarts(since, now, bucket)
	out.BucketSeconds = int(bucket.Seconds())

	people, err := h.peopleTrailingDay(ctx, starts, bucket, now)
	if err != nil {
		return fmt.Errorf("people: %w", err)
	}
	if err := h.fillReachSeries(ctx, out, starts, bucket); err != nil {
		return fmt.Errorf("reach series: %w", err)
	}
	if err := h.fillMoneySeries(ctx, out, starts, bucket, now, people); err != nil {
		return fmt.Errorf("money series: %w", err)
	}
	if err := h.fillTrafficSeries(ctx, out, starts, bucket, now, people); err != nil {
		return fmt.Errorf("traffic series: %w", err)
	}
	if err := h.fillSurvivalSeries(ctx, out, starts, bucket); err != nil {
		return fmt.Errorf("survival series: %w", err)
	}
	return nil
}

// peopleTrailingDay counts, for every bucket, the distinct accounts that
// reached in the 24 hours ending at its end (or at now, for the current one).
//
// A day and not the bucket itself: the per-person figures divide a day's
// worth of cost or traffic, and the people of one hour are a fraction of the
// people that hour's cost serves.
func (h *Handlers) peopleTrailingDay(ctx context.Context, starts []time.Time, bucket time.Duration, now time.Time) (map[time.Time]int64, error) {
	out := map[time.Time]int64{}
	if len(starts) == 0 {
		return out, nil
	}
	ends := make([]int64, len(starts))
	for i, at := range starts {
		end := at.Add(bucket)
		if end.After(now) {
			end = now
		}
		ends[i] = end.Unix()
	}
	const q = `
		SELECT e, uniqExact(account)
		FROM entry_reach
		ARRAY JOIN CAST(? AS Array(Int64)) AS e
		WHERE ts >= ?
		  AND toUnixTimestamp(ts) < e
		  AND toUnixTimestamp(ts) >= e - 86400
		GROUP BY e
	`
	rows, err := h.analytics.Query(ctx, q, ends, starts[0].Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byEnd := map[int64]int64{}
	for rows.Next() {
		var e int64
		var n uint64
		if err := rows.Scan(&e, &n); err != nil {
			return nil, err
		}
		byEnd[e] = int64(n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, at := range starts {
		if n := byEnd[ends[i]]; n > 0 {
			out[at] = n
		}
	}
	return out, nil
}

// fillReachSeries splits the window's attempts by bucket.
func (h *Handlers) fillReachSeries(ctx context.Context, out *entryOverview, starts []time.Time, bucket time.Duration) error {
	out.Reach.Series = []reachPoint{}
	if len(starts) == 0 {
		return nil
	}
	const q = `
		SELECT
			toStartOfInterval(ts, toIntervalHour(?))         AS bucket,
			count()                                          AS attempts,
			countIf(outcome != 'failed')                     AS any_through,
			countIf(outcome = 'direct' OR (outcome = 'relay' AND relays_tried <= 1)) AS first_try
		FROM entry_reach
		WHERE ts >= ?
		GROUP BY bucket
	`
	rows, err := h.analytics.Query(ctx, q, int64(bucket/time.Hour), starts[0])
	if err != nil {
		return err
	}
	defer rows.Close()
	type counts struct{ attempts, any, first uint64 }
	by := map[time.Time]counts{}
	for rows.Next() {
		var at time.Time
		var c counts
		if err := rows.Scan(&at, &c.attempts, &c.any, &c.first); err != nil {
			return err
		}
		by[at.UTC()] = c
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, at := range starts {
		p := reachPoint{Bucket: at.Format(time.RFC3339)}
		if c, ok := by[at]; ok && c.attempts > 0 {
			total := float64(c.attempts)
			anyPct := round2(float64(c.any) / total * 100)
			firstPct := round2(float64(c.first) / total * 100)
			p.Attempts, p.AnyPct, p.FirstPct = int64(c.attempts), &anyPct, &firstPct
		}
		out.Reach.Series = append(out.Reach.Series, p)
	}
	return nil
}

// fillMoneySeries accrues the rent of the machines in service bucket by
// bucket and adds the names bought in each. Computed in Go from the rows: the
// fleet and the ledger are a handful of rows, and the accrual is the same
// tstzrange clipping fillMachineSpend does in SQL, per bucket.
func (h *Handlers) fillMoneySeries(
	ctx context.Context,
	out *entryOverview,
	starts []time.Time,
	bucket time.Duration,
	now time.Time,
	people map[time.Time]int64,
) error {
	out.Money.Series = []moneyPoint{}
	if len(starts) == 0 {
		return nil
	}
	since := starts[0]

	type rented struct {
		from, to time.Time
		monthly  int64
	}
	var boxes []rented
	rows, err := h.pool.Query(ctx, `
		SELECT created_at, retired_at, monthly_cents
		FROM entry_boxes
		WHERE monthly_cents IS NOT NULL AND (retired_at IS NULL OR retired_at >= $1)
	`, since)
	if err != nil {
		return err
	}
	for rows.Next() {
		var b rented
		var retired *time.Time
		if err := rows.Scan(&b.from, &retired, &b.monthly); err != nil {
			rows.Close()
			return err
		}
		b.to = now
		if retired != nil {
			b.to = *retired
		}
		boxes = append(boxes, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Dated by created_at, the same rule fillMoney's month and the buyer's
	// cap use: the spend is committed from the moment the intent is written.
	spent := map[time.Time]int64{}
	drows, err := h.pool.Query(ctx, `
		SELECT created_at, cost_cents
		FROM entry_domains
		WHERE source = 'purchased' AND status <> 'failed' AND created_at >= $1
	`, since)
	if err != nil {
		return err
	}
	for drows.Next() {
		var at time.Time
		var cents int64
		if err := drows.Scan(&at, &cents); err != nil {
			drows.Close()
			return err
		}
		spent[at.UTC().Truncate(bucket)] += cents
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return err
	}

	for _, at := range starts {
		elapsed := bucketElapsed(at, now, bucket)
		end := at.Add(elapsed)
		var accrued float64
		for _, b := range boxes {
			from, to := maxTime(b.from, at), minTime(b.to, end)
			if !to.After(from) {
				continue
			}
			perDay := float64(b.monthly) / float64(daysInMonth(at))
			accrued += perDay * to.Sub(from).Hours() / 24
		}
		p := moneyPoint{
			Bucket:              at.Format(time.RFC3339),
			MachinesPerDayCents: int64(accrued * 24 / elapsed.Hours()),
			DomainsCents:        spent[at],
		}
		p.TotalCents = p.MachinesPerDayCents + p.DomainsCents
		if n, ok := people[at]; ok {
			perUser := p.TotalCents * 30 / n
			p.People, p.PerUserMonthCents = &n, &perUser
		}
		out.Money.Series = append(out.Money.Series, p)
	}
	return nil
}

// relayDeltasQ sums [analytics.EntryRelayDeltas] per bucket: the bucket width in hours,
// then the start of the span.
const relayDeltasQ = `
	SELECT
		toStartOfInterval(ts, toIntervalHour(?)) AS bucket,
		sum(d_up)                                AS up,
		sum(d_down)                              AS down
	FROM (` + analytics.EntryRelayDeltas + `)
	GROUP BY bucket
`

// relayDeltasByRelayQ sums [analytics.EntryRelayDeltas] per relay over the span.
const relayDeltasByRelayQ = `
	SELECT relay_name, sum(d_up) + sum(d_down) AS bytes
	FROM (` + analytics.EntryRelayDeltas + `)
	GROUP BY relay_name
`

// fillTrafficSeries is what the relays carried, bucket by bucket. The block
// itself is fillTraffic's; without it there is nothing to draw.
func (h *Handlers) fillTrafficSeries(
	ctx context.Context,
	out *entryOverview,
	starts []time.Time,
	bucket time.Duration,
	now time.Time,
	people map[time.Time]int64,
) error {
	if out.Traffic == nil || len(starts) == 0 {
		return nil
	}
	rows, err := h.analytics.Query(ctx, relayDeltasQ, int64(bucket/time.Hour), starts[0])
	if err != nil {
		return err
	}
	defer rows.Close()
	type carried struct{ up, down uint64 }
	by := map[time.Time]carried{}
	for rows.Next() {
		var at time.Time
		var c carried
		if err := rows.Scan(&at, &c.up, &c.down); err != nil {
			return err
		}
		by[at.UTC()] = c
	}
	if err := rows.Err(); err != nil {
		return err
	}
	const gb = 1024 * 1024 * 1024
	out.Traffic.Series = []trafficPoint{}
	for _, at := range starts {
		p := trafficPoint{Bucket: at.Format(time.RFC3339)}
		if c, ok := by[at]; ok {
			up, down := round2(float64(c.up)/gb), round2(float64(c.down)/gb)
			p.GBUp, p.GBDown = &up, &down
			if n, ok := people[at]; ok {
				perDay := (float64(c.up+c.down) / gb) * 24 / bucketElapsed(at, now, bucket).Hours()
				v := round2(perDay / float64(n))
				p.GBPerPersonDay = &v
			}
		}
		out.Traffic.Series = append(out.Traffic.Series, p)
	}
	return nil
}

// fillSurvivalSeries lists what the censor burned in each bucket and how long
// it had lived. Only censor deaths: a machine handed back on purpose did not
// "survive" for less, it was let go.
func (h *Handlers) fillSurvivalSeries(ctx context.Context, out *entryOverview, starts []time.Time, bucket time.Duration) error {
	out.Survival.Series = []survivalPoint{}
	if len(starts) == 0 {
		return nil
	}
	since := starts[0]
	relays := map[time.Time][]float64{}
	boxes := map[time.Time][]float64{}
	domains := map[time.Time][]float64{}

	collect := func(dst map[time.Time][]float64, q string, args ...any) error {
		rows, err := h.pool.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var at time.Time
			var days float64
			if err := rows.Scan(&at, &days); err != nil {
				return err
			}
			k := at.UTC().Truncate(bucket)
			dst[k] = append(dst[k], days)
		}
		return rows.Err()
	}
	if err := collect(relays, `
		SELECT died_at, EXTRACT(EPOCH FROM (died_at - coalesce(issued_at, created_at))) / 86400.0
		FROM entry_relays
		WHERE died_at >= $1 AND died_reason = ANY($2)
	`, since, entry.CensorDeaths); err != nil {
		return err
	}
	if err := collect(boxes, `
		SELECT retired_at, EXTRACT(EPOCH FROM (retired_at - created_at)) / 86400.0
		FROM entry_boxes
		WHERE retired_at >= $1 AND retired_reason = ANY($2)
	`, since, entry.CensorDeaths); err != nil {
		return err
	}
	// A domain has no cause of death of its own (it lives on the relay): a
	// burned name is the one kind of death the ledger records at all.
	if err := collect(domains, `
		SELECT burned_at, EXTRACT(EPOCH FROM (burned_at - assigned_at)) / 86400.0
		FROM entry_domains
		WHERE burned_at >= $1 AND assigned_at IS NOT NULL
	`, since); err != nil {
		return err
	}
	for _, at := range starts {
		out.Survival.Series = append(out.Survival.Series, survivalPoint{
			Bucket:           at.Format(time.RFC3339),
			Deaths:           len(relays[at]),
			BoxMedianDays:    roundPtr(median(boxes[at])),
			RelayMedianDays:  roundPtr(median(relays[at])),
			DomainMedianDays: roundPtr(median(domains[at])),
		})
	}
	return nil
}

func daysInMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
