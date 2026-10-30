package entry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// bookRepair writes a finished attempt as the rails will read it: this box,
// this cause, this long ago.
func (h *harness) bookRepair(incident uuid.UUID, box, cause, playbook, status string, ago time.Duration) {
	h.t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO entry_repairs (incident_id, box, cause, playbook, decided_by, status,
		                           started_at, finished_at)
		VALUES ($1, $2, $3, $4, 'auto', $5, $6, $6)
	`, incident, box, cause, playbook, status, time.Now().Add(-ago))
	if err != nil {
		h.t.Fatalf("book repair: %v", err)
	}
}

// openIncident is the usual two lines: a diagnosis the hub has to act on.
func (h *harness) openIncident(box, cause string, verdicts map[string]string) Incident {
	h.t.Helper()
	inc, err := h.svc.Open(context.Background(), Diagnosis{
		Box: box, Cause: cause, Advice: "тест", Verdicts: verdicts,
	})
	if err != nil {
		h.t.Fatalf("open incident: %v", err)
	}
	if inc == nil {
		h.t.Fatalf("cause %q opened no incident", cause)
	}
	// The test database is shared by every run against it, and the rails are
	// deliberately fleet-wide: a wave is "this cause on three machines", not
	// "three machines of this test". So a test takes its incident (and, by
	// cascade, its repairs) back out when it is done, or the next test reads
	// its own run as a wave.
	//
	// The pause goes with it (К2). A wave now writes a fuse that outlives its
	// window on purpose — it ends when a person ends it — so a test that
	// tripped one would otherwise leave every later test of that cause
	// blocked by it.
	h.t.Cleanup(func() {
		if _, err := h.pool.Exec(context.Background(),
			`DELETE FROM entry_incidents WHERE id = $1`, inc.ID); err != nil {
			h.t.Errorf("clean up incident: %v", err)
		}
		if _, err := h.pool.Exec(context.Background(),
			`DELETE FROM entry_cause_pauses WHERE cause = $1`, cause); err != nil {
			h.t.Errorf("clean up pauses: %v", err)
		}
	})
	return *inc
}

// Two attempts that did not help are bad luck; the third means the hypothesis
// is wrong, not the execution. The hub stops and says so in the incident,
// which is what moves the card into the console's queue.
func TestThreeFailedRepairsHandTheIncidentToAPerson(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	// Older than the wave window on purpose: what is under test is the
	// three-strikes rail on one box, and attempts inside the hour would trip
	// the fleet-wide rail first.
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	for i := 0; i < pol.FailStrikes; i++ {
		h.bookRepair(inc.ID, box, inc.Cause, PlaybookRetirePorts, repairFailed, pol.WaveWindow+time.Hour)
	}

	// consider() and not Pass(): the pass is this called over every standing
	// incident, and the test database holds other runs' incidents too.
	started, handed := NewMechanic(h.svc, nil).consider(ctx, inc)
	if started {
		t.Error("started a repair after three failures")
	}
	if !handed {
		t.Fatal("the incident was not handed to a person")
	}
	after, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if after.Handoff == "" {
		t.Error("the incident carries no reason the hub stopped")
	}
	if after.Status != IncidentOpen {
		t.Errorf("status = %q, want open: handing over does not make it less true", after.Status)
	}
}

// A repair that did not take must not become a loop: the box is left alone for
// the cooldown, and "not now" is not the same as "give up" — nothing is
// written on the incident.
func TestACooldownHoldsTheNextAttemptWithoutGivingUp(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	h.bookRepair(inc.ID, box, inc.Cause, PlaybookRetirePorts, repairFailed, time.Minute)

	m := NewMechanic(h.svc, nil)
	why, blocked, err := m.rails(ctx, inc, PlaybookForCause(inc.Cause).Name)
	if err != nil {
		t.Fatalf("rails: %v", err)
	}
	if !blocked {
		t.Fatal("a repair a minute ago did not hold the next one")
	}
	if why != "" {
		t.Errorf("a cooldown gave a reason to give up: %q", why)
	}
}

// And it is a cooldown, not a ban: once it is out, the box is fair game again.
func TestTheCooldownRunsOut(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	h.bookRepair(inc.ID, box, inc.Cause, PlaybookRetirePorts, repairFailed, pol.RepairCooldown+time.Minute)

	_, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name)
	if err != nil {
		t.Fatalf("rails: %v", err)
	}
	if blocked {
		t.Error("the cooldown never expires")
	}
}

// The cooldown is for a repair that did not take. One that worked, on an
// incident it closed, does not hold the next one back; one that ended `done`
// on the incident still standing did not cure it, and does.
func TestOnlyARepairThatDidNotTakeCoolsTheBoxDown(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	earlier := h.openIncident(box, CauseEdgeDown, map[string]string{box + "-1": "ip_banned"})
	inc := h.openIncident(box, CauseShortOfPorts, map[string]string{})
	h.bookRepair(earlier.ID, box, inc.Cause, PlaybookAddPorts, repairDone, time.Minute)

	m := NewMechanic(h.svc, nil)
	if _, blocked, err := m.rails(ctx, inc, PlaybookForCause(inc.Cause).Name); err != nil {
		t.Fatalf("rails: %v", err)
	} else if blocked {
		t.Error("a repair that worked on another incident held this one back")
	}

	h.bookRepair(inc.ID, box, inc.Cause, PlaybookAddPorts, repairDone, time.Minute)
	if _, blocked, err := m.rails(ctx, inc, PlaybookForCause(inc.Cause).Name); err != nil {
		t.Fatalf("rails: %v", err)
	} else if !blocked {
		t.Error("a repair that left its own incident standing did not hold the next attempt")
	}
}

// The strikes are a day's, not a lifetime's. Found on the stand 25.10: the box
// that had been through three edge_down chaos runs — each repaired by the hub
// itself — would have had its fourth stopped unit handed to a person, because
// the rail counted every attempt the box ever had.
func TestOldRepairsAreNotStrikes(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	for i := 0; i < pol.FailStrikes; i++ {
		h.bookRepair(inc.ID, box, inc.Cause, PlaybookRetirePorts, repairDone, strikeWindow+time.Hour)
	}

	why, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name)
	if err != nil {
		t.Fatalf("rails: %v", err)
	}
	if blocked {
		t.Errorf("repairs older than a day still count as strikes: %q", why)
	}
}

// Three machines dying of one cause inside an hour is a rule change at the
// censor, our own broken probe, or a bad build. Repairing the fourth one by
// itself — especially by buying something — is the wrong answer to all three.
func TestAWaveOfOneCausePausesTheAutopilotForIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatalf("detection policy: %v", err)
	}
	for i := 0; i < pol.WaveLimit; i++ {
		other, _ := h.box()
		prior := h.openIncident(other, CausePortsBanned, map[string]string{other + "-1": "ip_banned"})
		h.bookRepair(prior.ID, other, prior.Cause, PlaybookRetirePorts, repairDone, time.Minute)
	}
	box, _ := h.box()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})

	why, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name)
	if err != nil {
		t.Fatalf("rails: %v", err)
	}
	if !blocked || why == "" {
		t.Fatalf("a wave of %d did not pause the cause (blocked=%v, why=%q)", pol.WaveLimit, blocked, why)
	}
}

// One repair per box at a time. Not a convention — the unique index enforces
// it, because two repairs of one machine would race over its ssh session, its
// env and its relays.
func TestOnlyOneRepairRunsPerBox(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO entry_repairs (incident_id, box, cause, playbook, decided_by)
		VALUES ($1, $2, $3, $4, 'auto')
	`, inc.ID, box, inc.Cause, PlaybookRetirePorts); err != nil {
		t.Fatalf("book running repair: %v", err)
	}

	err := NewMechanic(h.svc, nil).run(ctx, inc, PlaybookRetirePorts, DecidedAuto)
	if !errors.Is(err, ErrRepairBusy) {
		t.Fatalf("err = %v, want ErrRepairBusy", err)
	}
	// And the rails say the same thing, so the timed pass skips it quietly
	// instead of booking an attempt that fails.
	if _, blocked, err := NewMechanic(h.svc, nil).rails(ctx, inc, PlaybookForCause(inc.Cause).Name); err != nil || !blocked {
		t.Fatalf("rails let a second repair through (blocked=%v, err=%v)", blocked, err)
	}
}

