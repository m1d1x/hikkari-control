package presets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// maxBody bounds one request: a group is at most a few thousand rules.
const maxBody = 2 << 20

var country = regexp.MustCompile(`^[A-Z]{2}$`)

// AdminHandlers edit groups and presets and drive their rollout.
type AdminHandlers struct {
	store *Store
	audit db.DBTX
}

// NewAdminHandlers builds the handlers; auditDB is where admin_audit_log lives.
func NewAdminHandlers(store *Store, auditDB db.DBTX) *AdminHandlers {
	return &AdminHandlers{store: store, audit: auditDB}
}

// RegisterAdminRoutes mounts the presets screens. gate is the admin plane's
// gate; owner adds the owner check. Editing groups and drafts reaches no
// device, so any admin may do it. What changes what devices are served —
// assignment, release, stage, revert, deleting a preset — is the owner's.
// Taken as functions so this package need not import the admin one.
func RegisterAdminRoutes(mux *http.ServeMux, h *AdminHandlers, gate, owner func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/presets/groups", gate(h.Groups))
	mux.HandleFunc("PUT /api/v1/admin/presets/groups/{id}", gate(h.PutGroup))
	mux.HandleFunc("DELETE /api/v1/admin/presets/groups/{id}", gate(h.DeleteGroup))
	mux.HandleFunc("POST /api/v1/admin/presets/rules/normalize", gate(h.Normalize))
	mux.HandleFunc("GET /api/v1/admin/presets", gate(h.Presets))
	// A preset's draft and its assignment arrive together; the assignment
	// moves devices at once, so the whole write is the owner's.
	mux.HandleFunc("POST /api/v1/admin/presets", owner(h.CreatePreset))
	mux.HandleFunc("PUT /api/v1/admin/presets/{id}", owner(h.UpdatePreset))
	mux.HandleFunc("DELETE /api/v1/admin/presets/{id}", owner(h.DeletePreset))
	mux.HandleFunc("POST /api/v1/admin/presets/{id}/release", owner(h.Release))
	mux.HandleFunc("POST /api/v1/admin/presets/{id}/stage", owner(h.SetStage))
	mux.HandleFunc("POST /api/v1/admin/presets/{id}/revert", owner(h.Revert))
}

// ---------- wire shapes ----------

type groupJSON struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Icon      string    `json:"icon"`
	Rules     Rules     `json:"rules"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type releaseJSON struct {
	ID        int64     `json:"id"`
	V         int       `json:"v"`
	Services  []Service `json:"services"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type presetJSON struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Default   bool         `json:"default"`
	Countries []string     `json:"countries"`
	Bypass    []string     `json:"bypass"`
	Proxy     []string     `json:"proxy"`
	Current   *releaseJSON `json:"current"`
	Next      *releaseJSON `json:"next"`
	Stage     int          `json:"stage"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

func toReleaseJSON(r *Release) *releaseJSON {
	if r == nil {
		return nil
	}
	return &releaseJSON{ID: r.ID, V: r.V, Services: r.Services, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
}

func toPresetJSON(p Preset) presetJSON {
	return presetJSON{
		ID: p.ID, Name: p.Name, Default: p.Default,
		Countries: nonNil(p.Countries), Bypass: nonNil(p.Bypass), Proxy: nonNil(p.Proxy),
		Current: toReleaseJSON(p.Current), Next: toReleaseJSON(p.Next),
		Stage: p.Stage, UpdatedAt: p.UpdatedAt,
	}
}

type draftJSON struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Default   bool     `json:"default"`
	Countries []string `json:"countries"`
	Bypass    []string `json:"bypass"`
	Proxy     []string `json:"proxy"`
}

// ---------- groups ----------

// Groups — GET /api/v1/admin/presets/groups.
func (h *AdminHandlers) Groups(w http.ResponseWriter, r *http.Request) {
	gs, err := h.store.Groups(r.Context())
	if h.refused(w, err) {
		return
	}
	out := make([]groupJSON, len(gs))
	for i, g := range gs {
		out[i] = groupJSON{ID: g.ID, Name: g.Name, Icon: g.Icon, Rules: g.Rules, UpdatedAt: g.UpdatedAt}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// PutGroup — PUT /api/v1/admin/presets/groups/{id}. Body: {name, icon,
// rules}. Creates or replaces; a rule the client would reject is a 422
// naming it.
func (h *AdminHandlers) PutGroup(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	var body groupJSON
	if !readJSON(w, r, &body) {
		return
	}
	g := Group{ID: r.PathValue("id"), Name: body.Name, Icon: body.Icon, Rules: body.Rules}
	if err := ValidateGroup(g); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if h.refused(w, h.store.PutGroup(r.Context(), g)) {
		return
	}
	h.log(r, actor, "presets.group.put", "preset_group", g.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DeleteGroup — DELETE /api/v1/admin/presets/groups/{id}; 409 while a
// preset's draft holds it.
func (h *AdminHandlers) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if h.refused(w, h.store.DeleteGroup(r.Context(), id)) {
		return
	}
	h.log(r, actor, "presets.group.delete", "preset_group", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Normalize — POST /api/v1/admin/presets/rules/normalize. Body: {category,
// value}. The canonical form of a pasted rule, so the editor stores what
// the client stores.
func (h *AdminHandlers) Normalize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Category string `json:"category"`
		Value    string `json:"value"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	normal, err := NormalizeRule(body.Category, body.Value)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": normal})
}

