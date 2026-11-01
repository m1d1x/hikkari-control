package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/auth"
)

// CSRFMiddleware enforces double-submit on mutating requests. Reads the
// nekko_admin_csrf cookie and expects the same value in the X-CSRF-Token
// header. Reject = 403 with a short JSON error so the panel can distinguish
// it from a generic 401.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !needsCSRFCheck(r) {
			next.ServeHTTP(w, r)
			return
		}

		header := r.Header.Get(auth.CSRFHeaderName)
		if header == "" {
			rejectCSRF(w, "missing X-CSRF-Token")
			return
		}

		if c, err := r.Cookie(auth.AdminCSRFCookieName); err == nil {
			if auth.CSRFTokensMatch(c.Value, header) {
				next.ServeHTTP(w, r)
				return
			}
		}
		rejectCSRF(w, "csrf token mismatch")
	})
}

// MaxBodyBytes wraps r.Body in http.MaxBytesReader for non-upload routes so
// a slow-body or oversized POST can't tie up server memory. Uploads listed in
// uploadPathPrefixes pass through unchanged.
//
// When a handler reads past the cap, MaxBytesReader returns a *MaxBytesError;
// handlers that don't translate this to 413 will surface a generic 500, which
// is still preferable to consuming an unbounded body.
func MaxBodyBytes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && !isUploadPath(r.URL.Path) {
			r.Body = http.MaxBytesReader(w, r.Body, defaultMaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// Trace generates trace_id (16 hex bytes) and request_id (8 hex bytes) for
// every request, attaches them to the request context via applog.WithTraceID/
// applog.WithRequestID, and echoes them back as response headers so the
// frontend can correlate its own log entries with the same trace.
//
// It also starts a root span per request that gets emitted to the configured
// applog SpanSink on response. The span name is "{METHOD} {normalizedPath}"
// where UUIDs and integer ids in the path are replaced with placeholders so
// the LowCardinality(String) column stays bounded.
//
// Health/readyz are skipped — they would dominate the spans table with noise.
func Trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-Id")
		if !isHexID(traceID, 32) {
			traceID = randHex(16)
		}
		requestID := r.Header.Get("X-Request-Id")
		if !isHexID(requestID, 16) {
			requestID = randHex(8)
		}

		ctx := applog.WithTraceID(r.Context(), traceID)
		ctx = applog.WithRequestID(ctx, requestID)

		w.Header().Set("X-Trace-Id", traceID)
		w.Header().Set("X-Request-Id", requestID)

		// Skip span emission for liveness/readiness probes — they fire every
		// few seconds from the load balancer and would drown signal in noise.
		path := r.URL.Path
		if path == "/healthz" || path == "/readyz" {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Start root span for the request. End is deferred so even panics or
		// hijacked WebSocket upgrades close out the span.
		spanName := r.Method + " " + normalizePath(path)
		ctx, span := applog.StartSpan(ctx, spanName)
		span.SetAttr("http.path", path)
		span.SetAttr("http.method", r.Method)

		sw := &statusWriter{ResponseWriter: w, status: 200}
		defer func() {
			span.SetAttr("http.status", sw.status)
			if sw.status >= 500 {
				span.SetError(errors.New(http.StatusText(sw.status)))
			}
			span.End()
		}()

		next.ServeHTTP(sw, r.WithContext(ctx))
	})
}

// RelaxDeadlinesForUploads extends per-connection Read/Write deadlines for the
// routes that stream. The base http.Server has ReadTimeout 30s / WriteTimeout
// 60s, which would cut a large object mid-stream; this middleware uses
// http.NewResponseController to push the deadlines forward only for requests
// that genuinely need it.
//
// Routes are matched against uploadPathPrefixes (defined in maxbytes.go) so
// the body cap and the deadline relaxation stay in sync.
func RelaxDeadlinesForUploads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUploadPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		rc := http.NewResponseController(w)
		now := time.Now()
		if err := rc.SetReadDeadline(now.Add(uploadReadDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			log.Printf("upload deadlines: SetReadDeadline %s: %v", r.URL.Path, err)
		}
		if err := rc.SetWriteDeadline(now.Add(uploadWriteDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			log.Printf("upload deadlines: SetWriteDeadline %s: %v", r.URL.Path, err)
		}
		next.ServeHTTP(w, r)
	})
}

