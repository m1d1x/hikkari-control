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
//   - udp: our own bare datagram, ChaCha20-Poly1305 and nothing else;
//   - h3: Naive over HTTP/3, Chromium's own QUIC (cronet) on the entry,
//     Caddy on the exit, streams only (wirestand_h3.go).
//
// WIRESTAND_LOAD (the entry's only) puts synthetic users over the wire
// (wirestand_load.go).
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
	"os/exec"
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

// WireStandConfig is one stand process's environment.
type WireStandConfig struct {
	Role   string // "entry" or "exit"
	Wire   string
	Port   int    // the exit's UDP port of this wire
	Exit   string // the exit's address, the entry's only
	SNI    string // the QUIC wires' server name
	Secret []byte // 32 bytes, both ends
	// BenchFrom and BenchUntil are the bench window (WIRESTAND_BENCH
	// "<unix>-<unix>", the entry's only): bigger bulks and a load inside it.
	BenchFrom, BenchUntil time.Time
	// Load is WIRESTAND_LOAD, the synthetic users' plan (the entry's only).
	Load *loadPlan
	// H3Congestion is Chromium's QUIC congestion controller on the wire h3
	// (WIRESTAND_H3_CC: "" Chromium's default, bbr, bbr2, cubic, reno).
	H3Congestion string
	// Cronet is libcronet.so's path for the wire h3's entry
	// (WIRESTAND_CRONET); empty, cronet looks beside the executable.
	Cronet string
	// Unit is the process's systemd unit (WIRESTAND_UNIT), whose IP
	// accounting gives the wire's packets in the minute's CPU line.
	Unit string
}

// WireStandConfigFromEnv reads WIRESTAND_*.
func WireStandConfigFromEnv(getenv func(string) string) (WireStandConfig, error) {
	c := WireStandConfig{Role: getenv("WIRESTAND_ROLE"), Wire: getenv("WIRESTAND_WIRE"), Exit: getenv("WIRESTAND_EXIT"), SNI: getenv("WIRESTAND_SNI"),
		H3Congestion: getenv("WIRESTAND_H3_CC"), Unit: getenv("WIRESTAND_UNIT"), Cronet: getenv("WIRESTAND_CRONET")}
	if c.Role != "entry" && c.Role != "exit" {
		return c, fmt.Errorf("WIRESTAND_ROLE: %q is not entry or exit", c.Role)
	}
	if _, ok := standSlot[c.Wire]; !ok {
		return c, fmt.Errorf("WIRESTAND_WIRE: %q is not %s, %s, %s, %s or %s", c.Wire, wireQUIC, wireSalamander, wireAwg, wireUDP, wireH3)
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
	if bench := getenv("WIRESTAND_BENCH"); bench != "" {
		a, b, _ := strings.Cut(bench, "-")
		from, err1 := strconv.ParseInt(a, 10, 64)
		until, err2 := strconv.ParseInt(b, 10, 64)
		if err1 != nil || err2 != nil || until <= from {
			return c, fmt.Errorf("WIRESTAND_BENCH: %q is not <unix>-<unix>", bench)
		}
		c.BenchFrom, c.BenchUntil = time.Unix(from, 0), time.Unix(until, 0)
	}
	if load := getenv("WIRESTAND_LOAD"); load != "" {
		p, err := parseLoadPlan(load)
		if err != nil {
			return c, err
		}
		c.Load = &p
	}
	switch c.H3Congestion {
	case "", "bbr", "bbr2", "cubic", "reno":
	default:
		return c, fmt.Errorf("WIRESTAND_H3_CC: %q is not bbr, bbr2, cubic or reno", c.H3Congestion)
	}
	if c.Wire == wireH3 && c.Role == "entry" && !h3Built {
		return c, errors.New("WIRESTAND_WIRE=h3: this edge is built without cronet (-tags with_purego,wirestand_h3)")
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
	go reportCPU(ctx, cfg.Wire, cfg.Unit)
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
	if !cfg.BenchUntil.IsZero() {
		standOut.Printf(standLog+"bench %s from %s until %s", cfg.Wire, cfg.BenchFrom.UTC().Format(time.RFC3339), cfg.BenchUntil.UTC().Format(time.RFC3339))
	}
	if cfg.Load != nil {
		standOut.Printf(standLog+"load %s from %s until %s", cfg.Wire, cfg.Load.From.UTC().Format(time.RFC3339), cfg.Load.Until().UTC().Format(time.RFC3339))
		go runStandLoad(ctx, cfg.Wire, w, *cfg.Load)
	}
	go runStandBulks(ctx, cfg.Wire, w, cfg.BenchFrom, cfg.BenchUntil)
	runStandCall(ctx, cfg.Wire, w.call, func(s string) { standOut.Print(standLog + s) })
	return nil
}

// ---- the entry's half: a wire is a way to call and a way to take bytes ----

// standWire is what the entry measures over.
type standWire struct {
	// call opens a datagram conversation with the exit's echo.
	call func(ctx context.Context) (net.Conn, error)
	// bulk takes bytes from the exit as the spec says.
	bulk func(ctx context.Context, b standBulkSpec) standBulkResult
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
			bulk: func(ctx context.Context, b standBulkSpec) standBulkResult {
				return streamBulk(ctx, b, func(ctx context.Context) (net.Conn, error) { return dc.DialContext(ctx, N.NetworkTCP, bulkAt) })
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
			bulk: func(ctx context.Context, b standBulkSpec) standBulkResult {
				return streamBulk(ctx, b, func(ctx context.Context) (net.Conn, error) {
					return tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(awgStandExit, awgBulkPort))
				})
			},
		}, nil
	case wireH3:
		return dialStandH3(ctx, cfg)
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
			bulk: func(ctx context.Context, b standBulkSpec) standBulkResult { return udpBulk(ctx, aead, exit, b) },
		}, nil
	}
	return standWire{}, fmt.Errorf("wire %q", cfg.Wire)
}

