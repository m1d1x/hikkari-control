package main

import (
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestCountryEgressRequiresConfirmedFailureAndRecovery(t *testing.T) {
	up, down, partial := egressHealth{true, true}, egressHealth{}, egressHealth{tcp: true}
	for _, home := range []egressCountry{countryNL, countryDE} {
		t.Run(home.String(), func(t *testing.T) {
			c := egressChoice{down: countryNone}
			check := func(primary, backup egressHealth, want egressCountry) {
				t.Helper()
				var health [2]egressHealth
				health[home], health[home.other()] = primary, backup
				if got := c.observe(health); got != want {
					t.Fatalf("country down = %s, want %s", got, want)
				}
			}
			for range autoFailures - 1 {
				check(down, up, countryNone)
			}
			check(partial, up, countryNone)
			for range autoFailures {
				check(down, down, countryNone)
			}
			check(down, partial, countryNone)
			check(down, up, home)
			check(up, up, home)
			check(partial, up, home)
			for range recoverySuccesses - 1 {
				check(up, up, home)
			}
			check(up, up, countryNone)
		})
	}
}

func TestCountryGatewayKeepsLivingTCPAndUDPOnRecovery(t *testing.T) {
	backends := [2]netip.AddrPort{echoEgress(t, 'N'), echoEgress(t, 'D')}
	ports := [2]netip.AddrPort{}
	gateways := [2]*autoGateway{}
	for _, home := range []egressCountry{countryNL, countryDE} {
		gateway, err := newAutoGateway(t.Context(), netip.MustParseAddrPort("127.0.0.1:0"), backends, home)
		if err != nil {
			t.Fatal(err)
		}
		go gateway.serve()
		t.Cleanup(func() { gateway.tcp.Close(); gateway.udp.Close() })
		gateways[home] = gateway
		ports[home] = gateway.tcp.Addr().(*net.TCPAddr).AddrPort()
	}
	dialUDP := func(country egressCountry) *net.UDPConn {
		t.Helper()
		conn, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(ports[country]))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	nlUDP, deUDP := dialUDP(countryNL), dialUDP(countryDE)
	nlTCP, deTCP := dialAutoTestTCP(t, ports[countryNL]), dialAutoTestTCP(t, ports[countryDE])
	defer nlTCP.Close()
	defer deTCP.Close()
	assertAutoEcho(t, nlUDP, 'N')
	assertAutoEcho(t, nlTCP, 'N')
	assertAutoEcho(t, deUDP, 'D')
	assertAutoEcho(t, deTCP, 'D')

	for _, gateway := range gateways {
		gateway.route(countryNL)
	}
	if _, err := nlTCP.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("former NL TCP connection still open: %v", err)
	}
	fallbackTCP := dialAutoTestTCP(t, ports[countryNL])
	defer fallbackTCP.Close()
	assertAutoEcho(t, fallbackTCP, 'D')
	assertAutoEcho(t, nlUDP, 'D')
	assertAutoEcho(t, deTCP, 'D')
	assertAutoEcho(t, deUDP, 'D')

	for _, gateway := range gateways {
		gateway.route(countryNone)
	}
	homeTCP := dialAutoTestTCP(t, ports[countryNL])
	defer homeTCP.Close()
	assertAutoEcho(t, homeTCP, 'N')
	assertAutoEcho(t, dialUDP(countryNL), 'N')
	assertAutoEcho(t, fallbackTCP, 'D')
	assertAutoEcho(t, nlUDP, 'D')

	for _, gateway := range gateways {
		gateway.route(countryDE)
	}
	if _, err := fallbackTCP.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("fallback TCP connection on dead DE still open: %v", err)
	}
	if _, err := deTCP.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("DE TCP connection still open: %v", err)
	}
	assertAutoEcho(t, homeTCP, 'N')
	assertAutoEcho(t, nlUDP, 'N')
	assertAutoEcho(t, deUDP, 'N')
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
