package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// Hikkari v1.01 end to end with the real client core (HIKKARI_TEST_CORE, a
// sing-box-lx build with the hikkari outbound): TCP over the Naive channel,
// UDP over the datagram channel, and UDP over the Naive channel when the
// datagram channel is off or unreachable. Loopback egress is allowed only here.
func TestHikkariRealCoreTwoChannels(t *testing.T) {
	binary := os.Getenv("HIKKARI_TEST_CORE")
	if binary == "" {
		t.Skip("HIKKARI_TEST_CORE not set")
	}
	exitPolicy = func() *egress { return &egress{} }
	key := "0123456789abcdef0123456789abcdef"
	certPEM, keyPEM := hikkariTestCert(t, "hk.example")

	payload := strings.Repeat("hikkari-tcp-", 40000)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, payload) }))
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

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	storage := t.TempDir()
	certDir := filepath.Join(storage, "certificates", "test", "hk.example")
	_ = os.MkdirAll(certDir, 0700)
	_ = os.WriteFile(filepath.Join(certDir, "hk.example.crt"), []byte(certPEM), 0600)
	_ = os.WriteFile(filepath.Join(certDir, "hk.example.key"), []byte(keyPEM), 0600)
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>site</html>"), 0644)

	cfg := naiveConfig{Listen: address, Host: "hk.example", User: "probe", Pass: "probe-secret-long-enough", Root: root, Storage: storage, UDP: true}
	n := newNaiveRuntime(cfg)
	_ = n.Set(context.Background(), []string{key})
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
	handlers := server["routes"].([]any)[0].(map[string]any)["handle"].([]any)
	handlers[1].(map[string]any)["acl"] = []any{map[string]any{"subjects": []string{"127.0.0.1"}, "allow": true}}
	raw, _ = json.Marshal(doc)
	if err = caddy.Load(raw, true); err != nil {
		t.Fatal(err)
	}
	defer caddy.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	udp, err := startHikkariUDP(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()

	_, port, _ := net.SplitHostPort(address)
	deadPort := hikkariFreeUDPPort(t)
	for _, tc := range []struct {
		name    string
		udp     map[string]any
		channel string
	}{
		{"datagram", nil, "datagram"},
		{"udp disabled", map[string]any{"disabled": true}, "uot"},
		{"datagram unreachable", map[string]any{"server_port": deadPort, "connect_timeout": "1s"}, "uot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uotBefore, dgBefore := n.uotFlows.Load(), n.datagramFlows.Load()
			outbound := map[string]any{"type": "hikkari", "tag": "proxy", "server": "127.0.0.1", "server_port": atoi(port), "password": key,
				"tls": map[string]any{"enabled": true, "server_name": "hk.example", "certificate": []string{certPEM}}}
			if tc.udp != nil {
				outbound["udp"] = tc.udp
			}
			mixed, stop := hikkariTestCore(t, binary, outbound)
			defer stop()

			proxy, _ := url.Parse("http://" + mixed)
			client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
			response, err := client.Get(origin.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) != payload {
				t.Fatalf("tcp: %d bytes", len(body))
			}

			sent, got, gaps := socksUDPEcho(t, mixed, echo.LocalAddr().(*net.UDPAddr), 100)
			if got < sent*9/10 {
				t.Fatalf("udp: %d of %d answered", got, sent)
			}
			uot, dg := n.uotFlows.Load()-uotBefore, n.datagramFlows.Load()-dgBefore
			switch tc.channel {
			case "datagram":
				if dg == 0 || uot != 0 {
					t.Fatalf("udp went datagram=%d uot=%d, want datagram", dg, uot)
				}
			case "uot":
				if uot == 0 || dg != 0 {
					t.Fatalf("udp went datagram=%d uot=%d, want uot", dg, uot)
				}
			}
			t.Logf("tcp %d bytes; udp %d/%d echoed over %s, worst gap %v", len(body), got, sent, tc.channel, gaps)
		})
	}

	// Revoking the key closes the datagram channel's door too.
	_ = n.Set(context.Background(), nil)
	outbound := map[string]any{"type": "hikkari", "tag": "proxy", "server": "127.0.0.1", "server_port": atoi(port), "password": key,
		"tls": map[string]any{"enabled": true, "server_name": "hk.example", "certificate": []string{certPEM}}}
	mixed, stop := hikkariTestCore(t, binary, outbound)
	defer stop()
	if _, got, _ := socksUDPEcho(t, mixed, echo.LocalAddr().(*net.UDPAddr), 10); got != 0 {
		t.Fatalf("revoked key: %d datagrams echoed", got)
	}
}

func atoi(s string) int { var p int; _, _ = fmt.Sscan(s, &p); return p }

func hikkariFreeUDPPort(t *testing.T) int {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func hikkariTestCert(t *testing.T, host string) (string, string) {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{host}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(private)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

// hikkariTestCore runs the client core with a mixed inbound in front of the
// given outbound and returns the inbound's address.
func hikkariTestCore(t *testing.T, binary string, outbound map[string]any) (string, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mixed := l.Addr().String()
	l.Close()
	host, port, _ := net.SplitHostPort(mixed)
	cfg := map[string]any{"log": map[string]any{"level": "warn"}, "inbounds": []any{map[string]any{"type": "mixed", "listen": host, "listen_port": atoi(port)}}, "outbounds": []any{outbound}, "route": map[string]any{"final": "proxy"}}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "client.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, "run", "-c", path)
	cmd.Dir = filepath.Dir(binary)
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	stop := func() { cancel(); _ = cmd.Wait() }
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", mixed, 100*time.Millisecond); err == nil {
			c.Close()
			return mixed, stop
		}
	}
	stop()
	t.Fatal("client core did not start")
	return "", nil
}

// socksUDPEcho sends count datagrams of 160 bytes every 20 ms (a call) through
// SOCKS5 UDP ASSOCIATE and counts the echoes; gap is the longest silence.
func socksUDPEcho(t *testing.T, socks string, target *net.UDPAddr, count int) (int, int, time.Duration) {
	t.Helper()
	ctrl, err := net.Dial("tcp", socks)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	reply := make([]byte, 64)
	_, _ = ctrl.Write([]byte{5, 1, 0})
	if _, err = io.ReadFull(ctrl, reply[:2]); err != nil {
		t.Fatal(err)
	}
	_, _ = ctrl.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	if _, err = io.ReadFull(ctrl, reply[:10]); err != nil || reply[1] != 0 {
		t.Fatalf("udp associate: %v %v", reply[:10], err)
	}
	relay := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:10]))}
	if relay.IP.IsUnspecified() {
		relay.IP = net.IPv4(127, 0, 0, 1)
	}
	conn, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	header := []byte{0, 0, 0, 1}
	header = append(header, target.IP.To4()...)
	header = binary.BigEndian.AppendUint16(header, uint16(target.Port))
	got := make(chan time.Time, count)
	go func() {
		b := make([]byte, 2048)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Read(b); err != nil {
				close(got)
				return
			}
			got <- time.Now()
		}
	}()
	packet := append(header, make([]byte, 160)...)
	for i := 0; i < count; i++ {
		_, _ = conn.Write(packet)
		time.Sleep(20 * time.Millisecond)
	}
	answered := 0
	var last time.Time
	var gap time.Duration
	for at := range got {
		if !last.IsZero() && at.Sub(last) > gap {
			gap = at.Sub(last)
		}
		last = at
		answered++
	}
	return count, answered, gap
}
