package entry

// admin_vantages.go is the operator surface for observation point slots
// (`entry_vantages`, migration 068, roadmap 42 К3′ / П9.12).
//
// # Why every route that issues a token sits behind the step-up
//
// An observation point is who judges the fleet, and its token is its voice:
// whoever holds it decides which verdicts reach the detector, can claim the
// hub's probe tasks, and fetches the dialling credentials of the whole pool.
// So opening a slot, putting another machine into it, reissuing its token and
// retiring it all require a fresh passkey touch (`stepUp`), just like
// `PUT /api/v1/admin/entry/policy`. Only the list is behind the owner gate,
// and it never carries a token.
//
// # Two ways a token reaches its prober
//
// A slot on a box the hub can reach (one of `entry_boxes` without relays,
// vantages.go) gets its token from DeployProber, which plants it in the box's
// prober.env over ssh: the answer carries a job and no token, and the owner
// never sees it. A slot on a standalone VPS gets the token in the one answer
// that issued it, with the env file the prober reads; the hub keeps only its
// hash and can never show it again. Reissuing is the same split: a box slot
// is redeployed, a standalone one gets a new token to carry by hand. Either
// way the old token stops working.
//
// Tokens are never logged: not in applog, not in the audit journal (which
// records the fact and the slot), not in a job's log.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
)

// vantageAnswer is a slot as a token-issuing route answers it: Env is the
// prober.env for a standalone slot whose token is in this answer, and
// DeployError says why a box slot's prober was not deployed (the slot itself
// was saved).
type vantageAnswer struct {
	Vantage
	Env         string `json:"env,omitempty"`
	DeployError string `json:"deployError,omitempty"`
}

// handOver gets a freshly saved slot's token to its prober. A slot on a box:
// the hub deploys the prober with a token of its own and the one in v is
// dropped — it is already void, and the owner is not meant to see it. A
// standalone slot keeps the token in the answer, with the env to go with it.
func (h *AdminHandlers) handOver(ctx context.Context, v Vantage, adminID string) vantageAnswer {
	if v.Box == "" {
		a := vantageAnswer{Vantage: v}
		if v.Token != "" && h.prov != nil && h.prov.proberAPIURL != "" {
			a.Env = proberEnv(h.prov.proberAPIURL, v.Slot, v.Token, "")
		}
		return a
	}
	v.Token = ""
	a := vantageAnswer{Vantage: v}
	if h.prov == nil || !h.prov.CanDeployProber() {
		a.DeployError = "хаб не знает своего адреса или в образе нет бинаря пробника"
		return a
	}
	jobID, err := h.prov.DeployProber(ctx, v.Box, v.Slot, adminID)
	if err != nil {
		a.DeployError = deployFailure(ctx, err)
		return a
	}
	a.JobID = &jobID
	return a
}

// deployFailure is what the screen may say about a deploy that did not start:
// the hub's own refusals verbatim, anything else only by name.
func deployFailure(ctx context.Context, err error) string {
	if errors.Is(err, ErrRejected) || errors.Is(err, ErrNoSuchBox) || errors.Is(err, ErrBoxRetired) || errors.Is(err, ErrBoxBusy) {
		return err.Error()
	}
	applog.Error(ctx, "entry", "prober deploy failed to start", applog.F("err", err.Error()))
	return "развёртывание не запустилось"
}

// writeVantageAnswer sends an answer that may carry a token: never cached.
func writeVantageAnswer(w http.ResponseWriter, a vantageAnswer) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, a)
}

// VantagesHandler — GET /api/v1/admin/entry/vantages: the registered slots and
// the machine inside each, with sealed hosts opened for the owner.
func (h *AdminHandlers) VantagesHandler(w http.ResponseWriter, r *http.Request) {
	slots, err := h.svc.VantageSlots(r.Context())
	if h.refused(w, err) {
		return
	}
	if slots == nil {
		slots = []Vantage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"vantages": slots})
}

// CreateVantageHandler — POST /api/v1/admin/entry/vantages: open a new slot
// (or re-open a retired one). Replacing the machine in an already active slot
// is refused here and must go through PUT /api/v1/admin/entry/vantages/{slot}.
func (h *AdminHandlers) CreateVantageHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var spec VantageSpec
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	v, err := h.svc.CreateVantage(r.Context(), spec, actor.ID)
	if h.refused(w, err) {
		return
	}
	a := h.handOver(r.Context(), v, actor.ID)
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.vantage.create",
		TargetType: "entry_vantage",
		TargetID:   a.Slot,
		Payload: map[string]any{
			"title":        a.Title,
			"box":          a.Box,
			"provider":     a.Provider,
			"host_given":   spec.Host != "",
			"job_id":       a.JobID,
			"token_shown":  a.Token != "",
			"deploy_error": a.DeployError != "",
		},
	})
	writeVantageAnswer(w, a)
}

