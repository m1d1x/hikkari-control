package entry

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// reprobeHarness is a harness with no domain reprobe left over from an earlier
// test (one waiting blocks the next pass), no earlier test's burned names in
// the second chance's ledger, and exactly one live XHTTP relay to lend its
// address, which it returns.
func reprobeHarness(t *testing.T) (*harness, string) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, `DELETE FROM entry_probe_tasks WHERE kind = 'domain'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM entry_suspects WHERE kind = 'domain'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_relays SET status = 'dead', died_at = now(), died_reason = 'other'
		WHERE status = 'alive' AND box IN (SELECT name FROM entry_boxes WHERE proto = 'vless-xhttp')`); err != nil {
		t.Fatal(err)
	}
	box, _ := h.xhttpBox()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)
	return h, relay
}

// burned puts a name in the ledger burned `hoursAgo` hours ago, under
// suspicion since then — as burnBoxDomainClear leaves it.
func (h *harness) burned(hoursAgo int) string {
	h.t.Helper()
	d := fmt.Sprintf("b%s.lol", uuid.NewString()[:8])
	if _, err := h.pool.Exec(context.Background(), `
		INSERT INTO entry_domains (domain, tld, status, source, burned_at)
		VALUES ($1, 'lol', 'burned', 'seeded', now() - make_interval(hours => $2))`, d, hoursAgo); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.pool.Exec(context.Background(), `
		INSERT INTO entry_suspects (kind, subject, reason, since)
		VALUES ('domain', $1, 'domain_blocked', now() - make_interval(hours => $2))`, d, hoursAgo); err != nil {
		h.t.Fatal(err)
	}
	return d
}

// nextDay moves every reprobe a day and an hour into the past.
func (h *harness) nextDay() {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(), `
		UPDATE entry_suspects SET last_probe_at = last_probe_at - interval '25 hours'
		WHERE last_probe_at IS NOT NULL`); err != nil {
		h.t.Fatal(err)
	}
}

