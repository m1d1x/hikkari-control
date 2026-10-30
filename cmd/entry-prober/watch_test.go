package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// An empty pool is the one round outcome that used to write nothing: the hub
// drains a box for a canary rollout, no relay is `alive`, and the vantage went
// quiet with a live process. From the outside that is the same picture as a
// hung prober — which is how a 15-minute blind spell was read as a hang on the
// stand (24.10). The eye has to say that it cannot see.
func TestAnEmptyPoolIsSaidOutLoudAndOnlyOnce(t *testing.T) {
	var mu sync.Mutex
	relays := "[]"
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/relays") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mu.Lock()
		body := relays
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer hub.Close()

	var out bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&out)
	// Restored even if an assertion below fails: a test that leaves the
	// package's logger pointed at its own buffer poisons every later test.
	defer log.SetOutput(prev)

	cfg := config{
		tunnelURL: hub.URL + "/healthz",
		hub: hubClient{
			base: hub.URL,
			http: &http.Client{Timeout: 2 * time.Second},
		},
		interval: 20 * time.Millisecond,
		timeout:  time.Second,
		// No xray is needed: an empty pool never starts an engine, and the
		// non-empty round below is only read for the "again" line.
		xrayBin:   "/nonexistent/xray",
		socksBase: 39800,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cfg.watchPool(ctx)
		close(done)
	}()

	// Several rounds of the empty pool, then a relay comes back.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	relays = `[{"name":"t1","address":"192.0.2.1","port":443,"cover":"example.net","publicKey":"k","shortId":"s","uuid":"u"}]`
	mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	logged := out.String()
	if n := strings.Count(logged, "pool is empty"); n != 1 {
		t.Fatalf("the empty pool must be reported once per spell, not %d times:\n%s", n, logged)
	}
	if !strings.Contains(logged, "pool has 1 relay(s) again") {
		t.Fatalf("the return of a relay must be reported too:\n%s", logged)
	}
}
