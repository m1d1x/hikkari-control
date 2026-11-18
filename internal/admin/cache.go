package admin

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The panel's charts are read from raw rows on every request: a day of the
// trunk call is about 1.7 s of the hub's two cores, and a page draws several.
// Clicking between tabs asked the same questions again and again, so every
// tab waited seconds for an answer the hub had given a minute ago.
//
// chartCache keeps the last answer of every chart request behind the admin
// gate. An answer younger than chartFresh is handed out as it is; one younger
// than chartStale is handed out at once and recomputed in the background, so
// the next click gets the new one; an older one is recomputed while the
// request waits. Two requests for the same chart never compute it twice.
//
// The data under these charts lands a minute at a time, so an answer up to
// chartFresh old says nothing the operator could act on differently.
const (
	chartFresh   = 30 * time.Second
	chartStale   = 10 * time.Minute
	chartTimeout = 2 * time.Minute
	// chartMaxBody bounds what one entry may hold: a chart answer is tens of
	// kilobytes, anything far larger is a list that should not live here.
	chartMaxBody = 4 << 20
)

// chartPaths are the read-only answers that are charts or summaries over
// minutes of data. Logs, jobs, security, keys and everything an action
// reads back right after itself stay out: those must be live.
var chartPaths = []string{
	"/api/v1/admin/entry/overview",
	"/api/v1/admin/entry/fleet",
	"/api/v1/admin/entry/fleet/chart",
	"/api/v1/admin/entry/pressure",
	"/api/v1/admin/entry/domains/metrics",
	"/api/v1/admin/entry/providers/compare",
	"/api/v1/admin/entry/spend",
	"/api/v1/admin/entry/protocols",
	"/api/v1/admin/fleet/profiles",
	"/api/v1/admin/hikkari/chart",
	"/api/v1/admin/relay-runs/chart",
	"/api/v1/admin/product/downtime",
	"/api/v1/admin/product/downtime-users",
	"/api/v1/admin/product/errors",
	"/api/v1/admin/product/metrics",
	"/api/v1/admin/product/testers",
	"/api/v1/admin/trunk/egress",
	"/api/v1/admin/trunk/hearing",
	"/api/v1/admin/trunk/wires",
}

func isChartPath(p string) bool {
	for _, c := range chartPaths {
		if p == c {
			return true
		}
	}
	return false
}

type chartAnswer struct {
	status int
	header http.Header
	body   []byte
	at     time.Time
}

type chartEntry struct {
	answer *chartAnswer  // nil until the first computation ends with 200
	wait   chan struct{} // open while a computation runs
	last   *chartAnswer  // the running computation's result, for its waiters
}

type chartCache struct {
	mu      sync.Mutex
	entries map[string]*chartEntry
	now     func() time.Time
}

var charts = &chartCache{entries: map[string]*chartEntry{}, now: time.Now}

// serve answers r from the cache or through next. The key holds the role:
// a gate inside next (RequireOwner) decides per role, so one role's answer
// is never handed to another.
func (c *chartCache) serve(w http.ResponseWriter, r *http.Request, role string, next http.Handler) {
	key := role + "\x00" + r.URL.RequestURI()
	now := c.now()

	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		e = &chartEntry{}
		c.entries[key] = e
		c.sweep(now)
	}
	if a := e.answer; a != nil {
		age := now.Sub(a.at)
		if age < chartFresh {
			c.mu.Unlock()
			a.write(w)
			return
		}
		if age < chartStale {
			if e.wait == nil {
				c.start(e, key, r, next)
			}
			c.mu.Unlock()
			a.write(w)
			return
		}
	}
	if e.wait == nil {
		c.start(e, key, r, next)
	}
	wait := e.wait
	c.mu.Unlock()

	select {
	case <-wait:
	case <-r.Context().Done():
		return
	}
	c.mu.Lock()
	a := e.last
	c.mu.Unlock()
	if a == nil {
		writeError(w, http.StatusInternalServerError, "server error")
		return
	}
	a.write(w)
}

// start computes the answer in its own goroutine, detached from the request
// that asked: an operator leaving the tab must not throw away a computation
// the next click will want. Called with c.mu held.
func (c *chartCache) start(e *chartEntry, key string, r *http.Request, next http.Handler) {
	done := make(chan struct{})
	e.wait = done
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), chartTimeout)
	req := r.Clone(ctx)
	go func() {
		defer cancel()
		rec := &chartRecorder{header: http.Header{}, status: http.StatusOK}
		func() {
			defer func() {
				if p := recover(); p != nil {
					applog.Error(ctx, "admin", "chart computation panicked", applog.F("path", r.URL.Path))
					rec.status = http.StatusInternalServerError
				}
			}()
			next.ServeHTTP(rec, req)
		}()
		a := &chartAnswer{status: rec.status, header: rec.header, body: rec.body.Bytes(), at: c.now()}
		c.mu.Lock()
		e.last = a
		if a.status == http.StatusOK && len(a.body) <= chartMaxBody {
			e.answer = a
		}
		e.wait = nil
		c.mu.Unlock()
		close(done)
	}()
}

// sweep drops answers nobody asked for within chartStale. Called with c.mu
// held, on every new key: the keys are charts times windows, a few hundred
// at most.
func (c *chartCache) sweep(now time.Time) {
	for k, e := range c.entries {
		if e.wait == nil && (e.answer == nil || now.Sub(e.answer.at) >= chartStale) && e.last != nil {
			delete(c.entries, k)
		}
	}
}

func (a *chartAnswer) write(w http.ResponseWriter) {
	h := w.Header()
	for k, v := range a.header {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		h[k] = v
	}
	w.WriteHeader(a.status)
	_, _ = w.Write(a.body)
}

// chartRecorder holds one computed answer.
type chartRecorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *chartRecorder) Header() http.Header { return r.header }

func (r *chartRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
}

func (r *chartRecorder) Write(p []byte) (int, error) {
	r.wrote = true
	return r.body.Write(p)
}
