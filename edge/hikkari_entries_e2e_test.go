package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hkserver "github.com/m1d1x/hikkari-protocol/server"
)

// entryForward is an entry of the cascade as the relay sees it: an L4 forward
// of one port, TCP and UDP, to the relay's. While cut it freezes both ways as
// a silent entry does: TCP bytes wait in the forward, UDP packets are lost.
type entryForward struct {
	addr string
	cut  atomic.Bool
}

func newEntryForward(t *testing.T, ctx context.Context, to string) *entryForward {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := &entryForward{addr: ln.Addr().String()}
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	go func() {
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", to)
			if err != nil {
				_ = down.Close()
				continue
			}
			context.AfterFunc(ctx, func() { _ = down.Close(); _ = up.Close() })
			go e.pump(up, down)
			go e.pump(down, up)
		}
	}()
	udpAddr, _ := net.ResolveUDPAddr("udp", e.addr)
	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	context.AfterFunc(ctx, func() { _ = pc.Close() })
	go e.forwardUDP(ctx, pc, to)
	return e
}

func (e *entryForward) pump(dst, src net.Conn) {
	defer dst.Close()
	b := make([]byte, 32<<10)
	for {
		n, err := src.Read(b)
		for e.cut.Load() {
			time.Sleep(2 * time.Millisecond)
		}
		if n > 0 {
			if _, werr := dst.Write(b[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// forwardUDP gives every client address a socket of its own to the relay,
// as a NAT does.
func (e *entryForward) forwardUDP(ctx context.Context, pc *net.UDPConn, to string) {
	relay, _ := net.ResolveUDPAddr("udp", to)
	flows := map[string]*net.UDPConn{}
	b := make([]byte, 65535)
	for {
		n, from, err := pc.ReadFromUDP(b)
		if err != nil {
			return
		}
		if e.cut.Load() {
			continue
		}
		up := flows[from.String()]
		if up == nil {
			if up, err = net.DialUDP("udp", nil, relay); err != nil {
				continue
			}
			flows[from.String()] = up
			context.AfterFunc(ctx, func() { _ = up.Close() })
			go func(up *net.UDPConn, from *net.UDPAddr) {
				rb := make([]byte, 65535)
				for {
					k, err := up.Read(rb)
					if err != nil {
						return
					}
					if !e.cut.Load() {
						_, _ = pc.WriteToUDP(rb[:k], from)
					}
				}
			}(up, from)
		}
		_, _ = up.Write(b[:n])
	}
}

// bigPattern is the byte at offset i of the slow download.
func bigPattern(i int64) byte { return byte(i*7 + i>>13) }

// Hikkari v1.07 with the real client core (HIKKARI_TEST_CORE) and the real
// relay: the core dials the relay through two entries, and entry 0 goes
// silent for 8 s under a call and a 64 MiB download, as aezamsk1 does. The
// call goes on by entry 1 without a pause a person hears, a new request goes
// by entry 1 within 2 s, and the download, which Chromium carries on entry 0's
// HTTP/2 connection, lives on when the entry answers again, whole.
//
// With HIKKARI_TEST_DROP=1 the cut is the kernel's instead of the forward's:
// iptables drops every packet to and from entry 0's port, so TCP backs off
// as on the way to a silent entry. It needs root in a netns of its own:
//
//	go test -c -o /tmp/edge.test . && sudo ip netns exec <ns> env HIKKARI_TEST_DROP=1 \n//	  HIKKARI_TEST_CORE=... /tmp/edge.test -test.run TestHikkariRealCoreEntries -test.v
func TestHikkariRealCoreEntries(t *testing.T) {
	binary := os.Getenv("HIKKARI_TEST_CORE")
	if binary == "" {
		t.Skip("HIKKARI_TEST_CORE not set")
	}
	exitPolicy = hkserver.NewPolicyExit(nil, nil, nil)
	key := "0123456789abcdef0123456789abcdef"
	certPEM, keyPEM := hikkariTestCert(t, "hk.example")
	const size = 64 << 20
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/big" {
			_, _ = io.WriteString(w, "small")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(size))
		chunk := make([]byte, 64<<10)
		for off := int64(0); off < size; off += int64(len(chunk)) {
			for i := range chunk {
				chunk[i] = bigPattern(off + int64(i))
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer origin.Close()
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := echo.ReadFromUDP(b)
			if err != nil {
				return
			}
			_, _ = echo.WriteToUDP(b[:n], from)
		}
	}()

	address, _, _ := startHikkariTestRelay(t, key, certPEM, keyPEM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entries := [2]*entryForward{newEntryForward(t, ctx, address), newEntryForward(t, ctx, address)}
	_, port0, _ := net.SplitHostPort(entries[0].addr)
	_, port1, _ := net.SplitHostPort(entries[1].addr)
	outbound := map[string]any{"type": "hikkari", "tag": "proxy", "server": "127.0.0.1", "server_port": atoi(port0), "password": key,
		"tls":     map[string]any{"enabled": true, "server_name": "hk.example", "certificate": []string{certPEM}},
		"udp":     map[string]any{"dup": true},
		"entries": []any{map[string]any{"server": "127.0.0.1", "server_port": atoi(port1)}}}
	mixed, stopCore := hikkariTestCoreLog(t, binary, outbound)
	stopped := false
	defer func() {
		if !stopped {
			stopCore()
		}
	}()
	time.Sleep(time.Second) // both entries answer their first pings
	proxy, _ := url.Parse("http://" + mixed)
	client := &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}

	// The download.
	var got, bad, stall atomic.Int64
	bad.Store(-1)
	downloaded := make(chan error, 1)
	go func() {
		response, err := client.Get(origin.URL + "/big")
		if err != nil {
			downloaded <- err
			return
		}
		defer response.Body.Close()
		b := make([]byte, 64<<10)
		for {
			start := time.Now()
			n, err := response.Body.Read(b)
			if wait := int64(time.Since(start)); wait > stall.Load() {
				stall.Store(wait)
			}
			at := got.Load()
			for i := 0; i < n; i++ {
				if bad.Load() < 0 && b[i] != bigPattern(at+int64(i)) {
					bad.Store(at + int64(i))
				}
			}
			got.Add(int64(n))
			if err == io.EOF {
				downloaded <- nil
				return
			}
			if err != nil {
				downloaded <- err
				return
			}
		}
	}()

	// The cut, and a new request 2 s into it.
	const cutFor = 8 * time.Second
	type probe struct {
		atCut   int64
		newTook time.Duration
		newErr  error
	}
	probed := make(chan probe, 1)
	go func() {
		var p probe
		time.Sleep(3 * time.Second)
		cutAt := time.Now()
		p.atCut = got.Load()
		cutEntry(t, entries[0], true)
		time.Sleep(time.Until(cutAt.Add(2 * time.Second)))
		start := time.Now()
		quick := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
		response, err := quick.Get(origin.URL + "/small")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) != "small" {
				err = fmt.Errorf("body %q", body)
			}
		}
		p.newTook, p.newErr = time.Since(start), err
		time.Sleep(time.Until(cutAt.Add(cutFor)))
		cutEntry(t, entries[0], false)
		probed <- p
	}()

	// The call: 1 s to be told a call, the cut at 3 s for 8 s, 3 s after.
	const packets = 700
	sent, answered, worst := socksUDPEcho(t, mixed, echo.LocalAddr().(*net.UDPAddr), packets)
	p := <-probed
	select {
	case err := <-downloaded:
		if err != nil {
			t.Errorf("download: %v", err)
		}
	case <-time.After(90 * time.Second):
		t.Errorf("download did not end: %d of %d bytes", got.Load(), int64(size))
	}
	logs := stopCore()
	stopped = true
	t.Logf("call: %d/%d echoed, longest pause %v; new request 2 s into the cut: %v (%v); download: %d MiB at the cut, %d of %d bytes, first bad %d, longest wait %v",
		answered, sent, worst.Round(time.Millisecond), p.newTook.Round(time.Millisecond), p.newErr, p.atCut>>20, got.Load(), int64(size), bad.Load(),
		time.Duration(stall.Load()).Round(time.Millisecond))
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "entry ") && !strings.Contains(line, "hikkari period") {
			t.Logf("core: %s", line)
		}
	}
	if i := strings.Index(logs, `"entries":`); i >= 0 {
		end := min(len(logs), i+400)
		t.Logf("core period: %s", logs[i:end])
	}
	if answered < sent-2 || worst > 60*time.Millisecond {
		t.Errorf("call: %d of %d, longest pause %v; want no pause over 60 ms", answered, sent, worst)
	}
	if p.newErr != nil || p.newTook > 2*time.Second {
		t.Errorf("new request 2 s into the cut: %v after %v", p.newErr, p.newTook)
	}
	if p.atCut == 0 || p.atCut >= size {
		t.Errorf("download had %d of %d bytes at the cut, want it running", p.atCut, int64(size))
	}
	if got.Load() != size || bad.Load() >= 0 {
		t.Errorf("download: %d of %d bytes, first bad at %d", got.Load(), int64(size), bad.Load())
	}
	if !strings.Contains(logs, "entry 0: silent for 1.5s, new connections go by entry 1") {
		t.Errorf("the core did not say entry 0 went silent")
	}
}

// cutEntry freezes the entry's forward, or with HIKKARI_TEST_DROP=1 has the
// kernel drop its packets both ways, TCP and UDP.
func cutEntry(t *testing.T, e *entryForward, on bool) {
	if os.Getenv("HIKKARI_TEST_DROP") != "1" {
		e.cut.Store(on)
		return
	}
	_, port, _ := net.SplitHostPort(e.addr)
	op := "-D"
	if on {
		op = "-I"
	}
	for _, proto := range []string{"tcp", "udp"} {
		for _, rule := range [][]string{
			{"-w", op, "INPUT", "-p", proto, "--dport", port, "-j", "DROP"},
			{"-w", op, "OUTPUT", "-p", proto, "--sport", port, "-j", "DROP"},
		} {
			if out, err := exec.Command("iptables", rule...).CombinedOutput(); err != nil {
				t.Errorf("iptables %v: %v: %s", rule, err, out)
			}
		}
	}
}
