package hetzner

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
	pages    [][]map[string]any
}

func newAPI(t *testing.T) (*api, *Client) {
	t.Helper()
	a := &api{t: t, ip: "0.0.0.0", fail: map[string]int{}, failMsg: map[string]string{}}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return a, New("hcloud_test_token", WithBaseURL(srv.URL))
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.auth = append(a.auth, r.Header.Get("Authorization"))
	if code, ok := a.fail[r.URL.Path]; ok {
		msg, ok := a.failMsg[r.URL.Path]
		if !ok {
			msg = "resource_limit_exceeded"
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"code":    msg,
				"message": msg,
			},
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
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.URL.Path == "/locations":
		write(map[string]any{"locations": []map[string]any{
			{"id": 1, "name": "fsn1", "city": "Falkenstein", "country": "DE", "network_zone": "eu-central"},
			{"id": 2, "name": "ash", "city": "Ashburn", "country": "US", "network_zone": "us-east"},
		}})
	case r.URL.Path == "/server_types":
		write(map[string]any{"server_types": []map[string]any{
			{
				"id": 1, "name": "cx22", "cores": 2, "memory": 4.0, "disk": 40,
				"included_traffic": 20000000000000,
				"prices": []map[string]any{
					{
						"location": "fsn1",
						"price_monthly": map[string]any{
							"net":   "3.79",
							"gross": "4.51",
						},
					},
					{
						"location": "ash",
						"price_monthly": map[string]any{
							"net":   "4.29",
							"gross": "5.11",
						},
					},
				},
			},
		}})
	case r.URL.Path == "/ssh_keys" && r.Method == http.MethodGet:
		keys := []map[string]any{}
		for i, k := range a.keys {
			keys = append(keys, map[string]any{
				"id": i + 1, "name": k["name"], "public_key": k["public_key"],
			})
		}
		write(map[string]any{"ssh_keys": keys})
	case r.URL.Path == "/ssh_keys" && r.Method == http.MethodPost:
		a.keys = append(a.keys, body)
		write(map[string]any{"ssh_key": map[string]any{
			"id": len(a.keys), "name": body["name"], "public_key": body["public_key"],
		}})
	case r.URL.Path == "/servers" && r.Method == http.MethodPost:
		a.created = append(a.created, body)
		labels := map[string]string{}
		if l, ok := body["labels"].(map[string]any); ok {
			for k, v := range l {
				if s, ok := v.(string); ok {
					labels[k] = s
				}
			}
		}
		write(map[string]any{"server": map[string]any{
			"id": 1, "name": body["name"], "status": "initializing",
			"server_type": map[string]any{"name": body["server_type"]},
			"datacenter":  map[string]any{"location": map[string]any{"name": body["location"]}},
			"public_net":  map[string]any{"ipv4": map[string]any{"ip": "0.0.0.0"}},
			"labels":      labels,
		}})
	case r.URL.Path == "/servers" && r.Method == http.MethodGet:
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		var rows []map[string]any
		if page-1 < len(a.pages) {
			rows = a.pages[page-1]
		}
		next := 0
		if page < len(a.pages) {
			next = page + 1
		}
		write(map[string]any{
			"servers": rows,
			"meta": map[string]any{
				"pagination": map[string]any{
					"page":      page,
					"per_page":  50,
					"next_page": next,
					"last_page": len(a.pages),
				},
			},
		})
	case r.URL.Path == "/servers/1" && r.Method == http.MethodGet:
		write(map[string]any{"server": map[string]any{
			"id": 1, "name": "nekko-fsn1", "status": "running",
			"server_type": map[string]any{"name": "cx22"},
			"datacenter":  map[string]any{"location": map[string]any{"name": "fsn1"}},
			"public_net":  map[string]any{"ipv4": map[string]any{"ip": a.ip}},
			"labels":      map[string]string{"tag": "nekko-entry"},
		}})
	case r.URL.Path == "/servers/1/actions/reboot" && r.Method == http.MethodPost:
		a.rebooted = append(a.rebooted, "1")
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"action": map[string]any{"id": 100, "status": "running"}})
	case r.URL.Path == "/servers/1" && r.Method == http.MethodDelete:
		a.deleted = append(a.deleted, "1")
		write(map[string]any{"action": map[string]any{"id": 101, "status": "running"}})
	case r.URL.Path == "/servers/gone" && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"error": map[string]string{"code": "not_found", "message": "server not found"}})
	default:
		a.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func TestAccountTokenValidation(t *testing.T) {
	a, c := newAPI(t)
	acct, err := c.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if acct.BalanceCents != nil || acct.PendingCents != 0 {
		t.Errorf("account = %+v, want nil balance / 0 pending", acct)
	}
	if len(a.auth) == 0 || a.auth[0] != "Bearer hcloud_test_token" {
		t.Errorf("auth header = %q", a.auth)
	}
}

