package main

// Naive keeps the wire protocol in hikkari-protocol/server/naive (the
// forwardproxy Caddy handler with Naive padding, carried into that repo).
// This middleware owns only the same grants and usage totals as keys.go.
import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	"github.com/m1d1x/hikkari-protocol/observe"
	hkserver "github.com/m1d1x/hikkari-protocol/server"
	_ "github.com/m1d1x/hikkari-protocol/server/naive"
	"github.com/m1d1x/hikkari-protocol/trunk"
)

const (
	protoNaive = "naive"
	// protoHikkari is Hikkari v1 (hikkari-protocol/spec/v1.md): the same Caddy
	// stream channel as Naive plus the datagram channel, always both. It has
	// its own EDGE_HIKKARI_* names and no switches: a Hikkari relay is not a
	// Naive relay with a flag.
	protoHikkari = "hikkari"
)

type naiveConfig struct {
	Listen, Host, User, Pass, Root, Storage string
	H3                                      bool
	// UDP is set for EDGE_PROTO=hikkari (hikkari.go): the datagram channel on
	// udp/<port> and UDP-over-TCP inside the tunnel.
	UDP bool
	// Trunk is the cascade trunk's exit on a Hikkari relay (trunk_exit.go);
	// no keys, no trunk.
	Trunk trunkExitConfig
	// network, when set, is the Caddy network the listener comes from:
	// relayNetwork for a Hikkari relay, whose listener outlives a process
	// (handoff.go).
	network string
}

func naiveFromEnv(get func(string) string) (naiveConfig, error) {
	hikkari := get("EDGE_PROTO") == protoHikkari
	prefix := "EDGE_NAIVE_"
	if hikkari {
		prefix = "EDGE_HIKKARI_"
	}
	n := naiveConfig{Listen: get("EDGE_LISTEN"), Host: get(prefix + "HOST"), User: get(prefix + "USER"), Pass: get(prefix + "PASS"), Root: get(prefix + "ROOT"), Storage: get("STATE_DIRECTORY"), UDP: hikkari}
	if n.Storage == "" {
		n.Storage = "/var/lib/nekko-naive"
	}
	if n.Listen == "" {
		n.Listen = "0.0.0.0:443"
	}
	if n.Root == "" {
		n.Root = "/var/www/" + n.Host
	}
	_, port, err := net.SplitHostPort(n.Listen)
	p, e := strconv.Atoi(port)
	if err != nil || e != nil || p < 1 || p > 65535 {
		return n, errors.New("naive: invalid listener")
	}
	if n.Host == "" || net.ParseIP(n.Host) != nil || !strings.Contains(n.Host, ".") || strings.ContainsAny(n.Host, "/\\\r\n\t :") {
		return n, errors.New("naive: invalid domain")
	}
	if n.User == "" || strings.ContainsAny(n.User, ":\r\n") || len(n.Pass) < 16 || strings.ContainsAny(n.Pass, "\r\n") {
		return n, errors.New("naive: invalid probe credential")
	}
	if hikkari {
		if n.Trunk, err = trunkExitFromEnv(get); err != nil {
			return n, err
		}
	}
	if value := get("EDGE_NAIVE_H3"); value != "" && !hikkari {
		n.H3, err = strconv.ParseBool(value)
		if err != nil {
			return n, errors.New("naive: invalid H3 setting")
		}
	}
	return n, nil
}

