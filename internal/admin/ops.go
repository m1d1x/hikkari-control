package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// RequireOps is the gate in front of /api/v1/ops/*: a bearer ops token, checked
// per request so a revoke takes effect at once. The token means nothing to
// RequireAdmin and an admin cookie means nothing here, so what the agent may do
// is exactly the routes mounted behind this gate: no machines, limits or
// subscriptions are among them.
//
// A non-empty action writes an audit row before the handler runs; the empty
// one is for lists and polls.
func RequireOps(pool db.Pool) func(action string, next http.HandlerFunc) http.HandlerFunc {
	return func(action string, next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			label, err := auth.CheckOpsToken(r.Context(), pool, token)
			if err != nil {
				if !errors.Is(err, auth.ErrOpsTokenInvalid) {
					applog.Error(r.Context(), "admin", "ops gate: "+err.Error())
				}
				writeError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			if action != "" {
				audit.Log(r.Context(), pool, r, audit.Entry{
					Action:     action,
					TargetType: "tester_install",
					TargetID:   r.PathValue("install"),
					Payload:    map[string]any{"token": label, "query": r.URL.RawQuery},
				})
			}
			next.ServeHTTP(w, r)
		}
	}
}

// RegisterOpsRoutes mounts the product reads the agent may use.
func RegisterOpsRoutes(mux *http.ServeMux, h *Handlers, gate func(action string, next http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/errors", gate("", h.ProductErrors))
}
