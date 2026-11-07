package wirecheck

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

// Store keeps the lines read and the references they are held to.
type Store struct{ pool db.Pool }

// NewStore works on pool.
func NewStore(pool db.Pool) *Store { return &Store{pool: pool} }

// Hello is one stored line with its findings.
type Hello struct {
	Install    string            `json:"install"`
	ClientTS   string            `json:"clientTs"`
	MeasuredAt time.Time         `json:"measuredAt"`
	Version    string            `json:"version"`
	Platform   string            `json:"platform"`
	Profile    string            `json:"profile"`
	Proto      string            `json:"proto"`
	Core       string            `json:"core"`
	Network    string            `json:"network,omitempty"`
	Knobs      map[string]string `json:"knobs"`
	State      string            `json:"state"`
	JA4        string            `json:"ja4,omitempty"`
	HK         string            `json:"hk,omitempty"`
	ALPNSent   string            `json:"alpnSent,omitempty"`
	Wire       map[string]string `json:"wire,omitempty"`
	Findings   []Finding         `json:"findings"`
}

// Insert stores hellos and returns the ones not stored before: a chunk read
// twice yields its findings once.
func (s *Store) Insert(ctx context.Context, hellos []Hello) ([]Hello, error) {
	var fresh []Hello
	for _, h := range hellos {
		knobs, _ := json.Marshal(h.Knobs)
		wire, _ := json.Marshal(h.Wire)
		if h.Findings == nil {
			h.Findings = []Finding{}
		}
		findings, _ := json.Marshal(h.Findings)
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO wire_hellos (install_id, client_ts, measured_at, version, platform, profile, proto, core,
				network, knobs, state, ja4, hk, alpn_sent, wire, findings)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			ON CONFLICT (install_id, client_ts, profile) DO NOTHING`,
			h.Install, h.ClientTS, h.MeasuredAt, h.Version, h.Platform, h.Profile, h.Proto, h.Core,
			h.Network, knobs, h.State, h.JA4, h.HK, h.ALPNSent, wire, findings)
		if err != nil {
			return fresh, fmt.Errorf("insert wire hello: %w", err)
		}
		if tag.RowsAffected() == 1 {
			fresh = append(fresh, h)
		}
	}
	return fresh, nil
}

// unseen is the hellos not stored yet.
func (s *Store) unseen(ctx context.Context, hellos []Hello) ([]Hello, error) {
	var out []Hello
	for _, h := range hellos {
		var seen bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wire_hellos WHERE install_id = $1 AND client_ts = $2 AND profile = $3)`,
			h.Install, h.ClientTS, h.Profile).Scan(&seen); err != nil {
			return nil, fmt.Errorf("wire hello stored: %w", err)
		}
		if !seen {
			out = append(out, h)
		}
	}
	return out, nil
}

// Query narrows a read of the stored lines.
type Query struct {
	Install  string
	Profile  string
	Since    time.Time
	Findings bool // only lines with a finding
	Limit    int
}

// Hellos reads stored lines, newest first.
func (s *Store) Hellos(ctx context.Context, q Query) ([]Hello, error) {
	if q.Limit <= 0 || q.Limit > 2000 {
		q.Limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT install_id, client_ts, measured_at, version, platform, profile, proto, core, network,
		       knobs, state, ja4, hk, alpn_sent, wire, findings
		FROM wire_hellos
		WHERE measured_at > $1 AND ($2 = '' OR install_id = $2) AND ($3 = '' OR profile = $3)
		  AND (NOT $4 OR findings <> '[]'::jsonb)
		ORDER BY measured_at DESC, id DESC LIMIT $5`, q.Since, q.Install, q.Profile, q.Findings, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("read wire hellos: %w", err)
	}
	defer rows.Close()
	out := []Hello{}
	for rows.Next() {
		var h Hello
		var knobs, wire, findings []byte
		if err := rows.Scan(&h.Install, &h.ClientTS, &h.MeasuredAt, &h.Version, &h.Platform, &h.Profile, &h.Proto,
			&h.Core, &h.Network, &knobs, &h.State, &h.JA4, &h.HK, &h.ALPNSent, &wire, &findings); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(knobs, &h.Knobs)
		_ = json.Unmarshal(wire, &h.Wire)
		_ = json.Unmarshal(findings, &h.Findings)
		out = append(out, h)
	}
	return out, rows.Err()
}

// RefRow is one reference hello as stored.
type RefRow struct {
	Core    string    `json:"core"`
	FP      string    `json:"fp"`
	HK      string    `json:"hk"`
	JA4     string    `json:"ja4"`
	Source  string    `json:"source"`
	AddedAt time.Time `json:"addedAt"`
}

var errBadFP = errors.New("fp: lower-case letters, digits and _")

func validFP(fp string) bool {
	if fp == "" || len(fp) > 32 {
		return false
	}
	for _, c := range fp {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// Validate refuses a reference row the table would refuse, with a reason.
func (r RefRow) Validate() error {
	switch {
	case !reCore.MatchString(r.Core):
		return errors.New("core: 12 hex of the revision")
	case !validFP(r.FP):
		return errBadFP
	case !reHK.MatchString(r.HK):
		return errors.New("hk: h1 and 16 hex")
	case !reJA4.MatchString(r.JA4):
		return errors.New("ja4: not a JA4")
	case r.Source == "" || len(r.Source) > 64:
		return errors.New("source: 1 to 64 characters")
	}
	return nil
}

// PutReference writes rows, replacing a row of the same core and fp: a new
// recording of a revision is the truth about it.
func (s *Store) PutReference(ctx context.Context, rows []RefRow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, r := range rows {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("%s/%s: %w", r.Core, r.FP, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO wire_reference (core, fp, hk, ja4, source) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (core, fp) DO UPDATE SET hk = EXCLUDED.hk, ja4 = EXCLUDED.ja4, source = EXCLUDED.source, added_at = now()`,
			r.Core, r.FP, r.HK, r.JA4, r.Source); err != nil {
			return fmt.Errorf("put reference: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// References lists the rows of one core, or of all when core is "".
func (s *Store) References(ctx context.Context, core string) ([]RefRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT core, fp, hk, ja4, source, added_at FROM wire_reference
		WHERE $1 = '' OR core = $1 ORDER BY core, fp`, core)
	if err != nil {
		return nil, fmt.Errorf("read references: %w", err)
	}
	defer rows.Close()
	out := []RefRow{}
	for rows.Next() {
		var r RefRow
		if err := rows.Scan(&r.Core, &r.FP, &r.HK, &r.JA4, &r.Source, &r.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// reference is one core's rows as Check takes them; nil when there are none.
func (s *Store) reference(ctx context.Context, core string) (Reference, error) {
	if core == "" {
		return nil, nil
	}
	rows, err := s.References(ctx, core)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ref := Reference{}
	for _, r := range rows {
		ref[r.FP] = RefHello{HK: r.HK, JA4: r.JA4}
	}
	return ref, nil
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

// errorFP groups one kind of finding: the same code on the same knob of the
// same profile, with the same expectation, is one group however many rounds
// repeat it. 16 hex, as the client errors table keys a group.
func errorFP(profile, core string, f Finding) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{f.Code, f.Knob, profile, core, f.Want}, "\x00")))
	return hex.EncodeToString(sum[:8])
}
