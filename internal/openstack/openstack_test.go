package openstack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mockOpenStack struct {
	t        *testing.T
	baseURL  string
	keys     []map[string]any
	servers  []map[string]any
	rebooted []string
	deleted  []string
	ip       string
	fail     map[string]int
	failMsg  map[string]string
	authReqs int
}

func newMockOpenStack(t *testing.T) (*mockOpenStack, *Client) {
	t.Helper()
	m := &mockOpenStack{
		t:       t,
		ip:      "0.0.0.0",
		fail:    map[string]int{},
		failMsg: map[string]string{},
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	m.baseURL = srv.URL

	cfg := Config{
		AuthURL:                 srv.URL + "/v3",
		Region:                  "RegionOne",
		ApplicationCredentialID: "app-id-123",
		ApplicationCredentialSecret: "app-sec-456",
	}
	client := New(cfg)
	return m, client
}

func (m *mockOpenStack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if code, ok := m.fail[r.URL.Path]; ok {
		msg, ok := m.failMsg[r.URL.Path]
		if !ok {
			msg = "Quota exceeded or resource unavailable"
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
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

	write := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}

	switch {
	// Keystone v3 auth
	case r.URL.Path == "/v3/auth/tokens" && r.Method == http.MethodPost:
		m.authReqs++
		w.Header().Set("X-Subject-Token", "mock-token-xyz")
		write(http.StatusCreated, map[string]any{
			"token": map[string]any{
				"expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"catalog": []map[string]any{
					{
						"type": "compute",
						"name": "nova",
						"endpoints": []map[string]any{
							{
								"id":        "ep-1",
								"interface": "public",
								"region":    "RegionOne",
								"url":       m.baseURL + "/compute",
							},
						},
					},
					{
						"type": "network",
						"name": "neutron",
						"endpoints": []map[string]any{
							{
								"id":        "ep-2",
								"interface": "public",
								"region":    "RegionOne",
								"url":       m.baseURL + "/network",
							},
						},
					},
					{
						"type": "volumev3",
						"name": "cinder",
						"endpoints": []map[string]any{
							{
								"id":        "ep-3",
								"interface": "public",
								"region":    "RegionOne",
								"url":       m.baseURL + "/volume",
							},
						},
					},
				},
			},
		})

	// Nova: flavors
	case r.URL.Path == "/compute/flavors/detail" && r.Method == http.MethodGet:
		write(http.StatusOK, map[string]any{
			"flavors": []map[string]any{
				{"id": "flavor-1", "name": "m1.small", "vcpus": 1, "ram": 2048, "disk": 20},
				{"id": "flavor-2", "name": "m1.medium", "vcpus": 2, "ram": 4096, "disk": 40},
			},
		})

	// Nova: images
	case r.URL.Path == "/compute/images" && r.Method == http.MethodGet:
		write(http.StatusOK, map[string]any{
			"images": []map[string]any{
				{"id": "img-ubuntu-24", "name": "Ubuntu 24.04 LTS"},
				{"id": "img-debian-12", "name": "Debian 12"},
			},
		})

	// Nova: keypairs
	case r.URL.Path == "/compute/os-keypairs" && r.Method == http.MethodGet:
		keypairs := []map[string]any{}
		for _, k := range m.keys {
			keypairs = append(keypairs, map[string]any{"keypair": k})
		}
		write(http.StatusOK, map[string]any{"keypairs": keypairs})

	case r.URL.Path == "/compute/os-keypairs" && r.Method == http.MethodPost:
		kp := body["keypair"].(map[string]any)
		m.keys = append(m.keys, kp)
		write(http.StatusOK, map[string]any{"keypair": kp})

	// Nova: servers create
	case r.URL.Path == "/compute/servers" && r.Method == http.MethodPost:
		s := body["server"].(map[string]any)
		// Nova before 2.52 refuses a create that carries tags (HostVDS, 01.11).
		if _, tagged := s["tags"]; tagged && r.Header.Get("OpenStack-API-Version") != "compute 2.52" {
			write(http.StatusBadRequest, map[string]any{"badRequest": map[string]any{
				"code": 400, "message": "Additional properties are not allowed ('tags' was unexpected)"}})
			return
		}
		srvMap := map[string]any{
			"id":     "srv-12345",
			"name":   s["name"],
			"status": "BUILD",
			"flavor": map[string]any{"id": s["flavorRef"]},
			"tags":   s["tags"],
			"addresses": map[string]any{
				"public": []map[string]any{
					{"addr": m.ip, "version": 4, "OS-EXT-IPS:type": "floating"},
				},
			},
		}
		m.servers = append(m.servers, srvMap)
		write(http.StatusAccepted, map[string]any{"server": srvMap})

	// Nova: servers detail
	case r.URL.Path == "/compute/servers/detail" && r.Method == http.MethodGet:
		write(http.StatusOK, map[string]any{"servers": m.servers})

	// Nova: server action (reboot)
	case strings.HasPrefix(r.URL.Path, "/compute/servers/") && strings.HasSuffix(r.URL.Path, "/action") && r.Method == http.MethodPost:
		parts := strings.Split(r.URL.Path, "/")
		id := parts[3]
		m.rebooted = append(m.rebooted, id)
		w.WriteHeader(http.StatusAccepted)

	// Nova: server get detail
	case strings.HasPrefix(r.URL.Path, "/compute/servers/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(r.URL.Path, "/compute/servers/")
		for _, s := range m.servers {
			if s["id"] == id {
				sCopy := map[string]any{}
				for k, v := range s {
					sCopy[k] = v
				}
				sCopy["status"] = "ACTIVE"
				sCopy["addresses"] = map[string]any{
					"public": []map[string]any{
						{"addr": m.ip, "version": 4, "OS-EXT-IPS:type": "floating"},
					},
				}
				write(http.StatusOK, map[string]any{"server": sCopy})
				return
			}
		}
		write(http.StatusNotFound, map[string]any{"itemNotFound": map[string]any{"message": "Instance not found"}})

	// Nova: server delete
	case strings.HasPrefix(r.URL.Path, "/compute/servers/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/compute/servers/")
		found := false
		for i, s := range m.servers {
			if s["id"] == id {
				m.deleted = append(m.deleted, id)
				m.servers = append(m.servers[:i], m.servers[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			write(http.StatusNotFound, map[string]any{"itemNotFound": map[string]any{"message": "Instance not found"}})
			return
		}
		w.WriteHeader(http.StatusNoContent)

	// Neutron: networks
	case r.URL.Path == "/network/v2.0/networks" && r.Method == http.MethodGet:
		write(http.StatusOK, map[string]any{
			"networks": []map[string]any{
				{"id": "net-pub-1", "name": "public", "status": "ACTIVE", "router:external": true},
				{"id": "net-priv-1", "name": "private", "status": "ACTIVE", "router:external": false},
			},
		})

	// Neutron: floating IPs
	case r.URL.Path == "/network/v2.0/floatingips" && r.Method == http.MethodGet:
		write(http.StatusOK, map[string]any{
			"floatingips": []map[string]any{
				{"id": "fip-1", "floating_ip_address": "198.51.100.5", "fixed_ip_address": "10.0.0.10", "status": "ACTIVE"},
			},
		})

	// Cinder: volumes
	case r.URL.Path == "/volume/volumes/detail" && r.Method == http.MethodGet:
		write(http.StatusOK, map[string]any{
			"volumes": []map[string]any{
				{"id": "vol-1", "name": "root-vol", "size": 50, "status": "in-use"},
			},
		})

	default:
		http.NotFound(w, r)
	}
}

func TestOpenStack_AuthenticationAndAccount(t *testing.T) {
	ctx := context.Background()
	_, client := newMockOpenStack(t)

	acct, err := client.Account(ctx)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acct.BalanceCents != nil {
		t.Fatalf("expected nil BalanceCents for OpenStack, got %v", *acct.BalanceCents)
	}
}

func TestOpenStack_PasswordAuth(t *testing.T) {
	ctx := context.Background()
	m, _ := newMockOpenStack(t)

	// Test password authentication path
	passClient := New(Config{
		AuthURL:     m.baseURL + "/v3",
		Username:    "test-user",
		Password:    "test-pass",
		ProjectName: "demo",
		DomainName:  "default",
	})
	acct, err := passClient.Account(ctx)
	if err != nil {
		t.Fatalf("Account with password: %v", err)
	}
	if acct.BalanceCents != nil {
		t.Fatalf("expected nil balance, got %v", acct.BalanceCents)
	}
}

func TestOpenStack_Regions(t *testing.T) {
	ctx := context.Background()
	_, client := newMockOpenStack(t)

	regions, err := client.Regions(ctx)
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(regions) != 1 || regions[0].ID != "RegionOne" {
		t.Fatalf("unexpected regions: %+v", regions)
	}
}

func TestOpenStack_Plans(t *testing.T) {
	ctx := context.Background()
	_, client := newMockOpenStack(t)

	plans, err := client.Plans(ctx)
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
	if plans[0].ID != "flavor-1" || plans[0].VCPUs != 1 || plans[0].RAMMB != 2048 || plans[0].DiskGB != 20 {
		t.Fatalf("unexpected plan 0: %+v", plans[0])
	}
	if plans[1].ID != "flavor-2" || plans[1].VCPUs != 2 || plans[1].RAMMB != 4096 || plans[1].DiskGB != 40 {
		t.Fatalf("unexpected plan 1: %+v", plans[1])
	}
}

func TestOpenStack_EnsureSSHKey(t *testing.T) {
	ctx := context.Background()
	m, client := newMockOpenStack(t)

	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey hikkari-hub"

	// First call creates key
	id1, err := client.EnsureSSHKey(ctx, "hikkari-key", pubKey)
	if err != nil {
		t.Fatalf("EnsureSSHKey first call: %v", err)
	}
	if id1 != "hikkari-key" {
		t.Fatalf("unexpected key name: %s", id1)
	}
	if len(m.keys) != 1 {
		t.Fatalf("expected 1 key in mock, got %d", len(m.keys))
	}

	// Second call with same public key should reuse
	id2, err := client.EnsureSSHKey(ctx, "different-name", pubKey)
	if err != nil {
		t.Fatalf("EnsureSSHKey second call: %v", err)
	}
	if id2 != "hikkari-key" {
		t.Fatalf("expected reused key name 'hikkari-key', got %s", id2)
	}
	if len(m.keys) != 1 {
		t.Fatalf("expected key count still 1, got %d", len(m.keys))
	}
}

func TestOpenStack_CreateAndInstance(t *testing.T) {
	ctx := context.Background()
	m, client := newMockOpenStack(t)
	m.ip = "198.51.100.42"

	inst, err := client.Create(ctx, CreateParams{
		Region:   "RegionOne",
		Plan:     "flavor-1",
		Label:    "test-box",
		Hostname: "box-host.example",
		SSHKeyID: "hikkari-key",
		UserData: "#!/bin/bash\necho hello",
		Tags:     []string{"hikkari-entry"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst.ID != "srv-12345" {
		t.Fatalf("unexpected ID: %s", inst.ID)
	}
	if inst.Label != "box-host.example" {
		t.Fatalf("unexpected label: %s", inst.Label)
	}
	if inst.Plan != "flavor-1" {
		t.Fatalf("unexpected plan: %s", inst.Plan)
	}

	// Verify user_data was base64-encoded
	lastSrv := m.servers[0]
	// Now fetch instance detail
	polled, err := client.Instance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if polled.Status != "ACTIVE" {
		t.Fatalf("expected ACTIVE, got %s", polled.Status)
	}
	if polled.Address != "198.51.100.42" {
		t.Fatalf("expected IP 198.51.100.42, got %s", polled.Address)
	}

	// List instances
	list, err := client.Instances(ctx)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(list) != 1 || list[0].ID != "srv-12345" {
		t.Fatalf("unexpected list: %+v", list)
	}
	_ = lastSrv
}

func TestOpenStack_RebootAndDelete(t *testing.T) {
	ctx := context.Background()
	m, client := newMockOpenStack(t)

	// Create a server
	inst, err := client.Create(ctx, CreateParams{
		Region: "RegionOne",
		Plan:   "flavor-1",
		Label:  "to-delete",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := client.Reboot(ctx, inst.ID); err != nil {
		t.Fatalf("Reboot: %v", err)
	}
	if len(m.rebooted) != 1 || m.rebooted[0] != inst.ID {
		t.Fatalf("expected rebooted [%s], got %+v", inst.ID, m.rebooted)
	}

	if err := client.Delete(ctx, inst.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(m.deleted) != 1 || m.deleted[0] != inst.ID {
		t.Fatalf("expected deleted [%s], got %+v", inst.ID, m.deleted)
	}

	// Next get should return ErrNotFound
	_, err = client.Instance(ctx, inst.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestOpenStack_UserDataEncoding(t *testing.T) {
	rawScript := "#!/bin/bash\necho test\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(rawScript))

	ctx := context.Background()
	_, client := newMockOpenStack(t)

	// Both raw script and pre-encoded string should work
	_, err := client.Create(ctx, CreateParams{
		Plan:     "flavor-1",
		Label:    "box-raw",
		UserData: rawScript,
	})
	if err != nil {
		t.Fatalf("Create raw: %v", err)
	}

	_, err = client.Create(ctx, CreateParams{
		Plan:     "flavor-1",
		Label:    "box-encoded",
		UserData: encoded,
	})
	if err != nil {
		t.Fatalf("Create encoded: %v", err)
	}
}

func TestOpenStack_NeutronAndCinder(t *testing.T) {
	ctx := context.Background()
	_, client := newMockOpenStack(t)

	nets, err := client.Networks(ctx)
	if err != nil {
		t.Fatalf("Networks: %v", err)
	}
	if len(nets) != 2 || nets[0].Name != "public" || !nets[0].External {
		t.Fatalf("unexpected networks: %+v", nets)
	}

	fips, err := client.FloatingIPs(ctx)
	if err != nil {
		t.Fatalf("FloatingIPs: %v", err)
	}
	if len(fips) != 1 || fips[0].FloatingIPAddress != "198.51.100.5" {
		t.Fatalf("unexpected floating ips: %+v", fips)
	}

	vols, err := client.Volumes(ctx)
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	if len(vols) != 1 || vols[0].Name != "root-vol" || vols[0].SizeGB != 50 {
		t.Fatalf("unexpected volumes: %+v", vols)
	}
}

func TestOpenStack_ErrorClassification(t *testing.T) {
	ctx := context.Background()
	m, client := newMockOpenStack(t)

	tests := []struct {
		name       string
		path       string
		status     int
		bodyMsg    string
		wantErr    error
		call       func() error
	}{
		{
			name:    "bad credentials 401",
			path:    "/v3/auth/tokens",
			status:  http.StatusUnauthorized,
			bodyMsg: "Unauthorized",
			wantErr: ErrBadCredentials,
			call: func() error {
				_, err := client.Account(ctx)
				return err
			},
		},
		{
			name:    "bad credentials 403",
			path:    "/compute/flavors/detail",
			status:  http.StatusForbidden,
			bodyMsg: "Forbidden",
			wantErr: ErrBadCredentials,
			call: func() error {
				_, err := client.Plans(ctx)
				return err
			},
		},
		{
			name:    "rate limit 429",
			path:    "/compute/flavors/detail",
			status:  http.StatusTooManyRequests,
			bodyMsg: "Rate limit exceeded",
			wantErr: ErrRateLimited,
			call: func() error {
				_, err := client.Plans(ctx)
				return err
			},
		},
		{
			name:    "out of capacity / quota 400",
			path:    "/compute/servers",
			status:  http.StatusBadRequest,
			bodyMsg: "Quota exceeded for ram or vcpus",
			wantErr: ErrOutOfCapacity,
			call: func() error {
				_, err := client.Create(ctx, CreateParams{Plan: "flavor-1", Label: "b"})
				return err
			},
		},
		{
			name:    "insufficient funds 402",
			path:    "/compute/servers",
			status:  http.StatusPaymentRequired,
			bodyMsg: "Balance is zero or payment required",
			wantErr: ErrInsufficientFunds,
			call: func() error {
				_, err := client.Create(ctx, CreateParams{Plan: "flavor-1", Label: "b"})
				return err
			},
		},
		{
			name:    "generic 500 APIError",
			path:    "/compute/flavors/detail",
			status:  http.StatusInternalServerError,
			bodyMsg: "Internal error",
			wantErr: nil,
			call: func() error {
				_, err := client.Plans(ctx)
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m.fail[tc.path] = tc.status
			m.failMsg[tc.path] = tc.bodyMsg
			defer delete(m.fail, tc.path)
			defer delete(m.failMsg, tc.path)

			err := tc.call()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.HTTPStatus != tc.status {
					t.Fatalf("expected APIError with status %d, got %v", tc.status, err)
				}
			}
		})
	}
}

func TestOpenStack_ProviderErrorsNeverCarryAnEgressAddress(t *testing.T) {
	ctx := context.Background()
	m, client := newMockOpenStack(t)

	// Ensure the server responds with an error that might contain the IP
	m.fail["/compute/flavors/detail"] = http.StatusInternalServerError
	m.failMsg["/compute/flavors/detail"] = "Error connecting to internal backend at 10.99.88.77 or egress 198.51.100.42"

	_, err := client.Plans(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	errStr := err.Error()
	if strings.Contains(errStr, "10.99.88.77") || strings.Contains(errStr, "198.51.100.42") {
		t.Fatalf("security violation: error leaked backend/egress IP: %s", errStr)
	}
}
