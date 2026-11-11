package main

// The trunk's other wires at the exit (hikkari-client
// plans/2026-11-11-trunk-wires-stage4.md): besides the pool's members, which
// come as CONNECTs to this relay (trunk_exit.go), members of other forms come
// on UDP ports of their own and join the same trunk sessions.
//
//	EDGE_HIKKARI_TRUNK_WIRES=udp=20447,quic=20448,salamander=20449
//
// udp is the bare wire (hikkari-protocol trunk/udpwire: packet members,
// datagrams only), quic and salamander Hikkari's datagram channel with a
// stream per member (trunk/quicwire), the two that led the bench hour for
// bulk. Every secret of a wire comes from the trunk keys; the QUIC wires'
// certificate is the first key's, for this relay's domain.
//
// The sockets are bound with SO_REUSEPORT: a new generation of the relay
// binds them beside the old one (handoff.go), and the kernel spreads new
// entry addresses over both while the old one drains its sessions.

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/quicwire"
	"github.com/m1d1x/hikkari-protocol/trunk/udpwire"
	"golang.org/x/sys/unix"
)

// Kinds of wire the exit serves.
const (
	trunkWireUDP        = "udp"
	trunkWireQUIC       = "quic"
	trunkWireSalamander = "salamander"
)

// trunkWiresFromEnv reads EDGE_HIKKARI_TRUNK_WIRES: kind=port, each kind
// once, each port once.
func trunkWiresFromEnv(raw string) (map[string]int, error) {
	out := map[string]int{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	ports := map[int]bool{}
	for _, item := range strings.Split(raw, ",") {
		kind, p, ok := strings.Cut(strings.TrimSpace(item), "=")
		port, err := strconv.Atoi(p)
		if !ok || err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("EDGE_HIKKARI_TRUNK_WIRES: %q is not kind=port", item)
		}
		switch kind {
		case trunkWireUDP, trunkWireQUIC, trunkWireSalamander:
		default:
			return nil, fmt.Errorf("EDGE_HIKKARI_TRUNK_WIRES: wire %q is not udp, quic or salamander", kind)
		}
		if _, dup := out[kind]; dup || ports[port] {
			return nil, fmt.Errorf("EDGE_HIKKARI_TRUNK_WIRES: %q twice", item)
		}
		out[kind], ports[port] = port, true
	}
	return out, nil
}

// reusePort binds a UDP socket that another generation may bind too.
func reusePort(ctx context.Context, port int) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1) }); err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(ctx, "udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// startTrunkWires serves the wires of cfg into x until ctx ends.
func startTrunkWires(ctx context.Context, cfg trunkExitConfig, host string, x *trunk.Exit) error {
	serve := func(c io.ReadWriteCloser) {
		if err := x.ServeMember(c); err != nil && ctx.Err() == nil {
			_ = c.Close()
		}
	}
	kinds := make([]string, 0, len(cfg.Wires))
	for k := range cfg.Wires {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var closers []io.Closer
	fail := func(err error) error {
		for _, c := range closers {
			_ = c.Close()
		}
		return err
	}
	for _, kind := range kinds {
		port := cfg.Wires[kind]
		conn, err := reusePort(ctx, port)
		if err != nil {
			return fail(fmt.Errorf("trunk wire %s on udp/%d: %w", kind, port, err))
		}
		switch kind {
		case trunkWireUDP:
			l, err := udpwire.Serve(conn, cfg.Keys, serve)
			if err != nil {
				conn.Close()
				return fail(err)
			}
			closers = append(closers, l)
		default:
			d, err := quicwire.Listen(ctx, quicwire.ListenOptions{Conn: conn, Keys: cfg.Keys, SNI: host, Salamander: kind == trunkWireSalamander, Serve: serve})
			if err != nil {
				conn.Close()
				return fail(err)
			}
			closers = append(closers, d)
		}
		log.Printf("nekko-edge: trunk wire %s on udp/%d", kind, port)
	}
	context.AfterFunc(ctx, func() {
		for _, c := range closers {
			_ = c.Close()
		}
	})
	return nil
}
