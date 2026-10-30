package entry

// admin_autopilot.go is the autopilot screen (roadmap 42 П9.10, П9.19): the
// money caps — the owner's one knob — the mode of each repair as a brake, the
// fleet as the hub sizes it, and its charts. The eviction thresholds on the
// same screen are admin_policy.go's.
//
// # Why the saves are behind the step-up
//
// Raising a cap, and moving a repair that spends money back to `auto`, are
// each a purchase made later by the hub with nobody pressing a buy button —
// the class of action the fresh-passkey gate exists for. Reading them is
// not.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
)

// knobBodyLimit bounds what a knob's save reads: one field and a value. The
// table's CHECK was the only bound before (audit mine), after the whole body
// had been read.
const knobBodyLimit = 4 << 10

// playbookModeRow is one cause as the modes panel lists it.
type playbookModeRow struct {
	Cause       string `json:"cause"`
	Playbook    string `json:"playbook"`
	Mode        string `json:"mode"`
	DefaultMode string `json:"defaultMode"`
	Spends      bool   `json:"spends"`
	Why         string `json:"why"`
}

// PlaybookModesHandler — GET /api/v1/admin/entry/playbook-modes: every cause
// the hub has a repair for, the mode it acts in now and the code's default.
func (h *AdminHandlers) PlaybookModesHandler(w http.ResponseWriter, r *http.Request) {
	modes, err := LoadPlaybookModes(r.Context(), h.svc.pool)
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modes": playbookModeRows(modes)})
}

// playbookModeRows lists the dispatcher's table with the owner's modes
// applied, by cause.
func playbookModeRows(modes PlaybookModes) []playbookModeRow {
	out := make([]playbookModeRow, 0, len(playbookFor))
	for cause, def := range playbookFor {
		if def.Name == "" {
			continue
		}
		p := modes.For(cause)
		out = append(out, playbookModeRow{
			Cause: cause, Playbook: p.Name, Mode: p.Mode, DefaultMode: def.Mode,
			Spends: p.Spends, Why: p.Why,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cause < out[j].Cause })
	return out
}

// SavePlaybookModeHandler — PUT /api/v1/admin/entry/playbook-modes/{cause}.
// Body {"mode": "auto" | "ask" | "off"}. Answers with the whole table, so the
// screen redraws from what the hub now holds.
func (h *AdminHandlers) SavePlaybookModeHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, knobBodyLimit)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	cause := r.PathValue("cause")
	if h.refused(w, h.svc.SetPlaybookMode(r.Context(), cause, body.Mode, actor.ID)) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.playbook_mode.save",
		TargetType: "entry_playbook_mode", TargetID: cause,
		Payload: map[string]any{"mode": body.Mode},
	})
	h.PlaybookModesHandler(w, r)
}

// fleetPoolRow is one pool as the fleet panel shows it.
type fleetPoolRow struct {
	FleetForecast
	Proto          string `json:"proto"`
	VoiceRegion    string `json:"voiceRegion,omitempty"`
	HorizonSeconds int    `json:"horizonSeconds"`
}

// FleetHandler — GET /api/v1/admin/entry/fleet: every pool with the target
// its load asks for (П9.16) and what that was read from, what it holds and
// what the forecast asks for; what one machine of each plan carries; the
// domain pool against its target; and the warnings the owner must not miss.
func (h *AdminHandlers) FleetHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	forecasts, err := h.svc.FleetForecasts(ctx)
	if h.refused(w, err) {
		return
	}
	domains, err := h.svc.DomainPoolNow(ctx)
	if h.refused(w, err) {
		return
	}
	held, err := h.svc.poolsInService(ctx)
	if h.refused(w, err) {
		return
	}
	pools := make([]fleetPoolRow, 0, len(forecasts))
	warnings := []string{}
	for _, f := range forecasts {
		proto, region := splitPool(f.Pool)
		pools = append(pools, fleetPoolRow{
			FleetForecast: f, Proto: proto, VoiceRegion: region, HorizonSeconds: f.HorizonSeconds(),
		})
	}
	if domains.Need > 0 && held[FleetPool(ProtoVlessXhttp, "")] && h.svc.currentBuyer().reg == nil {
		warnings = append(warnings, "пул доменов: регистратора нет — новых доменов не будет")
	}
	// What one machine of each plan carries (П9.15); the dataset going down
	// costs this column, not the screen.
	capacities, err := h.svc.PlanCapacities(ctx)
	if err != nil {
		applog.Error(ctx, "entry", "plan capacity read failed", applog.F("err", err.Error()))
	}
	if capacities == nil {
		capacities = []PlanCapacity{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pools": pools, "domainPool": domains, "warnings": warnings, "capacities": capacities,
	})
}

// SpendHandler — GET /api/v1/admin/entry/spend: every spend group against its
// caps — what the day and the month have cost so far, where they end at the
// current burn, and the caps.
func (h *AdminHandlers) SpendHandler(w http.ResponseWriter, r *http.Request) {
	spend, err := h.svc.Spend(r.Context())
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"spend": spend})
}

// SaveSpendCapsHandler — PUT /api/v1/admin/entry/spend/{role}. Body
// {"dailyCapCents", "monthlyCapCents"}, both required: a cap is a pair, and
// half of one saved would leave the other to a value nobody looked at.
// Answers with every group, so the screen redraws from what the hub holds.
func (h *AdminHandlers) SaveSpendCapsHandler(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Daily   *int `json:"dailyCapCents"`
		Monthly *int `json:"monthlyCapCents"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, knobBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Daily == nil || body.Monthly == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is {dailyCapCents, monthlyCapCents}"})
		return
	}
	role := r.PathValue("role")
	if _, err := h.svc.SaveSpendCaps(r.Context(), role, *body.Daily, *body.Monthly, actor.ID); h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.spend_caps.save",
		TargetType: "entry_spend_caps", TargetID: role,
		Payload: map[string]any{"dailyCapCents": *body.Daily, "monthlyCapCents": *body.Monthly},
	})
	h.SpendHandler(w, r)
}

// FleetChartHandler — GET /api/v1/admin/entry/fleet/chart: every chart of the
// autopilot screen, the whole doorway's and each pool's (poolchart.go).
func (h *AdminHandlers) FleetChartHandler(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.FleetChartNow(r.Context())
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// poolsInService are the pools with a machine still on the books — in service,
// or out of it under suspicion: what a missing registrar leaves without
// domains.
func (s *Service) poolsInService(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT proto, coalesce(region, '') FROM entry_boxes WHERE retired_at IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("entry: pools in service: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var proto, region string
		if err := rows.Scan(&proto, &region); err != nil {
			return nil, err
		}
		out[FleetPool(proto, region)] = true
	}
	return out, rows.Err()
}
