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

type egressCountry int

const (
	countryNL egressCountry = iota
	countryDE
	countryNone
	recoverySuccesses = 3
)

func (c egressCountry) String() string {
	return [...]string{"NL", "DE", "none"}[c]
}

func (c egressCountry) other() egressCountry { return countryDE - c }

// A country is confirmed down only when its peer proves both transports.
type egressChoice struct {
	down      egressCountry
	failures  [2]int
	successes [2]int
}

func (c *egressChoice) observe(health [2]egressHealth) egressCountry {
	for i, h := range health {
		if h.failed() {
			c.failures[i] = min(c.failures[i]+1, autoFailures)
		} else {
			c.failures[i] = 0
		}
		if h.healthy() {
			c.successes[i] = min(c.successes[i]+1, recoverySuccesses)
		} else {
			c.successes[i] = 0
		}
	}
	for i, failures := range c.failures {
		country := egressCountry(i)
		if failures == autoFailures && health[country.other()].healthy() {
			c.down = country
			return c.down
		}
	}
	if c.down != countryNone && c.successes[c.down] == recoverySuccesses {
		c.down = countryNone
	}
	return c.down
}

// Each public country port prefers its own backend. The backend paths retain
// their TCP and UDP flows independently of the preference for new flows.
type autoGateway struct {
	mu             sync.Mutex
	home, selected egressCountry
	paths          [2]*autoPath
	sessions       map[netip.AddrPort]autoUDPSession
	ctx            context.Context
	tcp            *net.TCPListener
	udp            *net.UDPConn
}

type autoPath struct {
	addr   netip.AddrPort
	ctx    context.Context
	cancel context.CancelFunc
}

type autoUDPSession struct {
	conn    *net.UDPConn
	country egressCountry
}

func newAutoGateway(ctx context.Context, listen netip.AddrPort, backends [2]netip.AddrPort, home egressCountry) (*autoGateway, error) {
	tcp, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(listen))
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(tcp.Addr().(*net.TCPAddr).AddrPort()))
	if err != nil {
		tcp.Close()
		return nil, err
	}
	g := &autoGateway{home: home, selected: home, sessions: make(map[netip.AddrPort]autoUDPSession), ctx: ctx, tcp: tcp, udp: udp}
	for i, addr := range backends {
		pathCtx, cancel := context.WithCancel(ctx)
		g.paths[i] = &autoPath{addr, pathCtx, cancel}
	}
	return g, nil
}

func (g *autoGateway) route(down egressCountry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	selected := g.home
	if down == g.home {
		selected = g.home.other()
	}
	if down != countryNone {
		g.paths[down].cancel()
		for peer, session := range g.sessions {
			if session.country == down {
				session.conn.Close()
				delete(g.sessions, peer)
			}
		}
	}
	path := g.paths[selected]
	if path.ctx.Err() != nil {
		ctx, cancel := context.WithCancel(g.ctx)
		g.paths[selected] = &autoPath{path.addr, ctx, cancel}
	}
	if selected != g.selected {
		log.Printf("country egress home=%s switched from=%s to=%s", g.home, g.selected, selected)
		g.selected = selected
	}
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
	for _, path := range g.paths {
		path.cancel()
	}
	for _, session := range g.sessions {
		session.conn.Close()
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
		path := g.paths[g.selected]
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
		conn := g.sessions[peer].conn
		if conn == nil && len(g.sessions) < autoUDPLimit {
			conn, err = net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(g.paths[g.selected].addr))
			if err == nil {
				g.sessions[peer] = autoUDPSession{conn, g.selected}
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
		if g.sessions[peer].conn == conn {
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
		if g.sessions[peer].conn != conn {
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
