// Package vultr is a direct HTTPS client for the Vultr v2 API, the hoster the
// hub rents relay machines from (plan 42 П9.2; first hoster by the owner's
// decision of 22.10, because the fleet already runs on it). It is a pure
// client: it knows nothing of the entry package, returns its own types, and
// reports failures as concrete error values. The adapter that makes it an
// entry.BoxProvider lives in internal/entrywire.
//
// Every call is a JSON request with the API key as a bearer token. The wire
// shapes below are the documented v2 ones; what the hub depends on is kept
// narrow on purpose — an id, an address, a price — so a field added upstream
// cannot break an order.
package vultr

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

// DefaultBaseURL is the v2 API root.
const DefaultBaseURL = "https://api.vultr.com/v2"

// requestTimeout caps a single call. An instance create answers in a second or
// two; the wait for the machine to boot is the caller's loop, not one request.
const requestTimeout = 30 * time.Second

// maxResponseBytes bounds how much of a response body is read. The plan list
// is the largest envelope and is tens of kilobytes.
const maxResponseBytes = 4 << 20

// listPageSize asks for every row in one page. The lists the hub reads
// (regions, plans, keys) are small enough for one; the instance list is
// followed to its last page all the same (Instances), because it is how the
// hub learns whether a lost order made a machine, and half of it is "no".
const listPageSize = 500

// maxListPages bounds the cursor walk: 50 pages of 500 is past any account
// this hub runs, and a cursor that loops must not hold the pass forever.
const maxListPages = 50

var (
	// ErrBadCredentials: the API key was rejected (401/403).
	ErrBadCredentials = errors.New("vultr: credentials rejected")
	// ErrNotFound: no such object (404) — an instance already deleted, a key
	// that is gone.
	ErrNotFound = errors.New("vultr: not found")
	// ErrRateLimited: too many calls in the window (429).
	ErrRateLimited = errors.New("vultr: rate limited")
	// ErrOutOfCapacity: the plan is not available in that region right now.
	// Distinct from a wrong plan id: the order is right and the hoster has
	// nothing to sell, so another region is the answer.
	ErrOutOfCapacity = errors.New("vultr: plan out of capacity in that region")
	// ErrInsufficientFunds: the account has no credit for the order.
	ErrInsufficientFunds = errors.New("vultr: insufficient account credit")
)

// APIError is a failure the API described in its own words.
type APIError struct {
	HTTPStatus int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("vultr: api error (http %d): %s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("vultr: api error (http %d)", e.HTTPStatus)
}

// Client talks to the Vultr API. Safe for concurrent use.
type Client struct {
	apiKey  string
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

// New returns a Client authenticating with apiKey — the key the owner saved in
// the console, sealed at rest (entry_providers). The client reads it from
// nowhere else.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: requestTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// call sends one request and decodes the response into out (nil to discard a
// 204). body nil sends no body.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vultr: marshal request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("vultr: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vultr: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("vultr: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var env struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &env)
		return classify(resp.StatusCode, env.Error)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("vultr: decode response: %w", err)
	}
	return nil
}

// classify maps an HTTP failure onto a concrete error. The status carries most
// of the meaning here (unlike Porkbun, which answers 200 with an envelope), and
// the message is matched only for the two the budget gate has to tell apart —
// and only on a 4xx, which is Vultr saying no. A 5xx is Vultr failing whatever
// its body says, and an order that met one may have made a machine (audit 42
// recheck НН7): it stays an APIError, never a refusal.
func classify(status int, msg string) error {
	m := strings.ToLower(msg)
	refused := status >= 400 && status < 500
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		// Vultr answers 401 both to a wrong key and to a right key used from
		// an address outside the account's API access control ("Unauthorized
		// IP address: ..."). Only its own words tell the owner which of the
		// two to fix, so they ride along with the sentinel.
		if msg = strings.TrimSpace(msg); msg != "" {
			return fmt.Errorf("%w: %s", ErrBadCredentials, msg)
		}
		return ErrBadCredentials
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case refused && (strings.Contains(m, "out of stock") || strings.Contains(m, "no available") ||
		strings.Contains(m, "not available in") || strings.Contains(m, "capacity")):
		return ErrOutOfCapacity
	case refused && (strings.Contains(m, "insufficient") || strings.Contains(m, "no credit") ||
		strings.Contains(m, "payment")):
		return ErrInsufficientFunds
	}
	return &APIError{HTTPStatus: status, Message: msg}
}

