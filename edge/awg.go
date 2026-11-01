package main

// awg.go is EDGE_PROTO=amneziawg: WireGuard with the handshake's fingerprint
// changed (junk packets, padded messages, moved header values), which is what
// a DPI box looks for in plain WireGuard.
//
// # In the process, on no interface
//
// The AmneziaWG device runs in userspace with a TCP/IP stack of its own
// (gVisor): no kernel module, no TUN, no NAT, no capability beyond what the
// other protocols have. So it is one binary the hub deploys and rolls like the
// rest, under the same DynamicUser, and a box needs nothing installed.
//
// # The same exit
//
// What a client sends leaves through the running Xray's own router and its
// "direct" outbound (egress.go): one exit, so what a VPN exit must never reach
// is refused here by the same rules, not by a copy of them. Xray has no
// inbound here; a flow enters its router tagged as one (session.Inbound).
//
// # Keys
//
// The hub's pulse answer lists "<public key> <address>" per subscriber, the
// edge makes its peers exactly that list. Unlike the other protocols a peer is
// counted by the device, so the totals come from it, not from Xray's stats.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	wgtun "github.com/amnezia-vpn/amneziawg-go/tun"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	protoAwg = "amneziawg"
	// awgTag names the tunnel to Xray's router, as an inbound's tag would.
	awgTag = "awg-tunnel"
	// awgMTU leaves room for the tunnel's own headers on any path a client
	// is likely to be on; the client side is told the same.
	awgMTU = 1280
	// awgUDPIdle is how long a UDP flow lives without a packet.
	awgUDPIdle = 2 * time.Minute
	// awgPeerActive is how recent a handshake must be for a peer to count as
	// connected: WireGuard rekeys every two minutes while traffic flows.
	awgPeerActive = 3 * time.Minute
)

// AwgConfig is one AmneziaWG relay's environment.
type AwgConfig struct {
	PrivateKey string // base64
	Port       int
	// ProbePeer is "<public key> <address>": the vantage's own peer, what
	// EDGE_CLIENTS is to the other protocols.
	ProbePeer string
	Obf       AwgObfuscation
}

// AwgObfuscation is what both ends must agree on. H1..H4 stay strings: the
// format also allows ranges.
type AwgObfuscation struct {
	Jc, Jmin, Jmax, S1, S2 int
	H1, H2, H3, H4         string
}

// AwgConfigFromEnv reads EDGE_PORT and EDGE_AWG_*.
func AwgConfigFromEnv(getenv func(string) string) (AwgConfig, error) {
	var c AwgConfig
	c.PrivateKey = getenv("EDGE_AWG_PRIVATE_KEY")
	if _, err := awgKeyHex(c.PrivateKey); err != nil {
		return c, fmt.Errorf("EDGE_AWG_PRIVATE_KEY: %w", err)
	}
	port, err := strconv.Atoi(getenv("EDGE_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return c, fmt.Errorf("EDGE_PORT: %q is not a port", getenv("EDGE_PORT"))
	}
	c.Port = port
	c.ProbePeer = getenv("EDGE_AWG_PROBE_PEER")
	if c.ProbePeer != "" {
		if _, err := parseAwgPeer(c.ProbePeer); err != nil {
			return c, fmt.Errorf("EDGE_AWG_PROBE_PEER: %w", err)
		}
	}
	for _, f := range []struct {
		name string
		to   *int
		min  int
	}{
		{"EDGE_AWG_JC", &c.Obf.Jc, 1}, {"EDGE_AWG_JMIN", &c.Obf.Jmin, 1}, {"EDGE_AWG_JMAX", &c.Obf.Jmax, 1},
		{"EDGE_AWG_S1", &c.Obf.S1, 0}, {"EDGE_AWG_S2", &c.Obf.S2, 0},
	} {
		v, err := strconv.Atoi(getenv(f.name))
		if err != nil || v < f.min {
			return c, fmt.Errorf("%s: %q", f.name, getenv(f.name))
		}
		*f.to = v
	}
	if c.Obf.Jmax < c.Obf.Jmin {
		return c, fmt.Errorf("EDGE_AWG_JMAX %d is below EDGE_AWG_JMIN %d", c.Obf.Jmax, c.Obf.Jmin)
	}
	for _, f := range []struct {
		name string
		to   *string
	}{
		{"EDGE_AWG_H1", &c.Obf.H1}, {"EDGE_AWG_H2", &c.Obf.H2}, {"EDGE_AWG_H3", &c.Obf.H3}, {"EDGE_AWG_H4", &c.Obf.H4},
	} {
		if *f.to = getenv(f.name); *f.to == "" {
			return c, fmt.Errorf("%s is empty", f.name)
		}
	}
	return c, nil
}

// XrayConfig is the exit alone: no inbound, the router and the outbounds every
// other protocol leaves through.
func (c AwgConfig) XrayConfig() map[string]any {
	return map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{},
		"outbounds": []any{
			directOutbound(0),
			map[string]any{"tag": dropTag, "protocol": "blackhole"},
		},
		"routing": map[string]any{"rules": egressRules([]string{awgTag})},
		"stats":   map[string]any{},
	}
}

