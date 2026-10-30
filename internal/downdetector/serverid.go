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
