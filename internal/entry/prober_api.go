package entry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// regionRe is the shape of a slot key (entry_vantages.slot).
var regionRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// The vantages' side of the pool (cmd/entry-prober, roadmap 42 П6.6, П6.15):
// what to watch, and the admission probes to run. Server-to-server behind the
// observation point's own token (vantages.go): the slot a request speaks for
// is the one its token belongs to, and nothing in the request can say
// otherwise.

// VantageAuth answers which active observation point a bearer token belongs
// to (ErrNoSuchVantage for none). *Service is the one in production; a test
// that serves the bench routes without a database brings its own.
type VantageAuth interface {
	VantageByToken(ctx context.Context, token string) (string, error)
}

type vantageKey struct{}

// vantageOf is the slot vantageGate let the request through as.
func vantageOf(r *http.Request) string {
	slot, _ := r.Context().Value(vantageKey{}).(string)
	return slot
}

// vantageGate is the check every prober route sits behind: a bearer that
// belongs to an active slot, or 401. A hub that cannot look the token up
// answers 503 rather than letting anyone in.
func vantageGate(auth VantageAuth) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			slot, err := auth.VantageByToken(r.Context(), token)
			switch {
			case errors.Is(err, ErrNoSuchVantage):
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			case err != nil:
				applog.Error(r.Context(), "entry", "vantage token lookup failed", applog.F("err", err.Error()))
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "probing is unavailable"})
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), vantageKey{}, slot)))
		}
	}
}

// RegisterProberRoutes mounts the prober routes on the public mux.
func RegisterProberRoutes(mux *http.ServeMux, svc *Service) {
	gate := vantageGate(svc)
	// The pool to watch on every routine round.
	mux.HandleFunc("GET /api/v1/internal/entry-probes/relays", gate(func(w http.ResponseWriter, r *http.Request) {
		relays, err := svc.WatchedProbeRelays(r.Context())
		if err != nil {
			applog.Error(r.Context(), "entry", "prober relays failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, relays)
	}))
	// The oldest queued task of the kind asked for (admission when unsaid),
	// or 204. Admission probes, benches and the reprobes of the second chance
	// (domains, machines) are claimed by separate loops: a ten-minute bench must not hold up a box waiting for
	// its admission.
	mux.HandleFunc("POST /api/v1/internal/entry-probes/tasks/claim", gate(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Kind string `json:"kind"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not {\"kind\"}"})
			return
		}
		if body.Kind == "" {
			body.Kind = TaskAdmission
		}
		if body.Kind != TaskAdmission && body.Kind != TaskBench && body.Kind != TaskDomain && body.Kind != TaskBox {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind is admission, bench, domain or box"})
			return
		}
		task, err := svc.ClaimProbeTask(r.Context(), vantageOf(r), body.Kind)
		if errors.Is(err, ErrRejected) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			applog.Error(r.Context(), "entry", "claim probe task failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		if task == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, task)
	}))
	// The verdicts of a claimed probe, and a bench's result.
	mux.HandleFunc("POST /api/v1/internal/entry-probes/tasks/{id}", gate(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such task"})
			return
		}
		var body struct {
			Verdicts map[string]string `json:"verdicts"`
			Result   *BenchResult      `json:"result"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body); err != nil || len(body.Verdicts) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not {\"verdicts\": {...}}"})
			return
		}
		err = svc.FinishProbeTask(r.Context(), id, vantageOf(r), body.Verdicts, body.Result)
		switch {
		case errors.Is(err, ErrNotAnAnswer):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrRejected):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case err != nil:
			applog.Error(r.Context(), "entry", "finish probe task failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
}
