package entry

// admin_policy.go is the detection screen's knobs (roadmap 42 К3): read the
// eviction thresholds, and turn one.
//
// # Why this file acts and entry_pressure.go only counts
//
// The console's rule is that a screen which counts does not act. The ban
// tempo, the waves and the vantage count are analytics and live in
// internal/admin; the thresholds are policy and are written here, beside the
// other buttons the hub gives a person.
//
// # Why saving is behind the step-up
//
// Lowering the strike count makes the autopilot retire machines sooner,
// widening the wave window makes it keep repairing through a rule change at
// the censor, and shortening a second chance hands machines back sooner. Both spend money without anybody pressing a buy button, which is
// exactly the class of action the fresh-passkey gate exists for.

import (
	"encoding/json"
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
)

// DetectionPolicyHandler — GET /api/v1/admin/entry/policy: the eviction
// thresholds as they stand, with the vantage grid they are read against.
//
// The vantage list rides along because one knob is unanswerable without it:
// "confirm a ban from two observation points" is a rule the hub can only
// honour when it has two, and the screen has to say which it is instead of
// drawing a switch that does nothing.
func (h *AdminHandlers) DetectionPolicyHandler(w http.ResponseWriter, r *http.Request) {
	policy, err := h.svc.DetectionPolicy(r.Context())
	if h.refused(w, err) {
		return
	}
	vantages, err := h.svc.Vantages(r.Context())
	if h.refused(w, err) {
		return
	}
	if vantages == nil {
		vantages = []string{}
	}
	writeJSON(w, http.StatusOK, policyResponse(policy, vantages))
}

// SaveDetectionPolicyHandler — PUT /api/v1/admin/entry/policy. Body is a
// patch: only the knobs that moved.
//
// A refusal is the operator's answer, not an error page: the bounds live in
// migration 065, and the two unanswerable switches (two vantages, ignoring the
// vantage's own outage) are refused with the reason, which is what the screen
// shows under them.
func (h *AdminHandlers) SaveDetectionPolicyHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var patch DetectionPolicyPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	policy, err := h.svc.SaveDetectionPolicy(r.Context(), patch, actor.ID)
	if h.refused(w, err) {
		return
	}
	vantages, err := h.svc.Vantages(r.Context())
	if h.refused(w, err) {
		return
	}
	if vantages == nil {
		vantages = []string{}
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.policy.save",
		TargetType: "entry_detection_policy",
		TargetID:   "policy",
		Payload: map[string]any{
			"failStrikes":           policy.FailStrikes,
			"waveLimit":             policy.WaveLimit,
			"waveWindowSeconds":     policy.WaveWindowSeconds(),
			"verdictWindowSeconds":  policy.VerdictWindowSeconds(),
			"repairCooldownSeconds": policy.RepairCooldownSeconds(),
			"requireTwoVantages":    policy.RequireTwoVantages,
			"secondChance":          secondChanceResponse(policy),
		},
	})
	writeJSON(w, http.StatusOK, policyResponse(policy, vantages))
}

// policyResponse is the shape both handlers return, so a save and a reload
// cannot disagree about it.
//
// Durations go out as seconds: a screen with a stepper on "окно волны" needs a
// number to step, and "1h0m0s" is not one.
func policyResponse(p DetectionPolicy, vantages []string) map[string]any {
	return map[string]any{
		"failStrikes":           p.FailStrikes,
		"waveLimit":             p.WaveLimit,
		"waveWindowSeconds":     p.WaveWindowSeconds(),
		"verdictWindowSeconds":  p.VerdictWindowSeconds(),
		"repairCooldownSeconds": p.RepairCooldownSeconds(),
		"requireTwoVantages":    p.RequireTwoVantages,
		"ignoreNetworkDown":     p.IgnoreNetworkDown,
		// The second step of each consumable's life (П9.11): how a suspect
		// is reprobed, returned and written off, per kind.
		"secondChance": secondChanceResponse(p),
		"updatedAt":    p.UpdatedAt,
		"updatedBy":    p.UpdatedBy,
		// The grid the policy is read against, and why one of its knobs is
		// locked. Both are facts, not advice: the screen renders them as they
		// come.
		"vantages": vantages,
		"locked": map[string]string{
			"ignoreNetworkDown": "пробник без сети не судит машину: выключенный, этот переключатель сдавал бы живые машины во время нашей же аварии",
		},
	}
}

// secondChanceResponse is the second chance's knobs by kind.
func secondChanceResponse(p DetectionPolicy) map[string]map[string]int {
	out := make(map[string]map[string]int, len(SuspectKinds))
	for _, kind := range SuspectKinds {
		out[kind] = secondChanceWire(p.Chance(kind))
	}
	return out
}

// SuspectsHandler — GET /api/v1/admin/entry/suspects: the second chance's
// ledger — every suspect now, then what was returned or written off in the
// last thirty days. Beside the knobs that decide it, so a person turning one
// sees what it would move.
func (h *AdminHandlers) SuspectsHandler(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Suspects(r.Context(), 200)
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suspects": list})
}
