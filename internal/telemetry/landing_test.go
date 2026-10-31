package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeLandingSink struct {
	downloads []struct {
		ts   time.Time
		os   string
		file string
	}
}

func (f *fakeLandingSink) InsertLandingDownload(_ context.Context, ts time.Time, os, file string) error {
	f.downloads = append(f.downloads, struct {
		ts   time.Time
		os   string
		file string
	}{ts: ts, os: os, file: file})
	return nil
}

func TestLandingServer_CORSAndTracking(t *testing.T) {
	sink := &fakeLandingSink{}
	now := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	srv := NewLandingServer(sink, []string{"https://hikkari.net", "http://localhost:5180"})
	srv.Now = func() time.Time { return now }

	mux := http.NewServeMux()
	srv.Register(mux)

	// Preflight OPTIONS
	req := httptest.NewRequest(http.MethodOptions, "/v1/landing/download", nil)
	req.Header.Set("Origin", "https://hikkari.net")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("options code %d, want 204", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://hikkari.net" {
		t.Fatalf("origin header %q", w.Header().Get("Access-Control-Allow-Origin"))
	}

	// Valid POST
	body := `{"os":"macos","file":".pkg","ts":1793498400}`
	req = httptest.NewRequest(http.MethodPost, "/v1/landing/download", strings.NewReader(body))
	req.Header.Set("Origin", "https://hikkari.net")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("post code %d, want 204, body: %s", w.Code, w.Body.String())
	}
	if len(sink.downloads) != 1 {
		t.Fatalf("recorded %d downloads, want 1", len(sink.downloads))
	}
	d := sink.downloads[0]
	if d.os != "macos" || d.file != ".pkg" {
		t.Fatalf("recorded %+v", d)
	}

	// Bad OS rejected
	req = httptest.NewRequest(http.MethodPost, "/v1/landing/download", strings.NewReader(`{"os":"solaris"}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad os code %d, want 400", w.Code)
	}
}
