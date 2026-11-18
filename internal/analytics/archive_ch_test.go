package analytics

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Against a real ClickHouse: TEST_CLICKHOUSE_DSN, skipped without it.
func archiveStore(t *testing.T) *Store {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_CLICKHOUSE_DSN"))
	if dsn == "" {
		t.Skip("no test ClickHouse — set TEST_CLICKHOUSE_DSN to run")
	}
	s, err := NewStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RunMigrations(context.Background()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return s
}

func TestTheArchiveKeepsTrunkRowsAndChunksAsWritten(t *testing.T) {
	s := archiveStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	ok := true
	if err := s.ArchiveTrunkRuns(ctx, []TrunkRunRow{
		{ID: 7, TS: ts, EntryBox: "aezamsk1", ExitBox: "unl1", Layer: "call", Target: "copies1", OK: &ok, Data: `{"gaps60": 3}`},
		{ID: 8, TS: ts, RoundTS: &ts, EntryBox: "aezamsk1", ExitBox: "unl1", Layer: "event", Data: `{}`},
	}); err != nil {
		t.Fatal(err)
	}
	// Written twice, as the overlap does: still one row per id on read.
	if err := s.ArchiveLogChunks(ctx, []LogChunkRow{
		{ID: 100, InstallID: "t-arch", Platform: "windows", Version: "1.0.92", Lines: 2, Body: "a\nb", ReceivedAt: ts},
		{ID: 100, InstallID: "t-arch", Platform: "windows", Version: "1.0.92", Lines: 2, Body: "a\nb", ReceivedAt: ts},
	}); err != nil {
		t.Fatal(err)
	}
	top, err := s.ArchiveMaxID(ctx, "archive_trunk_runs")
	if err != nil || top < 8 {
		t.Fatalf("max id = %d, %v", top, err)
	}
	ids, err := s.ArchivedIDs(ctx, "archive_trunk_runs", 7, 8)
	if err != nil || !ids[7] || !ids[8] {
		t.Fatalf("archived ids = %v, %v", ids, err)
	}
	got, err := s.ReadArchivedChunks(ctx, "t-arch", ts.Add(-time.Hour), ts.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "a\nb" || !got[0].ReceivedAt.Equal(ts) {
		t.Fatalf("read back %+v", got)
	}
	if _, err := s.ArchiveMaxID(ctx, "users; DROP TABLE x"); err == nil {
		t.Fatal("a table name outside the archive was accepted")
	}
}

func TestOurTablesKeepNoTTL(t *testing.T) {
	s := archiveStore(t)
	for _, tb := range fleetTablesKept {
		var engine string
		if err := s.conn.QueryRow(context.Background(), `SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = ?`, tb).Scan(&engine); err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
		if containsTTL(engine) {
			t.Errorf("%s still deletes rows: %s", tb, engine)
		}
	}
}

func TestOnlyTheServersOwnLeftoverLogsAreDropped(t *testing.T) {
	for name, want := range map[string]bool{
		"processors_profile_log": true, "part_log": true, "metric_log_0": true, "error_log_12": true,
		"metric_log": false, "error_log": false, "asynchronous_metric_log": false,
		"tables": false, "metric_log_x": false, "hikkari_obs": false,
	} {
		if got := serverLogLeftover(name); got != want {
			t.Errorf("serverLogLeftover(%q) = %v, want %v", name, got, want)
		}
	}
}
