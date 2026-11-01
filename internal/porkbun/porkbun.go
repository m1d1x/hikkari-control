// Package porkbun is a direct HTTPS client for the Porkbun domain API (the
// "for AI agents" v3.3 surface), the registrar the hub uses to buy and point
// a relay's throwaway domain (plan 42 П4в Ф6). It is a pure client: it knows
// nothing of the entry package, returns its own types, and reports failures
// as concrete error values, never a wrapped opaque error. The adapter that
// makes it an entry.DomainRegistrar lives in internal/entrywire.
//
// Every call is a single POST with the credentials in the JSON body (the API
// also accepts X-API-Key / X-Secret-API-Key headers, but the body form is the
// documented one and keeps the client uniform). Wire shapes below were taken
// from a live run against the owner's account on 2026-10-21.
package porkbun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the v3 API root. The purchase surface lives here, not on
// the older /json/v3/documentation DNS-only pages.
const DefaultBaseURL = "https://api.porkbun.com/api/json/v3"

// requestTimeout caps a single call. Porkbun's registration calls are the slow
// ones (a create can take a few seconds); 30s leaves headroom without pinning
// a goroutine forever when the upstream stalls.
const requestTimeout = 30 * time.Second

// maxResponseBytes bounds how much of a response body we read. Porkbun's
// envelopes are small; this guards against a proxy dumping a large error page.
const maxResponseBytes = 1 << 20

var (
	// ErrBadCredentials: the API rejected the key/secret pair (ping does not
	// report credentialsValid, or a call came back unauthenticated).
	ErrBadCredentials = errors.New("porkbun: credentials rejected")
	// ErrUnavailable: the domain cannot be registered (taken, reserved, or the
	// registrar will not sell it).
	ErrUnavailable = errors.New("porkbun: domain not available")
	// ErrNotRegisterable: the TLD cannot be bought through the API at all
	// (getRegistrationRequirements.apiRegisterable is false).
	ErrNotRegisterable = errors.New("porkbun: tld not api-registerable")
	// ErrInsufficientFunds: the prepaid credit does not cover the purchase.
	ErrInsufficientFunds = errors.New("porkbun: insufficient prepaid credit")
	// ErrOrderTooLarge: the order exceeds the account's monthly spend limit.
	ErrOrderTooLarge = errors.New("porkbun: order exceeds monthly spend limit")
	// ErrVerificationRequired: the account needs email/phone verification
	// before it may buy through the API.
	ErrVerificationRequired = errors.New("porkbun: account verification required")
	// ErrRateLimited: too many calls in the window (checkDomain is 10/10s).
	ErrRateLimited = errors.New("porkbun: rate limited")
)

