package entry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// maxUpload bounds one request body.
const maxUpload = 1 << 20

// AdminHandlers is the operator surface of the pool, on the admin plane only.
type AdminHandlers struct {
	svc   *Service
	prov  *Provisioner
	audit db.DBTX
}

// NewAdminHandlers builds the handlers; auditDB is where admin_audit_log lives.
func NewAdminHandlers(svc *Service, prov *Provisioner, auditDB db.DBTX) *AdminHandlers {
	return &AdminHandlers{svc: svc, prov: prov, audit: auditDB}
}

// RegisterAdminRoutes mounts the pool routes. gate MUST be the admin plane's
// owner gate (admin.RequireAdmin + admin.RequireOwner): who may change the pool
// decides who reaches every client. Relays enter the pool through the hub's
// own provisioning (П6.15), not through an upload. stepUp is the same gate plus
// a fresh passkey touch (admin.RequireStepUp): it guards what a stolen session
// must not do quietly — replace a provider's keys or spend money (П9.1). Taken
// as functions so this package need not import the admin one.
func RegisterAdminRoutes(mux *http.ServeMux, h *AdminHandlers, gate, stepUp func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/entry/relays", gate(h.List))
	mux.HandleFunc("GET /api/v1/admin/entry/sources", gate(h.SourcesStatus))
	mux.HandleFunc("POST /api/v1/admin/entry/sources", gate(h.PublishSources))
	// What destroys without a way back, or may buy, is behind the step-up
	// (owner's decision 27.10, audit 42 С5): a stolen owner session without a
	// fresh touch must not retire the fleet — retire deletes the instance and
	// its disk at the hoster — nor wipe a box, nor forget a host key so that
	// the next login trusts whoever answers on :22, nor record a box whose
	// first job may buy a domain.
	//
	// Marking a relay dead is a retire with a fuse (owner's decision 27.10,
	// audit 42 recheck НВ1): a machine whose relays are all dead is idle, and
	// the idle pass deletes it at the hoster six hours later. Provisioning a
	// recorded box stays on the gate — a domain it may buy obeys the
	// buy_domain mode and pause like any other spend (AcquireDomain).
	mux.HandleFunc("POST /api/v1/admin/entry/relays/{name}/dead", stepUp(h.MarkDead))
	mux.HandleFunc("POST /api/v1/admin/entry/boxes", stepUp(h.CreateBox))
	mux.HandleFunc("POST /api/v1/admin/entry/boxes/{name}/provision", gate(h.ProvisionBox))
	mux.HandleFunc("POST /api/v1/admin/entry/boxes/{name}/forget-host-key", stepUp(h.ForgetHostKey))
	mux.HandleFunc("POST /api/v1/admin/entry/boxes/{name}/retire", stepUp(h.RetireBox))
	mux.HandleFunc("POST /api/v1/admin/entry/boxes/{name}/wipe", stepUp(h.WipeBox))
	// The hoster under key (П9.2). Ordering a machine is behind the step-up
	// for the same reason buying a domain is — it spends money; rebooting one
	// is free.
	mux.HandleFunc("GET /api/v1/admin/entry/hosting", gate(h.Hosting))
	mux.HandleFunc("GET /api/v1/admin/entry/hosting/instances", gate(h.HostingInstances))
	mux.HandleFunc("POST /api/v1/admin/entry/boxes/order", stepUp(h.OrderBox))
	mux.HandleFunc("POST /api/v1/admin/entry/boxes/{name}/reboot", gate(h.RebootBox))
	mux.HandleFunc("DELETE /api/v1/admin/entry/boxes/{name}/instance", stepUp(h.DeleteBoxInstance))
	mux.HandleFunc("GET /api/v1/admin/entry/domains", gate(h.Domains))
	mux.HandleFunc("GET /api/v1/admin/entry/domains/suggest", gate(h.SuggestDomains))
	mux.HandleFunc("GET /api/v1/admin/entry/domains/account", gate(h.AccountDomains))
	mux.HandleFunc("GET /api/v1/admin/entry/domains/exclusions", gate(h.Exclusions))
	mux.HandleFunc("POST /api/v1/admin/entry/domains/exclusions", gate(h.ExcludeDomain))
	mux.HandleFunc("DELETE /api/v1/admin/entry/domains/exclusions/{domain}", stepUp(h.UnexcludeDomain))
	mux.HandleFunc("POST /api/v1/admin/entry/domains/checkout", gate(h.CheckoutDomain))
	mux.HandleFunc("POST /api/v1/admin/entry/domains/buy", stepUp(h.BuyDomain))
	mux.HandleFunc("POST /api/v1/admin/entry/domains/seed", gate(h.SeedDomain))
	mux.HandleFunc("GET /api/v1/admin/entry/providers", gate(h.Providers))
	mux.HandleFunc("PUT /api/v1/admin/entry/providers/{kind}", stepUp(h.SaveProvider))
	mux.HandleFunc("POST /api/v1/admin/entry/providers/{kind}/check", gate(h.CheckProvider))
	mux.HandleFunc("POST /api/v1/admin/entry/providers/{kind}/catalog", gate(h.ProviderCatalog))
	mux.HandleFunc("GET /api/v1/admin/entry/jobs", gate(h.Jobs))
	mux.HandleFunc("GET /api/v1/admin/entry/jobs/{id}", gate(h.Job))
	mux.HandleFunc("GET /api/v1/admin/entry/hub-key", gate(h.HubKey))
	// The console's answer back to an incident (П9.6). Running a repair is
	// behind the step-up: the repairs that wait for a person are the ones
	// that spend money. Dismissing and reading what was tried are not.
	mux.HandleFunc("POST /api/v1/admin/entry/incidents/{id}/run", stepUp(h.RunIncidentRepair))
	mux.HandleFunc("POST /api/v1/admin/entry/incidents/{id}/dismiss", gate(h.DismissIncident))
	mux.HandleFunc("GET /api/v1/admin/entry/incidents/{id}/repairs", gate(h.IncidentRepairs))
	// The wave fuse as a thing a person can see and lift (К2). Lifting is
	// behind the step-up: it is what lets the hub spend money on this cause
	// again, which is the mistake the fuse exists to prevent.
	// The eviction thresholds the detection screen turns (К3). Saving is
	// behind the step-up: a lower strike count retires machines sooner and a
	// wider wave window keeps the autopilot buying through a rule change at
	// the censor — money spent without a buy button being pressed.
	mux.HandleFunc("GET /api/v1/admin/entry/policy", gate(h.DetectionPolicyHandler))
	mux.HandleFunc("PUT /api/v1/admin/entry/policy", stepUp(h.SaveDetectionPolicyHandler))
	// What the eviction threshold took out of the pool and has not written
	// off yet (П9.11), and what ended lately — read only.
	mux.HandleFunc("GET /api/v1/admin/entry/suspects", gate(h.SuspectsHandler))
	// The canary's own thresholds and sample size (П6.14, migration 067).
	mux.HandleFunc("GET /api/v1/admin/entry/bench-policy", gate(h.BenchPolicyHandler))
	mux.HandleFunc("PUT /api/v1/admin/entry/bench-policy", stepUp(h.SaveBenchPolicyHandler))
	// The autopilot screen's own knobs (П9.10): a repair's mode and a pool's
	// target. Both saves are behind the step-up — each is a purchase the hub
	// makes later with nobody pressing a buy button.
	mux.HandleFunc("GET /api/v1/admin/entry/playbook-modes", gate(h.PlaybookModesHandler))
	mux.HandleFunc("PUT /api/v1/admin/entry/playbook-modes/{cause}", stepUp(h.SavePlaybookModeHandler))
	mux.HandleFunc("GET /api/v1/admin/entry/fleet", gate(h.FleetHandler))
	mux.HandleFunc("GET /api/v1/admin/entry/fleet/chart", gate(h.FleetChartHandler))
	// The owner's one knob (П9.19): raising a cap is a purchase the hub
	// makes later by itself, so its save is behind the step-up too.
	mux.HandleFunc("GET /api/v1/admin/entry/spend", gate(h.SpendHandler))
	mux.HandleFunc("PUT /api/v1/admin/entry/spend/{role}", stepUp(h.SaveSpendCapsHandler))
	mux.HandleFunc("GET /api/v1/admin/entry/pauses", gate(h.CausePauses))
	mux.HandleFunc("POST /api/v1/admin/entry/pauses/{cause}/lift", stepUp(h.LiftCausePause))
	// Observation point slots (К3′ / П9.12, migration 068). Reading is behind
	// the owner gate; everything that issues a slot's token (opening a slot,
	// replacing its machine, reissuing) or retires a slot sits behind the
	// step-up: a token is the slot's voice in the detector and fetches the
	// pool's dialling credentials (admin_vantages.go).
	mux.HandleFunc("GET /api/v1/admin/entry/vantages", gate(h.VantagesHandler))
	mux.HandleFunc("POST /api/v1/admin/entry/vantages", stepUp(h.CreateVantageHandler))
	mux.HandleFunc("POST /api/v1/admin/entry/vantages/{slot}/token", stepUp(h.ReissueVantageTokenHandler))
	mux.HandleFunc("PUT /api/v1/admin/entry/vantages/{slot}", stepUp(h.SaveVantageHandler))
	mux.HandleFunc("POST /api/v1/admin/entry/vantages/{slot}/retire", stepUp(h.RetireVantageHandler))
	mux.HandleFunc("DELETE /api/v1/admin/entry/vantages/{slot}", stepUp(h.RetireVantageHandler))
}

