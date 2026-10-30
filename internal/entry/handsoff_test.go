package entry

// handsoff_test.go is the second round of audit 42 (AUDIT/42-autopilot-recheck.md):
// what the first fixes introduced — a slot's voice, "no evidence" on an
// admission, a forgotten host key — had not reached the passes that hand
// machines back. Each test here was red on 3dd48564/dfd22f3b.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// relaysOn is every relay name on a box that is not dead.
func (h *hostHarness) relaysOn(box string) []string {
	h.t.Helper()
	relays, err := h.svc.List(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range relays {
		if r.Box == box && r.Status != StatusDead {
			out = append(out, r.Name)
		}
	}
	return out
}

// ageIdle moves everything that happened to a box `by` into the past, so the
// idle pass reads it as sitting that long.
func (h *hostHarness) ageIdle(box string, by time.Duration) {
	h.t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE entry_jobs SET created_at = created_at - make_interval(secs => $2),
		        finished_at = finished_at - make_interval(secs => $2) WHERE box = $1`,
		`UPDATE entry_boxes SET created_at = created_at - make_interval(secs => $2) WHERE name = $1`,
		`UPDATE entry_relays SET died_at = died_at - make_interval(secs => $2) WHERE box = $1 AND died_at IS NOT NULL`,
	} {
		if _, err := h.pool.Exec(ctx, q, box, by.Seconds()); err != nil {
			h.t.Fatal(err)
		}
	}
}

// isolateWave keeps the fuse fleet-wide state out of the way of other tests:
// the handbacks of machines written off earlier are moved out of the wave
// window now and when the test ends, and every pause still standing is lifted
// at the end. Without it, two runs of the package within the hour count each
// other's write-offs and blow the ip_banned fuse under unrelated tests.
func (h *hostHarness) isolateWave() {
	h.t.Helper()
	age := func() {
		_, _ = h.pool.Exec(context.Background(), `
			UPDATE entry_boxes SET retired_at = retired_at - interval '30 days'
			WHERE retired_at > now() - interval '30 days'
			  AND name IN (SELECT subject FROM entry_suspects WHERE kind = 'box' AND status = 'dead')`)
	}
	age()
	h.t.Cleanup(func() {
		age()
		_, _ = h.pool.Exec(context.Background(),
			`UPDATE entry_cause_pauses SET lifted_at = now(), lifted_by = 'test' WHERE lifted_at IS NULL`)
	})
}

// НВ1, path Б: every relay of a machine marked dead behind the owner gate
// alone, and six hours later the idle pass retired the machine and deleted it
// at the hoster. Owner's decision 27.10 (§8.1): marking a relay dead is behind
// the step-up — it is a retire with a six-hour fuse.
func TestMarkingARelayDeadNeedsAStepUp(t *testing.T) {
	h := newHostHarness(t)
	box := h.inService()
	touched := false
	do := h.stepUpMux(&touched)

	relays := h.relaysOn(box)
	if len(relays) == 0 {
		t.Fatal("the machine in service has no relay")
	}
	for _, r := range relays {
		if code, body := do("POST", "/api/v1/admin/entry/relays/"+r+"/dead",
			map[string]string{"reason": DeathOther}); code != http.StatusForbidden {
			t.Fatalf("mark %s dead without a touch: %d %s, want 403", r, code, body)
		}
	}
	if n := len(h.relaysOn(box)); n != len(relays) {
		t.Fatalf("a refused request killed %d relays", len(relays)-n)
	}

	h.ageIdle(box, 7*time.Hour)
	if _, _, err := NewMechanic(h.svc, h.prov).Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("the machine was handed back after refused requests: retired=%v deleted=%v",
			h.retired(box), h.host.deletedIDs())
	}

	// The touch is what opens it.
	touched = true
	if code, body := do("POST", "/api/v1/admin/entry/relays/"+relays[0]+"/dead",
		map[string]string{"reason": DeathOther}); code != http.StatusOK {
		t.Fatalf("mark dead with a touch: %d %s", code, body)
	}
}

// НВ1, path А: retiring a slot asks for the touch, and opening it again
// through POST /vantages gave it back its voice behind the gate alone. The
// route is behind the step-up since 5290bae6; this pins the revival.
func TestReopeningARetiredSlotNeedsAStepUp(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	touched := false
	do := ph.vantageMux(&touched)

	slot := "rv-" + uuid.NewString()[:6]
	dropSlot(t, ph, slot)
	if _, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Host: "198.51.100.31"}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := ph.svc.RetireVantage(ctx, slot, "test"); err != nil {
		t.Fatal(err)
	}
	if code, body := do("POST", "/api/v1/admin/entry/vantages",
		map[string]string{"slot": slot, "host": "198.51.100.31"}); code != http.StatusForbidden {
		t.Fatalf("reopen without a touch: %d %s, want 403", code, body)
	}
	v, err := ph.svc.VantageSlot(ctx, slot)
	if err != nil || v.Status != VantageRetired {
		t.Fatalf("slot after a refused reopen: %+v %v, want retired", v, err)
	}
}

// deadSuspect evicts a machine in service and writes its second chance off,
// as SecondChancePass does when the reprobes keep failing.
func (h *hostHarness) deadSuspect() string {
	h.t.Helper()
	ctx := context.Background()
	box := h.inService()
	if _, _, err := h.svc.SuspectBox(ctx, box, CauseIPBanned); err != nil {
		h.t.Fatalf("evict: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_suspects SET status = 'dead', ended_at = now(), outcome = 'test'
		WHERE kind = 'box' AND subject = $1 AND status = 'suspect'`, box); err != nil {
		h.t.Fatal(err)
	}
	return box
}

