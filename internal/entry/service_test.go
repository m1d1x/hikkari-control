package entry

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/config"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// The package's tests run in a database of their own, made afresh on the
// server NEKKO_TEST_DB (or TEST_DATABASE_URL) points at, and never in the
// database that URL names.
//
// That one is, on the local stack, the live hub's: compose's `nekko` runs
// against it with its rollout, resigner, detector and mechanic loops going
// (the last two only with CLICKHOUSE_DSN). Sharing it, the hub would claim
// the tests' incidents and start repairs on their boxes, queue probe tasks
// the tests' vantages then answer instead of their own, and the tests would
// kill the hub's relays (newRolloutHarness takes every live relay out). And
// even with nothing else connected, a run used to inherit whatever the last
// one left half-done — a job cut off by a kill, its probe task still queued.
//
// The name is fixed, not per run: the query text has to be a compile-time
// constant (tools/sqlguard), and a fixed name is also what lets the next run
// clear the last one's database instead of leaking one per run. It is left in
// place at the end, for whoever wants to look at what a failed run wrote. Two
// runs at once against one server collide on it loudly (the DROP refuses a
// database in use) rather than silently sharing it.
const (
	testDBName   = "nekko_entry_test"
	dropTestDB   = `DROP DATABASE IF EXISTS nekko_entry_test`
	createTestDB = `CREATE DATABASE nekko_entry_test`
)

// testDB is the run's database, made once in TestMain.
var testDB struct {
	dsn  string // the run's own database
	skip string // why the tests that need it skip: nothing configured, or unreachable
	err  error  // why it could not be made, on a server that answered
}

func TestMain(m *testing.M) {
	// Envelopes and grant observations are sealed, and the batch key is
	// derived from the content key.
	cryptobox.ConfigureEphemeralForTests()
	testDB.dsn, testDB.skip, testDB.err = freshTestDB(context.Background())
	os.Exit(m.Run())
}

// freshTestDB drops and recreates the run's database on the configured
// server and migrates it, returning its URL.
func freshTestDB(ctx context.Context) (dsn, skip string, err error) {
	server := os.Getenv("NEKKO_TEST_DB")
	if server == "" {
		server = os.Getenv("TEST_DATABASE_URL")
	}
	if server == "" {
		return "", "no test DB — set NEKKO_TEST_DB to run", nil
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", "", fmt.Errorf("NEKKO_TEST_DB must be a postgres:// URL")
	}
	if strings.TrimPrefix(u.Path, "/") == testDBName {
		return "", "", fmt.Errorf("NEKKO_TEST_DB names %s itself; point it at any other database on the server", testDBName)
	}
	admin, err := db.Connect(ctx, server)
	if err != nil {
		return "", fmt.Sprintf("no test DB reachable (%v)", err), nil
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, dropTestDB); err != nil {
		return "", "", fmt.Errorf("drop the last run's %s (another run, or a client, still connected?): %w", testDBName, err)
	}
	if _, err := admin.Exec(ctx, createTestDB); err != nil {
		return "", "", fmt.Errorf("create %s: %w", testDBName, err)
	}
	u.Path = "/" + testDBName
	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		return "", "", fmt.Errorf("connect to %s: %w", testDBName, err)
	}
	defer pool.Close()
	if err := db.RunMigrations(ctx, pool); err != nil {
		return "", "", fmt.Errorf("migrate %s: %w", testDBName, err)
	}
	return u.String(), "", nil
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testDB.err != nil {
		t.Fatalf("test database: %v", testDB.err)
	}
	if testDB.skip != "" {
		t.Skip(testDB.skip)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, testDB.dsn)
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	// The run's database is shared by its tests, one at a time; what follows
	// clears what an earlier test leaves behind that a later one would read.
	//
	// The candidate query sees the whole pool, so personal relays left alive
	// by earlier tests would join every batch here.
	if _, err := pool.Exec(ctx, `
		UPDATE entry_relays SET status = 'dead', died_at = now(), died_reason = 'other'
		WHERE kind = 'personal' AND status = 'alive'`); err != nil {
		t.Fatal(err)
	}
	// The source list is a singleton shared by the whole database; clear it so
	// each test starts with none, independent of what an earlier test imported.
	if _, err := pool.Exec(ctx, `DELETE FROM entry_sources`); err != nil {
		t.Fatal(err)
	}
	// The hub's ssh key is a singleton; each test makes its own.
	if _, err := pool.Exec(ctx, `DELETE FROM entry_hub_ssh`); err != nil {
		t.Fatal(err)
	}
	// The domain ledger (054) accumulates: an earlier test's seed would stay
	// assigned and its month's spend would count here. Clear it so the pool
	// and the budget start empty each test.
	if _, err := pool.Exec(ctx, `DELETE FROM entry_domains`); err != nil {
		t.Fatal(err)
	}
	// Machine orders (075) are fleet-wide: the order limit counts them, and
	// the budget counts the open ones. And every test's hoster is a fake that
	// dies with it, so a retired box an earlier test left undeleted has no
	// machine behind it any more — the pass of this test would otherwise
	// "delete" it at this test's hoster.
	if _, err := pool.Exec(ctx, `DELETE FROM entry_box_orders`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE entry_boxes SET released_at = now()
		WHERE retired_at IS NOT NULL AND released_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	// Provider accounts (056) are one per kind, and each test saves its own.
	if _, err := pool.Exec(ctx, `DELETE FROM entry_providers`); err != nil {
		t.Fatal(err)
	}
	// The do-not-use list (057) is shared by every test that pools a domain.
	if _, err := pool.Exec(ctx, `DELETE FROM entry_domain_exclusions`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testAccount(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (username) VALUES ($1) RETURNING id`,
		"e"+uuid.NewString()[:12]).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type harness struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *Service
	f    fixture
	made []string // relays this test created, retired on cleanup
}

func newHarness(t *testing.T) *harness {
	pool := testPool(t)
	f := loadFixture(t)
	seed, err := hex.DecodeString(f.SeedHex)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(pool, NewSigner(f.KeyID, seed), 2)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, pool: pool, svc: svc, f: f}
	// The spend caps are one row per group shared by every test, and the
	// test database keeps every machine and purchase of every run, so the
	// month's spend there is whatever the runs left. Every test starts with
	// room to spare; a test about the caps sets them against what is spent
	// (spendRoom).
	h.spendCaps(SpendHosting, generousCapCents, generousCapCents)
	h.spendCaps(SpendRegistrar, generousCapCents, generousCapCents)
	// The observation point every test's vantage speaks as: registered and
	// active, as only a registered slot is read or may claim (vantages.go).
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_vantages (slot, title) VALUES ($1, 'tests')
		ON CONFLICT (slot) DO UPDATE SET status = 'active', retired_at = NULL, last_admission_poll_at = NULL`, testVantage); err != nil {
		t.Fatal(err)
	}
	// The pool is shared by every test of the run: left alive, these relays
	// would be handed to the next test's accounts. Runs before the pool
	// closes (cleanups are LIFO). Every relay on a box the test touched goes,
	// not only the ones it named: a job mints a box's spare port by itself
	// (ports.go), and a test that knows only the first name would leave the
	// spare behind.
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `
			UPDATE entry_relays SET status = 'dead', died_at = now(), died_reason = 'other'
			WHERE status <> 'dead' AND (name = ANY($1)
			   OR box IN (SELECT box FROM entry_relays WHERE name = ANY($1)))`, h.made); err != nil {
			t.Errorf("retire this test's relays: %v", err)
		}
		// Cover sites are the pool's, not a box's: a cover a test put under
		// suspicion or struck would change what every later test's relays
		// borrow and what the detector calls short.
		if _, err := pool.Exec(context.Background(), `DELETE FROM entry_suspects WHERE kind = 'cover'`); err != nil {
			t.Errorf("clear the cover ledger: %v", err)
		}
	})
	// No job or repair of this test outlives it. One that did kept running
	// into the next test: its probe task, queued after this test's vantage had
	// stopped answering, was the oldest in the queue, so the next test's
	// vantage answered it instead of its own — whose job then timed out, and
	// left its own task for the test after, and so on down the file. Runs
	// first (registered last), while the pool can still take the verdicts.
	t.Cleanup(svc.stopBackground)
	return h
}

