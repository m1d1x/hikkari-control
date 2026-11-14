package main

// The Rust exit (hk-exit, hikkari-protocol rust/exit) under this relay
// (roadmap 4.6, R6.4.3; hikkari-client
// plans/2026-11-11-entry-hot-path-and-unit-economics.md, «Труба на Rust»;
// owner's decision of 2026-11-14): every wire of an entry that speaks to it
// ends in one process, so a stream or flow moving from wire to wire is never
// answered by two. This process is its brain: the keys, the ports open to
// the trunk, the testers' marks, the generations.
//
//	EDGE_HIKKARI_TRUNK_PIPE_KEYS=<trunk key>[,…]
//	EDGE_HIKKARI_TRUNK_PIPE_WIRES=udp=20461,h3dgram=20460
//	EDGE_HIKKARI_TRUNK_PIPE_BIN=/usr/local/lib/nekko-edge/hk-exit   (default: hk-exit beside nekko-edge)
//
// The members of an entry come to hk-exit by its trunk key: a CONNECT with a
// pipe key on the relay's own TLS is a pool member, handed to hk-exit as one
// end of a socket pair (the relay's TLS side copied to the other end); the
// wires' UDP sockets are hk-exit's. A key of EDGE_HIKKARI_TRUNK_KEYS stays
// with the Go exit (trunk_exit.go) and its wires (trunk_wires.go): one entry
// never reaches both.
//
// Generations (handoff.go): the next generation inherits the wires' sockets
// and one end of a handoff socket, whose other end this one's hk-exit
// drains to once the next one is up (pipectl Exit.Drain, Adopt).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// Kinds of wire hk-exit serves, and their ids: the entry's (trunkentry
// wires.go wireIDs).
const (
	pipeWireUDP     = "udp"
	pipeWireH3Dgram = "h3dgram"
)

var pipeWireIDs = map[string]trunk.WireID{pipeWireUDP: 3, pipeWireH3Dgram: 6}

// envPipeInherit names what a generation inherits for hk-exit: each wire's
// socket by kind and the new end of the handoff socket, as kind=fd pairs.
const envPipeInherit = "NEKKO_EDGE_PIPE_INHERIT"

// trunkPipeConfig is EDGE_HIKKARI_TRUNK_PIPE_*. No keys: no hk-exit.
type trunkPipeConfig struct {
	Keys  []string
	Wires map[string]int
	Bin   string
}

func trunkPipeFromEnv(get func(string) string) (trunkPipeConfig, error) {
	var c trunkPipeConfig
	raw := strings.TrimSpace(get("EDGE_HIKKARI_TRUNK_PIPE_KEYS"))
	if raw == "" {
		if get("EDGE_HIKKARI_TRUNK_PIPE_WIRES") != "" {
			return c, errors.New("EDGE_HIKKARI_TRUNK_PIPE_WIRES without EDGE_HIKKARI_TRUNK_PIPE_KEYS")
		}
		return c, nil
	}
	for _, key := range strings.Split(raw, ",") {
		if err := trunk.ParseKey(key); err != nil {
			return c, fmt.Errorf("EDGE_HIKKARI_TRUNK_PIPE_KEYS: %w", err)
		}
		c.Keys = append(c.Keys, key)
	}
	c.Wires = map[string]int{}
	ports := map[int]bool{}
	for _, item := range strings.Split(strings.TrimSpace(get("EDGE_HIKKARI_TRUNK_PIPE_WIRES")), ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		kind, p, ok := strings.Cut(item, "=")
		port, err := strconv.Atoi(p)
		if _, known := pipeWireIDs[kind]; !ok || err != nil || !known || port < 1 || port > 65535 {
			return c, fmt.Errorf("EDGE_HIKKARI_TRUNK_PIPE_WIRES: %q is not udp=port or h3dgram=port", item)
		}
		if _, dup := c.Wires[kind]; dup || ports[port] {
			return c, fmt.Errorf("EDGE_HIKKARI_TRUNK_PIPE_WIRES: %q twice", item)
		}
		c.Wires[kind], ports[port] = port, true
	}
	c.Bin = strings.TrimSpace(get("EDGE_HIKKARI_TRUNK_PIPE_BIN"))
	if c.Bin == "" {
		self, err := os.Executable()
		if err != nil {
			return c, err
		}
		c.Bin = filepath.Join(filepath.Dir(self), "hk-exit")
	}
	return c, nil
}

