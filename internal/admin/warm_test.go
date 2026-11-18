package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

// warmMux is the admin mux of a test: one chart and one live screen behind the
// real gate (no admin key: a request off the network is refused).
func warmMux(t *testing.T, n *atomic.Int64) http.Handler {
	t.Helper()
	charts = &chartCache{entries: map[string]*chartEntry{}, now: time.Now}
	warmed = &warmSet{asked: map[string]time.Time{}, dirty: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/trunk/wires", RequireAdmin(nil, nil, func(w http.ResponseWriter, r *http.Request) {
		if a := auth.GetAdmin(r); a == nil || a.Role != auth.AdminRoleOwner {
			t.Errorf("the chart computed without the owner's role: %+v", a)
		}
		fmt.Fprintf(w, `{"n":%d}`, n.Add(1))
	}))
	mux.HandleFunc("GET /api/v1/admin/security", RequireAdmin(nil, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the warmer reached a screen that is not a chart")
	}))
	return mux
}

func TestTheHubComputesAnOpenedChartOnItsStep(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	const uri = "/api/v1/admin/trunk/wires?window=24h"
	warmed.note(string(auth.AdminRoleOwner), uri, time.Now())

	w := NewChartWarmer(nil, mux)
	if got := w.round(context.Background()); got != 1 || n.Load() != 1 {
		t.Fatalf("round computed %d charts, the chart ran %d times", got, n.Load())
	}
	if age, ok := charts.age(string(auth.AdminRoleOwner), uri); !ok || age > time.Second {
		t.Fatalf("the warmed answer is not kept: %v %v", age, ok)
	}
	// Within its step the chart is not computed again.
	if got := w.round(context.Background()); got != 0 || n.Load() != 1 {
		t.Fatalf("a fresh chart was recomputed: round %d, ran %d", got, n.Load())
	}
	// Past its step it is.
	charts.now = func() time.Time { return time.Now().Add(warmEvery("24h")) }
	if got := w.round(context.Background()); got != 1 || n.Load() != 2 {
		t.Fatalf("a chart past its step was not recomputed: round %d, ran %d", got, n.Load())
	}
}

func TestTheWarmerReachesChartsOnly(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	warmed.note(string(auth.AdminRoleOwner), "/api/v1/admin/security", time.Now())
	NewChartWarmer(nil, mux).round(context.Background())

	rec := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), warmKey{}, auth.AdminRoleOwner)
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, "GET", "/api/v1/admin/security", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("the warmer's mark opened a live screen: %d", rec.Code)
	}
}

func TestARequestOffTheNetworkIsNotTheWarmer(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/admin/trunk/wires?window=24h", nil)
	req.Header.Set("X-Warm", "owner")
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || n.Load() != 0 {
		t.Fatalf("a request without a session got the chart: %d", rec.Code)
	}
}

func TestAChartNobodyOpenedForAWeekIsNotWarmed(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	warmed.note(string(auth.AdminRoleOwner), "/api/v1/admin/trunk/wires?window=7d", time.Now().Add(-warmKeep))
	if got := NewChartWarmer(nil, mux).round(context.Background()); got != 0 {
		t.Fatalf("a chart unopened for %v was warmed", warmKeep)
	}
}

func TestAChartIsFreshForItsStep(t *testing.T) {
	for w, want := range map[string]time.Duration{"6h": time.Minute, "24h": time.Minute, "": time.Minute, "7d": 5 * time.Minute, "30d": 15 * time.Minute} {
		if got := warmEvery(w); got != want {
			t.Errorf("warmEvery(%q) = %v, want %v", w, got, want)
		}
		if warmEvery(w) >= chartStale {
			t.Errorf("a %q chart goes stale before the warmer comes back", w)
		}
	}
}
