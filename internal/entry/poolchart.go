package entry

// poolchart.go is what the autopilot screen draws (roadmap 42 П9.18, П9.19):
// for the whole doorway and for every pool, three lines — the traffic, the
// machines, the domains — each a week back and a day ahead dashed, each in
// units and in money. So the owner sees why the hub bought or sold, and what
// it costs, without a number to set.
//
// # Never empty
//
// A pool that carried nothing, a hub with one machine, a hub with no dataset:
// each still has a line — a flat one at what it is, zero included — and a
// forecast running on from it. An empty frame says "broken"; a flat line at
// zero says "nothing happens", which is the truth.
//
// # The dashed lines are the hub's own reading
//
// The traffic ahead is the weekly reading the sizing acts on (demand.go); the
// machines ahead are what that traffic asks for by the sizing's own formula
// (sizing.go) plus the loss buffer (fleet.go); the domains ahead are the
// pool's derived target (domainpool.go). Not a second guess drawn beside the
// one the hub acts on.
//
// # Money
//
// Machines: what the machines alive at that hour cost a day (hourly price ×
// 24) — the burn, the figure the hosting caps are read against. Domains: what
// the purchases of the last 24 hours cost, at every hour — a one-off spend
// read as a rate, so it sits on the same axis as the registrar's daily cap.

import (
	"context"
	"fmt"
	"sort"
	"time"
)

const (
	// chartPast is how far back the chart draws: a week, one of every hour
	// the weekly forecast reads.
	chartPast = 7 * 24 * time.Hour
	// chartAhead is how far ahead the dashed line goes: a day, past any
	// horizon a hoster's admission takes.
	chartAhead = 24 * time.Hour
)

// ChartPoint is one hour of a line.
type ChartPoint struct {
	At time.Time `json:"at"`
	V  float64   `json:"v"`
}

// ChartLine is one line: the past solid, the future dashed. Forecast starts
// at the last point of History, so the two join.
type ChartLine struct {
	History  []ChartPoint `json:"history"`
	Forecast []ChartPoint `json:"forecast"`
}

// ChartSet is one pool's lines, or the whole doorway's (Pool empty).
type ChartSet struct {
	Pool string `json:"pool"`
	// LoadMbps is the traffic: an hour's busiest five minutes.
	LoadMbps ChartLine `json:"loadMbps"`
	// Machines and MachinesCentsPerDay are the machines and their burn.
	Machines            ChartLine `json:"machines"`
	MachinesCentsPerDay ChartLine `json:"machinesCentsPerDay"`
	// Domains and DomainsCentsPerDay are the domains held (in use and free)
	// and what was spent on them over the 24 hours to each point. Nil for a
	// pool that carries no domains.
	Domains            *ChartLine `json:"domains,omitempty"`
	DomainsCentsPerDay *ChartLine `json:"domainsCentsPerDay,omitempty"`
	// MachineMbps is what one machine of the pool carries, zero unmeasured;
	// Headroom the share the hub plans a machine to run at.
	MachineMbps float64 `json:"machineMbps"`
	Headroom    float64 `json:"headroom"`
	// Events are the machines that came and went in the window.
	Events []FleetEvent `json:"events"`
	// Why is the pool's own account of its target, in the console's words.
	Why string `json:"why,omitempty"`
}

// FleetChart is the whole screen's charts.
type FleetChart struct {
	Now   time.Time  `json:"now"`
	Total ChartSet   `json:"total"`
	Pools []ChartSet `json:"pools"`
	// Spend is every group against its caps: the caps are drawn as lines on
	// the money charts of the whole.
	Spend []SpendReading `json:"spend"`
}

