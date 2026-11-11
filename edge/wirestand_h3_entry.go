//go:build wirestand_h3

package main

// The entry's half of the stand's wire h3: one Chromium engine (cronet),
// QUIC forced, a CONNECT stream per conversation. Built only with
// -tags with_purego,wirestand_h3 (the stand's own build on the entry, which
// loads libcronet.so from its directory); the relay's edge has no cronet.

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/chromium"
)

const h3Built = true

// standH3 is the entry's engine, for the minute's CPU line (sockets).
var standH3 *chromium.H3

func dialStandH3(ctx context.Context, cfg WireStandConfig) (standWire, error) {
	if err := loadStandCronet(cfg.Cronet); err != nil {
		return standWire{}, fmt.Errorf("h3: libcronet: %w", err)
	}
	_, _, caPEM, err := h3CA(cfg)
	if err != nil {
		return standWire{}, err
	}
	h := &chromium.H3{
		Exit: netip.AddrPortFrom(netip.MustParseAddr(cfg.Exit), uint16(cfg.Port)).String(),
		SNI:  cfg.SNI, User: h3User, Key: cfg.password("h3"), Roots: string(caPEM), Congestion: cfg.H3Congestion,
	}
	if err := h.Start(ctx); err != nil {
		return standWire{}, fmt.Errorf("h3: engine: %w", err)
	}
	context.AfterFunc(ctx, func() { _ = h.Close() })
	standH3 = h
	standOut.Printf(standLog+"h3 on Chromium %s, congestion %q", chromium.Version(), cfg.H3Congestion)
	return standWire{
		call: func(ctx context.Context) (net.Conn, error) {
			c, err := h.Open(ctx, h3EchoAt(cfg.Port), 10*time.Second)
			if err != nil {
				return nil, err
			}
			return newH3Conn(c), nil
		},
		bulk: func(ctx context.Context, b standBulkSpec) standBulkResult {
			return streamBulk(ctx, b, func(ctx context.Context) (net.Conn, error) { return h.Open(ctx, h3BulkAt(cfg.Port), 10*time.Second) })
		},
	}, nil
}

// h3SocketsLine is how Chromium got its sockets, for the CPU line.
func h3SocketsLine() string {
	if standH3 == nil {
		return ""
	}
	s := standH3.Sockets()
	return fmt.Sprintf(" udp_direct=%d udp_relayed=%d tcp_asked=%d", s.Direct, s.Relayed, s.TCP)
}
