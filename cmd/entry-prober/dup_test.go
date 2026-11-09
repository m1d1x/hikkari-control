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
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/dup"
	"github.com/m1d1x/hikkari-protocol/server"
	M "github.com/sagernet/sing/common/metadata"
)

const dupTestKey = "subscriber-key-0123456789abcdef"

// testCert is a self-signed certificate for host and a pool that trusts it.
func testCert(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// openExit lets the session reach loopback, which a relay's policy refuses.
type openExit struct{}

func (openExit) Resolve(ctx context.Context, dest M.Socksaddr) (netip.AddrPort, error) {
	return dest.AddrPort(), nil
}
func (openExit) DialTCP(ctx context.Context, dest M.Socksaddr) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", dest.String())
}

type oneKey struct{}

func (oneKey) Admit(ctx context.Context, key string) (context.Context, func(), func(up, down int), bool) {
	if key != dupTestKey {
		return nil, nil, nil, false
	}
	ctx, cancel := context.WithCancel(ctx)
	return ctx, cancel, func(int, int) {}, true
}

// connectFront stands for the Naive core and the relay's Naive front: an
// HTTP CONNECT to dup.hikkari.arpa:0 is handed to the relay's dup hub, as
// edge/naive.go does after the tunnel. refuse answers 502 instead.
func connectFront(t *testing.T, hub *server.DupHub, refuse bool) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				r := bufio.NewReader(c)
				req, err := http.ReadRequest(r)
				if err != nil || req.Method != http.MethodConnect || !dup.IsHost(strings.TrimSuffix(req.Host, ":0")) || refuse {
					_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
					c.Close()
					return
				}
				_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
				_ = hub.ServeStream(context.Background(), dupTestKey, &bufferedConn{Conn: c, r: r})
			}()
		}
	}()
	return l.Addr().String()
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// sourceSTUN answers binding requests with XOR-MAPPED-ADDRESS of the source,
// and counts the sources it saw.
type sourceSTUN struct {
	*net.UDPConn
	mu      sync.Mutex
	sources map[string]int
}

func newSourceSTUN(t *testing.T) *sourceSTUN {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s := &sourceSTUN{UDPConn: c, sources: map[string]int{}}
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := c.ReadFromUDP(b)
			if err != nil {
				return
			}
			if n < 20 {
				continue
			}
			s.mu.Lock()
			s.sources[from.String()]++
			s.mu.Unlock()
			_, _ = c.WriteToUDP(stunAnswer(b[:20], from.AddrPort()), from)
		}
	}()
	return s
}

func (s *sourceSTUN) seen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sources)
}

// stunAnswer is a binding success for req's transaction with the source as
// XOR-MAPPED-ADDRESS.
func stunAnswer(req []byte, from netip.AddrPort) []byte {
	resp := make([]byte, 32)
	binary.BigEndian.PutUint16(resp, 0x0101)
	binary.BigEndian.PutUint16(resp[2:], 12)
	copy(resp[4:20], req[4:20])
	binary.BigEndian.PutUint16(resp[20:], 0x0020)
	binary.BigEndian.PutUint16(resp[22:], 8)
	resp[25] = 0x01
	binary.BigEndian.PutUint16(resp[26:], from.Port()^uint16(stunMagic>>16))
	ip := from.Addr().As4()
	binary.BigEndian.PutUint32(resp[28:], binary.BigEndian.Uint32(ip[:])^stunMagic)
	return resp
}

