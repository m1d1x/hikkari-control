package entry

// demand.go is how much a pool will have to carry (roadmap 42 П9.14): the
// first half of sizing the fleet by load, the second being how much one
// machine can carry (capacity.go, П9.15).
//
// # What is forecast
//
// The pool's traffic, in Mbit/s, at its peak — not its average: people lag at
// the peak, and a pool sized for the average lags every evening. The unit of
// time is a five-minute bucket (a relay pulses every 30 s, so a bucket holds
// ten readings and one lost pulse does not empty it), and an hour's peak is
// its busiest bucket.
//
// The forecast covers the window from now until a machine ordered now could
// be admitted — the horizon H of П9.10. Whatever is decided now has to carry
// the pool through that window, because nothing ordered later can arrive
// inside it.
//
// # How
//
// Traffic repeats by the week: an evening is busier than a night, a Sunday
// than a Tuesday. So each hour of the window is read off the same hour one and
// two weeks back, carried forward by the growth since — a straight line fitted
// to the daily peaks of the last fortnight. The busiest of those, and never
// less than the pool carries right now, is the forecast.
//
// With less than a week of history there is no "same hour last week" to read,
// and a line through a few days buys machines on noise (the rule П9.10 keeps
// for losses, fleetMinLosses). Then the forecast is the busiest hour there is,
// flat.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// demandWindow is how far back the forecast reads: two weeks, the two
	// samples of every hour of the week it takes.
	demandWindow = 14 * 24 * time.Hour
	// demandBucket is one reading of a pool's traffic.
	demandBucket = 5 * time.Minute
	// demandSeasonDays is the history the weekly reading needs; less, and
	// the forecast is the flat peak of what there is.
	demandSeasonDays = 7
	// demandSlopeDays is how many days with traffic the growth line needs.
	demandSlopeDays = 7
	// demandStale is how old the newest reading may be for the pool's "now"
	// to be now: two buckets, one closing and one in flight.
	demandStale = 2 * demandBucket
)

// LoadSource is where the hub reads what the relays carried back (the
// dataset the pulses of П9.3 land in). Declared here on the consumer side.
//
// Every read takes a map from relay name to the group it counts toward — a
// pool for demand, a machine for capacity — so the grouping is the hub's,
// which knows which relay sits on which machine, and the dataset never has to.
type LoadSource interface {
	// Load is each group's traffic and CPU since `since`, one row per
	// bucket of the given width. A bucket nobody measured is absent.
	Load(ctx context.Context, since time.Time, bucket time.Duration, groups map[string]string) ([]LoadPoint, error)
	// ReachRTT is the p95 RTT clients reported through each group's relays,
	// one row per bucket, with the number of reports behind it.
	ReachRTT(ctx context.Context, since time.Time, bucket time.Duration, groups map[string]string) ([]RTTPoint, error)
	// BenchPeaks is the best downstream any bench run measured through each
	// machine since `since`.
	BenchPeaks(ctx context.Context, since time.Time) (map[string]float64, error)
}

// LoadPoint is one group's traffic in one bucket.
type LoadPoint struct {
	Key  string
	At   time.Time
	Mbps float64
	// VolumeMbps is both directions added: what the hoster bills.
	VolumeMbps float64
	// CPUPct is the machine's CPU, as its relays reported it.
	CPUPct float64
}

// RTTPoint is the RTT people saw through one group in one bucket.
type RTTPoint struct {
	Key   string
	At    time.Time
	P95Ms float64
	N     int
}

// SetLoadSource wires the dataset. Unset, the fleet has no demand and no
// capacity to read, and says so.
func (s *Service) SetLoadSource(src LoadSource) { s.load = src }

// Basis of a demand forecast: what it could be read from.
const (
	// DemandNone: the pool carried nothing in the window.
	DemandNone = "none"
	// DemandFlat: less than a week of history; the busiest hour there is.
	DemandFlat = "flat"
	// DemandWeekly: the same hours of past weeks, carried forward by the
	// growth line.
	DemandWeekly = "weekly"
)

