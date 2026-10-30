package applog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Span is a single tracing span — one unit of work inside a trace.
// Spans are produced by middleware (root span per HTTP request) and by
// callers via StartSpan(ctx, name) for nested operations (DB query, RPC call,
// SFU action, ...). End() emits the span to the configured SpanSink.
//
// Always defer span.End() so the span is recorded even on early returns.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	StartedAt    time.Time
	UserID       uuid.UUID

	mu     sync.Mutex
	attrs  map[string]any
	status string // "ok" | "error"
	ended  bool

	sink SpanSink
}

// SpanSink consumes finished spans. Implementations must be non-blocking.
type SpanSink interface {
	WriteSpan(s SpanRecord)
}

// SpanRecord is the transport-neutral finished span the sink receives.
// Mirrors analytics.SpanRecord but lives here to avoid import cycles.
type SpanRecord struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	StartedAt    time.Time
	DurationMs   uint32
	Status       string
	UserID       uuid.UUID
	Attrs        string // JSON-encoded
}

// ── Default sink (package-level) ───────────────────────────────────────

var defaultSpanSink atomic.Pointer[SpanSink]

// SetDefaultSpanSink installs the package-level sink used by StartSpan.
// Safe to call at program startup. Pass nil to disable (StartSpan becomes
// a no-op — duration still measured but nothing is persisted).
func SetDefaultSpanSink(sink SpanSink) {
	if sink == nil {
		defaultSpanSink.Store(nil)
		return
	}
	defaultSpanSink.Store(&sink)
}

func getDefaultSpanSink() SpanSink {
	p := defaultSpanSink.Load()
	if p == nil {
		return nil
	}
	return *p
}

// ── Context propagation for the current span ───────────────────────────

const currentSpanIDKey ctxKey = 3

// WithSpanID stores the active span id in ctx so child spans can attach to it
// as parent_span_id. Used by middleware and StartSpan internally.
func WithSpanID(ctx context.Context, spanID string) context.Context {
	return context.WithValue(ctx, currentSpanIDKey, spanID)
}

// SpanIDFrom returns the active span id, or "" if no span is in ctx.
func SpanIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(currentSpanIDKey).(string)
	return v
}

// ── Public API ─────────────────────────────────────────────────────────

// StartSpan starts a new child span under the current trace/span in ctx.
// The returned ctx carries this span as the active one — child spans created
// from it will attach to it as their parent.
//
// Usage:
//
//	ctx, span := applog.StartSpan(ctx, "db.query users")
//	defer span.End()
//	rows, err := pool.Query(ctx, ...)
//	if err != nil { span.SetError(err) }
//
// If trace_id is missing in ctx (e.g. background goroutine outside a request)
// a new trace_id is generated so the span is still queryable.
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	traceID := TraceIDFrom(ctx)
	if traceID == "" {
		traceID = randomHex(16)
		ctx = WithTraceID(ctx, traceID)
	}
	parentSpanID := SpanIDFrom(ctx)
	spanID := randomHex(8)
	uid, _ := UserIDFrom(ctx)

	s := &Span{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Name:         name,
		StartedAt:    time.Now().UTC(),
		UserID:       uid,
		status:       "ok",
		sink:         getDefaultSpanSink(),
	}
	ctx = WithSpanID(ctx, spanID)
	return ctx, s
}

// SetError marks the span as failed. Multiple calls are safe — first non-nil
// error wins. Final status is recorded when End() is called.
func (s *Span) SetError(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "error" {
		s.status = "error"
		if s.attrs == nil {
			s.attrs = make(map[string]any, 1)
		}
		s.attrs["error"] = err.Error()
	}
}

// SetAttr adds a key/value pair to the span's attrs JSON.
// Last-write-wins on the same key.
func (s *Span) SetAttr(key string, value any) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = make(map[string]any, 4)
	}
	s.attrs[key] = value
}

// End emits the span to the sink. Idempotent — calling End twice does nothing.
func (s *Span) End() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	attrsCopy := s.attrs
	status := s.status
	s.mu.Unlock()

	if s.sink == nil {
		return
	}

	dur := time.Since(s.StartedAt)
	durMs := uint32(dur / time.Millisecond)

	attrsJSON := ""
	if len(attrsCopy) > 0 {
		if b, err := json.Marshal(attrsCopy); err == nil {
			attrsJSON = string(b)
		}
	}

	s.sink.WriteSpan(SpanRecord{
		TraceID:      s.TraceID,
		SpanID:       s.SpanID,
		ParentSpanID: s.ParentSpanID,
		Name:         s.Name,
		StartedAt:    s.StartedAt,
		DurationMs:   durMs,
		Status:       status,
		UserID:       s.UserID,
		Attrs:        attrsJSON,
	})
}

// ── Internal helpers ───────────────────────────────────────────────────

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
