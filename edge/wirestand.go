package main

// wirestand.go is EDGE_PROTO=wirestand: the stand of the trunk's second wire
// (hikkari-client plans/2026-11-11-trunk-second-wire.md, roadmap 4.1). It is
// experiment equipment, not a relay: one process per candidate wire on each end
// of the cascade's trunk (the entry in Russia, the exit abroad), only its own
// synthetic datagrams and bytes, never a user's.
//
// The entry end measures what the trunk's call measures (hikkari-protocol
// trunk/call.go, roadmap С8), over its own wire instead of the TCP pool: a 64 B
// datagram every 20 ms to the exit's echo and back, a line per minute of the
// wall clock with the same arithmetic. Every 10 minutes it also takes 1 MiB
// over the wire, and every minute it says what CPU its own process took and
// how much the box's neighbours stole. The hub reads these lines from the
// entry's journal next to the trunk's own (internal/cascadewatch).
//
// Wires (WIRESTAND_WIRE):
//
//   - quic: Hikkari's datagram channel as it is (QUIC with Chrome's Initial,
//     HTTP/3 auth, DATAGRAM frames; hikkari-protocol client and server);
//   - salamander: the same channel with salamander, the Hysteria2 shape;
//   - awg: AmneziaWG 3.1 in the process (amneziawg-go, gVisor) on both ends,
//     UDP and TCP inside the tunnel;
//   - udp: our own bare datagram, ChaCha20-Poly1305 and nothing else.
//
// Both ends derive every key from one secret (WIRESTAND_SECRET), which the hub
// writes to both. The exit's TLS certificate is self-signed and the entry does
// not check it: the stand measures the path, it carries nothing to protect.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	hkclient "github.com/m1d1x/hikkari-protocol/client"
	hkserver "github.com/m1d1x/hikkari-protocol/server"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

const (
	protoWireStand = "wirestand"

	wireQUIC       = "quic"
	wireSalamander = "salamander"
	wireAwg        = "awg"
	wireUDP        = "udp"

	// The call, as the trunk's (trunk/call.go): a voice frame's pace.
	standEvery  = 20 * time.Millisecond
	standSize   = 64
	standWindow = time.Minute
	standGrace  = 2 * time.Second
	// Pauses a call hears and a word lost, as the trunk's call counts them.
	standGapHeard = 60 * time.Millisecond
	standGapWord  = 150 * time.Millisecond
	standWorst    = 5

	// The bulk: 1 MiB every 10 minutes, each wire at its own minute of the
	// ten so two wires never load the path at once (and none at :x0, where
	// the hub's own rounds run).
	standBulk        = 1 << 20
	standBulkTimeout = 60 * time.Second
	// udpBulkChunk and udpBulkRate pace the bare wire's bulk: datagrams of a
	// common size at 20 Mbit/s.
	udpBulkChunk = 1200
	udpBulkRate  = 20_000_000

	// The AWG tunnel's own addresses and ports inside it.
	awgStandMTU = 1280
	awgEchoPort = 7
	awgBulkPort = 9

	standLog = "hikkari-wire-stand: "
)

// standOut writes the stand's lines as they are: the edge's own log goes
// through Caddy's JSON logger, and the hub reads these lines as plain text.
var standOut = log.New(os.Stderr, "", 0)

var (
	awgStandExit  = netip.MustParseAddr("10.77.0.1")
	awgStandEntry = netip.MustParseAddr("10.77.0.2")
)

// standBulkMinute is the minute of every ten a wire takes its 1 MiB.
var standBulkMinute = map[string]int{wireQUIC: 1, wireSalamander: 3, wireAwg: 6, wireUDP: 8}

// WireStandConfig is one stand process's environment.
type WireStandConfig struct {
	Role   string // "entry" or "exit"
	Wire   string
	Port   int    // the exit's UDP port of this wire
	Exit   string // the exit's address, the entry's only
	SNI    string // the QUIC wires' server name
	Secret []byte // 32 bytes, both ends
}

