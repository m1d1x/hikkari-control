package main

// receivers.go: the receivers of an entry over hk-pipe as the env says them
// (receivers_linux.go says what the brain does with them).

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// envActiveReceiver carries the active receiver's address to the next
// generation; not EDGE_TRUNK_*: the hub's env does not set it, the reload
// does (childEnv).
const envActiveReceiver = "NEKKO_TRUNK_RECEIVER"

// defaultReceiverPool is how many members a receiver on standby keeps in
// its pool: warm, not a second full pool (Э7).
const defaultReceiverPool = 8

// receivers reads EDGE_TRUNK_RECEIVERS (IPv4 addresses, commas) and
// EDGE_TRUNK_RECEIVER_POOL into c; exit is EDGE_TRUNK_EXIT's address.
func receivers(c *Config, get func(string) string, exit netip.Addr) error {
	c.ReceiverPool = defaultReceiverPool
	raw := strings.TrimSpace(get("EDGE_TRUNK_RECEIVERS"))
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(get("EDGE_TRUNK_PIPE")) == "" {
		return fmt.Errorf("EDGE_TRUNK_RECEIVERS: only an entry over hk-pipe moves between receivers")
	}
	seen := map[netip.Addr]bool{exit: true}
	for _, item := range strings.Split(raw, ",") {
		ip, err := netip.ParseAddr(strings.TrimSpace(item))
		if err != nil || !ip.Is4() {
			return fmt.Errorf("EDGE_TRUNK_RECEIVERS: %q is not an IPv4 address", item)
		}
		if seen[ip] {
			return fmt.Errorf("EDGE_TRUNK_RECEIVERS: %s twice (EDGE_TRUNK_EXIT is the first receiver)", ip)
		}
		seen[ip] = true
		c.Receivers = append(c.Receivers, ip)
	}
	if v := strings.TrimSpace(get("EDGE_TRUNK_RECEIVER_POOL")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxMembers {
			return fmt.Errorf("EDGE_TRUNK_RECEIVER_POOL: %q, 1-%d", v, maxMembers)
		}
		c.ReceiverPool = n
	}
	return nil
}

// receiverOrder is the receivers by the pipe's rx for a generation that
// starts on start: start first, then the group in the env's order.
func receiverOrder(exit netip.Addr, rest []netip.Addr, start netip.Addr) []netip.Addr {
	all := append([]netip.Addr{exit}, rest...)
	out := []netip.Addr{start}
	for _, ip := range all {
		if ip != start {
			out = append(out, ip)
		}
	}
	return out
}

// startReceiver is the receiver a generation starts on: the one the previous
// generation handed over when it is of the group, else EDGE_TRUNK_EXIT's.
// receiverFilePath is the hub's word on which receiver carries the entry
// (P5, entry-box trunk-entry-receiver): one IPv4 address beside the wires
// file. The brain moves there when the file changes, if that receiver is
// heard; a fresh start (no generation handed one over) starts there.
func receiverFilePath() string {
	return strings.TrimSuffix(wiresFilePath(), ".wires.json") + ".receiver"
}

// readReceiverFile reads the file: its bytes, and the address they name
// (invalid when the file is missing or says nothing).
func readReceiverFile(path string) ([]byte, netip.Addr, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, netip.Addr{}, nil
	}
	if err != nil {
		return nil, netip.Addr{}, err
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return raw, netip.Addr{}, nil
	}
	ip, err := netip.ParseAddr(text)
	if err != nil || !ip.Is4() {
		return raw, netip.Addr{}, fmt.Errorf("receiver file: %q is not an IPv4 address", text)
	}
	return raw, ip, nil
}

func startReceiver(exit netip.Addr, rest []netip.Addr, handed string) netip.Addr {
	ip, err := netip.ParseAddr(strings.TrimSpace(handed))
	if err != nil {
		return exit
	}
	for _, r := range append([]netip.Addr{exit}, rest...) {
		if r == ip {
			return ip
		}
	}
	return exit
}
