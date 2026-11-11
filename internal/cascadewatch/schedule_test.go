package cascadewatch

import (
	"context"
	"testing"
	"time"
)

// stealWindows are where in ten minutes the entry's neighbours steal its
// CPU (:x0 and :x5, opyt 1.1), with room on each side.
var stealWindows = [][2]time.Duration{
	{-30 * time.Second, 45 * time.Second},
	{4*time.Minute + 30*time.Second, 5*time.Minute + 45*time.Second},
}

func inSteal(at time.Duration) bool {
	for _, w := range stealWindows {
		if at >= w[0] && at <= w[1] || at-Every >= w[0] && at-Every <= w[1] {
			return true
		}
	}
	return false
}

// TestRoundIsOffTheSteal: the round's heavy part (its first minute: vps,
// path and the chunks) is in neither steal window.
func TestRoundIsOffTheSteal(t *testing.T) {
	for at := RoundAt; at <= RoundAt+time.Minute; at += 5 * time.Second {
		if inSteal(at) {
			t.Fatalf("the round runs at %s in its ten minutes, in a steal window", at)
		}
	}
}

// TestRoundWaitsForRoundAt: a round due at :x0 starts at RoundAt, with the
// round's own time for its name.
func TestRoundWaitsForRoundAt(t *testing.T) {
	round := time.Date(2026, 11, 12, 18, 40, 0, 0, time.UTC)
	now := round.Add(3 * time.Second)
	w := &Watch{now: func() time.Time { return now }}
	var waited time.Duration
	w.sleep = func(_ context.Context, d time.Duration) bool { waited = d; return false }
	if w.waitRoundAt(context.Background(), round) || waited != RoundAt-3*time.Second {
		t.Fatalf("waited %s", waited)
	}
	// Late (a hub up past RoundAt): at once.
	now = round.Add(2 * time.Minute)
	waited = 0
	if !w.waitRoundAt(context.Background(), round) || waited != 0 {
		t.Fatalf("a late round waited %s", waited)
	}
}
