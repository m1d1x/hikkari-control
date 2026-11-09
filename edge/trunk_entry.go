package main

// EDGE_PROTO=trunk-entry is the Russian half of the cascade with its trunk
// (package trunk; hikkari-client plans/2026-11-05-ru-cascade.md, шаг 2). It
// takes clients' TCP connections and UDP datagrams on its ports and carries
// them over a pool of members to a Hikkari relay abroad, which hands them to
// its own services on loopback.
//
//	client ─► :443 trunk-entry (RU) ═ pool, EDGE_TRUNK_MEMBERS ═► exit relay ─► 127.0.0.1:443
//
// What crosses the border is the trunk's TLS with the hello the hub chose
// (EDGE_TRUNK_FP), never the client's: опыт 0 found the border cutting
// clients' hellos both ways. The entry terminates the client's TCP only; the
// client's TLS and keys stay end to end with the exit's relay. It holds the
// trunk key and nothing of any subscriber.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

const protoTrunkEntry = "trunk-entry"

// TrunkEntryConfig is EDGE_TRUNK_*.
type TrunkEntryConfig struct {
	Exit, SNI, Key string
	Hello          string
	Members        int
	// TCP and UDP map a port the entry listens on to the exit's loopback
	// port it reaches.
	TCP, UDP       map[int]uint16
	Stall          time.Duration
	PortLo, PortHi int
}

const (
	defaultTrunkMembers = 32
	maxTrunkMembers     = 128
	// Source ports below Linux's ephemeral range (32768-60999), so the
	// members' chosen ports do not race the kernel's own picks.
	defaultTrunkPortLo = 20000
	defaultTrunkPortHi = 32767
)

func TrunkEntryConfigFromEnv(get func(string) string) (TrunkEntryConfig, error) {
	c := TrunkEntryConfig{
		Exit: strings.TrimSpace(get("EDGE_TRUNK_EXIT")), SNI: strings.TrimSpace(get("EDGE_TRUNK_SNI")),
		Key: get("EDGE_TRUNK_KEY"), Hello: strings.TrimSpace(get("EDGE_TRUNK_FP")),
		Members: defaultTrunkMembers, PortLo: defaultTrunkPortLo, PortHi: defaultTrunkPortHi,
	}
	host, port, err := net.SplitHostPort(c.Exit)
	if err != nil || net.ParseIP(host) == nil {
		return c, fmt.Errorf("EDGE_TRUNK_EXIT: %q is not ip:port", c.Exit)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return c, fmt.Errorf("EDGE_TRUNK_EXIT: port %q", port)
	}
	if c.SNI == "" || net.ParseIP(c.SNI) != nil || !strings.Contains(c.SNI, ".") || strings.ContainsAny(c.SNI, "/\\\r\n\t :") {
		return c, errors.New("EDGE_TRUNK_SNI: the exit relay's domain")
	}
	if err := trunk.ParseKey(c.Key); err != nil {
		return c, fmt.Errorf("EDGE_TRUNK_KEY: %w", err)
	}
	if _, ok := trunk.Hellos[c.Hello]; !ok {
		return c, fmt.Errorf("EDGE_TRUNK_FP: %q is not one of %s", c.Hello, strings.Join(trunk.HelloNames(), ", "))
	}
	if v := get("EDGE_TRUNK_MEMBERS"); v != "" {
		if c.Members, err = strconv.Atoi(v); err != nil || c.Members < 1 || c.Members > maxTrunkMembers {
			return c, fmt.Errorf("EDGE_TRUNK_MEMBERS: %q, 1-%d", v, maxTrunkMembers)
		}
	}
	if v := get("EDGE_TRUNK_STALL"); v != "" {
		if c.Stall, err = time.ParseDuration(v); err != nil || c.Stall < 50*time.Millisecond || c.Stall > 10*time.Second {
			return c, fmt.Errorf("EDGE_TRUNK_STALL: %q, 50ms-10s", v)
		}
	}
	if v := get("EDGE_TRUNK_SOURCE_PORTS"); v != "" {
		lo, hi, ok := strings.Cut(v, "-")
		a, e1 := strconv.Atoi(lo)
		b, e2 := strconv.Atoi(hi)
		if !ok || e1 != nil || e2 != nil {
			return c, fmt.Errorf("EDGE_TRUNK_SOURCE_PORTS: %q is not lo-hi", v)
		}
		c.PortLo, c.PortHi = a, b
	}
	if c.TCP, err = trunkForwards(get("EDGE_TRUNK_FORWARD"), "EDGE_TRUNK_FORWARD"); err != nil {
		return c, err
	}
	if len(c.TCP) == 0 {
		return c, errors.New("EDGE_TRUNK_FORWARD: empty, an entry must forward something")
	}
	if c.UDP, err = trunkForwards(get("EDGE_TRUNK_UDP"), "EDGE_TRUNK_UDP"); err != nil {
		return c, err
	}
	return c, nil
}

