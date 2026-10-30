package entry

// admin_incidents.go is the console's two buttons on an incident card
// (roadmap 42 П9.6): run the repair the hub proposed, or say it is not worth
// acting on.
//
// # Why only two
//
// The overview already answers what happened — the card's own text, cause,
// advice and playbook ride in GET /entry/overview. What was missing was any
// way to answer back. Until this file the console could show "сменить домен,
// тратит деньги" and nothing more; the cross on a queue card was deliberately
// left out of П9.5 for the same reason, since a cross that only hides a row
// locally is a lie the next reload uncovers.
//
// # Running is behind a fresh passkey touch, dismissing is not
//
// The repairs that wait for a person are exactly the ones that spend money or
// hand a machine back (playbooks.go), so the button that starts one is what a
// stolen session must not be able to press quietly. Dismissing costs nothing
// and is reversible by the detector: the cause is still true, so the next pass
// opens the incident again.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
)

// dismissBody is the optional note a person leaves when closing an incident by
// hand.
type dismissBody struct {
	Outcome string `json:"outcome"`
}

// runBody optionally names a repair other than the proposed one — the "another
// action" button on an incident card (К2). Absent or empty means the proposal.
type runBody struct {
	Playbook string `json:"playbook"`
}

// RunIncidentRepair — POST /api/v1/admin/entry/incidents/{id}/run. Body:
// {"playbook"} (optional). Carry out a repair against this incident because a
// person said so — the one the dispatcher proposed, or the one they named
// instead.
//
// The rails still apply and a refusal says which one stopped it: a person
// pressing the button knows no more than the hub about whether this is the
// third machine of an hour.
//
// A named playbook must be one the hub has code for (К2 §4). The console shows
// the menu; the hub does not execute what it was handed.
func (h *AdminHandlers) RunIncidentRepair(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	var body runBody
	// An empty or absent body is the ordinary case — the proposed repair.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload)).Decode(&body)
	// Built per request: a mechanic is the service and the provisioner behind
	// one method set, with every bit of its state in Postgres, so there is
	// nothing to share with the hub's own repair loop and nothing to keep.
	if err := NewMechanic(h.svc, h.prov).Start(r.Context(), id, actor.ID, clip(body.Playbook, 64)); err != nil {
		if errors.Is(err, ErrNoSuchIncident) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such incident"})
			return
		}
		if h.refused(w, err) {
			return
		}
	}
	inc, err := h.svc.Incident(r.Context(), id)
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.incident.repair",
		TargetType: "entry_incident",
		TargetID:   id.String(),
		Payload: map[string]any{
			"box": inc.Box, "cause": inc.Cause, "playbook": inc.Playbook,
			// What was asked for, beside what ran: an operator overruling the
			// dispatcher is the thing an audit trail must not blur.
			"asked": body.Playbook,
		},
	})
	writeJSON(w, http.StatusOK, map[string]any{"incident": inc})
}

// DismissIncident — POST /api/v1/admin/entry/incidents/{id}/dismiss. Body:
// {"outcome"} (optional). The cross on a queue card.
func (h *AdminHandlers) DismissIncident(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	var body dismissBody
	// An empty or absent body is the ordinary case — the cross sends none.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload)).Decode(&body)
	if err := h.svc.Dismiss(r.Context(), id, clip(body.Outcome, 512)); err != nil {
		if errors.Is(err, ErrNoSuchIncident) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such open incident"})
			return
		}
		if h.refused(w, err) {
			return
		}
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.incident.dismiss",
		TargetType: "entry_incident",
		TargetID:   id.String(),
		Payload:    map[string]any{"outcome": body.Outcome},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// IncidentRepairs — GET /api/v1/admin/entry/incidents/{id}/repairs: what was
// tried against this incident, oldest first. The answer to "why did the hub
// give up on it", which the card's handoff line only summarises.
func (h *AdminHandlers) IncidentRepairs(w http.ResponseWriter, r *http.Request) {
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	repairs, err := h.svc.Repairs(r.Context(), id)
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repairs": repairs})
}

func incidentID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is not a uuid"})
		return uuid.Nil, false
	}
	return id, true
}

// CausePauses — GET /api/v1/admin/entry/pauses: the wave fuses standing right
// now, oldest first.
//
// Its own call rather than a field on the incident feed: a pause is not an
// incident and does not belong in a list of them (К2 §4). What it is, is the
// answer to "why are there three of these and nobody working on them".
func (h *AdminHandlers) CausePauses(w http.ResponseWriter, r *http.Request) {
	pauses, err := h.svc.Pauses(r.Context())
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pauses": pauses, "playbooks": Playbooks()})
}

// LiftCausePause — POST /api/v1/admin/entry/pauses/{cause}/lift: a person
// looked at the wave and tells the autopilot to carry on.
//
// Behind the step-up for the same reason running a repair is: lifting a pause
// on `domain_blocked` is what lets the hub start buying domains again, and the
// fuse exists precisely because that is the expensive mistake to make
// automatically.
func (h *AdminHandlers) LiftCausePause(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	cause := r.PathValue("cause")
	freed, err := h.svc.LiftPause(r.Context(), cause, actor.ID)
	if errors.Is(err, ErrNoSuchPause) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no standing pause on this cause"})
		return
	}
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.pause.lift",
		TargetType: "entry_cause_pause",
		TargetID:   cause,
		Payload:    map[string]any{"freed": freed},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "freed": freed})
}
