package downtime

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Ingester moves clients' minutes from testers' log stream into product_tunnel_minutes.
type Ingester struct {
	store *Store
	pool  db.Pool
}

// NewIngester reads pool's log stream into store.
func NewIngester(store *Store, pool db.Pool) *Ingester { return &Ingester{store: store, pool: pool} }

// cursorName is the ingester's row in relay_runs_cursor.
const cursorName = "tester_downtime"

// Run ingests every interval until ctx ends.
func (in *Ingester) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := in.Once(ctx); err != nil {
			log.Printf("downtime: ingest: %v", err)
		} else if n > 0 {
			log.Printf("downtime: %d tunnel minutes", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once reads the chunks after the cursor and moves it past them. A line that
// does not parse is skipped, so one bad line never holds the stream up.
func (in *Ingester) Once(ctx context.Context) (int, error) {
	var cursor int64
	if err := in.pool.QueryRow(ctx, `SELECT COALESCE((SELECT last_chunk_id FROM relay_runs_cursor WHERE name = $1), 0)`, cursorName).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("read cursor: %w", err)
	}
	total := 0
	for {
		rows, err := in.pool.Query(ctx, `
			SELECT id, install_id, body FROM support_tester_log_chunks
			WHERE id > $1 AND body LIKE '%`+linePrefix+`%'
			ORDER BY id ASC LIMIT 200`, cursor)
		if err != nil {
			return total, fmt.Errorf("read chunks: %w", err)
		}
		type chunk struct {
			id      int64
			install string
			body    string
		}
		var chunks []chunk
		for rows.Next() {
			var c chunk
			if err := rows.Scan(&c.id, &c.install, &c.body); err != nil {
				rows.Close()
				return total, err
			}
			chunks = append(chunks, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, err
		}
		if len(chunks) == 0 {
			return total, nil
		}
		for _, c := range chunks {
			var minutes []Minute
			for _, line := range strings.Split(c.body, "\n") {
				if m, ok := ParseLine(line, c.install); ok {
					minutes = append(minutes, m...)
				}
			}
			n, err := in.store.Insert(ctx, minutes)
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
