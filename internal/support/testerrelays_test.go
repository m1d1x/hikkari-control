package support

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTesterRelaysReadTheLatestRound(t *testing.T) {
	store := NewMemTesterStore()
	mux := http.NewServeMux()
	RegisterTesterAdminRoutes(mux, store, func(h http.HandlerFunc) http.HandlerFunc { return h })

	const ts = "2026/11/02 15:04:05.000000 "
	add := func(install string, lines ...string) {
		for i := range lines {
			lines[i] = ts + lines[i]
		}
		if err := store.AppendChunk(context.Background(), TesterChunkRequest{InstallID: install, Platform: "windows", Version: "1.0.22", Lines: lines}); err != nil {
			t.Fatal(err)
		}
	}
	add("t-healthy", "[diag:reach] p-a type=hysteria2 proto=hysteria2 ping=70ms")
	add("t-broken",
		"[diag:reach] self Ethernet 2 isp=AS12389",
		"[diag:reach] control Ethernet 2 flows=12/12 med=20ms",
		"[diag:reach] p-reality type=vless proto=vless-reality ping=90ms flows[Ethernet 2]=12/12 med=30ms",
		"[diag:reach] p-xhttp type=vless proto=vless-xhttp ping=fail(503 Service Unavailable: {\"message\":\"An error\"}) flows[Ethernet 2]=3/12 med=40ms",
		"[diag:reach] p-hy2 type=hysteria2 proto=hysteria2 ping=84ms",
		"[diag:reach] p-awg type=wireguard ping=fail(504 Gateway Timeout: x)",
		"[singbox] unrelated line",
	)
	// A newer round: reality died, and its last success stays known.
	add("t-broken", "[diag:reach] p-reality type=vless proto=vless-reality ping=fail(503 Service Unavailable: x) flows[Ethernet 2]=0/12 med=-")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/tester-relays", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got TesterRelayScreen
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Installs) != 2 {
		t.Fatalf("installs = %+v", got.Installs)
	}
	broken := got.Installs[0]
	if broken.InstallID != "t-broken" {
		broken = got.Installs[1]
	}
	if broken.InstallID != "t-broken" || broken.Network != "AS12389" {
		t.Fatalf("broken = %+v", broken)
	}
	relays := map[string]TesterRelay{}
	for _, r := range broken.Relays {
		relays[r.Tag] = r
	}
	if r := relays["p-reality"]; r.Up || r.Reason != RelayTCPBlocked || r.LastOK == nil || r.Proto != "vless-reality" {
		t.Errorf("reality = %+v", r)
	}
	if r := relays["p-xhttp"]; r.Up || r.Reason != RelayTCPThinned || r.FlowsOpen == nil || *r.FlowsOpen != 3 || r.FlowsTotal != 12 {
		t.Errorf("xhttp = %+v", r)
	}
	if r := relays["p-hy2"]; !r.Up || r.PingMs != 84 || r.Reason != "" {
		t.Errorf("hy2 = %+v", r)
	}
	if r := relays["p-awg"]; r.Up || r.Reason != RelayTimeout || r.Proto != "wireguard" {
		t.Errorf("awg = %+v", r)
	}
}

func TestTesterRelayScreenDrawsTheWindowAsHistory(t *testing.T) {
	store := NewMemTesterStore()
	mux := http.NewServeMux()
	RegisterTesterAdminRoutes(mux, store, func(h http.HandlerFunc) http.HandlerFunc { return h })
	lines := []string{
		"[diag:reach] control Ethernet flows=12/12 med=20ms",
		"[diag:reach] p-a type=vless proto=vless-xhttp ping=fail(503 x) flows[Ethernet]=0/12 med=-",
		"[diag:reach] p-b type=hysteria2 proto=hysteria2 ping=80ms",
	}
	if err := store.AppendChunk(context.Background(), TesterChunkRequest{InstallID: "t-1", Lines: lines}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/tester-relays?window=7d", nil))
	var got TesterRelayScreen
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.BucketSeconds != 21600 || len(got.Buckets) != 28 {
		t.Fatalf("buckets = %d of %d s", len(got.Buckets), got.BucketSeconds)
	}
	if len(got.Protocols) != 2 || got.Protocols[0].Proto != "hysteria2" || got.Protocols[0].SharePct != 100 {
		t.Fatalf("protocols = %+v", got.Protocols)
	}
	if p50 := got.Protocols[0].P50Ms; p50 == nil || *p50 != 80 {
		t.Fatalf("p50 = %v", p50)
	}
	if got.Protocols[1].P50Ms != nil || got.Protocols[1].OkPct != 0 {
		t.Fatalf("xhttp = %+v", got.Protocols[1])
	}
	lastUp := func(key string) *float64 {
		for _, s := range got.ClientsByUp {
			if s.Key == key {
				return s.Values[len(s.Values)-1]
			}
		}
		return nil
	}
	if v := lastUp("one"); v == nil || *v != 100 {
		t.Fatalf("one relay up = %v", v)
	}
	if got.ClientsByUp[0].Values[0] != nil {
		t.Fatal("a bucket nobody measured must stay empty")
	}
	if len(got.Failures) != 1 || got.Failures[0].Key != RelayTCPBlocked {
		t.Fatalf("failures = %+v", got.Failures)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/tester-relays?window=30d", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("30d: got %d, the log is kept 7 days", rec.Code)
	}
}

func TestPercentileIsNearestRank(t *testing.T) {
	pings := []int{70, 84}
	if got := percentile(pings, 0.5); got == nil || *got != 70 {
		t.Fatalf("p50 = %v", got)
	}
	if got := percentile(pings, 0.95); got == nil || *got != 84 {
		t.Fatalf("p95 = %v", got)
	}
	if percentile(nil, 0.5) != nil {
		t.Fatal("no samples must stay unknown")
	}
}

func TestTesterRelaysFullFlowsMeanTheProtocolIsCut(t *testing.T) {
	open, total := 12, 12
	if got := relayFailure("503", &open, total, true); got != RelayProtocol {
		t.Fatalf("got %q", got)
	}
	// A dead control says nothing about the relay: the card itself has no way out.
	open = 0
	if got := relayFailure("503", &open, total, false); got != RelayError {
		t.Fatalf("got %q", got)
	}
}