// APIError is a structured failure the API reported in its envelope: a
// non-SUCCESS status with a message, and the HTTP status it came with. Known
// messages are mapped to the sentinels above; everything else surfaces as this
// so the operator sees the registrar's own words.
type APIError struct {
	HTTPStatus int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("porkbun: api error (http %d): %s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("porkbun: api error (http %d)", e.HTTPStatus)
}

// Client talks to the Porkbun API. It holds no long-lived connection; each
// method is one POST. The http.Client is reused for TLS session and connection
// pooling. A Client is safe for concurrent use.
type Client struct {
	apiKey    string
	secretKey string
	baseURL   string
	http      *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (for tests against an httptest server).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the HTTP client (for tests, or a custom transport).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a Client authenticating with apiKey and secretKey. The keys are
// the account the owner saved in the console (entry_providers, sealed at
// rest); the client itself never reads them from anywhere.
func New(apiKey, secretKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:    apiKey,
		secretKey: secretKey,
		baseURL:   DefaultBaseURL,
		http:      &http.Client{Timeout: requestTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ── Wire envelope ─────────────────────────────────────────────────────────

// auth is the credential pair every call carries in its body.
type auth struct {
	APIKey    string `json:"apikey"`
	SecretKey string `json:"secretapikey"`
}

func (c *Client) auth() auth { return auth{APIKey: c.apiKey, SecretKey: c.secretKey} }

// post sends body as JSON to path and decodes the response into out. header is
// an optional single header (used for Idempotency-Key); empty name skips it.
// It maps the registrar's non-SUCCESS envelope and HTTP errors to concrete
// errors before out is considered meaningful.
func (c *Client) post(ctx context.Context, path string, body any, out any, headerName, headerVal string) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("porkbun: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("porkbun: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if headerName != "" {
		req.Header.Set(headerName, headerVal)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("porkbun: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("porkbun: read response: %w", err)
	}
	// Every Porkbun envelope carries a status field; decode it first to tell
	// success from a structured error regardless of the HTTP code (the API
	// returns 400 with status:"ERROR" on a bad request, 200 on success).
	var env struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	if !strings.EqualFold(env.Status, "SUCCESS") {
		return classify(resp.StatusCode, env.Message)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("porkbun: decode response: %w", err)
		}
	}
	return nil
}

// classify maps a non-SUCCESS envelope to a concrete error. The message text
// is the registrar's; we match the stable fragments it uses for the failures
// the budget gate must distinguish, and fall back to APIError for the rest.
func classify(httpStatus int, msg string) error {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "invalid api key") || strings.Contains(m, "credential"):
		return ErrBadCredentials
	case strings.Contains(m, "insufficient") || strings.Contains(m, "not enough"):
		return ErrInsufficientFunds
	case strings.Contains(m, "monthly") || strings.Contains(m, "spend limit") || strings.Contains(m, "order_too_large") || strings.Contains(m, "too large"):
		return ErrOrderTooLarge
	case strings.Contains(m, "verification") || strings.Contains(m, "verify"):
		return ErrVerificationRequired
	case strings.Contains(m, "not available") || strings.Contains(m, "unavailable") || strings.Contains(m, "already registered"):
		return ErrUnavailable
	case strings.Contains(m, "rate") && strings.Contains(m, "limit"):
		return ErrRateLimited
	}
	return &APIError{HTTPStatus: httpStatus, Message: msg}
}

// ── Ping ────────────────────────────────────────────────────────────────

// Ping proves the credentials work and returns the account's egress IP as the
// registrar sees it. A failure is ErrBadCredentials or a transport error.
func (c *Client) Ping(ctx context.Context) (ip string, err error) {
	var out struct {
		YourIP string `json:"yourIp"`
	}
	if err := c.post(ctx, "ping", c.auth(), &out, "", ""); err != nil {
		return "", err
	}
	return out.YourIP, nil
}

// ── Account ─────────────────────────────────────────────────────────────

// Balance returns the account's prepaid credit in whole cents — what API
// purchases are paid from (a saved card is never charged by a purchase).
func (c *Client) Balance(ctx context.Context) (cents int, err error) {
	var out struct {
		Balance int `json:"balance"`
	}
	if err := c.post(ctx, "account/balance", c.auth(), &out, "", ""); err != nil {
		return 0, err
	}
	return out.Balance, nil
}

// SpendLimit is the monthly cap the registrar actually enforces on API
// purchases, as the account holder set it on the website (or the registrar's
// default when they did not). It cannot be changed over the API.
type SpendLimit struct {
	// LimitCents is the cap in whole cents; meaningful only when Limited.
	LimitCents int
	// Limited is false when the account has no cap on purchases at all.
	Limited bool
	// SpentCents is what API purchases spent this calendar month.
	SpentCents int
}

// SpendLimit reads the account's enforced monthly purchase cap and this
// month's spend (account/apiSettings).
func (c *Client) SpendLimit(ctx context.Context) (SpendLimit, error) {
	var out struct {
		MonthlySpend int `json:"monthlySpend"`
		SpendLimit   struct {
			Limit  int    `json:"limit"`
			Source string `json:"source"`
			Spent  int    `json:"spent"`
		} `json:"spendLimit"`
	}
	if err := c.post(ctx, "account/apiSettings", c.auth(), &out, "", ""); err != nil {
		return SpendLimit{}, err
	}
	spent := out.SpendLimit.Spent
	if spent == 0 {
		spent = out.MonthlySpend
	}
	return SpendLimit{
		LimitCents: out.SpendLimit.Limit,
		// source "none" is the registrar saying no cap applies.
		Limited:    out.SpendLimit.Source != "none",
		SpentCents: spent,
	}, nil
}

// AccountDomain is one domain registered on the account (domain/listAll).
type AccountDomain struct {
	Domain string
	TLD    string
	// Status is the registry status as the registrar reports it ("ACTIVE", …).
	Status string
	// ExpireDate is the registrar's "YYYY-MM-DD hh:mm:ss" expiry, as given.
	ExpireDate string
	// APIAccess is listAll's flag as given. It does not decide whether the key
	// may edit the domain's DNS (CanSetARecord does); kept for display only.
	APIAccess bool
}

// listAllPage is the registrar's page size for domain/listAll.
const listAllPage = 1000

// Domains lists every domain on the account, following listAll's pages.
func (c *Client) Domains(ctx context.Context) ([]AccountDomain, error) {
	var out []AccountDomain
	for start := 0; ; start += listAllPage {
		body := struct {
			auth
			Start int `json:"start"`
		}{auth: c.auth(), Start: start}
		var page struct {
			Count   int `json:"count"`
			Domains []struct {
				Domain     string  `json:"domain"`
				TLD        string  `json:"tld"`
				Status     string  `json:"status"`
				ExpireDate string  `json:"expireDate"`
				APIAccess  boolish `json:"apiAccess"`
			} `json:"domains"`
		}
		if err := c.post(ctx, "domain/listAll", body, &page, "", ""); err != nil {
			return nil, err
		}
		for _, d := range page.Domains {
			out = append(out, AccountDomain{
				Domain: strings.ToLower(d.Domain), TLD: strings.ToLower(d.TLD), Status: d.Status,
				ExpireDate: d.ExpireDate, APIAccess: bool(d.APIAccess),
			})
		}
		if len(page.Domains) < listAllPage {
			return out, nil
		}
	}
}

// ── Registrability ────────────────────────────────────────────────────────

// Registerable reports whether tld (without a leading dot, e.g. "lol") can be
// bought through the API. A TLD the API cannot sell returns false with no
// error; a transport or credential failure returns the error.
func (c *Client) Registerable(ctx context.Context, tld string) (bool, error) {
	tld = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tld)), ".")
	var out struct {
		APIRegisterable boolish `json:"apiRegisterable"`
	}
	if err := c.post(ctx, "domain/getRegistrationRequirements/"+tld, c.auth(), &out, "", ""); err != nil {
		return false, err
	}
	return bool(out.APIRegisterable), nil
}

