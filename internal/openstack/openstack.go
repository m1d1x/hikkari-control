// Package openstack is a HTTPS client for OpenStack clouds, implementing
// Keystone v3 identity (https://docs.openstack.org/api-ref/identity/v3/),
// Nova compute (https://docs.openstack.org/api-ref/compute/),
// Neutron network and Cinder block storage.
// It is a pure client: it knows nothing of the entry package, returns its
// own types, and reports failures as concrete error values. The adapter that
// makes it an entry.BoxProvider lives in internal/entrywire.
package openstack

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// requestTimeout caps a single call.
const requestTimeout = 30 * time.Second

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 4 << 20

var (
	// ErrBadCredentials: Keystone rejected the credentials (401/403).
	ErrBadCredentials = errors.New("openstack: credentials rejected")
	// ErrNotFound: no such object (404) — server, flavor or key already deleted.
	ErrNotFound = errors.New("openstack: not found")
	// ErrRateLimited: too many calls in the window (429).
	ErrRateLimited = errors.New("openstack: rate limited")
	// ErrOutOfCapacity: the flavor is not available in that region or hypervisors are full.
	ErrOutOfCapacity = errors.New("openstack: flavor out of capacity or hypervisor full")
	// ErrInsufficientFunds: project quota or credit exceeded.
	ErrInsufficientFunds = errors.New("openstack: project quota or credit exceeded")
)

