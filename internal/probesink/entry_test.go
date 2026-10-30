package probesink

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/analytics"
)

type fakeEntryStore struct {
	got []analytics.EntryProbeRecord
	err error
}

func (f *fakeEntryStore) InsertEntryProbes(_ context.Context, recs []analytics.EntryProbeRecord) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, recs...)
	return nil
}

// fakeHub is the entry pool as the ingest sees it: which slot each token
// belongs to, and which relays are dead.
type fakeHub struct {
	slots map[string]string
	dead  map[string]bool
	err   error
}

// testHub knows one observation point: testToken is ru-spb's.
func testHub() fakeHub { return fakeHub{slots: map[string]string{testToken: "ru-spb"}} }

func (f fakeHub) VantageByToken(_ context.Context, token string) (string, error) {
	if slot, ok := f.slots[token]; ok {
		return slot, nil
	}
	return "", errors.New("no such vantage")
}

func (f fakeHub) DeadRelays(_ context.Context, names []string) (map[string]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]bool{}
	for _, n := range names {
		if f.dead[n] {
			out[n] = true
		}
	}
	return out, nil
}

const testToken = "test-ingest-token"

func doEntryWith(t *testing.T, store entryProbeStore, hub entryHub, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-probes", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rr := httptest.NewRecorder()
	IngestEntry(store, hub).ServeHTTP(rr, req)
	return rr
}

// doEntry posts body as ru-spb (auth is the header to send).
func doEntry(t *testing.T, store entryProbeStore, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doEntryWith(t, store, testHub(), auth, body)
}

func TestIngestEntryFeatureOff(t *testing.T) {
	// No pool wired = feature off, regardless of a valid-looking request.
	rr := doEntryWith(t, &fakeEntryStore{}, nil, "Bearer "+testToken, `{"probes":[]}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no pool: got %d, want 503", rr.Code)
	}
}

func TestIngestEntryAuth(t *testing.T) {
	cases := []struct {
		name string
		auth string
	}{
		{"no header", ""},
		{"wrong scheme", "Token " + testToken},
		{"wrong token", "Bearer nope"},
		{"empty bearer", "Bearer "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doEntry(t, &fakeEntryStore{}, c.auth, `{"probes":[{"sourceRegion":"ru-spb","relayName":"ams-1","verdict":"clean"}]}`)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", rr.Code)
			}
		})
	}
}

func TestIngestEntryHappyPath(t *testing.T) {
	store := &fakeEntryStore{}
	body := `{"probes":[{
		"sourceRegion":"ru-spb","relayName":"ams1-1",
		"rawTcp":1,"realityOk":1,"realityRttMs":42.5,"realityHttp":200,
		"k1BareTls":1,"k2Control":1,"cover":"www.samsung.com","verdict":"clean","detail":"ok"
	}]}`
	rr := doEntry(t, store, "Bearer "+testToken, body)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rr.Code)
	}
	if len(store.got) != 1 {
		t.Fatalf("store received %d records, want 1", len(store.got))
	}
	r := store.got[0]
	if r.SourceRegion != "ru-spb" || r.RelayName != "ams1-1" {
		t.Fatalf("identity fields wrong: %+v", r)
	}
	if r.RawTCP != 1 || r.RealityOK != 1 || r.K1BareTLS != 1 || r.K2Control != 1 {
		t.Fatalf("signal flags wrong: %+v", r)
	}
	if r.RealityRTTMs != 42.5 || r.RealityHTTP != 200 {
		t.Fatalf("reality metrics wrong: %+v", r)
	}
	if r.Cover != "www.samsung.com" || r.Verdict != "clean" || r.Detail != "ok" {
		t.Fatalf("verdict fields wrong: %+v", r)
	}
}

func TestIngestEntryEmptyBatch(t *testing.T) {
	store := &fakeEntryStore{}
	rr := doEntry(t, store, "Bearer "+testToken, `{"probes":[]}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204", rr.Code)
	}
	if len(store.got) != 0 {
		t.Fatalf("store should not be touched on empty batch, got %d", len(store.got))
	}
}