// box creates a machine with a fresh name and address: boxes are never
// deleted, and one address is one box.
func (h *harness) box() (name, addr string) {
	h.t.Helper()
	// 48 random bits each: boxes accumulate across runs in the test database.
	u := uuid.New()
	name = fmt.Sprintf("t%d", uint64(u[0])<<40|uint64(u[1])<<32|uint64(u[2])<<24|
		uint64(u[3])<<16|uint64(u[4])<<8|uint64(u[5]))
	addr = fmt.Sprintf("2001:db8:%x%x:%x%x:%x%x::1", u[6], u[7], u[8], u[9], u[10], u[11])
	if err := h.svc.CreateBox(context.Background(), BoxSpec{
		Name: name, Address: addr, Provider: "vultr", Location: "amsterdam",
	}); err != nil {
		h.t.Fatalf("create box: %v", err)
	}
	return name, addr
}

// relay mints a pending relay on box.
func (h *harness) relay(box, kind string) string {
	h.t.Helper()
	name, err := h.svc.AddRelay(context.Background(), box, kind)
	if err != nil {
		h.t.Fatalf("add relay: %v", err)
	}
	h.made = append(h.made, name)
	return name
}

func (h *harness) admit(name string) {
	h.t.Helper()
	if err := h.svc.Admit(context.Background(), name); err != nil {
		h.t.Fatalf("admit %s: %v", name, err)
	}
}

