package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// edge is one relay as the hub hands it to a prober (internal/entry
// ProbeRelay). These are the client-side credentials — Reality for a stream
// relay, Hysteria2 for a voice relay — so the prober dials the relay exactly
// as a real client would, and a probe measures the real path, not a proxy of
// it.
type edge struct {
	Name      string   `json:"name"`
	Address   string   `json:"address"`
	Port      int      `json:"port"`
	Proto     string   `json:"proto"`
	UUID      string   `json:"uuid,omitempty"`
	PublicKey string   `json:"public_key,omitempty"`
	ShortID   string   `json:"short_id,omitempty"`
	Covers    []string `json:"covers,omitempty"`

	Auth       string          `json:"auth,omitempty"`
	ServerName string          `json:"server_name,omitempty"`
	CertSHA256 string          `json:"cert_sha256,omitempty"`
	FinalMask  json.RawMessage `json:"finalmask,omitempty"`
	EchoPort   int             `json:"echo_port,omitempty"`

	// XHTTP relay (П4в): the relay's domain, the secret path, the mode.
	Host string `json:"host,omitempty"`
	Path string `json:"path,omitempty"`
	Mode string `json:"mode,omitempty"`

	// AmneziaWG relay: dialled in this process, not through the engine.
	Awg *awgEdge `json:"awg,omitempty"`
}

const (
	protoHysteria = "hysteria2"
	protoXhttp    = "vless-xhttp"
	protoAwg      = "amneziawg"
)

// voice reports whether this is a voice relay (Hysteria2).
func (e edge) voice() bool { return e.Proto == protoHysteria }

// xhttp reports whether this is an XHTTP stream relay.
func (e edge) xhttp() bool { return e.Proto == protoXhttp }

// awg reports whether this is an AmneziaWG relay, which Xray cannot dial.
func (e edge) awg() bool { return e.Proto == protoAwg }

// probeCover is the cover SNI used for this edge's Reality outbound and its K1
// bare-TLS control. The methodology races every cover; the prober's first
// version measures the first one, which is enough to tell liveness and cause.
func (e edge) probeCover() string {
	if len(e.Covers) > 0 {
		return e.Covers[0]
	}
	return ""
}

// carriesCalls reports whether a stream relay also carries a call's datagrams
// (XUDP inside Reality, the TCP rung of the voice ladder, voice plan Ф5): the
// hub names its loopback echo.
func (e edge) carriesCalls() bool { return !e.voice() && e.EchoPort != 0 }

// validateEdges refuses a list a probe could not dial: a relay missing a field
// would make xray reject the whole config.
func validateEdges(edges []edge) error {
	for i, e := range edges {
		if e.Name == "" || e.Address == "" || e.Port == 0 {
			return fmt.Errorf("relay %d (%q) is missing a required field", i, e.Name)
		}
		switch {
		case e.voice():
			if e.Auth == "" || e.ServerName == "" || e.CertSHA256 == "" || e.EchoPort == 0 || len(e.FinalMask) == 0 {
				return fmt.Errorf("voice relay %d (%q) is missing a required field", i, e.Name)
			}
		case e.awg():
			if e.Awg == nil || e.Awg.ServerPublicKey == "" || e.Awg.ClientPrivateKey == "" || e.Awg.ClientAddress == "" {
				return fmt.Errorf("awg relay %d (%q) is missing a required field", i, e.Name)
			}
		case e.xhttp():
			if e.UUID == "" || e.Host == "" || e.Path == "" || e.Mode == "" {
				return fmt.Errorf("xhttp relay %d (%q) is missing a required field", i, e.Name)
			}
		case e.UUID == "" || e.PublicKey == "":
			return fmt.Errorf("relay %d (%q) is missing a required field", i, e.Name)
		}
	}
	return nil
}

// --- xray config generation (VLESS + Reality + Vision, one SOCKS per edge) ---

// Inbounds and outbounds are `any`: a stream relay renders the typed Reality
// shapes below, a voice relay its Hysteria pair (voiceInbound/voiceOutbound).
type xrayConfig struct {
	Log       xrayLog     `json:"log"`
	Inbounds  []any       `json:"inbounds"`
	Outbounds []any       `json:"outbounds"`
	Routing   xrayRouting `json:"routing"`
}

