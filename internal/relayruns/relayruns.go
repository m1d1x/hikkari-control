// Package relayruns is the relay dataset (hikkari-client
// plans/2026-11-04-relay-search-method.md): every measured round of a profile,
// from the matrix stand on our own vantages and from testers' [diag:load]
// rounds, with the knobs it ran with and what came through. It is how a knob
// that starts to be cut shows up against its own history, and what any
// "this relay works" rests on.
//
// A row names a profile by its label and its knobs by value. No address and no
// secret reaches it: the stand sends rows, not outbounds, and a tester's row is
// read from a log the client has already scrubbed.
package relayruns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Sources of a row.
const (
	SourceStand  = "stand"
	SourceTester = "tester"
)

// KnobBaseline is the stand's row for the same address and port with no
// protocol: a dead baseline means the address is cut, not a knob.
const KnobBaseline = "baseline"

// Knobs are the params a row can carry and a chart can be drawn by. The stand
// and the tester's config name them the same way. mode is XHTTP's, key_share
// Reality's: knobs of the config that the hub checks against what it handed
// out (internal/knobcheck).
var Knobs = []string{"fp", "sni", "port", "flow", "alpn", "mode", "key_share"}

// KnobBurst is the stand's load knob: how many requests the burst opens at
// once. It is not a knob of the config, so no params carry it; every row has a
// value for it, its BurstTotal.
const KnobBurst = "burst"

// EndAddrDead ends a tester's row whose address did not answer a ClientHello on
// any flow: no value was measured, so the row counts for no knob.
const EndAddrDead = "addr_dead"

// Row is one profile in one round from one vantage.
type Row struct {
	Source       string            `json:"source"`
	MeasuredAt   time.Time         `json:"ts"`
	Vantage      string            `json:"vantage"`
	Network      string            `json:"network,omitempty"`
	Core         string            `json:"core,omitempty"`
	Profile      string            `json:"profile"`
	Proto        string            `json:"proto,omitempty"`
	Knob         string            `json:"knob,omitempty"`
	Value        string            `json:"value,omitempty"`
	Params       map[string]string `json:"params,omitempty"`
	Round        int               `json:"round,omitempty"`
	Dl           int64             `json:"dl"`
	DlEnd        string            `json:"dl_end,omitempty"`
	Burst        int               `json:"burst"`
	BurstTotal   int               `json:"burst_total"`
	BurstTimeout int               `json:"burst_timeout"`
	After        int               `json:"after"`
	AfterTotal   int               `json:"after_total"`
	LoadError    bool              `json:"load_error,omitempty"`
	// Key makes a row sent twice the same row. Set by the store when empty.
	Key string `json:"-"`
}

