// Package digitalocean is a direct HTTPS client for the DigitalOcean API v2
// (https://api.digitalocean.com/v2), one of the hosters the hub rents relay machines
// from. It is a pure client: it knows nothing of the entry package, returns its
// own types, and reports failures as concrete error values. The adapter that
// makes it an entry.BoxProvider lives in internal/entrywire.
package digitalocean

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

// DefaultBaseURL is the DigitalOcean API v2 root.
const DefaultBaseURL = "https://api.digitalocean.com/v2"

// requestTimeout caps a single call.
const requestTimeout = 30 * time.Second

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 4 << 20

// listPageSize is the per_page parameter sent in list calls (max 200 in DO API).
const listPageSize = 200

// maxListPages bounds pagination walk to prevent infinite loops.
const maxListPages = 50

// overageCentsPerTB is DigitalOcean's published bandwidth overage price: $0.01 per GB = 1000 cents/TB.
const overageCentsPerTB = 1000

var (
	// ErrBadCredentials: the API token was rejected (401/403).
	ErrBadCredentials = errors.New("digitalocean: credentials rejected")
	// ErrNotFound: no such object (404) — droplet or key already deleted.
	ErrNotFound = errors.New("digitalocean: not found")
	// ErrRateLimited: too many calls in the window (429).
	ErrRateLimited = errors.New("digitalocean: rate limited")
	// ErrOutOfCapacity: the plan is not available in that region right now.
	ErrOutOfCapacity = errors.New("digitalocean: plan out of capacity in that region")
	// ErrInsufficientFunds: the account has no credit or billing is locked.
	ErrInsufficientFunds = errors.New("digitalocean: insufficient account credit")
)

// APIError preserves status without an untrusted provider response body.
type APIError struct {
	HTTPStatus int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("digitalocean: api error (http %d): %s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("digitalocean: api error (http %d)", e.HTTPStatus)
}

// Client talks to the DigitalOcean API. Safe for concurrent use.
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
			return fmt.Errorf("digitalocean: marshal request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("digitalocean: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("digitalocean: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("digitalocean: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var env struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &env)
		return classify(resp.StatusCode, env.ID, env.Message)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("digitalocean: decode response: %w", err)
	}
	return nil
}

// classify maps an HTTP failure onto a concrete error. Unclassified failures
// and 5xx return an APIError with no raw body to avoid leaking sensitive data
// such as egress IP addresses.
func classify(status int, errID, msg string) error {
	m := strings.ToLower(errID + " " + msg)
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
		strings.Contains(m, "droplet limit") || strings.Contains(m, "limit_exceeded")):
		return ErrOutOfCapacity
	case refused && (strings.Contains(m, "insufficient") || strings.Contains(m, "no credit") ||
		strings.Contains(m, "payment") || strings.Contains(m, "locked") || strings.Contains(m, "billing")):
		return ErrInsufficientFunds
	}
	return &APIError{HTTPStatus: status}
}

