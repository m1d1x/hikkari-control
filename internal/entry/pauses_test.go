package entry

// pauses_test.go covers the wave fuse as a thing of its own (К2): that it is
// written down when it blows, that it holds the mechanic back until a person
// lifts it, and that lifting it actually hands the cause's incidents back.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// dropPauses takes a cause's pauses back out. The rails are fleet-wide on
// purpose, and so is a pause: a row left behind would read as a standing fuse
// in every later test of the same cause.
func (h *harness) dropPauses(cause string) {
	h.t.Helper()
	h.t.Cleanup(func() {
		if _, err := h.pool.Exec(context.Background(),
			`DELETE FROM entry_cause_pauses WHERE cause = $1`, cause); err != nil {
			h.t.Errorf("clean up pauses: %v", err)
		}
	})
}

// The fuse blowing is a row, not just a refusal. Before К2 it existed only as
// the mechanic's behaviour, so the console could show three look-alike
// incidents with no way to learn that the autopilot had stood down.
func TestTheWaveFuseWritesItselfDown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.dropPauses(CausePortsBanned)

	// The thresholds are a policy row now (К3), so the test asks the hub what
	// a wave is instead of repeating the number.
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}

	// Three machines, each with a finished repair of the same cause inside
	// the wave window: that is a wave by the rails' own definition.
	for i := 0; i < pol.WaveLimit; i++ {
		box, _ := h.box()
		other := h.openIncident(box, CausePortsBanned, nil)
		h.bookRepair(other.ID, box, CausePortsBanned, PlaybookRetirePorts, repairDone, time.Minute)
	}

	box, _ := h.box()
	banned := h.relay(box, KindPersonal)
	h.admit(banned)
	h.admit(h.relay(box, KindPersonal))
	inc := h.openIncident(box, CausePortsBanned, map[string]string{banned: "ip_banned"})

	why, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name)
	if err != nil {
		t.Fatalf("rails: %v", err)
	}
	if !blocked || why == "" {
		t.Fatalf("rails: blocked=%v why=%q, want blocked with a reason", blocked, why)
	}

	pause, paused, err := h.svc.CausePaused(ctx, CausePortsBanned)
	if err != nil {
		t.Fatalf("cause paused: %v", err)
	}
	if !paused {
		t.Fatal("the fuse blew and nothing was written down")
	}
	if pause.Boxes < pol.WaveLimit {
		t.Errorf("pause boxes = %d, want at least %d", pause.Boxes, pol.WaveLimit)
	}
	if pause.Reason == "" {
		t.Error("pause carries no reason, so the console has nothing to show")
	}
}

// A standing pause outranks a clean per-box history. This is the behaviour the
// table was added for: the wave used to be counted over a rolling hour, so the
// autopilot resumed by itself with nobody having looked.
func TestAStandingPauseBlocksACauseWithNoHistoryOfItsOwn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.dropPauses(CauseFingerprint)

	box, _ := h.box()
	inc := h.openIncident(box, CauseFingerprint, nil)
	if err := h.svc.PauseCause(ctx, CauseFingerprint, wavePrefix+": тест", 4); err != nil {
		t.Fatalf("pause cause: %v", err)
	}

	why, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name)
	if err != nil {
		t.Fatalf("rails: %v", err)
	}
	if !blocked {
		t.Fatal("a paused cause was allowed to start a repair")
	}
	if why == "" {
		t.Error("blocked silently: the incident would wait with nothing said")
	}
}

// Pausing twice is the same fuse. The mechanic reaches it on every pass while
// the wave holds, and the console must not show it twice.
func TestPausingACauseTwiceIsOneFuse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.dropPauses(CauseDestination)

	for i := 0; i < 3; i++ {
		if err := h.svc.PauseCause(ctx, CauseDestination, "тест", i+1); err != nil {
			t.Fatalf("pause cause: %v", err)
		}
	}
	pauses, err := h.svc.Pauses(ctx)
	if err != nil {
		t.Fatalf("pauses: %v", err)
	}
	n := 0
	for _, p := range pauses {
		if p.Cause == CauseDestination {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d standing pauses on one cause, want 1", n)
	}
}

// Lifting the pause hands the cause's incidents back. A button that cleared
// the row and left the handoffs standing would change nothing a person can
// see: the mechanic skips anything handed over.
func TestLiftingAPauseFreesTheIncidentsTheFuseHandedOver(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.dropPauses(CauseDomainBlocked)

	box, _ := h.box()
	wave := h.openIncident(box, CauseDomainBlocked, nil)
	if err := h.svc.HandOff(ctx, wave.ID, wavePrefix+": за час причина сломала 4 машины"); err != nil {
		t.Fatalf("hand off: %v", err)
	}
	// An incident handed over for a reason of its own: its repairs failed
	// three times. Lifting the wave must not send the hub back at it.
	ownBox, _ := h.box()
	own := h.openIncident(ownBox, CauseDomainBlocked, nil)
	if err := h.svc.HandOff(ctx, own.ID, "ремонт пробовали 3 раза и не помогло"); err != nil {
		t.Fatalf("hand off: %v", err)
	}
	if err := h.svc.PauseCause(ctx, CauseDomainBlocked, wavePrefix+": тест", 4); err != nil {
		t.Fatalf("pause cause: %v", err)
	}

	freed, err := h.svc.LiftPause(ctx, CauseDomainBlocked, "admin-1")
	if err != nil {
		t.Fatalf("lift pause: %v", err)
	}
	if freed != 1 {
		t.Errorf("freed %d incidents, want 1 (the fuse's own)", freed)
	}

	back, err := h.svc.Incident(ctx, wave.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if back.Handoff != "" {
		t.Errorf("the fuse's incident still carries a handoff %q", back.Handoff)
	}
	kept, err := h.svc.Incident(ctx, own.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if kept.Handoff == "" {
		t.Error("an incident handed over on its own merits was freed with the wave")
	}
	if _, paused, err := h.svc.CausePaused(ctx, CauseDomainBlocked); err != nil || paused {
		t.Errorf("pause still standing after a lift (err %v)", err)
	}
}

// Lifting what is not paused says so, rather than reporting success over
// nothing.
func TestLiftingAPauseThatIsNotThereIsRefused(t *testing.T) {
	h := newHarness(t)
	h.dropPauses(CauseIPBanned)
	if _, err := h.svc.LiftPause(context.Background(), CauseIPBanned, "admin-1"); !errors.Is(err, ErrNoSuchPause) {
		t.Fatalf("err = %v, want ErrNoSuchPause", err)
	}
}
