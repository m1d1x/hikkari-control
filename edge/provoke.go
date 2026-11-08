package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A provoke edge (EDGE_PROTO=provoke) is the receiving end of опыт 0 of the
// РФ-каскад (hikkari-client plans/2026-11-05-ru-cascade.md, «Матрица опыта
// 0»): the same box stands in Russia and abroad, a tester's client sends it
// stimuli that ТСПУ is known to cut on the way abroad, and the two answers say
// which attacks are applied to a Russian address too. It is not a relay and
// carries no subscriber: it answers stimuli and remembers, per stimulus, what
// reached it. Comparing «left the client» with «reached the box» says where a
// stimulus died.
//
// Wire (the client side is hikkari-client engine/core/engine/singbox/diagnostics/provoke.go):
//
//   - TCP: TLS with any SNI, a self-signed certificate (TLS 1.3 hides it, the
//     client does not verify). The stimulus is tagged by an ALPN «hk-<16 hex>»
//     in the ClientHello, so the box learns the hello arrived even when nothing
//     after it does. The box answers the tag back as the negotiated protocol.
//     After the handshake the client sends one line:
//     «DL n» the box sends n bytes; «UL n» the client sends n bytes and the box
//     answers «OK got»; «SEEN t1,t2,…» the box answers one JSON line with what
//     it saw of each tag.
//   - UDP, same port: «HKP1» + 8-byte tag + 4-byte sequence + padding. The box
//     counts it under the tag and sends the datagram back unchanged.
//
// Client addresses are not kept. A record holds a tag, an SNI, byte counts and
// a time, and lives provokeTTL.

const protoProvoke = "provoke"

const (
	provokeTTL      = 10 * time.Minute
	provokeMaxRecs  = 4096
	provokeMaxBytes = 4 << 20 // one DL or UL
	provokeMaxConns = 256
	provokeMaxSeen  = 64
	// provokeUDPRate bounds echoes per second over the whole box: an echo is the
	// size of what came in, and a spoofed source must not turn the box into a
	// reflector of note.
	provokeUDPRate = 5000
	provokeIdle    = 15 * time.Second
	provokeALPN    = "hk-"
	provokeMagic   = "HKP1"
)

// ProvokeConfig is everything one provoke edge needs.
type ProvokeConfig struct {
	// Listen is host:port, served over TCP and UDP alike.
	Listen string
}

// ProvokeConfigFromEnv reads EDGE_PROVOKE_LISTEN (default 0.0.0.0:443).
func ProvokeConfigFromEnv(getenv func(string) string) (ProvokeConfig, error) {
	c := ProvokeConfig{Listen: strings.TrimSpace(getenv("EDGE_PROVOKE_LISTEN"))}
	if c.Listen == "" {
		c.Listen = "0.0.0.0:443"
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return c, fmt.Errorf("EDGE_PROVOKE_LISTEN: %q is not host:port", c.Listen)
	}
	if host != "" && net.ParseIP(host) == nil {
		return c, fmt.Errorf("EDGE_PROVOKE_LISTEN: %q is not an address", host)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return c, fmt.Errorf("EDGE_PROVOKE_LISTEN: port %q", port)
	}
	return c, nil
}

// provokeTag is the tag of an ALPN list, "" when the hello carries none.
func provokeTag(protos []string) string {
	for _, p := range protos {
		if t, ok := strings.CutPrefix(p, provokeALPN); ok && validTag(t) {
			return t
		}
	}
	return ""
}

func validTag(t string) bool {
	if len(t) != 16 {
		return false
	}
	_, err := hex.DecodeString(t)
	return err == nil
}

// provokeSeen is what the box saw of one tag.
type provokeSeen struct {
	SNI   string `json:"sni,omitempty"`
	Hello bool   `json:"hello"`
	In    int64  `json:"in"`
	Out   int64  `json:"out"`
	UDP   int64  `json:"udp"`
}

type provokeRecord struct {
	provokeSeen
	at time.Time
}

// provokeBook is the box's memory of stimuli, bounded in count and age.
type provokeBook struct {
	mu    sync.Mutex
	now   func() time.Time
	recs  map[string]*provokeRecord
	order []string // oldest first
	// last minute, for the log
	hellos map[string]int
	conns  int
	udp    int
}

