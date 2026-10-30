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
	scratch := fmt.Sprintf("nekko_baseline_%d", time.Now().UnixNano())
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

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations on an empty database: %v", err)
	}

	var tables int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM information_schema.tables WHERE table_schema='public'",
	).Scan(&tables); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	// 70 at the squash; the assertion is "the schema landed", not an exact
	// count, so it does not have to be edited every time a table is added.
	if tables < 60 {
		t.Fatalf("expected the full schema, got %d tables", tables)
	}

	// Seed rows are the half a schema-only dump loses silently: the schema
	// looks perfect and the process dies later on a missing support user or an
	// empty fraud config. Assert the rows the code assumes it will find.
	for _, want := range []struct {
		what  string
		query string
	}{
		{"support system user", `SELECT count(*) FROM users WHERE id = '00000000-0000-0000-0000-000000000001' AND is_system`},
		{"fraud datacenter patterns", `SELECT count(*) FROM fraud_datacenter_patterns`},
		{"fraud scoring signals", `SELECT count(*) FROM fraud_scoring_signals`},
		{"fraud action thresholds", `SELECT count(*) FROM fraud_action_thresholds WHERE id = 1`},
		{"mailer settings singleton", `SELECT count(*) FROM mailer_settings WHERE id = 1`},
	} {
		var n int
		if err := pool.QueryRow(ctx, want.query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", want.what, err)
		}
		if n == 0 {
			t.Errorf("%s: baseline seeded no rows", want.what)
		}
	}

	// Re-running must be a no-op: the runner keys on filename, and a second
	// pass that re-executed CREATE TABLE would fail here.
	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations is not idempotent: %v", err)
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
