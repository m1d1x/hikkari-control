package main

import (
	"strconv"
	"strings"
	"testing"
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

// The exit's rules, in order: the echo, SMTP, the private networks, then the
// internet. Shared by all three protocols.
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
	for _, n := range []string{"127.0.0.0/8", "10.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10", "fc00::/7", "::1/128"} {
		if !strings.Contains(nets, n) {
			t.Errorf("denylist misses %s", n)
		}
	}
	if toString(rules[3]["ip"]) != "[0.0.0.0/0 ::/0]" {
		t.Fatalf("last rule must open the internet: %v", rules[3])
	}
	if r := egressFinalRules(0); r[0]["action"] != "block" {
		t.Fatalf("no echo, no echo rule: %v", r[0])
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
		found := false
		for _, o := range doc["outbounds"].([]map[string]any) {
			if o["tag"] == directTag {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no exit outbound", name)
		}
	}
}
