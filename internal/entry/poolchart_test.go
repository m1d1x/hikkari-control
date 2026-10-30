package entry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The dashed line is the forecast the hub acts on: a weekly pool's next day
// reads last week's hours, so the evening peak lands on the evening; the
// solid line is the last week, hour by hour.
func TestDemandSeries_TheDashedLineIsTheWeeklyReading(t *testing.T) {
	pts := pool(demandNow.Add(-demandWindow), demandNow, evening(func(time.Time) float64 { return 100 }))
	d := forecastDemand(pts, demandNow, time.Hour)
	history, forecast := demandSeries(pts, d, demandNow)
	if len(history) < 7*24-1 || len(history) > 7*24+1 {
		t.Errorf("history has %d hours, want a week", len(history))
	}
	if len(forecast) != 25 {
		t.Fatalf("forecast has %d hours, want the next day (25 with this one)", len(forecast))
	}
	for _, p := range forecast {
		want := 10.0
		if p.At.Hour() == 20 {
			want = 100
		}
		if !near(p.Mbps, want) {
			t.Errorf("forecast at %s = %.1f, want %.0f", p.At.Format("02.01 15:04"), p.Mbps, want)
		}
	}
	// A pool that carried nothing has nothing to draw ahead.
	if _, f := demandSeries(nil, forecastDemand(nil, demandNow, 0), demandNow); len(f) != 0 {
		t.Errorf("empty pool forecast: %v", f)
	}
}

// The machine count is a step through the window, the money line is those
// machines' price a day, and every coming and going in it is an event. Ahead,
// the machines are what the forecast load asks for, at the price of the plan
// the hub orders.
func TestPoolSetLines(t *testing.T) {
	now := demandNow
	hour := func(h int) *time.Time { x := now.Add(time.Duration(h) * time.Hour); return &x }
	const hourly = 1.0 // 672 cents a month
	boxes := []chartBox{
		{name: "old", pool: "p", created: now.Add(-48 * time.Hour), hourlyCents: hourly},
		{name: "new", pool: "p", created: *hour(-2), hourlyCents: hourly},
		{name: "gone", pool: "p", created: now.Add(-48 * time.Hour), retired: hour(-1), reason: DeathRotated, handback: hour(-2), hourlyCents: hourly},
		{name: "other", pool: "q", created: now.Add(-48 * time.Hour), hourlyCents: hourly},
	}
	f := FleetForecast{Pool: "p", Target: 2, Sizing: sizingFor(2)}
	f.Sizing.Plan = &BoxPlan{ID: "small", MonthlyCents: 672}
	past, ahead := chartAxis(now)
	set := poolSet(f, boxes, map[time.Time]float64{}, past, ahead)

	// 16:00 · 17:00 · 18:00 · 19:00 · now (19:30): new at 17:30, gone at 18:30.
	tail := set.Machines.History[len(set.Machines.History)-5:]
	for i, want := range []float64{2, 2, 3, 2, 2} {
		if tail[i].V != want {
			t.Errorf("at %s: %.0f machines, want %.0f", tail[i].At.Format("15:04"), tail[i].V, want)
		}
	}
	if got := set.MachinesCentsPerDay.History[len(set.MachinesCentsPerDay.History)-1].V; got != 2*24*hourly {
		t.Errorf("burn now = %.1f cents a day, want two machines' 48", got)
	}
	// Ahead: the dashed line starts where the solid one ends, then the
	// sizing's own count for the forecast (a flat 139 Mbit/s → 2) at the
	// plan's 24 cents a day.
	if set.Machines.Forecast[0].V != 2 || set.Machines.Forecast[1].V != 2 || set.MachinesCentsPerDay.Forecast[1].V != 48 {
		t.Errorf("forecast: machines %+v, burn %+v", set.Machines.Forecast[:2], set.MachinesCentsPerDay.Forecast[:2])
	}
	if len(set.LoadMbps.History) != len(past) || len(set.LoadMbps.Forecast) != len(ahead) {
		t.Error("the load line does not span the axes")
	}
	kinds := ""
	for _, e := range set.Events {
		if e.At.After(now.Add(-3 * time.Hour)) {
			kinds += e.Kind + ":" + e.Box + " "
		}
	}
	if kinds != "in:new leaving:gone out:gone " {
		t.Errorf("events = %q", kinds)
	}
}

// A hub with nothing — no machine, no traffic, no domain — still draws every
// line: flat at zero, a week back and a day ahead.
func TestAnEmptyPoolDrawsFlatLines(t *testing.T) {
	past, ahead := chartAxis(demandNow)
	set := poolSet(FleetForecast{Pool: "p"}, nil, map[time.Time]float64{}, past, ahead)
	for name, l := range map[string]ChartLine{"load": set.LoadMbps, "machines": set.Machines, "burn": set.MachinesCentsPerDay} {
		if len(l.History) < 7*24 || len(l.Forecast) < 24 {
			t.Errorf("%s: %d back, %d ahead", name, len(l.History), len(l.Forecast))
		}
		for _, p := range append(l.History, l.Forecast...) {
			if p.V != 0 {
				t.Fatalf("%s at %s = %v, want 0", name, p.At, p.V)
			}
		}
	}
	count, spend := domainLines(nil, DomainPoolReading{}, past, ahead)
	if len(count.History) != len(past) || len(spend.Forecast) != len(ahead) {
		t.Error("the domain lines do not span the axes")
	}
}

