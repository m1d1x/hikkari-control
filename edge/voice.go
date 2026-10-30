package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
)

// A Hysteria2 edge (EDGE_PROTO=hysteria2) is a QUIC VPN exit: TCP streams and
// UDP datagrams in, out through the same exit as every other edge (egress.go).
//
//	udp/<port> hysteria2 (auth, self-signed cert, masks) ─► direct (egress.go)
//
// The echo is a UDP echo on loopback: an admission probe sends datagrams
// through the tunnel to it and times them, so the path is proven end to end.

// VoiceConfig is everything one voice edge needs, read from the environment.
type VoiceConfig struct {
	// Listen is the public UDP address, e.g. 0.0.0.0:443.
	Listen string
	// Auth is the Hysteria2 credential clients present.
	Auth string
	// CertPEM / KeyPEM are the self-signed leaf the signed record pins.
	CertPEM, KeyPEM string
	// FinalMask is Xray's finalmask.udp list, rendered by the hub from the
	// relay's masks (internal/entry xrayFinalmask) — the same list a client
	// renders from the signed record, so both ends wear the same masks in the
	// same order. Carried through as is.
	FinalMask json.RawMessage
	// EchoPort is the loopback UDP echo the admission probe aims at.
	EchoPort int
	// LogLevel is Xray's loglevel.
	LogLevel string
}

// udpIdleTimeout is how long the edge keeps a forwarded UDP flow without a
// packet: Xray's maximum. Xray's default 60 s would cut a muted call's flow
// whenever ICE consent checks pause (voice plan §6.2 п.1).
const udpIdleTimeout = 600

// VoiceConfigFromEnv reads the EDGE_* variables of a voice edge. Every problem
// is reported at once, so a broken env file is fixed in one pass.
func VoiceConfigFromEnv(getenv func(string) string) (VoiceConfig, error) {
	c := VoiceConfig{
		Listen:   getenv("EDGE_LISTEN"),
		Auth:     getenv("EDGE_HY_AUTH"),
		LogLevel: getenv("EDGE_LOG_LEVEL"),
	}
	if c.Listen == "" {
		c.Listen = "0.0.0.0:443"
	}
	if c.LogLevel == "" {
		c.LogLevel = "warning"
	}
	var errs []error
	if len(c.Auth) < 16 {
		errs = append(errs, errors.New("EDGE_HY_AUTH is empty or shorter than 16"))
	}
	b64 := func(name string) string {
		raw, err := base64.StdEncoding.DecodeString(getenv(name))
		if err != nil || len(raw) == 0 {
			errs = append(errs, fmt.Errorf("%s is empty or not base64", name))
		}
		return string(raw)
	}
	c.CertPEM, c.KeyPEM = b64("EDGE_HY_CERT"), b64("EDGE_HY_KEY")
	if raw := b64("EDGE_HY_FINALMASK"); raw != "" {
		var list []map[string]any
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			errs = append(errs, fmt.Errorf("EDGE_HY_FINALMASK is not a JSON list: %w", err))
		}
		c.FinalMask = json.RawMessage(raw)
	}
	var err error
	c.EchoPort, err = echoPortFromEnv(getenv)
	errs = append(errs, err)
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("EDGE_LISTEN: %w", err))
	}
	return c, errors.Join(errs...)
}

// echoPortFromEnv reads EDGE_ECHO_PORT, the loopback echo of the voice
// admission probe.
func echoPortFromEnv(getenv func(string) string) (int, error) {
	echo, err := strconv.Atoi(getenv("EDGE_ECHO_PORT"))
	if err != nil || echo < 1024 || echo > 65535 {
		return 0, fmt.Errorf("EDGE_ECHO_PORT: want 1024..65535, got %q", getenv("EDGE_ECHO_PORT"))
	}
	return echo, nil
}

func pemLines(pem string) []string { return strings.Split(strings.TrimSpace(pem), "\n") }

// XrayConfig renders the Xray JSON document of a voice edge.
func (c VoiceConfig) XrayConfig() map[string]any {
	host, portStr, _ := net.SplitHostPort(c.Listen)
	port, _ := strconv.Atoi(portStr)
	stream := map[string]any{
		"network":          "hysteria",
		"hysteriaSettings": map[string]any{"version": 2, "udpIdleTimeout": udpIdleTimeout},
		"security":         "tls",
		"tlsSettings": map[string]any{
			"alpn":         []string{"h3"},
			"certificates": []map[string]any{{"certificate": pemLines(c.CertPEM), "key": pemLines(c.KeyPEM)}},
		},
	}
	// An empty list is a bare QUIC handshake on the wire; the hub always
	// sends masks, the edge serves what it is given.
	stream["finalmask"] = map[string]any{"udp": c.FinalMask}
	doc := map[string]any{
		"log": map[string]any{"loglevel": c.LogLevel},
		"inbounds": []map[string]any{{
			"tag":            "voice",
			"listen":         host,
			"port":           port,
			"protocol":       "hysteria",
			"settings":       map[string]any{"version": 2, "clients": []map[string]any{{"auth": c.Auth}}},
			"streamSettings": stream,
		}},
		"outbounds": []map[string]any{directOutbound(c.EchoPort)},
	}
	withStats(doc)
	return doc
}

// serveEcho answers every datagram on loopback udp/port with itself, until
// ctx ends. Loopback only: the tunnel's freedom reaches it, the internet
// cannot.
func serveEcho(ctx context.Context, port int) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return fmt.Errorf("echo: %w", err)
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("nekko-edge: echo stopped: %v", err)
				}
				return
			}
			_, _ = conn.WriteToUDP(buf[:n], from)
		}
	}()
	return nil
}
