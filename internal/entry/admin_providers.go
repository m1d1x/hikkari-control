package entry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
)

// The console's provider accounts and domain purchases (roadmap 42 П9.1).
// Saving keys and buying are behind a fresh passkey touch (stepUp in
// RegisterAdminRoutes); reading, quoting and re-checking are not.

// Providers — GET /api/v1/admin/entry/providers: every built-in provider kind
// with its saved account (never the keys), and the registrars' spend.
func (h *AdminHandlers) Providers(w http.ResponseWriter, r *http.Request) {
	ps, err := h.svc.Providers(r.Context())
	if h.refused(w, err) {
		return
	}
	spend, err := h.svc.DomainSpendNow(r.Context())
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": ps, "domainSpend": spend})
}

// SaveProvider — PUT /api/v1/admin/entry/providers/{kind}. Body: {"enabled",
// "credentials": {field: value} or absent to keep the saved ones, "tlds",
// "defaultRegion", "defaultPlan"}. What an account may spend is its group's
// (SpendCaps), not the account's.
// New credentials are checked against the provider before anything is saved.
// The last two are a hoster's order defaults; a registrar refuses them.
func (h *AdminHandlers) SaveProvider(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Enabled       bool              `json:"enabled"`
		Credentials   map[string]string `json:"credentials"`
		TLDs          []string          `json:"tlds"`
		DefaultRegion string            `json:"defaultRegion"`
		DefaultPlan   string            `json:"defaultPlan"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not a provider account"})
		return
	}
	kind := r.PathValue("kind")
	v, err := h.svc.SaveProvider(r.Context(), kind, ProviderUpdate{
		Enabled: body.Enabled, Credentials: body.Credentials, TLDs: body.TLDs,
		DefaultRegion: body.DefaultRegion, DefaultPlan: body.DefaultPlan,
	}, actor.ID)
	if h.refusedProvider(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.provider.save", TargetType: "entry_provider", TargetID: kind,
		// The journal says a key was replaced and which one by its masked
		// tail, never the key.
		Payload: map[string]any{"enabled": body.Enabled, "credentials_replaced": body.Credentials != nil,
			"creds_hint": v.CredsHint, "tlds": v.TLDs,
			"default_region": v.DefaultRegion, "default_plan": v.DefaultPlan},
	})
	writeJSON(w, http.StatusOK, map[string]any{"provider": v})
}

// CheckProvider — POST /api/v1/admin/entry/providers/{kind}/check: ask the
// provider again whether the saved key works and how much credit is left.
func (h *AdminHandlers) CheckProvider(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.CheckProvider(r.Context(), r.PathValue("kind"))
	if h.refusedProvider(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": v})
}

// ProviderCatalog reads one hoster's catalog using a draft or saved key.
// It is read-only: authentication is required, no step-up or save is needed.
func (h *AdminHandlers) ProviderCatalog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Credentials map[string]string `json:"credentials"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, knobBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is {credentials: {...}} or {} for the saved key"})
		return
	}
	catalog, err := h.svc.PreviewProviderCatalog(r.Context(), r.PathValue("kind"), body.Credentials)
	if h.refusedProvider(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog": catalog})
}

