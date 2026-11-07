package stand

import (
	"context"
	"fmt"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCapacityMeasuresPayloadWithAHardBudget(t *testing.T) {
	var sent atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		w.Header().Set("Content-Length", strconv.Itoa(n))
		block := make([]byte, 64<<10)
		for n > 0 {
			size := len(block)
			if size > n {
				size = n
			}
			written, err := w.Write(block[:size])
			sent.Add(int64(written))
			if err != nil {
				return
			}
			w.(http.Flusher).Flush()
			n -= written
			time.Sleep(8 * time.Millisecond)
		}
	}))
	defer server.Close()
	got := measureCapacity(context.Background(), server.Client(), server.URL, strings.Repeat("a", 64))
	if err := got.Validate(time.Now()); err != nil {
		t.Fatalf("invalid measurement: %+v %v", got, err)
	}
	if got.Error != "" || got.Mbps <= 0 || got.Limited || sent.Load() > capacity.MaxBytes {
		t.Fatalf("bad measurement or excessive traffic: %+v bytes=%d", got, sent.Load())
	}
}

func TestCapacityKeepsCompletedRoundWhenALargerOneIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		if n > 1<<20 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(n))
		for block := make([]byte, 64<<10); n > 0; n -= len(block) {
			w.Write(block)
			w.(http.Flusher).Flush()
			time.Sleep(2 * time.Millisecond)
		}
	}))
	defer server.Close()
	got := measureCapacity(context.Background(), server.Client(), server.URL, strings.Repeat("a", 64))
	if err := got.Validate(time.Now()); err != nil {
		t.Fatalf("invalid measurement: %+v %v", got, err)
	}
	if got.Error != "" || got.Mbps <= 0 || !got.Limited {
		t.Fatalf("completed round lost after refusal: %+v", got)
	}
}

func TestCapacityRejectsWrongHTTPAndShortBodies(t *testing.T) {
	for _, status := range []int{200, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, "not measurement data")
		}))
		got := measureCapacity(context.Background(), server.Client(), server.URL, strings.Repeat("a", 64))
		server.Close()
		if got.Error == "" || got.Mbps != 0 {
			t.Fatalf("bad response became capacity: %+v", got)
		}
	}
}

func TestLatencyRequiresSuccessfulRoundTrips(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.Close {
			t.Error("latency reused a warm connection")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		time.Sleep(8 * time.Millisecond)
		w.WriteHeader(204)
	}))
	defer server.Close()
	if got := measureLatency(context.Background(), server.Client(), server.URL); got < 8 {
		t.Fatalf("invalid RTT %d", got)
	}
	if calls.Load() != 3 {
		t.Fatal("missing samples")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	if got := measureLatency(context.Background(), bad.Client(), bad.URL); got != 0 {
		t.Fatal("failure became ping")
	}
}
