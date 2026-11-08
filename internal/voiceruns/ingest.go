package voiceruns

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// chunk is one stored batch of a tester's log.
type chunk struct {
	id         int64
	install    string
	version    string
	body       string
	receivedAt time.Time
}

// rowsOf reads a chunk's voice lines, in order.
func rowsOf(c chunk) []Row {
	var rows []Row
	for _, line := range strings.Split(c.body, "\n") {
		if r, ok := parseLine(line, c.install, c.receivedAt, c.version); ok {
			rows = append(rows, r)
		}
	}
	return rows
}

// Ingester moves testers' voice series from the log stream into the dataset.
type Ingester struct {
	store *Store
	pool  db.Pool
}

// NewIngester reads the log stream of pool into store.
func NewIngester(store *Store, pool db.Pool) *Ingester {
	return &Ingester{store: store, pool: pool}
}

// cursorName is the ingester's row in relay_runs_cursor.
const cursorName = "tester_voice"

// Run ingests every interval until ctx ends. The first pass reads every chunk
// the stream still keeps.
func (in *Ingester) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := in.Once(ctx); err != nil {
			log.Printf("voiceruns: ingest: %v", err)
		} else if n > 0 {
			log.Printf("voiceruns: %d series", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once reads the chunks after the cursor and moves it past them.
func (in *Ingester) Once(ctx context.Context) (int, error) {
	var cursor int64
	if err := in.pool.QueryRow(ctx, `SELECT COALESCE((SELECT last_chunk_id FROM relay_runs_cursor WHERE name = $1), 0)`, cursorName).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("read cursor: %w", err)
	}
	total := 0
	for {
		chunks, err := in.chunks(ctx, cursor, 200)
		if err != nil || len(chunks) == 0 {
			return total, err
		}
		for _, c := range chunks {
			n, err := in.store.Insert(ctx, rowsOf(c))
			if err != nil {
				return total, fmt.Errorf("chunk %d: %w", c.id, err)
			}
			total += n
			cursor = c.id
		}
		if _, err := in.pool.Exec(ctx, `
			INSERT INTO relay_runs_cursor (name, last_chunk_id, updated_at) VALUES ($1, $2, now())
			ON CONFLICT (name) DO UPDATE SET last_chunk_id = EXCLUDED.last_chunk_id, updated_at = now()
		`, cursorName, cursor); err != nil {
			return total, fmt.Errorf("move cursor: %w", err)
		}
	}
}

// chunks are the chunks after id that hold a voice line.
func (in *Ingester) chunks(ctx context.Context, after int64, limit int) ([]chunk, error) {
	rows, err := in.pool.Query(ctx, `
		SELECT id, install_id, version, body, received_at FROM support_tester_log_chunks
		WHERE id > $1 AND body LIKE '%[diag:voice%'
		ORDER BY id ASC LIMIT $2
	`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read voice chunks: %w", err)
	}
	defer rows.Close()
	var out []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.id, &c.install, &c.version, &c.body, &c.receivedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