// FleetEvent is a machine coming into a pool or leaving it.
type FleetEvent struct {
	At  time.Time `json:"at"`
	Box string    `json:"box"`
	// Kind: "in" (ordered or added), "leaving" (being emptied for a
	// handback), "out" (retired, with Reason).
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

// chartBox is a machine as the chart reads it.
type chartBox struct {
	name, pool string
	created    time.Time
	retired    *time.Time
	reason     string
	handback   *time.Time
	// hourlyCents is its price an hour, zero for a machine rented by hand.
	hourlyCents float64
}

func (b chartBox) aliveAt(at time.Time) bool {
	return !b.created.After(at) && (b.retired == nil || b.retired.After(at))
}

// chartDomain is a domain as the chart reads it.
type chartDomain struct {
	bought time.Time
	gone   *time.Time
	cents  int
}

// chartAxis is the hours of the past, oldest first, ending at now; and the
// hours ahead, starting at now.
func chartAxis(now time.Time) (past, ahead []time.Time) {
	for h := now.Add(-chartPast).Truncate(time.Hour); h.Before(now); h = h.Add(time.Hour) {
		past = append(past, h)
	}
	past = append(past, now)
	ahead = append(ahead, now)
	for h := now.Truncate(time.Hour).Add(time.Hour); !h.After(now.Add(chartAhead)); h = h.Add(time.Hour) {
		ahead = append(ahead, h)
	}
	return past, ahead
}

// chartEventAxis retains short-lived resources that would disappear between
// two hourly samples. Every pool and aggregate still shares the same axis.
func chartEventAxis(past []time.Time, boxes []chartBox, domains []chartDomain) []time.Time {
	from, to := past[0], past[len(past)-1]
	points := make(map[int64]time.Time, len(past))
	add := func(at time.Time) {
		if !at.Before(from) && !at.After(to) {
			points[at.UnixNano()] = at
		}
	}
	for _, at := range past {
		add(at)
	}
	for _, b := range boxes {
		add(b.created)
		if b.retired != nil {
			add(*b.retired)
		}
	}
	for _, d := range domains {
		add(d.bought)
		add(d.bought.Add(24 * time.Hour))
		if d.gone != nil {
			add(*d.gone)
		}
	}
	out := make([]time.Time, 0, len(points))
	for _, at := range points {
		out = append(out, at)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// lineOf draws f over both axes.
func lineOf(past, ahead []time.Time, history func(time.Time) float64, forecast func(time.Time) float64) ChartLine {
	l := ChartLine{History: make([]ChartPoint, len(past)), Forecast: make([]ChartPoint, len(ahead))}
	for i, h := range past {
		l.History[i] = ChartPoint{At: h, V: history(h)}
	}
	for i, h := range ahead {
		if i == 0 {
			// The dashed line starts where the solid one ends.
			l.Forecast[i] = ChartPoint{At: h, V: l.History[len(l.History)-1].V}
			continue
		}
		l.Forecast[i] = ChartPoint{At: h, V: forecast(h)}
	}
	return l
}

// sumLines adds lines point by point; they share their axes.
func sumLines(lines []ChartLine, past, ahead []time.Time) ChartLine {
	out := lineOf(past, ahead, func(time.Time) float64 { return 0 }, func(time.Time) float64 { return 0 })
	for _, l := range lines {
		for i := range out.History {
			out.History[i].V += l.History[i].V
		}
		for i := range out.Forecast {
			out.Forecast[i].V += l.Forecast[i].V
		}
	}
	return out
}

// demandAhead is the pool's traffic at hour h ahead, read by the rules
// forecastDemand reads it by: the weekly reading where there is one, the flat
// peak otherwise; where the weekly reading has no sample for an hour, the
// pool's load now.
func demandAhead(hours map[time.Time]float64, d PoolDemand, h time.Time) float64 {
	switch d.Basis {
	case DemandWeekly:
		if v, ok := seasonalAt(hours, d.SlopeMbpsPerDay, h); ok {
			return v
		}
		return d.NowMbps
	case DemandFlat:
		return d.ForecastMbps
	}
	return 0
}

// demandSeries is the last week's hourly peaks and the next day hour by hour,
// as the dashed line reads them (demandAhead). Kept for the test that pins
// the dashed line to the weekly reading.
func demandSeries(points []LoadPoint, d PoolDemand, now time.Time) (history, forecast []DemandPoint) {
	hours := hourlyPeaks(points)
	history = []DemandPoint{}
	for h, v := range hours {
		if !h.Before(now.Add(-chartPast)) {
			history = append(history, DemandPoint{At: h, Mbps: v})
		}
	}
	sort.Slice(history, func(i, j int) bool { return history[i].At.Before(history[j].At) })
	forecast = []DemandPoint{}
	for h := now.Truncate(time.Hour); !h.After(now.Add(chartAhead)); h = h.Add(time.Hour) {
		if d.Basis == DemandNone {
			continue
		}
		forecast = append(forecast, DemandPoint{At: h, Mbps: demandAhead(hours, d, h)})
	}
	return history, forecast
}

// DemandPoint is one hour of a pool's traffic: its busiest bucket.
type DemandPoint struct {
	At   time.Time `json:"at"`
	Mbps float64   `json:"mbps"`
}

// machinesFor is how many machines a pool holds for mbps: the sizing's own
// formula over the machine it is divided by, the loss buffer on top. Without
// a capacity there is nothing to divide by, and the pool holds what the
// forecast says it holds now.
func machinesFor(mbps float64, f FleetForecast) float64 {
	c := f.Sizing.Capacity
	if c == nil || c.Mbps <= 0 || f.Sizing.Demand == nil {
		return float64(f.Target + f.Buffer)
	}
	// Reuse sizing, including its quota floor and no-data policy, rather
	// than plotting a second approximation of the machine target.
	sz := f.Sizing
	demand := *sz.Demand
	demand.ForecastMbps = mbps
	sz.Demand = &demand
	target, _, _ := loadTarget(sz, f.Live)
	return float64(target + f.Buffer)
}

// FleetChartNow reads every chart of the screen.
func (s *Service) FleetChartNow(ctx context.Context) (FleetChart, error) {
	now := s.now()
	past, ahead := chartAxis(now)
	out := FleetChart{Now: now}

	forecasts, err := s.FleetForecasts(ctx)
	if err != nil {
		return out, err
	}
	boxes, err := s.chartBoxes(ctx, now.Add(-chartPast))
	if err != nil {
		return out, err
	}
	hourly, err := s.poolLoads(ctx, forecasts, now)
	if err != nil {
		return out, err
	}
	domains, err := s.chartDomains(ctx, now.Add(-chartPast-24*time.Hour))
	if err != nil {
		return out, err
	}
	past = chartEventAxis(past, boxes, domains)
	pool, err := s.DomainPoolNow(ctx)
	if err != nil {
		return out, err
	}
	if out.Spend, err = s.Spend(ctx); err != nil {
		return out, err
	}

	var loads, machines, burns []ChartLine
	for _, f := range forecasts {
		set := poolSet(f, boxes, hourly[f.Pool], past, ahead)
		if f.Pool == FleetPool(ProtoVlessXhttp, "") {
			d, m := domainLines(domains, pool, past, ahead)
			set.Domains, set.DomainsCentsPerDay = &d, &m
		}
		out.Pools = append(out.Pools, set)
		loads, machines, burns = append(loads, set.LoadMbps), append(machines, set.Machines), append(burns, set.MachinesCentsPerDay)
	}
	// The UDP protocol stays visible even before an SFU announces a routable
	// media address. This is a chart only, never a pool to rent machines for.
	hasVoice := false
	for _, f := range forecasts {
		proto, _ := splitPool(f.Pool)
		hasVoice = hasVoice || proto == ProtoHysteria2
	}
	if !hasVoice {
		empty := poolSet(FleetForecast{Pool: ProtoHysteria2}, nil, nil, past, ahead)
		empty.Why = "Нет регионов SFU с адресом для пересылки UDP — нагрузка и парк равны нулю; после настройки региона пул появится автоматически"
		out.Pools = append(out.Pools, empty)
	}
	d, m := domainLines(domains, pool, past, ahead)
	out.Total = ChartSet{
		LoadMbps:            sumLines(loads, past, ahead),
		Machines:            sumLines(machines, past, ahead),
		MachinesCentsPerDay: sumLines(burns, past, ahead),
		Domains:             &d,
		DomainsCentsPerDay:  &m,
		Headroom:            fleetHeadroom,
		Events:              eventsOf(boxes, "", now.Add(-chartPast)),
	}
	return out, nil
}

// poolSet is one pool's lines.
func poolSet(f FleetForecast, boxes []chartBox, hours map[time.Time]float64, past, ahead []time.Time) ChartSet {
	var mine []chartBox
	priced, sum := 0, 0.0
	for _, b := range boxes {
		if b.pool != f.Pool {
			continue
		}
		mine = append(mine, b)
		if b.hourlyCents > 0 && b.retired == nil {
			priced++
			sum += b.hourlyCents
		}
	}
	// What a machine ordered from now on is expected to cost a day: the plan
	// the hub orders, else what the pool's machines cost on average.
	perMachineDay := 0.0
	switch {
	case f.Sizing.Plan != nil && f.Sizing.Plan.MonthlyCents > 0:
		perMachineDay = float64(f.Sizing.Plan.MonthlyCents) / billedHoursPerMonth * 24
	case priced > 0:
		perMachineDay = sum / float64(priced) * 24
	}
	var d PoolDemand
	if f.Sizing.Demand != nil {
		d = *f.Sizing.Demand
	}
	set := ChartSet{
		Pool:     f.Pool,
		Headroom: fleetHeadroom,
		Why:      f.TargetWhy,
		Events:   eventsOf(mine, f.Pool, past[0]),
	}
	if f.Sizing.Capacity != nil {
		set.MachineMbps = f.Sizing.Capacity.Mbps
	}
	load := func(h time.Time) float64 { return demandAhead(hours, d, h) }
	set.LoadMbps = lineOf(past, ahead, func(h time.Time) float64 { return hours[h.Truncate(time.Hour)] }, load)
	set.Machines = lineOf(past, ahead,
		func(h time.Time) float64 {
			n := 0
			for _, b := range mine {
				if b.aliveAt(h) {
					n++
				}
			}
			return float64(n)
		},
		func(h time.Time) float64 { return machinesFor(load(h), f) })
	set.MachinesCentsPerDay = lineOf(past, ahead,
		func(h time.Time) float64 {
			c := 0.0
			for _, b := range mine {
				if b.aliveAt(h) {
					c += b.hourlyCents * 24
				}
			}
			return c
		},
		func(h time.Time) float64 { return machinesFor(load(h), f) * perMachineDay })
	return set
}

// domainLines are the domains held and the day's domain spend, back and
// ahead. Ahead, the pool reaches its target and holds it — every burnt name
// replaced — and the spend is the purchases that takes: the shortage now, then
// the burn rate's names a day, at the average price paid.
func domainLines(domains []chartDomain, pool DomainPoolReading, past, ahead []time.Time) (count, spend ChartLine) {
	avg := 0.0
	for _, d := range domains {
		avg += float64(d.cents)
	}
	if len(domains) > 0 {
		avg /= float64(len(domains))
	}
	held := func(h time.Time) float64 {
		n := 0
		for _, d := range domains {
			if !d.bought.After(h) && (d.gone == nil || d.gone.After(h)) {
				n++
			}
		}
		return float64(n)
	}
	spent := func(h time.Time) float64 {
		c := 0
		for _, d := range domains {
			if d.cents > 0 && d.bought.After(h.Add(-24*time.Hour)) && !d.bought.After(h) {
				c += d.cents
			}
		}
		return float64(c)
	}
	target := float64(pool.InUse + max(pool.Target, pool.Free))
	perDay := float64(pool.Burns) / (fleetLossWindow.Hours() / 24) * avg
	short := float64(max(0, pool.Need)) * avg
	now := ahead[0]
	count = lineOf(past, ahead, held, func(time.Time) float64 { return target })
	spend = lineOf(past, ahead, spent, func(h time.Time) float64 {
		// The shortage is bought on the next pass and stays in the trailing
		// day for 24 hours.
		if h.Sub(now) < 24*time.Hour {
			return perDay + short
		}
		return perDay
	})
	return count, spend
}

// eventsOf is the comings and goings of boxes since `from`.
func eventsOf(boxes []chartBox, pool string, from time.Time) []FleetEvent {
	events := []FleetEvent{}
	for _, b := range boxes {
		if pool != "" && b.pool != pool {
			continue
		}
		if !b.created.Before(from) {
			events = append(events, FleetEvent{At: b.created, Box: b.name, Kind: "in"})
		}
		if b.handback != nil && !b.handback.Before(from) {
			events = append(events, FleetEvent{At: *b.handback, Box: b.name, Kind: "leaving"})
		}
		if b.retired != nil && !b.retired.Before(from) {
			events = append(events, FleetEvent{At: *b.retired, Box: b.name, Kind: "out", Reason: b.reason})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	return events
}

// chartBoxes reads every machine alive at some point since `since`.
func (s *Service) chartBoxes(ctx context.Context, since time.Time) ([]chartBox, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, proto, coalesce(region, ''), created_at, retired_at, coalesce(retired_reason, ''),
		       handback_since, coalesce(monthly_cents, 0)
		FROM entry_boxes
		WHERE retired_at IS NULL OR retired_at > $1
	`, since)
	if err != nil {
		return nil, fmt.Errorf("entry: chart machines: %w", err)
	}
	defer rows.Close()
	var out []chartBox
	for rows.Next() {
		var b chartBox
		var proto, region string
		var monthly int
		if err := rows.Scan(&b.name, &proto, &region, &b.created, &b.retired, &b.reason, &b.handback, &monthly); err != nil {
			return nil, fmt.Errorf("entry: chart machine: %w", err)
		}
		b.pool = FleetPool(proto, region)
		b.hourlyCents = float64(monthly) / billedHoursPerMonth
		out = append(out, b)
	}
	return out, rows.Err()
}

// chartDomains reads every domain held at some point since `since`: owned
// and not yet given up (burnt for good, failed). A burnt name under its
// second chance is not held — it carries nobody until it is returned.
func (s *Service) chartDomains(ctx context.Context, since time.Time) ([]chartDomain, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT coalesce(bought_at, created_at),
		       CASE WHEN status IN ('burned', 'dead') THEN coalesce(burned_at, updated_at) END,
		       CASE WHEN source = 'purchased' THEN cost_cents ELSE 0 END
		FROM entry_domains
		WHERE status NOT IN ('failed', 'buying')
		  AND (status NOT IN ('burned', 'dead') OR coalesce(burned_at, updated_at) > $1)
	`, since)
	if err != nil {
		return nil, fmt.Errorf("entry: chart domains: %w", err)
	}
	defer rows.Close()
	var out []chartDomain
	for rows.Next() {
		var d chartDomain
		if err := rows.Scan(&d.bought, &d.gone, &d.cents); err != nil {
			return nil, fmt.Errorf("entry: chart domain: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// poolLoads is every pool's hourly peaks over the window: one read of the
// dataset for all of them. Empty maps without a dataset.
func (s *Service) poolLoads(ctx context.Context, forecasts []FleetForecast, now time.Time) (map[string]map[time.Time]float64, error) {
	out := make(map[string]map[time.Time]float64, len(forecasts))
	for _, f := range forecasts {
		out[f.Pool] = map[time.Time]float64{}
	}
	if s.load == nil {
		return out, nil
	}
	relays, err := s.fleetRelays(ctx)
	if err != nil {
		return nil, err
	}
	groups := map[string]string{}
	for _, r := range relays {
		if _, kept := out[r.pool]; kept {
			groups[r.name] = r.pool
		}
	}
	if len(groups) == 0 {
		return out, nil
	}
	points, err := s.load.Load(ctx, now.Add(-demandWindow), demandBucket, groups)
	if err != nil {
		return nil, fmt.Errorf("entry: chart load: %w", err)
	}
	byPool := map[string][]LoadPoint{}
	for _, p := range points {
		byPool[p.Key] = append(byPool[p.Key], p)
	}
	for pool, ps := range byPool {
		if _, kept := out[pool]; kept {
			out[pool] = hourlyPeaks(ps)
		}
	}
	return out, nil
}