// PoolDemand is one pool's forecast traffic.
type PoolDemand struct {
	// NowMbps is the busiest bucket of the last hour.
	NowMbps float64 `json:"nowMbps"`
	// ForecastMbps is the busiest the pool is expected to be between now and
	// the horizon: what the machines decided now must carry.
	ForecastMbps float64 `json:"forecastMbps"`
	// PeakAt is when the forecast's peak falls.
	PeakAt time.Time `json:"peakAt"`
	// SlopeMbpsPerDay is the growth line's slope; zero when there was not
	// enough history to fit one.
	SlopeMbpsPerDay float64 `json:"slopeMbpsPerDay"`
	// HistoryDays is how far back the pool's traffic goes in the window.
	HistoryDays float64 `json:"historyDays"`
	// MonthGB is the traffic the pool would bill over thirty days at the
	// mean volume of its last week (П9.18, the quota floor in sizing.go).
	MonthGB float64 `json:"monthGb"`
	Basis   string  `json:"basis"`
	// LastAt is the newest reading; Fresh says it is recent enough for
	// NowMbps to be the pool's load now and not its load when the relays
	// fell silent.
	LastAt time.Time `json:"lastAt"`
	Fresh  bool      `json:"fresh"`
	// Silent names the pool's live relays that have not reported in the last
	// demandStale; Complete is none of them. A pool with silent relays carries
	// more than it reads, so its forecast is a floor: nothing may shrink the
	// pool on it (П9.17), whatever it says.
	Silent   []string `json:"silent,omitempty"`
	Complete bool     `json:"complete"`
	// Why is the forecast's own account of itself, in the console's words.
	Why string `json:"why"`
}

// forecastDemand reads one pool's buckets (any order) into a forecast for
// the window [now, now+horizon].
func forecastDemand(points []LoadPoint, now time.Time, horizon time.Duration) PoolDemand {
	if len(points) == 0 {
		return PoolDemand{Basis: DemandNone, Why: fmt.Sprintf("за %d дней пул ничего не вёз", int(demandWindow.Hours()/24))}
	}
	hours := hourlyPeaks(points)
	first := points[0].At
	var d PoolDemand
	for _, p := range points {
		if p.At.Before(first) {
			first = p.At
		}
		if p.At.After(d.LastAt) {
			d.LastAt = p.At
		}
		if !p.At.Before(now.Add(-time.Hour)) {
			d.NowMbps = math.Max(d.NowMbps, p.Mbps)
		}
	}
	// Only whole days enter the growth line: today has not had its evening
	// yet, and the window's first day may have begun after it, and either
	// would read as a fall.
	days := map[time.Time]float64{}
	for h, v := range hours {
		day := h.Truncate(24 * time.Hour)
		if day.Before(first) || day.Add(24*time.Hour).After(now) {
			continue
		}
		days[day] = math.Max(days[day], v)
	}
	d.HistoryDays = now.Sub(first).Hours() / 24
	d.Fresh = now.Sub(d.LastAt) <= demandStale
	d.MonthGB = monthGB(points, now)
	d.ForecastMbps, d.PeakAt = d.NowMbps, now

	if d.HistoryDays < demandSeasonDays {
		d.Basis = DemandFlat
		for h, v := range hours {
			if v > d.ForecastMbps {
				d.ForecastMbps, d.PeakAt = v, h
			}
		}
		d.Why = fmt.Sprintf("истории %.1f дн. — меньше недели: держим самый загруженный час, что был, %.1f Мбит/с",
			d.HistoryDays, d.ForecastMbps)
		return d
	}

	d.Basis = DemandWeekly
	d.SlopeMbpsPerDay = dailySlope(days)
	seen := false
	for h := now.Truncate(time.Hour); !h.After(now.Add(horizon)); h = h.Add(time.Hour) {
		v, ok := seasonalAt(hours, d.SlopeMbpsPerDay, h)
		if !ok {
			continue
		}
		seen = true
		if v > d.ForecastMbps {
			d.ForecastMbps, d.PeakAt = v, h
		}
	}
	if !seen {
		// The same hours of past weeks went unmeasured (the pool was empty
		// then, or the relays silent): the last day's peak stands in.
		for h, v := range hours {
			if !h.Before(now.Add(-24*time.Hour)) && v > d.ForecastMbps {
				d.ForecastMbps, d.PeakAt = v, h
			}
		}
		d.Why = fmt.Sprintf("тех же часов прошлых недель не мерили — держим пик последних суток, %.1f Мбит/с", d.ForecastMbps)
		return d
	}
	d.Why = fmt.Sprintf("по тем же часам прошлых недель и росту %+.1f Мбит/с в сутки: к %s ждём %.1f Мбит/с (сейчас %.1f)",
		d.SlopeMbpsPerDay, d.PeakAt.Format("02.01 15:04"), d.ForecastMbps, d.NowMbps)
	return d
}

// hourlyPeaks is every hour's busiest bucket.
func hourlyPeaks(points []LoadPoint) map[time.Time]float64 {
	hours := map[time.Time]float64{}
	for _, p := range points {
		h := p.At.Truncate(time.Hour)
		hours[h] = math.Max(hours[h], p.Mbps)
	}
	return hours
}

// seasonalAt is the weekly reading of hour h: the same hour one and two
// weeks back, each carried forward by the growth line over the weeks since,
// the busier of the two. False when neither was measured.
func seasonalAt(hours map[time.Time]float64, slopePerDay float64, h time.Time) (float64, bool) {
	week := 7 * 24 * time.Hour
	best, seen := 0.0, false
	for k := 1; k <= 2; k++ {
		past, ok := hours[h.Add(-time.Duration(k)*week)]
		if !ok {
			continue
		}
		v := math.Max(0, past+slopePerDay*float64(7*k))
		if !seen || v > best {
			best, seen = v, true
		}
	}
	return best, seen
}

