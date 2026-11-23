package stand

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// The downloading user rides in a task's vals only when there is one: a task
// without it reads the same as before Bulk, and a point that does not know
// the key refuses a task that has it rather than run it without.
func TestLoadSpecBulk(t *testing.T) {
	spec := LoadSpec{Users: 3, Minutes: 10, Echo: netip.MustParseAddrPort("203.0.113.7:2053"), Path: LoadPathEntry}
	if slices.ContainsFunc(spec.Values(), func(v string) bool { return len(v) > 4 && v[:5] == "bulk=" }) {
		t.Fatalf("no bulk, yet %v", spec.Values())
	}
	spec.Bulk = 4
	got, err := ParseLoadSpec(spec.Values())
	if err != nil || got != spec {
		t.Fatalf("round trip %+v %v", got, err)
	}
	for _, n := range []int{-1, loadMaxBulk + 1} {
		s := spec
		s.Bulk = n
		if s.Validate() == nil {
			t.Fatalf("bulk %d accepted", n)
		}
	}
}

// A download is counted by the minute it arrived in, only from start to end,
// on as many connections as streams, again as soon as one is over; a source
// that fails is counted and retried.
func TestRunBulkCountsByMinuteOnItsOwnConnections(t *testing.T) {
	chunk := make([]byte, 256<<10)
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		for range 4 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	now := time.Now()
	// One "minute" of the window only: runBulk counts by real minutes, so
	// the window is a minute long and the test reads its one bucket.
	start, end := now.Add(200*time.Millisecond), now.Add(time.Minute+200*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b := runBulk(ctx, "", srv.URL+"/", 3, now, start, end)
	if b.Streams != 3 || len(b.MbpsPerMinute) != 1 {
		t.Fatalf("%+v", b)
	}
	if b.MB <= 0 || b.MbpsPerMinute[0] <= 0 {
		t.Fatalf("nothing counted: %+v", b)
	}
	if c := conns.Load(); c < 3 {
		t.Fatalf("%d connections for 3 streams", c)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel2()
	now = time.Now()
	f := runBulk(ctx2, "", srv.URL+"/fail", 2, now, now, now.Add(time.Minute))
	if f.Failed < 2 || f.MB != 0 || f.Error == "" {
		t.Fatalf("a failing source: %+v", f)
	}
}
