package billing

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/httputil"
)

const (
	maxBody = 4 << 10
	// ordersPerHour bounds what one address asks for that costs something
	// elsewhere: orders open payments at the provider, recoveries send mail.
	ordersPerHour = 20
	// maxTracked bounds the limiter's memory; past it, new addresses wait
	// for the next window rather than evicting someone mid-window.
	maxTracked = 100_000
)

// Public serves the landing's checkout and the provider's notifications on
// the public plane:
//
//	GET  /v1/billing/plans               what is on sale
//	POST /v1/billing/orders              {plan, email, renew?} → {id, secret, pay_url}
//	GET  /v1/billing/orders/{id}         Authorization: Bearer <secret>
//	POST /v1/billing/recover             {email}: the address's links, by mail
//	POST /v1/billing/webhook/{provider}  the provider's notifications
//
// The landing is another origin: origins lists the ones answered.
type Public struct {
	svc     *Service
	origins []string

	mu     sync.Mutex
	window time.Time
	opened map[string]int
}

func NewPublic(svc *Service, origins []string) *Public {
	return &Public{svc: svc, origins: origins, opened: map[string]int{}}
}

func (p *Public) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/billing/plans", p.cors(p.plans))
	mux.HandleFunc("POST /v1/billing/orders", p.cors(p.place))
	mux.HandleFunc("GET /v1/billing/orders/{id}", p.cors(p.order))
	mux.HandleFunc("POST /v1/billing/recover", p.cors(p.recover))
	mux.HandleFunc("OPTIONS /v1/billing/", p.cors(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if p.svc.provider != nil {
		mux.HandleFunc("POST /v1/billing/webhook/"+p.svc.provider.Name(), p.webhook)
		if sb, ok := p.svc.provider.(*Sandbox); ok {
			sb.register(mux, p.svc)
		}
	}
}

func (p *Public) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && slices.Contains(p.origins, o) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Set("Access-Control-Allow-Methods", "GET, POST")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
		}
		next(w, r)
	}
}

type planOut struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Days       int    `json:"days"`
	PriceCents int    `json:"price_cents"`
}

func outPlan(id, title string, days, price int) planOut {
	return planOut{ID: id, Title: title, Days: days, PriceCents: price}
}

func (p *Public) plans(w http.ResponseWriter, r *http.Request) {
	plans, err := p.svc.subs.Plans(r.Context())
	if err != nil {
		applog.Error(r.Context(), "billing", "list plans", applog.F("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]planOut, 0, len(plans))
	for _, pl := range plans {
		// A free plan is issued by hand, never sold. The traffic ceiling is
		// a fair-use guard, not part of the offer.
		if pl.PriceCents > 0 {
			out = append(out, outPlan(pl.ID, pl.Title, pl.Days, pl.PriceCents))
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"open": p.svc.Open(), "currency": "RUB", "plans": out})
}

func (p *Public) place(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Plan  string `json:"plan"`
		Email string `json:"email"`
		Renew string `json:"renew"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "body")
		return
	}
	if !p.allow(httputil.ClientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate")
		return
	}
	placed, err := p.svc.Place(r.Context(), in.Plan, in.Email, in.Renew)
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, map[string]string{
			"id": placed.ID.String(), "secret": placed.Secret, "pay_url": placed.PayURL,
		})
	case errors.Is(err, ErrClosed):
		writeErr(w, http.StatusServiceUnavailable, "closed")
	case errors.Is(err, ErrEmail):
		writeErr(w, http.StatusBadRequest, "email")
	case errors.Is(err, ErrPlan):
		writeErr(w, http.StatusBadRequest, "plan")
	case errors.Is(err, ErrRenewal):
		writeErr(w, http.StatusBadRequest, "renew")
	default:
		applog.Error(r.Context(), "billing", "place order", applog.F("err", err.Error()))
		writeErr(w, http.StatusBadGateway, "provider")
	}
}

func (p *Public) order(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err != nil || !ok || secret == "" {
		writeErr(w, http.StatusNotFound, "order")
		return
	}
	v, err := p.svc.Status(r.Context(), id, secret)
	if errors.Is(err, ErrUnknownOrder) {
		writeErr(w, http.StatusNotFound, "order")
		return
	}
	if err != nil {
		applog.Error(r.Context(), "billing", "order status", applog.F("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := map[string]any{
		"status":  v.Status,
		"renewal": v.Renewal,
		"plan":    outPlan(v.Plan.ID, v.Plan.Title, v.Plan.Days, v.Plan.PriceCents),
	}
	if v.URL != "" {
		out["url"] = v.URL
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// recover answers 202 whether or not the address bought anything: the
// letter, not the page, says so (letters.go).
func (p *Public) recover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "body")
		return
	}
	if !p.allow(httputil.ClientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate")
		return
	}
	err := p.svc.Recover(r.Context(), in.Email)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
	case errors.Is(err, ErrEmail):
		writeErr(w, http.StatusBadRequest, "email")
	default:
		applog.Error(r.Context(), "billing", "recover", applog.F("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
	}
}

// webhook answers 2xx for every notification that needs no retry, and an
// error for one the provider should send again.
func (p *Public) webhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	ev, err := p.svc.provider.Webhook(r)
	switch {
	case errors.Is(err, ErrNotForUs):
		w.WriteHeader(http.StatusOK)
		return
	case errors.Is(err, ErrSignature):
		applog.Warn(r.Context(), "billing", "webhook signature refused")
		w.WriteHeader(http.StatusUnauthorized)
		return
	case err != nil:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	err = p.svc.Settle(r.Context(), ev)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, ErrUnknownOrder):
		applog.Warn(r.Context(), "billing", "webhook for unknown order", applog.F("ref", ev.Ref))
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, ErrAmount):
		// Money taken that the order does not account for: the owner's
		// call, never a silent fix.
		applog.Error(r.Context(), "billing", "paid amount mismatch", applog.F("err", err.Error()))
		w.WriteHeader(http.StatusUnprocessableEntity)
	default:
		applog.Error(r.Context(), "billing", "settle", applog.F("err", err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// allow counts an order against addr's hourly allowance. The address is held
// for the window only and never logged.
func (p *Public) allow(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.svc.now()
	if now.Sub(p.window) >= time.Hour {
		p.window, p.opened = now, map[string]int{}
	}
	n, seen := p.opened[addr]
	if !seen && len(p.opened) >= maxTracked {
		return false
	}
	if n >= ordersPerHour {
		return false
	}
	p.opened[addr] = n + 1
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