// trunkForwards reads "listen=exitport,...".
func trunkForwards(raw, name string) (map[int]uint16, error) {
	out := map[int]uint16{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		l, r, ok := strings.Cut(strings.TrimSpace(item), "=")
		a, e1 := strconv.Atoi(l)
		b, e2 := strconv.Atoi(r)
		if !ok || e1 != nil || e2 != nil || a < 1 || a > 65535 || b < 1 || b > 65535 {
			return nil, fmt.Errorf("%s: %q is not port=port", name, item)
		}
		if _, dup := out[a]; dup {
			return nil, fmt.Errorf("%s: port %d twice", name, a)
		}
		out[a] = uint16(b)
	}
	return out, nil
}

func runTrunkEntry() error {
	c, err := TrunkEntryConfigFromEnv(osEnv)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	entry, err := startTrunkEntry(ctx, c, nil)
	if err != nil {
		return err
	}
	go logTrunkEntry(ctx, entry)
	log.Printf("nekko-edge: trunk entry up: exit %s (%s), %d members, fp %s, tcp %v, udp %v",
		c.Exit, c.SNI, c.Members, c.Hello, forwardList(c.TCP), forwardList(c.UDP))
	<-ctx.Done()
	return nil
}

// startTrunkEntry opens the listeners and the pool. dial replaces the
// network dialer in tests.
func startTrunkEntry(ctx context.Context, c TrunkEntryConfig, dial trunk.Dial) (*trunk.Entry, error) {
	if dial == nil {
		dial = trunk.Dialer{Exit: c.Exit, SNI: c.SNI, Hello: trunk.Hellos[c.Hello], Key: c.Key}.Dial
	}
	ports, err := trunk.NewPortBook(c.PortLo, c.PortHi, 0)
	if err != nil {
		return nil, err
	}
	entry, err := trunk.NewEntry(trunk.Config{Stall: c.Stall}, c.Members, dial, ports, log.Printf)
	if err != nil {
		return nil, err
	}
	s := entry.Session()
	var listeners []func()
	fail := func(err error) (*trunk.Entry, error) {
		for _, close := range listeners {
			close()
		}
		return nil, err
	}
	for port, to := range c.TCP {
		ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if err != nil {
			return fail(fmt.Errorf("trunk entry tcp :%d: %w", port, err))
		}
		listeners = append(listeners, func() { ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.SetKeepAlive(true)
				}
				go s.Carry(conn, to)
			}
		}()
	}
	for port, to := range c.UDP {
		pc, err := net.ListenPacket("udp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if err != nil {
			return fail(fmt.Errorf("trunk entry udp :%d: %w", port, err))
		}
		listeners = append(listeners, func() { pc.Close() })
		go serveTrunkUDP(ctx, s, pc, to)
	}
	go func() {
		<-ctx.Done()
		for _, close := range listeners {
			close()
		}
	}()
	go entry.Run(ctx)
	return entry, nil
}

// serveTrunkUDP gives each client address on pc its own flow.
func serveTrunkUDP(ctx context.Context, s *trunk.Session, pc net.PacketConn, to uint16) {
	var mu sync.Mutex
	flows := map[string]*trunk.Flow{}
	buf := make([]byte, trunk.MaxPayload)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		key := from.String()
		mu.Lock()
		f := flows[key]
		if f == nil {
			client := from
			f = s.OpenFlow(to, func(p []byte) { _, _ = pc.WriteTo(p, client) }, func() {
				mu.Lock()
				delete(flows, key)
				mu.Unlock()
			})
			flows[key] = f
		}
		mu.Unlock()
		if !f.Send(buf[:n]) {
			mu.Lock()
			if flows[key] == f {
				delete(flows, key)
			}
			mu.Unlock()
		}
	}
}

// logTrunkEntry writes the pool's counters once a minute.
func logTrunkEntry(ctx context.Context, entry *trunk.Entry) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st := entry.Session().Stats()
			dead, kills := entry.Ports().Counts()
			log.Printf("nekko-edge: trunk entry: members=%d joined=%d stalled=%d broken=%d resent=%d dead_ports=%d port_kills=%d streams=%d/%d reset=%d up=%d down=%d flows=%d dgrams=%d/%d copies=%d",
				st.Members, st.MembersJoined, st.MembersStall, st.MembersBroken, st.Resent, dead, kills,
				st.Streams, st.StreamsOpened, st.StreamsReset, st.Up, st.Down, st.Flows, st.DgramsOut, st.DgramsIn, st.DgramsCopy)
		}
	}
}

func forwardList(m map[int]uint16) []string {
	out := make([]string, 0, len(m))
	for l, r := range m {
		out = append(out, fmt.Sprintf("%d→%d", l, r))
	}
	sort.Strings(out)
	return out
}
