package billing

// admin.go is the admin console's «Подписки»: subscriptions, plans and
// orders, and what the owner does to them by hand — issue, renew, revoke,
// change a plan. Every write is audited.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

const gib = 1 << 30

// Admin serves the console.
type Admin struct {
	svc   *Service
	audit db.DBTX
}

func NewAdmin(svc *Service, auditDB db.DBTX) *Admin {
	return &Admin{svc: svc, audit: auditDB}
}

// RegisterAdminRoutes mounts the console's routes. Everything here touches
// money or access, so all of it is the owner's; revoking cuts a paying
// subscriber off and asks for a fresh step-up too. Taken as functions so this
// package need not import the admin one.
func RegisterAdminRoutes(mux *http.ServeMux, h *Admin, owner, ownerStepUp func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/billing", owner(h.Overview))
	mux.HandleFunc("GET /api/v1/admin/billing/subscriptions", owner(h.Subscriptions))
	mux.HandleFunc("POST /api/v1/admin/billing/subscriptions", owner(h.Issue))
	mux.HandleFunc("GET /api/v1/admin/billing/subscriptions/{name}", owner(h.Subscription))
	mux.HandleFunc("POST /api/v1/admin/billing/subscriptions/{name}/renew", owner(h.Renew))
	mux.HandleFunc("POST /api/v1/admin/billing/subscriptions/{name}/revoke", ownerStepUp(h.Revoke))
	mux.HandleFunc("PUT /api/v1/admin/billing/plans/{id}", owner(h.PutPlan))
	mux.HandleFunc("POST /api/v1/admin/billing/plans/{id}/retire", owner(h.RetirePlan))
}

// ---------- wire shapes ----------

type subscriptionJSON struct {
	Name         string     `json:"name"`
	Email        string     `json:"email"`
	Plan         string     `json:"plan"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"createdAt"`
	ExpiresAt    *time.Time `json:"expiresAt"`
	RevokedAt    *time.Time `json:"revokedAt"`
	TrafficBytes int64      `json:"trafficBytes"`
	UsedBytes    int64      `json:"usedBytes"`
	Relays       []string   `json:"relays"`
}

func toSubscriptionJSON(v subscription.Summary) subscriptionJSON {
	out := subscriptionJSON{
		Name: v.Name, Email: v.Email, Plan: v.Plan, State: string(v.State), CreatedAt: v.CreatedAt,
		TrafficBytes: v.TrafficBytes, UsedBytes: v.Used, Relays: v.Relays,
	}
	if !v.ExpiresAt.IsZero() {
		out.ExpiresAt = &v.ExpiresAt
	}
	if !v.RevokedAt.IsZero() {
		out.RevokedAt = &v.RevokedAt
	}
	if out.Relays == nil {
		out.Relays = []string{}
	}
	return out
}

type planJSON struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Days          int    `json:"days"`
	TrafficGiB    int64  `json:"trafficGiB"`
	PriceCents    int    `json:"priceCents"`
	Retired       bool   `json:"retired"`
	Subscriptions int    `json:"subscriptions"`
}

type orderJSON struct {
	ID          uuid.UUID  `json:"id"`
	Plan        string     `json:"plan"`
	AmountCents int        `json:"amountCents"`
	Email       string     `json:"email"`
	Renewal     bool       `json:"renewal"`
	Account     string     `json:"account"`
	Provider    string     `json:"provider"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	ClosedAt    *time.Time `json:"closedAt"`
}

// ---------- reads ----------

// Overview is the provider, the plans, takings and the latest orders.
func (h *Admin) Overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	plans, err := h.svc.subs.AllPlans(ctx)
	if h.failed(w, err) {
		return
	}
	outPlans := make([]planJSON, 0, len(plans))
	for _, p := range plans {
		outPlans = append(outPlans, planJSON{
			ID: p.ID, Title: p.Title, Days: p.Days, TrafficGiB: p.TrafficBytes / gib,
			PriceCents: p.PriceCents, Retired: p.Retired, Subscriptions: p.Subscriptions,
		})
	}
	orders, err := h.orders(r, `TRUE`, r.URL.Query().Get("status"))
	if h.failed(w, err) {
		return
	}
	var paid30, count30 int64
	var live int64
	err = h.svc.pool.QueryRow(ctx, `
		SELECT coalesce(sum(amount_cents), 0), count(*) FROM hub_orders
		WHERE status = 'paid' AND closed_at > now() - interval '30 days'`).Scan(&paid30, &count30)
	if h.failed(w, err) {
		return
	}
	live, err = h.svc.subs.CountLive(ctx)
	if h.failed(w, err) {
		return
	}
	provider := ""
	if h.svc.provider != nil {
		provider = h.svc.provider.Name()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": provider,
		"plans":    outPlans,
		"orders":   orders,
		"totals":   map[string]int64{"paid30dCents": paid30, "paid30dOrders": count30, "live": live},
	})
}

// Subscriptions lists subscriptions; ?q= filters by name or email.
func (h *Admin) Subscriptions(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.subs.List(r.Context(), r.URL.Query().Get("q"), 500)
	if h.failed(w, err) {
		return
	}
	out := make([]subscriptionJSON, 0, len(list))
	for _, v := range list {
		out = append(out, toSubscriptionJSON(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

// Subscription is one subscription with its link and its orders.
func (h *Admin) Subscription(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.subs.Get(r.Context(), r.PathValue("name"))
	if h.failed(w, err) {
		return
	}
	link, err := h.svc.subs.URL(r.Context(), v.Account)
	if err != nil && !errors.Is(err, subscription.ErrUnknown) {
		h.failed(w, err)
		return
	}
	orders, err := h.orders(r, `account_id = $2`, "", v.Account)
	if h.failed(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscription": toSubscriptionJSON(v),
		// "" for a revoked one, or one issued before links were kept
		"url":    link,
		"orders": orders,
	})
}

// orders is the latest orders matching where ($1 is the status filter, ""
// for any; further args follow).
func (h *Admin) orders(r *http.Request, where, status string, args ...any) ([]orderJSON, error) {
	rows, err := h.svc.pool.Query(r.Context(), `
		SELECT o.id, o.plan_id, o.amount_cents, o.email, o.renewal, coalesce(a.name, ''), o.provider,
		       o.status, o.created_at, o.closed_at
		FROM hub_orders o LEFT JOIN hub_accounts a ON a.id = o.account_id
		WHERE ($1 = '' OR o.status = $1) AND `+where+`
		ORDER BY o.created_at DESC LIMIT 200`, append([]any{status}, args...)...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (orderJSON, error) {
		var o orderJSON
		return o, r.Scan(&o.ID, &o.Plan, &o.AmountCents, &o.Email, &o.Renewal, &o.Account, &o.Provider,
			&o.Status, &o.CreatedAt, &o.ClosedAt)
	})
	if out == nil {
		out = []orderJSON{}
	}
	return out, err
}

// ---------- writes ----------

// Issue gives a subscription by hand (a gift, a tester, a refund made good):
// {name, plan}. The link is answered once, as from the CLI.
func (h *Admin) Issue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Plan string `json:"plan"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	token, err := h.svc.subs.Add(r.Context(), in.Name, in.Plan)
	if h.failed(w, err) {
		return
	}
	h.log(r, "subscription.issue", in.Name, map[string]any{"plan": in.Plan})
	writeJSON(w, http.StatusCreated, map[string]string{"url": subscription.LinkURL(token)})
}