// НВ1 step 7: the eviction obeys the wave and its pause, and the handback the
// eviction leads to did not — a paused wave on ip_banned still had the pass
// delete the machine at the hoster two days later.
func TestADeadSuspectWaitsForAPausedWave(t *testing.T) {
	h := newHostHarness(t)
	h.isolateWave()
	ctx := context.Background()
	box := h.deadSuspect()
	if err := h.svc.PauseCause(ctx, CauseIPBanned, "волна: тест", 3); err != nil {
		t.Fatal(err)
	}

	gone, err := h.prov.retireDeadSuspects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 || h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("handed back %v under a paused wave: retired=%v deleted=%v", gone, h.retired(box), h.host.deletedIDs())
	}

	// A person looked and lifted it: the ledger's decision goes through.
	if _, err := h.pool.Exec(ctx,
		`UPDATE entry_cause_pauses SET lifted_at = now(), lifted_by = 'test' WHERE lifted_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if gone, err := h.prov.retireDeadSuspects(ctx); err != nil || len(gone) != 1 {
		t.Fatalf("after the lift handed back %v (%v), want %s", gone, err, box)
	}
	if !h.retired(box) {
		t.Fatal("the dead suspect is still in service after the lift")
	}
}

// The handbacks themselves are a wave: more of them in the window than the
// limit allows is the same "new rule at the censor, or our bug" the eviction's
// fuse stands for, and it blows the same fuse.
func TestDeadSuspectHandbacksAreAWave(t *testing.T) {
	h := newHostHarness(t)
	h.isolateWave()
	ctx := context.Background()
	before, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	limit := 2
	if _, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{WaveLimit: &limit}, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.svc.SaveDetectionPolicy(context.Background(), DetectionPolicyPatch{WaveLimit: &before.WaveLimit}, "test")
	})
	boxes := []string{h.deadSuspect(), h.deadSuspect(), h.deadSuspect()}

	gone, err := h.prov.retireDeadSuspects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != limit {
		t.Fatalf("handed back %v, want %d — the wave limit", gone, limit)
	}
	if _, paused, err := h.svc.CausePaused(ctx, CauseIPBanned); err != nil || !paused {
		t.Fatalf("the wave of handbacks blew no fuse (%v)", err)
	}
	left := 0
	for _, b := range boxes {
		if !h.retired(b) {
			left++
		}
	}
	if left != 1 {
		t.Fatalf("%d dead suspects left in service, want the one past the limit", left)
	}
}

// lastOrdered is the box of the newest order this test's hoster took.
func (h *hostHarness) lastOrdered() string {
	h.t.Helper()
	h.host.mu.Lock()
	defer h.host.mu.Unlock()
	if len(h.host.ordered) == 0 {
		h.t.Fatal("nothing was ordered")
	}
	return h.host.ordered[len(h.host.ordered)-1].Hostname
}

// awaitRepair waits for the incident's newest repair to end and returns its
// status.
func (h *hostHarness) awaitRepair(incident uuid.UUID) string {
	h.t.Helper()
	for i := 0; i < 600; i++ {
		var status string
		err := h.pool.QueryRow(context.Background(), `
			SELECT status FROM entry_repairs WHERE incident_id = $1 ORDER BY started_at DESC LIMIT 1`,
			incident).Scan(&status)
		if err == nil && status != repairRunning {
			return status
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatal("the repair never ended")
	return ""
}

// shortOfFleet opens a Reality pool incident under a target far above what
// any run leaves alive, removed when the test ends.
func (h *hostHarness) shortOfFleet() Incident {
	h.t.Helper()
	ctx := context.Background()
	pool := FleetPool(ProtoVlessRealityVision, "")
	// This fixture exercises one admission, not a 50-machine burst. Read
	// the shared pool so earlier tests cannot change the shortage's size.
	for n := 1; ; n++ {
		h.svc.primeSizing(map[string]PoolSizing{pool: sizingFor(n)})
		f, err := h.svc.poolForecast(ctx, mustPolicy(h.t, h.svc), pool)
		if err != nil {
			h.t.Fatal(err)
		}
		if f.Need == 1 {
			break
		}
		if n > 1000 {
			h.t.Fatal("cannot size a one-machine admission trial")
		}
	}
	_, _ = h.pool.Exec(ctx, `DELETE FROM entry_repairs WHERE pool = $1`, pool)
	inc, err := h.svc.openPool(ctx, pool, CauseShortOfFleet, "тест", map[string]string{})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = h.pool.Exec(ctx, `DELETE FROM entry_repairs WHERE pool = $1`, pool)
		_, _ = h.pool.Exec(ctx, `DELETE FROM entry_incidents WHERE pool = $1`, pool)
	})
	return *inc
}

// НС1, owner's decision 27.10 (§8.3): a vantage that cannot judge — its xray
// broken, every answer probe_failed, or nobody claiming the task — is no
// evidence against an address. The fresh machine used to be retired and
// deleted after five minutes and the attempt counted as a strike, so a deaf
// prober handed the pool's top-up to a person with nothing against any
// address. Now the machine is kept for a live vantage, and the attempt is
// `held`: not a strike.
func TestADeafVantageHoldsTheFreshMachineAndStrikesNothing(t *testing.T) {
	h := newHostHarness(t)
	h.prov.probeTimeout = 400 * time.Millisecond
	h.svc.SetVantages(fakeVantages{regions: []string{testVantage}})
	ctx := context.Background()
	inc := h.shortOfFleet()
	m := NewMechanic(h.svc, h.prov)

	h.vantage(VerdictProbeFailed)
	if err := m.Start(ctx, inc.ID, "", PlaybookScaleUp); err != nil {
		t.Fatalf("start: %v", err)
	}
	status := h.awaitRepair(inc.ID)
	box := h.lastOrdered()
	h.t.Cleanup(func() { h.retire(box) })
	if h.retired(box) || len(h.host.deletedIDs()) != 0 {
		t.Fatalf("a machine nobody had evidence against was handed back: retired=%v deleted=%v",
			h.retired(box), h.host.deletedIDs())
	}
	if status != repairHeld {
		t.Fatalf("the attempt ended %q, want %q — no evidence is not a failure", status, repairHeld)
	}

	// Three attempts like it are still no reason to bring a person in.
	pool := FleetPool(ProtoVlessRealityVision, "")
	for i := 0; i < 3; i++ {
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO entry_repairs (incident_id, pool, cause, playbook, decided_by, status, finished_at, started_at)
			VALUES ($1, $2, $3, $4, 'auto', 'held', now(), now() - interval '2 hours')`,
			inc.ID, pool, CauseShortOfFleet, PlaybookScaleUp); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if why, _, err := m.poolRails(ctx, inc, PlaybookScaleUp, policy); err != nil || why != "" {
		t.Fatalf("held attempts handed the pool to a person: %q (%v)", why, err)
	}
}

