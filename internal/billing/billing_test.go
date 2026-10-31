package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

func TestMain(m *testing.M) {
	cryptobox.ConfigureEphemeralForTests()
	os.Exit(m.Run())
}

func livePool(t *testing.T) db.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run billing integration tests")
	}
	ctx := context.Background()
	p, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := db.RunMigrations(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p
}

type noBatch struct{}

func (noBatch) BundleFor(context.Context, uuid.UUID, entry.GrantMeta, string) (entry.Bundle, error) {
	return entry.Bundle{}, nil
}

// shop is a hub selling one month for 99 ₽ through the sandbox.
type shop struct {
	t    *testing.T
	subs *subscription.Service
	svc  *Service
	sb   *Sandbox
	mux  *http.ServeMux
	plan string
}

func newShop(t *testing.T, open bool) *shop {
	t.Helper()
	pool := livePool(t)
	subs := subscription.New(pool, noBatch{}, nil)
	plan := "t-b-" + uuid.NewString()[:8]
	if err := subs.SetPlan(context.Background(), subscription.Plan{
		ID: plan, Title: "1 месяц", Days: 30, TrafficBytes: 1 << 40, PriceCents: 9900,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subs.RetirePlan(context.Background(), plan) })
	sb := NewSandbox("https://api.test", []byte("sandbox-secret"))
	var provider Provider
	if open {
		provider = sb
	}
	svc := New(pool, subs, provider, "https://hikkari.test/pay/done")
	mux := http.NewServeMux()
	NewPublic(svc, []string{"https://hikkari.test"}).Register(mux)
	return &shop{t: t, subs: subs, svc: svc, sb: sb, mux: mux, plan: plan}
}

