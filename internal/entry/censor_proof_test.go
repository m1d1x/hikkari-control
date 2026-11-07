package entry

import (
	"context"
	"errors"
	"testing"
	"time"
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
