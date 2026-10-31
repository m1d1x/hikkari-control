// Package vdsina is a direct HTTPS client for the VDSina Public API v1
// (https://userapi.vdsina.ru/v1 or https://userapi.vdsina.com/v1).
// It is a pure client: it knows nothing of the entry package, returns its
// own types, and reports failures as concrete error values. The adapter that
// makes it an entry.BoxProvider lives in internal/entrywire.
package vdsina

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

// DefaultBaseURL is the VDSina API v1 root.
const DefaultBaseURL = "https://userapi.vdsina.ru/v1"

// FallbackBaseURL is the international VDSina API v1 root.
const FallbackBaseURL = "https://userapi.vdsina.com/v1"

// requestTimeout caps a single call.
const requestTimeout = 30 * time.Second

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 4 << 20

// overageCentsPerTB is VDSina bandwidth overage (0 as VDSina does not bill per GB over pool).
const overageCentsPerTB = 0

var (
	// ErrBadCredentials: the API token was rejected (401/403).
	ErrBadCredentials = errors.New("vdsina: credentials rejected")
	// ErrNotFound: no such object (404) — server or key already deleted.
	ErrNotFound = errors.New("vdsina: not found")
	// ErrRateLimited: too many calls in the window (429).
	ErrRateLimited = errors.New("vdsina: rate limited")
	// ErrOutOfCapacity: the plan is not available in that datacenter right now.
	ErrOutOfCapacity = errors.New("vdsina: plan out of capacity in that datacenter")
	// ErrInsufficientFunds: the account has insufficient balance for the order.
	ErrInsufficientFunds = errors.New("vdsina: insufficient account balance")
)

// APIError preserves status without an untrusted provider response body.
type APIError struct {
	HTTPStatus int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("vdsina: api error (http %d): %s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("vdsina: api error (http %d)", e.HTTPStatus)
}

// Client talks to the VDSina API. Safe for concurrent use.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (e.g. for tests or userapi.vdsina.com).
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

// apiEnvelope is VDSina standard envelope format.
type apiEnvelope[T any] struct {
	Status     string `json:"status"`
	StatusMsg  string `json:"status_msg"`
	StatusCode int    `json:"status_code"`
	Data       T      `json:"data"`
}

// call sends one request and decodes the response into out.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var bodyBuf []byte
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vdsina: marshal request: %w", err)
		}
		bodyBuf = buf
	}

	raw, statusCode, baseEnv, err := c.doRequest(ctx, c.baseURL, method, path, bodyBuf)
	if err != nil {
		return err
	}

	// If default .ru returned 401 Unauthorized, automatically try international .com
	if (statusCode == http.StatusUnauthorized || baseEnv.Status == "error" && strings.Contains(strings.ToLower(baseEnv.StatusMsg), "unauthorized")) && c.baseURL == DefaultBaseURL {
		raw2, sc2, env2, err2 := c.doRequest(ctx, FallbackBaseURL, method, path, bodyBuf)
		if err2 == nil && sc2 >= 200 && sc2 <= 299 && env2.Status != "error" {
			c.baseURL = FallbackBaseURL
			raw = raw2
			statusCode = sc2
			baseEnv = env2
		}
	}

	if statusCode < 200 || statusCode > 299 {
		code := statusCode
		if baseEnv.StatusCode != 0 {
			code = baseEnv.StatusCode
		}
		return classify(code, baseEnv.StatusMsg)
	}
	if baseEnv.Status == "error" {
		code := http.StatusBadRequest
		if baseEnv.StatusCode != 0 {
			code = baseEnv.StatusCode
		}
		return classify(code, baseEnv.StatusMsg)
	}

	if out == nil || len(baseEnv.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(baseEnv.Data, out); err != nil {
		return fmt.Errorf("vdsina: decode response data: %w", err)
	}
	_ = raw
	return nil
}

type vdsinaBaseEnv struct {
	Status      string          `json:"status"`
	StatusMsg   string          `json:"status_msg"`
	StatusCode  int             `json:"status_code"`
	Description *string         `json:"description"`
	Data        json.RawMessage `json:"data"`
}

