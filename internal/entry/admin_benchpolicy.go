package entry

// admin_benchpolicy.go is the canary's knobs (roadmap 42 П6.14): read what a
// build is judged by, and turn one.
//
// # Why these are a person's knobs and not constants
//
// A rollout that stalls stalls in front of the owner, who is looking at two
// numbers — what the canary measured and what it demanded — and deciding
// whether the build is bad or the hour was. While those lived in Go that
// decision cost a rebuild and a deploy of the hub.
//
// # Why saving is behind the step-up
//
// Loosening the throughput threshold lets a slower build onto every entry
// point in the fleet, and the cost of that is paid by every person whose call
// goes through one. Same class as the detection knobs next door.

import (
	"encoding/json"
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
)

// BenchPolicyHandler — GET /api/v1/admin/entry/bench-policy.
func (h *AdminHandlers) BenchPolicyHandler(w http.ResponseWriter, r *http.Request) {
	policy, err := h.svc.BenchPolicy(r.Context())
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, benchPolicyResponse(policy))
}

// SaveBenchPolicyHandler — PUT /api/v1/admin/entry/bench-policy. Body is a
// patch: only the knobs that moved.
func (h *AdminHandlers) SaveBenchPolicyHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var patch BenchPolicyPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	policy, err := h.svc.SaveBenchPolicy(r.Context(), patch, actor.ID)
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.bench_policy.save",
		TargetType: "entry_bench_policy",
		TargetID:   "bench-policy",
		Payload: map[string]any{
			"minThroughputPct": policy.MinThroughputPct(),
			"maxRttGrowthPct":  policy.MaxRTTGrowthPct(),
			"pairs":            policy.Pairs,
			"runSeconds":       policy.RunSeconds(),
		},
	})
	writeJSON(w, http.StatusOK, benchPolicyResponse(policy))
}

// benchPolicyResponse is the shape both handlers return, so a save and a
// reload cannot disagree about it.
//
// `benchingSeconds` is computed here rather than in the screen: how long a
// canary will now spend measuring is the consequence of the `pairs` knob, and
// a person turning it should read that consequence off the same answer that
// carries the knob.
func benchPolicyResponse(p BenchPolicy) map[string]any {
	return map[string]any{
		"minThroughputPct": p.MinThroughputPct(),
		"maxRttGrowthPct":  p.MaxRTTGrowthPct(),
		"pairs":            p.Pairs,
		"runSeconds":       p.RunSeconds(),
		"benchingSeconds":  int(p.Benching().Seconds()),
		"updatedAt":        p.UpdatedAt,
		"updatedBy":        p.UpdatedBy,
	}
}
