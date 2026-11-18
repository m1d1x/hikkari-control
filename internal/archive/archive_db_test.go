package archive

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// dbPool applies the real migrations to a scratch database.
func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if adminURL == "" {
		t.Skip("no test DB — set TEST_DATABASE_URL to run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("cannot reach test DB: %v", err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("hikkari_archive_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return pool
}

// memSink is the archive in memory; drop names ids it "loses" on write.
type memSink struct {
	mu     sync.Mutex
	trunk  map[int64]analytics.TrunkRunRow
	chunks map[int64]analytics.LogChunkRow
	drop   map[int64]bool
}

func newMemSink() *memSink {
	return &memSink{trunk: map[int64]analytics.TrunkRunRow{}, chunks: map[int64]analytics.LogChunkRow{}, drop: map[int64]bool{}}
}

func (s *memSink) ArchiveTrunkRuns(_ context.Context, rows []analytics.TrunkRunRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		if s.drop[r.ID] {
			delete(s.drop, r.ID)
			continue
		}
		s.trunk[r.ID] = r
	}
	return nil
}

func (s *memSink) ArchiveLogChunks(_ context.Context, rows []analytics.LogChunkRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		s.chunks[r.ID] = r
	}
	return nil
}

func (s *memSink) ids(table string) map[int64]bool {
	out := map[int64]bool{}
	if table == "archive_trunk_runs" {
		for id := range s.trunk {
			out[id] = true
		}
	} else {
		for id := range s.chunks {
			out[id] = true
		}
	}
	return out
}

func (s *memSink) ArchiveMaxID(_ context.Context, table string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var top int64
	for id := range s.ids(table) {
		top = max(top, id)
	}
	return top, nil
}

func (s *memSink) ArchivedIDs(_ context.Context, table string, lo, hi int64) (map[int64]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int64]bool{}
	for id := range s.ids(table) {
		if id >= lo && id <= hi {
			out[id] = true
		}
	}
	return out, nil
}

func addTrunk(t *testing.T, pool *pgxpool.Pool, ts time.Time, data string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO trunk_runs (ts, entry_box, exit_box, layer, target, ok, data)
		VALUES ($1, 'aezamsk1', 'unl1', 'call', 'copies1', true, $2::jsonb) RETURNING id`, ts, data).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func countPG(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEveryRowIsArchivedAndOnlyOldArchivedRowsLeavePostgres(t *testing.T) {
	pool := dbPool(t)
	sink := newMemSink()
	now := time.Now()
	old := addTrunk(t, pool, now.Add(-40*24*time.Hour), `{"gaps60": 3}`)
	fresh := addTrunk(t, pool, now.Add(-time.Hour), `{"gaps60": 0}`)

	m := New(pool, sink)
	m.Once(context.Background())

	if len(sink.trunk) != 2 {
		t.Fatalf("archive holds %d rows, want both", len(sink.trunk))
	}
	if got := sink.trunk[old].Data; !strings.Contains(got, `"gaps60": 3`) {
		t.Fatalf("archived data = %q", got)
	}
	if countPG(t, pool, "trunk_runs") != 1 {
		t.Fatal("the old archived row is still in Postgres, or the fresh one left")
	}
	var left int64
	_ = pool.QueryRow(context.Background(), "SELECT id FROM trunk_runs").Scan(&left)
	if left != fresh {
		t.Fatalf("Postgres kept %d, want the fresh row %d", left, fresh)
	}
}

func TestARowTheArchiveLostIsCopiedAgainBeforeItLeaves(t *testing.T) {
	pool := dbPool(t)
	sink := newMemSink()
	id := addTrunk(t, pool, time.Now().Add(-40*24*time.Hour), `{}`)
	sink.drop[id] = true // the first write of it is lost

	m := New(pool, sink)
	m.Once(context.Background())
	if countPG(t, pool, "trunk_runs") != 1 {
		t.Fatal("a row the archive did not hold left Postgres")
	}
	if _, ok := sink.trunk[id]; !ok {
		t.Fatal("the lost row was not copied again")
	}
	m.Once(context.Background())
	if countPG(t, pool, "trunk_runs") != 0 {
		t.Fatal("the row the archive now holds stayed in Postgres")
	}
}

func TestATestersLogLeavesPostgresAfterAWeekOnlyIntoTheArchive(t *testing.T) {
	pool := dbPool(t)
	sink := newMemSink()
	ctx := context.Background()
	for _, age := range []time.Duration{8 * 24 * time.Hour, time.Hour} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO support_tester_log_chunks (install_id, platform, version, lines, body, received_at)
			VALUES ('t-1', 'windows', '1.0.92', 1, 'line', $1)`, time.Now().Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	New(pool, sink).Once(ctx)
	if len(sink.chunks) != 2 {
		t.Fatalf("archive holds %d chunks, want 2", len(sink.chunks))
	}
	if countPG(t, pool, "support_tester_log_chunks") != 1 {
		t.Fatal("Postgres does not hold exactly the last week")
	}
}

func TestOnlyEndedQueueRowsOlderThan90DaysAreDropped(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	old := time.Now().Add(-100 * 24 * time.Hour)
	for _, st := range []string{"done", "queued"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO stand_tasks (slot, knob, vals, rounds, baseline_path, status, created_at)
			VALUES ('s', 'k', '{}', 1, '', $1, $2)`, st, old); err != nil {
			t.Fatal(err)
		}
	}
	New(pool, newMemSink()).Once(ctx)
	var status string
	if err := pool.QueryRow(ctx, "SELECT string_agg(status, ',') FROM stand_tasks").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("stand tasks left: %q, want the open one only", status)
	}
}
