package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const testDatabaseEnv = "TEST_DATABASE_URL"

// TestBaselineAppliesToEmptyDatabase is the gate on the squashed schema: it
// creates a scratch database, runs the migration set against it exactly the way
// the server does at boot, and asserts the tables are there.
//
// It exists because 001_baseline.sql is a pg_dump, not hand-written SQL, and a
// dump carries things pgx cannot execute — psql meta-commands, a session SET
// preamble, and a schema_migrations table the runner creates itself. Applying
// it with `psql` proves nothing about the boot path; this does.
func TestBaselineAppliesToEmptyDatabase(t *testing.T) {
	adminURL := strings.TrimSpace(os.Getenv(testDatabaseEnv))
	if adminURL == "" {
		t.Skipf("no test DB — set %s to run", testDatabaseEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("cannot reach test DB: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Skipf("cannot reach test DB: %v", err)
	}

	// Unique name so parallel packages cannot collide on it.
	scratch := fmt.Sprintf("hikkari_baseline_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+scratch); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	defer func() {
		// Own context: the test's is already cancelled by the time this runs
		// on a failure path, and a leaked database would break the next run.
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dropCancel()
		if _, err := admin.Exec(dropCtx, "DROP DATABASE IF EXISTS "+scratch+" WITH (FORCE)"); err != nil {
			t.Logf("drop scratch database: %v", err)
		}
	}()

	pool, err := pgxpool.New(ctx, replaceDBName(adminURL, scratch))
	if err != nil {
		t.Fatalf("connect to scratch database: %v", err)
	}
	defer pool.Close()

	// A hub and a CLI may start together against an empty database.
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- RunMigrations(ctx, pool) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent migration on empty database: %v", err)
		}
	}

	var tables int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM information_schema.tables WHERE table_schema='public'",
	).Scan(&tables); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	// The baseline must contain ONLY the control plane: a forgotten chat,
	// file or SFU table is a regression, even when all fleet queries work.
	// The routing presets clients fetch are the control plane's too, and so
	// are plans, subscribers' access keys and what those keys carried.
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "schema_migrations" && name != "hub_accounts" && name != "hub_subscriptions" &&
			name != "hub_access_keys" && name != "hub_key_usage" && name != "hub_plans" &&
			name != "hub_orders" && name != "hub_mail_outbox" &&
			!strings.HasPrefix(name, "entry_") && !strings.HasPrefix(name, "admin_") && name != "admins" &&
			name != "presets" && !strings.HasPrefix(name, "preset_") {
			t.Errorf("non-control table in baseline: %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if tables < 30 {
		t.Fatalf("fleet schema incomplete: %d tables", tables)
	}
	for _, table := range []string{"entry_detection_policy", "entry_bench_policy", "entry_spend_caps"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("missing seed: %s", table)
		}
	}
	var references int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND confrelid = 'hub_accounts'::regclass`).Scan(&references); err != nil {
		t.Fatal(err)
	}
	if references != 6 {
		t.Errorf("account foreign keys: %d, want grants, fail reports, subscriptions, access keys, key usage and orders", references)
	}
	var cap int
	if err := pool.QueryRow(ctx, `SELECT sum(daily_cap_cents + monthly_cap_cents) FROM entry_spend_caps`).Scan(&cap); err != nil {
		t.Fatal(err)
	}
	if cap != 0 {
		t.Error("fresh hub can spend before owner configured caps")
	}

	// Re-running must be a no-op: the runner keys on filename, and a second
	// pass that re-executed CREATE TABLE would fail here.
	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations is not idempotent: %v", err)
	}
	// A previous experimental database is refused, without erasing accounts
	// or updating migration history to pretend the baseline landed.
	if _, err := pool.Exec(ctx, `INSERT INTO hub_accounts (name) VALUES ('preserved'); INSERT INTO schema_migrations(filename) VALUES ('001_baseline.sql')`); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, pool); err == nil || !strings.Contains(err.Error(), "legacy schema") {
		t.Fatalf("legacy database accepted: %v", err)
	}
	var preserved bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hub_accounts WHERE name='preserved')`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("legacy refusal lost data: %v", err)
	}
	t.Logf("baseline applied to an empty database: %d tables", tables)
}

// replaceDBName swaps the database name in a postgres URL, keeping everything
// else (credentials, host, query params) intact.
func replaceDBName(url, name string) string {
	q := ""
	if i := strings.Index(url, "?"); i >= 0 {
		q = url[i:]
		url = url[:i]
	}
	if i := strings.LastIndex(url, "/"); i >= 0 {
		url = url[:i]
	}
	return url + "/" + name + q
}
