// Command nekko-trunk-entry is the Russian half of the cascade with its trunk
// (hikkari-protocol trunk, spec/trunk.md; hikkari-client
// plans/2026-11-05-ru-cascade.md, шаг 2). It takes clients' TCP connections and
// UDP datagrams on its ports and carries them over a pool of members to a
// Hikkari relay abroad, which hands them to its own services on loopback.
//
//	client ─► :443 entry (RU) ═ pool, EDGE_TRUNK_MEMBERS, Chromium ═► exit relay ─► 127.0.0.1:443
//
// What crosses the border is the trunk's own Hikkari stream channel, dialled
// by Chromium (trunk/chromium, the cronet build the client core links), never
// the client's TLS: опыт 0 found the border cutting clients' hellos both ways.
// The entry ends the client's TCP only; the client's TLS and keys stay end to
// end with the exit's relay. It holds the trunk key and nothing of any
// subscriber.
//
// Its own binary, not an EDGE_PROTO of nekko-edge: it links cronet (on Linux
// libcronet.so beside the binary, -tags with_purego), which no relay needs.
//
// Usage:
//
//	nekko-trunk-entry          run with EDGE_TRUNK_* from the environment
//	nekko-trunk-entry check    validate the environment and load libcronet, then exit
package main

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
	"github.com/m1d1x/hikkari-protocol/trunk/chromium"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	c, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("nekko-trunk-entry: %v", err)
	}
	if err := chromium.Load(); err != nil {
		log.Fatalf("nekko-trunk-entry: %v", err)
	}
	switch cmd {
	case "check":
		fmt.Printf("config OK, Chromium %s\n", chromium.Version())
	case "run":
		if err := run(c); err != nil {
			log.Fatalf("nekko-trunk-entry: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "usage: nekko-trunk-entry [run|check]\n")
		os.Exit(2)
	}
}

// Config is EDGE_TRUNK_*.
type Config struct {
	Exit, SNI, Key string
	Members        int
	// TCP and UDP map a port the entry listens on to the exit's loopback
	// port it reaches.
	TCP, UDP map[int]uint16
	// OneCopy are UDP listen ports whose datagrams cross the trunk once:
	// their sender copies them itself (Hikkari v1.02 sends a call over both
	// its channels), and a second copy here would make four.
	OneCopy        map[int]bool
	Stall          time.Duration
	PortLo, PortHi int
}

const (
	defaultMembers = 32
	maxMembers     = 128
	// Source ports below Linux's ephemeral range (32768-60999), so the
	// members' chosen ports do not race the kernel's own picks.
	defaultPortLo = 20000
	defaultPortHi = 32767
)

func ConfigFromEnv(get func(string) string) (Config, error) {
	c := Config{
		Exit: strings.TrimSpace(get("EDGE_TRUNK_EXIT")), SNI: strings.TrimSpace(get("EDGE_TRUNK_SNI")), Key: get("EDGE_TRUNK_KEY"),
		Members: defaultMembers, PortLo: defaultPortLo, PortHi: defaultPortHi,
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
	if v := get("EDGE_TRUNK_MEMBERS"); v != "" {
		if c.Members, err = strconv.Atoi(v); err != nil || c.Members < 1 || c.Members > maxMembers {
			return c, fmt.Errorf("EDGE_TRUNK_MEMBERS: %q, 1-%d", v, maxMembers)
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
	if c.TCP, err = forwards(get("EDGE_TRUNK_FORWARD"), "EDGE_TRUNK_FORWARD"); err != nil {
		return c, err
	}
	if len(c.TCP) == 0 {
		return c, errors.New("EDGE_TRUNK_FORWARD: empty, an entry must forward something")
	}
	if c.UDP, err = forwards(get("EDGE_TRUNK_UDP"), "EDGE_TRUNK_UDP"); err != nil {
		return c, err
	}
	c.OneCopy = map[int]bool{}
	if raw := strings.TrimSpace(get("EDGE_TRUNK_UDP_ONE_COPY")); raw != "" {
		for _, item := range strings.Split(raw, ",") {
			p, err := strconv.Atoi(strings.TrimSpace(item))
			if err != nil {
				return c, fmt.Errorf("EDGE_TRUNK_UDP_ONE_COPY: %q is not a port", item)
			}
			if _, ok := c.UDP[p]; !ok {
				return c, fmt.Errorf("EDGE_TRUNK_UDP_ONE_COPY: %d is not in EDGE_TRUNK_UDP", p)
			}
			c.OneCopy[p] = true
		}
	}
	return c, nil
}

// forwards reads "listen=exitport,...".
func forwards(raw, name string) (map[int]uint16, error) {
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

func run(c Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dial := chromium.Dialer{Exit: c.Exit, SNI: c.SNI, Key: c.Key}.Dial
	entry, err := start(ctx, c, dial)
	if err != nil {
		return err
	}
	go logStats(ctx, entry)
	log.Printf("nekko-trunk-entry: up: exit %s (%s), %d members, Chromium %s, tcp %v, udp %v, udp one copy %v",
		c.Exit, c.SNI, c.Members, chromium.Version(), forwardList(c.TCP), forwardList(c.UDP), portList(c.OneCopy))
	<-ctx.Done()
	return nil
}

// start opens the listeners and the pool.
func start(ctx context.Context, c Config, dial trunk.Dial) (*trunk.Entry, error) {
	ports, err := trunk.NewPortBook(c.PortLo, c.PortHi, 0)
	if err != nil {
		return nil, err
	}
	entry, err := trunk.NewEntry(trunk.Config{Stall: c.Stall}, c.Members, dial, ports, log.Printf)
	if err != nil {
		return nil, err
	}
	var closers []func()
	fail := func(err error) (*trunk.Entry, error) {
		for _, close := range closers {
			close()
		}
		return nil, err
	}
	for port, to := range c.TCP {
		ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if err != nil {
			return fail(fmt.Errorf("tcp :%d: %w", port, err))
		}
		closers = append(closers, func() { ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
						return
					}
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.SetKeepAlive(true)
				}
				// The current session: after the exit drained (a hitless
				// rollout of its relay) that is a new one, while what the
				// old one carries stays on it.
				go entry.Session().Carry(conn, to)
			}
		}()
	}
	for port, to := range c.UDP {
		pc, err := net.ListenPacket("udp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if err != nil {
			return fail(fmt.Errorf("udp :%d: %w", port, err))
		}
		closers = append(closers, func() { pc.Close() })
		copies := 0 // the session's
		if c.OneCopy[port] {
			copies = 1
		}
		go serveUDP(ctx, entry.Session, pc, to, copies)
	}
	go func() {
		<-ctx.Done()
		for _, close := range closers {
			close()
		}
	}()
	go entry.Run(ctx)
	return entry, nil
}

// serveUDP gives each client address on pc its own flow on the session
// current when it opens, each datagram carried by copies members (0: the
// session's).
func serveUDP(ctx context.Context, session func() *trunk.Session, pc net.PacketConn, to uint16, copies int) {
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
			f = session().OpenFlowCopies(to, copies, func(p []byte) { _, _ = pc.WriteTo(p, client) }, func() {
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

// logStats writes the pool's counters once a minute. The hub reads the line
// from the journal (hikkari-control internal/cascadewatch); its keys are
// that reader's contract.
func logStats(ctx context.Context, entry *trunk.Entry) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			log.Print("nekko-trunk-entry: " + statsLine(entry.Session().Stats(), entry.Session().MemberRTTs(), entry.Ports()))
		}
	}
}

// statsLine is the minute line: the counters since start, then the members'
// smoothed round trips in milliseconds (p50, p90, max over the members up).
func statsLine(st trunk.Stats, rtts []time.Duration, ports *trunk.PortBook) string {
	dead, kills := ports.Counts()
	return fmt.Sprintf("members=%d joined=%d stalled=%d broken=%d resent=%d dead_ports=%d port_kills=%d streams=%d/%d reset=%d up=%d down=%d flows=%d dgrams=%d/%d copies=%d pauses=%d paused_ms=%d rtt_p50=%s rtt_p90=%s rtt_max=%s",
		st.Members, st.MembersJoined, st.MembersStall, st.MembersBroken, st.Resent, dead, kills,
		st.Streams, st.StreamsOpened, st.StreamsReset, st.Up, st.Down, st.Flows, st.DgramsOut, st.DgramsIn, st.DgramsCopy,
		st.Pauses, st.Paused.Milliseconds(), rttAt(rtts, 50), rttAt(rtts, 90), rttAt(rtts, 100))
}

// rttAt is the q-th percentile of sorted round trips in milliseconds, "-"
// for none.
func rttAt(sorted []time.Duration, q int) string {
	if len(sorted) == 0 {
		return "-"
	}
	i := (len(sorted)*q + 99) / 100
	if i < 1 {
		i = 1
	}
	return strconv.FormatFloat(float64(sorted[i-1].Microseconds())/1000, 'f', 1, 64)
}

func portList(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func forwardList(m map[int]uint16) []string {
	out := make([]string, 0, len(m))
	for l, r := range m {
		out = append(out, fmt.Sprintf("%d→%d", l, r))
	}
	sort.Strings(out)
	return out
}
