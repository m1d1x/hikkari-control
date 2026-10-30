package entry

// timeline_test.go covers the two things К2 added to an incident's own record:
// the moment its replacement entered the pool, and an operator choosing a
// repair other than the proposed one.

import (
	"context"
	"errors"
	"testing"
)

// The admission is a later moment than the closing, and the detector is what
// writes it: a repair closes its incident optimistically, so until a probe
// round agrees there is no evidence the replacement carries traffic.
func TestAHealthyBoxAdmitsTheReplacementOfARecentlyClosedIncident(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	live := h.relay(box, KindPersonal)
	h.admit(live)

	inc := h.openIncident(box, CausePortsBanned, map[string]string{live: "ip_banned"})
	if err := h.svc.Close(ctx, inc.ID, IncidentResolved, "сняли порты", 0); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if closed.AdmittedAt != nil {
		t.Fatal("closing an incident admitted the replacement by itself")
	}

	// The probes now read clean, which is the same evidence a person would
	// accept that the pool is whole again.
	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{live: "clean"}})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}

	after, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if after.AdmittedAt == nil {
		t.Fatal("a healthy box left the closed incident with no admission")
	}
	if after.ClosedAt == nil || after.AdmittedAt.Before(*after.ClosedAt) {
		t.Errorf("admitted %v is before closed %v", after.AdmittedAt, after.ClosedAt)
	}
}

// A replacement's incident stays on the machine that was handed back, which
// has no relays left and never reads healthy again. Its admission is the new
// machine reading healthy — found through `replaced_by` — and nothing else:
// another healthy box in the fleet says nothing about this repair.
func TestAReplacementIsAdmittedByTheNewMachineReadingHealthy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old, _ := h.box()
	gone := h.relay(old, KindPersonal)
	h.admit(gone)
	fresh, _ := h.box()
	live := h.relay(fresh, KindPersonal)
	h.admit(live)
	bystander, _ := h.box()
	other := h.relay(bystander, KindPersonal)
	h.admit(other)

	inc := h.openIncident(old, CauseIPBanned, map[string]string{gone: "ip_banned"})
	if err := h.svc.closeIncident(ctx, inc.ID, IncidentResolved, "машина заменена", 500, fresh); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := h.svc.Retire(ctx, old, DeathIPBanned); err != nil {
		t.Fatalf("retire the old machine: %v", err)
	}

	// Only a bystander reads healthy: not this repair's proof.
	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{other: "clean"}})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}
	mid, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if mid.ReplacedBy != fresh {
		t.Errorf("replacedBy = %q, want %q", mid.ReplacedBy, fresh)
	}
	if mid.AdmittedAt != nil {
		t.Fatal("a box that is not the replacement admitted the repair")
	}

	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{live: "clean", other: "clean"}})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}
	after, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if after.AdmittedAt == nil {
		t.Fatal("the new machine read healthy and the replacement was not admitted")
	}
}

// An incident still standing is not admitted by a healthy round. It would be
// the same claim read twice: the detector closes it in the same pass, and the
// closing is what the admission attaches to.
func TestAnOpenIncidentIsNotAdmitted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	live := h.relay(box, KindPersonal)
	h.admit(live)
	inc := h.openIncident(box, CausePortsBanned, map[string]string{live: "ip_banned"})

	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{live: "clean"}})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}
	after, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	// The detector closed it as resolved in this very pass, and the admission
	// may ride along with that — what must not happen is an admission while it
	// is still open.
	if after.Status == IncidentOpen && after.AdmittedAt != nil {
		t.Error("an open incident was admitted")
	}
}

// "Another action": the operator overrules the dispatcher, and the record says
// `manual` so П9.8 can read the disagreement.
func TestChoosingAnotherPlaybookRecordsItAsManual(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	banned := h.relay(box, KindPersonal)
	h.admit(banned)
	h.admit(h.relay(box, KindPersonal))
	// The dispatcher proposes retire_ports for this cause. The person asks
	// for the cheapest repair there is instead — restarting the unit.
	inc := h.openIncident(box, CausePortsBanned, map[string]string{banned: "ip_banned"})

	if err := NewMechanic(h.svc, nil).Start(ctx, inc.ID, "admin-1", PlaybookRestartEdge); err != nil {
		t.Fatalf("start: %v", err)
	}
	repairs, err := h.svc.Repairs(ctx, inc.ID)
	if err != nil {
		t.Fatalf("repairs: %v", err)
	}
	if len(repairs) != 1 {
		t.Fatalf("repairs = %d, want 1", len(repairs))
	}
	if repairs[0].Playbook != PlaybookRestartEdge {
		t.Errorf("playbook = %q, want %q", repairs[0].Playbook, PlaybookRestartEdge)
	}
	if repairs[0].DecidedBy != DecidedManual {
		t.Errorf("decidedBy = %q, want manual", repairs[0].DecidedBy)
	}
}

// A repair the hub has no code for is refused by name. The console offers a
// menu; the hub does not execute what it was handed.
func TestChoosingARepairTheHubDoesNotKnowIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})

	err := NewMechanic(h.svc, nil).Start(ctx, inc.ID, "admin-1", "rm -rf")
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

// Every playbook the console can offer is one the mechanic has code for. The
// menu and the switch in perform() drifting apart would mean a button that
// books an attempt and then refuses it.
func TestEveryOfferedPlaybookIsOneTheHubCanPerform(t *testing.T) {
	known := map[string]bool{
		PlaybookEvictDomain:   true,
		PlaybookRotateReality: true,
		PlaybookEvictBox:      true,
		PlaybookRetirePorts:   true,
		PlaybookRebootBox:     true,
		PlaybookRestartEdge:   true,
		PlaybookRotateDest:    true,
		PlaybookAddPorts:      true,
		PlaybookAddCovers:     true,
		PlaybookScaleUp:       true,
		PlaybookBuyDomain:     true,
	}
	opts := Playbooks()
	if len(opts) == 0 {
		t.Fatal("the console would show an empty menu")
	}
	for _, o := range opts {
		if !known[o.Name] {
			t.Errorf("playbook %q is offered and perform() has no case for it", o.Name)
		}
		if o.Cause == "" {
			t.Errorf("playbook %q is offered without the cause it answers", o.Name)
		}
	}
}
