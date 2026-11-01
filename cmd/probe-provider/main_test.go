package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func captureOutput(f func()) string {
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w

	outC := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		outC <- buf.String()
	}()

	f()

	_ = w.Close()
	os.Stdout = oldStdout
	out := <-outC
	_ = r.Close()
	return out
}

func TestResolveToken(t *testing.T) {
	t.Setenv("TEST_ENV_VAR", "from_env")

	// Specific flag wins
	if got := resolveToken("from_specific", "from_key", "from_token", "TEST_ENV_VAR"); got != "from_specific" {
		t.Fatalf("expected from_specific, got %q", got)
	}

	// Key wins over token and env
	if got := resolveToken("", "from_key", "from_token", "TEST_ENV_VAR"); got != "from_key" {
		t.Fatalf("expected from_key, got %q", got)
	}

	// Token wins over env
	if got := resolveToken("", "", "from_token", "TEST_ENV_VAR"); got != "from_token" {
		t.Fatalf("expected from_token, got %q", got)
	}

	// Env fallback
	if got := resolveToken("", "", "", "TEST_ENV_VAR"); got != "from_env" {
		t.Fatalf("expected from_env, got %q", got)
	}

	// Missing env
	if got := resolveToken("", "", "", "NON_EXISTENT_VAR"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestResolveDOToken(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "primary_do")
	t.Setenv("DO_TOKEN", "fallback_do")

	if got := resolveDOToken("", "", ""); got != "primary_do" {
		t.Fatalf("expected primary_do, got %q", got)
	}

	t.Setenv("DIGITALOCEAN_TOKEN", "")
	if got := resolveDOToken("", "", ""); got != "fallback_do" {
		t.Fatalf("expected fallback_do, got %q", got)
	}

	if got := resolveDOToken("flag_token", "", ""); got != "flag_token" {
		t.Fatalf("expected flag_token, got %q", got)
	}
}

func TestDetectProvider(t *testing.T) {
	// Single env in shell -> auto-detect
	cases := []struct {
		envKey string
		want   string
	}{
		{"VULTR_API_KEY", "vultr"},
		{"HETZNER_TOKEN", "hetzner"},
		{"DIGITALOCEAN_TOKEN", "digitalocean"},
		{"DO_TOKEN", "digitalocean"},
		{"VDSINA_TOKEN", "vdsina"},
		{"OPENSTACK_AUTH_URL", "openstack"},
		{"PORKBUN_API_KEY", "porkbun"},
		{"PORKBUN_SECRET_KEY", "porkbun"},
	}

	for _, tc := range cases {
		m := map[string]string{tc.envKey: "token123"}
		if got := detectProvider(m); got != tc.want {
			t.Errorf("detectProvider(%s) = %q; want %q", tc.envKey, got, tc.want)
		}
	}

	// Multiple in shell -> ambiguous, returns empty
	multi := map[string]string{
		"VULTR_API_KEY": "k1",
		"HETZNER_TOKEN": "k2",
	}
	if got := detectProvider(multi); got != "" {
		t.Errorf("expected empty for multi-provider, got %q", got)
	}
}

func TestProbeVultr_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/account":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"account": map[string]any{"balance": -10.50, "pending_charges": 2.00},
			})
		case "/regions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"regions": []map[string]any{
					{"id": "ams", "city": "Amsterdam", "country": "NL", "continent": "Europe"},
				},
			})
		case "/plans":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"plans": []map[string]any{
					{"id": "vc2-1c-1gb", "vcpu_count": 1, "ram": 1024, "disk": 25, "bandwidth": 1000, "monthly_cost": 5.00, "locations": []string{"ams"}},
				},
			})
		case "/instances":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"instances": []map[string]any{
					{"id": "sub-1", "label": "test-box", "main_ip": "1.2.3.4", "status": "active"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probeVultr(context.Background(), "test-key")
	})

	if !strings.Contains(out, "[+] Auth OK! Balance: $8.50 (850 cents)") {
		t.Errorf("expected Auth OK balance, got:\n%s", out)
	}
	if !strings.Contains(out, "[+] Discovered 1 Datacenters/Regions:") {
		t.Errorf("expected 1 region, got:\n%s", out)
	}
	if !strings.Contains(out, "[ams] Amsterdam (NL, Europe)") {
		t.Errorf("expected ams region details, got:\n%s", out)
	}
	if !strings.Contains(out, "Plan ID: vc2-1c-1gb") {
		t.Errorf("expected plan id, got:\n%s", out)
	}
	if !strings.Contains(out, "Existing servers on account: 1") {
		t.Errorf("expected 1 instance, got:\n%s", out)
	}
	if !strings.Contains(out, "[SUCCESS] Vultr credentials verified successfully!") {
		t.Errorf("expected success message, got:\n%s", out)
	}
}