func TestPlansCarryThePriceAndTheRegions(t *testing.T) {
	_, c := newAPI(t)
	plans, err := c.Plans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	p := plans[0]
	if p.MonthlyCents != 379 || p.VCPUs != 2 || p.RAMMB != 4096 || len(p.Regions) != 2 {
		t.Errorf("plan = %+v, want 379 cents, 2 vcpus, 4096 ram, 2 regions", p)
	}
	regions, err := c.Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[0].City != "Falkenstein" || regions[0].Country != "DE" || regions[1].Continent != "North America" {
		t.Errorf("regions = %+v", regions)
	}
}

func TestEnsureSSHKeyUploadsOnceAndMatchesOnTheMaterial(t *testing.T) {
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

func TestCreateReturnsNoAddressUntilTheMachineHasOne(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	inst, err := c.Create(ctx, CreateParams{
		Region: "fsn1", Plan: "cx22", Label: "nekko-fsn1", Hostname: "fsn1",
		SSHKeyID: "1", Tags: []string{"nekko-entry"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Address != "" {
		t.Errorf("address %q on a machine that is still pending", inst.Address)
	}
	if got := a.created[0]; got["image"] != "ubuntu-24.04" || got["start_after_create"] != true ||
		got["location"] != "fsn1" || got["name"] != "fsn1" {
		t.Errorf("order body = %v", got)
	}
	if ks, ok := a.created[0]["ssh_keys"].([]any); !ok || len(ks) != 1 || ks[0] != float64(1) {
		t.Errorf("order named ssh keys %v", a.created[0]["ssh_keys"])
	}

	if got, err := c.Instance(ctx, "1"); err != nil || got.Address != "" {
		t.Errorf("instance = %+v, err = %v: 0.0.0.0 is not an address", got, err)
	}
	a.ip = "203.0.113.7"
	got, err := c.Instance(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "203.0.113.7" || got.Status != "running" {
		t.Errorf("instance = %+v", got)
	}
}

func TestRebootAndDelete(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	if err := c.Reboot(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if len(a.rebooted) != 1 || len(a.deleted) != 1 {
		t.Errorf("rebooted %v, deleted %v", a.rebooted, a.deleted)
	}
	if err := c.Delete(ctx, "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a missing instance: err = %v, want ErrNotFound", err)
	}
}

func TestFailuresAreConcrete(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrBadCredentials},
		{http.StatusForbidden, ErrBadCredentials},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusBadRequest, ErrOutOfCapacity},
	} {
		a, c := newAPI(t)
		a.fail["/locations"] = tc.status
		if _, err := c.Account(context.Background()); !errors.Is(err, tc.want) {
			t.Errorf("http %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}

	a, c := newAPI(t)
	a.fail["/locations"] = http.StatusUnauthorized
	a.failMsg["/locations"] = "Unauthorized IP address: 203.0.113.7"
	if _, err := c.Account(context.Background()); !errors.Is(err, ErrBadCredentials) ||
		!strings.Contains(err.Error(), "IP allowlist") || strings.Contains(err.Error(), "203.0.113.7") {
		t.Errorf("401 with reason: err = %v, want safe IP allowlist advice without leaked IP", err)
	}

	a, c = newAPI(t)
	a.fail["/locations"] = http.StatusInternalServerError
	a.failMsg["/locations"] = "internal server error secret-leak"
	_, err := c.Regions(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 500 || strings.Contains(apiErr.Error(), "secret-leak") {
		t.Errorf("err = %v, want status without provider response body", err)
	}
}

func TestInstancesReadsEveryPage(t *testing.T) {
	a, c := newAPI(t)
	a.pages = [][]map[string]any{
		{{"id": 1, "name": "nekko-1", "server_type": map[string]any{"name": "cx22"}, "datacenter": map[string]any{"location": map[string]any{"name": "fsn1"}}, "public_net": map[string]any{"ipv4": map[string]any{"ip": "203.0.113.1"}}}},
		{{"id": 2, "name": "nekko-2", "server_type": map[string]any{"name": "cx22"}, "datacenter": map[string]any{"location": map[string]any{"name": "fsn1"}}, "public_net": map[string]any{"ipv4": map[string]any{"ip": "203.0.113.2"}}}},
		{{"id": 3, "name": "nekko-3", "server_type": map[string]any{"name": "cx22"}, "datacenter": map[string]any{"location": map[string]any{"name": "fsn1"}}, "public_net": map[string]any{"ipv4": map[string]any{"ip": "203.0.113.3"}}}},
	}
	list, err := c.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range list {
		ids = append(ids, i.ID)
	}
	if strings.Join(ids, ",") != "1,2,3" {
		t.Fatalf("instances = %v, want every page", ids)
	}
}

func TestAServerErrorIsNeverARefusal(t *testing.T) {
	for _, msg := range []string{"resource_limit_exceeded", "out of stock", "insufficient funds", "no capacity"} {
		a, c := newAPI(t)
		a.fail["/locations"] = http.StatusServiceUnavailable
		a.failMsg["/locations"] = msg
		_, err := c.Account(context.Background())
		if errors.Is(err, ErrOutOfCapacity) || errors.Is(err, ErrInsufficientFunds) {
			t.Errorf("503 %q: err = %v, want APIError", msg, err)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusServiceUnavailable {
			t.Errorf("503 %q: err = %v, want status kept", msg, err)
		}
	}
}

func TestProviderErrorsNeverCarryAnEgressAddress(t *testing.T) {
	for _, status := range []int{401, 403, 500, 502} {
		err := classify(status, "error_code", "Unauthorized IP address: 203.0.113.7 secret-response")
		if strings.Contains(err.Error(), "203.0.113.7") || strings.Contains(err.Error(), "secret-response") {
			t.Fatalf("response leaked: %v", err)
		}
	}
}

func TestAliasedMethods(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	inst, err := c.CreateInstance(ctx, CreateParams{
		Region: "fsn1", Plan: "cx22", Hostname: "fsn1", SSHKeyID: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != "1" {
		t.Errorf("CreateInstance id = %s", inst.ID)
	}

	got, err := c.GetInstance(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "1" {
		t.Errorf("GetInstance id = %s", got.ID)
	}

	a.pages = [][]map[string]any{
		{{"id": 1, "name": "nekko-1", "server_type": map[string]any{"name": "cx22"}, "datacenter": map[string]any{"location": map[string]any{"name": "fsn1"}}, "public_net": map[string]any{"ipv4": map[string]any{"ip": "203.0.113.1"}}}},
	}
	list, err := c.ListInstances(ctx, "nekko-entry")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("ListInstances len = %d", len(list))
	}

	if err := c.RebootInstance(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteInstance(ctx, "1"); err != nil {
		t.Fatal(err)
	}
}
