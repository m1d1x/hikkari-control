package main

// Naive keeps upstream Caddy/forwardproxy responsible for the wire protocol.
// This middleware owns only the same grants and usage totals as keys.go.
import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/caddyserver/forwardproxy"
)

const protoNaive = "naive"

type naiveConfig struct {
	Listen, Host, User, Pass, Root, Storage string
	H3                                      bool
	// UDP turns the relay into a Hikkari v1.01 relay (hikkari.go): the
	// datagram channel on udp/<port> and UDP-over-TCP inside the tunnel.
	UDP bool
}

func naiveFromEnv(get func(string) string) (naiveConfig, error) {
	n := naiveConfig{Listen: get("EDGE_LISTEN"), Host: get("EDGE_NAIVE_HOST"), User: get("EDGE_NAIVE_USER"), Pass: get("EDGE_NAIVE_PASS"), Root: get("EDGE_NAIVE_ROOT"), Storage: get("STATE_DIRECTORY")}
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
	if value := get("EDGE_NAIVE_H3"); value != "" {
		n.H3, err = strconv.ParseBool(value)
		if err != nil {
			return n, errors.New("naive: invalid H3 setting")
		}
	}
	if value := get("EDGE_HIKKARI_UDP"); value != "" {
		n.UDP, err = strconv.ParseBool(value)
		if err != nil {
			return n, errors.New("naive: invalid Hikkari UDP setting")
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

	return json.Marshal(map[string]any{
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"storage": map[string]any{"module": "file_system", "root": n.Storage},
		"apps": map[string]any{
			"tls": map[string]any{"certificates": map[string]any{"automate": []string{n.Host}}},
			"http": map[string]any{"servers": map[string]any{"naive": map[string]any{
				"listen": []string{n.Listen}, "protocols": protocols, "tls_connection_policies": []any{map[string]any{}},
				"routes": []any{map[string]any{"handle": []any{
					map[string]any{"handler": "hikkari_naive"},
					map[string]any{"handler": "forward_proxy", "hide_ip": true, "hide_via": true, "probe_resistance": map[string]any{}, "auth_credentials": [][]byte{credential}, "acl": []any{map[string]any{"subjects": deny, "allow": false}}},
					map[string]any{"handler": "file_server", "root": n.Root},
				}}},
			}}},
		},
	})
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
	// uotFlows and datagramFlows count UDP associations per Hikkari channel.
	uotFlows, datagramFlows atomic.Int64
}

func newNaiveRuntime(cfg naiveConfig) *naiveRuntime {
	return &naiveRuntime{cfg: cfg, keys: map[string]bool{}, totals: map[string]*naiveCount{}, sessions: map[uint64]naiveSession{}}
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
	n.mu.Unlock()
	defer func() { cancel(); n.mu.Lock(); delete(n.sessions, id); n.mu.Unlock() }()
	if n.cfg.UDP && isUoTHost(r.Host) {
		ctx = n.withUoT(ctx, r.Host)
	}
	r = r.Clone(ctx)
	r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(n.cfg.User+":"+n.cfg.Pass)))
	r.Body = &naiveBody{ReadCloser: r.Body, total: &n.total, key: counter}
	// Do not return upstream errors to Caddy's request logger: a CONNECT URL is
	// a destination. Only byte counters, never destinations, leave this layer.
	_ = next.ServeHTTP(&naiveWriter{ResponseWriter: w, total: &n.total, key: counter}, r)
	return nil
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
	runtime := newNaiveRuntime(cfg)
	if !activeNaive.CompareAndSwap(nil, runtime) {
		return errors.New("naive controller already running")
	}
	defer activeNaive.Store(nil)
	document, err := cfg.document()
	if err != nil {
		return err
	}
	if err = caddy.Load(document, true); err != nil {
		return fmt.Errorf("naive Caddy: %w", err)
	}
	defer caddy.Stop()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if cfg.UDP {
		udp, err := startHikkariUDP(ctx, runtime)
		if err != nil {
			return err
		}
		defer udp.Close()
	}
	if pc, ok := pulseFromEnv(osEnv); ok {
		spawnPulse(ctx, runtime, pc, runtime)
	}
	<-ctx.Done()
	runtime.Close()
	return nil
}

var _ caddy.Provisioner = (*naiveGate)(nil)
var _ caddyhttp.MiddlewareHandler = (*naiveGate)(nil)
