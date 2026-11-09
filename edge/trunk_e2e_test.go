package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
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

	entryPort := freePort(t, "tcp")
	ec := TrunkEntryConfig{
		Exit: relay, SNI: "hk.example", Key: trunkKey, Hello: "chrome_131", Members: 4,
		TCP: map[int]uint16{entryPort: echoPort}, UDP: map[int]uint16{entryPort: echoPort},
		PortLo: defaultTrunkPortLo, PortHi: defaultTrunkPortHi,
	}
	dial := trunk.Dialer{Exit: relay, SNI: "hk.example", Hello: trunk.Hellos[ec.Hello], Key: trunkKey, Roots: roots}.Dial
	entry, err := startTrunkEntry(ctx, ec, dial)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for entry.Session().Stats().Members < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("pool did not come up: %+v", entry.Session().Stats())
		}
		time.Sleep(20 * time.Millisecond)
	}

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(entryPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	data := make([]byte, 2<<20)
	_, _ = rand.Read(data)
	go func() { _, _ = c.Write(data) }()
	got := make([]byte, len(data))
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("tcp through the trunk: %v", err)
	}

	u, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(entryPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	_, _ = u.Write([]byte("voice"))
	_ = u.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 64)
	if k, err := u.Read(b); err != nil || string(b[:k]) != "voice" {
		t.Fatalf("udp through the trunk: %q %v", b[:k], err)
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

// TestHubOffersTheTrunkHellos: the hub refuses a trunk fp before it touches a
// box (internal/entry/trunk.go trunkFPs); it must name exactly what the edge
// takes. Checked from the repository's own source.
func TestHubOffersTheTrunkHellos(t *testing.T) {
	src, err := os.ReadFile("../internal/entry/trunk.go")
	if err != nil {
		t.Skip("internal/entry is not next to edge")
	}
	start := bytes.Index(src, []byte("var trunkFPs = map[string]bool{"))
	if start < 0 {
		t.Fatal("trunkFPs not found")
	}
	body := src[start:]
	body = body[:bytes.Index(body, []byte("\n}"))]
	hub := map[string]bool{}
	for _, part := range bytes.Split(body, []byte(`"`))[1:] {
		if s := string(part); !strings.ContainsAny(s, ":{}\n\t ,") && s != "" {
			hub[s] = true
		}
	}
	if len(hub) != len(trunk.Hellos) {
		t.Fatalf("hub offers %v, edge takes %v", hub, trunk.HelloNames())
	}
	for name := range trunk.Hellos {
		if !hub[name] {
			t.Fatalf("hub does not offer %s", name)
		}
	}
}

func TestTrunkEntryConfig(t *testing.T) {
	env := map[string]string{
		"EDGE_TRUNK_EXIT": "203.0.113.8:443", "EDGE_TRUNK_SNI": "hk.example", "EDGE_TRUNK_KEY": "trunk-key-0123456789abcdef-xyz",
		"EDGE_TRUNK_FP": "chrome_131", "EDGE_TRUNK_MEMBERS": "32", "EDGE_TRUNK_FORWARD": "8053=2053,8443=443", "EDGE_TRUNK_UDP": "8443=443",
	}
	c, err := TrunkEntryConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Members != 32 || c.TCP[8053] != 2053 || c.UDP[8443] != 443 || len(c.UDP) != 1 {
		t.Fatalf("%+v", c)
	}
	for k, bad := range map[string]string{
		"EDGE_TRUNK_EXIT": "hk.example:443", "EDGE_TRUNK_SNI": "203.0.113.8", "EDGE_TRUNK_KEY": "short",
		"EDGE_TRUNK_FP": "chrome", "EDGE_TRUNK_MEMBERS": "0", "EDGE_TRUNK_FORWARD": "",
	} {
		was := env[k]
		env[k] = bad
		if _, err := TrunkEntryConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("%s=%q accepted", k, bad)
		}
		env[k] = was
	}
}