// And without a vantage that has reported lately, the top-up does not start
// at all: nothing is ordered for an admission nobody can judge.
func TestScaleUpWaitsForALiveVantage(t *testing.T) {
	h := newHostHarness(t)
	h.svc.SetVantages(fakeVantages{})
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `UPDATE entry_vantages SET last_admission_poll_at = NULL WHERE slot = $1`, testVantage); err != nil {
		t.Fatal(err)
	}
	inc := h.shortOfFleet()
	m := NewMechanic(h.svc, h.prov)
	orders := h.host.orderCount()

	if err := m.Start(ctx, inc.ID, "", PlaybookScaleUp); !errors.Is(err, ErrRejected) {
		t.Fatalf("start with no live vantage: %v, want refused", err)
	}
	policy, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if why, blocked, err := m.poolRails(ctx, inc, PlaybookScaleUp, policy); err != nil || !blocked || why != "" {
		t.Fatalf("rails with no live vantage: %q blocked=%v (%v), want a quiet wait", why, blocked, err)
	}
	if n := h.host.orderCount(); n != orders {
		t.Fatalf("ordered %d machines with no vantage to admit them", n-orders)
	}
	// A slot nobody registered does not count, however lately it spoke.
	h.svc.SetVantages(fakeVantages{regions: []string{"zz-nobody"}})
	if _, blocked, _ := m.poolRails(ctx, inc, PlaybookScaleUp, policy); !blocked {
		t.Fatal("an unregistered region counted as a live vantage")
	}
	h.svc.SetVantages(fakeVantages{regions: []string{testVantage}})
	if err := h.svc.noteAdmissionPoll(ctx, h.pool, testVantage); err != nil {
		t.Fatal(err)
	}
	if _, blocked, err := m.poolRails(ctx, inc, PlaybookScaleUp, policy); err != nil || blocked {
		t.Fatalf("rails with a live vantage: blocked=%v (%v)", blocked, err)
	}
}

