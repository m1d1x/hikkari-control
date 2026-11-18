package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

// warmMux is the admin mux of a test: every chart and one live screen behind
// the real gate (no admin key: a request off the network is refused).
func warmMux(t *testing.T, n *atomic.Int64) http.Handler {
	t.Helper()
	charts = &chartCache{entries: map[string]*chartEntry{}, now: time.Now}
	warmed = &warmSet{asked: map[string]time.Time{}, dirty: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/security", RequireAdmin(nil, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the warmer reached a screen that is not a chart")
	}))
	mux.HandleFunc("GET /api/v1/admin/", RequireAdmin(nil, nil, func(w http.ResponseWriter, r *http.Request) {
		if a := auth.GetAdmin(r); a == nil || a.Role != auth.AdminRoleOwner {
			t.Errorf("the chart computed without the owner's role: %+v", a)
		}
		fmt.Fprintf(w, `{"n":%d}`, n.Add(1))
	}))
	return mux
}

func TestTheHubComputesEveryChartOnItsStep(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	all := int64(len(warmCharts()))
	owner := string(auth.AdminRoleOwner)

	w := NewChartWarmer(nil, mux)
	if got := w.round(context.Background()); int64(got) != all || n.Load() != all {
		t.Fatalf("round computed %d charts, ran %d, want every one of %d", got, n.Load(), all)
	}
	for _, uri := range warmCharts() {
		if age, ok := charts.age(owner, uri); !ok || age > time.Second {
			t.Fatalf("%s is not kept: %v %v", uri, age, ok)
		}
	}
	// Within their step no chart is computed again.
	if got := w.round(context.Background()); got != 0 {
		t.Fatalf("fresh charts were recomputed: round %d", got)
	}
	// A minute on, the charts on a minute's step are; the ones over days wait.
	charts.now = func() time.Time { return time.Now().Add(time.Minute) }
	minute := 0
	for _, uri := range warmCharts() {
		u, _ := url.ParseRequestURI(uri)
		if warmEvery(chartWindow(u.Query())) == time.Minute {
			minute++
		}
	}
	if got := w.round(context.Background()); got != minute || minute == 0 || minute == int(all) {
		t.Fatalf("a minute on, round recomputed %d charts, want the %d on a minute's step", got, minute)
	}
}

func TestAChartOpenedForOneEntryIsComputedToo(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	const uri = "/api/v1/admin/trunk/hearing?window=24h&view=voice&entry=aezamsk1"
	warmed.note(string(auth.AdminRoleOwner), uri, time.Now())
	NewChartWarmer(nil, mux).round(context.Background())
	if _, ok := charts.age(string(auth.AdminRoleOwner), uri); !ok {
		t.Fatal("the entry's chart was not computed")
	}
}

func TestAnActionDropsTheKeptCharts(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	NewChartWarmer(nil, mux).round(context.Background())
	charts.dropAll()
	if _, ok := charts.age(string(auth.AdminRoleOwner), warmCharts()[0]); ok {
		t.Fatal("a chart outlived an action")
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

func TestAnEntrysChartNobodyOpenedFor30DaysIsNotWarmed(t *testing.T) {
	var n atomic.Int64
	mux := warmMux(t, &n)
	const uri = "/api/v1/admin/trunk/hearing?window=7d&view=voice&entry=gone"
	warmed.note(string(auth.AdminRoleOwner), uri, time.Now().Add(-warmKeep))
	NewChartWarmer(nil, mux).round(context.Background())
	if _, ok := charts.age(string(auth.AdminRoleOwner), uri); ok {
		t.Fatalf("an entry's chart unopened for %v was warmed", warmKeep)
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
