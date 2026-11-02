package vdsina

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type api struct {
	t        *testing.T
	keys     []map[string]any
	created  []map[string]any
	rebooted []string
	deleted  []string
	ip       string
	fail     map[string]int
	failMsg  map[string]string
	auth     []string
}

// server is a machine as the API really shows it: nested objects, and no ip
// object until the machine has an address.
func (a *api) server() map[string]any {
	out := map[string]any{
		"id": 101, "name": "ams1", "status": "active",
		"datacenter":  map[string]any{"id": 4, "name": "Amsterdam 1, Netherlands", "country": "nl"},
		"server-plan": map[string]any{"id": 1, "name": "1 RAM / 1 CPU / 10 NVMe"},
	}
	if a.ip != "0.0.0.0" {
		out["ip"] = map[string]any{"id": 119402, "ip": a.ip, "type": "4"}
	}
	return out
}

func newAPI(t *testing.T) (*api, *Client) {
	t.Helper()
	a := &api{t: t, ip: "0.0.0.0", fail: map[string]int{}, failMsg: map[string]string{}}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return a, New("vdsina_test_token", WithBaseURL(srv.URL))
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.auth = append(a.auth, r.Header.Get("Authorization"))
	if code, ok := a.fail[r.URL.Path]; ok {
		msg, ok := a.failMsg[r.URL.Path]
		if !ok {
			msg = "server limit reached"
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "error",
			"status_msg": msg,
		})
		return
	}
	body := map[string]any{}
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"status_msg": "ok",
			"data":       v,
		})
	}
	switch {
	case r.URL.Path == "/account.balance":
		write(map[string]any{
			"real":  2818.50,
			"bonus": 1240.25,
		})
	case r.URL.Path == "/datacenter":
		write([]map[string]any{
			{"id": 1, "name": "Москва, Россия", "city": "Москва", "country": "RU", "active": 1},
			{"id": 4, "name": "Амстердам, Нидерланды", "city": "Амстердам", "country": "NL", "active": 1},
		})
	case r.URL.Path == "/server-plan":
		write([]map[string]any{
			{
				"id": 1, "name": "Стандарт-1", "cost": 11.5, "period": "day",
				"cpu": 1, "ram": 1.0, "disk": 30, "enable": 1, "active": 1,
				"data": map[string]any{
					"datacenter": []int{1, 4},
				},
			},
		})
	case r.URL.Path == "/template":
		write([]map[string]any{
			{"id": 12, "name": "Debian 11 x64", "ssh-key": true},
			{"id": 23, "name": "Ubuntu 24.04 x64", "ssh-key": true},
			{"id": 19, "name": "Ubuntu 22.04 x64", "ssh-key": true},
		})
	case r.URL.Path == "/ssh-key" && r.Method == http.MethodGet:
		keys := []map[string]any{}
		for i, k := range a.keys {
			keys = append(keys, map[string]any{
				"id": i + 1, "name": k["name"],
			})
		}
		write(keys)
	case strings.HasPrefix(r.URL.Path, "/ssh-key/") && r.Method == http.MethodGet:
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/ssh-key/"))
		write(map[string]any{"id": id, "name": a.keys[id-1]["name"], "data": a.keys[id-1]["data"]})
	case r.URL.Path == "/ssh-key" && r.Method == http.MethodPost:
		a.keys = append(a.keys, body)
		write(map[string]any{
			"id": len(a.keys), "name": body["name"], "data": body["data"],
		})
	case r.URL.Path == "/server" && r.Method == http.MethodPost:
		a.created = append(a.created, body)
		write(map[string]any{"id": 101})
	case r.URL.Path == "/server" && r.Method == http.MethodGet:
		write([]map[string]any{
			a.server(),
		})
	case r.URL.Path == "/server/101" && r.Method == http.MethodGet:
		write(a.server())
	case strings.HasPrefix(r.URL.Path, "/server.reboot/101") && r.Method == http.MethodPut:
		a.rebooted = append(a.rebooted, "101")
		write(map[string]any{"status": "ok"})
	case r.URL.Path == "/server/101" && r.Method == http.MethodDelete:
		a.deleted = append(a.deleted, "101")
		write(map[string]any{"status": "ok"})
	case r.URL.Path == "/server/gone" && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "error",
			"status_msg": "Server not found",
		})
	default:
		a.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func TestAccountReadsMoney(t *testing.T) {
	a, c := newAPI(t)
	acct, err := c.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 2818.50 + 1240.25 = 4058.75 -> 405875 cents
	if acct.BalanceCents != 405875 || acct.PendingCents != 0 {
		t.Errorf("acct = %+v, want 405875 balance / 0 pending", acct)
	}
	if len(a.auth) == 0 || a.auth[0] != "vdsina_test_token" {
		t.Errorf("auth header = %q", a.auth)
	}
}

func TestPlansAndRegions(t *testing.T) {
	_, c := newAPI(t)
	plans, err := c.Plans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	p := plans[0]
	// 11.5 per day * 30 days = 345.00 rubles = 34500 cents
	if p.MonthlyCents != 34500 || p.VCPUs != 1 || p.RAMMB != 1024 || p.DiskGB != 30 || len(p.Regions) != 2 {
		t.Errorf("plan = %+v", p)
	}
	regions, err := c.Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[0].ID != "1" || regions[1].ID != "4" {
		t.Errorf("regions = %+v", regions)
	}
}

func TestUbuntuLTS(t *testing.T) {
	_, c := newAPI(t)
	tmpl, err := c.UbuntuLTS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.ID != 23 {
		t.Errorf("tmpl = %+v, want Ubuntu 24.04 (ID 23)", tmpl)
	}
}

