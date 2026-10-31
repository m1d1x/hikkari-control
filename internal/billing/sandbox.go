package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// Sandbox is a payment provider that moves no money: the hub itself shows
// the payment page, with «оплатить» and «отменить». It runs the whole
// purchase — order, payment page, notification, issued subscription, link on
// the landing — before a merchant is connected, and on a test hub after.
//
// Its notifications are signed like a real provider's (HMAC-SHA256 of the
// body under the secret, X-Sandbox-Signature), so the webhook path is the
// one a merchant uses. Payments live in memory: a restart cancels the open
// ones, as an abandoned payment would be.
type Sandbox struct {
	// base is the hub's public URL the payment page is served under.
	base   string
	secret []byte

	mu       sync.Mutex
	payments map[string]*sandboxPayment
}

type sandboxPayment struct {
	Payment
	state State
}

func NewSandbox(base string, secret []byte) *Sandbox {
	return &Sandbox{base: base, secret: secret, payments: map[string]*sandboxPayment{}}
}

func (*Sandbox) Name() string { return "sandbox" }

func (s *Sandbox) Create(_ context.Context, p Payment) (Checkout, error) {
	ref := "sb_" + uuid.NewString()
	s.mu.Lock()
	s.payments[ref] = &sandboxPayment{Payment: p}
	s.mu.Unlock()
	return Checkout{Ref: ref, URL: s.base + "/v1/billing/sandbox/" + ref}, nil
}

func (s *Sandbox) Check(_ context.Context, ref string) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[ref]
	if !ok {
		// Forgotten by a restart: never paid here.
		return Event{Ref: ref, State: Canceled}, nil
	}
	return Event{Ref: ref, State: p.state, AmountCents: p.AmountCents}, nil
}

// sandboxNote is the sandbox's notification body.
type sandboxNote struct {
	Ref         string `json:"ref"`
	State       string `json:"state"`
	AmountCents int    `json:"amount_cents"`
}

func (s *Sandbox) Webhook(r *http.Request) (Event, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return Event{}, err
	}
	got, err := hex.DecodeString(r.Header.Get("X-Sandbox-Signature"))
	if err != nil || !hmac.Equal(got, s.sign(body)) {
		return Event{}, ErrSignature
	}
	var n sandboxNote
	if err := json.Unmarshal(body, &n); err != nil {
		return Event{}, err
	}
	ev := Event{Ref: n.Ref, AmountCents: n.AmountCents}
	switch n.State {
	case "paid":
		ev.State = Paid
	case "canceled":
		ev.State = Canceled
	default:
		return Event{}, ErrNotForUs
	}
	return ev, nil
}

// sign is the signature of a notification body.
func (s *Sandbox) sign(body []byte) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write(body)
	return m.Sum(nil)
}

// Note is a signed notification for ref, as the sandbox sends it.
func (s *Sandbox) Note(ref string, state State, amount int) (body []byte, signature string) {
	st := map[State]string{Paid: "paid", Canceled: "canceled", Pending: "pending"}[state]
	body, _ = json.Marshal(sandboxNote{Ref: ref, State: st, AmountCents: amount})
	return body, hex.EncodeToString(s.sign(body))
}

func (s *Sandbox) register(mux *http.ServeMux, svc *Service) {
	mux.HandleFunc("GET /v1/billing/sandbox/{ref}", s.page)
	mux.HandleFunc("POST /v1/billing/sandbox/{ref}", func(w http.ResponseWriter, r *http.Request) {
		s.decide(w, r, svc)
	})
}

var sandboxPage = template.Must(template.New("sandbox").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Тестовая оплата</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0e0e12;color:#f1f1f4;font:16px/1.5 system-ui,sans-serif}
main{width:min(400px,100% - 32px);padding:32px;border:1px solid #2a2a33;border-radius:18px;background:#16161c}
.tag{display:inline-block;padding:2px 10px;border-radius:999px;background:#3a2a10;color:#ffb648;font-size:13px}
h1{font-size:20px;margin:16px 0 4px}.sum{font-size:32px;font-weight:600;margin:16px 0}
p{color:#a0a0ab;margin:0}form{display:flex;gap:12px;margin-top:24px}
button{flex:1;height:44px;border-radius:14px;border:1px solid #2a2a33;background:#22222a;color:inherit;font:inherit;cursor:pointer}
button.pay{background:#ff7eb9;border-color:#ff7eb9;color:#1a0f15;font-weight:600}
</style></head><body><main>
<span class="tag">тест: деньги не списываются</span>
<h1>{{.Description}}</h1><p>{{.Email}}</p>
<div class="sum">{{.Sum}} ₽</div>
<form method="post"><button name="state" value="canceled">отменить</button><button class="pay" name="state" value="paid">оплатить</button></form>
</main></body></html>`))

func (s *Sandbox) page(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p, ok := s.payments[r.PathValue("ref")]
	var view struct{ Description, Email, Sum string }
	if ok {
		view.Description, view.Email = p.Description, p.Email
		view.Sum = rubles(p.AmountCents)
	}
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = sandboxPage.Execute(w, view)
}

// decide records the buyer's choice and notifies the hub the way a provider
// would, through the signed webhook path, then sends the buyer back.
func (s *Sandbox) decide(w http.ResponseWriter, r *http.Request, svc *Service) {
	ref := r.PathValue("ref")
	state := map[string]State{"paid": Paid, "canceled": Canceled}[r.FormValue("state")]
	s.mu.Lock()
	p, ok := s.payments[ref]
	if ok && p.state == Pending && state != Pending {
		p.state = state
	}
	s.mu.Unlock()
	if !ok || state == Pending {
		http.NotFound(w, r)
		return
	}
	body, sig := s.Note(ref, p.state, p.AmountCents)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Sandbox-Signature", sig)
	ev, err := s.Webhook(req)
	if err == nil {
		err = svc.Settle(r.Context(), ev)
	}
	if err != nil && !errors.Is(err, ErrNotForUs) {
		// The order is settled later by Check, as with a lost notification.
		applog.Error(r.Context(), "billing", "sandbox settle", applog.F("err", err.Error()))
	}
	http.Redirect(w, r, p.ReturnURL, http.StatusSeeOther)
}

// rubles is kopecks as the price is shown: 99, 490, 12,50.
func rubles(cents int) string {
	if cents%100 == 0 {
		return strconv.Itoa(cents / 100)
	}
	return fmt.Sprintf("%d,%02d", cents/100, cents%100)
}
