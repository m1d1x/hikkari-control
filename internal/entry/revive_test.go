package entry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The chain П9.4 was written for: the grid names a cause the probe could not,
// and that cause now has a free repair the hub may run by itself. Before it,
// every one of these three read as a banned address, whose only repair is a
// new machine.
func TestTheGridsCausesAllHaveAFreeRepair(t *testing.T) {
	for _, tc := range []struct{ cause, playbook string }{
		{CauseEdgeDown, PlaybookRestartEdge},
		{CauseProviderDown, PlaybookRebootBox},
		{CauseDestination, PlaybookRotateDest},
	} {
		p := PlaybookForCause(tc.cause)
		if p.Name != tc.playbook {
			t.Errorf("%s → %q, want %q", tc.cause, p.Name, tc.playbook)
		}
		if p.Spends {
			t.Errorf("%s: the repair is marked as spending money", tc.cause)
		}
		if !p.MayRunUnattended() {
			t.Errorf("%s: a free repair is not on auto", tc.cause)
		}
	}
}

// A banned address costs a machine still, but the purchase is no longer the
// repair of the box (П9.11): the box is evicted for free and at once, and the
// one cause that buys a machine is the fleet's shortage that eviction leaves.
func TestABannedAddressIsEvictedAndOnlyTheFleetBuys(t *testing.T) {
	p := PlaybookForCause(CauseIPBanned)
	if p.Name != PlaybookEvictBox || p.Spends || p.Mode != ModeAuto {
		t.Fatalf("ip_banned is %q %q/spends=%v, want evict_box on auto, free", p.Name, p.Mode, p.Spends)
	}
	var buyers []string
	for _, o := range Playbooks() {
		if o.Spends && o.Name != PlaybookBuyDomain {
			buyers = append(buyers, o.Name)
		}
	}
	if len(buyers) != 1 || buyers[0] != PlaybookScaleUp {
		t.Fatalf("playbooks that buy a machine = %v, want only scale_up", buyers)
	}
}

// The end of the chain, at value level: the probe says "banned", the grid says
// "the unit is stopped", and what comes out is the repair that touches nothing
// but the process.
func TestAStoppedUnitEndsInARestartAndNotANewMachine(t *testing.T) {
	d := RefineCause(probeCause("ams1"), &GridSnapshot{
		SSH: GridOK, Units: map[string]bool{"ams1-1": false},
	})
	if got := PlaybookForCause(d.Cause).Name; got != PlaybookRestartEdge {
		t.Fatalf("playbook = %q, want restart_edge", got)
	}
}

// stoppedUnits reads the grid's own lines and nothing else: a relay's verdict
// in the same map must never be mistaken for a unit state, which is what the
// "unit:" prefix is for.
func TestStoppedUnitsReadsOnlyTheGridsUnitLines(t *testing.T) {
	got := stoppedUnits(map[string]string{
		"unit:ams1-2": "inactive",
		"unit:ams1-1": "inactive",
		"unit:ams1-3": "active",
		"ams1-1":      "ip_banned",
		"hub:ams1-1":  "unreachable",
		"ssh":         "ok",
	})
	if strings.Join(got, ",") != "ams1-1,ams1-2" {
		t.Fatalf("stopped = %v, want the two inactive units sorted", got)
	}
}

// An incident whose evidence holds no stopped unit is not what this playbook
// is for, and the repair says so instead of restarting the box on a guess.
func TestRestartEdgeRefusesAnIncidentWithNoStoppedUnit(t *testing.T) {
	m := NewMechanic(nil, nil)
	_, err := m.restartEdge(context.Background(), Incident{
		Box: "ams1", Cause: CauseEdgeDown,
		Evidence: map[string]string{"ssh": GridOK, "unit:ams1-1": "active"},
	})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "остановленного юнита") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The rail is checked before the hands: a hub with no provisioner still knows
// whether the incident was one it could have repaired, and the two refusals
// must not be confused in the console.
func TestRestartEdgeNamesTheMissingProvisionerSeparately(t *testing.T) {
	m := NewMechanic(nil, nil)
	_, err := m.restartEdge(context.Background(), Incident{
		Box: "ams1", Cause: CauseEdgeDown,
		Evidence: map[string]string{"ssh": GridOK, "unit:ams1-1": "inactive"},
	})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "провизионера") {
		t.Errorf("a stopped unit got the wrong refusal: %v", err)
	}
}

// What systemd said after the restart, parsed. A unit the box did not report
// on is absent, which reads as not active — the safe direction: the hub must
// not call a unit repaired on no answer.
func TestRestartedStatesReadsWhatTheBoxSaid(t *testing.T) {
	states := restartedStates("ams1-1=active\nams1-2=failed\n ams1-3=activating \n\n")
	if !states["ams1-1"] {
		t.Error("an active unit read as down")
	}
	if states["ams1-2"] || states["ams1-3"] {
		t.Error("a unit that is not active read as up")
	}
	if _, reported := states["ams1-4"]; reported {
		t.Error("a unit nobody reported on appeared in the states")
	}
}

