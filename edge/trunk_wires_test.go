package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/quicwire"
	"github.com/m1d1x/hikkari-protocol/trunk/udpwire"
)

func TestTrunkWiresFromEnv(t *testing.T) {
	w, err := trunkWiresFromEnv("udp=20447, quic=20448,salamander=20449")
	if err != nil || len(w) != 3 || w["udp"] != 20447 || w["salamander"] != 20449 {
		t.Fatalf("%v %v", w, err)
	}
	for _, bad := range []string{"udp=0", "awg=20450", "udp=1,quic=1", "udp=1,udp=2", "udp"} {
		if _, err := trunkWiresFromEnv(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := trunkExitFromEnv(func(k string) string {
		return map[string]string{"EDGE_HIKKARI_TRUNK_WIRES": "udp=20447"}[k]
	}); err == nil {
		t.Fatal("wires without a trunk key accepted")
	}
}

// TestTrunkWiresAtTheExit: the exit serves the udp and quic wires on their
// ports into the same trunk; an entry's stream crosses on quic and its call
// on udp, to the relay's loopback.
func TestTrunkWiresAtTheExit(t *testing.T) {
	const key = "trunk-key-0123456789abcdef-xyz"
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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
	echoPort := uint16(echo.Addr().(*net.TCPAddr).Port)
	free := func() int {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	cfg := trunkExitConfig{Keys: []string{key}, Ports: map[uint16]bool{echoPort: true}, Wires: map[string]int{"udp": free(), "quic": free()}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := trunk.NewExit(trunk.Config{}, trunkLocal{ports: cfg.Ports})
	go x.Run(ctx)
	if err := startTrunkWires(ctx, cfg, "hk.example", x); err != nil {
		t.Fatal(err)
	}
	at := func(kind string) netip.AddrPort {
		return netip.MustParseAddrPort("127.0.0.1:" + strconv.Itoa(cfg.Wires[kind]))
	}

	ports, _ := trunk.NewPortBook(44000, 45000, time.Minute)
	pool := func(ctx context.Context, _ int) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go func() { _ = x.ServeMember(b) }()
		return a, nil
	}
	entry, err := trunk.NewEntry(trunk.Config{}, 1, pool, ports, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	const quicWire, udpWire trunk.WireID = 1, 2
	if err := entry.AddWire(trunk.Wire{ID: quicWire, Name: "quic", Shape: "browser", Members: 2, Streams: true,
		Dial: quicwire.Dialer{Exit: at("quic"), Key: key, SNI: "hk.example"}.Dial}); err != nil {
		t.Fatal(err)
	}
	if err := entry.AddWire(trunk.Wire{ID: udpWire, Name: "udp", Shape: "random", Members: 2, Packet: true,
		Dial: udpwire.Dialer{Exit: at("udp"), Key: key}.Dial}); err != nil {
		t.Fatal(err)
	}
	entry.SetRoute(trunk.Route{Voice: udpWire, Bulk: quicWire})
	go entry.Run(ctx)
	deadline := time.Now().Add(15 * time.Second)
	for w := entry.Wires(); w[quicWire].Members < 2 || w[udpWire].Members < 2; w = entry.Wires() {
		if time.Now().After(deadline) {
			t.Fatalf("wires did not come up: %+v", w)
		}
		time.Sleep(20 * time.Millisecond)
	}

	client, edgeSide := net.Pipe()
	defer client.Close()
	go entry.Session().Carry(edgeSide, echoPort)
	data := make([]byte, 2<<20)
	_, _ = rand.Read(data)
	go func() { _, _ = client.Write(data) }()
	got := make([]byte, len(data))
	_ = client.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(client, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stream over quic: %v", err)
	}

	var back atomic.Int64
	f := entry.Session().OpenFlowWith(trunk.EchoPort, trunk.FlowOptions{Copies: 1, Pin: udpWire}, func([]byte) { back.Add(1) }, nil)
	defer f.Close()
	for i := 0; i < 50; i++ {
		f.Send(make([]byte, 64))
		time.Sleep(2 * time.Millisecond)
	}
	for deadline := time.Now().Add(5 * time.Second); back.Load() < 50 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if back.Load() != 50 {
		t.Fatalf("%d of 50 of the call came back on udp", back.Load())
	}
}
