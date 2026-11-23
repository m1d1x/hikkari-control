package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	autoFailures = 3
	autoUDPIdle  = 90 * time.Second
	autoUDPLimit = 4096
	autoUDPWrite = 20 * time.Millisecond
)

type egressHealth struct{ tcp, udp bool }

func (h egressHealth) healthy() bool { return h.tcp && h.udp }
func (h egressHealth) failed() bool  { return !h.tcp && !h.udp }

// egressChoice latches onto DE for the lifetime of the Auto gateway once NL
// loses both probes and DE proves both transports. A partial failure or loss
// of the points shared by both exits cannot move an otherwise working route.
type egressChoice struct {
	de       bool
	failures int
}

func (c *egressChoice) observe(nl, de egressHealth) bool {
	if c.de {
		return false
	}
	if !nl.failed() {
		c.failures = 0
		return false
	}
	if c.failures < autoFailures {
		c.failures++
	}
	if c.failures < autoFailures || !de.healthy() {
		return false
	}
	c.de = true
	return true
}

// autoGateway accepts the Auto profile on one TCP/UDP port. Each backend is
// the explicit country's already-warm trunk listener on this same entry.
// TLS and QUIC stay end to end with the selected Hikkari relay.
type autoGateway struct {
	mu       sync.Mutex
	choice   egressChoice
	path     *autoPath
	sessions map[netip.AddrPort]*net.UDPConn
	ctx      context.Context
	tcp      *net.TCPListener
	udp      *net.UDPConn
}

type autoPath struct {
	addr   netip.AddrPort
	ctx    context.Context
	cancel context.CancelFunc
}

func newAutoGateway(ctx context.Context, listen, nl netip.AddrPort) (*autoGateway, error) {
	tcp, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(listen))
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(tcp.Addr().(*net.TCPAddr).AddrPort()))
	if err != nil {
		tcp.Close()
		return nil, err
	}
	pathCtx, cancel := context.WithCancel(ctx)
	return &autoGateway{
		path: &autoPath{nl, pathCtx, cancel}, sessions: make(map[netip.AddrPort]*net.UDPConn),
		ctx: ctx, tcp: tcp, udp: udp,
	}, nil
}

func (g *autoGateway) observe(nl, de egressHealth, fallback netip.AddrPort) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.choice.observe(nl, de) {
		return
	}
	log.Printf("nekko-trunk-entry: auto egress failed country=NL consecutive=%d", autoFailures)
	g.path.cancel()
	for _, conn := range g.sessions {
		conn.Close()
	}
	clear(g.sessions)
	ctx, cancel := context.WithCancel(g.ctx)
	g.path = &autoPath{fallback, ctx, cancel}
	log.Print("nekko-trunk-entry: auto egress switched from=NL to=DE")
}

func (g *autoGateway) serve() error {
	closeListeners := func() {
		g.tcp.Close()
		g.udp.Close()
	}
	stop := context.AfterFunc(g.ctx, closeListeners)
	defer stop()
	errCh := make(chan error, 2)
	go func() { errCh <- g.serveTCP() }()
	go func() { errCh <- g.serveUDP() }()
	err := <-errCh
	closeListeners()
	<-errCh
	g.mu.Lock()
	g.path.cancel()
	for _, conn := range g.sessions {
		conn.Close()
	}
	clear(g.sessions)
	g.mu.Unlock()
	if g.ctx.Err() != nil {
		return nil
	}
	return err
}

func (g *autoGateway) serveTCP() error {
	for {
		conn, err := g.tcp.AcceptTCP()
		if err != nil {
			return err
		}
		g.mu.Lock()
		path := g.path
		g.mu.Unlock()
		go path.forwardTCP(conn)
	}
}

func (p *autoPath) forwardTCP(client *net.TCPConn) {
	defer client.Close()
	dialer := net.Dialer{Timeout: 2 * time.Second}
	upstream, err := dialer.DialContext(p.ctx, "tcp4", p.addr.String())
	if err != nil {
		return
	}
	backend := upstream.(*net.TCPConn)
	defer backend.Close()
	stop := context.AfterFunc(p.ctx, func() {
		client.Close()
		backend.Close()
	})
	defer stop()
	copyHalf := func(dst, src *net.TCPConn) {
		if _, err := io.Copy(dst, src); err != nil {
			// Wake the other direction on an abort without breaking TCP EOF.
			dst.Close()
			src.Close()
			return
		}
		dst.CloseWrite()
	}
	done := make(chan struct{})
	go func() {
		copyHalf(backend, client)
		close(done)
	}()
	copyHalf(client, backend)
	<-done
}

func (g *autoGateway) serveUDP() error {
	buf := make([]byte, 65535)
	for {
		n, peer, err := g.udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			return err
		}
		g.mu.Lock()
		conn := g.sessions[peer]
		if conn == nil && len(g.sessions) < autoUDPLimit {
			conn, err = net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(g.path.addr))
			if err == nil {
				g.sessions[peer] = conn
				go g.udpResponses(peer, conn)
			}
		}
		if conn != nil {
			now := time.Now()
			conn.SetReadDeadline(now.Add(autoUDPIdle))
			conn.SetWriteDeadline(now.Add(autoUDPWrite))
			conn.Write(buf[:n])
		}
		g.mu.Unlock()
	}
}

func (g *autoGateway) udpResponses(peer netip.AddrPort, conn *net.UDPConn) {
	defer conn.Close()
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.sessions[peer] == conn {
			delete(g.sessions, peer)
		}
	}()
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		g.mu.Lock()
		if g.sessions[peer] != conn {
			g.mu.Unlock()
			return
		}
		g.udp.SetWriteDeadline(time.Now().Add(autoUDPWrite))
		g.udp.WriteToUDPAddrPort(buf[:n], peer)
		g.mu.Unlock()
	}
}

func autoEgressAddress(raw string, loopback bool) (netip.AddrPort, error) {
	addr, err := netip.ParseAddrPort(raw)
	if err != nil || addr.Port() == 0 || !addr.Addr().Is4() {
		return netip.AddrPort{}, errors.New("ожидается IPv4:порт")
	}
	if loopback && !addr.Addr().IsLoopback() {
		return netip.AddrPort{}, fmt.Errorf("%s: бэкенд должен слушать на loopback", addr)
	}
	return addr, nil
}
