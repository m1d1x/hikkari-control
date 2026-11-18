package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/fx"
)

// stepUpMux mounts the pool's admin routes behind an owner gate that always
// lets the test's admin in, and a step-up that refuses until touched is set —
// the shape admin.RequireStepUp has in cmd/server.
func (h *hostHarness) stepUpMux(touched *bool) func(method, path string, body any) (int, []byte) {
	h.t.Helper()
	var adminID string
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, 'entry step-up test', 'x', 'owner') RETURNING id::text`,
		"stepup-"+uuid.NewString()[:8]).Scan(&adminID); err != nil {
		h.t.Fatal(err)
	}
	gate := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: adminID})))
		}
	}
	stepUp := func(next http.HandlerFunc) http.HandlerFunc {
		return gate(func(w http.ResponseWriter, r *http.Request) {
			if !*touched {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "step_up_required"})
				return
			}
			next(w, r)
		})
	}
	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, NewAdminHandlers(h.svc, h.prov, h.pool), gate, stepUp)
	return func(method, path string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		return rec.Code, rec.Body.Bytes()
	}
}

// Owner's decision 27.10 (audit 42 С5): what destroys without a way back, or
// buys, asks for a fresh passkey touch, like deleting an instance and ordering
// a machine already did. A stolen owner session without the touch used to
// retire the whole fleet — instances and disks deleted at the hoster — or
// forget a host key so that the next login trusts whoever answers.
func TestDestructiveBoxActionsNeedAStepUp(t *testing.T) {
	h := newHostHarness(t)
	box := h.inService()
	touched := false
	do := h.stepUpMux(&touched)

	cmdsBefore := len(h.box.cmds)
	keyBefore := h.hostKeyOf(box)

	if code, body := do("POST", "/api/v1/admin/entry/boxes/"+box+"/retire",
		map[string]string{"reason": DeathIPBanned}); code != http.StatusForbidden {
		t.Errorf("retire without a touch: %d %s, want 403", code, body)
	}
	if got := h.host.deletedIDs(); len(got) != 0 {
		t.Errorf("retire without a touch deleted instances %v at the hoster", got)
	}
	if h.retired(box) || h.aliveOn(box) == 0 {
		t.Errorf("retire without a touch took the box out of the pool")
	}

	if code, body := do("POST", "/api/v1/admin/entry/boxes/"+box+"/forget-host-key", nil); code != http.StatusForbidden {
		t.Errorf("forget-host-key without a touch: %d %s, want 403", code, body)
	}
	if k := h.hostKeyOf(box); k != keyBefore {
		t.Errorf("forget-host-key without a touch: key %q, was %q", k, keyBefore)
	}

	if code, body := do("POST", "/api/v1/admin/entry/boxes/"+box+"/wipe", nil); code != http.StatusForbidden {
		t.Errorf("wipe without a touch: %d %s, want 403", code, body)
	}

	newName := boxName("s", uuid.New())
	if code, body := do("POST", "/api/v1/admin/entry/boxes", map[string]any{
		"name": newName, "address": "2001:db8::5e", "provider": "vultr", "location": "amsterdam",
		"password": "provider-pw",
	}); code != http.StatusForbidden {
		t.Errorf("create box without a touch: %d %s, want 403", code, body)
	}
	if _, err := h.svc.boxAccess(context.Background(), newName); err == nil {
		t.Errorf("create box without a touch recorded %s", newName)
	}

	h.box.mu.Lock()
	cmdsAfter := len(h.box.cmds)
	h.box.mu.Unlock()
	if cmdsAfter != cmdsBefore {
		t.Errorf("the box ran %d commands for requests that were refused", cmdsAfter-cmdsBefore)
	}

	// The touch is what opens them, not a route that no longer works.
	touched = true
	// A ban is the prober's call (censor_proof.go): it says banned.
	h.vantage(VerdictIPBanned)
	if code, body := do("POST", "/api/v1/admin/entry/boxes/"+box+"/retire",
		map[string]string{"reason": DeathIPBanned}); code != http.StatusOK ||
		!bytes.Contains(body, []byte(`"instanceDeleted":true`)) {
		t.Fatalf("retire with a touch: %d %s", code, body)
	}
	if got := h.host.deletedIDs(); len(got) != 1 {
		t.Fatalf("deleted = %v, want the retired machine's instance", got)
	}
}

// The owner names a machine on the screen (18.11, «мейн хаб»): a label is set,
// taken back with "", and refused when it is not one short line.
func TestABoxTakesTheOwnersLabel(t *testing.T) {
	h := newHostHarness(t)
	box := h.inService()
	touched := false
	do := h.stepUpMux(&touched)
	label := func() *string {
		var l *string
		if err := h.pool.QueryRow(context.Background(), `SELECT label FROM entry_boxes WHERE name = $1`, box).Scan(&l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	path := "/api/v1/admin/entry/boxes/" + box + "/label"
	if code, body := do("PUT", path, map[string]string{"label": "  мейн хаб "}); code != http.StatusOK {
		t.Fatalf("label without a touch: %d %s, want 200 (a label acts on nothing)", code, body)
	}
	if l := label(); l == nil || *l != "мейн хаб" {
		t.Errorf("label = %v, want «мейн хаб» trimmed", l)
	}
	for _, bad := range []string{strings.Repeat("я", MaxBoxLabel+1), "две\nстроки"} {
		if code, _ := do("PUT", path, map[string]string{"label": bad}); code != http.StatusBadRequest {
			t.Errorf("label %q: %d, want 400", bad, code)
		}
	}
	if l := label(); l == nil || *l != "мейн хаб" {
		t.Errorf("a refused label changed it to %v", l)
	}
	if code, _ := do("PUT", path, map[string]string{"label": ""}); code != http.StatusOK {
		t.Errorf("clearing the label: %d, want 200", code)
	}
	if l := label(); l != nil {
		t.Errorf("label = %q after clearing, want NULL", *l)
	}
	if code, _ := do("PUT", "/api/v1/admin/entry/boxes/no-such-box/label", map[string]string{"label": "x"}); code != http.StatusNotFound {
		t.Errorf("label of an unknown box: %d, want 404", code)
	}
}

// The owner prices a machine from the screen, not the console (18.11: «флот
// часто меняется»): the same price `entry-box price` sets, counted in cents.
func TestABoxIsPricedFromTheScreen(t *testing.T) {
	h := newHostHarness(t)
	h.svc.SetRates(fx.Fixed(map[string]float64{fx.USD: 82, fx.EUR: 91}))
	box := h.inService()
	touched := false
	do := h.stepUpMux(&touched)
	path := "/api/v1/admin/entry/boxes/" + box + "/price"

	code, body := do("PUT", path, map[string]any{"amount": 469, "currency": "руб"})
	if code != http.StatusOK {
		t.Fatalf("price without a touch: %d %s, want 200 (no money moves)", code, body)
	}
	var got struct {
		MonthlyCents int `json:"monthlyCents"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.MonthlyCents != 572 {
		t.Errorf("answer %s, want monthlyCents 572 (469 / 82)", body)
	}
	var amount float64
	var cur string
	var cents int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT price_amount::float8, price_currency, monthly_cents FROM entry_boxes WHERE name = $1`, box).
		Scan(&amount, &cur, &cents); err != nil {
		t.Fatal(err)
	}
	if amount != 469 || cur != fx.RUB || cents != 572 {
		t.Errorf("stored %v %s %d, want 469 RUB 572", amount, cur, cents)
	}
	for _, bad := range []map[string]any{{"amount": 0, "currency": "USD"}, {"amount": 5, "currency": "GBP"}} {
		if code, _ := do("PUT", path, bad); code != http.StatusBadRequest {
			t.Errorf("price %v: %d, want 400", bad, code)
		}
	}
	if code, _ := do("PUT", "/api/v1/admin/entry/boxes/no-such-box/price", map[string]any{"amount": 1, "currency": "USD"}); code != http.StatusNotFound {
		t.Errorf("price of an unknown box: %d, want 404", code)
	}
}

// The terabytes in a hoster's plan are typed on the screen (18.11: «у аезы щас
// миллиарды за тб»): set, changed, forgotten with 0.
func TestAHostersPlanTerabytesAreSetFromTheScreen(t *testing.T) {
	h := newHostHarness(t)
	box := h.inService()
	var provider string
	if err := h.pool.QueryRow(context.Background(), `SELECT provider FROM entry_boxes WHERE name = $1`, box).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_provider_plans WHERE provider = $1`, provider)
	})
	touched := false
	do := h.stepUpMux(&touched)
	path := "/api/v1/admin/entry/hosters/" + provider + "/plan-tb"
	planTB := func() *float64 {
		var tb *float64
		err := h.pool.QueryRow(context.Background(), `SELECT plan_tb::float8 FROM entry_provider_plans WHERE provider = $1`, provider).Scan(&tb)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return tb
	}
	for _, tb := range []float64{32, 2.5} {
		if code, body := do("PUT", path, map[string]any{"tb": tb}); code != http.StatusOK {
			t.Fatalf("plan %v TB without a touch: %d %s, want 200", tb, code, body)
		}
		if got := planTB(); got == nil || *got != tb {
			t.Errorf("plan_tb = %v, want %v", got, tb)
		}
	}
	if code, _ := do("PUT", path, map[string]any{"tb": -1}); code != http.StatusBadRequest {
		t.Errorf("negative plan: %d, want 400", code)
	}
	if code, _ := do("PUT", path, map[string]any{"tb": 0}); code != http.StatusOK {
		t.Errorf("forgetting the plan: %d, want 200", code)
	}
	if got := planTB(); got != nil {
		t.Errorf("plan_tb = %v after 0, want none", *got)
	}
	if code, _ := do("PUT", "/api/v1/admin/entry/hosters/no-such-hoster/plan-tb", map[string]any{"tb": 1}); code != http.StatusNotFound {
		t.Errorf("plan of an unknown hoster: %d, want 404", code)
	}
}