// ---- bulk: bytes from the exit over the wire ----
//
// Every 10 minutes a wire takes 1 MiB (does it arrive whole). In a bench
// window (WIRESTAND_BENCH) it takes 12 MiB as fast as it goes instead, the
// size of the hub's chunk round over the TCP pool (48 × 256 KiB), so goodputs
// compare; and once in the hour it holds 100 Mbit/s for 90 s, in 10 s slices,
// to find a cut by volume.

// standBulkSpec is one bulk: Bytes to take; Rate, when set, the pace in bits
// a second (the reader's for a stream, the exit's for the bare wire); For,
// when set, how long a load lasts.
type standBulkSpec struct {
	Kind  string // "1m", "12m", "load"
	Bytes int
	Rate  int
	For   time.Duration
}

// standBulkResult is what came.
type standBulkResult struct {
	Bytes  int
	End    string // ok, timeout, closed, dial
	Took   time.Duration
	Slices []int // bytes per 10 s, a load's
}

const (
	standBenchBulk = 12 << 20
	standLoadRate  = 100_000_000
	standLoadFor   = 90 * time.Second
	standSlice     = 10 * time.Second
	// standBulkMax is the most a stream or the bare wire hands out at once:
	// a load's 90 s at 100 Mbit/s, with room.
	standBulkMax = 2 << 30
)

// standSlot is a wire's place in every ten minutes: its own seconds, so no
// two wires take bytes at once and a load (90 s) ends before the next wire's
// slot. :x0 is the hub's own round.
var standSlot = map[string]time.Duration{
	wireQUIC:       1 * time.Minute,
	wireSalamander: 3*time.Minute + 30*time.Second,
	wireAwg:        6 * time.Minute,
	wireUDP:        8*time.Minute + 30*time.Second,
	// h3 takes no load in a bench (no 90 s left free in the ten minutes):
	// its 1 MiB, or 12 MiB, only.
	wireH3: 7*time.Minute + 40*time.Second,
}

// standWireOrder is the order the wires take their load in a bench hour.
var standWireOrder = []string{wireQUIC, wireSalamander, wireAwg, wireUDP}