// WireStandConfigFromEnv reads WIRESTAND_*.
func WireStandConfigFromEnv(getenv func(string) string) (WireStandConfig, error) {
	c := WireStandConfig{Role: getenv("WIRESTAND_ROLE"), Wire: getenv("WIRESTAND_WIRE"), Exit: getenv("WIRESTAND_EXIT"), SNI: getenv("WIRESTAND_SNI")}
	if c.Role != "entry" && c.Role != "exit" {
		return c, fmt.Errorf("WIRESTAND_ROLE: %q is not entry or exit", c.Role)
	}
	if _, ok := standBulkMinute[c.Wire]; !ok {
		return c, fmt.Errorf("WIRESTAND_WIRE: %q is not %s, %s, %s or %s", c.Wire, wireQUIC, wireSalamander, wireAwg, wireUDP)
	}
	port, err := strconv.Atoi(getenv("WIRESTAND_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return c, fmt.Errorf("WIRESTAND_PORT: %q is not a port", getenv("WIRESTAND_PORT"))
	}
	c.Port = port
	if c.Secret, err = hex.DecodeString(getenv("WIRESTAND_SECRET")); err != nil || len(c.Secret) != 32 {
		return c, errors.New("WIRESTAND_SECRET: not 32 bytes of hex")
	}
	if c.Role == "entry" {
		if _, err := netip.ParseAddr(c.Exit); err != nil {
			return c, fmt.Errorf("WIRESTAND_EXIT: %q is not an address", c.Exit)
		}
	}
	if c.SNI == "" {
		c.SNI = "www.example.com"
	}
	return c, nil
}

// key derives one of the stand's keys from the shared secret.
func (c WireStandConfig) key(info string, n int) []byte {
	k, err := hkdf.Key(sha256.New, c.Secret, nil, "hikkari-wire-stand "+info, n)
	if err != nil {
		panic(err) // only for n beyond what SHA-256 can give
	}
	return k
}

func (c WireStandConfig) password(info string) string { return hex.EncodeToString(c.key(info, 16)) }

func wireStand() error {
	cfg, err := WireStandConfigFromEnv(osEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go reportCPU(ctx, cfg.Wire)
	if cfg.Role == "exit" {
		if err := serveWire(ctx, cfg); err != nil {
			return err
		}
		standOut.Printf(standLog+"exit of wire %s on udp/%d", cfg.Wire, cfg.Port)
		<-ctx.Done()
		return nil
	}
	w, err := dialWire(ctx, cfg)
	if err != nil {
		return err
	}
	standOut.Printf(standLog+"entry of wire %s to %s udp/%d", cfg.Wire, cfg.Exit, cfg.Port)
	go runStandBulks(ctx, cfg.Wire, w)
	runStandCall(ctx, cfg.Wire, w.call, func(s string) { standOut.Print(standLog + s) })
	return nil
}

// ---- the entry's half: a wire is a way to call and a way to take bytes ----

// standWire is what the entry measures over.
type standWire struct {
	// call opens a datagram conversation with the exit's echo.
	call func(ctx context.Context) (net.Conn, error)
	// bulk takes n bytes from the exit and says how many came.
	bulk func(ctx context.Context, n int) (int, error)
}

func dialWire(ctx context.Context, cfg WireStandConfig) (standWire, error) {
	exit := netip.AddrPortFrom(netip.MustParseAddr(cfg.Exit), uint16(cfg.Port))
	switch cfg.Wire {
	case wireQUIC, wireSalamander:
		salamander := ""
		if cfg.Wire == wireSalamander {
			salamander = cfg.password("salamander")
		}
		dc, err := hkclient.NewDatagram(hkclient.DatagramOptions{
			Context: ctx, Dialer: N.SystemDialer, Server: M.SocksaddrFromNetIP(exit),
			Password: cfg.password("key"), Salamander: salamander,
			TLS: &standTLS{config: &tls.Config{ServerName: cfg.SNI, InsecureSkipVerify: true}},
		})
		if err != nil {
			return standWire{}, err
		}
		// The exit sends every destination to its own echo and bulk; these
		// names are only what the channel carries.
		echo, bulkAt := M.ParseSocksaddrHostPort("echo.stand.arpa", 7), M.ParseSocksaddrHostPort("bulk.stand.arpa", 9)
		return standWire{
			call: func(ctx context.Context) (net.Conn, error) { return dc.DialContext(ctx, N.NetworkUDP, echo) },
			bulk: func(ctx context.Context, n int) (int, error) {
				return streamBulk(ctx, n, func(ctx context.Context) (net.Conn, error) { return dc.DialContext(ctx, N.NetworkTCP, bulkAt) })
			},
		}, nil
	case wireAwg:
		tnet, err := startStandAwg(cfg, false)
		if err != nil {
			return standWire{}, err
		}
		return standWire{
			call: func(ctx context.Context) (net.Conn, error) {
				return tnet.DialUDPAddrPort(netip.AddrPort{}, netip.AddrPortFrom(awgStandExit, awgEchoPort))
			},
			bulk: func(ctx context.Context, n int) (int, error) {
				return streamBulk(ctx, n, func(ctx context.Context) (net.Conn, error) {
					return tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(awgStandExit, awgBulkPort))
				})
			},
		}, nil
	case wireUDP:
		aead, err := chacha20poly1305.New(cfg.key("udp", chacha20poly1305.KeySize))
		if err != nil {
			return standWire{}, err
		}
		return standWire{
			call: func(ctx context.Context) (net.Conn, error) {
				c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(exit))
				if err != nil {
					return nil, err
				}
				return &sealedConn{UDPConn: c, aead: aead}, nil
			},
			bulk: func(ctx context.Context, n int) (int, error) { return udpBulk(ctx, aead, exit, n) },
		}, nil
	}
	return standWire{}, fmt.Errorf("wire %q", cfg.Wire)
}

