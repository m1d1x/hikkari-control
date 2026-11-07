package entry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A box retired by hand for the censor needs the vantage's word. On 02.11
// eun16 and eun17 were written off as ip_banned on two testers' timeouts and
// came back clean from Russia on 07.11.
func TestABanIsTheProbersCallNotTheTesters(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	relay := ph.relay(box, KindPersonal)
	ph.admit(relay)

	// The prober sees it clean: not a ban, whatever the testers saw.
	ph.vantage(VerdictClean)
	if _, err := ph.svc.ProveCensorDeath(ctx, box, DeathIPBanned); !errors.Is(err, ErrRejected) {
		t.Fatalf("a clean relay written off as banned: err = %v, want a refusal", err)
	}

	// Another reason is the owner's to give: nothing is asked.
	if v, err := ph.svc.ProveCensorDeath(ctx, box, DeathRotated); err != nil || v != nil {
		t.Fatalf("a rotation asked the prober: v=%v err=%v", v, err)
	}

	// The prober says banned: the write-off stands on its verdict.
	ph.vantage(VerdictIPBanned)
	v, err := ph.svc.ProveCensorDeath(ctx, box, DeathIPBanned)
	if err != nil {
		t.Fatalf("a banned relay: %v", err)
	}
	if v[relay] != VerdictIPBanned {
		t.Fatalf("verdicts %v, want %s banned", v, relay)
	}
}

// No answer is no evidence: the write-off is refused and the task nobody waits
// for any more is taken back off the queue.
func TestABanWithoutAVerdictIsRefused(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	relay := ph.relay(box, KindPersonal)
	ph.admit(relay)

	timeout, poll := censorProofTimeout, censorProofPoll
	censorProofTimeout, censorProofPoll = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { censorProofTimeout, censorProofPoll = timeout, poll })

	if _, err := ph.svc.ProveCensorDeath(ctx, box, DeathIPBanned); !errors.Is(err, ErrRejected) {
		t.Fatalf("no verdict: err = %v, want a refusal", err)
	}
	var queued int
	if err := ph.pool.QueryRow(ctx,
		`SELECT count(*) FROM entry_probe_tasks WHERE status = 'queued' AND $1 = ANY(relays)`, relay).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("%d probe tasks left queued for a verdict nobody reads", queued)
	}
}

// One point's refusal against another's clean is a tie, not a verdict: the
// relay reads unconfirmed — neither admitted nor refused.
func TestARefusalOnePointGivesIsUnconfirmed(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	relay := ph.relay(box, KindPersonal)
	ph.admit(relay)
	ph.livePoints(testVantage, testVantage2)

	go func() {
		for i, slot := range []string{testVantage, testVantage2} {
			verdict := []string{VerdictIPBanned, VerdictClean}[i]
			for j := 0; j < 200; j++ {
				task, err := ph.svc.ClaimProbeTask(ctx, slot, TaskAdmission)
				if err == nil && task != nil {
					_ = ph.svc.FinishProbeTask(ctx, task.ID, slot, map[string]string{relay: verdict}, nil)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	v, err := ph.svc.askConfirmed(ctx, uuid.Nil, []string{relay}, time.Now().Add(10*time.Second), 20*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v[relay] != verdictUnconfirmed {
		t.Fatalf("verdict %q, want %s", v[relay], verdictUnconfirmed)
	}
}

// A point abroad claims no admission probe: its "clean" says nothing about a
// block in Russia. Its bench runs are its own.
func TestAPointAbroadDoesNotJudgeABan(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	if _, err := ph.pool.Exec(ctx, `
		INSERT INTO entry_vantages (slot, title, in_russia) VALUES ('lv-test', 'abroad', false)
		ON CONFLICT (slot) DO UPDATE SET status = 'active', retired_at = NULL, in_russia = false`); err != nil {
		t.Fatal(err)
	}
	box, _ := ph.harness.box()
	relay := ph.relay(box, KindPersonal)
	ph.admit(relay)
	task, err := ph.svc.createProbeTask(ctx, uuid.Nil, []string{relay}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = ph.pool.Exec(context.Background(), `DELETE FROM entry_probe_tasks WHERE id = $1`, task) })
	got, err := ph.svc.ClaimProbeTask(ctx, "lv-test", TaskAdmission)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("the point abroad claimed admission task %s", got.ID)
	}
}

// With one point in Russia live and no testers' reports, a refusal is one
// instance: not a verdict (owner, 07.11: two instances, a prober among them).
// The wait does not hang on points that are not there.
func TestTheOnlyPointsRefusalAloneIsNotAVerdict(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	relay := ph.relay(box, KindPersonal)
	ph.admit(relay)
	ph.livePoints(testVantage)

	go func() {
		for j := 0; j < 200; j++ {
			task, err := ph.svc.ClaimProbeTask(ctx, testVantage, TaskAdmission)
			if err == nil && task != nil {
				_ = ph.svc.FinishProbeTask(ctx, task.ID, testVantage, map[string]string{relay: VerdictIPBanned}, nil)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	start := time.Now()
	v, err := ph.svc.askConfirmed(ctx, uuid.Nil, []string{relay}, time.Now().Add(10*time.Second), 20*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v[relay] != verdictUnconfirmed {
		t.Fatalf("verdict %q, want %s: one point without the testers is one instance", v[relay], verdictUnconfirmed)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("waited %s for points that are not there", time.Since(start))
	}
}

// Two instances: two points in Russia refusing are a verdict.
func TestTwoPointsRefusingAreAVerdict(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	relay := ph.relay(box, KindPersonal)
	ph.admit(relay)
	ph.livePoints(testVantage, testVantage2)

	go func() {
		for _, slot := range []string{testVantage, testVantage2} {
			for j := 0; j < 200; j++ {
				task, err := ph.svc.ClaimProbeTask(ctx, slot, TaskAdmission)
				if err == nil && task != nil {
					_ = ph.svc.FinishProbeTask(ctx, task.ID, slot, map[string]string{relay: VerdictIPBanned}, nil)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	v, err := ph.svc.askConfirmed(ctx, uuid.Nil, []string{relay}, time.Now().Add(10*time.Second), 20*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v[relay] != VerdictIPBanned {
		t.Fatalf("verdict %q, want %s from two points", v[relay], VerdictIPBanned)
	}
}
