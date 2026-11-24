package cascadewatch

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// A core behind border points answers only its points (H6): the path layer
// must measure the hops the trunk dials, not the closed core.
func TestThePathGoesToTheBorderPointsAndNotToTheCoreBehindThem(t *testing.T) {
	p := entry.TrunkPair{Entry: "e", Exit: "core", ExitAddress: "203.0.113.9", RelayPort: 8443,
		Points:     []string{"pa", "pb"},
		PointNames: map[string]string{"198.51.100.1": "pa", "198.51.100.2": "pb", "198.51.100.3": "stand-only"}}
	got := pathTargets(p)
	want := []pathTarget{{"pa", "198.51.100.1:8443"}, {"pb", "198.51.100.2:8443"}}
	if !slices.Equal(got, want) {
		t.Fatalf("targets %v, want %v", got, want)
	}

	p.Points = nil
	if got := pathTargets(p); !slices.Equal(got, []pathTarget{{"exit", "203.0.113.9:8443"}}) {
		t.Fatalf("without points %v", got)
	}
}

func TestARedPathToAPointNamesThePoint(t *testing.T) {
	v := judgeRound([]Run{
		{Layer: LayerPath, Vantage: "entry", Target: "pa", OK: boolp(false), Data: map[string]any{"why": "TCP 0/12"}},
	}, nil, nil)
	if v.FirstRed != LayerPath || !slices.Equal(v.Red[LayerPath], []string{"вход → точка pa: TCP 0/12"}) {
		t.Fatalf("verdict %+v", v)
	}
}

// Members stalling behind border points is the pool's everyday healing: it is
// an alert only when the people's call heard it in the same minutes.
func TestDBStallsAreAnAlertOnlyUnderARedPeoplesCall(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	now := time.Now().UTC().Truncate(time.Minute)
	w, _, _, errs := newTestWatch(t, pool, &fakeBoxes{}, &now)
	pair := w.fleet.(fakeFleet).pair
	entryMinute := func(at time.Time, stalled int) Run {
		return Run{TS: at, Entry: pair.Entry, Exit: pair.Exit, Layer: LayerEntry, Vantage: "entry",
			Data: map[string]any{"members": 32, "joined": 100 + stalled, "stalled": stalled}}
	}
	call := func(at time.Time, ok bool) Run {
		return Run{TS: at, Entry: pair.Entry, Exit: pair.Exit, Layer: LayerCall, Vantage: "entry", Target: "copies1", OK: boolp(ok),
			Data: map[string]any{"sent": 3000, "got": 3000}}
	}
	t0 := now.Add(-stallWindow)
	if err := w.store.Insert(ctx, entryMinute(t0, 0), entryMinute(now, 12), call(t0.Add(time.Minute), true), call(now.Add(-time.Minute), true)); err != nil {
		t.Fatal(err)
	}
	w.alertEntry(ctx, pair, nil)
	if n := errs.codes()["cascade.stalls"]; n != 0 {
		t.Fatalf("12 stalls under a clean call alerted %d times", n)
	}
	if err := w.store.Insert(ctx, call(now.Add(-2*time.Minute), false)); err != nil {
		t.Fatal(err)
	}
	w.alertEntry(ctx, pair, nil)
	if n := errs.codes()["cascade.stalls"]; n != 1 {
		t.Fatalf("stalls under a red call alerted %d times", n)
	}
}
