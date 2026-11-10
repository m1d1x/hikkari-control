package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/m1d1x/hikkari-protocol/trunk"
	"golang.org/x/net/http2"
)

// The trunk end to end through a real Hikkari relay (Caddy, the Naive proxy,
// padding, the relay's certificate): a client's TCP and UDP enter
// trunk-entry, cross the pool and reach a service on the relay's loopback.
// Strangers and subscribers asking for the trunk host get no member.
func TestTrunkThroughHikkariRelay(t *testing.T) {
	const trunkKey = "trunk-key-0123456789abcdef-xyz"
	certPEM, keyPEM := hikkariTestCert(t, "hk.example")

	// The exit box's own services: a TCP and a UDP echo on loopback.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	echoPort := uint16(echo.Addr().(*net.TCPAddr).Port)
	uecho, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(echoPort))))
	if err != nil {
		t.Fatal(err)
	}
	defer uecho.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := uecho.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = uecho.WriteTo(b[:n], from)
		}
	}()

	relay := freeAddr(t)
	storage := t.TempDir()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>site</html>"), 0644)
	get := func(k string) string {
		return map[string]string{
			"EDGE_PROTO": protoHikkari, "EDGE_LISTEN": relay, "EDGE_HIKKARI_HOST": "hk.example",
			"EDGE_HIKKARI_USER": "probe", "EDGE_HIKKARI_PASS": "probe-secret-long-enough", "EDGE_HIKKARI_ROOT": root,
			"STATE_DIRECTORY": storage, "EDGE_HIKKARI_TRUNK_KEYS": trunkKey,
			"EDGE_HIKKARI_TRUNK_PORTS": strconv.Itoa(int(echoPort)),
		}[k]
	}
	cfg, err := naiveFromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	n := newNaiveRuntime(cfg)
	_ = n.Set(context.Background(), []string{"0123456789abcdef0123456789abcdef"})
	activeNaive.Store(n)
	defer activeNaive.Store(nil)
	raw, _ := cfg.document()
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["storage"] = map[string]any{"module": "file_system", "root": t.TempDir()}
	apps := doc["apps"].(map[string]any)
	apps["tls"] = map[string]any{"certificates": map[string]any{"load_pem": []any{map[string]any{"certificate": certPEM, "key": keyPEM}}}}
	server := apps["http"].(map[string]any)["servers"].(map[string]any)["naive"].(map[string]any)
	server["automatic_https"] = map[string]any{"disable": true}
	raw, _ = json.Marshal(doc)
	if err = caddy.Load(raw, true); err != nil {
		t.Fatal(err)
	}
	defer caddy.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.startTrunkExit(ctx)

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(certPEM))

	// Who may not have a member.
	for _, tc := range []struct{ name, user, pass, host string }{
		{"wrong key", trunk.User, "trunk-key-WRONG-0123456789abc", trunk.Host},
		{"subscriber on the trunk host", "hikkari", "0123456789abcdef0123456789abcdef", trunk.Host},
		{"trunk key elsewhere", trunk.User, trunkKey, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(echoPort)))},
	} {
		if code := connectStatus(t, relay, roots, tc.user, tc.pass, tc.host); code == http.StatusOK {
			t.Fatalf("%s: CONNECT answered 200", tc.name)
		}
	}

	// Members dialled by a Go client speaking the member's wire (the entry
	// dials them with Chromium, hikkari-protocol trunk/chromium; Chromium
	// against this Caddy relay is the Hikkari client's own path).
	dial := func(ctx context.Context, port int) (io.ReadWriteCloser, error) {
		return goMember(relay, roots, trunkKey, port)
	}
	ports, _ := trunk.NewPortBook(42000, 43000, time.Minute)
	entry, err := trunk.NewEntry(trunk.Config{}, 4, dial, ports, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	go entry.Run(ctx)
	deadline := time.Now().Add(15 * time.Second)
	for entry.Session().Stats().Members < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("pool did not come up: %+v", entry.Session().Stats())
		}
		time.Sleep(20 * time.Millisecond)
	}

	c, edgeSide := net.Pipe()
	go entry.Session().Carry(edgeSide, echoPort)
	defer c.Close()
	data := make([]byte, 2<<20)
	_, _ = rand.Read(data)
	go func() { _, _ = c.Write(data) }()
	got := make([]byte, len(data))
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("tcp through the trunk: %v", err)
	}

	back := make(chan string, 1)
	f := entry.Session().OpenFlow(echoPort, func(p []byte) { back <- string(p) }, nil)
	f.Send([]byte("voice"))
	select {
	case v := <-back:
		if v != "voice" {
			t.Fatalf("udp through the trunk: %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("udp through the trunk: no answer")
	}
	if sessions, st := n.trunk.Stats(); sessions != 1 || st.Members != 4 {
		t.Fatalf("exit: %d sessions, %+v", sessions, st)
	}
	if up, down, _, _ := n.read(); up != 0 || down != 0 {
		t.Fatalf("trunk bytes were counted as a subscriber's: up %d down %d", up, down)
	}
}

// connectStatus sends one HTTP/2 CONNECT to the relay and returns its status.
func connectStatus(t *testing.T, relay string, roots *x509.CertPool, user, pass, host string) int {
	t.Helper()
	conn, err := tls.Dial("tcp", relay, &tls.Config{ServerName: "hk.example", RootCAs: roots, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cc, err := (&http2.Transport{}).NewClientConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	defer pw.Close()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: host}, Host: host, Body: pr, ContentLength: -1,
		Header: http.Header{"Proxy-Authorization": {basicAuth(user, pass)}}}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func freeAddr(t *testing.T) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(t, "tcp")))
}