// APIError preserves status without an untrusted provider response body.
type APIError struct {
	HTTPStatus int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("openstack: api error (http %d): %s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("openstack: api error (http %d)", e.HTTPStatus)
}

// Config configures OpenStack credentials.
type Config struct {
	AuthURL                     string
	Region                      string
	NetworkID                   string
	ApplicationCredentialID     string
	ApplicationCredentialSecret string
	Username                    string
	Password                    string
	ProjectID                   string
	ProjectName                 string
	DomainName                  string
}

// Client talks to the OpenStack APIs. Safe for concurrent use.
type Client struct {
	cfg        Config
	http       *http.Client
	mu         sync.Mutex
	token      string
	tokenExp   time.Time
	computeURL string
	networkURL string
	imageURL   string
	volumeURL  string
	regions    []string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a Client authenticating with cfg.
func New(cfg Config, opts ...Option) *Client {
	c := &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: requestTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

type authPayload struct {
	Auth struct {
		Identity struct {
			Methods               []string `json:"methods"`
			ApplicationCredential *struct {
				ID     string `json:"id"`
				Secret string `json:"secret"`
			} `json:"application_credential,omitempty"`
			Password *struct {
				User struct {
					Name     string `json:"name"`
					Password string `json:"password"`
					Domain   struct {
						Name string `json:"name"`
					} `json:"domain"`
				} `json:"user"`
			} `json:"password,omitempty"`
		} `json:"identity"`
		Scope *struct {
			Project struct {
				ID     string `json:"id,omitempty"`
				Name   string `json:"name,omitempty"`
				Domain *struct {
					Name string `json:"name,omitempty"`
				} `json:"domain,omitempty"`
			} `json:"project"`
		} `json:"scope,omitempty"`
	} `json:"auth"`
}

type tokenResponse struct {
	Token struct {
		ExpiresAt string `json:"expires_at"`
		Catalog   []struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Endpoints []struct {
				ID        string `json:"id"`
				Interface string `json:"interface"`
				Region    string `json:"region"`
				URL       string `json:"url"`
			} `json:"endpoints"`
		} `json:"catalog"`
	} `json:"token"`
}

// authenticate fetches a Keystone v3 token and service catalog.
func (c *Client) authenticate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExp.Add(-2*time.Minute)) {
		return nil
	}

	authURL := strings.TrimRight(c.cfg.AuthURL, "/")
	if !strings.HasSuffix(authURL, "/v3") && !strings.Contains(authURL, "/v3/") {
		authURL += "/v3"
	}

	var body authPayload
	if c.cfg.ApplicationCredentialID != "" {
		body.Auth.Identity.Methods = []string{"application_credential"}
		body.Auth.Identity.ApplicationCredential = &struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		}{
			ID:     c.cfg.ApplicationCredentialID,
			Secret: c.cfg.ApplicationCredentialSecret,
		}
	} else {
		domain := c.cfg.DomainName
		if domain == "" {
			domain = "Default"
		}
		body.Auth.Identity.Methods = []string{"password"}
		body.Auth.Identity.Password = &struct {
			User struct {
				Name     string `json:"name"`
				Password string `json:"password"`
				Domain   struct {
					Name string `json:"name"`
				} `json:"domain"`
			} `json:"user"`
		}{}
		body.Auth.Identity.Password.User.Name = c.cfg.Username
		body.Auth.Identity.Password.User.Password = c.cfg.Password
		body.Auth.Identity.Password.User.Domain.Name = domain

		proj := c.cfg.ProjectName
		if proj == "" && c.cfg.ProjectID == "" {
			proj = c.cfg.Username
		}
		if c.cfg.ProjectID != "" || proj != "" {
			body.Auth.Scope = &struct {
				Project struct {
					ID     string `json:"id,omitempty"`
					Name   string `json:"name,omitempty"`
					Domain *struct {
						Name string `json:"name,omitempty"`
					} `json:"domain,omitempty"`
				} `json:"project"`
			}{}
			body.Auth.Scope.Project.ID = c.cfg.ProjectID
			body.Auth.Scope.Project.Name = proj
			if proj != "" {
				body.Auth.Scope.Project.Domain = &struct {
					Name string `json:"name,omitempty"`
				}{Name: domain}
			}
		}
	}

	rawReq, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("openstack: marshal auth: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL+"/auth/tokens", bytes.NewReader(rawReq))
	if err != nil {
		return fmt.Errorf("openstack: build auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openstack: http auth: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("openstack: read auth response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classify(resp.StatusCode, string(raw))
	}

	token := resp.Header.Get("X-Subject-Token")
	if token == "" {
		return fmt.Errorf("openstack: missing X-Subject-Token in auth response")
	}

	var tokResp tokenResponse
	if err := json.Unmarshal(raw, &tokResp); err != nil {
		return fmt.Errorf("openstack: decode token catalog: %w", err)
	}

	c.token = token
	if exp, err := time.Parse(time.RFC3339, tokResp.Token.ExpiresAt); err == nil {
		c.tokenExp = exp
	} else {
		c.tokenExp = time.Now().Add(1 * time.Hour)
	}

	regSet := make(map[string]bool)
	for _, s := range tokResp.Token.Catalog {
		for _, ep := range s.Endpoints {
			if ep.Interface == "public" || ep.Interface == "" {
				if ep.Region != "" {
					regSet[ep.Region] = true
				}
				matchRegion := c.cfg.Region == "" || ep.Region == "" || ep.Region == c.cfg.Region
				if matchRegion {
					switch s.Type {
					case "compute":
						if c.computeURL == "" || ep.Region == c.cfg.Region {
							c.computeURL = strings.TrimRight(ep.URL, "/")
						}
					case "network":
						if c.networkURL == "" || ep.Region == c.cfg.Region {
							c.networkURL = strings.TrimRight(ep.URL, "/")
						}
					case "image":
						if c.imageURL == "" || ep.Region == c.cfg.Region {
							c.imageURL = strings.TrimRight(ep.URL, "/")
						}
					case "volumev3", "volumev2", "block-storage":
						if c.volumeURL == "" || ep.Region == c.cfg.Region {
							c.volumeURL = strings.TrimRight(ep.URL, "/")
						}
					}
				}
			}
		}
	}
	c.regions = make([]string, 0, len(regSet))
	for r := range regSet {
		c.regions = append(c.regions, r)
	}
	if len(c.regions) == 0 {
		c.regions = []string{"RegionOne"}
	}
	return nil
}

func (c *Client) call(ctx context.Context, method, targetURL string, body, out any) error {
	if err := c.authenticate(ctx); err != nil {
		return err
	}

	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("openstack: marshal request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, rdr)
	if err != nil {
		return fmt.Errorf("openstack: build request: %w", err)
	}
	req.Header.Set("X-Auth-Token", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openstack: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("openstack: read response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classify(resp.StatusCode, string(raw))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("openstack: decode response: %w", err)
	}
	return nil
}

func classify(status int, raw string) error {
	m := strings.ToLower(raw)
	refused := status >= 400 && status < 500
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		if strings.Contains(m, "unauthorized ip") {
			return fmt.Errorf("%w: check the account IP allowlist", ErrBadCredentials)
		}
		return ErrBadCredentials
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case refused && (strings.Contains(m, "quota") || strings.Contains(m, "limit") ||
		strings.Contains(m, "capacity") || strings.Contains(m, "no valid host") ||
		strings.Contains(m, "exceeded")):
		return ErrOutOfCapacity
	case refused && (strings.Contains(m, "insufficient") || strings.Contains(m, "funds") ||
		strings.Contains(m, "balance") || strings.Contains(m, "payment")):
		return ErrInsufficientFunds
	}
	return &APIError{HTTPStatus: status}
}

// ── Account ─────────────────────────────────────────────────────────────

// Account represents account status. OpenStack Keystone authentication proves
// the credentials work.
type Account struct {
	BalanceCents *int
	PendingCents int
}

// Account proves credentials by authenticating with Keystone.
func (c *Client) Account(ctx context.Context) (Account, error) {
	if err := c.authenticate(ctx); err != nil {
		return Account{}, err
	}
	return Account{BalanceCents: nil, PendingCents: 0}, nil
}

// ── Catalog ─────────────────────────────────────────────────────────────

// Region is an OpenStack region or availability zone.
type Region struct {
	ID        string `json:"id"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Continent string `json:"continent"`
}

// Regions lists the discovered regions (from Keystone catalog).
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, err
	}
	regions := make([]Region, 0, len(c.regions))
	for _, r := range c.regions {
		regions = append(regions, Region{
			ID:        r,
			City:      r,
			Country:   "Cloud",
			Continent: "OpenStack",
		})
	}
	return regions, nil
}

// Plan is an OpenStack flavor.
type Plan struct {
	ID                string
	VCPUs             int
	RAMMB             int
	DiskGB            int
	BandwidthGB       int
	MonthlyCents      int
	OverageCentsPerTB int
	Regions           []string
}

type wireFlavor struct {
	ID    any    `json:"id"`
	Name  string `json:"name"`
	RAM   int    `json:"ram"`
	VCPUs int    `json:"vcpus"`
	Disk  int    `json:"disk"`
}

// Plans lists flavors from Nova (GET {compute}/flavors/detail).
func (c *Client) Plans(ctx context.Context) ([]Plan, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Flavors []wireFlavor `json:"flavors"`
	}
	computeURL := c.computeURL
	if err := c.call(ctx, http.MethodGet, computeURL+"/flavors/detail", nil, &out); err != nil {
		return nil, err
	}
	plans := make([]Plan, 0, len(out.Flavors))
	for _, f := range out.Flavors {
		plans = append(plans, Plan{
			ID:                fmt.Sprint(f.ID),
			VCPUs:             f.VCPUs,
			RAMMB:             f.RAM,
			DiskGB:            f.Disk,
			BandwidthGB:       10000,
			// OpenStack flavors do not define price in Nova API; estimate monthly cents so budget caps & catalog selection work
			MonthlyCents:      99 + f.VCPUs*100 + (f.RAM/1024)*50,
			OverageCentsPerTB: 0,
			Regions:           c.regions,
		})
	}
	return plans, nil
}

// ── SSH Keys ────────────────────────────────────────────────────────────

// SSHKey is one Nova keypair.
type SSHKey struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// SSHKeys lists keypairs from Nova (GET {compute}/os-keypairs).
func (c *Client) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Keypairs []struct {
			Keypair SSHKey `json:"keypair"`
		} `json:"keypairs"`
	}
	if err := c.call(ctx, http.MethodGet, c.computeURL+"/os-keypairs", nil, &out); err != nil {
		return nil, err
	}
	keys := make([]SSHKey, 0, len(out.Keypairs))
	for _, k := range out.Keypairs {
		keys = append(keys, k.Keypair)
	}
	return keys, nil
}

// EnsureSSHKey finds or registers publicKey under name and returns its string key_name.
func (c *Client) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	want := normalizeKey(publicKey)
	if want == "" {
		return "", fmt.Errorf("openstack: empty ssh public key")
	}
	keys, err := c.SSHKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if normalizeKey(k.PublicKey) == want {
			return k.Name, nil
		}
	}
	body := map[string]any{
		"keypair": map[string]string{
			"name":       name,
			"public_key": publicKey,
		},
	}
	var out struct {
		Keypair SSHKey `json:"keypair"`
	}
	if err := c.call(ctx, http.MethodPost, c.computeURL+"/os-keypairs", body, &out); err != nil {
		return "", err
	}
	return out.Keypair.Name, nil
}

func normalizeKey(k string) string {
	f := strings.Fields(strings.TrimSpace(k))
	if len(f) < 2 {
		return strings.TrimSpace(k)
	}
	return f[0] + " " + f[1]
}

// ── Instances ───────────────────────────────────────────────────────────

// Instance is one Nova server.
type Instance struct {
	ID      string
	Label   string
	Region  string
	Plan    string
	Status  string
	Address string
	Tags    []string
}

type wireAddress struct {
	Addr    string `json:"addr"`
	Version int    `json:"version"`
	Type    string `json:"OS-EXT-IPS:type"`
}

type wireServer struct {
	ID        string                   `json:"id"`
	Name      string                   `json:"name"`
	Status    string                   `json:"status"`
	Addresses map[string][]wireAddress `json:"addresses"`
	Flavor    struct {
		ID any `json:"id"`
	} `json:"flavor"`
	Tags []string `json:"tags"`
}

func (w wireServer) toInstance(region string) Instance {
	addr := ""
	for _, addrs := range w.Addresses {
		for _, a := range addrs {
			if a.Version == 4 {
				ip := strings.TrimSpace(a.Addr)
				if ip == "" || ip == "0.0.0.0" {
					continue
				}
				if a.Type == "floating" {
					addr = ip
					break
				}
				if addr == "" || (!isPublicIPv4(addr) && isPublicIPv4(ip)) {
					addr = ip
				}
			}
		}
	}
	return Instance{
		ID:      w.ID,
		Label:   w.Name,
		Region:  region,
		Plan:    fmt.Sprint(w.Flavor.ID),
		Status:  w.Status,
		Address: addr,
		Tags:    w.Tags,
	}
}

func isPublicIPv4(raw string) bool {
	ip := net.ParseIP(raw)
	if ip == nil {
		return false
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return !v4.IsLoopback() && !v4.IsPrivate() && !v4.IsLinkLocalUnicast()
}

// CreateParams defines server order parameters.
type CreateParams struct {
	Region    string
	Plan      string
	ImageRef  string
	NetworkID string
	Label     string
	Hostname  string
	SSHKeyID  string
	UserData  string
	Tags      []string
}

// Create orders a server (POST {compute}/servers).
func (c *Client) Create(ctx context.Context, p CreateParams) (Instance, error) {
	if err := c.authenticate(ctx); err != nil {
		return Instance{}, err
	}
	name := p.Hostname
	if name == "" {
		name = p.Label
	}
	imageRef := p.ImageRef
	if imageRef == "" {
		img, err := c.UbuntuImage(ctx)
		if err == nil {
			imageRef = img
		}
	}
	serverBody := map[string]any{
		"name":      name,
		"flavorRef": p.Plan,
		"imageRef":  imageRef,
	}
	netID := p.NetworkID
	if netID == "" {
		netID = c.cfg.NetworkID
	}
	if netID != "" {
		serverBody["networks"] = []map[string]string{{"uuid": netID}}
	}
	if p.SSHKeyID != "" {
		serverBody["key_name"] = p.SSHKeyID
	}
	if p.UserData != "" {
		ud := p.UserData
		// Nova requires user_data to be Base64-encoded.
		if _, err := base64.StdEncoding.DecodeString(ud); err != nil {
			ud = base64.StdEncoding.EncodeToString([]byte(p.UserData))
		}
		serverBody["user_data"] = ud
	}
	if len(p.Tags) > 0 {
		serverBody["tags"] = p.Tags
	}
	body := map[string]any{"server": serverBody}

	var out struct {
		Server wireServer `json:"server"`
	}
	if err := c.call(ctx, http.MethodPost, c.computeURL+"/servers", body, &out); err != nil {
		return Instance{}, err
	}
	inst := out.Server.toInstance(p.Region)
	if inst.Plan == "" || inst.Plan == "<nil>" {
		inst.Plan = p.Plan
	}
	return inst, nil
}

// UbuntuImage finds an Ubuntu image in Glance or Nova.
func (c *Client) UbuntuImage(ctx context.Context) (string, error) {
	if c.imageURL != "" {
		var out struct {
			Images []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"images"`
		}
		if err := c.call(ctx, http.MethodGet, c.imageURL+"/v2/images", nil, &out); err == nil {
			// Find newest Ubuntu (prefer 24.04 or 22.04, avoid prebuilt or OLD)
			for _, img := range out.Images {
				n := strings.ToLower(img.Name)
				if strings.Contains(n, "ubuntu-24.04") && !strings.Contains(n, "old") && !strings.Contains(n, "prebuilt") {
					return img.ID, nil
				}
			}
			for _, img := range out.Images {
				n := strings.ToLower(img.Name)
				if strings.Contains(n, "ubuntu") && !strings.Contains(n, "old") && !strings.Contains(n, "prebuilt") {
					return img.ID, nil
				}
			}
		}
	}

	var out struct {
		Images []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"images"`
	}
	if err := c.call(ctx, http.MethodGet, c.computeURL+"/images", nil, &out); err != nil {
		return "", err
	}
	for _, img := range out.Images {
		if strings.Contains(strings.ToLower(img.Name), "ubuntu") {
			return img.ID, nil
		}
	}
	if len(out.Images) > 0 {
		return out.Images[0].ID, nil
	}
	return "", fmt.Errorf("openstack: no image found")
}

// Instance reads one server (GET {compute}/servers/{id}).
func (c *Client) Instance(ctx context.Context, id string) (Instance, error) {
	if err := c.authenticate(ctx); err != nil {
		return Instance{}, err
	}
	var out struct {
		Server wireServer `json:"server"`
	}
	if err := c.call(ctx, http.MethodGet, c.computeURL+"/servers/"+url.PathEscape(id), nil, &out); err != nil {
		return Instance{}, err
	}
	return out.Server.toInstance(c.cfg.Region), nil
}

// Instances lists servers (GET {compute}/servers/detail).
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Servers []wireServer `json:"servers"`
	}
	if err := c.call(ctx, http.MethodGet, c.computeURL+"/servers/detail", nil, &out); err != nil {
		return nil, err
	}
	list := make([]Instance, 0, len(out.Servers))
	for _, s := range out.Servers {
		list = append(list, s.toInstance(c.cfg.Region))
	}
	return list, nil
}

// Reboot restarts a server (POST {compute}/servers/{id}/action).
func (c *Client) Reboot(ctx context.Context, id string) error {
	if err := c.authenticate(ctx); err != nil {
		return err
	}
	body := map[string]any{"reboot": map[string]string{"type": "SOFT"}}
	return c.call(ctx, http.MethodPost, c.computeURL+"/servers/"+url.PathEscape(id)+"/action", body, nil)
}

// Delete destroys a server (DELETE {compute}/servers/{id}).
func (c *Client) Delete(ctx context.Context, id string) error {
	if err := c.authenticate(ctx); err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, c.computeURL+"/servers/"+url.PathEscape(id), nil, nil)
}

// ── Neutron Network (https://docs.openstack.org/api-ref/network/) ──────

// Network is a Neutron virtual network.
type Network struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	External bool   `json:"router:external"`
}

// Networks lists virtual networks (GET {network}/v2.0/networks).
func (c *Client) Networks(ctx context.Context) ([]Network, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, err
	}
	if c.networkURL == "" {
		return nil, fmt.Errorf("openstack: network service not found in catalog")
	}
	var out struct {
		Networks []Network `json:"networks"`
	}
	if err := c.call(ctx, http.MethodGet, c.networkURL+"/v2.0/networks", nil, &out); err != nil {
		return nil, err
	}
	return out.Networks, nil
}

// FloatingIP is an external allocated IP from Neutron.
type FloatingIP struct {
	ID                string `json:"id"`
	FloatingIPAddress string `json:"floating_ip_address"`
	FixedIPAddress    string `json:"fixed_ip_address"`
	Status            string `json:"status"`
}

// FloatingIPs lists floating IPs (GET {network}/v2.0/floatingips).
func (c *Client) FloatingIPs(ctx context.Context) ([]FloatingIP, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, err
	}
	if c.networkURL == "" {
		return nil, fmt.Errorf("openstack: network service not found in catalog")
	}
	var out struct {
		FloatingIPs []FloatingIP `json:"floatingips"`
	}
	if err := c.call(ctx, http.MethodGet, c.networkURL+"/v2.0/floatingips", nil, &out); err != nil {
		return nil, err
	}
	return out.FloatingIPs, nil
}

// ── Cinder Block Storage (https://docs.openstack.org/api-ref/block-storage/) ──

// Volume is a Cinder block storage volume.
type Volume struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	SizeGB int    `json:"size"`
	Status string `json:"status"`
}

// Volumes lists storage volumes (GET {volume}/volumes/detail).
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, err
	}
	if c.volumeURL == "" {
		return nil, fmt.Errorf("openstack: block storage service not found in catalog")
	}
	var out struct {
		Volumes []Volume `json:"volumes"`
	}
	if err := c.call(ctx, http.MethodGet, c.volumeURL+"/volumes/detail", nil, &out); err != nil {
		return nil, err
	}
	return out.Volumes, nil
}

