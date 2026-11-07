package wirecheck

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

// Ingester reads [diag:wire] lines from the tester log stream, checks them and
// stores them; findings also go to the client errors (telemetry_errors), where
// the errors list groups them with everything else that breaks.
type Ingester struct {
	store *Store
	// errs is the client errors table; nil keeps findings in wire_hellos only.
	errs telemetry.Sink
}

// NewIngester reads into store; errs may be nil (no ClickHouse).
func NewIngester(store *Store, errs telemetry.Sink) *Ingester {
	return &Ingester{store: store, errs: errs}
}

// cursorName is this reader's row in relay_runs_cursor, beside the load rounds'.
const cursorName = "tester_wire"

// ComponentWire is the errors' component of every finding.
const ComponentWire = "wire"

// Run ingests every interval until ctx ends.
func (in *Ingester) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := in.Once(ctx); err != nil {
			log.Printf("wirecheck: %v", err)
		} else if n > 0 {
			log.Printf("wirecheck: %d lines", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type chunk struct {
	id         int64
	install    string
	platform   string
	version    string
	body       string
	receivedAt time.Time
}

// Once reads the chunks after the cursor, then moves it past them. A chunk
// read again (a crash before the cursor moved) adds nothing: its lines are
// stored, and only lines not stored before report their findings.
func (in *Ingester) Once(ctx context.Context) (int, error) {
	var cursor int64
	if err := in.store.pool.QueryRow(ctx, `SELECT COALESCE((SELECT last_chunk_id FROM relay_runs_cursor WHERE name = $1), 0)`, cursorName).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("read cursor: %w", err)
	}
	total := 0
	refs := map[string]Reference{}
	for {
		chunks, err := in.chunks(ctx, cursor, 200)
		if err != nil || len(chunks) == 0 {
			return total, err
		}
		for _, c := range chunks {
			n, err := in.chunk(ctx, c, refs)
			if err != nil {
				return total, fmt.Errorf("chunk %d: %w", c.id, err)
			}
			total += n
			cursor = c.id
		}
		if _, err := in.store.pool.Exec(ctx, `
			INSERT INTO relay_runs_cursor (name, last_chunk_id, updated_at) VALUES ($1, $2, now())
			ON CONFLICT (name) DO UPDATE SET last_chunk_id = EXCLUDED.last_chunk_id, updated_at = now()
		`, cursorName, cursor); err != nil {
			return total, fmt.Errorf("move cursor: %w", err)
		}
	}
}

func (in *Ingester) chunks(ctx context.Context, after int64, limit int) ([]chunk, error) {
	rows, err := in.store.pool.Query(ctx, `
		SELECT id, install_id, platform, version, body, received_at FROM support_tester_log_chunks
		WHERE id > $1 AND body LIKE '%[diag:wire] p-%'
		ORDER BY id ASC LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read wire chunks: %w", err)
	}
	defer rows.Close()
	var out []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.id, &c.install, &c.platform, &c.version, &c.body, &c.receivedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// chunk checks and stores one chunk's lines. refs caches references by core
// over one pass: a pass is seconds, a reference changes by hand.
func (in *Ingester) chunk(ctx context.Context, c chunk, refs map[string]Reference) (int, error) {
	var hellos []Hello
	declared := map[string]Declared{}
	for _, text := range strings.Split(c.body, "\n") {
		l, ok := ParseLine(text)
		if !ok {
			continue
		}
		ref, cached := refs[l.Core]
		if !cached {
			var err error
			if ref, err = in.store.reference(ctx, l.Core); err != nil {
				return 0, err
			}
			refs[l.Core] = ref
		}
		key := l.Profile + "|" + l.Network
		d, seen := declared[key]
		if !seen {
			var err error
			if d, err = in.store.declared(ctx, l.Profile, asnOf(l.Network)); err != nil {
				return 0, err
			}
			declared[key] = d
		}
		ts := l.ClientTS
		if ts == "" {
			ts = c.receivedAt.UTC().Format(time.RFC3339Nano)
		}
		hellos = append(hellos, Hello{
			Install: c.install, ClientTS: ts, MeasuredAt: c.receivedAt, Version: c.version, Platform: c.platform,
			Profile: l.Profile, Proto: l.Proto, Core: l.Core, Network: l.Network, Knobs: l.Knobs, State: l.State,
			JA4: l.JA4, HK: l.HK, ALPNSent: l.ALPNSent, Wire: l.Wire,
			Findings: Check(l, ref, d, c.receivedAt),
		})
	}
	// Findings go out before the lines are stored: a failed send leaves the
	// chunk to the next pass whole. A crash between the two sends a line's
	// findings twice, which counts a round double rather than losing it.
	fresh, err := in.store.unseen(ctx, hellos)
	if err != nil {
		return 0, err
	}
	if err := in.report(ctx, fresh); err != nil {
		return 0, err
	}
	if _, err := in.store.Insert(ctx, hellos); err != nil {
		return 0, err
	}
	return len(fresh), nil
}

// report sends the findings of freshly stored lines to the client errors, one
// row per finding, under the tester's install, version and OS.
func (in *Ingester) report(ctx context.Context, hellos []Hello) error {
	if in.errs == nil {
		return nil
	}
	var rows []telemetry.ErrorRow
	for _, h := range hellos {
		for _, f := range h.Findings {
			msg := f.Msg
			if r := []rune(msg); len(r) > 300 {
				msg = string(r[:300])
			}
			rows = append(rows, telemetry.ErrorRow{
				TS: h.MeasuredAt, Install: h.Install,
				App:  telemetry.App{Version: h.Version, Channel: "tester", OS: h.Platform},
				Code: f.Code, Component: ComponentWire, FP: errorFP(h.Profile, h.Core, f), Msg: msg, Count: 1,
			})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if err := in.errs.InsertTelemetry(ctx, nil, rows); err != nil {
		return fmt.Errorf("findings to errors: %w", err)
	}
	return nil
}

// asnOf reads «AS12389»; 0 is no known network.
func asnOf(network string) uint32 {
	n, err := strconv.ParseUint(strings.TrimPrefix(network, "AS"), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}
