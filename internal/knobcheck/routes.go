package knobcheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// Resolver turns a tester reference (tester-003, 003, an install id or a
// prefix) into the install id.
type Resolver func(ctx context.Context, ref string) (string, error)

// RegisterOpsRoutes mounts the agent's view of the checks: per round, the
// profile, the network, what the hub handed out, what the config held, and when.
func RegisterOpsRoutes(mux *http.ServeMux, s *Store, resolve Resolver, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/knob-check", gate("", func(w http.ResponseWriter, r *http.Request) {
		q := Query{Profile: r.URL.Query().Get("profile"), Mismatch: r.URL.Query().Get("mismatch") == "1"}
		q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
		span := 24 * time.Hour
		if v := r.URL.Query().Get("last"); v != "" {
			d, err := parseSpan(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "last: 6h, 24h, 7d"})
				return
			}
			span = d
		}
		q.Since = time.Now().Add(-span)
		if who := r.URL.Query().Get("who"); who != "" {
			id, err := resolve(r.Context(), who)
			switch {
			case errors.Is(err, support.ErrAliasNotFound):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "install not found"})
				return
			case errors.Is(err, support.ErrAmbiguousInstall):
				writeJSON(w, http.StatusConflict, map[string]string{"error": "reference matches several installs — be more specific"})
				return
			case err != nil:
				applog.Error(r.Context(), "knobcheck", "resolve failed", applog.F("err", err.Error()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			q.Install = id
		}
		checks, err := s.Checks(r.Context(), q)
		if err != nil {
			applog.Error(r.Context(), "knobcheck", "read failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
	}))
	mux.HandleFunc("GET /api/v1/ops/edge-hellos", gate("", func(w http.ResponseWriter, r *http.Request) {
		span := 24 * time.Hour
		if v := r.URL.Query().Get("last"); v != "" {
			d, err := parseSpan(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "last: 6h, 24h, 7d"})
				return
			}
			span = d
		}
		nodes, err := s.EdgeHellos(r.Context(), r.URL.Query().Get("relay"), time.Now().Add(-span))
		if err != nil {
			applog.Error(r.Context(), "knobcheck", "edge hellos failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
	}))
}

// parseSpan reads 30m, 6h or 7d.
func parseSpan(v string) (time.Duration, error) {
	if n := len(v); n > 1 && v[n-1] == 'd' {
		days, err := strconv.Atoi(v[:n-1])
		if err != nil || days <= 0 || days > 90 {
			return 0, errors.New("bad span")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > 90*24*time.Hour {
		return 0, errors.New("bad span")
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
