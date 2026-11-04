package relayruns

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/db"
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
	name := fmt.Sprintf("hikkari_relayruns_%d", time.Now().UnixNano())
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

func TestDBInsertIsIdempotent(t *testing.T) {
	pool := dbPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	row := Row{Source: SourceStand, MeasuredAt: at, Vantage: "spb", Core: "sing-box-lx-1.14.3", Profile: "amsterdam-1-002",
		Knob: "fp", Value: "firefox", Params: map[string]string{"fp": "firefox", "port": "8443"}, Round: 1,
		Dl: 1 << 20, DlEnd: "ok", Burst: 49, BurstTotal: 50, After: 3, AfterTotal: 3}
	for i := 0; i < 2; i++ {
		n, err := s.Insert(ctx, []Row{row})
		if err != nil {
			t.Fatal(err)
		}
		if want := 1 - i; n != want {
			t.Fatalf("pass %d added %d, want %d", i, n, want)
		}
	}
	rows, err := s.Rows(ctx, Query{Profile: "amsterdam-1-002", Since: at.Add(-time.Minute)})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if rows[0].Params["port"] != "8443" || rows[0].Burst != 49 || !rows[0].MeasuredAt.Equal(at) {
		t.Errorf("read back %+v", rows[0])
	}
	if p, err := s.Profiles(ctx, at.Add(-time.Minute)); err != nil || len(p) != 1 || p[0] != "amsterdam-1-002" {
		t.Errorf("profiles %v err %v", p, err)
	}
}

func TestDBIngestTesterRounds(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	add := func(install, body string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO support_tester_log_chunks (install_id, platform, version, lines, body)
			VALUES ($1, 'windows', '1.0.35', 1, $2)`, install, body); err != nil {
			t.Fatal(err)
		}
	}
	chrome := strings.ReplaceAll(configFirefox, `"fingerprint":"firefox"`, `"fingerprint":"chrome"`)
	add("t-a", "2026/11/04 01:00:03 [diag:config] "+chrome+"\n2026/11/04 01:00:04 [diag:reach] self iface=eth isp=AS12389")
	add("t-b", "2026/11/04 01:00:05 [diag:config] "+configFirefox) // another install's config must not leak
	add("t-a", "2026/11/04 01:01:00 [diag:load] p-amsterdam-1-002 proto=vless-reality dl=0/1048576 took=219ms end=closed burst=0/50 timeout=0 med=- after=0/3 med=-")
	add("t-a", "2026/11/04 01:25:43 [diag:config] "+configFirefox+"\n"+
		"2026/11/04 01:27:40 [diag:load] p-amsterdam-1-002 proto=vless-reality dl=1048576/1048576 took=534ms end=ok burst=49/50 timeout=0 med=266ms after=3/3 med=165ms")
	// The same line shipped twice, as a resend after an outage does.
	add("t-a", "2026/11/04 01:27:40 [diag:load] p-amsterdam-1-002 proto=vless-reality dl=1048576/1048576 took=534ms end=ok burst=49/50 timeout=0 med=266ms after=3/3 med=165ms")

	s := NewStore(pool)
	in := NewIngester(s, pool)
	n, err := in.Once(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("added %d, want 2", n)
	}
	if n, err := in.Once(ctx); err != nil || n != 0 {
		t.Fatalf("second pass added %d err %v", n, err)
	}
	rows, err := s.Rows(ctx, Query{Source: SourceTester, Since: time.Now().Add(-time.Hour)})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if rows[0].Params["fp"] != "chrome" || rows[1].Params["fp"] != "firefox" {
		t.Errorf("fp %q then %q, want chrome then firefox", rows[0].Params["fp"], rows[1].Params["fp"])
	}
	if rows[0].Network != "AS12389" || rows[0].Vantage != "t-a" {
		t.Errorf("row %+v", rows[0])
	}
	var cursor int64
	if err := pool.QueryRow(ctx, `SELECT last_chunk_id FROM relay_runs_cursor WHERE name = $1`, cursorName).Scan(&cursor); err != nil || cursor == 0 {
		t.Errorf("cursor %d err %v", cursor, err)
	}
}
