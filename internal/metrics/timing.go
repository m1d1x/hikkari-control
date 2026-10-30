package metrics

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// timingsKey scopes the per-request accumulator inside a context.
type timingsKey struct{}

// Timings accumulates labelled durations for a single HTTP request and is
// safe for concurrent use across handler goroutines (e.g. parallel DB+S3).
type Timings struct {
	mu     sync.Mutex
	totals map[string]int64 // label -> nanoseconds
}

// WithTimings attaches an empty Timings accumulator to the context.
// Middleware calls this once per request; handlers and lower-level code can
// then call Add or Track without caring whether timing is enabled.
func WithTimings(ctx context.Context) (context.Context, *Timings) {
	t := &Timings{totals: make(map[string]int64, 4)}
	return context.WithValue(ctx, timingsKey{}, t), t
}

// fromContext returns the accumulator attached to ctx, or nil if none.
func fromContext(ctx context.Context) *Timings {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(timingsKey{}).(*Timings)
	return t
}

// Add records a duration under the given label. No-op if no accumulator is
// attached to ctx, so it is always safe to call from arbitrary code paths.
func Add(ctx context.Context, label string, d time.Duration) {
	t := fromContext(ctx)
	if t == nil || d <= 0 {
		return
	}
	t.mu.Lock()
	t.totals[label] += int64(d)
	t.mu.Unlock()
}

// Track returns a closure that records the elapsed time under label when
// invoked. Intended for `defer metrics.Track(ctx, "s3")()` patterns.
func Track(ctx context.Context, label string) func() {
	t := fromContext(ctx)
	if t == nil {
		return noop
	}
	start := time.Now()
	return func() {
		Add(ctx, label, time.Since(start))
	}
}

func noop() {}

// header renders the accumulated timings as a Server-Timing fragment, e.g.
// "db;dur=12.34, s3;dur=4.56". Returns empty string if nothing was recorded.
func (t *Timings) header() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	if len(t.totals) == 0 {
		t.mu.Unlock()
		return ""
	}
	labels := make([]string, 0, len(t.totals))
	values := make(map[string]int64, len(t.totals))
	for k, v := range t.totals {
		labels = append(labels, k)
		values[k] = v
	}
	t.mu.Unlock()

	sort.Strings(labels)
	var b strings.Builder
	for i, l := range labels {
		if i > 0 {
			b.WriteString(", ")
		}
		ms := float64(values[l]) / float64(time.Millisecond)
		b.WriteString(l)
		b.WriteString(";dur=")
		b.WriteString(formatMillis(ms))
	}
	return b.String()
}

// formatMillis renders a duration in milliseconds with 2 decimal places
// without pulling in fmt for every header write.
func formatMillis(ms float64) string {
	// Round to 2 decimals manually — avoids fmt allocation on the hot path.
	if ms < 0 {
		ms = 0
	}
	hundredths := int64(ms*100 + 0.5)
	whole := hundredths / 100
	frac := hundredths % 100
	var buf [24]byte
	n := appendInt(buf[:0], whole)
	n = append(n, '.')
	if frac < 10 {
		n = append(n, '0')
	}
	n = appendInt(n, frac)
	return string(n)
}

func appendInt(dst []byte, v int64) []byte {
	if v == 0 {
		return append(dst, '0')
	}
	var tmp [20]byte
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	return append(dst, tmp[i:]...)
}

// Counter exposes a lightweight atomic counter for callers that want to
// measure event counts (e.g. number of DB roundtrips) alongside durations.
// Currently unused by the header but reserved so the API can grow without
// breaking callers.
type Counter struct{ v atomic.Int64 }

func (c *Counter) Inc()         { c.v.Add(1) }
func (c *Counter) Load() int64  { return c.v.Load() }
func (c *Counter) Reset() int64 { return c.v.Swap(0) }
