package stand

import (
	"context"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/relayruns"
	"strings"
	"testing"
	"time"
)

func TestDBCapacityClaimStoreAndFreshSummary(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages(slot,in_russia) VALUES('ru-capacity',true),('outside-capacity',false)`); err != nil {
		t.Fatal(err)
	}
	scheduler := newTestScheduler(pool, nil, relayruns.NewStore(pool))
	if n, err := scheduler.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("RU scheduling: %d %v", n, err)
	}
	if old, err := scheduler.Claim(ctx, "ru-capacity"); err != nil || old != nil {
		t.Fatalf("old prober claimed new task: %+v %v", old, err)
	}
	task, err := scheduler.ClaimWithCapacity(ctx, "ru-capacity", true)
	if err != nil || task == nil || task.Knob != capacity.TaskKind {
		t.Fatalf("capacity claim: %+v %v", task, err)
	}
	key := strings.Repeat("a", 64)
	sample := capacity.Sample{Key: key, MeasuredAt: time.Now().Unix(), Bytes: 1000000, DurationMS: 1000, Mbps: 8}
	if _, err := scheduler.Finish(ctx, "outside-capacity", task.ID, Result{Capacity: []capacity.Sample{sample}}); err == nil {
		t.Fatal("another point submitted a result")
	}
	if n, err := scheduler.Finish(ctx, "ru-capacity", task.ID, Result{Core: "test-core", Capacity: []capacity.Sample{sample}}); err != nil || n != 1 {
		t.Fatalf("finish: %d %v", n, err)
	}
	store := capacity.Store{Pool: pool}
	metrics, err := store.Metrics(ctx, []string{key})
	if err != nil || len(metrics) != 1 || metrics[0].Mbps != 8 || metrics[0].Points != 1 {
		t.Fatalf("summary: %+v %v", metrics, err)
	}
	if n, err := store.Save(ctx, "ru-capacity", []capacity.Sample{sample}, "test-core"); err != nil || n != 0 {
		t.Fatalf("duplicate changed observation: %d %v", n, err)
	}
	unrelated, err := store.Metrics(ctx, []string{strings.Repeat("b", 64)})
	if err != nil || len(unrelated) != 0 {
		t.Fatal("summary exposed another configuration")
	}
	// An overseas control point must not contribute a misleading RU measurement.
	sample.Mbps = 16
	sample.DurationMS = 500
	if _, err := store.Save(ctx, "outside-capacity", []capacity.Sample{sample}, "test-core"); err != nil {
		t.Fatal(err)
	}
	metrics, err = store.Metrics(ctx, []string{key})
	if err != nil || len(metrics) != 1 || metrics[0].Points != 1 || metrics[0].Mbps != 8 {
		t.Fatal("overseas point entered RU rating")
	}
	if _, err := pool.Exec(ctx, `UPDATE relay_capacity SET measured_at=now()-interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	metrics, err = store.Metrics(ctx, []string{key})
	if err != nil || len(metrics) != 0 {
		t.Fatal("expired measurements remained visible")
	}
}