// listQuery is the per_page parameter every list call sends.
func listQuery() string {
	return "?" + url.Values{"per_page": {strconv.Itoa(listPageSize)}}.Encode()
}

// ── Account ─────────────────────────────────────────────────────────────

// Account is the account's money.
type Account struct {
	// BalanceCents is the credit left, in whole cents, as the billing page
	// calls it ("Remaining Credit"); negative on an account that owes.
	BalanceCents int
	// PendingCents is this period's accrued, unbilled charges.
	PendingCents int
}

// Account proves the key works and reads the money (GET /account).
func (c *Client) Account(ctx context.Context) (Account, error) {
	var out struct {
		Account struct {
			Balance        float64 `json:"balance"`
			PendingCharges float64 `json:"pending_charges"`
		} `json:"account"`
	}
	if err := c.call(ctx, http.MethodGet, "/account", nil, &out); err != nil {
		return Account{}, err
	}
	// Vultr's `balance` is what the account owes on finalized invoices, so a
	// prepaid account reads negative (-6.00 is six dollars of credit), and
	// this month's accrual is not in it yet. What is left to spend is the
	// two together with the sign turned: shown raw, the console said
	// "$-6.00" for an account with $3.66 left (stand, 25.10).
	return Account{
		BalanceCents: -dollarsToCents(out.Account.Balance) - dollarsToCents(out.Account.PendingCharges),
		PendingCents: dollarsToCents(out.Account.PendingCharges),
	}, nil
}

// dollarsToCents turns the API's dollar float into whole cents. Rounded, not
// truncated: a price of 5.00 that arrives as 4.999999 is five dollars.
func dollarsToCents(v float64) int {
	return int(math.Round(v * 100))
}

// ── Catalog ─────────────────────────────────────────────────────────────