// The cheap repair: the banned ports leave the pool and the box keeps the ones
// that answer. The machine is innocent and is not touched.
func TestRetiringPortsLeavesTheBoxItsWorkingRelays(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	banned := h.relay(box, KindPersonal)
	h.admit(banned)
	survivor := h.relay(box, KindPersonal)
	h.admit(survivor)
	inc := h.openIncident(box, CausePortsBanned, map[string]string{banned: "ip_banned"})

	outcome, err := NewMechanic(h.svc, nil).retirePorts(ctx, inc)
	if err != nil {
		t.Fatalf("retire ports: %v", err)
	}
	if outcome == "" {
		t.Error("the repair said nothing about what it did")
	}
	relays, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range relays {
		switch r.Name {
		case banned:
			if r.Status != "dead" {
				t.Errorf("the banned relay is still %q", r.Status)
			}
		case survivor:
			if r.Status != StatusAlive {
				t.Errorf("the working relay was taken out too: %q", r.Status)
			}
		}
	}
}

// And it refuses to empty a box. Every live relay banned is not a port
// problem, it is the address — and quietly retiring the machine here would
// skip the make-before-break the pool requires.
func TestRetiringPortsRefusesToEmptyABox(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	only := h.relay(box, KindPersonal)
	h.admit(only)
	inc := h.openIncident(box, CausePortsBanned, map[string]string{only: "ip_banned"})

	if _, err := NewMechanic(h.svc, nil).retirePorts(ctx, inc); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	relays, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range relays {
		if r.Name == only && r.Status != StatusAlive {
			t.Errorf("the box's only relay was taken out anyway: %q", r.Status)
		}
	}
}