func (n naiveConfig) document() ([]byte, error) {
	protocols := []string{"h1", "h2"}
	// The datagram channel owns udp/<port>; Caddy's HTTP/3 would take it.
	if n.H3 && !n.UDP {
		protocols = append(protocols, "h3")
	}
	credential := []byte(base64.StdEncoding.EncodeToString([]byte(n.User + ":" + n.Pass)))
	deny := append([]string{}, blockedNets...)
	for _, domain := range blockedDomains {
		domain = strings.TrimPrefix(domain, "domain:")
		deny = append(deny, domain, "*."+domain)
	}
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			if ip, _, err := net.ParseCIDR(address.String()); err == nil {
				deny = append(deny, ip.String())
			}
		}
	}

	listen := n.Listen
	if n.network != "" {
		listen = n.network + "/" + n.Listen
	}
	return json.Marshal(map[string]any{
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"storage": map[string]any{"module": "file_system", "root": n.Storage},
		"apps": map[string]any{
			"tls": map[string]any{"certificates": map[string]any{"automate": []string{n.Host}}},
			"http": map[string]any{"servers": map[string]any{"naive": n.withWrappers(map[string]any{
				"listen": []string{listen}, "protocols": protocols, "tls_connection_policies": []any{map[string]any{}},
				"routes": []any{map[string]any{"handle": []any{
					map[string]any{"handler": "hikkari_naive"},
					map[string]any{"handler": "hikkari_proxy", "hide_ip": true, "hide_via": true, "probe_resistance": map[string]any{}, "auth_credentials": [][]byte{credential}, "acl": []any{map[string]any{"subjects": deny, "allow": false}}},
					map[string]any{"handler": "file_server", "root": n.Root},
				}}},
			})}},
		},
	})
}

// withWrappers puts the v1.04 TCP watch (tcpwatch.go) under TLS on a
// Hikkari relay's server; a Naive relay keeps Caddy's own listener.
func (n naiveConfig) withWrappers(server map[string]any) map[string]any {
	if n.UDP {
		server["listener_wrappers"] = []any{map[string]any{"wrapper": "hikkari_watch"}, map[string]any{"wrapper": "tls"}}
	}
	return server
}

type naiveCount struct{ up, down atomic.Int64 }
type naiveSession struct {
	key    string
	cancel context.CancelFunc
}
type naiveRuntime struct {
	cfg      naiveConfig
	mu       sync.Mutex
	keys     map[string]bool
	totals   map[string]*naiveCount
	sessions map[uint64]naiveSession
	serial   uint64
	closed   bool
	total    naiveCount
	// onKeys, when set, receives every new roster (the datagram channel).
	onKeys func([]string)
	// uotFlows counts UDP associations over the Naive channel.
	uotFlows atomic.Int64
	// dup serves v1.02 duplicated UDP sessions for both channels; set when
	// the relay is Hikkari.
	dup *hkserver.DupHub
	// trunk is the cascade trunk's exit (trunk_exit.go), nil without keys.
	trunk *trunk.Exit
	// book and watches are v1.04 observability (tcpwatch.go), set when the
	// relay is Hikkari: what each key's sessions saw, by path.
	book    *observe.Book
	watches *tcpWatches
	// unsent are periods a pulse did not deliver, merged into the next.
	unsent map[string]*observe.Period
	taken  time.Time
	// pathOf tells the trunk's connections from clients' (tcpwatch.go);
	// tests on loopback replace it.
	pathOf func(remote string) string
	// draining is set once a newer generation took over (handoff.go).
	draining atomic.Bool
}

func newNaiveRuntime(cfg naiveConfig) *naiveRuntime {
	n := &naiveRuntime{cfg: cfg, keys: map[string]bool{}, totals: map[string]*naiveCount{}, sessions: map[uint64]naiveSession{}, pathOf: pathOf}
	if cfg.UDP {
		n.book = observe.NewBook()
		n.watches = newTCPWatches(n.book)
		n.taken = time.Now()
	}
	return n
}

// periods hands out what the book counted since the last pulse, with what
// the last pulses did not deliver, and the seconds they cover.
func (n *naiveRuntime) periods() (map[string]*observe.Period, int64) {
	if n.book == nil {
		return nil, 0
	}
	got := n.book.Take()
	n.mu.Lock()
	defer n.mu.Unlock()
	for group, p := range n.unsent {
		if q := got[group]; q != nil {
			p = observe.Merge(map[string]*observe.Period{"0": p, "1": q})
		}
		got[group] = p
	}
	n.unsent = nil
	seconds := int64(time.Since(n.taken).Seconds())
	return got, seconds
}

