package entry

// secondchance_test.go covers the middle step of a consumable's life (roadmap
// 42 П9.11): a machine and a cover site out of the pool on suspicion, back
// after clean reprobes on separate days, written off when the second chance
// fails. The domain's half is in reprobe_test.go, where §11 began.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// suspectBoxHarness is a hosting harness with one machine in service, no
// machine reprobe left over from an earlier test, and the machine evicted.
func suspectBoxHarness(t *testing.T) (*hostHarness, string) {
	t.Helper()
	h := newHostHarness(t)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `DELETE FROM entry_probe_tasks WHERE kind = 'box'`); err != nil {
		t.Fatal(err)
	}
	h.isolateWave()
	box := h.inService()
	if _, _, err := h.svc.SuspectBox(ctx, box, CauseIPBanned); err != nil {
		t.Fatalf("evict: %v", err)
	}
	return h, box
}

// boxChance is the machine's second chance as the policy holds it.
func (h *harness) boxChance() SecondChance {
	h.t.Helper()
	p, err := h.svc.DetectionPolicy(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return p.Box
}

// later moves the last reprobe of every suspect of a kind `by` into the
// past — the eviction itself, for one never reprobed.
func (h *harness) later(kind string, by time.Duration) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(), `
		UPDATE entry_suspects SET
			last_probe_at = last_probe_at - make_interval(secs => $2),
			since = CASE WHEN last_probe_at IS NULL THEN since - make_interval(secs => $2) ELSE since END
		WHERE kind = $1 AND status = 'suspect'`, kind, by.Seconds()); err != nil {
		h.t.Fatal(err)
	}
}

// answerBoxReprobe claims the queued machine reprobe as a vantage would and
// answers every relay it carries with verdict.
func (h *harness) answerBoxReprobe(box, verdict string) {
	h.t.Helper()
	ctx := context.Background()
	task, err := h.svc.ClaimProbeTask(ctx, testVantage, TaskBox)
	if err != nil {
		h.t.Fatal(err)
	}
	if task == nil {
		h.t.Fatal("no machine reprobe queued")
	}
	v := map[string]string{}
	for _, r := range task.Relays {
		if r.Box != box || r.Address == "" {
			h.t.Fatalf("reprobe carries %+v, want the relays of %s with their address", r, box)
		}
		v[r.Name] = verdict
	}
	if err := h.svc.FinishProbeTask(ctx, task.ID, testVantage, v, nil); err != nil {
		h.t.Fatalf("finish reprobe: %v", err)
	}
}

// boxReprobeDay runs one probe interval: the pass queues, the vantage answers.
func (h *harness) boxReprobeDay(box, verdict string) {
	h.t.Helper()
	c := h.boxChance()
	h.later(SuspectBox, c.ProbeEvery+time.Hour)
	if n, err := h.svc.reprobeSuspectBoxes(context.Background(), c); err != nil || n < 1 {
		h.t.Fatalf("queued %d machine reprobes (%v), want this one", n, err)
	}
	h.answerBoxReprobe(box, verdict)
}

func (h *harness) suspicion(kind, subject string) (status string, streak, fails int) {
	h.t.Helper()
	if err := h.pool.QueryRow(context.Background(), `
		SELECT status, clean_streak, failed_probes FROM entry_suspects
		WHERE kind = $1 AND subject = $2 ORDER BY since DESC LIMIT 1`, kind, subject,
	).Scan(&status, &streak, &fails); err != nil {
		h.t.Fatalf("ledger %s %s: %v", kind, subject, err)
	}
	return status, streak, fails
}