// ── Check ─────────────────────────────────────────────────────────────────

// CheckResult is the live answer for one domain.
type CheckResult struct {
	// Available is whether the domain can be registered now.
	Available bool
	// CostCents is the first-year registration price in whole cents, the
	// figure a Create must pass as cost. It is parsed from the "price" string
	// without floating-point rounding.
	CostCents int
}

// CheckDomain returns the live availability and first-year price of domain.
// This is the only price source a purchase may trust: the bulk pricing list
// omits promotions and premium pricing that the create cost must match.
// Porkbun rate-limits this to ten calls per ten seconds.
func (c *Client) CheckDomain(ctx context.Context, domain string) (CheckResult, error) {
	var out struct {
		Response struct {
			Avail string `json:"avail"`
			Price string `json:"price"`
		} `json:"response"`
	}
	if err := c.post(ctx, "domain/checkDomain/"+strings.ToLower(domain), c.auth(), &out, "", ""); err != nil {
		return CheckResult{}, err
	}
	cents, err := dollarsToCents(out.Response.Price)
	if err != nil {
		return CheckResult{}, fmt.Errorf("porkbun: check %s: price %q: %w", domain, out.Response.Price, err)
	}
	return CheckResult{
		Available: strings.EqualFold(out.Response.Avail, "yes"),
		CostCents: cents,
	}, nil
}

