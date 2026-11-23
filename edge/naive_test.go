package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func naiveRequest(key string) *http.Request {
	r := httptest.NewRequest(http.MethodConnect, "https://example.com:443", strings.NewReader("payload"))
	r.ProtoMajor = 2
	r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("hikkari:"+key)))
	return r
}
func TestNaiveKeysCountersAndRevocation(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	n := newNaiveRuntime(naiveConfig{User: "probe", Pass: "probe-probe-probe-probe"})
	gate := &naiveGate{runtime: n}
	if err := n.Set(context.Background(), []string{key}); err != nil {
		t.Fatal(err)
	}
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("Proxy-Authorization") == naiveRequest(key).Header.Get("Proxy-Authorization") {
			t.Fatal("subscriber key reached upstream handler")
		}
		_, err := io.Copy(w, r.Body)
		return err
	})
	if err := gate.ServeHTTP(httptest.NewRecorder(), naiveRequest(key), next); err != nil {
		t.Fatal(err)
	}
	up, down, conns, keys := n.read()
	if up != 7 || down != 7 || conns != 0 || keys[key].Up != 7 || keys[key].Down != 7 {
		t.Fatalf("totals: %d %d %d %#v", up, down, conns, keys)
	}
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gate.ServeHTTP(httptest.NewRecorder(), naiveRequest(key), caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			close(entered)
			<-r.Context().Done()
			return r.Context().Err()
		}))
	}()
	<-entered
	if err := n.Set(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revoked stream stayed live")
	}
	// A revoked subscriber's original header must reach the hidden upstream
	// rejection, never be rewritten into the relay's shared credential.
	_ = gate.ServeHTTP(httptest.NewRecorder(), naiveRequest(key), caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("Proxy-Authorization") != naiveRequest(key).Header.Get("Proxy-Authorization") {
			t.Fatal("revoked key authenticated")
		}
		return nil
	}))
	_, _, _, keys = n.read()
	if keys[key].Up != 7 || keys[key].Down != 7 {
		t.Fatal("revocation discarded usage totals")
	}
}

func TestHikkariFromEnv(t *testing.T) {
	env := map[string]string{"EDGE_PROTO": "hikkari", "EDGE_HIKKARI_HOST": "hk.example.com", "EDGE_HIKKARI_USER": "hikkari", "EDGE_HIKKARI_PASS": "0123456789abcdef0123", "EDGE_NAIVE_HOST": "wrong.example.com"}
	cfg, err := naiveFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UDP || cfg.Host != "hk.example.com" || cfg.H3 {
		t.Fatalf("hikkari config: %+v", cfg)
	}
	env["EDGE_PROTO"] = "naive"
	env["EDGE_NAIVE_USER"], env["EDGE_NAIVE_PASS"] = "probe", "0123456789abcdef0123"
	cfg, err = naiveFromEnv(func(k string) string { return env[k] })
	if err != nil || cfg.UDP || cfg.Host != "wrong.example.com" {
		t.Fatalf("naive config: %+v %v", cfg, err)
	}
}