func parseDollarStr(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func dollarsToCents(v float64) int {
	return int(math.Round(v * 100))
}

// ── Account ─────────────────────────────────────────────────────────────

// Account is the account's money.
type Account struct {
	BalanceCents int
	PendingCents int
}

// Account proves the key works and reads the money (GET /v2/customers/my/balance).
func (c *Client) Account(ctx context.Context) (Account, error) {
	var out struct {
		MonthToDateBalance string `json:"month_to_date_balance"`
		AccountBalance     string `json:"account_balance"`
		MonthToDateUsage   string `json:"month_to_date_usage"`
	}
	if err := c.call(ctx, http.MethodGet, "/customers/my/balance", nil, &out); err != nil {
		return Account{}, err
	}
	acctBal := parseDollarStr(out.AccountBalance)
	usage := parseDollarStr(out.MonthToDateUsage)
	// Similar to Vultr, positive account_balance is money owed on finalized invoices,
	// negative is credit. MonthToDateUsage is accrued unbilled charges.
	return Account{
		BalanceCents: -dollarsToCents(acctBal) - dollarsToCents(usage),
		PendingCents: dollarsToCents(usage),
	}, nil
}

// ── Catalog ─────────────────────────────────────────────────────────────

// Region is one DigitalOcean datacenter location.
type Region struct {
	ID        string `json:"id"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Continent string `json:"continent"`
}

func regionLocation(slug, name string) (city, country, continent string) {
	city = strings.TrimSpace(name)
	for i, f := range strings.Fields(name) {
		if i == 0 {
			city = f
		}
	}
	slugPrefix := slug
	if len(slugPrefix) >= 3 {
		slugPrefix = slug[:3]
	}
	switch slugPrefix {
	case "ams":
		country = "NL"
		continent = "Europe"
	case "fra":
		country = "DE"
		continent = "Europe"
	case "lon":
		country = "GB"
		continent = "Europe"
	case "nyc":
		country = "US"
		continent = "North America"
	case "sfo":
		country = "US"
		continent = "North America"
	case "tor":
		country = "CA"
		continent = "North America"
	case "sgp":
		country = "SG"
		continent = "Asia"
	case "blr":
		country = "IN"
		continent = "Asia"
	case "syd":
		country = "AU"
		continent = "Oceania"
	default:
		country = city
		continent = "Global"
	}
	return
}

// Regions lists the active regions (GET /v2/regions).
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	var out struct {
		Regions []struct {
			Name      string   `json:"name"`
			Slug      string   `json:"slug"`
			Sizes     []string `json:"sizes"`
			Available bool     `json:"available"`
		} `json:"regions"`
	}
	if err := c.call(ctx, http.MethodGet, "/regions?per_page="+strconv.Itoa(listPageSize), nil, &out); err != nil {
		return nil, err
	}
	regions := make([]Region, 0, len(out.Regions))
	for _, r := range out.Regions {
		if !r.Available {
			continue
		}
		city, country, continent := regionLocation(r.Slug, r.Name)
		regions = append(regions, Region{
			ID:        r.Slug,
			City:      city,
			Country:   country,
			Continent: continent,
		})
	}
	return regions, nil
}

// Plan is one machine size.
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

// Plans lists machine sizes (GET /v2/sizes).
func (c *Client) Plans(ctx context.Context) ([]Plan, error) {
	var out struct {
		Sizes []struct {
			Slug         string   `json:"slug"`
			Memory       int      `json:"memory"`
			VCPUs        int      `json:"vcpus"`
			Disk         int      `json:"disk"`
			Transfer     float64  `json:"transfer"` // TB
			PriceMonthly float64  `json:"price_monthly"`
			Regions      []string `json:"regions"`
			Available    bool     `json:"available"`
		} `json:"sizes"`
	}
	if err := c.call(ctx, http.MethodGet, "/sizes?per_page="+strconv.Itoa(listPageSize), nil, &out); err != nil {
		return nil, err
	}
	plans := make([]Plan, 0, len(out.Sizes))
	for _, s := range out.Sizes {
		if !s.Available && len(s.Regions) == 0 {
			continue
		}
		bw := int(math.Round(s.Transfer * 1024))
		if bw == 0 {
			bw = 1024
		}
		plans = append(plans, Plan{
			ID:                s.Slug,
			VCPUs:             s.VCPUs,
			RAMMB:             s.Memory,
			DiskGB:            s.Disk,
			BandwidthGB:       bw,
			MonthlyCents:      dollarsToCents(s.PriceMonthly),
			OverageCentsPerTB: overageCentsPerTB,
			Regions:           s.Regions,
		})
	}
	return plans, nil
}

// ── SSH Keys ────────────────────────────────────────────────────────────

// SSHKey is one public key on the account.
type SSHKey struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// SSHKeys lists all keys on the account (GET /v2/account/keys).
func (c *Client) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	var out struct {
		SSHKeys []SSHKey `json:"ssh_keys"`
	}
	if err := c.call(ctx, http.MethodGet, "/account/keys?per_page="+strconv.Itoa(listPageSize), nil, &out); err != nil {
		return nil, err
	}
	return out.SSHKeys, nil
}

// EnsureSSHKey finds or uploads publicKey under name and returns its string ID.
func (c *Client) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	want := normalizeKey(publicKey)
	if want == "" {
		return "", fmt.Errorf("digitalocean: empty ssh public key")
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
	if err := c.call(ctx, http.MethodPost, "/account/keys", body, &out); err != nil {
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

// ── Droplets (Instances) ────────────────────────────────────────────────

// Instance is one droplet.
type Instance struct {
	ID      string
	Label   string
	Region  string
	Plan    string
	Status  string
	Address string
	Tags    []string
}

type wireDroplet struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	SizeSlug string `json:"size_slug"`
	Region   struct {
		Slug string `json:"slug"`
	} `json:"region"`
	Networks struct {
		V4 []struct {
			IPAddress string `json:"ip_address"`
			Type      string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
	Tags []string `json:"tags"`
}

func (w wireDroplet) toInstance() Instance {
	addr := ""
	for _, net := range w.Networks.V4 {
		if net.Type == "public" {
			addr = publicIP(net.IPAddress)
			if addr != "" {
				break
			}
		}
	}
	return Instance{
		ID:      strconv.Itoa(w.ID),
		Label:   w.Name,
		Region:  w.Region.Slug,
		Plan:    w.SizeSlug,
		Status:  w.Status,
		Address: addr,
		Tags:    w.Tags,
	}
}

func publicIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "0.0.0.0" {
		return ""
	}
	return ip
}

// CreateParams defines droplet order parameters.
type CreateParams struct {
	Region   string
	Plan     string
	Image    string
	Label    string
	Hostname string
	SSHKeyID string
	UserData string
	Tags     []string
}

// Create orders a machine (POST /v2/droplets).
func (c *Client) Create(ctx context.Context, p CreateParams) (Instance, error) {
	image := p.Image
	if image == "" {
		image = "ubuntu-24-04-x64"
	}
	name := p.Hostname
	if name == "" {
		name = p.Label
	}
	body := map[string]any{
		"name":               name,
		"region":             p.Region,
		"size":               p.Plan,
		"image":              image,
		"backups":            false,
		"ipv6":               false,
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
	if len(p.Tags) > 0 {
		body["tags"] = p.Tags
	}
	var out struct {
		Droplet wireDroplet `json:"droplet"`
	}
	if err := c.call(ctx, http.MethodPost, "/droplets", body, &out); err != nil {
		return Instance{}, err
	}
	return out.Droplet.toInstance(), nil
}

// CreateInstance is an alias for Create.
func (c *Client) CreateInstance(ctx context.Context, p CreateParams) (Instance, error) {
	return c.Create(ctx, p)
}

// Instance reads one droplet (GET /v2/droplets/{id}).
func (c *Client) Instance(ctx context.Context, id string) (Instance, error) {
	var out struct {
		Droplet wireDroplet `json:"droplet"`
	}
	if err := c.call(ctx, http.MethodGet, "/droplets/"+url.PathEscape(id), nil, &out); err != nil {
		return Instance{}, err
	}
	return out.Droplet.toInstance(), nil
}

// GetInstance is an alias for Instance.
func (c *Client) GetInstance(ctx context.Context, id string) (Instance, error) {
	return c.Instance(ctx, id)
}

// ListInstances lists droplets, optionally filtered by tag.
func (c *Client) ListInstances(ctx context.Context, tag string) ([]Instance, error) {
	var list []Instance
	page := 1
	for {
		if page > maxListPages {
			return nil, fmt.Errorf("digitalocean: droplet list longer than %d pages", maxListPages)
		}
		q := url.Values{
			"page":     {strconv.Itoa(page)},
			"per_page": {strconv.Itoa(listPageSize)},
		}
		if tag != "" {
			q.Set("tag_name", tag)
		}
		var out struct {
			Droplets []wireDroplet `json:"droplets"`
			Links    struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		if err := c.call(ctx, http.MethodGet, "/droplets?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, d := range out.Droplets {
			list = append(list, d.toInstance())
		}
		if out.Links.Pages.Next == "" {
			return list, nil
		}
		page++
	}
}

// Instances lists every droplet on the account (GET /v2/droplets).
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	return c.ListInstances(ctx, "")
}

// Reboot restarts a droplet (POST /v2/droplets/{id}/actions).
func (c *Client) Reboot(ctx context.Context, id string) error {
	body := map[string]string{"type": "reboot"}
	return c.call(ctx, http.MethodPost, "/droplets/"+url.PathEscape(id)+"/actions", body, nil)
}

// RebootInstance is an alias for Reboot.
func (c *Client) RebootInstance(ctx context.Context, id string) error {
	return c.Reboot(ctx, id)
}

// Delete destroys a droplet (DELETE /v2/droplets/{id}).
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/droplets/"+url.PathEscape(id), nil, nil)
}

// DeleteInstance is an alias for Delete.
func (c *Client) DeleteInstance(ctx context.Context, id string) error {
	return c.Delete(ctx, id)
}