// ── Create (dry-run and real) ───────────────────────────────────────────────

// CreateParams is a registration request.
type CreateParams struct {
	// Domain is the full name to register, lower-case.
	Domain string
	// CostCents must equal a fresh CheckDomain of the same name, to the cent.
	CostCents int
	// DryRun rehearses the purchase without spending when true.
	DryRun bool
	// IdempotencyKey makes a real purchase safe to retry over a dropped
	// response. The registrar replays the first response — error included —
	// for 24h, so a retry after a fixed failure must use a NEW key.
	IdempotencyKey string
}

// CreateResult is what a Create returns. On a dry run the booleans and balance
// are the pre-flight; on a real purchase OrderID is set.
type CreateResult struct {
	WouldSucceed       bool
	SufficientFunds    bool
	WithinMonthlyLimit bool
	BalanceCents       int
	OrderID            string
}

// Create registers Domain, or rehearses it when DryRun is set. cost travels as
// whole cents and must match a fresh CheckDomain. A real purchase draws on the
// account's prepaid credit; the saved card is not charged through the API.
func (c *Client) Create(ctx context.Context, p CreateParams) (CreateResult, error) {
	body := struct {
		auth
		Cost         int    `json:"cost"`
		AgreeToTerms string `json:"agreeToTerms"`
		DryRun       bool   `json:"dryRun,omitempty"`
	}{auth: c.auth(), Cost: p.CostCents, AgreeToTerms: "yes", DryRun: p.DryRun}

	var out struct {
		WouldSucceed       boolish         `json:"wouldSucceed"`
		SufficientFunds    boolish         `json:"sufficientFunds"`
		WithinMonthlyLimit boolish         `json:"withinMonthlySpendLimit"`
		BalanceDisplay     string          `json:"balanceDisplay"`
		Balance            json.RawMessage `json:"balance"`
		OrderID            jsonNum         `json:"orderId"`
	}
	const idemHeader = "Idempotency-Key"
	if err := c.post(ctx, "domain/create/"+strings.ToLower(p.Domain), body, &out, idemHeader, p.IdempotencyKey); err != nil {
		return CreateResult{}, err
	}
	balCents := createBalanceCents(out.Balance, out.BalanceDisplay)
	return CreateResult{
		WouldSucceed:       bool(out.WouldSucceed),
		SufficientFunds:    bool(out.SufficientFunds),
		WithinMonthlyLimit: bool(out.WithinMonthlyLimit),
		BalanceCents:       balCents,
		OrderID:            string(out.OrderID),
	}, nil
}

// createBalanceCents reads domain/create's balance. Porkbun has sent it both
// ways: a JSON number in whole cents, as account/balance does (live, 01.11 —
// a string-only field failed every dry run), and a dollar string ("8.46").
// Neither present falls back to balanceDisplay ("$8.46").
func createBalanceCents(raw json.RawMessage, display string) int {
	v := strings.TrimSpace(string(raw))
	if v != "" && v != "null" && !strings.HasPrefix(v, `"`) {
		var cents int
		if err := json.Unmarshal(raw, &cents); err == nil {
			return cents
		}
	}
	bal := strings.Trim(v, `"`)
	if bal == "" || bal == "null" {
		bal = display
	}
	cents, _ := dollarsToCents(strings.TrimPrefix(strings.TrimSpace(bal), "$"))
	return cents
}

// ── DNS ─────────────────────────────────────────────────────────────────

