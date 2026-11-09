package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"
)

// The trunk's layer: chunks from a provoke receiver (hikkari-control
// edge/provoke.go: TLS with any SNI, «DL n», one connection per command, up
// to 4 MiB), straight across the border or through the trunk entry on its
// loopback port. The measurement of опыт 1 (hikkari-client
// plans/research/tools/trunkprobe), the hub's to run every round:
//
//	single: chunks one after another, a connection each, no retry;
//	pool:   poolConns connections at once; a chunk with no handshake and first
//	        byte in hsWait or no progress for stallWait goes again on a new
//	        connection (a new source port) until the deadline.

const (
	defaultChunks    = 48
	defaultChunkSize = 256 << 10
	poolConns        = 32
	hsWait           = 3 * time.Second
	stallWait        = time.Second
	trunkDeadline    = 60 * time.Second
	coverSNI         = "www.microsoft.com"
)

// Trunk is one run.
type Trunk struct {
	Target    string         `json:"target"`
	Mode      string         `json:"mode"`
	Conns     int            `json:"conns"`
	Chunks    int            `json:"chunks"`
	ChunkSize int64          `json:"chunk_size"`
	Delivered int            `json:"delivered"`
	Attempts  int            `json:"attempts"`
	FirstOK   int            `json:"first_ok"`
	Ends      map[string]int `json:"ends"`
	ChunkP50  float64        `json:"chunk_p50_ms"`
	ChunkP99  float64        `json:"chunk_p99_ms"`
	WallMS    float64        `json:"wall_ms"`
	Mbps      float64        `json:"goodput_mbps"`
}

type attempt struct {
	ok  bool
	end string // ok, dial, hs, stall_first, stall_mid, closed
	dur time.Duration
}

func measureTrunk(mode, addr string, chunks int, size int64) (Trunk, error) {
	if mode != "single" && mode != "pool" {
		return Trunk{}, fmt.Errorf("trunk: mode %q, single or pool", mode)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return Trunk{}, fmt.Errorf("trunk: %q is not host:port", addr)
	}
	if size <= 0 || size > 4<<20 || chunks > 512 {
		return Trunk{}, errors.New("trunk: chunk up to 4 MiB, up to 512 chunks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), trunkDeadline)
	defer cancel()
	start := time.Now()
	var atts []attempt
	firstTry := map[int]bool{}
	doneAt := map[int]time.Duration{}
	conns := 1
	if mode == "single" {
		for i := 0; i < chunks && ctx.Err() == nil; i++ {
			a := fetch(ctx, addr, size)
			atts = append(atts, a)
			firstTry[i] = a.ok
			if a.ok {
				doneAt[i] = a.dur
			}
		}
	} else {
		conns = poolConns
		queue := make(chan int, chunks)
		for i := 0; i < chunks; i++ {
			queue <- i
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		remaining := chunks
		firstSeen := map[int]bool{}
		chunkStart := map[int]time.Time{}
		allDone := make(chan struct{})
		for w := 0; w < conns; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case <-allDone:
						return
					case i := <-queue:
						mu.Lock()
						if _, ok := chunkStart[i]; !ok {
							chunkStart[i] = time.Now()
						}
						mu.Unlock()
						a := fetch(ctx, addr, size)
						mu.Lock()
						atts = append(atts, a)
						if !firstSeen[i] {
							firstSeen[i] = true
							firstTry[i] = a.ok
						}
						if a.ok {
							doneAt[i] = time.Since(chunkStart[i])
							if remaining--; remaining == 0 {
								close(allDone)
							}
						} else if ctx.Err() == nil {
							queue <- i
						}
						mu.Unlock()
					}
				}
			}()
		}
		wg.Wait()
	}
	wall := time.Since(start)
	r := Trunk{Target: addr, Mode: mode, Conns: conns, Chunks: chunks, ChunkSize: size, Attempts: len(atts),
		Ends: map[string]int{}, WallMS: msOf(wall)}
	for _, a := range atts {
		r.Ends[a.end]++
	}
	for _, ok := range firstTry {
		if ok {
			r.FirstOK++
		}
	}
	times := make([]float64, 0, len(doneAt))
	for _, d := range doneAt {
		times = append(times, msOf(d))
	}
	sort.Float64s(times)
	r.Delivered = len(times)
	if len(times) > 0 {
		r.ChunkP50 = times[len(times)/2]
		r.ChunkP99 = times[min((len(times)*99)/100, len(times)-1)]
	}
	r.Mbps = float64(int64(r.Delivered)*size*8) / wall.Seconds() / 1e6
	return r, nil
}

// fetch is one attempt: a new TCP connection (a new source port), TLS, DL.
func fetch(ctx context.Context, addr string, size int64) attempt {
	start := time.Now()
	var a attempt
	d := net.Dialer{Timeout: hsWait}
	raw, err := d.DialContext(ctx, "tcp4", addr)
	if err != nil {
		a.end, a.dur = "dial", time.Since(start)
		return a
	}
	defer raw.Close()
	_ = raw.SetDeadline(start.Add(hsWait))
	var tag [8]byte
	_, _ = rand.Read(tag[:])
	c := tls.Client(raw, &tls.Config{ServerName: coverSNI, NextProtos: []string{"hk-" + hex.EncodeToString(tag[:])}, InsecureSkipVerify: true})
	if err := c.HandshakeContext(ctx); err != nil {
		a.end, a.dur = "hs", time.Since(start)
		return a
	}
	if _, err := fmt.Fprintf(c, "DL %d\n", size); err != nil {
		a.end, a.dur = "closed", time.Since(start)
		return a
	}
	buf := make([]byte, 64<<10)
	var got int64
	for got < size {
		n, err := c.Read(buf)
		if n > 0 {
			got += int64(n)
			_ = raw.SetDeadline(time.Now().Add(stallWait))
		}
		if err != nil && got < size {
			var ne net.Error
			switch {
			case errors.As(err, &ne) && ne.Timeout() && got == 0:
				a.end = "stall_first"
			case errors.As(err, &ne) && ne.Timeout():
				a.end = "stall_mid"
			case errors.Is(err, io.EOF):
				a.end = "closed"
			default:
				a.end = "closed"
			}
			a.dur = time.Since(start)
			return a
		}
	}
	a.ok, a.end, a.dur = true, "ok", time.Since(start)
	return a
}

func msOf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