// admitted makes n alive relays of kind, each on its own box, and returns
// their names sorted.
func (h *harness) admitted(kind string, n int) []string {
	h.t.Helper()
	var names []string
	for i := 0; i < n; i++ {
		box, _ := h.box()
		name := h.relay(box, kind)
		h.admit(name)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *harness) personal(n int) []string { return h.admitted(KindPersonal, n) }

// batch returns the relay names in the account's batch, sorted.
func (h *harness) batch(account uuid.UUID) []string {
	h.t.Helper()
	envs, err := h.svc.BatchFor(context.Background(), account, GrantMeta{IP: "198.51.100.1"})
	if err != nil {
		h.t.Fatalf("batch: %v", err)
	}
	var names []string
	for _, e := range envs {
		r, err := Verify(e, h.f.keys(h.t), time.Now())
		if err != nil {
			h.t.Fatalf("the hub served a record that does not verify: %v", err)
		}
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}

func TestARelayIsBornPendingAndServedOnlyOnceAdmitted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, addr := h.box()
	name := h.relay(box, KindPersonal)
	if name != box+"-1" {
		t.Fatalf("first relay on %s named %s", box, name)
	}

	account := testAccount(t, h.pool)
	if got := h.batch(account); len(got) != 0 {
		t.Fatalf("a pending relay was handed out: %v", got)
	}
	h.admit(name)
	got := h.batch(account)
	if len(got) != 1 || got[0] != name {
		t.Fatalf("batch after admission: %v", got)
	}

	// The record is the identity on the box's address.
	envs, _ := h.svc.BatchFor(ctx, account, GrantMeta{})
	r, err := Verify(envs[0], h.f.keys(t), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, err := h.svc.RelayIdentity(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if r.Address != addr || r.Port != id.Port || r.Port != 443 || r.Proto != ProtoVlessRealityVision {
		t.Fatalf("record %+v, identity port %d, box address %s", r, id.Port, addr)
	}

	if err := h.svc.Admit(ctx, name); !errors.Is(err, ErrNoSuchRelay) {
		t.Fatalf("admitting twice: %v, want ErrNoSuchRelay", err)
	}

	// Re-signing moves the record, not the batch.
	before := r.IssuedAt
	h.svc.now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := h.svc.Resign(ctx, name); err != nil {
		t.Fatal(err)
	}
	h.svc.now = time.Now
	envs, _ = h.svc.BatchFor(ctx, account, GrantMeta{})
	r, _ = Verify(envs[0], h.f.keys(t), time.Now())
	if r.IssuedAt <= before {
		t.Fatalf("re-sign kept issued_at %d", r.IssuedAt)
	}
	var grants int
	_ = h.pool.QueryRow(ctx,
		`SELECT count(*) FROM entry_grants WHERE account_id = $1`, account).Scan(&grants)
	if grants != 1 {
		t.Fatalf("re-signing changed the batch: %d grants", grants)
	}

	if _, err := h.svc.MarkDead(ctx, name, DeathOther); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Resign(ctx, name); !errors.Is(err, ErrNoSuchRelay) {
		t.Fatalf("re-signing a dead relay: %v, want ErrNoSuchRelay", err)
	}
}

func TestWithoutAKeyTheHubServesButDoesNotSign(t *testing.T) {
	h := newHarness(t)
	h.svc.signer = nil
	box, _ := h.box()
	name := h.relay(box, KindPersonal)
	if err := h.svc.Admit(context.Background(), name); !errors.Is(err, ErrNoSigner) {
		t.Fatalf("admit without a key: %v, want ErrNoSigner", err)
	}
	if _, err := h.svc.PublishSources(context.Background(), testSources().Sources); !errors.Is(err, ErrNoSigner) {
		t.Fatalf("publish without a key: %v, want ErrNoSigner", err)
	}
}

func TestRelayNamesAndPortsOnABox(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	var ports []int
	for i := 0; i < 3; i++ {
		name := h.relay(box, KindPersonal)
		if name != fmt.Sprintf("%s-%d", box, i+1) {
			t.Fatalf("relay %d named %s", i+1, name)
		}
		id, _ := h.svc.RelayIdentity(ctx, name)
		ports = append(ports, id.Port)
	}
	if fmt.Sprint(ports) != "[443 8443 2053]" {
		t.Fatalf("ports %v", ports)
	}
	// A dead relay frees its port, never its name.
	if _, err := h.svc.MarkDead(ctx, box+"-2", DeathOther); err != nil {
		t.Fatal(err)
	}
	name := h.relay(box, KindPersonal)
	id, _ := h.svc.RelayIdentity(ctx, name)
	if name != box+"-4" || id.Port != 8443 || id.LocalPortBase != 10301 {
		t.Fatalf("after a death: %s on %d (local %d)", name, id.Port, id.LocalPortBase)
	}
	if _, err := h.svc.AddRelay(ctx, "nobox1", KindPersonal); !errors.Is(err, ErrNoSuchBox) {
		t.Fatalf("relay on a missing box: %v", err)
	}
	if _, err := h.svc.AddRelay(ctx, box, "public"); !errors.Is(err, ErrRejected) {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestBoxRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, addr := h.box()
	for what, b := range map[string]BoxSpec{
		"bad name":       {Name: "ams-1", Address: "192.0.2.10", Provider: "vultr", Location: "ams"},
		"no provider":    {Name: box + "9", Address: "192.0.2.11", Location: "ams"},
		"no address":     {Name: box + "8", Provider: "vultr", Location: "ams"},
		"same name":      {Name: box, Address: "192.0.2.12", Provider: "vultr", Location: "ams"},
		"shared address": {Name: box + "7", Address: addr, Provider: "vultr", Location: "ams"},
	} {
		if err := h.svc.CreateBox(ctx, b); !errors.Is(err, ErrRejected) {
			t.Fatalf("%s: %v, want ErrRejected", what, err)
		}
	}
}

func TestSecretsAreSealedAtRest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, addr := h.box()
	name := h.relay(box, KindPersonal)
	h.admit(name)
	id, _ := h.svc.RelayIdentity(ctx, name)
	var addrCT, idCT, envCT []byte
	if err := h.pool.QueryRow(ctx, `
		SELECT b.address_ct, r.identity_ct, r.envelope_ct
		FROM entry_relays r JOIN entry_boxes b ON b.name = r.box WHERE r.name = $1`, name).
		Scan(&addrCT, &idCT, &envCT); err != nil {
		t.Fatal(err)
	}
	for what, ct := range map[string][]byte{"address": addrCT, "identity": idCT, "envelope": envCT} {
		for _, secret := range []string{addr, id.UUID, id.PrivateKey, "payload"} {
			if strings.Contains(string(ct), secret) {
				t.Fatalf("%s holds %q in the clear", what, secret)
			}
		}
	}
}

func TestABatchIsStableWhenThePoolGrows(t *testing.T) {
	h := newHarness(t)
	h.personal(3)
	account := testAccount(t, h.pool)
	before := h.batch(account)
	if len(before) != 2 {
		t.Fatalf("batch %v, want 2 relays", before)
	}
	// New relays would outrank the held ones for some accounts — and must
	// still not move anybody's batch.
	h.personal(5)
	if after := h.batch(account); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("a growing pool reshuffled the batch: %v → %v", before, after)
	}
}

func TestADeathChangesExactlyTheSlotItEmptied(t *testing.T) {
	h := newHarness(t)
	h.personal(5)
	account := testAccount(t, h.pool)
	before := h.batch(account)
	dead, kept := before[0], before[1]

	revoked, err := h.svc.MarkDead(context.Background(), dead, DeathOther)
	if err != nil || revoked != 1 {
		t.Fatalf("mark dead: revoked %d, err %v", revoked, err)
	}
	after := h.batch(account)
	if len(after) != 2 {
		t.Fatalf("batch after death: %v", after)
	}
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	if has(after, dead) || !has(after, kept) {
		t.Fatalf("before %v, %s died, after %v: want %s kept and %s gone", before, dead, after, kept, dead)
	}

	var triggers []string
	rows, _ := h.pool.Query(context.Background(), `
		SELECT trigger || ':' || coalesce(revoke_reason, '-') FROM entry_grants
		WHERE account_id = $1 ORDER BY id`, account)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		triggers = append(triggers, s)
	}
	rows.Close()
	sort.Strings(triggers)
	want := "[death_replacement:- initial:- initial:relay_dead]"
	if fmt.Sprint(triggers) != want {
		t.Fatalf("registry %v, want %s", triggers, want)
	}
	if _, err := h.svc.MarkDead(context.Background(), dead, DeathOther); !errors.Is(err, ErrNoSuchRelay) {
		t.Fatalf("killing twice: %v, want ErrNoSuchRelay", err)
	}
}

func TestAPendingRelayCanDie(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	name := h.relay(box, KindPersonal)
	if _, err := h.svc.MarkDead(context.Background(), name, DeathOther); err != nil {
		t.Fatalf("a box handed back after a dirty probe: %v", err)
	}
	if err := h.svc.Admit(context.Background(), name); !errors.Is(err, ErrNoSuchRelay) {
		t.Fatalf("admitting a dead relay: %v", err)
	}
}

func TestBatchesAreCombinationsNotGroups(t *testing.T) {
	h := newHarness(t)
	h.personal(6)
	distinct := map[string]bool{}
	for i := 0; i < 8; i++ {
		distinct[fmt.Sprint(h.batch(testAccount(t, h.pool)))] = true
	}
	// 15 possible pairs; eight accounts all landing on one would mean the
	// hash is not keyed by the account.
	if len(distinct) < 3 {
		t.Fatalf("8 accounts got only %d distinct batches: %v", len(distinct), distinct)
	}
}

func TestAnExpiredGrantIsNotServedAndNotRefilled(t *testing.T) {
	h := newHarness(t)
	t0 := time.Now()
	h.personal(3)
	account := testAccount(t, h.pool)
	before := h.batch(account)

	// The hub did not re-sign in time: same relays, records past expiry. A
	// relay signed later outlives them, so a refill would be possible — and
	// must not happen.
	h.svc.now = func() time.Time { return t0.Add(10 * 24 * time.Hour) }
	h.personal(1)
	h.svc.now = func() time.Time { return t0.Add(RecordLifetime + time.Hour) }
	envs, err := h.svc.BatchFor(context.Background(), account, GrantMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 0 {
		t.Fatalf("served %d expired records", len(envs))
	}
	h.svc.now = time.Now
	if after := h.batch(account); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("an expiry refilled the batch: %v → %v", before, after)
	}
}

func TestBootstrapRelaysNeverJoinABatch(t *testing.T) {
	h := newHarness(t)
	h.admitted(KindBootstrap, 2)
	if got := h.batch(testAccount(t, h.pool)); len(got) != 0 {
		t.Fatalf("bootstrap relays in a personal batch: %v", got)
	}
}

func TestConcurrentRequestsFillASlotOnce(t *testing.T) {
	h := newHarness(t)
	h.personal(4)
	account := testAccount(t, h.pool)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.svc.BatchFor(context.Background(), account, GrantMeta{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var grants int
	_ = h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM entry_grants WHERE account_id = $1`, account).Scan(&grants)
	if grants != 2 {
		t.Fatalf("8 concurrent requests made %d grants, want 2", grants)
	}
}

func TestWhatTheHubObservedIsSealed(t *testing.T) {
	h := newHarness(t)
	h.personal(1)
	account := testAccount(t, h.pool)
	if _, err := h.svc.BatchFor(context.Background(), account,
		GrantMeta{IP: "198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	var meta []byte
	if err := h.pool.QueryRow(context.Background(), `
		SELECT meta_ct FROM entry_grants WHERE account_id = $1`, account).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(meta), "198.51.100.7") {
		t.Fatal("grant meta is stored in the clear")
	}
	plain, err := cryptobox.DecryptString(meta)
	if err != nil || !strings.Contains(plain, "198.51.100.7") || !strings.Contains(plain, "account_created") {
		t.Fatalf("meta does not open to what was observed: %q %v", plain, err)
	}
}

func TestTheBundleNamesItsSetAndAnswersUnchanged(t *testing.T) {
	h := newHarness(t)
	h.personal(3)
	account := testAccount(t, h.pool)
	ctx := context.Background()

	first, err := h.svc.BundleFor(ctx, account, GrantMeta{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Unchanged || len(first.Relays) != 2 || first.Version == "" || first.ExpiresAt.IsZero() {
		t.Fatalf("first bundle: %+v", first)
	}
	for _, raw := range first.Relays {
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(env, h.f.keys(t), time.Now()); err != nil {
			t.Fatalf("served bytes do not verify: %v", err)
		}
	}

	again, err := h.svc.BundleFor(ctx, account, GrantMeta{}, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged || len(again.Relays) != 0 || again.Version != first.Version {
		t.Fatalf("same version asked again: %+v", again)
	}

	// A death changes the set, and with it the version.
	var env Envelope
	_ = json.Unmarshal(first.Relays[0], &env)
	r, _ := Verify(env, h.f.keys(t), time.Now())
	if _, err := h.svc.MarkDead(ctx, r.Name, DeathOther); err != nil {
		t.Fatal(err)
	}
	after, err := h.svc.BundleFor(ctx, account, GrantMeta{}, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if after.Unchanged || after.Version == first.Version || len(after.Relays) != 2 {
		t.Fatalf("after a death: %+v", after)
	}
}

func TestAFailReportIsRecordedAndKillsNothing(t *testing.T) {
	h := newHarness(t)
	all := h.personal(3)
	account := testAccount(t, h.pool)
	ctx := context.Background()
	held := h.batch(account)
	mine := held[0]
	var notMine string
	for _, n := range all {
		if n != held[0] && n != held[1] {
			notMine = n
		}
	}

	reports := []FailReport{
		{Relay: mine, Stage: StageTLS, Cover: "www.samsung.com", ObservedAt: time.Now().Add(-time.Minute), Detail: "eof"},
		{Relay: mine, Stage: StageTLS, ObservedAt: time.Now()},                     // repeat within the hour
		{Relay: mine, Stage: StageTCP, ObservedAt: time.Now().Add(48 * time.Hour)}, // clock ahead: clamped
		{Relay: notMine, Stage: StageTCP, ObservedAt: time.Now()},                  // never handed to us
		{Relay: mine, Stage: "smoke-signals", ObservedAt: time.Now()},              // unknown stage
	}
	kept, err := h.svc.RecordFailures(ctx, account, "198.51.100.9", reports)
	if err != nil {
		t.Fatal(err)
	}
	if kept != 2 {
		t.Fatalf("kept %d reports, want 2 (tls + tcp for a relay we hold)", kept)
	}

	var future int
	_ = h.pool.QueryRow(ctx, `
		SELECT count(*) FROM entry_fail_reports
		WHERE account_id = $1 AND observed_at > now() + interval '1 minute'`, account).Scan(&future)
	if future != 0 {
		t.Fatal("a timestamp from the future was stored as is")
	}
	var metaCT []byte
	_ = h.pool.QueryRow(ctx, `
		SELECT meta_ct FROM entry_fail_reports WHERE account_id = $1 AND stage = 'tls'`, account).Scan(&metaCT)
	if plain, err := cryptobox.DecryptString(metaCT); err != nil ||
		!strings.Contains(plain, "www.samsung.com") || !strings.Contains(plain, "198.51.100.9") {
		t.Fatalf("report meta: %q %v", plain, err)
	}

	// Reporting is not a way to a new address: the relay lives, the batch stands.
	if after := h.batch(account); fmt.Sprint(after) != fmt.Sprint(held) {
		t.Fatalf("a report moved the batch: %v → %v", held, after)
	}
	var status string
	_ = h.pool.QueryRow(ctx, `SELECT status FROM entry_relays WHERE name = $1`, mine).Scan(&status)
	if status != "alive" {
		t.Fatalf("a report killed %s", mine)
	}
}

func TestTheBundleCarriesTheSourceListAndTracksItsVersion(t *testing.T) {
	h := newHarness(t)
	h.personal(3)
	account := testAccount(t, h.pool)
	ctx := context.Background()

	// No source list published yet: the bundle carries none.
	first, err := h.svc.BundleFor(ctx, account, GrantMeta{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Sources != nil {
		t.Fatal("a bundle carried sources before any were published")
	}

	// Publish one: the version moves and the envelope rides along, verifying.
	st, err := h.svc.PublishSources(ctx, testSources().Sources)
	if err != nil || !st.Present || st.KeyID != h.f.KeyID {
		t.Fatalf("publish sources: %+v %v", st, err)
	}
	withSources, err := h.svc.BundleFor(ctx, account, GrantMeta{}, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if withSources.Unchanged || withSources.Version == first.Version || withSources.Sources == nil {
		t.Fatalf("publishing a source list did not re-ship the bundle: %+v", withSources)
	}
	var env Envelope
	if err := json.Unmarshal(withSources.Sources, &env); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySources(env, h.f.keys(t), time.Now()); err != nil {
		t.Fatalf("served source list does not verify: %v", err)
	}

	// Same version again: unchanged, nothing shipped.
	again, err := h.svc.BundleFor(ctx, account, GrantMeta{}, withSources.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged || again.Sources != nil {
		t.Fatalf("same version asked again carried a payload: %+v", again)
	}

	// A later publish is a newer version.
	h.svc.now = func() time.Time { return time.Now().Add(time.Minute) }
	newer, err := h.svc.PublishSources(ctx, testSources().Sources[:1])
	h.svc.now = time.Now
	if err != nil || !newer.IssuedAt.After(st.IssuedAt) {
		t.Fatalf("second publish: %+v %v (first issued %s)", newer, err, st.IssuedAt)
	}

	bad := testSources().Sources
	bad[1].URL = "http://plain.example.org"
	if _, err := h.svc.PublishSources(ctx, bad); !errors.Is(err, ErrRejected) {
		t.Fatalf("plain-http source: %v, want ErrRejected", err)
	}
}

func TestTheOwnerSeesRelaysOpenedAndGroupedByBox(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, addr := h.box()
	x := h.relay(box, KindPersonal)
	y := h.relay(box, KindPersonal)
	h.admit(x)

	relays, err := h.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]PoolRelay{}
	for _, r := range relays {
		seen[r.Name] = r
	}
	if r := seen[x]; r.Status != "alive" || r.Address != addr || r.Port != 443 ||
		r.Proto != ProtoVlessRealityVision || r.Box != box || r.SignedAt == nil || r.ExpiresAt == nil {
		t.Fatalf("admitted relay as the owner sees it: %+v", r)
	}
	if r := seen[y]; r.Status != "pending" || r.Port != 8443 || r.SignedAt != nil || r.Box != box {
		t.Fatalf("pending relay as the owner sees it: %+v", r)
	}

	boxes, err := h.svc.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range boxes {
		if b.Name != box {
			continue
		}
		if b.Address != addr || b.Provider != "vultr" || b.Location != "amsterdam" ||
			strings.Join(b.Relays, ",") != x+","+y {
			t.Fatalf("box as the owner sees it: %+v", b)
		}
		return
	}
	t.Fatalf("box %s missing from %+v", box, boxes)
}

func TestAnUnopenableRelayDoesNotBlindTheOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, addr := h.box()
	ok := h.relay(box, KindPersonal)
	rot := box + "-9"
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO entry_relays (name, kind, status, died_at, died_reason, box, identity_ct)
		VALUES ($1, 'bootstrap', 'dead', now(), 'other', $2, '\x00')`, rot, box); err != nil {
		t.Fatal(err)
	}
	relays, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("one unopenable relay failed the whole list: %v", err)
	}
	var sawOK, sawRot bool
	for _, r := range relays {
		switch r.Name {
		case ok:
			sawOK = r.Address == addr && !r.Unreadable
		case rot:
			sawRot = r.Unreadable && r.Address == ""
		}
	}
	if !sawOK || !sawRot {
		t.Fatalf("ok opened: %v, rot flagged unreadable: %v", sawOK, sawRot)
	}
}

// nudges records what the service told the live sessions (П6.11).
type nudges struct {
	mu      sync.Mutex
	relays  [][]uuid.UUID
	sources int
}

func (n *nudges) RelaysChanged(accounts []uuid.UUID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.relays = append(n.relays, append([]uuid.UUID(nil), accounts...))
}

func (n *nudges) SourcesChanged() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sources++
}

