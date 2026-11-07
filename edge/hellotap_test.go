package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
)

func TestProxyV2Header(t *testing.T) {
	v4 := proxyV2(&net.TCPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 51000}, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 443})
	want := append(append([]byte(nil), proxyV2Sig...), 0x21, 0x11, 0x00, 12, 203, 0, 113, 7, 10, 0, 0, 1, 0xc7, 0x38, 0x01, 0xbb)
	if !bytes.Equal(v4, want) {
		t.Fatalf("v4 % x", v4)
	}
	v6 := proxyV2(&net.TCPAddr{IP: net.ParseIP("2001:db8::7"), Port: 1}, &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 443})
	if len(v6) != 16+36 || v6[12] != 0x21 || v6[13] != 0x21 || v6[15] != 36 {
		t.Fatalf("v6 % x", v6)
	}
	if local := proxyV2(&net.UnixAddr{}, &net.UnixAddr{}); len(local) != 16 || local[12] != 0x20 {
		t.Fatalf("local % x", local)
	}
}

func TestTapCountsPerCoverAndStaysBounded(t *testing.T) {
	tap := newHelloTap([]string{"wire-mirror.example"})
	for _, name := range []string{"tls-chrome", "reality-chrome", "tls-firefox"} {
		cover, hk := tap.classify(bytes.NewReader(append([]byte{22, 3, 1, byte(len(readHello(t, name)) >> 8), byte(len(readHello(t, name)))}, readHello(t, name)...)))
		tap.count(cover, hk)
	}
	browser := readHello(t, "browser-chromium-152-pane-a")
	cover, hk := tap.classify(bytes.NewReader(append([]byte{22, 3, 1, byte(len(browser) >> 8), byte(len(browser))}, browser...)))
	tap.count(cover, hk)
	cover, hk = tap.classify(bytes.NewReader([]byte("GET / HTTP/1.1\r\n\r\n")))
	tap.count(cover, hk)
	got := tap.totals()
	if got["wire-mirror.example"]["h1e431ec7444af069a"] != 2 || got["wire-mirror.example"]["h1a7f29bb57cd8c350"] != 1 ||
		got[tapNoCover]["h1e08906f1ba730b11"] != 1 || got[tapNoCover][tapUnread] != 1 {
		t.Fatalf("%v", got)
	}
	for i := 0; i < tapMaxKeys*2; i++ {
		tap.count(tapNoCover, fmt.Sprintf("h1%016x", i))
	}
	keys := 0
	for _, byHK := range tap.totals() {
		keys += len(byHK)
	}
	if keys != tapMaxKeys+1 || tap.totals()[tapNoCover][tapOther] == 0 {
		t.Fatalf("%d keys, other %d", keys, tap.totals()[tapNoCover][tapOther])
	}
}

func TestTapConfigMovesTheRouterBehindTheTap(t *testing.T) {
	m := good()
	m["EDGE_HELLO_TAP"] = "1"
	c, err := ConfigFromEnv(env(m))
	if err != nil || !c.HelloTap || c.TapPort() != 10003 {
		t.Fatalf("%+v %v", c, err)
	}
	router := c.XrayConfig()["inbounds"].([]map[string]any)[0]
	if router["listen"] != "127.0.0.1" || router["port"] != 10003 {
		t.Fatalf("%v", router)
	}
	raw, _ := json.Marshal(c.XrayConfig())
	if !bytes.Contains(raw, []byte(`"acceptProxyProtocol":true`)) {
		t.Fatal("no PROXY protocol on the router")
	}
	xc, err := core.LoadConfig("json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("xray rejects the config: %v", err)
	}
	inst, err := core.New(xc)
	if err != nil {
		t.Fatal(err)
	}
	_ = inst.Close()
	m["EDGE_ECHO_PORT"] = "10003"
	if _, err := ConfigFromEnv(env(m)); err == nil {
		t.Fatal("echo on the tap's port")
	}
}

// The whole path: a client sends a hello and more, the tap counts it and
// hands it to a real Xray inbound that takes PROXY v2, which passes it on
// untouched to a server that echoes. Every byte comes back, and none of the
// header: Xray took it as the header, not as data.
func TestTapHandsEverythingToXray(t *testing.T) {
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
	router := freePort(t, "tcp")
	doc := map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []map[string]any{{
			"tag": "sni-router", "listen": "127.0.0.1", "port": router, "protocol": "dokodemo-door",
			"settings":       map[string]any{"address": "127.0.0.1", "port": echo.Addr().(*net.TCPAddr).Port, "network": "tcp"},
			"streamSettings": map[string]any{"sockopt": map[string]any{"acceptProxyProtocol": true}},
		}},
		"outbounds": []map[string]any{{"tag": "direct", "protocol": "freedom"}},
	}
	raw, _ := json.Marshal(doc)
	xc, err := core.LoadConfig("json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := core.New(xc)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer inst.Close()

	tap := newHelloTap([]string{"wire-mirror.example"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tap.serve(ctx, l, fmt.Sprintf("127.0.0.1:%d", router))

	msg := readHello(t, "tls-chrome_155")
	// The hello in two records, as a post-quantum share can split it, then data.
	half := len(msg) / 2
	var sent []byte
	sent = append(sent, 22, 3, 1, byte(half>>8), byte(half))
	sent = append(sent, msg[:half]...)
	sent = append(sent, 22, 3, 1, byte((len(msg)-half)>>8), byte(len(msg)-half))
	sent = append(sent, msg[half:]...)
	sent = append(sent, []byte("after the hello")...)

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(sent); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(sent))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("echo: %v", err)
	}
	if !bytes.Equal(got, sent) {
		t.Fatalf("bytes changed on the way: %q", got[:min(32, len(got))])
	}
	if n := tap.totals()["wire-mirror.example"]["h1121e2b99d2744911"]; n != 1 {
		t.Fatalf("%v", tap.totals())
	}
}
