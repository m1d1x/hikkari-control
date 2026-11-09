package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	hkclient "github.com/m1d1x/hikkari-protocol/client"
	"github.com/m1d1x/hikkari-protocol/dup"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

// A Hikkari client with `dup=1` (v1.02, v1.03) does not use the datagram
// channel the way probeDatagram does. Every UDP association is a session of
// dup frames addressed to dup.hikkari.arpa:0, carried by the datagram channel,
// by a second QUIC connection for a call's voice, and by a copy inside Naive
// (CONNECT dup.hikkari.arpa:0); on the relay the dup hub joins the carriers,
// keeps one exit socket and, from v1.03, answers an attach with its lane. A
// plain Hysteria2 association never touches the hub, so a broken hub left the
// row at `datagram 100/100`.
//
// probeDup runs a real session with the client package of hikkari-protocol
// (Dup and VoiceDatagram, as the core's outbound builds it) and a call: 160 B
// every 20 ms to a public STUN server, long enough for the call to be seen and
// the stream copy to come up. Each carrier's frames are counted on the way in.
// The STUN answers say which address the far end saw, so the row knows
// whether the hub kept one exit socket for the session.
//
// The stream channel is the pinned Chromium core again (startNaiveProxy):
// the CONNECT goes through its local mixed inbound, so the copy rides the
// same Naive the client's does, not a Go imitation.
const (
	// dupPackets is 5 s of a call: one second to be seen as a call, a dial of
	// the stream copy, and the rest carried by every carrier.
	dupPackets = 250
	// dupCleanShare of the call must come back.
	dupCleanShare = 0.98
)

// dupProbe is one dup session's outcome.
type dupProbe struct {
	sent, got int
	// sources is how many reflexive addresses the STUN server reported: one
	// when the hub kept one exit socket for the session.
	sources int
	// lanes: the relay answered the attach (v1.03).
	lanes bool
	// voice and stream count answer frames by the voice datagram carrier and
	// by the stream copy, every copy: the first and the duplicates alike.
	voice, stream int
	medianMs      float32
}

// ok is the session's verdict: lanes kept, the call answered, one source at
// the far end, and the stream copy carrying answers for at least half of the
// call (it comes up after the first second).
func (p dupProbe) ok() bool {
	return p.sent > 0 && p.lanes && p.sources == 1 &&
		float64(p.got) >= dupCleanShare*float64(p.sent) && 2*p.stream >= p.sent
}

func (p dupProbe) detail() string {
	s := "dup " + strconv.Itoa(p.got) + "/" + strconv.Itoa(p.sent)
	if p.lanes {
		s += " lanes"
	} else {
		s += " nolanes"
	}
	return s + " voice " + strconv.Itoa(p.voice) + " naive " + strconv.Itoa(p.stream) + " src " + strconv.Itoa(p.sources)
}

// withDup folds the session into the row: its own columns beside the
// datagram door's, a word in the detail, and nothing in the verdict, which is
// the stream probe's.
func withDup(r probeResult, p dupProbe) probeResult {
	if r.Detail != "" {
		r.Detail += "; " + p.detail()
	} else {
		r.Detail = p.detail()
	}
	r.DupGot, r.DupSent = uint16(p.got), uint16(p.sent)
	r.DupVoice, r.DupNaive = uint16(p.voice), uint16(p.stream)
	r.DupSources = uint8(min(p.sources, 255))
	r.DupLanes = b2u8(p.lanes)
	r.DupOK = b2u8(p.ok())
	return r
}

// probeDupEdge runs the session against e through a fresh Naive core and
// folds it into res.
func probeDupEdge(ctx context.Context, binary string, e edge, res probeResult, timeout time.Duration) probeResult {
	port, stop, err := startNaiveProxy(ctx, binary, e)
	if err != nil {
		return withDup(res, dupProbe{})
	}
	defer stop()
	target, err := resolveTarget(ctx, datagramTarget)
	if err != nil {
		return withDup(res, dupProbe{})
	}
	return withDup(res, probeDup(ctx, dupSetup{
		server:   M.ParseSocksaddrHostPort(e.Address, uint16(e.Port)),
		host:     e.Naive.Host,
		key:      e.Naive.Pass,
		proxy:    net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		target:   target,
		packets:  dupPackets,
		interval: voiceInterval,
	}, timeout))
}

// resolveTarget gives the STUN server as an IPv4 address: the session's
// frames name it and the relay's exit takes it as it is.
func resolveTarget(ctx context.Context, hostPort string) (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return netip.AddrPort{}, err
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return netip.AddrPort{}, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil || len(addrs) == 0 {
		return netip.AddrPort{}, fmt.Errorf("resolve %s: %v", host, err)
	}
	return netip.AddrPortFrom(addrs[0].Unmap(), uint16(p)), nil
}