func TestADeathNudgesExactlyTheHoldersOfThatRelay(t *testing.T) {
	h := newHarness(t)
	n := &nudges{}
	h.svc.SetNotifier(n)
	h.personal(3)

	// Six accounts with batches (2 of 3 relays each, by the keyed hash), and
	// one that never asked: it holds nothing, so a death owes it nothing.
	holdersOf := map[string]map[uuid.UUID]bool{}
	for range 6 {
		a := testAccount(t, h.pool)
		for _, name := range h.batch(a) {
			if holdersOf[name] == nil {
				holdersOf[name] = map[uuid.UUID]bool{}
			}
			holdersOf[name][a] = true
		}
	}
	bystander := testAccount(t, h.pool)

	// Kill the relay held by the most accounts, so the assertion is about a
	// real subset and not a degenerate one.
	dead := ""
	for name, set := range holdersOf {
		if dead == "" || len(set) > len(holdersOf[dead]) {
			dead = name
		}
	}
	revoked, err := h.svc.MarkDead(context.Background(), dead, DeathOther)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.relays) != 1 {
		t.Fatalf("%d nudges for one death, want 1", len(n.relays))
	}
	got := n.relays[0]
	if int64(len(got)) != revoked || len(got) != len(holdersOf[dead]) {
		t.Fatalf("nudged %d accounts, revoked %d grants, %d held %s", len(got), revoked, len(holdersOf[dead]), dead)
	}
	for _, a := range got {
		if !holdersOf[dead][a] {
			t.Fatalf("nudged %s, which never held %s", a, dead)
		}
		if a == bystander {
			t.Fatal("nudged an account with no batch")
		}
	}
	if n.sources != 0 {
		t.Fatalf("a death nudged everyone (%d)", n.sources)
	}

	// A death nobody holds (the pool's spare after the refill took the rest)
	// nudges nobody: an empty fan-out is not a call.
	spare := h.personal(1)[0]
	if _, err := h.svc.MarkDead(context.Background(), spare, DeathOther); err != nil {
		t.Fatal(err)
	}
	if len(n.relays) != 1 {
		t.Fatalf("a death with no holders nudged %v", n.relays[1:])
	}
}

