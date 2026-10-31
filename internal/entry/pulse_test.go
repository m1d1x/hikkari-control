package entry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pulseRecorder keeps what RecordPulse handed the dataset.
type pulseRecorder struct {
	rows []PulseRecord
}

func (p *pulseRecorder) InsertEntryRelayStats(_ context.Context, rows []PulseRecord) error {
	p.rows = append(p.rows, rows...)
	return nil
}

// A relay's protocol is the protocol of its box, never what the relay claims:
// a box that misreported itself would put its traffic in another protocol's
// column on the console.
func TestPulseTakesTheProtocolFromTheBoxNotFromTheRelay(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)

	rec := &pulseRecorder{}
	h.svc.SetPulseSink(rec)

	if err := h.svc.RecordPulse(context.Background(), "s", PulseToken("s", box), PulseReport{
		Relay: relay, UptimeS: 900, BytesUp: 1 << 20, BytesDown: 4 << 20, Conns: 12,
		CPUPct: 7.5, MemMB: 310, Version: "4f21a9bbccdd",
	}); err != nil {
		t.Fatalf("record pulse: %v", err)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("sink took %d rows, want 1", len(rec.rows))
	}
	got := rec.rows[0]
	if got.Proto == "" {
		t.Fatal("the row carries no protocol: the console cannot column it")
	}
	if got.Relay != relay || got.BytesUp != 1<<20 || got.BytesDown != 4<<20 || got.Conns != 12 {
		t.Fatalf("pulse stored as %+v", got)
	}
}

// A pulse from a name the pool does not hold is refused, not swallowed: it
// comes from a machine we provisioned and handed a token to, so an unknown
// name means the deployment is wrong and the box's log must say so.
func TestPulseFromAnUnknownRelayIsRefused(t *testing.T) {
	h := newHarness(t)
	rec := &pulseRecorder{}
	h.svc.SetPulseSink(rec)

	err := h.svc.RecordPulse(context.Background(), "s", PulseToken("s", "nosuch1"), PulseReport{Relay: "nosuch1-9", UptimeS: 10})
	if err == nil {
		t.Fatal("a pulse under an unknown name was accepted")
	}
	if len(rec.rows) != 0 {
		t.Fatalf("the dataset took %d rows for an unknown relay", len(rec.rows))
	}

	// A malformed name never reaches the database at all.
	if err := h.svc.RecordPulse(context.Background(), "s", PulseToken("s", "x"), PulseReport{Relay: "NOT A NAME"}); err == nil {
		t.Fatal("a malformed relay name was accepted")
	}
}

// Everything a box controls is clamped before the dataset sees it, so a query
// over entry_relay_stats never has to defend itself against one wrong machine.
func TestPulseClampsWhatABoxClaims(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	rec := &pulseRecorder{}
	h.svc.SetPulseSink(rec)

	if err := h.svc.RecordPulse(context.Background(), "s", PulseToken("s", box), PulseReport{
		Relay: relay, UptimeS: -5, BytesUp: -1, BytesDown: -1, Conns: -1,
		CPUPct: 9000, MemMB: -1, Version: strings.Repeat("x", 500),
	}); err != nil {
		t.Fatalf("record pulse: %v", err)
	}
	got := rec.rows[0]
	if got.UptimeS != 0 || got.BytesUp != 0 || got.BytesDown != 0 || got.Conns != 0 || got.MemMB != 0 {
		t.Errorf("negative counters survived: %+v", got)
	}
	if got.CPUPct != 100 {
		t.Errorf("cpu = %v, want clamped to 100", got.CPUPct)
	}
	if len(got.Version) > maxPulseVersion {
		t.Errorf("version is %d bytes, want at most %d", len(got.Version), maxPulseVersion)
	}
}

// A hub without analytics keeps taking pulses: the boxes' loop stays healthy
// and only the console's load figures go missing.
func TestPulseWithoutASinkStillAccepts(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.svc.SetPulseSink(nil)

	if err := h.svc.RecordPulse(context.Background(), "s", PulseToken("s", box), PulseReport{Relay: relay, UptimeS: 30}); err != nil {
		t.Fatalf("a hub without analytics must still accept a pulse: %v", err)
	}
}

// The ingest is its own token, and an unset one switches the surface off
// rather than taking anonymous counters from anywhere.
func TestPulseRouteNeedsItsOwnBearerAndIsOffWhenUnset(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.svc.SetPulseSink(&pulseRecorder{})

	body := func() *strings.Reader {
		raw, _ := json.Marshal(pulseBody{Relay: relay, UptimeS: 60, BytesUp: 10, BytesDown: 20})
		return strings.NewReader(string(raw))
	}

	off := http.NewServeMux()
	RegisterPulseRoutes(off, h.svc, "")
	rec := httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-pulse", body()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no token configured: status %d, want 503", rec.Code)
	}

	on := http.NewServeMux()
	RegisterPulseRoutes(on, h.svc, "right-token")
	right := PulseToken("right-token", box)

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-pulse", body())
	req.Header.Set("Authorization", "Bearer wrong-token")
	on.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-pulse", body())
	req.Header.Set("Authorization", "Bearer "+right)
	on.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("right token: status %d (%s), want 204", rec.Code, rec.Body.String())
	}
}

