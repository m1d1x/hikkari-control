package entrywire

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/digitalocean"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/hetzner"
	"github.com/m1d1x/hikkari-control/internal/vdsina"
)

func TestProvidersKindsIncludesAllHosters(t *testing.T) {
	p := Providers()
	kinds := p.Kinds()

	found := map[string]bool{}
	for _, k := range kinds {
		found[k.Kind] = true
	}

	for _, expected := range []string{"porkbun", "vultr", "vdsina", "hetzner", "digitalocean", "openstack"} {
		if !found[expected] {
			t.Errorf("expected provider kind %q not found in Kinds()", expected)
		}
	}
}

func TestHetznerHosterAdaptsAllMethods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
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
							{"location": "fsn1", "price_monthly": map[string]any{"net": "3.79"}},
						},
					},
				},
			})
		case r.URL.Path == "/ssh_keys" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ssh_keys": []map[string]any{
					{"id": 10, "name": "nekko-hub", "public_key": "ssh-ed25519 AAA123 hub@nekko"},
				},
			})
		case r.URL.Path == "/servers" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"server": map[string]any{
					"id": 101, "name": "ams1", "status": "initializing",
					"server_type": map[string]any{"name": "cx22"},
					"datacenter":  map[string]any{"location": map[string]any{"name": "fsn1"}},
					"public_net":  map[string]any{"ipv4": map[string]any{"ip": "0.0.0.0"}},
				},
			})
		case r.URL.Path == "/servers/101" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"server": map[string]any{
					"id": 101, "name": "ams1", "status": "running",
					"server_type": map[string]any{"name": "cx22"},
					"datacenter":  map[string]any{"location": map[string]any{"name": "fsn1"}},
					"public_net":  map[string]any{"ipv4": map[string]any{"ip": "203.0.113.10"}},
				},
			})
		case r.URL.Path == "/servers" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"servers": []map[string]any{
					{
						"id": 101, "name": "ams1", "status": "running",
						"server_type": map[string]any{"name": "cx22"},
						"datacenter":  map[string]any{"location": map[string]any{"name": "fsn1"}},
						"public_net":  map[string]any{"ipv4": map[string]any{"ip": "203.0.113.10"}},
					},
				},
				"meta": map[string]any{
					"pagination": map[string]any{"page": 1, "next_page": 0, "last_page": 1},
				},
			})
		case r.URL.Path == "/servers/101/actions/reboot":
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/servers/101" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/servers/404":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found"}})
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	bp, err := Providers().Hosting("hetzner", map[string]string{"api_token": "test-tok"})
	if err != nil {
		t.Fatal(err)
	}

	// Use test server base URL
	client := hetzner.New("test-tok", hetzner.WithBaseURL(srv.URL))
	bp = HetznerHoster(client)
	ctx := context.Background()

	// 1. Account
	acct, err := bp.Account(ctx)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acct.BalanceCents != nil {
		t.Errorf("acct.BalanceCents = %v, want nil", acct.BalanceCents)
	}

	// 2. Regions
	regions, err := bp.Regions(ctx)
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(regions) != 1 || regions[0].ID != "fsn1" {
		t.Errorf("regions = %+v", regions)
	}

	// 3. Plans
	plans, err := bp.Plans(ctx)
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(plans) != 1 || plans[0].ID != "cx22" || plans[0].MonthlyCents != 379 {
		t.Errorf("plans = %+v", plans)
	}

	// 4. SSH Key
	keyID, err := bp.EnsureSSHKey(ctx, "nekko-hub", "ssh-ed25519 AAA123 other@host")
	if err != nil {
		t.Fatalf("EnsureSSHKey: %v", err)
	}
	if keyID != "10" {
		t.Errorf("keyID = %q, want 10", keyID)
	}

	// 5. Create
	inst, err := bp.Create(ctx, entry.BoxOrder{
		Region: "fsn1", Plan: "cx22", Label: "nekko-ams1", Hostname: "ams1",
		SSHKeyID: keyID, Tag: "nekko-entry",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst.ID != "101" || inst.Address != "" {
		t.Errorf("inst = %+v", inst)
	}

	// 6. Instance
	got, err := bp.Instance(ctx, "101")
	if err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if got.Address != "203.0.113.10" {
		t.Errorf("got = %+v", got)
	}

	// 7. Instances
	all, err := bp.Instances(ctx)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(all) != 1 || all[0].ID != "101" {
		t.Errorf("all = %+v", all)
	}

	// 8. Reboot & Delete
	if err := bp.Reboot(ctx, "101"); err != nil {
		t.Errorf("Reboot: %v", err)
	}
	if err := bp.Delete(ctx, "101"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	// Delete already gone is not error
	if err := bp.Delete(ctx, "404"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
	// Instance missing returns ErrNoSuchInstance
	if _, err := bp.Instance(ctx, "404"); !errors.Is(err, entry.ErrNoSuchInstance) {
		t.Errorf("Instance missing: err = %v, want ErrNoSuchInstance", err)
	}
}

func TestDigitalOceanHosterAdaptsAllMethods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/customers/my/balance":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"month_to_date_balance": "0.00",
				"account_balance":       "-10.00",
				"month_to_date_usage":   "2.00",
			})
		case r.URL.Path == "/regions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"regions": []map[string]any{
					{"name": "Amsterdam 3", "slug": "ams3", "sizes": []string{"s-1vcpu-1gb"}, "available": true},
				},
			})
		case r.URL.Path == "/sizes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sizes": []map[string]any{
					{
						"slug": "s-1vcpu-1gb", "vcpus": 1, "memory": 1024, "disk": 25,
						"transfer": 1.0, "price_monthly": 6.0, "regions": []string{"ams3"}, "available": true,
					},
				},
			})
		case r.URL.Path == "/account/keys" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ssh_keys": []map[string]any{
					{"id": 20, "name": "nekko-hub", "public_key": "ssh-ed25519 AAA123 hub@nekko"},
				},
			})
		case r.URL.Path == "/droplets" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"droplet": map[string]any{
					"id": 202, "name": "ams3", "status": "new", "size_slug": "s-1vcpu-1gb",
					"region":   map[string]any{"slug": "ams3"},
					"networks": map[string]any{"v4": []any{}},
				},
			})
		case r.URL.Path == "/droplets/202" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"droplet": map[string]any{
					"id": 202, "name": "ams3", "status": "active", "size_slug": "s-1vcpu-1gb",
					"region": map[string]any{"slug": "ams3"},
					"networks": map[string]any{
						"v4": []map[string]any{
							{"ip_address": "203.0.113.20", "type": "public"},
						},
					},
				},
			})
		case r.URL.Path == "/droplets" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"droplets": []map[string]any{
					{
						"id": 202, "name": "ams3", "status": "active", "size_slug": "s-1vcpu-1gb",
						"region": map[string]any{"slug": "ams3"},
						"networks": map[string]any{
							"v4": []map[string]any{
								{"ip_address": "203.0.113.20", "type": "public"},
							},
						},
					},
				},
				"links": map[string]any{"pages": map[string]any{}},
			})
		case r.URL.Path == "/droplets/202/actions":
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/droplets/202" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/droplets/404":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "not_found"})
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	bp, err := Providers().Hosting("digitalocean", map[string]string{"api_token": "test-tok"})
	if err != nil {
		t.Fatal(err)
	}

	client := digitalocean.New("test-tok", digitalocean.WithBaseURL(srv.URL))
	bp = DigitalOceanHoster(client)
	ctx := context.Background()

	// 1. Account
	acct, err := bp.Account(ctx)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acct.BalanceCents == nil || *acct.BalanceCents != 800 || acct.PendingCents != 200 {
		t.Errorf("acct = %+v, want 800 balance / 200 pending", acct)
	}

	// 2. Regions
	regions, err := bp.Regions(ctx)
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(regions) != 1 || regions[0].ID != "ams3" {
		t.Errorf("regions = %+v", regions)
	}

	// 3. Plans
	plans, err := bp.Plans(ctx)
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(plans) != 1 || plans[0].ID != "s-1vcpu-1gb" || plans[0].MonthlyCents != 600 {
		t.Errorf("plans = %+v", plans)
	}

	// 4. SSH Key
	keyID, err := bp.EnsureSSHKey(ctx, "nekko-hub", "ssh-ed25519 AAA123 other@host")
	if err != nil {
		t.Fatalf("EnsureSSHKey: %v", err)
	}
	if keyID != "20" {
		t.Errorf("keyID = %q, want 20", keyID)
	}

	// 5. Create
	inst, err := bp.Create(ctx, entry.BoxOrder{
		Region: "ams3", Plan: "s-1vcpu-1gb", Label: "nekko-ams3", Hostname: "ams3",
		SSHKeyID: keyID, Tag: "nekko-entry",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst.ID != "202" || inst.Address != "" {
		t.Errorf("inst = %+v", inst)
	}

	// 6. Instance
	got, err := bp.Instance(ctx, "202")
	if err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if got.Address != "203.0.113.20" {
		t.Errorf("got = %+v", got)
	}

	// 7. Instances
	all, err := bp.Instances(ctx)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(all) != 1 || all[0].ID != "202" {
		t.Errorf("all = %+v", all)
	}

	// 8. Reboot & Delete
	if err := bp.Reboot(ctx, "202"); err != nil {
		t.Errorf("Reboot: %v", err)
	}
	if err := bp.Delete(ctx, "202"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := bp.Delete(ctx, "404"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
	if _, err := bp.Instance(ctx, "404"); !errors.Is(err, entry.ErrNoSuchInstance) {
		t.Errorf("Instance missing: err = %v, want ErrNoSuchInstance", err)
	}
}

func TestVDSinaHosterAdaptsAllMethods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"status_msg": "ok",
				"data":       data,
			})
		}
		switch {
		case r.URL.Path == "/account.balance":
			write(map[string]any{
				"real":  1500.0,
				"bonus": 500.0,
			})
		case r.URL.Path == "/datacenter":
			write([]map[string]any{
				{"id": 4, "name": "Амстердам, Нидерланды", "city": "Амстердам", "country": "NL", "active": 1},
			})
		case r.URL.Path == "/server-plan":
			write([]map[string]any{
				{
					"id": 1, "name": "Стандарт-1", "cost": 10.0, "period": "day",
					"cpu": 1, "ram": 1024, "disk": 30, "enable": 1, "active": 1,
					"data": map[string]any{"datacenter": []int{4}},
				},
			})
		case r.URL.Path == "/template":
			write([]map[string]any{
				{"id": 23, "name": "Ubuntu 24.04 x64", "ssh-key": true},
			})
		case r.URL.Path == "/ssh-key" && r.Method == http.MethodGet:
			write([]map[string]any{
				{"id": 30, "name": "nekko-hub", "data": "ssh-ed25519 AAA123 hub@nekko"},
			})
		case r.URL.Path == "/server" && r.Method == http.MethodPost:
			write(map[string]any{
				"id": 303, "name": "ams1", "status": "new", "ip": "0.0.0.0",
				"datacenter": 4, "server_plan": 1,
			})
		case r.URL.Path == "/server/303" && r.Method == http.MethodGet:
			write(map[string]any{
				"id": 303, "name": "ams1", "status": "active", "ip": "185.123.45.67",
				"datacenter": 4, "server_plan": 1,
			})
		case r.URL.Path == "/server" && r.Method == http.MethodGet:
			write([]map[string]any{
				{
					"id": 303, "name": "ams1", "status": "active", "ip": "185.123.45.67",
					"datacenter": 4, "server_plan": 1,
				},
			})
		case r.URL.Path == "/server.reboot/303":
			write(map[string]any{"status": "ok"})
		case r.URL.Path == "/server/303" && r.Method == http.MethodDelete:
			write(map[string]any{"status": "ok"})
		case r.URL.Path == "/server/404":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "status_msg": "not found"})
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	bp, err := Providers().Hosting("vdsina", map[string]string{"api_token": "test-tok"})
	if err != nil {
		t.Fatal(err)
	}

	client := vdsina.New("test-tok", vdsina.WithBaseURL(srv.URL))
	bp = VDSinaHoster(client)
	ctx := context.Background()

	// 1. Account
	acct, err := bp.Account(ctx)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	// (1500 + 500) * 100 = 200000 cents
	if acct.BalanceCents == nil || *acct.BalanceCents != 200000 || acct.PendingCents != 0 {
		t.Errorf("acct = %+v, want 200000 balance / 0 pending", acct)
	}

	// 2. Regions
	regions, err := bp.Regions(ctx)
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(regions) != 1 || regions[0].ID != "4" {
		t.Errorf("regions = %+v", regions)
	}

	// 3. Plans
	plans, err := bp.Plans(ctx)
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	// 10/day * 30 * 100 = 30000 cents
	if len(plans) != 1 || plans[0].ID != "1" || plans[0].MonthlyCents != 30000 {
		t.Errorf("plans = %+v", plans)
	}

	// 4. SSH Key
	keyID, err := bp.EnsureSSHKey(ctx, "nekko-hub", "ssh-ed25519 AAA123 other@host")
	if err != nil {
		t.Fatalf("EnsureSSHKey: %v", err)
	}
	if keyID != "30" {
		t.Errorf("keyID = %q, want 30", keyID)
	}

	// 5. Create
	inst, err := bp.Create(ctx, entry.BoxOrder{
		Region: "4", Plan: "1", Label: "nekko-ams1", Hostname: "ams1",
		SSHKeyID: keyID, Tag: "nekko-entry",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst.ID != "303" || inst.Address != "" {
		t.Errorf("inst = %+v", inst)
	}

	// 6. Instance
	got, err := bp.Instance(ctx, "303")
	if err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if got.Address != "185.123.45.67" {
		t.Errorf("got = %+v", got)
	}

	// 7. Instances
	all, err := bp.Instances(ctx)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(all) != 1 || all[0].ID != "303" {
		t.Errorf("all = %+v", all)
	}

	// 8. Reboot & Delete
	if err := bp.Reboot(ctx, "303"); err != nil {
		t.Errorf("Reboot: %v", err)
	}
	if err := bp.Delete(ctx, "303"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := bp.Delete(ctx, "404"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
	if _, err := bp.Instance(ctx, "404"); !errors.Is(err, entry.ErrNoSuchInstance) {
		t.Errorf("Instance missing: err = %v, want ErrNoSuchInstance", err)
	}
}

func TestOpenStackHosterAdaptsAllMethods(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v3/auth/tokens":
			w.Header().Set("X-Subject-Token", "mock-token-xyz")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": map[string]any{
					"expires_at": "2030-01-01T00:00:00Z",
					"catalog": []map[string]any{
						{
							"type": "compute",
							"endpoints": []map[string]any{
								{"interface": "public", "region": "RegionOne", "url": srvURL + "/compute"},
							},
						},
					},
				},
			})
		case r.URL.Path == "/compute/flavors/detail":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"flavors": []map[string]any{
					{"id": "flavor-1", "name": "m1.small", "vcpus": 1, "ram": 2048, "disk": 20},
				},
			})
		case r.URL.Path == "/compute/os-keypairs" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keypairs": []map[string]any{
					{"keypair": map[string]any{"name": "nekko-key", "public_key": "ssh-ed25519 AAA123"}},
				},
			})
		case r.URL.Path == "/compute/servers" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"server": map[string]any{
					"id": "srv-999", "name": "ams1", "status": "BUILD",
					"flavor": map[string]any{"id": "flavor-1"},
				},
			})
		case r.URL.Path == "/compute/servers/srv-999" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"server": map[string]any{
					"id": "srv-999", "name": "ams1", "status": "ACTIVE",
					"flavor": map[string]any{"id": "flavor-1"},
					"addresses": map[string]any{
						"public": []map[string]any{
							{"addr": "198.51.100.99", "version": 4, "OS-EXT-IPS:type": "floating"},
						},
					},
				},
			})
		case r.URL.Path == "/compute/servers/detail" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"servers": []map[string]any{
					{
						"id": "srv-999", "name": "ams1", "status": "ACTIVE",
						"flavor": map[string]any{"id": "flavor-1"},
						"addresses": map[string]any{
							"public": []map[string]any{
								{"addr": "198.51.100.99", "version": 4, "OS-EXT-IPS:type": "floating"},
							},
						},
					},
				},
			})
		case r.URL.Path == "/compute/servers/srv-999/action" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == "/compute/servers/srv-999" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/compute/servers/404":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"itemNotFound": map[string]string{"message": "not found"}})
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	p := Providers()
	bp, err := p.Hosting("openstack", map[string]string{
		"auth_url":        srv.URL + "/v3",
		"app_cred_id":     "app-id",
		"app_cred_secret": "app-secret",
		"region":          "RegionOne",
	})
	if err != nil {
		t.Fatalf("Hosting: %v", err)
	}

	ctx := context.Background()

	// 1. Account
	acct, err := bp.Account(ctx)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acct.BalanceCents != nil {
		t.Errorf("OpenStack balance = %v, want nil", acct.BalanceCents)
	}

	// 2. Regions
	regions, err := bp.Regions(ctx)
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(regions) != 1 || regions[0].ID != "RegionOne" {
		t.Errorf("regions = %+v", regions)
	}

	// 3. Plans
	plans, err := bp.Plans(ctx)
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(plans) != 1 || plans[0].ID != "flavor-1" {
		t.Errorf("plans = %+v", plans)
	}

	// 4. SSH Key
	keyID, err := bp.EnsureSSHKey(ctx, "nekko-key", "ssh-ed25519 AAA123 other@host")
	if err != nil {
		t.Fatalf("EnsureSSHKey: %v", err)
	}
	if keyID != "nekko-key" {
		t.Errorf("keyID = %q, want nekko-key", keyID)
	}

	// 5. Create
	inst, err := bp.Create(ctx, entry.BoxOrder{
		Region: "RegionOne", Plan: "flavor-1", Label: "nekko-ams1", Hostname: "ams1",
		SSHKeyID: keyID, Tag: "nekko-entry",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst.ID != "srv-999" {
		t.Errorf("inst = %+v", inst)
	}

	// 6. Instance
	got, err := bp.Instance(ctx, "srv-999")
	if err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if got.Address != "198.51.100.99" {
		t.Errorf("got = %+v", got)
	}

	// 7. Instances
	all, err := bp.Instances(ctx)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(all) != 1 || all[0].ID != "srv-999" {
		t.Errorf("all = %+v", all)
	}

	// 8. Reboot & Delete
	if err := bp.Reboot(ctx, "srv-999"); err != nil {
		t.Errorf("Reboot: %v", err)
	}
	if err := bp.Delete(ctx, "srv-999"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := bp.Delete(ctx, "404"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
	if _, err := bp.Instance(ctx, "404"); !errors.Is(err, entry.ErrNoSuchInstance) {
		t.Errorf("Instance missing: err = %v, want ErrNoSuchInstance", err)
	}
}
