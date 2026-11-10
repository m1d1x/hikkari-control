package main

import (
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"syscall"
	"testing"

	"github.com/m1d1x/hikkari-protocol/observe"
)

func TestTCPWatchCountsClaimedConnections(t *testing.T) {
	book := observe.NewBook()
	ws := newTCPWatches(book)
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := watchListener{Listener: inner, ws: ws}
	defer l.Close()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	claimed, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	a, b := <-accepted, <-accepted
	server := a
	if a.RemoteAddr().String() != claimed.LocalAddr().String() {
		server = b
	}
	group := observe.RelayGroup(observe.PathDirect, "k")
	ws.claim(server.RemoteAddr().String(), group)
	go func() { _, _ = io.Copy(io.Discard, claimed) }()
	if _, err := server.Write(make([]byte, 256<<10)); err != nil {
		t.Fatal(err)
	}
	got := book.Take()
	if len(got) != 1 || got[group] == nil || got[group].TCP == nil {
		t.Fatalf("one claimed connection, got %+v", got)
	}
	tcp := got[group].TCP
	if tcp.Conns != 1 || tcp.Opened != 1 || tcp.Ended != 0 {
		t.Fatalf("live: %+v", tcp)
	}
	kernel := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	if kernel && (tcp.BytesOut == 0 || tcp.SegsOut == 0) {
		t.Fatalf("no kernel numbers: %+v", tcp)
	}
	_ = server.Close()
	got = book.Take()
	if tcp := got[group].TCP; tcp.Conns != 1 || tcp.Opened != 0 || tcp.Ended != 1 {
		t.Fatalf("closed: %+v", tcp)
	}
	if got := book.Take(); len(got) != 0 {
		t.Fatalf("a closed connection still counts: %+v", got)
	}
	_ = claimed.Close()
	_ = stranger.Close()
}

func TestTunnelEndAndPath(t *testing.T) {
	for _, c := range []struct {
		err               error
		revoked, stopping bool
		want              string
	}{
		{nil, false, false, observe.EndEOF},
		{io.EOF, false, false, observe.EndEOF},
		{os.ErrDeadlineExceeded, false, false, observe.EndIdle},
		{syscall.ECONNRESET, false, false, observe.EndReset},
		{errors.New("dial"), false, false, observe.EndError},
		{io.EOF, true, false, observe.EndRevoked},
		{io.EOF, true, true, observe.EndStop},
	} {
		if got := tunnelEnd(c.err, c.revoked, c.stopping); got != c.want {
			t.Errorf("tunnelEnd(%v, %v, %v) = %s, want %s", c.err, c.revoked, c.stopping, got, c.want)
		}
	}
	if pathOf("127.0.0.1:5000") != observe.PathEntry || pathOf("[::1]:5000") != observe.PathEntry || pathOf("203.0.113.5:443") != observe.PathDirect {
		t.Fatal("path by remote")
	}
}