// НС3: the operator reinstalls an idle machine and forgets its host key (a
// step-up action). Any top-up of the same pool used to take it off the shelf,
// fail ssh for ten minutes on the unknown key and delete it at the hoster in
// the middle of the reinstall; six hours of idling did the same without ssh.
// A machine whose key was forgotten is in the operator's hands.
func TestAMachineWithAForgottenKeyIsLeftToTheOperator(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	idle, instance := h.idleMachine()
	if err := h.svc.ForgetHostKey(ctx, idle); err != nil {
		t.Fatal(err)
	}
	h.prov.bootWait = 50 * time.Millisecond

	h.vantage("clean")
	if took, note, _ := h.prov.adoptIdle(ctx, OrderSpec{}); took == idle || strings.Contains(note, idle) {
		t.Fatalf("adoptIdle touched the machine with a forgotten key: %q", note)
	}
	if h.retired(idle) || contains(h.host.deletedIDs(), instance) {
		t.Fatal("a top-up handed back the machine the operator is reinstalling")
	}

	h.ageIdle(idle, 7*time.Hour)
	if gone, err := h.prov.handBackIdle(ctx); err != nil || contains(gone, idle) {
		t.Fatalf("the idle pass handed back %v (%v), the machine with a forgotten key among them", gone, err)
	}
	if h.retired(idle) || contains(h.host.deletedIDs(), instance) {
		t.Fatal("six hours of idling deleted the machine the operator is reinstalling")
	}
}

