package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

// A real VLESS + Vision + Reality client through a Reality edge, in one
// process: datagrams ride XUDP inside the session, the echo and a "site"
// answer, another port on the box itself does not. The cover is a local
// TLS 1.3 server standing in for the real site the handshake borrows.
func TestARealityEdgeCarriesDatagramsToTheInternetButNotToTheBox(t *testing.T) {
	cover := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cover.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	cover.StartTLS()
	t.Cleanup(cover.Close)

	priv := bytes.Repeat([]byte{7}, curve25519.ScalarSize)
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	const coverName, uuid, sid = "www.cover.example", "00000000-0000-4000-8000-000000000001", "0123456789abcdef"

	edgePort, echoPort := freePort(t, "tcp"), freePort(t, "udp")
	sitePort, strangerPort := freePort(t, "udp"), freePort(t, "udp")
	udpEcho(t, sitePort, "site")
	udpEcho(t, strangerPort, "stranger")

	c, err := ConfigFromEnv(env(map[string]string{
		"EDGE_LISTEN":          "127.0.0.1:" + strconv.Itoa(edgePort),
		"EDGE_PRIVATE_KEY":     enc.EncodeToString(priv),
		"EDGE_SHORT_IDS":       sid,
		"EDGE_CLIENTS":         uuid,
		"EDGE_COVERS":          coverName,
		"EDGE_LOCAL_PORT_BASE": strconv.Itoa(freePort(t, "tcp")),
		"EDGE_ECHO_PORT":       strconv.Itoa(echoPort),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := serveEcho(t.Context(), c.EchoPort); err != nil {
		t.Fatal(err)
	}
	doc := c.XrayConfig()
	// The handshake borrows the local cover instead of the real site on :443.
	coverAddr := cover.Listener.Addr().String()
	for _, in := range doc["inbounds"].([]map[string]any) {
		if ss, ok := in["streamSettings"].(map[string]any); ok {
			ss["realitySettings"].(map[string]any)["dest"] = coverAddr
		}
	}
	doc["log"] = map[string]any{"loglevel": "none"}
	asInternet(t, doc, "udp", sitePort)
	startXray(t, doc)

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
			"protocol": "vless",
			"settings": map[string]any{"vnext": []map[string]any{{
				"address": "127.0.0.1", "port": edgePort,
				"users": []map[string]any{{"id": uuid, "encryption": "none", "flow": "xtls-rprx-vision"}},
			}}},
			"streamSettings": map[string]any{
				"network":  "tcp",
				"security": "reality",
				"realitySettings": map[string]any{
					"serverName": coverName, "fingerprint": "chrome",
					"publicKey": enc.EncodeToString(pub), "shortId": sid,
				},
			},
		}},
	})

	if got, ok := ask(t, doors["echo"], 10*time.Second); !ok || got != "ping" {
		t.Fatalf("echo through the Reality edge: %q %v", got, ok)
	}
	if got, ok := ask(t, doors["site"], 5*time.Second); !ok || got != "site:ping" {
		t.Fatalf("a site through the Reality edge: %q %v", got, ok)
	}
	if got, ok := ask(t, doors["stranger"], 2*time.Second); ok {
		t.Fatalf("the Reality edge reached a port on the box: %q", got)
	}
}
