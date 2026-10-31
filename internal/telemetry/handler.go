package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"
)

// Limits of one request (client: BatchMaxItems 200, BatchMaxBytes 64 KiB).
const (
	MaxBody  = 128 << 10
	MaxItems = 200
	// PerInstallPerHour allows a client to drain a week's offline queue
	// (≤1 MiB in ≤64 KiB batches) within an hour, and not much more.
	PerInstallPerHour = 40
	// maxTracked bounds the limiter's memory; past it, new installs wait for
	// the next window rather than evicting someone mid-window.
	maxTracked = 200_000
	clockSkew  = 5 * time.Minute
	maxAge     = 8 * 24 * time.Hour
)

// App is the build and platform of one batch.
type App struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	OS      string `json:"os"`
	OSMajor string `json:"os_major"`
	Arch    string `json:"arch"`
}

type inEvent struct {
	TS     int64          `json:"ts"`
	Name   string         `json:"name"`
	Fields map[string]any `json:"fields"`
}

type inError struct {
	TS        int64  `json:"ts"`
	Code      string `json:"code"`
	Component string `json:"component"`
	FP        string `json:"fp"`
	Msg       string `json:"msg"`
	Count     int    `json:"count"`
}

type envelope struct {
	V       int       `json:"v"`
	Install string    `json:"install"`
	App     App       `json:"app"`
	Events  []inEvent `json:"events"`
	Errors  []inError `json:"errors"`
}

// EventRow and ErrorRow are what storage receives: checked, canonical, with
// the client's time clamped into a sane window.
type EventRow struct {
	TS      time.Time
	Install string
	App     App
	Name    string
	Fields  string // canonical JSON
	Engine  string // copied out of Fields for the daily rollup
}

type ErrorRow struct {
	TS        time.Time
	Install   string
	App       App
	Code      string
	Component string
	FP        string
	Msg       string
	Count     uint32
}

// Sink stores accepted rows. An error makes the hub answer 503, so the
// client keeps the batch and retries.
type Sink interface {
	InsertTelemetry(ctx context.Context, events []EventRow, errs []ErrorRow) error
}

// Server is POST /v1/events.
type Server struct {
	Sink Sink
	Now  func() time.Time

	mu      sync.Mutex
	window  time.Time
	counts  map[string]int
	skipped uint64
}

// Handler answers 204 when the batch is stored, 400 for an envelope that is
// not v1, 413 for an oversized body, 429 past the per-install rate and 503
// when storage fails. The client drops a batch only on 4xx other than 429.
// The request's address is never read: not for limits, not for logs.
func (s *Server) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		var env envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := checkEnvelope(env); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !s.allow(env.Install) {
			w.Header().Set("Retry-After", "600")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		events, errs, skipped := s.rows(env)
		if skipped > 0 {
			s.mu.Lock()
			s.skipped += uint64(skipped)
			s.mu.Unlock()
		}
		if len(events)+len(errs) > 0 {
			if err := s.Sink.InsertTelemetry(r.Context(), events, errs); err != nil {
				log.Printf("telemetry: store %d events, %d errors: %v", len(events), len(errs), err)
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// Skipped is how many items valid envelopes carried that the hub did not
// know — a sign clients run ahead of the hub.
func (s *Server) Skipped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

func checkEnvelope(env envelope) error {
	switch {
	case env.V != 1:
		return errors.New("unsupported version")
	case !reInstall.MatchString(env.Install):
		return errors.New("bad install")
	case len(env.Events)+len(env.Errors) == 0:
		return errors.New("empty batch")
	case len(env.Events)+len(env.Errors) > MaxItems:
		return errors.New("too many items")
	}
	return checkApp(env.App)
}

func (s *Server) rows(env envelope) ([]EventRow, []ErrorRow, int) {
	now := s.now()
	skipped := 0
	events := make([]EventRow, 0, len(env.Events))
	for _, e := range env.Events {
		fields, ok := checkFields(e.Name, e.Fields)
		if !ok {
			skipped++
			continue
		}
		engine, _ := e.Fields["engine"].(string)
		events = append(events, EventRow{TS: clamp(e.TS, now), Install: env.Install, App: env.App, Name: e.Name, Fields: fields, Engine: engine})
	}
	errs := make([]ErrorRow, 0, len(env.Errors))
	for _, e := range env.Errors {
		if !errorCodes[e.Code] || !components[e.Component] || !reFP.MatchString(e.FP) ||
			e.Count < 1 || !utf8.ValidString(e.Msg) || utf8.RuneCountInString(e.Msg) > msgMaxRunes {
			skipped++
			continue
		}
		errs = append(errs, ErrorRow{TS: clamp(e.TS, now), Install: env.Install, App: env.App, Code: e.Code,
			Component: e.Component, FP: e.FP, Msg: e.Msg, Count: uint32(min(e.Count, 1<<20))})
	}
	return events, errs, skipped
}

// clamp keeps a client's clock from writing into the future or into
// partitions TTL has already dropped; such items take the arrival time.
func clamp(ts int64, now time.Time) time.Time {
	t := time.Unix(ts, 0).UTC()
	if t.After(now.Add(clockSkew)) || t.Before(now.Add(-maxAge)) {
		return now.Truncate(time.Minute)
	}
	return t
}

// allow is a fixed hourly window per install ID.
func (s *Server) allow(install string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := now.Truncate(time.Hour); !w.Equal(s.window) || s.counts == nil {
		s.window = w
		s.counts = map[string]int{}
	}
	n, seen := s.counts[install]
	if !seen && len(s.counts) >= maxTracked {
		return false
	}
	if n >= PerInstallPerHour {
		return false
	}
	s.counts[install] = n + 1
	return true
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