// nextBulk is a wire's next bulk after now. Outside a bench window it is
// 1 MiB at the wire's slot; inside it 12 MiB, except the cycle of ten minutes
// whose number (from the window's start, from 1) is the wire's place in
// standWireOrder plus one: then the load.
func nextBulk(wire string, now, benchFrom, benchUntil time.Time) (time.Time, standBulkSpec) {
	at := now.Truncate(10 * time.Minute).Add(standSlot[wire])
	if !at.After(now) {
		at = at.Add(10 * time.Minute)
	}
	if at.Before(benchFrom) || !at.Before(benchUntil) {
		return at, standBulkSpec{Kind: "1m", Bytes: standBulk, Rate: udpBulkRate}
	}
	cycle := int(at.Sub(benchFrom) / (10 * time.Minute))
	for i, w := range standWireOrder {
		if w == wire && cycle == i+1 {
			return at, standBulkSpec{Kind: "load", Bytes: int(int64(standLoadRate) * int64(standLoadFor/time.Second) / 8), Rate: standLoadRate, For: standLoadFor}
		}
	}
	return at, standBulkSpec{Kind: "12m", Bytes: standBenchBulk, Rate: standLoadRate}
}

// streamBulk asks a stream for b.Bytes (u64 big-endian) and reads until they
// came, the stream closes or the time is up; a load reads no faster than
// b.Rate and stops after b.For.
func streamBulk(ctx context.Context, b standBulkSpec, dial func(context.Context) (net.Conn, error)) standBulkResult {
	limit := standBulkTimeout
	if b.For > 0 {
		limit = b.For + 30*time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	start := time.Now()
	res := standBulkResult{End: "ok"}
	c, err := dial(ctx)
	if err != nil {
		res.End, res.Took = "dial", time.Since(start)
		return res
	}
	defer c.Close()
	context.AfterFunc(ctx, func() { _ = c.Close() })
	var req [8]byte
	binary.BigEndian.PutUint64(req[:], uint64(b.Bytes))
	if _, err := c.Write(req[:]); err != nil {
		res.End, res.Took = "dial", time.Since(start)
		return res
	}
	buf := make([]byte, 64<<10)
	for res.Bytes < b.Bytes {
		if b.For > 0 && time.Since(start) >= b.For {
			break
		}
		n, err := c.Read(buf[:min(len(buf), b.Bytes-res.Bytes)])
		if n > 0 {
			res.Bytes += n
			if b.For > 0 {
				res.Slices = addSlice(res.Slices, time.Since(start), n)
				// The reader's pace: no faster than Rate.
				if ahead := time.Duration(float64(res.Bytes*8)/float64(b.Rate)*float64(time.Second)) - time.Since(start); ahead > time.Millisecond {
					time.Sleep(ahead)
				}
			}
		}
		if err != nil {
			switch {
			case ctx.Err() != nil:
				res.End = "timeout"
			case res.Bytes < b.Bytes:
				res.End = "closed"
			}
			break
		}
	}
	res.Took = time.Since(start)
	return res
}

// addSlice counts n bytes in the 10 s slice at elapsed.
func addSlice(slices []int, elapsed time.Duration, n int) []int {
	i := int(elapsed / standSlice)
	for len(slices) <= i {
		slices = append(slices, 0)
	}
	slices[i] += n
	return slices
}

// bulkLine is one bulk. mbps is what came over the time it took; a load
// adds its slices in Mbit/s.
func bulkLine(wire string, from time.Time, b standBulkSpec, r standBulkResult) string {
	want := b.Bytes
	mbps := 0.0
	if r.Took > 0 {
		mbps = float64(r.Bytes*8) / r.Took.Seconds() / 1e6
	}
	line := fmt.Sprintf("bulk wire=%s kind=%s from=%d bytes=%d want=%d ms=%d end=%s mbps=%.1f",
		wire, b.Kind, from.Unix(), r.Bytes, want, r.Took.Milliseconds(), r.End, mbps)
	if len(r.Slices) > 0 {
		parts := make([]string, len(r.Slices))
		for i, n := range r.Slices {
			parts[i] = strconv.FormatFloat(float64(n*8)/standSlice.Seconds()/1e6, 'f', 1, 64)
		}
		line += " slices=" + strings.Join(parts, ",")
	}
	return line
}

// runStandBulks takes the wire's bulks until ctx ends.
func runStandBulks(ctx context.Context, wire string, w standWire, benchFrom, benchUntil time.Time) {
	for {
		at, spec := nextBulk(wire, time.Now(), benchFrom, benchUntil)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(at)):
		}
		start := time.Now()
		res := w.bulk(ctx, spec)
		standOut.Print(standLog + bulkLine(wire, start, spec, res))
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
	// threads is each thread's CPU (tid: comm, time): Go's threads carry the
	// process's name, Chromium's their own.
	threads map[int]threadCPU
	// ip is the unit's IP accounting, when there is one.
	ip *ipAccount
}