// A free repair the dispatcher put on `auto` is one the hub finishes by
// itself, end to end: booked, acted on, and the incident closed with what it
// did.
func TestThePassRepairsAnAutoCauseByItself(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	banned := h.relay(box, KindPersonal)
	h.admit(banned)
	survivor := h.relay(box, KindPersonal)
	h.admit(survivor)
	inc := h.openIncident(box, CausePortsBanned, map[string]string{banned: "ip_banned"})
	// The round that confirms it, started after the incident opened: the hub
	// does not retire a port on the reading it opened on (portsSettled).
	next := inc.OpenedAt.Add(30 * time.Second)
	h.svc.SetVerdicts(&fakeTimedVerdicts{rows: []timedVerdict{{banned, "ip_banned", next}, {survivor, "clean", next}}})
	t.Cleanup(func() { h.svc.SetVerdicts(nil) })

	started, _ := NewMechanic(h.svc, nil).consider(ctx, inc)
	if !started {
		t.Fatal("the hub did not repair a cause its dispatcher put on auto")
	}
	// The repair runs in its own goroutine; wait on the condition, not on a
	// guessed delay.
	deadline := time.Now().Add(10 * time.Second)
	for {
		after, err := h.svc.Incident(ctx, inc.ID)
		if err != nil {
			t.Fatalf("read incident: %v", err)
		}
		if after.Status == IncidentResolved {
			if after.Outcome == "" {
				t.Error("a resolved incident says nothing about how")
			}
			if after.DecidedBy != DecidedAuto {
				t.Errorf("decidedBy = %q, want auto", after.DecidedBy)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the repair never finished: incident is %q", after.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	repairs, err := h.svc.Repairs(ctx, inc.ID)
	if err != nil {
		t.Fatalf("repairs: %v", err)
	}
	if len(repairs) != 1 || repairs[0].Status != repairDone {
		t.Fatalf("repairs = %+v, want one done", repairs)
	}
}

// A cause the owner set to `ask` waits for a person. The pass must walk past
// it without touching it, every minute, forever.
func TestThePassLeavesAnAskCauseAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_incidents WHERE pool = $1`, DomainPool)
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseShortOfDomains)
	})
	if err := h.svc.SetPlaybookMode(ctx, CauseShortOfDomains, ModeAsk, "test"); err != nil {
		t.Fatal(err)
	}
	opened, err := h.svc.openPool(ctx, DomainPool, CauseShortOfDomains, "тест", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	inc := *opened

	started, handed := NewMechanic(h.svc, nil).consider(ctx, inc)
	if started || handed {
		t.Fatalf("the pass touched an `ask` incident (started=%v, handed=%v)", started, handed)
	}
	after, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if after.Status != IncidentOpen || after.Playbook != "" {
		t.Errorf("incident moved: status %q, playbook %q", after.Status, after.Playbook)
	}
}

// A cause nobody wrote a playbook for is refused in so many words. Reaching
// for the nearest-looking repair would be worse than admitting it.
func TestStartingARepairForACauseWithNoPlaybookIsRefused(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	// `other` is a real death reason the dispatcher knows no repair for, and
	// there is no sensible one to write: it is what the hub records when a
	// relay died for a reason nobody classified. (This test used to use
	// provider_down, which П9.6б gave a playbook — a reboot through the
	// hoster's API, on the cause the grid of П9.4 made nameable.)
	inc := h.openIncident(box, DeathOther, nil)

	err := NewMechanic(h.svc, nil).Start(ctx, inc.ID, "admin-1", "")
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

// The console's button is what `ask` is for: the person overrides the mode,
// and the record says a person decided it.
func TestTheConsoleButtonRunsAnAskRepairAndRecordsWhoDecided(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	banned := h.relay(box, KindPersonal)
	h.admit(banned)
	h.admit(h.relay(box, KindPersonal))
	// The button is exercised on a free repair: ports_banned is
	// on `auto`. To exercise the button itself the incident is started by
	// hand on the free repair, and what is asserted is the bookkeeping.
	inc := h.openIncident(box, CausePortsBanned, map[string]string{banned: "ip_banned"})

	if err := NewMechanic(h.svc, nil).Start(ctx, inc.ID, "admin-1", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	repairs, err := h.svc.Repairs(ctx, inc.ID)
	if err != nil {
		t.Fatalf("repairs: %v", err)
	}
	if len(repairs) != 1 {
		t.Fatalf("repairs = %d, want 1", len(repairs))
	}
	if repairs[0].DecidedBy != DecidedAsk {
		t.Errorf("decidedBy = %q, want ask", repairs[0].DecidedBy)
	}
}

// The cross: a person says it is not worth acting on, and it closes as
// dismissed — told apart from a repair's own `resolved`, because П9.8 reads
// the difference.
func TestDismissingAnIncidentClosesItAsDismissed(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})

	if err := h.svc.Dismiss(ctx, inc.ID, ""); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	after, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if after.Status != IncidentDismissed {
		t.Errorf("status = %q, want dismissed", after.Status)
	}
	if after.ClosedAt == nil || after.Outcome == "" {
		t.Error("a dismissed incident carries neither a time nor a word about why")
	}
}

// The detector closes what no longer holds — but not an incident a person was
// asked about. A box whose relays are all dead has no evidence at all, and
// "no evidence" is not "it is fine now".
func TestTheDetectorKeepsAnIncidentItHandedToAPerson(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	if err := h.svc.HandOff(ctx, inc.ID, "пробовали трижды"); err != nil {
		t.Fatalf("hand over: %v", err)
	}

	// No verdicts at all: the box reads as unjudged.
	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{}})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}
	after, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if after.Status != IncidentOpen {
		t.Errorf("status = %q: the queue emptied itself while nobody acted", after.Status)
	}
}

// A re-key swaps every per-relay secret the protocol shows and nothing about
// the machine's layout: the port and the loopback block are the box's, the
// covers are a different repair's.
func TestRotatingRealityKeepsThePortAndTheCovers(t *testing.T) {
	before, err := newIdentity(443, 20000)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	after, err := before.rotatedReality()
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if after.PublicKey == before.PublicKey || after.ShortID == before.ShortID || after.UUID == before.UUID {
		t.Error("a rotation that leaves a credential in place is not a rotation")
	}
	if after.Port != before.Port || after.LocalPortBase != before.LocalPortBase {
		t.Error("the rotation moved the box's port layout")
	}
	if len(after.Covers) != len(before.Covers) || after.Covers[0] != before.Covers[0] {
		t.Errorf("covers changed: %v → %v", before.Covers, after.Covers)
	}
}

// And it refuses a relay that has no Reality half, instead of succeeding
// without doing anything — a repair that reports success having changed
// nothing is worse than one that fails.
func TestRotatingRealityRefusesAVoiceRelay(t *testing.T) {
	voice, err := newVoiceIdentity(443, 20000)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := voice.rotatedReality(); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

// A hub with no provisioner cannot touch a machine, and says so. The repairs
// that only move rows in the hub still work.
func TestARepairThatNeedsAMachineSaysSoWithoutOne(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CauseFingerprint, map[string]string{box + "-1": "fingerprint"})

	if _, err := NewMechanic(h.svc, nil).rotateReality(ctx, inc); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

// A restart leaves rows claiming a repair is running, and that row holds the
// box's only lock. The sweep is the same one entry_jobs has had since П6.15.
func TestAbandoningRepairsAfterARestart(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()
	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO entry_repairs (incident_id, box, cause, playbook, decided_by)
		VALUES ($1, $2, $3, $4, 'auto')
	`, inc.ID, box, inc.Cause, PlaybookRetirePorts); err != nil {
		t.Fatalf("book running repair: %v", err)
	}

	if _, err := h.svc.AbandonRunningRepairs(ctx); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	repairs, err := h.svc.Repairs(ctx, inc.ID)
	if err != nil {
		t.Fatalf("repairs: %v", err)
	}
	if len(repairs) != 1 || repairs[0].Status != repairFailed || repairs[0].Error == "" {
		t.Fatalf("repairs = %+v, want one failed with a reason", repairs)
	}
}
