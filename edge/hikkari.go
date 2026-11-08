package main

// Hikkari v1.01 is one protocol with two channels on one relay and one key:
//
//	tcp/443  Naive: Caddy/forwardproxy, HTTP/2 CONNECT, Naive padding (naive.go)
//	         + UDP-over-TCP inside the same CONNECT tunnel, the fallback
//	         when the datagram channel does not pass (uot below)
//	udp/443  datagrams: QUIC + HTTP/3 auth, UDP as QUIC DATAGRAM frames, the
//	         Hysteria2 wire (sing-quic, the library the client's core speaks);
//	         an unauthenticated HTTP/3 request gets the same site as tcp/443
//
// A client sends TCP over the first and UDP over the second; when the second is
// dead it sends UDP over the first. Both channels admit the same key roster
// (naiveRuntime.keys), count into the same totals and are revoked together.
//
// EDGE_PROTO stays naive: a Hikkari relay is a Naive relay plus EDGE_HIKKARI_UDP=1,
// so links of clients that know only Naive keep working on it.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/forwardproxy"
	"github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
	"github.com/sagernet/sing/common/uot"
)

// hikkariUDPIdle is how long a forwarded UDP flow lives without a packet. Long
// on purpose: a muted call pauses its ICE consent checks (voice.go).
const hikkariUDPIdle = 600 * time.Second

// isUoTHost reports whether a CONNECT asks for the UDP-over-TCP fallback.
func isUoTHost(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	return host == uot.MagicAddress || host == uot.LegacyMagicAddress
}

// withUoT puts the UDP-over-TCP server on the request context: forwardproxy
// answers the CONNECT, strips the Naive padding and hands the bytes to one end
// of a pipe, the relay below reads the other end.
func (n *naiveRuntime) withUoT(ctx context.Context, hostPort string) context.Context {
	host, _, _ := net.SplitHostPort(hostPort)
	version := uot.Version
	if host == uot.LegacyMagicAddress {
		version = uot.LegacyVersion
	}
	return context.WithValue(ctx, forwardproxy.ContextKeyDial{}, func(ctx context.Context) (net.Conn, error) {
		proxySide, edgeSide := net.Pipe()
		go func() {
			defer edgeSide.Close()
			stop := context.AfterFunc(ctx, func() { _ = edgeSide.Close() })
			defer stop()
			var request uot.Request
			if version == uot.Version {
				r, err := uot.ReadRequest(edgeSide)
				if err != nil {
					return
				}
				request = *r
			}
			// Bytes are counted by naiveBody/naiveWriter around the tunnel.
			n.uotFlows.Add(1)
			_ = relayPackets(ctx, uot.NewConn(edgeSide, request), request, nil)
		}()
		return proxySide, nil
	})
}

// egress applies egress.go's exit policy to a destination the edge dials
// itself (the datagram channel and UoT), outside Xray's finalRules.
type egress struct {
	nets []netip.Prefix
}

