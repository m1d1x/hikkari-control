package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/entrybench"
)

// A run against the hub's real bench routes (internal/entry), dialled
// directly instead of through a relay's SOCKS: what is under test is the
// traffic and the arithmetic, not xray.

var quickShape = benchShape{
	chatMinGap: 20 * time.Millisecond, chatMaxGap: 60 * time.Millisecond,
	mediaFirst: 100 * time.Millisecond, mediaMinGap: 300 * time.Millisecond, mediaMaxGap: 400 * time.Millisecond,
	mediaDown: 256 << 10, mediaUp: 64 << 10,
	voiceEvery:  20 * time.Millisecond,
	chatTimeout: 2 * time.Second, mediaTimout: 5 * time.Second,
	voiceDrain: 200 * time.Millisecond,
}

type plainDialer struct{ net.Dialer }

// cutDialer is a relay that can restart under the run: cut closes every
// connection through it and refuses new ones for `down`.
type cutDialer struct {
	net.Dialer
	mu    sync.Mutex
	conns []net.Conn
	until time.Time
}

func (d *cutDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	down := time.Now().Before(d.until)
	d.mu.Unlock()
	if down {
		return nil, errors.New("relay restarting")
	}
	c, err := d.Dialer.DialContext(ctx, network, addr)
	if err == nil {
		d.mu.Lock()
		d.conns = append(d.conns, c)
		d.mu.Unlock()
	}
	return c, err
}

func (d *cutDialer) cut(down time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		c.Close()
	}
	d.conns = nil
	d.until = time.Now().Add(down)
}

// oneVantage is the hub's token lookup with one slot: "tok" is ru-test's.
type oneVantage struct{}

func (oneVantage) VantageByToken(_ context.Context, token string) (string, error) {
	if token == "tok" {
		return "ru-test", nil
	}
	return "", entry.ErrNoSuchVantage
}

func hubBench(t *testing.T) (*httptest.Server, benchTarget) {
	t.Helper()
	mux := http.NewServeMux()
	entry.RegisterBenchRoutes(mux, oneVantage{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, benchTarget{base: srv.URL + entrybench.Base, token: "tok", dialer: &plainDialer{}}
}

func TestABenchMeasuresEveryFlow(t *testing.T) {
	_, target := hubBench(t)
	r := runBench(context.Background(), target, 3*time.Second, quickShape)
	if r.Error != "" {
		t.Fatalf("run failed: %s", r.Error)
	}
	if r.Drops != 0 || r.ChatErrors != 0 || r.MediaErrors != 0 || r.VoiceDrops != 0 {
		t.Fatalf("a clean path broke: %+v", r)
	}
	if r.ChatN < 20 || r.ChatRTTP50Ms <= 0 || r.ChatRTTP95Ms < r.ChatRTTP50Ms {
		t.Fatalf("chat: %+v", r)
	}
	if r.MediaN < 4 || r.DownMbps <= 0 || r.UpMbps <= 0 || r.BytesDown < int64(quickShape.mediaDown) {
		t.Fatalf("media: %+v", r)
	}
	// ~150 frames in 3 s at 20 ms; every one came back on a loopback.
	if r.VoiceSent < 100 || r.VoiceLost != 0 || r.VoiceRTTP50Ms <= 0 {
		t.Fatalf("voice: sent %d lost %d rtt %.2f", r.VoiceSent, r.VoiceLost, r.VoiceRTTP50Ms)
	}
	if r.DurationS < 3 || r.DurationS > 6 {
		t.Fatalf("duration %.1f s", r.DurationS)
	}
	// And the hub would pass it against itself, at the thresholds the bench
	// policy ships with (internal/db/migrations/067_entry_bench_policy.sql).
	pol := entry.BenchPolicy{MinThroughputShare: 0.8, MaxRTTGrowth: 1.3, Pairs: 1}
	if ok, why, err := entry.CompareBench(r, r, pol); !ok || err != nil {
		t.Fatalf("a run against itself: %v %v", why, err)
	}
}

func TestAPathThatBreaksMidRunCountsItsDrops(t *testing.T) {
	_, target := hubBench(t)
	relay := &cutDialer{}
	target.dialer = relay
	go func() {
		time.Sleep(time.Second)
		relay.cut(500 * time.Millisecond)
	}()
	r := runBench(context.Background(), target, 2500*time.Millisecond, quickShape)
	if r.Error != "" {
		t.Fatalf("the run started fine: %s", r.Error)
	}
	if r.Drops == 0 || r.VoiceDrops == 0 {
		t.Fatalf("connections were cut and nothing was counted: %+v", r)
	}
}

func TestADeadTunnelIsNoMeasurement(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens: the tunnel is down
	target := benchTarget{base: "http://" + addr + entrybench.Base, token: "tok", dialer: &plainDialer{}}
	r := runBench(context.Background(), target, 2*time.Second, quickShape)
	if r.Error == "" || r.ChatN != 0 {
		t.Fatalf("a dead path measured something: %+v", r)
	}
}

func TestBenchBaseIsTheTunnelledHost(t *testing.T) {
	got, err := benchBase("https://api.nekkochat.com/healthz")
	if err != nil || got != "https://api.nekkochat.com"+entrybench.Base {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := benchBase("healthz"); err == nil {
		t.Fatal("a URL with no host")
	}
}

func TestPercentileByNearestRank(t *testing.T) {
	xs := []float64{5, 1, 4, 2, 3}
	if p := percentile(xs, 0.5); p != 3 {
		t.Fatalf("p50 %v", p)
	}
	if p := percentile(xs, 0.95); p != 5 {
		t.Fatalf("p95 %v", p)
	}
	if percentile(nil, 0.5) != 0 {
		t.Fatal("empty")
	}
}

func TestACanaryAndItsControlAreBenchedAtOnce(t *testing.T) {
	_, canary := hubBench(t)
	_, control := hubBench(t)
	start := time.Now()
	r := runBenches(context.Background(), []benchTarget{canary, control}, 2*time.Second, quickShape)
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("the two ran one after the other (%s)", took)
	}
	if r.Error != "" || r.ChatN == 0 || r.Control == nil || r.Control.ChatN == 0 || r.Control.Control != nil {
		t.Fatalf("pair %+v control %+v", r, r.Control)
	}
	if lone := runBenches(context.Background(), []benchTarget{canary}, time.Second, quickShape); lone.Control != nil {
		t.Fatal("a lone bench carries a control")
	}
}
