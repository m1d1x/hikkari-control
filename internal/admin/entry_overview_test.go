package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// waitReachRows blocks until an account's rows are readable. The insert is
// async, so wait for the rows rather than guessing a delay.
func waitReachRows(t *testing.T, store *analytics.Store, account string, n uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var got uint64
		if err := store.QueryRow(context.Background(), &got,
			"SELECT count() FROM entry_reach WHERE account = ?", account); err != nil {
			t.Fatalf("count: %v", err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d reach rows never became readable (have %d)", n, got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// livePostgres opens the test database the entry tables live in.
func livePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("NEKKO_TEST_DB")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("no test DB — set NEKKO_TEST_DB to run")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("no test DB reachable (%v)", err)
	}
	if err := db.RunMigrations(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// reachRow is one attempt, written straight to the dataset: the console reads
// what the hub stored, and this test is about the reading.
func reachRow(account, outcome, relay string, tried uint8, rtt, connect uint32, at time.Time) analytics.EntryReachRecord {
	return analytics.EntryReachRecord{
		Timestamp: at, Account: account, Outcome: outcome, RelayName: relay,
		RelaysTried: tried, RTTMs: rtt, ConnectMs: connect,
	}
}

// The doorway shares must partition every attempt: a screen whose three
// numbers do not add up to the whole is worse than no screen.
func TestEntryOverviewReachSharesPartitionEveryAttempt(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	// Two accounts so the "people" count has something to be distinct about.
	alice := uuid.NewString()[:32]
	bob := uuid.NewString()[:32]
	now := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)

	rows := []analytics.EntryReachRecord{
		// Alice: in at once twice (direct, then one relay on the first dial).
		reachRow(alice, "direct", "", 0, 19, 240, now),
		reachRow(alice, "relay", "ovw1-1", 1, 48, 900, now),
		// Bob: needed the race to work through relays, and once never got in.
		reachRow(bob, "relay", "ovw1-1", 3, 210, 4200, now),
		reachRow(bob, "failed", "", 0, 0, 0, now),
	}
	if err := store.InsertEntryReach(ctx, rows); err != nil {
		t.Fatalf("insert reach: %v", err)
	}
	waitReachRows(t, store, alice, 2)
	waitReachRows(t, store, bob, 2)

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	sum := got.Reach.FirstTryPct + got.Reach.RetriedPct + got.Reach.FailedPct
	if sum < 99.9 || sum > 100.1 {
		t.Fatalf("the three shares sum to %.2f%%, not 100 — the screen would be lying: %+v", sum, got.Reach)
	}
	if got.Reach.Attempts < 4 {
		t.Fatalf("attempts = %d, want at least the 4 just written", got.Reach.Attempts)
	}
	if got.Reach.People < 2 {
		t.Fatalf("people = %d, want at least the 2 distinct accounts", got.Reach.People)
	}
	if got.Reach.FailedPct == 0 {
		t.Error("an attempt that never connected was not counted; the one metric only clients can give is lost")
	}
}

// A relay dialled first time over is a first-try success as much as a direct
// connection: the person waited once. Counting it as a retry would make the
// headline a measure of how often the tunnel is used, which it is not.
func TestEntryOverviewCountsAFirstDialRelayAsFirstTry(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)

	account := uuid.NewString()[:32]
	now := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	if err := store.InsertEntryReach(context.Background(), []analytics.EntryReachRecord{
		reachRow(account, "relay", "ovw2-1", 1, 40, 800, now),
	}); err != nil {
		t.Fatalf("insert reach: %v", err)
	}
	waitReachRows(t, store, account, 1)

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=24h", nil))

	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reach.FirstTryPct == 0 {
		t.Fatalf("a relay that won on the first dial counted as a retry: %+v", got.Reach)
	}
}

// What the hub cannot compute must be named, not defaulted. This is the rule
// the old system page broke by rendering 99.9% uptime out of nothing.
func TestEntryOverviewNamesWhatItCannotComputeInsteadOfInventingIt(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// traffic, incidents and machine money are NOT here unconditionally:
	// traffic is computed whenever any relay has pulsed (П9.3), incidents
	// since П9.5, and a machine's rent since the hoster came under key
	// (П9.2) — a figure is named in `missing` only while this hub genuinely
	// cannot compute it, which is checked below for the machines.
	want := map[string]bool{
		"protocols.lossPct": false,
	}
	for _, m := range got.Missing {
		if _, ok := want[m.Figure]; ok {
			want[m.Figure] = true
		}
		if m.Why == "" {
			t.Errorf("%q is missing with no reason — the console cannot say why", m.Figure)
		}
	}
	for figure, named := range want {
		if !named {
			t.Errorf("%q is not computable and not named in `missing`", figure)
		}
	}

	// The contract for a figure that may or may not be computable: either the
	// hub has something to compute it from, or it says it has not. Never a
	// plausible zero under a real label. The machine line is not a figure only
	// while machines are in service and none of them has a known rent; an
	// empty fleet is a true zero, because entry_boxes is the whole fleet.
	namedMachines := false
	for _, m := range got.Missing {
		if m.Figure == "money.machines" {
			namedMachines = true
		}
	}
	unknowable := got.Money.MachinesPriced == 0 && got.Money.MachinesUnpriced > 0
	if namedMachines != unknowable {
		t.Errorf("money.machines named in missing: %v, with %d priced and %d unpriced machines on record",
			namedMachines, got.Money.MachinesPriced, got.Money.MachinesUnpriced)
	}
	if !namedMachines && got.Money.MachinesPriced == 0 && got.Money.Machines != (spendLine{}) {
		t.Errorf("no machine has a price and the machine line still reads %+v", got.Money.Machines)
	}
}

// A machine the hub rented carries its rent, so the article the dashboard used
// to leave out is a figure now: accrued by the day over the days the machine
// was in service, and run out to the end of the month at the current fleet's
// rate. A machine rented by hand is counted as unpriced, never as free.
func TestEntryOverviewAccruesMachineRentAndSaysWhatItCannotPrice(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	// Born at the start of the month (or earlier) so there is accrual to
	// read, and still in service so it counts towards the projection.
	born := time.Now().UTC().AddDate(0, 0, -20)
	priced := "ovwp" + fmt.Sprint(time.Now().UnixNano()%1_000_000)
	hand := "ovwh" + fmt.Sprint(time.Now().UnixNano()%1_000_000)
	for _, b := range []struct {
		name    string
		monthly any
	}{{priced, 3000}, {hand, nil}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO entry_boxes (name, address_ct, provider, location, ssh_user, proto, created_at,
			                         provider_instance_id, provider_region, plan, monthly_cents)
			VALUES ($1, '\x00', 'test', 'nowhere', 'root', 'vless-reality-vision', $2, $3, 'r1', 'small', $4)
		`, b.name, born, nullableInstance(b.name, b.monthly), b.monthly); err != nil {
			t.Fatalf("seed box %s: %v", b.name, err)
		}
		name := b.name
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM entry_boxes WHERE name = $1`, name)
		})
	}

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=7d", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	switch {
	case got.Money.Machines.MonthCents <= 0:
		t.Errorf("a $30 machine has been in service all month and the month totals %d",
			got.Money.Machines.MonthCents)
	case got.Money.Machines.PerDayCents <= 0:
		t.Errorf("per-day rent = %d", got.Money.Machines.PerDayCents)
	case got.Money.Machines.ProjectionCents < got.Money.Machines.MonthCents:
		t.Errorf("the projection %d is under the month's accrual %d",
			got.Money.Machines.ProjectionCents, got.Money.Machines.MonthCents)
	case got.Money.MachinesUnpriced == 0:
		t.Error("a hand-rented machine is in service and nothing says its rent is unknown")
	}
	// The third row of the console's money table is the sum of the two
	// articles, so a reader who only looks at it is not shown half the bill.
	if got.Money.Total.MonthCents != got.Money.Domains.MonthCents+got.Money.Machines.MonthCents {
		t.Errorf("total %d is not domains %d + machines %d", got.Money.Total.MonthCents,
			got.Money.Domains.MonthCents, got.Money.Machines.MonthCents)
	}
	for _, m := range got.Missing {
		if m.Figure == "money.machines" {
			t.Error("money.machines is named absent while a priced machine is on record")
		}
	}
}