// domainChance is the domain's second chance as the policy holds it.
func (h *harness) domainChance() SecondChance {
	h.t.Helper()
	p, err := h.svc.DetectionPolicy(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return p.Domain
}

// answerReprobe claims the queued domain reprobe as a vantage would and
// answers every name it carries with verdict; it returns the names.
func (h *harness) answerReprobe(relay, verdict string) []string {
	h.t.Helper()
	ctx := context.Background()
	task, err := h.svc.ClaimProbeTask(ctx, testVantage, TaskDomain)
	if err != nil {
		h.t.Fatal(err)
	}
	if task == nil {
		h.t.Fatal("no domain reprobe queued")
	}
	if len(task.Relays) != 1 || task.Relays[0].Name != relay || task.Relays[0].Address == "" {
		h.t.Fatalf("reprobe lends %+v, want the live xhttp relay %s with its address", task.Relays, relay)
	}
	var p domainTaskParams
	if err := json.Unmarshal(task.Params, &p); err != nil {
		h.t.Fatal(err)
	}
	verdicts := map[string]string{}
	for _, d := range p.Domains {
		verdicts[d] = verdict
	}
	if err := h.svc.FinishProbeTask(ctx, task.ID, testVantage, verdicts, nil); err != nil {
		h.t.Fatalf("finish reprobe: %v", err)
	}
	return p.Domains
}

// reprobeDay runs one day: the pass queues, the vantage answers.
func (h *harness) reprobeDay(relay, verdict string) []string {
	h.t.Helper()
	h.nextDay()
	if _, err := h.svc.ReprobeBurnedDomains(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	return h.answerReprobe(relay, verdict)
}

func (h *harness) ledger(domain string) Domain {
	h.t.Helper()
	d, ok, err := h.svc.repo.domain(context.Background(), domain)
	if err != nil || !ok {
		h.t.Fatalf("ledger %s: ok=%v err=%v", domain, ok, err)
	}
	return d
}

// Three clean days in a row return a burned name to the pool as owned, and the
// next box draws it from there.
func TestReprobeReturnsABurnedDomainAfterThreeCleanDays(t *testing.T) {
	h, relay := reprobeHarness(t)
	ctx := context.Background()
	d := h.burned(48)
	streak := h.domainChance().ReturnClean
	if streak != 3 {
		t.Fatalf("domain return streak = %d, want the 072 rule of 3 as the default", streak)
	}

	for day := 1; day <= streak; day++ {
		if got := h.reprobeDay(relay, "clean"); !slices.Contains(got, d) {
			t.Fatalf("day %d: reprobe carried %v, not %s", day, got, d)
		}
		row := h.ledger(d)
		if day < streak {
			if row.Status != domainBurned || row.ReprobeStreak != day {
				t.Fatalf("day %d: %s streak %d, want burned with streak %d", day, row.Status, row.ReprobeStreak, day)
			}
			continue
		}
		if row.Status != domainOwned || row.ReprobeStreak != 0 {
			t.Fatalf("after %d clean days: %s streak %d, want owned with the streak reset", day, row.Status, row.ReprobeStreak)
		}
	}

	box, _ := h.xhttpBoxNoDomain(t)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	got, ok, err := h.svc.repo.claimFreeDomain(ctx, tx, box)
	if err != nil || !ok || got != d {
		t.Fatalf("pool draw = %q ok=%v err=%v, want the returned %s", got, ok, err, d)
	}
}

// One clean reading returns nothing; a blocked one starts the count over; a
// reading that says nothing about the name leaves the count alone.
func TestReprobeOneCleanReadDoesNotReturnAndABlockResets(t *testing.T) {
	h, relay := reprobeHarness(t)
	d := h.burned(48)

	h.reprobeDay(relay, "clean")
	if row := h.ledger(d); row.Status != domainBurned || row.ReprobeStreak != 1 {
		t.Fatalf("one clean read: %s streak %d, want still burned with streak 1", row.Status, row.ReprobeStreak)
	}
	h.reprobeDay(relay, "clean")
	h.reprobeDay(relay, "network_down")
	if row := h.ledger(d); row.Status != domainBurned || row.ReprobeStreak != 2 {
		t.Fatalf("network_down: %s streak %d, want the streak of 2 kept", row.Status, row.ReprobeStreak)
	}
	h.reprobeDay(relay, CauseDomainBlocked)
	if row := h.ledger(d); row.Status != domainBurned || row.ReprobeStreak != 0 {
		t.Fatalf("blocked: %s streak %d, want burned with the streak reset", row.Status, row.ReprobeStreak)
	}
	h.reprobeDay(relay, "clean")
	h.reprobeDay(relay, "clean")
	if row := h.ledger(d); row.Status != domainBurned {
		t.Fatalf("two clean days after a block returned the name (%s)", row.Status)
	}
}

// A name is asked about at most once a day: a second pass the same day queues
// nothing, a name burned less than a day ago waits its day, and a reprobe no
// vantage has taken yet holds the next one back rather than piling up.
func TestReprobeKeepsTheDailyRate(t *testing.T) {
	h, relay := reprobeHarness(t)
	ctx := context.Background()
	due := h.burned(48)
	fresh := h.burned(1)

	n, err := h.svc.ReprobeBurnedDomains(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queued %d names, want only the one burned two days ago", n)
	}
	got := h.answerReprobe(relay, "clean")
	if !slices.Equal(got, []string{due}) {
		t.Fatalf("reprobe carried %v, want [%s]", got, due)
	}
	if n, err := h.svc.ReprobeBurnedDomains(ctx); err != nil || n != 0 {
		t.Fatalf("second pass the same day queued %d (err %v), want none", n, err)
	}

	// A day on, both are due; nobody takes the reprobe, and the pass after
	// queues nothing more and stamps nothing.
	h.nextDay()
	if _, err := h.pool.Exec(ctx, `UPDATE entry_suspects SET since = since - interval '1 day' WHERE subject = $1`, fresh); err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.ReprobeBurnedDomains(ctx); err != nil || n != 2 {
		t.Fatalf("next day queued %d (err %v), want both names", n, err)
	}
	h.nextDay()
	if n, err := h.svc.ReprobeBurnedDomains(ctx); err != nil || n != 0 {
		t.Fatalf("with a reprobe still waiting the pass queued %d (err %v), want none", n, err)
	}
}

// With no live XHTTP relay there is no address to try a name against: nothing
// is queued and nothing is stamped asked. An excluded name is never asked.
func TestReprobeNeedsALiveXhttpAddressAndSkipsExcluded(t *testing.T) {
	h, relay := reprobeHarness(t)
	ctx := context.Background()
	d := h.burned(48)
	excluded := h.burned(48)
	if err := h.svc.repo.insertExclusion(ctx, excluded, "", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := h.svc.MarkDead(ctx, relay, DeathOther); err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.ReprobeBurnedDomains(ctx); err != nil || n != 0 {
		t.Fatalf("with no live xhttp relay queued %d (err %v), want none", n, err)
	}
	if row := h.ledger(d); row.LastReprobeAt != nil {
		t.Fatal("a name was stamped asked with nothing to ask it against")
	}

	box, _ := h.xhttpBox()
	live := h.relay(box, KindPersonal)
	h.admit(live)
	if n, err := h.svc.ReprobeBurnedDomains(ctx); err != nil || n != 1 {
		t.Fatalf("queued %d (err %v), want the one name not excluded", n, err)
	}
	if got := h.answerReprobe(live, "clean"); !slices.Equal(got, []string{d}) {
		t.Fatalf("reprobe carried %v, want [%s]", got, d)
	}
}

// A burned name stays out of the routine watch list: the relay that carried it
// is dead, and the name rides only its own reprobe task, never a probe round.
func TestABurnedDomainIsNotWatched(t *testing.T) {
	h, _ := reprobeHarness(t)
	allowDomainSpend(h.t, h.pool)
	ctx := context.Background()
	box, addr := h.xhttpBoxNoDomain(t)
	h.svc.SetDomainRegistrar(newFakeRegistrar(), DefaultDomainPolicy())
	h.svc.checkSpacing = 0
	if err := h.svc.SeedDomain(ctx, "harbor1234.lol"); err != nil {
		t.Fatal(err)
	}
	d, _, err := h.svc.AcquireDomain(ctx, box, addr)
	if err != nil {
		t.Fatal(err)
	}
	relay := h.relay(box, KindPersonal)
	h.admit(relay)
	if _, err := h.svc.MarkDead(ctx, relay, DeathDomainBlocked); err != nil {
		t.Fatal(err)
	}
	// What the replacement of a blocked domain does first (Ф6).
	if _, err := h.svc.burnBoxDomainClear(ctx, box); err != nil {
		t.Fatal(err)
	}
	if row := h.ledger(d); row.Status != domainBurned {
		t.Fatalf("%s after domain_blocked, want burned", row.Status)
	}
	watched, err := h.svc.WatchedProbeRelays(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range watched {
		if r.Host == d || r.Name == relay {
			t.Fatalf("burned %s is in the watch list via %s", d, r.Name)
		}
	}
}

// The second chance's last step for a name (П9.11): enough failed reprobes
// write it off — dead, never drawn, never asked about again, not renewed.
func TestABurnedDomainThatKeepsFailingIsWrittenOff(t *testing.T) {
	h, relay := reprobeHarness(t)
	ctx := context.Background()
	d := h.burned(48)
	fails := h.domainChance().DeadFails

	for day := 1; day <= fails; day++ {
		h.reprobeDay(relay, CauseDomainBlocked)
		row := h.ledger(d)
		if day < fails && row.Status != domainBurned {
			t.Fatalf("day %d: %s, want still burned", day, row.Status)
		}
	}
	if row := h.ledger(d); row.Status != domainDead {
		t.Fatalf("after %d failed reprobes: %s, want dead", fails, row.Status)
	}
	var status, outcome string
	if err := h.pool.QueryRow(ctx, `SELECT status, outcome FROM entry_suspects WHERE kind = 'domain' AND subject = $1`, d).
		Scan(&status, &outcome); err != nil {
		t.Fatal(err)
	}
	if status != suspectDead || outcome == "" {
		t.Fatalf("ledger %s %q, want dead with an outcome", status, outcome)
	}

	// Never asked about again.
	h.nextDay()
	if n, err := h.svc.ReprobeBurnedDomains(ctx); err != nil || n != 0 {
		t.Fatalf("a dead name was queued (%d, %v)", n, err)
	}
	// And a second suspicion of the same name is not opened: death is once.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := suspect(ctx, tx, SuspectDomain, d, CauseDomainBlocked); err != nil {
		t.Fatalf("suspect a dead name: %v", err)
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM entry_suspects WHERE subject = $1 AND status = 'suspect'`, d).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open suspicions of a dead name: %d (%v)", open, err)
	}
}

// A name that never clears in time is written off at the deadline, whatever
// its streak: the deadline is the owner's answer to "is it worth renewing".
func TestABurnedDomainPastItsDeadlineIsWrittenOff(t *testing.T) {
	h, relay := reprobeHarness(t)
	ctx := context.Background()
	d := h.burned(48)
	h.reprobeDay(relay, "clean")

	c := h.domainChance()
	if _, err := h.pool.Exec(ctx, `UPDATE entry_suspects SET since = now() - make_interval(secs => $2) - interval '1 hour'
		WHERE subject = $1`, d, c.DeadAfter.Seconds()); err != nil {
		t.Fatal(err)
	}
	dead, _, err := h.svc.SecondChancePass(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dead < 1 {
		t.Fatalf("written off %d, want the name past its deadline", dead)
	}
	if row := h.ledger(d); row.Status != domainDead {
		t.Fatalf("%s past the deadline, want dead", row.Status)
	}
}
