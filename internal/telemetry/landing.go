package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/httputil"
)

const (
	maxLandingBody   = 2 << 10
	downloadsPerHour = 60
)

// LandingSink receives download button clicks from the marketing landing.
type LandingSink interface {
	InsertLandingDownload(ctx context.Context, ts time.Time, os, file string) error
}

// LandingServer receives download button clicks reported by the landing.
type LandingServer struct {
	Sink    LandingSink
	Origins []string
	Now     func() time.Time

	mu     sync.Mutex
	window time.Time
	counts map[string]int
}

func NewLandingServer(sink LandingSink, origins []string) *LandingServer {
	return &LandingServer{
		Sink:    sink,
		Origins: origins,
		counts:  map[string]int{},
	}
}

func (s *LandingServer) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/landing/download", s.cors(s.handleDownload))
	mux.HandleFunc("OPTIONS /v1/landing/download", s.cors(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (s *LandingServer) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && slices.Contains(s.Origins, o) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func (s *LandingServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OS   string `json:"os"`
		File string `json:"file"`
		TS   int64  `json:"ts"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLandingBody)).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	in.OS = strings.ToLower(strings.TrimSpace(in.OS))
	in.File = strings.ToLower(strings.TrimSpace(in.File))
	if in.OS == "" || !oses[in.OS] {
		http.Error(w, "bad os", http.StatusBadRequest)
		return
	}
	if len(in.File) > 16 {
		http.Error(w, "bad file", http.StatusBadRequest)
		return
	}
	if !s.allow(httputil.ClientIP(r)) {
		w.Header().Set("Retry-After", "600")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	}
	now := s.now()
	ts := now
	if in.TS > 0 {
		t := time.Unix(in.TS, 0).UTC()
		if !t.After(now.Add(clockSkew)) && !t.Before(now.Add(-24*time.Hour)) {
			ts = t
		}
	}
	if s.Sink != nil {
		if err := s.Sink.InsertLandingDownload(r.Context(), ts, in.OS, in.File); err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *LandingServer) allow(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Sub(s.window) >= time.Hour {
		s.window, s.counts = now, map[string]int{}
	}
	n, seen := s.counts[addr]
	if !seen && len(s.counts) >= maxTracked {
		return false
	}
	if n >= downloadsPerHour {
		return false
	}
	s.counts[addr] = n + 1
	return true
}

func (s *LandingServer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}