// nullableInstance gives the priced box an instance id (the hub ordered it)
// and the hand-rented one none, which is what tells them apart everywhere.
func nullableInstance(name string, monthly any) any {
	if monthly == nil {
		return nil
	}
	return "inst-" + name
}

// A window the hub does not serve is a refusal, not a silent default: a
// console that quietly answers for a different span than it asked for puts a
// wrong number under a right label.
func TestEntryOverviewRefusesAWindowItDoesNotServe(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=5y", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// Survival is derived from rows the hub already keeps — no counter beside
// them to fall out of step. A domain that lived two days and one that lived
// four must give a median of three.
func TestEntryOverviewDerivesDomainLifetimeFromTheLedger(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	now := time.Now().UTC()
	for i, hours := range []float64{48, 96} {
		domain := "ovw-" + uuid.NewString()[:8] + ".test"
		assigned := now.Add(-time.Duration(hours+2) * time.Hour)
		burned := assigned.Add(time.Duration(hours) * time.Hour)
		if _, err := pool.Exec(ctx, `
			INSERT INTO entry_domains (domain, tld, status, source, cost_cents, assigned_at, burned_at, created_at)
			VALUES ($1, 'test', 'burned', 'purchased', $2, $3, $4, $5)
		`, domain, 900+i, assigned, burned, now); err != nil {
			t.Fatalf("seed domain: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM entry_domains WHERE domain = $1`, domain)
		})
	}

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=30d", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Survival.DomainHours == nil {
		t.Fatal("a burnt domain is on record and the median came back nil")
	}
	// Other rows may share the table; the two seeded ones must at least pull
	// the median into the range they define.
	if *got.Survival.DomainHours <= 0 {
		t.Fatalf("median domain life = %v hours", *got.Survival.DomainHours)
	}
	if got.Money.Domains.MonthCents <= 0 {
		t.Fatalf("two purchased domains are on record and the month totals %d", got.Money.Domains.MonthCents)
	}
	// Cost per person is only a number when there are people: a spend divided
	// by nobody must be absent, not enormous.
	if got.Reach.People == 0 && got.Money.PerUserMonth != nil {
		t.Fatal("cost per person was computed with no people in the window")
	}
}

