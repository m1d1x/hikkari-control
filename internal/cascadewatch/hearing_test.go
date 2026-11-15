package cascadewatch

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSetLines: a set's minute is a row of the call layer under
// <source>-<set>, its counts and the pool's lag as numbers; red when a
// listener with the selector's 60 ms buffer had holes of 1% or a run of 2,
// either way. A call's line keeps its old keys and gains the hearing's.
func TestSetLines(t *testing.T) {
	const hear = " up_frames=3000 up_holes40=9 up_runs2_40=1 up_bad_s40=0 up_holes60=3 up_runs2_60=0 up_bad_s60=0 up_holes100=1 up_runs2_100=0 up_bad_s100=0" +
		" down_frames=2990 down_holes40=4 down_runs2_40=0 down_bad_s40=0 down_holes60=2 down_runs2_60=0 down_bad_s60=0 down_holes100=0 down_runs2_100=0 down_bad_s100=0"
	clean, ok := classifySet(journalLine{Msg: "nekko-trunk-entry: call set=voice wires=udp,pool from=1794355200" + hear + " lag_down_n=2900 lag_down_p50=4.2 lag_down_p95=31.0 lag_down_max=410.0"}, "trunk")
	if !ok || clean.Layer != LayerCall || clean.Target != "trunk-voice" || clean.OK == nil || !*clean.OK || clean.TS.Unix() != 1794355200 {
		t.Fatalf("clean set %+v", clean)
	}
	if num(clean.Data, "up_holes60") != 3 || num(clean.Data, "down_frames") != 2990 || num(clean.Data, "lag_down_p95_ms") != 31 || num(clean.Data, "lag_down_n") != 2900 {
		t.Fatalf("clean set data %v", clean.Data)
	}
	if w, _ := clean.Data["wires"].([]string); strings.Join(w, ",") != "udp,pool" || has(clean.Data, "lost") {
		t.Fatalf("clean set wires %v", clean.Data)
	}

	run := strings.Replace(hear, "down_runs2_60=0", "down_runs2_60=1", 1)
	red, ok := classifySet(journalLine{Msg: "nekko-trunk-entry: call set=voice wires=udp,pool from=1794355260" + run}, "pipe")
	if !ok || red.Target != "pipe-voice" || red.OK == nil || *red.OK || !strings.Contains(red.Data["why"].(string), "вниз") {
		t.Fatalf("a run down %+v", red)
	}
	holes := strings.Replace(hear, "up_holes60=3", "up_holes60=30", 1)
	if r, _ := classifySet(journalLine{Msg: "nekko-trunk-entry: call set=voice wires=pool from=1794355320" + holes}, "trunk"); r.OK == nil || *r.OK {
		t.Fatalf("1%% holes up %+v", r)
	}
	if r, _ := classifySet(journalLine{Msg: "nekko-trunk-entry: call set=voice wires=- from=1794355380"}, "trunk"); r.OK != nil {
		t.Fatalf("nothing heard is not judged: %+v", r)
	}
	if _, ok := classifySet(journalLine{Msg: "nekko-trunk-entry: call wire=udp copies=1 from=1794355200 sent=3000 got=3000"}, "trunk"); ok {
		t.Fatal("a wire's call taken for a set's")
	}

	call, ok := classifyWire(journalLine{Msg: "nekko-trunk-entry: call wire=udp copies=1 from=1794355200 sent=3000 got=3000 rtt_p50=44.0 rtt_p99=51.0 rtt_max=60.0 gaps60=0 gaps150=0 gap_max=40 silent_ms=0 sessions=1 worst=-" + hear})
	if !ok || call.Target != "trunk-udp" || num(call.Data, "up_holes40") != 9 || num(call.Data, "gap_max_ms") != 40 {
		t.Fatalf("a wire's call with hearing %+v", call)
	}
}

