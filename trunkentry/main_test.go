package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

func TestConfig(t *testing.T) {
	env := map[string]string{
		"EDGE_TRUNK_EXIT": "203.0.113.8:443", "EDGE_TRUNK_SNI": "hk.example", "EDGE_TRUNK_KEY": "trunk-key-0123456789abcdef-xyz",
		"EDGE_TRUNK_MEMBERS": "32", "EDGE_TRUNK_FORWARD": "8053=2053,8443=443", "EDGE_TRUNK_UDP": "8443=443",
	}
	c, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Members != 32 || c.TCP[8053] != 2053 || c.UDP[8443] != 443 || len(c.UDP) != 1 || len(c.OneCopy) != 0 {
		t.Fatalf("%+v", c)
	}
	env["EDGE_TRUNK_UDP_ONE_COPY"] = "8443"
	if c, err = ConfigFromEnv(func(k string) string { return env[k] }); err != nil || !c.OneCopy[8443] {
		t.Fatalf("one copy on 8443: %+v %v", c.OneCopy, err)
	}
	for k, bad := range map[string]string{
		"EDGE_TRUNK_EXIT": "hk.example:443", "EDGE_TRUNK_SNI": "203.0.113.8", "EDGE_TRUNK_KEY": "short",
		"EDGE_TRUNK_MEMBERS": "0", "EDGE_TRUNK_FORWARD": "", "EDGE_TRUNK_UDP": "x=1",
		"EDGE_TRUNK_UDP_ONE_COPY": "8053", // not a UDP port of the entry
	} {
		was := env[k]
		env[k] = bad
		if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("%s=%q accepted", k, bad)
		}
		env[k] = was
	}
}

type loop struct{ tcp, udp string }

func (l loop) DialStream(ctx context.Context, _ uint16) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", l.tcp)
}
func (l loop) DialFlow(ctx context.Context, _ uint16) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "udp", l.udp)
}

// TestListeners: a client's TCP and UDP on the entry's ports reach the exit's
// loopback service and come back (members are pipes to an in-process exit).
func TestListeners(t *testing.T) {
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
	uecho, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer uecho.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := uecho.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = uecho.WriteTo(b[:n], from)
		}
	}()
	x := trunk.NewExit(trunk.Config{}, loop{echo.Addr().String(), uecho.LocalAddr().String()})
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
	c := Config{Members: 3, TCP: map[int]uint16{port: 443}, UDP: map[int]uint16{port: 443}, PortLo: defaultPortLo, PortHi: defaultPortHi}
	entry, err := start(ctx, c, dial)
	if err != nil {
		t.Fatal(err)
	}
	for entry.Session().Stats().Members < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data := make([]byte, 1<<20)
	_, _ = rand.Read(data)
	go func() { _, _ = conn.Write(data) }()
	got := make([]byte, len(data))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("tcp: %v", err)
	}
	u, _ := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	defer u.Close()
	_, _ = u.Write([]byte("voice"))
	_ = u.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 16)
	if n, err := u.Read(b); err != nil || string(b[:n]) != "voice" {
		t.Fatalf("udp: %q %v", b[:n], err)
	}
}

// The minute line keeps the keys the hub reads, the round trips last.
func TestStatsLine(t *testing.T) {
	ports, err := trunk.NewPortBook(20000, 32767, 0)
	if err != nil {
		t.Fatal(err)
	}
	ports.Dead(20001)
	line := statsLine(trunk.Stats{Members: 32, MembersJoined: 33, MembersStall: 1, Resent: 2, Up: 10, Down: 20, DgramsOut: 5, DgramsIn: 4},
		[]time.Duration{20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond, 100 * time.Millisecond}, ports)
	want := "members=32 joined=33 stalled=1 broken=0 resent=2 dead_ports=1 port_kills=1 streams=0/0 reset=0 up=10 down=20 flows=0 dgrams=5/4 copies=0 rtt_p50=30.0 rtt_p90=100.0 rtt_max=100.0"
	if line != want {
		t.Fatalf("\n got %s\nwant %s", line, want)
	}
	if got := statsLine(trunk.Stats{}, nil, ports); !strings.HasSuffix(got, "rtt_p50=- rtt_p90=- rtt_max=-") {
		t.Fatalf("no members: %s", got)
	}
}
