package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

// The console surface of provider accounts and domain purchases (П9.1): keys
// and spending only behind the step-up gate, keys never read back, provider
// refusals in the provider's words.
func TestAdminProvidersAndPurchases(t *testing.T) {
	h, c := fakeProviders(t)
	var adminID string
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, 'entry test', 'x', 'owner') RETURNING id::text`,
		"prov-"+uuid.NewString()[:8]).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	steppedUp := false
	gate := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: adminID})))
		}
	}
	// The real step-up gate is admin.RequireStepUp; here a flag stands in for
	// the passkey touch.
	stepUp := func(next http.HandlerFunc) http.HandlerFunc {
		return gate(func(w http.ResponseWriter, r *http.Request) {
			if !steppedUp {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "step_up_required"})
				return
			}
			next(w, r)
		})
	}
	RegisterAdminRoutes(mux, NewAdminHandlers(h.svc, nil, h.pool), gate, stepUp)
	do := func(method, path string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		return rec.Code, rec.Body.Bytes()
	}

	save := map[string]any{
		// 160 per domain: .lol (154) passes, .autos (180) does not.
		"enabled": true, "credentials": goodCreds, "tlds": []string{"lol"},
	}
	if code, _ := do("PUT", "/api/v1/admin/entry/providers/"+fakeKind, save); code != http.StatusForbidden {
		t.Fatalf("save without a touch: %d, want 403", code)
	}
	steppedUp = true
	if code, body := do("PUT", "/api/v1/admin/entry/providers/"+fakeKind, save); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}

	// A wrong key: refused with the reason, nothing replaced.
	bad := map[string]any{"enabled": true, "credentials": map[string]string{"api_key": "pk1_otherotherot", "secret": "nope"}}
	if code, _ := do("PUT", "/api/v1/admin/entry/providers/"+fakeKind, bad); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad key: %d, want 422", code)
	}

	code, listed := do("GET", "/api/v1/admin/entry/providers", nil)
	if code != 200 {
		t.Fatalf("list: %d %s", code, listed)
	}
	if bytes.Contains(listed, []byte("right-secret")) || bytes.Contains(listed, []byte("pk1_fakefakefake")) {
		t.Fatalf("the list carries the key: %s", listed)
	}
	if !bytes.Contains(listed, []byte(`"credsHint":"pk1_…fake"`)) || !bytes.Contains(listed, []byte(`"active":true`)) {
		t.Fatalf("list lost the saved account: %s", listed)
	}

	if code, body := do("POST", "/api/v1/admin/entry/providers/"+fakeKind+"/check", nil); code != 200 {
		t.Fatalf("check: %d %s", code, body)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/providers/nope/check", nil); code != 404 {
		t.Fatalf("check of an unknown kind: %d, want 404", code)
	}

	if code, body := do("GET", "/api/v1/admin/entry/domains/suggest?n=1", nil); code != 200 || !bytes.Contains(body, []byte(".lol")) {
		t.Fatalf("suggest: %d %s", code, body)
	}
	if code, body := do("POST", "/api/v1/admin/entry/domains/checkout", map[string]string{"domain": "birch2048.lol"}); code != 200 ||
		!bytes.Contains(body, []byte(`"costCents":154`)) {
		t.Fatalf("checkout: %d %s", code, body)
	}
	if c.reg.buys != 0 {
		t.Fatal("quoting spent money")
	}

	steppedUp = false
	if code, _ := do("POST", "/api/v1/admin/entry/domains/buy", map[string]string{"domain": "birch2048.lol"}); code != http.StatusForbidden {
		t.Fatalf("buy without a touch: %d, want 403", code)
	}
	steppedUp = true
	if code, body := do("POST", "/api/v1/admin/entry/domains/buy", map[string]string{"domain": "birch2048.lol"}); code != 200 ||
		!bytes.Contains(body, []byte(`"status":"owned"`)) {
		t.Fatalf("buy: %d %s", code, body)
	}
	h.spendRoom(SpendRegistrar, 100)
	if code, _ := do("POST", "/api/v1/admin/entry/domains/buy", map[string]string{"domain": "birch2048.autos"}); code != http.StatusConflict {
		t.Fatalf("buy over the cap: %d, want 409", code)
	}

	if code, body := do("POST", "/api/v1/admin/entry/domains/seed", map[string]string{"domain": "slate7777.top"}); code != 200 {
		t.Fatalf("seed: %d %s", code, body)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/domains/seed", map[string]string{"domain": "nowhere4242.lol"}); code != 400 {
		t.Fatalf("seed of a name not on the account: %d, want 400", code)
	}
	if code, body := do("GET", "/api/v1/admin/entry/domains/account", nil); code != 200 ||
		!bytes.Contains(body, []byte(`"domain":"slate7777.top"`)) || !bytes.Contains(body, []byte(`"hubStatus":"owned"`)) {
		t.Fatalf("account domains: %d %s", code, body)
	}
	// The do-not-use list: adding is protective (no touch), removing is not.
	steppedUp = false
	if code, body := do("POST", "/api/v1/admin/entry/domains/exclusions", map[string]string{"domain": "oldbrand.lol", "note": "legacy"}); code != 200 {
		t.Fatalf("exclude: %d %s", code, body)
	}
	if code, body := do("GET", "/api/v1/admin/entry/domains/exclusions", nil); code != 200 ||
		!bytes.Contains(body, []byte(`"domain":"oldbrand.lol"`)) || !bytes.Contains(body, []byte(`"builtIn":true`)) {
		t.Fatalf("exclusions: %d %s", code, body)
	}
	if code, _ := do("DELETE", "/api/v1/admin/entry/domains/exclusions/oldbrand.lol", nil); code != http.StatusForbidden {
		t.Fatalf("unexclude without a touch: %d, want 403", code)
	}
	steppedUp = true
	if code, _ := do("DELETE", "/api/v1/admin/entry/domains/exclusions/oldbrand.lol", nil); code != 200 {
		t.Fatalf("unexclude: %d", code)
	}
	if code, _ := do("DELETE", "/api/v1/admin/entry/domains/exclusions/oldbrand.lol", nil); code != 404 {
		t.Fatalf("unexclude twice: %d, want 404", code)
	}

	code, ledger := do("GET", "/api/v1/admin/entry/domains", nil)
	if code != 200 || !bytes.Contains(ledger, []byte("birch2048.lol")) || !bytes.Contains(ledger, []byte("slate7777.top")) ||
		!bytes.Contains(ledger, []byte(`"spend":{"role":"registrar"`)) {
		t.Fatalf("ledger: %d %s", code, ledger)
	}

	// Every save and buy is in the audit trail, the key never.
	var n int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM admin_audit_log
		WHERE admin_id = $1 AND action IN ('entry.provider.save', 'entry.domain.buy', 'entry.domain.seed')`,
		adminID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("audit rows = %d, want 3 (save, buy, seed)", n)
	}
	var leaked int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM admin_audit_log WHERE admin_id = $1 AND payload::text LIKE '%right-secret%'`,
		adminID).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Error("a key reached the audit trail")
	}
}

// A hoster is saved through the console with its order defaults. Found on the
// stand 25.10: the first Vultr key ever entered was refused with "body is not
// a provider account" — the console sent defaultRegion and defaultPlan, the
// handler did not know them, and its strict decoder threw the whole body out.
// Every hoster save through the console had been failing since П9.2.
func TestTheConsoleSavesAHosterWithItsOrderDefaults(t *testing.T) {
	h := newHostHarness(t)
	var adminID string
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, 'entry test', 'x', 'owner') RETURNING id::text`,
		"host-"+uuid.NewString()[:8]).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	admin := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: adminID})))
		}
	}
	RegisterAdminRoutes(mux, NewAdminHandlers(h.svc, nil, h.pool), admin, admin)
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"enabled": true, "credentials": goodHostCreds,
		"defaultRegion": "r2", "defaultPlan": "r2only",
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/admin/entry/providers/"+fakeHostKind, &buf))
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.Bytes())
	}
	ps, err := h.svc.Providers(context.Background())
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	for _, p := range ps {
		if p.Kind == fakeHostKind {
			if p.DefaultRegion != "r2" || p.DefaultPlan != "r2only" {
				t.Fatalf("saved defaults %q / %q, want r2 / r2only", p.DefaultRegion, p.DefaultPlan)
			}
			return
		}
	}
	t.Fatal("the hoster is not listed after saving")
}
