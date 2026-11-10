package main

// Hikkari v1 (v1.01, v1.02) on this edge: one protocol with two channels on one relay and
// one key. The protocol lives in github.com/m1d1x/hikkari-protocol; the edge
// gives it the relay's roster, counters, certificate, site and exit policy.
//
//	tcp/443  Naive (hikkari-protocol/server/naive, a Caddy handler) + the
//	         UDP-over-TCP fallback inside the same CONNECT tunnel
//	udp/443  the datagram channel (hikkari-protocol/server: QUIC DATAGRAM);
//	         an unauthenticated HTTP/3 request gets the same site as tcp/443
//	both     v1.02 duplicated UDP: a CONNECT to dup.hikkari.arpa and a UDP
//	         association addressed to it are carriers of one session, one
//	         exit socket (hikkari-protocol/dup)
//
// Both channels admit the same key roster (naiveRuntime.keys), count into the
// same totals and are revoked together. EDGE_PROTO=hikkari is its own protocol with
// its own env (EDGE_HIKKARI_*); it shares the Caddy runtime with EDGE_PROTO=naive.

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/m1d1x/hikkari-protocol/observe"
	hkserver "github.com/m1d1x/hikkari-protocol/server"
	M "github.com/sagernet/sing/common/metadata"
)

// exitPolicy is egress.go's exit for what the edge dials itself (the datagram
// channel and UoT), outside Xray's finalRules: the blocked networks plus the
// box's own addresses, the blocked zones and outbound SMTP.
var exitPolicy = func() hkserver.Exit {
	nets := append([]string{}, blockedNets...)
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			if ip, _, err := net.ParseCIDR(address.String()); err == nil {
				nets = append(nets, ip.String())
			}
		}
	}
	port, _ := strconv.Atoi(blockedPorts)
	return hkserver.NewPolicyExit(nets, blockedDomains, []uint16{uint16(port)})
}()

// withUoT serves a CONNECT to the UDP-over-TCP magic host inside the tunnel.
// Bytes are counted by naiveBody/naiveWriter around the tunnel.
func (n *naiveRuntime) withUoT(ctx context.Context, hostPort string) context.Context {
	return hkserver.WithUoT(ctx, hostPort, exitPolicy, func() { n.uotFlows.Add(1) })
}

// Admit implements hkserver.Admitter: a session for a key the roster holds now
// (the empty key is the probe credential), revoked with the key.
func (n *naiveRuntime) Admit(parent context.Context, key string) (context.Context, func(), func(up, down int), bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || (key != "" && !n.keys[key]) {
		return nil, nil, nil, false
	}
	var counter *naiveCount
	if key != "" {
		counter = n.totals[key]
		if counter == nil {
			counter = &naiveCount{}
			n.totals[key] = counter
		}
	}
	ctx, cancel := context.WithCancel(parent)
	n.serial++
	id := n.serial
	n.sessions[id] = naiveSession{key, cancel}
	done := func() { cancel(); n.mu.Lock(); delete(n.sessions, id); n.mu.Unlock() }
	count := func(up, down int) {
		n.total.up.Add(int64(up))
		n.total.down.Add(int64(down))
		if counter != nil {
			counter.up.Add(int64(up))
			counter.down.Add(int64(down))
		}
	}
	return ctx, done, count, true
}

// roster is the key set as a list.
func (n *naiveRuntime) roster() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return sortedKeys(n.keys)
}

// hikkariUDP is the datagram channel and this generation's view of its
// socket.
type hikkariUDP struct {
	*hkserver.Datagram
	conn *hkserver.GenerationConn
}

// startHikkariUDP opens the datagram channel on the relay's UDP socket (this
// generation's, handoff.go) with the relay's ACME certificate (Caddy's
// storage) and the roster n admits.
func startHikkariUDP(ctx context.Context, n *naiveRuntime, socks *sockets) (*hikkariUDP, error) {
	// v1.02: one hub for both channels, so the carriers of a session share
	// one exit socket. Set before the datagram channel admits anyone.
	n.mu.Lock()
	n.dup = hkserver.NewDupHub(exitPolicy, 0)
	n.dup.Observe(n.book)
	hub := n.dup
	n.mu.Unlock()
	conn, err := hkserver.NewGenerationConn(socks.udp, socks.gen, socks.dir)
	if err != nil {
		return nil, fmt.Errorf("hikkari udp: %w", err)
	}
	d, err := hkserver.ListenDatagram(ctx, hkserver.DatagramOptions{
		Conn:          conn,
		ConnectionIDs: hkserver.GenerationIDs{Gen: socks.gen},
		Certificate:   hkserver.CaddyCertificate(n.cfg.Storage, n.cfg.Host),
		Masquerade:    http.FileServer(http.Dir(n.cfg.Root)),
		ProbePassword: n.cfg.Pass,
		Admitter:      n,
		Exit:          exitPolicy,
		Dup:           hub,
		Book:          n.book,
		Group: func(key string, remote M.Socksaddr) string {
			return observe.RelayGroup(n.pathOf(remote.String()), key)
		},
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("hikkari udp: %w", err)
	}
	n.mu.Lock()
	n.onKeys = d.SetKeys
	n.mu.Unlock()
	d.SetKeys(n.roster())
	log.Printf("nekko-edge: hikkari datagram channel on udp %s", n.cfg.Listen)
	return &hikkariUDP{Datagram: d, conn: conn}, nil
}