// НН1: an observation slot on a machine the hub ordered, whose prober job
// never started (the deploy refused before startJobOf) — no relay by design,
// no prober job either. The idle pass handed it back and the slot pointed at
// a deleted machine.
func TestASlotsMachineIsNotIdle(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box, instance := h.idleMachine()
	slot := "nn1-" + uuid.NewString()[:6]
	dropSlot(t, h.provHarness, slot)
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO entry_vantages (slot, title, box) VALUES ($1, 'nn1', $2)`, slot, box); err != nil {
		t.Fatal(err)
	}
	idle, err := h.svc.idleBoxes(ctx, fakeHostKind, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range idle {
		if b.name == box {
			t.Fatalf("the slot's machine %s reads as idle", box)
		}
	}
	h.ageIdle(box, 7*time.Hour)
	if gone, err := h.prov.handBackIdle(ctx); err != nil || contains(gone, box) {
		t.Fatalf("the idle pass handed back %v (%v)", gone, err)
	}
	if h.retired(box) || contains(h.host.deletedIDs(), instance) {
		t.Fatal("the slot's machine was handed back")
	}
}

// lostOrder places an order whose answer never arrives — the machine is made
// at the hoster, the hub hears a timeout — and ages it past orderSettleAfter.
// Every other open order of this hoster kind is settled first: the pass reads
// twenty a time, oldest first, and the test database keeps every run's.
func (h *hostHarness) lostOrder() (id uuid.UUID, instance string) {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_box_orders SET status = 'released', updated_at = now()
		WHERE provider = $1 AND status IN ('reserved', 'created', 'undeleted')`, fakeHostKind); err != nil {
		h.t.Fatal(err)
	}
	h.host.mu.Lock()
	h.host.loseAnswer = errors.New("vultr: http: context deadline exceeded")
	h.host.mu.Unlock()
	if _, err := h.prov.orderAndRecord(ctx, OrderSpec{}); err == nil {
		h.t.Fatal("an order with a lost answer succeeded")
	}
	h.host.mu.Lock()
	h.host.loseAnswer = nil
	for iid, inst := range h.host.instances {
		if inst.Label == "nekko-"+h.host.ordered[len(h.host.ordered)-1].Hostname {
			instance = iid
		}
	}
	h.host.mu.Unlock()
	if err := h.pool.QueryRow(ctx, `
		UPDATE entry_box_orders SET updated_at = now() - interval '1 hour'
		WHERE name = $1 AND status = 'reserved' RETURNING id`, h.lastOrdered()).Scan(&id); err != nil {
		h.t.Fatalf("the lost order is not reserved: %v", err)
	}
	return id, instance
}