func (c *Client) doRequest(ctx context.Context, baseURL, method, path string, bodyBuf []byte) ([]byte, int, vdsinaBaseEnv, error) {
	var rdr io.Reader
	if len(bodyBuf) > 0 {
		rdr = bytes.NewReader(bodyBuf)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, rdr)
	if err != nil {
		return nil, 0, vdsinaBaseEnv{}, fmt.Errorf("vdsina: build request: %w", err)
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Accept", "application/json")
	if len(bodyBuf) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, vdsinaBaseEnv{}, fmt.Errorf("vdsina: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, vdsinaBaseEnv{}, fmt.Errorf("vdsina: read response: %w", err)
	}

	var baseEnv vdsinaBaseEnv
	_ = json.Unmarshal(raw, &baseEnv)
	return raw, resp.StatusCode, baseEnv, nil
}

// classify maps an HTTP or API failure onto a concrete error. Unclassified
// failures and 5xx return an APIError with no raw body to avoid leaking sensitive data.
func classify(status int, msg string) error {
	m := strings.ToLower(msg)
	refused := status >= 400 && status < 500
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden ||
		strings.Contains(m, "unauthorized") || strings.Contains(m, "auth error") || strings.Contains(m, "token"):
		if strings.Contains(m, "unauthorized ip") {
			return fmt.Errorf("%w: check the account IP allowlist", ErrBadCredentials)
		}
		return ErrBadCredentials
	case status == http.StatusNotFound || strings.Contains(m, "not found"):
		return ErrNotFound
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case refused && (strings.Contains(m, "out of stock") || strings.Contains(m, "no available") ||
		strings.Contains(m, "not available") || strings.Contains(m, "capacity") ||
		strings.Contains(m, "limit")):
		return ErrOutOfCapacity
	case refused && (strings.Contains(m, "insufficient") || strings.Contains(m, "balance") ||
		strings.Contains(m, "money") || strings.Contains(m, "funds") || strings.Contains(m, "payment")):
		return ErrInsufficientFunds
	}
	return &APIError{HTTPStatus: status}
}

// ── Account ─────────────────────────────────────────────────────────────

// Account is the account's money.
type Account struct {
	BalanceCents int
	PendingCents int
}

// Account proves the token works and reads balance (GET /v1/account.balance).
func (c *Client) Account(ctx context.Context) (Account, error) {
	var out struct {
		Real     any    `json:"real"`
		Bonus    any    `json:"bonus"`
		Currency string `json:"currency"`
	}
	if err := c.call(ctx, http.MethodGet, "/account.balance", nil, &out); err != nil {
		return Account{}, err
	}
	total := toFloat(out.Real) + toFloat(out.Bonus)
	return Account{
		BalanceCents: int(math.Round(total * 100)),
		PendingCents: 0,
	}, nil
}

func toFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	default:
		return 0
	}
}

func isActive(v any) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case float64:
		return val != 0
	case string:
		return val == "1" || strings.ToLower(val) == "true"
	default:
		return true
	}
}

// ── Catalog ─────────────────────────────────────────────────────────────