// fixedLoad is a dataset that read every group at a steady rate for a day.
type fixedLoad struct{ mbps float64 }

func (f fixedLoad) Load(_ context.Context, since time.Time, bucket time.Duration, groups map[string]string) ([]LoadPoint, error) {
	keys := map[string]bool{}
	for _, k := range groups {
		keys[k] = true
	}
	start := time.Now().Add(-24 * time.Hour)
	if since.After(start) {
		start = since
	}
	var out []LoadPoint
	for k := range keys {
		for t := start.Truncate(bucket); t.Before(time.Now()); t = t.Add(bucket) {
			out = append(out, LoadPoint{Key: k, At: t, Mbps: f.mbps, VolumeMbps: 2 * f.mbps, CPUPct: 10})
		}
	}
	return out, nil
}
func (fixedLoad) ReachRTT(context.Context, time.Time, time.Duration, map[string]string) ([]RTTPoint, error) {
	return nil, nil
}
func (fixedLoad) BenchPeaks(context.Context, time.Time) (map[string]float64, error) {
	return map[string]float64{}, nil
}

// The autopilot screen's charts, end to end: the whole and every pool, the
// pool's machine and its traffic in it, the money beside the counts.
func TestFleetChartHandler(t *testing.T) {
	h := newFleetHarness(t)
	h.inPool()
	h.target(1)
	h.svc.SetLoadSource(fixedLoad{mbps: 12})
	mux := http.NewServeMux()
	pass := func(next http.HandlerFunc) http.HandlerFunc { return next }
	RegisterAdminRoutes(mux, NewAdminHandlers(h.svc, h.prov, h.pool), pass, pass)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/admin/entry/fleet/chart", nil))
	var c FleetChart
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("fleet chart: %d %s", rec.Code, rec.Body.String())
	}
	var mine *ChartSet
	for i := range c.Pools {
		if c.Pools[i].Pool == h.key {
			mine = &c.Pools[i]
		}
	}
	if mine == nil {
		t.Fatalf("the pool is not charted: %d pools", len(c.Pools))
	}
	if last := mine.Machines.History[len(mine.Machines.History)-1]; last.V != 1 {
		t.Errorf("machines now = %v, want 1", last.V)
	}
	if last := mine.LoadMbps.History[len(mine.LoadMbps.History)-1]; !near(last.V, 12) {
		t.Errorf("load now = %v, want 12", last.V)
	}
	if burn := mine.MachinesCentsPerDay.History[len(mine.MachinesCentsPerDay.History)-1].V; burn <= 0 {
		t.Errorf("a priced machine burns %v a day", burn)
	}
	if len(c.Total.Machines.Forecast) == 0 || c.Total.Domains == nil || len(c.Spend) != 2 {
		t.Errorf("total %+v, spend %+v", c.Total.Machines.Forecast, c.Spend)
	}
	if mine.Domains != nil {
		t.Error("a voice pool carries a domain line")
	}
}

func TestChartsShowUDPEvenWithoutRoutableSFU(t *testing.T) {
	h := newHarness(t)
	chart, err := h.svc.FleetChartNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range chart.Pools {
		proto, _ := splitPool(set.Pool)
		if proto != ProtoHysteria2 {
			continue
		}
		if len(set.LoadMbps.History) < 168 || len(set.LoadMbps.Forecast) < 24 {
			t.Fatalf("UDP chart has no continuous history/forecast: %+v", set)
		}
		return
	}
	t.Fatal("UDP protocol disappeared without routable SFU regions")
}

func TestMachineChartUsesTheSameQuotaFloorAsSizing(t *testing.T) {
	sz := sizingFor(1)
	sz.Plan = &BoxPlan{MonthlyCents: 500, BandwidthGB: 1000, OverageCentsPerTB: 1000}
	sz.Demand.MonthGB = 4500
	f := FleetForecast{Sizing: sz, Buffer: 1}
	target, _, _ := loadTarget(sz, 0)
	if got := machinesFor(sz.Demand.ForecastMbps, f); got != float64(target+f.Buffer) || target != 5 {
		t.Fatalf("chart = %.0f; sizing = %d plus buffer %d", got, target, f.Buffer)
	}
}

func TestChartsKeepMachinesRentedAndReleasedWithinOneHour(t *testing.T) {
	now := demandNow
	start, end := now.Add(-20*time.Minute), now.Add(-10*time.Minute)
	boxes := []chartBox{{name: "short", pool: "p", created: start, retired: &end, hourlyCents: 1}}
	past, ahead := chartAxis(now)
	past = chartEventAxis(past, boxes, nil)
	set := poolSet(FleetForecast{Pool: "p"}, boxes, nil, past, ahead)
	peak := 0.0
	for _, p := range set.Machines.History {
		peak = max(peak, p.V)
	}
	if peak != 1 || set.Machines.History[len(set.Machines.History)-1].V != 0 {
		t.Fatalf("short rental vanished or stayed alive: peak %.0f", peak)
	}
}