func (c trunkPipeConfig) admits(key string) bool { return trunkExitConfig{Keys: c.Keys}.admits(key) }

// trunkPipe is hk-exit under this process.
type trunkPipe struct {
	x       *pipectl.Exit
	cfg     trunkPipeConfig
	gen     byte
	targets *trunkTargets

	mu sync.Mutex
	// wires are the wires' sockets by kind, which the next generation
	// inherits; sent, those hk-exit has.
	wires map[string]*net.UDPConn
	sent  map[string]bool
	// handoff is the old end of the socket of a handoff under way: this
	// hk-exit drains to it once the next generation is up.
	handoff *os.File
	// alone is set when this generation drained with no hk-exit to drain
	// to.
	alone bool
	// told are the targets hk-exit told, by its connection's network and
	// local address.
	told map[string]*trunkTarget
}

// startTrunkPipe runs hk-exit for cfg under this generation: the wires'
// sockets inherited from the previous generation (with the handoff's new
// end, which it adopts) or bound here, the ports open to the trunk, the
// testers' reports into the pulse.
func (n *naiveRuntime) startTrunkPipe(ctx context.Context, gen byte) error {
	cfg := n.cfg.Pipe
	if len(cfg.Keys) == 0 {
		return nil
	}
	n.mu.Lock()
	if n.targets == nil {
		n.targets = newTrunkTargets()
	}
	targets := n.targets
	n.mu.Unlock()
	p := &trunkPipe{cfg: cfg, gen: gen, targets: targets, wires: map[string]*net.UDPConn{}, sent: map[string]bool{}, told: map[string]*trunkTarget{}}
	inherited, adopt, err := pipeInherited(os.Getenv(envPipeInherit))
	if err != nil {
		return err
	}
	_ = os.Unsetenv(envPipeInherit)
	x, err := pipectl.StartExit(pipectl.ExitConfig{Bin: cfg.Bin, Stderr: os.Stderr,
		Logf:    func(format string, a ...any) { log.Printf("nekko-edge: "+format, a...) },
		Local:   p.local,
		Observe: n.trunkObserved})
	if err != nil {
		closeFiles(inherited, adopt)
		return fmt.Errorf("hk-exit: %w", err)
	}
	p.x = x
	fail := func(err error) error {
		closeFiles(inherited, adopt)
		x.Close()
		return err
	}
	if err := x.SetGeneration(gen); err != nil {
		return fail(err)
	}
	if adopt != nil {
		if err := x.Adopt(adopt); err != nil {
			return fail(err)
		}
		adopt.Close()
		adopt = nil
	}
	for kind, port := range cfg.Wires {
		conn, err := pipeWireSocket(inherited[kind], port)
		delete(inherited, kind)
		if err != nil {
			return fail(fmt.Errorf("hk-exit wire %s on udp/%d: %w", kind, port, err))
		}
		p.wires[kind] = conn
	}
	closeFiles(inherited, nil)
	if err := x.SetPool(nil, 0, 0); err != nil {
		return fail(err)
	}
	ports := make([]uint16, 0, len(n.cfg.Trunk.Ports))
	for port := range n.cfg.Trunk.Ports {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	if err := x.SetPorts(ports); err != nil {
		return fail(err)
	}
	if conn := p.wires[pipeWireUDP]; conn != nil {
		if err := x.SetWire(pipectl.ExitWire{ID: pipeWireIDs[pipeWireUDP], Conn: conn, Keys: cfg.Keys}); err != nil {
			return fail(err)
		}
		p.sent[pipeWireUDP] = true
	}
	n.mu.Lock()
	n.pipe = p
	n.mu.Unlock()
	if p.wires[pipeWireH3Dgram] != nil {
		go p.giveH3Dgram(ctx, n.cfg.Storage, n.cfg.Host)
	}
	go p.minutes(ctx)
	go func() {
		select {
		case <-ctx.Done():
		case <-x.Done():
			if ctx.Err() == nil {
				log.Printf("nekko-edge: hk-exit ended under the relay; the pipe's wires carry nothing until the relay restarts")
			}
		}
	}()
	log.Printf("nekko-edge: hk-exit generation %d on, %d key(s), wires %v, ports %v", gen, len(cfg.Keys), cfg.Wires, ports)
	return nil
}

// pipeWireSocket is a wire's socket: the one inherited, or bound here.
func pipeWireSocket(f *os.File, port int) (*net.UDPConn, error) {
	if f != nil {
		pc, err := net.FilePacketConn(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("inherited: %w", err)
		}
		return pc.(*net.UDPConn), nil
	}
	return net.ListenUDP("udp4", &net.UDPAddr{Port: port})
}

// giveH3Dgram gives hk-exit Chromium's QUIC wire once the relay's ACME
// certificate for its domain is in Caddy's storage (the trunk's SNI is the
// relay's domain; the first start of a relay waits for its certificate).
func (p *trunkPipe) giveH3Dgram(ctx context.Context, storage, host string) {
	for {
		cert, key := relayCertificate(storage, host)
		if cert != "" {
			w := pipectl.ExitWire{ID: pipeWireIDs[pipeWireH3Dgram], Form: pipectl.FormH3Dgram, Conn: p.wires[pipeWireH3Dgram],
				Keys: p.cfg.Keys, CertFile: cert, KeyFile: key}
			if err := p.x.SetWire(w); err != nil {
				log.Printf("nekko-edge: hk-exit wire h3dgram: %v", err)
				return
			}
			p.mu.Lock()
			p.sent[pipeWireH3Dgram] = true
			p.mu.Unlock()
			log.Printf("nekko-edge: hk-exit wire h3dgram on udp/%d, certificate %s", p.cfg.Wires[pipeWireH3Dgram], cert)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// relayCertificate is the newest certificate and key Caddy keeps for host
// (hikkari-protocol server/certs.go CaddyCertificate reads the same).
func relayCertificate(storage, host string) (cert, key string) {
	matches, _ := filepath.Glob(filepath.Join(storage, "certificates", "*", host, host+".crt"))
	var newest time.Time
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.ModTime().After(newest) {
			k := strings.TrimSuffix(m, ".crt") + ".key"
			if _, err := os.Stat(k); err == nil {
				cert, key, newest = m, k, st.ModTime()
			}
		}
	}
	return cert, key
}

// local keeps the trunk's targets as hk-exit tells its connections to the
// relay: the relay sees a connection's local port on loopback as its peer.
func (p *trunkPipe) local(l pipectl.ExitLocal) {
	network := "tcp"
	if l.Kind == trunk.ObsFlow {
		network = "udp"
	}
	remote := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(l.Port)))
	key := network + " " + remote
	p.mu.Lock()
	defer p.mu.Unlock()
	if !l.Open {
		if t := p.told[key]; t != nil {
			p.targets.forget(key, t)
			delete(p.told, key)
		}
		return
	}
	kind, session, stream, x := l.Kind, l.Session, l.Stream, p.x
	t := &trunkTarget{mark: func(tag string) {
		if _, err := x.Mark(kind, session, stream, tag); err != nil {
			log.Printf("nekko-edge: hk-exit mark: %v", err)
		}
	}}
	p.told[key] = t
	p.targets.told(network, remote, t)
}

// serveMember hands a pool member's CONNECT to hk-exit: one end of a socket
// pair goes to it, the other is what the relay's tunnel copies to and from.
func (p *trunkPipe) serveMember() (net.Conn, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("hk-exit member: %w", err)
	}
	exitSide, relaySide := os.NewFile(uintptr(fds[0]), "member-exit"), os.NewFile(uintptr(fds[1]), "member-relay")
	defer exitSide.Close()
	defer relaySide.Close()
	if err := p.x.Link(exitSide); err != nil {
		return nil, err
	}
	return net.FileConn(relaySide)
}

