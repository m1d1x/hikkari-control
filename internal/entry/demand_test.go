package entry

import (
	"math"
	"testing"
	"time"
)

// pool builds a pool's buckets every five minutes over [from, to), each
// carrying mbps(t).
func pool(from, to time.Time, mbps func(time.Time) float64) []LoadPoint {
	var out []LoadPoint
	for t := from; t.Before(to); t = t.Add(demandBucket) {
		out = append(out, LoadPoint{Key: "p", At: t, Mbps: mbps(t)})
	}
	return out
}

// evening is a pool whose traffic peaks at `peak` from 20:00 to 21:00 and
// idles at 10 Mbit/s otherwise.
func evening(peak func(time.Time) float64) func(time.Time) float64 {
	return func(t time.Time) float64 {
		if t.Hour() == 20 {
			return peak(t)
		}
		return 10
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.5 }

var demandNow = time.Date(2026, 10, 27, 19, 30, 0, 0, time.UTC)

func TestForecastDemand_EmptyPoolCarriedNothing(t *testing.T) {
	d := forecastDemand(nil, demandNow, time.Hour)
	if d.Basis != DemandNone || d.ForecastMbps != 0 {
		t.Fatalf("empty pool: %+v", d)
	}
}

// Less than a week: no same hour of last week to read and no line worth
// fitting. The busiest hour there was holds, flat, even outside the window.
func TestForecastDemand_YoungPoolHoldsItsBusiestHour(t *testing.T) {
	pts := pool(demandNow.Add(-3*24*time.Hour), demandNow, evening(func(time.Time) float64 { return 50 }))
	d := forecastDemand(pts, demandNow, time.Hour)
	if d.Basis != DemandFlat || !near(d.ForecastMbps, 50) || d.SlopeMbpsPerDay != 0 {
		t.Fatalf("young pool: %+v", d)
	}
	if !near(d.NowMbps, 10) {
		t.Fatalf("now: %v, want 10", d.NowMbps)
	}
}

// The window reaches into the evening peak: last weeks' 20:00 is the forecast.
func TestForecastDemand_WeeklyReadsTheSameHourOfPastWeeks(t *testing.T) {
	pts := pool(demandNow.Add(-demandWindow), demandNow, evening(func(time.Time) float64 { return 100 }))
	d := forecastDemand(pts, demandNow, time.Hour)
	if d.Basis != DemandWeekly || !near(d.ForecastMbps, 100) {
		t.Fatalf("weekly: %+v", d)
	}
	if d.PeakAt.Hour() != 20 {
		t.Fatalf("peak at %v, want 20:00", d.PeakAt)
	}
	if !near(d.SlopeMbpsPerDay, 0) {
		t.Fatalf("flat traffic grew by %v a day", d.SlopeMbpsPerDay)
	}
}

// The evening peak grows by 10 Mbit/s a day: last week's 20:00 is not enough,
// it is carried forward by the week of growth since.
func TestForecastDemand_GrowthCarriesLastWeekForward(t *testing.T) {
	start := demandNow.Add(-demandWindow).Truncate(24 * time.Hour)
	grow := func(t time.Time) float64 { return 100 + 10*t.Sub(start).Hours()/24 }
	pts := pool(start, demandNow, evening(grow))
	d := forecastDemand(pts, demandNow, time.Hour)
	want := grow(time.Date(2026, 10, 27, 20, 0, 0, 0, time.UTC))
	if !near(d.SlopeMbpsPerDay, 10) {
		t.Fatalf("slope %v, want 10", d.SlopeMbpsPerDay)
	}
	if math.Abs(d.ForecastMbps-want) > 2 {
		t.Fatalf("forecast %v, want ≈%v (%+v)", d.ForecastMbps, want, d)
	}
}

// A machine decided at 10:00 and ready by 10:30 does not have to carry the
// evening: the forecast is the morning, and a night pool can shrink.
func TestForecastDemand_HorizonShortOfThePeakDoesNotHoldIt(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	pts := pool(now.Add(-demandWindow), now, evening(func(time.Time) float64 { return 100 }))
	d := forecastDemand(pts, now, 30*time.Minute)
	if !near(d.ForecastMbps, 10) {
		t.Fatalf("morning forecast %v, want 10 (%+v)", d.ForecastMbps, d)
	}
}

// Past weeks are a floor under nothing: a pool busier now than it ever was
// at this hour is forecast at least at what it carries.
func TestForecastDemand_NeverBelowWhatThePoolCarriesNow(t *testing.T) {
	pts := pool(demandNow.Add(-demandWindow), demandNow, func(t time.Time) float64 {
		if demandNow.Sub(t) < 30*time.Minute {
			return 300
		}
		return 10
	})
	d := forecastDemand(pts, demandNow, time.Hour)
	if !near(d.NowMbps, 300) || !near(d.ForecastMbps, 300) {
		t.Fatalf("surge: %+v", d)
	}
}

// The same hours of past weeks were never measured (the relays were silent
// then): the last day's peak stands in rather than nothing.
func TestForecastDemand_UnmeasuredPastHoursFallBackToTheLastDay(t *testing.T) {
	pts := pool(demandNow.Add(-demandWindow), demandNow, evening(func(time.Time) float64 { return 80 }))
	kept := pts[:0]
	for _, p := range pts {
		if h := p.At.Hour(); h == 19 || h == 20 {
			if demandNow.Sub(p.At) > 24*time.Hour {
				continue
			}
		}
		kept = append(kept, p)
	}
	d := forecastDemand(kept, demandNow, time.Hour)
	if d.Basis != DemandWeekly || !near(d.ForecastMbps, 80) {
		t.Fatalf("fallback: %+v", d)
	}
}

func TestDailySlope_NeedsAWeekOfDays(t *testing.T) {
	days := map[time.Time]float64{}
	for i := range demandSlopeDays - 1 {
		days[demandNow.Add(time.Duration(i)*24*time.Hour)] = float64(10 * i)
	}
	if s := dailySlope(days); s != 0 {
		t.Fatalf("slope from %d days: %v", len(days), s)
	}
	days[demandNow.Add(time.Duration(demandSlopeDays-1)*24*time.Hour)] = float64(10 * (demandSlopeDays - 1))
	if s := dailySlope(days); !near(s, 10) {
		t.Fatalf("slope %v, want 10", s)
	}
}

// A pool whose newest reading is older than two buckets is not read as
// carrying now what it carried when its relays fell silent.
func TestForecastDemand_StaleReadingIsNotNow(t *testing.T) {
	flat := func(time.Time) float64 { return 10 }
	d := forecastDemand(pool(demandNow.Add(-time.Hour), demandNow, flat), demandNow, time.Hour)
	if !d.Fresh {
		t.Fatalf("readings up to now: %+v", d)
	}
	d = forecastDemand(pool(demandNow.Add(-time.Hour), demandNow.Add(-demandStale-demandBucket), flat), demandNow, time.Hour)
	if d.Fresh {
		t.Fatalf("last reading %v before now is fresh", demandNow.Sub(d.LastAt))
	}
}

// Only live relays that said nothing are silent, grouped by their pool; a
// dead relay's silence is expected.
func TestSilentRelays(t *testing.T) {
	relays := []fleetRelay{
		{name: "a-1", pool: "p", alive: true},
		{name: "a-2", pool: "p", alive: true},
		{name: "a-3", pool: "p", alive: false},
		{name: "b-1", pool: "q", alive: true},
	}
	got := silentRelays(relays, map[string]bool{"a-1": true, "b-1": true})
	if len(got) != 1 || len(got["p"]) != 1 || got["p"][0] != "a-2" {
		t.Fatalf("silent: %v", got)
	}
}
