package entry

import (
	"context"
	"testing"
	"time"
)

// fakeGrid answers with a fixed snapshot and counts how often it was asked:
// the escalated look costs an ssh session, so how often it runs is part of
// what is under test.
type fakeGrid struct {
	snap  GridSnapshot
	calls int
}

func (f *fakeGrid) Inspect(context.Context, string, []PoolRelay) GridSnapshot {
	f.calls++
	return f.snap
}

// probeCause is the diagnosis the RU round alone produces: a box whose every
// relay reads banned.
func probeCause(box string) Diagnosis {
	return Diagnosis{
		Box: box, Cause: CauseIPBanned, Advice: "сменить ip",
		Judged: 1, Alive: 1,
		Verdicts: map[string]string{box + "-1": "ip_banned"},
	}
}

// Without the grid nothing changes. The escalation adds precision; it is
// never what makes a diagnosis possible.
func TestRefiningWithoutAGridChangesNothing(t *testing.T) {
	d := probeCause("ams1")
	if got := RefineCause(d, nil); got.Cause != CauseIPBanned {
		t.Fatalf("cause = %q, want it untouched", got.Cause)
	}
}

// The cheapest repair there is, and the one most easily mistaken for a banned
// address: the machine answers the hub's key and the relay is simply not
// running.
func TestAStoppedUnitOnALiveMachineIsEdgeDown(t *testing.T) {
	d := probeCause("ams1")
	g := GridSnapshot{SSH: GridOK, Units: map[string]bool{"ams1-1": false}}

	got := RefineCause(d, &g)
	if got.Cause != CauseEdgeDown {
		t.Fatalf("cause = %q, want edge_down", got.Cause)
	}
	if got.Verdicts["unit:ams1-1"] != "inactive" {
		t.Errorf("the evidence does not carry what was seen: %v", got.Verdicts)
	}
	if got.Verdicts["ams1-1"] != "ip_banned" {
		t.Errorf("the grid overwrote the probe's own verdict: %v", got.Verdicts)
	}
}

// Neither ssh nor the hub's own handshake: the machine is gone, and this is
// not the censor's doing. The distinction is the whole point — one is cured
// by waiting and a reboot, the other by a new machine.
func TestNoSSHAndNoReachIsTheProviderNotTheCensor(t *testing.T) {
	d := probeCause("ams1")
	g := GridSnapshot{SSH: GridFail, Reach: map[string]bool{"ams1-1": false}}

	if got := RefineCause(d, &g); got.Cause != CauseProviderDown {
		t.Fatalf("cause = %q, want provider_down", got.Cause)
	}
}

// But ssh alone is not enough: a box that refuses the hub's login while
// answering its handshake has a firewall or an sshd problem, not a dead
// machine — and the probe's own cause is still the better reading.
func TestNoSSHWhileTheHubIsStillAnsweredKeepsTheProbeCause(t *testing.T) {
	d := probeCause("ams1")
	g := GridSnapshot{SSH: GridFail, Reach: map[string]bool{"ams1-1": true}}

	if got := RefineCause(d, &g); got.Cause != CauseIPBanned {
		t.Fatalf("cause = %q, want the probe's ip_banned to stand", got.Cause)
	}
}

// The cover site is dead: the machine, the address and the protocol are all
// fine, and the handshake has nothing to hide behind.
func TestADeadCoverIsADestinationProblem(t *testing.T) {
	d := probeCause("ams1")
	g := GridSnapshot{
		SSH:   GridOK,
		Units: map[string]bool{"ams1-1": true},
		Reach: map[string]bool{"ams1-1": true},
		Dests: map[string]bool{"www.samsung.com": false, "www.nvidia.com": true},
	}

	got := RefineCause(d, &g)
	if got.Cause != CauseDestination {
		t.Fatalf("cause = %q, want destination", got.Cause)
	}
	if got.Verdicts["dest:www.samsung.com"] != "dead" {
		t.Errorf("the evidence does not name the dead cover: %v", got.Verdicts)
	}
}

