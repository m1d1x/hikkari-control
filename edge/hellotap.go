package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// The hello tap stands in front of a Reality edge's SNI router: it reads the
// ClientHello off the socket, counts its hk under the cover it names, and
// hands the connection to Xray on loopback with every byte it read, behind a
// PROXY v2 header so Xray still sees the client's address. What the node
// receives is then held to what the release's core sends for the fingerprint
// the node hands out (hikkari-client plans/2026-11-07-knob-truth-wire-diagnostics.md,
// W5): a path that rewrites hellos — a provider's proxy, fragmentation, a
// MITM box — shows as a node that gets a hash no core sends. Client addresses
// are not kept; the counters hold a cover, a hash and a number.
//
// Every connection to the port is counted, ours and everyone else's: probers,
// scanners and browsers that hit the cover. The counts say what reaches the
// node, not who sent it.

const (
	// helloWait bounds the read of a hello. Xray's Reality waits for one too,
	// and a client that sends nothing in time is handed over as it is.
	helloWait = 5 * time.Second
	// tapMaxKeys bounds the distinct (cover, hk) pairs a run keeps: a scanner
	// sweep of random hellos must not grow the map without end.
	tapMaxKeys = 512
)

// Labels of a connection without an hk.
const (
	tapUnread   = "unread"   // not one whole TLS ClientHello in time: HTTP, a scan, silence
	tapUnparsed = "unparsed" // a ClientHello the parser refused
	tapOther    = "other"    // past tapMaxKeys
	tapNoCover  = "-"        // an SNI that is none of the covers, or none at all
)

// helloTap counts hellos per cover since the process started, as the pulse
// reports its other totals (pulse.go).
type helloTap struct {
	covers map[string]bool
	mu     sync.Mutex
	counts map[string]map[string]int64 // cover → hk → connections
	keys   int
}

func newHelloTap(covers []string) *helloTap {
	t := &helloTap{covers: map[string]bool{}, counts: map[string]map[string]int64{}}
	for _, c := range covers {
		t.covers[c] = true
	}
	return t
}

func (t *helloTap) count(cover, hk string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, seen := t.counts[cover][hk]; !seen {
		if t.keys >= tapMaxKeys {
			cover, hk = tapNoCover, tapOther
		} else {
			t.keys++
		}
	}
	if t.counts[cover] == nil {
		t.counts[cover] = map[string]int64{}
	}
	t.counts[cover][hk]++
}

// totals is a copy of the counters, for the pulse.
func (t *helloTap) totals() map[string]map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]map[string]int64, len(t.counts))
	for cover, byHK := range t.counts {
		m := make(map[string]int64, len(byHK))
		for hk, n := range byHK {
			m[hk] = n
		}
		out[cover] = m
	}
	return out
}

// classify reads the hello from r and names it: the cover and the hk.
func (t *helloTap) classify(r io.Reader) (cover, hk string) {
	msg, _, err := readClientHello(r)
	if err != nil {
		return tapNoCover, tapUnread
	}
	h, err := parseClientHello(msg)
	if err != nil {
		return tapNoCover, tapUnparsed
	}
	cover = tapNoCover
	if t.covers[h.ServerName] {
		cover = h.ServerName
	}
	return cover, h.hk()
}

// serve accepts on l until ctx ends and hands every connection to upstream.
func (t *helloTap) serve(ctx context.Context, l net.Listener, upstream string) {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			log.Printf("nekko-edge: hello tap: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go t.handle(c, upstream)
	}
}

// handle reads the hello, counts it and splices the connection to upstream,
// the bytes read first. A hello that does not read is still handed over:
// Xray answers it as it would without the tap.
func (t *helloTap) handle(c net.Conn, upstream string) {
	defer c.Close()
	var seen bytes.Buffer
	_ = c.SetReadDeadline(time.Now().Add(helloWait))
	cover, hk := t.classify(io.TeeReader(io.LimitReader(c, wireMaxHello+64*1024), &seen))
	_ = c.SetReadDeadline(time.Time{})
	t.count(cover, hk)

	up, err := net.DialTimeout("tcp", upstream, helloWait)
	if err != nil {
		log.Printf("nekko-edge: hello tap: upstream: %v", err)
		return
	}
	defer up.Close()
	head := proxyV2(c.RemoteAddr(), c.LocalAddr())
	if _, err := up.Write(append(head, seen.Bytes()...)); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
	<-done
}

// proxyV2Sig opens every PROXY protocol v2 header.
var proxyV2Sig = []byte{0x0d, 0x0a, 0x0d, 0x0a, 0x00, 0x0d, 0x0a, 0x51, 0x55, 0x49, 0x54, 0x0a}

// proxyV2 is the PROXY v2 header of a TCP connection from src to dst; LOCAL
// (no address) when either is not TCP over IP.
func proxyV2(src, dst net.Addr) []byte {
	out := append([]byte(nil), proxyV2Sig...)
	s, ok1 := src.(*net.TCPAddr)
	d, ok2 := dst.(*net.TCPAddr)
	if !ok1 || !ok2 {
		return append(out, 0x20, 0x00, 0x00, 0x00)
	}
	if s4, d4 := s.IP.To4(), d.IP.To4(); s4 != nil && d4 != nil {
		out = append(out, 0x21, 0x11, 0x00, 12)
		out = append(out, s4...)
		out = append(out, d4...)
	} else {
		out = append(out, 0x21, 0x21, 0x00, 36)
		out = append(out, s.IP.To16()...)
		out = append(out, d.IP.To16()...)
	}
	out = binary.BigEndian.AppendUint16(out, uint16(s.Port))
	return binary.BigEndian.AppendUint16(out, uint16(d.Port))
}