// dupSetup is where the session goes and how long the call is.
type dupSetup struct {
	server M.Socksaddr // the relay's datagram channel
	host   string      // the relay's domain
	key    string
	// roots trusts the relay's certificate; nil is the system's.
	roots *x509.CertPool
	// proxy is the Naive core's mixed inbound (HTTP CONNECT).
	proxy    string
	target   netip.AddrPort
	packets  int
	interval time.Duration
}

// carrierCount counts the frames one kind of carrier brought in.
type carrierCount struct {
	attach, packets atomic.Int32
}

func (c *carrierCount) frame(b []byte) {
	f, err := dup.Decode(b)
	if err != nil {
		return
	}
	switch f.Kind {
	case dup.KindAttach:
		c.attach.Add(1)
	case dup.KindPacket:
		c.packets.Add(1)
	}
}

func probeDup(ctx context.Context, s dupSetup, timeout time.Duration) dupProbe {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var primary, voice, stream carrierCount
	newDatagram := func(count *carrierCount) (*countedDatagram, error) {
		d, err := hkclient.NewDatagram(hkclient.DatagramOptions{
			Context:  ctx,
			Dialer:   N.SystemDialer,
			Server:   s.server,
			Password: s.key,
			TLS:      &stdTLS{config: &tls.Config{ServerName: s.host, RootCAs: s.roots}},
		})
		if err != nil {
			return nil, err
		}
		return &countedDatagram{Datagram: d, count: count}, nil
	}
	primaryChannel, err := newDatagram(&primary)
	if err != nil {
		return dupProbe{}
	}
	defer primaryChannel.Close()
	voiceChannel, err := newDatagram(&voice)
	if err != nil {
		return dupProbe{}
	}
	defer voiceChannel.Close()
	c := hkclient.New(hkclient.Options{
		Stream:        &proxyStream{proxy: s.proxy, count: &stream},
		Datagram:      primaryChannel,
		VoiceDatagram: voiceChannel,
		Dup:           true,
	})
	conn, err := c.ListenPacket(ctx, M.Socksaddr{})
	if err != nil {
		return dupProbe{}
	}
	defer conn.Close()
	p := stunCall(ctx, conn, net.UDPAddrFromAddrPort(s.target), s.packets, s.interval, timeout)
	p.lanes = primary.attach.Load() > 0
	p.voice = int(voice.packets.Load())
	p.stream = int(stream.packets.Load())
	return p
}

