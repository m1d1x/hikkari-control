// Package hetzner is a direct HTTPS client for the Hetzner Cloud v1 API
// (https://api.hetzner.cloud/v1), one of the hosters the hub rents relay machines
// from. It is a pure client: it knows nothing of the entry package, returns its
// own types, and reports failures as concrete error values. The adapter that
// makes it an entry.BoxProvider lives in internal/entrywire.
package hetzner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Hetzner Cloud v1 API root.
const DefaultBaseURL = "https://api.hetzner.cloud/v1"

// requestTimeout caps a single call.
const requestTimeout = 30 * time.Second

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 4 << 20

// listPageSize is the per_page parameter sent in list calls (max 50 in Hetzner API).
const listPageSize = 50

// maxListPages bounds pagination walk to prevent infinite loops.
const maxListPages = 50

// overageCentsPerTB is Hetzner's published traffic overage price: 1.00 EUR per TB.
const overageCentsPerTB = 100

var (
	// ErrBadCredentials: the API token was rejected (401/403).
	ErrBadCredentials = errors.New("hetzner: credentials rejected")
	// ErrNotFound: no such object (404) — server or key already deleted.
	ErrNotFound = errors.New("hetzner: not found")
	// ErrRateLimited: too many calls in the window (429).
	ErrRateLimited = errors.New("hetzner: rate limited")
	// ErrOutOfCapacity: the plan is not available in that region right now.
	ErrOutOfCapacity = errors.New("hetzner: plan out of capacity in that region")
	// ErrInsufficientFunds: the account has no credit or billing is locked.
	ErrInsufficientFunds = errors.New("hetzner: insufficient account credit")
)

// APIError preserves status without an untrusted provider response body.
type APIError struct {
	HTTPStatus int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("hetzner: api error (http %d): %s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("hetzner: api error (http %d)", e.HTTPStatus)
}

// Client talks to the Hetzner Cloud API. Safe for concurrent use.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (for tests against an httptest server).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a Client authenticating with token.
func New(token string, opts ...Option) *Client {
	c := &Client{
		token:   token,
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: requestTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// call sends one request and decodes the response into out (nil to discard).
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("hetzner: marshal request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("hetzner: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hetzner: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("hetzner: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &env)
		msg := env.Error.Message
		if msg == "" {
			msg = env.Message
		}
		return classify(resp.StatusCode, env.Error.Code, msg)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("hetzner: decode response: %w", err)
	}
	return nil
}

// classify maps an HTTP failure onto a concrete error. Unclassified failures
// and 5xx return an APIError with no raw body to avoid leaking sensitive data
// such as egress IP addresses.
func classify(status int, code, msg string) error {
	m := strings.ToLower(code + " " + msg)
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
	case refused && (strings.Contains(m, "out of stock") || strings.Contains(m, "no available") ||
		strings.Contains(m, "not available in") || strings.Contains(m, "capacity") ||
		strings.Contains(m, "resource_limit_exceeded") || strings.Contains(m, "server_limit_exceeded") ||
		strings.Contains(m, "resource_unavailable") || strings.Contains(m, "limit_exceeded")):
		return ErrOutOfCapacity
	case refused && (strings.Contains(m, "insufficient") || strings.Contains(m, "no credit") ||
		strings.Contains(m, "payment") || strings.Contains(m, "blocked")):
		return ErrInsufficientFunds
	}
	return &APIError{HTTPStatus: status}
}

// ── Account ─────────────────────────────────────────────────────────────

// Account represents account status. Hetzner Cloud has no balance API endpoint,
// so BalanceCents is nil when the token is valid.
type Account struct {
	BalanceCents *int
	PendingCents int
}

// Account verifies the API token by querying /locations?per_page=1.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var out struct {
		Locations []any `json:"locations"`
	}
	if err := c.call(ctx, http.MethodGet, "/locations?per_page=1", nil, &out); err != nil {
		return Account{}, err
	}
	return Account{
		BalanceCents: nil,
		PendingCents: 0,
	}, nil
}

// ── Catalog ─────────────────────────────────────────────────────────────