func TestAPublishedSourceListNudgesEveryoneAndARefusedOneNobody(t *testing.T) {
	h := newHarness(t)
	n := &nudges{}
	h.svc.SetNotifier(n)
	ctx := context.Background()

	if _, err := h.svc.PublishSources(ctx, testSources().Sources); err != nil {
		t.Fatal(err)
	}
	if n.sources != 1 {
		t.Fatalf("%d source nudges after a publish, want 1", n.sources)
	}
	bad := testSources().Sources
	bad[1].URL = "http://plain.example.org"
	if _, err := h.svc.PublishSources(ctx, bad); !errors.Is(err, ErrRejected) {
		t.Fatalf("plain-http source: %v", err)
	}
	if n.sources != 1 {
		t.Fatalf("a refused list nudged everyone (%d)", n.sources)
	}
	if len(n.relays) != 0 {
		t.Fatalf("a source list nudged relay holders: %v", n.relays)
	}
}

// voiceBox creates a Hysteria2 box in front of region.
func (h *harness) voiceBox(region string) string {
	h.t.Helper()
	u := uuid.New()
	name := fmt.Sprintf("v%d", uint64(u[0])<<40|uint64(u[1])<<32|uint64(u[2])<<24|
		uint64(u[3])<<16|uint64(u[4])<<8|uint64(u[5]))
	addr := fmt.Sprintf("2001:db8:%x%x:%x%x:%x%x::2", u[6], u[7], u[8], u[9], u[10], u[11])
	if err := h.svc.CreateBox(context.Background(), BoxSpec{
		Name: name, Address: addr, Provider: "vultr", Location: "amsterdam",
		Proto: ProtoHysteria2, Region: region,
	}); err != nil {
		h.t.Fatalf("create voice box: %v", err)
	}
	return name
}