// Region is one VDSina datacenter.
type Region struct {
	ID        string `json:"id"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Continent string `json:"continent"`
}

type wireDatacenter struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	City    string `json:"city"`
	Active  any    `json:"active"`
}

// Regions lists available datacenters (GET /v1/datacenter).
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	var out []wireDatacenter
	if err := c.call(ctx, http.MethodGet, "/datacenter", nil, &out); err != nil {
		return nil, err
	}
	regions := make([]Region, 0, len(out))
	for _, d := range out {
		city := d.City
		country := d.Country
		if city == "" {
			parts := strings.Split(d.Name, ",")
			if len(parts) > 0 {
				city = strings.TrimSpace(parts[0])
			} else {
				city = d.Name
			}
		}
		if country == "" {
			parts := strings.Split(d.Name, ",")
			if len(parts) > 1 {
				country = strings.TrimSpace(parts[1])
			} else {
				country = city
			}
		}
		continent := "Europe"
		if strings.Contains(strings.ToLower(country), "us") || strings.Contains(strings.ToLower(city), "usa") {
			continent = "North America"
		}
		regions = append(regions, Region{
			ID:        strconv.Itoa(d.ID),
			City:      city,
			Country:   country,
			Continent: continent,
		})
	}
	return regions, nil
}

// Plan is one server tariff.
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

type wirePlan struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Cost    float64 `json:"cost"`
	Period  string  `json:"period"`
	CPU     int     `json:"cpu"`
	RAM     float64 `json:"ram"`
	Disk    int     `json:"disk"`
	Enable  any     `json:"enable"`
	Active  any     `json:"active"`
	Data    struct {
		Datacenter []int `json:"datacenter"`
		CPU        struct {
			Value int `json:"value"`
		} `json:"cpu"`
		RAM struct {
			Value int `json:"value"`
		} `json:"ram"`
		Disk struct {
			Value int `json:"value"`
		} `json:"disk"`
		Traff struct {
			Value int `json:"value"`
		} `json:"traff"`
	} `json:"data"`
}

// Plans lists the available server plans (GET /v1/server-plan or via /server-group).
func (c *Client) Plans(ctx context.Context) ([]Plan, error) {
	var out []wirePlan
	err := c.call(ctx, http.MethodGet, "/server-plan", nil, &out)
	if err != nil {
		// VDSina v1 groups plans by server-group
		var groups []struct {
			ID     int    `json:"id"`
			Active any    `json:"active"`
			Type   string `json:"type"`
		}
		if gErr := c.call(ctx, http.MethodGet, "/server-group", nil, &groups); gErr == nil {
			for _, g := range groups {
				if g.Type == "vds" && isActive(g.Active) {
					var groupPlans []wirePlan
					if pErr := c.call(ctx, http.MethodGet, fmt.Sprintf("/server-plan/%d", g.ID), nil, &groupPlans); pErr == nil {
						out = append(out, groupPlans...)
					}
				}
			}
		}
		if len(out) == 0 && err != nil {
			return nil, err
		}
	}
	// Fetch datacenters to map fallback regions if a plan has no explicit datacenter array
	allRegions, _ := c.Regions(ctx)
	allRegionIDs := make([]string, 0, len(allRegions))
	for _, r := range allRegions {
		allRegionIDs = append(allRegionIDs, r.ID)
	}

	plans := make([]Plan, 0, len(out))
	seen := make(map[int]bool)
	for _, p := range out {
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		if !isActive(p.Active) || !isActive(p.Enable) {
			continue
		}
		cpu := p.CPU
		if cpu == 0 && p.Data.CPU.Value != 0 {
			cpu = p.Data.CPU.Value
		}
		if cpu == 0 {
			cpu = 1
		}

		ramMB := int(math.Round(p.RAM))
		if ramMB < 512 {
			// Memory given in GB
			ramMB = int(math.Round(p.RAM * 1024))
		}
		if ramMB == 0 && p.Data.RAM.Value != 0 {
			ramMB = p.Data.RAM.Value * 1024
		}
		if ramMB == 0 {
			ramMB = 1024
		}

		diskGB := p.Disk
		if diskGB == 0 && p.Data.Disk.Value != 0 {
			diskGB = p.Data.Disk.Value
		}
		if diskGB == 0 {
			diskGB = 10
		}

		bwGB := 32768
		if p.Data.Traff.Value > 0 {
			bwGB = p.Data.Traff.Value * 1024
		}

		monthly := p.Cost
		if strings.Contains(strings.ToLower(p.Period), "day") {
			monthly = p.Cost * 30
		}

		var locs []string
		if len(p.Data.Datacenter) > 0 {
			for _, dc := range p.Data.Datacenter {
				locs = append(locs, strconv.Itoa(dc))
			}
		} else {
			locs = allRegionIDs
		}

		plans = append(plans, Plan{
			ID:                strconv.Itoa(p.ID),
			VCPUs:             cpu,
			RAMMB:             ramMB,
			DiskGB:            diskGB,
			BandwidthGB:       bwGB,
			MonthlyCents:      int(math.Round(monthly * 100)),
			OverageCentsPerTB: overageCentsPerTB,
			Regions:           locs,
		})
	}
	return plans, nil
}

// Template is one OS template.
type Template struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	SSHKey bool   `json:"ssh-key"`
}

// Templates lists OS templates (GET /v1/template).
func (c *Client) Templates(ctx context.Context) ([]Template, error) {
	var out []Template
	if err := c.call(ctx, http.MethodGet, "/template", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UbuntuLTS resolves newest 64-bit Ubuntu LTS template.
func (c *Client) UbuntuLTS(ctx context.Context) (Template, error) {
	list, err := c.Templates(ctx)
	if err != nil {
		return Template{}, err
	}
	best := Template{}
	bestVer := -1.0
	for _, t := range list {
		name := strings.ToLower(t.Name)
		if !strings.Contains(name, "ubuntu") {
			continue
		}
		for _, f := range strings.Fields(name) {
			if v, err := strconv.ParseFloat(f, 64); err == nil {
				if v > bestVer {
					best, bestVer = t, v
				}
			}
		}
	}
	if bestVer < 0 {
		return Template{}, fmt.Errorf("vdsina: no Ubuntu template on offer")
	}
	return best, nil
}

// ── SSH Keys ────────────────────────────────────────────────────────────

// SSHKey is one SSH key on the account.
type SSHKey struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Data string `json:"data"`
}

// SSHKeys lists keys on the account (GET /v1/ssh-key).
func (c *Client) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	var out []SSHKey
	if err := c.call(ctx, http.MethodGet, "/ssh-key", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureSSHKey finds or uploads publicKey under name and returns its string ID.
func (c *Client) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	want := normalizeKey(publicKey)
	if want == "" {
		return "", fmt.Errorf("vdsina: empty ssh public key")
	}
	keys, err := c.SSHKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if normalizeKey(k.Data) == want {
			return strconv.Itoa(k.ID), nil
		}
	}
	body := map[string]string{"name": name, "data": publicKey}
	var out SSHKey
	if err := c.call(ctx, http.MethodPost, "/ssh-key", body, &out); err != nil {
		return "", err
	}
	return strconv.Itoa(out.ID), nil
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
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	StatusText     string `json:"status_text"`
	IP             string `json:"ip"`
	Datacenter     any    `json:"datacenter"`
	DatacenterName string `json:"datacenter_name"`
	ServerPlan     any    `json:"server_plan"`
}

func (w wireServer) toInstance() Instance {
	dc := ""
	switch v := w.Datacenter.(type) {
	case float64:
		dc = strconv.Itoa(int(v))
	case string:
		dc = v
	}
	plan := ""
	switch v := w.ServerPlan.(type) {
	case float64:
		plan = strconv.Itoa(int(v))
	case string:
		plan = v
	}
	return Instance{
		ID:      strconv.Itoa(w.ID),
		Label:   w.Name,
		Region:  dc,
		Plan:    plan,
		Status:  w.Status,
		Address: publicIP(w.IP),
		Tags:    nil,
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
	Region     string
	Plan       string
	TemplateID int
	Label      string
	Hostname   string
	SSHKeyID   string
	Tags       []string
}

// Create orders a machine (POST /v1/server).
func (c *Client) Create(ctx context.Context, p CreateParams) (Instance, error) {
	dcID, err := strconv.Atoi(p.Region)
	if err != nil {
		return Instance{}, fmt.Errorf("vdsina: invalid datacenter id %q", p.Region)
	}
	planID, err := strconv.Atoi(p.Plan)
	if err != nil {
		return Instance{}, fmt.Errorf("vdsina: invalid server plan id %q", p.Plan)
	}
	tmplID := p.TemplateID
	if tmplID == 0 {
		tmpl, err := c.UbuntuLTS(ctx)
		if err != nil {
			return Instance{}, err
		}
		tmplID = tmpl.ID
	}
	name := p.Hostname
	if name == "" {
		name = p.Label
	}
	body := map[string]any{
		"datacenter":  dcID,
		"server-plan": planID,
		"template":    tmplID,
		"name":        name,
		"autoprolong": true,
	}
	if p.SSHKeyID != "" {
		if id, err := strconv.Atoi(p.SSHKeyID); err == nil {
			body["ssh_key"] = id
		}
	}
	var out wireServer
	if err := c.call(ctx, http.MethodPost, "/server", body, &out); err != nil {
		return Instance{}, err
	}
	inst := out.toInstance()
	if inst.Region == "" {
		inst.Region = p.Region
	}
	if inst.Plan == "" {
		inst.Plan = p.Plan
	}
	return inst, nil
}

// CreateInstance is an alias for Create.
func (c *Client) CreateInstance(ctx context.Context, p CreateParams) (Instance, error) {
	return c.Create(ctx, p)
}

// Instance reads one server (GET /v1/server/{id}).
func (c *Client) Instance(ctx context.Context, id string) (Instance, error) {
	var out wireServer
	if err := c.call(ctx, http.MethodGet, "/server/"+url.PathEscape(id), nil, &out); err != nil {
		return Instance{}, err
	}
	return out.toInstance(), nil
}

// GetInstance is an alias for Instance.
func (c *Client) GetInstance(ctx context.Context, id string) (Instance, error) {
	return c.Instance(ctx, id)
}

// ListInstances lists all servers on the account (GET /v1/server).
func (c *Client) ListInstances(ctx context.Context, tag string) ([]Instance, error) {
	var out []wireServer
	if err := c.call(ctx, http.MethodGet, "/server", nil, &out); err != nil {
		return nil, err
	}
	list := make([]Instance, 0, len(out))
	for _, s := range out {
		inst := s.toInstance()
		list = append(list, inst)
	}
	return list, nil
}

// Instances lists every server on the account (GET /v1/server).
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	return c.ListInstances(ctx, "")
}

// Reboot restarts a server (PUT /v1/server.reboot/{id}).
func (c *Client) Reboot(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPut, "/server.reboot/"+url.PathEscape(id), nil, nil)
}

// RebootInstance is an alias for Reboot.
func (c *Client) RebootInstance(ctx context.Context, id string) error {
	return c.Reboot(ctx, id)
}

// Delete destroys a server (DELETE /v1/server/{id}).
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/server/"+url.PathEscape(id), nil, nil)
}

// DeleteInstance is an alias for Delete.
func (c *Client) DeleteInstance(ctx context.Context, id string) error {
	return c.Delete(ctx, id)
}
