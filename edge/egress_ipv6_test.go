package main

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// serveNames answers A and AAAA for the given names from a local server and
// makes it the resolver of this process, the one the exit asks. Any other name
// does not exist.
func serveNames(t *testing.T, names map[string][]string) {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) != 1 {
				continue
			}
			question := q.Questions[0]
			reply := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: q.ID, Response: true, Authoritative: true},
				Questions: q.Questions,
			}
			addrs, known := names[question.Name.String()]
			if !known {
				reply.RCode = dnsmessage.RCodeNameError
			}
			for _, text := range addrs {
				addr := netip.MustParseAddr(text)
				head := dnsmessage.ResourceHeader{Name: question.Name, Type: question.Type, Class: question.Class, TTL: 1}
				switch {
				case addr.Is4() && question.Type == dnsmessage.TypeA:
					reply.Answers = append(reply.Answers, dnsmessage.Resource{Header: head, Body: &dnsmessage.AResource{A: addr.As4()}})
				case addr.Is6() && question.Type == dnsmessage.TypeAAAA:
					reply.Answers = append(reply.Answers, dnsmessage.Resource{Header: head, Body: &dnsmessage.AAAAResource{AAAA: addr.As16()}})
				}
			}
			packed, err := reply.Pack()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(packed, from)
		}
	}()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp4", pc.LocalAddr().String())
		},
	}
	t.Cleanup(func() { net.DefaultResolver = previous })
}

// A real tunnel to an exit that resolves names itself. The site listens on both
// families of one port, each answering with its own tag, so the reply says which
// address the exit picked. A closed port on ::1 stands in for what the exit must
// refuse: unlike a Russian address, it would answer if the exit dialled it.
func TestAnExitTakesIPv6OnlyForANameWithoutIPv4(t *testing.T) {
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	probe.Close()

	edgePort, local := freePort(t, "tcp"), freePort(t, "tcp")
	site, refused := freePort(t, "tcp"), freePort(t, "tcp")
	tcpEchoOn(t, "127.0.0.1", site, "four")
	tcpEchoOn(t, "::1", site, "six")
	tcpEchoOn(t, "::1", refused, "refused")

	serveNames(t, map[string][]string{
		"only-six.example.":   {"::1"},
		"both.example.":       {"127.0.0.1", "::1"},
		"ru-four.example.":    {"77.88.55.242", "::1"},
		"to-the-box.example.": {"::1"},
	})

	m, pin := xhttpEnv(t, edgePort, local)
	_, _ = startXhttpEdge(t, m, site)
	at := func(name string, port int) string { return net.JoinHostPort(name, strconv.Itoa(port)) }
	doors := xhttpClientTo(t, testClient, edgePort, pin, "stream-one", map[string]string{
		"only-six":   at("only-six.example", site),
		"both":       at("both.example", site),
		"ru-four":    at("ru-four.example", site),
		"to-the-box": at("to-the-box.example", refused),
		"raw-six":    at("::1", site),
	})

	if got := talk(t, doors["only-six"], 5*time.Second); got != "six:ping" {
		t.Errorf("a name with only an AAAA record: %q", got)
	}
	if got := talk(t, doors["raw-six"], 5*time.Second); got != "six:ping" {
		t.Errorf("an IPv6 address from the client: %q", got)
	}
	// UseIP picks at random: enough rounds that it would land on IPv6.
	for range 12 {
		if got := talk(t, doors["both"], 5*time.Second); got != "four:ping" {
			t.Fatalf("a name with both records must stay on IPv4: %q", got)
		}
	}
	// The Russian A record is refused, and the AAAA next to it is no way round.
	if got := talk(t, doors["ru-four"], 2*time.Second); got != "" {
		t.Errorf("a Russian IPv4 site answered over its IPv6: %q", got)
	}
	// finalRules see the address the fallback resolved to.
	if got := talk(t, doors["to-the-box"], 2*time.Second); got != "" {
		t.Errorf("a name resolving to the box's IPv6 got through: %q", got)
	}
}