// TestDBAHearingChartIsAMinuteABucket: a minute of a wire's call and of the
// voice set are each a point, holes a share of the direction's frames, runs
// and bad seconds as counted; view voice is the voice set alone, at all three
// buffers; an old call's minute without the hearing draws nothing.
func TestDBAHearingChartIsAMinuteABucket(t *testing.T) {
	ctx := context.Background()
	store := NewStore(dbPool(t))
	now := time.Date(2026, 11, 15, 5, 55, 30, 0, time.UTC)
	at := func(m int) time.Time { return time.Date(2026, 11, 15, 5, m, 0, 0, time.UTC) }
	row := func(ts time.Time, target string, data map[string]any) Run {
		return Run{TS: ts, Entry: "aezamsk1", Exit: "unl1", Layer: LayerCall, Vantage: "entry", Target: target, OK: boolp(true), Data: data}
	}
	heard := func(up, down float64) map[string]any {
		return map[string]any{"up_frames": 3000.0, "down_frames": 2000.0,
			"up_holes40": up * 2, "up_holes60": up, "up_holes100": up / 2, "up_runs2_60": 1.0, "up_bad_s60": 2.0,
			"down_holes40": down * 2, "down_holes60": down, "down_holes100": down / 2, "down_runs2_60": 0.0, "down_bad_s60": 0.0}
	}
	set := heard(30, 2)
	set["set"] = "voice"
	if err := store.Insert(ctx,
		row(at(50), "trunk-udp", heard(300, 20)),
		row(at(50), "trunk-voice", set),
		row(at(51), "copies1", map[string]any{"sent": 3000.0, "lost": 0.0}),
	); err != nil {
		t.Fatal(err)
	}
	c, err := store.BuildHearingChart(ctx, "aezamsk1", "6h", hearViewTrunk, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Buckets) != 360 || c.BucketSeconds != 60 || c.Buckets[359] != "2026-11-15T05:55:00Z" {
		t.Fatalf("buckets %d × %d, last %s", len(c.Buckets), c.BucketSeconds, c.Buckets[len(c.Buckets)-1])
	}
	i := len(c.Buckets) - 6 // 05:50
	get := func(c WireChart, group, key string) *float64 {
		for _, l := range c.Lines {
			if l.Group == group && l.Key == key {
				return l.Values[i]
			}
		}
		t.Fatalf("no line %s %s in %+v", group, key, c.Lines)
		return nil
	}
	for _, w := range []struct {
		group, key string
		want       float64
	}{
		{"holes60_up", "trunk-udp", 10}, {"holes60_down", "trunk-udp", 1}, {"runs2_60_up", "trunk-udp", 1}, {"bad_s60_up", "trunk-udp", 2},
		{"holes60_up", "trunk-voice", 1}, {"holes60_down", "trunk-voice", 0.1},
	} {
		if v := get(c, w.group, w.key); v == nil || *v-w.want > 1e-9 || w.want-*v > 1e-9 {
			t.Fatalf("%s %s = %v, want %v", w.group, w.key, v, w.want)
		}
	}
	for _, l := range c.Lines {
		if l.Key == "copies1" {
			t.Fatalf("a call without the hearing drew %+v", l)
		}
	}
	v, err := store.BuildHearingChart(ctx, "aezamsk1", "6h", hearViewVoice, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range v.Lines {
		if l.Key != "trunk-voice" {
			t.Fatalf("view voice drew %s", l.Key)
		}
	}
	if x := get(v, "holes40_up", "trunk-voice"); x == nil || *x != 2 {
		t.Fatalf("holes40 up %v", x)
	}
	if x := get(v, "holes100_down", "trunk-voice"); x == nil || *x != 0.05 {
		t.Fatalf("holes100 down %v", x)
	}
	if _, err := store.BuildHearingChart(ctx, "", "6h", "pool", now); err != errHearingView {
		t.Fatalf("view pool: %v", err)
	}
}

// TestHearingGroups: the trunk page draws holes, runs and bad seconds at
// 60 ms, the fleet's «Качество голоса» holes at all three buffers, both ways.
func TestHearingGroups(t *testing.T) {
	var trunk, voice []string
	for _, g := range hearingGroups(hearViewTrunk) {
		trunk = append(trunk, g[0]+":"+g[2])
	}
	for _, g := range hearingGroups(hearViewVoice) {
		voice = append(voice, g[0]+":"+g[2])
	}
	if got := strings.Join(trunk, " "); got != "holes60_up:share runs2_60_up:sum bad_s60_up:sum holes60_down:share runs2_60_down:sum bad_s60_down:sum" {
		t.Fatalf("trunk %s", got)
	}
	if got := strings.Join(voice, " "); got != "holes40_up:share holes60_up:share holes100_up:share holes40_down:share holes60_down:share holes100_down:share" {
		t.Fatalf("voice %s", got)
	}
}