// SuggestDomains — GET /api/v1/admin/entry/domains/suggest?n=3: fresh
// candidate names under the per-domain cap, quoted live.
func (h *AdminHandlers) SuggestDomains(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	offers, err := h.svc.SuggestDomains(r.Context(), n)
	if h.refusedProvider(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offers": offers})
}

// AccountDomains — GET /api/v1/admin/entry/domains/account: the domains on the
// registrar account next to the hub's ledger, each with whether it may go into
// the pool and why not.
func (h *AdminHandlers) AccountDomains(w http.ResponseWriter, r *http.Request) {
	ds, err := h.svc.AccountDomains(r.Context())
	if h.refusedProvider(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": ds})
}

// CheckoutDomain — POST /api/v1/admin/entry/domains/checkout. Body:
// {"domain"}. A fresh quote, the registrar's dry run and the hub's caps;
// spends nothing.
func (h *AdminHandlers) CheckoutDomain(w http.ResponseWriter, r *http.Request) {
	name, ok := readDomainBody(w, r)
	if !ok {
		return
	}
	co, err := h.svc.CheckoutDomain(r.Context(), name)
	if h.refusedProvider(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkout": co})
}

// BuyDomain — POST /api/v1/admin/entry/domains/buy. Body: {"domain"}. Buys the
// name into the pool through the same budget gate as the hub's own purchases.
func (h *AdminHandlers) BuyDomain(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	name, ok := readDomainBody(w, r)
	if !ok {
		return
	}
	d, err := h.svc.BuyDomain(r.Context(), name)
	if h.refusedProvider(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.domain.buy", TargetType: "entry_domain", TargetID: d.Domain,
		Payload: map[string]any{"cost_cents": d.CostCents, "order_id": d.OrderID},
	})
	writeJSON(w, http.StatusOK, map[string]any{"domain": toDomainRow(d)})
}

// SeedDomain — POST /api/v1/admin/entry/domains/seed. Body: {"domain"}. A name
// the owner bought by hand goes into the pool, drawn before any purchase.
func (h *AdminHandlers) SeedDomain(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	name, ok := readDomainBody(w, r)
	if !ok {
		return
	}
	// The seed asks the registrar whether the name is on the account, so its
	// refusals are a provider call's.
	if h.refusedProvider(w, h.svc.SeedDomain(r.Context(), name)) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.domain.seed", TargetType: "entry_domain", TargetID: name,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Exclusions — GET /api/v1/admin/entry/domains/exclusions: the do-not-use
// list, the brand's built-in entry first.
func (h *AdminHandlers) Exclusions(w http.ResponseWriter, r *http.Request) {
	xs, err := h.svc.Exclusions(r.Context())
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exclusions": xs})
}

// ExcludeDomain — POST /api/v1/admin/entry/domains/exclusions. Body:
// {"domain", "note"}. Protective, so no step-up: it only narrows what the
// hub may use.
func (h *AdminHandlers) ExcludeDomain(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Domain string `json:"domain"`
		Note   string `json:"note"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Domain == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not a domain"})
		return
	}
	if h.refused(w, h.svc.ExcludeDomain(r.Context(), body.Domain, body.Note, actor.ID)) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.domain.exclude", TargetType: "entry_domain", TargetID: body.Domain,
		Payload: map[string]any{"note": body.Note},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// UnexcludeDomain — DELETE /api/v1/admin/entry/domains/exclusions/{domain}.
// Behind step-up: it makes a name usable for a relay again.
func (h *AdminHandlers) UnexcludeDomain(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	domain := r.PathValue("domain")
	err := h.svc.UnexcludeDomain(r.Context(), domain)
	if errors.Is(err, ErrNoSuchExclusion) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.domain.unexclude", TargetType: "entry_domain", TargetID: domain,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func readDomainBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Domain string `json:"domain"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Domain == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not a domain"})
		return "", false
	}
	return body.Domain, true
}

// refusedProvider is refused for calls that reach out to a provider: the
// hub's own refusals by their meaning, and anything else — the provider's
// answer or a dead link to it — as 502 with the provider's words, which the
// owner needs to act on (a revoked key, no credit, a rate limit).
func (h *AdminHandlers) refusedProvider(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrUnknownProvider):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such provider"})
	case errors.Is(err, ErrProviderCheck):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNoRegistrar), errors.Is(err, ErrBudget), errors.Is(err, ErrDomainUnavailable),
		errors.Is(err, ErrNoCandidate):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrRejected):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		applog.Warn(context.Background(), "entry", "provider call failed", applog.F("err", err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	return true
}

// ── The hoster (roadmap 42 П9.2) ────────────────────────────────────────

// Hosting — GET /api/v1/admin/entry/hosting: what the enabled hoster sells,
// its saved defaults, and the hosting group's spend against its caps. One call,
// because the console's order form needs all of it at once.
func (h *AdminHandlers) Hosting(w http.ResponseWriter, r *http.Request) {
	cat, err := h.svc.HostingCatalog(r.Context())
	if h.refusedProvider(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosting": cat})
}

// HostingInstances — GET /api/v1/admin/entry/hosting/instances: the machines
// on the hoster's account, each said to belong to a box or to be an orphan the
// hub is paying for and has no row for.
func (h *AdminHandlers) HostingInstances(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.HostingInstances(r.Context())
	if h.refusedProvider(w, err) {
		return
	}
	if list == nil {
		list = []HostingInstance{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": list})
}

// OrderBox — POST /api/v1/admin/entry/boxes/order. Body: {"region", "plan",
// "name", "proto", "voiceRegion"} — every field optional but the protocol's
// own requirements: the region and plan fall back to the hoster's defaults and
// the name is derived from the region's city.
//
// Behind the step-up: it rents a machine, which is the most expensive thing
// the console can do.
func (h *AdminHandlers) OrderBox(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Region      string `json:"region"`
		Plan        string `json:"plan"`
		Name        string `json:"name"`
		Proto       string `json:"proto"`
		VoiceRegion string `json:"voiceRegion"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if r.ContentLength != 0 {
		if err := dec.Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not an order"})
			return
		}
	}
	box, job, err := h.prov.OrderBox(r.Context(), OrderSpec{
		Region: body.Region, Plan: body.Plan, Name: body.Name,
		Proto: body.Proto, VoiceRegion: body.VoiceRegion,
	}, actor.ID)
	if h.refusedProvider(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.order", TargetType: "entry_box", TargetID: box,
		Payload: map[string]any{"job": job, "region": body.Region, "plan": body.Plan, "proto": body.Proto},
	})
	writeJSON(w, http.StatusOK, map[string]any{"box": box, "jobId": job})
}

// RebootBox — POST /api/v1/admin/entry/boxes/{name}/reboot: restart the
// machine through the hoster and wait for it to answer, as a job. Free and
// reversible, so no step-up.
func (h *AdminHandlers) RebootBox(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	box := r.PathValue("name")
	id, err := h.prov.RebootBox(r.Context(), box, actor.ID)
	if h.refusedProvider(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.reboot", TargetType: "entry_box", TargetID: box,
		Payload: map[string]any{"job": id},
	})
	writeJSON(w, http.StatusOK, map[string]any{"jobId": id})
}

// DeleteBoxInstance — DELETE /api/v1/admin/entry/boxes/{name}/instance: hand
// a retired box's machine back to the hoster and stop the charge. The retire
// does this itself; this is the retry for when it could not (the hoster was
// down, the key had been replaced).
func (h *AdminHandlers) DeleteBoxInstance(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	box := r.PathValue("name")
	if h.refusedProvider(w, h.svc.DeleteBoxInstance(r.Context(), box)) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.delete_instance", TargetType: "entry_box", TargetID: box,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
