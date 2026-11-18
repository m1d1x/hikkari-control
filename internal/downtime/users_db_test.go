package downtime

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	name := fmt.Sprintf("hikkari_downtime_%d", time.Now().UnixNano())
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

func TestDBUserPeriodsAreKeptOnceAndReadWithoutTesters(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	s := NewStore(pool)
	hour := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	const user, tester = "t-00000000000000000000000000000001", "t-00000000000000000000000000000002"
	if _, err := pool.Exec(ctx, `INSERT INTO support_install_aliases (install_id, alias) VALUES ($1, 'tester-900')`, tester); err != nil {
		t.Fatal(err)
	}
	first := telemetry.TunnelPeriod{Install: user, At: hour, Flowing: 8, Down: 1, CoreDown: 1, StallMinutes: 9, N300: 4, MS300: 2600, N1000: 1, MS1000: 1400}
	periods := []telemetry.TunnelPeriod{
		{Install: user, At: hour, Flowing: 1}, // the same period, sent before; the later one stands
		first,
		{Install: user, At: hour.Add(10 * time.Minute), Idle: 9, NetDown: 1, StallMinutes: 10},
		{Install: tester, At: hour, Down: 10},
	}
	// A batch the hub answered 503 to comes again: still one row a period.
	for i := 0; i < 2; i++ {
		if err := s.InsertTunnelPeriods(ctx, periods); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM product_user_tunnel_periods`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("rows = %d, %v", rows, err)
	}
	counts, err := s.UserCounts(ctx, hour, hour.Add(time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := UserCount{Install: user, Bucket: hour, On: 20, Lost: 2, StallMinutes: 19, MS300: 2600, MS1000: 1400}
	if len(counts) != 1 || counts[0].Install != want.Install || !counts[0].Bucket.Equal(want.Bucket) ||
		counts[0].On != want.On || counts[0].Lost != want.Lost || counts[0].StallMinutes != want.StallMinutes ||
		counts[0].MS300 != want.MS300 || counts[0].MS1000 != want.MS1000 {
		t.Fatalf("counts = %+v\nwant     %+v", counts, want)
	}

	// The table holds a period's numbers and has no column for anything else.
	cols, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = 'product_user_tunnel_periods' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer cols.Close()
	var names []string
	for cols.Next() {
		var n string
		if err := cols.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if got := strings.Join(names, ","); got != "install_id,period,o,i,d,c,n,stall_minutes,n300,ms300,n1000,ms1000,n3000,ms3000" {
		t.Fatalf("columns = %s", got)
	}
}

// A tester's bytes go to the hub through the tunnel only: a 1.0.93 line's
// figures past the tunnel are dropped, and the table has no place for them
// (owner, 18.11: what went past the VPN is the tester's own business).
func TestDBTesterTrafficKeepsTheTunnelOnly(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	s := NewStore(pool)
	old, ok := ParseBytesLine("[downtime] bytes from=1800000000 v=100,200,300,400;.", "t-a")
	if !ok {
		t.Fatal("1.0.93 line not read")
	}
	fresh, ok := ParseBytesLine("[downtime] bytes from=1800000120 v=10,20", "t-a")
	if !ok {
		t.Fatal("1.0.94 line not read")
	}
	if n, err := s.InsertTraffic(ctx, append(old, fresh...)); err != nil || n != 3 {
		t.Fatalf("insert: %d %v", n, err)
	}
	since := time.Unix(1800000000, 0).UTC()
	got, err := s.TrafficCounts(ctx, since, since.Add(time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tunnel != 330 {
		t.Fatalf("counts %+v, want 330 through the tunnel", got)
	}
	var direct int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'product_tester_traffic_minutes' AND column_name LIKE 'direct%'`).Scan(&direct); err != nil {
		t.Fatal(err)
	}
	if direct != 0 {
		t.Fatalf("the table still has %d columns for traffic past the tunnel", direct)
	}
}