// List — GET /api/v1/admin/entry/relays: the pool and the boxes behind it,
// addresses opened for the owner (the gate is owner-only), and the relay
// builds this hub deploys — a box whose edgeSha differs runs another version.
func (h *AdminHandlers) List(w http.ResponseWriter, r *http.Request) {
	relays, err := h.svc.List(r.Context())
	if err != nil {
		applog.Error(r.Context(), "entry", "admin list failed", applog.F("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	boxes, err := h.svc.Boxes(r.Context())
	if err != nil {
		applog.Error(r.Context(), "entry", "admin list boxes failed", applog.F("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if relays == nil {
		relays = []PoolRelay{}
	}
	if boxes == nil {
		boxes = []PoolBox{}
	}
	builds := []EdgeBuild{}
	if h.prov != nil {
		builds = h.prov.EdgeBuilds()
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": relays, "boxes": boxes, "edgeBuilds": builds})
}

// SourcesStatus — GET /api/v1/admin/entry/sources: the current source list for
// the operator (version and lifetime), or present=false when none is imported.
func (h *AdminHandlers) SourcesStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Sources(r.Context())
	if err != nil {
		applog.Error(r.Context(), "entry", "sources status failed", applog.F("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// PublishSources — POST /api/v1/admin/entry/sources, body =
// {"sources": [{"kind": "doh_txt", "resolver": …, "name": …} | {"kind":
// "https", "url": …}, …]}. The hub signs it with a fresh version and serves it
// from now on.
func (h *AdminHandlers) PublishSources(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Sources []Source `json:"sources"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not {\"sources\": [...]}"})
		return
	}
	st, err := h.svc.PublishSources(r.Context(), body.Sources)
	switch {
	case errors.Is(err, ErrRejected):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, ErrNoSigner):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the hub has no signing key"})
		return
	case err != nil:
		applog.Error(r.Context(), "entry", "publish sources failed", applog.F("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.sources.publish",
		TargetType: "entry_sources",
		TargetID:   st.KeyID,
		Payload:    map[string]any{"sources": len(body.Sources), "issued_at": st.IssuedAt},
	})
	writeJSON(w, http.StatusOK, st)
}

// deathBody is the body of a death: {"reason"}, one of the Death* reasons.
type deathBody struct {
	Reason string `json:"reason"`
}

func readDeath(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body deathBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not {\"reason\"}"})
		return "", false
	}
	return body.Reason, true
}

// MarkDead — POST /api/v1/admin/entry/relays/{name}/dead. Body: {"reason"}.
func (h *AdminHandlers) MarkDead(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	reason, ok := readDeath(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	revoked, err := h.svc.MarkDead(r.Context(), name, reason)
	if errors.Is(err, ErrNoSuchRelay) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no relay with that name that is not dead"})
		return
	}
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID:    actor.ID,
		Action:     "entry.relay.dead",
		TargetType: "entry_relay",
		TargetID:   name,
		Payload:    map[string]any{"revoked_grants": revoked, "reason": reason},
	})
	writeJSON(w, http.StatusOK, map[string]any{"revokedGrants": revoked})
}

// CreateBox — POST /api/v1/admin/entry/boxes: a machine just rented, and its
// first provisioning job. Body: {"name", "address", "user", "password",
// "provider", "location", "rentedUntil": "YYYY-MM-DD", "proto", "region"}.
// proto is "vless-reality-vision" (the default) or "hysteria2", a voice entry
// in front of `region`'s SFUs. The password is used once by the job and never
// stored or logged.
func (h *AdminHandlers) CreateBox(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Name        string `json:"name"`
		Address     string `json:"address"`
		User        string `json:"user"`
		Password    string `json:"password"`
		Provider    string `json:"provider"`
		Location    string `json:"location"`
		RentedUntil string `json:"rentedUntil"`
		Proto       string `json:"proto"`
		Region      string `json:"region"`
		Domain      string `json:"domain"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not a box"})
		return
	}
	spec := BoxSpec{Name: body.Name, Address: body.Address, Provider: body.Provider,
		Location: body.Location, SSHUser: body.User, Proto: body.Proto, Region: body.Region, Domain: body.Domain}
	if body.RentedUntil != "" {
		d, err := time.Parse(time.DateOnly, body.RentedUntil)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rentedUntil must be YYYY-MM-DD"})
			return
		}
		spec.RentedUntil = &d
	}
	id, err := h.prov.CreateAndProvision(r.Context(), NewBox{BoxSpec: spec, Password: body.Password}, actor.ID)
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.create", TargetType: "entry_box", TargetID: spec.Name,
		Payload: map[string]any{"job": id, "provider": spec.Provider, "location": spec.Location,
			"proto": spec.Proto, "region": spec.Region, "password_given": body.Password != "",
			// The domain is sealed like the address: the journal says one was
			// given, not which.
			"domain_given": body.Domain != ""},
	})
	writeJSON(w, http.StatusOK, map[string]any{"jobId": id})
}

// ProvisionBox — POST /api/v1/admin/entry/boxes/{name}/provision: run the job
// again (an update: newer binary, fresh records). Body: {"password"} — only
// when the hub key is not on the box yet.
func (h *AdminHandlers) ProvisionBox(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpload)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is not {\"password\"}"})
			return
		}
	}
	box := r.PathValue("name")
	id, err := h.prov.Provision(r.Context(), box, body.Password, actor.ID)
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.provision", TargetType: "entry_box", TargetID: box,
		Payload: map[string]any{"job": id, "password_given": body.Password != ""},
	})
	writeJSON(w, http.StatusOK, map[string]any{"jobId": id})
}