var exitPolicy = sync.OnceValue(func() *egress {
	e := &egress{}
	for _, cidr := range blockedNets {
		if p, err := netip.ParsePrefix(cidr); err == nil {
			e.nets = append(e.nets, p.Masked())
		} else if a, err := netip.ParseAddr(cidr); err == nil {
			e.nets = append(e.nets, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			if p, err := netip.ParsePrefix(address.String()); err == nil {
				e.nets = append(e.nets, netip.PrefixFrom(p.Addr().Unmap(), p.Addr().BitLen()))
			}
		}
	}
	return e
})

var errRefused = errors.New("destination refused by exit policy")

func (e *egress) addrAllowed(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() {
		return false
	}
	for _, p := range e.nets {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func domainRefused(fqdn string) bool {
	fqdn = strings.TrimSuffix(strings.ToLower(fqdn), ".")
	for _, d := range blockedDomains {
		d = strings.TrimPrefix(d, "domain:")
		if fqdn == d || strings.HasSuffix(fqdn, "."+d) {
			return true
		}
	}
	return false
}

// resolve returns the first allowed address of a destination.
func (e *egress) resolve(ctx context.Context, dest M.Socksaddr) (netip.AddrPort, error) {
	if dest.Port == 0 {
		return netip.AddrPort{}, errRefused
	}
	if dest.IsFqdn() {
		if domainRefused(dest.Fqdn) {
			return netip.AddrPort{}, errRefused
		}
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", dest.Fqdn)
		if err != nil {
			return netip.AddrPort{}, err
		}
		for _, a := range addrs {
			if e.addrAllowed(a) {
				return netip.AddrPortFrom(a.Unmap(), dest.Port), nil
			}
		}
		return netip.AddrPort{}, errRefused
	}
	if !e.addrAllowed(dest.Addr) {
		return netip.AddrPort{}, errRefused
	}
	return netip.AddrPortFrom(dest.Addr.Unmap(), dest.Port), nil
}

func (e *egress) dialTCP(ctx context.Context, dest M.Socksaddr) (net.Conn, error) {
	if strconv.Itoa(int(dest.Port)) == blockedPorts {
		return nil, errRefused
	}
	ap, err := e.resolve(ctx, dest)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", ap.String())
}

// relayPackets carries one UDP association between a client channel and a
// socket of the box until either side ends or the flow idles out. A fixed
// request (UoT connect mode) sends every packet to its destination; otherwise
// each packet names its own. count, when set, receives bytes up and down.
func relayPackets(ctx context.Context, client N.PacketConn, request uot.Request, count func(up, down int)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sock, err := net.ListenUDP("udp", nil)
	if err != nil {
		return err
	}
	defer sock.Close()
	defer client.Close()
	context.AfterFunc(ctx, func() { _ = sock.Close(); _ = client.Close() })
	policy := exitPolicy()
	// Answers keep the destination the client asked for: a client that sent to
	// a name must see the name back, not the address it resolved to.
	var names sync.Map // netip.AddrPort -> M.Socksaddr
	resolved := map[string]netip.AddrPort{}
	idle := time.AfterFunc(hikkariUDPIdle, cancel)
	defer idle.Stop()

	go func() {
		defer cancel()
		b := make([]byte, 65535)
		for {
			nr, from, err := sock.ReadFromUDPAddrPort(b)
			if err != nil {
				return
			}
			idle.Reset(hikkariUDPIdle)
			from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
			dest := M.SocksaddrFromNetIP(from)
			if name, ok := names.Load(from); ok {
				dest = name.(M.Socksaddr)
			}
			packet := buf.NewSize(nr)
			_, _ = packet.Write(b[:nr])
			if err := client.WritePacket(packet, dest); err != nil {
				return
			}
			if count != nil {
				count(0, nr)
			}
		}
	}()

	for {
		packet := buf.NewPacket()
		dest, err := client.ReadPacket(packet)
		if err != nil {
			packet.Release()
			return nil
		}
		idle.Reset(hikkariUDPIdle)
		if request.IsConnect {
			dest = request.Destination
		}
		key := dest.String()
		ap, ok := resolved[key]
		if !ok {
			ap, err = policy.resolve(ctx, dest)
			if err != nil {
				packet.Release()
				continue
			}
			if len(resolved) < 4096 {
				resolved[key] = ap
			}
			if dest.IsFqdn() {
				names.Store(ap, dest)
			}
		}
		size := packet.Len()
		_, err = sock.WriteToUDPAddrPort(packet.Bytes(), ap)
		packet.Release()
		if err == nil && count != nil {
			count(size, 0)
		}
	}
}

// hikkariUDP is the datagram channel of one relay.
type hikkariUDP struct {
	n       *naiveRuntime
	service *hysteria2.Service[string]
	conn    net.PacketConn
}

// startHikkariUDP opens udp/<port> of n's listener with the relay's ACME
// certificate (Caddy's storage) and the roster n admits.
func startHikkariUDP(ctx context.Context, n *naiveRuntime) (*hikkariUDP, error) {
	certs := &caddyCerts{storage: n.cfg.Storage, host: n.cfg.Host}
	h := &hikkariUDP{n: n}
	service, err := hysteria2.NewService[string](hysteria2.ServiceOptions{
		Context:           ctx,
		Logger:            quietLogger{},
		TLSConfig:         &serverTLS{config: &tls.Config{GetCertificate: certs.get, NextProtos: []string{"h3"}, MinVersion: tls.VersionTLS13}},
		UDPTimeout:        hikkariUDPIdle,
		Handler:           h,
		MasqueradeHandler: http.FileServer(http.Dir(n.cfg.Root)),
	})
	if err != nil {
		return nil, fmt.Errorf("hikkari udp: %w", err)
	}
	h.service = service
	n.onKeys = h.setUsers
	h.setUsers(n.roster())
	h.conn, err = net.ListenPacket("udp", n.cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("hikkari udp: %w", err)
	}
	if err := service.Start(h.conn); err != nil {
		h.conn.Close()
		return nil, fmt.Errorf("hikkari udp: %w", err)
	}
	log.Printf("nekko-edge: hikkari datagram channel on udp %s", n.cfg.Listen)
	return h, nil
}

// setUsers gives the channel the roster: user = password = access key, and the
// relay's probe credential under the empty user, which counts nowhere.
func (h *hikkariUDP) setUsers(keys []string) {
	users := append([]string{""}, keys...)
	passwords := append([]string{h.n.cfg.Pass}, keys...)
	h.service.UpdateUsers(users, passwords)
}

func (h *hikkariUDP) Close() {
	_ = h.service.Close()
	if h.conn != nil {
		_ = h.conn.Close()
	}
}

// NewConnectionEx serves a TCP stream on the datagram channel. The Hikkari
// client sends TCP over Naive, but a stream here is the same grant: allowed.
func (h *hikkariUDP) NewConnectionEx(ctx context.Context, conn net.Conn, _ M.Socksaddr, dest M.Socksaddr, onClose N.CloseHandlerFunc) {
	key, _ := auth.UserFromContext[string](ctx)
	go func() {
		defer closeWith(onClose)
		defer conn.Close()
		ctx, done, count := h.n.admit(ctx, key)
		if ctx == nil {
			return
		}
		defer done()
		target, err := exitPolicy().dialTCP(ctx, dest)
		if err != nil {
			return
		}
		defer target.Close()
		context.AfterFunc(ctx, func() { _ = conn.Close(); _ = target.Close() })
		go func() {
			nr, _ := io.Copy(target, conn)
			count(int(nr), 0)
			_ = target.Close()
		}()
		nr, _ := io.Copy(conn, target)
		count(0, int(nr))
	}()
}

// NewPacketConnectionEx serves one UDP association on the datagram channel.
func (h *hikkariUDP) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, _ M.Socksaddr, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	key, _ := auth.UserFromContext[string](ctx)
	go func() {
		defer closeWith(onClose)
		ctx, done, count := h.n.admit(ctx, key)
		if ctx == nil {
			_ = conn.Close()
			return
		}
		defer done()
		h.n.datagramFlows.Add(1)
		_ = relayPackets(ctx, conn, uot.Request{}, count)
	}()
}

func closeWith(onClose N.CloseHandlerFunc) {
	if onClose != nil {
		onClose(nil)
	}
}

// admit opens a session for a key the roster holds now (the empty key is the
// probe credential): a context revoked with the key, a done func and a byte
// counter. A nil context means refused.
func (n *naiveRuntime) admit(parent context.Context, key string) (context.Context, func(), func(up, down int)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || (key != "" && !n.keys[key]) {
		return nil, nil, nil
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
	return ctx, done, count
}

// roster is the key set as a list.
func (n *naiveRuntime) roster() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return sortedKeys(n.keys)
}

// caddyCerts serves the certificate Caddy keeps for the relay's domain from its
// file storage, reloading when Caddy renews it.
type caddyCerts struct {
	storage, host string
	mu            sync.Mutex
	path          string
	mtime         time.Time
	cert          *tls.Certificate
}

func (c *caddyCerts) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(c.storage, "certificates", "*", c.host, c.host+".crt"))
	var newest string
	var newestTime time.Time
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.ModTime().After(newestTime) {
			newest, newestTime = m, st.ModTime()
		}
	}
	if newest == "" {
		if c.cert != nil {
			return c.cert, nil
		}
		return nil, errors.New("hikkari udp: no certificate yet for " + c.host)
	}
	if newest == c.path && newestTime.Equal(c.mtime) && c.cert != nil {
		return c.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(newest, strings.TrimSuffix(newest, ".crt")+".key")
	if err != nil {
		if c.cert != nil {
			return c.cert, nil
		}
		return nil, err
	}
	c.path, c.mtime, c.cert = newest, newestTime, &cert
	return c.cert, nil
}

