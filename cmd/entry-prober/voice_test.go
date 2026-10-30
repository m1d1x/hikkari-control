package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func localEcho(t *testing.T, drop func(seq int) bool) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for n := 0; ; n++ {
			k, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !drop(n) {
				_, _ = c.WriteToUDP(buf[:k], from)
			}
		}
	}()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func TestAVoiceBurstIsJudgedByWhatCameBack(t *testing.T) {
	ctx := context.Background()
	whole := probeVoiceTunnel(ctx, localEcho(t, func(int) bool { return false }), time.Second)
	if whole.sent != voicePackets || whole.got != voicePackets || !whole.ok() {
		t.Fatalf("clean path: %+v", whole)
	}
	if v := classifyVoice(whole, true); v != verdictClean {
		t.Fatalf("clean path verdict %s", v)
	}
	// Dies after the handshake-sized start: the case a short probe misses.
	dying := probeVoiceTunnel(ctx, localEcho(t, func(n int) bool { return n >= 20 }), 300*time.Millisecond)
	if dying.ok() || classifyVoice(dying, true) != verdictUnknown {
		t.Fatalf("a path dying under load passed: %+v", dying)
	}
	if v := classifyVoice(whole, false); v != verdictNetworkDown {
		t.Fatalf("no network, verdict %s", v)
	}
}

func TestAVoiceRelayGetsAUDPDoorToItsEcho(t *testing.T) {
	e := edge{
		Name: "ams2-1", Address: "203.0.113.8", Port: 443, Proto: protoHysteria,
		Auth: "aaaaaaaaaaaaaaaaaaaa", ServerName: "x.example", CertSHA256: "ab",
		FinalMask: json.RawMessage(`[{"type":"salamander","settings":{"password":"p"}}]`), EchoPort: 10001,
	}
	if err := validateEdges([]edge{e}); err != nil {
		t.Fatal(err)
	}
	raw, ports, _, err := buildXrayConfig([]edge{e}, 20000, "chrome")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			Settings struct {
				Address, Network string
				Port             int
			} `json:"settings"`
		} `json:"inbounds"`
		Outbounds []struct {
			Protocol       string `json:"protocol"`
			StreamSettings struct {
				TLS struct {
					Pin string `json:"pinnedPeerCertSha256"`
				} `json:"tlsSettings"`
				FinalMask struct {
					UDP        json.RawMessage `json:"udp"`
					QuicParams struct {
						NoParrot bool `json:"disableChromeParrot"`
					} `json:"quicParams"`
				} `json:"finalmask"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	in, out := cfg.Inbounds[0], cfg.Outbounds[0]
	var flat bytes.Buffer
	if err := json.Compact(&flat, out.StreamSettings.FinalMask.UDP); err != nil {
		t.Fatal(err)
	}
	if ports["ams2-1"] != 20000 || in.Protocol != "dokodemo-door" || in.Settings.Port != 10001 ||
		in.Settings.Address != "127.0.0.1" || in.Settings.Network != "tcp,udp" {
		t.Fatalf("door: %+v", in)
	}
	if out.Protocol != "hysteria" || out.StreamSettings.TLS.Pin != "ab" ||
		flat.String() != string(e.FinalMask) || !out.StreamSettings.FinalMask.QuicParams.NoParrot {
		t.Fatalf("outbound: %+v", out)
	}
	e.EchoPort = 0
	if err := validateEdges([]edge{e}); err == nil {
		t.Fatal("a voice relay without its echo port was accepted")
	}
}

// A stream relay that carries calls (voice plan Ф5) gets a datagram door
// through its own Reality outbound to its echo, and its row is clean only if
// the burst came back through it.
func TestAStreamRelayIsProbedForItsTCPRung(t *testing.T) {
	r := edge{Name: "ams1-1", Address: "203.0.113.7", Port: 443, Proto: "vless-reality-vision",
		UUID: "u", PublicKey: "k", ShortID: "s", Covers: []string{"www.a.example"}, EchoPort: 10100}
	old := r
	old.Name, old.EchoPort = "ams1-2", 0
	raw, ports, xudp, err := buildXrayConfig([]edge{r, old}, 20000, "chrome")
	if err != nil {
		t.Fatal(err)
	}
	if ports["ams1-1"] != 20000 || ports["ams1-2"] != 20001 || len(xudp) != 1 || xudp["ams1-1"] != 20002 {
		t.Fatalf("ports %v, datagram doors %v", ports, xudp)
	}
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Port     int    `json:"port"`
			Settings struct {
				Address, Network string
				Port             int
			} `json:"settings"`
		} `json:"inbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var door bool
	for _, in := range cfg.Inbounds {
		if in.Tag == "xudp-ams1-1" {
			door = in.Port == 20002 && in.Settings.Port == 10100 && in.Settings.Network == "udp" && in.Settings.Address == "127.0.0.1"
		}
	}
	rule := cfg.Routing.Rules[0]
	if !door || rule.OutboundTag != "out-ams1-1" || len(rule.InboundTag) != 2 || rule.InboundTag[1] != "xudp-ams1-1" {
		t.Fatalf("datagram door %v, rule %+v", door, rule)
	}

	clean := probeResult{Verdict: verdictClean}
	if v := withXUDP(clean, voiceProbe{sent: 100, got: 97}); v.Verdict != verdictClean || v.Detail != "xudp 97/100" {
		t.Fatalf("a carried burst: %+v", v)
	}
	// A shut door leaves the stream's verdict alone and is recorded as the
	// numbers it is (П9.7): folding it into the verdict made a relay with a
	// working tunnel read `unknown`, which is the word for "no evidence", and
	// no repair could then pass its own admission probe.
	shut := withXUDP(clean, voiceProbe{sent: 100, got: 3})
	if shut.Verdict != verdictClean {
		t.Fatalf("the voice door rewrote the stream's verdict: %+v", shut)
	}
	if shut.XUDPGot != 3 || shut.XUDPSent != 100 {
		t.Fatalf("the door was not recorded: %+v", shut)
	}
	if v := withXUDP(probeResult{Verdict: verdictIPBanned}, voiceProbe{sent: 100}); v.Verdict != verdictIPBanned {
		t.Fatalf("the stream's cause was overwritten: %+v", v)
	}
}