// awgKeyHex is a base64 WireGuard key as the device's control protocol takes it.
func awgKeyHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("not base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("%d bytes, not 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// awgPeer is one entry of the hub's list.
type awgPeer struct {
	publicHex string
	addr      netip.Addr
}

func parseAwgPeer(entry string) (awgPeer, error) {
	pub, addr, ok := strings.Cut(entry, " ")
	if !ok {
		return awgPeer{}, fmt.Errorf("not \"<key> <address>\"")
	}
	h, err := awgKeyHex(pub)
	if err != nil {
		return awgPeer{}, err
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return awgPeer{}, err
	}
	return awgPeer{publicHex: h, addr: a}, nil
}

// awgDialer opens a flow to where the client asked. In the edge, the running
// Xray's router.
type awgDialer func(ctx context.Context, dest xnet.Destination) (net.Conn, error)

// xrayDialer enters Xray's router as the tunnel inbound.
func xrayDialer(inst *core.Instance) awgDialer {
	return func(ctx context.Context, dest xnet.Destination) (net.Conn, error) {
		ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: awgTag})
		return core.Dial(ctx, inst, dest)
	}
}

// awgServer is the running device and the stack behind it.
type awgServer struct {
	dev *device.Device
	tun *awgTun

	mu    sync.Mutex
	peers map[string]bool // base64 public keys of the hub's list
}

// startAwg brings the device up on cfg.Port.
func startAwg(ctx context.Context, cfg AwgConfig, dial awgDialer) (*awgServer, error) {
	tunDev, err := newAwgTun(ctx, dial)
	if err != nil {
		return nil, err
	}
	logger := &device.Logger{Verbosef: device.DiscardLogf, Errorf: func(f string, a ...any) { log.Printf("awg: "+f, a...) }}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	s := &awgServer{dev: dev, tun: tunDev, peers: map[string]bool{}}

	priv, err := awgKeyHex(cfg.PrivateKey)
	if err != nil {
		dev.Close()
		return nil, err
	}
	var uapi strings.Builder
	fmt.Fprintf(&uapi, "private_key=%s\nlisten_port=%d\n", priv, cfg.Port)
	fmt.Fprintf(&uapi, "jc=%d\njmin=%d\njmax=%d\ns1=%d\ns2=%d\nh1=%s\nh2=%s\nh3=%s\nh4=%s\n",
		cfg.Obf.Jc, cfg.Obf.Jmin, cfg.Obf.Jmax, cfg.Obf.S1, cfg.Obf.S2, cfg.Obf.H1, cfg.Obf.H2, cfg.Obf.H3, cfg.Obf.H4)
	if cfg.ProbePeer != "" {
		p, err := parseAwgPeer(cfg.ProbePeer)
		if err != nil {
			dev.Close()
			return nil, err
		}
		writeAwgPeer(&uapi, p)
	}
	if err := dev.IpcSet(uapi.String()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("awg config: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("awg up: %w", err)
	}
	return s, nil
}

func writeAwgPeer(w io.StringWriter, p awgPeer) {
	w.WriteString("public_key=" + p.publicHex + "\nreplace_allowed_ips=true\nallowed_ip=" +
		netip.PrefixFrom(p.addr, p.addr.BitLen()).String() + "\n")
}

// Close takes the device down.
func (s *awgServer) Close() { s.dev.Close() }

// Set makes the peers exactly the hub's list. Peers staying are not touched, so
// nobody's session drops; the vantage's own peer is not on the list and stays.
func (s *awgServer) Set(_ context.Context, entries []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]awgPeer, len(entries))
	var errs []string
	for _, e := range entries {
		p, err := parseAwgPeer(e)
		if err != nil {
			errs = append(errs, fmt.Sprintf("peer %.8s…: %v", e, err))
			continue
		}
		key, _, _ := strings.Cut(e, " ")
		want[key] = p
	}
	var uapi strings.Builder
	for key := range s.peers {
		if _, ok := want[key]; !ok {
			h, _ := awgKeyHex(key)
			uapi.WriteString("public_key=" + h + "\nremove=true\n")
		}
	}
	for key, p := range want {
		if !s.peers[key] {
			writeAwgPeer(&uapi, p)
		}
	}
	if uapi.Len() > 0 {
		if err := s.dev.IpcSet(uapi.String()); err != nil {
			return fmt.Errorf("awg peers: %w", err)
		}
	}
	s.peers = make(map[string]bool, len(want))
	for key := range want {
		s.peers[key] = true
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("awg peers: %s", strings.Join(errs, "; "))
	}
	return nil
}