// ForgetHostKey — POST /api/v1/admin/entry/boxes/{name}/forget-host-key: the
// operator reinstalled the machine; trust the key it presents next.
func (h *AdminHandlers) ForgetHostKey(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	box := r.PathValue("name")
	if h.refused(w, h.svc.ForgetHostKey(r.Context(), box)) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.forget_host_key", TargetType: "entry_box", TargetID: box,
	})
	writeJSON(w, http.StatusOK, map[string]any{})
}

// RetireBox — POST /api/v1/admin/entry/boxes/{name}/retire: the machine goes
// back to its provider. Body: {"reason"}. Every relay on it dies with that
// reason at once, then a job wipes the machine; the job id is returned to
// follow, and the pool change stands even if the wipe fails.
func (h *AdminHandlers) RetireBox(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	reason, ok := readDeath(w, r)
	if !ok {
		return
	}
	box := r.PathValue("name")
	relays, holders, err := h.svc.Retire(r.Context(), box, reason)
	if h.refused(w, err) {
		return
	}
	if relays == nil {
		relays = []string{}
	}
	// How the machine is let go depends on whose it is. A machine the hub
	// ordered is deleted at the hoster: destroying the disk erases more than
	// the wipe script can, and it is what stops the charge — wiping it first
	// would only be a race between ssh and the delete. A machine rented by
	// hand cannot be deleted from here, so it is wiped and the owner cancels
	// the rent in the panel.
	//
	// Either way the pool change stands: the relays are dead whether or not
	// the hub could reach the machine, and the console offers the step that
	// failed again.
	out := map[string]any{"relays": relays, "holders": holders}
	logged := map[string]any{"reason": reason, "relays": relays, "holders": holders}
	if err := h.svc.DeleteBoxInstance(r.Context(), box); err == nil {
		out["instanceDeleted"] = true
		logged["instance_deleted"] = true
	} else if errors.Is(err, ErrRejected) || errors.Is(err, ErrNoHoster) {
		// No instance to delete (rented by hand, or no hoster configured):
		// the machine is wiped over ssh, as it always was.
		job, werr := h.prov.Wipe(r.Context(), box, actor.ID)
		out["jobId"], logged["job"] = job, job
		if werr != nil {
			out["wipeError"] = werr.Error()
		}
	} else {
		out["instanceError"] = err.Error()
		logged["instance_error"] = err.Error()
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.retire", TargetType: "entry_box", TargetID: box,
		Payload: logged,
	})
	writeJSON(w, http.StatusOK, out)
}