func TestProbeVultr_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "Invalid API key"})
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probeVultr(context.Background(), "invalid-key")
	})

	if !strings.Contains(out, "Account check failed") {
		t.Errorf("expected account check failed, got:\n%s", out)
	}
}

func TestProbeHetzner_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/locations" && r.URL.Query().Get("per_page") == "1":
			_ = json.NewEncoder(w).Encode(map[string]any{"locations": []map[string]any{}})
		case r.URL.Path == "/locations":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"locations": []map[string]any{
					{"id": 1, "name": "fsn1", "city": "Falkenstein", "country": "DE", "network_zone": "eu-central"},
				},
			})
		case r.URL.Path == "/server_types":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"server_types": []map[string]any{
					{
						"id": 1, "name": "cx22", "cores": 2, "memory": 4.0, "disk": 40,
						"prices": []map[string]any{
							{"location": "fsn1", "price_monthly": map[string]any{"net": "3.29", "gross": "3.92"}},
						},
					},
				},
			})
		case r.URL.Path == "/servers":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"servers": []map[string]any{
					{
						"id": 1, "name": "box-1", "status": "running",
						"server_type": map[string]any{"name": "cx22"},
						"datacenter":  map[string]any{"location": map[string]any{"name": "fsn1"}},
						"public_net":  map[string]any{"ipv4": map[string]any{"ip": "2.3.4.5"}},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probeHetzner(context.Background(), "test-token")
	})

	if !strings.Contains(out, "[+] Auth OK! Balance: no balance endpoint (post-paid / invoice)") {
		t.Errorf("expected Auth OK balance, got:\n%s", out)
	}
	if !strings.Contains(out, "[+] Discovered 1 Datacenters/Regions:") {
		t.Errorf("expected 1 region, got:\n%s", out)
	}
	if !strings.Contains(out, "[fsn1] Falkenstein (DE, Europe)") {
		t.Errorf("expected fsn1 details, got:\n%s", out)
	}
	if !strings.Contains(out, "Plan ID: cx22") {
		t.Errorf("expected plan id, got:\n%s", out)
	}
	if !strings.Contains(out, "Existing servers on account: 1") {
		t.Errorf("expected 1 instance, got:\n%s", out)
	}
	if !strings.Contains(out, "[SUCCESS] Hetzner Cloud credentials verified successfully!") {
		t.Errorf("expected success message, got:\n%s", out)
	}
}

func TestProbeHetzner_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "unauthorized", "message": "unable to authenticate"},
		})
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probeHetzner(context.Background(), "invalid-token")
	})

	if !strings.Contains(out, "Account check failed") {
		t.Errorf("expected account check failed, got:\n%s", out)
	}
}