func (s *shop) do(method, path, auth string, body any, header ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	var buf bytes.Buffer
	switch b := body.(type) {
	case nil:
	case []byte:
		buf.Write(b)
	default:
		_ = json.NewEncoder(&buf).Encode(b)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Origin", "https://hikkari.test")
	if auth != "" {
		r.Header.Set("Authorization", "Bearer "+auth)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

type placed struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
	PayURL string `json:"pay_url"`
}

func (s *shop) order(email, renew string) placed {
	s.t.Helper()
	w := s.do("POST", "/v1/billing/orders", "", map[string]string{"plan": s.plan, "email": email, "renew": renew})
	if w.Code != http.StatusCreated {
		s.t.Fatalf("order: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://hikkari.test" {
		s.t.Fatal("the landing's origin is not allowed")
	}
	var p placed
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return p
}

type status struct {
	Status string `json:"status"`
	URL    string `json:"url"`
}

func (s *shop) status(p placed) status {
	s.t.Helper()
	w := s.do("GET", "/v1/billing/orders/"+p.ID, p.Secret, nil)
	if w.Code != http.StatusOK {
		s.t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	var st status
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	return st
}

func (s *shop) ref(p placed) string {
	s.t.Helper()
	ref, ok := strings.CutPrefix(p.PayURL, "https://api.test/v1/billing/sandbox/")
	if !ok {
		s.t.Fatalf("pay url %q", p.PayURL)
	}
	return ref
}

// notify sends the sandbox's signed notification to the webhook.
func (s *shop) notify(ref string, state State, amount int) int {
	s.t.Helper()
	body, sig := s.sb.Note(ref, state, amount)
	return s.do("POST", "/v1/billing/webhook/sandbox", "", body, "X-Sandbox-Signature", sig).Code
}

func (s *shop) token(url string) string {
	s.t.Helper()
	i := strings.LastIndex(url, "/sub/")
	if i < 0 {
		s.t.Fatalf("subscription url %q", url)
	}
	return url[i+len("/sub/"):]
}

// A buyer orders, pays, and reads their link off the order; the link is a
// live subscription on the plan, and a repeated notification gives nothing
// twice.
func TestAPaidOrderIssuesOneSubscription(t *testing.T) {
	s := newShop(t, true)
	ctx := context.Background()

	w := s.do("GET", "/v1/billing/plans", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"`+s.plan+`"`) ||
		!strings.Contains(w.Body.String(), `"open":true`) {
		t.Fatalf("plans: %d %s", w.Code, w.Body)
	}

	p := s.order("Buyer@Example.com", "")
	if st := s.status(p); st.Status != "pending" || st.URL != "" {
		t.Fatalf("before paying: %+v", st)
	}
	if code := s.do("GET", "/v1/billing/orders/"+p.ID, "wrong-secret", nil).Code; code != http.StatusNotFound {
		t.Fatalf("someone else's order answered %d", code)
	}

	ref := s.ref(p)
	if code := s.notify(ref, Paid, 9900); code != http.StatusOK {
		t.Fatalf("webhook %d", code)
	}
	st := s.status(p)
	if st.Status != "paid" || st.URL == "" {
		t.Fatalf("after paying: %+v", st)
	}
	account, err := s.subs.Account(ctx, s.token(st.URL))
	if err != nil {
		t.Fatalf("the link is not a subscription: %v", err)
	}
	id := uuid.MustParse(p.ID)
	terms, err := s.subs.Status(ctx, accountName(id))
	if err != nil {
		t.Fatal(err)
	}
	if terms.Plan != s.plan || time.Until(terms.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("terms %+v", terms)
	}

	if code := s.notify(ref, Paid, 9900); code != http.StatusOK {
		t.Fatalf("repeated webhook %d", code)
	}
	again, _ := s.subs.Status(ctx, accountName(id))
	if !again.ExpiresAt.Equal(terms.ExpiresAt) {
		t.Fatalf("a repeated notification moved the end: %v → %v", terms.ExpiresAt, again.ExpiresAt)
	}
	if a2, _ := s.subs.Account(ctx, s.token(s.status(p).URL)); a2 != account {
		t.Fatal("a repeated notification issued another subscription")
	}
}

// An order placed with a subscription's token pays for that subscription's
// next period: same link, the end moved by the plan's days.
func TestARenewalExtendsTheSameSubscription(t *testing.T) {
	s := newShop(t, true)
	ctx := context.Background()
	first := s.order("renew@example.com", "")
	s.notify(s.ref(first), Paid, 9900)
	url := s.status(first).URL
	name := accountName(uuid.MustParse(first.ID))
	before, _ := s.subs.Status(ctx, name)

	renewal := s.order("renew@example.com", s.token(url))
	s.notify(s.ref(renewal), Paid, 9900)
	st := s.status(renewal)
	if st.Status != "paid" || st.URL != url {
		t.Fatalf("renewal: %+v, want the same link %s", st, url)
	}
	after, _ := s.subs.Status(ctx, name)
	if got := after.ExpiresAt.Sub(before.ExpiresAt); got < 29*24*time.Hour || got > 31*24*time.Hour {
		t.Fatalf("renewal moved the end by %v", got)
	}

	w := s.do("POST", "/v1/billing/orders", "", map[string]string{"plan": s.plan, "email": "x@example.com", "renew": "no-such-token-123456"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "renew") {
		t.Fatalf("renewing an unknown token: %d %s", w.Code, w.Body)
	}
}

// A lost notification costs the buyer nothing: their page asks, the hub asks
// the provider, the order settles.
func TestALostNotificationIsMadeUpByAskingTheProvider(t *testing.T) {
	s := newShop(t, true)
	p := s.order("lost@example.com", "")
	s.sb.mu.Lock()
	s.sb.payments[s.ref(p)].state = Paid
	s.sb.mu.Unlock()
	if st := s.status(p); st.Status != "paid" || st.URL == "" {
		t.Fatalf("after asking the provider: %+v", st)
	}
}

// What is refused: a forged notification, a sum other than the order's, a
// bad address, a plan not on sale, and any order while no provider is set.
func TestBillingRefuses(t *testing.T) {
	s := newShop(t, true)
	p := s.order("refuse@example.com", "")
	ref := s.ref(p)

	body, _ := s.sb.Note(ref, Paid, 9900)
	if code := s.do("POST", "/v1/billing/webhook/sandbox", "", body, "X-Sandbox-Signature", "00").Code; code != http.StatusUnauthorized {
		t.Fatalf("forged notification: %d", code)
	}
	if code := s.notify(ref, Paid, 100); code != http.StatusUnprocessableEntity {
		t.Fatalf("short payment: %d", code)
	}
	if st := s.status(p); st.Status != "pending" {
		t.Fatalf("after refusals the order is %s", st.Status)
	}
	if code := s.notify(ref, Canceled, 0); code != http.StatusOK {
		t.Fatalf("cancel: %d", code)
	}
	if st := s.status(p); st.Status != "canceled" {
		t.Fatalf("after cancel: %s", st.Status)
	}

	for _, c := range []struct {
		plan, email, want string
	}{
		{s.plan, "not an address", "email"},
		{s.plan, "a@b.c, d@e.f", "email"},
		{"t-b-nothing", "a@example.com", "plan"},
	} {
		w := s.do("POST", "/v1/billing/orders", "", map[string]string{"plan": c.plan, "email": c.email})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
			t.Fatalf("%+v: %d %s", c, w.Code, w.Body)
		}
	}

	closed := newShop(t, false)
	w := closed.do("POST", "/v1/billing/orders", "", map[string]string{"plan": closed.plan, "email": "a@example.com"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed shop took an order: %d", w.Code)
	}
	if w := closed.do("GET", "/v1/billing/plans", "", nil); !strings.Contains(w.Body.String(), `"open":false`) {
		t.Fatalf("closed shop plans: %s", w.Body)
	}
}

// The sandbox's page pays and sends the buyer back to the landing with the
// order and its secret.
func TestTheSandboxPagePaysAndReturns(t *testing.T) {
	s := newShop(t, true)
	p := s.order("page@example.com", "")
	path := strings.TrimPrefix(p.PayURL, "https://api.test")
	if w := s.do("GET", path, "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "99 ₽") {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
	w := s.do("POST", path, "", []byte("state=paid"), "Content-Type", "application/x-www-form-urlencoded")
	want := "https://hikkari.test/pay/done?order=" + p.ID + "#key=" + p.Secret
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("pay: %d → %q", w.Code, w.Header().Get("Location"))
	}
	if st := s.status(p); st.Status != "paid" {
		t.Fatalf("after the page: %s", st.Status)
	}
}