// awgPeerStat is one peer as the device counts it. Received is what the client
// sent, so it is the key's up.
type awgPeerStat struct {
	up, down      int64
	lastHandshake time.Time
}

// stats reads the device's per-peer counters, by base64 public key.
func (s *awgServer) stats() (map[string]awgPeerStat, error) {
	raw, err := s.dev.IpcGet()
	if err != nil {
		return nil, err
	}
	return parseAwgStats(raw), nil
}

func parseAwgStats(raw string) map[string]awgPeerStat {
	out := map[string]awgPeerStat{}
	var key string
	var cur awgPeerStat
	flush := func() {
		if key != "" {
			out[key] = cur
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "public_key":
			flush()
			cur = awgPeerStat{}
			key = ""
			if b, err := hex.DecodeString(v); err == nil {
				key = base64.StdEncoding.EncodeToString(b)
			}
		case "rx_bytes":
			cur.up, _ = strconv.ParseInt(v, 10, 64)
		case "tx_bytes":
			cur.down, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_sec":
			if n, _ := strconv.ParseInt(v, 10, 64); n > 0 {
				cur.lastHandshake = time.Unix(n, 0)
			}
		}
	}
	flush()
	return out
}

// totals is the pulse's reading: bytes over all peers, the probe's included as
// with the other protocols' shared credential, peers lately in touch, and each
// subscriber's key.
func (s *awgServer) totals(now time.Time) (up, down, conns int64, keys map[string]pulseKey) {
	stats, err := s.stats()
	keys = map[string]pulseKey{}
	if err != nil {
		log.Printf("nekko-edge: awg stats: %v", err)
		return 0, 0, 0, keys
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, st := range stats {
		up += st.up
		down += st.down
		if !st.lastHandshake.IsZero() && now.Sub(st.lastHandshake) < awgPeerActive {
			conns++
		}
		if s.peers[key] {
			keys[key] = pulseKey{Up: st.up, Down: st.down}
		}
	}
	return up, down, conns, keys
}

// awgTun is the device's tun.Device: packets go to and from a gVisor stack
// that accepts a connection to any address and hands it to the dialer.
type awgTun struct {
	ep       *channel.Endpoint
	stack    *stack.Stack
	events   chan wgtun.Event
	incoming chan *buffer.View
	handle   *channel.NotificationHandle
	once     sync.Once
}

const awgNIC = 1

func newAwgTun(ctx context.Context, dial awgDialer) (*awgTun, error) {
	t := &awgTun{
		ep: channel.New(1024, awgMTU, ""),
		stack: stack.New(stack.Options{
			NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
			TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		}),
		events:   make(chan wgtun.Event, 1),
		incoming: make(chan *buffer.View),
	}
	sack := tcpip.TCPSACKEnabled(true)
	if err := t.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		return nil, fmt.Errorf("tcp sack: %v", err)
	}
	t.handle = t.ep.AddNotify(t)
	if err := t.stack.CreateNIC(awgNIC, t.ep); err != nil {
		return nil, fmt.Errorf("nic: %v", err)
	}
	// Every address is ours: the stack stands in for the whole internet.
	if err := t.stack.SetPromiscuousMode(awgNIC, true); err != nil {
		return nil, fmt.Errorf("promiscuous: %v", err)
	}
	if err := t.stack.SetSpoofing(awgNIC, true); err != nil {
		return nil, fmt.Errorf("spoofing: %v", err)
	}
	t.stack.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: awgNIC}})

	t.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcp.NewForwarder(t.stack, 0, 4096, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)
		ep.SocketOptions().SetKeepAlive(true)
		client := gonet.NewTCPConn(&wq, ep)
		dest := xnet.TCPDestination(xnet.IPAddress(id.LocalAddress.AsSlice()), xnet.Port(id.LocalPort))
		go relayFlow(ctx, client, dest, dial, 0)
	}).HandlePacket)
	t.stack.SetTransportProtocolHandler(udp.ProtocolNumber, udp.NewForwarder(t.stack, func(r *udp.ForwarderRequest) bool {
		id := r.ID()
		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			return true
		}
		client := gonet.NewUDPConn(&wq, ep)
		dest := xnet.UDPDestination(xnet.IPAddress(id.LocalAddress.AsSlice()), xnet.Port(id.LocalPort))
		go relayFlow(ctx, client, dest, dial, awgUDPIdle)
		return true
	}).HandlePacket)

	t.events <- wgtun.EventUp
	return t, nil
}

