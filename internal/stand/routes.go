package stand

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// maxResultBytes bounds a finish body: maxTaskRows rows of a few hundred bytes.
const maxResultBytes = 2 << 20

// RegisterProberRoutes mounts the points' side of the stand beside the other
// prober routes (internal/entry prober_api.go). gate lets through a bearer of
// an active slot; slotOf is the slot it let the request through as.
func RegisterProberRoutes(mux *http.ServeMux, s *Scheduler, gate func(http.HandlerFunc) http.HandlerFunc, slotOf func(*http.Request) string) {
	// The point's oldest queued run, or 204.
	mux.HandleFunc("POST /api/v1/internal/entry-probes/stand/claim", gate(func(w http.ResponseWriter, r *http.Request) {
		task, err := s.Claim(r.Context(), slotOf(r))
		if err != nil {
			applog.Error(r.Context(), "stand", "claim failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		if task == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, task)
	}))
	// A run's rows, and why it stopped short.
	mux.HandleFunc("POST /api/v1/internal/entry-probes/stand/{id}", gate(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such task"})
			return
		}
		var res Result
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResultBytes)).Decode(&res); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not {\"core\", \"rows\", \"error\"}"})
			return
		}
		added, err := s.Finish(r.Context(), slotOf(r), id, res)
		switch {
		case errors.Is(err, ErrNoSuchTask):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrNotAResult):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case err != nil:
			applog.Error(r.Context(), "stand", "finish failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		default:
			writeJSON(w, http.StatusOK, map[string]int{"added": added})
		}
	}))
}

// RegisterOpsRoutes mounts the agent's view of the stand: what it measures
// and its latest runs. Reading only; the plan is changed in the database.
func RegisterOpsRoutes(mux *http.ServeMux, s *Scheduler, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/stand", gate("", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		tasks, err := s.Recent(r.Context(), limit)
		if err != nil {
			applog.Error(r.Context(), "stand", "recent failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		st, knobs, err := s.Plan(r.Context())
		if err != nil {
			applog.Error(r.Context(), "stand", "plan failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": st, "knobs": knobs, "tasks": tasks})
	}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