func TestEnsureSSHKey(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	id, err := c.EnsureSSHKey(ctx, "nekko-hub", "ssh-ed25519 AAAAMATERIAL hub@nekko")
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.EnsureSSHKey(ctx, "nekko-hub", "ssh-ed25519 AAAAMATERIAL someone-else@laptop")
	if err != nil {
		t.Fatal(err)
	}
	if id != again {
		t.Errorf("ids %q and %q for one key", id, again)
	}
	if len(a.keys) != 1 {
		t.Errorf("keys uploaded: %d, want 1", len(a.keys))
	}
	if _, err := c.EnsureSSHKey(ctx, "nekko-hub", "  "); err == nil {
		t.Error("an empty key was accepted")
	}
}

func TestCreateAndInstance(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	inst, err := c.Create(ctx, CreateParams{
		Region: "4", Plan: "1", Hostname: "ams1", SSHKeyID: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Address != "" {
		t.Errorf("address %q on machine still initializing", inst.Address)
	}
	if got := a.created[0]; got["datacenter"] != float64(4) || got["server-plan"] != float64(1) || got["template"] != float64(23) || got["name"] != "ams1" {
		t.Errorf("create payload = %v", got)
	}

	a.ip = "185.123.45.67"
	got, err := c.Instance(ctx, "101")
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "185.123.45.67" || got.Status != "active" {
		t.Errorf("got = %+v", got)
	}
}

func TestRebootAndDelete(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	if err := c.Reboot(ctx, "101"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "101"); err != nil {
		t.Fatal(err)
	}
	if len(a.rebooted) != 1 || len(a.deleted) != 1 {
		t.Errorf("rebooted %v, deleted %v", a.rebooted, a.deleted)
	}
	if err := c.Delete(ctx, "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting missing: err = %v, want ErrNotFound", err)
	}
}

func TestFailuresAreConcrete(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
		want   error
	}{
		{http.StatusUnauthorized, "invalid token", ErrBadCredentials},
		{http.StatusForbidden, "forbidden", ErrBadCredentials},
		{http.StatusTooManyRequests, "rate limit", ErrRateLimited},
		{http.StatusBadRequest, "out of stock", ErrOutOfCapacity},
		{http.StatusBadRequest, "insufficient balance", ErrInsufficientFunds},
	} {
		a, c := newAPI(t)
		a.fail["/account.balance"] = tc.status
		a.failMsg["/account.balance"] = tc.msg
		if _, err := c.Account(context.Background()); !errors.Is(err, tc.want) {
			t.Errorf("%d %s: err = %v, want %v", tc.status, tc.msg, err, tc.want)
		}
	}

	a, c := newAPI(t)
	a.fail["/account.balance"] = http.StatusUnauthorized
	a.failMsg["/account.balance"] = "Unauthorized IP address: 203.0.113.7"
	if _, err := c.Account(context.Background()); !errors.Is(err, ErrBadCredentials) ||
		!strings.Contains(err.Error(), "IP allowlist") || strings.Contains(err.Error(), "203.0.113.7") {
		t.Errorf("401 with reason: err = %v, want safe IP allowlist advice without leaked IP", err)
	}

	a, c = newAPI(t)
	a.fail["/datacenter"] = http.StatusInternalServerError
	a.failMsg["/datacenter"] = "internal error secret-response"
	_, err := c.Regions(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 500 || strings.Contains(apiErr.Error(), "secret-response") {
		t.Errorf("err = %v, want status without leaked response", err)
	}
}

func TestProviderErrorsNeverCarryAnEgressAddress(t *testing.T) {
	for _, status := range []int{401, 403, 500, 502} {
		err := classify(status, "Unauthorized IP address: 203.0.113.7 secret-response")
		if strings.Contains(err.Error(), "203.0.113.7") || strings.Contains(err.Error(), "secret-response") {
			t.Fatalf("response leaked: %v", err)
		}
	}
}

func TestCreateSendsTheKeyUnderTheNameTheAPIReads(t *testing.T) {
	a, c := newAPI(t)
	if _, err := c.Create(context.Background(), CreateParams{Region: "4", Plan: "1", Hostname: "ams1", SSHKeyID: "28954"}); err != nil {
		t.Fatal(err)
	}
	if got := a.created[0]["ssh-key"]; got != float64(28954) {
		t.Fatalf("ssh-key = %v in %v", got, a.created[0])
	}
}

func TestEnsureSSHKeyFindsAKeyTheListDoesNotSpellOut(t *testing.T) {
	a, c := newAPI(t)
	a.keys = append(a.keys, map[string]any{"name": "hub", "data": "ssh-ed25519 AAAA one"})
	id, err := c.EnsureSSHKey(context.Background(), "hub", "ssh-ed25519 AAAA two")
	if err != nil {
		t.Fatal(err)
	}
	if id != "1" || len(a.keys) != 1 {
		t.Fatalf("id = %s, keys = %d: an uploaded key was uploaded again", id, len(a.keys))
	}
}

func TestRefusalKeepsItsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","status_code":200,"status_msg":"","description":"Another operation is currently being performed","data":null}`))
	}))
	t.Cleanup(srv.Close)
	err := New("t", WithBaseURL(srv.URL)).Delete(context.Background(), "101")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanLimitReadsAsCapacity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","status_code":400,"status_msg":"Bad Request","description":"Validation Error","data":{"server-plan":["You cannot order more services under this tariff plan"]}}`))
	}))
	t.Cleanup(srv.Close)
	_, err := New("t", WithBaseURL(srv.URL)).Create(context.Background(), CreateParams{Region: "4", Plan: "36", TemplateID: 64, Hostname: "ams1"})
	if !errors.Is(err, ErrOutOfCapacity) {
		t.Fatalf("err = %v", err)
	}
}
