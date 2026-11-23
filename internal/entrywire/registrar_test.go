package entrywire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/porkbun"
)

func TestEnsureSubdomainA(t *testing.T) {
	for _, tt := range []struct {
		name    string
		records string
		wantID  string
		create  bool
	}{
		{"create below apex", `[{"id":"1","name":"cedar.lol","type":"A","content":"192.0.2.2"}]`, "25", true},
		{"reuse existing", `[{"id":"25","name":"Edge.Cedar.Lol.","type":"A","content":"192.0.2.1"}]`, "25", false},
		{"conflicting A", `[{"id":"25","name":"edge.cedar.lol","type":"A","content":"192.0.2.1"},
			{"id":"26","name":"edge.cedar.lol","type":"A","content":"192.0.2.2"}]`, "", false},
		{"CNAME", `[{"id":"25","name":"edge.cedar.lol","type":"CNAME","content":"old.example"}]`, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var creates atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/dns/retrieve/cedar.lol":
					fmt.Fprintf(w, `{"status":"SUCCESS","records":%s}`, tt.records)
				case "/dns/create/cedar.lol":
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					for key, want := range map[string]string{"name": "edge", "type": "A", "content": "192.0.2.1", "ttl": "600"} {
						if body[key] != want {
							t.Errorf("request %s = %q, want %q", key, body[key], want)
						}
					}
					creates.Add(1)
					fmt.Fprint(w, `{"status":"SUCCESS","id":25}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			reg := registrar{c: porkbun.New("key", "secret", porkbun.WithBaseURL(server.URL), porkbun.WithHTTPClient(server.Client()))}
			id, err := reg.EnsureSubdomainA(context.Background(), "cedar.lol", "edge", netip.MustParseAddr("192.0.2.1"))
			if tt.wantID == "" {
				if !errors.Is(err, entry.ErrRejected) {
					t.Fatalf("expected conflict, got %q, %v", id, err)
				}
			} else if id != tt.wantID || err != nil {
				t.Fatalf("got %q, %v, want record %s", id, err, tt.wantID)
			}
			if n := creates.Load(); n > 1 || (n == 1) != tt.create {
				t.Errorf("create requests = %d, expected create = %t", n, tt.create)
			}
		})
	}
}
