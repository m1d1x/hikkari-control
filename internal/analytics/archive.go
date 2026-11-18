package analytics

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// The archive keeps our own measurements forever (owner's decision 18.11:
// «замеры не удалять, переносить в сжатое»). Postgres holds the fresh rows the
// hub reads all the time; every row is copied here as it lands and stays,
// compressed, after Postgres lets it go (internal/archive). Only the
// ClickHouse server's own logs about itself are cleaned.

// fleetTablesKept are our tables that once carried a 180-day TTL: fleet,
// relay and tester measurements. The users' telemetry keeps its 90 days
// (hikkari-client docs/TELEMETRY.md promises it).
var fleetTablesKept = []string{
	"entry_probes", "entry_benches", "entry_reach", "entry_relay_stats",
	"entry_user_traffic", "hikkari_obs", "tester_legs",
}

// serverLogsOff are the ClickHouse server's logs about itself that
// deploy/clickhouse-privacy.xml turns off: the tables they left are dropped.
// The ones it keeps (metric_log, asynchronous_metric_log, error_log) carry a
// week's TTL there; when the server finds a log's table unlike its config it
// renames the old one to <name>_<n> and starts a new one, and those old
// copies are dropped too.
var serverLogsOff = []string{
	"processors_profile_log", "part_log", "asynchronous_insert_log", "query_views_log",
	"query_log", "query_thread_log", "text_log", "trace_log", "session_log",
}

var serverLogsKept = []string{"metric_log", "asynchronous_metric_log", "error_log"}

func (s *Store) migrateArchive(ctx context.Context) error {
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS archive_trunk_runs (
			id        Int64,
			ts        DateTime64(6, 'UTC') CODEC(Delta, ZSTD(1)),
			round_ts  Nullable(DateTime64(6, 'UTC')),
			entry_box LowCardinality(String),
			exit_box  LowCardinality(String),
			layer     LowCardinality(String),
			vantage   LowCardinality(String),
			target    LowCardinality(String),
			ok        Nullable(UInt8),
			data      String CODEC(ZSTD(3))
		) ENGINE = ReplacingMergeTree()
		PARTITION BY toYYYYMM(ts)
		ORDER BY (entry_box, layer, ts, id)
	`); err != nil {
		return fmt.Errorf("archive schema trunk_runs: %w", err)
	}
	if err := s.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS archive_tester_log_chunks (
			id          Int64,
			install_id  String,
			platform    LowCardinality(String),
			version     LowCardinality(String),
			lines       Int32,
			dropped     Int32,
			body        String CODEC(ZSTD(3)),
			received_at DateTime64(6, 'UTC') CODEC(Delta, ZSTD(1))
		) ENGINE = ReplacingMergeTree()
		PARTITION BY toYYYYMM(received_at)
		ORDER BY (install_id, received_at, id)
	`); err != nil {
		return fmt.Errorf("archive schema tester_log_chunks: %w", err)
	}
	for _, t := range fleetTablesKept {
		var ttl string
		if err := s.conn.QueryRow(ctx, `SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = ?`, t).Scan(&ttl); err != nil {
			continue
		}
		if !containsTTL(ttl) {
			continue
		}
		if err := s.conn.Exec(ctx, "ALTER TABLE "+t+" REMOVE TTL"); err != nil {
			return fmt.Errorf("keep %s: %w", t, err)
		}
	}
	// The server's own logs: a failure here is not ours to stop on.
	rows, err := s.conn.Query(ctx, `SELECT name FROM system.tables WHERE database = 'system'`)
	if err != nil {
		log.Printf("clickhouse: server logs not listed: %v", err)
		return nil
	}
	var drop []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			break
		}
		if serverLogLeftover(name) {
			drop = append(drop, name)
		}
	}
	rows.Close()
	for _, name := range drop {
		if err := s.conn.Exec(ctx, "DROP TABLE IF EXISTS system."+name+" SYNC"); err != nil {
			log.Printf("clickhouse: server log %s not dropped: %v", name, err)
		}
	}
	return nil
}

// serverLogLeftover says whether a table of the system database is one the
// server's config no longer writes: a log turned off, or an old copy of a log
// renamed when its config changed.
func serverLogLeftover(name string) bool {
	for _, off := range serverLogsOff {
		if name == off || isRenamedCopy(name, off) {
			return true
		}
	}
	for _, kept := range serverLogsKept {
		if isRenamedCopy(name, kept) {
			return true
		}
	}
	return false
}

