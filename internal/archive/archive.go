// Package archive moves the hub's own measurements out of Postgres into the
// ClickHouse archive and keeps them there forever (owner's decision 18.11:
// «замеры не удалять, переносить в сжатое»).
//
// Every row of trunk_runs and of the testers' log chunks is copied to the
// archive as it lands (by id, a little behind, so a row committed late is not
// skipped). Postgres then keeps what the hub reads all the time: trunk_runs
// HotTrunk, log chunks HotLogs. A row leaves Postgres only after the archive
// is seen to hold its id; a row the archive lacks is copied again first.
//
// Service rows that are queues, not measurements (stand tasks, job logs,
// orders that ended), are dropped after ServiceKeep: their results already
// live in the datasets.
package archive

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
)

const (
	// HotTrunk is how long trunk_runs stay in Postgres: every chart of the
	// panel spans 30 days at most, a day of margin on top.
	HotTrunk = 31 * 24 * time.Hour
	// HotLogs is how long the testers' log chunks stay in Postgres: the
	// ingesters and a complaint's analysis read the last week.
	HotLogs = 7 * 24 * time.Hour
	// ServiceKeep is how long queue rows stay after they ended.
	ServiceKeep = 90 * 24 * time.Hour

	every   = time.Hour
	batch   = 2000
	overlap = 1000 // ids re-read behind the archive's newest, for late commits
)

// Sink is the archive (analytics.Store).
type Sink interface {
	ArchiveTrunkRuns(ctx context.Context, rows []analytics.TrunkRunRow) error
	ArchiveLogChunks(ctx context.Context, rows []analytics.LogChunkRow) error
	ArchiveMaxID(ctx context.Context, table string) (int64, error)
	ArchivedIDs(ctx context.Context, table string, lo, hi int64) (map[int64]bool, error)
}

// Mover copies and prunes.
type Mover struct {
	pg   db.Pool
	sink Sink
	now  func() time.Time
}

func New(pg db.Pool, sink Sink) *Mover {
	return &Mover{pg: pg, sink: sink, now: time.Now}
}

// Run moves once a minute after start and then every hour until ctx ends.
func (m *Mover) Run(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.Once(ctx)
		t.Reset(every)
	}
}

// Once is one pass: copy, then prune.
func (m *Mover) Once(ctx context.Context) {
	for _, tb := range tables {
		copied, err := m.copyNew(ctx, tb)
		if err != nil {
			applog.Error(ctx, "archive", "copy failed", applog.F("table", tb.pg), applog.F("err", err.Error()))
			continue
		}
		pruned, err := m.prune(ctx, tb, m.now().Add(-tb.hot))
		if err != nil {
			applog.Error(ctx, "archive", "prune failed", applog.F("table", tb.pg), applog.F("err", err.Error()))
		}
		if copied > 0 || pruned > 0 {
			applog.Info(ctx, "archive", "moved", applog.F("table", tb.pg),
				applog.F("copied", copied), applog.F("pruned", pruned))
		}
	}
	if n, err := m.pruneService(ctx, m.now().Add(-ServiceKeep)); err != nil {
		applog.Error(ctx, "archive", "service prune failed", applog.F("err", err.Error()))
	} else if n > 0 {
		applog.Info(ctx, "archive", "service rows dropped", applog.F("rows", n))
	}
}

// table is one Postgres table and its archive.
type table struct {
	pg, ch string
	when   string // the column Postgres keeps by
	hot    time.Duration
	// read selects rows by a WHERE clause on id ($1, $2) into the archive.
	read func(ctx context.Context, pg db.Pool, where string, args ...any) (int, []int64, func(context.Context, Sink) error, error)
}

var tables = []table{
	{pg: "trunk_runs", ch: "archive_trunk_runs", when: "ts", hot: HotTrunk, read: readTrunk},
	{pg: "support_tester_log_chunks", ch: "archive_tester_log_chunks", when: "received_at", hot: HotLogs, read: readChunks},
}

// copyNew copies every row with an id past the archive's newest, less the
// overlap. Returns how many it copied.
func (m *Mover) copyNew(ctx context.Context, tb table) (int, error) {
	top, err := m.sink.ArchiveMaxID(ctx, tb.ch)
	if err != nil {
		return 0, err
	}
	from := max(top-overlap, 0)
	total := 0
	for ctx.Err() == nil {
		n, ids, write, err := tb.read(ctx, m.pg, "id > $1 ORDER BY id LIMIT $2", from, batch)
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		if err := write(ctx, m.sink); err != nil {
			return total, err
		}
		total += n
		from = ids[len(ids)-1]
		if n < batch {
			return total, nil
		}
	}
	return total, ctx.Err()
}

