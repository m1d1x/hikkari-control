package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// pointTLSEnv is the hub's EDGE_HIKKARI_TRUNK_POINT_TLS for points, each
// address with its name and its PEM pair.
func pointTLSEnv(t *testing.T, legacy bool, points ...trunkPointID) string {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString
	for i := range points {
		points[i].Cert, points[i].Key = b64([]byte(points[i].Cert)), b64([]byte(points[i].Key))
	}
	raw, err := json.Marshal(trunkPointTLS{AllowLegacy: legacy, Points: points})
	if err != nil {
		t.Fatal(err)
	}
	return b64(raw)
}

func leafOf(t *testing.T, certPEM string) []byte {
	t.Helper()
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil {
		t.Fatal("no certificate")
	}
	return b.Bytes
}

// The hub's point names are refused when one would make two points one in
// the open: a name shared by two points or with the core, one address for
// two names, a certificate for more than its name, a key two points share.
func TestPointNamesAreEachTheirOwn(t *testing.T) {
	core := "core.example"
	a, aKey := hikkariTestCert(t, "a.example")
	b, bKey := hikkariTestCert(t, "b.example")
	ok := pointTLSEnv(t, false,
		trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: a, Key: aKey},
		trunkPointID{IP: mustAddr("10.0.0.2"), Name: "b.example", Cert: b, Key: bKey})
	cfg, err := parseTrunkPointTLS(ok, core)
	if err != nil || cfg == nil || len(cfg.Points) != 2 || cfg.AllowLegacy {
		t.Fatalf("two points: %+v %v", cfg, err)
	}
	if cfg, err := parseTrunkPointTLS("", core); cfg != nil || err != nil {
		t.Fatalf("no points: %+v %v", cfg, err)
	}
	coreCert, coreKey := hikkariTestCert(t, core)
	for name, env := range map[string]string{
		"one name twice": pointTLSEnv(t, false,
			trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: a, Key: aKey},
			trunkPointID{IP: mustAddr("10.0.0.2"), Name: "a.example", Cert: a, Key: aKey}),
		"the core's name": pointTLSEnv(t, false, trunkPointID{IP: mustAddr("10.0.0.1"), Name: core, Cert: coreCert, Key: coreKey}),
		"one address twice": pointTLSEnv(t, false,
			trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: a, Key: aKey},
			trunkPointID{IP: mustAddr("10.0.0.1"), Name: "b.example", Cert: b, Key: bKey}),
		"another's certificate": pointTLSEnv(t, false, trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: b, Key: bKey}),
		"a broken pair":         pointTLSEnv(t, false, trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: a, Key: bKey}),
		"IPv6":                  pointTLSEnv(t, false, trunkPointID{IP: mustAddr("::1"), Name: "a.example", Cert: a, Key: aKey}),
		"not base64":            "%%%",
	} {
		if _, err := parseTrunkPointTLS(env, core); err == nil {
			t.Fatalf("%s: taken", name)
		}
	}

	// One key under two names: the certificate log would not tie them, the
	// key on the wire would.
	if _, err := parseTrunkPointTLS(pointTLSEnv(t, false,
		trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: a, Key: aKey},
		trunkPointID{IP: mustAddr("10.0.0.2"), Name: "b.example", Cert: reissue(t, "b.example", aKey), Key: aKey}), core); err == nil {
		t.Fatal("one key for two points taken")
	}

	// Two names on one certificate: the certificate log ties them.
	both, bothKey := hikkariTestCertNames(t, "a.example", "b.example")
	if _, err := parseTrunkPointTLS(pointTLSEnv(t, false, trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: both, Key: bothKey}), core); err == nil {
		t.Fatal("a certificate of two names taken")
	}
}