func TestProbeDigitalOcean_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/customers/my/balance":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"account_balance":     "-12.50",
				"month_to_date_usage": "2.50",
			})
		case "/regions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"regions": []map[string]any{
					{"name": "Amsterdam 3", "slug": "ams3", "available": true},
				},
			})
		case "/sizes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sizes": []map[string]any{
					{"slug": "s-1vcpu-1gb", "vcpus": 1, "memory": 1024, "disk": 25, "price_monthly": 6.00, "regions": []string{"ams3"}, "available": true},
				},
			})
		case "/droplets":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"droplets": []map[string]any{
					{
						"id": 1, "name": "box-1", "status": "active",
						"size_slug": "s-1vcpu-1gb",
						"region":    map[string]any{"slug": "ams3"},
						"networks": map[string]any{
							"v4": []map[string]any{
								{"ip_address": "3.4.5.6", "type": "public"},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probeDigitalOcean(context.Background(), "test-token")
	})

	if !strings.Contains(out, "[+] Auth OK! Balance: $10.00 (1000 cents)") {
		t.Errorf("expected Auth OK balance, got:\n%s", out)
	}
	if !strings.Contains(out, "[+] Discovered 1 Datacenters/Regions:") {
		t.Errorf("expected 1 region, got:\n%s", out)
	}
	if !strings.Contains(out, "[ams3] Amsterdam (NL, Europe)") {
		t.Errorf("expected ams3 details, got:\n%s", out)
	}
	if !strings.Contains(out, "Plan ID: s-1vcpu-1gb") {
		t.Errorf("expected plan id, got:\n%s", out)
	}
	if !strings.Contains(out, "Existing servers on account: 1") {
		t.Errorf("expected 1 instance, got:\n%s", out)
	}
	if !strings.Contains(out, "[SUCCESS] DigitalOcean credentials verified successfully!") {
		t.Errorf("expected success message, got:\n%s", out)
	}
}

func TestProbeDigitalOcean_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "unauthorized", "message": "Bad credentials"})
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probeDigitalOcean(context.Background(), "invalid-token")
	})

	if !strings.Contains(out, "Account check failed") {
		t.Errorf("expected account check failed, got:\n%s", out)
	}
}

func TestProbePorkbun_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ping":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "SUCCESS", "yourIp": "198.51.100.42",
			})
		case "/account/balance":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "SUCCESS", "balance": 2550,
			})
		case "/account/apiSettings":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "SUCCESS", "monthlySpend": 450,
				"spendLimit": map[string]any{
					"limit": 15000, "source": "user", "spent": 450,
				},
			})
		case "/domain/listAll":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "SUCCESS", "count": 1,
				"domains": []map[string]any{
					{
						"domain": "testrelay.lol", "tld": "lol",
						"status": "ACTIVE", "expireDate": "2027-10-31 12:00:00",
						"apiAccess": 1,
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probePorkbun(context.Background(), "pk1_test", "sk1_test", srv.URL)
	})

	if !strings.Contains(out, "[+] Auth OK! Connected from IP: 198.51.100.42") {
		t.Errorf("expected Auth OK IP, got:\n%s", out)
	}
	if !strings.Contains(out, "[+] Prepaid Balance: $25.50 (2550 cents)") {
		t.Errorf("expected balance, got:\n%s", out)
	}
	if !strings.Contains(out, "[+] Monthly Spend Limit: $150.00 (Spent this month: $4.50)") {
		t.Errorf("expected spend limit, got:\n%s", out)
	}
	if !strings.Contains(out, "Existing domains on account: 1") {
		t.Errorf("expected 1 domain, got:\n%s", out)
	}
	if !strings.Contains(out, "testrelay.lol (Status: ACTIVE") {
		t.Errorf("expected testrelay.lol details, got:\n%s", out)
	}
	if !strings.Contains(out, "[SUCCESS] Porkbun credentials verified successfully!") {
		t.Errorf("expected success message, got:\n%s", out)
	}
}

func TestProbePorkbun_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ERROR", "message": "Invalid API key or secret key.",
		})
	}))
	defer srv.Close()

	origAPIURL := *apiURL
	*apiURL = srv.URL
	defer func() { *apiURL = origAPIURL }()

	out := captureOutput(func() {
		probePorkbun(context.Background(), "pk1_bad", "sk1_bad", srv.URL)
	})

	if !strings.Contains(out, "[-] Ping / Auth failed: porkbun: credentials rejected") {
		t.Errorf("expected ping / auth failed, got:\n%s", out)
	}
	if !strings.Contains(out, "Check that both API key (pk1_…) and Secret key (sk1_…) are correct") {
		t.Errorf("expected hint, got:\n%s", out)
	}
}
