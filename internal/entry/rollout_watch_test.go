package entry

import (
	"strings"
	"testing"
	"time"
)

func TestJudgeSwap(t *testing.T) {
	cases := []struct {
		name          string
		before, after trafficSample
		fail          string
	}{
		{"quiet box stays quiet", trafficSample{conns: 0}, trafficSample{conns: 0}, ""},
		{"probing background is no failure", trafficSample{conns: 40, rejectsMin: 3}, trafficSample{conns: 38, rejectsMin: 15}, ""},
		// euw1 on 09.11: zero refusals before, ~200 a minute in the drain.
		{"refusals jump", trafficSample{conns: 40, rejectsMin: 0}, trafficSample{conns: 40, rejectsMin: 200}, "отказов"},
		{"a noisy box may stay noisy", trafficSample{conns: 40, rejectsMin: 30}, trafficSample{conns: 40, rejectsMin: 80}, ""},
		{"a noisy box may not triple", trafficSample{conns: 40, rejectsMin: 30}, trafficSample{conns: 40, rejectsMin: 100}, "отказов"},
		{"connections never came back", trafficSample{conns: 40}, trafficSample{conns: 5}, "соединений"},
		{"few connections are not judged", trafficSample{conns: 4}, trafficSample{conns: 0}, ""},
		{"uncounted connections are not judged", trafficSample{conns: 40}, trafficSample{conns: -1}, ""},
	}
	for _, c := range cases {
		got := judgeSwap(c.before, c.after)
		if (c.fail == "") != (got == "") || (c.fail != "" && !strings.Contains(got, c.fail)) {
			t.Errorf("%s: %q, want %q", c.name, got, c.fail)
		}
	}
}

func TestEveningPeakIsMoscowEvening(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	for h, want := range map[int]bool{9: false, 17: false, 18: true, 21: true, 23: true, 0: false, 3: false} {
		if got := eveningPeak(time.Date(2026, 11, 9, h, 30, 0, 0, msk)); got != want {
			t.Errorf("%02d:30 MSK peak=%v, want %v", h, got, want)
		}
	}
}