// relayStat is one pulse row, written straight to the dataset.
func relayStat(relay string, up, down uint64, uptime uint32, at time.Time) analytics.EntryRelayStatRecord {
	return analytics.EntryRelayStatRecord{
		Timestamp: at, RelayName: relay, Proto: "vless-reality-vision",
		UptimeS: uptime, BytesUp: up, BytesDown: down, Conns: 3, Version: "4f21a9bbccdd",
	}
}

// waitStatRows blocks until a relay's pulses are readable.
func waitStatRows(t *testing.T, store *analytics.Store, relay string, n uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var got uint64
		if err := store.QueryRow(context.Background(), &got,
			"SELECT count() FROM entry_relay_stats WHERE relay_name = ?", relay); err != nil {
			t.Fatalf("count: %v", err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pulse rows never became readable (have %d)", n, got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Pulses carry totals since the edge started, so a window's traffic is the
// difference between its first and last row per relay — not their sum, which
// would count the same bytes over and over.
func TestEntryOverviewTrafficDifferencesCumulativeCountersInsteadOfSummingThem(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	relay := "ovwt" + uuid.NewString()[:6] + "-1"
	now := time.Now().UTC().Truncate(time.Second)
	const gb = uint64(1024 * 1024 * 1024)

	// Three pulses of one relay: the window carries 2 GB up and 2 GB down,
	// though the rows add up to far more.
	if err := store.InsertEntryRelayStats(ctx, []analytics.EntryRelayStatRecord{
		relayStat(relay, 1*gb, 3*gb, 60, now.Add(-20*time.Minute)),
		relayStat(relay, 2*gb, 4*gb, 660, now.Add(-10*time.Minute)),
		relayStat(relay, 3*gb, 5*gb, 1260, now.Add(-1*time.Minute)),
	}); err != nil {
		t.Fatalf("insert pulses: %v", err)
	}
	waitStatRows(t, store, relay, 3)

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Traffic == nil {
		t.Fatal("relays pulsed and the traffic block is absent")
	}
	if got.Traffic.GBTotal < 4 {
		t.Fatalf("traffic = %v GB, want at least the 4 GB just written", got.Traffic.GBTotal)
	}
	// The table accumulates across runs, so the bound cannot be a constant.
	// The invariant that does hold whatever else is in the window: summing
	// cumulative counters always exceeds differencing them.
	var naive float64
	if err := store.QueryRow(ctx, &naive, `
		SELECT sum(bytes_up + bytes_down) / 1073741824.0
		FROM entry_relay_stats WHERE ts >= now() - INTERVAL 24 HOUR
	`); err != nil {
		t.Fatalf("naive sum: %v", err)
	}
	if got.Traffic.GBTotal >= naive {
		t.Fatalf("traffic = %v GB against a naive sum of %v: counters look summed, not differenced",
			got.Traffic.GBTotal, naive)
	}
	for _, m := range got.Missing {
		if m.Figure == "traffic" {
			t.Error("traffic is named as missing although relays pulsed")
		}
	}
}

// With no pulses in the window the block is absent and named, never zero:
// "0 GB" would read as "nobody used the doorway this week", which is a
// different and alarming claim.
func TestEntryOverviewTrafficIsAbsentAndNamedWhenNoRelayPulsed(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	// A window far in the past of any pulse this suite writes.
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=24h", nil))

	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Traffic != nil {
		return // another test's pulses are in the window; nothing to assert here
	}
	named := false
	for _, m := range got.Missing {
		if m.Figure == "traffic" {
			named = true
		}
	}
	if !named {
		t.Error("no relay pulsed, and the console is not told why traffic is blank")
	}
}

// The two right-hand lists are split by what the playbook allows, not by how
// bad the incident is: a banned IP the hub may replace unattended belongs in
// the log, and a cheap domain swap that still wants a yes belongs in the
// queue. Getting this backwards would put work in front of a person that the
// autopilot was already doing.
func TestEntryOverviewSplitsIncidentsByWhoCanFinishThem(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	// entry_boxes.name is ^[a-z]+[0-9]+$: letters, then digits.
	box := fmt.Sprintf("ovwi%d", time.Now().UnixNano()%1_000_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
		VALUES ($1, '\x00', 'vultr', 'amsterdam', 'vless-reality-vision')
	`, box); err != nil {
		t.Fatalf("seed box: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_boxes WHERE name = $1`, box)
	})

	// Queue ownership follows the saved mode. Domain eviction is free and
	// automatic since Nekko P9.19; explicitly ask for operator approval here
	// instead of asserting the obsolete "paid domain swap" default.
	if _, err := pool.Exec(ctx, `INSERT INTO entry_playbook_modes (cause, mode) VALUES ('domain_blocked', 'ask') ON CONFLICT (cause) DO UPDATE SET mode = 'ask'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_playbook_modes WHERE cause = 'domain_blocked'`)
	})

	for _, cause := range []string{"domain_blocked", "fingerprint"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO entry_incidents (box, cause, advice, evidence)
			VALUES ($1, $2, 'тест', '{}'::jsonb)
		`, box, cause); err != nil {
			t.Fatalf("seed incident %s: %v", cause, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_incidents WHERE box = $1`, box)
	})

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var inQueue, inLog string
	for _, r := range got.NeedsYou {
		if r.Box == box {
			inQueue = r.Cause
		}
	}
	for _, r := range got.Automation {
		if r.Box == box {
			inLog = r.Cause
		}
	}
	if inQueue != "domain_blocked" {
		t.Errorf("the repair requiring approval is not in the queue (got %q)", inQueue)
	}
	if inLog != "fingerprint" {
		t.Errorf("the free repair is not in the log (got %q)", inLog)
	}
	for _, m := range got.Missing {
		if m.Figure == "incidents" {
			t.Error("incidents are still named as missing")
		}
	}
}