type xrayLog struct {
	LogLevel string `json:"loglevel"`
}

type xrayInbound struct {
	Tag      string            `json:"tag"`
	Listen   string            `json:"listen"`
	Port     int               `json:"port"`
	Protocol string            `json:"protocol"`
	Settings xraySocksSettings `json:"settings"`
}

type xraySocksSettings struct {
	UDP bool `json:"udp"`
}

type xrayOutbound struct {
	Tag            string             `json:"tag"`
	Protocol       string             `json:"protocol"`
	Settings       xrayVLESSSettings  `json:"settings"`
	StreamSettings xrayStreamSettings `json:"streamSettings"`
}

type xrayVLESSSettings struct {
	Vnext []xrayVnext `json:"vnext"`
}

type xrayVnext struct {
	Address string     `json:"address"`
	Port    int        `json:"port"`
	Users   []xrayUser `json:"users"`
}

type xrayUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
	Flow       string `json:"flow"`
}

type xrayStreamSettings struct {
	Network         string              `json:"network"`
	Security        string              `json:"security"`
	RealitySettings xrayRealitySettings `json:"realitySettings"`
}

type xrayRealitySettings struct {
	ServerName  string `json:"serverName"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	ShortID     string `json:"shortId"`
}

type xrayRouting struct {
	Rules []xrayRule `json:"rules"`
}

type xrayRule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag"`
	OutboundTag string   `json:"outboundTag"`
}

// voiceInbound is a voice relay's local door: a UDP datagram sent to it goes
// through the relay's Hysteria tunnel to the relay's loopback echo (TCP too,
// only so the readiness check can see the listener; the edge refuses TCP).
func voiceInbound(tag string, port int, e edge) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "dokodemo-door",
		"settings": map[string]any{"address": "127.0.0.1", "port": e.EchoPort, "network": "tcp,udp"},
	}
}

// voiceOutbound dials the relay as the client does (nekko-transport-xray
// HysteriaOutbound): pinned certificate, the relay's masks, Chrome's QUIC
// parrot off under them.
func voiceOutbound(tag string, e edge) map[string]any {
	return map[string]any{
		"tag": tag, "protocol": "hysteria",
		"settings": map[string]any{"version": 2, "address": e.Address, "port": e.Port},
		"streamSettings": map[string]any{
			"network":          "hysteria",
			"hysteriaSettings": map[string]any{"version": 2, "auth": e.Auth},
			"security":         "tls",
			"tlsSettings": map[string]any{
				"serverName": e.ServerName, "alpn": []string{"h3"}, "pinnedPeerCertSha256": e.CertSHA256,
			},
			"finalmask": map[string]any{
				"udp":        e.FinalMask,
				"quicParams": map[string]any{"disableChromeParrot": true},
			},
		},
	}
}

// xhttpOutbound dials an XHTTP relay as the client does (nekko-transport-xray
// XhttpOutbound): the address dialled, the domain only in SNI and Host, the
// certificate checked against CA roots — a probe that skipped the check would
// call a relay clean that every client refuses.
func xhttpOutbound(tag string, e edge, fingerprint string) map[string]any {
	return map[string]any{
		"tag": tag, "protocol": "vless",
		"settings": map[string]any{"vnext": []map[string]any{{
			"address": e.Address, "port": e.Port,
			"users": []map[string]any{{"id": e.UUID, "encryption": "none"}},
		}}},
		"streamSettings": map[string]any{
			"network":       "xhttp",
			"xhttpSettings": map[string]any{"host": e.Host, "path": e.Path, "mode": e.Mode},
			"security":      "tls",
			"tlsSettings": map[string]any{
				"serverName": e.Host, "fingerprint": fingerprint, "alpn": []string{"h2", "http/1.1"},
			},
		},
	}
}

// xudpInbound is a stream relay's datagram door: UDP sent to it rides the
// relay's Reality outbound — Vision turns it into XUDP — to the relay's
// loopback echo, the way a call's RTP rides the TCP rung.
func xudpInbound(tag string, port int, e edge) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "dokodemo-door",
		"settings": map[string]any{"address": "127.0.0.1", "port": e.EchoPort, "network": "udp"},
	}
}

