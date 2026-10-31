package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
	"golang.org/x/crypto/curve25519"
)

// asInternet lets the exit of a rendered config reach the given loopback
// ports, standing in for public sites in a test that has no internet. The
// rules are prepended, so the real denylist still refuses every other
// loopback port — which is what the tests check next to it.
func asInternet(t *testing.T, doc map[string]any, network string, ports ...int) {
	t.Helper()
	for _, o := range doc["outbounds"].([]map[string]any) {
		if o["tag"] != directTag {
			continue
		}
		settings := o["settings"].(map[string]any)
		var allow []map[string]any
		for _, p := range ports {
			allow = append(allow, map[string]any{
				"action": "allow", "network": network, "ip": []string{"127.0.0.1/32", "::1/128"}, "port": strconv.Itoa(p),
			})
		}
		settings["finalRules"] = append(allow, settings["finalRules"].([]map[string]any)...)
		return
	}
	t.Fatal("no direct outbound in the config")
}

// The exit's rules, in order: the echo, SMTP, the private and Russian networks,
// then the internet. Shared by all three protocols.
func TestTheExitRefusesTheBoxThePrivateNetsAndSMTP(t *testing.T) {
	rules := egressFinalRules(10100)
	got := make([]string, len(rules))
	for i, r := range rules {
		got[i] = strings.Join([]string{r["action"].(string), toString(r["network"]), toString(r["port"])}, " ")
	}
	want := []string{"allow udp 10100", "block <nil> 25", "block <nil> <nil>", "allow <nil> <nil>"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rules %v, want %v", got, want)
	}
	nets := toString(rules[2]["ip"])
	for _, n := range []string{
		"127.0.0.0/8", "10.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10", "fc00::/7", "::1/128",
		"77.88.0.0/18", "185.71.76.0/22", "91.194.226.0/23", "5.136.0.0/13", "2a02:6b8::/32",
	} {
		if !strings.Contains(nets, n) {
			t.Errorf("denylist misses %s", n)
		}
	}
	var prefixes []netip.Prefix
	for _, cidr := range rules[2]["ip"].([]string) {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			t.Fatalf("invalid CIDR in egressFinalRules: %q: %v", cidr, err)
		}
		prefixes = append(prefixes, p)
	}
	for _, sample := range []string{
		"77.88.55.242",  // Yandex
		"185.71.76.1",   // VK
		"194.54.14.1",   // Sberbank
		"91.194.226.10", // T-Bank
		"5.136.10.20",   // Rostelecom
		"176.59.10.1",   // Tele2 RU
		"2a02:6b8::1",   // Yandex IPv6
	} {
		ip := netip.MustParseAddr(sample)
		covered := false
		for _, p := range prefixes {
			if p.Contains(ip) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("denylist does not cover Russian IP %s", sample)
		}
	}
	if len(blockedRUNets) < 8000 {
		t.Fatalf("expected full RU CIDR table (>8000 blocks), got %d", len(blockedRUNets))
	}
	if toString(rules[3]["ip"]) != "[0.0.0.0/0 ::/0]" {
		t.Fatalf("last rule must open the internet: %v", rules[3])
	}
	if r := egressFinalRules(0); r[0]["action"] != "block" {
		t.Fatalf("no echo, no echo rule: %v", r[0])
	}

	er := egressRules([]string{"in-a"})
	if len(er) != 3 || er[0]["outboundTag"] != dropTag || er[1]["outboundTag"] != dropTag || er[2]["outboundTag"] != directTag {
		t.Fatalf("egressRules shape: %v", er)
	}
	domains := toString(er[0]["domain"])
	for _, d := range []string{
		"domain:ru", "domain:xn--p1ai", "domain:su",
		"domain:yandex.com", "domain:vk.com", "domain:mail.ru",
		"domain:sberbank.ru", "domain:tbank.ru", "domain:gosuslugi.ru",
		"domain:ozon.ru", "domain:wildberries.ru",
	} {
		if !strings.Contains(domains, d) {
			t.Errorf("blockedDomains misses %s", d)
		}
	}
}

