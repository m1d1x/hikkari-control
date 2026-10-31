package admin

// product_window.go is the time axis shared by the product screens (metrics,
// errors): the three windows the console offers and how each is cut into
// buckets. Everything is counted from the clients' telemetry
// (internal/analytics/telemetry.go), whose raw rows live 90 days, so every
// window here is read from the raw tables.

import "time"

// productWindow is one of the console's windows: N buckets of Step, the last
// one being the bucket that holds now (still filling).
type productWindow struct {
	Name string
	N    int
	Step time.Duration
}

var productWindows = map[string]productWindow{
	"24h": {Name: "24h", N: 24, Step: time.Hour},
	"7d":  {Name: "7d", N: 28, Step: 6 * time.Hour},
	"30d": {Name: "30d", N: 30, Step: 24 * time.Hour},
}

// parseProductWindow reads ?window=, defaulting to def.
func parseProductWindow(raw, def string) (productWindow, bool) {
	if raw == "" {
		raw = def
	}
	w, ok := productWindows[raw]
	return w, ok
}

// span returns the window's first bucket start and its end (the end of the
// bucket holding now). Buckets are aligned in UTC, the same way ClickHouse's
// toStartOfInterval aligns them, so a Go bucket and a SQL bucket share a key.
func (w productWindow) span(now time.Time) (time.Time, time.Time) {
	end := now.UTC().Truncate(w.Step).Add(w.Step)
	return end.Add(-time.Duration(w.N) * w.Step), end
}

// buckets lists the bucket starts, oldest first, as unix milliseconds — what
// the screen's chart takes.
func (w productWindow) buckets(since time.Time) []int64 {
	out := make([]int64, w.N)
	for i := range out {
		out[i] = since.Add(time.Duration(i) * w.Step).UnixMilli()
	}
	return out
}

// index maps a bucket start read from SQL to its slot, or -1.
func (w productWindow) index(since, bucket time.Time) int {
	d := bucket.Sub(since)
	if d < 0 {
		return -1
	}
	i := int(d / w.Step)
	if i >= w.N {
		return -1
	}
	return i
}
