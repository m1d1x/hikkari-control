package entry

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Roadmap 42 П6.10: a death says why, a box goes back to its provider, and
// the hub renews its own paperwork.

func TestADeathSaysWhy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	names := h.personal(2)

	if _, err := h.svc.MarkDead(ctx, names[0], "banned-probably"); !errors.Is(err, ErrRejected) {
		t.Fatalf("a death with no known reason: %v, want ErrRejected", err)
	}
	if _, err := h.svc.MarkDead(ctx, names[0], ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("a death with no reason: %v, want ErrRejected", err)
	}
	var status string
	if err := h.pool.QueryRow(ctx, `SELECT status FROM entry_relays WHERE name = $1`, names[0]).Scan(&status); err != nil || status != "alive" {
		t.Fatalf("a refused death killed the relay: %s %v", status, err)
	}

	if _, err := h.svc.MarkDead(ctx, names[0], DeathIPBanned); err != nil {
		t.Fatal(err)
	}
	relays, err := h.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(relays, func(r PoolRelay) bool { return r.Name == names[0] })
	if i < 0 || relays[i].Status != "dead" || relays[i].DiedReason != DeathIPBanned {
		t.Fatalf("the console does not see the reason: %+v", relays[i])
	}

	// The schema holds the rule too: a dead row with no reason cannot be written.
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_relays SET status = 'dead', died_at = now() WHERE name = $1`, names[1]); err == nil {
		t.Fatal("the schema took a death with no reason")
	}
}

func TestARetiredBoxLeavesThePoolAtOnceAndTakesNoMoreWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	nudged := &nudges{}
	h.svc.SetNotifier(nudged)
	h.personal(3) // elsewhere: what the holders are refilled from

	box, _ := h.box()
	a, b := h.relay(box, KindPersonal), h.relay(box, KindPersonal)
	h.admit(a)
	h.admit(b)
	pending := h.relay(box, KindPersonal)

	// An account holding a relay of this box.
	var holder []string
	var account = testAccount(t, h.pool)
	for i := 0; i < 40 && !slices.ContainsFunc(holder, func(n string) bool { return n == a || n == b }); i++ {
		account = testAccount(t, h.pool)
		holder = h.batch(account)
	}
	if !slices.ContainsFunc(holder, func(n string) bool { return n == a || n == b }) {
		t.Fatal("no account got a relay of the box in 40 tries")
	}

	died, told, err := h.svc.Retire(ctx, box, DeathProviderDown)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(died)
	if want := []string{a, b, pending}; !slices.Equal(died, sortedCopy(want)) {
		t.Fatalf("retire killed %v, want %v", died, sortedCopy(want))
	}
	nudged.mu.Lock()
	told1 := len(nudged.relays) == 1 && slices.Contains(nudged.relays[0], account)
	nudged.mu.Unlock()
	if told < 1 || !told1 {
		t.Fatalf("the holder was not told: told %d, nudged %v", told, nudged.relays)
	}

	rows, err := h.pool.Query(ctx, `
		SELECT status, coalesce(died_reason, '') FROM entry_relays WHERE box = $1`, box)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var st, why string
		_ = rows.Scan(&st, &why)
		if st != "dead" || why != DeathProviderDown {
			t.Fatalf("a relay of the retired box is %s/%q", st, why)
		}
	}
	rows.Close()
	var retired bool
	if err := h.pool.QueryRow(ctx, `SELECT retired_at IS NOT NULL FROM entry_boxes WHERE name = $1`, box).Scan(&retired); err != nil || !retired {
		t.Fatalf("box not marked retired: %v %v", retired, err)
	}
	// The grant was revoked as a death, and the refill is a death replacement:
	// the dataset reads a hand-back the way it reads any death.
	after := h.batch(account)
	if slices.Contains(after, a) || slices.Contains(after, b) || len(after) != 2 {
		t.Fatalf("batch after retire: %v", after)
	}

	if _, _, err := h.svc.Retire(ctx, box, DeathProviderDown); !errors.Is(err, ErrBoxRetired) {
		t.Fatalf("retiring twice: %v", err)
	}
	if _, err := h.svc.AddRelay(ctx, box, KindPersonal); !errors.Is(err, ErrBoxRetired) {
		t.Fatalf("a relay on a retired box: %v", err)
	}
	if _, _, err := h.svc.Drain(ctx, box, StatusDraining); !errors.Is(err, ErrBoxRetired) {
		t.Fatalf("a rollout on a retired box: %v", err)
	}
	boxes, err := h.svc.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(boxes, func(x PoolBox) bool { return x.Name == box })
	if i < 0 || boxes[i].RetiredAt == nil {
		t.Fatalf("the console does not see the box retired: %+v", boxes[i])
	}
	if _, _, err := h.svc.Retire(ctx, "nosuch1", DeathOther); !errors.Is(err, ErrNoSuchBox) {
		t.Fatalf("retiring a box that does not exist: %v", err)
	}
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

func TestARetiredBoxIsWipedAndTheHubLocksItselfOut(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	ph.vantage("clean")
	box, id := ph.newBox("provider-pw-123")
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("provisioning: %+v\n%s", j, ph.logText(id))
	}

	if _, err := ph.prov.Wipe(ctx, box, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("wiping a box in service: %v, want ErrRejected", err)
	}
	if _, _, err := ph.svc.Retire(ctx, box, DeathRotated); err != nil {
		t.Fatal(err)
	}
	if _, err := ph.prov.Provision(ctx, box, "", ""); !errors.Is(err, ErrBoxRetired) {
		t.Fatalf("provisioning a retired box: %v", err)
	}

	wipe, err := ph.prov.Wipe(ctx, box, "")
	if err != nil {
		t.Fatal(err)
	}
	j := ph.wait(wipe)
	if j.Status != "done" || j.Kind != JobRetire {
		t.Fatalf("wipe: %+v\n%s", j, ph.logText(wipe))
	}
	_, hubPub, err := ph.svc.HubSSHKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ph.box.mu.Lock()
	last, lastIn := ph.box.cmds[len(ph.box.cmds)-1], ph.box.stdins[len(ph.box.stdins)-1]
	gone, keyIn := ph.box.binary == nil, ph.box.keyIn
	ph.box.mu.Unlock()
	if !strings.Contains(last, hubPub) || !strings.Contains(lastIn, "rm -rf /etc/nekko-edge") {
		t.Fatalf("the wipe did not run with the hub key: %q", last)
	}
	if !gone || keyIn {
		t.Fatalf("after the wipe: relay gone %v, hub key still in %v", gone, keyIn)
	}

	// The hub cannot log in again: a second wipe fails at login and says the
	// pool change already stands.
	again, err := ph.prov.Wipe(ctx, box, "")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(again); j.Status != "failed" || !strings.Contains(j.Error, "сняты с пула") {
		t.Fatalf("a second wipe: %+v", j)
	}
}

func TestAReSignKeepsARolloutState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	name := h.relay(box, KindPersonal)
	h.admit(name)
	if _, _, err := h.svc.Drain(ctx, box, StatusTesting); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := h.pool.QueryRow(ctx, `SELECT expires_at FROM entry_relays WHERE name = $1`, name).Scan(&before); err != nil {
		t.Fatal(err)
	}
	h.svc.now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := h.svc.Resign(ctx, name); err != nil {
		t.Fatalf("re-signing a testing relay: %v", err)
	}
	var status string
	var after time.Time
	if err := h.pool.QueryRow(ctx, `SELECT status, expires_at FROM entry_relays WHERE name = $1`, name).Scan(&status, &after); err != nil {
		t.Fatal(err)
	}
	if status != StatusTesting || !after.After(before) {
		t.Fatalf("after re-sign: %s, expiry %s → %s", status, before, after)
	}
	pending := h.relay(box, KindPersonal)
	if err := h.svc.Resign(ctx, pending); !errors.Is(err, ErrNoSuchRelay) {
		t.Fatalf("re-signing a pending relay: %v", err)
	}
}

func TestTheHubRenewsWhatRunsOutAndNothingElse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old := h.personal(1)[0]
	if _, err := h.svc.PublishSources(ctx, testSources().Sources); err != nil {
		t.Fatal(err)
	}

	// Nothing is near expiry yet.
	relays, sources, err := h.svc.ResignExpiring(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(relays, old) || sources {
		t.Fatalf("renewed a fresh record: %v, sources %v", relays, sources)
	}

	// Three weeks and two days on: inside the week before expiry.
	later := time.Now().Add(RecordLifetime - ResignAhead + 2*24*time.Hour)
	h.svc.now = func() time.Time { return later }
	fresh := h.personal(1)[0] // signed "later": a full month left
	relays, sources, err = h.svc.ResignExpiring(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(relays, old) || slices.Contains(relays, fresh) || !sources {
		t.Fatalf("renewed %v (want %s, not %s), sources %v", relays, old, fresh, sources)
	}
	var expires time.Time
	if err := h.pool.QueryRow(ctx, `SELECT expires_at FROM entry_relays WHERE name = $1`, old).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if want := later.Add(RecordLifetime); expires.Unix() != want.Unix() {
		t.Fatalf("renewed record expires %s, want %s", expires, want)
	}
	st, err := h.svc.Sources(ctx)
	if err != nil || st.ExpiresAt.Unix() != later.Add(RecordLifetime).Unix() {
		t.Fatalf("source list after renewal: %+v %v", st, err)
	}
	// The renewed list is the same list, and it verifies.
	env, _, ok, err := h.svc.repo.currentSources(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := h.svc.republishSources(ctx, env); err != nil {
		t.Fatalf("the renewed list does not reopen: %v", err)
	}
}

func TestWithoutAKeyNothingIsRenewed(t *testing.T) {
	h := newHarness(t)
	svc, err := NewService(h.pool, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResignExpiring(context.Background()); !errors.Is(err, ErrNoSigner) {
		t.Fatalf("renewing without a key: %v", err)
	}
}