// SaveVantageHandler — PUT /api/v1/admin/entry/vantages/{slot}: put or replace
// the machine in a slot. Behind step-up.
func (h *AdminHandlers) SaveVantageHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var spec VantageSpec
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if slot := r.PathValue("slot"); slot != "" {
		spec.Slot = slot
	}
	prev, _ := h.svc.VantageSlot(r.Context(), spec.Slot)
	v, err := h.svc.SaveVantage(r.Context(), spec, actor.ID)
	if h.refused(w, err) {
		return
	}
	if prev.Box != "" && prev.Box != v.Box && h.prov != nil {
		if used, err := h.svc.boxUsedByActiveVantage(r.Context(), prev.Box); err == nil && !used {
			h.prov.StopProber(prev.Box)
		}
	}
	// The same machine kept (a title edit): its prober runs with the token
	// it has, and a redeploy would only rotate it (audit 42 recheck НН2).
	var a vantageAnswer
	if v.Box != "" && prev.Box == v.Box && prev.Status == VantageActive {
		v.Token = ""
		a = vantageAnswer{Vantage: v}
	} else {
		a = h.handOver(r.Context(), v, actor.ID)
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.vantage.save",
		TargetType: "entry_vantage",
		TargetID:   a.Slot,
		Payload: map[string]any{
			"title":        a.Title,
			"box":          a.Box,
			"provider":     a.Provider,
			"host_given":   spec.Host != "",
			"job_id":       a.JobID,
			"token_shown":  a.Token != "",
			"deploy_error": a.DeployError != "",
		},
	})
	writeVantageAnswer(w, a)
}

// ReissueVantageTokenHandler — POST /api/v1/admin/entry/vantages/{slot}/token:
// a new token for an active slot; the old one stops working. A slot on a box
// is redeployed (the answer carries the job), a standalone slot gets the
// token once in the answer. Behind step-up.
func (h *AdminHandlers) ReissueVantageTokenHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	v, err := h.svc.VantageSlot(r.Context(), r.PathValue("slot"))
	if h.refused(w, err) {
		return
	}
	if v.Status != VantageActive {
		h.refused(w, ErrNoSuchVantage)
		return
	}
	a := vantageAnswer{Vantage: v}
	if v.Box != "" {
		if h.prov == nil || !h.prov.CanDeployProber() {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "хаб не знает своего адреса или в образе нет бинаря пробника"})
			return
		}
		jobID, err := h.prov.DeployProber(r.Context(), v.Box, v.Slot, actor.ID)
		if h.refused(w, err) {
			return
		}
		a.JobID = &jobID
	} else {
		if v.Token, err = h.svc.IssueVantageToken(r.Context(), v.Slot); h.refused(w, err) {
			return
		}
		a = h.handOver(r.Context(), v, actor.ID)
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.vantage.token",
		TargetType: "entry_vantage",
		TargetID:   a.Slot,
		Payload: map[string]any{
			"box":         a.Box,
			"job_id":      a.JobID,
			"token_shown": a.Token != "",
		},
	})
	writeVantageAnswer(w, a)
}

// RetireVantageHandler — POST /api/v1/admin/entry/vantages/{slot}/retire and
// DELETE /api/v1/admin/entry/vantages/{slot}: retire a slot. Behind step-up.
func (h *AdminHandlers) RetireVantageHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	slot := r.PathValue("slot")
	v, err := h.svc.RetireVantage(r.Context(), slot, actor.ID)
	if h.refused(w, err) {
		return
	}
	if v.Box != "" && h.prov != nil {
		if used, err := h.svc.boxUsedByActiveVantage(r.Context(), v.Box); err == nil && !used {
			h.prov.StopProber(v.Box)
		}
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.vantage.retire",
		TargetType: "entry_vantage",
		TargetID:   v.Slot,
		Payload: map[string]any{
			"retiredAt": v.RetiredAt,
		},
	})
	writeJSON(w, http.StatusOK, v)
}
