package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chartClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *chartClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *chartClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newChartCache() (*chartCache, *chartClock) {
	clk := &chartClock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	return &chartCache{entries: map[string]*chartEntry{}, now: clk.now}, clk
}

// countingChart answers with how many times it was computed.
func countingChart(n *atomic.Int64, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if release != nil {
			<-release
		}
		v := n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"n":%d}`, v)
	})
}

func askChart(c *chartCache, role, url string, h http.Handler) string {
	rec := httptest.NewRecorder()
	c.serve(rec, httptest.NewRequest("GET", url, nil), role, h)
	return rec.Body.String()
}

// waitComputed waits until the background computation of url ends.
func waitComputed(t *testing.T, c *chartCache, role, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		e := c.entries[role+"\x00"+url]
		busy := e != nil && e.wait != nil
		c.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the chart was never computed")
}

func TestAChartAskedAgainWithinAMinuteIsNotComputedAgain(t *testing.T) {
	c, clk := newChartCache()
	var n atomic.Int64
	h := countingChart(&n, nil)
	const url = "/api/v1/admin/trunk/wires?window=24h"

	if got := askChart(c, "owner", url, h); got != `{"n":1}` {
		t.Fatalf("first ask = %s", got)
	}
	clk.add(chartFresh - time.Second)
	if got := askChart(c, "owner", url, h); got != `{"n":1}` {
		t.Fatalf("a fresh answer was computed again: %s", got)
	}
	if n.Load() != 1 {
		t.Fatalf("computed %d times", n.Load())
	}
}

func TestAStaleChartIsHandedOutAtOnceAndRecomputedBehind(t *testing.T) {
	c, clk := newChartCache()
	var n atomic.Int64
	h := countingChart(&n, nil)
	const url = "/api/v1/admin/trunk/wires?window=24h"

	askChart(c, "owner", url, h)
	clk.add(warmEvery("24h") + time.Second)
	if got := askChart(c, "owner", url, h); got != `{"n":1}` {
		t.Fatalf("a stale ask waited for the new answer: %s", got)
	}
	waitComputed(t, c, "owner", url)
	if got := askChart(c, "owner", url, h); got != `{"n":2}` {
		t.Fatalf("the next ask did not get the recomputed answer: %s", got)
	}
}

func TestAChartOlderThanStaleIsRecomputedWhileTheAskWaits(t *testing.T) {
	c, clk := newChartCache()
	var n atomic.Int64
	h := countingChart(&n, nil)
	const url = "/api/v1/admin/trunk/wires?window=7d"

	askChart(c, "owner", url, h)
	clk.add(chartStale + time.Second)
	if got := askChart(c, "owner", url, h); got != `{"n":2}` {
		t.Fatalf("an answer older than chartStale was handed out: %s", got)
	}
}

func TestTwoAsksForOneChartComputeItOnce(t *testing.T) {
	c, _ := newChartCache()
	var n atomic.Int64
	release := make(chan struct{})
	h := countingChart(&n, release)
	const url = "/api/v1/admin/trunk/hearing?window=24h"

	var wg sync.WaitGroup
	got := make([]string, 2)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = askChart(c, "owner", url, h)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n.Load() != 1 || got[0] != `{"n":1}` || got[1] != `{"n":1}` {
		t.Fatalf("computed %d times, answers %v", n.Load(), got)
	}
}

func TestOneRolesChartIsNotHandedToAnother(t *testing.T) {
	c, _ := newChartCache()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Role") != "owner" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner_required"})
			return
		}
		fmt.Fprint(w, `{"secret":1}`)
	})
	const url = "/api/v1/admin/entry/spend"

	owner := httptest.NewRequest("GET", url, nil)
	owner.Header.Set("X-Role", "owner")
	c.serve(httptest.NewRecorder(), owner, "owner", h)

	rec := httptest.NewRecorder()
	c.serve(rec, httptest.NewRequest("GET", url, nil), "viewer", h)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a viewer got the owner's answer: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAFailedChartIsNotKept(t *testing.T) {
	c, _ := newChartCache()
	var fail atomic.Bool
	fail.Store(true)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "db down", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"ok":1}`)
	})
	const url = "/api/v1/admin/trunk/egress?window=24h"

	rec := httptest.NewRecorder()
	c.serve(rec, httptest.NewRequest("GET", url, nil), "owner", h)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("the failure was not passed on: %d", rec.Code)
	}
	fail.Store(false)
	if got := askChart(c, "owner", url, h); got != `{"ok":1}` {
		t.Fatalf("the failure was kept: %s", got)
	}
}

func TestOnlyChartsAreCached(t *testing.T) {
	for _, p := range []string{
		"/api/v1/admin/security",
		"/api/v1/admin/support/tester-logs",
		"/api/v1/admin/entry/jobs",
		"/api/v1/admin/entry/hub-key",
		"/api/v1/admin/trunk/wires/x",
	} {
		if isChartPath(p) {
			t.Errorf("%s is cached", p)
		}
	}
	if !isChartPath("/api/v1/admin/trunk/wires") {
		t.Error("trunk wires are not cached")
	}
}