// streamBulk asks a stream for n bytes (u64 big-endian) and reads until it
// closes or the time is up.
func streamBulk(ctx context.Context, n int, dial func(context.Context) (net.Conn, error)) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, standBulkTimeout)
	defer cancel()
	c, err := dial(ctx)
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer c.Close()
	context.AfterFunc(ctx, func() { _ = c.Close() })
	var req [8]byte
	binary.BigEndian.PutUint64(req[:], uint64(n))
	if _, err := c.Write(req[:]); err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	got, err := io.Copy(io.Discard, io.LimitReader(c, int64(n)))
	if int(got) == n {
		return n, nil // a stream that ends with its last bytes says EOF with them
	}
	if ctx.Err() != nil {
		return int(got), context.DeadlineExceeded
	}
	return int(got), err
}

// runStandBulks takes 1 MiB at the wire's minute of every ten.
func runStandBulks(ctx context.Context, wire string, w standWire) {
	minute := standBulkMinute[wire]
	for {
		now := time.Now()
		next := now.Truncate(10 * time.Minute).Add(time.Duration(minute)*time.Minute + 30*time.Second)
		if !next.After(now) {
			next = next.Add(10 * time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		start := time.Now()
		got, err := w.bulk(ctx, standBulk)
		end := "ok"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			end = "timeout"
		case err != nil && strings.HasPrefix(err.Error(), "dial"):
			end = "dial"
		case got < standBulk:
			end = "closed"
		}
		standOut.Printf(standLog+"bulk wire=%s from=%d bytes=%d want=%d ms=%d end=%s", wire, start.Unix(), got, standBulk, time.Since(start).Milliseconds(), end)
	}
}

// ---- the call: the trunk's call over a wire ----

// standGap is one pause between answers.
type standGap struct {
	At  time.Time // the answer before the pause
	Len time.Duration
}

// standWindowStats is one minute of a call, as trunk.CallWindow.
type standWindowStats struct {
	From, To time.Time
	Sent     int
	Got      int
	RTTP50   time.Duration
	RTTP99   time.Duration
	RTTMax   time.Duration
	Gaps60   int
	Gaps150  int
	GapMax   time.Duration
	Silent   time.Duration
	Worst    []standGap
	Sessions int // conversations the call used in the window (a wire that broke opens anew)
}

type standBucket struct {
	from, to time.Time
	sent     int
	got      int
	rtts     []time.Duration
	arrivals []time.Time
	sessions map[int]bool
}

// standCall keeps the books of one call: what was sent when, what came back.
type standCall struct {
	start time.Time

	mu      sync.Mutex
	buckets map[time.Time]*standBucket
	answers map[uint64]bool
	sentAt  map[uint64]time.Time
}

func newStandCall(start time.Time) *standCall {
	return &standCall{start: start, buckets: map[time.Time]*standBucket{}, answers: map[uint64]bool{}, sentAt: map[uint64]time.Time{}}
}

func (c *standCall) bucketLocked(at time.Time) *standBucket {
	from := at.Truncate(standWindow)
	b := c.buckets[from]
	if b == nil {
		b = &standBucket{from: from, to: from.Add(standWindow), sessions: map[int]bool{}}
		c.buckets[from] = b
	}
	return b
}

func (c *standCall) sent(seq uint64, at time.Time, session int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.bucketLocked(at)
	b.sent++
	b.sessions[session] = true
	c.sentAt[seq] = at
}

func (c *standCall) answer(seq uint64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.sentAt[seq]
	if !ok || c.answers[seq] {
		return
	}
	c.answers[seq] = true
	if sb := c.buckets[at.Truncate(standWindow)]; sb != nil {
		sb.got++
		sb.rtts = append(sb.rtts, now.Sub(at))
	}
	ab := c.bucketLocked(now)
	ab.arrivals = append(ab.arrivals, now)
}

// due takes the windows whose grace is over.
func (c *standCall) due(now time.Time) []standWindowStats {
	c.mu.Lock()
	var done []*standBucket
	for from, b := range c.buckets {
		if now.Sub(b.to) >= standGrace {
			done = append(done, b)
			delete(c.buckets, from)
		}
	}
	for seq, at := range c.sentAt {
		if now.Sub(at.Truncate(standWindow).Add(standWindow)) >= standGrace {
			delete(c.sentAt, seq)
			delete(c.answers, seq)
		}
	}
	c.mu.Unlock()
	sort.Slice(done, func(i, j int) bool { return done[i].from.Before(done[j].from) })
	out := make([]standWindowStats, 0, len(done))
	for _, b := range done {
		from := b.from
		if c.start.After(from) {
			from = c.start
		}
		out = append(out, b.window(from))
	}
	return out
}

// window is trunk/call.go's arithmetic: the pauses between answers, from
// the window's start (or the call's) to its end.
func (b *standBucket) window(from time.Time) standWindowStats {
	w := standWindowStats{From: b.from, To: b.to, Sent: b.sent, Got: b.got, Sessions: len(b.sessions)}
	if len(b.rtts) > 0 {
		sort.Slice(b.rtts, func(i, j int) bool { return b.rtts[i] < b.rtts[j] })
		w.RTTP50 = standQuantile(b.rtts, 50)
		w.RTTP99 = standQuantile(b.rtts, 99)
		w.RTTMax = b.rtts[len(b.rtts)-1]
	}
	sort.Slice(b.arrivals, func(i, j int) bool { return b.arrivals[i].Before(b.arrivals[j]) })
	points := append(append([]time.Time{from}, b.arrivals...), b.to)
	for i := 1; i < len(points); i++ {
		gap := points[i].Sub(points[i-1])
		if gap > w.GapMax {
			w.GapMax = gap
		}
		if gap <= standGapHeard {
			continue
		}
		w.Gaps60++
		if gap > standGapWord {
			w.Gaps150++
		}
		w.Silent += gap - standEvery
		w.Worst = append(w.Worst, standGap{At: points[i-1], Len: gap})
	}
	sort.Slice(w.Worst, func(i, j int) bool { return w.Worst[i].Len > w.Worst[j].Len })
	if len(w.Worst) > standWorst {
		w.Worst = w.Worst[:standWorst]
	}
	return w
}

func standQuantile(sorted []time.Duration, q int) time.Duration {
	i := (len(sorted)*q + 99) / 100
	if i < 1 {
		i = 1
	}
	return sorted[i-1]
}

// runStandCall keeps the call going over dial's conversations until ctx ends:
// a datagram every 20 ms, a conversation that fails to send or to open is
// replaced at the next tick (that datagram counts as lost).
func runStandCall(ctx context.Context, wire string, dial func(context.Context) (net.Conn, error), say func(string)) {
	c := newStandCall(time.Now())
	var (
		cur     net.Conn
		session int
		seq     uint64
	)
	defer func() {
		if cur != nil {
			cur.Close()
		}
	}()
	open := func() {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conv, err := dial(dctx)
		if err != nil {
			return
		}
		session++
		cur = conv
		go func(conv net.Conn) {
			buf := make([]byte, 2048)
			for {
				n, err := conv.Read(buf)
				if err != nil {
					return
				}
				if n >= 8 {
					c.answer(binary.BigEndian.Uint64(buf), time.Now())
				}
			}
		}(conv)
	}
	t := time.NewTicker(standEvery)
	defer t.Stop()
	payload := make([]byte, standSize)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			seq++
			if cur == nil {
				open()
			}
			c.sent(seq, now, session)
			if cur != nil {
				binary.BigEndian.PutUint64(payload, seq)
				if _, err := cur.Write(payload); err != nil {
					cur.Close()
					cur = nil
				}
			}
			for _, w := range c.due(time.Now()) {
				say(standCallLine(wire, w))
			}
		}
	}
}

