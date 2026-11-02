package downdetector

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ServerID is the client's id for a server from a subscription
// (hikkari-client engine/core/downdetector.ServerID). The two must never
// drift apart: serverid_test.go pins a known value.
func ServerID(host string, port int, proto string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", strings.ToLower(host), port, proto)))
	return "s:" + hex.EncodeToString(sum[:16])
}

// NodeID is the id of one of our own nodes.
func NodeID(slug string) string { return "n:" + slug }

// clientProtos maps a fleet node's protocol (entry.Proto*) to the slug the
// client hashes into ServerID for the link the subscription hands it
// (hikkari-client profile.ProtoSlug): security=reality and the transport
// type of a vless link, the profile type of an AmneziaWG config. A protocol
// missing here has no client id, and its node is judged by the outside
// probe alone.
var clientProtos = map[string]string{
	"vless-reality-vision": "vless-reality",
	"vless-xhttp":          "vless-xhttp",
	"hysteria2":            "hysteria2",
	"amneziawg":            "wireguard",
}

// FleetClientID is the id clients report one of our nodes under: the hash of
// the address and port the subscription gives them, not the node's slug,
// which a client never sees. Empty when the node's protocol has no mapping
// or its address is not known yet.
func FleetClientID(host string, port int, proto string) string {
	slug, ok := clientProtos[proto]
	if !ok || host == "" || port < 1 {
		return ""
	}
	return ServerID(host, port, slug)
}