// minutes logs hk-exit's counters every minute while it has sessions, in
// the Go exit's words (trunk_exit.go).
func (p *trunkPipe) minutes(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.x.Done():
			return
		case <-t.C:
			tk, at := p.x.Tick()
			if tk.Sessions == 0 || time.Since(at) > 5*time.Second {
				continue
			}
			log.Printf("nekko-edge: trunk exit pipe: sessions=%d members=%d stalled=%d broken=%d resent=%d streams=%d/%d reset=%d up=%d down=%d flows=%d dgrams=%d/%d copies=%d pauses=%d late_max_ms=%d",
				tk.Sessions, tk.Members+tk.Links, tk.Silent+tk.Quiet, tk.Gone, tk.Resent, tk.Streams, tk.StreamsOpened, tk.ResetPeer+tk.ResetLocal,
				tk.Up, tk.Down, tk.Flows, tk.DgramsOut, tk.DgramsIn, tk.DgramsCopy, tk.Pauses, tk.LateMax.Milliseconds())
		}
	}
}

// forChild is what the next generation inherits for hk-exit: each wire's
// socket and the new end of a fresh handoff socket, whose old end this
// hk-exit drains to once the child is up (drain). The env names them by
// their place among ExtraFiles from first.
func (p *trunkPipe) forChild(first int) (files []*os.File, env string, err error) {
	old, nw, err := pipectl.HandoffLink()
	if err != nil {
		return nil, "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var pairs []string
	kinds := make([]string, 0, len(p.wires))
	for kind := range p.wires {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		f, err := p.wires[kind].File()
		if err != nil {
			closeFiles(nil, old)
			closeFiles(nil, nw)
			for _, f := range files {
				f.Close()
			}
			return nil, "", err
		}
		pairs = append(pairs, fmt.Sprintf("%s=%d", kind, first+len(files)))
		files = append(files, f)
	}
	pairs = append(pairs, fmt.Sprintf("handoff=%d", first+len(files)))
	files = append(files, nw)
	if p.handoff != nil {
		p.handoff.Close()
	}
	p.handoff = old
	return files, envPipeInherit + "=" + strings.Join(pairs, ","), nil
}

// childFailed forgets the handoff of a child that did not come up: this
// hk-exit goes on as if nothing happened.
func (p *trunkPipe) childFailed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handoff != nil {
		p.handoff.Close()
		p.handoff = nil
	}
}

