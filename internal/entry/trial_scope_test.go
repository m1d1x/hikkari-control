package entry

import (
	"context"
	"testing"
	"time"
)

func TestTrialHandbackPreservesBaselineEvenWhenLeaving(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	baseline := h.inPool()
	h.inPool() // A second baseline machine still counts toward surplus, but is not selected.
	owned1, owned2 := h.inPool(), h.inPool()
	h.target(0)
	// An existing drain is outside the trial, too: neither finish nor take-back may touch it.
	if _, _, err := h.svc.drainForHandback(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	h.svc.now = func() time.Time { return time.Now().Add(50 * time.Minute) }
	for i := 0; i < 2; i++ {
		if _, err := TrialHandBackForTest(h.prov, ctx, []string{owned1, owned2}); err != nil {
			t.Fatal(err)
		}
	}
	if h.retired(baseline) || !h.leaving(baseline) {
		t.Fatal("baseline drain was changed")
	}
	for _, name := range []string{owned1, owned2} {
		if !h.retired(name) {
			t.Fatalf("owned box %s was not handed back", name)
		}
	}
	if _, err := TrialHandBackForTest(h.prov, ctx, nil); err == nil {
		t.Fatal("empty scope accepted")
	}
}

func TestTrialReleaseDoesNotReconcileUnrelatedRetiredBox(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	baseline, owned := h.inPool(), h.inPool()
	for _, name := range []string{baseline, owned} {
		if _, _, err := h.svc.Retire(ctx, name, DeathRotated); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := TrialReleaseForTest(h.svc, ctx, []string{owned}); err != nil {
		t.Fatal(err)
	}
	var baselineReleased, ownedReleased bool
	if err := h.pool.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM entry_boxes WHERE name=$1`, baseline).Scan(&baselineReleased); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM entry_boxes WHERE name=$1`, owned).Scan(&ownedReleased); err != nil {
		t.Fatal(err)
	}
	if baselineReleased || !ownedReleased {
		t.Fatalf("baseline released %v, owned released %v", baselineReleased, ownedReleased)
	}
}