// ---------- presets ----------

// Presets — GET /api/v1/admin/presets, the default first.
func (h *AdminHandlers) Presets(w http.ResponseWriter, r *http.Request) {
	ps, err := h.store.Presets(r.Context())
	if h.refused(w, err) {
		return
	}
	out := make([]presetJSON, len(ps))
	for i, p := range ps {
		out[i] = toPresetJSON(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": out})
}

// CreatePreset — POST /api/v1/admin/presets. Body: {id, name, default,
// countries, bypass, proxy}.
func (h *AdminHandlers) CreatePreset(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	var body draftJSON
	if !readJSON(w, r, &body) {
		return
	}
	if !ValidID(body.ID) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "некорректный id пресета"})
		return
	}
	d, ok := draft(w, body)
	if !ok || h.refused(w, h.store.CreatePreset(r.Context(), body.ID, d)) {
		return
	}
	h.log(r, actor, "presets.preset.create", "preset", body.ID, map[string]any{"default": d.Default, "countries": d.Countries})
	h.writePreset(w, r, body.ID, http.StatusCreated)
}

// UpdatePreset — PUT /api/v1/admin/presets/{id}. Body as for create, id
// ignored.
func (h *AdminHandlers) UpdatePreset(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	var body draftJSON
	if !readJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	d, ok := draft(w, body)
	if !ok || h.refused(w, h.store.UpdatePreset(r.Context(), id, d)) {
		return
	}
	h.log(r, actor, "presets.preset.update", "preset", id, map[string]any{"default": d.Default, "countries": d.Countries})
	h.writePreset(w, r, id, http.StatusOK)
}

func draft(w http.ResponseWriter, body draftJSON) (PresetDraft, bool) {
	d := PresetDraft{Name: body.Name, Default: body.Default, Countries: body.Countries, Bypass: body.Bypass, Proxy: body.Proxy}
	if len(d.Name) == 0 || len(d.Name) > 100 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "название пресета: от 1 до 100 символов"})
		return d, false
	}
	for _, c := range d.Countries {
		if !country.MatchString(c) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "страна " + c + ": нужен код ISO из двух заглавных букв"})
			return d, false
		}
	}
	return d, true
}