// Region is one Vultr location.
type Region struct {
	ID        string `json:"id"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Continent string `json:"continent"`
}

// Regions lists every location (GET /regions).
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	var out struct {
		Regions []Region `json:"regions"`
	}
	if err := c.call(ctx, http.MethodGet, "/regions"+listQuery(), nil, &out); err != nil {
		return nil, err
	}
	return out.Regions, nil
}

// Plan is one machine size.
type Plan struct {
	ID           string
	VCPUs        int
	RAMMB        int
	DiskGB       int
	BandwidthGB  int
	MonthlyCents int
	// OverageCentsPerTB is what traffic past the account's pooled allowance
	// costs. Not in the API's answer: it is Vultr's published price for
	// bandwidth over the pool, $0.01 per GB, written here as the one place
	// that knows it.
	OverageCentsPerTB int
	// Regions is where the plan can be ordered.
	Regions []string
}

// overageCentsPerTB is Vultr's published price for traffic past the
// account's pooled bandwidth allowance: $0.01 per GB.
const overageCentsPerTB = 1000

// Plans lists the plans on offer (GET /plans). Vultr's `type` filter is not
// applied: which families the owner may order from is a policy question, and
// the console shows what the account can actually buy.
func (c *Client) Plans(ctx context.Context) ([]Plan, error) {
	var out struct {
		Plans []struct {
			ID          string   `json:"id"`
			VCPUCount   int      `json:"vcpu_count"`
			RAM         int      `json:"ram"`
			Disk        int      `json:"disk"`
			Bandwidth   int      `json:"bandwidth"`
			MonthlyCost float64  `json:"monthly_cost"`
			Locations   []string `json:"locations"`
		} `json:"plans"`
	}
	if err := c.call(ctx, http.MethodGet, "/plans"+listQuery(), nil, &out); err != nil {
		return nil, err
	}
	plans := make([]Plan, 0, len(out.Plans))
	for _, p := range out.Plans {
		plans = append(plans, Plan{
			ID: p.ID, VCPUs: p.VCPUCount, RAMMB: p.RAM, DiskGB: p.Disk,
			BandwidthGB: p.Bandwidth, MonthlyCents: dollarsToCents(p.MonthlyCost),
			OverageCentsPerTB: overageCentsPerTB, Regions: p.Locations,
		})
	}
	return plans, nil
}

// OS is one installable image.
type OS struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Arch   string `json:"arch"`
	Family string `json:"family"`
}

// OSes lists the images on offer (GET /os).
func (c *Client) OSes(ctx context.Context) ([]OS, error) {
	var out struct {
		OS []OS `json:"os"`
	}
	if err := c.call(ctx, http.MethodGet, "/os"+listQuery(), nil, &out); err != nil {
		return nil, err
	}
	return out.OS, nil
}

// UbuntuLTS is the newest 64-bit Ubuntu LTS image the account can install.
//
// Resolved from the hoster's own list rather than hardcoded: an os id is a
// number that means nothing locally, goes stale every two years, and would
// make a wrong order succeed quietly on the wrong distribution. The relay
// deploy targets Ubuntu LTS, so that is what is asked for by name.
func (c *Client) UbuntuLTS(ctx context.Context) (OS, error) {
	list, err := c.OSes(ctx)
	if err != nil {
		return OS{}, err
	}
	best := OS{}
	bestVer := -1.0
	for _, o := range list {
		if o.Arch != "x64" || !strings.Contains(o.Name, "Ubuntu") || !strings.Contains(o.Name, "LTS") {
			continue
		}
		v := ubuntuVersion(o.Name)
		if v > bestVer {
			best, bestVer = o, v
		}
	}
	if bestVer < 0 {
		return OS{}, fmt.Errorf("vultr: no 64-bit Ubuntu LTS image on offer")
	}
	return best, nil
}

// ubuntuVersion pulls the "24.04" out of "Ubuntu 24.04 LTS x64"; -1 when the
// name carries no version.
func ubuntuVersion(name string) float64 {
	for _, f := range strings.Fields(name) {
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			return v
		}
	}
	return -1
}

// ── SSH keys ────────────────────────────────────────────────────────────

// SSHKey is one key on the account.
type SSHKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"ssh_key"`
}

// SSHKeys lists the keys on the account (GET /ssh-keys).
func (c *Client) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	var out struct {
		SSHKeys []SSHKey `json:"ssh_keys"`
	}
	if err := c.call(ctx, http.MethodGet, "/ssh-keys"+listQuery(), nil, &out); err != nil {
		return nil, err
	}
	return out.SSHKeys, nil
}

// EnsureSSHKey returns the id of publicKey on the account, uploading it under
// name if it is not there yet.
//
// Matched on the key material and not on the name: the name is ours to choose
// and a second hub (or a second run) must reuse the key rather than pile up
// duplicates that an order then has to choose between.
func (c *Client) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	want := normalizeKey(publicKey)
	if want == "" {
		return "", fmt.Errorf("vultr: empty ssh public key")
	}
	keys, err := c.SSHKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if normalizeKey(k.Key) == want {
			return k.ID, nil
		}
	}
	var out struct {
		SSHKey SSHKey `json:"ssh_key"`
	}
	body := map[string]string{"name": name, "ssh_key": publicKey}
	if err := c.call(ctx, http.MethodPost, "/ssh-keys", body, &out); err != nil {
		return "", err
	}
	return out.SSHKey.ID, nil
}

// normalizeKey compares keys by type and material, ignoring the trailing
// comment: the same key uploaded with another comment is the same key.
func normalizeKey(k string) string {
	f := strings.Fields(strings.TrimSpace(k))
	if len(f) < 2 {
		return strings.TrimSpace(k)
	}
	return f[0] + " " + f[1]
}

