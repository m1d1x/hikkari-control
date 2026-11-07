package wirecheck

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

// dbPool applies the real migrations to a scratch database.
func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if adminURL == "" {
		t.Skip("no test DB — set TEST_DATABASE_URL to run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("cannot reach test DB: %v", err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("hikkari_wirecheck_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return pool
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// seedNode is a live Reality node of label on a relay of its own.
func seedNode(t *testing.T, pool *pgxpool.Pool, slug, label, fingerprint string) {
	t.Helper()
	now := time.Now()
	exec(t, pool, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto) VALUES ('ams3', '\x00', 'vdsina', 'amsterdam', 'vless-reality-vision')`)
	exec(t, pool, `INSERT INTO entry_relays (name, kind, status, key_id, envelope_ct, issued_at, expires_at, signed_at, box, identity_ct)
		VALUES ('ams3-1', 'personal', 'alive', 'k1', '\x00', $1, $2, $1, 'ams3', '\x00')`, now, now.Add(30*24*time.Hour))
	exec(t, pool, `INSERT INTO entry_nodes (slug, relay_id, proto, region, label, fingerprint)
		SELECT $1, id, 'vless-reality-vision', 'eu', $2, NULLIF($3, '') FROM entry_relays WHERE name = 'ams3-1'`, slug, label, fingerprint)
}

type sink struct {
	mu   sync.Mutex
	rows []telemetry.ErrorRow
	fail error
}

func (s *sink) InsertTelemetry(_ context.Context, _ []telemetry.EventRow, errs []telemetry.ErrorRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.rows = append(s.rows, errs...)
	return nil
}

func TestDBKnobsAtFollowsEveryChange(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	seedNode(t, pool, "ams-a", "amsterdam-1-002", "")
	s := NewStore(pool)
	exec(t, pool, `UPDATE entry_nodes SET knobs_at = now() - interval '1 day'`)
	d, err := s.declared(ctx, "amsterdam-1-002", 0)
	if err != nil || !d.Known || d.FP != "chrome" || time.Since(d.ChangedAt) < 23*time.Hour {
		t.Fatalf("%+v %v", d, err)
	}
	// A label change is no knob change.
	exec(t, pool, `UPDATE entry_nodes SET label = 'amsterdam-1-009'`)
	exec(t, pool, `UPDATE entry_nodes SET label = 'amsterdam-1-002'`)
	if d, _ := s.declared(ctx, "amsterdam-1-002", 0); time.Since(d.ChangedAt) < 23*time.Hour {
		t.Fatal("a label change counted as a knob change")
	}
	exec(t, pool, `UPDATE entry_nodes SET fingerprint = 'chrome_155'`)
	if d, _ := s.declared(ctx, "amsterdam-1-002", 0); d.FP != "chrome_155" || time.Since(d.ChangedAt) > time.Minute {
		t.Fatalf("node change: %+v", d)
	}
	exec(t, pool, `UPDATE entry_nodes SET knobs_at = now() - interval '1 day'`)
	exec(t, pool, `INSERT INTO entry_node_network_knobs (slug, asn, fingerprint, updated_at) VALUES ('ams-a', 12389, 'firefox', now() - interval '1 day')`)
	d, _ = s.declared(ctx, "amsterdam-1-002", 12389)
	if d.FP != "firefox" || time.Since(d.ChangedAt) > time.Minute {
		t.Fatalf("network insert: %+v", d) // the trigger stamps the insert, whatever the writer says
	}
	if d, _ := s.declared(ctx, "amsterdam-1-002", 3216); d.FP != "chrome_155" {
		t.Fatalf("another network: %+v", d)
	}
	exec(t, pool, `UPDATE entry_nodes SET knobs_at = now() - interval '1 day'`)
	exec(t, pool, `DELETE FROM entry_node_network_knobs`)
	if d, _ := s.declared(ctx, "amsterdam-1-002", 12389); d.FP != "chrome_155" || time.Since(d.ChangedAt) > time.Minute {
		t.Fatalf("network delete: %+v", d)
	}
	if d, _ := s.declared(ctx, "nobody", 0); d.Known {
		t.Fatal("a profile that is no node")
	}
}

func TestDBReferenceRefusesWhatTheTableWould(t *testing.T) {
	s := NewStore(dbPool(t))
	ctx := context.Background()
	good := RefRow{Core: "f350f1e6fea7", FP: "chrome", HK: "h1e431ec7444af069a", JA4: "t13d1516h2_8daaf6152771_d8a2da3f94cd", Source: "test"}
	if err := s.PutReference(ctx, []RefRow{good}); err != nil {
		t.Fatal(err)
	}
	good.HK = "h1121e2b99d2744911"
	if err := s.PutReference(ctx, []RefRow{good}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.References(ctx, "f350f1e6fea7")
	if err != nil || len(rows) != 1 || rows[0].HK != "h1121e2b99d2744911" {
		t.Fatalf("a new recording replaces the old: %+v %v", rows, err)
	}
	for _, bad := range []RefRow{
		{Core: "F350", FP: "chrome", HK: good.HK, JA4: good.JA4, Source: "x"},
		{Core: good.Core, FP: "Chrome!", HK: good.HK, JA4: good.JA4, Source: "x"},
		{Core: good.Core, FP: "chrome", HK: "deadbeef", JA4: good.JA4, Source: "x"},
		{Core: good.Core, FP: "chrome", HK: good.HK, JA4: "t13", Source: "x"},
	} {
		if err := s.PutReference(ctx, []RefRow{bad}); err == nil {
			t.Errorf("stored %+v", bad)
		}
	}
}

// The whole path: a tester's chunk with its wire lines, the reference and the
// node's knobs, out come the findings, in wire_hellos and in the errors, once.
func TestDBIngestFindsAndReportsOnce(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	seedNode(t, pool, "ams-a", "amsterdam-1-002", "chrome_155")
	exec(t, pool, `UPDATE entry_nodes SET knobs_at = now() - interval '1 day'`)
	s := NewStore(pool)
	var rows []RefRow
	for fp, r := range ref {
		rows = append(rows, RefRow{Core: "f350f1e6fea7", FP: fp, HK: r.HK, JA4: r.JA4, Source: "test"})
	}
	if err := s.PutReference(ctx, rows); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{"2026/11/07 12:00:00.000 [diag:load] p-amsterdam-1-002 proto=vless-reality dl=1/1", lineChrome, lineGo, lineSkip}, "\n")
	exec(t, pool, `INSERT INTO support_tester_log_chunks (install_id, platform, version, lines, body) VALUES ('t-aaaa', 'windows', '1.0.54', 4, $1)`, body)

	errs := &sink{fail: fmt.Errorf("clickhouse down")}
	in := NewIngester(s, errs)
	if _, err := in.Once(ctx); err == nil {
		t.Fatal("a failed send moved on")
	}
	errs.fail = nil
	n, err := in.Once(ctx)
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	got := map[string]string{}
	for _, r := range errs.rows {
		got[r.Code] = r.Msg
		if r.Install != "t-aaaa" || r.Component != ComponentWire || r.App.Version != "1.0.54" || r.App.OS != "windows" || len(r.FP) != 16 {
			t.Fatalf("%+v", r)
		}
	}
	// The node hands out chrome_155; the config holds chrome. The imported
	// profile without fp sends Go's hello.
	if len(errs.rows) != 2 || !strings.Contains(got[CodeKnobMismatch], "fp=chrome_155") || got[CodeGoHello] == "" {
		t.Fatalf("%+v", errs.rows)
	}
	hellos, err := s.Hellos(ctx, Query{Install: "t-aaaa", Since: time.Now().Add(-time.Hour)})
	if err != nil || len(hellos) != 3 {
		t.Fatalf("%d %v", len(hellos), err)
	}
	with, _ := s.Hellos(ctx, Query{Since: time.Now().Add(-time.Hour), Findings: true})
	if len(with) != 2 {
		t.Fatalf("lines with findings: %d", len(with))
	}

	// The same chunk read again, as after a crash before the cursor moved.
	exec(t, pool, `UPDATE relay_runs_cursor SET last_chunk_id = 0 WHERE name = $1`, cursorName)
	if n, err := in.Once(ctx); err != nil || n != 0 || len(errs.rows) != 2 {
		t.Fatalf("read twice: %d %v %d", n, err, len(errs.rows))
	}
}