// The cover list narrows to what answered, keeping race order.
func TestNarrowCoversKeepsRaceOrder(t *testing.T) {
	keep, lost := narrowCovers(
		[]string{"www.samsung.com", "www.nvidia.com", "www.tesla.com"},
		map[string]bool{"www.samsung.com": true, "www.nvidia.com": false, "www.tesla.com": true},
	)
	if strings.Join(keep, ",") != "www.samsung.com,www.tesla.com" {
		t.Fatalf("keep = %v, want the live two in their original order", keep)
	}
	if strings.Join(lost, ",") != "www.nvidia.com" {
		t.Fatalf("lost = %v, want the dead one", lost)
	}
}

// A cover the job never managed to dial is kept, not dropped. Same rule as the
// grid's: "we did not ask" is not "it is dead", and stripping a relay of a
// cover because the hub's own network blinked would be the autopilot breaking
// a working relay.
func TestNarrowCoversKeepsACoverItCouldNotAsk(t *testing.T) {
	keep, lost := narrowCovers(
		[]string{"www.samsung.com", "www.nvidia.com"},
		map[string]bool{"www.samsung.com": false},
	)
	if strings.Join(keep, ",") != "www.nvidia.com" {
		t.Fatalf("keep = %v, want the unasked cover kept", keep)
	}
	if len(lost) != 1 {
		t.Fatalf("lost = %v, want only the one that answered dead", lost)
	}
}

// Every cover dead is not a repair this playbook can make: a Reality relay
// without a cover answers nobody, so narrowing to nothing must be refused
// rather than written. Widening the pool is a person's decision about which
// sites to trust, and the hub does not get to invent one.
func TestARelayMayNotBeLeftWithoutACover(t *testing.T) {
	keep, lost := narrowCovers(
		[]string{"www.samsung.com", "www.nvidia.com"},
		map[string]bool{"www.samsung.com": false, "www.nvidia.com": false},
	)
	if len(keep) != 0 || len(lost) != 2 {
		t.Fatalf("keep = %v, lost = %v, want everything lost", keep, lost)
	}
	// And the identity itself refuses the empty set, so no path through the
	// job can write a relay that cannot answer.
	id := Identity{PrivateKey: "k", PublicKey: "p", ShortID: "01", UUID: "u",
		Covers: []string{"www.samsung.com"}, Port: 443, LocalPortBase: 10001}
	if _, err := id.withCovers(nil); !errors.Is(err, ErrRejected) {
		t.Fatalf("withCovers(nil) = %v, want a refusal", err)
	}
}

// Changing covers is not a re-key: a dead cover site is somebody else's
// outage, and the relay's secrets were never shown to anyone because of it.
// Rotating them too would cost every holder its record for nothing.
func TestWithCoversKeepsTheRelaysSecrets(t *testing.T) {
	id := Identity{PrivateKey: "k", PublicKey: "p", ShortID: "01", UUID: "u",
		Covers: []string{"www.samsung.com", "www.nvidia.com"}, Port: 443, LocalPortBase: 10001}
	got, err := id.withCovers([]string{"www.nvidia.com"})
	if err != nil {
		t.Fatalf("withCovers: %v", err)
	}
	if got.PrivateKey != id.PrivateKey || got.ShortID != id.ShortID || got.UUID != id.UUID {
		t.Error("the cover swap rotated credentials it had no business touching")
	}
	if got.Port != id.Port || got.LocalPortBase != id.LocalPortBase {
		t.Error("the cover swap moved the relay's ports")
	}
	if strings.Join(id.Covers, ",") != "www.samsung.com,www.nvidia.com" {
		t.Error("the original identity was mutated")
	}
}

// A voice or XHTTP relay has no Reality half and no covers, and a repair that
// silently did nothing to one would report success.
func TestWithCoversRefusesARelayWithoutCovers(t *testing.T) {
	voice := Identity{Port: 443, LocalPortBase: 10001,
		Hysteria: &HysteriaIdentity{Auth: "a"}}
	if _, err := voice.withCovers([]string{"www.nvidia.com"}); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want a refusal for a voice relay", err)
	}
}

// Every playbook the dispatcher can name now has a body: a name that reaches
// perform's default is a table entry somebody added without one, and the hub
// must say so rather than report a repair that never ran.
func TestEveryNamedPlaybookHasAnImplementation(t *testing.T) {
	h := newHarness(t)
	// A name no box has, deliberately: this test asks the dispatcher whether
	// every playbook it names has a body, and it must not add a machine to
	// the pool to do it — a box left behind changes what the rollout tests
	// walk over.
	const box = "nosuchbox"
	// No provisioner: every repair that touches a machine then refuses for
	// want of hands, which is a different refusal from "no such repair" —
	// and telling those two apart is exactly what is under test.
	m := NewMechanic(h.svc, nil)
	for cause, p := range playbookFor {
		if p.Name == "" {
			continue
		}
		_, err := m.perform(context.Background(), Incident{Box: box, Cause: cause}, p.Name)
		if err != nil && strings.Contains(err.Error(), "ещё не реализован") {
			t.Errorf("%s names %q, which has no body", cause, p.Name)
		}
	}
}
