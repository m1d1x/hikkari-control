package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

// awg31Env is the env the hub plants for a 3.1 relay (internal/entry AwgV3),
// with the values the 06.11 stand ran.
func awg31Env(t *testing.T) map[string]string {
	t.Helper()
	priv, pub := awgKeyPair(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"EDGE_AWG_PRIVATE_KEY": priv, "EDGE_PORT": fmt.Sprint(freeUDPPort(t)), "EDGE_AWG_PROBE_KEY": pub, "EDGE_AWG_PROBE_ADDRESS": "10.66.0.1",
		"EDGE_AWG_JC": "4", "EDGE_AWG_JMIN": "40", "EDGE_AWG_JMAX": "70",
		"EDGE_AWG_S1": "16", "EDGE_AWG_S2": "16", "EDGE_AWG_S3": "16", "EDGE_AWG_S4": "16",
		"EDGE_AWG_H1": "1", "EDGE_AWG_H2": "2", "EDGE_AWG_H3": "3", "EDGE_AWG_H4": "4",
		"EDGE_AWG_HEADER_PROTECTION_KEY":    base64.StdEncoding.EncodeToString(key),
		"EDGE_AWG_CONTENT_PADDING_ADDITION": "10-50",
		"EDGE_AWG_REKEY_AFTER_TIME":         "100-120",
		"EDGE_AWG_REKEY_TIMEOUT":            "5-7",
		"EDGE_AWG_REJECT_AFTER_TIME":        "170-190",
		"EDGE_AWG_KEEPALIVE_TIMEOUT":        "10-15",
		"EDGE_AWG_MAX_HANDSHAKE_ATTEMPTS":   "15-20",
		"EDGE_AWG_RANDOM_TRAILERS":          "1",
		"EDGE_AWG_DISABLE_COOKIES":          "1",
	}
}

// The edge reads every 3.1 field from the hub's env and hands each to the
// device; the device reads them back.
func TestAwg31EnvReachesTheDevice(t *testing.T) {
	env := awg31Env(t)
	cfg, err := AwgConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	v := cfg.Obf.V3
	if !v.RandomTrailers || !v.DisableCookies || v.HeaderProtectionKey != env["EDGE_AWG_HEADER_PROTECTION_KEY"] || len(v.Ranges) != len(awgV3Ranges) {
		t.Fatalf("%+v", v)
	}
	s, err := startAwg(context.Background(), cfg, dialTo("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	keyHex, _ := awgKeyHex(v.HeaderProtectionKey)
	for _, want := range []string{
		"header_protection_key=" + keyHex, "content_padding_addition=10-50", "rekey_after_time=100-120",
		"rekey_timeout=5-7", "reject_after_time=170-190", "keepalive_timeout=10-15",
		"max_handshake_attempts=15-20", "random_trailers=1", "disable_cookies=1",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("device lacks %s", want)
		}
	}
}

// A 3.1 relay carries a 3.1 client's traffic, and gives a client that has the
// same 2.0 fields but not the 3.1 ones no handshake at all: what makes a
// silently downgraded profile a dead one.
func TestAwg31RelayAnswersOnlyA31Client(t *testing.T) {
	site := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "через 3.1") })}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go site.Serve(ln)

	env := awg31Env(t)
	cfg, err := AwgConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	s, err := startAwg(context.Background(), cfg, dialTo(ln.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, serverPub := awgKeyPairFrom(t, cfg.PrivateKey)
	newPriv, newPub := awgKeyPair(t)
	oldPriv, oldPub := awgKeyPair(t)
	if err := s.Set(context.Background(), []string{newPub + " 10.66.0.2", oldPub + " 10.66.0.3"}); err != nil {
		t.Fatal(err)
	}
	url := "http://" + awgFarSite + ":8080/"
	v31 := awgClient(t, serverPub, cfg.Port, "10.66.0.2", cfg.Obf, newPriv)
	if got, err := awgHTTP(v31, url, 20*time.Second); err != nil || got != "через 3.1" {
		t.Fatalf("3.1 client: %q, %v", got, err)
	}
	downgraded := cfg.Obf
	downgraded.V3 = AwgV3{}
	v20 := awgClient(t, serverPub, cfg.Port, "10.66.0.3", downgraded, oldPriv)
	if _, err := awgHTTP(v20, url, 3*time.Second); err == nil {
		t.Fatal("a client without the 3.1 fields got through")
	}
	if stats, _ := s.stats(); !stats[oldPub].lastHandshake.IsZero() {
		t.Fatal("a handshake without header protection completed")
	}
}

// What the device would refuse is refused at start, by its EDGE_AWG_ name.
func TestAwg31EnvNamesWhatIsWrong(t *testing.T) {
	env := awg31Env(t)
	get := func(k string) string { return env[k] }
	for key, bad := range map[string]string{
		"EDGE_AWG_HEADER_PROTECTION_KEY":    "c2hvcnQ=",
		"EDGE_AWG_S3":                       "8",
		"EDGE_AWG_REKEY_AFTER_TIME":         "120-100",
		"EDGE_AWG_CONTENT_PADDING_ADDITION": "10-x",
		"EDGE_AWG_RANDOM_TRAILERS":          "yes",
		"EDGE_AWG_DISABLE_COOKIES":          "true",
	} {
		was := env[key]
		env[key] = bad
		if _, err := AwgConfigFromEnv(get); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%q: %v", key, bad, err)
		}
		env[key] = was
	}
}

// awgKeyPairFrom is the public key of a base64 private one.
func awgKeyPairFrom(t *testing.T, priv string) (string, string) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}
