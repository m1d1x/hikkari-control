package entry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeVantages is the grid, as small or as large as a test needs it.
type fakeVantages struct {
	regions []string
	err     error
}

func (f fakeVantages) Vantages(context.Context, time.Time, time.Time) ([]string, error) {
	return f.regions, f.err
}

// The thresholds were Go constants until К3. The point of moving them was that
// the rails read the row, so a knob turned in the console changes what the
// autopilot does without a rebuild — which is only true if the mechanic asks
// the row rather than remembering a number.
func TestLoweringTheStrikeCountHandsAnIncidentOverSooner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})

	before, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	t.Cleanup(func() {
		strikes := before.FailStrikes
		if _, err := h.svc.SaveDetectionPolicy(context.Background(),
			DetectionPolicyPatch{FailStrikes: &strikes}, "test-restore"); err != nil {
			t.Errorf("restore policy: %v", err)
		}
	})

	// One failed attempt, outside the wave window so the fleet-wide rail
	// cannot be what blocks.
	h.bookRepair(inc.ID, box, inc.Cause, PlaybookRetirePorts, repairFailed, before.WaveWindow+time.Hour)

	// With the shipped three strikes, one failure is bad luck: the mechanic
	// tries again.
	if _, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name); err != nil {
		t.Fatalf("rails: %v", err)
	} else if blocked {
		t.Fatalf("one failure out of %d strikes already blocked the repair", before.FailStrikes)
	}

	one := 1
	if _, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{FailStrikes: &one}, "test"); err != nil {
		t.Fatalf("save policy: %v", err)
	}

	why, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name)
	if err != nil {
		t.Fatalf("rails after save: %v", err)
	}
	if !blocked || why == "" {
		t.Fatalf("one strike did not hand the incident over (blocked=%v, why=%q)", blocked, why)
	}
}

// A rule the hub cannot satisfy must be refused, not stored. With one vantage,
// "confirm a ban from two observation points" would confirm nothing and
// therefore evict nothing — the screen would read as a tightening and act as a
// full stop.
func TestTwoVantageConfirmationIsRefusedWhileTheGridHasOneVantage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.svc.SetVantages(fakeVantages{regions: []string{"msk"}})

	on := true
	_, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{RequireTwoVantages: &on}, "test")
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}

	p, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	if p.RequireTwoVantages {
		t.Fatal("the refused rule was stored anyway")
	}

	// With a second observation point reporting, the same request is a real
	// tightening and goes through.
	// Registered slots: only those count (Service.Vantages).
	h.svc.SetVantages(fakeVantages{regions: []string{h.openSlot("msk"), h.openSlot("spb")}})
	p, err = h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{RequireTwoVantages: &on}, "test")
	if err != nil {
		t.Fatalf("save with two vantages: %v", err)
	}
	if !p.RequireTwoVantages {
		t.Fatal("two vantages reported and the rule still did not take")
	}
	off := false
	if _, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{RequireTwoVantages: &off}, "test"); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

// Our own network trouble is not evidence about a machine. The switch exists
// so the screen can show the rule; its off position would retire healthy
// machines during an outage of ours, so the hub refuses it.
func TestTheVantageOutageRuleCannotBeTurnedOff(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	off := false
	_, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{IgnoreNetworkDown: &off}, "test")
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	p, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	if !p.IgnoreNetworkDown {
		t.Fatal("the locked invariant was cleared")
	}
}

// A threshold outside the bounds the migration sets is the operator's answer,
// not an internal fault: the console shows the refusal under the knob.
func TestAThresholdOutsideItsBoundsIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	absurd := 500
	if _, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{FailStrikes: &absurd}, "test"); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

// A patch carries only the knobs that moved: saving one must not reset
// another, or the screen's second stepper would undo its first.
func TestAPatchLeavesTheKnobsItDoesNotName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	before, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	t.Cleanup(func() {
		limit, window := before.WaveLimit, before.WaveWindowSeconds()
		if _, err := h.svc.SaveDetectionPolicy(context.Background(),
			DetectionPolicyPatch{WaveLimit: &limit, WaveWindowSeconds: &window}, "test-restore"); err != nil {
			t.Errorf("restore policy: %v", err)
		}
	})

	limit := before.WaveLimit + 1
	after, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{WaveLimit: &limit}, "test")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if after.WaveLimit != limit {
		t.Fatalf("wave limit = %d, want %d", after.WaveLimit, limit)
	}
	if after.WaveWindow != before.WaveWindow {
		t.Errorf("the wave window moved from %v to %v on a patch that never named it",
			before.WaveWindow, after.WaveWindow)
	}
	if after.FailStrikes != before.FailStrikes {
		t.Errorf("the strike count moved from %d to %d on a patch that never named it",
			before.FailStrikes, after.FailStrikes)
	}
	if after.UpdatedBy != "test" {
		t.Errorf("updatedBy = %q, want the actor who saved", after.UpdatedBy)
	}
}
