package relayruns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Testers' rounds come from their log stream (support_tester_log_chunks): the
// client writes a [diag:load] line per profile every 30 minutes and on
// probe_load (hikkari-client engine/singbox/diagnostics/load.go). The line says
// what came through; the knobs it ran with come from the line itself when the
// client writes them, and otherwise from the install's latest [diag:config]
// before it, the config the client was running. A row whose config is not
// readable keeps no params rather than the hub's guess: the hub's own idea of a
// node can be minutes ahead of the client's subscription.

var (
	reLineTS     = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+`)
	reLoadLine   = regexp.MustCompile(`\[diag:load\] (p-\S+) (.*)$`)
	reReachSelf  = regexp.MustCompile(`\[diag:reach\] self .*isp=(AS\d+)`)
	reConfigLine = regexp.MustCompile(`\[diag:config\] (\{.*)$`)
)

// loadLine is one profile's [diag:load] result.
type loadLine struct {
	ts     string // the client's local time, as written
	tag    string
	fields map[string]string
}

// parseLoadLine reads «2026/11/04 03:33:06.289 [diag:load] p-x proto=... dl=a/b
// took=... end=ok burst=n/50 timeout=n med=... after=n/3 med=...». Only the
// first of repeated keys is kept (med appears twice).
func parseLoadLine(line string) (loadLine, bool) {
	m := reLoadLine.FindStringSubmatch(line)
	if m == nil {
		return loadLine{}, false
	}
	out := loadLine{tag: m[1], fields: map[string]string{}}
	if ts := reLineTS.FindStringSubmatch(line); ts != nil {
		out.ts = ts[1]
	}
	rest := m[2]
	if i := strings.Index(rest, "load_error="); i >= 0 {
		out.fields["load_error"] = "1"
		rest = rest[:i]
	}
	for _, f := range strings.Fields(rest) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		if _, seen := out.fields[k]; !seen {
			out.fields[k] = v
		}
	}
	_, hasDl := out.fields["dl"]
	_, failed := out.fields["load_error"]
	return out, hasDl || failed
}

func ratio(s string) (int64, int64) {
	a, b, _ := strings.Cut(s, "/")
	n, _ := strconv.ParseInt(a, 10, 64)
	d, _ := strconv.ParseInt(b, 10, 64)
	return n, d
}

// row turns the line into a dataset row. params are the config's knobs for the
// profile, nil when unknown; knobs written on the line win over them.
func (l loadLine) row(install string, at time.Time, network string, params map[string]string) Row {
	r := Row{
		Source: SourceTester, MeasuredAt: at, Vantage: install, Network: network,
		Profile: strings.TrimPrefix(l.tag, "p-"), Proto: l.fields["proto"], Params: map[string]string{},
		Key: install + "|" + l.ts + "|" + l.tag,
	}
	if l.ts == "" {
		r.Key = install + "|" + at.UTC().Format(time.RFC3339Nano) + "|" + l.tag
	}
	for k, v := range params {
		r.Params[k] = v
	}
	for _, k := range Knobs {
		if v, ok := l.fields[k]; ok && reValue.MatchString(v) {
			r.Params[k] = v
		}
	}
	if l.fields["load_error"] != "" {
		r.LoadError = true
		return r
	}
	got, _ := ratio(l.fields["dl"])
	r.Dl = got
	r.DlEnd = l.fields["end"]
	b, bt := ratio(l.fields["burst"])
	r.Burst, r.BurstTotal = int(b), int(bt)
	r.BurstTimeout, _ = strconv.Atoi(l.fields["timeout"])
	a, at2 := ratio(l.fields["after"])
	r.After, r.AfterTotal = int(a), int(at2)
	if !reEnd.MatchString(r.DlEnd) {
		r.DlEnd = "error"
	}
	return r
}

// configParams reads the knobs of every p- outbound of a [diag:config] line.
// The line is cut at 8 KB, so it is not one JSON value: the decoder walks it
// and keeps what is whole before the cut. Outbounds come after dns, endpoints,
// inbounds and log, so a config with a long endpoint can lose them.
func configParams(text string) map[string]map[string]string {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil
		}
		key, _ := t.(string)
		if key != "outbounds" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return nil
		}
		out := map[string]map[string]string{}
		for dec.More() {
			var ob map[string]any
			if err := dec.Decode(&ob); err != nil {
				return out
			}
			if tag, _ := ob["tag"].(string); strings.HasPrefix(tag, "p-") {
				out[tag] = ParamsOf(ob)
			}
		}
		return out
	}
	return nil
}

// chunk is one stored batch of a tester's log.
type chunk struct {
	id         int64
	install    string
	body       string
	receivedAt time.Time
}

// chunkSource reads the tester log stream.
type chunkSource interface {
	// loadChunks are the chunks after id that hold a [diag:load] line.
	loadChunks(ctx context.Context, after int64, limit int) ([]chunk, error)
	// lastBefore is the install's newest line before chunk id holding marker.
	lastBefore(ctx context.Context, install string, before int64, marker string) (string, error)
}

type dbChunks struct{ pool db.Pool }

func (d dbChunks) loadChunks(ctx context.Context, after int64, limit int) ([]chunk, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id, install_id, body, received_at FROM support_tester_log_chunks
		WHERE id > $1 AND body LIKE '%[diag:load] p-%'
		ORDER BY id ASC LIMIT $2
	`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read load chunks: %w", err)
	}
	defer rows.Close()
	var out []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.id, &c.install, &c.body, &c.receivedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d dbChunks) lastBefore(ctx context.Context, install string, before int64, marker string) (string, error) {
	var line string
	err := d.pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT line FROM unnest(string_to_array(body, E'\n')) WITH ORDINALITY AS l(line, n)
			WHERE strpos(line, $3) > 0 ORDER BY n DESC LIMIT 1
		), '')
		FROM support_tester_log_chunks
		WHERE install_id = $1 AND id < $2 AND strpos(body, $3) > 0
		ORDER BY id DESC LIMIT 1
	`, install, before, marker).Scan(&line)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return line, err
}

// Ingester moves testers' rounds from the log stream into the dataset.
type Ingester struct {
	store  *Store
	chunks chunkSource
	pool   db.Pool
}

// NewIngester reads the log stream of pool into store.
func NewIngester(store *Store, pool db.Pool) *Ingester {
	return &Ingester{store: store, chunks: dbChunks{pool}, pool: pool}
}

const cursorName = "tester_load"

// Run ingests every interval until ctx ends. The first pass reads every chunk
// still kept, so the dataset starts with the week the stream holds.
func (in *Ingester) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := in.Once(ctx); err != nil {
			log.Printf("relayruns: tester ingest: %v", err)
		} else if n > 0 {
			log.Printf("relayruns: %d tester rows", n)
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
		chunks, err := in.chunks.loadChunks(ctx, cursor, 200)
		if err != nil || len(chunks) == 0 {
			return total, err
		}
		for _, c := range chunks {
			rows, err := in.rowsOf(ctx, c)
			if err != nil {
				return total, err
			}
			n, err := in.store.Insert(ctx, rows)
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

// rowsOf reads one chunk's load lines, in order, with the config and network
// in force at each: lines of the chunk before it, else the newest before the
// chunk.
func (in *Ingester) rowsOf(ctx context.Context, c chunk) ([]Row, error) {
	var (
		config  map[string]map[string]string
		network string
		looked  bool
		rows    []Row
	)
	for _, line := range strings.Split(c.body, "\n") {
		if m := reConfigLine.FindStringSubmatch(line); m != nil {
			config, looked = configParams(m[1]), true
			continue
		}
		if m := reReachSelf.FindStringSubmatch(line); m != nil {
			network = m[1]
			continue
		}
		l, ok := parseLoadLine(line)
		if !ok {
			continue
		}
		if !looked {
			looked = true
			text, err := in.chunks.lastBefore(ctx, c.install, c.id, "[diag:config] ")
			if err != nil {
				return nil, fmt.Errorf("config before chunk %d: %w", c.id, err)
			}
			if m := reConfigLine.FindStringSubmatch(text); m != nil {
				config = configParams(m[1])
			}
		}
		if network == "" {
			text, err := in.chunks.lastBefore(ctx, c.install, c.id, "[diag:reach] self ")
			if err != nil {
				return nil, fmt.Errorf("network before chunk %d: %w", c.id, err)
			}
			if m := reReachSelf.FindStringSubmatch(text); m != nil {
				network = m[1]
			} else {
				network = "-" // looked, not known: do not ask again for this chunk
			}
		}
		net := network
		if net == "-" {
			net = ""
		}
		r := l.row(c.install, c.receivedAt, net, config[l.tag])
		if r.Validate() != nil {
			continue // a line this hub cannot read is not a measurement
		}
		rows = append(rows, r)
	}
	return rows, nil
}
