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
	h.prov.media = fixedMedia{{IP: "203.0.113.9", From: 40000, To: 40099}}
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
	if code, body := do("POST", "/api/v1/admin/entry/boxes/"+box+"/retire",
		map[string]string{"reason": DeathIPBanned}); code != http.StatusOK ||
		!bytes.Contains(body, []byte(`"instanceDeleted":true`)) {
		t.Fatalf("retire with a touch: %d %s", code, body)
	}
	if got := h.host.deletedIDs(); len(got) != 1 {
		t.Fatalf("deleted = %v, want the retired machine's instance", got)
	}
}