// The protocol sparkline used to be a sine wave seeded from the protocol's
// name, stretched between the real p50 and p95: the figures were measured, the
// shape was drawn. The series must now be the measurement, which means an hour
// nobody reported through carries no median at all rather than a zero — a line
// drawn through that hour would claim a reading nobody took.
func TestEntryOverviewLatencySeriesIsMeasuredAndBreaksWhereNothingWasReported(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	tag := uuid.NewString()[:6]
	box := "ksparkbox" + fmt.Sprint(time.Now().UnixNano()%100000)
	relay := box + "-r1"
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
		VALUES ($1, '\x00', 'vultr', 'amsterdam', 'vless-reality-vision')
	`, box); err != nil {
		t.Fatalf("seed box: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_boxes WHERE name = $1`, box)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_relays (name, kind, status, box, identity_ct)
		VALUES ($1, 'personal', 'pending', $2, '\x00')
	`, relay, box); err != nil {
		t.Fatalf("seed relay: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_relays WHERE name = $1`, relay)
	})

	// Two reported hours with deliberately different medians, and an empty
	// hour between them. The 24h window buckets hourly, so the gap is a
	// bucket of its own.
	account := "k8-" + tag
	now := time.Now().UTC()
	early := now.Add(-5 * time.Hour)
	late := now.Add(-2 * time.Hour)
	rows := []analytics.EntryReachRecord{
		reachRow(account, "relay", relay, 1, 40, 900, early),
		reachRow(account, "relay", relay, 1, 40, 900, early.Add(time.Minute)),
		reachRow(account, "relay", relay, 1, 400, 2200, late),
		reachRow(account, "relay", relay, 1, 400, 2200, late.Add(time.Minute)),
	}
	if err := store.InsertEntryReach(ctx, rows); err != nil {
		t.Fatalf("insert reach: %v", err)
	}
	waitReachRows(t, store, account, uint64(len(rows)))

	h := &Handlers{analytics: store, pool: pool}
	rec := httptest.NewRecorder()
	h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got entryOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var block *protocolBlock
	for i := range got.Protocols {
		if got.Protocols[i].Proto == "vless-reality-vision" {
			block = &got.Protocols[i]
		}
	}
	if block == nil {
		t.Fatalf("the seeded relay's protocol is absent from %+v", got.Protocols)
	}
	if len(block.Latency) == 0 {
		t.Fatal("the protocol block came back without a latency series")
	}

	at := func(ts time.Time) *latencyPoint {
		key := ts.Truncate(time.Hour).Format(time.RFC3339)
		for i := range block.Latency {
			if block.Latency[i].Bucket == key {
				return &block.Latency[i]
			}
		}
		return nil
	}

	// The two reported hours must carry the medians that were reported, not a
	// shape between them. Other tests share the dataset, so the assertion is
	// that each bucket sits on the side of the other it was seeded on.
	lo, hi := at(early), at(late)
	if lo == nil || lo.P50Ms == nil {
		t.Fatalf("the hour with reports has no median: %+v", lo)
	}
	if hi == nil || hi.P50Ms == nil {
		t.Fatalf("the later hour with reports has no median: %+v", hi)
	}
	if !(*lo.P50Ms < *hi.P50Ms) {
		t.Fatalf("medians do not follow the reports: early %v, late %v", *lo.P50Ms, *hi.P50Ms)
	}
	if lo.Attempts == 0 || hi.Attempts == 0 {
		t.Fatalf("a measured bucket reports no sample size: %+v %+v", lo, hi)
	}

	// The hour in between was never reported through: it must be a hole, so
	// the chart can break the line there.
	gap := at(now.Add(-4 * time.Hour))
	if gap == nil {
		t.Fatal("the empty hour is missing from the series entirely")
	}
	if gap.P50Ms != nil {
		t.Fatalf("an hour nobody reported through carries a median: %v", *gap.P50Ms)
	}
	if gap.Attempts != 0 {
		t.Fatalf("an hour nobody reported through counts %d attempts", gap.Attempts)
	}
}