var (
	reLabel = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,80}$`)
	reValue = regexp.MustCompile(`^[A-Za-z0-9._,/:-]{0,64}$`)
	// reCore names a core build: "sing-box-lx v1.14.3-lx.21+489ac0f3".
	reCore = regexp.MustCompile(`^[A-Za-z0-9._+ -]{0,80}$`)
	reEnd  = regexp.MustCompile(`^(ok|timeout|closed|http\d{3}|error|addr_dead|)$`)
)

// Validate refuses a row that could not have come from a measurement, so a
// typo in a caller does not become history.
func (r Row) Validate() error {
	switch {
	case r.Source != SourceStand && r.Source != SourceTester:
		return fmt.Errorf("source %q", r.Source)
	case r.MeasuredAt.IsZero():
		return errors.New("no time")
	case r.MeasuredAt.After(time.Now().Add(10 * time.Minute)):
		return errors.New("time in the future")
	case !reLabel.MatchString(r.Vantage):
		return fmt.Errorf("vantage %q", r.Vantage)
	case !reLabel.MatchString(r.Profile):
		return fmt.Errorf("profile %q", r.Profile)
	case r.Knob != "" && r.Knob != KnobBaseline && r.Knob != KnobBurst && !isKnob(r.Knob):
		return fmt.Errorf("knob %q", r.Knob)
	case !reValue.MatchString(r.Value) || !reCore.MatchString(r.Core) || !reValue.MatchString(r.Proto) || !reValue.MatchString(r.Network):
		return errors.New("a field is not a plain value")
	case !reEnd.MatchString(r.DlEnd):
		return fmt.Errorf("dl_end %q", r.DlEnd)
	case r.Dl < 0 || r.Burst < 0 || r.BurstTimeout < 0 || r.After < 0 || r.Burst > r.BurstTotal || r.After > r.AfterTotal:
		return errors.New("counts do not add up")
	case r.BurstTotal > 1000 || r.AfterTotal > 1000:
		return errors.New("counts too large")
	}
	for k, v := range r.Params {
		if !isKnob(k) || !reValue.MatchString(v) {
			return fmt.Errorf("param %s=%q", k, v)
		}
	}
	return nil
}

func isKnob(k string) bool {
	for _, n := range Knobs {
		if n == k {
			return true
		}
	}
	return false
}

func (r Row) key() string {
	if r.Key != "" {
		return r.Key
	}
	return strings.Join([]string{r.Vantage, r.MeasuredAt.UTC().Format(time.RFC3339Nano), r.Profile, r.Knob, r.Value, strconv.Itoa(r.Round)}, "|")
}

// ParamsOf reads the knobs of a sing-box outbound. A knob the outbound does
// not set is left out rather than guessed; flow is "none" on a VLESS outbound
// without one, since that is a value of the knob, not a missing one.
func ParamsOf(ob map[string]any) map[string]string {
	out := map[string]string{}
	if p := intOf(ob["server_port"]); p > 0 {
		out["port"] = strconv.Itoa(p)
	}
	if typ, _ := ob["type"].(string); typ == "vless" {
		out["flow"] = "none"
		if f, _ := ob["flow"].(string); f != "" {
			out["flow"] = f
		}
	}
	if tr, _ := ob["transport"].(map[string]any); tr != nil {
		if typ, _ := tr["type"].(string); typ == "xhttp" {
			if m, _ := tr["mode"].(string); m != "" {
				out["mode"] = m
			}
		}
	}
	tls, _ := ob["tls"].(map[string]any)
	if tls == nil {
		return out
	}
	if s, _ := tls["server_name"].(string); s != "" {
		out["sni"] = s
	}
	if u, _ := tls["utls"].(map[string]any); u != nil {
		if on, _ := u["enabled"].(bool); on {
			if fp, _ := u["fingerprint"].(string); fp != "" {
				out["fp"] = fp
			}
		}
	}
	if list, _ := tls["alpn"].([]any); len(list) > 0 {
		parts := make([]string, 0, len(list))
		for _, v := range list {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
		out["alpn"] = strings.Join(parts, "/")
	}
	if r, _ := tls["reality"].(map[string]any); r != nil {
		if ks, _ := r["key_share"].(string); ks != "" {
			out["key_share"] = ks
		}
	}
	for k, v := range out {
		if !reValue.MatchString(v) {
			delete(out, k)
		}
	}
	return out
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// Store is the dataset in Postgres.
type Store struct {
	pool db.Pool
}

// NewStore opens the dataset on pool.
func NewStore(pool db.Pool) *Store { return &Store{pool: pool} }

// Insert adds rows; a row already there is kept as it was. It returns how many
// were new.
func (s *Store) Insert(ctx context.Context, rows []Row) (int, error) {
	added := 0
	for _, r := range rows {
		if err := r.Validate(); err != nil {
			return added, err
		}
		params := r.Params
		if params == nil {
			params = map[string]string{}
		}
		raw, _ := json.Marshal(params)
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO relay_runs (source, source_key, measured_at, vantage, network, core, profile, proto,
			                        knob, value, params, round, dl, dl_end, burst, burst_total, burst_timeout,
			                        after, after_total, load_error)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
			ON CONFLICT (source, source_key) DO NOTHING
		`, r.Source, r.key(), r.MeasuredAt.UTC(), r.Vantage, r.Network, r.Core, r.Profile, r.Proto,
			r.Knob, r.Value, raw, r.Round, r.Dl, r.DlEnd, r.Burst, r.BurstTotal, r.BurstTimeout,
			r.After, r.AfterTotal, r.LoadError)
		if err != nil {
			return added, fmt.Errorf("insert relay run: %w", err)
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

// Query picks rows of the dataset.
type Query struct {
	Profile string
	Source  string
	Since   time.Time
	Until   time.Time
	Limit   int
}

// Rows reads the rows of a query, oldest first.
func (s *Store) Rows(ctx context.Context, q Query) ([]Row, error) {
	if q.Until.IsZero() {
		q.Until = time.Now()
	}
	if q.Limit <= 0 || q.Limit > 50000 {
		q.Limit = 50000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT source, measured_at, vantage, network, core, profile, proto, knob, value, params, round,
		       dl, dl_end, burst, burst_total, burst_timeout, after, after_total, load_error
		FROM (
			SELECT * FROM relay_runs
			WHERE measured_at > $1 AND measured_at <= $2
			  AND ($3 = '' OR profile = $3) AND ($4 = '' OR source = $4)
			ORDER BY measured_at DESC
			LIMIT $5
		) newest ORDER BY measured_at ASC, id ASC
	`, q.Since, q.Until, q.Profile, q.Source, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("read relay runs: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var raw []byte
		if err := rows.Scan(&r.Source, &r.MeasuredAt, &r.Vantage, &r.Network, &r.Core, &r.Profile, &r.Proto,
			&r.Knob, &r.Value, &raw, &r.Round, &r.Dl, &r.DlEnd, &r.Burst, &r.BurstTotal, &r.BurstTimeout,
			&r.After, &r.AfterTotal, &r.LoadError); err != nil {
			return nil, fmt.Errorf("scan relay run: %w", err)
		}
		_ = json.Unmarshal(raw, &r.Params)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Profiles lists the profiles measured since a time, most rows first.
func (s *Store) Profiles(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT profile FROM relay_runs WHERE measured_at > $1
		GROUP BY profile ORDER BY count(*) DESC, profile
	`, since)
	if err != nil {
		return nil, fmt.Errorf("list relay run profiles: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