// relayFlow carries one client flow to its destination until either end ends.
// idle, when set, ends a flow that went quiet: UDP has no close of its own.
func relayFlow(ctx context.Context, client net.Conn, dest xnet.Destination, dial awgDialer, idle time.Duration) {
	defer client.Close()
	flowCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	remote, err := dial(flowCtx, dest)
	if err != nil {
		return
	}
	defer remote.Close()

	var mu sync.Mutex
	last := time.Now()
	touch := func() {
		mu.Lock()
		last = time.Now()
		mu.Unlock()
	}
	done := make(chan struct{}, 2)
	pump := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				touch()
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go pump(remote, client)
	go pump(client, remote)

	var quiet <-chan time.Time
	if idle > 0 {
		ticker := time.NewTicker(idle / 4)
		defer ticker.Stop()
		quiet = ticker.C
	}
	for {
		select {
		case <-done:
			return
		case <-flowCtx.Done():
			return
		case <-quiet:
			mu.Lock()
			stale := time.Since(last) > idle
			mu.Unlock()
			if stale {
				return
			}
		}
	}
}

// The tun.Device of a process with no interface.

func (t *awgTun) File() *os.File             { return nil }
func (t *awgTun) MTU() (int, error)          { return awgMTU, nil }
func (t *awgTun) Name() (string, error)      { return "awg", nil }
func (t *awgTun) Events() <-chan wgtun.Event { return t.events }
func (t *awgTun) BatchSize() int             { return 1 }

func (t *awgTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	view, ok := <-t.incoming
	if !ok {
		return 0, os.ErrClosed
	}
	n, err := view.Read(bufs[0][offset:])
	if err != nil {
		return 0, err
	}
	sizes[0] = n
	return 1, nil
}

func (t *awgTun) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		packet := b[offset:]
		if len(packet) == 0 {
			continue
		}
		if packet[0]>>4 != 4 {
			// The tunnel carries IPv4: a client's IPv6 has nowhere to go here.
			if packet[0]>>4 == 6 {
				continue
			}
			return 0, syscall.EAFNOSUPPORT
		}
		pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(packet)})
		t.ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
		pkb.DecRef()
	}
	return len(bufs), nil
}

// WriteNotify is the stack saying it has a packet for the device.
func (t *awgTun) WriteNotify() {
	pkt := t.ep.Read()
	if pkt == nil {
		return
	}
	view := pkt.ToView()
	pkt.DecRef()
	t.incoming <- view
}

func (t *awgTun) Close() error {
	t.once.Do(func() {
		t.stack.RemoveNIC(awgNIC)
		t.stack.Close()
		t.ep.RemoveNotify(t.handle)
		t.ep.Close()
		close(t.events)
		close(t.incoming)
	})
	return nil
}