// prune deletes rows older than cutoff whose ids the archive holds, copying
// any it lacks first (they are deleted on the next pass). Returns how many it
// deleted.
func (m *Mover) prune(ctx context.Context, tb table, cutoff time.Time) (int, error) {
	total := 0
	for ctx.Err() == nil {
		rows, err := m.pg.Query(ctx, fmt.Sprintf(
			"SELECT id FROM %s WHERE %s < $1 ORDER BY id LIMIT $2", tb.pg, tb.when), cutoff, batch)
		if err != nil {
			return total, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		have, err := m.sink.ArchivedIDs(ctx, tb.ch, ids[0], ids[len(ids)-1])
		if err != nil {
			return total, err
		}
		var held, missing []int64
		for _, id := range ids {
			if have[id] {
				held = append(held, id)
			} else {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			_, _, write, err := tb.read(ctx, m.pg, "id = ANY($1)", missing)
			if err != nil {
				return total, err
			}
			if err := write(ctx, m.sink); err != nil {
				return total, err
			}
			applog.Warn(ctx, "archive", "rows were missing from the archive, copied again",
				applog.F("table", tb.pg), applog.F("rows", len(missing)))
			// Deleted on the next pass, once the archive is seen to hold them.
		}
		if len(held) > 0 {
			tag, err := m.pg.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ANY($1)", tb.pg), held)
			if err != nil {
				return total, err
			}
			total += int(tag.RowsAffected())
		}
		if len(missing) > 0 || len(ids) < batch {
			return total, nil
		}
	}
	return total, ctx.Err()
}

func readTrunk(ctx context.Context, pg db.Pool, where string, args ...any) (int, []int64, func(context.Context, Sink) error, error) {
	rows, err := pg.Query(ctx, `
		SELECT id, ts, round_ts, entry_box, exit_box, layer, vantage, target, ok, data::text
		FROM trunk_runs WHERE `+where, args...)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read trunk_runs: %w", err)
	}
	defer rows.Close()
	var out []analytics.TrunkRunRow
	var ids []int64
	for rows.Next() {
		var r analytics.TrunkRunRow
		if err := rows.Scan(&r.ID, &r.TS, &r.RoundTS, &r.EntryBox, &r.ExitBox, &r.Layer, &r.Vantage, &r.Target, &r.OK, &r.Data); err != nil {
			return 0, nil, nil, fmt.Errorf("scan trunk_runs: %w", err)
		}
		out = append(out, r)
		ids = append(ids, r.ID)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, nil, err
	}
	return len(out), ids, func(ctx context.Context, s Sink) error { return s.ArchiveTrunkRuns(ctx, out) }, nil
}

func readChunks(ctx context.Context, pg db.Pool, where string, args ...any) (int, []int64, func(context.Context, Sink) error, error) {
	rows, err := pg.Query(ctx, `
		SELECT id, install_id, platform, version, lines, dropped, body, received_at
		FROM support_tester_log_chunks WHERE `+where, args...)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read tester log chunks: %w", err)
	}
	defer rows.Close()
	var out []analytics.LogChunkRow
	var ids []int64
	for rows.Next() {
		var r analytics.LogChunkRow
		if err := rows.Scan(&r.ID, &r.InstallID, &r.Platform, &r.Version, &r.Lines, &r.Dropped, &r.Body, &r.ReceivedAt); err != nil {
			return 0, nil, nil, fmt.Errorf("scan tester log chunk: %w", err)
		}
		out = append(out, r)
		ids = append(ids, r.ID)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, nil, err
	}
	return len(out), ids, func(ctx context.Context, s Sink) error { return s.ArchiveLogChunks(ctx, out) }, nil
}

// pruneService drops queue rows that ended before cutoff. Returns how many.
func (m *Mover) pruneService(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM stand_tasks WHERE status IN ('done', 'failed', 'expired') AND created_at < $1`,
		`DELETE FROM entry_job_log WHERE at < $1`,
		`DELETE FROM entry_box_orders WHERE status NOT IN ('reserved', 'created', 'undeleted') AND updated_at < $1`,
	} {
		tag, err := m.pg.Exec(ctx, q, cutoff)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}
