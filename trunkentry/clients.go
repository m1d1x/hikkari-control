package main

// The leg client → entry, as one sum over every client of the minute (the
// owner's 14.11: loss and ping from the client to the Russian entry on the
// fleet screen). The entry is blind (roadmap 3.2, decision of 10.11): no line
// names a client, an address, a stream or where it goes; this is the kernel's
// TCP view of all the clients' sockets added up, as the minute line adds up
// the pool. What it counts:
//
//   - segs: TCP segments the entry sent to its clients in the minute;
//   - retrans: of those, how many it sent again (the kernel's
//     tcpi_total_retrans): loss on the way down, entry → client, and of the
//     acks back; per 1000 segments it is the leg's loss;
//   - rtt_p50, rtt_p90: the kernel's smoothed round trip of each socket that
//     sent in the minute, in ms, across the sockets.
//
// The UDP channel of the clients (QUIC datagrams) is the client's cipher end
// to end: the entry sees no loss in it, so it is not here.
//
// A socket is read each minute while it lives and once more when it closes
// (clientConn.Close), so a short connection counts in the minute it ended.
// The hub reads the line (hikkari-control internal/cascadewatch clients.go):
// its keys are that reader's contract.

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"
)

// clientRTTsMax caps the round trips a minute keeps for its percentiles.
const clientRTTsMax = 8192

// clientLegs counts the clients' sockets of one generation.
type clientLegs struct {
	mu            sync.Mutex
	live          map[*clientConn]tcpSample
	segs, retrans int64
	rtts          []time.Duration
	closed        int
}

func newClientLegs() *clientLegs { return &clientLegs{live: map[*clientConn]tcpSample{}} }

// clientConn is a client's TCP connection that adds its last minute up when
// it closes. It keeps every method of the connection (CloseWrite and
// SyscallConn among them: the trunk half-closes it, observe reads it).
type clientConn struct {
	*net.TCPConn
	legs *clientLegs
	once sync.Once
}

// track wraps an accepted connection.
func (l *clientLegs) track(c *net.TCPConn) *clientConn {
	cc := &clientConn{TCPConn: c, legs: l}
	l.mu.Lock()
	l.live[cc] = tcpSample{}
	l.mu.Unlock()
	return cc
}

// Close reads the socket one last time, then closes it.
func (c *clientConn) Close() error {
	c.once.Do(func() {
		s, ok := clientTCP(c.TCPConn)
		c.legs.mu.Lock()
		if last, live := c.legs.live[c]; live {
			if ok {
				c.legs.addLocked(last, s)
			}
			delete(c.legs.live, c)
			c.legs.closed++
		}
		c.legs.mu.Unlock()
	})
	return c.TCPConn.Close()
}

// addLocked counts what a socket did since its last reading.
func (l *clientLegs) addLocked(last, now tcpSample) {
	segs := now.SegsOut - last.SegsOut
	if segs <= 0 {
		return
	}
	l.segs += segs
	if r := now.Retrans - last.Retrans; r > 0 {
		l.retrans += r
	}
	if now.RTT > 0 && len(l.rtts) < clientRTTsMax {
		l.rtts = append(l.rtts, now.RTT)
	}
}

// clientMinute is one minute of the leg.
type clientMinute struct {
	from          time.Time
	conns, closed int
	segs, retrans int64
	rtts          []time.Duration
}

// minute reads every live socket and hands the minute over, starting the
// next one.
func (l *clientLegs) minute(from time.Time) clientMinute {
	l.mu.Lock()
	conns := make([]*clientConn, 0, len(l.live))
	for c := range l.live {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	// The kernel is read outside the lock: a Close in between is counted
	// by itself, once.
	read := make(map[*clientConn]tcpSample, len(conns))
	for _, c := range conns {
		if s, ok := clientTCP(c.TCPConn); ok {
			read[c] = s
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for c, s := range read {
		last, live := l.live[c]
		if !live {
			continue
		}
		l.addLocked(last, s)
		l.live[c] = s
	}
	m := clientMinute{from: from, conns: len(l.live), closed: l.closed, segs: l.segs, retrans: l.retrans, rtts: l.rtts}
	l.segs, l.retrans, l.rtts, l.closed = 0, 0, nil, 0
	return m
}

// clientLine is the minute: from is its start (unix), loss_pm the
// retransmissions per 1000 segments, the round trips in ms; "-" where no
// socket sent.
func clientLine(m clientMinute) string {
	loss, p50, p90 := "-", "-", "-"
	if m.segs > 0 {
		loss = fmt.Sprintf("%.2f", float64(m.retrans)*1000/float64(m.segs))
	}
	if len(m.rtts) > 0 {
		sort.Slice(m.rtts, func(i, j int) bool { return m.rtts[i] < m.rtts[j] })
		at := func(q float64) string {
			return fmt.Sprintf("%.1f", float64(m.rtts[int(q*float64(len(m.rtts)-1))].Microseconds())/1000)
		}
		p50, p90 = at(0.5), at(0.9)
	}
	return fmt.Sprintf("clients tcp from=%d conns=%d closed=%d sending=%d segs=%d retrans=%d loss_pm=%s rtt_p50=%s rtt_p90=%s",
		m.from.Unix(), m.conns, m.closed, len(m.rtts), m.segs, m.retrans, loss, p50, p90)
}

// logClients writes the leg's minute on the wall clock's minutes until ctx
// ends: the newest generation's.
func logClients(ctx context.Context, l *clientLegs) {
	from := time.Now().Truncate(time.Minute)
	for {
		next := from.Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		log.Print("nekko-trunk-entry: " + clientLine(l.minute(from)))
		from = next
	}
}
