package hikkariobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// linePrefix starts a client's period in its log (sing-box-lx patch "hikkari
// 0013", protocol/hikkari/period.go).
const linePrefix = "hikkari period s="

// reProfile is the outbound's tag in a sing-box line: "outbound/hikkari[p-002]:".
var reProfile = regexp.MustCompile(`outbound/hikkari\[([^\]]{1,64})\]`)

// ClientLine is one period a client wrote.
type ClientLine struct {
	Profile string
	Seconds int64
	Period  json.RawMessage
}

// ParseClientLine reads a client's period from one log line.
func ParseClientLine(line string) (ClientLine, bool) {
	i := strings.Index(line, linePrefix)
	if i < 0 {
		return ClientLine{}, false
	}
	rest := line[i+len(linePrefix):]
	num, period, ok := strings.Cut(rest, " ")
	if !ok {
		return ClientLine{}, false
	}
	seconds, err := strconv.ParseInt(num, 10, 64)
	if err != nil || seconds < 0 {
		return ClientLine{}, false
	}
	period = strings.TrimSpace(period)
	if !json.Valid([]byte(period)) || !strings.HasPrefix(period, "{") {
		return ClientLine{}, false
	}
	c := ClientLine{Seconds: seconds, Period: json.RawMessage(period)}
	if m := reProfile.FindStringSubmatch(line[:i]); m != nil {
		c.Profile = clipProfile(m[1])
	}
	return c, true
}

// clipProfile keeps a profile tag to the characters tags are made of.
func clipProfile(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Ingester moves clients' periods from testers' log stream into hikkari_obs.
type Ingester struct {
	rec  *Recorder
	pool db.Pool
}

// NewIngester reads pool's log stream into rec.
func NewIngester(rec *Recorder, pool db.Pool) *Ingester { return &Ingester{rec: rec, pool: pool} }

// cursorName is the ingester's row in relay_runs_cursor.
const cursorName = "tester_hikkari"

// Run ingests every interval until ctx ends.
func (in *Ingester) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := in.Once(ctx); err != nil {
			log.Printf("hikkariobs: ingest: %v", err)
		} else if n > 0 {
			log.Printf("hikkariobs: %d client periods", n)
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
			SELECT id, install_id, body, received_at FROM support_tester_log_chunks
			WHERE id > $1 AND body LIKE '%`+linePrefix+`%'
			ORDER BY id ASC LIMIT 200`, cursor)
		if err != nil {
			return total, fmt.Errorf("read chunks: %w", err)
		}
		type chunk struct {
			id       int64
			install  string
			body     string
			received time.Time
		}
		var chunks []chunk
		for rows.Next() {
			var c chunk
			if err := rows.Scan(&c.id, &c.install, &c.body, &c.received); err != nil {
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
			for _, line := range strings.Split(c.body, "\n") {
				p, ok := ParseClientLine(line)
				if !ok {
					continue
				}
				if err := in.rec.RecordClientPeriod(ctx, c.received.UTC(), c.install, p.Profile, p.Seconds, p.Period); err != nil {
					log.Printf("hikkariobs: chunk %d: %v", c.id, err)
					continue
				}
				total++
			}
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