// The core serves each point's name only from that point's address, and
// its own name only from elsewhere; with the transition on, its own name
// from the points too. The handshake is Caddy's, on loopback as point a.
func TestTheCoreAnswersAPointsNameOnlyFromThatPoint(t *testing.T) {
	core := "core.example"
	coreCert, coreKey := hikkariTestCert(t, core)
	a, aKey := hikkariTestCert(t, "a.example")
	b, bKey := hikkariTestCert(t, "b.example")
	for _, legacy := range []bool{false, true} {
		env := pointTLSEnv(t, legacy,
			trunkPointID{IP: mustAddr("127.0.0.1"), Name: "a.example", Cert: a, Key: aKey},
			trunkPointID{IP: mustAddr("192.0.2.9"), Name: "b.example", Cert: b, Key: bKey})
		pts, err := parseTrunkPointTLS(env, core)
		if err != nil {
			t.Fatal(err)
		}
		address := freeTCP(t)
		cfg := naiveConfig{Listen: address, Host: core, User: "probe", Pass: "probe-secret-long-enough", Root: t.TempDir(), Storage: t.TempDir(),
			CertPEM: coreCert, KeyPEM: coreKey, PointTLS: pts}
		raw, err := cfg.document()
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		doc["storage"] = map[string]any{"module": "file_system", "root": t.TempDir()}
		server := doc["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["naive"].(map[string]any)
		server["automatic_https"] = map[string]any{"disable": true}
		server["listener_wrappers"] = nil
		server["routes"] = []any{map[string]any{"handle": []any{map[string]any{"handler": "static_response", "body": "ok"}}}}
		delete(doc, "logging")
		raw, _ = json.Marshal(doc)
		if err := caddy.Load(raw, true); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]string{"a.example": a, "b.example": "", core: map[bool]string{false: "", true: coreCert}[legacy], "other.example": ""} {
			got, err := handshake(address, name)
			switch {
			case want == "" && err == nil:
				t.Errorf("legacy %v: %s answered from point a", legacy, name)
			case want != "" && err != nil:
				t.Errorf("legacy %v: %s refused from point a: %v", legacy, name, err)
			case want != "" && string(got) != string(leafOf(t, want)):
				t.Errorf("legacy %v: %s served another certificate", legacy, name)
			}
		}
		if err := caddy.Stop(); err != nil {
			t.Fatal(err)
		}
	}
}

// The H3 wire's message for hk-exit names each point's files, written once
// under the box's storage.
func TestTheExitHearsEachPointsFiles(t *testing.T) {
	core := "core.example"
	coreCert, coreKey := hikkariTestCert(t, core)
	a, aKey := hikkariTestCert(t, "a.example")
	pts, err := parseTrunkPointTLS(pointTLSEnv(t, true, trunkPointID{IP: mustAddr("10.0.0.1"), Name: "a.example", Cert: a, Key: aKey}), core)
	if err != nil {
		t.Fatal(err)
	}
	n := naiveConfig{Host: core, Storage: t.TempDir(), CertPEM: coreCert, KeyPEM: coreKey, PointTLS: pts}
	x, err := n.exitPointTLS()
	if err != nil {
		t.Fatal(err)
	}
	if x.CoreName != core || !x.AllowLegacy || len(x.Points) != 1 || x.Points[0].SNI != "a.example" || x.Points[0].IP != mustAddr("10.0.0.1") {
		t.Fatalf("exit point TLS %+v", x)
	}
	pair, err := tls.LoadX509KeyPair(x.Points[0].CertFile, x.Points[0].KeyFile)
	if err != nil || string(pair.Certificate[0]) != string(leafOf(t, a)) {
		t.Fatalf("point a's files: %v", err)
	}
	none := naiveConfig{Host: core}
	if x, err := none.exitPointTLS(); x != nil || err != nil {
		t.Fatalf("no points: %+v %v", x, err)
	}
}

func handshake(address, name string) ([]byte, error) {
	d := &net.Dialer{Timeout: 3 * time.Second, LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}}
	c, err := tls.DialWithDialer(d, "tcp", address, &tls.Config{ServerName: name, InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].Raw, nil
}

func freeTCP(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// reissue is a certificate for host under the key in keyPEM.
func reissue(t *testing.T, host, keyPEM string) string {
	t.Helper()
	b, _ := pem.Decode([]byte(keyPEM))
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certUnder(t, []string{host}, k.(*ecdsa.PrivateKey))
}

// hikkariTestCertNames is hikkariTestCert for several names on one
// certificate.
func hikkariTestCertNames(t *testing.T, hosts ...string) (string, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return certUnder(t, hosts, k), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func certUnder(t *testing.T, hosts []string, k *ecdsa.PrivateKey) string {
	t.Helper()
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: hosts,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