// periodsSent says whether a pulse delivered the periods it carried; those it
// did not go with the next one, the time they cover with them.
func (n *naiveRuntime) periodsSent(periods map[string]*observe.Period, ok bool) {
	if n.book == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if ok {
		n.taken = time.Now()
		return
	}
	if len(periods) > 0 {
		n.unsent = periods
	}
}

// Set removes the authority before cancelling streams, atomically with new
// CONNECT admission. Adding a subscriber never restarts another's connection.
func (n *naiveRuntime) Set(ctx context.Context, keys []string) error {
	want := make(map[string]bool, len(keys))
	for _, key := range keys {
		if len(key) < 16 || len(key) > 256 {
			return errors.New("naive: invalid key roster")
		}
		want[key] = true
	}
	n.mu.Lock()
	n.keys = want
	var cancel []context.CancelFunc
	for _, s := range n.sessions {
		if s.key != "" && !want[s.key] {
			cancel = append(cancel, s.cancel)
		}
	}
	onKeys := n.onKeys
	n.mu.Unlock()
	if onKeys != nil {
		onKeys(sortedKeys(want))
	}
	for _, f := range cancel {
		f()
	}
	return nil
}
func (n *naiveRuntime) read() (up, down, conns int64, keys map[string]pulseKey) {
	n.mu.Lock()
	defer n.mu.Unlock()
	keys = make(map[string]pulseKey, len(n.totals))
	for key, c := range n.totals {
		keys[key] = pulseKey{Up: c.up.Load(), Down: c.down.Load()}
	}
	return n.total.up.Load(), n.total.down.Load(), int64(len(n.sessions)), keys
}

func (n *naiveRuntime) Close() {
	n.mu.Lock()
	n.closed = true
	n.keys = nil
	cancel := make([]context.CancelFunc, 0, len(n.sessions))
	for _, s := range n.sessions {
		cancel = append(cancel, s.cancel)
	}
	n.mu.Unlock()
	for _, f := range cancel {
		f()
	}
}

var activeNaive atomic.Pointer[naiveRuntime]

type naiveGate struct{ runtime *naiveRuntime }

func init() { caddy.RegisterModule(naiveGate{}) }
func (naiveGate) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.hikkari_naive", New: func() caddy.Module { return new(naiveGate) }}
}
func (g *naiveGate) Provision(caddy.Context) error {
	g.runtime = activeNaive.Load()
	if g.runtime == nil {
		return errors.New("naive controller not configured")
	}
	return nil
}

