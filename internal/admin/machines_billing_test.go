package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// billingReq builds a request the handler reads PathValue from — the mux
// normally fills those in, and these tests call the handler directly.
func billingReq(method, target, body string, pathValues map[string]string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	return req
}

// accountOf finds one hoster in the list response.
func accountOf(t *testing.T, accounts []providerAccount, provider string) providerAccount {
	t.Helper()
	for _, a := range accounts {
		if a.Provider == provider {
			return a
		}
	}
	t.Fatalf("no account %q in %+v", provider, accounts)
	return providerAccount{}
}

// seedPricedBox writes a live machine with its own-currency price, which is
// what the account list totals.
func seedPricedBox(t *testing.T, pool *pgxpool.Pool, name, provider string, amount float64, currency string) {
	t.Helper()
	seedCompareBox(t, pool, name, provider, 5, nil, 0, "")
	if _, err := pool.Exec(context.Background(), `
		UPDATE entry_boxes SET price_amount = $2, price_currency = $3 WHERE name = $1
	`, name, amount, currency); err != nil {
		t.Fatalf("seed price of %s: %v", name, err)
	}
}

// TestProviderAccountsList is the screen's arithmetic: live machines counted
// per hoster, their own-currency prices summed, and a hoster with machines but
// no account row still on the screen (the union is the point — the owner fills
// the row in from the screen).
func TestProviderAccountsList(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	tag := boxTag("pac")
	withRow := tag + "withrow"
	withMachines := tag + "mach"
	rowOnly := tag + "rowonly"

	seedPricedBox(t, pool, tag+"a1", withRow, 10.50, "USD")
	seedPricedBox(t, pool, tag+"a2", withRow, 4.50, "USD")
	seedPricedBox(t, pool, tag+"b1", withMachines, 2.10, "EUR")

	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_provider_accounts (provider, display_name, asn, billing_url, api_host, note)
		VALUES ($1, 'Aeza', 'AS203273', 'https://my.aeza.net', 'my.aeza.net', 'платим картой')
	`, withRow); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_provider_accounts WHERE provider = ANY($1)`,
			[]string{withRow, withMachines, rowOnly})
	})
	// A row for a hoster the hub has never bought from.
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_provider_accounts (provider, display_name, note) VALUES ($1, 'Xorek', 'на будущее')
	`, rowOnly); err != nil {
		t.Fatalf("seed row-only account: %v", err)
	}

	rec := httptest.NewRecorder()
	h.EntryProviderAccounts(rec, billingReq(http.MethodGet, "/api/v1/admin/entry/provider-accounts", "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Accounts []providerAccount `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	a := accountOf(t, res.Accounts, withRow)
	if a.Machines != 2 {
		t.Errorf("machines = %d, want 2", a.Machines)
	}
	if a.MonthlyCents != 1500 {
		t.Errorf("monthlyCents = %d, want 1500 ($10.50 + $4.50)", a.MonthlyCents)
	}
	if a.Currency != "USD" {
		t.Errorf("currency = %q, want USD", a.Currency)
	}
	if a.DisplayName != "Aeza" || a.ASN != "AS203273" || a.BillingURL != "https://my.aeza.net" || a.APIHost != "my.aeza.net" {
		t.Errorf("record not read back: %+v", a)
	}
	if a.HasLogin || a.HasPassword || a.HasAPIKey {
		t.Errorf("secrets reported before any were written: %+v", a)
	}

	b := accountOf(t, res.Accounts, withMachines)
	if b.Machines != 1 || b.MonthlyCents != 210 || b.Currency != "EUR" {
		t.Errorf("account without a row = %+v, want 1 machine at 210 EUR cents", b)
	}
	if b.DisplayName != "" {
		t.Errorf("displayName = %q for a hoster with no row, want empty", b.DisplayName)
	}

	c := accountOf(t, res.Accounts, rowOnly)
	if c.Machines != 0 || c.MonthlyCents != 0 {
		t.Errorf("row without machines = %+v, want zero machines", c)
	}
	if c.DisplayName != "Xorek" {
		t.Errorf("row-only displayName = %q, want Xorek", c.DisplayName)
	}
}

