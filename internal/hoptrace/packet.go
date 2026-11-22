package hoptrace

import (
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
)

const snapLength = 128

// packet takes an Ethernet IPv4 UDP frame, possibly truncated after the
// fingerprint prefix. IP and UDP addresses, ports and checksums can change
// across the point and are deliberately outside the fingerprint.
func packet(frame []byte, out bool, c Config) (Packet, bool) {
	if len(frame) < 42 || binary.BigEndian.Uint16(frame[12:14]) != 0x0800 {
		return Packet{}, false
	}
	ip := frame[14:]
	header := int(ip[0]&15) * 4
	if ip[0]>>4 != 4 || header < 20 || len(ip) < header+8 || ip[9] != 17 || binary.BigEndian.Uint16(ip[6:8])&0x3fff != 0 {
		return Packet{}, false
	}
	address := netip.AddrFrom4([4]byte(ip[12:16]))
	if out {
		address = netip.AddrFrom4([4]byte(ip[16:20]))
	}
	peer := 0
	for peer < len(c.Peers) && c.Peers[peer].IP != address {
		peer++
	}
	if peer == len(c.Peers) {
		return Packet{}, false
	}
	udp := ip[header:]
	src, dst := binary.BigEndian.Uint16(udp[:2]), binary.BigEndian.Uint16(udp[2:4])
	var port uint16
	for _, p := range c.Ports {
		if p == dst || p == src {
			port = p
			break
		}
	}
	size := int(binary.BigEndian.Uint16(udp[4:6]))
	prefix := min(size-8, 32)
	if port == 0 || prefix < 12 || len(udp) < 8+prefix {
		return Packet{}, false
	}
	var fingerprint [34]byte
	copy(fingerprint[:2], udp[4:6])
	copy(fingerprint[2:], udp[8:8+prefix])
	digest := sha256.Sum256(fingerprint[:2+prefix])
	return Packet{ID: binary.BigEndian.Uint64(digest[:8]), Port: port, Peer: uint8(peer), Out: out}, true
}