func (h *hostHarness) orderStatus(id uuid.UUID) string {
	h.t.Helper()
	var s string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT status FROM entry_box_orders WHERE id = $1`, id).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// НС2 (a): the machine of a lost answer is not in the listing yet — Vultr's
// list lags, or it sits on a page the hub did not read. One look that found
// nothing wrote the order off as "no machine", and a machine that existed left
// the month's cap. "Not found" is not "not there": the order stays counted
// until several listings in a row, over time, found nothing — and when the
// machine shows up meanwhile, it is deleted and only then released.
func TestALostOrderIsNotWrittenOffOnOneEmptyListing(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	before := h.dayProjected()
	id, instance := h.lostOrder()
	if instance == "" {
		t.Fatal("the fake made no machine for the lost order")
	}
	h.host.mu.Lock()
	h.host.hidden = map[string]bool{instance: true}
	h.host.mu.Unlock()

	for i := 0; i < 3; i++ {
		if _, err := h.svc.ReleaseUndeleted(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st := h.orderStatus(id); st != orderReserved {
		t.Fatalf("after three quick listings the order is %q, want still reserved", st)
	}
	if got := h.dayProjected(); !nearCents(got, before+machineDay()) {
		t.Fatalf("the day %.2f → %.2f: the lost order left the budget", before, got)
	}

	// It shows up: deleted at the hoster, and only then out of the budget.
	h.host.mu.Lock()
	h.host.hidden = nil
	h.host.mu.Unlock()
	if _, err := h.svc.ReleaseUndeleted(ctx); err != nil {
		t.Fatal(err)
	}
	if !contains(h.host.deletedIDs(), instance) || h.orderStatus(id) != orderReleased {
		t.Fatalf("the machine that showed up: deleted=%v order=%q", h.host.deletedIDs(), h.orderStatus(id))
	}
}

// НС2 (b): the owner renamed the machine in the hoster's panel while the hub
// waited for the answer. The label no longer matches, the tag still says it is
// the hub's — and a tagged machine the hub cannot account for keeps every
// lost order in the budget, however long, until a person settles it.
func TestALostOrderStaysCountedWhileATaggedMachineIsUnaccounted(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	before := h.dayProjected()
	id, instance := h.lostOrder()
	h.host.mu.Lock()
	h.host.instances[instance].Label = "renamed-by-hand"
	h.host.mu.Unlock()

	for i := 0; i < 5; i++ {
		if _, err := h.svc.ReleaseUndeleted(ctx); err != nil {
			t.Fatal(err)
		}
		_, _ = h.pool.Exec(ctx, `
			UPDATE entry_box_orders SET unseen_since = unseen_since - interval '1 hour' WHERE id = $1`, id)
	}
	if st := h.orderStatus(id); st != orderReserved {
		t.Fatalf("the order is %q with a tagged machine nobody accounts for, want reserved", st)
	}
	if got := h.dayProjected(); !nearCents(got, before+machineDay()) {
		t.Fatalf("the day %.2f → %.2f", before, got)
	}
	if contains(h.host.deletedIDs(), instance) {
		t.Fatal("a machine matched by nothing but its tag was deleted")
	}
}

// And a lost order that really made nothing is written off — after the
// listings in a row and the time, not on the first look.
func TestALostOrderThatMadeNothingIsWrittenOffInTime(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	id, instance := h.lostOrder()
	h.host.mu.Lock()
	delete(h.host.instances, instance)
	h.host.mu.Unlock()

	if _, err := h.svc.ReleaseUndeleted(ctx); err != nil {
		t.Fatal(err)
	}
	if st := h.orderStatus(id); st != orderReserved {
		t.Fatalf("written off on the first look: %q", st)
	}
	for i := 0; i < orderUnseenPasses; i++ {
		_, _ = h.pool.Exec(ctx, `
			UPDATE entry_box_orders SET unseen_since = unseen_since - interval '1 hour' WHERE id = $1`, id)
		if _, err := h.svc.ReleaseUndeleted(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st := h.orderStatus(id); st != orderReleased {
		t.Fatalf("after %d empty listings over hours the order is %q, want released", orderUnseenPasses+1, st)
	}
}

// settleLeftoverPurchases writes off every purchase still open on this test
// database, now and when the test ends: the replay is fleet-wide, and an
// earlier run's `unknown` would be replayed by this one.
func settleLeftoverPurchases(h *harness) {
	h.t.Helper()
	settle := func() {
		_, _ = h.pool.Exec(context.Background(), `
			UPDATE entry_domains SET status = 'failed', error = 'test cleanup'
			WHERE status IN ('buying', 'unknown')`)
	}
	settle()
	h.t.Cleanup(settle)
}

// НН6: the console's «buy» went straight to the purchase, past the rule the
// hub's own purchases obey — nothing new is bought while an earlier purchase
// is unsettled, because that one may be a name already paid for.
func TestAConsolePurchaseSettlesTheUnansweredOneFirst(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	ctx := context.Background()
	settleLeftoverPurchases(h)
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0
	reg.buyLost = errors.New("porkbun: http: context deadline exceeded")

	box, addr := h.xhttpBoxNoDomain(t)
	if _, _, err := h.svc.AcquireDomain(ctx, box, addr); err == nil {
		t.Fatal("a purchase with no answer succeeded")
	}
	lost := reg.keys[0][0]

	// The registrar is not answering at all now: the console's purchase
	// stops at the replay and sends no new Buy.
	reg.mu.Lock()
	reg.down = errors.New("porkbun: http: connection refused")
	reg.mu.Unlock()
	wanted := "console" + uuid.NewString()[:6] + ".lol"
	if _, err := h.svc.BuyDomain(ctx, wanted); err == nil {
		t.Fatal("the console bought a new name while the last purchase is unsettled")
	}
	for _, k := range reg.keys {
		if k[0] == wanted {
			t.Fatalf("Buy was sent for %s with %s unsettled: %v", wanted, lost, reg.keys)
		}
	}

	// It answers: the old one is settled into the pool, then the new one bought.
	reg.mu.Lock()
	reg.down, reg.buyLost = nil, nil
	reg.mu.Unlock()
	if _, err := h.svc.BuyDomain(ctx, wanted); err != nil {
		t.Fatalf("console buy once the registrar answers: %v", err)
	}
	if d, ok, _ := h.svc.repo.domain(ctx, lost); !ok || d.Status != domainOwned {
		t.Fatalf("the unanswered purchase %s is %+v, want settled into the pool", lost, d)
	}
}

// НН6: a purchase whose row stayed `buying` — the hub died between the
// commit and the Buy, or markDomainUnknown failed — was never replayed: the
// replay read only `unknown`, and a name possibly paid for sat outside the
// pool for good. A `buying` row past the time a Buy takes is replayed too.
func TestAStuckBuyingPurchaseIsReplayed(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	ctx := context.Background()
	settleLeftoverPurchases(h)
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0

	name := "stuck" + uuid.NewString()[:6] + ".lol"
	key := "key-" + uuid.NewString()
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO entry_domains (domain, tld, status, source, cost_cents, idempotency_key, created_at, updated_at)
		VALUES ($1, 'lol', 'buying', 'purchased', 154, $2, now() - interval '1 hour', now() - interval '1 hour')`,
		name, key); err != nil {
		t.Fatal(err)
	}
	// The registrar did charge for it, as a hub dying after the Buy leaves it.
	reg.mu.Lock()
	reg.charged = map[string]bool{key: true}
	reg.mu.Unlock()

	box, addr := h.xhttpBoxNoDomain(t)
	got, bought, err := h.svc.AcquireDomain(ctx, box, addr)
	if err != nil {
		t.Fatal(err)
	}
	if got != name || len(reg.keys) != 1 || reg.keys[0] != [2]string{name, key} {
		t.Fatalf("acquired %s (bought=%v) with buys %v, want the stuck %s replayed with its key", got, bought, reg.keys, name)
	}
}

