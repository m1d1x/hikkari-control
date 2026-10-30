package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
)

const testSNI = "voice.example"

// leaf makes a self-signed certificate the way the hub does
// (internal/entry selfSignedLeaf) and returns it, its key and its pin.
func leaf(t *testing.T) (certPEM, keyPEM, pin string) {
	t.Helper()
	return leafFor(t, testSNI)
}

// leafFor is a self-signed P-256 leaf for name, with its SHA-256 pin.
func leafFor(t *testing.T, name string) (certPEM, keyPEM, pin string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})), hex.EncodeToString(sum[:])
}

// testFinalmask is what the hub renders for Salamander under one junk packet.
const testFinalmask = `[{"type":"salamander","settings":{"password":"pwpwpwpwpwpwpwpwpwpw"}},` +
	`{"type":"noise","settings":{"reset":"0-0","noise":[{"rand":"40-90","delay":"0-2"}]}}]`

func voiceEnv(t *testing.T, port, echo int) (map[string]string, string) {
	t.Helper()
	cert, key, pin := leaf(t)
	b64 := base64.StdEncoding.EncodeToString
	return map[string]string{
		"EDGE_PROTO":        protoHysteria,
		"EDGE_LISTEN":       "127.0.0.1:" + strconv.Itoa(port),
		"EDGE_HY_AUTH":      "aaaaaaaaaaaaaaaaaaaa",
		"EDGE_HY_CERT":      b64([]byte(cert)),
		"EDGE_HY_KEY":       b64([]byte(key)),
		"EDGE_HY_FINALMASK": b64([]byte(testFinalmask)),
		"EDGE_ECHO_PORT":    strconv.Itoa(echo),
	}, pin
}

func TestAVoiceEdgeReportsEveryProblemAtOnce(t *testing.T) {
	_, err := VoiceConfigFromEnv(env(map[string]string{"EDGE_HY_FINALMASK": base64.StdEncoding.EncodeToString([]byte(`{"type":"prefix"}`))}))
	if err == nil {
		t.Fatal("empty env must fail")
	}
	for _, name := range []string{"EDGE_HY_AUTH", "EDGE_HY_CERT", "EDGE_HY_KEY", "EDGE_ECHO_PORT", "EDGE_HY_FINALMASK"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return x
	case []string:
		return "[" + strings.Join(x, " ") + "]"
	}
	return "?"
}

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// udpEcho answers datagrams on 127.0.0.1:port, prefixed with tag so a reply
// names who answered.
func udpEcho(t *testing.T, port int, tag string) {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = c.WriteToUDP(append([]byte(tag+":"), buf[:n]...), from)
		}
	}()
}

func startXray(t *testing.T, doc map[string]any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := core.LoadConfig("json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("xray rejects the config: %v\n%s", err, raw)
	}
	inst, err := core.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close() })
}

// ask sends one datagram to the client's local door and waits for a reply.
func ask(t *testing.T, door int, wait time.Duration) (string, bool) {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: door})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	deadline := time.Now().Add(wait)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if n, err := c.Read(buf); err == nil {
			return string(buf[:n]), true
		}
	}
	return "", false
}

// A real Hysteria2 client — pinned certificate, the same masks — through the
// edge, in one process: a "site" and the echo answer, another port on the box
// does not, and the masks on the two ends agree (a mismatch is a silent
// tunnel, which is what this would show).
func TestAHysteriaEdgeCarriesDatagramsToTheInternetButNotToTheBox(t *testing.T) {
	edgePort, echoPort := freePort(t, "udp"), freePort(t, "udp")
	sitePort, strangerPort := freePort(t, "udp"), freePort(t, "udp")
	udpEcho(t, sitePort, "site")
	udpEcho(t, strangerPort, "stranger")

	m, pin := voiceEnv(t, edgePort, echoPort)
	c, err := VoiceConfigFromEnv(env(m))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := serveEcho(ctx, c.EchoPort); err != nil {
		t.Fatal(err)
	}
	doc := c.XrayConfig()
	asInternet(t, doc, "udp", sitePort)
	startXray(t, doc)

	// The client: one door per destination, all through one Hysteria outbound.
	dests := map[string]int{"echo": echoPort, "site": sitePort, "stranger": strangerPort}
	doors := map[string]int{}
	var inbounds []map[string]any
	for name, port := range dests {
		door := freePort(t, "udp")
		doors[name] = door
		inbounds = append(inbounds, map[string]any{
			"tag": name, "listen": "127.0.0.1", "port": door, "protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1", "port": port, "network": "udp"},
		})
	}
	startXray(t, map[string]any{
		"log":      map[string]any{"loglevel": "none"},
		"inbounds": inbounds,
		"outbounds": []map[string]any{{
			"protocol": "hysteria",
			"settings": map[string]any{"version": 2, "address": "127.0.0.1", "port": edgePort},
			"streamSettings": map[string]any{
				"network":          "hysteria",
				"hysteriaSettings": map[string]any{"version": 2, "auth": m["EDGE_HY_AUTH"]},
				"security":         "tls",
				"tlsSettings": map[string]any{
					"serverName": testSNI, "alpn": []string{"h3"}, "pinnedPeerCertSha256": pin,
				},
				"finalmask": map[string]any{
					"udp":        json.RawMessage(testFinalmask),
					"quicParams": map[string]any{"disableChromeParrot": true},
				},
			},
		}},
	})

	if got, ok := ask(t, doors["echo"], 10*time.Second); !ok || got != "ping" {
		t.Fatalf("echo through the edge: %q %v", got, ok)
	}
	if got, ok := ask(t, doors["site"], 5*time.Second); !ok || got != "site:ping" {
		t.Fatalf("a site through the edge: %q %v", got, ok)
	}
	if got, ok := ask(t, doors["stranger"], 2*time.Second); ok {
		t.Fatalf("the edge reached a port on the box: %q", got)
	}
}
