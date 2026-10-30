package entry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Roadmap 42 П6.14 Ф1: the version a box runs, the states a relay passes
// through during a rollout, and the rollout record itself.

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (ph *provHarness) boxRow(name string) PoolBox {
	ph.t.Helper()
	boxes, err := ph.svc.Boxes(context.Background())
	if err != nil {
		ph.t.Fatal(err)
	}
	for _, b := range boxes {
		if b.Name == name {
			return b
		}
	}
	ph.t.Fatalf("no box %s", name)
	return PoolBox{}
}

func TestABoxRecordsTheBuildItRuns(t *testing.T) {
	ph := newProvHarness(t)
	v1 := sha256Hex([]byte("edge-binary"))
	if got := ph.prov.EdgeBuilds(); len(got) != 1 || got[0].Arch != "amd64" || got[0].SHA != v1 {
		t.Fatalf("hub builds %+v, want amd64 only at %s", got, v1[:12])
	}

	ph.vantage("clean")
	box, id := ph.newBox(ph.box.password)
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, ph.logText(id))
	}
	if b := ph.boxRow(box); b.Arch != "amd64" || b.EdgeSHA != v1 || b.EdgeSeenAt == nil {
		t.Fatalf("box after the first run: arch %q sha %q seen %v", b.Arch, b.EdgeSHA, b.EdgeSeenAt)
	}

	// A hub started from a newer image carries another build; its run puts
	// that build on the box and the box says so.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "linux-amd64"), 0o755); err != nil {
		t.Fatal(err)
	}
	newer := []byte("edge-binary-v2")
	if err := os.WriteFile(filepath.Join(dir, "linux-amd64", "nekko-edge"), newer, 0o755); err != nil {
		t.Fatal(err)
	}
	next := NewProvisioner(ph.svc, ph.box.dial, dir, "api.example.org:443", nil)
	next.probeTimeout, next.probePoll = ph.prov.probeTimeout, ph.prov.probePoll
	ph.vantage("clean")
	id2, err := next.Provision(context.Background(), box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(id2); j.Status != "done" {
		t.Fatalf("update %+v\n%s", j, ph.logText(id2))
	}
	if b := ph.boxRow(box); b.EdgeSHA != sha256Hex(newer) {
		t.Fatalf("box after the update runs %q, want %s", b.EdgeSHA, sha256Hex(newer)[:12])
	}
	if !strings.Contains(ph.logText(id2), "загружен") {
		t.Fatalf("update log:\n%s", ph.logText(id2))
	}

	// The build is the one hashed at start: a file changed under a running
	// hub is refused, never sent under the old version's name.
	if err := os.WriteFile(filepath.Join(dir, "linux-amd64", "nekko-edge"), []byte("swapped-under-us"), 0o755); err != nil {
		t.Fatal(err)
	}
	ph.box.binary = nil // the box lost its binary: the run must upload
	ph.vantage("clean")
	id3, err := next.Provision(context.Background(), box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(id3); j.Status != "failed" || !strings.Contains(j.Error, "изменился после старта") {
		t.Fatalf("changed artifact %+v", j)
	}
}

func TestABoxOfAnArchTheHubHasNoBuildForIsRefused(t *testing.T) {
	ph := newProvHarness(t)
	ph.prov.builds = map[string]EdgeBuild{}
	_, id := ph.newBox(ph.box.password)
	if j := ph.wait(id); j.Status != "failed" || !strings.Contains(j.Error, "нет бинаря релея под amd64") {
		t.Fatalf("job %+v", j)
	}
}

func TestATestingOrDrainingRelayIsNotHandedOutAndCanDie(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, state := range []string{"testing", "draining"} {
		names := h.personal(1)
		if _, err := h.pool.Exec(ctx, `UPDATE entry_relays SET status = $2 WHERE name = $1`, names[0], state); err != nil {
			t.Fatalf("alive → %s: %v", state, err)
		}
		// Not a candidate for any slot. Asked of the candidates, not of a
		// batch: the shared test pool may hold other alive relays, and a batch
		// of two could skip this one by rank alone.
		if handedOut(t, h, names[0]) {
			t.Fatalf("a %s relay is still a candidate for a slot", state)
		}
		if _, err := h.svc.MarkDead(ctx, names[0], DeathOther); err != nil {
			t.Fatalf("a %s relay must be able to die: %v", state, err)
		}
	}

	// Only a relay that was alive reaches these states: a pending one has no
	// record to go back to the pool with.
	box, _ := h.box()
	pending := h.relay(box, KindPersonal)
	_, err := h.pool.Exec(ctx, `UPDATE entry_relays SET status = 'testing' WHERE name = $1`, pending)
	if !isCheckViolation(err) {
		t.Fatalf("pending → testing: %v, want a check violation", err)
	}
}

func TestOneRolloutPerArchMovesAndABuildIsTriedOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The rollouts table is shared with whatever else runs on this database:
	// everything here happens in one transaction and is rolled back.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // the point
	// Clear the moving slot for this transaction only.
	if _, err := tx.Exec(ctx, `UPDATE entry_rollouts SET stage = 'stopped', error = 'test', finished_at = now()
		WHERE stage IN ('canary', 'rolling')`); err != nil {
		t.Fatal(err)
	}
	insert := func(arch, sha string) error {
		_, err := tx.Exec(ctx, `SAVEPOINT s`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO entry_rollouts (arch, edge_sha) VALUES ($1, $2)`, arch, sha)
		if err != nil {
			_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT s`)
		}
		return err
	}
	a, b := sha256Hex([]byte(uuid.NewString())), sha256Hex([]byte(uuid.NewString()))
	if err := insert("amd64", a); err != nil {
		t.Fatal(err)
	}
	if err := insert("amd64", b); !isUniqueViolationErr(err) {
		t.Fatalf("a second moving amd64 rollout: %v, want a unique violation", err)
	}
	if err := insert("arm64", b); err != nil {
		t.Fatalf("arm64 moves on its own: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE entry_rollouts SET stage = 'failed', error = 'canary', finished_at = now()
		WHERE arch = 'amd64' AND edge_sha = $1`, a); err != nil {
		t.Fatal(err)
	}
	if err := insert("amd64", a); !isUniqueViolationErr(err) {
		t.Fatalf("retrying a failed build: %v, want a unique violation", err)
	}
	if err := insert("amd64", b); err != nil {
		t.Fatalf("the next build after a failed one: %v", err)
	}
}

func handedOut(t *testing.T, h *harness, relay string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only
	cands, err := h.svc.repo.candidates(ctx, tx, testAccount(t, h.pool), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.name == relay {
			return true
		}
	}
	return false
}

func isCheckViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514"
}

func isUniqueViolationErr(err error) bool { return err != nil && isUniqueViolation(err) }

// boxOf is the box a harness relay runs on: relays are named <box>-<n>.
func boxOf(relay string) string { return relay[:strings.LastIndexByte(relay, '-')] }

func grantOf(t *testing.T, h *harness, account uuid.UUID, relay string) (trigger string, reason *string) {
	t.Helper()
	err := h.pool.QueryRow(context.Background(), `
		SELECT g.trigger, g.revoke_reason FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id
		WHERE g.account_id = $1 AND r.name = $2 ORDER BY g.id DESC LIMIT 1`, account, relay).Scan(&trigger, &reason)
	if err != nil {
		t.Fatalf("grant of %s on %s: %v", account, relay, err)
	}
	return trigger, reason
}

func TestADrainMovesHoldersOffAndARestoreBringsTheRelayBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	n := &nudges{}
	h.svc.SetNotifier(n)
	pool := h.personal(3)
	a := testAccount(t, h.pool)
	held := h.batch(a)
	if len(held) != 2 {
		t.Fatalf("batch %v", held)
	}
	gone := held[0]

	relays, holders, err := h.svc.Drain(ctx, boxOf(gone), StatusDraining)
	if err != nil || relays != 1 || holders < 1 {
		t.Fatalf("drain: %d relays, %d holders, %v", relays, holders, err)
	}
	if len(n.relays) != 1 || !slices.Contains(n.relays[0], a) {
		t.Fatalf("nudges %v, want one naming %s", n.relays, a)
	}
	if _, reason := grantOf(t, h, a, gone); reason == nil || *reason != "drained" {
		t.Fatalf("the grant of %s was revoked as %v, want drained", gone, reason)
	}

	// The slot is refilled from the rest of the pool, and the refill says why.
	after := h.batch(a)
	if len(after) != 2 || slices.Contains(after, gone) {
		t.Fatalf("batch after the drain %v (pool %v, drained %s)", after, pool, gone)
	}
	for _, r := range after {
		if r == held[1] {
			continue
		}
		if trig, _ := grantOf(t, h, a, r); trig != "drain_replacement" {
			t.Fatalf("the refill of a drained slot is %q", trig)
		}
	}

	// Back in the pool: handed out again, and nobody's batch moves for it.
	back, err := h.svc.Restore(ctx, boxOf(gone))
	if err != nil || len(back) != 1 || back[0] != gone {
		t.Fatalf("restore: %v, %v", back, err)
	}
	if !handedOut(t, h, gone) {
		t.Fatal("a restored relay is not a candidate again")
	}
	if again := h.batch(a); !slices.Equal(again, after) {
		t.Fatalf("a restore moved a full batch: %v → %v", after, again)
	}
	// Nothing to restore twice, and a drain of a box with nothing alive is a no-op.
	if back, _ := h.svc.Restore(ctx, boxOf(gone)); len(back) != 0 {
		t.Fatalf("second restore %v", back)
	}
}

func TestADrainIsRefusedForOtherStatesAndUnknownBoxes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	for _, st := range []string{"alive", "dead", "pending", ""} {
		if _, _, err := h.svc.Drain(ctx, box, st); !errors.Is(err, ErrRejected) {
			t.Fatalf("drain to %q: %v", st, err)
		}
	}
	if _, _, err := h.svc.Drain(ctx, box+"0", StatusTesting); !errors.Is(err, ErrNoSuchBox) {
		t.Fatalf("drain of an unknown box: %v", err)
	}
	// A pending relay has never been handed out: a drain leaves it be.
	pending := h.relay(box, KindPersonal)
	if relays, _, err := h.svc.Drain(ctx, box, StatusTesting); err != nil || relays != 0 {
		t.Fatalf("drain moved %d relays (%v)", relays, err)
	}
	if err := h.svc.Admit(ctx, pending); err != nil {
		t.Fatalf("the pending relay is still pending: %v", err)
	}
}

func TestADeathAndADrainAtOnceAreEachLabelled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.personal(5)
	a := testAccount(t, h.pool)
	held := h.batch(a)
	if _, err := h.svc.MarkDead(ctx, held[0], DeathOther); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Drain(ctx, boxOf(held[1]), StatusTesting); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range h.batch(a) {
		trig, _ := grantOf(t, h, a, r)
		got = append(got, trig)
	}
	slices.Sort(got)
	if want := []string{"death_replacement", "drain_replacement"}; !slices.Equal(got, want) {
		t.Fatalf("refill triggers %v, want %v", got, want)
	}
}

func TestWaitingForAnEmptyBox(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	id, err := ph.svc.startJob(ctx, box, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ph.svc.finishJob(ctx, id, nil) //nolint:errcheck // frees the box
	ph.prov.drainPoll = time.Millisecond
	j := &jobRun{p: ph.prov, ctx: ctx, id: id, box: box}
	relays := []provRelay{{name: box + "-1", id: Identity{Port: 443}}, {name: box + "-2", id: Identity{Port: 8443}}}
	r := &fakeRemote{b: ph.box}

	ph.box.conns = []int{3, 3, 1, 0}
	if left, err := j.waitEmpty(r, relays, time.Minute); err != nil || left != 0 {
		t.Fatalf("emptying box: %d left, %v", left, err)
	}
	// Both ports are asked about, and the log follows the count, not the poll.
	if last := ph.box.cmds[len(ph.box.cmds)-1]; last != "bash -s -- 443 8443" {
		t.Fatalf("asked %q", last)
	}
	if l := ph.logText(id); strings.Count(l, "соединений:") != 3 {
		t.Fatalf("log:\n%s", l)
	}

	// Past the deadline the hub goes ahead and says how many it will cut.
	ph.box.conns = []int{2}
	if left, err := j.waitEmpty(r, relays, 0); err != nil || left != 2 {
		t.Fatalf("stuck box: %d left, %v", left, err)
	}
	if !strings.Contains(ph.logText(id), "срок пересадки") {
		t.Fatalf("log:\n%s", ph.logText(id))
	}
}
