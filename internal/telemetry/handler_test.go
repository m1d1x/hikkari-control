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

type fakeTunnel struct {
	periods []TunnelPeriod
	fail    error
}

func (f *fakeTunnel) InsertTunnelPeriods(_ context.Context, p []TunnelPeriod) error {
	if f.fail != nil {
		return f.fail
	}
	f.periods = append(f.periods, p...)
	return nil
}

func TestATunnelEventIsKeptAsItsNumbersAndNothingElse(t *testing.T) {
	sink, tunnel := &fakeSink{}, &fakeTunnel{}
	s := &Server{Sink: sink, Tunnel: tunnel, Now: func() time.Time { return now }}
	ts := now.Add(-20 * time.Minute).Unix()
	w := post(t, s, batch(`"events":[{"ts":`+itoa(ts)+`,"name":"tunnel","fields":{"o":7,"d":2,"n":1,"stall_min":9,"s300":4,"s300_ms":2600,"s1000":1,"s1000_ms":1400}}]`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	want := TunnelPeriod{
		Install: install, At: time.Unix(ts, 0).UTC(),
		Flowing: 7, Down: 2, NetDown: 1, StallMinutes: 9, N300: 4, MS300: 2600, N1000: 1, MS1000: 1400,
	}
	if len(tunnel.periods) != 1 || tunnel.periods[0] != want {
		t.Fatalf("periods = %+v\nwant      %+v", tunnel.periods, want)
	}
	if len(sink.events) != 1 || sink.events[0].Fields != `{"d":2,"n":1,"o":7,"s1000":1,"s1000_ms":1400,"s300":4,"s300_ms":2600,"stall_min":9}` {
		t.Fatalf("events = %+v", sink.events)
	}
}

// An event that carries anything the contract does not name is not trimmed
// down to its numbers: it is dropped whole, in both stores. What the hub keeps
// of a tunnel event can only be what docs/TELEMETRY.md §4a lists.
func TestATunnelEventWithAnythingExtraStoresNothing(t *testing.T) {
	ts := itoa(now.Add(-20 * time.Minute).Unix())
	for name, fields := range map[string]string{
		"a profile tag":        `{"o":7,"profile":"p-002-3"}`,
		"a network":            `{"o":7,"isp":"AS8359"}`,
		"a destination":        `{"o":7,"dst":"example.org"}`,
		"an address":           `{"o":7,"addr":"203.0.113.7"}`,
		"a nested object":      `{"o":7,"extra":{"host":"example.org"}}`,
		"text for a number":    `{"o":"example.org"}`,
		"a fraction":           `{"o":7.5}`,
		"a negative":           `{"o":7,"d":-1}`,
		"minutes past a day":   `{"o":1441}`,
		"no minutes":           `{"stall_min":0}`,
		"stalls without timed": `{"o":7,"s300":2,"s300_ms":900}`,
		"more timed than on":   `{"o":2,"stall_min":3}`,
	} {
		sink, tunnel := &fakeSink{}, &fakeTunnel{}
		s := &Server{Sink: sink, Tunnel: tunnel, Now: func() time.Time { return now }}
		w := post(t, s, batch(`"events":[{"ts":`+ts+`,"name":"tunnel","fields":`+fields+`}]`))
		if w.Code != http.StatusNoContent {
			t.Errorf("%s: status %d: %s", name, w.Code, w.Body)
		}
		if len(tunnel.periods) != 0 || len(sink.events) != 0 {
			t.Errorf("%s: stored periods %+v, events %+v", name, tunnel.periods, sink.events)
		}
		if s.Skipped() != 1 {
			t.Errorf("%s: skipped = %d", name, s.Skipped())
		}
	}
}

func TestATunnelEventOutsideTheHubsClockIsNoPeriod(t *testing.T) {
	for name, ts := range map[string]int64{
		"the future":      now.Add(time.Hour).Unix(),
		"older than kept": now.Add(-9 * 24 * time.Hour).Unix(),
		"not on a minute": now.Add(-20*time.Minute).Unix() + 7,
	} {
		sink, tunnel := &fakeSink{}, &fakeTunnel{}
		s := &Server{Sink: sink, Tunnel: tunnel, Now: func() time.Time { return now }}
		if w := post(t, s, batch(`"events":[{"ts":`+itoa(ts)+`,"name":"tunnel","fields":{"o":7}}]`)); w.Code != http.StatusNoContent {
			t.Errorf("%s: status %d", name, w.Code)
		}
		if len(tunnel.periods) != 0 || len(sink.events) != 0 {
			t.Errorf("%s: stored periods %+v, events %+v", name, tunnel.periods, sink.events)
		}
	}
}

func TestAFailedPeriodStoreAsksTheClientToRetryAndWritesNoEvent(t *testing.T) {
	sink, tunnel := &fakeSink{}, &fakeTunnel{fail: errors.New("postgres down")}
	s := &Server{Sink: sink, Tunnel: tunnel, Now: func() time.Time { return now }}
	w := post(t, s, batch(`"events":[{"ts":`+itoa(now.Add(-20*time.Minute).Unix())+`,"name":"tunnel","fields":{"o":7}}]`))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
	if len(sink.events) != 0 {
		t.Fatalf("events written before the periods: %+v", sink.events)
	}
}