func newProvokeBook() *provokeBook {
	return &provokeBook{now: time.Now, recs: map[string]*provokeRecord{}, hellos: map[string]int{}}
}

// recLocked returns the tag's record, making it and evicting the oldest past
// the cap or the age.
func (b *provokeBook) recLocked(tag string) *provokeRecord {
	now := b.now()
	for len(b.order) > 0 {
		old := b.recs[b.order[0]]
		if old != nil && len(b.order) < provokeMaxRecs && now.Sub(old.at) < provokeTTL {
			break
		}
		delete(b.recs, b.order[0])
		b.order = b.order[1:]
	}
	r := b.recs[tag]
	if r == nil {
		r = &provokeRecord{at: now}
		b.recs[tag] = r
		b.order = append(b.order, tag)
	}
	return r
}

func (b *provokeBook) hello(tag, sni string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hellos[sni]++
	if tag == "" {
		return
	}
	r := b.recLocked(tag)
	r.Hello, r.SNI = true, sni
}

func (b *provokeBook) bytes(tag string, in, out int64) {
	if tag == "" || in == 0 && out == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.recLocked(tag)
	r.In += in
	r.Out += out
}

func (b *provokeBook) datagram(tag string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.udp++
	b.recLocked(tag).UDP++
}

func (b *provokeBook) conn() {
	b.mu.Lock()
	b.conns++
	b.mu.Unlock()
}

func (b *provokeBook) seen(tags []string) map[string]provokeSeen {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]provokeSeen, len(tags))
	for _, t := range tags {
		if r := b.recs[t]; r != nil {
			out[t] = r.provokeSeen
		} else {
			out[t] = provokeSeen{}
		}
	}
	return out
}

// minute is the last minute's totals for the log, and resets them.
func (b *provokeBook) minute() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conns == 0 && b.udp == 0 && len(b.hellos) == 0 {
		return ""
	}
	snis := make([]string, 0, len(b.hellos))
	for s, n := range b.hellos {
		name := s
		if name == "" {
			name = "-"
		}
		snis = append(snis, fmt.Sprintf("%s=%d", name, n))
	}
	sort.Strings(snis)
	line := fmt.Sprintf("provoke: 1m conns=%d udp=%d hellos[%s]", b.conns, b.udp, strings.Join(snis, " "))
	b.conns, b.udp, b.hellos = 0, 0, map[string]int{}
	return line
}

// provokeServer is one running provoke edge.
type provokeServer struct {
	book  *provokeBook
	tls   *tls.Config
	slots chan struct{}
	fill  []byte
	tcp   net.Listener
	udp   *net.UDPConn
}

func newProvokeServer() (*provokeServer, error) {
	cert, err := provokeCert()
	if err != nil {
		return nil, err
	}
	s := &provokeServer{book: newProvokeBook(), slots: make(chan struct{}, provokeMaxConns), fill: make([]byte, 64<<10)}
	_, _ = rand.Read(s.fill)
	base := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.tls = &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(hi *tls.ClientHelloInfo) (*tls.Config, error) {
			tag := provokeTag(hi.SupportedProtos)
			s.book.hello(tag, hi.ServerName)
			c := base.Clone()
			if tag != "" {
				c.NextProtos = []string{provokeALPN + tag}
			}
			return c, nil
		},
	}
	return s, nil
}

// provokeCert is a fresh self-signed P-256 certificate. Its name does not
// matter: TLS 1.3 encrypts it and the client does not check it.
func provokeCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// start listens on addr over TCP and UDP. With port 0 the UDP socket takes the
// port TCP got.
func (s *provokeServer) start(ctx context.Context, addr string) error {
	tl, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("provoke tcp: %w", err)
	}
	ua, err := net.ResolveUDPAddr("udp", tl.Addr().String())
	if err != nil {
		tl.Close()
		return err
	}
	ul, err := net.ListenUDP("udp", ua)
	if err != nil {
		tl.Close()
		return fmt.Errorf("provoke udp: %w", err)
	}
	s.tcp, s.udp = tl, ul
	go func() { <-ctx.Done(); tl.Close(); ul.Close() }()
	go s.serveTCP(ctx)
	go s.serveUDP(ctx)
	return nil
}

