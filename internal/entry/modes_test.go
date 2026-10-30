package entry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The owner's mode for a cause overrides the code's default (П9.8):
// short_of_domains defaults to `auto` since П9.19 — whatever fits under the
// caps the hub does by itself — and the owner's `ask` holds it for a person,
// until the owner moves it back. Found on the stand 25.10 the other way round:
// a mode that could only change by a rebuild.
func TestTheOwnersModeOverridesTheDefault(t *testing.T) {
	h := newHarness(t)
	h.svc.SetDomainRegistrar(newFakeRegistrar(), DefaultDomainPolicy())
	ctx := context.Background()
	if PlaybookForCause(CauseShortOfDomains).Mode != ModeAuto {
		t.Fatal("the default for short_of_domains is no longer auto; this test needs another cause")
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = h.pool.Exec(ctx, `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseShortOfDomains)
		_, _ = h.pool.Exec(ctx, `DELETE FROM entry_incidents WHERE pool = $1`, DomainPool)
	})
	inc, err := h.svc.openPool(ctx, DomainPool, CauseShortOfDomains, "тест", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetPlaybookMode(ctx, CauseShortOfDomains, ModeAsk, "test"); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	if started, _ := NewMechanic(h.svc, nil).consider(ctx, *inc); started {
		t.Fatal("a cause the owner moved to `ask` was started by the pass")
	}
	if err := h.svc.SetPlaybookMode(ctx, CauseShortOfDomains, ModeAuto, "test"); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	p, err := h.svc.Playbook(ctx, CauseShortOfDomains)
	if err != nil {
		t.Fatalf("playbook: %v", err)
	}
	if p.Name != PlaybookBuyDomain || !p.MayRunUnattended() || !p.Spends {
		t.Fatalf("playbook = %+v, want buy_domain on auto, still marked as spending", p)
	}
	if started, _ := NewMechanic(h.svc, nil).consider(ctx, *inc); !started {
		t.Fatal("the pass did not start a cause the owner moved back to auto")
	}
}

// A mode is a word from three, and only for a cause the hub can repair.
func TestAModeIsRefusedForNothingToRepairAndForNonsense(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.SetPlaybookMode(ctx, CauseIPBanned, "sometimes", "test"); !errors.Is(err, ErrRejected) {
		t.Errorf("mode \"sometimes\": err = %v, want ErrRejected", err)
	}
	if err := h.svc.SetPlaybookMode(ctx, CauseUnknown, ModeAuto, "test"); !errors.Is(err, ErrRejected) {
		t.Errorf("mode for a cause with no repair: err = %v, want ErrRejected", err)
	}
}

// provider_down climbs to evict_box when a reboot cannot help (plan §3,
// П9.11): out of the pool, replaced by the fleet, handed back only when its
// second chance fails. Found on the stand 25.10: the owner deleted the
// hand-rented ams1 at the hoster, reboot_box was refused for it, and the hub
// stood with an empty pool.
func TestAMachineThatCannotBeRebootedIsEvicted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseIPBanned)
	})
	box, _ := h.box() // rented by hand: no instance id at the hoster
	inc := h.openIncident(box, CauseProviderDown, map[string]string{"ssh": "fail"})
	m := NewMechanic(h.svc, nil)

	p, err := m.playbook(ctx, inc)
	if err != nil {
		t.Fatalf("playbook: %v", err)
	}
	if p.Name != PlaybookEvictBox || !p.MayRunUnattended() || p.Spends {
		t.Fatalf("playbook = %+v, want evict_box on auto by default, free", p)
	}
	// The climb takes the mode the owner set for evict_box's own cause.
	if err := h.svc.SetPlaybookMode(ctx, CauseIPBanned, ModeAsk, "test"); err != nil {
		t.Fatal(err)
	}
	if p, _ = m.playbook(ctx, inc); p.Name != PlaybookEvictBox || p.MayRunUnattended() {
		t.Fatalf("playbook = %+v, want evict_box on ask after the owner's override", p)
	}
}

// A machine the hub rented is rebooted first, and evicted only once a reboot
// on this incident did not bring it back.
func TestAHubMachineIsRebootedBeforeItIsEvicted(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	name, job, err := h.prov.OrderBox(ctx, OrderSpec{}, "")
	if err != nil {
		t.Fatal(err)
	}
	h.made = append(h.made, name+"-1")
	h.vantage("clean")
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("provision %+v", j)
	}
	h.t.Cleanup(func() { h.retire(name) })
	inc := h.openIncident(name, CauseProviderDown, map[string]string{"ssh": "fail"})
	m := NewMechanic(h.svc, h.prov)

	if p, _ := m.playbook(ctx, inc); p.Name != PlaybookRebootBox {
		t.Fatalf("first rung = %q, want reboot_box", p.Name)
	}
	h.bookRepair(inc.ID, name, inc.Cause, PlaybookRebootBox, repairDone, time.Minute)
	if p, _ := m.playbook(ctx, inc); p.Name != PlaybookEvictBox {
		t.Fatalf("after a reboot that left the incident standing = %q, want evict_box", p.Name)
	}
}