// An evicted machine is out of everything that reads the pool: no relay of it
// is handed out, the routine rounds do not watch it, a rollout does not touch
// it, and the fleet does not count it as stock.
func TestASuspectMachineIsOutOfThePoolTheRoundsAndTheFleet(t *testing.T) {
	h, box := suspectBoxHarness(t)
	ctx := context.Background()

	if h.aliveOn(box) != 0 {
		t.Fatal("the suspect machine still hands out relays")
	}
	list, err := h.svc.Suspects(ctx, 0)
	if err != nil || !slices.ContainsFunc(list, func(x Suspect) bool {
		return x.Kind == SuspectBox && x.Subject == box && x.Status == suspectOpen && x.Reason == CauseIPBanned
	}) {
		t.Fatalf("the console's ledger does not list %s (%v)", box, err)
	}
	watched, err := h.svc.WatchedProbeRelays(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range watched {
		if r.Box == box {
			t.Fatalf("suspect relay %s rides the routine rounds", r.Name)
		}
	}
	fleet, err := h.svc.fleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range fleet {
		if b.name == box {
			t.Fatal("a rollout would take the suspect machine")
		}
	}
	var live int
	if err := h.pool.QueryRow(ctx, `
		SELECT count(*) FROM entry_boxes b WHERE b.name = $1
		  AND EXISTS (SELECT 1 FROM entry_relays r WHERE r.box = b.name AND r.status = 'alive')`, box,
	).Scan(&live); err != nil || live != 0 {
		t.Fatalf("the fleet counts the suspect machine as live (%d, %v)", live, err)
	}
	if h.retired(box) {
		t.Fatal("eviction retired the machine")
	}
}

// Clean reprobes in a row, a probe interval apart, bring the machine back:
// its relays alive again, the ledger closed as returned. One clean read is not
// enough, a reprobe nobody has answered holds the next one back, and a reading
// that says nothing moves nothing.
func TestASuspectMachineComesBackAfterCleanReprobesOnSeparateDays(t *testing.T) {
	h, box := suspectBoxHarness(t)
	ctx := context.Background()
	c := h.boxChance()
	if c.ReturnClean != 2 || c.DeadFails != 2 || c.ProbeEvery != 24*time.Hour || c.DeadAfter != 72*time.Hour {
		t.Fatalf("box defaults = %+v, want daily, back after 2, dead after 2 or 3 days", c)
	}

	// Not asked about before its interval is up.
	if n, err := h.svc.reprobeSuspectBoxes(ctx, c); err != nil || n != 0 {
		t.Fatalf("queued %d (%v) right after the eviction, want none", n, err)
	}
	h.boxReprobeDay(box, "clean")
	if status, streak, _ := h.suspicion(SuspectBox, box); status != suspectOpen || streak != 1 {
		t.Fatalf("one clean read: %s streak %d, want still suspect with 1", status, streak)
	}
	if h.aliveOn(box) != 0 {
		t.Fatal("one clean read returned the machine")
	}
	h.boxReprobeDay(box, "network_down")
	if status, streak, fails := h.suspicion(SuspectBox, box); status != suspectOpen || streak != 1 || fails != 0 {
		t.Fatalf("network_down: %s streak %d fails %d, want nothing moved", status, streak, fails)
	}

	// Queued and not answered: the next pass waits for it.
	h.later(SuspectBox, c.ProbeEvery+time.Hour)
	if n, err := h.svc.reprobeSuspectBoxes(ctx, c); err != nil || n != 1 {
		t.Fatalf("queued %d (%v), want one", n, err)
	}
	h.later(SuspectBox, c.ProbeEvery+time.Hour)
	if n, err := h.svc.reprobeSuspectBoxes(ctx, c); err != nil || n != 0 {
		t.Fatalf("with a reprobe still waiting queued %d (%v), want none", n, err)
	}
	h.answerBoxReprobe(box, "clean")

	if status, _, _ := h.suspicion(SuspectBox, box); status != suspectReturned {
		t.Fatalf("after %d clean reads: %s, want returned", c.ReturnClean, status)
	}
	if h.aliveOn(box) == 0 {
		t.Fatal("the returned machine hands out nothing")
	}
	if h.retired(box) {
		t.Fatal("the returned machine was retired")
	}
}

// Failed reprobes write the machine off, and the mechanic's pass hands it
// back: retired with the cause that evicted it, deleted at the hoster.
func TestASuspectMachineThatKeepsFailingIsHandedBack(t *testing.T) {
	h, box := suspectBoxHarness(t)
	ctx := context.Background()
	instance := h.instanceOf(box)
	c := h.boxChance()

	h.boxReprobeDay(box, "clean")
	for i := 0; i < c.DeadFails; i++ {
		h.boxReprobeDay(box, "ip_banned")
	}
	if status, _, fails := h.suspicion(SuspectBox, box); status != suspectDead || fails != c.DeadFails {
		t.Fatalf("after %d failed reads: %s with %d, want dead", c.DeadFails, status, fails)
	}
	// The ledger decided; nothing has touched the machine yet.
	if h.retired(box) {
		t.Fatal("the machine was retired inside the verdict's transaction")
	}

	gone, err := h.prov.retireDeadSuspects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(gone, box) || !h.retired(box) {
		t.Fatalf("handed back %v, want %s retired", gone, box)
	}
	if !slices.Contains(h.host.deletedIDs(), instance) {
		t.Errorf("deleted = %v, want the instance %s", h.host.deletedIDs(), instance)
	}
	var reason string
	if err := h.pool.QueryRow(ctx, `SELECT retired_reason FROM entry_boxes WHERE name = $1`, box).Scan(&reason); err != nil || reason != CauseIPBanned {
		t.Errorf("retired as %q (%v), want ip_banned", reason, err)
	}
	// Once is enough.
	if again, err := h.prov.retireDeadSuspects(ctx); err != nil || slices.Contains(again, box) {
		t.Errorf("second pass handed back %v (%v)", again, err)
	}
}

// A machine that never clears in time is written off at the deadline.
func TestASuspectMachinePastItsDeadlineIsWrittenOff(t *testing.T) {
	h, box := suspectBoxHarness(t)
	ctx := context.Background()
	c := h.boxChance()
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_suspects SET since = now() - make_interval(secs => $2) - interval '1 hour'
		WHERE kind = 'box' AND subject = $1`, box, c.DeadAfter.Seconds()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.SecondChancePass(ctx); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := h.suspicion(SuspectBox, box); status != suspectDead {
		t.Fatalf("%s past the deadline, want dead", status)
	}
	if gone, err := h.prov.retireDeadSuspects(ctx); err != nil || !slices.Contains(gone, box) {
		t.Fatalf("handed back %v (%v), want %s", gone, err, box)
	}
}

// A machine reprobe reads strictly: all judged relays healthy is clean, any
// blocked one fails it, and nothing judged is nothing learned.
func TestABoxReprobeReadsStrictly(t *testing.T) {
	relays := []string{"b-1", "b-2"}
	for _, tc := range []struct {
		v    map[string]string
		want reading
	}{
		{map[string]string{"b-1": "clean", "b-2": "reality_wins"}, readClean},
		{map[string]string{"b-1": "clean", "b-2": "network_down"}, readClean},
		{map[string]string{"b-1": "clean", "b-2": "ip_banned"}, readFailed},
		{map[string]string{"b-1": "network_down", "b-2": "unknown"}, readNothing},
		{map[string]string{"x-9": "clean"}, readNothing},
	} {
		if got := boxReading(tc.v, relays); got != tc.want {
			t.Errorf("%v → %v, want %v", tc.v, got, tc.want)
		}
	}
}

// A dropped cover is suspect: reprobed from the hub on its own interval, back
// after clean reads spanning a day, struck after enough failures — and a
// struck cover is not borrowed by a new relay.
func TestACoverSecondChance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Cover
	if c.ReturnClean != 3 || c.ProbeEvery != 12*time.Hour {
		t.Fatalf("cover defaults = %+v, want every 12h, back after 3", c)
	}
	back, gone := DefaultCovers[1], DefaultCovers[2]
	if err := h.svc.suspectCovers(ctx, []string{back, gone}); err != nil {
		t.Fatal(err)
	}
	h.svc.coverAnswers = func(host string) bool { return host == back }

	pass := func() {
		t.Helper()
		h.later(SuspectCover, c.ProbeEvery+time.Hour)
		if _, err := h.svc.reprobeSuspectCovers(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < c.ReturnClean; i++ {
		pass()
	}
	if status, streak, _ := h.suspicion(SuspectCover, back); status != suspectOpen || streak != c.ReturnClean-1 {
		t.Fatalf("%s streak %d after %d clean reads, want still suspect", status, streak, c.ReturnClean-1)
	}
	ledger, err := h.svc.coverLedger(ctx)
	if err != nil || ledger.usable(back) {
		t.Fatalf("a suspect cover reads usable (%v)", err)
	}
	pass()
	if status, _, _ := h.suspicion(SuspectCover, back); status != suspectReturned {
		t.Fatalf("%s after %d clean reads, want returned", status, c.ReturnClean)
	}
	for i := c.ReturnClean; i < c.DeadFails; i++ {
		pass()
	}
	if status, _, fails := h.suspicion(SuspectCover, gone); status != suspectDead || fails != c.DeadFails {
		t.Fatalf("%s with %d fails, want dead after %d", status, fails, c.DeadFails)
	}
	ledger, err = h.svc.coverLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ledger.usable(back) || ledger.usable(gone) || slices.Contains(ledger.proven(), gone) {
		t.Fatalf("ledger %v: want %s usable and %s struck", ledger, back, gone)
	}

	// A new relay borrows the proven covers that are left, in race order.
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	id, err := h.svc.RelayIdentity(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(id.Covers, gone) || len(id.Covers) != len(DefaultCovers)-1 {
		t.Fatalf("new relay covers = %v, want the proven ones without %s", id.Covers, gone)
	}
}

// A cover under its second chance is out of service at once (П9.11): a new
// relay is not born with it. Seen on the stand 27.10 — ams8 and ams9 came up
// racing www.samsung.com hours after rotate_dest had dropped it. When every
// proven cover is suspect, the suspects are the last there are and a new
// relay still gets them, as lastProvenCovers keeps them from being struck.
func TestANewRelayDoesNotBorrowASuspectCover(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	suspect := DefaultCovers[0]
	if err := h.svc.suspectCovers(ctx, []string{suspect}); err != nil {
		t.Fatal(err)
	}
	box, _ := h.box()
	id, err := h.svc.RelayIdentity(ctx, h.relay(box, KindPersonal))
	if err != nil {
		t.Fatal(err)
	}
	if want := DefaultCovers[1:]; !slices.Equal(id.Covers, want) {
		t.Fatalf("new relay covers = %v with %s suspect, want %v", id.Covers, suspect, want)
	}

	if err := h.svc.suspectCovers(ctx, DefaultCovers); err != nil {
		t.Fatal(err)
	}
	box, _ = h.box()
	id, err = h.svc.RelayIdentity(ctx, h.relay(box, KindPersonal))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(id.Covers, DefaultCovers) {
		t.Fatalf("new relay covers = %v with every cover suspect, want the last proven %v", id.Covers, DefaultCovers)
	}
}

// When every proven cover is suspect at once, none of them is struck: that is
// our network, not three sites dying together, and striking them would leave
// relays nothing to race.
func TestTheLastProvenCoversAreNotStruck(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.suspectCovers(ctx, DefaultCovers); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE entry_suspects SET since = now() - make_interval(secs => $1) - interval '1 hour'
		WHERE kind = 'cover'`, p.Cover.DeadAfter.Seconds()); err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.expire(ctx, SuspectCover, p.Cover); err != nil || n != 0 {
		t.Fatalf("struck %d (%v) with every proven cover suspect, want none", n, err)
	}
	h.svc.coverAnswers = func(string) bool { return false }
	for i := 0; i < p.Cover.DeadFails+1; i++ {
		h.later(SuspectCover, p.Cover.ProbeEvery+time.Hour)
		if _, err := h.svc.reprobeSuspectCovers(ctx, p.Cover); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range DefaultCovers {
		if status, _, _ := h.suspicion(SuspectCover, c); status != suspectOpen {
			t.Errorf("%s is %s, want still suspect", c, status)
		}
	}
}

// The knobs are the owner's, per kind, and the migration's rules hold
// whatever the screen sends: a return spans most of a day, the deadline
// leaves room for one, and only the three kinds have a second chance.
func TestTheSecondChanceKnobsKeepTheirRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	before, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restore := map[string]SecondChancePatch{}
		for _, kind := range SuspectKinds {
			c := before.Chance(kind)
			every, after := int(c.ProbeEvery/time.Second), int(c.DeadAfter/time.Second)
			restore[kind] = SecondChancePatch{ProbeEverySeconds: &every, ReturnClean: &c.ReturnClean,
				DeadFails: &c.DeadFails, DeadAfterSeconds: &after}
		}
		if _, err := h.svc.SaveDetectionPolicy(context.Background(), DetectionPolicyPatch{SecondChance: restore}, "test"); err != nil {
			t.Errorf("restore: %v", err)
		}
	})

	p, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{
		SecondChance: map[string]SecondChancePatch{SuspectBox: {ReturnClean: ptr(3), DeadAfterSeconds: ptr(4 * 86400)}},
	}, "test")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if p.Box.ReturnClean != 3 || p.Box.DeadAfter != 96*time.Hour || p.Domain != before.Domain || p.Cover != before.Cover {
		t.Fatalf("after the save: %+v", p)
	}

	for name, patch := range map[string]map[string]SecondChancePatch{
		"a return inside an hour":    {SuspectDomain: {ProbeEverySeconds: ptr(3600), ReturnClean: ptr(2)}},
		"a deadline before a return": {SuspectBox: {DeadAfterSeconds: ptr(86400)}},
		"one clean read":             {SuspectCover: {ReturnClean: ptr(1)}},
		"a kind with no second step": {"key": {ReturnClean: ptr(2)}},
	} {
		if _, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{SecondChance: patch}, "test"); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	got, err := h.svc.readDetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Box != p.Box || got.Domain != before.Domain {
		t.Errorf("a refused save moved the knobs: %+v", got)
	}

	resp := policyResponse(got, nil)
	sc, ok := resp["secondChance"].(map[string]map[string]int)
	if !ok || sc[SuspectBox]["returnClean"] != 3 || sc[SuspectDomain]["probeEverySeconds"] != 86400 {
		t.Errorf("console shape = %v", resp["secondChance"])
	}
}

