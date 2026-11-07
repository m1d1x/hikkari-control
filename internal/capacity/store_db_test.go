package capacity

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/m1d1x/hikkari-control/internal/db"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStoreHistoryLatestFailureAndPointBoundary(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("hikkari_metrics_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages(slot,in_russia) VALUES('ru-one',true),('foreign',false)`); err != nil {
		t.Fatal(err)
	}
	store := Store{Pool: pool}
	now := time.Now().Unix()
	key := strings.Repeat("a", 64)
	sample := Sample{Key: key, MeasuredAt: now - 1900, Bytes: 1000000, DurationMS: 1000, Mbps: 8, RTTMS: 120}
	if n, err := store.Save(ctx, "ru-one", []Sample{sample}, "test"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := store.Save(ctx, "ru-one", []Sample{sample}, "test"); err != nil || n != 0 {
		t.Fatal("duplicate", n, err)
	}
	sample.MeasuredAt = now - 20
	sample.RTTMS = 80
	if _, err := store.Save(ctx, "ru-one", []Sample{sample}, "test"); err != nil {
		t.Fatal(err)
	}
	sample.RTTMS = 1
	if _, err := store.Save(ctx, "foreign", []Sample{sample}, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Metrics(ctx, []string{key, key})
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if got[0].RTTMS != 80 || got[0].Points != 1 || len(got[0].History) != 2 {
		t.Fatalf("wrong source/history: %+v", got)
	}
	failure := Sample{Key: key, MeasuredAt: now, Error: "unreachable"}
	if _, err := store.Save(ctx, "ru-one", []Sample{failure}, "test"); err != nil {
		t.Fatal(err)
	}
	got, err = store.Metrics(ctx, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Mbps != 0 || got[0].RTTMS != 0 || got[0].History[len(got[0].History)-1].Mbps != 0 {
		t.Fatalf("failure retained success: %+v", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE entry_vantages SET status='retired',retired_at=now() WHERE slot='ru-one'`); err != nil {
		t.Fatal(err)
	}
	got, err = store.Metrics(ctx, []string{key})
	if err != nil || len(got) != 0 {
		t.Fatal("retired point accepted", got, err)
	}
}
