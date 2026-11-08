package subscription

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

func TestBrowserNegotiationPreservesClients(t *testing.T) {
	for _, tt := range []struct {
		accept, features, engine string
		want                     bool
	}{
		{"text/html,application/xhtml+xml,*/*;q=0.8", "", "", true},
		{"*/*", "", "", false}, {"text/plain", "", "", false},
		{"text/html;q=0", "", "", false}, {"text/html;q=bad", "", "", false},
		{"text/html", "relay-metrics-v2", "", false}, {"text/html", "", "xray", false},
	} {
		r := httptest.NewRequest("GET", "/sub/token?engine="+tt.engine, nil)
		r.Header.Set("Accept", tt.accept)
		r.Header.Set(featuresHeader, tt.features)
		if got := wantsSubscriptionPage(r); got != tt.want {
			t.Errorf("%+v: got %v", tt, got)
		}
	}
}

func TestSubscriptionBrowserPageAndClientBody(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("page", bytes.Repeat([]byte{9}, 32))
	s := New(pool, &fakeBatch{envelopes: [][]byte{signed(t, signer, xhttpRecord())}}, []entry.Key{signer.Key()})
	token, err := s.Add(context.Background(), "t-"+uuid.NewString()[:8], unlimited(t, s))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sub/{token}", s.Handler())
	for _, accept := range []string{"text/html", "*/*"} {
		path := "/sub/" + token
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Accept", accept)
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, r)
		if out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("subscription was cached")
		}
		switch accept {
		case "text/html":
			if out.Code != http.StatusSeeOther {
				t.Fatalf("browser status %d", out.Code)
			}
			target, err := url.Parse(out.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			fragment, err := url.ParseQuery(target.Fragment)
			if err != nil {
				t.Fatal(err)
			}
			if target.Path != "/open" || fragment.Get("url") != LinkURL(token) {
				t.Fatalf("wrong browser location: %s", target)
			}
			if out.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("credential could leak through referrer")
			}
		default:
			if out.Code != 200 || out.Header().Get("Subscription-Userinfo") == "" {
				t.Fatal("client format changed")
			}
		}
	}
	r := httptest.NewRequest("GET", "/sub/"+strings.Repeat("x", 32), nil)
	r.Header.Set("Accept", "text/html")
	out := httptest.NewRecorder()
	mux.ServeHTTP(out, r)
	if out.Code != 404 {
		t.Fatalf("unknown token: %d", out.Code)
	}
}
