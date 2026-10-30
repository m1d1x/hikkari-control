package downdetector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	inviteToken = "invite-token-0123456789"
	adminToken  = "admin-token-0123456789"
)

func newTestServer(t *testing.T, targets []Target) (*Server, *Prober) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "dd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := NewProber(targets)
	return &Server{
		Store:            NewStore(),
		Prober:           p,
		DB:               db,
		Auth:             NewAuth(Secrets{InviteTokens: []string{inviteToken}, AdminTokens: []string{adminToken}}, 120),
		K:                2,
		OverviewMaxHours: 720,
		Now:              func() time.Time { return testNow },
	}, p
}

func do(t *testing.T, h http.Handler, method, url, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReportThenStatus(t *testing.T) {
	s, p := newTestServer(t, []Target{{ID: srvA, Host: "example.com", Port: 443}})
	p.dial = func(context.Context, string) error { return nil }
	p.Round(context.Background())
	h := s.Handler()

	if rec := do(t, h, "POST", "/v1/reports", inviteToken, validReport); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, "GET", "/v1/status?asn=12389&country=ru&ids="+srvA+",n:unknown-node", inviteToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	var out []ServerStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("status = %+v", out)
	}
	if out[0].External != ExternalUp || out[0].SameASN != (Group{1, 9, 1}) || out[0].VerdictIfDown != VerdictUnknown {
		t.Errorf("registry server = %+v", out[0])
	}
	if out[1].External != ExternalUnknown || out[1].VerdictIfDown != VerdictUnknown {
		t.Errorf("unknown server = %+v", out[1])
	}
}

func TestAuth(t *testing.T) {
	s, _ := newTestServer(t, nil)
	h := s.Handler()
	for _, tok := range []string{"", "wrong-token-0123456789", adminToken} {
		if rec := do(t, h, "POST", "/v1/reports", tok, validReport); rec.Code != http.StatusUnauthorized {
			t.Errorf("report with %q: %d", tok, rec.Code)
		}
	}
	if rec := do(t, h, "GET", "/v1/overview", inviteToken, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("overview with invite token: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/v1/overview?hours=48", adminToken, ""); rec.Code != http.StatusOK {
		t.Errorf("overview with admin token: %d %s", rec.Code, rec.Body)
	}
}

func TestBadRequests(t *testing.T) {
	s, _ := newTestServer(t, nil)
	h := s.Handler()
	if rec := do(t, h, "POST", "/v1/reports", inviteToken, strings.Replace(validReport, `"tunnel"`, `"udp"`, 1)); rec.Code != http.StatusBadRequest {
		t.Errorf("bad report: %d", rec.Code)
	}
	for _, q := range []string{"", "?ids=s:nothex", "?ids=" + srvA + "&asn=-1", "?ids=" + srvA + "&country=rus"} {
		if rec := do(t, h, "GET", "/v1/status"+q, inviteToken, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("status %q: %d", q, rec.Code)
		}
	}
}

func TestTokenRateLimit(t *testing.T) {
	s, _ := newTestServer(t, nil)
	s.Auth = NewAuth(Secrets{InviteTokens: []string{inviteToken}}, 2)
	h := s.Handler()
	codes := []int{}
	for range 3 {
		codes = append(codes, do(t, h, "GET", "/v1/status?ids="+srvA, inviteToken, "").Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v", codes)
	}
}

func TestProberStates(t *testing.T) {
	p := NewProber([]Target{{ID: srvA, Host: "h", Port: 1}})
	fail := true
	p.dial = func(context.Context, string) error {
		if fail {
			return errors.New("refused")
		}
		return nil
	}
	if p.External(srvA) != ExternalUnknown {
		t.Fatal("not probed yet must be unknown")
	}
	want := []External{ExternalUp, ExternalUp, ExternalDown}
	for i, w := range want {
		p.Round(context.Background())
		if got := p.External(srvA); got != w {
			t.Fatalf("after %d failures: %s, want %s", i+1, got, w)
		}
	}
	fail = false
	p.Round(context.Background())
	if p.External(srvA) != ExternalUp {
		t.Fatal("one success must turn it up")
	}
	if p.External("n:other") != ExternalUnknown {
		t.Fatal("server outside the registry must be unknown")
	}
}