// standCallLine is a minute of the call, the trunk's call line with the wire
// named (trunkentry callLine): the hub reads both alike.
func standCallLine(wire string, w standWindowStats) string {
	worst := make([]string, 0, len(w.Worst))
	for _, g := range w.Worst {
		worst = append(worst, fmt.Sprintf("%d:%d", g.At.UnixMilli(), g.Len.Milliseconds()))
	}
	if len(worst) == 0 {
		worst = append(worst, "-")
	}
	ms := func(d time.Duration) string {
		if w.Got == 0 {
			return "-"
		}
		return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64)
	}
	return fmt.Sprintf("call wire=%s copies=1 from=%d sent=%d got=%d rtt_p50=%s rtt_p99=%s rtt_max=%s gaps60=%d gaps150=%d gap_max=%d silent_ms=%d sessions=%d worst=%s",
		wire, w.From.Unix(), w.Sent, w.Got, ms(w.RTTP50), ms(w.RTTP99), ms(w.RTTMax),
		w.Gaps60, w.Gaps150, w.GapMax.Milliseconds(), w.Silent.Milliseconds(), w.Sessions, strings.Join(worst, ","))
}

// ---- CPU: what the process took and what the box's neighbours stole ----

// cpuSample is the process's CPU time and the box's counters at one moment.
type cpuSample struct {
	proc               time.Duration
	total, steal, idle uint64
}

