package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// adminMux mounts the console with open gates: the gates are the admin
// package's and tested there.
func (s *shop) adminMux() *http.ServeMux {
	mux := http.NewServeMux()
	open := func(next http.HandlerFunc) http.HandlerFunc { return next }
	RegisterAdminRoutes(mux, NewAdmin(s.svc, s.svc.pool), open, open)
	return mux
}

func call(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The console shows a paid subscription with its link and order, renews it,
// revokes it, and issues one by hand.
func TestTheConsoleManagesSubscriptions(t *testing.T) {
	s := newShop(t, true)
	mux := s.adminMux()
	p := s.order("console@example.com", "")
	s.notify(s.ref(p), Paid, 9900)
	name := accountName(uuid.MustParse(p.ID))

	code, out := call(t, mux, "GET", "/api/v1/admin/billing/subscriptions?q=console@example", "")
	if code != http.StatusOK || !strings.Contains(mustJSON(out), name) {
		t.Fatalf("list: %d %v", code, out)
	}
	code, out = call(t, mux, "GET", "/api/v1/admin/billing/subscriptions/"+name, "")
	if code != http.StatusOK || out["url"] != s.status(p).URL {
		t.Fatalf("detail: %d %v", code, out)
	}
	if orders := out["orders"].([]any); len(orders) != 1 || orders[0].(map[string]any)["status"] != "paid" {
		t.Fatalf("orders %v", orders)
	}
	if state := out["subscription"].(map[string]any)["state"]; state != "live" {
		t.Fatalf("state %v", state)
	}

	code, _ = call(t, mux, "POST", "/api/v1/admin/billing/subscriptions/"+name+"/renew", `{"plan":"`+s.plan+`"}`)
	if code != http.StatusOK {
		t.Fatalf("renew %d", code)
	}
	code, out = call(t, mux, "POST", "/api/v1/admin/billing/subscriptions/"+name+"/revoke", "")
	if code != http.StatusOK || out["subscription"].(map[string]any)["state"] != "revoked" {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if code, _ = call(t, mux, "POST", "/api/v1/admin/billing/subscriptions/"+name+"/revoke", ""); code != http.StatusNotFound {
		t.Fatalf("revoking twice: %d", code)
	}

	gift := "t-gift-" + uuid.NewString()[:6]
	code, out = call(t, mux, "POST", "/api/v1/admin/billing/subscriptions", `{"name":"`+gift+`","plan":"`+s.plan+`"}`)
	if code != http.StatusCreated || !strings.Contains(out["url"].(string), "/sub/") {
		t.Fatalf("issue: %d %v", code, out)
	}
	if code, _ = call(t, mux, "POST", "/api/v1/admin/billing/subscriptions", `{"name":"`+gift+`","plan":"`+s.plan+`"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a taken name: %d", code)
	}
}

// Plans are put on sale, changed and retired from the console.
func TestTheConsoleEditsPlans(t *testing.T) {
	s := newShop(t, true)
	mux := s.adminMux()
	id := "t-c-" + uuid.NewString()[:6]
	code, _ := call(t, mux, "PUT", "/api/v1/admin/billing/plans/"+id,
		`{"title":"3 месяца","days":90,"trafficGiB":3000,"priceCents":25000}`)
	if code != http.StatusOK {
		t.Fatalf("put %d", code)
	}
	plans, err := s.subs.Plans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range plans {
		found = found || (p.ID == id && p.TrafficBytes == 3000*gib && p.PriceCents == 25000)
	}
	if !found {
		t.Fatalf("plan %s not on sale: %+v", id, plans)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/admin/billing/plans/"+id, `{"days":0}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a zero-day plan: %d", code)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/admin/billing/plans/"+id+"/retire", ""); code != http.StatusOK {
		t.Fatalf("retire %d", code)
	}
	code, out := call(t, mux, "GET", "/api/v1/admin/billing", "")
	if code != http.StatusOK || out["provider"] != "sandbox" || !strings.Contains(mustJSON(out), `"id":"`+id+`"`) || !strings.Contains(mustJSON(out), `"title":"3 месяца"`) {
		t.Fatalf("overview: %d %v", code, out)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