// dupRelay is a relay with a dup hub on both channels, on loopback.
func dupRelay(t *testing.T, refuseStream bool) (dupSetup, *server.DupHub) {
	t.Helper()
	cert, pool := testCert(t, "hk.example")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub := server.NewDupHub(openExit{}, 0)
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	listen := c.LocalAddr().String()
	c.Close()
	d, err := server.ListenDatagram(ctx, server.DatagramOptions{
		Listen:      listen,
		Certificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil },
		Admitter:    oneKey{},
		Exit:        openExit{},
		Dup:         hub,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.SetKeys([]string{dupTestKey})
	return dupSetup{
		server: M.ParseSocksaddr(listen), host: "hk.example", key: dupTestKey, roots: pool,
		proxy: connectFront(t, hub, refuseStream), packets: 150, interval: voiceInterval,
	}, hub
}

func TestStunMapped(t *testing.T) {
	from := netip.MustParseAddrPort("203.0.113.7:40000")
	a, ok := stunMapped(stunAnswer(make([]byte, 20), from))
	if !ok || a != from {
		t.Fatalf("%v %v", a, ok)
	}
	if _, ok := stunMapped(stunRequest(1, [8]byte{})); ok {
		t.Fatal("a request read as an answer")
	}
}

// The session as a client with dup=1 runs it: the relay answers the attach,
// the call comes back, the far end sees one source, and the stream copy
// carries answers once the call is seen.
func TestDupProbeSession(t *testing.T) {
	s, hub := dupRelay(t, false)
	stun := newSourceSTUN(t)
	s.target = stun.LocalAddr().(*net.UDPAddr).AddrPort()
	p := probeDup(context.Background(), s, time.Second)
	t.Logf("%s, median %.1f ms", p.detail(), p.medianMs)
	if !p.ok() || p.sent != 150 || p.got < 147 || !p.lanes || p.sources != 1 || p.stream < 75 || p.voice == 0 {
		t.Fatalf("live hub: %+v", p)
	}
	if stun.seen() != 1 {
		t.Fatalf("far end saw %d sources", stun.seen())
	}
	if streams, datagrams := hub.Carriers(); streams == 0 || datagrams < 2 {
		t.Fatalf("hub carriers: stream %d, datagram %d", streams, datagrams)
	}
	r := withDup(probeResult{Verdict: verdictClean, Detail: "naive; datagram 100/100"}, p)
	if r.Verdict != verdictClean || !strings.HasPrefix(r.Detail, "naive; datagram 100/100; dup ") ||
		r.DupOK != 1 || r.DupLanes != 1 || r.DupSources != 1 || int(r.DupGot) != p.got || int(r.DupNaive) != p.stream {
		t.Fatalf("row: %+v", r)
	}
}

// A relay whose Naive front does not take the copy: the call still comes
// back by the datagram channel, and the session says the copy never came.
func TestDupProbeStreamRefused(t *testing.T) {
	s, _ := dupRelay(t, true)
	s.target = newSourceSTUN(t).LocalAddr().(*net.UDPAddr).AddrPort()
	p := probeDup(context.Background(), s, time.Second)
	t.Logf("%s", p.detail())
	if p.ok() || p.stream != 0 || p.got < 147 || !p.lanes {
		t.Fatalf("stream refused: %+v", p)
	}
}

// No relay at all: nothing sent counts as answered, and the row is not ok.
func TestDupProbeDeadRelay(t *testing.T) {
	s, _ := dupRelay(t, false)
	s.key = "wrong-key"
	s.target = newSourceSTUN(t).LocalAddr().(*net.UDPAddr).AddrPort()
	start := time.Now()
	p := probeDup(context.Background(), s, time.Second)
	if p.ok() || p.got != 0 || time.Since(start) > 15*time.Second {
		t.Fatalf("dead relay: %+v after %v", p, time.Since(start))
	}
	if r := withDup(probeResult{Detail: "naive"}, p); r.DupOK != 0 || !strings.Contains(r.Detail, "; dup 0/") {
		t.Fatalf("row: %+v", r)
	}
}

// Live: HIKKARI_TEST_CORE (a client core with Naive) and HIKKARI_TEST_RECORD
// (`hub entry-box record <relay>` of a Hikkari relay), as TestDatagramDoorLive.
func TestDupSessionLive(t *testing.T) {
	core, recordPath := os.Getenv("HIKKARI_TEST_CORE"), os.Getenv("HIKKARI_TEST_RECORD")
	if core == "" || recordPath == "" {
		t.Skip("HIKKARI_TEST_CORE and HIKKARI_TEST_RECORD not set")
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var env struct{ Payload string }
	_ = json.Unmarshal(raw, &env)
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	var rec struct {
		Address, Host, User, Pass, Name string
		Port                            int
	}
	if err := json.Unmarshal(payload, &rec); err != nil {
		t.Fatal(err)
	}
	e := edge{Name: rec.Name, Proto: protoHikkari, Address: rec.Address, Port: rec.Port, Naive: &naiveEdge{Host: rec.Host, User: rec.User, Pass: rec.Pass}}
	r := probeDupEdge(context.Background(), core, e, probeResult{Verdict: verdictClean, Detail: "naive; datagram 100/100"}, 3*time.Second)
	t.Logf("%s", r.Detail)
	if r.DupOK != 1 {
		t.Fatalf("live dup session: %+v", r)
	}
}
