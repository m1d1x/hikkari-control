// Package httputil contains small HTTP helpers shared across the codebase.
package httputil

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

// trustedProxyCIDRs is an optional allowlist of CIDR ranges from which
// X-Real-IP is honoured even when the peer is not in the loopback/private
// default set. Populated lazily from the TRUSTED_PROXY_CIDRS env var
// (comma-separated). Empty list keeps the default behaviour.
var (
	trustedProxiesOnce sync.Once
	trustedProxies     []*net.IPNet
)

func loadTrustedProxies() []*net.IPNet {
	trustedProxiesOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXY_CIDRS"))
		if raw == "" {
			return
		}
		out := make([]*net.IPNet, 0, 4)
		for _, c := range strings.Split(raw, ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if _, n, err := net.ParseCIDR(c); err == nil {
				out = append(out, n)
			}
		}
		trustedProxies = out
	})
	return trustedProxies
}

// ClientIP returns the client IP address as seen by the trusted edge proxy.
//
// Trust model: whatever reverse proxy sits in front of the Go process MUST set
// X-Real-IP to the actual TCP peer's address. Anything else (X-Forwarded-For,
// Forwarded, custom
// headers) is attacker-controlled and ignored on purpose: a client can send
// `X-Forwarded-For: 1.2.3.4` to bypass per-IP rate limits if we trust it.
//
// Hardening:
//   - X-Real-IP is only honoured when (a) it parses as a valid IP literal
//     AND (b) the immediate TCP peer is a loopback/private address —
//     i.e. an attacker connecting directly to the public socket cannot
//     forge X-Real-IP because their RemoteAddr is public, so the header
//     is ignored and we fall back to RemoteAddr (their real IP).
//   - When a public-IP peer sets X-Real-IP, we trust RemoteAddr (the real
//     socket peer) instead and drop the header.
//
// Returns an empty string only if RemoteAddr is malformed; callers should
// treat empty as "unknown" and decide whether to allow or block.
func ClientIP(r *http.Request) string {
	peer := remoteAddrIP(r)
	if hdr := strings.TrimSpace(r.Header.Get("X-Real-IP")); hdr != "" {
		if parsed := net.ParseIP(hdr); parsed != nil && peerIsTrustedProxy(peer) {
			return parsed.String()
		}
	}
	return peer
}

func remoteAddrIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr may already be a bare IP in some test setups.
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// peerIsTrustedProxy reports whether the immediate TCP peer is one we
// will accept proxy headers from: loopback or RFC1918/CGNAT private space.
// A proxy terminating TLS on the same host reaches us over loopback; treating
// private space as trusted covers containerised setups where the proxy and the
// Go binary share a docker network.
func peerIsTrustedProxy(addr string) bool {
	if addr == "" {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range loadTrustedProxies() {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
