package knobcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Store reads tester rounds from the relay dataset and keeps their checks.
type Store struct{ pool db.Pool }

// NewStore works on pool.
func NewStore(pool db.Pool) *Store { return &Store{pool: pool} }

// Round is one tester round of the relay dataset, with what the tester's log
// says about the client it came from.
type Round struct {
	RunID      int64
	Install    string
	MeasuredAt time.Time
	Network    string
	Profile    string
	Params     map[string]string
	Version    string
	Platform   string
}

// lookBack bounds the rounds a pass considers: an older round says nothing
// about the knobs now, and the dataset keeps rounds for good.
const lookBack = 7 * 24 * time.Hour

// unchecked are tester rounds of the last lookBack with no check yet, oldest
// first. The version and OS come from the log chunk the round was read from:
// a tester round is stamped with its chunk's received_at
// (internal/relayruns/tester.go).
func (s *Store) unchecked(ctx context.Context, limit int) ([]Round, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.vantage, r.measured_at, r.network, r.profile, r.params,
		       coalesce(c.version, ''), coalesce(c.platform, '')
		FROM relay_runs r
		LEFT JOIN knob_checks k ON k.run_id = r.id
		LEFT JOIN LATERAL (
			SELECT version, platform FROM support_tester_log_chunks
			WHERE install_id = r.vantage AND received_at <= r.measured_at
			ORDER BY received_at DESC LIMIT 1
		) c ON true
		WHERE r.source = 'tester' AND r.measured_at > now() - $1::interval AND k.run_id IS NULL
		ORDER BY r.measured_at ASC, r.id ASC LIMIT $2`, fmt.Sprintf("%d seconds", int(lookBack.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("read unchecked rounds: %w", err)
	}
	defer rows.Close()
	var out []Round
	for rows.Next() {
		var r Round
		var params []byte
		if err := rows.Scan(&r.RunID, &r.Install, &r.MeasuredAt, &r.Network, &r.Profile, &params, &r.Version, &r.Platform); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(params, &r.Params)
		out = append(out, r)
	}
	return out, rows.Err()
}

// declaredJSON is what the check row keeps of Declared.
func declaredJSON(d Declared) []byte {
	if !d.Known {
		return []byte("{}")
	}
	m := map[string]string{"fp": d.FP, "alpn": d.ALPN}
	if d.SNI != "" {
		m["sni"] = d.SNI
	}
	b, _ := json.Marshal(m)
	return b
}

// record stores a round's check; a round checked before keeps its first check.
func (s *Store) record(ctx context.Context, runID int64, state string, d Declared, findings []Finding) error {
	if findings == nil {
		findings = []Finding{}
	}
	f, _ := json.Marshal(findings)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO knob_checks (run_id, state, declared, findings) VALUES ($1, $2, $3, $4)
		ON CONFLICT (run_id) DO NOTHING`, runID, state, declaredJSON(d), f); err != nil {
		return fmt.Errorf("record knob check: %w", err)
	}
	return nil
}

// declared reads what the hub hands out for the node labelled profile to a
// subscriber of network (0: the node's own values).
func (s *Store) declared(ctx context.Context, profile string, asn uint32) (Declared, error) {
	var proto, fp, sni, alpn string
	var changed time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT n.proto, coalesce(k.fingerprint, n.fingerprint, ''), coalesce(k.sni, n.sni, ''),
		       coalesce(k.alpn, n.alpn, ''), greatest(n.knobs_at, coalesce(k.updated_at, n.knobs_at))
		FROM entry_nodes n
		LEFT JOIN entry_node_network_knobs k ON k.slug = n.slug AND k.asn = $2
		WHERE n.label = $1`, profile, int64(asn)).Scan(&proto, &fp, &sni, &alpn, &changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Declared{}, nil
	}
	if err != nil {
		return Declared{}, fmt.Errorf("declared knobs: %w", err)
	}
	return DeclaredFor(proto, fp, sni, alpn, changed), nil
}

// CheckRow is one checked round as the ops view shows it.
type CheckRow struct {
	Install    string            `json:"install"`
	MeasuredAt time.Time         `json:"measuredAt"`
	Profile    string            `json:"profile"`
	Network    string            `json:"network,omitempty"`
	State      string            `json:"state"`
	Declared   map[string]string `json:"declared"`
	Config     map[string]string `json:"config"`
	Findings   []Finding         `json:"findings"`
	CheckedAt  time.Time         `json:"checkedAt"`
}

// Query narrows a read of the checks.
type Query struct {
	Install  string
	Profile  string
	Since    time.Time
	Mismatch bool // only rounds with a finding
	Limit    int
}

// Checks reads checked rounds, newest first.
func (s *Store) Checks(ctx context.Context, q Query) ([]CheckRow, error) {
	if q.Limit <= 0 || q.Limit > 2000 {
		q.Limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.vantage, r.measured_at, r.profile, r.network, k.state, k.declared, r.params, k.findings, k.checked_at
		FROM knob_checks k JOIN relay_runs r ON r.id = k.run_id
		WHERE r.measured_at > $1 AND ($2 = '' OR r.vantage = $2) AND ($3 = '' OR r.profile = $3)
		  AND (NOT $4 OR k.state = 'mismatch')
		ORDER BY r.measured_at DESC, r.id DESC LIMIT $5`, q.Since, q.Install, q.Profile, q.Mismatch, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("read knob checks: %w", err)
	}
	defer rows.Close()
	out := []CheckRow{}
	for rows.Next() {
		var c CheckRow
		var declared, params, findings []byte
		if err := rows.Scan(&c.Install, &c.MeasuredAt, &c.Profile, &c.Network, &c.State, &declared, &params, &findings, &c.CheckedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(declared, &c.Declared)
		_ = json.Unmarshal(params, &c.Config)
		_ = json.Unmarshal(findings, &c.Findings)
		out = append(out, c)
	}
	return out, rows.Err()
}

// errorFP groups one kind of finding: the same knob of the same profile, with
// the same expectation and the same wrong value, is one group however many
// rounds repeat it. 16 hex, as the client errors table keys a group.
func errorFP(profile string, f Finding) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{f.Code, f.Knob, profile, f.Want, f.Got}, "\x00")))
	return hex.EncodeToString(sum[:8])
}
