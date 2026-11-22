package cascadewatch

import (
	"context"
	"testing"
	"time"
)

// TestDBAWireChartDrawsEveryTargetAndTheMoves: a bucket of minutes of two
// calls, the stand's and the entry's downloads and CPU under one name per
// wire, and the selector's moves as marks; a bucket nobody measured is null.
func TestDBAWireChartDrawsEveryTargetAndTheMoves(t *testing.T) {
	ctx := context.Background()
	store := NewStore(dbPool(t))
	now := time.Date(2026, 11, 11, 5, 55, 0, 0, time.UTC)
	at := func(m int) time.Time { return time.Date(2026, 11, 11, 5, m, 0, 0, time.UTC) }
	call := func(ts time.Time, target string, ok bool, gaps60, p99, gapMax, lost, silent float64) Run {
		return Run{TS: ts, Entry: "spb", Exit: "unl1", Layer: LayerCall, Vantage: "entry", Target: target, OK: boolp(ok),
			Data: map[string]any{"sent": 3000.0, "lost": lost, "gaps60": gaps60, "rtt_p99_ms": p99, "gap_max_ms": gapMax, "silent_ms": silent}}
	}
	wire := func(ts time.Time, target string, ok bool, data map[string]any) Run {
		return Run{TS: ts, Entry: "spb", Exit: "unl1", Layer: LayerWire, Vantage: "entry", Target: target, OK: boolp(ok), Data: data}
	}
	ev := func(ts time.Time, data map[string]any) Run {
		return Run{TS: ts, Entry: "spb", Exit: "unl1", Layer: LayerEvent, Vantage: "entry", Data: data}
	}
	if err := store.Insert(ctx,
		call(at(41), "copies2", false, 4, 80, 3092, 30, 4000),
		call(at(42), "copies2", true, 1, 60, 70, 0, 100),
		call(at(43), "copies2", true, 0, 50, 40, 0, 0),
		call(at(41), "trunk-udp", true, 0, 51, 90, 3, 0),
		wire(at(41), "udp", true, map[string]any{"mode": "bulk", "bytes": 1048576.0, "ms": 100.0, "end": "ok"}),
		wire(at(42), "trunk-quic", true, map[string]any{"mode": "bulk_1m", "bytes": 1048576.0, "ms": 50.0, "end": "ok"}),
		wire(at(43), "trunk-quic", false, map[string]any{"mode": "bulk_1m", "bytes": 1000.0, "ms": 5000.0, "end": "timeout"}),
		wire(at(44), "quic", true, map[string]any{"mode": "cpu", "cpu_ms": 3600.0}),
		wire(at(44), "quic", true, map[string]any{"mode": "bulk_12m", "bytes": 12582912.0, "ms": 400.0, "end": "ok"}),
		ev(at(45), map[string]any{"kind": "route", "class": "voice", "from": "udp", "to": "pool", "why": "red"}),
		ev(at(46), map[string]any{"kind": "member_down"}),
	); err != nil {
		t.Fatal(err)
	}
	c, err := store.BuildWireChart(ctx, "", "6h", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Buckets) != 360 || c.BucketSeconds != 60 || c.Buckets[len(c.Buckets)-1] != "2026-11-11T05:55:00Z" {
		t.Fatalf("buckets %d × %d, last %s", len(c.Buckets), c.BucketSeconds, c.Buckets[len(c.Buckets)-1])
	}
	// a minute of the call is a bucket of its own: the window steps by the minute
	idx := func(hhmm string) int {
		want := "2026-11-11T" + hhmm + ":00Z"
		for i, b := range c.Buckets {
			if b == want {
				return i
			}
		}
		t.Fatalf("no bucket %s", hhmm)
		return -1
	}
	get := func(group, key string) []*float64 {
		for _, l := range c.Lines {
			if l.Group == group && l.Key == key {
				return l.Values
			}
		}
		t.Fatalf("no line %s %s in %+v", group, key, c.Lines)
		return nil
	}
	for _, w := range []struct {
		group, key, at string
		want           float64
	}{
		{ChartRedMinutes, "copies2", "05:41", 1}, {ChartGaps60, "copies2", "05:41", 4},
		{ChartRTTP99, "copies2", "05:41", 80}, {ChartGapMax, "copies2", "05:41", 3092},
		{ChartLoss, "copies2", "05:41", 30 * 1000.0 / 3000}, {ChartSilence, "copies2", "05:41", 4},
		{ChartRedMinutes, "copies2", "05:42", 0}, {ChartGaps60, "copies2", "05:42", 1},
		{ChartRedMinutes, "trunk-udp", "05:41", 0},
		{ChartBulkMbps, "wire-udp", "05:41", 1048576 * 8 / 100.0 / 1000}, {ChartBulkFailed, "wire-udp", "05:41", 0},
		{ChartBulkMbps, "trunk-quic", "05:42", 1048576 * 8 / 50.0 / 1000}, {ChartBulkFailed, "trunk-quic", "05:43", 1},
		{ChartCPU, "wire-quic", "05:44", 3.6},
	} {
		v := get(w.group, w.key)
		i := idx(w.at)
		if v[i] == nil || *v[i]-w.want > 1e-9 || w.want-*v[i] > 1e-9 {
			t.Fatalf("%s %s at %s = %v, want %v", w.group, w.key, w.at, v[i], w.want)
		}
	}
	for _, k := range []struct{ group, key string }{
		{ChartGaps60, "copies2"}, {ChartBulkMbps, "wire-udp"}, {ChartCPU, "wire-quic"},
	} {
		if v := get(k.group, k.key)[idx("05:50")]; v != nil {
			t.Fatalf("%s %s: an empty bucket says %v", k.group, k.key, *v)
		}
	}
	if len(c.Marks) != 1 || c.Marks[0].Label != "голос: udp красный, на pool" || c.Marks[0].At != "2026-11-11T05:45:00Z" {
		t.Fatalf("marks %+v", c.Marks)
	}
	if _, err := store.BuildWireChart(ctx, "", "1y", now); err == nil {
		t.Fatal("a window it does not draw")
	}
}
