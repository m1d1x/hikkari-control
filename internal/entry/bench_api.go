package entry

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"github.com/m1d1x/hikkari-control/internal/entrybench"
)

// The hub's end of a bench (roadmap 42 П6.14 Ф3): what a vantage's traffic,
// tunnelled through a relay, talks to. The relay forwards to this gateway's
// api host like any client's, so these routes sit on the public mux — behind
// a vantage's token: they cost bandwidth, and nobody else has a use for them.
//
//	POST /echo?reply=N  — a chat exchange: the body is read, N bytes answer it.
//	GET  /blob?size=N   — a photo-sized download.
//	POST /sink          — an upload; answers how many bytes arrived.
//	GET  /stream        — a WebSocket that echoes every binary frame: the
//	                      voice-like flow.
//
// Payloads are random bytes: compressible filler would measure the wrong
// thing, and a run of zeros looks like nothing a messenger sends.

const benchBase = entrybench.Base

// Bounds a bench request may ask for: a chat message, a photo, a voice frame.
const (
	maxBenchEcho   = 64 << 10
	maxBenchBlob   = 8 << 20
	maxBenchFrame  = 1 << 10
	maxBenchStream = MaxBenchDuration + time.Minute
	benchFrameIdle = 30 * time.Second
)

var benchUpgrader = websocket.Upgrader{
	ReadBufferSize:  maxBenchFrame,
	WriteBufferSize: maxBenchFrame,
	// Server to server behind a bearer token: no browser, no origin to check.
	CheckOrigin: func(*http.Request) bool { return true },
}

// RegisterBenchRoutes mounts the bench routes on the public mux, behind an
// observation point's own token, like the rest of the prober surface.
func RegisterBenchRoutes(mux *http.ServeMux, auth VantageAuth) {
	gate := vantageGate(auth)
	mux.HandleFunc("POST "+benchBase+"/echo", gate(benchEcho))
	mux.HandleFunc("GET "+benchBase+"/blob", gate(benchBlob))
	mux.HandleFunc("POST "+benchBase+"/sink", gate(benchSink))
	mux.HandleFunc("GET "+benchBase+"/stream", gate(benchStream))
}

// sizeParam reads a byte count from the query, bounded to [0, max].
func sizeParam(r *http.Request, name string, max int) (int, bool) {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	return n, err == nil && n >= 0 && n <= max
}

func benchEcho(w http.ResponseWriter, r *http.Request) {
	reply, ok := sizeParam(r, "reply", maxBenchEcho)
	if !ok {
		http.Error(w, "reply must be 0..65536", http.StatusBadRequest)
		return
	}
	if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxBenchEcho)); err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	writeRandom(w, reply)
}

func benchBlob(w http.ResponseWriter, r *http.Request) {
	size, ok := sizeParam(r, "size", maxBenchBlob)
	if !ok {
		http.Error(w, "size must be 0..8 MiB", http.StatusBadRequest)
		return
	}
	writeRandom(w, size)
}

func benchSink(w http.ResponseWriter, r *http.Request) {
	n, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxBenchBlob))
	if err != nil {
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"bytes": n})
}

func benchStream(w http.ResponseWriter, r *http.Request) {
	conn, err := benchUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has answered
	}
	defer conn.Close()
	conn.SetReadLimit(maxBenchFrame)
	end := time.Now().Add(maxBenchStream)
	for time.Now().Before(end) {
		_ = conn.SetReadDeadline(time.Now().Add(benchFrameIdle))
		kind, frame, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.BinaryMessage {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(benchFrameIdle))
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			return
		}
	}
}

// writeRandom answers n random bytes from a ChaCha8 stream seeded per request:
// incompressible, and cheap enough for 8 MiB at line rate.
func writeRandom(w http.ResponseWriter, n int) {
	var seed [32]byte
	_, _ = rand.Read(seed[:])
	src := mrand.NewChaCha8(seed)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(n))
	buf := make([]byte, 32<<10)
	for n > 0 {
		chunk := buf[:min(n, len(buf))]
		for i := 0; i+8 <= len(chunk); i += 8 {
			binary.LittleEndian.PutUint64(chunk[i:], src.Uint64())
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
		n -= len(chunk)
	}
}
