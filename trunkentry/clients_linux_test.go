package main

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// The leg client → entry adds up every client's socket of the minute: what
// the entry sent, what it sent again, the round trips; a socket that closed
// in the minute counts in it once, and the line names no client.
func TestTheClientsLegAddsUpEverySocketOfTheMinute(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	legs := newClientLegs()
	accepted := make(chan *clientConn, 2)
	go func() {
		for i := 0; i < 2; i++ {
			c, err := ln.AcceptTCP()
			if err != nil {
				return
			}
			accepted <- legs.track(c)
		}
	}()
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		go func() { _, _ = io.Copy(io.Discard, c) }()
	}
	a, b := <-accepted, <-accepted
	payload := make([]byte, 256<<10)
	for _, c := range []*clientConn{a, b} {
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	b.Close() // its last reading is taken here, once
	b.Close()
	from := time.Unix(1794650000, 0)
	m := legs.minute(from)
	if m.conns != 1 || m.closed != 1 || m.segs < 2 || len(m.rtts) != 2 || m.retrans != 0 {
		t.Fatalf("minute %+v: want 1 live, 1 closed, both sockets' segments and round trips, no retransmission", m)
	}
	line := clientLine(m)
	if !strings.HasPrefix(line, "clients tcp from=1794650000 conns=1 closed=1 sending=2 ") || !strings.Contains(line, " loss_pm=0.00 ") {
		t.Fatalf("line %q", line)
	}
	if strings.Contains(line, "127.0.0.1") || strings.Contains(line, ln.Addr().String()) {
		t.Fatalf("the line names a client: %q", line)
	}
	// The next minute counts only what came after: an idle socket sends
	// nothing, and says no round trip.
	next := legs.minute(from.Add(time.Minute))
	if next.segs != 0 || next.closed != 0 || next.conns != 1 {
		t.Fatalf("idle minute %+v", next)
	}
	if got := clientLine(next); !strings.HasSuffix(got, "segs=0 retrans=0 loss_pm=- rtt_p50=- rtt_p90=-") {
		t.Fatalf("idle line %q", got)
	}
}