func readCPU() (cpuSample, error) {
	var s cpuSample
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return s, err
	}
	s.proc, err = procCPU(string(raw))
	if err != nil {
		return s, err
	}
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return s, err
	}
	s.total, s.steal, s.idle, err = boxCPU(string(stat))
	return s, err
}

// procCPU is utime+stime of a /proc/<pid>/stat line, at 100 ticks a second.
func procCPU(stat string) (time.Duration, error) {
	// The command name may hold spaces; the fields after it do not.
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("stat: no command")
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 13 {
		return 0, errors.New("stat: short")
	}
	ut, err1 := strconv.ParseUint(f[11], 10, 64)
	st, err2 := strconv.ParseUint(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, errors.New("stat: utime/stime")
	}
	return time.Duration(ut+st) * 10 * time.Millisecond, nil
}

// boxCPU reads the "cpu" line of /proc/stat: all ticks, steal, idle+iowait.
func boxCPU(stat string) (total, steal, idle uint64, err error) {
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) < 9 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64
		for i := range v {
			v[i], _ = strconv.ParseUint(f[i+1], 10, 64)
			total += v[i]
		}
		return total, v[7], v[3] + v[4], nil
	}
	return 0, 0, 0, errors.New("/proc/stat: no cpu line")
}

// cpuLine is the minute between two samples.
func cpuLine(wire string, from time.Time, a, b cpuSample) string {
	dt := float64(b.total - a.total)
	pct := func(x uint64) string {
		if dt <= 0 {
			return "0"
		}
		return strconv.FormatFloat(100*float64(x)/dt, 'f', 1, 64)
	}
	busy := (b.total - a.total) - (b.idle - a.idle) - (b.steal - a.steal)
	return fmt.Sprintf("cpu wire=%s from=%d cpu_ms=%d steal_pct=%s busy_pct=%s",
		wire, from.Unix(), (b.proc - a.proc).Milliseconds(), pct(b.steal-a.steal), pct(busy))
}