// A dead process outranks a dead cover: the repair that touches least wins,
// and restarting a unit is cheaper than moving every client's handshake.
func TestAStoppedUnitOutranksADeadCover(t *testing.T) {
	d := probeCause("ams1")
	g := GridSnapshot{
		SSH:   GridOK,
		Units: map[string]bool{"ams1-1": false},
		Dests: map[string]bool{"www.samsung.com": false},
	}

	if got := RefineCause(d, &g); got.Cause != CauseEdgeDown {
		t.Fatalf("cause = %q, want edge_down first", got.Cause)
	}
}

// The grid finding nothing wrong is itself the finding: a box that answers
// the hub and not Russia IS a banned address.
func TestAHealthyGridLeavesTheCensorsVerdictStanding(t *testing.T) {
	d := probeCause("ams1")
	g := GridSnapshot{
		SSH:   GridOK,
		Units: map[string]bool{"ams1-1": true},
		Reach: map[string]bool{"ams1-1": true},
		Dests: map[string]bool{"www.samsung.com": true},
	}

	got := RefineCause(d, &g)
	if got.Cause != CauseIPBanned {
		t.Fatalf("cause = %q, want ip_banned", got.Cause)
	}
	if got.Verdicts["hub:ams1-1"] != "reached" || got.Verdicts["ssh"] != GridOK {
		t.Errorf("the evidence does not show the box answering the hub: %v", got.Verdicts)
	}
}

// An empty map is not a finding. A voice box, whose UDP port answers nobody,
// is left unjudged rather than called unreachable — and a grid that read
// nothing must not condemn it.
func TestAnUnreadGridSignalIsNotAFinding(t *testing.T) {
	d := probeCause("ams1")
	g := GridSnapshot{SSH: GridFail, Reach: map[string]bool{}, Units: map[string]bool{}}

	if got := RefineCause(d, &g); got.Cause != CauseIPBanned {
		t.Fatalf("cause = %q: an unread signal was treated as a failure", got.Cause)
	}
}

// The detector escalates a box that read badly, and the incident it opens
// carries the refined cause — not the probe's first guess.
func TestTheDetectorEscalatesABadBoxAndOpensTheRefinedCause(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)

	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{relay: "ip_banned"}})
	grid := &fakeGrid{snap: GridSnapshot{SSH: GridOK, Units: map[string]bool{relay: false}}}
	h.svc.SetInspector(grid)
	t.Cleanup(func() {
		if _, err := h.pool.Exec(context.Background(),
			`DELETE FROM entry_incidents WHERE box = $1`, box); err != nil {
			h.t.Errorf("clean up incidents: %v", err)
		}
	})

	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}
	if grid.calls != 1 {
		t.Fatalf("the grid ran %d times for one bad box, want 1", grid.calls)
	}
	inc := h.incidentOn(box)
	if inc.Cause != CauseEdgeDown {
		t.Fatalf("cause = %q, want edge_down", inc.Cause)
	}
	if inc.Evidence["ssh"] != GridOK {
		t.Errorf("the incident does not carry the grid: %v", inc.Evidence)
	}
}

// And it does not escalate again every minute. The look costs an ssh session,
// and a cause already refined does not change because a minute passed — what
// changes it is a repair or a healthy round.
func TestTheDetectorLooksCloselyOncePerIncident(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)

	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{relay: "ip_banned"}})
	grid := &fakeGrid{snap: GridSnapshot{SSH: GridOK, Units: map[string]bool{relay: false}}}
	h.svc.SetInspector(grid)
	t.Cleanup(func() {
		if _, err := h.pool.Exec(context.Background(),
			`DELETE FROM entry_incidents WHERE box = $1`, box); err != nil {
			h.t.Errorf("clean up incidents: %v", err)
		}
	})

	for i := 0; i < 3; i++ {
		if _, _, err := h.svc.Detect(ctx); err != nil {
			t.Fatalf("detect %d: %v", i, err)
		}
	}
	if grid.calls != 1 {
		t.Fatalf("the grid ran %d times over three rounds, want 1", grid.calls)
	}
	// And the incident the grid named is still standing: the probe-only
	// rounds that followed cannot un-say a cause they cannot see.
	if inc := h.incidentOn(box); inc.Cause != CauseEdgeDown || inc.Status != IncidentOpen {
		t.Fatalf("incident is %q/%q, want an open edge_down", inc.Cause, inc.Status)
	}
}