func TestEveryProtocolLeavesThroughTheExit(t *testing.T) {
	c, _ := ConfigFromEnv(env(good()))
	m, _ := voiceEnv(t, 8443, 10001)
	v, err := VoiceConfigFromEnv(env(m))
	if err != nil {
		t.Fatal(err)
	}
	x, _ := xhttpEnv(t, 443, 10001)
	xc, err := XhttpConfigFromEnv(env(x))
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]map[string]any{"reality": c.XrayConfig(), "hysteria2": v.XrayConfig(), "xhttp": xc.XrayConfig()} {
		foundDirect, foundDrop := false, false
		for _, o := range doc["outbounds"].([]map[string]any) {
			if o["tag"] == directTag {
				foundDirect = true
			}
			if o["tag"] == dropTag && o["protocol"] == "blackhole" {
				foundDrop = true
			}
		}
		if !foundDirect || !foundDrop {
			t.Errorf("%s: missing direct (%v) or drop (%v) outbound", name, foundDirect, foundDrop)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		cfg, err := core.LoadConfig("json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: xray rejects config: %v", name, err)
		}
		inst, err := core.New(cfg)
		if err != nil {
			t.Fatalf("%s: xray cannot build instance: %v", name, err)
		}
		_ = inst.Close()
	}
}

