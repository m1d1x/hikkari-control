package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// The hub computes the panel's charts itself, on each chart's own step, so an
// operator opening a tab gets an answer already made instead of waiting for
// raw rows to be read (cache.go). Every chart of the panel in every window it
// offers is computed all the time (warmCharts), for the owner. A chart drawn
// for one entry, knob or profile (the fleet's voice per cascade, the relay
// runs) has as many URLs as there are of those: each one an operator opened is
// computed too, for warmKeep after it was last opened, and kept in
// admin_warm_charts so a restart of the hub picks them up again.
//
// One chart at a time: the hub has two cores and the trunk's own journals to
// read too.
const (
	warmKeep  = 30 * 24 * time.Hour
	warmTick  = 10 * time.Second
	warmMax   = 200 // opened URLs warmed at most; past it the least recently opened go
	warmFlush = time.Minute
)

// warmCharts is every chart URL the panel asks for, byte for byte as
// admin/src/lib/api builds it: the cache keys on the URL.
func warmCharts() []string {
	var out []string
	add := func(path, param string, values ...string) {
		if len(values) == 0 {
			out = append(out, path)
			return
		}
		for _, v := range values {
			out = append(out, path+"?"+param+"="+v)
		}
	}
	day3 := []string{"24h", "7d", "30d"}
	add("/api/v1/admin/entry/overview", "window", day3...)
	add("/api/v1/admin/entry/machines", "window", day3...)
	add("/api/v1/admin/entry/protocols", "window", day3...)
	add("/api/v1/admin/entry/fleet", "")
	add("/api/v1/admin/entry/fleet/chart", "")
	add("/api/v1/admin/entry/spend", "")
	add("/api/v1/admin/entry/providers/compare", "")
	add("/api/v1/admin/entry/pressure", "range", "15m", "1h", "6h", "24h")
	add("/api/v1/admin/entry/domains/metrics", "window", "7d", "30d", "90d")
	add("/api/v1/admin/fleet/profiles", "window", day3...)
	add("/api/v1/admin/hikkari/chart", "window", "6h", "24h", "7d")
	for _, p := range []string{"metrics", "errors", "downtime", "downtime-users", "testers"} {
		add("/api/v1/admin/product/"+p, "window", day3...)
	}
	add("/api/v1/admin/trunk/wires", "window", "6h", "24h", "7d")
	add("/api/v1/admin/trunk/egress", "window", "6h", "24h", "7d")
	for _, w := range []string{"6h", "24h", "7d", "30d"} {
		out = append(out, "/api/v1/admin/trunk/hearing?window="+w+"&view=trunk")
	}
	add("/api/v1/admin/support/tester-relays", "window", "24h", "7d")
	return out
}

// warmEvery is the step on which a chart over `window` (or `range`) is
// recomputed: the width of its smallest bucket, a minute at least.
func warmEvery(window string) time.Duration {
	switch window {
	case "7d":
		return 5 * time.Minute
	case "30d", "90d":
		return 15 * time.Minute
	default: // 6h, 24h and charts with no window
		return time.Minute
	}
}

// chartWindow is the span a chart URL asks for: `window`, or `range` on the
// censor's pressure.
func chartWindow(q url.Values) string {
	if w := q.Get("window"); w != "" {
		return w
	}
	return q.Get("range")
}

// warmKey marks a request the warmer made in this process. A request off the
// network carries only what net/http puts in its context, so it cannot carry
// this key.
type warmKey struct{}

func warmRole(ctx context.Context) (auth.AdminRole, bool) {
	r, ok := ctx.Value(warmKey{}).(auth.AdminRole)
	return r, ok
}

// warmSet is the charts asked for, by "role\x00uri", with when they were last
// asked; dirty holds those not yet written to the table.
type warmSet struct {
	mu    sync.Mutex
	asked map[string]time.Time
	dirty map[string]bool
}

var warmed = &warmSet{asked: map[string]time.Time{}, dirty: map[string]bool{}}

// note records that an operator opened a chart. Written to the table at most
// once a minute per chart.
func (s *warmSet) note(role, uri string, now time.Time) {
	k := role + "\x00" + uri
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.asked[k]; ok && now.Sub(prev) < warmFlush {
		return
	}
	s.asked[k] = now
	s.dirty[k] = true
}

type warmChart struct {
	role, uri string
	asked     time.Time
}

// list is the charts asked within warmKeep, most recent first, at most warmMax.
func (s *warmSet) list(now time.Time) []warmChart {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]warmChart, 0, len(s.asked))
	for k, at := range s.asked {
		if now.Sub(at) >= warmKeep {
			delete(s.asked, k)
			continue
		}
		role, uri := splitWarmKey(k)
		out = append(out, warmChart{role: role, uri: uri, asked: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].asked.After(out[j].asked) })
	if len(out) > warmMax {
		for _, c := range out[warmMax:] {
			delete(s.asked, c.role+"\x00"+c.uri)
		}
		out = out[:warmMax]
	}
	return out
}

func splitWarmKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i], k[i+1:]
		}
	}
	return "", k
}

// ChartWarmer recomputes the panel's charts behind the admin gate.
type ChartWarmer struct {
	pool db.Pool
	h    http.Handler
	now  func() time.Time
}

