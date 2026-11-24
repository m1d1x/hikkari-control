package cascadewatch

import (
	"context"
	"testing"
	"time"
)

// The people's call is traced across the points at the pauses a call hears
// (over 150 ms), not only at the ones that break it (500 ms).
func TestDBAPauseACallHearsIsTracedAcrossThePoints(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	now := time.Now().UTC().Truncate(time.Minute)
	w, _, _, _ := newTestWatch(t, pool, &fakeBoxes{}, &now)
	pair := w.fleet.(fakeFleet).pair
	call := func(at time.Time, gapMs int) Run {
		return Run{TS: at, Entry: pair.Entry, Exit: pair.Exit, Layer: LayerCall, Vantage: "entry", Target: "copies1", OK: boolp(gapMs <= 150),
			Data: map[string]any{"gap_max_ms": gapMs, "worst": []any{map[string]any{"at": at.Add(20 * time.Second).Format(time.RFC3339Nano), "ms": gapMs}}}}
	}
	quiet, heard := now.Add(-3*time.Minute), now.Add(-2*time.Minute)
	if err := w.store.Insert(ctx, call(quiet, 120), call(heard, 210)); err != nil {
		t.Fatal(err)
	}
	got, err := w.store.pendingHops(ctx, pair, now.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].minute.Equal(heard) || got[0].gap != 210*time.Millisecond {
		t.Fatalf("incidents %+v, want the 210 ms pause alone", got)
	}
}
