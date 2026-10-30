package entry

import (
	"context"
	"strings"
	"testing"
	"time"
)

func demandOf(mbps float64) *PoolDemand {
	return &PoolDemand{Basis: DemandWeekly, ForecastMbps: mbps, Fresh: true, Complete: true}
}

var plan100 = &PlanCapacity{Mbps: 100, Source: CapacityRTT}

// The forecast over 70 % of a machine, rounded up: 150 Mbit/s on 100 Mbit/s
// machines is three, not two — two would run at 75 %. And the pool keeps a
// machine until the rest would run at 50 % without it: the same forecast over
// the lower line, so buying and selling never meet.
func TestLoadTarget_BuysAt70KeepsTo50(t *testing.T) {
	for _, c := range []struct {
		mbps       float64
		want, keep int
	}{{1, 1, 1}, {50, 1, 1}, {50.1, 1, 2}, {70, 1, 2}, {70.1, 2, 2}, {140, 2, 3}, {150, 3, 3}} {
		got, keep, why := loadTarget(PoolSizing{Demand: demandOf(c.mbps), Capacity: plan100}, 5)
		if got != c.want || keep != c.keep {
			t.Errorf("%.1f Mbit/s: target %d keep %d, want %d and %d (%s)", c.mbps, got, keep, c.want, c.keep, why)
		}
	}
}

// A pool that carried nothing holds one machine for the first comer, and
// sheds the rest: nobody is on them.
func TestLoadTarget_ColdStartIsOneMachine(t *testing.T) {
	got, keep, _ := loadTarget(PoolSizing{Demand: &PoolDemand{Basis: DemandNone}, Capacity: plan100}, 4)
	if got != 1 || keep != 1 {
		t.Fatalf("idle pool: target %d keep %d, want 1 and 1", got, keep)
	}
}

// Every blind reading keeps what is live: no dataset holds it exactly (and
// buys nothing either), no capacity holds at least one, stale or incomplete
// demand never lets go of a live machine — and none of them stops a rise the
// reading does show.
func TestLoadTarget_NeverShrinksBlind(t *testing.T) {
	check := func(name string, sz PoolSizing, live, target, keep int, word string) {
		t.Helper()
		got, k, why := loadTarget(sz, live)
		if got != target || k != keep || !strings.Contains(why, word) {
			t.Errorf("%s: target %d keep %d (%s), want %d and %d with %q", name, got, k, why, target, keep, word)
		}
	}
	check("no dataset, nothing live", PoolSizing{}, 0, 0, 0, "нет датасета")
	check("no dataset", PoolSizing{}, 3, 3, 3, "нет датасета")
	check("no capacity, nothing live", PoolSizing{Demand: demandOf(500)}, 0, 1, 1, "не мерена")
	check("no capacity", PoolSizing{Demand: demandOf(500)}, 3, 3, 3, "не мерена")
	stale := demandOf(10)
	stale.Fresh = false
	check("stale", PoolSizing{Demand: stale, Capacity: plan100}, 4, 1, 4, "старые")
	silent := demandOf(10)
	silent.Complete, silent.Silent = false, []string{"a-1"}
	check("incomplete", PoolSizing{Demand: silent, Capacity: plan100}, 4, 1, 4, "молчат 1")
	silent.ForecastMbps = 500
	check("incomplete but rising", PoolSizing{Demand: silent, Capacity: plan100}, 4, 8, 10, "нужно 8")
}

// The plan the hub orders is what a forecast is divided by; unmeasured, the
// best-measured plan among the pool's own machines stands in, the smaller on
// a tie.
func TestPoolCapacity_OrderedPlanThenTheBestOfThePool(t *testing.T) {
	a, b, c := planRef{"v", "a"}, planRef{"v", "b"}, planRef{"v", "c"}
	byPlan := map[planRef]PlanCapacity{
		a: {Mbps: 300, Source: CapacityCPU},
		b: {Mbps: 120, Source: CapacityRTT},
		c: {Mbps: 90, Source: CapacityRTT},
	}
	own := map[planRef]bool{a: true, b: true, c: true}
	if got, ok := poolCapacity(byPlan, a, own); !ok || got.Mbps != 300 {
		t.Errorf("ordered plan measured: %+v", got)
	}
	if got, ok := poolCapacity(byPlan, planRef{"v", "new"}, own); !ok || got.Mbps != 90 {
		t.Errorf("ordered plan unmeasured: %+v, want the rtt plan at 90", got)
	}
	if _, ok := poolCapacity(byPlan, planRef{"v", "new"}, nil); ok {
		t.Error("nothing measured anywhere, yet a capacity")
	}
}

// countingLoad is a dataset that has seen nothing and counts how often it is
// asked.
type countingLoad struct{ loads int }

func (c *countingLoad) Load(context.Context, time.Time, time.Duration, map[string]string) ([]LoadPoint, error) {
	c.loads++
	return nil, nil
}
func (c *countingLoad) ReachRTT(context.Context, time.Time, time.Duration, map[string]string) ([]RTTPoint, error) {
	return nil, nil
}
func (c *countingLoad) BenchPeaks(context.Context, time.Time) (map[string]float64, error) {
	return map[string]float64{}, nil
}

