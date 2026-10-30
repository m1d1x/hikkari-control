package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
)

// TestCensorProbes_AgainstClickHouse round-trips a sample through the real
// entry_probes table and the Censor reader. The column types live in
// ClickHouse, not in Go, so only a live store proves the scan matches them —
// a Float32 column scanned into float64 compiles fine and 500s at runtime.
//
// Needs CLICKHOUSE_TEST_DSN, e.g. the local compose stack:
//
//	CLICKHOUSE_TEST_DSN=clickhouse://nekko:nekko@127.0.0.1:9000/nekko_analytics
//
// Each test gets a database of its own, dropped when it ends: series and
// medians are read across the whole table, so rows another test (or an
// earlier run) left behind would land in the buckets this one asserts on.
func liveClickHouse(t *testing.T) *analytics.Store {
	t.Helper()
	dsn := os.Getenv("CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLICKHOUSE_TEST_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("CLICKHOUSE_TEST_DSN: %v", err)
	}
	admin, err := analytics.NewStore(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random: %v", err)
	}
	db := "t_" + hex.EncodeToString(suffix[:])
	if err := admin.Exec(context.Background(), "CREATE DATABASE "+db); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)
		_ = admin.Close()
	})
	u.Path = "/" + db
	store, err := analytics.NewStore(u.String())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return store
}

// waitRows blocks until relay has n rows readable. The insert is async
// (wait_for_async_insert=0): wait for the rows rather than guessing a delay.
func waitRows(t *testing.T, store *analytics.Store, relay string, n uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var got uint64
		if err := store.QueryRow(context.Background(), &got,
			"SELECT count() FROM entry_probes WHERE relay_name = ?", relay); err != nil {
			t.Fatalf("count: %v", err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d rows for %s never became readable (have %d)", n, relay, got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