func reportCPU(ctx context.Context, wire string) {
	prev, err := readCPU()
	if err != nil {
		return // not Linux: nothing to say
	}
	from := time.Now().Truncate(time.Minute)
	for {
		next := from.Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		cur, err := readCPU()
		if err != nil {
			return
		}
		standOut.Print(standLog + cpuLine(wire, from, prev, cur))
		prev, from = cur, next
	}
}

// ---- the exit's half ----

func serveWire(ctx context.Context, cfg WireStandConfig) error {
	switch cfg.Wire {
	case wireQUIC, wireSalamander:
		return serveStandDatagram(ctx, cfg)
	case wireAwg:
		tnet, err := startStandAwg(cfg, true)
		if err != nil {
			return err
		}
		u, err := tnet.ListenUDPAddrPort(netip.AddrPortFrom(awgStandExit, awgEchoPort))
		if err != nil {
			return err
		}
		go echoPackets(u)
		l, err := tnet.ListenTCPAddrPort(netip.AddrPortFrom(awgStandExit, awgBulkPort))
		if err != nil {
			return err
		}
		go serveBulk(l)
		return nil
	case wireUDP:
		aead, err := chacha20poly1305.New(cfg.key("udp", chacha20poly1305.KeySize))
		if err != nil {
			return err
		}
		c, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.Port})
		if err != nil {
			return err
		}
		context.AfterFunc(ctx, func() { _ = c.Close() })
		go serveSealed(c, aead)
		return nil
	}
	return fmt.Errorf("wire %q", cfg.Wire)
}

// echoPackets sends every datagram back to where it came from.
func echoPackets(c net.PacketConn) {
	buf := make([]byte, 2048)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return
		}
		_, _ = c.WriteTo(buf[:n], from)
	}
}

// serveBulk gives each stream the bytes it asks for and closes it.
func serveBulk(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(standBulkTimeout))
			var req [8]byte
			if _, err := io.ReadFull(c, req[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint64(req[:])
			if n > 16*standBulk {
				return
			}
			_, _ = io.CopyN(c, zeroReader{}, int64(n))
		}(c)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// serveStandDatagram is Hikkari's datagram channel whose exit is the stand's
// own echo and bulk on loopback, whatever destination the entry names.
func serveStandDatagram(ctx context.Context, cfg WireStandConfig) error {
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go echoPackets(echo)
	bulk, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go serveBulk(bulk)
	context.AfterFunc(ctx, func() { _ = echo.Close(); _ = bulk.Close() })
	cert, err := standCertificate(cfg.SNI)
	if err != nil {
		return err
	}
	salamander := ""
	if cfg.Wire == wireSalamander {
		salamander = cfg.password("salamander")
	}
	key := cfg.password("key")
	d, err := hkserver.ListenDatagram(ctx, hkserver.DatagramOptions{
		Listen:      fmt.Sprintf(":%d", cfg.Port),
		Certificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil },
		Salamander:  salamander,
		Admitter:    standAdmitter{},
		Exit:        standExit{echo: echo.LocalAddr().(*net.UDPAddr).AddrPort(), bulk: bulk.Addr().String()},
	})
	if err != nil {
		return err
	}
	d.SetKeys([]string{key})
	context.AfterFunc(ctx, func() { _ = d.Close() })
	return nil
}

// standAdmitter admits every key the roster holds (only the stand's own):
// nothing to count.
type standAdmitter struct{}

func (standAdmitter) Admit(ctx context.Context, key string) (context.Context, func(), func(int, int), bool) {
	if key == "" {
		return nil, nil, nil, false // the probe credential: the stand has no probe
	}
	return ctx, func() {}, func(int, int) {}, true
}

// standExit sends every datagram to the echo and every stream to the bulk.
type standExit struct {
	echo netip.AddrPort
	bulk string
}

func (e standExit) Resolve(context.Context, M.Socksaddr) (netip.AddrPort, error) { return e.echo, nil }

func (e standExit) DialTCP(ctx context.Context, _ M.Socksaddr) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", e.bulk)
}