// NewChartWarmer warms the charts adminMux serves. pool may be nil: the
// charts asked since the hub started are warmed, none survive a restart.
func NewChartWarmer(pool db.Pool, adminMux http.Handler) *ChartWarmer {
	return &ChartWarmer{pool: pool, h: adminMux, now: time.Now}
}

// Run warms until ctx ends.
func (w *ChartWarmer) Run(ctx context.Context) {
	w.load(ctx)
	t := time.NewTicker(warmTick)
	defer t.Stop()
	lastFlush := w.now()
	var spent time.Duration
	var rounds int
	lastReport := w.now()
	for {
		start := time.Now()
		n := w.round(ctx)
		if n > 0 {
			spent += time.Since(start)
			rounds += n
		}
		if now := w.now(); now.Sub(lastFlush) >= warmFlush {
			w.flush(ctx)
			lastFlush = now
		}
		if now := w.now(); now.Sub(lastReport) >= 10*time.Minute {
			applog.Info(ctx, "admin", "charts warmed",
				applog.F("charts", rounds), applog.F("ms", spent.Milliseconds()),
				applog.F("kept", len(warmed.list(now))))
			spent, rounds, lastReport = 0, 0, now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// round recomputes every chart whose answer is older than its step. Returns
// how many it computed.
func (w *ChartWarmer) round(ctx context.Context) int {
	n := 0
	owner := string(auth.AdminRoleOwner)
	all := make([]warmChart, 0, 64)
	seen := map[string]bool{}
	for _, uri := range warmCharts() {
		all = append(all, warmChart{role: owner, uri: uri})
		seen[owner+"\x00"+uri] = true
	}
	for _, c := range warmed.list(w.now()) {
		if !seen[c.role+"\x00"+c.uri] {
			all = append(all, c)
		}
	}
	for _, c := range all {
		if ctx.Err() != nil {
			return n
		}
		u, err := url.ParseRequestURI(c.uri)
		if err != nil || !isChartPath(u.Path) {
			continue
		}
		every := warmEvery(chartWindow(u.Query()))
		// A little early, so a click on the step's edge still finds it fresh.
		if age, ok := charts.age(c.role, c.uri); ok && age < every-warmTick {
			continue
		}
		w.compute(ctx, c.role, c.uri)
		n++
	}
	return n
}

func (w *ChartWarmer) compute(ctx context.Context, role, uri string) {
	cctx, cancel := context.WithTimeout(context.WithValue(ctx, warmKey{}, auth.AdminRole(role)), chartTimeout)
	defer cancel()
	req := httptest.NewRequestWithContext(cctx, http.MethodGet, uri, nil)
	rec := &discardWriter{header: http.Header{}}
	w.h.ServeHTTP(rec, req)
}

// load reads the charts asked within warmKeep from the table.
func (w *ChartWarmer) load(ctx context.Context) {
	if w.pool == nil {
		return
	}
	rows, err := w.pool.Query(ctx, `
		SELECT role, uri, asked_at FROM admin_warm_charts
		WHERE asked_at > $1 ORDER BY asked_at DESC LIMIT $2`, w.now().Add(-warmKeep), warmMax)
	if err != nil {
		applog.Error(ctx, "admin", "warm charts read failed", applog.F("err", err.Error()))
		return
	}
	defer rows.Close()
	warmed.mu.Lock()
	defer warmed.mu.Unlock()
	for rows.Next() {
		var role, uri string
		var at time.Time
		if err := rows.Scan(&role, &uri, &at); err != nil {
			return
		}
		k := role + "\x00" + uri
		if prev, ok := warmed.asked[k]; !ok || at.After(prev) {
			warmed.asked[k] = at
		}
	}
}

// flush writes the charts asked since the last flush and drops the ones
// nobody asked for within warmKeep.
func (w *ChartWarmer) flush(ctx context.Context) {
	warmed.mu.Lock()
	type row struct {
		role, uri string
		at        time.Time
	}
	var out []row
	for k := range warmed.dirty {
		if at, ok := warmed.asked[k]; ok {
			role, uri := splitWarmKey(k)
			out = append(out, row{role, uri, at})
		}
	}
	warmed.dirty = map[string]bool{}
	warmed.mu.Unlock()
	if w.pool == nil {
		return
	}
	for _, r := range out {
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO admin_warm_charts (role, uri, asked_at) VALUES ($1, $2, $3)
			ON CONFLICT (role, uri) DO UPDATE SET asked_at = GREATEST(admin_warm_charts.asked_at, EXCLUDED.asked_at)`,
			r.role, r.uri, r.at); err != nil {
			applog.Error(ctx, "admin", "warm charts write failed", applog.F("err", err.Error()))
			return
		}
	}
	if _, err := w.pool.Exec(ctx, `DELETE FROM admin_warm_charts WHERE asked_at <= $1`, w.now().Add(-warmKeep)); err != nil {
		applog.Error(ctx, "admin", "warm charts prune failed", applog.F("err", err.Error()))
	}
}

type discardWriter struct{ header http.Header }

func (d *discardWriter) Header() http.Header         { return d.header }
func (d *discardWriter) WriteHeader(int)             {}
func (d *discardWriter) Write(p []byte) (int, error) { return io.Discard.Write(p) }