func TestIngestEntryBadRequests(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"invalid json", `{not json`},
		{"missing relay name", `{"probes":[{"sourceRegion":"ru-spb","verdict":"clean"}]}`},
		{"bad label with space", `{"probes":[{"relayName":"ams 1","verdict":"clean"}]}`},
		{"bad verdict", `{"probes":[{"sourceRegion":"ru-spb","relayName":"ams-1","verdict":"melted"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeEntryStore{}
			rr := doEntry(t, store, "Bearer "+testToken, c.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", rr.Code)
			}
			if len(store.got) != 0 {
				t.Fatalf("a rejected batch must not reach the store")
			}
		})
	}
}

func TestIngestEntryBatchTooLarge(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"probes":[`)
	for i := 0; i <= maxProbesPerBatch; i++ { // one over the cap
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"sourceRegion":"ru-spb","relayName":"r%d","verdict":"clean"}`, i)
	}
	sb.WriteString(`]}`)
	rr := doEntry(t, &fakeEntryStore{}, "Bearer "+testToken, sb.String())
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d, want 413", rr.Code)
	}
}

func TestIngestEntryStoreError(t *testing.T) {
	store := &fakeEntryStore{err: fmt.Errorf("clickhouse down")}
	rr := doEntry(t, store, "Bearer "+testToken, `{"probes":[{"sourceRegion":"ru-spb","relayName":"ams-1","verdict":"clean"}]}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rr.Code)
	}
}

// Guard: the empty-verdict case normalises to a stored empty string, which the
// analytics layer coerces to "unknown". Keep the handler lenient here so a
// prober that could not classify (e.g. a crash mid-round) is still recorded.
func TestIngestEntryEmptyVerdictAccepted(t *testing.T) {
	store := &fakeEntryStore{}
	rr := doEntry(t, store, "Bearer "+testToken, `{"probes":[{"sourceRegion":"ru-spb","relayName":"ams-1","verdict":""}]}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rr.Code)
	}
	if len(store.got) != 1 || store.got[0].Verdict != "" {
		t.Fatalf("empty verdict should pass through as empty, got %+v", store.got)
	}
}

// A machine handed back mid-round is probed after its instance is gone, and
// reads as banned from everywhere. That sample must not reach the dataset, or
// every manual hand-back is a ban on the detection screen.
func TestIngestEntryDropsDeadRelays(t *testing.T) {
	store := &fakeEntryStore{}
	body := `{"probes":[
		{"sourceRegion":"ru-spb","relayName":"ams2-2","verdict":"ip_banned"},
		{"sourceRegion":"ru-spb","relayName":"ams3-1","verdict":"clean"}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-probes", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	IngestEntry(store, fakeHub{slots: testHub().slots, dead: map[string]bool{"ams2-2": true}}).ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rr.Code)
	}
	if len(store.got) != 1 || store.got[0].RelayName != "ams3-1" {
		t.Fatalf("store got %+v, want only ams3-1", store.got)
	}
}

func TestIngestEntryAllDeadTouchesNothing(t *testing.T) {
	store := &fakeEntryStore{err: fmt.Errorf("must not be called")}
	body := `{"probes":[{"sourceRegion":"ru-spb","relayName":"ams2-1","verdict":"ip_banned"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-probes", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	IngestEntry(store, fakeHub{slots: testHub().slots, dead: map[string]bool{"ams2-1": true}}).ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rr.Code)
	}
}

func TestIngestEntryPoolErrorIsNotSwallowed(t *testing.T) {
	store := &fakeEntryStore{}
	body := `{"probes":[{"sourceRegion":"ru-spb","relayName":"ams2-1","verdict":"clean"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/entry-probes", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	IngestEntry(store, fakeHub{slots: testHub().slots, err: fmt.Errorf("db down")}).ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError || len(store.got) != 0 {
		t.Fatalf("got %d with %d rows, want 500 and nothing stored", rr.Code, len(store.got))
	}
}