func (g *naiveGate) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if r.Method != http.MethodConnect {
		return next.ServeHTTP(w, r)
	}
	fields := strings.Fields(r.Header.Get("Proxy-Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Basic") {
		return next.ServeHTTP(w, r)
	}
	plain, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return next.ServeHTTP(w, r)
	}
	user, pass, ok := strings.Cut(string(plain), ":")
	if !ok {
		return next.ServeHTTP(w, r)
	}
	n := g.runtime
	if user == trunk.User || isTrunkHost(r.Host) {
		n.watches.claim(r.RemoteAddr, trunkGroup)
		return n.serveTrunk(w, r, next, user, pass)
	}
	probe := subtle.ConstantTimeCompare([]byte(user+":"+pass), []byte(n.cfg.User+":"+n.cfg.Pass)) == 1
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return nil
	}
	if !probe && (user != "hikkari" || !n.keys[pass]) {
		n.mu.Unlock()
		return next.ServeHTTP(w, r)
	}
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port == "25" {
		n.mu.Unlock()
		http.Error(w, "destination unavailable", http.StatusForbidden)
		return nil
	}
	if r.ProtoMajor < 2 {
		n.mu.Unlock()
		http.Error(w, "unsupported transport", http.StatusBadRequest)
		return nil
	}
	key := ""
	var counter *naiveCount
	if !probe {
		key = pass
		counter = n.totals[key]
		if counter == nil {
			counter = &naiveCount{}
			n.totals[key] = counter
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	n.serial++
	id := n.serial
	n.sessions[id] = naiveSession{key, cancel}
	hub := n.dup
	n.mu.Unlock()
	defer func() { cancel(); n.mu.Lock(); delete(n.sessions, id); n.mu.Unlock() }()
	path := n.pathOf(r.RemoteAddr)
	group := observe.RelayGroup(path, key)
	if path == observe.PathDirect {
		// A loopback connection is the trunk's: its kernel numbers are the box's.
		n.watches.claim(r.RemoteAddr, group)
	}
	ctx = observe.WithGroup(ctx, group)
	if n.cfg.UDP && hkserver.IsUoTHost(r.Host) {
		ctx = n.withUoT(ctx, r.Host)
	}
	if hub != nil && hkserver.IsDupHost(r.Host) {
		ctx = hkserver.WithDup(ctx, hub, key)
	}
	r = r.Clone(ctx)
	r.Header.Set("Proxy-Authorization", basicAuth(n.cfg.User, n.cfg.Pass))
	r.Body = &naiveBody{ReadCloser: r.Body, total: &n.total, key: counter}
	// Do not return upstream errors to Caddy's request logger: a CONNECT URL is
	// a destination. Only byte counters, never destinations, leave this layer.
	err = next.ServeHTTP(&naiveWriter{ResponseWriter: w, total: &n.total, key: counter}, r)
	n.tunnelEnded(group, key, err)
	return nil
}

// tunnelEnded counts how a key's tunnel ended in its group's period.
func (n *naiveRuntime) tunnelEnded(group, key string, err error) {
	if n.book == nil {
		return
	}
	n.mu.Lock()
	stopping := n.closed
	revoked := key != "" && !n.keys[key]
	n.mu.Unlock()
	reason := tunnelEnd(err, revoked, stopping)
	n.book.Record(group, func(p *observe.Period) {
		if p.Tunnels == nil {
			p.Tunnels = &observe.Ends{}
		}
		p.Tunnels.Add(reason)
	})
}

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

type naiveBody struct {
	io.ReadCloser
	total, key *naiveCount
}

func (b *naiveBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.total.up.Add(int64(n))
	if b.key != nil {
		b.key.up.Add(int64(n))
	}
	return n, err
}

type naiveWriter struct {
	http.ResponseWriter
	total, key *naiveCount
}

func (w *naiveWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.total.down.Add(int64(n))
	if w.key != nil {
		w.key.down.Add(int64(n))
	}
	return n, err
}
func (w *naiveWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *naiveWriter) FlushError() error           { return http.NewResponseController(w.ResponseWriter).Flush() }
func (w *naiveWriter) Flush()                      { _ = w.FlushError() }

func runNaive() error {
	cfg, err := naiveFromEnv(osEnv)
	if err != nil {
		return err
	}
	// A Hikkari relay owns its sockets, so that a rollout hands them to the
	// next generation (handoff.go); a Naive relay leaves its listener to
	// Caddy.
	var socks *sockets
	if cfg.UDP {
		if socks, err = openSockets(cfg.Listen); err != nil {
			return err
		}
		relayListener.Store(&listenerSlot{socks.tcp})
		cfg.network = relayNetwork
	}
	err = serveNaive(cfg, socks)
	if err != nil && socks != nil {
		socks.failed(err)
	}
	return err
}

func serveNaive(cfg naiveConfig, socks *sockets) error {
	runtime := newNaiveRuntime(cfg)
	if !activeNaive.CompareAndSwap(nil, runtime) {
		return errors.New("naive controller already running")
	}
	defer activeNaive.Store(nil)
	setActiveWatches(runtime.watches)
	defer setActiveWatches(nil)
	document, err := cfg.document()
	if err != nil {
		return err
	}
	if documentHook != nil {
		document = documentHook(document)
	}
	if err = caddy.Load(document, true); err != nil {
		return fmt.Errorf("naive Caddy: %w", err)
	}
	stopCaddy := sync.OnceFunc(func() { _ = caddy.Stop() })
	defer stopCaddy()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runtime.startTrunkExit(ctx)
	var udp *hikkariUDP
	if cfg.UDP {
		if udp, err = startHikkariUDP(ctx, runtime, socks); err != nil {
			return err
		}
		defer udp.Close()
	}
	var ctl *controller
	if socks != nil {
		if ctl, err = listenControl(socks, cfg.Listen); err != nil {
			return err
		}
	}
	if pc, ok := pulseFromEnv(osEnv); ok {
		spawnPulse(ctx, runtime, pc, runtime)
	}
	if socks != nil {
		socks.up()
		log.Printf("nekko-edge: generation %d up", socks.gen)
	}
	var handedTo <-chan byte
	if ctl != nil {
		go ctl.serve(ctx)
		handedTo = ctl.handedTo
	}
	select {
	case <-ctx.Done():
	case next := <-handedTo:
		runtime.drain(ctx, next, udp, stopCaddy)
	}
	runtime.Close()
	return nil
}

// drain is this generation's end after a handoff to next: everything new is
// the next one's, what this one carries goes on until it ends or drainLimit.
func (n *naiveRuntime) drain(ctx context.Context, next byte, udp *hikkariUDP, stopCaddy func()) {
	n.draining.Store(true)
	if udp != nil {
		udp.conn.Drain(next)
	}
	n.mu.Lock()
	x := n.trunk
	n.mu.Unlock()
	if x != nil {
		x.Drain()
	}
	go stopCaddy() // no more accepts; HTTP/2 says GOAWAY, open tunnels go on
	started := time.Now()
	limit := time.NewTimer(drainLimit)
	defer limit.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-limit.C:
			n.mu.Lock()
			left := len(n.sessions)
			n.mu.Unlock()
			log.Printf("nekko-edge: drained for %s, %d session(s) left end now", drainLimit, left)
			return
		case <-tick.C:
			n.mu.Lock()
			left := len(n.sessions)
			n.mu.Unlock()
			// Not in the first seconds: systemd hears of the new main
			// process before this one leaves.
			if left == 0 && time.Since(started) >= drainMinimum {
				log.Printf("nekko-edge: drained in %s", time.Since(started).Round(time.Second))
				return
			}
		}
	}
}

