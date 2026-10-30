package entry

import (
	"context"
	"testing"
	"time"
)

// The hoster charges whole hours from creation: what is left of the current
// one, whatever hour it is.
func TestPaidHourLeft(t *testing.T) {
	created := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want time.Duration
	}{
		{created, time.Hour},
		{created.Add(50 * time.Minute), 10 * time.Minute},
		{created.Add(3*time.Hour + 59*time.Minute), time.Minute},
		{created.Add(-time.Minute), time.Hour}, // a clock behind the row
	} {
		if got := paidHourLeft(created, c.at); got != c.want {
			t.Errorf("%s in: %s left, want %s", c.at.Sub(created), got, c.want)
		}
	}
}

// The fewest people to move: the least loaded; of equals (and with no
// dataset, all equal), the oldest — candidates come oldest first. Never more
// than are asked for, nor than there are.
func TestPickLeastLoaded(t *testing.T) {
	cands := []handbackCandidate{{name: "old", mbps: 30}, {name: "mid", mbps: 5}, {name: "new", mbps: 5}}
	if got := pickLeastLoaded(cands, 1); len(got) != 1 || got[0].name != "mid" {
		t.Errorf("least loaded = %v, want mid", got)
	}
	if got := pickLeastLoaded(cands, 2); len(got) != 2 || got[0].name != "mid" || got[1].name != "new" {
		t.Errorf("two least loaded = %v, want mid and new", got)
	}
	if got := pickLeastLoaded(cands, 5); len(got) != 3 {
		t.Errorf("five of three = %v", got)
	}
	if got := pickLeastLoaded([]handbackCandidate{{name: "old"}, {name: "new"}}, 1); got[0].name != "old" {
		t.Errorf("no dataset = %s, want the oldest", got[0].name)
	}
}

// A leaving machine whose pool needs it again goes back to the pool: its
// relays alive, the mark off, nothing bought and nothing handed back.
func TestALeavingMachineIsTakenBackWhenThePoolNeedsIt(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.inPool()
	box := h.inPool()
	h.target(1)
	if _, _, err := h.svc.drainForHandback(ctx, box); err != nil {
		t.Fatal(err)
	}
	if !h.leaving(box) {
		t.Fatal("drainForHandback did not mark the machine leaving")
	}
	// It still counts: the fleet does not read it as a machine short.
	f, err := h.svc.poolForecast(ctx, mustPolicy(t, h.svc), h.key)
	if err != nil {
		t.Fatal(err)
	}
	if f.Live != 2 || f.Leaving != 1 || f.Need > 0 {
		t.Fatalf("pool with one leaving: %+v", f)
	}

	h.target(3) // load rose: three machines needed, two would not do
	if _, err := h.prov.handBackSurplus(ctx); err != nil {
		t.Fatal(err)
	}
	if h.leaving(box) || h.retired(box) {
		t.Fatalf("leaving %v retired %v, want taken back", h.leaving(box), h.retired(box))
	}
	var alive int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_relays WHERE box = $1 AND status = 'alive'`, box).Scan(&alive); err != nil || alive == 0 {
		t.Fatalf("relays of the machine taken back are not alive (%d, %v)", alive, err)
	}
}

// A stream machine is handed back when nobody is left on its relay ports, or
// when the drain deadline passed with somebody still there — and not before.
func TestALeavingStreamMachineWaitsForItsPeopleThenTheDeadline(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	keep := h.inService()
	box := h.inService()
	t.Cleanup(func() { h.retire(keep) })
	// Reality keeps one: every other live machine of the shared pool is over.
	h.svc.primeSizing(map[string]PoolSizing{FleetPool(ProtoVlessRealityVision, ""): sizingFor(0)})
	if _, _, err := h.svc.drainForHandback(ctx, box); err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t, h.svc)

	h.box.conns = []int{3}
	if n, err := h.prov.boxConnections(ctx, box); err != nil || n != 3 {
		t.Fatalf("connections on the box = %d, %v; want 3", n, err)
	}
	if gone, err := h.prov.finishHandbacks(ctx, policy); err != nil || contains(gone, box) {
		t.Fatalf("handed back with three people on it (gone %v, %v)", gone, err)
	}
	if h.retired(box) {
		t.Fatal("a machine with people on it and time left was handed back")
	}

	if _, err := h.pool.Exec(ctx, `UPDATE entry_boxes SET handback_since = now() - $2::interval WHERE name = $1`,
		box, (handbackDrainFor + time.Minute).String()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.prov.finishHandbacks(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if !h.retired(box) {
		t.Fatal("past the drain deadline the machine was kept")
	}
}

// A machine nobody is left on goes at once.
func TestALeavingStreamMachineGoesWhenEmpty(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	keep := h.inService()
	box := h.inService()
	t.Cleanup(func() { h.retire(keep) })
	h.svc.primeSizing(map[string]PoolSizing{FleetPool(ProtoVlessRealityVision, ""): sizingFor(0)})
	if _, _, err := h.svc.drainForHandback(ctx, box); err != nil {
		t.Fatal(err)
	}
	h.box.conns = []int{0}
	if _, err := h.prov.finishHandbacks(ctx, mustPolicy(t, h.svc)); err != nil {
		t.Fatal(err)
	}
	if !h.retired(box) {
		t.Fatal("an empty leaving machine was kept")
	}
}

func mustPolicy(t *testing.T, s *Service) DetectionPolicy {
	t.Helper()
	p, err := s.DetectionPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
