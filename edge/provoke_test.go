package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func startProvoke(t *testing.T) (*provokeServer, string) {
	t.Helper()
	s, err := newProvokeServer()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	return s, s.tcp.Addr().String()
}

// stimulus is the client side as diagnostics/provoke.go speaks it.
func stimulus(t *testing.T, addr, sni, tag, command string) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	cfg := &tls.Config{ServerName: sni, InsecureSkipVerify: true}
	if tag != "" {
		cfg.NextProtos = []string{provokeALPN + tag}
	}
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if tag != "" && c.ConnectionState().NegotiatedProtocol != provokeALPN+tag {
		t.Fatalf("negotiated %q, want the tag back", c.ConnectionState().NegotiatedProtocol)
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, command+"\n"); err != nil {
		t.Fatal(err)
	}
	return c, bufio.NewReader(c)
}

func seen(t *testing.T, addr string, tags ...string) map[string]provokeSeen {
	t.Helper()
	_, r := stimulus(t, addr, "www.microsoft.com", "00000000000000ff", "SEEN "+strings.Join(tags, ","))
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]provokeSeen
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("seen %q: %v", line, err)
	}
	return out
}

func TestProvokeDownloadIsCountedUnderTheTagAndSNI(t *testing.T) {
	_, addr := startProvoke(t)
	tag := "0123456789abcdef"
	_, r := stimulus(t, addr, "www.youtube.com", tag, "DL 100000")
	got, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if got != 100000 {
		t.Fatalf("got %d bytes, want 100000", got)
	}
	s := seen(t, addr, tag, "fedcba9876543210")[tag]
	if !s.Hello || s.SNI != "www.youtube.com" || s.Out != 100000 || s.In != 0 {
		t.Fatalf("seen %+v", s)
	}
	if other := seen(t, addr, "fedcba9876543210")["fedcba9876543210"]; other.Hello {
		t.Fatalf("an unknown tag reads as seen: %+v", other)
	}
}

func TestProvokeUploadAnswersWhatArrived(t *testing.T) {
	_, addr := startProvoke(t)
	tag := "1111111111111111"
	c, r := stimulus(t, addr, "rutracker.org", tag, "UL 50000")
	if _, err := c.Write(make([]byte, 50000)); err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(line) != "OK 50000" {
		t.Fatalf("answer %q", line)
	}
	if s := seen(t, addr, tag)[tag]; s.In != 50000 || s.SNI != "rutracker.org" {
		t.Fatalf("seen %+v", s)
	}
}

func TestProvokeHelloWithoutTagStillServed(t *testing.T) {
	s, addr := startProvoke(t)
	_, r := stimulus(t, addr, "example.org", "", "DL 10")
	if got, _ := io.Copy(io.Discard, r); got != 10 {
		t.Fatalf("got %d", got)
	}
	if line := s.book.minute(); !strings.Contains(line, "example.org=1") {
		t.Fatalf("minute %q", line)
	}
}

func TestProvokeRefusesOversizeAndUnknownCommands(t *testing.T) {
	_, addr := startProvoke(t)
	for _, cmd := range []string{fmt.Sprintf("DL %d", provokeMaxBytes+1), "DL -1", "RM -rf", "UL x"} {
		_, r := stimulus(t, addr, "a.example", "2222222222222222", cmd)
		if got, _ := io.Copy(io.Discard, r); got != 0 {
			t.Fatalf("%q answered %d bytes", cmd, got)
		}
	}
}

func TestProvokeEchoesTaggedDatagrams(t *testing.T) {
	_, addr := startProvoke(t)
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tag, _ := hex.DecodeString("3333333333333333")
	packet := append(append([]byte(provokeMagic), tag...), 0, 0, 0, 1)
	packet = append(packet, make([]byte, 140)...)
	echoed := 0
	buf := make([]byte, 2048)
	for i := 0; i < 5; i++ {
		if _, err := c.Write(packet); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		n, err := c.Read(buf)
		if err == nil && n == len(packet) {
			echoed++
		}
	}
	// junk is neither counted nor answered
	_, _ = c.Write([]byte("hello"))
	if echoed != 5 {
		t.Fatalf("echoed %d of 5", echoed)
	}
	if s := seen(t, addr, "3333333333333333")["3333333333333333"]; s.UDP != 5 {
		t.Fatalf("seen %+v", s)
	}
}

func TestProvokeBookIsBounded(t *testing.T) {
	b := newProvokeBook()
	now := time.Now()
	b.now = func() time.Time { return now }
	for i := 0; i < provokeMaxRecs+10; i++ {
		b.hello(fmt.Sprintf("%016x", i), "x")
	}
	if len(b.recs) > provokeMaxRecs || len(b.order) != len(b.recs) {
		t.Fatalf("recs %d order %d", len(b.recs), len(b.order))
	}
	if b.seen([]string{fmt.Sprintf("%016x", 0)})[fmt.Sprintf("%016x", 0)].Hello {
		t.Fatal("the oldest record outlived the cap")
	}
	now = now.Add(provokeTTL + time.Second)
	b.hello("ffffffffffffffff", "y")
	if len(b.recs) != 1 {
		t.Fatalf("aged records kept: %d", len(b.recs))
	}
}

func TestProvokeConfigFromEnv(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if c, err := ProvokeConfigFromEnv(env("")); err != nil || c.Listen != "0.0.0.0:443" {
		t.Fatalf("default: %+v %v", c, err)
	}
	if _, err := ProvokeConfigFromEnv(env("0.0.0.0:8443")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"443", "host:443", "0.0.0.0:0", "0.0.0.0:70000"} {
		if _, err := ProvokeConfigFromEnv(env(bad)); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
