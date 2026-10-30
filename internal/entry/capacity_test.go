package entry

import (
	"math"
	"testing"
)

// machine is a box's readings from 1 to n Mbit/s, one per Mbit/s, with the
// CPU and RTT each load gives.
func machine(n int, cpu func(float64) float64, rtt func(float64) float64, reports int) []BoxReading {
	out := make([]BoxReading, 0, n)
	for i := 1; i <= n; i++ {
		x := float64(i)
		r := BoxReading{Mbps: x, N: reports}
		if cpu != nil {
			r.CPUPct = cpu(x)
		}
		if rtt != nil {
			r.P95Ms = rtt(x)
		}
		out = append(out, r)
	}
	return out
}

// flatRTT is people's RTT on a machine with room: 40 ms whatever the load.
func flatRTT(float64) float64 { return 40 }

// kneeAt is a machine whose RTT holds at 40 ms up to `k` Mbit/s and doubles past it.
func kneeAt(k float64) func(float64) float64 {
	return func(x float64) float64 {
		if x >= k {
			return 80
		}
		return 40
	}
}

// People's RTT doubled past 60 Mbit/s: that is the machine's capacity, and it
// wins over a CPU line that would let it run to 200.
func TestEstimateBoxCapacity_RTTKneeIsTheCapacity(t *testing.T) {
	got := estimateBoxCapacity(machine(100, func(x float64) float64 { return 5 + x*0.475 }, kneeAt(60), 10), 0)
	if got.Source != CapacityRTT || math.Abs(got.Mbps-60) > 1 {
		t.Fatalf("knee: %+v, want rtt ≈60", got)
	}
}

// The machine runs out of CPU before people notice: the CPU line, below the
// knee, is the capacity.
func TestEstimateBoxCapacity_CPUBelowTheKneeWins(t *testing.T) {
	// 10 % idle, +1 % per Mbit/s: 100 % at 90 Mbit/s. The knee is never reached.
	got := estimateBoxCapacity(machine(60, func(x float64) float64 { return 10 + x }, flatRTT, 10), 0)
	if got.Source != CapacityCPU || math.Abs(got.Mbps-90) > 1 {
		t.Fatalf("cpu: %+v, want cpu ≈90", got)
	}
}

// Nobody reported RTT through the machine: the CPU line alone.
func TestEstimateBoxCapacity_CPUWithoutReports(t *testing.T) {
	got := estimateBoxCapacity(machine(40, func(x float64) float64 { return 20 + 2*x }, nil, 0), 0)
	if got.Source != CapacityCPU || math.Abs(got.Mbps-40) > 1 {
		t.Fatalf("cpu without reports: %+v, want cpu ≈40", got)
	}
}

// CPU that does not move with the traffic is noise, not a line: nothing is
// extrapolated, and what is known is the floor — the most it carried with
// people's RTT intact, or the bench, whichever is more.
func TestEstimateBoxCapacity_NoLineIsAFloor(t *testing.T) {
	noise := func(x float64) float64 { return 30 + 10*math.Sin(x*7) }
	got := estimateBoxCapacity(machine(50, noise, flatRTT, 10), 0)
	if got.Source != CapacityFloor || got.Mbps != 50 {
		t.Fatalf("floor from load: %+v, want floor 50", got)
	}
	got = estimateBoxCapacity(machine(50, noise, flatRTT, 10), 120)
	if got.Source != CapacityFloor || got.Mbps != 120 {
		t.Fatalf("floor from bench: %+v, want floor 120", got)
	}
}

// Too little of a machine's history to believe anything: a bench is still a
// floor, and without one there is nothing.
func TestEstimateBoxCapacity_TooLittleHistory(t *testing.T) {
	short := machine(capacityMinBuckets-1, func(x float64) float64 { return 10 + x }, nil, 0)
	if got := estimateBoxCapacity(short, 0); got.Source != CapacityFloor || got.Mbps != float64(capacityMinBuckets-1) {
		t.Fatalf("short history: %+v", got)
	}
	if got := estimateBoxCapacity(nil, 0); got.Source != CapacityNone {
		t.Fatalf("empty: %+v", got)
	}
	if got := estimateBoxCapacity(nil, 80); got.Source != CapacityFloor || got.Mbps != 80 {
		t.Fatalf("bench only: %+v", got)
	}
}

// Buckets with a couple of reports do not judge a knee: one slow client is
// not a machine out of room.
func TestEstimateBoxCapacity_ThinReportsDoNotMakeAKnee(t *testing.T) {
	got := estimateBoxCapacity(machine(100, nil, kneeAt(30), kneeMinReports-1), 0)
	if got.Source == CapacityRTT {
		t.Fatalf("knee from %d reports a bucket: %+v", kneeMinReports-1, got)
	}
}

// A single slow bucket at the top is not a knee.
func TestRTTKnee_OneSlowBucketIsNotAKnee(t *testing.T) {
	rs := machine(60, nil, func(x float64) float64 {
		if x == 60 {
			return 400
		}
		return 40
	}, 10)
	if _, healthy, ok := rttKnee(rs); ok || healthy != 59 {
		t.Fatalf("one slow bucket: ok=%v healthy=%v", ok, healthy)
	}
}

// A plan reads the best source any of its machines has, and the median among
// the machines that have it.
func TestPlanCapacity_BestSourceMedian(t *testing.T) {
	pc := planCapacity("vultr", "vc2-1c-1gb", []BoxCapacity{
		{Mbps: 300, Source: CapacityCPU},
		{Mbps: 90, Source: CapacityRTT},
		{Mbps: 110, Source: CapacityRTT},
		{Mbps: 100, Source: CapacityRTT},
		{Mbps: 20, Source: CapacityFloor},
		{Source: CapacityNone},
	})
	if pc.Source != CapacityRTT || pc.Mbps != 100 || pc.Boxes != 3 || pc.Machines != 6 {
		t.Fatalf("plan: %+v", pc)
	}
	if pc := planCapacity("vultr", "x", []BoxCapacity{{Source: CapacityNone}}); pc.Source != CapacityNone || pc.Mbps != 0 {
		t.Fatalf("nothing measured: %+v", pc)
	}
}