// TestProviderAccountSave is the record's editor: upsert, empty strings
// allowed, the cap nullable.
func TestProviderAccountSave(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	provider := boxTag("psv") + "host"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_provider_accounts WHERE provider = $1`, provider)
	})

	body := `{"displayName":"U1","asn":"AS24838","billingUrl":"https://u1.host/bill",` +
		`"apiHost":"u1.host","spendCapCents":4200,"note":"оплата раз в месяц"}`
	rec := httptest.NewRecorder()
	h.EntryProviderAccountSave(rec, billingReq(http.MethodPatch, "/x", body, map[string]string{"kind": provider}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Account providerAccount `json:"account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if saved.Account.DisplayName != "U1" || saved.Account.ASN != "AS24838" {
		t.Errorf("saved account = %+v", saved.Account)
	}
	if saved.Account.SpendCapCents == nil || *saved.Account.SpendCapCents != 4200 {
		t.Errorf("spendCapCents = %v, want 4200", saved.Account.SpendCapCents)
	}

	// Empty strings are a value: a hoster whose ASN the owner has not looked
	// up yet, and a cap taken back.
	body = `{"displayName":"","asn":"","billingUrl":"","apiHost":"","spendCapCents":null,"note":""}`
	rec = httptest.NewRecorder()
	h.EntryProviderAccountSave(rec, billingReq(http.MethodPatch, "/x", body, map[string]string{"kind": provider}))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty patch status = %d: %s", rec.Code, rec.Body.String())
	}
	var got providerAccount
	if err := pool.QueryRow(context.Background(), `
		SELECT display_name, asn, billing_url, api_host, spend_cap_cents, note
		FROM entry_provider_accounts WHERE provider = $1
	`, provider).Scan(&got.DisplayName, &got.ASN, &got.BillingURL, &got.APIHost, &got.SpendCapCents, &got.Note); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.DisplayName != "" || got.ASN != "" || got.Note != "" {
		t.Errorf("empty strings did not survive the round trip: %+v", got)
	}
	if got.SpendCapCents != nil {
		t.Errorf("spendCapCents = %v, want NULL after null", *got.SpendCapCents)
	}

	// A kind the hub's provider table could not hold is refused, not stored.
	rec = httptest.NewRecorder()
	h.EntryProviderAccountSave(rec, billingReq(http.MethodPatch, "/x", `{"displayName":"x"}`,
		map[string]string{"kind": "Bad-Kind"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad kind status = %d, want 400", rec.Code)
	}

	// The cap is a figure, not a negative number.
	rec = httptest.NewRecorder()
	h.EntryProviderAccountSave(rec, billingReq(http.MethodPatch, "/x", `{"spendCapCents":-1}`,
		map[string]string{"kind": provider}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("negative cap status = %d, want 400", rec.Code)
	}
}

// TestProviderAccountSecret is the seal: what is written is not the plaintext
// in the column, what is read back is, and the read leaves an audit row that
// names the field without naming the value.
func TestProviderAccountSecret(t *testing.T) {
	cryptobox.ConfigureEphemeralForTests()
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	provider := boxTag("sec") + "host"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_provider_accounts WHERE provider = $1`, provider)
	})

	const login, password, apiKey = "owner@example.test", "s3cr3t-pass-9", "api-key-abc-123"
	body := `{"login":"` + login + `","password":"` + password + `","apiKey":"` + apiKey + `"}`
	rec := httptest.NewRecorder()
	h.EntryProviderAccountSecret(rec, billingReq(http.MethodPut, "/x", body, map[string]string{"kind": provider}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var flags map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &flags); err != nil {
		t.Fatalf("unmarshal flags: %v", err)
	}
	if !flags["hasLogin"] || !flags["hasPassword"] || !flags["hasApiKey"] {
		t.Errorf("flags = %v, want all true", flags)
	}

	var loginCT, passwordCT, apiKeyCT []byte
	if err := pool.QueryRow(context.Background(), `
		SELECT login_ct, password_ct, api_key_ct FROM entry_provider_accounts WHERE provider = $1
	`, provider).Scan(&loginCT, &passwordCT, &apiKeyCT); err != nil {
		t.Fatalf("read sealed: %v", err)
	}
	for field, pair := range map[string]struct {
		ct   []byte
		text string
	}{
		"login": {loginCT, login}, "password": {passwordCT, password}, "apiKey": {apiKeyCT, apiKey},
	} {
		if len(pair.ct) == 0 {
			t.Errorf("%s_ct is empty", field)
			continue
		}
		if string(pair.ct) == pair.text || strings.Contains(string(pair.ct), pair.text) {
			t.Errorf("%s_ct holds the plaintext", field)
		}
	}

	reveal := func(field string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.EntryProviderAccountReveal(rec, billingReq(http.MethodGet, "/x", "",
			map[string]string{"kind": provider, "field": field}))
		return rec
	}
	rec = reveal("login")
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal status = %d: %s", rec.Code, rec.Body.String())
	}
	var opened struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil {
		t.Fatalf("unmarshal value: %v", err)
	}
	if opened.Value != login {
		t.Errorf("revealed login = %q, want the original", opened.Value)
	}

	// The journal knows which field was opened on which hoster — and not what
	// it held.
	var audited, leaked int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			count(*) FILTER (WHERE action = 'entry.provider_account.secret.reveal'
			                   AND payload->>'field' = 'login'),
			count(*) FILTER (WHERE payload::text LIKE '%' || $2 || '%')
		FROM admin_audit_log
		WHERE target_type = 'entry_provider_account' AND target_id = $1
	`, provider, login).Scan(&audited, &leaked); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if audited != 1 {
		t.Errorf("reveal audit rows = %d, want 1", audited)
	}
	if leaked != 0 {
		t.Errorf("%d audit rows carry the secret itself", leaked)
	}

	// An empty string clears the field; a cleared one is a 404.
	rec = httptest.NewRecorder()
	h.EntryProviderAccountSecret(rec, billingReq(http.MethodPut, "/x", `{"password":""}`,
		map[string]string{"kind": provider}))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d: %s", rec.Code, rec.Body.String())
	}
	flags = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &flags)
	if flags["hasPassword"] {
		t.Error("hasPassword is true after clearing it")
	}
	if rec := reveal("password"); rec.Code != http.StatusNotFound {
		t.Errorf("reveal of a cleared secret = %d, want 404", rec.Code)
	}
	if rec := reveal("nonsense"); rec.Code != http.StatusBadRequest {
		t.Errorf("reveal of an unknown field = %d, want 400", rec.Code)
	}
}