// Renew starts the next period on {plan}, free of charge.
func (h *Admin) Renew(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Plan string `json:"plan"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if h.failed(w, h.svc.subs.Renew(r.Context(), name, in.Plan)) {
		return
	}
	h.log(r, "subscription.renew", name, map[string]any{"plan": in.Plan})
	h.writeSubscription(w, r, name)
}

// Revoke cuts the subscription off on every relay at their next pulse.
func (h *Admin) Revoke(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if h.failed(w, h.svc.subs.Revoke(r.Context(), name)) {
		return
	}
	h.log(r, "subscription.revoke", name, nil)
	h.writeSubscription(w, r, name)
}

// PutPlan puts a plan on sale or changes it: {title, days, trafficGiB,
// priceCents}. Subscriptions already sold keep their terms.
func (h *Admin) PutPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title      string `json:"title"`
		Days       int    `json:"days"`
		TrafficGiB int64  `json:"trafficGiB"`
		PriceCents int    `json:"priceCents"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	p := subscription.Plan{ID: id, Title: in.Title, Days: in.Days, TrafficBytes: in.TrafficGiB * gib, PriceCents: in.PriceCents}
	if err := h.svc.subs.SetPlan(r.Context(), p); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "тариф: id из [a-z0-9-], дней > 0, трафик и цена не меньше 0",
		})
		return
	}
	h.log(r, "plan.set", id, map[string]any{
		"title": in.Title, "days": in.Days, "trafficGiB": in.TrafficGiB, "priceCents": in.PriceCents,
	})
	w.WriteHeader(http.StatusNoContent)
}

// RetirePlan takes a plan off sale.
func (h *Admin) RetirePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.failed(w, h.svc.subs.RetirePlan(r.Context(), id)) {
		return
	}
	h.log(r, "plan.retire", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- plumbing ----------

func (h *Admin) writeSubscription(w http.ResponseWriter, r *http.Request, name string) {
	v, err := h.svc.subs.Get(r.Context(), name)
	if h.failed(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription": toSubscriptionJSON(v)})
}

func (h *Admin) log(r *http.Request, action, target string, payload map[string]any) {
	a := auth.GetAdmin(r)
	if a == nil {
		return
	}
	audit.Log(r.Context(), h.audit, r, audit.Entry{
		AdminID: a.ID, Action: action, TargetType: "billing", TargetID: target, Payload: payload,
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return false
	}
	return true
}

// failed answers err and reports whether there was one.
func (h *Admin) failed(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	status, msg := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, subscription.ErrUnknown):
		status, msg = http.StatusNotFound, "подписка не найдена или уже отозвана"
	case errors.Is(err, subscription.ErrUnknownPlan):
		status, msg = http.StatusUnprocessableEntity, "такого тарифа нет в продаже"
	case errors.Is(err, subscription.ErrName):
		status, msg = http.StatusUnprocessableEntity, "имя: латиница, цифры, - и _, до 32 символов, и не занято"
	default:
		applog.Error(context.Background(), "billing", "admin call failed", applog.F("err", err.Error()))
	}
	writeJSON(w, status, map[string]string{"error": msg})
	return true
}
