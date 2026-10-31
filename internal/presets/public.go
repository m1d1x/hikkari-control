package presets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/httputil"
)

const (
	// epochPoll is how soon a change reaches open streams. The epoch lives in
	// Postgres, so every hub instance sees every change without a bus.
	epochPoll = 5 * time.Second
	// keepAlive outlives idle proxies, well inside the client's 5-minute
	// stream lifetime.
	keepAlive = 25 * time.Second
	// maxStreams bounds open event streams; past it a client falls back to
	// its minute poll, which is correct, only slower.
	maxStreams = 20_000
	writeGrace = 10 * time.Second
)

// Public serves the catalog to clients: GET /v1/presets/catalog?bucket=NN
// and the change stream GET /v1/presets/events. Neither identifies a
// device: the bucket is the client's own random 0-99, the country comes
// from the request's address and is not kept.
type Public struct {
	store *Store
	// country places the request's address; "" when unknown, which serves
	// the default preset.
	country func(ip string) string

	mu      sync.Mutex
	streams int
	subs    map[chan int64]struct{}
	epoch   int64
}

func NewPublic(store *Store, country func(ip string) string) *Public {
	if country == nil {
		country = func(string) string { return "" }
	}
	return &Public{store: store, country: country, subs: map[chan int64]struct{}{}}
}

func (p *Public) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/presets/catalog", p.Catalog)
	mux.HandleFunc("GET /v1/presets/events", p.Events)
}

// Catalog answers with the document engine/core/presets.Parse reads, or 204
// when nothing is released for the device: it keeps what it has. The ETag
// names the epoch and the release, so a poll with nothing new is a 304.
func (p *Public) Catalog(w http.ResponseWriter, r *http.Request) {
	bucket, err := strconv.Atoi(r.URL.Query().Get("bucket"))
	if err != nil || bucket < 0 || bucket > 99 {
		http.Error(w, "bucket: 0-99", http.StatusBadRequest)
		return
	}
	sv, ok, err := p.store.Serve(r.Context(), p.country(httputil.ClientIP(r)), bucket)
	if err != nil {
		applog.Error(r.Context(), "presets", "serve catalog", applog.F("err", err.Error()))
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	etag := fmt.Sprintf(`"%d-%d"`, sv.Version, sv.Release)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body, err := json.Marshal(catalogDoc{SchemaVersion: 1, Version: sv.Version, Services: sv.Services})
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// Events streams `data: <version>` whenever the epoch moves. The client
// treats any event as "refetch"; the document itself always comes from
// Catalog.
func (p *Public) Events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	ch, ok := p.subscribe()
	if !ok {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	defer p.unsubscribe(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	// The server's WriteTimeout would cut a stream at a minute; each write
	// gets its own deadline instead.
	write := func(s string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeGrace))
		if _, err := fmt.Fprint(w, s); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(": hello\n\n") {
		return
	}
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case v := <-ch:
			if !write(fmt.Sprintf("data: %d\n\n", v)) {
				return
			}
		case <-tick.C:
			if !write(": ping\n\n") {
				return
			}
		}
	}
}

func (p *Public) subscribe() (chan int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streams >= maxStreams {
		return nil, false
	}
	p.streams++
	ch := make(chan int64, 1)
	p.subs[ch] = struct{}{}
	return ch, true
}

func (p *Public) unsubscribe(ch chan int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streams--
	delete(p.subs, ch)
}

// Watch follows the epoch until ctx ends and wakes every open stream on a
// change. A stream that has not taken the last change yet keeps it: one
// pending refetch covers any number of changes.
func (p *Public) Watch(ctx context.Context) {
	t := time.NewTicker(epochPoll)
	defer t.Stop()
	for {
		if v, err := p.store.Epoch(ctx); err == nil {
			p.publish(v)
		} else if ctx.Err() == nil {
			applog.Error(ctx, "presets", "read epoch", applog.F("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *Public) publish(v int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	first := p.epoch == 0
	if v == p.epoch {
		return
	}
	p.epoch = v
	if first {
		return // the epoch at startup is no change
	}
	for ch := range p.subs {
		select {
		case ch <- v:
		default:
		}
	}
}