// TestMachineBilling is the tariff editor: price and currency, the plan's
// traffic ceiling, the paid-until date — and the fleet screen showing them.
func TestMachineBilling(t *testing.T) {
	pool := livePostgres(t)
	h := &Handlers{pool: pool}

	tag := boxTag("bil")
	provider := tag + "host"
	name := tag + "m1"
	seedCompareBox(t, pool, name, provider, 5, nil, 0, "")
	if _, err := pool.Exec(context.Background(), `UPDATE entry_boxes SET role = 'egress' WHERE name = $1`, name); err != nil {
		t.Fatalf("seed role: %v", err)
	}

	body := `{"priceAmount":469,"priceCurrency":"RUB","trafficQuotaGb":500,` +
		`"rentedUntil":"2026-12-31","plan":"HighCPU-4"}`
	rec := httptest.NewRecorder()
	h.EntryMachineBilling(rec, billingReq(http.MethodPatch, "/x", body, map[string]string{"name": name}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Machine machineBilling `json:"machine"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Machine.PriceAmount == nil || *res.Machine.PriceAmount != 469 || res.Machine.PriceCurrency != "RUB" {
		t.Errorf("price = %v %s, want 469 RUB", res.Machine.PriceAmount, res.Machine.PriceCurrency)
	}
	if res.Machine.TrafficQuotaGB == nil || *res.Machine.TrafficQuotaGB != 500 {
		t.Errorf("trafficQuotaGb = %v, want 500", res.Machine.TrafficQuotaGB)
	}
	if res.Machine.RentedUntil == nil || *res.Machine.RentedUntil != "2026-12-31" {
		t.Errorf("rentedUntil = %v, want 2026-12-31", res.Machine.RentedUntil)
	}
	if res.Machine.Plan != "HighCPU-4" {
		t.Errorf("plan = %q, want HighCPU-4", res.Machine.Plan)
	}

	var amount float64
	var currency, plan string
	var quota *int
	var rented *string
	if err := pool.QueryRow(context.Background(), `
		SELECT price_amount::float8, coalesce(price_currency, ''), traffic_quota_gb,
		       rented_until::text, plan
		FROM entry_boxes WHERE name = $1
	`, name).Scan(&amount, &currency, &quota, &rented, &plan); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if amount != 469 || currency != "RUB" {
		t.Errorf("stored price = %v %s, want 469 RUB", amount, currency)
	}
	if quota == nil || *quota != 500 {
		t.Errorf("stored quota = %v, want 500", quota)
	}
	if rented == nil || *rented != "2026-12-31" {
		t.Errorf("stored rentedUntil = %v, want 2026-12-31", rented)
	}
	if plan != "HighCPU-4" {
		t.Errorf("stored plan = %q", plan)
	}

	// The fleet screen carries the same figures, plus the role the frontend's
	// column reads.
	fleet := httptest.NewRecorder()
	h.EntryFleet(fleet, httptest.NewRequest(http.MethodGet, "/api/v1/admin/entry/machines?window=24h", nil))
	if fleet.Code != http.StatusOK {
		t.Fatalf("fleet status = %d: %s", fleet.Code, fleet.Body.String())
	}
	var fe entryFleet
	if err := json.Unmarshal(fleet.Body.Bytes(), &fe); err != nil {
		t.Fatalf("unmarshal fleet: %v", err)
	}
	var found *fleetBox
	for i := range fe.Boxes {
		if fe.Boxes[i].Name == name {
			found = &fe.Boxes[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("machine %s is not on the fleet screen", name)
	}
	if found.Role != "egress" {
		t.Errorf("fleet role = %q, want egress", found.Role)
	}
	if found.TrafficQuotaGB == nil || *found.TrafficQuotaGB != 500 {
		t.Errorf("fleet trafficQuotaGb = %v, want 500", found.TrafficQuotaGB)
	}
	if found.PriceAmount == nil || *found.PriceAmount != 469 || found.PriceCurrency != "RUB" {
		t.Errorf("fleet price = %v %s, want 469 RUB", found.PriceAmount, found.PriceCurrency)
	}

	// Absent keeps, null clears: the quota goes, the price stays.
	rec = httptest.NewRecorder()
	h.EntryMachineBilling(rec, billingReq(http.MethodPatch, "/x", `{"trafficQuotaGb":null}`,
		map[string]string{"name": name}))
	if rec.Code != http.StatusOK {
		t.Fatalf("null quota status = %d: %s", rec.Code, rec.Body.String())
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT traffic_quota_gb, price_amount::float8 FROM entry_boxes WHERE name = $1`,
		name).Scan(&quota, &amount); err != nil {
		t.Fatalf("read back after null: %v", err)
	}
	if quota != nil {
		t.Errorf("quota = %v, want NULL", *quota)
	}
	if amount != 469 {
		t.Errorf("price = %v, want it kept", amount)
	}

	// A zero price clears the pair (the column refuses 0, and "not known" is
	// what NULL means).
	rec = httptest.NewRecorder()
	h.EntryMachineBilling(rec, billingReq(http.MethodPatch, "/x", `{"priceAmount":0}`,
		map[string]string{"name": name}))
	if rec.Code != http.StatusOK {
		t.Fatalf("zero price status = %d: %s", rec.Code, rec.Body.String())
	}
	var price *float64
	var cur *string
	if err := pool.QueryRow(context.Background(),
		`SELECT price_amount::float8, price_currency FROM entry_boxes WHERE name = $1`, name).Scan(&price, &cur); err != nil {
		t.Fatalf("read back after zero: %v", err)
	}
	if price != nil || cur != nil {
		t.Errorf("price pair after zero = %v/%v, want NULL/NULL", price, cur)
	}

	// Refusals: a negative price, a date that is not a date, a field nobody
	// asked for, a machine that does not exist.
	for _, c := range []struct {
		name string
		body string
	}{
		{"negative price", `{"priceAmount":-1}`},
		{"bad date", `{"rentedUntil":"31.12.2026"}`},
		{"unknown field", `{"price":10}`},
		{"price without currency", `{"priceAmount":10}`},
		{"negative quota", `{"trafficQuotaGb":-5}`},
	} {
		rec := httptest.NewRecorder()
		h.EntryMachineBilling(rec, billingReq(http.MethodPatch, "/x", c.body, map[string]string{"name": name}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", c.name, rec.Code, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	h.EntryMachineBilling(rec, billingReq(http.MethodPatch, "/x", `{"plan":"x"}`,
		map[string]string{"name": tag + "nosuch"}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown machine status = %d, want 404", rec.Code)
	}
}