// monthGB is thirty days of the mean volume of the last week's readings, in
// GB. A bucket nobody measured is left out of the mean rather than read as
// zero.
func monthGB(points []LoadPoint, now time.Time) float64 {
	var sum float64
	n := 0
	for _, p := range points {
		if !p.At.Before(now.Add(-7 * 24 * time.Hour)) {
			sum += p.VolumeMbps
			n++
		}
	}
	if n == 0 {
		return 0
	}
	const month = 30 * 24 * time.Hour
	return sum / float64(n) * 1e6 / 8 * month.Seconds() / 1e9
}

// dailySlope is the least-squares slope of the daily peaks, in Mbit/s per
// day; zero with fewer than demandSlopeDays days to fit.
func dailySlope(days map[time.Time]float64) float64 {
	if len(days) < demandSlopeDays {
		return 0
	}
	keys := make([]time.Time, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	var sx, sy, sxx, sxy float64
	n := float64(len(keys))
	for _, k := range keys {
		x := k.Sub(keys[0]).Hours() / 24
		y := days[k]
		sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}

// fleetRelay is one relay as the sizing reads it: where it sits and what that
// machine is.
type fleetRelay struct {
	name, box, pool, provider, plan string
	// alive: the relay carries people now, on a machine still in service.
	alive bool
}

// fleetRelays lists every relay the hub ever ran, dead ones included: a
// fortnight of a pool's traffic was carried by machines that are gone now,
// and the pool's history must not shrink with each handback.
func (s *Service) fleetRelays(ctx context.Context) ([]fleetRelay, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.name, b.name, b.proto, coalesce(b.region, ''), b.provider, b.plan,
		       r.status = 'alive' AND b.retired_at IS NULL
		FROM entry_relays r JOIN entry_boxes b ON b.name = r.box
	`)
	if err != nil {
		return nil, fmt.Errorf("entry: fleet relays: %w", err)
	}
	defer rows.Close()
	var out []fleetRelay
	for rows.Next() {
		var r fleetRelay
		var proto, region string
		if err := rows.Scan(&r.name, &r.box, &proto, &region, &r.provider, &r.plan, &r.alive); err != nil {
			return nil, fmt.Errorf("entry: scan fleet relay: %w", err)
		}
		r.pool = FleetPool(proto, region)
		out = append(out, r)
	}
	return out, rows.Err()
}

// poolDemands forecasts every pool in `horizons` (pool → its horizon). Nil,
// without an error, when the hub has no dataset to read.
func (s *Service) poolDemands(ctx context.Context, horizons map[string]time.Duration) (map[string]PoolDemand, error) {
	if s.load == nil {
		return nil, nil
	}
	relays, err := s.fleetRelays(ctx)
	if err != nil {
		return nil, err
	}
	groups := make(map[string]string, len(relays))
	for _, r := range relays {
		groups[r.name] = r.pool
	}
	now := s.now()
	points, err := s.load.Load(ctx, now.Add(-demandWindow), demandBucket, groups)
	if err != nil {
		return nil, fmt.Errorf("entry: pool load: %w", err)
	}
	byPool := map[string][]LoadPoint{}
	for _, p := range points {
		byPool[p.Key] = append(byPool[p.Key], p)
	}
	// Who reported lately, relay by relay: a pool's sum cannot say which of
	// its relays it is missing.
	live := map[string]string{}
	for _, r := range relays {
		if r.alive {
			live[r.name] = r.name
		}
	}
	recent, err := s.load.Load(ctx, now.Add(-demandStale), demandBucket, live)
	if err != nil {
		return nil, fmt.Errorf("entry: relays reporting: %w", err)
	}
	reported := map[string]bool{}
	for _, p := range recent {
		reported[p.Key] = true
	}
	silent := silentRelays(relays, reported)
	out := make(map[string]PoolDemand, len(horizons))
	for pool, h := range horizons {
		d := forecastDemand(byPool[pool], now, h)
		d.Silent = silent[pool]
		d.Complete = len(d.Silent) == 0
		if !d.Complete {
			d.Why += fmt.Sprintf("; молчат %d живых релеев — цифры занижены", len(d.Silent))
		}
		out[pool] = d
	}
	return out, nil
}

// silentRelays groups by pool the live relays that have not reported.
func silentRelays(relays []fleetRelay, reported map[string]bool) map[string][]string {
	out := map[string][]string{}
	for _, r := range relays {
		if r.alive && !reported[r.name] {
			out[r.pool] = append(out[r.pool], r.name)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}
