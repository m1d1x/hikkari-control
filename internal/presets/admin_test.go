package presets

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

type adminClient struct {
	t     *testing.T
	mux   *http.ServeMux
	owner bool
}

func newAdminClient(t *testing.T) (*adminClient, *Store) {
	s := liveStore(t)
	c := &adminClient{t: t, mux: http.NewServeMux(), owner: true}
	gate := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: "00000000-0000-0000-0000-000000000001", Login: "kirill"})))
		}
	}
	owner := func(next http.HandlerFunc) http.HandlerFunc {
		return gate(func(w http.ResponseWriter, r *http.Request) {
			if !c.owner {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next(w, r)
		})
	}
	RegisterAdminRoutes(c.mux, NewAdminHandlers(s, nil), gate, owner)
	return c, s
}

func (c *adminClient) do(method, path string, body any, want int) map[string]any {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			c.t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	c.mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
	if rec.Code != want {
		c.t.Fatalf("%s %s: %d %s, want %d", method, path, rec.Code, rec.Body.String(), want)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// The screens' whole loop over HTTP: groups, a preset, a partial release,
// the stage to 100, and what a device is then served.
func TestTheAdminSurfaceEditsReleasesAndRollsOut(t *testing.T) {
	c, s := newAdminClient(t)
	c.do("PUT", "/api/v1/admin/presets/groups/steam", map[string]any{
		"name": "Steam", "icon": "steam",
		"rules": map[string]any{"domains": []string{"steampowered.com"}, "ips": []string{}, "processes": map[string]any{"windows": []string{"steam.exe"}}},
	}, http.StatusOK)
	bad := c.do("PUT", "/api/v1/admin/presets/groups/discord", map[string]any{
		"name": "Discord", "rules": map[string]any{"domains": []string{"https://Discord.com/app"}},
	}, http.StatusUnprocessableEntity)
	if bad["error"] == "" {
		t.Fatal("no reason for a refused group")
	}
	norm := c.do("POST", "/api/v1/admin/presets/rules/normalize", map[string]string{"category": "domains", "value": "https://Discord.com/app"}, http.StatusOK)
	if norm["value"] != "discord.com" {
		t.Fatalf("normalized to %v", norm["value"])
	}
	c.do("PUT", "/api/v1/admin/presets/groups/discord", map[string]any{
		"name": "Discord", "icon": "discord", "rules": map[string]any{"domains": []string{"discord.com"}},
	}, http.StatusOK)

	groups := c.do("GET", "/api/v1/admin/presets/groups", nil, http.StatusOK)["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("%d groups", len(groups))
	}

	c.do("POST", "/api/v1/admin/presets", map[string]any{"id": "ru", "name": "россия", "default": true, "countries": []string{"ru"}}, http.StatusUnprocessableEntity)
	created := c.do("POST", "/api/v1/admin/presets", map[string]any{
		"id": "ru", "name": "россия", "default": true, "countries": []string{"RU"}, "bypass": []string{"steam"}, "proxy": []string{"discord"},
	}, http.StatusCreated)["preset"].(map[string]any)
	if created["current"] != nil || created["next"] != nil {
		t.Fatal("a new preset has releases")
	}
	c.do("DELETE", "/api/v1/admin/presets/groups/steam", nil, http.StatusConflict)

	c.owner = false
	c.do("POST", "/api/v1/admin/presets/ru/release", map[string]int{"stage": 5}, http.StatusForbidden)
	c.owner = true

	p := c.do("POST", "/api/v1/admin/presets/ru/release", map[string]int{"stage": 5}, http.StatusOK)["preset"].(map[string]any)
	if p["next"] == nil || p["stage"].(float64) != 5 || p["current"] != nil {
		t.Fatalf("after release: %v", p)
	}
	c.do("POST", "/api/v1/admin/presets/ru/stage", map[string]int{"stage": 101}, http.StatusUnprocessableEntity)
	p = c.do("POST", "/api/v1/admin/presets/ru/stage", map[string]int{"stage": 100}, http.StatusOK)["preset"].(map[string]any)
	if p["next"] != nil || p["current"].(map[string]any)["createdBy"] != "kirill" {
		t.Fatalf("after 100: %v", p)
	}
	c.do("POST", "/api/v1/admin/presets/ru/revert", nil, http.StatusConflict)

	if sv, ok := serve(t, s, "RU", 99); !ok || len(sv.Services) != 2 {
		t.Fatalf("device served %v %+v", ok, sv)
	}
	c.do("DELETE", "/api/v1/admin/presets/ru", nil, http.StatusConflict)
	c.do("POST", "/api/v1/admin/presets/ghost/release", map[string]int{"stage": 100}, http.StatusNotFound)
}
