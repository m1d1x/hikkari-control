package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

func TestTheAdminSurfaceListsKillsAndPublishes(t *testing.T) {
	h := newHarness(t)
	var adminID string
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, 'entry test', 'x', 'owner') RETURNING id::text`,
		"entry-"+uuid.NewString()[:8]).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	// The real gate is admin.RequireAdmin + RequireOwner (cmd/server); here it
	// only puts an owner on the request, or nobody.
	asOwner := true
	// A box the hub cannot log into: the wipe after a retire fails in its job,
	// and the retire still stands.
	prov := NewProvisioner(h.svc, (&fakeBox{hostKey: "ssh-ed25519 AAAA"}).dial, t.TempDir(), "api.example.org:443", nil)
	gate := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if asOwner {
				r = r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: adminID}))
			}
			next(w, r)
		}
	}
	RegisterAdminRoutes(mux, NewAdminHandlers(h.svc, prov, h.pool), gate, gate)
	do := func(method, path string, body any) (int, map[string]any) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	h1 := h.personal(1)[0]
	code, out := do("GET", "/api/v1/admin/entry/relays", nil)
	listed, _ := json.Marshal(out)
	if code != 200 || !bytes.Contains(listed, []byte(`"`+h1+`"`)) {
		t.Fatalf("list: %d %s", code, listed)
	}
	if bytes.Contains(listed, []byte("private_key")) || bytes.Contains(listed, []byte("uuid")) {
		t.Fatal("the list carries relay secrets")
	}

	if code, _ := do("POST", "/api/v1/admin/entry/relays/"+h1+"/dead", nil); code != 400 {
		t.Fatalf("a death with no body: %d, want 400", code)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/relays/"+h1+"/dead", map[string]string{"reason": "who knows"}); code != 400 {
		t.Fatalf("a death with an unknown reason: %d, want 400", code)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/relays/"+h1+"/dead", map[string]string{"reason": DeathFingerprint}); code != 200 {
		t.Fatalf("mark dead: %d", code)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/relays/"+h1+"/dead", map[string]string{"reason": DeathFingerprint}); code != 404 {
		t.Fatalf("dead twice: %d, want 404", code)
	}

	// A box handed back: its relays die at once, the wipe is a job to follow.
	box, _ := h.box()
	r1 := h.relay(box, KindPersonal)
	h.admit(r1)
	code, out = do("POST", "/api/v1/admin/entry/boxes/"+box+"/retire", map[string]string{"reason": DeathRotated})
	if code != 200 || out["jobId"] == nil || fmt.Sprint(out["relays"]) != "["+r1+"]" {
		t.Fatalf("retire: %d %v", code, out)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/boxes/"+box+"/retire", map[string]string{"reason": DeathRotated}); code != 409 {
		t.Fatalf("retire twice: %d, want 409", code)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/boxes/"+box+"/provision", nil); code != 409 {
		t.Fatalf("provisioning a retired box: %d, want 409", code)
	}

	if code, out := do("POST", "/api/v1/admin/entry/sources", map[string]any{"sources": testSources().Sources}); code != 200 || out["present"] != true {
		t.Fatalf("publish sources: %d %v", code, out)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/sources", map[string]any{"sources": []any{}}); code != 400 {
		t.Fatalf("empty source list: %d, want 400", code)
	}

	var audited int
	_ = h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM admin_audit_log
		WHERE admin_id = $1 AND action IN ('entry.sources.publish', 'entry.relay.dead', 'entry.box.retire')`, adminID).Scan(&audited)
	if audited != 3 {
		t.Fatalf("audit rows %d, want the publish, the death and the retire", audited)
	}

	asOwner = false
	if code, _ := do("POST", "/api/v1/admin/entry/relays/"+h1+"/dead", nil); code != 401 {
		t.Fatalf("no admin on the request: %d, want 401", code)
	}
}
