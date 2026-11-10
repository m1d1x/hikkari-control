package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixedSource struct{}

func (fixedSource) read() (int64, int64, int64, map[string]pulseKey) { return 0, 0, 0, nil }

type recordedKeys struct {
	mu   sync.Mutex
	sets [][]string
	at   []time.Time
}

func (r *recordedKeys) Set(_ context.Context, keys []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sets = append(r.sets, keys)
	r.at = append(r.at, time.Now())
	return nil
}

// A restarted edge asks the hub for its keys at once, and again soon while
// the hub has not answered, instead of a whole pulse interval later.
func TestFirstPulseBringsTheKeysAtOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable) // the hub missed the first one
			return
		}
		_ = json.NewEncoder(w).Encode(map[string][]string{"keys": {"k1", "k2"}})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keys := &recordedKeys{}
	start := time.Now()
	spawnPulse(ctx, fixedSource{}, pulseConfig{url: srv.URL, token: "t", relay: "r"}, keys)
	deadline := time.Now().Add(pulseBootRetry + 3*time.Second)
	for time.Now().Before(deadline) {
		keys.mu.Lock()
		n := len(keys.sets)
		keys.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	keys.mu.Lock()
	defer keys.mu.Unlock()
	if len(keys.sets) != 1 || len(keys.sets[0]) != 2 {
		t.Fatalf("keys %v after %d pulses", keys.sets, calls.Load())
	}
	if took := keys.at[0].Sub(start); took > pulseBootRetry+time.Second {
		t.Fatalf("keys arrived %v after the start", took)
	}
	// Keyed: the next pulse waits the full interval.
	time.Sleep(500 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d pulses, want 2 before the interval", n)
	}
}

// legKeys is a relay that keeps the trunk exit's rows and takes the testers'
// keys, as naiveRuntime does.
type legKeys struct {
	recordedKeys
	observed []string
	legs     []legRow
	sent     [][]legRow
}

func (l *legKeys) read() (int64, int64, int64, map[string]pulseKey) { return 0, 0, 0, nil }
func (l *legKeys) setObserved(keys []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observed = keys
}
func (l *legKeys) takeLegs() []legRow {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.legs
	l.legs = nil
	return out
}
func (l *legKeys) legsSent(rows []legRow, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ok {
		l.sent = append(l.sent, rows)
	}
}

// A pulse carries the exit's rows of testers' streams, and its answer names
// the testers' keys to observe.
func TestPulseCarriesLegsAndTakesObserved(t *testing.T) {
	got := make(chan []legRow, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body pulseBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body.Legs
		_ = json.NewEncoder(w).Encode(map[string][]string{"keys": {"k1", "k2"}, "observe": {"k2"}})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay := &legKeys{legs: []legRow{{ID: "14c4c8b037d9dc9e", Side: "exit", Kind: "stream", Phase: "mark", Key: "k2"}}}
	spawnPulse(ctx, relay, pulseConfig{url: srv.URL, token: "t", relay: "r"}, relay)
	select {
	case rows := <-got:
		if len(rows) != 1 || rows[0].Key != "k2" {
			t.Fatalf("legs in the pulse %+v", rows)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no pulse")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		relay.mu.Lock()
		done := len(relay.observed) == 1 && len(relay.sent) == 1
		relay.mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("observed %v, delivered %v", relay.observed, relay.sent)
}
