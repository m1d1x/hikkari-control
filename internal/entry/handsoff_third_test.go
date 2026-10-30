package entry

// handsoff_third_test.go is the third pass of audit 42
// (AUDIT/42-autopilot-third.md): a deadline that is not the machine's own,
// the layers of the operator's-hands predicate no test reached, and a sentence
// passed on a machine while it was in the operator's hands. Each test here was
// red on b3e66214 or killed a mutant that survived it (§4: F, G, H).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// НС-3-1: the repair's deadline (20 min) is shorter than what a fresh machine
// may take — an idle one that never came up eats bootWait first — and it used
// to cut the deaf vantage's probe short of its own five minutes. The probe
// returned the bare context error, which is not errNoVerdict, so the machine
// nobody had judged was retired, deleted, and the attempt was a strike: what
// the owner's decision 27.10 (§8.3) ruled out. A deadline that is not the
// machine's own is no evidence against it.
func TestARepairDeadlineIsNoEvidenceAgainstAFreshMachine(t *testing.T) {
	h := newHostHarness(t)
	h.prov.probeTimeout = 4 * time.Second
	h.vantage(VerdictProbeFailed)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, _ := h.prov.obtainMachines(ctx, OrderSpec{Proto: ProtoVlessRealityVision}, 1)
	err := got[0].err
	box := h.lastOrdered()
	h.t.Cleanup(func() { h.retire(box) })
	if h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("a deadline handed back the machine nobody judged: err=%v retired=%v deleted=%v",
			err, h.retired(box), h.host.deletedIDs())
	}
	if !errors.Is(err, errNoVerdict) {
		t.Fatalf("the cut attempt ended %v, want no evidence", err)
	}
}

// The same cut earlier: the machine has not let the hub in yet when the
// repair's deadline ends the wait, long before its own bootWait. Booting
// slower than a repair's remainder says nothing about the machine.
func TestARepairDeadlineDuringTheBootWaitKeepsTheMachine(t *testing.T) {
	h := newHostHarness(t)
	h.box.keyIn = false // sshd not up yet: the hub's key is refused
	h.prov.bootWait = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	got, _ := h.prov.obtainMachines(ctx, OrderSpec{Proto: ProtoVlessRealityVision}, 1)
	err := got[0].err
	box := h.lastOrdered()
	h.t.Cleanup(func() { h.retire(box) })
	if h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("a deadline handed back a machine still booting: err=%v retired=%v deleted=%v",
			err, h.retired(box), h.host.deletedIDs())
	}
	if !errors.Is(err, errNoVerdict) {
		t.Fatalf("the cut attempt ended %v, want no evidence", err)
	}

	// The machine's own bootWait is evidence, and still hands it back.
	h.prov.bootWait = 50 * time.Millisecond
	if err := h.prov.standUp(context.Background(), box, "test"); err == nil || errors.Is(err, errNoVerdict) {
		t.Fatalf("a machine past its own bootWait ended %v, want a failure", err)
	}
}

// The whole repair: a scale_up whose deadline cuts the deaf probe is `held`,
// not a strike, and its machine stays rented.
func TestARepairCutByItsDeadlineIsHeld(t *testing.T) {
	h := newHostHarness(t)
	h.prov.probeTimeout = 4 * time.Second
	inc := h.shortOfFleet()
	m := NewMechanic(h.svc, h.prov)
	m.timeout = 2 * time.Second

	h.vantage(VerdictProbeFailed)
	if err := m.Start(context.Background(), inc.ID, "", PlaybookScaleUp); err != nil {
		t.Fatalf("start: %v", err)
	}
	status := h.awaitRepair(inc.ID)
	box := h.lastOrdered()
	h.t.Cleanup(func() { h.retire(box) })
	if h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("the repair's deadline handed back its machine: retired=%v deleted=%v",
			h.retired(box), h.host.deletedIDs())
	}
	if status != repairHeld {
		t.Fatalf("the attempt ended %q, want %q — a deadline is not a failure", status, repairHeld)
	}
}

