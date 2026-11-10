package main

// v1.04 observability on the stream channel (hikkari-protocol spec/v1.md):
// the kernel's numbers of every TCP connection that carries a Hikkari key's
// tunnels, the cascade's trunk members apart. A listener wrapper sees each
// connection come and go; the Naive gate tells which key it carries, and only
// then does it count, so the site's visitors and probers stay out.
//
//	accept ─► hikkari_watch (raw TCP) ─► tls ─► h2 ─► hikkari_naive: claim(remote, group)
//	close  ─► the last TCP_INFO, the connection's last period

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/m1d1x/hikkari-protocol/observe"
)

// tcpSample is one TCP_INFO reading: the smoothed RTT, its variance, the
// congestion window in segments, and counters since the connection opened.
type tcpSample struct {
	RTT, RTTVar time.Duration
	Cwnd        int64
	SegsOut     int64
	Retrans     int64
	BytesOut    int64 // acked by the peer
	BytesIn     int64
}

// tcpWatches are the connections the listener wrapper holds now, by remote
// address: a request's RemoteAddr names its connection.
type tcpWatches struct {
	mu    sync.Mutex
	conns map[string]*tcpWatch
	book  *observe.Book
}

func newTCPWatches(book *observe.Book) *tcpWatches {
	return &tcpWatches{conns: map[string]*tcpWatch{}, book: book}
}

// activeWatches is the runtime's set, for the listener wrapper Caddy builds.
var activeWatches = struct {
	sync.Mutex
	w *tcpWatches
}{}

func setActiveWatches(w *tcpWatches) {
	activeWatches.Lock()
	activeWatches.w = w
	activeWatches.Unlock()
}

func currentWatches() *tcpWatches {
	activeWatches.Lock()
	defer activeWatches.Unlock()
	return activeWatches.w
}

// claim counts the connection from remote under group, from its first
// claim; later claims of another group do not move it.
func (ws *tcpWatches) claim(remote, group string) {
	if ws == nil {
		return
	}
	ws.mu.Lock()
	w := ws.conns[remote]
	ws.mu.Unlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.booked || w.ended {
		w.mu.Unlock()
		return
	}
	w.booked = true
	w.mu.Unlock()
	remove := ws.book.Add(group, w.take)
	w.mu.Lock()
	w.unbook = remove
	ended := w.ended
	w.mu.Unlock()
	if ended {
		remove()
	}
}

type tcpWatch struct {
	raw syscall.RawConn

	mu       sync.Mutex
	booked   bool
	fresh    bool // not counted in a period yet
	ended    bool
	reported bool // its end counted
	last     tcpSample
	final    tcpSample
	finalOK  bool
	unbook   func()
}

func (w *tcpWatch) read() (tcpSample, bool) {
	var s tcpSample
	var ok bool
	err := w.raw.Control(func(fd uintptr) { s, ok = tcpInfo(fd) })
	return s, ok && err == nil
}

// take adds the connection's period to p; the Book calls it.
func (w *tcpWatch) take(p *observe.Period) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reported {
		return
	}
	var s tcpSample
	var ok bool
	if w.ended {
		s, ok = w.final, w.finalOK
	} else {
		s, ok = w.read()
	}
	if p.TCP == nil {
		p.TCP = &observe.TCP{}
	}
	t := p.TCP
	t.Conns++
	if w.fresh {
		t.Opened++
		w.fresh = false
	}
	if w.ended {
		t.Ended++
		w.reported = true
	}
	if !ok {
		return
	}
	segs := max(s.SegsOut-w.last.SegsOut, 0)
	retrans := max(s.Retrans-w.last.Retrans, 0)
	t.SegsOut += segs
	t.Retrans += retrans
	t.BytesOut += max(s.BytesOut-w.last.BytesOut, 0)
	t.BytesIn += max(s.BytesIn-w.last.BytesIn, 0)
	if s.RTT > 0 {
		t.RTT.Add(observe.Ms(s.RTT))
		t.RTTVar.Add(observe.Ms(s.RTTVar))
		t.Cwnd.Add(float64(s.Cwnd))
	}
	if segs > 0 {
		t.Share.Add(float64(retrans) * 1000 / float64(segs))
	}
	w.last = s
}

// watchConn is an accepted connection under watch.
type watchConn struct {
	net.Conn
	ws     *tcpWatches
	w      *tcpWatch
	remote string
	once   sync.Once
}

func (c *watchConn) Close() error {
	c.once.Do(func() {
		s, ok := c.w.read()
		c.w.mu.Lock()
		c.w.ended, c.w.final, c.w.finalOK = true, s, ok
		unbook := c.w.unbook
		c.w.mu.Unlock()
		c.ws.mu.Lock()
		if c.ws.conns[c.remote] == c.w {
			delete(c.ws.conns, c.remote)
		}
		c.ws.mu.Unlock()
		if unbook != nil {
			unbook()
		}
	})
	return c.Conn.Close()
}

// NetConn is the connection under the watch.
func (c *watchConn) NetConn() net.Conn { return c.Conn }

type watchListener struct {
	net.Listener
	ws *tcpWatches
}

func (l watchListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil || l.ws == nil {
		return conn, err
	}
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return conn, nil
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return conn, nil
	}
	w := &tcpWatch{raw: raw, fresh: true}
	remote := conn.RemoteAddr().String()
	l.ws.mu.Lock()
	l.ws.conns[remote] = w
	l.ws.mu.Unlock()
	return &watchConn{Conn: conn, ws: l.ws, w: w, remote: remote}, nil
}

// watchWrapper is the listener wrapper caddy.listeners.hikkari_watch; it goes
// before tls, so it holds the raw TCP connection.
type watchWrapper struct{}

func init() { caddy.RegisterModule(watchWrapper{}) }

func (watchWrapper) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.listeners.hikkari_watch", New: func() caddy.Module { return new(watchWrapper) }}
}

func (watchWrapper) WrapListener(l net.Listener) net.Listener {
	return watchListener{Listener: l, ws: currentWatches()}
}

var _ caddy.ListenerWrapper = (*watchWrapper)(nil)

// pathOf tells a client's own connection from one the cascade's trunk
// carried here: those arrive on loopback.
func pathOf(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return observe.PathEntry
	}
	return observe.PathDirect
}

// trunkGroup is the book's group of the trunk's members: one per relay, the
// trunk's keys are secrets the hub does not hold.
var trunkGroup = observe.RelayGroup("trunk", "")

// tunnelEnd names how a CONNECT tunnel ended. revoked and stopping are the
// runtime's own reasons; err is what the proxy returned.
func tunnelEnd(err error, revoked, stopping bool) string {
	var ne net.Error
	switch {
	case stopping:
		return observe.EndStop
	case revoked:
		return observe.EndRevoked
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed), errors.Is(err, io.ErrClosedPipe), errors.Is(err, context.Canceled):
		return observe.EndEOF
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return observe.EndIdle
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EPIPE):
		return observe.EndReset
	default:
		return observe.EndError
	}
}
