package entry

import (
	"context"
	"fmt"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Upstreams renders what a stream relay may forward to — EDGE_UPSTREAMS, the
// edge's whole allow-list (edge/config.go): the gateway, and the HTTP/WSS hosts the
// client reaches through the tunnel (roadmap 42 П8) — the object store its
// presigned URLs point at, the update channel and registered SFU signaling.
// Anything not listed the relay
// drops, so it stays a pipe to our own addresses and never an open proxy.
//
// gateway is host:port. Each endpoint is a URL ("https://s3.example.com") or a
// bare host; its port defaults by scheme. Endpoints that are empty, loopback or
// an IP literal are left out, logged: the edge only matches names (the client
// dials the URL's host), and a relay that forwarded to "localhost" would hand
// whoever holds a client key the relay box's own local services. Leaving one
// out costs exactly that HTTP path through the tunnel on that deployment — a
// dev stack, where the bucket is on loopback anyway.
func Upstreams(gateway string, endpoints ...string) (string, error) {
	if _, _, err := net.SplitHostPort(gateway); err != nil {
		return "", fmt.Errorf("relay upstreams: gateway %q: %w", gateway, err)
	}
	out := []string{gateway}
	seen := map[string]bool{gateway: true}
	for _, e := range endpoints {
		hp, ok := upstreamOf(e)
		if !ok {
			if strings.TrimSpace(e) != "" {
				applog.Warn(context.Background(), "entry", "relay upstream is not a public name, relays will not forward to it", applog.F("endpoint", e))
			}
			continue
		}
		if !seen[hp] {
			seen[hp] = true
			out = append(out, hp)
		}
	}
	return strings.Join(out, ","), nil
}

// upstreamOf is host:port of an endpoint URL or bare host, when the host is a
// public name.
func upstreamOf(endpoint string) (string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", false
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", false
	}
	port := u.Port()
	if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "wss" && u.Scheme != "ws" {
		return "", false
	}
	if port == "" {
		switch u.Scheme {
		case "https", "wss":
			port = "443"
		case "http", "ws":
			port = "80"
		default:
			return "", false
		}
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}
