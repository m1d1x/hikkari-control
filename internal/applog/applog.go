// Package applog is a thin structured-logging helper that fan-outs records
// both to stderr (transitional) and to a Sink that typically writes them to
// ClickHouse + the live admin log broker.
//
// applog intentionally has no dependencies on analytics/logsink so it can be
// imported by low-level packages (auth, sfu, db, ...). The concrete sink
// implementation lives in internal/applog/applogsink.
//
// Usage:
//
//	applog.Info(ctx, "sfu", "user joined room", applog.F("user_id", uid), applog.F("room", rid))
package applog

import (
	"context"
	"encoding/json"
	"log"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Levels compatible with the app_logs.level LowCardinality column.
const (
	LevelTrace = "trace"
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Record is the transport-neutral log line applog hands to a Sink. It mirrors
// analytics.LogRecord but lives here to avoid import cycles (auth→applog
// must not reach back into analytics).
type Record struct {
	Timestamp time.Time
	Level     string
	Source    string
	UserID    uuid.UUID
	SessionID string
	Category  string
	Message   string
	Context   string
	ServerID  string

	TraceID     string
	RequestID   string
	DurationMs  uint32
	Stacktrace  string
	Fingerprint string
	File        string
	Line        uint32
	ReleaseSHA  string
}

// Sink accepts batches of log records. Implementations must be non-blocking.
type Sink interface {
	Write(records []Record)
}

// Field is a single key/value pair attached to a log line.
type Field struct {
	Key   string
	Value any
}

// F is a shorthand constructor for Field.
func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

// Logger fans log lines out to stderr and an optional Sink.
type Logger struct {
	sink       Sink
	source     string
	serverID   string
	releaseSHA string
}

// New creates a Logger. sink may be nil (stderr-only).
// releaseSHA tags every record with the current build commit for release
// regression analysis (correlate "this error appeared in build X").
func New(sink Sink, source, serverID, releaseSHA string) *Logger {
	if source == "" {
		source = "backend"
	}
	return &Logger{sink: sink, source: source, serverID: serverID, releaseSHA: releaseSHA}
}

// ── Context user_id ────────────────────────────────────────────────────

type ctxKey int

const userIDKey ctxKey = 0

// WithUserID attaches a user id to ctx so downstream applog calls pick it up
// automatically.
func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFrom retrieves the user id attached via WithUserID.
func UserIDFrom(ctx context.Context) (uuid.UUID, bool) {
	if ctx == nil {
		return uuid.Nil, false
	}
	v := ctx.Value(userIDKey)
	if v == nil {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok
}

const (
	traceIDKey   ctxKey = 1
	requestIDKey ctxKey = 2
)

// WithTraceID attaches a trace id to ctx so downstream applog calls pick it up
// automatically.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// TraceIDFrom retrieves the trace id attached via WithTraceID.
func TraceIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(traceIDKey).(string)
	return v
}

// WithRequestID attaches a request id to ctx so downstream applog calls pick
// it up automatically.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom retrieves the request id attached via WithRequestID.
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

// trimGoPath collapses an absolute Go source path to its "package/file.go"
// suffix so log records stay short while still pointing to the right file.
func trimGoPath(p string) string {
	// Keep only "package/file.go" suffix.
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			for j := i - 1; j >= 0; j-- {
				if p[j] == '/' || p[j] == '\\' {
					return p[j+1:]
				}
			}
			return p
		}
	}
	return p
}

// ── Field encoding ─────────────────────────────────────────────────────

