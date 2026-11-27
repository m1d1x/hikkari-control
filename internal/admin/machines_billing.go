package admin

// machines_billing.go is the machines screen's bookkeeping: the hosters the
// fleet is bought from, how to pay them, and what each machine's tariff says.
//
// entry_boxes.provider has always been what the hub's API keys go by; nothing
// knew where the invoice lives, what the ASN is, or which console login pays
// for it. 093 adds entry_provider_accounts for exactly that, sealed secrets
// included, and entry_boxes.traffic_quota_gb for the tariff's ceiling.
//
// Two rules hold here, as on the rest of the admin plane:
//   - a secret is never part of a list: the screen learns has* flags, and the
//     one request that opens a value is audited with what was opened;
//   - a machine's price is a figure the owner types, no money moves. Its
//     monthly_cents is entry.SyncBoxPrices'/`hub entry-box price`'s, computed
//     through internal/fx; this screen writes price_amount/price_currency and
//     leaves the converted figure to them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/audit"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/fx"
)

// providerAccount is one hoster as the screen shows it: the owner's record
// plus what the fleet actually has there. Secrets are flags only.
type providerAccount struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"displayName"`
	ASN         string `json:"asn"`
	BillingURL  string `json:"billingUrl"`
	APIHost     string `json:"apiHost"`
	// SpendCapCents is the owner's own ceiling for this hoster; a note, not
	// a gate (entry_budgets is what stops spending).
	SpendCapCents *int   `json:"spendCapCents"`
	Note          string `json:"note"`

	HasLogin    bool `json:"hasLogin"`
	HasPassword bool `json:"hasPassword"`
	HasAPIKey   bool `json:"hasApiKey"`

	// Machines and MonthlyCents are the live machines at this hoster and
	// what their own-currency prices add up to. Currency is empty when the
	// machines are billed in more than one (or none is priced).
	Machines     int    `json:"machines"`
	MonthlyCents int    `json:"monthlyCents"`
	Currency     string `json:"currency"`
}

// providerAccountsQ is the union of the owner's rows and the hosters the fleet
// actually has machines at, so a machine without a row is still on the screen
// and a row without machines can be filled in before the first purchase.
// Parameter: a provider to narrow to, or NULL for all.
const providerAccountsQ = `
	SELECT
		coalesce(a.provider, b.provider) AS provider,
		coalesce(a.display_name, ''),
		coalesce(a.asn, ''),
		coalesce(a.billing_url, ''),
		coalesce(a.api_host, ''),
		a.spend_cap_cents,
		coalesce(a.note, ''),
		a.login_ct IS NOT NULL,
		a.password_ct IS NOT NULL,
		a.api_key_ct IS NOT NULL,
		coalesce(b.machines, 0),
		coalesce(b.cents, 0),
		coalesce(b.currency, '')
	FROM entry_provider_accounts a
	FULL OUTER JOIN (
		SELECT provider,
		       count(*) AS machines,
		       round(coalesce(sum(price_amount), 0) * 100)::bigint AS cents,
		       CASE WHEN count(DISTINCT price_currency) = 1
		            THEN min(price_currency) ELSE '' END AS currency
		FROM entry_boxes
		WHERE retired_at IS NULL
		GROUP BY provider
	) b ON b.provider = a.provider
	WHERE ($1::text IS NULL OR coalesce(a.provider, b.provider) = $1)
	ORDER BY 1