// allowDomainSpend sets buy_domain to `auto` for the test, which is what lets
// a provision buy a name when the pool is dry (domainSpendHeld), and puts the
// owner's row back as it was when the test ends.
func allowDomainSpend(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var was *string
	_ = pool.QueryRow(ctx, `SELECT mode FROM entry_playbook_modes WHERE cause = $1`, CauseShortOfDomains).Scan(&was)
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_playbook_modes (cause, mode, updated_by) VALUES ($1, 'auto', 'test')
		ON CONFLICT (cause) DO UPDATE SET mode = 'auto'`, CauseShortOfDomains); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if was == nil {
			_, _ = pool.Exec(ctx, `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseShortOfDomains)
			return
		}
		_, _ = pool.Exec(ctx, `UPDATE entry_playbook_modes SET mode = $2 WHERE cause = $1`, CauseShortOfDomains, *was)
	})
}

// З2, owner's decision 27.10 (§8.2): provisioning stays on the gate, but a
// name it buys for an XHTTP box with the pool dry obeys buy_domain's mode and
// the domains' pause, as any spend does. It used to buy past both.
func TestAProvisionsDomainPurchaseObeysTheModeAndThePause(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	settleLeftoverPurchases(h)
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0
	box, addr := h.xhttpBoxNoDomain(t)

	// The owner's mode is `ask`: a person buys, not a deploy.
	if err := h.svc.SetPlaybookMode(ctx, CauseShortOfDomains, ModeAsk, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseShortOfDomains)
	})
	if _, _, err := h.svc.AcquireDomain(ctx, box, addr); !errors.Is(err, ErrRejected) {
		t.Fatalf("acquire under `ask`: %v, want refused", err)
	}

	// `auto`, but the domains' spend is paused.
	if err := h.svc.SetPlaybookMode(ctx, CauseShortOfDomains, ModeAuto, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.PauseCause(ctx, CauseDomainBlocked, "волна: тест", 3); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(),
			`UPDATE entry_cause_pauses SET lifted_at = now(), lifted_by = 'test' WHERE lifted_at IS NULL`)
	})
	if _, _, err := h.svc.AcquireDomain(ctx, box, addr); !errors.Is(err, ErrRejected) {
		t.Fatalf("acquire under a paused domain wave: %v, want refused", err)
	}
	if len(reg.keys) != 0 {
		t.Fatalf("Buy was sent %v while the spend was held", reg.keys)
	}
}