// A voice entry rides its own lane of the batch (voice plan Ф3): the account
// keeps its full set of stream relays AND gets one voice entry per region, the
// voice grant is labelled initial (not a replacement of something that died)
// even when the region appears after the account already held its stream
// relays, and a second entry in the same region does not grow the batch.
func TestAVoiceEntryRidesItsOwnLane(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.personal(3)
	account := testAccount(t, h.pool)
	if before := h.batch(account); len(before) != h.svc.batchSize {
		t.Fatalf("stream batch %v", before)
	}

	region := "t-" + uuid.NewString()[:8]
	voice := h.relay(h.voiceBox(region), KindPersonal)
	h.admit(voice)
	got := h.batch(account)
	if len(got) != h.svc.batchSize+VoiceSlotsPerRegion || !slices.Contains(got, voice) {
		t.Fatalf("batch %v: want %d stream relays and %s", got, h.svc.batchSize, voice)
	}
	var trigger string
	if err := h.pool.QueryRow(ctx, `
		SELECT g.trigger FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id
		WHERE g.account_id = $1 AND r.name = $2`, account, voice).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if trigger != triggerInitial {
		t.Fatalf("voice grant trigger %q, want %q", trigger, triggerInitial)
	}

	// The record the client gets is a voice record for this region.
	envs, err := h.svc.BatchFor(ctx, account, GrantMeta{})
	if err != nil {
		t.Fatal(err)
	}
	var voiceSeen bool
	for _, e := range envs {
		r, err := Verify(e, h.f.keys(t), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if r.Name == voice {
			voiceSeen = r.Proto == ProtoHysteria2
		}
	}
	if !voiceSeen {
		t.Fatal("the voice relay was not served as hysteria2")
	}

	second := h.relay(h.voiceBox(region), KindPersonal)
	h.admit(second)
	if again := h.batch(account); !slices.Equal(again, got) {
		t.Fatalf("a second entry in the region grew or reshuffled the batch: %v -> %v", got, again)
	}

}

func TestABoxSaysWhatItSpeaks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for what, spec := range map[string]BoxSpec{
		"voice without a region":  {Proto: ProtoHysteria2},
		"stream with a region":    {Region: "ru"},
		"unknown protocol":        {Proto: "wireguard"},
		"voice with a bad region": {Proto: ProtoHysteria2, Region: "RU"},
	} {
		spec.Name, spec.Address, spec.Provider, spec.Location = "x1", "203.0.113.250", "vultr", "amsterdam"
		if err := h.svc.CreateBox(ctx, spec); !errors.Is(err, ErrRejected) {
			t.Fatalf("%s: %v", what, err)
		}
	}
}