`

// providerKindRE is migration 001's entry_providers_kind_check, the shape the
// hub's provider kinds already have (cloud4box, vdsina, hostvds).
var providerKindRE = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// EntryProviderAccounts answers GET /api/v1/admin/entry/provider-accounts.
func (h *Handlers) EntryProviderAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.providerAccounts(r.Context(), nil)
	if err != nil {
		log.Printf("admin provider accounts: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]any{"accounts": accounts})
}

func (h *Handlers) providerAccounts(ctx context.Context, only *string) ([]providerAccount, error) {
	rows, err := h.pool.Query(ctx, providerAccountsQ, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []providerAccount{}
	for rows.Next() {
		var a providerAccount
		if err := rows.Scan(
			&a.Provider, &a.DisplayName, &a.ASN, &a.BillingURL, &a.APIHost,
			&a.SpendCapCents, &a.Note,
			&a.HasLogin, &a.HasPassword, &a.HasAPIKey,
			&a.Machines, &a.MonthlyCents, &a.Currency,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// providerAccountOne reads one row of the same shape, or nil when the hoster is
// neither in the table nor has machines.
func (h *Handlers) providerAccountOne(ctx context.Context, provider string) (*providerAccount, error) {
	list, err := h.providerAccounts(ctx, &provider)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// EntryProviderAccountSave answers PATCH
// /api/v1/admin/entry/provider-accounts/{kind}: upserts the owner's record of
// a hoster. Nothing here is a secret except through the /secret route, and
// empty strings are a legitimate value (a hoster whose ASN the owner has not
// looked up yet).
func (h *Handlers) EntryProviderAccountSave(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	provider := strings.TrimSpace(r.PathValue("kind"))
	if !providerKindRE.MatchString(provider) {
		writeError(w, http.StatusBadRequest, "provider kind is lower-case letters and digits")
		return
	}
	var body struct {
		DisplayName   string `json:"displayName"`
		ASN           string `json:"asn"`
		BillingURL    string `json:"billingUrl"`
		APIHost       string `json:"apiHost"`
		SpendCapCents *int   `json:"spendCapCents"`
		Note          string `json:"note"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body is not a provider account")
		return
	}
	if body.SpendCapCents != nil && *body.SpendCapCents < 0 {
		writeError(w, http.StatusBadRequest, "spendCapCents must be zero or more")
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO entry_provider_accounts
			(provider, display_name, asn, billing_url, api_host, spend_cap_cents, note, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (provider) DO UPDATE SET
			display_name    = excluded.display_name,
			asn             = excluded.asn,
			billing_url     = excluded.billing_url,
			api_host        = excluded.api_host,
			spend_cap_cents = excluded.spend_cap_cents,
			note            = excluded.note,
			updated_at      = now()
	`, provider,
		strings.TrimSpace(body.DisplayName),
		strings.TrimSpace(body.ASN),
		strings.TrimSpace(body.BillingURL),
		strings.TrimSpace(body.APIHost),
		body.SpendCapCents,
		strings.TrimSpace(body.Note),
	); err != nil {
		log.Printf("admin provider account save: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	account, err := h.providerAccountOne(r.Context(), provider)
	if err != nil {
		log.Printf("admin provider account save: read back: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if account == nil {
		log.Printf("admin provider account save: %s vanished after upsert", provider)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	audit.Log(r.Context(), h.pool, r, audit.Entry{
		AdminID: adminID(actor), Action: "entry.provider_account.save",
		TargetType: "entry_provider_account", TargetID: provider,
		Payload: map[string]any{
			"display_name": account.DisplayName, "asn": account.ASN,
			"billing_url": account.BillingURL, "api_host": account.APIHost,
			"spend_cap_cents": account.SpendCapCents, "note": account.Note,
		},
	})
	writeJSONResponse(w, map[string]any{"account": account})
}

// EntryProviderAccountSecret answers PUT
// /api/v1/admin/entry/provider-accounts/{kind}/secret: writes or clears the
// hoster's console login, password and API key. A field that is absent is left
// alone; an empty string clears it (NULL), because "no password on record" is
// a fact the owner must be able to state. The values go in sealed and never
// come back out of this handler.
func (h *Handlers) EntryProviderAccountSecret(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	provider := strings.TrimSpace(r.PathValue("kind"))
	if !providerKindRE.MatchString(provider) {
		writeError(w, http.StatusBadRequest, "provider kind is lower-case letters and digits")
		return
	}
	if !cryptobox.IsConfigured() {
		log.Printf("admin provider account secret: cryptobox is not configured")
		writeError(w, http.StatusServiceUnavailable, "secrets are not configured on this hub")
		return
	}
	var body struct {
		Login    *string `json:"login"`
		Password *string `json:"password"`
		APIKey   *string `json:"apiKey"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body is {login?, password?, apiKey?}")
		return
	}
	if body.Login == nil && body.Password == nil && body.APIKey == nil {
		writeError(w, http.StatusBadRequest, "body names no secret to write")
		return
	}

	// The row may not exist yet: writing a login before filling the rest in is
	// a legitimate order of work, and the table's defaults are the empty
	// account.
	if _, err := h.pool.Exec(r.Context(),
		`INSERT INTO entry_provider_accounts (provider) VALUES ($1) ON CONFLICT (provider) DO NOTHING`,
		provider); err != nil {
		log.Printf("admin provider account secret: ensure row: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	sets := make([]string, 0, 3)
	args := []any{provider}
	written := make([]string, 0, 3)
	cleared := make([]string, 0, 3)
	write := func(field, column string, v *string) error {
		if v == nil {
			return nil
		}
		var arg any
		if *v == "" {
			cleared = append(cleared, field)
		} else {
			sealed, err := cryptobox.EncryptString(*v)
			if err != nil {
				return fmt.Errorf("seal %s: %w", field, err)
			}
			arg = sealed
			written = append(written, field)
		}
		args = append(args, arg)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
		return nil
	}
	if err := write("login", "login_ct", body.Login); err != nil {
		log.Printf("admin provider account secret: %v", err)
		http.Error(w, "seal failed", http.StatusInternalServerError)
		return
	}
	if err := write("password", "password_ct", body.Password); err != nil {
		log.Printf("admin provider account secret: %v", err)
		http.Error(w, "seal failed", http.StatusInternalServerError)
		return
	}
	if err := write("apiKey", "api_key_ct", body.APIKey); err != nil {
		log.Printf("admin provider account secret: %v", err)
		http.Error(w, "seal failed", http.StatusInternalServerError)
		return
	}
	if _, err := h.pool.Exec(r.Context(),
		`UPDATE entry_provider_accounts SET `+strings.Join(sets, ", ")+`, updated_at = now() WHERE provider = $1`,
		args...); err != nil {
		log.Printf("admin provider account secret: update: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	account, err := h.providerAccountOne(r.Context(), provider)
	if err != nil || account == nil {
		log.Printf("admin provider account secret: read flags: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	// The journal says which secret was touched and on which hoster, never
	// what it now holds.
	audit.Log(r.Context(), h.pool, r, audit.Entry{
		AdminID: adminID(actor), Action: "entry.provider_account.secret",
		TargetType: "entry_provider_account", TargetID: provider,
		Payload: map[string]any{"written": written, "cleared": cleared},
	})
	writeJSONResponse(w, map[string]any{
		"hasLogin": account.HasLogin, "hasPassword": account.HasPassword, "hasApiKey": account.HasAPIKey,
	})
}

// EntryProviderAccountReveal answers GET
// /api/v1/admin/entry/provider-accounts/{kind}/secret/{field}: one sealed
// value, opened. Owner-only and always audited — an audit row that says which
// hoster's which secret was read, with no room for the value itself.
func (h *Handlers) EntryProviderAccountReveal(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	provider := strings.TrimSpace(r.PathValue("kind"))
	field := r.PathValue("field")
	column, ok := providerSecretColumns[field]
	if !ok {
		writeError(w, http.StatusBadRequest, "field is login, password or apiKey")
		return
	}
	var sealed []byte
	err := h.pool.QueryRow(r.Context(),
		`SELECT `+column+` FROM entry_provider_accounts WHERE provider = $1`, provider).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such provider account")
		return
	}
	if err != nil {
		log.Printf("admin provider account secret reveal: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if len(sealed) == 0 {
		writeError(w, http.StatusNotFound, "secret is not set")
		return
	}
	plain, err := cryptobox.DecryptString(sealed)
	if err != nil {
		// A seal this build's key cannot open is not a 404: the row exists and
		// the owner must know the key is wrong, not that nothing was saved.
		log.Printf("admin provider account secret reveal: %s/%s: %v", provider, field, err)
		http.Error(w, "secret cannot be opened with this key", http.StatusInternalServerError)
		return
	}
	audit.Log(r.Context(), h.pool, r, audit.Entry{
		AdminID: adminID(actor), Action: "entry.provider_account.secret.reveal",
		TargetType: "entry_provider_account", TargetID: provider,
		Payload: map[string]any{"field": field},
	})
	writeJSONResponse(w, map[string]any{"value": plain})
}

// providerSecretColumns maps the API's field names to the sealed columns. The
// map, not the request, is what reaches the SQL.
var providerSecretColumns = map[string]string{
	"login":    "login_ct",
	"password": "password_ct",
	"apiKey":   "api_key_ct",
}

// machineBilling is one machine's tariff as the screen edits it.
type machineBilling struct {
	Name           string   `json:"name"`
	PriceAmount    *float64 `json:"priceAmount"`
	PriceCurrency  string   `json:"priceCurrency,omitempty"`
	MonthlyCents   *int     `json:"monthlyCents"`
	TrafficQuotaGB *int     `json:"trafficQuotaGb"`
	RentedUntil    *string  `json:"rentedUntil"`
	Plan           string   `json:"plan"`
}

// EntryMachineBilling answers PATCH
// /api/v1/admin/entry/machines/{name}/billing. Body: {priceAmount?,
// priceCurrency?, trafficQuotaGb?, rentedUntil?, plan?}; an absent field keeps
// its value, null clears the nullable ones.
//
// priceAmount 0 clears the pair: the column refuses a zero price
// (064: price_amount > 0), and "free" is not a thing the hub can bill, while
// "not known" is exactly what NULL means. A price needs its currency — taken
// from the body, else from the machine's existing one.
//
// monthly_cents is deliberately untouched: converting a rouble or euro price
// to US cents is internal/fx's (entry.SyncBoxPrices, `hub entry-box price`), and
// a screen that guessed a rate would write a wrong figure into the economics.
func (h *Handlers) EntryMachineBilling(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetAdmin(r)
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "machine is not named")
		return
	}
	var body map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body is not a billing update")
		return
	}
	for k := range body {
		if _, ok := billingFields[k]; !ok {
			writeError(w, http.StatusBadRequest, "unknown field "+k)
			return
		}
	}

	ctx := r.Context()
	var (
		amount   *float64
		currency string
		quota    *int
		rented   *time.Time
		plan     string
		monthly  *int
	)
	err := h.pool.QueryRow(ctx, `
		SELECT price_amount::float8, coalesce(price_currency, ''), traffic_quota_gb,
		       rented_until, coalesce(plan, ''), monthly_cents
		FROM entry_boxes WHERE name = $1
	`, name).Scan(&amount, &currency, &quota, &rented, &plan, &monthly)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such machine")
		return
	}
	if err != nil {
		log.Printf("admin machine billing: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if raw, ok := body["priceAmount"]; ok {
		if isJSONNull(raw) {
			amount, currency = nil, ""
		} else {
			var v float64
			if err := json.Unmarshal(raw, &v); err != nil {
				writeError(w, http.StatusBadRequest, "priceAmount is a number")
				return
			}
			if v < 0 {
				writeError(w, http.StatusBadRequest, "priceAmount must be zero or more")
				return
			}
			if v == 0 {
				amount, currency = nil, ""
			} else {
				amount = &v
			}
		}
	}
	if raw, ok := body["priceCurrency"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || strings.TrimSpace(s) == "" {
			writeError(w, http.StatusBadRequest, "priceCurrency is USD, EUR or RUB")
			return
		}
		cur, err := fx.Normalize(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "priceCurrency is USD, EUR or RUB")
			return
		}
		if amount == nil {
			writeError(w, http.StatusBadRequest, "priceCurrency without priceAmount")
			return
		}
		currency = cur
	}
	if amount != nil && currency == "" {
		writeError(w, http.StatusBadRequest, "priceCurrency is required with a price")
		return
	}
	if raw, ok := body["trafficQuotaGb"]; ok {
		if isJSONNull(raw) {
			quota = nil
		} else {
			var v int
			if err := json.Unmarshal(raw, &v); err != nil {
				writeError(w, http.StatusBadRequest, "trafficQuotaGb is a whole number of gigabytes")
				return
			}
			if v < 0 {
				writeError(w, http.StatusBadRequest, "trafficQuotaGb must be zero or more")
				return
			}
			quota = &v
		}
	}
	if raw, ok := body["rentedUntil"]; ok {
		if isJSONNull(raw) {
			rented = nil
		} else {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				writeError(w, http.StatusBadRequest, "rentedUntil is YYYY-MM-DD")
				return
			}
			day, err := time.Parse("2006-01-02", s)
			if err != nil {
				writeError(w, http.StatusBadRequest, "rentedUntil is YYYY-MM-DD")
				return
			}
			rented = &day
		}
	}
	if raw, ok := body["plan"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			writeError(w, http.StatusBadRequest, "plan is a string")
			return
		}
		plan = strings.TrimSpace(s)
	}

	var priceArg any
	if amount != nil {
		priceArg = *amount
	}
	var currencyArg any
	if amount != nil {
		currencyArg = currency
	}
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_boxes
		SET price_amount = $2, price_currency = $3, traffic_quota_gb = $4,
		    rented_until = $5, plan = $6
		WHERE name = $1
	`, name, priceArg, currencyArg, quota, rented, plan); err != nil {
		log.Printf("admin machine billing: update: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	changed := make([]string, 0, len(body))
	for k := range body {
		changed = append(changed, k)
	}
	audit.Log(ctx, h.pool, r, audit.Entry{
		AdminID: adminID(actor), Action: "entry.machine.billing",
		TargetType: "entry_box", TargetID: name,
		Payload: map[string]any{
			"changed": changed, "price_amount": amount, "price_currency": currency,
			"traffic_quota_gb": quota, "plan": plan,
		},
	})

	out := machineBilling{Name: name, PriceAmount: amount, MonthlyCents: monthly,
		TrafficQuotaGB: quota, Plan: plan}
	if amount != nil {
		out.PriceCurrency = currency
	}
	if rented != nil {
		s := rented.Format("2006-01-02")
		out.RentedUntil = &s
	}
	writeJSONResponse(w, map[string]any{"machine": out})
}

// billingFields is what PATCH .../billing accepts; anything else is refused
// rather than ignored, so a typo cannot look like a saved price.
var billingFields = map[string]struct{}{
	"priceAmount": {}, "priceCurrency": {}, "trafficQuotaGb": {},
	"rentedUntil": {}, "plan": {},
}

// isJSONNull tells an explicit `null` (clear the field) from an absent key
// (keep it) — the difference the nullable columns depend on.
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// adminID is the audit row's actor, empty when the gate has no admin (the
// hub's own warmer never reaches a mutating route, but the helper keeps the
// call sites honest).
func adminID(a *auth.AdminInfo) string {
	if a == nil {
		return ""
	}
	return a.ID
}