type threadCPU struct {
	comm string
	cpu  time.Duration
}

type ipAccount struct{ inPkts, outPkts, inBytes, outBytes uint64 }

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
	s.threads = readThreads()
	return s, err
}

// readThreads is every thread's CPU from /proc/self/task.
func readThreads() map[int]threadCPU {
	out := map[int]threadCPU{}
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return out
	}
	for _, t := range tasks {
		tid, err := strconv.Atoi(t.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/self/task/" + t.Name() + "/stat")
		if err != nil {
			continue
		}
		cpu, err := procCPU(string(raw))
		if err != nil {
			continue
		}
		comm := ""
		if i, j := strings.IndexByte(string(raw), '('), strings.LastIndexByte(string(raw), ')'); i >= 0 && j > i {
			comm = string(raw[i+1 : j])
		}
		out[tid] = threadCPU{comm: comm, cpu: cpu}
	}
	return out
}

// readIPAccount is the unit's IP accounting (IPAccounting=yes): packets and
// bytes in and out of the process's cgroup, which is the wire's own.
func readIPAccount(unit string) *ipAccount {
	if unit == "" {
		return nil
	}
	raw, err := exec.Command("systemctl", "show", "-p", "IPIngressPackets", "-p", "IPEgressPackets", "-p", "IPIngressBytes", "-p", "IPEgressBytes", unit).Output()
	if err != nil {
		return nil
	}
	return parseIPAccount(string(raw))
}

// parseIPAccount reads systemctl show's Key=value lines; nil when any is
// missing or not set (no accounting).
func parseIPAccount(raw string) *ipAccount {
	v := map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		k, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return nil
		}
		v[k] = n
	}
	if len(v) != 4 {
		return nil
	}
	return &ipAccount{inPkts: v["IPIngressPackets"], outPkts: v["IPEgressPackets"], inBytes: v["IPIngressBytes"], outBytes: v["IPEgressBytes"]}
}

// threadSplit is the minute's CPU of Go's threads (named as the process) and
// of the others (Chromium's), and the three busiest others by name.
func threadSplit(main string, a, b map[int]threadCPU) (goCPU, other time.Duration, top string) {
	byName := map[string]time.Duration{}
	for tid, t := range b {
		d := t.cpu - a[tid].cpu
		if _, ok := a[tid]; !ok {
			d = t.cpu
		}
		if d <= 0 {
			continue
		}
		if t.comm == main {
			goCPU += d
			continue
		}
		other += d
		byName[t.comm] += d
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return byName[names[i]] > byName[names[j]] })
	parts := []string{}
	for i, n := range names {
		if i == 3 {
			break
		}
		parts = append(parts, strings.ReplaceAll(n, " ", "_")+":"+strconv.FormatInt(byName[n].Milliseconds(), 10))
	}
	if len(parts) == 0 {
		return goCPU, other, "-"
	}
	return goCPU, other, strings.Join(parts, ",")
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
	line := fmt.Sprintf("cpu wire=%s from=%d cpu_ms=%d steal_pct=%s busy_pct=%s",
		wire, from.Unix(), (b.proc - a.proc).Milliseconds(), pct(b.steal-a.steal), pct(busy))
	if len(b.threads) > 0 {
		goCPU, other, top := threadSplit(mainComm(), a.threads, b.threads)
		line += fmt.Sprintf(" go_ms=%d other_ms=%d other_top=%s", goCPU.Milliseconds(), other.Milliseconds(), top)
	}
	if a.ip != nil && b.ip != nil {
		line += fmt.Sprintf(" pkts_in=%d pkts_out=%d bytes_in=%d bytes_out=%d", b.ip.inPkts-a.ip.inPkts, b.ip.outPkts-a.ip.outPkts, b.ip.inBytes-a.ip.inBytes, b.ip.outBytes-a.ip.outBytes)
	}
	return line + h3SocketsLine()
}

// mainComm is the name Go's threads carry: the process's, as the kernel
// cuts it (15 bytes).
func mainComm() string {
	raw, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func reportCPU(ctx context.Context, wire, unit string) {
	prev, err := readCPU()
	if err != nil {
		return // not Linux: nothing to say
	}
	prev.ip = readIPAccount(unit)
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
		cur.ip = readIPAccount(unit)
		standOut.Print(standLog + cpuLine(wire, from, prev, cur))
		prev, from = cur, next
	}
}