// The receiver group's certificate from the hub replaces Caddy's own order
// (P1); a pair for another name, a broken pair or half of one is refused at
// start, while the old generation still serves.
func TestTheGroupCertificateReplacesCaddysOrder(t *testing.T) {
	certFor := func(host string, notAfter time.Time) (string, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
		der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		kder, _ := x509.MarshalECPrivateKey(key)
		b64 := base64.StdEncoding.EncodeToString
		return b64(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), b64(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	}
	env := map[string]string{"EDGE_PROTO": "hikkari", "EDGE_HIKKARI_HOST": "otter9119.lol", "EDGE_HIKKARI_USER": "hikkari", "EDGE_HIKKARI_PASS": "0123456789abcdef0123"}
	get := func(k string) string { return env[k] }

	cfg, err := naiveFromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := cfg.document()
	if !strings.Contains(string(doc), `"automate":["otter9119.lol"]`) {
		t.Fatalf("no certificate from the hub, yet Caddy does not order: %s", doc)
	}

	env["EDGE_HIKKARI_CERT"], env["EDGE_HIKKARI_KEY"] = certFor("otter9119.lol", time.Now().Add(90*24*time.Hour))
	if cfg, err = naiveFromEnv(get); err != nil {
		t.Fatal(err)
	}
	doc, _ = cfg.document()
	if strings.Contains(string(doc), "automate") || !strings.Contains(string(doc), "load_pem") {
		t.Fatalf("the hub's certificate is not what Caddy serves: %s", doc)
	}

	good, goodKey := env["EDGE_HIKKARI_CERT"], env["EDGE_HIKKARI_KEY"]
	other, _ := certFor("otter9119.lol", time.Now().Add(time.Hour))
	for name, pair := range map[string][2]string{
		"another name": func() [2]string { c, k := certFor("pine6573.lol", time.Now().Add(time.Hour)); return [2]string{c, k} }(),
		"expired": func() [2]string {
			c, k := certFor("otter9119.lol", time.Now().Add(-time.Minute))
			return [2]string{c, k}
		}(),
		"key of another": {other, goodKey},
		"no key":         {good, ""},
		"not base64":     {"%%%", goodKey},
	} {
		env["EDGE_HIKKARI_CERT"], env["EDGE_HIKKARI_KEY"] = pair[0], pair[1]
		if _, err := naiveFromEnv(get); err == nil {
			t.Fatalf("%s: started", name)
		}
	}
}

// The group's certificate is the datagram channel's and hk-exit's too
// (P1): a spare receiver has no certificate of Caddy's own in its storage,
// so a channel reading only that one never answers there.
func TestTheGroupCertificateIsEveryChannels(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), DNSNames: []string{"otter9119.lol"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	n := naiveConfig{Host: "otter9119.lol", Storage: t.TempDir(),
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))}

	get, err := n.groupCertificate()
	if err != nil || get == nil {
		t.Fatalf("no certificate for the datagram channel: %v", err)
	}
	c, err := get(&tls.ClientHelloInfo{ServerName: "otter9119.lol"})
	if err != nil || len(c.Certificate) == 0 || string(c.Certificate[0]) != string(der) {
		t.Fatalf("the datagram channel serves another certificate: %v", err)
	}
	cert, keyFile, err := n.groupCertFiles()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(cert, keyFile); err != nil {
		t.Fatalf("hk-exit's files: %v", err)
	}
	if st, err := os.Stat(keyFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the key's file: %v %v", st.Mode(), err)
	}

	none := naiveConfig{Host: "otter9119.lol", Storage: t.TempDir()}
	if get, _ := none.groupCertificate(); get != nil {
		t.Fatal("a certificate without the hub's")
	}
	if cert, _, _ := none.groupCertFiles(); cert != "" {
		t.Fatal("files without the hub's certificate")
	}
}

// Decision 2026-11-24: every relay of the group, with the hub's group
// certificate, holds one stateless reset key, so the relay of another
// country resets a client the cascade's entry moved there; another group's
// relay holds another key, and a relay without one keeps a random key.
func TestTheGroupsRelaysShareOneStatelessResetKey(t *testing.T) {
	certA, keyA := hikkariTestCert(t, "otter9119.lol")
	certB, keyB := hikkariTestCert(t, "pine6573.lol")
	nl := naiveConfig{Host: "otter9119.lol", CertPEM: certA, KeyPEM: keyA}
	de := naiveConfig{Host: "otter9119.lol", CertPEM: certA, KeyPEM: keyA}
	other := naiveConfig{Host: "pine6573.lol", CertPEM: certB, KeyPEM: keyB}
	keys := map[string]*[32]byte{}
	for name, n := range map[string]naiveConfig{"nl": nl, "de": de, "other": other} {
		k, err := n.groupResetKey()
		if err != nil || k == nil {
			t.Fatalf("%s: no reset key: %v", name, err)
		}
		keys[name] = (*[32]byte)(k)
	}
	if *keys["nl"] != *keys["de"] {
		t.Fatal("two relays of one group hold different reset keys")
	}
	if *keys["nl"] == *keys["other"] {
		t.Fatal("relays of two groups hold one reset key")
	}
	if k, err := (naiveConfig{Host: "otter9119.lol"}).groupResetKey(); k != nil || err != nil {
		t.Fatalf("a relay without the group's certificate: key %v, err %v", k, err)
	}
}
