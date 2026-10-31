package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

func TestTelemetryRowsAndDailyRollups(t *testing.T) {
	s := liveStore(t) // migrated once by the helper
	ctx := context.Background()
	if err := s.RunMigrations(ctx); err != nil {
		t.Fatalf("migrations are not idempotent: %v", err)
	}
	day := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	app := telemetry.App{Version: "1.0.4", Channel: "stable", OS: "macos", OSMajor: "26", Arch: "arm64"}
	a, b := "t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "t-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	events := []telemetry.EventRow{
		{TS: day, Install: a, App: app, Name: "connect", Engine: "xray", Fields: `{"engine":"xray","ok":true}`},
		{TS: day.Add(time.Minute), Install: a, App: app, Name: "connect", Engine: "xray", Fields: `{"engine":"xray","ok":false}`},
		{TS: day, Install: b, App: app, Name: "connect", Engine: "xray", Fields: `{"engine":"xray","ok":true}`},
	}
	errs := []telemetry.ErrorRow{
		{TS: day, Install: a, App: app, Code: "engine.start", Component: "xray", FP: "0011223344556677", Msg: "dial <ip>", Count: 3},
		{TS: day, Install: b, App: app, Code: "engine.start", Component: "xray", FP: "0011223344556677", Msg: "dial <ip>", Count: 1},
	}
	if err := s.InsertTelemetry(ctx, events, errs); err != nil {
		t.Fatal(err)
	}
	var installs, total uint64
	row := s.conn.QueryRow(ctx, `SELECT uniqMerge(installs), sum(events) FROM telemetry_daily_events WHERE day = toDate(?) AND name = 'connect'`, day)
	if err := row.Scan(&installs, &total); err != nil {
		t.Fatal(err)
	}
	if installs != 2 || total != 3 {
		t.Fatalf("daily events: installs %d, events %d", installs, total)
	}
	row = s.conn.QueryRow(ctx, `SELECT uniqMerge(installs), sum(occurrences) FROM telemetry_daily_errors WHERE day = toDate(?) AND code = 'engine.start'`, day)
	if err := row.Scan(&installs, &total); err != nil {
		t.Fatal(err)
	}
	if installs != 2 || total != 4 {
		t.Fatalf("daily errors: installs %d, occurrences %d", installs, total)
	}
	var ttl string
	if err := s.conn.QueryRow(ctx, `SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = 'telemetry_events'`).Scan(&ttl); err != nil {
		t.Fatal(err)
	}
	if !contains(ttl, "toIntervalDay(90)") {
		t.Fatalf("raw events TTL: %s", ttl)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
