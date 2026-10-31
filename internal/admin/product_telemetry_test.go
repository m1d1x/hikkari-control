package admin

import (
	"context"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

func TestProductWindowSpan(t *testing.T) {
	now := time.Date(2026, 10, 31, 14, 37, 0, 0, time.UTC)
	since, end := productWindows["7d"].span(now)
	if want := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("end %v, want %v", end, want)
	}
	if want := end.Add(-7 * 24 * time.Hour); !since.Equal(want) {
		t.Fatalf("since %v, want %v", since, want)
	}
	w := productWindows["7d"]
	if i := w.index(since, end.Add(-time.Hour)); i != w.N-1 {
		t.Fatalf("last bucket index %d", i)
	}
	if i := w.index(since, since.Add(-time.Hour)); i != -1 {
		t.Fatalf("bucket before the window: index %d", i)
	}
}

func TestVersionsNewestFirst(t *testing.T) {
	vs := []string{"1.0.9", "1.0.10", "0.9.4", "1.1.0"}
	sortVersionsDesc(vs)
	want := []string{"1.1.0", "1.0.10", "1.0.9", "0.9.4"}
	for i := range want {
		if vs[i] != want[i] {
			t.Fatalf("got %v, want %v", vs, want)
		}
	}
}

// TestProductScreens_AgainstClickHouse writes client telemetry through the
// hub's own insert and reads both product screens back. Needs
// CLICKHOUSE_TEST_DSN (see liveClickHouse).
func TestProductScreens_AgainstClickHouse(t *testing.T) {
	store := liveClickHouse(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 14, 37, 0, 0, time.UTC)
	mac := telemetry.App{Version: "1.0.10", Channel: "stable", OS: "macos", OSMajor: "26", Arch: "arm64"}
	win := telemetry.App{Version: "1.0.9", Channel: "stable", OS: "windows", OSMajor: "11", Arch: "amd64"}
	a, b, c := "t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "t-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "t-cccccccccccccccccccccccccccccccc"
	at := func(d time.Duration) time.Time { return now.Add(-d) }

	events := []telemetry.EventRow{
		// a: new today, connected on the second try.
		{TS: at(3 * time.Hour), Install: a, App: mac, Name: "install", Fields: `{}`},
		{TS: at(3 * time.Hour), Install: a, App: mac, Name: "launch", Fields: `{"cold":true}`},
		{TS: at(2 * time.Hour), Install: a, App: mac, Name: "connect", Engine: "xray", Fields: `{"engine":"xray","ok":false,"secs":9}`},
		{TS: at(2 * time.Hour), Install: a, App: mac, Name: "connect", Engine: "xray", Fields: `{"engine":"xray","ok":true,"secs":2}`},
		{TS: at(2 * time.Hour), Install: a, App: mac, Name: "first_connect_ok", Engine: "xray", Fields: `{"engine":"xray","secs_since_install":3600}`},
		// b: new today, never connected.
		{TS: at(1 * time.Hour), Install: b, App: win, Name: "install", Fields: `{}`},
		// c: installed last month, came back today.
		{TS: at(40 * 24 * time.Hour), Install: c, App: win, Name: "install", Fields: `{}`},
		{TS: at(30 * time.Minute), Install: c, App: win, Name: "connect", Engine: "singbox", Fields: `{"engine":"singbox","ok":true,"secs":4}`},
		{TS: at(30 * time.Minute), Install: c, App: win, Name: "daily_active", Fields: `{"connected_minutes":90}`},
	}
	errs := []telemetry.ErrorRow{
		{TS: at(2 * time.Hour), Install: a, App: mac, Code: "engine.start", Component: "xray", FP: "0011223344556677", Msg: "dial <ip>: refused", Count: 3},
		{TS: at(1 * time.Hour), Install: c, App: win, Code: "engine.start", Component: "xray", FP: "0011223344556677", Msg: "dial <ip>: refused", Count: 2},
		{TS: at(5 * time.Hour), Install: b, App: win, Code: "tunnel.dns", Component: "singbox", FP: "8899aabbccddeeff", Msg: "exchange timeout", Count: 1},
		// Outside the 24h window.
		{TS: at(3 * 24 * time.Hour), Install: b, App: win, Code: "ui.exception", Component: "ui", FP: "ffffffffffffffff", Msg: "old", Count: 1},
	}
	if err := store.InsertTelemetry(ctx, events, errs); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(nil, store)

	m, err := h.productMetrics(ctx, productWindows["24h"], now)
	if err != nil {
		t.Fatal(err)
	}
	if m.People.Active != 3 || m.People.Fresh != 2 || m.People.Returning != 1 || m.People.Total != 3 {
		t.Fatalf("people: %+v", m.People)
	}
	if len(m.Buckets) != 24 || len(m.People.Series.Active) != 24 {
		t.Fatalf("buckets: %d, series %d", len(m.Buckets), len(m.People.Series.Active))
	}
	var seriesFresh uint64
	for _, v := range m.People.Series.Fresh {
		seriesFresh += v
	}
	if seriesFresh != 2 {
		t.Fatalf("fresh series sums to %d", seriesFresh)
	}
	if m.Connect.Attempts != 3 || m.Connect.OkPct == nil || int(*m.Connect.OkPct) != 66 {
		t.Fatalf("connect: %+v", m.Connect)
	}
	if m.Connect.SecsP50 == nil || *m.Connect.SecsP50 != 3 {
		t.Fatalf("connect p50: %v", m.Connect.SecsP50)
	}
	if m.Connect.TunnelMinutesP50 == nil || *m.Connect.TunnelMinutesP50 != 90 {
		t.Fatalf("tunnel minutes: %v", m.Connect.TunnelMinutesP50)
	}
	if m.Activation.Installs != 2 || m.Activation.Connected != 1 || m.Activation.SecsP50 == nil || *m.Activation.SecsP50 != 3600 {
		t.Fatalf("activation: %+v", m.Activation)
	}
	if len(m.Platforms) != 2 || m.Platforms[0].Key != "windows" || m.Platforms[0].Installs != 2 {
		t.Fatalf("platforms: %+v", m.Platforms)
	}

	if m.Downloads.Installs != 2 || m.Downloads.Clicks != nil || m.Downloads.ConvPct != nil {
		t.Fatalf("downloads: %+v", m.Downloads)
	}
	if len(m.Downloads.Platforms) != 2 || m.Downloads.Platforms[0].Installs != 1 {
		t.Fatalf("download platforms: %+v", m.Downloads.Platforms)
	}

	e, err := h.productErrors(ctx, productWindows["24h"], now)
	if err != nil {
		t.Fatal(err)
	}
	if e.Occurrences != 6 || e.Installs != 3 || len(e.Groups) != 2 || e.Truncated {
		t.Fatalf("errors: occurrences %d, installs %d, groups %d", e.Occurrences, e.Installs, len(e.Groups))
	}
	g := e.Groups[0]
	if g.Code != "engine.start" || g.Occurrences != 5 || g.Installs != 2 {
		t.Fatalf("top group: %+v", g)
	}
	if len(g.OSes) != 2 || g.OSes[0] != "macos" || len(g.Versions) != 2 || g.Versions[0] != "1.0.10" {
		t.Fatalf("top group split: oses %v, versions %v", g.OSes, g.Versions)
	}
	if g.ID != "engine.start/xray/0011223344556677" {
		t.Fatalf("group id %q", g.ID)
	}

	// An empty window answers nulls, not zeros.
	empty, err := h.productMetrics(ctx, productWindows["24h"], now.Add(-60*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Connect.OkPct != nil || empty.Connect.SecsP50 != nil || empty.Activation.Pct != nil {
		t.Fatalf("empty window: %+v %+v", empty.Connect, empty.Activation)
	}
}