// serverTLS adapts a crypto/tls config to sing's ServerConfig; QUIC only reads
// STDConfig.
type serverTLS struct{ config *tls.Config }

func (s *serverTLS) ServerName() string                  { return s.config.ServerName }
func (s *serverTLS) SetServerName(name string)           { s.config.ServerName = name }
func (s *serverTLS) NextProtos() []string                { return s.config.NextProtos }
func (s *serverTLS) SetNextProtos(protos []string)       { s.config.NextProtos = protos }
func (s *serverTLS) HandshakeTimeout() time.Duration     { return 0 }
func (s *serverTLS) SetHandshakeTimeout(time.Duration)   {}
func (s *serverTLS) STDConfig() (*aTLS.STDConfig, error) { return s.config, nil }
func (s *serverTLS) Client(net.Conn) (aTLS.Conn, error)  { return nil, errors.ErrUnsupported }
func (s *serverTLS) Server(net.Conn) (aTLS.Conn, error)  { return nil, errors.ErrUnsupported }
func (s *serverTLS) Clone() aTLS.Config                  { return &serverTLS{config: s.config.Clone()} }
func (s *serverTLS) Start() error                        { return nil }
func (s *serverTLS) Close() error                        { return nil }

// quietLogger drops sing-quic's per-connection chatter: it would name
// destinations, and the edge logs none.
type quietLogger struct{}

func (quietLogger) Trace(...any) {}
func (quietLogger) Debug(...any) {}
func (quietLogger) Info(...any)  {}
func (quietLogger) Warn(...any)  {}
func (quietLogger) Error(...any) {}
func (quietLogger) Fatal(...any) {}
func (quietLogger) Panic(...any) {}
