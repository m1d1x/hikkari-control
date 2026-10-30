package main

import (
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/config"
)

// securityHeadersMiddleware sets baseline response headers that block common
// data-exfiltration vectors (MIME sniffing, clickjacking, full Referer leaks).
// HSTS is only emitted when the request actually arrived over TLS so local
// http://localhost dev sessions don't pin the browser to HTTPS.
//
// Content-Security-Policy is set separately by middleware.CSPMiddleware so
// the policy can be tuned (and toggled report-only/enforce) without touching
// the rest of the security headers.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	allowedOrigins := map[string]bool{}
	for _, o := range config.Default().AllBrowserOrigins() {
		allowedOrigins[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-CSRF-Token")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware allows cross-origin requests from our own browser surfaces:
// the landing on the apex, the web app host, and the admin panel. The list is
// config.Domain.AllBrowserOrigins() — derived from PUBLIC_BASE_DOMAIN, with dev
// servers added through PUBLIC_WEB_EXTRA_ORIGINS / PUBLIC_ADMIN_EXTRA_ORIGINS
// (compose.override.yml sets both). A `http://localhost:5174` literal used to
// live here, which put a dev origin in the production allowlist and hid the env
// var that already existed for it.
//
// Anything not on the list gets no Access-Control-Allow-Origin at all, which is
// the fail-closed shape: the request is still served (same-origin and non-browser
// callers are unaffected), the browser just refuses to read the response.
//
// corsAllowedMethods must name every method a route is registered under. The
// list once lacked PATCH, so every PATCH from the admin panel (its own origin,
// :5174) died in the preflight as a bare «Failed to fetch» while curl and the
// tests — same-origin, no preflight — saw the route work.
const corsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