func (s *provokeServer) serveTCP(ctx context.Context) {
	for {
		c, err := s.tcp.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("nekko-edge: provoke accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			c.Close() // full: a stimulus that finds the box busy reads as a dead flow, so say it in the log
			log.Printf("nekko-edge: provoke: %d connections already, one dropped", provokeMaxConns)
			continue
		}
		s.book.conn()
		go func() {
			defer func() { <-s.slots }()
			s.handle(c)
		}()
	}
}

// handle serves one TLS stimulus: the handshake, then one command.
func (s *provokeServer) handle(raw net.Conn) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(provokeIdle))
	c := tls.Server(raw, s.tls)
	if err := c.Handshake(); err != nil {
		return
	}
	tag, _ := strings.CutPrefix(c.ConnectionState().NegotiatedProtocol, provokeALPN)
	r := bufio.NewReaderSize(c, 4096)
	line, err := readLine(r)
	if err != nil {
		return
	}
	verb, arg, _ := strings.Cut(line, " ")
	switch verb {
	case "DL":
		n, ok := provokeSize(arg)
		if !ok {
			return
		}
		s.download(c, raw, tag, n)
	case "UL":
		n, ok := provokeSize(arg)
		if !ok {
			return
		}
		got := s.upload(r, raw, tag, n)
		_ = raw.SetDeadline(time.Now().Add(provokeIdle))
		_, _ = fmt.Fprintf(c, "OK %d\n", got)
	case "SEEN":
		var tags []string
		for _, t := range strings.Split(arg, ",") {
			if validTag(t) && len(tags) < provokeMaxSeen {
				tags = append(tags, t)
			}
		}
		out, _ := json.Marshal(s.book.seen(tags))
		_, _ = c.Write(append(out, '\n'))
	}
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(line)), nil
}

func provokeSize(arg string) (int64, bool) {
	n, err := strconv.ParseInt(arg, 10, 64)
	return n, err == nil && n > 0 && n <= provokeMaxBytes
}

// download sends n bytes, counting into the tag as they leave, so a flow that
// stalls part way shows how far it got. Each write extends the idle deadline:
// a stalled flow ends provokeIdle after the last byte that left.
func (s *provokeServer) download(c *tls.Conn, raw net.Conn, tag string, n int64) {
	for n > 0 {
		chunk := s.fill
		if int64(len(chunk)) > n {
			chunk = chunk[:n]
		}
		_ = raw.SetDeadline(time.Now().Add(provokeIdle))
		w, err := c.Write(chunk)
		s.book.bytes(tag, 0, int64(w))
		if err != nil {
			return
		}
		n -= int64(w)
	}
}

func (s *provokeServer) upload(r io.Reader, raw net.Conn, tag string, n int64) int64 {
	buf := make([]byte, 32<<10)
	var got int64
	for got < n {
		_ = raw.SetDeadline(time.Now().Add(provokeIdle))
		want := int64(len(buf))
		if n-got < want {
			want = n - got
		}
		k, err := r.Read(buf[:want])
		got += int64(k)
		s.book.bytes(tag, int64(k), 0)
		if err != nil {
			break
		}
	}
	return got
}

func (s *provokeServer) serveUDP(ctx context.Context) {
	buf := make([]byte, 2048)
	budget, window := 0, time.Now()
	for {
		n, from, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n < len(provokeMagic)+12 || string(buf[:len(provokeMagic)]) != provokeMagic {
			continue
		}
		s.book.datagram(hex.EncodeToString(buf[len(provokeMagic) : len(provokeMagic)+8]))
		if now := time.Now(); now.Sub(window) >= time.Second {
			budget, window = 0, now
		}
		if budget >= provokeUDPRate {
			continue
		}
		budget++
		_, _ = s.udp.WriteToUDP(buf[:n], from)
	}
}

// logMinutes writes the last minute's totals once a minute while there are any.
func (s *provokeServer) logMinutes(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if line := s.book.minute(); line != "" {
				log.Print("nekko-edge: " + line)
			}
		}
	}
}

// provoke runs a provoke edge until SIGTERM: no Xray, no pulse, no keys.
func provoke() error {
	c, err := ProvokeConfigFromEnv(osEnv)
	if err != nil {
		return err
	}
	s, err := newProvokeServer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.start(ctx, c.Listen); err != nil {
		return err
	}
	go s.logMinutes(ctx)
	log.Printf("nekko-edge: provoke up on %s tcp+udp", c.Listen)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	return nil
}
