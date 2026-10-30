package metrics

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Service health gauges — set by health tracker.
var (
	ServiceUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "nekko",
		Name:      "service_up",
		Help:      "Whether a backing service is healthy (1) or down (0).",
	}, []string{"service"})

	ServiceLatency = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "nekko",
		Name:      "service_check_latency_seconds",
		Help:      "Latency of the last health check in seconds.",
	}, []string{"service"})

	// AlertingSinksConfigured is the number of ENABLED real notification sinks
	// on this binary's alerting engine, EXCLUDING the always-on log fallback.
	// 0 means alerts go nowhere a human will see them — the 2026-05-19 failure
	// mode where the engine logged "engine started (1 sinks)" and a 9h 100% CPU
	// incident paged no one. Set once by alerting.New; an external dead-man's
	// switch can alert on `nekko_alerting_sinks_configured == 0`.
	AlertingSinksConfigured = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "nekko",
		Name:      "alerting_sinks_configured",
		Help:      "Number of enabled real notification sinks (relay/telegram/webhook/nekkobot), excluding the always-on log fallback. 0 = alerts go nowhere.",
	})
)

// HTTP metrics — recorded by middleware.
var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "nekko",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "nekko",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency in seconds.",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"method", "path"})
)

// Runtime / business metrics.
//
// SFU voice metrics live in sfu.go because they need a region constLabel
// resolved at process start (each SFU bin runs in its own geo-shard).
var (
	WSConnectionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "nekko",
		Name:      "ws_connections_active",
		Help:      "Number of active WebSocket connections.",
	})
)

// Handler returns the Prometheus metrics HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Middleware records HTTP request metrics and emits a Server-Timing header
// so browser DevTools and Go benchmarks can see wall-clock server time.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip metrics endpoint and WebSocket upgrades — they need raw ResponseWriter
		if r.URL.Path == "/metrics" || r.URL.Path == "/ws" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		ctx, timings := WithTimings(r.Context())
		r = r.WithContext(ctx)
		rw := &responseWriter{ResponseWriter: w, statusCode: 200, start: start, timings: timings}
		next.ServeHTTP(rw, r)
		duration := time.Since(start).Seconds()

		// Normalize path to avoid cardinality explosion
		path := normalizePath(r.URL.Path)
		method := r.Method

		httpRequestsTotal.WithLabelValues(method, path, strconv.Itoa(rw.statusCode)).Inc()
		httpRequestDuration.WithLabelValues(method, path).Observe(duration)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode  int
	start       time.Time
	wroteHeader bool
	timings     *Timings
}

// Write ensures Server-Timing is emitted for handlers that never call
// WriteHeader explicitly (e.g. they stream bytes with an implicit 200).
func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.writeServerTiming()
		rw.wroteHeader = true
	}
	return rw.ResponseWriter.Write(b)
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.writeServerTiming()
		rw.wroteHeader = true
	}
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) writeServerTiming() {
	ms := float64(time.Since(rw.start).Microseconds()) / 1000.0
	header := fmt.Sprintf("total;dur=%.2f", ms)
	if extra := rw.timings.header(); extra != "" {
		header = header + ", " + extra
	}
	rw.ResponseWriter.Header().Set("Server-Timing", header)
}

// Hijack implements http.Hijacker for WebSocket support.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Unwrap exposes the inner ResponseWriter so http.NewResponseController can
// reach the underlying connection for SetReadDeadline / SetWriteDeadline.
// Without it, deadline-relaxation middleware silently no-ops.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// normalizePath collapses dynamic path segments to reduce label cardinality.
func normalizePath(p string) string {
	// Keep the first 3 segments, collapse IDs
	// /api/v1/admin/analytics/events → /api/v1/admin/analytics/events
	// /api/servers/abc-123/members   → /api/servers/:id/members
	segments := splitPath(p)
	for i, seg := range segments {
		if looksLikeID(seg) {
			segments[i] = ":id"
		}
	}
	result := "/"
	for i, s := range segments {
		if i > 0 {
			result += "/"
		}
		result += s
	}
	return result
}

func splitPath(p string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			if start < i {
				parts = append(parts, p[start:i])
			}
			start = i + 1
		}
	}
	if start < len(p) {
		parts = append(parts, p[start:])
	}
	return parts
}

func looksLikeID(s string) bool {
	if len(s) < 8 {
		return false
	}
	digits := 0
	dashes := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digits++
		}
		if c == '-' {
			dashes++
		}
	}
	// UUID-like or numeric ID
	return (dashes >= 3 && len(s) > 20) || digits > len(s)/2
}