// drain makes this hk-exit the old one: it drains to the next generation's
// at the other end of the handoff socket.
func (p *trunkPipe) drain() {
	p.mu.Lock()
	h := p.handoff
	p.handoff = nil
	p.alone = h == nil
	p.mu.Unlock()
	if h == nil {
		// The next generation has no hk-exit: this one carries its members
		// as the relay's tunnels, which the drain waits for, and its flows
		// end with this process.
		log.Printf("nekko-edge: hk-exit generation %d: the next generation has none, nothing to drain to", p.gen)
		return
	}
	defer h.Close()
	if err := p.x.Drain(h); err != nil {
		log.Printf("nekko-edge: hk-exit drain: %v", err)
		return
	}
	log.Printf("nekko-edge: hk-exit generation %d drains", p.gen)
}

// drained says whether this hk-exit, draining, carries nothing more (or is
// gone).
func (p *trunkPipe) drained() bool {
	p.mu.Lock()
	alone := p.alone
	p.mu.Unlock()
	if alone {
		return true
	}
	select {
	case <-p.x.Drained():
		return true
	case <-p.x.Done():
		return true
	default:
		return false
	}
}

// pipeInherited reads envPipeInherit: the wires' sockets by kind and the
// handoff's new end.
func pipeInherited(spec string) (wires map[string]*os.File, handoff *os.File, err error) {
	wires = map[string]*os.File{}
	if spec == "" {
		return wires, nil, nil
	}
	for _, item := range strings.Split(spec, ",") {
		kind, v, ok := strings.Cut(item, "=")
		fd, err := strconv.ParseUint(v, 10, 31)
		if !ok || err != nil {
			closeFiles(wires, handoff)
			return nil, nil, fmt.Errorf("%s=%q", envPipeInherit, spec)
		}
		f := os.NewFile(uintptr(fd), kind)
		if kind == "handoff" {
			handoff = f
		} else {
			wires[kind] = f
		}
	}
	return wires, handoff, nil
}

func closeFiles(m map[string]*os.File, f *os.File) {
	for _, x := range m {
		x.Close()
	}
	if f != nil {
		f.Close()
	}
}

// closeTrunkPipe ends the runtime's hk-exit, if it has one.
func (n *naiveRuntime) closeTrunkPipe() {
	n.mu.Lock()
	p := n.pipe
	n.pipe = nil
	n.mu.Unlock()
	if p != nil {
		p.close()
	}
}

// close ends hk-exit and closes its wires' sockets in this process.
func (p *trunkPipe) close() {
	p.x.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.wires {
		c.Close()
	}
	if p.handoff != nil {
		p.handoff.Close()
		p.handoff = nil
	}
}