// An unknown relay is refused on the wire too, so the box's own log shows the
// deployment went wrong instead of reporting into a void — with the same 401
// as a wrong token, so the answer is no oracle of which names exist.
func TestPulseRouteRefusesAnUnknownRelay(t *testing.T) {
	h := newHarness(t)
	h.svc.SetPulseSink(&pulseRecorder{})

	mux := http.NewServeMux()
	RegisterPulseRoutes(mux, h.svc, "tok")
	raw, _ := json.Marshal(pulseBody{Relay: "ghost1-1", UptimeS: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-pulse", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+PulseToken("tok", "ghost1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 for a relay the pool does not hold", rec.Code)
	}
}

// The interval the two halves agree on is a contract, not a coincidence: the
// console reads silence against it.
func TestPulseIntervalIsTheOneTheEdgeUses(t *testing.T) {
	if PulseInterval != 30*time.Second {
		t.Fatalf("PulseInterval = %v; edge/pulse.go pulseEvery must match", PulseInterval)
	}
}

// One token per machine (audit 42 Н1). A single EDGE_PULSE_TOKEN on every box
// let any seized machine pulse under every relay name in the pool — dead ones
// included — and the 404/204 split told it which names exist. A box now holds
// a token of its own, derived from the hub's secret, and a pulse is taken only
// for a live relay of that box; every other refusal looks the same.
func TestAPulseIsTakenOnlyUnderItsOwnMachinesToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	boxA, _ := h.box()
	a := h.relay(boxA, KindPersonal)
	gone := h.relay(boxA, KindPersonal)
	h.admit(gone)
	if _, err := h.svc.MarkDead(ctx, gone, DeathOther); err != nil {
		t.Fatal(err)
	}
	boxB, _ := h.box()
	b := h.relay(boxB, KindPersonal)

	rec := &pulseRecorder{}
	h.svc.SetPulseSink(rec)
	mux := http.NewServeMux()
	RegisterPulseRoutes(mux, h.svc, "hub-pulse-secret")
	send := func(token, relay string) (int, string) {
		raw, _ := json.Marshal(pulseBody{Relay: relay, UptimeS: 60})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-pulse", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	tokA := PulseToken("hub-pulse-secret", boxA)
	if tokA == "" || tokA == "hub-pulse-secret" || tokA == PulseToken("hub-pulse-secret", boxB) {
		t.Fatalf("box tokens: A %q, B %q", tokA, PulseToken("hub-pulse-secret", boxB))
	}
	if code, body := send(tokA, a); code != http.StatusNoContent {
		t.Fatalf("own relay under own token: %d %s", code, body)
	}

	refusals := map[string][2]string{
		"another box's relay":     {tokA, b},
		"the hub's own secret":    {"hub-pulse-secret", a},
		"a dead relay of the box": {tokA, gone},
		"a name nobody holds":     {tokA, "ghost1-1"},
	}
	var first string
	for what, c := range refusals {
		code, body := send(c[0], c[1])
		if code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s, want 401", what, code, body)
		}
		if first == "" {
			first = body
		} else if body != first {
			t.Errorf("%s answers %q, another refusal %q: the difference is an oracle", what, body, first)
		}
	}
	if len(rec.rows) != 1 || rec.rows[0].Relay != a {
		t.Fatalf("rows taken: %+v, want only %s", rec.rows, a)
	}
}

// What a box is given to pulse with is its own token, never the hub's secret.
func TestProvisioningPlantsTheBoxsOwnPulseToken(t *testing.T) {
	ph := newProvHarness(t)
	ph.prov.SetPulse("https://api.example.org/api/v1/internal/entry-pulse", "hub-pulse-secret")
	box, _ := ph.provisioned()

	ph.box.mu.Lock()
	defer ph.box.mu.Unlock()
	var env string
	for _, in := range ph.box.stdins {
		if strings.Contains(in, "EDGE_PULSE_TOKEN=") {
			env = in
		}
	}
	if env == "" {
		t.Fatal("no relay env carried a pulse token")
	}
	if strings.Contains(env, "hub-pulse-secret") || !strings.Contains(env, "EDGE_PULSE_TOKEN="+PulseToken("hub-pulse-secret", box)+"\n") {
		t.Fatalf("relay env:\n%s", env)
	}
}

// relayKeysFake is the subscription side of a pulse.
type relayKeysFake struct {
	keys  []string
	proto string
	boot  string
	usage map[string]KeyUsage
}

func (f *relayKeysFake) KeysOf(_ context.Context, _, proto string) ([]string, error) {
	f.proto = proto
	return f.keys, nil
}

func (f *relayKeysFake) RecordUsage(_ context.Context, _, boot string, usage map[string]KeyUsage) error {
	f.boot, f.usage = boot, usage
	return nil
}

// A pulse files its per-key totals and is answered with the relay's keys, in
// the protocol of its box; without access keys the answer is empty (204), so
// the relay leaves its list alone.
func TestAPulseIsAnsweredWithTheRelaysKeys(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	post := func() *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		RegisterPulseRoutes(mux, h.svc, "s")
		body := `{"relay":"` + relay + `","uptimeS":60,"boot":"00112233445566778899aabbccddeeff","keys":{"k1":{"up":5,"down":7}}}`
		req := httptest.NewRequest("POST", "/api/v1/internal/entry-pulse", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+PulseToken("s", box))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(); rec.Code != http.StatusNoContent {
		t.Fatalf("without keys: %d", rec.Code)
	}

	keys := &relayKeysFake{keys: []string{"k1", "k2"}}
	h.svc.SetRelayKeys(keys)
	rec := post()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var answer pulseAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || len(answer.Keys) != 2 {
		t.Fatalf("answer %s %v", rec.Body, err)
	}
	if keys.proto == "" || keys.boot != "00112233445566778899aabbccddeeff" || keys.usage["k1"] != (KeyUsage{Up: 5, Down: 7}) {
		t.Fatalf("filed %+v", keys)
	}
}