// НН2: DeployProber committed the slot's new token, then asked for the box's
// job lock. On a box with a job running the deploy was refused — and the
// working prober's token was already void, the new one delivered nowhere. A
// title edit through the save route did the same on every save.
func TestARefusedProberDeployLeavesTheWorkingTokenAlone(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	ph.box.mu.Lock()
	ph.box.keyIn = true
	ph.box.mu.Unlock()
	slot := "nn2-" + uuid.NewString()[:6]
	dropSlot(t, ph, slot)
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Box: box}, "test")
	if err != nil {
		t.Fatal(err)
	}

	job, err := ph.svc.startJobOf(ctx, JobProvision, box, "", uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ph.prov.DeployProber(ctx, box, slot, ""); !errors.Is(err, ErrBoxBusy) {
		t.Fatalf("deploy onto a busy box: %v, want ErrBoxBusy", err)
	}
	if got, err := ph.svc.VantageByToken(ctx, v.Token); err != nil || got != slot {
		t.Fatalf("a refused deploy voided the working token: %q %v", got, err)
	}
	_ = ph.svc.finishJob(ctx, job, nil)

	// A save that keeps the machine and changes the title redeploys nothing.
	touched := true
	do := ph.vantageMux(&touched)
	if code, body := do("PUT", "/api/v1/admin/entry/vantages/"+slot,
		map[string]string{"title": "новое имя", "box": box}); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	if got, err := ph.svc.VantageByToken(ctx, v.Token); err != nil || got != slot {
		t.Fatalf("a title edit voided the working token: %q %v", got, err)
	}
}

// regionalHealth is probe history by the slot that wrote it: FailingRelays
// and RelayRTT read only the slots they are given.
type regionalHealth map[string]map[string]time.Time // slot → relay → last failure

func (h regionalHealth) FailingRelays(_ context.Context, _ time.Time, slots []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for _, s := range slots {
		for r, at := range h[s] {
			out[r] = at
		}
	}
	return out, nil
}

func (h regionalHealth) RelayRTT(context.Context, string, time.Time, time.Time, []string) (float64, int, error) {
	return 0, 0, nil
}

// НН3: the rollout's readers mixed every source_region, a retired slot's
// included, so a slot the hub no longer listens to could hold a box out of
// the canary — or bend the path check — with rows the detector would drop.
func TestARetiredSlotsRowsDoNotHoldACanaryBack(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	ctx := context.Background()
	gone := "ret-" + uuid.NewString()[:6]
	if _, err := rh.pool.Exec(ctx, `
		INSERT INTO entry_vantages (slot, title, status, retired_at) VALUES ($1, 'retired', 'retired', now())`, gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = rh.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, gone) })

	o := rh.orchestrator()
	o.SetHealth(regionalHealth{gone: {box + "-1": time.Now()}})
	o.round(ctx)
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone || ro.CanaryBox != box {
		t.Fatalf("a retired slot's failure held the canary back: %+v", ro)
	}
}

// scriptedRemote answers every command with out.
type scriptedRemote struct{ out string }

func (r scriptedRemote) Run(context.Context, string, io.Reader) (string, error) { return r.out, nil }
func (scriptedRemote) HostKey() string                                          { return "" }
func (scriptedRemote) Close() error                                             { return nil }

// НН5: a box is recorded under the address as typed, and the errors from
// below print an IP canonically — the stored spelling is the canonical one,
// so Redact and the one-address-one-box rule see the same string as net. And
// a machine's raw answer is not quoted into a job's error: it lands in
// entry_job_log and entry_jobs.error (migration 043: "not raw remote output").
func TestABoxAddressIsCanonicalAndRawOutputStaysOut(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := uuid.New()
	name := boxName("c", u)
	typed := fmt.Sprintf("2001:DB8:%X%X:0:0::%X", u[3], u[4], u[5]+1)
	if err := h.svc.CreateBox(ctx, BoxSpec{Name: name, Address: typed, Provider: "vultr", Location: "amsterdam"}); err != nil {
		t.Fatal(err)
	}
	acc, err := h.svc.boxAccess(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if want := net.ParseIP(typed).String(); acc.address != want {
		t.Fatalf("stored %q, want the canonical %q", acc.address, want)
	}

	secret := "root:hunter2-sealed"
	j := &jobRun{ctx: ctx, box: name}
	if _, err := j.arch(scriptedRemote{out: "x86_65 " + secret}); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("arch error carries the machine's raw answer: %v", err)
	}
	if _, err := j.waitEmpty(scriptedRemote{out: secret}, []provRelay{{id: Identity{Port: 443}}}, time.Millisecond); err == nil ||
		strings.Contains(err.Error(), secret) {
		t.Fatalf("connection count error carries the machine's raw answer: %v", err)
	}
}