// jobsOf counts the jobs of kind a box has had.
func (h *hostHarness) jobsOf(box, kind string) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM entry_jobs WHERE box = $1 AND kind = $2`, box, kind).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// НН-3-3, layer H: the write-off's own query leaves the operator's machines
// out. A dead suspect whose key was forgotten is not even taken into a retire
// job — the job's recheck (layer F) is the second wall, not the first. With
// both gone it was deleted at the hoster; with H alone gone, a retire job
// was opened on a machine being reinstalled.
func TestADeadSuspectInTheOperatorsHandsIsNotTaken(t *testing.T) {
	h := newHostHarness(t)
	h.isolateWave()
	ctx := context.Background()
	box := h.deadSuspect()
	if err := h.svc.ForgetHostKey(ctx, box); err != nil {
		t.Fatal(err)
	}

	gone, err := h.prov.retireDeadSuspects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if contains(gone, box) || h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("handed back the dead suspect the operator is reinstalling: gone=%v deleted=%v", gone, h.host.deletedIDs())
	}
	if n := h.jobsOf(box, JobRetire); n != 0 {
		t.Fatalf("the write-off opened %d retire job(s) on a machine in the operator's hands", n)
	}
}

// НН-3-3, layer F: a key forgotten between a pass listing a machine and its
// job retiring it. The job reads the predicate again; without that read, the
// pass that listed the machine a moment before the operator's step-up deleted
// it in the middle of the reinstall.
func TestTheHandbackJobRereadsTheOperatorsHands(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box, instance := h.idleMachine()
	// The pass has listed it; now the operator forgets its key.
	if err := h.svc.ForgetHostKey(ctx, box); err != nil {
		t.Fatal(err)
	}

	err := h.prov.execJob(ctx, JobRetire, box, uuid.Nil, func(j *jobRun) error {
		return j.autoHandBack(DeathOther)
	})
	if !errors.Is(err, errOperatorsMachine) {
		t.Fatalf("the handback job ended %v, want %v", err, errOperatorsMachine)
	}
	if h.retired(box) || contains(h.host.deletedIDs(), instance) {
		t.Fatal("the handback job retired a machine whose key the operator forgot after the listing")
	}
}

// НН-3-3, layer G: a fresh machine that did not make it is handed back by
// handBackFresh, outside any pass's listing. Its key forgotten while the
// deploy ran, it is the operator's.
func TestAFreshMachineInTheOperatorsHandsIsNotHandedBack(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box, instance := h.idleMachine()
	if err := h.svc.ForgetHostKey(ctx, box); err != nil {
		t.Fatal(err)
	}

	err := h.prov.handBackFresh(ctx, box, errors.New("разворот не удался"))
	if err == nil || !strings.Contains(err.Error(), "не сдавали") {
		t.Fatalf("handBackFresh said %v, want the machine kept", err)
	}
	if h.retired(box) || contains(h.host.deletedIDs(), instance) {
		t.Fatal("handBackFresh retired a machine whose key the operator forgot")
	}
}

// НН-3-1, owner's decision 27.10: a sentence passed on a machine while it
// was in the operator's hands is not carried out once the operator lets go.
// The operator forgets the key and reinstalls; the relays are unreachable
// meanwhile, the vantage reads ip_banned, the machine is evicted and its
// second chance dies. The write-off waits while the key is forgotten — and
// used to delete the machine on the first pass after the operator's job took
// the new key. The operator's job meeting the reinstalled machine withdraws
// the open sentence: its relays are admitted again on the deploy's own fresh
// verdict, and a new second chance starts only on a new eviction.
func TestTheOperatorsReinstallWithdrawsAnOldSentence(t *testing.T) {
	h := newHostHarness(t)
	h.isolateWave()
	ctx := context.Background()
	box := h.inService()
	if err := h.svc.ForgetHostKey(ctx, box); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.SuspectBox(ctx, box, CauseIPBanned); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_suspects SET status = 'dead', ended_at = now(), outcome = 'test'
		WHERE kind = 'box' AND subject = $1 AND status = 'suspect'`, box); err != nil {
		t.Fatal(err)
	}
	if gone, err := h.prov.retireDeadSuspects(ctx); err != nil || contains(gone, box) {
		t.Fatalf("the write-off took a machine whose key is forgotten: %v (%v)", gone, err)
	}

	// The operator's job meets the reinstalled machine.
	h.vantage(VerdictClean)
	job, err := h.prov.Provision(ctx, box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("the operator's deploy: %+v\n%s", j, h.logText(job))
	}

	gone, err := h.prov.retireDeadSuspects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if contains(gone, box) || h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("the sentence passed during the reinstall was carried out after it: gone=%v deleted=%v",
			gone, h.host.deletedIDs())
	}
	if relays := h.relaysOn(box); len(relays) == 0 {
		t.Fatal("the reinstalled machine has no relay left")
	}
	for _, r := range h.relaysOn(box) {
		if st := h.relayStatus(r); st != StatusAlive {
			t.Fatalf("relay %s is %s after a clean deploy, want alive", r, st)
		}
	}
	var open, withdrawn int
	if err := h.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status IN ('suspect', 'dead')),
		       count(*) FILTER (WHERE status = $2)
		FROM entry_suspects WHERE kind = 'box' AND subject = $1`, box, suspectWithdrawn).Scan(&open, &withdrawn); err != nil {
		t.Fatal(err)
	}
	if open != 0 || withdrawn != 1 {
		t.Fatalf("the machine's ledger after the reinstall: %d open or dead, %d withdrawn; want 0 and 1", open, withdrawn)
	}

	// A new verdict opens a new second chance: death was the old machine's.
	if _, _, err := h.svc.SuspectBox(ctx, box, CauseIPBanned); err != nil {
		t.Fatalf("evict after the reinstall: %v", err)
	}
	if err := h.pool.QueryRow(ctx, `
		SELECT count(*) FROM entry_suspects WHERE kind = 'box' AND subject = $1 AND status = 'suspect'`, box).Scan(&open); err != nil || open != 1 {
		t.Fatalf("a new eviction opened %d second chance(s) (%v), want 1", open, err)
	}
}
