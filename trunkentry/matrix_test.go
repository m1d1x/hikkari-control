package main

import (
	"strings"
	"testing"
	"time"
)

func TestTheMatrixReadsItsTargets(t *testing.T) {
	ts, err := parseMatrix("rx:unl1=10.0.0.1/20, rx:ams3=10.0.0.2/20,ru:ru-eurodir=10.0.0.3/20,eu:eu-fra=217.243.20.18/100")
	if err != nil || len(ts) != 4 || ts[3].Kind != "eu" || ts[3].Every != 100*time.Millisecond || ts[1].Name != "ams3" {
		t.Fatalf("%+v %v", ts, err)
	}
	for _, bad := range []string{"xx:a=10.0.0.1/20", "rx:a=10.0.0.1", "rx:a=::1/20", "rx:a=10.0.0.1/5", "rx:a=10.0.0.1/20,rx:a=10.0.0.2/20"} {
		if _, err := parseMatrix(bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
	if ts, err := parseMatrix(""); err != nil || len(ts) != 0 {
		t.Fatalf("no matrix: %v %v", ts, err)
	}
}

// series answers every `every` from `from` to `to`, but not in the holes.
func series(name, kind string, every time.Duration, from, to time.Time, holes ...[2]time.Time) *matrixSeries {
	s := &matrixSeries{t: matrixTarget{Name: name, Kind: kind, Every: every}}
next:
	for at := from; at.Before(to); at = at.Add(every) {
		s.sent = append(s.sent, at)
		for _, h := range holes {
			if !at.Before(h[0]) && at.Before(h[1]) {
				continue next
			}
		}
		s.answers = append(s.answers, at.Add(40*time.Millisecond))
		s.rtts = append(s.rtts, 40*time.Millisecond)
	}
	return s
}

// 19.11 12:10:01: every receiver silent 5.3 s. What the matrix would have
// said by who else was heard in those seconds.
func TestTheMatrixSaysWhoseSilenceItWas(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 12, 9, 0, 0, time.UTC)
	t1 := t0.Add(2 * time.Minute)
	hole := [2]time.Time{t0.Add(61421 * time.Millisecond), t0.Add(66743 * time.Millisecond)}
	const ms20, ms100 = 20 * time.Millisecond, 100 * time.Millisecond
	cases := []struct {
		name    string
		series  []*matrixSeries
		boxLate time.Duration
		want    string
	}{
		{"everything silent, Russia too", []*matrixSeries{
			series("unl1", "rx", ms20, t0, t1, hole), series("ams3", "rx", ms20, t0, t1, hole),
			series("ru-eurodir", "ru", ms20, t0, t1, hole), series("eu-fra", "eu", ms100, t0, t1, hole)}, 0, "uplink"},
		{"Russia heard, nothing abroad", []*matrixSeries{
			series("unl1", "rx", ms20, t0, t1, hole), series("ams3", "rx", ms20, t0, t1, hole),
			series("ru-eurodir", "ru", ms20, t0, t1), series("eu-fra", "eu", ms100, t0, t1, hole)}, 0, "border"},
		{"the neutral EU heard, our receivers not", []*matrixSeries{
			series("unl1", "rx", ms20, t0, t1, hole), series("ams3", "rx", ms20, t0, t1, hole),
			series("ru-eurodir", "ru", ms20, t0, t1), series("eu-fra", "eu", ms100, t0, t1)}, 0, "receivers"},
		{"one receiver", []*matrixSeries{
			series("unl1", "rx", ms20, t0, t1, hole), series("ams3", "rx", ms20, t0, t1),
			series("ru-eurodir", "ru", ms20, t0, t1), series("eu-fra", "eu", ms100, t0, t1)}, 0, "receiver:unl1"},
		{"the entry's process did not run", []*matrixSeries{
			series("unl1", "rx", ms20, t0, t1, hole), series("ams3", "rx", ms20, t0, t1, hole),
			series("ru-eurodir", "ru", ms20, t0, t1, hole), series("eu-fra", "eu", ms100, t0, t1, hole)}, 4 * time.Second, "box"},
		{"Russia flapping long before: no word on it", []*matrixSeries{
			series("unl1", "rx", ms20, t0, t1, hole), series("ams3", "rx", ms20, t0, t1, hole),
			series("ru-eurodir", "ru", ms20, t0, t1, [2]time.Time{t0.Add(30 * time.Second), t1}), series("eu-fra", "eu", ms100, t0, t1, hole)}, 0, "unknown"},
	}
	for _, c := range cases {
		sil := c.series[0].silences(t0, t1)
		if len(sil) != 1 || sil[0][1].Sub(sil[0][0]) < 5300*time.Millisecond {
			t.Fatalf("%s: silences %v", c.name, sil)
		}
		v := judgeSilence(c.series, sil[0][0], sil[0][1], c.boxLate)
		if v.Verdict != c.want {
			t.Errorf("%s: %s, want %s", c.name, v.line(), c.want)
		}
	}
}

func TestTheMatrixMinuteLineReadsAsTheCallsLine(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 12, 10, 0, 0, time.UTC)
	s := series("ams3", "rx", 20*time.Millisecond, t0, t0.Add(time.Minute), [2]time.Time{t0.Add(20 * time.Second), t0.Add(20300 * time.Millisecond)})
	line := s.window(t0, t0.Add(time.Minute))
	for _, want := range []string{"matrix target=ams3 kind=rx every_ms=20 from=1795090200 sent=3000 got=2983", "gaps150=1", "gap_max=320", "rtt_p50=40.0"} {
		if !strings.Contains(line, want) {
			t.Errorf("%s: no %q", line, want)
		}
	}
	eu := series("eu-fra", "eu", 100*time.Millisecond, t0, t0.Add(time.Minute), [2]time.Time{t0.Add(20 * time.Second), t0.Add(20100 * time.Millisecond)})
	if line := eu.window(t0, t0.Add(time.Minute)); !strings.Contains(line, "gaps150=0") {
		t.Errorf("one ping lost at 100 ms counted as a word: %s", line)
	}
	box := boxMinute(t0, []boxSecond{{StealPct: 1}, {StealPct: 6, RxDrop: 2}}, []time.Duration{50 * time.Millisecond, 250 * time.Millisecond})
	if box != "matrix box from=1795090200 late_max_ms=250 late_over100=1 late_over500=0 steal_max_pct=6.0 steal_avg_pct=3.50 rx_drop=2 tx_drop=0" {
		t.Errorf("box %s", box)
	}
}
