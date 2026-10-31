package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeSink struct {
	events []EventRow
	errs   []ErrorRow
	fail   error
}

func (f *fakeSink) InsertTelemetry(_ context.Context, ev []EventRow, er []ErrorRow) error {
	if f.fail != nil {
		return f.fail
	}
	f.events = append(f.events, ev...)
	f.errs = append(f.errs, er...)
	return nil
}

const install = "t-0123456789abcdef0123456789abcdef"

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func post(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler()(w, httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body)))
	return w
}

func batch(items string) string {
	return `{"v":1,"install":"` + install + `","app":{"version":"1.0.4","channel":"stable","os":"macos","os_major":"26","arch":"arm64"},` + items + `}`
}

func TestAValidBatchIsStoredCanonically(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{Sink: sink, Now: func() time.Time { return now }}
	ts := now.Add(-time.Hour).Unix()
	w := post(t, s, batch(`"events":[
		{"ts":`+itoa(ts)+`,"name":"install"},
		{"ts":`+itoa(ts)+`,"name":"connect","fields":{"proto":"vless-reality","engine":"xray","ok":false,"secs":4,"error":"engine.start"}}
	],"errors":[{"ts":`+itoa(ts)+`,"code":"engine.start","component":"xray","fp":"0011223344556677","msg":"dial <ip> failed","count":2}]`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(sink.events) != 2 || len(sink.errs) != 1 {
		t.Fatalf("stored %d events, %d errors", len(sink.events), len(sink.errs))
	}
	c := sink.events[1]
	if c.Fields != `{"engine":"xray","error":"engine.start","ok":false,"proto":"vless-reality","secs":4}` || c.Engine != "xray" {
		t.Fatalf("connect row = %+v", c)
	}
	if sink.events[0].Fields != "{}" || !sink.events[0].TS.Equal(time.Unix(ts, 0)) {
		t.Fatalf("install row = %+v", sink.events[0])
	}
	if sink.errs[0].Count != 2 || sink.errs[0].App.OS != "macos" {
		t.Fatalf("error row = %+v", sink.errs[0])
	}
}

func TestBadEnvelopesAreRefusedWhole(t *testing.T) {
	s := &Server{Sink: &fakeSink{}, Now: func() time.Time { return now }}
	ev := `"events":[{"ts":1,"name":"install"}]`
	bad := []string{
		`not json`,
		strings.Replace(batch(ev), `"v":1`, `"v":2`, 1),
		strings.Replace(batch(ev), install, "r-123", 1),
		strings.Replace(batch(ev), `"os":"macos"`, `"os":"203.0.113.7"`, 1),
		strings.Replace(batch(ev), `"version":"1.0.4"`, `"version":"user@host"`, 1),
		batch(`"events":[]`),
		batch(`"events":[` + strings.TrimSuffix(strings.Repeat(`{"ts":1,"name":"install"},`, MaxItems+1), ",") + `]`),
	}
	for _, b := range bad {
		if w := post(t, s, b); w.Code != http.StatusBadRequest {
			t.Errorf("status %d for %.80s", w.Code, b)
		}
	}
	big := batch(`"events":[{"ts":1,"name":"install","pad":"` + strings.Repeat("x", MaxBody) + `"}]`)
	if w := post(t, s, big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status %d", w.Code)
	}
}

func TestUnknownItemsAreSkippedNotRefused(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{Sink: sink, Now: func() time.Time { return now }}
	w := post(t, s, batch(`"events":[
		{"ts":1,"name":"launch","fields":{"cold":true}},
		{"ts":1,"name":"from_the_future"},
		{"ts":1,"name":"ui_open","fields":{"screen":"logs","url":"https://example.org"}},
		{"ts":1,"name":"connect","fields":{"engine":"wireguard"}},
		{"ts":1,"name":"connect","fields":{"secs":-3}}
	],"errors":[
		{"ts":1,"code":"made.up","component":"ui","fp":"0011223344556677","msg":"x","count":1},
		{"ts":1,"code":"ui.exception","component":"ui","fp":"zz","msg":"x","count":1},
		{"ts":1,"code":"ui.exception","component":"ui","fp":"0011223344556677","msg":"`+strings.Repeat("я", msgMaxRunes+1)+`","count":1}
	]`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if len(sink.events) != 1 || len(sink.errs) != 0 || s.Skipped() != 7 {
		t.Fatalf("stored %d/%d, skipped %d", len(sink.events), len(sink.errs), s.Skipped())
	}
}

func TestClientClocksAreClamped(t *testing.T) {
	sink := &fakeSink{}
	s := &Server{Sink: sink, Now: func() time.Time { return now }}
	future, ancient := now.Add(time.Hour).Unix(), now.Add(-30*24*time.Hour).Unix()
	post(t, s, batch(`"events":[{"ts":`+itoa(future)+`,"name":"install"},{"ts":`+itoa(ancient)+`,"name":"install"}]`))
	for _, e := range sink.events {
		if !e.TS.Equal(now) {
			t.Fatalf("ts = %v, want arrival time", e.TS)
		}
	}
}

func TestEachInstallIsRateLimited(t *testing.T) {
	s := &Server{Sink: &fakeSink{}, Now: func() time.Time { return now }}
	b := batch(`"events":[{"ts":1,"name":"install"}]`)
	for i := 0; i < PerInstallPerHour; i++ {
		if w := post(t, s, b); w.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	w := post(t, s, b)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d", w.Code)
	}
	other := strings.Replace(b, install, "t-ffffffffffffffffffffffffffffffff", 1)
	if w := post(t, s, other); w.Code != http.StatusNoContent {
		t.Fatalf("another install was limited: %d", w.Code)
	}
	now := now.Add(time.Hour)
	s.Now = func() time.Time { return now }
	if w := post(t, s, b); w.Code != http.StatusNoContent {
		t.Fatalf("next hour still limited: %d", w.Code)
	}
}

func TestStorageFailureAsksTheClientToRetry(t *testing.T) {
	s := &Server{Sink: &fakeSink{fail: errors.New("clickhouse down")}, Now: func() time.Time { return now }}
	if w := post(t, s, batch(`"events":[{"ts":1,"name":"install"}]`)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