// standCertificate is a self-signed certificate for name, new every run.
func standCertificate(name string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		DNSNames: []string{name}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// standTLS adapts a crypto/tls client config to sing's Config; QUIC reads
// only STDConfig.
type standTLS struct{ config *tls.Config }

func (c *standTLS) ServerName() string                  { return c.config.ServerName }
func (c *standTLS) SetServerName(name string)           { c.config.ServerName = name }
func (c *standTLS) NextProtos() []string                { return c.config.NextProtos }
func (c *standTLS) SetNextProtos(protos []string)       { c.config.NextProtos = protos }
func (c *standTLS) HandshakeTimeout() time.Duration     { return 0 }
func (c *standTLS) SetHandshakeTimeout(time.Duration)   {}
func (c *standTLS) STDConfig() (*aTLS.STDConfig, error) { return c.config, nil }
func (c *standTLS) Client(net.Conn) (aTLS.Conn, error)  { return nil, errors.ErrUnsupported }
func (c *standTLS) Clone() aTLS.Config                  { return &standTLS{config: c.config.Clone()} }

// ---- AWG: both ends in the process ----

// standAwgUAPI is one end's device configuration: AmneziaWG 3.1 with the
// paddings, header ranges and header protection our relays draw, all derived
// from the stand's secret so both ends agree.
func standAwgUAPI(cfg WireStandConfig, exit bool) (string, error) {
	privExit, privEntry := awgPrivate(cfg.key("awg exit", 32)), awgPrivate(cfg.key("awg entry", 32))
	own, peer := privEntry, privExit
	if exit {
		own, peer = privExit, privEntry
	}
	peerPub, err := curve25519.X25519(peer, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	v3 := AwgV3{
		HeaderProtectionKey: base64.StdEncoding.EncodeToString(cfg.key("awg header protection", 32)),
		RandomTrailers:      true,
	}
	v3text, err := v3.uapi()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", hex.EncodeToString(own))
	if exit {
		fmt.Fprintf(&b, "listen_port=%d\n", cfg.Port)
	}
	b.WriteString("jc=4\njmin=40\njmax=70\ns1=37\ns2=58\ns3=24\ns4=16\n")
	b.WriteString("h1=471800590-471800690\nh2=1246894907-1246895007\nh3=923637689-923637789\nh4=1769581055-1869581055\n")
	b.WriteString(v3text)
	fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(peerPub))
	if exit {
		fmt.Fprintf(&b, "allowed_ip=%s/32\n", awgStandEntry)
	} else {
		fmt.Fprintf(&b, "endpoint=%s\nallowed_ip=%s/32\npersistent_keepalive_interval=25\n",
			netip.AddrPortFrom(netip.MustParseAddr(cfg.Exit), uint16(cfg.Port)), awgStandExit)
	}
	return b.String(), nil
}

// awgPrivate clamps 32 bytes into an X25519 private key.
func awgPrivate(k []byte) []byte {
	k[0] &= 248
	k[31] = k[31]&127 | 64
	return k
}

func startStandAwg(cfg WireStandConfig, exit bool) (*netstack.Net, error) {
	addr := awgStandEntry
	if exit {
		addr = awgStandExit
	}
	tunDev, tnet, err := netstack.CreateNetTUN([]netip.Addr{addr}, nil, awgStandMTU)
	if err != nil {
		return nil, err
	}
	logger := &device.Logger{Verbosef: device.DiscardLogf, Errorf: func(f string, a ...any) { standOut.Printf(standLog+"awg: "+f, a...) }}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	uapi, err := standAwgUAPI(cfg, exit)
	if err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("awg config: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("awg up: %w", err)
	}
	return tnet, nil
}

// ---- the bare wire: a sealed datagram and nothing else ----
//
// A datagram is nonce (12) + ChaCha20-Poly1305 of kind (1) + body. Kind 1 is
// the call (the exit seals the body back), 2 asks for a bulk (body: u32
// bytes), 3 is a piece of the bulk (body: u32 offset + bytes).

const (
	sealedCall     = 1
	sealedBulkAsk  = 2
	sealedBulkPart = 3
)

func seal(aead interface {
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
	NonceSize() int
}, kind byte, body []byte) []byte {
	out := make([]byte, aead.NonceSize(), aead.NonceSize()+1+len(body)+16)
	_, _ = rand.Read(out)
	plain := append([]byte{kind}, body...)
	return aead.Seal(out, out[:aead.NonceSize()], plain, nil)
}

type opener interface {
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	NonceSize() int
}

func unseal(aead opener, p []byte) (byte, []byte, bool) {
	if len(p) < aead.NonceSize()+1 {
		return 0, nil, false
	}
	plain, err := aead.Open(nil, p[:aead.NonceSize()], p[aead.NonceSize():], nil)
	if err != nil || len(plain) < 1 {
		return 0, nil, false
	}
	return plain[0], plain[1:], true
}

type sealingAEAD interface {
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	NonceSize() int
}

// sealedConn is the call's conversation on the bare wire.
type sealedConn struct {
	*net.UDPConn
	aead sealingAEAD
}

func (c *sealedConn) Write(p []byte) (int, error) {
	if _, err := c.UDPConn.Write(seal(c.aead, sealedCall, p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *sealedConn) Read(p []byte) (int, error) {
	buf := make([]byte, 2048)
	for {
		n, err := c.UDPConn.Read(buf)
		if err != nil {
			return 0, err
		}
		if kind, body, ok := unseal(c.aead, buf[:n]); ok && kind == sealedCall {
			return copy(p, body), nil
		}
	}
}

// serveSealed is the bare wire's exit.
func serveSealed(c *net.UDPConn, aead sealingAEAD) {
	buf := make([]byte, 2048)
	for {
		n, from, err := c.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		kind, body, ok := unseal(aead, buf[:n])
		if !ok {
			continue // a stranger: no answer
		}
		switch kind {
		case sealedCall:
			_, _ = c.WriteToUDPAddrPort(seal(aead, sealedCall, body), from)
		case sealedBulkAsk:
			if len(body) >= 4 {
				if want := int(binary.BigEndian.Uint32(body)); want <= 16*standBulk {
					go sendSealedBulk(c, aead, from, want)
				}
			}
		}
	}
}

// sendSealedBulk sends want bytes in pieces, paced at udpBulkRate.
func sendSealedBulk(c *net.UDPConn, aead sealingAEAD, to netip.AddrPort, want int) {
	piece := make([]byte, 4+udpBulkChunk)
	every := time.Duration(float64(time.Second) * float64(udpBulkChunk*8) / udpBulkRate)
	next := time.Now()
	for off := 0; off < want; off += udpBulkChunk {
		n := min(udpBulkChunk, want-off)
		binary.BigEndian.PutUint32(piece, uint32(off))
		_, _ = c.WriteToUDPAddrPort(seal(aead, sealedBulkPart, piece[:4+n]), to)
		next = next.Add(every)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
	}
}

// udpBulk asks the bare wire's exit for n bytes and counts the distinct
// pieces that arrive within a second after the last one (or the timeout).
func udpBulk(ctx context.Context, aead sealingAEAD, exit netip.AddrPort, n int) (int, error) {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(exit))
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer c.Close()
	ask := make([]byte, 4)
	binary.BigEndian.PutUint32(ask, uint32(n))
	if _, err := c.Write(seal(aead, sealedBulkAsk, ask)); err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	deadline := time.Now().Add(standBulkTimeout)
	seen := map[uint32]bool{}
	got := 0
	buf := make([]byte, 2048)
	wait := 5 * time.Second // the first piece; then a second of silence ends it
	for got < n {
		if ctx.Err() != nil {
			return got, ctx.Err()
		}
		until := time.Now().Add(wait)
		if until.After(deadline) {
			until = deadline
		}
		_ = c.SetReadDeadline(until)
		k, err := c.Read(buf)
		if err != nil {
			if time.Now().After(deadline) {
				return got, context.DeadlineExceeded
			}
			return got, nil // silence: the rest was lost
		}
		kind, body, ok := unseal(aead, buf[:k])
		if !ok || kind != sealedBulkPart || len(body) < 4 {
			continue
		}
		off := binary.BigEndian.Uint32(body)
		if !seen[off] {
			seen[off] = true
			got += len(body) - 4
		}
		wait = time.Second
	}
	return got, nil
}