// stunCall is stunBurst on a packet connection, with the far end's view of
// the source kept: warm-up, then packets binding requests every interval,
// then up to timeout for stragglers.
func stunCall(ctx context.Context, conn net.PacketConn, to net.Addr, packets int, interval, timeout time.Duration) dupProbe {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	buf := make([]byte, 2048)
	sources := map[netip.AddrPort]bool{}

	warmed := false
	for deadline := time.Now().Add(datagramWarmup); !warmed && time.Now().Before(deadline) && ctx.Err() == nil; {
		_, _ = conn.WriteTo(stunRequest(1<<31, nonce), to)
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if n, _, err := conn.ReadFrom(buf); err == nil {
			if _, ok := stunSeq(buf[:n], nonce); ok {
				warmed = true
				if a, ok := stunMapped(buf[:n]); ok {
					sources[a] = true
				}
			}
		}
	}
	if !warmed {
		return dupProbe{sent: packets}
	}
	_ = conn.SetReadDeadline(time.Time{})

	var mu sync.Mutex
	sentAt := make(map[uint32]time.Time, packets)
	var rtts []time.Duration
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFrom(b)
			if err != nil {
				return
			}
			seq, ok := stunSeq(b[:n], nonce)
			if !ok {
				continue
			}
			mu.Lock()
			if a, ok := stunMapped(b[:n]); ok {
				sources[a] = true
			}
			if at, ok := sentAt[seq]; ok {
				rtts = append(rtts, time.Since(at))
				delete(sentAt, seq)
			}
			mu.Unlock()
		}
	}()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	sent := 0
	for seq := uint32(0); seq < uint32(packets); seq++ {
		mu.Lock()
		sentAt[seq] = time.Now()
		mu.Unlock()
		if _, err := conn.WriteTo(stunRequest(seq, nonce), to); err == nil {
			sent++
		}
		select {
		case <-ctx.Done():
			return dupProbe{}
		case <-tick.C:
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	<-done
	mu.Lock()
	defer mu.Unlock()
	p := dupProbe{sent: sent, got: len(rtts), sources: len(sources)}
	if len(rtts) > 0 {
		sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
		p.medianMs = float32(rtts[len(rtts)/2].Microseconds()) / 1000
	}
	return p
}

// stunMapped reads the reflexive address of a binding success response:
// XOR-MAPPED-ADDRESS, or MAPPED-ADDRESS from an older server. IPv4 only.
func stunMapped(b []byte) (netip.AddrPort, bool) {
	if len(b) < 20 || binary.BigEndian.Uint16(b) != 0x0101 {
		return netip.AddrPort{}, false
	}
	end := 20 + int(binary.BigEndian.Uint16(b[2:]))
	if end > len(b) {
		end = len(b)
	}
	var mapped netip.AddrPort
	found := false
	for i := 20; i+4 <= end; {
		typ, l := binary.BigEndian.Uint16(b[i:]), int(binary.BigEndian.Uint16(b[i+2:]))
		v := b[i+4:]
		if len(v) < l {
			break
		}
		v = v[:l]
		if l >= 8 && v[1] == 0x01 {
			port := binary.BigEndian.Uint16(v[2:])
			var ip [4]byte
			copy(ip[:], v[4:8])
			switch typ {
			case 0x0020:
				port ^= stunMagic >> 16
				binary.BigEndian.PutUint32(ip[:], binary.BigEndian.Uint32(ip[:])^stunMagic)
				return netip.AddrPortFrom(netip.AddrFrom4(ip), port), true
			case 0x0001:
				mapped, found = netip.AddrPortFrom(netip.AddrFrom4(ip), port), true
			}
		}
		i += 4 + (l+3)&^3
	}
	return mapped, found
}

// proxyStream is the stream channel through the Naive core's mixed inbound:
// an HTTP CONNECT to the local port, which the core's naive outbound carries
// to the relay. Frames read back are counted for the row.
type proxyStream struct {
	proxy string
	count *carrierCount
}

func (s *proxyStream) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkTCP {
		return nil, N.ErrUnknownNetwork
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.proxy)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	target := dest.String()
	if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		conn.Close()
		return nil, err
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("CONNECT %s: %s", target, resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	return &countedStream{Conn: conn, r: r, count: s.count}, nil
}

func (s *proxyStream) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("dup probe: no UDP-over-TCP")
}

// countedStream counts the dup frames the relay sends down the stream copy,
// each after a uint16 length, as they pass to the client package's reader.
type countedStream struct {
	net.Conn
	r     *bufio.Reader
	count *carrierCount
	buf   []byte
}

func (c *countedStream) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.buf = append(c.buf, p[:n]...)
	for len(c.buf) >= 2 {
		l := int(binary.BigEndian.Uint16(c.buf))
		if len(c.buf) < 2+l {
			break
		}
		c.count.frame(c.buf[2 : 2+l])
		c.buf = c.buf[2+l:]
	}
	return n, err
}

// countedDatagram is a datagram channel whose packet connections count the
// frames they bring in.
type countedDatagram struct {
	*hkclient.Datagram
	count *carrierCount
}

func (d *countedDatagram) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	p, err := d.Datagram.ListenPacket(ctx, dest)
	if err != nil {
		return nil, err
	}
	return &countedPacket{PacketConn: p, count: d.count}, nil
}

type countedPacket struct {
	net.PacketConn
	count *carrierCount
}

func (p *countedPacket) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := p.PacketConn.ReadFrom(b)
	if err == nil {
		p.count.frame(b[:n])
	}
	return n, addr, err
}

// stdTLS is a crypto/tls client config as sing's Config; QUIC reads only
// STDConfig.
type stdTLS struct{ config *tls.Config }

func (c *stdTLS) ServerName() string                  { return c.config.ServerName }
func (c *stdTLS) SetServerName(name string)           { c.config.ServerName = name }
func (c *stdTLS) NextProtos() []string                { return c.config.NextProtos }
func (c *stdTLS) SetNextProtos(protos []string)       { c.config.NextProtos = protos }
func (c *stdTLS) HandshakeTimeout() time.Duration     { return 0 }
func (c *stdTLS) SetHandshakeTimeout(time.Duration)   {}
func (c *stdTLS) STDConfig() (*aTLS.STDConfig, error) { return c.config, nil }
func (c *stdTLS) Client(net.Conn) (aTLS.Conn, error)  { return nil, errors.New("dup probe: QUIC only") }
func (c *stdTLS) Clone() aTLS.Config                  { return &stdTLS{config: c.config.Clone()} }
