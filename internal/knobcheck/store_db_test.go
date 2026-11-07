package knobcheck

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
	"github.com/m1d1x/hikkari-control/internal/relayruns"
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
	name := fmt.Sprintf("hikkari_knobcheck_%d", time.Now().UnixNano())
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

// The whole path: a tester's chunk with its load rounds goes through the
// relay dataset's ingester, then the checker holds each round to the node's
// knobs; findings land in knob_checks and in the errors, once.
func TestDBIngestFindsAndReportsOnce(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	seedNode(t, pool, "ams-a", "amsterdam-1-002", "chrome_155")
	exec(t, pool, `UPDATE entry_nodes SET knobs_at = now() - interval '1 day'`)
	body := strings.Join([]string{
		"2026/11/07 12:00:00.000 [diag:reach] self iface=eth isp=AS8402",
		// The node hands out chrome_155; the config holds chrome.
		"2026/11/07 12:00:01.000 [diag:load] p-amsterdam-1-002 proto=vless-reality fp=chrome sni=www.samsung.com port=8443 flow=xtls-rprx-vision dl=1048576/1048576 took=1s end=ok burst=50/50 timeout=0 med=1ms after=3/3 med=1ms",
		// A link the tester imported: nothing to hold it to.
		"2026/11/07 12:00:02.000 [diag:load] p-imported proto=vless-xhttp fp=edge port=443 flow=none alpn=h2/http/1.1 dl=1048576/1048576 took=1s end=ok burst=50/50 timeout=0 med=1ms after=3/3 med=1ms",
	}, "\n")
	exec(t, pool, `INSERT INTO support_tester_log_chunks (install_id, platform, version, lines, body) VALUES ('t-aaaa', 'windows', '1.0.56', 3, $1)`, body)
	if n, err := relayruns.NewIngester(relayruns.NewStore(pool), pool).Once(ctx); err != nil || n != 2 {
		t.Fatalf("relay rows: %d %v", n, err)
	}

	s := NewStore(pool)
	errs := &sink{fail: fmt.Errorf("clickhouse down")}
	in := NewIngester(s, errs)
	if _, err := in.Once(ctx); err == nil {
		t.Fatal("a failed send moved on")
	}
	if got, _ := s.Checks(ctx, Query{Since: time.Now().Add(-time.Hour)}); len(got) != 0 {
		t.Fatalf("a round whose findings were not sent is checked: %+v", got)
	}
	errs.fail = nil
	n, err := in.Once(ctx)
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if len(errs.rows) != 1 {
		t.Fatalf("%+v", errs.rows)
	}
	r := errs.rows[0]
	if r.Code != CodeKnobMismatch || r.Install != "t-aaaa" || r.Component != ComponentKnob || r.App.Version != "1.0.56" ||
		r.App.OS != "windows" || len(r.FP) != 16 || r.Msg != "p-amsterdam-1-002: hub выдаёт fp=chrome_155 для AS8402, в конфиге клиента chrome" {
		t.Fatalf("%+v", r)
	}
	all, err := s.Checks(ctx, Query{Install: "t-aaaa", Since: time.Now().Add(-time.Hour)})
	if err != nil || len(all) != 2 {
		t.Fatalf("%+v %v", all, err)
	}
	states := map[string]string{}
	for _, c := range all {
		states[c.Profile] = c.State
	}
	if states["amsterdam-1-002"] != StateMismatch || states["imported"] != StateUnknown {
		t.Fatalf("%v", states)
	}
	bad, _ := s.Checks(ctx, Query{Since: time.Now().Add(-time.Hour), Mismatch: true})
	if len(bad) != 1 || bad[0].Declared["fp"] != "chrome_155" || bad[0].Config["fp"] != "chrome" || bad[0].Network != "AS8402" {
		t.Fatalf("%+v", bad)
	}

	// A second pass finds nothing new.
	if n, err := in.Once(ctx); err != nil || n != 0 || len(errs.rows) != 1 {
		t.Fatalf("read twice: %d %v %d", n, err, len(errs.rows))
	}
}

// The wire tables of 032 are gone, the knobs_at triggers stay.
func TestDBWireTablesAreGone(t *testing.T) {
	pool := dbPool(t)
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename LIKE 'wire\_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_trigger WHERE tgname IN ('entry_nodes_knobs_at', 'entry_node_network_knobs_at', 'entry_node_network_knobs_gone')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("triggers %d %v", n, err)
	}
}
