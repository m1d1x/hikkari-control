package main

import (
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestAutoEgressRequiresConfirmedFailureAndReadyDE(t *testing.T) {
	up, down, partial := egressHealth{true, true}, egressHealth{}, egressHealth{tcp: true}
	var c egressChoice
	for range autoFailures - 1 {
		if c.observe(down, up) {
			t.Fatal("moved before confirming failure")
		}
	}
	if c.observe(partial, up) || c.failures != 0 {
		t.Fatal("partial failure must reset the count")
	}
	for range autoFailures {
		if c.observe(down, down) {
			t.Fatal("moved while neither exit was reachable")
		}
	}
	if c.observe(down, partial) || !c.observe(down, up) {
		t.Fatal("failover requires TCP and UDP on DE")
	}
	if c.observe(up, up) || !c.de {
		t.Fatal("recovery of NL interrupted the connections on DE")
	}
}

func TestAutoEgressSwitchesTCPAndExistingUDPSourceTogether(t *testing.T) {
	nl := echoEgress(t, 'N')
	de := echoEgress(t, 'D')
	gateway, err := newAutoGateway(t.Context(), netip.MustParseAddrPort("127.0.0.1:0"), nl)
	if err != nil {
		t.Fatal(err)
	}
	go gateway.serve()
	t.Cleanup(func() { gateway.tcp.Close(); gateway.udp.Close() })
	auto := gateway.tcp.Addr().(*net.TCPAddr).AddrPort()
	udp, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(auto))
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	tcp := dialAutoTestTCP(t, auto)
	defer tcp.Close()
	assertAutoEcho(t, tcp, 'N')
	assertAutoEcho(t, udp, 'N')

	for range autoFailures {
		gateway.observe(egressHealth{}, egressHealth{true, true}, de)
	}
	if _, err := tcp.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("former Auto TCP connection still open: %v", err)
	}
	newTCP := dialAutoTestTCP(t, auto)
	defer newTCP.Close()
	assertAutoEcho(t, newTCP, 'D')
	assertAutoEcho(t, udp, 'D') // same client socket and source port
	for _, backend := range []struct {
		addr netip.AddrPort
		want byte
	}{{nl, 'N'}, {de, 'D'}} {
		conn := dialAutoTestTCP(t, backend.addr)
		assertAutoEcho(t, conn, backend.want)
		conn.Close()
	}
}

func assertAutoEcho(t *testing.T, conn net.Conn, want byte) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var got [1]byte
	if _, err := io.ReadFull(conn, got[:]); err != nil || got[0] != want {
		t.Fatalf("got %q, want %q: %v", got, want, err)
	}
}

func dialAutoTestTCP(t *testing.T, addr netip.AddrPort) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func echoEgress(t *testing.T, reply byte) netip.AddrPort {
	t.Helper()
	tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tcp.Close() })
	addr := tcp.Addr().(*net.TCPAddr).AddrPort()
	udp, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(addr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close() })
	go func() {
		for {
			conn, err := tcp.AcceptTCP()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var buf [1]byte
				for {
					if _, err := conn.Read(buf[:]); err != nil {
						return
					}
					if _, err := conn.Write([]byte{reply}); err != nil {
						return
					}
				}
			}()
		}
	}()
	go func() {
		var buf [1]byte
		for {
			_, peer, err := udp.ReadFromUDPAddrPort(buf[:])
			if err != nil {
				return
			}
			udp.WriteToUDPAddrPort([]byte{reply}, peer)
		}
	}()
	return addr
}
