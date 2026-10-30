package main

import "strconv"

// A Hikkari edge is a VPN exit: whatever a client's tunnel asks for leaves the
// box through one freedom outbound, "direct". Nekko's edge was a pipe to one
// gateway and dropped everything else; here the allowlist is the internet and
// the denylist is what a VPN exit must never reach on a user's behalf:
//
//   - the box itself and the hoster's private network (loopback, RFC 1918,
//     link-local with the cloud metadata service at 169.254.169.254, CGNAT,
//     multicast, reserved), so a client cannot talk to services bound on the
//     box or to the provider's internal endpoints;
//   - outbound SMTP (port 25): the first thing an abused exit sends, and the
//     reason a hoster suspends a machine without asking.
//
// The rules are freedom's finalRules, checked against the address actually
// dialled — after a domain is resolved — so a name that resolves into a
// private range is refused as well. The one loopback destination allowed is
// the edge's own UDP echo, which the admission probe times through the tunnel.

// directTag is the outbound every tunnel's traffic leaves through.
const directTag = "direct"

// blockedNets are never dialled through the tunnel.
var blockedNets = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
}

// blockedPorts are refused on every address.
const blockedPorts = "25"

// egressFinalRules: the echo, then the denylist, then everything else allowed.
// With finalRules set freedom follows them alone (its built-in refusal of
// private addresses no longer applies), so the list states every case.
func egressFinalRules(echo int) []map[string]any {
	var rules []map[string]any
	if echo > 0 {
		rules = append(rules, map[string]any{
			"action": "allow", "network": "udp", "ip": []string{"127.0.0.1/32"}, "port": strconv.Itoa(echo),
		})
	}
	return append(rules,
		map[string]any{"action": "block", "port": blockedPorts},
		map[string]any{"action": "block", "ip": blockedNets},
		map[string]any{"action": "allow", "ip": []string{"0.0.0.0/0", "::/0"}},
	)
}

// directOutbound is the exit itself.
func directOutbound(echo int) map[string]any {
	return map[string]any{
		"tag":      directTag,
		"protocol": "freedom",
		"settings": map[string]any{
			// Resolve on the box, so finalRules see the real address.
			"domainStrategy": "UseIP",
			"finalRules":     egressFinalRules(echo),
		},
	}
}

// egressRule sends every tunnel inbound in tags to the exit.
func egressRule(tags []string) map[string]any {
	return map[string]any{
		"type":        "field",
		"inboundTag":  tags,
		"outboundTag": directTag,
	}
}