// drainMinimum is the least an old generation stays after a handoff.
const drainMinimum = 3 * time.Second

// documentHook, when set, rewrites the Caddy document before it loads: the
// handoff test's processes use it for their certificate and loopback.
var documentHook func([]byte) []byte

// relayNetwork is the Caddy network a Hikkari relay's listener comes from:
// the socket this process owns (bound or inherited, handoff.go), so that
// Caddy closing it closes only this process's descriptor.
const relayNetwork = "hikkari"

type listenerSlot struct{ ln net.Listener }

var relayListener atomic.Pointer[listenerSlot]

func init() { caddy.RegisterNetwork(relayNetwork, relayNetworkListener) }

// relayNetworkListener hands Caddy the relay's own socket for the relay's
// port only. Caddy derives other servers from the relay's address, with its
// network: the automatic HTTP->HTTPS redirects on :80. Those listen on their
// own TCP socket, as they did before the relay owned its listener; given
// the relay's socket they took half of its connections as plain HTTP (unl1
// 10.11 20:02 UTC: no trunk member and no probe got through).
func relayNetworkListener(ctx context.Context, _, host, portRange string, portOffset uint, cfg net.ListenConfig) (any, error) {
	slot := relayListener.Load()
	if slot == nil {
		return nil, errors.New("hikkari: no relay listener")
	}
	if _, port, err := net.SplitHostPort(slot.ln.Addr().String()); err == nil && port == portRange && portOffset == 0 {
		return slot.ln, nil
	}
	na, err := caddy.ParseNetworkAddress("tcp/" + net.JoinHostPort(host, portRange))
	if err != nil {
		return nil, err
	}
	return na.Listen(ctx, portOffset, cfg)
}

var _ caddy.Provisioner = (*naiveGate)(nil)
var _ caddyhttp.MiddlewareHandler = (*naiveGate)(nil)

// drained says a newer generation took over (pulse.go).
func (n *naiveRuntime) drained() bool { return n.draining.Load() }