// needsCSRFCheck is the gate. Safe methods + the allow-list pass through;
// everything else needs to present a valid token.
func needsCSRFCheck(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	path := r.URL.Path
	if csrfSkipPaths[path] {
		return false
	}
	for _, p := range csrfSkipPrefixes {
		if strings.HasPrefix(path, p) {
			return false
		}
	}
	return true
}

// rejectCSRF writes a 403 with a stable JSON body. Kept tiny — no need
// for the full writeError shape from internal/auth (which lives in a
// package we'd otherwise import here just for one helper).
func rejectCSRF(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	// Minimal JSON, hand-written to avoid pulling encoding/json into the
	// hot path for what's effectively a constant.
	_, _ = w.Write([]byte(`{"error":"csrf","reason":"` + reason + `"}`))
}

// defaultMaxBodyBytes caps non-upload request bodies. 1 MiB is far above any
// JSON API payload Nekko issues today and far below what an attacker would
// need to exhaust memory by streaming an unbounded body.
const defaultMaxBodyBytes int64 = 1 << 20

func isUploadPath(p string) bool {
	for _, prefix := range uploadPathPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// statusWriter wraps http.ResponseWriter to capture the status code so the
// trace span can record it. http.Hijacker is preserved so WebSocket upgrades
// continue to work — without this Upgrade() would fail for routes wrapped by
// the middleware.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func normalizePath(p string) string {
	p = pathUUIDRe.ReplaceAllString(p, "/{id}")
	p = pathNumRe.ReplaceAllString(p, "/{n}")
	// Trim long paths so the LowCardinality column stays bounded. 200 chars
	// is enough for any sensible REST path; pathological inputs get truncated.
	if len(p) > 200 {
		p = p[:200]
	}
	return p
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func isHexID(s string, expectedLen int) bool {
	if len(s) != expectedLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// uploadReadDeadline is how long a streaming request gets to finish reading
// its body. 30 minutes covers a 100 MiB transfer on a 50 KiB/s 3G link;
// anything slower than that is a stuck client we'd rather drop.
const uploadReadDeadline = 30 * time.Minute

// uploadWriteDeadline bounds the response phase — for the admin media read,
// the window to stream a large object out of S3 to a slow client. Same
// envelope as read.
const uploadWriteDeadline = 30 * time.Minute

// csrfSkipPaths lists exact path matches that bypass csrf enforcement.
var csrfSkipPaths = map[string]bool{
	// Admin refresh rotates the csrf cookie itself, so it cannot require the
	// previous one. Its own gate is the Ed25519 device challenge.
	"/api/auth/admin/refresh": true,
	// Browsers cannot attach our cookies to a violation report.
	"/api/csp-report": true,
	// Public client support report submission.
	"/api/v1/support/report": true,
}

// csrfSkipPrefixes lists path prefixes that bypass csrf enforcement.
// Used for routes that namespace under a fixed root (probes etc).
var csrfSkipPrefixes = []string{
	"/api/v1/internal/", // internal probes — bearer-token authed
	// Provider webhook callbacks. Third-party services (Unisender Go,
	// future Stripe etc.) can't carry our CSRF cookie/header — each
	// handler under this prefix MUST verify its own per-provider auth
	// (HMAC, signed body, MD5-of-substituted-body, etc.).
	"/api/v1/webhooks/",
	// Alerting silence API — bearer-token authed (ALERT_SILENCE_TOKEN).
	// No cookie path is available; the bearer token in the handler is the
	// gate.
	"/api/admin/alerting/",
}

// uploadPathPrefixes bypass the body cap and the default Read/Write deadlines
// — they stream large payloads or large responses out of S3.
//
// One entry is left, and it is a READ: the admin's fetch of a ticket
// attachment. The three write prefixes that used to stand beside it
// (`/api/upload`, `/api/uploads`, `/api/auth/avatar`) named routes that no
// longer exist — bytes reach S3 through a presigned PUT the client gets on
// the wire, so nothing large is POSTed to this mux any more.
var uploadPathPrefixes = []string{
	"/api/v1/admin/media",
}

var (
	pathUUIDRe = regexp.MustCompile(`/[0-9a-f]{8}-[0-9a-f-]+`)
	pathNumRe  = regexp.MustCompile(`/\d+`)
)