// Region is one Hetzner location.
type Region struct {
	ID        string `json:"id"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Continent string `json:"continent"`
}

// Regions lists all locations (GET /locations).
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	var out struct {
		Locations []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Country     string `json:"country"`
			City        string `json:"city"`
			NetworkZone string `json:"network_zone"`
		} `json:"locations"`
	}
	if err := c.call(ctx, http.MethodGet, "/locations?per_page="+strconv.Itoa(listPageSize), nil, &out); err != nil {
		return nil, err
	}
	regions := make([]Region, 0, len(out.Locations))
	for _, l := range out.Locations {
		continent := "Europe"
		if strings.HasPrefix(l.NetworkZone, "us") || l.Country == "US" {
			continent = "North America"
		} else if strings.HasPrefix(l.NetworkZone, "ap") || l.Country == "SG" {
			continent = "Asia"
		}
		regions = append(regions, Region{
			ID:        l.Name,
			City:      l.City,
			Country:   l.Country,
			Continent: continent,
		})
	}
	return regions, nil
}

// Plan is one server type.
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

func parsePrice(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if val, err := strconv.ParseFloat(s, 64); err == nil {
			return val
		}
	}
	return 0
}

// Plans lists the available server types (GET /server_types).
func (c *Client) Plans(ctx context.Context) ([]Plan, error) {
	var out struct {
		ServerTypes []struct {
			ID              int             `json:"id"`
			Name            string          `json:"name"`
			Description     string          `json:"description"`
			Cores           int             `json:"cores"`
			Memory          float64         `json:"memory"` // GB
			Disk            int             `json:"disk"`   // GB
			IncludedTraffic int64           `json:"included_traffic"`
			Prices          []struct {
				Location     string `json:"location"`
				PriceMonthly struct {
					Net   json.RawMessage `json:"net"`
					Gross json.RawMessage `json:"gross"`
				} `json:"price_monthly"`
			} `json:"prices"`
		} `json:"server_types"`
	}
	if err := c.call(ctx, http.MethodGet, "/server_types?per_page="+strconv.Itoa(listPageSize), nil, &out); err != nil {
		return nil, err
	}
	plans := make([]Plan, 0, len(out.ServerTypes))
	for _, st := range out.ServerTypes {
		locs := make([]string, 0, len(st.Prices))
		monthlyCents := 0
		for _, p := range st.Prices {
			locs = append(locs, p.Location)
			if monthlyCents == 0 {
				net := parsePrice(p.PriceMonthly.Net)
				monthlyCents = int(math.Round(net * 100))
			}
		}
		bw := 20000 // 20 TB default in Hetzner Cloud
		if st.IncludedTraffic > 0 {
			if st.IncludedTraffic >= 1_000_000_000 {
				bw = int(st.IncludedTraffic / (1000 * 1000 * 1000))
			} else {
				bw = int(st.IncludedTraffic)
			}
		}
		plans = append(plans, Plan{
			ID:                st.Name,
			VCPUs:             st.Cores,
			RAMMB:             int(math.Round(st.Memory * 1024)),
			DiskGB:            st.Disk,
			BandwidthGB:       bw,
			MonthlyCents:      monthlyCents,
			OverageCentsPerTB: overageCentsPerTB,
			Regions:           locs,
		})
	}
	return plans, nil
}

// ── SSH Keys ────────────────────────────────────────────────────────────

// SSHKey is one public key on the Hetzner account.
type SSHKey struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// SSHKeys lists all SSH keys on the account (GET /ssh_keys).
func (c *Client) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	var out struct {
		SSHKeys []SSHKey `json:"ssh_keys"`
	}
	if err := c.call(ctx, http.MethodGet, "/ssh_keys?per_page="+strconv.Itoa(listPageSize), nil, &out); err != nil {
		return nil, err
	}
	return out.SSHKeys, nil
}

// EnsureSSHKey finds or uploads publicKey under name and returns its string ID.
func (c *Client) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	want := normalizeKey(publicKey)
	if want == "" {
		return "", fmt.Errorf("hetzner: empty ssh public key")
	}
	keys, err := c.SSHKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if normalizeKey(k.PublicKey) == want {
			return strconv.Itoa(k.ID), nil
		}
	}
	var out struct {
		SSHKey SSHKey `json:"ssh_key"`
	}
	body := map[string]string{"name": name, "public_key": publicKey}
	if err := c.call(ctx, http.MethodPost, "/ssh_keys", body, &out); err != nil {
		return "", err
	}
	return strconv.Itoa(out.SSHKey.ID), nil
}

func normalizeKey(k string) string {
	f := strings.Fields(strings.TrimSpace(k))
	if len(f) < 2 {
		return strings.TrimSpace(k)
	}
	return f[0] + " " + f[1]
}

// ── Instances ───────────────────────────────────────────────────────────

// Instance is one server.
type Instance struct {
	ID      string
	Label   string
	Region  string
	Plan    string
	Status  string
	Address string
	Tags    []string
}

type wireServer struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
	ServerType struct {
		Name string `json:"name"`
	} `json:"server_type"`
	Datacenter struct {
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
	} `json:"datacenter"`
	Labels map[string]string `json:"labels"`
}

func (w wireServer) toInstance() Instance {
	tags := make([]string, 0, len(w.Labels))
	for k, v := range w.Labels {
		if k == "tag" {
			tags = append(tags, v)
		} else if v == "true" || v == "" {
			tags = append(tags, k)
		} else {
			tags = append(tags, k+"="+v)
		}
	}
	return Instance{
		ID:      strconv.Itoa(w.ID),
		Label:   w.Name,
		Region:  w.Datacenter.Location.Name,
		Plan:    w.ServerType.Name,
		Status:  w.Status,
		Address: publicIP(w.PublicNet.IPv4.IP),
		Tags:    tags,
	}
}

func publicIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "0.0.0.0" {
		return ""
	}
	return ip
}

// CreateParams defines order parameters.
type CreateParams struct {
	Region   string
	Plan     string
	Image    string
	Label    string
	Hostname string
	SSHKeyID string
	UserData string
	Tags     []string
	Labels   map[string]string
}

// Create orders a machine (POST /servers).
func (c *Client) Create(ctx context.Context, p CreateParams) (Instance, error) {
	image := p.Image
	if image == "" {
		image = "ubuntu-24.04"
	}
	name := p.Hostname
	if name == "" {
		name = p.Label
	}
	labels := make(map[string]string)
	for k, v := range p.Labels {
		labels[k] = v
	}
	for _, t := range p.Tags {
		if strings.Contains(t, "=") {
			parts := strings.SplitN(t, "=", 2)
			labels[parts[0]] = parts[1]
		} else {
			labels["tag"] = t
		}
	}
	body := map[string]any{
		"name":               name,
		"server_type":        p.Plan,
		"location":           p.Region,
		"image":              image,
		"start_after_create": true,
	}
	if p.SSHKeyID != "" {
		if id, err := strconv.Atoi(p.SSHKeyID); err == nil {
			body["ssh_keys"] = []any{id}
		} else {
			body["ssh_keys"] = []any{p.SSHKeyID}
		}
	}
	if p.UserData != "" {
		body["user_data"] = p.UserData
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}
	var out struct {
		Server wireServer `json:"server"`
	}
	if err := c.call(ctx, http.MethodPost, "/servers", body, &out); err != nil {
		return Instance{}, err
	}
	return out.Server.toInstance(), nil
}

// CreateInstance is an alias for Create.
func (c *Client) CreateInstance(ctx context.Context, p CreateParams) (Instance, error) {
	return c.Create(ctx, p)
}

// Instance reads one server (GET /servers/{id}).
func (c *Client) Instance(ctx context.Context, id string) (Instance, error) {
	var out struct {
		Server wireServer `json:"server"`
	}
	if err := c.call(ctx, http.MethodGet, "/servers/"+url.PathEscape(id), nil, &out); err != nil {
		return Instance{}, err
	}
	return out.Server.toInstance(), nil
}

// GetInstance is an alias for Instance.
func (c *Client) GetInstance(ctx context.Context, id string) (Instance, error) {
	return c.Instance(ctx, id)
}

// ListInstances lists machines, optionally filtered by tag/label.
func (c *Client) ListInstances(ctx context.Context, tag string) ([]Instance, error) {
	var list []Instance
	for page := 1; ; page++ {
		if page > maxListPages {
			return nil, fmt.Errorf("hetzner: instance list longer than %d pages", maxListPages)
		}
		q := url.Values{
			"page":     {strconv.Itoa(page)},
			"per_page": {strconv.Itoa(listPageSize)},
		}
		if tag != "" {
			if strings.Contains(tag, "=") {
				q.Set("label_selector", tag)
			} else {
				q.Set("label_selector", "tag="+tag)
			}
		}
		var out struct {
			Servers []wireServer `json:"servers"`
			Meta    struct {
				Pagination struct {
					Page     int `json:"page"`
					NextPage int `json:"next_page"`
					LastPage int `json:"last_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		if err := c.call(ctx, http.MethodGet, "/servers?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, w := range out.Servers {
			list = append(list, w.toInstance())
		}
		if out.Meta.Pagination.NextPage == 0 || out.Meta.Pagination.Page >= out.Meta.Pagination.LastPage {
			return list, nil
		}
	}
}

// Instances lists every server on the account (GET /servers).
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	return c.ListInstances(ctx, "")
}

// Reboot restarts a server (POST /servers/{id}/actions/reboot).
func (c *Client) Reboot(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPost, "/servers/"+url.PathEscape(id)+"/actions/reboot", nil, nil)
}

// RebootInstance is an alias for Reboot.
func (c *Client) RebootInstance(ctx context.Context, id string) error {
	return c.Reboot(ctx, id)
}

// Delete destroys a server (DELETE /servers/{id}).
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/servers/"+url.PathEscape(id), nil, nil)
}

// DeleteInstance is an alias for Delete.
func (c *Client) DeleteInstance(ctx context.Context, id string) error {
	return c.Delete(ctx, id)
}