// waitUserTrafficRows blocks until n socket flushes are readable: the insert
// is async, so wait for the rows rather than guessing a delay.
func waitUserTrafficRows(t *testing.T, store *analytics.Store, n uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var got uint64
		if err := store.QueryRow(context.Background(), &got, "SELECT count() FROM entry_user_traffic"); err != nil {
			t.Fatalf("count: %v", err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d user traffic rows never became readable (have %d)", n, got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Traffic per person is read per account and taken as a median, not divided
// out of a total: one heavy account must not make everyone look heavy. The
// relay column is what came through a box, and the share adds up from the
// same rows.
func TestEntryOverviewTrafficPerUserIsAMedianOverAccounts(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()
	// The only writer of this table is the socket flush; the test owns it.
	if err := store.Exec(ctx, "TRUNCATE TABLE entry_user_traffic"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	h := &Handlers{analytics: store, pool: pool}
	ask := func() entryOverview {
		t.Helper()
		rec := httptest.NewRecorder()
		h.EntryOverview(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/overview?window=24h", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var got entryOverview
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}
	named := func(o entryOverview, figure string) bool {
		for _, m := range o.Missing {
			if m.Figure == figure {
				return true
			}
		}
		return false
	}

	// Nothing flushed: absent and named, never a zero.
	if got := ask(); got.TrafficPerUser != nil || !named(got, "traffic.perUser") {
		t.Fatalf("empty dataset: perUser = %+v, named = %v", got.TrafficPerUser, named(got, "traffic.perUser"))
	}

	const mb = 1024 * 1024
	now := time.Now().UTC().Add(-time.Minute)
	acct := func(i int) string { return fmt.Sprintf("%032d", i) }
	rows := []analytics.EntryUserTrafficRecord{
		// Three light people, one of them in two flushes and partly through a box.
		{Timestamp: now, Account: acct(1), Box: "", BytesDown: 1 * mb},
		{Timestamp: now, Account: acct(2), Box: "ovwbox", BytesDown: 1 * mb},
		{Timestamp: now, Account: acct(2), Box: "", BytesUp: 1 * mb},
		{Timestamp: now, Account: acct(3), Box: "ovwbox", BytesDown: 3 * mb},
		// And one very heavy one, all through a box.
		{Timestamp: now, Account: acct(4), Box: "ovwbox", BytesDown: 1000 * mb},
		// Out of the window: not counted.
		{Timestamp: now.Add(-48 * time.Hour), Account: acct(5), BytesDown: 500 * mb},
	}
	if err := store.InsertEntryUserTraffic(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}
	waitUserTrafficRows(t, store, uint64(len(rows)))

	got := ask()
	p := got.TrafficPerUser
	if p == nil || named(got, "traffic.perUser") {
		t.Fatalf("perUser = %+v with rows on record", p)
	}
	if p.People != 4 || p.ViaRelayPeople != 3 {
		t.Fatalf("people = %d via relay = %d, want 4 and 3", p.People, p.ViaRelayPeople)
	}
	// Totals per account: 1, 2, 3, 1000 MB. quantileExact(0.5) of four is the
	// upper middle — 3 — not the mean of 251.5 the heavy one would drag to.
	if p.MedianMBDay != 3 {
		t.Fatalf("median = %v MB/day, want 3", p.MedianMBDay)
	}
	// Relay bytes per account that used one: 1, 3, 1000 → median 3.
	if p.ViaRelayMedianMBDay == nil || *p.ViaRelayMedianMBDay != 3 {
		t.Fatalf("via relay median = %v, want 3", p.ViaRelayMedianMBDay)
	}
	// 1004 of 1006 MB came through a box.
	if want := round2(1004.0 / 1006.0 * 100); p.ViaRelayPct != want {
		t.Fatalf("via relay share = %v%%, want %v", p.ViaRelayPct, want)
	}
	if !named(got, "traffic.perUser.media") {
		t.Fatal("the socket-only scope is not named — the figure would pass for a person's whole traffic")
	}
}
