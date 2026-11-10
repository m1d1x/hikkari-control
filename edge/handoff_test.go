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
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/client"
	hkserver "github.com/m1d1x/hikkari-protocol/server"
	"github.com/m1d1x/hikkari-protocol/trunk"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
	"golang.org/x/net/http2"
)

// TestMain lets the test binary be a relay process: the handoff test starts
// it as the first generation, and every handoff starts it again as the next
// one, exactly as systemd and the binary on disk would.
func TestMain(m *testing.M) {
	if os.Getenv("EDGE_TEST_GENERATION") == "1" {
		runTestGeneration()
		return
	}
	os.Exit(m.Run())
}

// runTestGeneration is a Hikkari relay with the test's certificate and with
// loopback allowed (the test's echo servers are there); nothing else differs.
func runTestGeneration() {
	exitPolicy = hkserver.NewPolicyExit(nil, nil, nil)
	certPEM, keyPEM := os.Getenv("EDGE_TEST_CERT_PEM"), os.Getenv("EDGE_TEST_KEY_PEM")
	documentHook = func(raw []byte) []byte {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			log.Fatal(err)
		}
		apps := doc["apps"].(map[string]any)
		apps["tls"] = map[string]any{"certificates": map[string]any{"load_pem": []any{map[string]any{"certificate": certPEM, "key": keyPEM}}}}
		server := apps["http"].(map[string]any)["servers"].(map[string]any)["naive"].(map[string]any)
		server["automatic_https"] = map[string]any{"disable": true}
		handlers := server["routes"].([]any)[0].(map[string]any)["handle"].([]any)
		handlers[1].(map[string]any)["acl"] = []any{map[string]any{"subjects": []string{"127.0.0.1"}, "allow": true}}
		out, _ := json.Marshal(doc)
		return out
	}
	if err := run(); err != nil {
		log.Printf("nekko-edge (test generation): %v", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestHandoffKeepsSessions: three handoffs in a row (a canary's
// deploy/rollback pairs) under a live Naive tunnel, a live UDP flow on the
// datagram channel and a live stream through the cascade's trunk. None of
// them is cut; what opens after each handoff lands on the newest
// generation; the old generations leave once their sessions end.
func TestHandoffKeepsSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("starts relay processes")
	}
	const trunkKey = "trunk-key-0123456789abcdef-xyz"
	const probeUser, probePass = "probe", "probe-secret-long-enough"
	certPEM, keyPEM := hikkariTestCert(t, "hk.example")
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(certPEM))

	echoTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoTCP.Close()
	go func() {
		for {
			c, err := echoTCP.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	echoPort := echoTCP.Addr().(*net.TCPAddr).Port
	echoUDP, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(echoPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer echoUDP.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := echoUDP.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = echoUDP.WriteTo(b[:n], from)
		}
	}()

	relay := freeAddr(t)
	run, err := os.MkdirTemp("/tmp", "hkho") // a unix socket path must be short
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	storage := t.TempDir()
	certDir := filepath.Join(storage, "certificates", "test", "hk.example")
	_ = os.MkdirAll(certDir, 0o700)
	_ = os.WriteFile(filepath.Join(certDir, "hk.example.crt"), []byte(certPEM), 0o600)
	_ = os.WriteFile(filepath.Join(certDir, "hk.example.key"), []byte(keyPEM), 0o600)
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>site</html>"), 0o644)
	env := map[string]string{
		"EDGE_PROTO": protoHikkari, "EDGE_LISTEN": relay, "EDGE_HIKKARI_HOST": "hk.example",
		"EDGE_HIKKARI_USER": probeUser, "EDGE_HIKKARI_PASS": probePass, "EDGE_HIKKARI_ROOT": root,
		"EDGE_HIKKARI_TRUNK_KEYS": trunkKey, "EDGE_HIKKARI_TRUNK_PORTS": strconv.Itoa(echoPort),
		"EDGE_TEST_GENERATION": "1", "EDGE_TEST_CERT_PEM": certPEM, "EDGE_TEST_KEY_PEM": keyPEM,
	}

	// The first generation, as systemd starts it.
	first := exec.Command(os.Args[0])
	first.Env = append(os.Environ(), "RUNTIME_DIRECTORY="+run, "STATE_DIRECTORY="+storage)
	for k, v := range env {
		first.Env = append(first.Env, k+"="+v)
	}
	first.Stdout, first.Stderr = os.Stderr, os.Stderr
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	firstGone := make(chan struct{})
	go func() { _ = first.Wait(); close(firstGone) }()
	pids := []int{first.Process.Pid}
	defer func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	control := filepath.Join(run, controlName)
	waitUntil(t, 15*time.Second, "first generation up", func() bool {
		c, err := net.Dial("unix", control)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A Naive tunnel to the TCP echo.
	tunnel, err := naiveTunnel(relay, roots, probeUser, probePass, net.JoinHostPort("127.0.0.1", strconv.Itoa(echoPort)), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	// A UDP flow on the datagram channel to the UDP echo.
	dc, err := client.NewDatagram(client.DatagramOptions{
		Context: ctx, Dialer: N.SystemDialer, Server: M.ParseSocksaddr(relay), Password: probePass,
		TLS: &testTLS{config: &tls.Config{ServerName: "hk.example", RootCAs: roots}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	flow, err := dc.DialContext(ctx, "udp", M.ParseSocksaddr(net.JoinHostPort("127.0.0.1", strconv.Itoa(echoPort))))
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	// The cascade's entry with its pool, and a stream through it.
	ports, _ := trunk.NewPortBook(44000, 45000, time.Minute)
	entry, err := trunk.NewEntry(trunk.Config{}, 4, func(ctx context.Context, port int) (io.ReadWriteCloser, error) {
		return goMember(relay, roots, trunkKey, port)
	}, ports, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	go entry.Run(ctx)
	waitUntil(t, 15*time.Second, "trunk pool", func() bool { return entry.Session().Stats().Members == 4 })
	cascaded, edgeSide := net.Pipe()
	go entry.Session().Carry(edgeSide, uint16(echoPort))
	defer cascaded.Close()

	live := func(stage string) {
		t.Helper()
		echoes(t, stage+": naive tunnel", tunnel, 256<<10)
		echoes(t, stage+": trunk stream", cascaded, 256<<10)
		if got := udpEchoes(flow, 30); got < 28 {
			t.Fatalf("%s: udp flow echoed %d of 30", stage, got)
		}
	}
	live("first generation")

	for i := 1; i <= 3; i++ {
		reply, err := requestHandoff(control, handoffRequest{Env: env, Binary: os.Args[0]})
		if err != nil {
			t.Fatalf("handoff %d: %v", i, err)
		}
		pids = append(pids, reply.PID)
		stage := fmt.Sprintf("after handoff %d", i)
		live(stage)
		// What opens now is the newest generation's, and works.
		fresh, err := naiveTunnel(relay, roots, probeUser, probePass, net.JoinHostPort("127.0.0.1", strconv.Itoa(echoPort)), 0)
		if err != nil {
			t.Fatalf("%s: new tunnel: %v", stage, err)
		}
		echoes(t, stage+": new tunnel", fresh, 64<<10)
		fresh.Close()
		dc2, err := client.NewDatagram(client.DatagramOptions{
			Context: ctx, Dialer: N.SystemDialer, Server: M.ParseSocksaddr(relay), Password: probePass,
			TLS: &testTLS{config: &tls.Config{ServerName: "hk.example", RootCAs: roots}},
		})
		if err != nil {
			t.Fatal(err)
		}
		flow2, err := dc2.DialContext(ctx, "udp", M.ParseSocksaddr(net.JoinHostPort("127.0.0.1", strconv.Itoa(echoPort))))
		if err != nil {
			t.Fatalf("%s: new udp flow: %v", stage, err)
		}
		if got := udpEchoes(flow2, 20); got < 19 {
			t.Fatalf("%s: new udp flow echoed %d of 20", stage, got)
		}
		flow2.Close()
		dc2.Close()
		waitUntil(t, 15*time.Second, stage+": trunk on a new session", func() bool {
			return entry.Session().Stats().Members == 4
		})
		c2, e2 := net.Pipe()
		go entry.Session().Carry(e2, uint16(echoPort))
		echoes(t, stage+": new trunk stream", c2, 64<<10)
		c2.Close()
	}
	live("after three handoffs")

	// The first generation leaves once what it carries has ended: the
	// tunnel, the flow's QUIC connection and the trunk's old session.
	tunnel.Close()
	flow.Close()
	dc.Close()
	cascaded.Close()
	select {
	case <-firstGone:
	case <-time.After(30 * time.Second):
		t.Fatal("the first generation did not leave after its sessions ended")
	}
}

func waitUntil(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// echoes sends size random bytes through c and reads them back.
func echoes(t *testing.T, what string, c io.ReadWriter, size int) {
	t.Helper()
	data := make([]byte, size)
	_, _ = rand.Read(data)
	errc := make(chan error, 1)
	go func() { _, err := c.Write(data); errc <- err }()
	got := make([]byte, size)
	read := make(chan error, 1)
	go func() { _, err := io.ReadFull(c, got); read <- err }()
	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("%s: read back: %v", what, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: no echo in 20 s", what)
	}
	if err := <-errc; err != nil {
		t.Fatalf("%s: write: %v", what, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%s: echo differs", what)
	}
}

// udpEchoes sends n packets one by one and counts the answers.
func udpEchoes(c net.Conn, n int) int {
	got := 0
	buf := make([]byte, 2048)
	for i := 0; i < n; i++ {
		msg := []byte(fmt.Sprintf("packet-%d", i))
		if _, err := c.Write(msg); err != nil {
			continue
		}
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		if k, err := c.Read(buf); err == nil && bytes.Equal(buf[:k], msg) {
			got++
		}
	}
	return got
}

// naiveTunnel is a Naive CONNECT over Go's TLS and HTTP/2 with the padding
// of the first frames, as goMember is for a trunk member.
func naiveTunnel(relay string, roots *x509.CertPool, user, pass, host string, port int) (*goTunnel, error) {
	d := net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{Port: port}}
	raw, err := tls.DialWithDialer(&d, "tcp", relay, &tls.Config{ServerName: "hk.example", RootCAs: roots, NextProtos: []string{"h2"}})
	if err != nil {
		return nil, err
	}
	cc, err := (&http2.Transport{}).NewClientConn(raw)
	if err != nil {
		raw.Close()
		return nil, err
	}
	body, feed := io.Pipe()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: host}, Host: host, Body: body, ContentLength: -1,
		Header: http.Header{"Proxy-Authorization": {basicAuth(user, pass)}, "Padding": {strings.Repeat("~", 40)}}}
	resp, err := cc.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		raw.Close()
		return nil, fmt.Errorf("connect: %v %v", resp, err)
	}
	return &goTunnel{r: &unpadReader{r: resp.Body}, w: &padWriter{w: feed}, closers: []io.Closer{feed, resp.Body, raw}}, nil
}

// testTLS is the datagram client's TLS: the test's roots.
type testTLS struct{ config *tls.Config }

func (c *testTLS) ServerName() string                  { return c.config.ServerName }
func (c *testTLS) SetServerName(name string)           { c.config.ServerName = name }
func (c *testTLS) NextProtos() []string                { return c.config.NextProtos }
func (c *testTLS) SetNextProtos(protos []string)       { c.config.NextProtos = protos }
func (c *testTLS) HandshakeTimeout() time.Duration     { return 0 }
func (c *testTLS) SetHandshakeTimeout(time.Duration)   {}
func (c *testTLS) STDConfig() (*aTLS.STDConfig, error) { return c.config, nil }
func (c *testTLS) Client(net.Conn) (aTLS.Conn, error)  { return nil, net.ErrClosed }
func (c *testTLS) Clone() aTLS.Config                  { return &testTLS{config: c.config.Clone()} }

// The relay's network gives Caddy the relay's socket for the relay's port
// and nothing else: the redirect server Caddy adds on :80 with the same
// network must get a socket of its own.
func TestRelayNetworkGivesTheRelaySocketOnlyForItsPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	relayListener.Store(&listenerSlot{ln})
	defer relayListener.Store(nil)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	got, err := relayNetworkListener(context.Background(), relayNetwork, "127.0.0.1", port, 0, net.ListenConfig{})
	if err != nil || got != ln {
		t.Fatalf("relay port: %v %v, want the relay's socket", got, err)
	}
	other := strconv.Itoa(freePort(t, "tcp"))
	got, err = relayNetworkListener(context.Background(), relayNetwork, "127.0.0.1", other, 0, net.ListenConfig{})
	if err != nil {
		t.Fatalf("other port: %v", err)
	}
	if got == ln {
		t.Fatal("another port got the relay's socket")
	}
	l, ok := got.(net.Listener)
	if !ok {
		t.Fatalf("other port: %T", got)
	}
	defer l.Close()
	if _, p, _ := net.SplitHostPort(l.Addr().String()); p != other {
		t.Fatalf("other port listens on %s, want %s", p, other)
	}
}