// WipeBox — POST /api/v1/admin/entry/boxes/{name}/wipe: run the wipe of a
// retired box again (the first one could not log in, or a job was running).
func (h *AdminHandlers) WipeBox(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	if actor == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	box := r.PathValue("name")
	id, err := h.prov.Wipe(r.Context(), box, actor.ID)
	if h.refused(w, err) {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: actor.ID, Action: "entry.box.wipe", TargetType: "entry_box", TargetID: box,
		Payload: map[string]any{"job": id},
	})
	writeJSON(w, http.StatusOK, map[string]any{"jobId": id})
}

// Jobs — GET /api/v1/admin/entry/jobs: the latest provisioning runs.
func (h *AdminHandlers) Jobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.svc.Jobs(r.Context(), 50)
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// domainRow is one line of the registrar ledger for the console.
type domainRow struct {
	Domain     string     `json:"domain"`
	TLD        string     `json:"tld"`
	Status     string     `json:"status"`
	Box        string     `json:"box,omitempty"`
	Source     string     `json:"source"`
	CostCents  int        `json:"costCents"`
	OrderID    string     `json:"orderId,omitempty"`
	Error      string     `json:"error,omitempty"`
	BoughtAt   *time.Time `json:"boughtAt,omitempty"`
	AssignedAt *time.Time `json:"assignedAt,omitempty"`
	BurnedAt   *time.Time `json:"burnedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	// A burned name's second chance (073, its open row in the ledger): last
	// asked, clean reads in a row.
	LastReprobeAt *time.Time `json:"lastReprobeAt,omitempty"`
	ReprobeStreak int        `json:"reprobeStreak,omitempty"`
}

// Domains — GET /api/v1/admin/entry/domains: the registrar ledger newest-first
// (Ф6) — the pool, every purchase with its cost and order id, and burned
// names. Each purchase the hub makes is one row here.
func (h *AdminHandlers) Domains(w http.ResponseWriter, r *http.Request) {
	ds, err := h.svc.Domains(r.Context(), 200)
	if h.refused(w, err) {
		return
	}
	spend, err := h.svc.DomainSpendNow(r.Context())
	if h.refused(w, err) {
		return
	}
	rows := make([]domainRow, 0, len(ds))
	for _, d := range ds {
		rows = append(rows, toDomainRow(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": rows, "spend": spend})
}

func toDomainRow(d Domain) domainRow {
	return domainRow{
		Domain: d.Domain, TLD: d.TLD, Status: d.Status, Box: d.Box, Source: d.Source,
		CostCents: d.CostCents, OrderID: d.OrderID, Error: d.Error,
		BoughtAt: d.BoughtAt, AssignedAt: d.AssignedAt, BurnedAt: d.BurnedAt, CreatedAt: d.CreatedAt,
		LastReprobeAt: d.LastReprobeAt, ReprobeStreak: d.ReprobeStreak,
	}
}

// Job — GET /api/v1/admin/entry/jobs/{id}?after=N: one run and its log lines
// after seq N — the console polls it while the job runs.
func (h *AdminHandlers) Job(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	job, err := h.svc.Job(r.Context(), id)
	if h.refused(w, err) {
		return
	}
	lines, err := h.svc.JobLog(r.Context(), id, after)
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job, "lines": lines})
}

// HubKey — GET /api/v1/admin/entry/hub-key: the hub's ssh public key, to paste
// into a provider's panel when ordering a machine (no password needed then).
func (h *AdminHandlers) HubKey(w http.ResponseWriter, r *http.Request) {
	_, pub, err := h.svc.HubSSHKey(r.Context())
	if h.refused(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": pub})
}

// refused writes the response for err and reports whether there was one.
func (h *AdminHandlers) refused(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrRejected):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNoSuchBox), errors.Is(err, ErrNoSuchJob), errors.Is(err, ErrNoSuchRelay), errors.Is(err, ErrNoSuchVantage):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrBoxBusy), errors.Is(err, ErrBoxRetired):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNoSigner):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the hub has no signing key"})
	default:
		applog.Error(context.Background(), "entry", "admin call failed", applog.F("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
