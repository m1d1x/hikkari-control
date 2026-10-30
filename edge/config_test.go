package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/core"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func good() map[string]string {
	return map[string]string{
		// Any 32 bytes in Xray's encoding make a valid X25519 private key.
		"EDGE_PRIVATE_KEY": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"EDGE_SHORT_IDS":   "0123456789abcdef",
		"EDGE_CLIENTS":     "00000000-0000-4000-8000-000000000001",
		"EDGE_COVERS":      "www.a.example, www.b.example",
		"EDGE_ECHO_PORT":   "10100",
	}
}

func TestEveryMissingVariableIsReportedAtOnce(t *testing.T) {
	_, err := ConfigFromEnv(env(map[string]string{}))
	if err == nil {
		t.Fatal("empty env must fail")
	}
	for _, name := range []string{"EDGE_PRIVATE_KEY", "EDGE_SHORT_IDS", "EDGE_CLIENTS", "EDGE_COVERS", "EDGE_ECHO_PORT"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestDefaults(t *testing.T) {
	c, err := ConfigFromEnv(env(good()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:443" || c.LocalPortBase != 10001 || c.LogLevel != "warning" {
		t.Fatalf("defaults: %+v", c)
	}
	if len(c.Covers) != 2 || c.Covers[1] != "www.b.example" {
		t.Fatalf("covers trimmed and ordered: %v", c.Covers)
	}
}

// rules returns the routing rules of a rendered config.
func rules(t *testing.T, c Config) []map[string]any {
	t.Helper()
	return c.XrayConfig()["routing"].(map[string]any)["rules"].([]map[string]any)
}

func TestTheDefaultOutboundIsTheDrop(t *testing.T) {
	c, _ := ConfigFromEnv(env(good()))
	outs := c.XrayConfig()["outbounds"].([]map[string]any)
	if outs[0]["protocol"] != "blackhole" {
		t.Fatalf("first outbound (Xray's default) must drop, got %v", outs[0])
	}
}

func TestEveryVLESSUserLeavesThroughTheExit(t *testing.T) {
	c, _ := ConfigFromEnv(env(good()))
	rs := rules(t, c)
	last := rs[len(rs)-1]
	tags, _ := last["inboundTag"].([]string)
	if last["outboundTag"] != directTag || len(tags) != 2 || tags[0] != "reality-www.a.example" {
		t.Fatalf("last rule must send every Reality inbound to the exit, got %v", last)
	}
}

func TestTheEchoMayNotSitOnACoverPort(t *testing.T) {
	m := good()
	m["EDGE_ECHO_PORT"] = "10002" // base 10001, two covers: 10001 and 10002
	if _, err := ConfigFromEnv(env(m)); err == nil || !strings.Contains(err.Error(), "EDGE_ECHO_PORT") {
		t.Fatalf("an echo on a cover port was accepted: %v", err)
	}
}

func TestUnknownSNIGoesToTheFirstCover(t *testing.T) {
	c, _ := ConfigFromEnv(env(good()))
	var fallback map[string]any
	for _, r := range rules(t, c) {
		if tags, _ := r["inboundTag"].([]string); len(tags) == 1 && tags[0] == "sni-router" && r["domain"] == nil {
			fallback = r
		}
	}
	if fallback == nil || fallback["outboundTag"] != "to-www.a.example" {
		t.Fatalf("SNI fallback: %v", fallback)
	}
}

func TestXrayAcceptsTheRenderedConfig(t *testing.T) {
	c, _ := ConfigFromEnv(env(good()))
	raw, err := json.Marshal(c.XrayConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := core.LoadConfig("json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("xray rejects the config: %v", err)
	}
	inst, err := core.New(cfg)
	if err != nil {
		t.Fatalf("xray cannot build an instance: %v", err)
	}
	_ = inst.Close()
}
