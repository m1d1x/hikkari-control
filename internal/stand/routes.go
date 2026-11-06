package stand

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/relayruns"
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
	// The fleet's links for a claimed run, in the subscription format (base64
	// of the links, one per line), so the point reads them with the client's
	// own fetcher and parser. Not behind the point's bearer: the parser fetches
	// a bare URL. The path's token is the credential, good only while its run
	// is claimed; anything else gets the same 404.
	mux.HandleFunc("GET "+LinksPath+"{token}", func(w http.ResponseWriter, r *http.Request) {
		links, err := s.FleetLinks(r.Context(), r.PathValue("token"))
		if errors.Is(err, ErrNoSuchLinks) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			applog.Error(r.Context(), "stand", "fleet links failed", applog.F("err", err.Error()))
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))))
	})
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

// RegisterKnobOpsRoutes mounts the agent's view of the knob controller: its
// policy, every live node's values and the journal of the last days.
func RegisterKnobOpsRoutes(mux *http.ServeMux, c *Controller, runs *relayruns.Store, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/knobs", gate("", func(w http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days <= 0 || days > 30 {
			days = 7
		}
		p, err := c.PolicyNow(r.Context())
		if err == nil {
			var nodes []NodeKnobs
			nodes, err = c.Nodes(r.Context())
			if err == nil {
				var events []relayruns.KnobEvent
				events, err = runs.KnobEvents(r.Context(), r.URL.Query().Get("profile"), "", time.Now().AddDate(0, 0, -days), 0)
				if err == nil {
					writeJSON(w, http.StatusOK, map[string]any{
						"policy": map[string]any{"mode": p.Mode, "window": p.Window.String(), "min_rounds": p.MinRounds,
							"low": p.Low, "high": p.High, "margin": p.Margin, "cooldown": p.Cooldown.String(), "control_slots": p.Control},
						"nodes": nodes, "events": events,
					})
					return
				}
			}
		}
		applog.Error(r.Context(), "knobs", "ops view failed", applog.F("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
	}))
}