// DeletePreset — DELETE /api/v1/admin/presets/{id}. Its devices move to the
// default at once; the default itself cannot be deleted.
func (h *AdminHandlers) DeletePreset(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if h.refused(w, h.store.DeletePreset(r.Context(), id)) {
		return
	}
	h.log(r, actor, "presets.preset.delete", "preset", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Release — POST /api/v1/admin/presets/{id}/release. Body: {stage}, the
// percent of devices that get it now; 100 ships it to everyone at once.
func (h *AdminHandlers) Release(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	stage, ok := readStage(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	rel, err := h.store.Release(r.Context(), id, stage, actor.Login)
	if h.refused(w, err) {
		return
	}
	h.log(r, actor, "presets.release", "preset", id, map[string]any{"v": rel.V, "release": rel.ID, "stage": stage})
	h.writePreset(w, r, id, http.StatusOK)
}

// SetStage — POST /api/v1/admin/presets/{id}/stage. Body: {stage}, any
// percent; 100 makes the release current.
func (h *AdminHandlers) SetStage(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	stage, ok := readStage(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if h.refused(w, h.store.SetStage(r.Context(), id, stage)) {
		return
	}
	h.log(r, actor, "presets.stage", "preset", id, map[string]any{"stage": stage})
	h.writePreset(w, r, id, http.StatusOK)
}

// Revert — POST /api/v1/admin/presets/{id}/revert: every device goes back
// to the current release.
func (h *AdminHandlers) Revert(w http.ResponseWriter, r *http.Request) {
	actor, ok := admin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if h.refused(w, h.store.Revert(r.Context(), id)) {
		return
	}
	h.log(r, actor, "presets.revert", "preset", id, nil)
	h.writePreset(w, r, id, http.StatusOK)
}

func readStage(w http.ResponseWriter, r *http.Request) (int, bool) {
	var body struct {
		Stage *int `json:"stage"`
	}
	if !readJSON(w, r, &body) {
		return 0, false
	}
	if body.Stage == nil || *body.Stage < 0 || *body.Stage > 100 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "стадия: процент от 0 до 100"})
		return 0, false
	}
	return *body.Stage, true
}

// writePreset answers a write with the preset as it now stands, so the
// screen redraws from the server's truth.
func (h *AdminHandlers) writePreset(w http.ResponseWriter, r *http.Request, id string, status int) {
	p, err := h.store.Preset(r.Context(), id)
	if h.refused(w, err) {
		return
	}
	writeJSON(w, status, map[string]any{"preset": toPresetJSON(p)})
}

// ---------- plumbing ----------

func admin(w http.ResponseWriter, r *http.Request) (*auth.AdminInfo, bool) {
	a := auth.GetAdmin(r)
	if a == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	return a, a != nil
}

func (h *AdminHandlers) log(r *http.Request, a *auth.AdminInfo, action, targetType, id string, payload map[string]any) {
	audit.Log(r.Context(), h.audit, r, audit.Entry{AdminID: a.ID, Action: action, TargetType: targetType, TargetID: id, Payload: payload})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос: " + err.Error()})
		return false
	}
	return true
}

// refused answers a store error and reports whether there was one.
func (h *AdminHandlers) refused(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	status, msg := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, ErrNotFound):
		status, msg = http.StatusNotFound, "не найдено"
	case errors.Is(err, ErrExists):
		status, msg = http.StatusConflict, "такой id уже есть"
	case errors.Is(err, ErrGroupInUse):
		status, msg = http.StatusConflict, "группа стоит в пресете: сначала убери её оттуда"
	case errors.Is(err, ErrDefault):
		status, msg = http.StatusConflict, "пресет по умолчанию нельзя удалить: сначала сделай другой пресетом по умолчанию"
	case errors.Is(err, ErrNoNext):
		status, msg = http.StatusConflict, "сейчас ничего не раскатывается"
	case errors.Is(err, ErrUnknownGroup):
		status, msg = http.StatusUnprocessableEntity, "в пресете неизвестная группа"
	case errors.Is(err, ErrGroupTwice):
		status, msg = http.StatusUnprocessableEntity, "группа стоит дважды или с обеих сторон"
	case errors.Is(err, ErrNothingToShip):
		status, msg = http.StatusUnprocessableEntity, "в пресете нет групп"
	case errors.Is(err, ErrInvalid):
		status, msg = http.StatusUnprocessableEntity, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")
	default:
		applog.Error(context.Background(), "presets", "admin call failed", applog.F("err", err.Error()))
	}
	writeJSON(w, status, map[string]string{"error": msg})
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