// The eviction's outcome says who replaces the machine: the fleet, which
// sizes the pool by its load.
func TestTheEvictionSaysWhoReplacesTheMachine(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box := h.inService()
	inc := h.openIncident(box, CauseIPBanned, map[string]string{box + "-1": "ip_banned"})
	out, err := NewMechanic(h.svc, h.prov).evictBox(ctx, inc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "замену добирает парк") || !strings.Contains(out, ProtoVlessRealityVision) {
		t.Errorf("outcome = %q", out)
	}
}

// The cover repairs keep the ledger: rotate_dest puts what it drops under
// suspicion, add_covers writes back only what the second chance cleared, and
// a cover add_covers finds dead with no word against it in the ledger (one
// dropped before the ledger existed) becomes suspect, so the detector stops
// calling the box short of it.
func TestTheCoverRepairsKeepTheLedger(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box := h.inService()
	nvidia, tesla := DefaultCovers[1], DefaultCovers[2]

	h.svc.coverAnswers = func(host string) bool { return host != nvidia }
	h.vantage("clean")
	if _, dropped, err := h.prov.RotateDest(ctx, box); err != nil || !slices.Equal(dropped, []string{nvidia}) {
		t.Fatalf("rotate_dest dropped %v (%v), want [%s]", dropped, err, nvidia)
	}
	if status, _, _ := h.suspicion(SuspectCover, nvidia); status != suspectOpen {
		t.Fatalf("dropped cover is %s, want suspect", status)
	}

	// Answering again is not enough while it is suspect.
	h.svc.coverAnswers = func(string) bool { return true }
	if _, _, err := h.prov.RestoreCovers(ctx, box); !errors.Is(err, ErrRejected) {
		t.Fatalf("add_covers with the cover still suspect: err = %v, want a refusal", err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE entry_suspects SET status = 'returned', ended_at = now()
		WHERE kind = 'cover' AND subject = $1 AND status = 'suspect'`, nvidia); err != nil {
		t.Fatal(err)
	}
	h.vantage("clean")
	if _, back, err := h.prov.RestoreCovers(ctx, box); err != nil || !slices.Equal(back, []string{nvidia}) {
		t.Fatalf("add_covers brought back %v (%v), want [%s]", back, err, nvidia)
	}

	// A cover missing with no ledger row, and dead.
	relay := box + "-1"
	id, err := h.svc.RelayIdentity(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}
	narrowed, err := id.withCovers([]string{DefaultCovers[0], nvidia})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.setRelayIdentity(ctx, relay, narrowed); err != nil {
		t.Fatal(err)
	}
	h.svc.coverAnswers = func(host string) bool { return host != tesla }
	if _, _, err := h.prov.RestoreCovers(ctx, box); !errors.Is(err, ErrRejected) {
		t.Fatalf("add_covers with the missing cover dead: err = %v, want a refusal", err)
	}
	if status, _, _ := h.suspicion(SuspectCover, tesla); status != suspectOpen {
		t.Fatalf("a cover found dead by add_covers is %s, want suspect", status)
	}
}
