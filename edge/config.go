package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Config is everything one edge relay needs, read from the environment. An
// edge holds no other state: the same env on a fresh box is the same relay.
type Config struct {
	// Listen is the public address, normally 0.0.0.0:443.
	Listen string
	// PrivateKey is the Reality X25519 private key, base64url as Xray prints it.
	PrivateKey string
	// ShortIDs the edge accepts (hex).
	ShortIDs []string
	// Clients are the VLESS user ids allowed through.
	Clients []string
	// Covers are the sites the handshake may borrow (SNI). The first one also
	// answers a ClientHello naming none of them — a probe, a scanner.
	Covers []string
	// LocalPortBase is the first loopback port of the per-cover Reality
	// inbounds; two edges on one box need distinct bases.
	LocalPortBase int
	// EchoPort is the loopback UDP echo the admission probe aims at.
	EchoPort int
	// LogLevel is Xray's loglevel.
	LogLevel string
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ConfigFromEnv reads the EDGE_* variables. Every missing required variable
// is reported at once, so a broken env file is fixed in one pass.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:     getenv("EDGE_LISTEN"),
		PrivateKey: getenv("EDGE_PRIVATE_KEY"),
		ShortIDs:   list(getenv("EDGE_SHORT_IDS")),
		Clients:    list(getenv("EDGE_CLIENTS")),
		Covers:     list(getenv("EDGE_COVERS")),
		LogLevel:   getenv("EDGE_LOG_LEVEL"),
	}
	if c.Listen == "" {
		c.Listen = "0.0.0.0:443"
	}
	if c.LogLevel == "" {
		c.LogLevel = "warning"
	}
	c.LocalPortBase = 10001
	if v := getenv("EDGE_LOCAL_PORT_BASE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1024 || n > 65000 {
			return Config{}, fmt.Errorf("EDGE_LOCAL_PORT_BASE: want 1024..65000, got %q", v)
		}
		c.LocalPortBase = n
	}

	var errs []error
	if c.PrivateKey == "" {
		errs = append(errs, errors.New("EDGE_PRIVATE_KEY is empty"))
	}
	if len(c.ShortIDs) == 0 {
		errs = append(errs, errors.New("EDGE_SHORT_IDS is empty"))
	}
	if len(c.Clients) == 0 {
		errs = append(errs, errors.New("EDGE_CLIENTS is empty"))
	}
	if len(c.Covers) == 0 {
		errs = append(errs, errors.New("EDGE_COVERS is empty"))
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("EDGE_LISTEN: %w", err))
	}
	var err error
	c.EchoPort, err = echoPortFromEnv(getenv)
	errs = append(errs, err)
	if err == nil && c.EchoPort >= c.LocalPortBase && c.EchoPort < c.LocalPortBase+len(c.Covers) {
		errs = append(errs, fmt.Errorf("EDGE_ECHO_PORT %d is one of the cover inbounds' ports", c.EchoPort))
	}
	return c, errors.Join(errs...)
}

// XrayConfig renders the Xray JSON document (as a value; the caller marshals).
//
//	:443 dokodemo-door, sniffing tls (routeOnly)
//	  └─ rule by SNI ─► freedom{redirect 127.0.0.1:<base+i>} ─► Reality inbound of cover i
//	                                                              └─ VLESS user ─► direct (egress.go)
func (c Config) XrayConfig() map[string]any {
	listenHost, listenPort, _ := net.SplitHostPort(c.Listen)
	port, _ := strconv.Atoi(listenPort)

	clients := make([]map[string]any, 0, len(c.Clients))
	for _, id := range c.Clients {
		clients = append(clients, map[string]any{"id": id, "flow": "xtls-rprx-vision"})
	}

	inbounds := []map[string]any{{
		"tag":      "sni-router",
		"listen":   listenHost,
		"port":     port,
		"protocol": "dokodemo-door",
		"settings": map[string]any{
			"address": "127.0.0.1",
			"port":    c.LocalPortBase,
			"network": "tcp",
		},
		"sniffing": map[string]any{
			"enabled":      true,
			"destOverride": []string{"tls"},
			"routeOnly":    true,
		},
	}}
	outbounds := []map[string]any{
		// First outbound is Xray's default; make it the drop, so a rule that
		// fails to match never turns into an open proxy.
		{"tag": "drop", "protocol": "blackhole"},
		directOutbound(c.EchoPort),
	}
	var rules []map[string]any
	realityTags := make([]string, 0, len(c.Covers))

	for i, cover := range c.Covers {
		local := c.LocalPortBase + i
		tag := realityTag(cover)
		realityTags = append(realityTags, tag)
		inbounds = append(inbounds, map[string]any{
			"tag":      tag,
			"listen":   "127.0.0.1",
			"port":     local,
			"protocol": "vless",
			"settings": map[string]any{
				"clients":    clients,
				"decryption": "none",
			},
			"streamSettings": map[string]any{
				"network":  "tcp",
				"security": "reality",
				"realitySettings": map[string]any{
					"dest":        cover + ":443",
					"serverNames": []string{cover},
					"privateKey":  c.PrivateKey,
					"shortIds":    c.ShortIDs,
				},
			},
			"sniffing": tunnelSniffing(),
		})
		hop := "to-" + cover
		outbounds = append(outbounds, map[string]any{
			"tag":      hop,
			"protocol": "freedom",
			"settings": map[string]any{"redirect": fmt.Sprintf("127.0.0.1:%d", local)},
		})
		rules = append(rules, map[string]any{
			"type":        "field",
			"inboundTag":  []string{"sni-router"},
			"domain":      []string{"full:" + cover},
			"outboundTag": hop,
		})
	}
	// No (known) SNI: hand it to the first cover, which proxies it to the real
	// site — what a prober expects to see.
	rules = append(rules, map[string]any{
		"type":        "field",
		"inboundTag":  []string{"sni-router"},
		"outboundTag": "to-" + c.Covers[0],
	})
	// Everything a VLESS user asks for leaves through the exit, minus blocked RU destinations.
	rules = append(rules, egressRules(realityTags)...)

	doc := map[string]any{
		"log":       map[string]any{"loglevel": c.LogLevel},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules":          rules,
		},
	}
	withStats(doc)
	return doc
}

// realityTag is the inbound of one cover.
func realityTag(cover string) string { return "reality-" + cover }

// withStats turns on Xray's per-inbound byte counters and the per-user ones of
// access keys, which the pulse reads (roadmap 42 П9.3, keys.go). A per-user
// counter is keyed by the access key, a random string only the hub can tie to
// an account; the shared credential carries no email and is counted in the
// inbound totals alone. Holding the machine still says nothing about who was
// on it (§0.1).
//
// Applied to every protocol's document through this one helper, so the three
// cannot drift into counting different things.
func withStats(doc map[string]any) {
	doc["stats"] = map[string]any{}
	doc["policy"] = map[string]any{
		"system": map[string]any{
			"statsInboundUplink":   true,
			"statsInboundDownlink": true,
		},
		"levels": map[string]any{
			fmt.Sprint(userLevel): map[string]any{
				"statsUserUplink":   true,
				"statsUserDownlink": true,
			},
		},
	}
}

// osEnv is the production getenv.
var osEnv = os.Getenv
