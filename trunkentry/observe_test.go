package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

// targetLoop is loop that hands out which stream each dial is for, as the
// exit's relay learns it.
type targetLoop struct {
	loop
	targets chan trunk.Target
}

func (l targetLoop) DialStream(ctx context.Context, port uint16) (net.Conn, error) {
	if t, ok := trunk.TargetFrom(ctx); ok {
		l.targets <- t
	}
	return l.loop.DialStream(ctx, port)
}

// TestObservedStreamLine: a stream the exit marks is written to the
// entry's journal at the mark and at its end, under the observation's id,
// with the trunk's counters; the line names neither the client nor the
// tester's key (which the entry never gets). A stream nobody marked writes
// no line.
func TestObservedStreamLine(t *testing.T) {
	journal := &syncBuffer{}
	log.SetOutput(journal)
	defer log.SetOutput(os.Stderr)

	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	local := targetLoop{loop{echo.Addr().String(), "127.0.0.1:9"}, make(chan trunk.Target, 4)}
	x := trunk.NewExit(trunk.Config{}, local)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go x.Run(ctx)
	dial := func(context.Context, int) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go func() { _ = x.ServeMember(b) }()
		return a, nil
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	entry, err := start(ctx, Config{Members: 2, TCP: map[int]uint16{port: 443}, PortLo: defaultPortLo, PortHi: defaultPortHi}, dial)
	if err != nil {
		t.Fatal(err)
	}
	for entry.Session().Stats().Members < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	echoOnce := func() net.Conn {
		c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = c.Write([]byte("ping"))
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			t.Fatal(err)
		}
		return c
	}

	plain := echoOnce()
	<-local.targets
	plain.Close()

	c := echoOnce()
	tg := <-local.targets
	const tag = "tester-key-never-on-the-entry"
	if _, ok := tg.Session.Observe(tg.Kind, tg.ID, tag); !ok {
		t.Fatal("not marked")
	}
	clientPort := strconv.Itoa(c.LocalAddr().(*net.TCPAddr).Port)
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(journal.String(), " observed ") < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	out := journal.String()
	if n := strings.Count(out, " observed "); n != 2 {
		t.Fatalf("%d observed lines, want the marked stream's mark and end:\n%s", n, out)
	}
	for _, want := range []string{"kind=stream phase=mark port=443", "phase=end", "up=4 ", "end=fin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{tag, ":" + clientPort, "127.0.0.1"} {
		if strings.Contains(out, leak) {
			t.Fatalf("the journal says %q:\n%s", leak, out)
		}
	}
}

// syncBuffer is a journal the test reads while the session's reporter
// writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