// The fortnight is read once per bucket, not on every detector pass; a pool
// the reading does not cover, or a bucket gone by, reads it again.
func TestPoolSizings_ReadOncePerBucket(t *testing.T) {
	h := newHostHarness(t)
	src := &countingLoad{}
	h.svc.SetLoadSource(src)
	ctx := context.Background()
	policy, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := func(pools ...string) int {
		t.Helper()
		before := src.loads
		if _, err := h.svc.poolSizings(ctx, policy, pools); err != nil {
			t.Fatal(err)
		}
		return src.loads - before
	}
	reality := FleetPool(ProtoVlessRealityVision, "")
	if read(reality) == 0 {
		t.Fatal("the first reading did not read the dataset")
	}
	if n := read(reality); n != 0 {
		t.Fatalf("a second pass in the same bucket read the dataset %d times", n)
	}
	if read(reality, FleetPool(ProtoVlessXhttp, "")) == 0 {
		t.Fatal("a pool the reading did not cover was not read")
	}
	later := time.Now().Add(demandBucket + time.Second)
	h.svc.now = func() time.Time { return later }
	if read(reality) == 0 {
		t.Fatal("a reading a bucket old was kept")
	}
}

// A plan's allowance cheaper than the same traffic as overage: the pool holds
// machines for the month's volume even where the peak needs fewer. Dearer:
// overage is paid and no machine is bought for it.
func TestLoadTarget_QuotaFloorWhenAMachineIsCheaperThanOverage(t *testing.T) {
	d := demandOf(10) // one machine by the peak
	d.MonthGB = 5000  // five terabytes a month
	cheap := &BoxPlan{ID: "small", MonthlyCents: 500, BandwidthGB: 1000, OverageCentsPerTB: 1000}
	got, keep, why := loadTarget(PoolSizing{Demand: d, Capacity: plan100, Plan: cheap}, 1)
	if got != 5 || keep != 5 || !strings.Contains(why, "не меньше 5") {
		t.Errorf("$5 for 1 TB against $10 a TB of overage: target %d keep %d (%s), want 5", got, keep, why)
	}
	dear := &BoxPlan{ID: "big", MonthlyCents: 4000, BandwidthGB: 1000, OverageCentsPerTB: 1000}
	got, _, why = loadTarget(PoolSizing{Demand: d, Capacity: plan100, Plan: dear}, 1)
	if got != 1 || !strings.Contains(why, "дешевле машины") {
		t.Errorf("$40 for 1 TB against $10 a TB of overage: target %d (%s), want 1", got, why)
	}
	unknown := &BoxPlan{ID: "x", MonthlyCents: 500, BandwidthGB: 1000}
	if got, _, why := loadTarget(PoolSizing{Demand: d, Capacity: plan100, Plan: unknown}, 1); got != 1 || strings.Contains(why, "квот") {
		t.Errorf("no overage price: target %d (%s), want 1 and no word of a quota", got, why)
	}
}

// Thirty days of the last week's mean volume; an unmeasured bucket is not a
// zero.
func TestMonthGB(t *testing.T) {
	now := demandNow
	pts := []LoadPoint{
		{At: now.Add(-time.Hour), VolumeMbps: 8},
		{At: now.Add(-2 * time.Hour), VolumeMbps: 8},
		{At: now.Add(-10 * 24 * time.Hour), VolumeMbps: 800}, // older than a week
	}
	// 8 Mbit/s = 1 MB/s; thirty days of it is 2592 GB.
	if got := monthGB(pts, now); got < 2591 || got > 2593 {
		t.Errorf("month = %.1f GB, want 2592", got)
	}
}

// The real empty-main-hub snapshot had 0.085 Mbps of historic probe traffic
// and a plan floor of 0.037 Mbps. Dividing by that floor bought four boxes.
func TestObservedProbeTrafficIsNotAMachineCapacityCeiling(t *testing.T) {
	sz := PoolSizing{Demand: demandOf(0.0845536267), Capacity: &PlanCapacity{Mbps: 0.0366044713, Source: CapacityFloor}}
	for _, live := range []int{0, 1, 4} {
		n, k, why := loadTarget(sz, live)
		if n != max(1, live) || k != max(1, live) || !strings.Contains(why, "нижней границе") {
			t.Fatalf("live=%d: target=%d keep=%d: %s", live, n, k, why)
		}
	}
	sz.Capacity.Source = CapacityRTT
	if n, _, _ := loadTarget(sz, 1); n != 4 {
		t.Fatalf("a measured ceiling must still scale: got %d", n)
	}
	sz.Capacity.Source = CapacityFloor
	sz.Demand.MonthGB = 3000
	sz.Plan = &BoxPlan{MonthlyCents: 500, BandwidthGB: 1024, OverageCentsPerTB: 1000}
	if n, _, _ := loadTarget(sz, 1); n != 3 {
		t.Fatalf("measured traffic allowance still scales on price: got %d", n)
	}
}