// ── Instances ───────────────────────────────────────────────────────────

// Instance is one machine.
type Instance struct {
	ID      string
	Label   string
	Region  string
	Plan    string
	Status  string
	Address string
	Tags    []string
}

// wireInstance is the API's shape.
type wireInstance struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Region string   `json:"region"`
	Plan   string   `json:"plan"`
	Status string   `json:"status"`
	MainIP string   `json:"main_ip"`
	Tags   []string `json:"tags"`
}

func (w wireInstance) toInstance() Instance {
	return Instance{
		ID: w.ID, Label: w.Label, Region: w.Region, Plan: w.Plan,
		Status: w.Status, Address: publicIP(w.MainIP), Tags: w.Tags,
	}
}

// publicIP drops the placeholder Vultr returns while a machine is still being
// built. An address of 0.0.0.0 is not an address, and the caller that waits
// for one must not be handed it.
func publicIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "0.0.0.0" {
		return ""
	}
	return ip
}

// CreateParams is one order.
type CreateParams struct {
	Region   string
	Plan     string
	OSID     int
	Label    string
	Hostname string
	SSHKeyID string
	Tags     []string
}

// Create orders a machine (POST /instances). It returns as soon as the order
// is taken; the instance's Address is usually empty until it has booted, so
// the caller polls [Client.Instance].
//
// Backups are off and IPv6 is off: a relay is a throwaway whose whole state
// lives in the hub, paying for snapshots of it is waste, and a second address
// family is a second thing to get the firewall right on.
func (c *Client) Create(ctx context.Context, p CreateParams) (Instance, error) {
	if p.OSID == 0 {
		os, err := c.UbuntuLTS(ctx)
		if err != nil {
			return Instance{}, err
		}
		p.OSID = os.ID
	}
	body := map[string]any{
		"region":      p.Region,
		"plan":        p.Plan,
		"os_id":       p.OSID,
		"label":       p.Label,
		"hostname":    p.Hostname,
		"backups":     "disabled",
		"enable_ipv6": false,
	}
	if p.SSHKeyID != "" {
		body["sshkey_id"] = []string{p.SSHKeyID}
	}
	if len(p.Tags) > 0 {
		body["tags"] = p.Tags
	}
	var out struct {
		Instance wireInstance `json:"instance"`
	}
	if err := c.call(ctx, http.MethodPost, "/instances", body, &out); err != nil {
		return Instance{}, err
	}
	return out.Instance.toInstance(), nil
}

// Instance reads one machine (GET /instances/{id}).
func (c *Client) Instance(ctx context.Context, id string) (Instance, error) {
	var out struct {
		Instance wireInstance `json:"instance"`
	}
	if err := c.call(ctx, http.MethodGet, "/instances/"+url.PathEscape(id), nil, &out); err != nil {
		return Instance{}, err
	}
	return out.Instance.toInstance(), nil
}

// Instances lists every machine on the account (GET /instances).
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	var list []Instance
	cursor := ""
	for page := 0; ; page++ {
		if page == maxListPages {
			return nil, fmt.Errorf("vultr: instance list longer than %d pages", maxListPages)
		}
		q := url.Values{"per_page": {strconv.Itoa(listPageSize)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var out struct {
			Instances []wireInstance `json:"instances"`
			Meta      struct {
				Links struct {
					Next string `json:"next"`
				} `json:"links"`
			} `json:"meta"`
		}
		if err := c.call(ctx, http.MethodGet, "/instances?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, w := range out.Instances {
			list = append(list, w.toInstance())
		}
		if cursor = out.Meta.Links.Next; cursor == "" {
			return list, nil
		}
	}
}

// Reboot restarts a machine (POST /instances/{id}/reboot).
func (c *Client) Reboot(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/reboot", nil, nil)
}

// Delete destroys a machine and stops its charge (DELETE /instances/{id}).
// An instance already gone comes back as ErrNotFound, which the caller is
// free to treat as done.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/instances/"+url.PathEscape(id), nil, nil)
}