// fieldsToJSON encodes key/value pairs into a JSON object string. Empty keys
// are skipped; nil values are recorded as "<missing>".
func fieldsToJSON(fields []Field) string {
	if len(fields) == 0 {
		return ""
	}
	m := make(map[string]any, len(fields))
	for _, f := range fields {
		if f.Key == "" {
			continue
		}
		if f.Value == nil {
			m[f.Key] = "<missing>"
			continue
		}
		m[f.Key] = f.Value
	}
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// ── Logger methods ────────────────────────────────────────────────────

func (l *Logger) logLine(ctx context.Context, level, cat, msg string, fields []Field) {
	ctxJSON := fieldsToJSON(fields)

	// Duplicate to stderr for operator visibility during migration.
	if ctxJSON != "" {
		log.Printf("[%s/%s] %s %s", level, cat, msg, ctxJSON)
	} else {
		log.Printf("[%s/%s] %s", level, cat, msg)
	}

	if l == nil || l.sink == nil {
		return
	}

	traceID := TraceIDFrom(ctx)
	uid, _ := UserIDFrom(ctx)
	if !shouldSample(level, cat, traceID) {
		// Per-user boost overrides sampling for debug/trace — the admin can
		// flip this on for a specific user_id when debugging a complaint
		// without raising verbosity for everyone.
		if !isBoosted(ctx, uid) {
			return
		}
	}
	file, line := "", uint32(0)
	if _, f, ln, ok := runtime.Caller(2); ok { // 2 = skip logLine + public wrapper
		file = trimGoPath(f)
		line = uint32(ln)
	}
	fp := ""
	if level == LevelWarn || level == LevelError {
		fp = Fingerprint(cat, msg)
	}
	rec := Record{
		Timestamp:   time.Now().UTC(),
		Level:       level,
		Source:      l.source,
		UserID:      uid,
		Category:    cat,
		Message:     msg,
		Context:     ctxJSON,
		ServerID:    l.serverID,
		TraceID:     traceID,
		RequestID:   RequestIDFrom(ctx),
		File:        file,
		Line:        line,
		Fingerprint: fp,
		ReleaseSHA:  l.releaseSHA,
	}
	l.sink.Write([]Record{rec})
}

func (l *Logger) Trace(ctx context.Context, cat, msg string, fields ...Field) {
	l.logLine(ctx, LevelTrace, cat, msg, fields)
}
func (l *Logger) Debug(ctx context.Context, cat, msg string, fields ...Field) {
	l.logLine(ctx, LevelDebug, cat, msg, fields)
}
func (l *Logger) Info(ctx context.Context, cat, msg string, fields ...Field) {
	l.logLine(ctx, LevelInfo, cat, msg, fields)
}
func (l *Logger) Warn(ctx context.Context, cat, msg string, fields ...Field) {
	l.logLine(ctx, LevelWarn, cat, msg, fields)
}
func (l *Logger) Error(ctx context.Context, cat, msg string, fields ...Field) {
	l.logLine(ctx, LevelError, cat, msg, fields)
}

// ── Package-level default ─────────────────────────────────────────────

var defaultLogger atomic.Pointer[Logger]

// SetDefault installs the package-level logger used by the bare Trace/Debug/
// Info/Warn/Error helpers. Safe to call at program startup.
func SetDefault(l *Logger) {
	defaultLogger.Store(l)
}

// Default returns the package-level logger (may be nil before SetDefault).
func Default() *Logger {
	return defaultLogger.Load()
}

func getDefault() *Logger {
	if l := defaultLogger.Load(); l != nil {
		return l
	}
	// Fallback: stderr-only logger so callers never panic before init.
	return &Logger{source: "backend"}
}

// Trace logs via the default logger.
func Trace(ctx context.Context, cat, msg string, fields ...Field) {
	getDefault().Trace(ctx, cat, msg, fields...)
}

// Debug logs via the default logger.
func Debug(ctx context.Context, cat, msg string, fields ...Field) {
	getDefault().Debug(ctx, cat, msg, fields...)
}

// Info logs via the default logger.
func Info(ctx context.Context, cat, msg string, fields ...Field) {
	getDefault().Info(ctx, cat, msg, fields...)
}

// Warn logs via the default logger.
func Warn(ctx context.Context, cat, msg string, fields ...Field) {
	getDefault().Warn(ctx, cat, msg, fields...)
}

// Error logs via the default logger.
func Error(ctx context.Context, cat, msg string, fields ...Field) {
	getDefault().Error(ctx, cat, msg, fields...)
}