// ---- the exit's half ----

func serveWire(ctx context.Context, cfg WireStandConfig) error {
	switch cfg.Wire {
	case wireQUIC, wireSalamander:
		return serveStandDatagram(ctx, cfg)
	case wireH3:
		return serveStandH3(ctx, cfg)
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
			// A load reads 90 s at its own pace; the deadline only ends
			// a stream its reader left.
			_ = c.SetDeadline(time.Now().Add(standLoadFor + 2*standBulkTimeout))
			var req [8]byte
			if _, err := io.ReadFull(c, req[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint64(req[:])
			if n > standBulkMax {
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
			if len(body) >= 8 {
				want, rate := int(binary.BigEndian.Uint32(body)), int(binary.BigEndian.Uint32(body[4:]))*1000
				if want <= standBulkMax && rate > 0 && rate <= 2*standLoadRate {
					go sendSealedBulk(c, aead, from, want, rate)
				}
			}
		}
	}
}

// sendSealedBulk sends want bytes in pieces of udpBulkChunk, numbered,
// paced at rate bits a second.
func sendSealedBulk(c *net.UDPConn, aead sealingAEAD, to netip.AddrPort, want, rate int) {
	piece := make([]byte, 4+udpBulkChunk)
	every := time.Duration(float64(time.Second) * float64(udpBulkChunk*8) / float64(rate))
	next := time.Now()
	for i, off := 0, 0; off < want; i, off = i+1, off+udpBulkChunk {
		n := min(udpBulkChunk, want-off)
		binary.BigEndian.PutUint32(piece, uint32(i))
		_, _ = c.WriteToUDPAddrPort(seal(aead, sealedBulkPart, piece[:4+n]), to)
		next = next.Add(every)
		if d := time.Until(next); d > time.Millisecond {
			time.Sleep(d)
		}
	}
}

// udpBulk asks the bare wire's exit for b.Bytes at b.Rate and counts the
// distinct pieces that arrive until a second passes without one (or the time
// is up). The bare wire has no resend: what is missing was lost.
func udpBulk(ctx context.Context, aead sealingAEAD, exit netip.AddrPort, b standBulkSpec) standBulkResult {
	start := time.Now()
	res := standBulkResult{End: "ok"}
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(exit))
	if err != nil {
		res.End = "dial"
		return res
	}
	defer c.Close()
	_ = c.SetReadBuffer(8 << 20)
	ask := make([]byte, 8)
	binary.BigEndian.PutUint32(ask, uint32(b.Bytes))
	binary.BigEndian.PutUint32(ask[4:], uint32(b.Rate/1000))
	if _, err := c.Write(seal(aead, sealedBulkAsk, ask)); err != nil {
		res.End = "dial"
		return res
	}
	limit := standBulkTimeout
	if b.For > 0 {
		limit = b.For + 30*time.Second
	}
	deadline := start.Add(limit)
	seen := make([]bool, (b.Bytes+udpBulkChunk-1)/udpBulkChunk)
	buf := make([]byte, 2048)
	wait := 5 * time.Second // the first piece; then a second of silence ends it
	for res.Bytes < b.Bytes {
		if ctx.Err() != nil {
			res.End = "timeout"
			break
		}
		until := time.Now().Add(wait)
		if until.After(deadline) {
			until = deadline
		}
		_ = c.SetReadDeadline(until)
		k, err := c.Read(buf)
		if err != nil {
			if !time.Now().Before(deadline) {
				res.End = "timeout"
			} else {
				res.End = "closed" // silence: the rest was lost
			}
			break
		}
		kind, body, ok := unseal(aead, buf[:k])
		if !ok || kind != sealedBulkPart || len(body) < 4 {
			continue
		}
		i := int(binary.BigEndian.Uint32(body))
		if i < len(seen) && !seen[i] {
			seen[i] = true
			res.Bytes += len(body) - 4
			if b.For > 0 {
				res.Slices = addSlice(res.Slices, time.Since(start), len(body)-4)
			}
		}
		wait = time.Second
	}
	res.Took = time.Since(start)
	return res
}