// goMember is one trunk member over Go's TLS and HTTP/2: CONNECT
// trunk.Host with the trunk credential and Naive padding of the first 8
// frames each way.
func goMember(relay string, roots *x509.CertPool, key string, port int) (io.ReadWriteCloser, error) {
	d := net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{Port: port}}
	raw, err := d.Dial("tcp", relay)
	if err != nil {
		return nil, err
	}
	return goConnect(raw, roots, trunk.User, key, trunk.Host)
}

// goConnect is one padded Naive CONNECT to host over raw, a connection to
// the relay (direct, or carried by the trunk as a client's would be).
func goConnect(raw net.Conn, roots *x509.CertPool, user, pass, host string) (io.ReadWriteCloser, error) {
	tc := tls.Client(raw, &tls.Config{ServerName: "hk.example", RootCAs: roots, NextProtos: []string{"h2"}})
	if err := tc.Handshake(); err != nil {
		raw.Close()
		return nil, err
	}
	cc, err := (&http2.Transport{}).NewClientConn(tc)
	if err != nil {
		tc.Close()
		return nil, err
	}
	body, feed := io.Pipe()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: host}, Host: host, Body: body, ContentLength: -1,
		Header: http.Header{"Proxy-Authorization": {basicAuth(user, pass)}, "Padding": {strings.Repeat("~", 40)}}}
	resp, err := cc.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		tc.Close()
		return nil, fmt.Errorf("connect: %v %v", resp, err)
	}
	return &goTunnel{r: &unpadReader{r: resp.Body}, w: &padWriter{w: feed}, closers: []io.Closer{feed, resp.Body, tc}}, nil
}

type goTunnel struct {
	r       io.Reader
	w       io.Writer
	closers []io.Closer
}

func (g *goTunnel) Read(p []byte) (int, error)  { return g.r.Read(p) }
func (g *goTunnel) Write(p []byte) (int, error) { return g.w.Write(p) }
func (g *goTunnel) Close() error {
	for _, c := range g.closers {
		_ = c.Close()
	}
	return nil
}

type padWriter struct {
	w    io.Writer
	sent int
}

func (p *padWriter) Write(b []byte) (int, error) {
	if p.sent >= 8 {
		return p.w.Write(b)
	}
	written := 0
	for len(b) > 0 && p.sent < 8 {
		n := min(len(b), 65535-3-16)
		frame := append([]byte{byte(n >> 8), byte(n), 16}, b[:n]...)
		frame = append(frame, make([]byte, 16)...)
		if _, err := p.w.Write(frame); err != nil {
			return written, err
		}
		p.sent++
		written += n
		b = b[n:]
	}
	if len(b) > 0 {
		n, err := p.w.Write(b)
		return written + n, err
	}
	return written, nil
}

type unpadReader struct {
	r         io.Reader
	got, left int
	pad       int
}

func (u *unpadReader) Read(b []byte) (int, error) {
	for u.got < 8 || u.left > 0 {
		if u.left == 0 {
			var h [3]byte
			if _, err := io.ReadFull(u.r, h[:]); err != nil {
				return 0, err
			}
			u.left, u.pad = int(h[0])<<8|int(h[1]), int(h[2])
			u.got++
			if u.left == 0 {
				if _, err := io.CopyN(io.Discard, u.r, int64(u.pad)); err != nil {
					return 0, err
				}
				continue
			}
		}
		if len(b) > u.left {
			b = b[:u.left]
		}
		n, err := u.r.Read(b)
		u.left -= n
		if u.left == 0 && n > 0 && u.pad > 0 {
			if _, err := io.CopyN(io.Discard, u.r, int64(u.pad)); err != nil {
				return n, err
			}
		}
		return n, err
	}
	return u.r.Read(b)
}