// isRenamedCopy: name is base_<digits>.
func isRenamedCopy(name, base string) bool {
	rest, ok := strings.CutPrefix(name, base+"_")
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func containsTTL(engineFull string) bool {
	for i := 0; i+4 <= len(engineFull); i++ {
		if engineFull[i:i+4] == " TTL" {
			return true
		}
	}
	return false
}

// TrunkRunRow is one row of trunk_runs as the archive holds it.
type TrunkRunRow struct {
	ID       int64
	TS       time.Time
	RoundTS  *time.Time
	EntryBox string
	ExitBox  string
	Layer    string
	Vantage  string
	Target   string
	OK       *bool
	Data     string
}

// LogChunkRow is one chunk of a tester's log as the archive holds it.
type LogChunkRow struct {
	ID         int64
	InstallID  string
	Platform   string
	Version    string
	Lines      int32
	Dropped    int32
	Body       string
	ReceivedAt time.Time
}

// ArchiveTrunkRuns writes rows to archive_trunk_runs. Synchronous: the caller
// deletes from Postgres only what this has written.
func (s *Store) ArchiveTrunkRuns(ctx context.Context, rows []TrunkRunRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO archive_trunk_runs
		(id, ts, round_ts, entry_box, exit_box, layer, vantage, target, ok, data)`)
	if err != nil {
		return fmt.Errorf("prepare archive_trunk_runs: %w", err)
	}
	for _, r := range rows {
		var ok *uint8
		if r.OK != nil {
			v := uint8(0)
			if *r.OK {
				v = 1
			}
			ok = &v
		}
		if err := batch.Append(r.ID, r.TS.UTC(), utcPtr(r.RoundTS), r.EntryBox, r.ExitBox, r.Layer, r.Vantage, r.Target, ok, r.Data); err != nil {
			return fmt.Errorf("append archive_trunk_runs: %w", err)
		}
	}
	return batch.Send()
}

// ArchiveLogChunks writes chunks to archive_tester_log_chunks, synchronously.
func (s *Store) ArchiveLogChunks(ctx context.Context, rows []LogChunkRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO archive_tester_log_chunks
		(id, install_id, platform, version, lines, dropped, body, received_at)`)
	if err != nil {
		return fmt.Errorf("prepare archive_tester_log_chunks: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(r.ID, r.InstallID, r.Platform, r.Version, r.Lines, r.Dropped, r.Body, r.ReceivedAt.UTC()); err != nil {
			return fmt.Errorf("append archive_tester_log_chunks: %w", err)
		}
	}
	return batch.Send()
}

// ArchiveMaxID is the highest id the archive table holds, 0 when empty.
func (s *Store) ArchiveMaxID(ctx context.Context, table string) (int64, error) {
	if err := archiveTable(table); err != nil {
		return 0, err
	}
	var id int64
	if err := s.conn.QueryRow(ctx, "SELECT max(id) FROM "+table).Scan(&id); err != nil {
		return 0, fmt.Errorf("max id of %s: %w", table, err)
	}
	return id, nil
}

// ArchivedIDs is the set of ids between lo and hi the archive table holds.
func (s *Store) ArchivedIDs(ctx context.Context, table string, lo, hi int64) (map[int64]bool, error) {
	if err := archiveTable(table); err != nil {
		return nil, err
	}
	rows, err := s.conn.Query(ctx, "SELECT DISTINCT id FROM "+table+" WHERE id >= ? AND id <= ?", lo, hi)
	if err != nil {
		return nil, fmt.Errorf("ids of %s: %w", table, err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ReadArchivedChunks is the newest `limit` chunks of one install received in
// (since, until], oldest first: the tester's log past what Postgres keeps.
func (s *Store) ReadArchivedChunks(ctx context.Context, installID string, since, until time.Time, limit int) ([]LogChunkRow, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, install_id, platform, version, lines, dropped, body, received_at
		FROM archive_tester_log_chunks FINAL
		WHERE install_id = ? AND received_at > ? AND received_at <= ?
		ORDER BY received_at DESC, id DESC
		LIMIT ?`, installID, since.UTC(), until.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("read archived chunks: %w", err)
	}
	defer rows.Close()
	var out []LogChunkRow
	for rows.Next() {
		var r LogChunkRow
		if err := rows.Scan(&r.ID, &r.InstallID, &r.Platform, &r.Version, &r.Lines, &r.Dropped, &r.Body, &r.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan archived chunk: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func archiveTable(t string) error {
	switch t {
	case "archive_trunk_runs", "archive_tester_log_chunks":
		return nil
	}
	return fmt.Errorf("not an archive table: %q", t)
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