// And the probe-only rounds that follow must not open a second incident on
// the same box under their own shallower cause. One failure, one incident:
// two of them would put the mechanic to work on the cheap reading — a domain
// swap that spends money — while the repair the grid actually earned waits.
func TestAProbeOnlyRoundDoesNotOpenASecondIncident(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)

	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{relay: "ip_banned"}})
	h.svc.SetInspector(&fakeGrid{snap: GridSnapshot{SSH: GridOK, Units: map[string]bool{relay: false}}})
	t.Cleanup(func() {
		if _, err := h.pool.Exec(context.Background(),
			`DELETE FROM entry_incidents WHERE box = $1`, box); err != nil {
			h.t.Errorf("clean up incidents: %v", err)
		}
	})

	for i := 0; i < 3; i++ {
		if _, _, err := h.svc.Detect(ctx); err != nil {
			t.Fatalf("detect %d: %v", i, err)
		}
	}

	var n int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM entry_incidents WHERE box = $1`, box).Scan(&n); err != nil {
		t.Fatalf("count incidents: %v", err)
	}
	if n != 1 {
		t.Fatalf("%d incidents on one box over three rounds, want 1", n)
	}
}

// A box that comes back healthy closes its incident, grid cause or not: that
// clause is what keeps the feed honest, and the grid must not have made it
// unreachable.
func TestAHealthyRoundStillClosesAGridCause(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)

	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{relay: "ip_banned"}})
	h.svc.SetInspector(&fakeGrid{snap: GridSnapshot{SSH: GridOK, Units: map[string]bool{relay: false}}})
	t.Cleanup(func() {
		if _, err := h.pool.Exec(context.Background(),
			`DELETE FROM entry_incidents WHERE box = $1`, box); err != nil {
			h.t.Errorf("clean up incidents: %v", err)
		}
	})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}

	// The relay answers again.
	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{relay: "clean"}})
	if _, closed, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect again: %v", err)
	} else if closed != 1 {
		t.Fatalf("closed %d incidents, want 1", closed)
	}
	if inc := h.incidentOn(box); inc.Status != IncidentResolved {
		t.Fatalf("status = %q, want resolved", inc.Status)
	}
}

// Evidence keys are prefixed, so a grid line can never be read as a relay's
// verdict in the same map — the console shows both side by side.
func TestGridEvidenceIsPrefixed(t *testing.T) {
	g := GridSnapshot{
		SSH:   GridOK,
		Units: map[string]bool{"ams1-1": true},
		Reach: map[string]bool{"ams1-1": false},
		Dests: map[string]bool{"www.tesla.com": true},
		At:    time.Now(),
		Err:   "посмотрели не всё",
	}
	ev := g.Evidence()
	for _, key := range []string{"ssh", "unit:ams1-1", "hub:ams1-1", "dest:www.tesla.com", "grid"} {
		if ev[key] == "" {
			t.Errorf("evidence has no %q: %v", key, ev)
		}
	}
	if _, bare := ev["ams1-1"]; bare {
		t.Error("a grid line landed under a relay's own name")
	}
}

// incidentOn is the standing or latest incident of a box, for the tests that
// check what the detector decided about one.
func (h *harness) incidentOn(box string) Incident {
	h.t.Helper()
	incidents, err := h.svc.Incidents(context.Background(), 200)
	if err != nil {
		h.t.Fatalf("incidents: %v", err)
	}
	for _, inc := range incidents {
		if inc.Box == box {
			return inc
		}
	}
	h.t.Fatalf("no incident on %s", box)
	return Incident{}
}
