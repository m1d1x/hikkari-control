package voiceruns

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
	name := fmt.Sprintf("hikkari_voiceruns_%d", time.Now().UnixNano())
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

func addChunk(t *testing.T, pool *pgxpool.Pool, install, body string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO support_tester_log_chunks (install_id, platform, version, lines, body)
		VALUES ($1, 'windows', '1.0.60', $2, $3)
	`, install, strings.Count(body, "\n")+1, body); err != nil {
		t.Fatal(err)
	}
}

func TestDBIngestKeepsEachSeriesOnce(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	store := NewStore(pool)
	in := NewIngester(store, pool)

	addChunk(t, pool, "t-1", strings.Join([]string{
		"2026/11/08 15:00:00.100 [diag:stun] unrelated",
		"2026/11/08 15:00:31.000 [diag:voice] path=tun profile=p-003 proto=vless-reality isp=AS12389 isp_at=card mobile=0 target=cloudflare sent=1500 recv=1500 loss=0.00 loss_run=0 rtt_p50=47 rtt_p95=55 rtt_max=240 jitter=3.4 gaps60=2 gaps150=1 gap_max=220 reorder=0 dup=0",
		"2026/11/08 15:01:02.000 [diag:voice] path=direct iface=Ethernet isp=AS12389 isp_at=card mobile=0 target=cloudflare sent=1500 recv=1499 loss=0.07 loss_run=1 rtt_p50=3 rtt_p95=4 rtt_max=9 jitter=0.3 gaps60=0 gaps150=0 gap_max=41 reorder=0 dup=0",
	}, "\n"))
	addChunk(t, pool, "t-2", "2026/11/08 15:02:00.000 [diag:voice nonce=n1] path=tun profile=p-9 proto=hysteria2 isp=unknown isp_at=baseline mobile=0 error=\"target not resolved\"")

	n, err := in.Once(ctx)
	if err != nil || n != 3 {
		t.Fatalf("first pass: %d rows, %v", n, err)
	}
	if n, err := in.Once(ctx); err != nil || n != 0 {
		t.Fatalf("second pass: %d rows, %v", n, err)
	}
	// The same lines arriving again (a client resending its batch) are no new series.
	addChunk(t, pool, "t-1", "2026/11/08 15:00:31.000 [diag:voice] path=tun profile=p-003 proto=vless-reality isp=AS12389 isp_at=card mobile=0 target=cloudflare sent=1500 recv=1500 loss=0.00 loss_run=0")
	if n, err := in.Once(ctx); err != nil || n != 0 {
		t.Fatalf("resent line: %d rows, %v", n, err)
	}

	rows, err := store.Rows(ctx, Query{Since: time.Now().Add(-time.Hour)})
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows: %d, %v", len(rows), err)
	}
	tun := rows[0]
	if tun.Path != "tun" || tun.Proto != "vless-reality" || tun.Gaps150 != 1 || *tun.GapMax != 220 || *tun.Jitter != 3.4 || tun.ClientVersion != "1.0.60" {
		t.Fatalf("tunnel row: %+v", tun)
	}
	if rows[2].Error != "target not resolved" || !rows[2].OnRequest {
		t.Fatalf("error row: %+v", rows[2])
	}
	only, err := store.Rows(ctx, Query{Since: time.Now().Add(-time.Hour), Install: "t-2"})
	if err != nil || len(only) != 1 {
		t.Fatalf("by install: %d, %v", len(only), err)
	}
	byProto, err := store.Rows(ctx, Query{Since: time.Now().Add(-time.Hour), Proto: "vless-reality"})
	if err != nil || len(byProto) != 1 {
		t.Fatalf("by proto: %d, %v", len(byProto), err)
	}
}