// xhttpBox creates an XHTTP box on its own domain.
func (h *harness) xhttpBox() (name, domain string) {
	h.t.Helper()
	u := uuid.New()
	name = fmt.Sprintf("x%d", uint64(u[0])<<40|uint64(u[1])<<32|uint64(u[2])<<24|
		uint64(u[3])<<16|uint64(u[4])<<8|uint64(u[5]))
	addr := fmt.Sprintf("2001:db8:%x%x:%x%x:%x%x::3", u[6], u[7], u[8], u[9], u[10], u[11])
	domain = name + ".example-relay.xyz"
	if err := h.svc.CreateBox(context.Background(), BoxSpec{
		Name: name, Address: addr, Provider: "vultr", Location: "amsterdam",
		Proto: ProtoVlessXhttp, Domain: domain,
	}); err != nil {
		h.t.Fatalf("create xhttp box: %v", err)
	}
	return name, domain
}

// An XHTTP box (П4в) keeps its domain sealed, mints one relay at a time on
// :443 with that domain and a secret path, serves it as an XHTTP record in its
// own lane beside the Reality relays, and refuses a domain it cannot use.
func TestAnXhttpBoxServesItsDomainOneRelayAtATime(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, domain := h.xhttpBox()

	var domainCT []byte
	if err := h.pool.QueryRow(ctx, `SELECT domain_ct FROM entry_boxes WHERE name = $1`, box).Scan(&domainCT); err != nil {
		t.Fatal(err)
	}
	if len(domainCT) == 0 || bytes.Contains(domainCT, []byte(domain)) {
		t.Fatal("the domain is not sealed at rest")
	}

	relay := h.relay(box, KindPersonal)
	id, err := h.svc.RelayIdentity(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}
	if id.Xhttp == nil || id.Xhttp.Domain != domain || id.Port != 443 || id.proto() != ProtoVlessXhttp {
		t.Fatalf("identity %+v %+v", id, id.Xhttp)
	}
	if _, err := h.svc.AddRelay(ctx, box, KindPersonal); !errors.Is(err, ErrRejected) {
		t.Fatalf("a second live relay on an xhttp box: %v", err)
	}

	h.personal(2)
	h.admit(relay)
	account := testAccount(t, h.pool)
	got := h.batch(account)
	if !slices.Contains(got, relay) {
		t.Fatalf("batch %v lacks %s", got, relay)
	}
	envs, err := h.svc.BatchFor(ctx, account, GrantMeta{})
	if err != nil {
		t.Fatal(err)
	}
	var streams, xhttp int
	for _, e := range envs {
		r, err := Verify(e, h.f.keys(t), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		switch r.Proto {
		case ProtoVlessXhttp:
			xhttp++
		case ProtoVlessRealityVision:
			streams++
		}
	}
	if xhttp != 1 || streams != h.svc.batchSize {
		t.Fatalf("served %d xhttp and %d reality records, want 1 and %d", xhttp, streams, h.svc.batchSize)
	}

	boxes, err := h.svc.openBoxes(ctx, h.pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range boxes {
		if b.Name == box && b.Domain != domain {
			t.Fatalf("console sees domain %q", b.Domain)
		}
	}

	for what, spec := range map[string]BoxSpec{
		"a domain on reality":       {Domain: "a.example-relay.xyz"},
		"the domain of another box": {Proto: ProtoVlessXhttp, Domain: domain},
		"the brand's domain":        {Proto: ProtoVlessXhttp, Domain: "relay." + config.Default().Base},
	} {
		// A fresh address each, so the refusal is about the domain, not a
		// taken address.
		u := uuid.New()
		spec.Name, spec.Provider, spec.Location = "y1", "vultr", "amsterdam"
		spec.Address = fmt.Sprintf("2001:db8:%x%x:%x%x:%x%x::4", u[6], u[7], u[8], u[9], u[10], u[11])
		if err := h.svc.CreateBox(ctx, spec); !errors.Is(err, ErrRejected) {
			t.Fatalf("%s: %v", what, err)
		}
	}
}

// testVantage is the slot the tests' vantages claim and answer as.
const testVantage = "ru-test"
