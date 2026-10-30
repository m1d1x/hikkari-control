package vultr

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

// api is a fake Vultr: it answers the handful of routes the hub uses and
// records what it was sent, so the tests are about our reading of the wire and
// not about a live account.
type api struct {
	t        *testing.T
	keys     []map[string]any
	created  []map[string]any
	rebooted []string
	deleted  []string
	ip       string
	fail     map[string]int    // path → status
	failMsg  map[string]string // path → the provider's own words; a default when unset
	auth     []string
	// pages is GET /instances, one slice per page, linked by a cursor.
	pages [][]map[string]any
}

func newAPI(t *testing.T) (*api, *Client) {
	t.Helper()
	a := &api{t: t, ip: "0.0.0.0", fail: map[string]int{}, failMsg: map[string]string{}}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return a, New("vk1_test", WithBaseURL(srv.URL))
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.auth = append(a.auth, r.Header.Get("Authorization"))
	if code, ok := a.fail[r.URL.Path]; ok {
		msg, ok := a.failMsg[r.URL.Path]
		if !ok {
			msg = "no available instances in this region"
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
	case r.URL.Path == "/account":
		// Dollars as floats, and one of them is the classic float that must
		// not truncate to 4.99.
		write(map[string]any{"account": map[string]any{"balance": -5.999999, "pending_charges": 2.34}})
	case r.URL.Path == "/regions":
		write(map[string]any{"regions": []map[string]any{
			{"id": "ams", "city": "Amsterdam", "country": "NL"},
		}})
	case r.URL.Path == "/plans":
		write(map[string]any{"plans": []map[string]any{
			{"id": "vc2-1c-1gb", "vcpu_count": 1, "ram": 1024, "disk": 25,
				"bandwidth": 1024, "monthly_cost": 5, "locations": []string{"ams", "fra"}},
		}})
	case r.URL.Path == "/os":
		write(map[string]any{"os": []map[string]any{
			{"id": 1743, "name": "Ubuntu 22.04 LTS x64", "arch": "x64", "family": "ubuntu"},
			{"id": 2284, "name": "Ubuntu 24.04 LTS x64", "arch": "x64", "family": "ubuntu"},
			{"id": 2285, "name": "Ubuntu 24.04 LTS arm64", "arch": "arm64", "family": "ubuntu"},
			{"id": 2300, "name": "Debian 13 x64", "arch": "x64", "family": "debian"},
		}})
	case r.URL.Path == "/ssh-keys" && r.Method == http.MethodGet:
		keys := []map[string]any{}
		for i, k := range a.keys {
			keys = append(keys, map[string]any{
				"id": "key-" + string(rune('a'+i)), "name": k["name"], "ssh_key": k["ssh_key"],
			})
		}
		write(map[string]any{"ssh_keys": keys})
	case r.URL.Path == "/ssh-keys" && r.Method == http.MethodPost:
		a.keys = append(a.keys, body)
		write(map[string]any{"ssh_key": map[string]any{
			"id": "key-" + string(rune('a'+len(a.keys)-1)), "name": body["name"], "ssh_key": body["ssh_key"],
		}})
	case r.URL.Path == "/instances" && r.Method == http.MethodPost:
		a.created = append(a.created, body)
		write(map[string]any{"instance": map[string]any{
			"id": "inst-1", "label": body["label"], "region": body["region"], "plan": body["plan"],
			"status": "pending", "main_ip": "0.0.0.0", "tags": body["tags"],
		}})
	case r.URL.Path == "/instances" && r.Method == http.MethodGet:
		page := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			page, _ = strconv.Atoi(strings.TrimPrefix(c, "page-"))
		}
		next := ""
		if page+1 < len(a.pages) {
			next = "page-" + strconv.Itoa(page+1)
		}
		var rows []map[string]any
		if page < len(a.pages) {
			rows = a.pages[page]
		}
		write(map[string]any{"instances": rows, "meta": map[string]any{
			"total": 0, "links": map[string]any{"next": next, "prev": ""},
		}})
	case r.URL.Path == "/instances/inst-1" && r.Method == http.MethodGet:
		write(map[string]any{"instance": map[string]any{
			"id": "inst-1", "label": "nekko-ams1", "region": "ams", "plan": "vc2-1c-1gb",
			"status": "active", "main_ip": a.ip,
		}})
	case r.URL.Path == "/instances/inst-1" && r.Method == http.MethodDelete:
		a.deleted = append(a.deleted, "inst-1")
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/instances/inst-1/reboot":
		a.rebooted = append(a.rebooted, "inst-1")
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/instances/gone" && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]string{"error": "Instance not found"})
	default:
		a.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

// Money arrives as dollars and is kept as whole cents, rounded: a balance that
// comes over the wire as -5.999999 is six dollars and not $5.99. And the
// balance is read the way Vultr's billing page reads it: negative `balance`
// is prepaid credit, this month's accrual comes off it. The stand's account
// on 25.10 — -6.00 and 2.34 — is $3.66 left, which the console showed as
// "$-6.00".
func TestAccountReadsMoneyAsRoundedCents(t *testing.T) {
	a, c := newAPI(t)
	acct, err := c.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if acct.BalanceCents != 366 || acct.PendingCents != 234 {
		t.Errorf("account = %+v, want 366 left / 234 pending", acct)
	}
	if len(a.auth) == 0 || a.auth[0] != "Bearer vk1_test" {
		t.Errorf("auth header = %q", a.auth)
	}
}

// The catalog is read the way the budget gate needs it: a price in cents and
// the regions the plan is actually sold in.
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
	if p.MonthlyCents != 500 || p.VCPUs != 1 || p.RAMMB != 1024 || len(p.Regions) != 2 {
		t.Errorf("plan = %+v", p)
	}
	regions, err := c.Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 1 || regions[0].City != "Amsterdam" {
		t.Errorf("regions = %+v", regions)
	}
}

// The image is resolved from the hoster's own list, newest 64-bit LTS: an os
// id hardcoded here would go stale every two years and could install the
// wrong distribution without failing.
func TestUbuntuLTSPicksTheNewest64BitImage(t *testing.T) {
	_, c := newAPI(t)
	os, err := c.UbuntuLTS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if os.ID != 2284 {
		t.Errorf("os = %+v, want Ubuntu 24.04 LTS x64", os)
	}
}

// The same key is reused, whatever comment it carries: a second upload would
// leave two keys on the account and an order having to choose.
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

// An order is answered before the machine is built, so the placeholder address
// Vultr returns must not read as an address — the caller polls until there is
// a real one.
func TestCreateReturnsNoAddressUntilTheMachineHasOne(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	inst, err := c.Create(ctx, CreateParams{
		Region: "ams", Plan: "vc2-1c-1gb", Label: "nekko-ams1", Hostname: "ams1",
		SSHKeyID: "key-a", Tags: []string{"nekko-entry"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Address != "" {
		t.Errorf("address %q on a machine that is still pending", inst.Address)
	}
	if got := a.created[0]; got["os_id"] != float64(2284) || got["backups"] != "disabled" ||
		got["enable_ipv6"] != false || got["hostname"] != "ams1" {
		t.Errorf("order body = %v", got)
	}
	if ks, ok := a.created[0]["sshkey_id"].([]any); !ok || len(ks) != 1 || ks[0] != "key-a" {
		t.Errorf("order named ssh keys %v", a.created[0]["sshkey_id"])
	}

	if got, err := c.Instance(ctx, "inst-1"); err != nil || got.Address != "" {
		t.Errorf("instance = %+v, err = %v: 0.0.0.0 is not an address", got, err)
	}
	a.ip = "203.0.113.7"
	got, err := c.Instance(ctx, "inst-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "203.0.113.7" || got.Status != "active" {
		t.Errorf("instance = %+v", got)
	}
}

// Reboot and delete are the two buttons the autopilot presses, and an instance
// that is already gone is reported as such — the adapter is what decides that
// this counts as deleted.
func TestRebootAndDelete(t *testing.T) {
	a, c := newAPI(t)
	ctx := context.Background()
	if err := c.Reboot(ctx, "inst-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "inst-1"); err != nil {
		t.Fatal(err)
	}
	if len(a.rebooted) != 1 || len(a.deleted) != 1 {
		t.Errorf("rebooted %v, deleted %v", a.rebooted, a.deleted)
	}
	if err := c.Delete(ctx, "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a missing instance: err = %v, want ErrNotFound", err)
	}
}

// The failures the owner has to act on are concrete values, not an opaque
// error: a dead key, a region with nothing to sell, a rate limit.
func TestFailuresAreConcrete(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrBadCredentials},
		{http.StatusForbidden, ErrBadCredentials},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusBadRequest, ErrOutOfCapacity}, // the fake's message says so
	} {
		a, c := newAPI(t)
		a.fail["/account"] = tc.status
		if _, err := c.Account(context.Background()); !errors.Is(err, tc.want) {
			t.Errorf("http %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}

	// A rejected key keeps Vultr's reason: a wrong token and an address outside
	// the account's access control are both 401, and they are fixed in
	// different places (found on the stand 25.10).
	a, c := newAPI(t)
	a.fail["/account"] = http.StatusUnauthorized
	a.failMsg["/account"] = "Unauthorized IP address: 203.0.113.7"
	if _, err := c.Account(context.Background()); !errors.Is(err, ErrBadCredentials) ||
		!strings.Contains(err.Error(), "203.0.113.7") {
		t.Errorf("401 with a reason: err = %v, want ErrBadCredentials carrying Vultr's words", err)
	}

	// Anything the client has no sentinel for keeps the provider's own words,
	// because those are what the owner has to act on.
	a, c = newAPI(t)
	a.fail["/regions"] = http.StatusInternalServerError
	a.failMsg["/regions"] = "the datacentre is on fire"
	_, err := c.Regions(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 500 || !strings.Contains(apiErr.Error(), "on fire") {
		t.Errorf("err = %v, want the provider's own words", err)
	}
}

// Audit 42 recheck НС2: the listing is how the hub learns whether an order
// whose answer was lost made a machine, and it read one page of 500. A
// machine on the second page was "not made" and its order left the budget.
func TestInstancesReadsEveryPage(t *testing.T) {
	a, c := newAPI(t)
	a.pages = [][]map[string]any{
		{{"id": "a", "label": "nekko-a1", "main_ip": "203.0.113.1", "tags": []string{"nekko-entry"}}},
		{{"id": "b", "label": "nekko-b1", "main_ip": "203.0.113.2"}},
		{{"id": "c", "label": "nekko-c1", "main_ip": "203.0.113.3"}},
	}
	list, err := c.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range list {
		ids = append(ids, i.ID)
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("instances = %v, want every page", ids)
	}
}

// НН7: the words "capacity" or "payment" are a refusal only when Vultr said
// no — a 4xx. A 5xx is the hoster failing, whatever its body says, and the
// order may have made a machine.
func TestAServerErrorIsNeverARefusal(t *testing.T) {
	for _, msg := range []string{"no capacity", "insufficient funds", "payment required", "not available in ams"} {
		a, c := newAPI(t)
		a.fail["/account"] = http.StatusServiceUnavailable
		a.failMsg["/account"] = msg
		_, err := c.Account(context.Background())
		if errors.Is(err, ErrOutOfCapacity) || errors.Is(err, ErrInsufficientFunds) {
			t.Errorf("503 %q: err = %v, want an APIError", msg, err)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusServiceUnavailable {
			t.Errorf("503 %q: err = %v, want the status kept", msg, err)
		}
	}
}