// buildXrayConfig emits one local inbound per edge (on socksBase+i) routed to
// that edge's outbound: SOCKS to Reality for a stream relay, a UDP door to
// Hysteria for a voice relay. A stream relay that carries calls also gets a
// UDP door through its Reality outbound, after all of those
// (socksBase+len(edges)+i). Returns the config JSON, the name→port map the
// probe loop reads and the name→port map of those datagram doors.
// fingerprint is the uTLS profile ("chrome").
func buildXrayConfig(edges []edge, socksBase int, fingerprint string) ([]byte, map[string]int, map[string]int, error) {
	cfg := xrayConfig{Log: xrayLog{LogLevel: "warning"}}
	ports := make(map[string]int, len(edges))
	xudp := map[string]int{}
	for i, e := range edges {
		port := socksBase + i
		ports[e.Name] = port
		if e.awg() {
			continue
		}
		inTag := "in-" + e.Name
		outTag := "out-" + e.Name
		inTags := []string{inTag}
		if e.carriesCalls() {
			door := socksBase + len(edges) + i
			xudp[e.Name] = door
			inTags = append(inTags, "xudp-"+e.Name)
			cfg.Inbounds = append(cfg.Inbounds, xudpInbound("xudp-"+e.Name, door, e))
		}
		cfg.Routing.Rules = append(cfg.Routing.Rules, xrayRule{
			Type:        "field",
			InboundTag:  inTags,
			OutboundTag: outTag,
		})
		if e.voice() {
			cfg.Inbounds = append(cfg.Inbounds, voiceInbound(inTag, port, e))
			cfg.Outbounds = append(cfg.Outbounds, voiceOutbound(outTag, e))
			continue
		}
		cfg.Inbounds = append(cfg.Inbounds, xrayInbound{
			Tag:      inTag,
			Listen:   "127.0.0.1",
			Port:     port,
			Protocol: "socks",
			Settings: xraySocksSettings{UDP: false},
		})
		if e.xhttp() {
			cfg.Outbounds = append(cfg.Outbounds, xhttpOutbound(outTag, e, fingerprint))
			continue
		}
		cfg.Outbounds = append(cfg.Outbounds, xrayOutbound{
			Tag:      outTag,
			Protocol: "vless",
			Settings: xrayVLESSSettings{Vnext: []xrayVnext{{
				Address: e.Address,
				Port:    e.Port,
				Users: []xrayUser{{
					ID:         e.UUID,
					Encryption: "none",
					Flow:       "xtls-rprx-vision",
				}},
			}}},
			StreamSettings: xrayStreamSettings{
				Network:  "tcp",
				Security: "reality",
				RealitySettings: xrayRealitySettings{
					ServerName:  e.probeCover(),
					Fingerprint: fingerprint,
					PublicKey:   e.PublicKey,
					ShortID:     e.ShortID,
				},
			},
		})
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal xray config: %w", err)
	}
	return raw, ports, xudp, nil
}

// startXray writes the config to a temp file, launches `xray run -c <file>`,
// and returns the running command plus a cleanup that stops it and removes the
// file. The caller waits for the SOCKS ports with waitSocks.
func startXray(ctx context.Context, binPath string, config []byte) (*exec.Cmd, func(), error) {
	f, err := os.CreateTemp("", "entry-prober-xray-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("temp config: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(config); err != nil {
		f.Close()
		os.Remove(path)
		return nil, nil, fmt.Errorf("write config: %w", err)
	}
	f.Close()

	cmd := exec.CommandContext(ctx, binPath, "run", "-c", path)
	cmd.Stdout = os.Stderr // xray's own logs go to our stderr, out of the JSON on stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return nil, nil, fmt.Errorf("start xray: %w", err)
	}
	cleanup := func() {
		// Kill only; watchExit (or CommandContext on ctx cancel) reaps. A
		// second Wait here would race that one.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		os.Remove(path)
	}
	return cmd, cleanup, nil
}

// watchExit returns a channel that closes when the xray process exits, so the
// prober stops probing through a dead tunnel engine. It owns the single
// cmd.Wait() that reaps the child.
func watchExit(cmd *exec.Cmd) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(ch)
	}()
	return ch
}

// waitSocks blocks until a SOCKS port accepts a TCP connection or the deadline
// passes. xray opens its listeners a beat after the process starts.
func waitSocks(port int, deadline time.Duration) error {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port))
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("socks port %d did not open within %s", port, deadline)
}