// Real Xray tunnels across all three edge protocols (xhttp, reality, hysteria2)
// verify that Russian domains (.ru, .рф, .su, ecosystems), Russian IP ranges
// from the embedded RIPE table, and raw-IP connections carrying a Russian
// HTTP Host / TLS SNI are blackholed at the edge while ordinary internet
// destinations pass through to the exit.
func TestTheEdgeBlocksRussianDomainsAndIPsWhileAllowingTheInternet(t *testing.T) {
	targets := map[string]string{
		"open-com":       "example.com",
		"open-org":       "wikipedia.org",
		"open-ip":        "1.1.1.1",
		"ru-tld":         "gosuslugi.ru",
		"ru-sub":         "online.sberbank.ru",
		"rf-tld":         "xn--80a1acny.xn--p1ai",
		"su-tld":         "ussr.su",
		"vk-com":         "vk.com",
		"yandex-com":     "yandex.com",
		"tbank":          "tbank.ru",
		"ozon-cdn":       "ozonusercontent.com",
		"wildberries":    "wildberries.ru",
		"ru-ip-yandex":   "77.88.55.242",
		"ru-ip-vk":       "185.71.76.1",
		"ru-ip-sber":     "194.54.14.1",
		"ru-ip-tbank":    "91.194.226.10",
		"ru-ip-rt":       "5.136.10.20",
		"ru-ip-regional": "176.59.10.1",
	}
	allowedList := []string{"open-com", "open-org", "open-ip"}
	blockedList := []string{
		"ru-tld", "ru-sub", "rf-tld", "su-tld",
		"vk-com", "yandex-com", "tbank", "ozon-cdn", "wildberries",
		"ru-ip-yandex", "ru-ip-vk", "ru-ip-sber", "ru-ip-tbank", "ru-ip-rt", "ru-ip-regional",
	}

	redirectExit := func(t *testing.T, doc map[string]any, network string, sitePort int) {
		t.Helper()
		asInternet(t, doc, network, sitePort)
		for _, o := range doc["outbounds"].([]map[string]any) {
			if o["tag"] == directTag {
				o["settings"].(map[string]any)["redirect"] = "127.0.0.1:" + strconv.Itoa(sitePort)
			}
		}
	}

	checkTCPTunnel := func(t *testing.T, clientOutbound map[string]any, sitePort int) {
		t.Helper()
		doors := map[string]int{}
		var inbounds []map[string]any
		for name, addr := range targets {
			door := freePort(t, "tcp")
			doors[name] = door
			inbounds = append(inbounds, map[string]any{
				"tag": "door-" + name, "listen": "127.0.0.1", "port": door, "protocol": "dokodemo-door",
				"settings": map[string]any{"address": addr, "port": sitePort, "network": "tcp"},
			})
		}
		startXray(t, map[string]any{
			"log":       map[string]any{"loglevel": "none"},
			"inbounds":  inbounds,
			"outbounds": []map[string]any{clientOutbound},
		})
		for _, allowed := range allowedList {
			var got string
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if got = talk(t, doors[allowed], time.Second); got == "site:ping" {
					break
				}
				time.Sleep(150 * time.Millisecond)
			}
			if got != "site:ping" {
				t.Fatalf("%s (%s) should pass through the exit, got %q", allowed, targets[allowed], got)
			}
		}
		for _, blocked := range blockedList {
			if got := talk(t, doors[blocked], 350*time.Millisecond); got != "" {
				t.Fatalf("%s (%s) must be blocked by the edge, got %q", blocked, targets[blocked], got)
			}
		}
		// Sniffing check: connecting by raw IP 1.1.1.1 (open-ip) but sending an
		// HTTP request with Host: sberbank.ru must be caught by tunnelSniffing().
		c, err := net.DialTimeout("tcp4", "127.0.0.1:"+strconv.Itoa(doors["open-ip"]), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(400 * time.Millisecond))
		_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: sberbank.ru\r\n\r\n"))
		buf := make([]byte, 256)
		if n, err := c.Read(buf); err == nil && n > 0 {
			t.Fatalf("raw IP with sniffed Host: sberbank.ru escaped: %q", string(buf[:n]))
		}
	}

	t.Run("xhttp", func(t *testing.T) {
		edgePort, local, sitePort := freePort(t, "tcp"), freePort(t, "tcp"), freePort(t, "tcp")
		tcpEcho(t, sitePort, "site")
		m, pin := xhttpEnv(t, edgePort, local)
		c, err := XhttpConfigFromEnv(env(m))
		if err != nil {
			t.Fatal(err)
		}
		doc := c.XrayConfig()
		redirectExit(t, doc, "tcp", sitePort)
		startXray(t, doc)
		if err := serveFront(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		checkTCPTunnel(t, map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []map[string]any{{
				"address": "127.0.0.1", "port": edgePort,
				"users": []map[string]any{{"id": testClient, "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{
				"network":       "xhttp",
				"security":      "tls",
				"xhttpSettings": map[string]any{"host": testDomain, "path": testPath, "mode": "stream-one"},
				"tlsSettings": map[string]any{
					"serverName":           testDomain,
					"fingerprint":          "chrome",
					"alpn":                 []string{"h2", "http/1.1"},
					"pinnedPeerCertSha256": pin,
				},
			},
		}, sitePort)
	})

	t.Run("reality", func(t *testing.T) {
		cover := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		cover.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
		cover.StartTLS()
		t.Cleanup(cover.Close)

		priv := bytes.Repeat([]byte{7}, curve25519.ScalarSize)
		priv[0] &= 248
		priv[31] &= 127
		priv[31] |= 64
		pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
		enc := base64.RawURLEncoding
		const coverName, uuid, sid = "www.cover.example", "00000000-0000-4000-8000-000000000001", "0123456789abcdef"

		edgePort, echoPort, sitePort := freePort(t, "tcp"), freePort(t, "udp"), freePort(t, "tcp")
		tcpEcho(t, sitePort, "site")
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
		doc := c.XrayConfig()
		coverAddr := cover.Listener.Addr().String()
		for _, in := range doc["inbounds"].([]map[string]any) {
			if ss, ok := in["streamSettings"].(map[string]any); ok {
				ss["realitySettings"].(map[string]any)["dest"] = coverAddr
			}
		}
		doc["log"] = map[string]any{"loglevel": "none"}
		redirectExit(t, doc, "tcp", sitePort)
		startXray(t, doc)

		checkTCPTunnel(t, map[string]any{
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
		}, sitePort)
	})

	t.Run("hysteria2", func(t *testing.T) {
		edgePort, echoPort, sitePort := freePort(t, "udp"), freePort(t, "udp"), freePort(t, "tcp")
		tcpEcho(t, sitePort, "site")
		m, pin := voiceEnv(t, edgePort, echoPort)
		c, err := VoiceConfigFromEnv(env(m))
		if err != nil {
			t.Fatal(err)
		}
		doc := c.XrayConfig()
		redirectExit(t, doc, "tcp", sitePort)
		startXray(t, doc)

		checkTCPTunnel(t, map[string]any{
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
		}, sitePort)
	})
}
