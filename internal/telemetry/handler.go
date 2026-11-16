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
	// tunnel is the period of a "tunnel" event, read out of its checked fields.
	tunnel *TunnelPeriod
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

// TunnelPeriod is one "tunnel" event: a stretch of an install's tunnel being
// on, as minutes by state and the stalls its core timed. It is all the event
// carries; the row has no column for anything else.
type TunnelPeriod struct {
	Install string
	// At is the period's first minute.
	At time.Time
	// Minutes by state: something came back, idle, asked and unanswered, the
	// core down, the user's own network down.
	Flowing, Idle, Down, CoreDown, NetDown int
	// StallMinutes is how many of the minutes had their stalls measured.
	StallMinutes int
	// Stalls of at least 300, 1000 and 3000 ms: how many, and their length.
	N300, N1000, N3000    int
	MS300, MS1000, MS3000 int64
}

// TunnelSink stores the periods the main metric is drawn from. Writing the
// same period twice must leave one row: a client resends a batch the hub
// answered 503 to.
type TunnelSink interface {
	InsertTunnelPeriods(ctx context.Context, periods []TunnelPeriod) error
}

// Server is POST /v1/events.
type Server struct {
	Sink Sink
	// Tunnel takes the "tunnel" events next to the events table; nil keeps
	// them in the events table only.
	Tunnel TunnelSink
	Now    func() time.Time

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
		// The periods first: writing them again is harmless, so a failure
		// after them costs nothing when the client sends the batch again.
		if periods := tunnelPeriods(events); len(periods) > 0 && s.Tunnel != nil {
			if err := s.Tunnel.InsertTunnelPeriods(r.Context(), periods); err != nil {
				log.Printf("telemetry: store %d tunnel periods: %v", len(periods), err)
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
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
		row := EventRow{TS: clamp(e.TS, now), Install: env.Install, App: env.App, Name: e.Name, Fields: fields, Engine: engine}
		if e.Name == tunnelEvent {
			// A period is kept under its own minute: one the clock cannot
			// vouch for would be drawn where it did not happen.
			p, ok := tunnelPeriod(env.Install, e, now)
			if !ok {
				skipped++
				continue
			}
			row.tunnel = &p
		}
		events = append(events, row)
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

const tunnelEvent = "tunnel"

// tunnelPeriod reads a checked "tunnel" event into a period. It takes each
// number by the name the contract gives it and nothing else. An event without
// a minute, with more measured minutes than minutes, or timed outside what the
// hub accepts is no period.
func tunnelPeriod(install string, e inEvent, now time.Time) (TunnelPeriod, bool) {
	num := func(key string) int64 {
		v, _ := e.Fields[key].(float64)
		return int64(v)
	}
	at := time.Unix(e.TS, 0).UTC()
	if e.TS%60 != 0 || at.After(now.Add(clockSkew)) || at.Before(now.Add(-maxAge)) {
		return TunnelPeriod{}, false
	}
	p := TunnelPeriod{
		Install: install, At: at,
		Flowing: int(num("o")), Idle: int(num("i")), Down: int(num("d")), CoreDown: int(num("c")), NetDown: int(num("n")),
		StallMinutes: int(num("stall_min")),
		N300:         int(num("s300")), N1000: int(num("s1000")), N3000: int(num("s3000")),
		MS300: num("s300_ms"), MS1000: num("s1000_ms"), MS3000: num("s3000_ms"),
	}
	minutes := p.Flowing + p.Idle + p.Down + p.CoreDown + p.NetDown
	if minutes == 0 || minutes > int(tunnelMinutes.max) || p.StallMinutes > minutes {
		return TunnelPeriod{}, false
	}
	if p.StallMinutes == 0 && p.N300+p.N1000+p.N3000 > 0 {
		return TunnelPeriod{}, false
	}
	return p, true
}

func tunnelPeriods(events []EventRow) []TunnelPeriod {
	var out []TunnelPeriod
	for _, e := range events {
		if e.tunnel != nil {
			out = append(out, *e.tunnel)
		}
	}
	return out
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
