package cascadewatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Layers of the dataset (migration 050).
const (
	LayerEntry    = "entry"    // the entry's minute line
	LayerEvent    = "event"    // a member down, the exit's epoch, the entry's start
	LayerVPS      = "vps"      // the boxes themselves
	LayerPath     = "path"     // bare TCP and ICMP between the boxes
	LayerTrunk    = "trunk"    // chunks across the border, straight and through the trunk
	LayerProtocol = "protocol" // the client core on the exit's Hikkari relay, straight and through the entry
	LayerVerdict  = "verdict"  // which layer of a round went red first
	LayerCall     = "call"     // the trunk's own call, a minute of it (migration 056)
	LayerLoad     = "load"     // a synthetic user's stream through the entry (stand/load.go, migration 062)
)

// Order is the layers of a round from the bottom up: the first red one is
// the one to blame.
var Order = []string{LayerVPS, LayerPath, LayerTrunk, LayerProtocol}

// Run is one row of trunk_runs.
type Run struct {
	ID      int64          `json:"id,omitempty"`
	TS      time.Time      `json:"ts"`
	Round   *time.Time     `json:"round,omitempty"`
	Entry   string         `json:"entry"`
	Exit    string         `json:"exit"`
	Layer   string         `json:"layer"`
	Vantage string         `json:"vantage,omitempty"`
	Target  string         `json:"target,omitempty"`
	OK      *bool          `json:"ok,omitempty"`
	Data    map[string]any `json:"data"`
}

func boolp(b bool) *bool { return &b }

// Store is trunk_runs.
type Store struct{ pool db.Pool }

func NewStore(pool db.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Insert(ctx context.Context, runs ...Run) error {
	return insertRuns(ctx, s.pool, runs)
}

func insertRuns(ctx context.Context, q db.DBTX, runs []Run) error {
	for _, r := range runs {
		data := r.Data
		if data == nil {
			data = map[string]any{}
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO trunk_runs (ts, round_ts, entry_box, exit_box, layer, vantage, target, ok, data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			r.TS, r.Round, r.Entry, r.Exit, r.Layer, r.Vantage, r.Target, r.OK, raw); err != nil {
			return fmt.Errorf("trunk_runs: %w", err)
		}
	}
	return nil
}

// InsertJournal files the rows read from an entry's journal together with
// how far it was read, so a hub that restarts goes on from there and does not
// file the same lines twice (migration 057).
func (s *Store) InsertJournal(ctx context.Context, entry string, readTo time.Time, runs []Run) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := insertRuns(ctx, tx, runs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trunk_journal_cursor (entry_box, read_to) VALUES ($1, $2)
		ON CONFLICT (entry_box) DO UPDATE SET read_to = GREATEST(trunk_journal_cursor.read_to, EXCLUDED.read_to)`,
		entry, readTo); err != nil {
		return fmt.Errorf("trunk_journal_cursor: %w", err)
	}
	return tx.Commit(ctx)
}

// journalCursor is how far the entry's journal was read. With no cursor
// filed yet (a hub from before migration 057), it is the newest row read
// from the journal with its own time: the entry's minute line or an event.
func (s *Store) journalCursor(ctx context.Context, entry string) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `SELECT read_to FROM trunk_journal_cursor WHERE entry_box = $1`, entry).Scan(&t)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, err
	}
	if t != nil {
		return t.UTC(), nil
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT max(ts) FROM trunk_runs WHERE entry_box = $1 AND vantage = 'entry' AND layer IN ('entry', 'event')`,
		entry).Scan(&t); err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return t.UTC(), nil
}

// Query selects rows.
type Query struct {
	Since, Until time.Time
	Entry        string
	Layer        string
	Round        *time.Time
	Limit        int
	// Newest takes the newest Limit rows (still returned oldest first).
	Newest bool
}

const maxRows = 20000

func (s *Store) Rows(ctx context.Context, q Query) ([]Run, error) {
	if q.Until.IsZero() {
		q.Until = time.Now().Add(24 * time.Hour) // no upper bound
	}
	if q.Limit <= 0 || q.Limit > maxRows {
		q.Limit = maxRows
	}
	order := "ASC"
	if q.Newest {
		order = "DESC"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, ts, round_ts, entry_box, exit_box, layer, vantage, target, ok, data FROM trunk_runs
		WHERE ts >= $1 AND ts < $2 AND ($3 = '' OR entry_box = $3) AND ($4 = '' OR layer = $4)
		  AND ($5::timestamptz IS NULL OR round_ts = $5)
		ORDER BY ts `+order+`, id `+order+` LIMIT $6`,
		q.Since, q.Until, q.Entry, q.Layer, q.Round, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if q.Newest {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, rows.Err()
}

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var raw []byte
	if err := row.Scan(&r.ID, &r.TS, &r.Round, &r.Entry, &r.Exit, &r.Layer, &r.Vantage, &r.Target, &r.OK, &raw); err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r.Data); err != nil {
		return r, err
	}
	return r, nil
}

// last is the newest row of entry's layer at vantage (any when ""), nil for
// none.
func (s *Store) last(ctx context.Context, entry, layer, vantage string) (*Run, error) {
	r, err := scanRun(s.pool.QueryRow(ctx, `
		SELECT id, ts, round_ts, entry_box, exit_box, layer, vantage, target, ok, data FROM trunk_runs
		WHERE entry_box = $1 AND layer = $2 AND ($3 = '' OR vantage = $3) ORDER BY ts DESC, id DESC LIMIT 1`,
		entry, layer, vantage))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// pendingRounds are entry's rounds since since with no verdict yet.
func (s *Store) pendingRounds(ctx context.Context, entry string, since time.Time) ([]time.Time, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT round_ts FROM trunk_runs r WHERE entry_box = $1 AND round_ts IS NOT NULL AND round_ts >= $2
		  AND NOT EXISTS (SELECT 1 FROM trunk_runs v WHERE v.entry_box = $1 AND v.layer = 'verdict' AND v.round_ts = r.round_ts)
		ORDER BY round_ts`, entry, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// lastRound is entry's newest round, zero for none.
func (s *Store) lastRound(ctx context.Context, entry string) (time.Time, error) {
	var t *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(round_ts) FROM trunk_runs WHERE entry_box = $1`, entry).Scan(&t); err != nil || t == nil {
		return time.Time{}, err
	}
	return *t, nil
}
