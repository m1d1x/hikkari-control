package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/caddyserver/caddy/v2"
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
)

// Real upstream Naive framing, Caddy TLS/H2/H3 and Chromium client. Loopback
// is permitted only in this fixture's ACL; production keeps egress.go's ACL.
func TestNaiveRealChromiumTrafficAndRevoke(t *testing.T) {
	binary := os.Getenv("HIKKARI_TEST_CORE")
	if binary == "" {
		t.Skip("HIKKARI_TEST_CORE not set")
	}
	key := "0123456789abcdef0123456789abcdef"
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{"naive.example"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, _ := x509.MarshalPKCS8PrivateKey(private)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	for _, quic := range []bool{false, true} {
		t.Run(fmt.Sprintf("quic=%v", quic), func(t *testing.T) {
			stopped := make(chan struct{})
			payload := strings.Repeat("naive-payload-", 81920)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/hold" {
					_, _ = w.Write([]byte("start"))
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					close(stopped)
					return
				}
				_, _ = io.WriteString(w, payload)
			}))
			defer origin.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			cfg := naiveConfig{Listen: address, Host: "naive.example", User: "probe", Pass: "probe-secret-long-enough", H3: quic, Root: t.TempDir()}
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
			client, stop := naiveTestCore(t, binary, address, key, certPEM, quic)
			defer stop()
			response, err := client.Get(origin.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(body) != payload {
				t.Fatalf("transfer: %d bytes, %v", len(body), err)
			}
			up, down, _, usage := n.read()
			if up <= 0 || down < int64(len(payload)) || usage[key].Down < int64(len(payload)) {
				t.Fatalf("traffic not counted: %d/%d %#v", up, down, usage)
			}
			response, err = client.Get(origin.URL + "/hold")
			if err != nil {
				t.Fatal(err)
			}
			start := make([]byte, 5)
			if _, err = io.ReadFull(response.Body, start); err != nil {
				t.Fatal(err)
			}
			if err = n.Set(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("revocation did not close the real destination socket")
			}
			response.Body.Close()
			response, err = client.Get(origin.URL)
			if err == nil {
				data, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if string(data) == payload {
					t.Fatal("revoked key still carries traffic")
				}
			}
			t.Logf("Chromium H3=%v: %d bytes, per-key usage and active-stream revocation passed", quic, len(payload))
		})
	}
}

func naiveTestCore(t *testing.T, binary, address, key, cert string, quic bool) (*http.Client, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	host, remotePort, _ := net.SplitHostPort(address)
	var p int
	_, _ = fmt.Sscan(remotePort, &p)
	cfg := map[string]any{"log": map[string]any{"disabled": true}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}}, "outbounds": []any{map[string]any{"type": "naive", "tag": "proxy", "server": host, "server_port": p, "username": "hikkari", "password": key, "quic": quic, "tls": map[string]any{"enabled": true, "server_name": "naive.example", "certificate": []string{cert}}}}, "route": map[string]any{"final": "proxy"}}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "client.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, "run", "-c", path)
	cmd.Dir = filepath.Dir(binary)
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	stop := func() { cancel(); _ = cmd.Wait() }
	ready := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if e == nil {
			c.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		stop()
		t.Fatal("Chromium core did not start")
	}
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	return &http.Client{Timeout: 10 * time.Second, Transport: transport}, func() { transport.CloseIdleConnections(); stop() }
}