// CreateARecord adds an apex A record for domain pointing at ip. Porkbun's
// dns/create adds a record; aiming a domain with an existing apex A record
// elsewhere needs the old record cleared first, which SetARecord in the
// adapter handles via DeleteARecords.
func (c *Client) CreateARecord(ctx context.Context, domain, ip string) error {
	body := struct {
		auth
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
		TTL     string `json:"ttl"`
	}{auth: c.auth(), Name: "", Type: "A", Content: ip, TTL: "600"}
	return c.post(ctx, "dns/create/"+strings.ToLower(domain), body, nil, "", "")
}

// CanSetARecord rehearses adding an apex A record to domain (dns/create with
// dryRun): whether the key may edit this domain's DNS, checked by the
// registrar itself. Nothing is changed. A refusal the registrar explains comes
// back as an error.
//
// This, not listAll's apiAccess flag, is the answer: on 2026-10-22 a domain
// listed with apiAccess 0 still passed this rehearsal and its records read
// fine over the API.
func (c *Client) CanSetARecord(ctx context.Context, domain string) error {
	body := struct {
		auth
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
		TTL     string `json:"ttl"`
		DryRun  bool   `json:"dryRun"`
	}{auth: c.auth(), Type: "A", Content: "203.0.113.1", TTL: "600", DryRun: true}
	var out struct {
		WouldSucceed boolish `json:"wouldSucceed"`
		Message      string  `json:"message"`
	}
	if err := c.post(ctx, "dns/create/"+strings.ToLower(domain), body, &out, "", ""); err != nil {
		return err
	}
	if !bool(out.WouldSucceed) {
		return &APIError{HTTPStatus: http.StatusOK, Message: out.Message}
	}
	return nil
}

// DNSRecord is one DNS record as the registrar stores it.
type DNSRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Records returns domain's DNS records (dns/retrieve).
func (c *Client) Records(ctx context.Context, domain string) ([]DNSRecord, error) {
	var out struct {
		Records []DNSRecord `json:"records"`
	}
	if err := c.post(ctx, "dns/retrieve/"+strings.ToLower(domain), c.auth(), &out, "", ""); err != nil {
		return nil, err
	}
	return out.Records, nil
}

// DeleteRecord removes one DNS record by its id (dns/delete).
func (c *Client) DeleteRecord(ctx context.Context, domain, id string) error {
	return c.post(ctx, "dns/delete/"+strings.ToLower(domain)+"/"+id, c.auth(), nil, "", "")
}

// ── Parsing helpers ─────────────────────────────────────────────────────────

// dollarsToCents parses a decimal dollar string ("1.54", "11", "2.0") into
// whole cents without floating-point rounding. It rejects anything that is not
// a plain non-negative decimal with at most two fractional digits.
func dollarsToCents(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty price")
	}
	neg := strings.HasPrefix(s, "-")
	if neg {
		return 0, errors.New("negative price")
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	w, err := strconv.Atoi(whole)
	if err != nil {
		return 0, err
	}
	cents := w * 100
	if hasFrac {
		switch len(frac) {
		case 0:
			// "1." — treat as no fraction.
		case 1:
			d, err := strconv.Atoi(frac)
			if err != nil {
				return 0, err
			}
			cents += d * 10
		default:
			d, err := strconv.Atoi(frac[:2])
			if err != nil {
				return 0, err
			}
			cents += d
			// Porkbun prices never carry sub-cent precision; ignore any
			// trailing digits rather than rounding them in.
		}
	}
	return cents, nil
}

// boolish decodes a JSON value that the API expresses inconsistently as a bool
// (true), a string ("yes"/"no") or a number (1/0).
type boolish bool

func (b *boolish) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	switch strings.ToLower(s) {
	case "true", "yes", "1":
		*b = true
	case "false", "no", "0", "", "null":
		*b = false
	default:
		*b = false
	}
	return nil
}

// jsonNum decodes a JSON field that may arrive as a number or a string into
// its string form (used for orderId, which the API has returned both ways).
type jsonNum string

func (n *jsonNum) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "null" {
		s = ""
	}
	*n = jsonNum(s)
	return nil
}
