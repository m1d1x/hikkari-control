package admin

import (
	"context"
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
func liveClickHouse(t *testing.T) *analytics.Store {
	t.Helper()
	dsn := os.Getenv("CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLICKHOUSE_TEST_DSN not set")
	}
	store, err := analytics.NewStore(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
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
