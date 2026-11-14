package main

// handoff.go: the trunk entry is replaced without cutting what it carries
// (roadmap 2.2б; hikkari-client plans/2026-11-10-edge-hitless-rollout.md,
// the same scheme as a Hikkari relay's, edge/handoff.go).
//
// The processes of the entry are generations of one systemd unit. A rollout
// is `systemctl reload nekko-trunk-entry`, whose ExecReload runs
// `nekko-trunk-entry reload` as root: it reads the entry's env and hands it to
// the running process over the control socket. That process starts the
// binary now on disk as the next generation and gives it its listening
// sockets (the same sockets, not bound again). The child opens a pool of its
// own, a new session at the exit, and says when it has members. Then:
//
//   - TCP: the old generation stops accepting; the connections it carries go
//     on over its own session, every new one is the child's;
//   - UDP: the old generation stops reading the shared sockets and tells the
//     child which client addresses its flows are for. The child forwards
//     their datagrams to the old one's mailbox (a unixgram socket in the
//     runtime directory); a flow the old one ended, or no old one left, and
//     the child takes the address itself. Answers go out of the shared socket
//     from whichever process holds the flow;
//   - the minute line and the trunk's call are the newest generation's.
//
// The old process leaves when its sessions carry nothing, or after
// drainLimit. A child that does not come up in time is killed, and the old
// process goes on as before: the reload fails, nothing was cut.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

// handoffVersion is what `nekko-trunk-entry handoff-version` prints: the
// generation contract (env names, descriptors, control socket, mailboxes)
// this build speaks. The hub reloads only into a build that prints it.
const handoffVersion = 1

const (
	// envInherit lists the inherited sockets in descriptor order from 3:
	// "t8053,t8443,u8443", then the readiness pipe and the owners pipe.
	envInherit    = "NEKKO_TRUNK_INHERIT"
	envGeneration = "NEKKO_TRUNK_GEN"
	controlName   = "control.sock"
	// readerName is the mailbox of the generation that reads the UDP
	// sockets now: older ones say there which addresses they let go.
	readerName = "reader.sock"
	// runtimeDir is the unit's RuntimeDirectory.
	runtimeDir = "/run/" + "nekko-trunk-entry"
)

var (
	// drainLimit is how long an old generation keeps what it carries; the
	// exit's old process and a retired session keep theirs as long.
	drainLimit = 30 * time.Minute
	// childUpWithin bounds how long a new generation may take to have
	// members and say so.
	childUpWithin = 30 * time.Second
	drainEvery    = 2 * time.Second
)

// sockets are the entry's listening sockets and its generation.
type sockets struct {
	tcp map[int]*net.TCPListener
	udp map[int]*net.UDPConn
	gen int
	// ready and owners, for an inherited generation, are the pipes to the
	// previous one: ready to say how it goes, owners to hear which client
	// addresses the older generations keep. A brain over hk-pipe gets the
	// new end of the pipes' handoff link in owners' place: the pipes tell
	// each other whose flows stay (brain_linux.go).
	ready  *os.File
	owners *os.File
	// dir is the unit's runtime directory; empty means no handoff (a
	// process run by hand, or tests).
	dir string
}

func runtimeDirectory() string {
	dir := os.Getenv("RUNTIME_DIRECTORY")
	if i := strings.IndexByte(dir, ':'); i >= 0 {
		dir = dir[:i] // systemd lists several with ':'; ours is the first
	}
	return dir
}

// openSockets inherits the sockets from the previous generation, or binds
// them when this process is the first.
func openSockets(c Config) (*sockets, error) {
	spec := os.Getenv(envInherit)
	if spec == "" {
		s, err := bindSockets(c)
		if err != nil {
			return nil, err
		}
		s.dir = runtimeDirectory()
		return s, nil
	}
	gen, err := strconv.Atoi(os.Getenv(envGeneration))
	if err != nil || gen < 1 {
		return nil, fmt.Errorf("%s=%q", envGeneration, os.Getenv(envGeneration))
	}
	s := &sockets{tcp: map[int]*net.TCPListener{}, udp: map[int]*net.UDPConn{}, gen: gen, dir: runtimeDirectory()}
	names := strings.Split(spec, ",")
	fd := uintptr(3)
	for _, name := range names {
		if len(name) < 2 {
			return nil, fmt.Errorf("%s=%q", envInherit, spec)
		}
		port, err := strconv.Atoi(name[1:])
		if err != nil {
			return nil, fmt.Errorf("%s=%q", envInherit, spec)
		}
		f := os.NewFile(fd, name)
		fd++
		switch name[0] {
		case 't':
			ln, err := net.FileListener(f)
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("inherited tcp :%d: %w", port, err)
			}
			s.tcp[port] = ln.(*net.TCPListener)
		case 'u':
			pc, err := net.FilePacketConn(f)
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("inherited udp :%d: %w", port, err)
			}
			s.udp[port] = pc.(*net.UDPConn)
		default:
			return nil, fmt.Errorf("%s=%q", envInherit, spec)
		}
	}
	s.ready, s.owners = os.NewFile(fd, "ready"), os.NewFile(fd+1, "owners")
	_ = os.Unsetenv(envInherit)
	if err := s.matches(c); err != nil {
		return nil, err
	}
	return s, nil
}

// bindSockets binds the listen ports of c. Over hk-pipe they are IPv4
// alone: the pipe reads clients' addresses as IPv4 and refuses a
// dual-stack socket (hikkari-sys udp_from_fd).
func bindSockets(c Config) (*sockets, error) {
	s := &sockets{tcp: map[int]*net.TCPListener{}, udp: map[int]*net.UDPConn{}, gen: 1}
	tcp, udp := "tcp", "udp"
	if c.Pipe != "" {
		tcp, udp = "tcp4", "udp4"
	}
	for port := range c.TCP {
		ln, err := net.ListenTCP(tcp, &net.TCPAddr{Port: port})
		if err != nil {
			s.close()
			return nil, fmt.Errorf("tcp :%d: %w", port, err)
		}
		s.tcp[port] = ln
	}
	for port := range c.UDP {
		pc, err := net.ListenUDP(udp, &net.UDPAddr{Port: port})
		if err != nil {
			s.close()
			return nil, fmt.Errorf("udp :%d: %w", port, err)
		}
		s.udp[port] = pc
	}
	return s, nil
}

func (s *sockets) close() {
	for _, ln := range s.tcp {
		ln.Close()
	}
	for _, pc := range s.udp {
		pc.Close()
	}
}

// matches says whether c listens where these sockets do: a handoff cannot
// move a port, that is a restart.
func (s *sockets) matches(c Config) error {
	want, have := listenSpec(c), s.spec()
	if strings.Join(want, ",") != strings.Join(have, ",") {
		return fmt.Errorf("listen ports change from %v to %v: restart it", have, want)
	}
	return nil
}

func (s *sockets) spec() []string {
	var out []string
	for p := range s.tcp {
		out = append(out, "t"+strconv.Itoa(p))
	}
	for p := range s.udp {
		out = append(out, "u"+strconv.Itoa(p))
	}
	sort.Strings(out)
	return out
}

func listenSpec(c Config) []string {
	var out []string
	for p := range c.TCP {
		out = append(out, "t"+strconv.Itoa(p))
	}
	for p := range c.UDP {
		out = append(out, "u"+strconv.Itoa(p))
	}
	sort.Strings(out)
	return out
}

// tell writes a line to the previous generation, once.
func (s *sockets) tell(line string) {
	if s.ready != nil {
		_, _ = s.ready.WriteString(strings.ReplaceAll(line, "\n", " ") + "\n")
	}
}

// failed tells the previous generation why this one did not come up.
func (s *sockets) failed(err error) {
	if s.ready != nil {
		s.tell("failed: " + err.Error())
		s.ready.Close()
		s.ready = nil
	}
}

// sdNotify sends a state to systemd when the unit expects it.
func sdNotify(state string) {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return
	}
	if path[0] == '@' {
		path = "\x00" + path[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		log.Printf("nekko-trunk-entry: sd_notify: %v", err)
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(state))
}

// ── one generation ──

// node is the running generation: its sockets, its pool, what it serves.
type node struct {
	c     Config
	s     *sockets
	entry *trunk.Entry
	ctx   context.Context
	ports map[int]*udpPort
	// legs adds up the clients' TCP sockets for the leg client → entry
	// (clients.go).
	legs *clientLegs

	// extras stops the minute line and the call: the newest's alone.
	extras context.CancelFunc
	// handed is closed once a successor is up: this one drains.
	handed   chan struct{}
	draining atomic.Bool

	mbox   *net.UnixConn // gen-<g>.sock: datagrams of our flows from the newest
	reader *net.UnixConn // reader.sock while this one reads the UDP sockets
	sendMu sync.Mutex
	sendTo map[string]*net.UnixConn
}

func (n *node) context() context.Context { return n.ctx }
func (n *node) sockets() *sockets        { return n.s }

// startOn opens the pool on s. Nothing is served yet (serve).
func startOn(ctx context.Context, c Config, dial trunk.Dial, s *sockets) (*node, error) {
	ports, err := trunk.NewPortBook(c.PortLo, c.PortHi, 0)
	if err != nil {
		return nil, err
	}
	// Observe: the exit marks testers' streams, the entry logs them
	// (observe.go); the hello says so to the exit.
	entry, err := trunk.NewEntry(trunk.Config{Stall: c.Stall, Observe: logObserved}, c.Members, dial, ports, log.Printf)
	if err != nil {
		return nil, err
	}
	// The trunk's other wires, from the hub's file (wires.go). One that
	// cannot be read leaves the pool alone: a wire less, not an entry less.
	if err := addWires(c, entry); err != nil {
		log.Printf("nekko-trunk-entry: wires: %v (the pool alone)", err)
	}
	n := &node{c: c, s: s, entry: entry, ctx: ctx, ports: map[int]*udpPort{}, legs: newClientLegs(), handed: make(chan struct{}), sendTo: map[string]*net.UnixConn{}}
	for port, pc := range s.udp {
		copies := 0 // the session's
		if c.OneCopy[port] {
			copies = 1
		}
		n.ports[port] = &udpPort{n: n, port: port, to: c.UDP[port], copies: copies, pc: pc,
			flows: map[string]*trunk.Flow{}, owner: map[string]int{}}
	}
	go func() {
		<-ctx.Done()
		s.close()
		n.closeMailboxes()
	}()
	go entry.Run(ctx)
	return n, nil
}

// serve takes clients: accepts on the TCP listeners, reads the UDP sockets
// with the client addresses the older generations keep (port → address →
// generation), and opens the mailboxes.
func (n *node) serve(owners map[int]map[string]int) error {
	if n.s.dir != "" {
		if err := n.openMailboxes(); err != nil {
			return err
		}
	}
	for port, ln := range n.s.tcp {
		go n.accept(ln, n.c.TCP[port])
	}
	for port, p := range n.ports {
		for addr, g := range owners[port] {
			p.owner[addr] = g
		}
		p.startReading()
	}
	return nil
}

func (n *node) accept(ln *net.TCPListener, to uint16) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if n.ctx.Err() != nil || n.draining.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		tc := conn.(*net.TCPConn)
		_ = tc.SetKeepAlive(true)
		// The current session: after the exit drained (a hitless rollout
		// of its relay) that is a new one, while what the old one carries
		// stays on it.
		go n.entry.Session().Carry(n.legs.track(tc), to)
	}
}

// startExtras starts the minute lines (the pool's, the clients') and the
// trunk's call.
func (n *node) startExtras() {
	ctx, cancel := context.WithCancel(n.ctx)
	n.extras = cancel
	go logStats(ctx, n.entry)
	go logClients(ctx, goClients(n.legs))
	runCalls(ctx, n.entry)
	go runWires(ctx, n.c, n.entry)
}

// comeUp serves: at once for the first generation, after the previous one
// let go of the UDP sockets for an inherited one.
func (n *node) comeUp() error {
	if n.s.ready == nil {
		if err := n.serve(nil); err != nil {
			return err
		}
		n.startExtras()
		sdNotify(fmt.Sprintf("MAINPID=%d\nREADY=1", os.Getpid()))
		return nil
	}
	// Members first: a client the old one stops taking must find a pool.
	deadline := time.Now().Add(childUpWithin - 5*time.Second)
	for n.entry.Session().Stats().Members < 1 {
		if time.Now().After(deadline) {
			return errors.New("no member came up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	n.s.tell("ready")
	owners, err := readOwners(n.s.owners, 10*time.Second)
	n.s.owners.Close()
	if err != nil {
		return fmt.Errorf("owners: %w", err)
	}
	if err := n.serve(owners); err != nil {
		return err
	}
	n.startExtras()
	// systemd first: the previous generation may leave as soon as it
	// hears from us, and systemd must not take that for the service's end.
	sdNotify(fmt.Sprintf("MAINPID=%d\nREADY=1", os.Getpid()))
	n.s.tell("up")
	n.s.ready.Close()
	n.s.ready = nil
	return nil
}

func readOwners(f *os.File, within time.Duration) (map[int]map[string]int, error) {
	type result struct {
		m   map[int]map[string]int
		err error
	}
	got := make(chan result, 1)
	go func() {
		var m map[int]map[string]int
		err := json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(&m)
		got <- result{m, err}
	}()
	select {
	case r := <-got:
		return r.m, r.err
	case <-time.After(within):
		return nil, errors.New("the previous generation said nothing")
	}
}

// owners is what this generation hands over: its own flows and the ones it
// forwards to older generations.
func (n *node) owners() map[int]map[string]int {
	out := map[int]map[string]int{}
	for port, p := range n.ports {
		m := map[string]int{}
		p.mu.Lock()
		for addr := range p.flows {
			m[addr] = n.s.gen
		}
		for addr, g := range p.owner {
			m[addr] = g
		}
		p.mu.Unlock()
		out[port] = m
	}
	return out
}

// handOver makes this generation an old one: no new clients, no minute
// line, no call; what it carries goes on.
func (n *node) handOver() {
	n.draining.Store(true)
	for _, ln := range n.s.tcp {
		ln.Close() // this process's descriptor; the successor has its own
	}
	if n.extras != nil {
		n.extras()
	}
	if n.reader != nil {
		n.reader.Close() // the successor bound reader.sock again
		n.reader = nil
	}
	close(n.handed)
}

// busy is whether this generation still carries something.
func (n *node) busy() bool {
	return n.entry.Session().Busy() || n.entry.Retired() > 0
}

// drain waits until this old generation carries nothing, or drainLimit. Its
// sessions retire first: a drain of the exit opens no new pool here, since
// this generation takes no new clients.
func (n *node) drain() {
	n.entry.Retire()
	streams, flows := n.entry.Carrying()
	log.Printf("nekko-trunk-entry: generation %d drains with %d stream(s) and %d flow(s)", n.s.gen, streams, flows)
	limit := time.After(drainLimit)
	t := time.NewTicker(drainEvery)
	defer t.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-limit:
			streams, flows := n.entry.Carrying()
			log.Printf("nekko-trunk-entry: generation %d leaves after %s with %d stream(s) and %d flow(s)", n.s.gen, drainLimit, streams, flows)
			return
		case <-t.C:
			if !n.busy() {
				log.Printf("nekko-trunk-entry: generation %d carries nothing more and leaves", n.s.gen)
				return
			}
		}
	}
}

// ── UDP: one port ──

type udpPort struct {
	n      *node
	port   int
	to     uint16
	copies int
	pc     *net.UDPConn

	mu    sync.Mutex
	flows map[string]*trunk.Flow
	// owner are client addresses whose flows an older generation holds.
	owner map[string]int

	stop atomic.Bool
	done chan struct{}
}

func (p *udpPort) startReading() {
	p.stop.Store(false)
	_ = p.pc.SetReadDeadline(time.Time{})
	p.done = make(chan struct{})
	go p.read()
}

// pause stops reading and waits until the reader is out.
func (p *udpPort) pause() {
	p.stop.Store(true)
	_ = p.pc.SetReadDeadline(time.Now())
	<-p.done
}

func (p *udpPort) read() {
	defer close(p.done)
	buf := make([]byte, trunk.MaxPayload)
	for {
		size, from, err := p.pc.ReadFromUDPAddrPort(buf)
		if p.stop.Load() {
			return // a datagram read now is the successor's: it is lost, as at a pause
		}
		if err != nil {
			if p.n.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		p.datagram(from, buf[:size])
	}
}

// datagram is one client datagram read from the socket, or handed back by
// an older generation that no longer has its flow.
func (p *udpPort) datagram(from netip.AddrPort, data []byte) {
	key := from.String()
	p.mu.Lock()
	if g, ok := p.owner[key]; ok {
		p.mu.Unlock()
		if p.n.forward(g, p.port, key, data) == nil {
			return
		}
		p.mu.Lock()
		if p.owner[key] == g {
			delete(p.owner, key) // that generation is gone
		}
	}
	f := p.flows[key]
	if f == nil {
		f = p.open(from, key)
	}
	p.mu.Unlock()
	if !f.Send(data) {
		p.mu.Lock()
		if p.flows[key] == f {
			delete(p.flows, key)
		}
		p.mu.Unlock()
	}
}

// open gives a client address its own flow on the session current now.
// p.mu is held.
func (p *udpPort) open(client netip.AddrPort, key string) *trunk.Flow {
	var f *trunk.Flow
	// A port whose sender copies its datagrams (Hikkari v1.02's calls) is
	// the voice class (wires.go): its route is the wire ranked for calls.
	class := trunk.ClassBulk
	if p.copies == 1 {
		class = trunk.ClassVoice
	}
	f = p.n.entry.Session().OpenFlowWith(p.to, trunk.FlowOptions{Copies: p.copies, Class: class, Pin: trunk.AnyWire}, func(b []byte) { _, _ = p.pc.WriteToUDPAddrPort(b, client) }, func() {
		p.mu.Lock()
		mine := p.flows[key] == f
		if mine {
			delete(p.flows, key)
		}
		p.mu.Unlock()
		if mine && p.n.draining.Load() {
			p.n.release(p.port, key, nil)
		}
	})
	p.flows[key] = f
	return f
}

// fromOlder is a datagram an older generation could not take: its flow is
// gone, the address is this generation's now.
func (p *udpPort) fromOlder(g int, key string, data []byte) {
	p.mu.Lock()
	if p.owner[key] == g {
		delete(p.owner, key)
	}
	p.mu.Unlock()
	if len(data) == 0 {
		return
	}
	if from, err := netip.ParseAddrPort(key); err == nil {
		p.datagram(from, data)
	}
}

// fromNewest is a datagram of one of this old generation's flows, read by
// the newest one.
func (p *udpPort) fromNewest(key string, data []byte) {
	p.mu.Lock()
	f := p.flows[key]
	p.mu.Unlock()
	if f == nil || !f.Send(data) {
		p.n.release(p.port, key, data)
	}
}

// ── mailboxes ──

// A mailbox message: kind, the sender's generation, the port, the client
// address, the datagram.
const (
	msgDatagram = 'd' // newest → old: a datagram of the old one's flow
	msgRelease  = 'r' // old → newest: the flow is gone (with the datagram it could not take)
)

func encodeMsg(kind byte, gen, port int, addr string, data []byte) []byte {
	b := make([]byte, 0, 8+len(addr)+len(data))
	b = append(b, kind)
	b = binary.BigEndian.AppendUint32(b, uint32(gen))
	b = binary.BigEndian.AppendUint16(b, uint16(port))
	b = append(b, byte(len(addr)))
	b = append(b, addr...)
	return append(b, data...)
}

func decodeMsg(b []byte) (kind byte, gen, port int, addr string, data []byte, ok bool) {
	if len(b) < 8 || int(b[7]) > len(b)-8 {
		return 0, 0, 0, "", nil, false
	}
	n := int(b[7])
	return b[0], int(binary.BigEndian.Uint32(b[1:5])), int(binary.BigEndian.Uint16(b[5:7])), string(b[8 : 8+n]), b[8+n:], true
}

func (n *node) mailboxPath(gen int) string {
	return filepath.Join(n.s.dir, "gen-"+strconv.Itoa(gen)+".sock")
}

// openMailboxes binds this generation's mailbox and, as the reader of the
// UDP sockets now, reader.sock (taking the path over from the previous one).
func (n *node) openMailboxes() error {
	bind := func(path string) (*net.UnixConn, error) {
		_ = os.Remove(path)
		c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
		if err != nil {
			return nil, fmt.Errorf("mailbox %s: %w", filepath.Base(path), err)
		}
		_ = c.SetReadBuffer(4 << 20)
		return c, nil
	}
	var err error
	if n.mbox, err = bind(n.mailboxPath(n.s.gen)); err != nil {
		return err
	}
	if n.reader, err = bind(filepath.Join(n.s.dir, readerName)); err != nil {
		return err
	}
	// The reader path outlives this process (a datagram socket's path is
	// not unlinked on close): the next generation binds it again.
	go n.readMailbox(n.mbox)
	go n.readMailbox(n.reader)
	return nil
}

func (n *node) closeMailboxes() {
	if n.mbox != nil {
		n.mbox.Close()
		// Nobody may forward to a generation gone: the newest takes the
		// address itself when the path is not there.
		_ = os.Remove(n.mailboxPath(n.s.gen))
	}
	if n.reader != nil {
		n.reader.Close()
	}
	n.sendMu.Lock()
	for _, c := range n.sendTo {
		c.Close()
	}
	n.sendTo = map[string]*net.UnixConn{}
	n.sendMu.Unlock()
}

func (n *node) readMailbox(c *net.UnixConn) {
	buf := make([]byte, 8+256+trunk.MaxPayload)
	for {
		size, _, err := c.ReadFromUnix(buf)
		if err != nil {
			return
		}
		kind, gen, port, addr, data, ok := decodeMsg(buf[:size])
		p := n.ports[port]
		if !ok || p == nil {
			continue
		}
		switch kind {
		case msgDatagram:
			p.fromNewest(addr, data)
		case msgRelease:
			p.fromOlder(gen, addr, data)
		}
	}
}

// send writes one message to the mailbox at path.
func (n *node) send(path string, msg []byte) error {
	n.sendMu.Lock()
	c := n.sendTo[path]
	if c == nil {
		var err error
		if c, err = net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"}); err != nil {
			n.sendMu.Unlock()
			return err
		}
		n.sendTo[path] = c
	}
	n.sendMu.Unlock()
	if _, err := c.Write(msg); err != nil {
		n.sendMu.Lock()
		if n.sendTo[path] == c {
			delete(n.sendTo, path)
		}
		n.sendMu.Unlock()
		c.Close()
		return err
	}
	return nil
}

// forward hands a datagram to the older generation g that holds its flow.
func (n *node) forward(g, port int, key string, data []byte) error {
	if n.s.dir == "" {
		return errors.New("no mailboxes")
	}
	return n.send(n.mailboxPath(g), encodeMsg(msgDatagram, n.s.gen, port, key, data))
}

// release tells the newest generation that this old one no longer has a
// flow for key, with the datagram it could not take.
func (n *node) release(port int, key string, data []byte) {
	if n.s.dir == "" {
		return
	}
	if err := n.send(filepath.Join(n.s.dir, readerName), encodeMsg(msgRelease, n.s.gen, port, key, data)); err != nil {
		log.Printf("nekko-trunk-entry: generation %d: release: %v", n.s.gen, err)
	}
}

// ── control ──

// handoffRequest is what `nekko-trunk-entry reload` sends: the entry's env
// and the binary to start.
type handoffRequest struct {
	Env    map[string]string `json:"env"`
	Binary string            `json:"binary,omitempty"`
}

type handoffReply struct {
	OK    bool   `json:"ok"`
	PID   int    `json:"pid,omitempty"`
	Gen   int    `json:"gen,omitempty"`
	Error string `json:"error,omitempty"`
}

// generation is what the control socket hands over from: a Go entry's
// (node) or a brain's over hk-pipe (brain_linux.go).
type generation interface {
	context() context.Context
	sockets() *sockets
	// spawn starts the next generation with req's env and waits until it
	// is up; on any failure the child is killed and this one serves on.
	spawn(req handoffRequest) (pid, gen int, err error)
	// handOver makes this generation an old one: no new clients, no minute
	// line, no call; what it carries goes on.
	handOver()
}

// controller is the newest generation's control socket.
type controller struct {
	n    generation
	ln   *net.UnixListener
	once sync.Once
}

// listenControl binds the control socket, taking the path over from the
// previous generation. Nil without a runtime directory.
func listenControl(n generation) (*controller, error) {
	if n.sockets().dir == "" {
		return nil, nil
	}
	path := filepath.Join(n.sockets().dir, controlName)
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	ln.SetUnlinkOnClose(false) // the next generation binds it again
	return &controller{n: n, ln: ln}, nil
}

// serve answers handoff requests until one succeeds or the process ends.
func (c *controller) serve() {
	go func() { <-c.n.context().Done(); c.close() }()
	for {
		conn, err := c.ln.Accept()
		if err != nil {
			return
		}
		done := c.answer(conn)
		conn.Close()
		if done {
			c.close()
			return
		}
	}
}

func (c *controller) close() { c.once.Do(func() { _ = c.ln.Close() }) }

// answer serves one request; true when the next generation is up.
func (c *controller) answer(conn net.Conn) bool {
	_ = conn.SetDeadline(time.Now().Add(childUpWithin + 20*time.Second))
	reply := func(r handoffReply) { _ = json.NewEncoder(conn).Encode(r) }
	var req handoffRequest
	if err := json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&req); err != nil {
		reply(handoffReply{Error: "request: " + err.Error()})
		return false
	}
	pid, gen, err := c.n.spawn(req)
	if err != nil {
		log.Printf("nekko-trunk-entry: handoff refused: %v", err)
		reply(handoffReply{Error: err.Error()})
		return false
	}
	// Said again from here: whichever message systemd reads first, the
	// child is the main process before this one leaves.
	sdNotify(fmt.Sprintf("MAINPID=%d", pid))
	log.Printf("nekko-trunk-entry: generation %d (pid %d) is up, generation %d drains", gen, pid, c.n.sockets().gen)
	c.n.handOver()
	reply(handoffReply{OK: true, PID: pid, Gen: gen})
	return true
}

// spawn starts the next generation with req's env and waits until it is up.
// On any failure the child is killed and this generation serves on.
func (n *node) spawn(req handoffRequest) (int, int, error) {
	cfg, err := ConfigFromEnv(func(k string) string { return req.Env[k] })
	if err != nil {
		return 0, 0, err
	}
	if err := n.s.matches(cfg); err != nil {
		return 0, 0, err
	}
	if cfg.Pipe != "" {
		return 0, 0, errors.New("the next generation runs over hk-pipe, this one in Go: restart it")
	}
	binary := req.Binary
	if binary == "" {
		if binary, err = os.Executable(); err != nil {
			return 0, 0, err
		}
	}
	next := n.s.gen + 1
	spec := n.s.spec()
	files := make([]*os.File, 0, len(spec)+2)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	for _, name := range spec {
		port, _ := strconv.Atoi(name[1:])
		var f *os.File
		if name[0] == 't' {
			f, err = n.s.tcp[port].File()
		} else {
			f, err = n.s.udp[port].File()
		}
		if err != nil {
			return 0, 0, err
		}
		files = append(files, f)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return 0, 0, err
	}
	defer readyR.Close()
	ownersR, ownersW, err := os.Pipe()
	if err != nil {
		readyW.Close()
		return 0, 0, err
	}
	defer ownersW.Close()
	files = append(files, readyW, ownersR)

	cmd := exec.Command(binary, "run")
	cmd.Env = childEnv(os.Environ(), req.Env, spec, next)
	cmd.ExtraFiles = files
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Start()
	readyW.Close()
	ownersR.Close()
	files = files[:len(files)-2]
	if err != nil {
		return 0, 0, fmt.Errorf("start %s: %w", binary, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	lines := make(chan string, 2)
	go func() {
		r := bufio.NewReader(readyR)
		for {
			line, err := r.ReadString('\n')
			if line = strings.TrimSpace(line); line != "" {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	paused := false
	fail := func(why string) (int, int, error) {
		_ = cmd.Process.Kill()
		<-exited
		if paused {
			for _, p := range n.ports {
				p.startReading()
			}
		}
		return 0, 0, fmt.Errorf("generation %d: %s", next, why)
	}
	await := func(want string, within time.Duration) error {
		select {
		case line, ok := <-lines:
			if !ok {
				return errors.New("exited before it was up")
			}
			if line != want {
				return errors.New(line)
			}
			return nil
		case <-time.After(within):
			return fmt.Errorf("no %q within %s", want, within)
		}
	}
	if err := await("ready", childUpWithin); err != nil {
		return fail(err.Error())
	}
	// The child has members: stop reading UDP, tell it whose addresses
	// stay here, then it reads.
	for _, p := range n.ports {
		p.pause()
	}
	paused = true
	if err := json.NewEncoder(ownersW).Encode(n.owners()); err != nil {
		return fail("owners: " + err.Error())
	}
	ownersW.Close()
	if err := await("up", 15*time.Second); err != nil {
		return fail(err.Error())
	}
	return cmd.Process.Pid, next, nil
}

// childEnv is the parent's environment without its entry config, plus the
// new one and the handoff's own names. systemd's names (NOTIFY_SOCKET,
// RUNTIME_DIRECTORY, …) pass through.
func childEnv(own []string, entry map[string]string, spec []string, gen int) []string {
	var out []string
	for _, kv := range own {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "EDGE_TRUNK_") || k == envInherit || k == envGeneration {
			continue
		}
		if _, set := entry[k]; set {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range entry {
		out = append(out, k+"="+v)
	}
	return append(out, envInherit+"="+strings.Join(spec, ","), envGeneration+"="+strconv.Itoa(gen))
}

// requestHandoff asks the generation behind control to hand over to req.
func requestHandoff(control string, req handoffRequest) (handoffReply, error) {
	conn, err := net.DialTimeout("unix", control, 5*time.Second)
	if err != nil {
		return handoffReply{}, fmt.Errorf("no running generation to hand over from: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(childUpWithin + 30*time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return handoffReply{}, err
	}
	var reply handoffReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return handoffReply{}, fmt.Errorf("no answer: %w", err)
	}
	if !reply.OK {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

// reloadCommand is `nekko-trunk-entry reload [env file]`, systemd's
// ExecReload (as root): the entry's env, handed to the running generation
// with this binary. The env file is the unit's (envFile without one: the
// stand over hk-pipe has its own), the control socket in the unit's runtime
// directory, which systemd names to ExecReload as to ExecStart.
func reloadCommand(file string) error {
	if file == "" {
		file = envFile
	}
	env, err := readEnvFile(file)
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	dir := runtimeDirectory()
	if dir == "" {
		dir = runtimeDir
	}
	reply, err := requestHandoff(filepath.Join(dir, controlName), handoffRequest{Env: env, Binary: binary})
	if err != nil {
		return err
	}
	fmt.Printf("nekko-trunk-entry: generation %d is up (pid %d), the previous one drains\n", reply.Gen, reply.PID)
	return nil
}

// envFile is where the hub writes the entry's env (internal/entry trunk.go).
const envFile = "/etc/nekko-trunk-entry.env"

// readEnvFile reads KEY=VALUE lines as the hub writes them: no quoting, '#'
// starts a comment line.
func readEnvFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%s: line %q is not KEY=VALUE", path, line)
		}
		env[k] = v
	}
	return env, nil
}

// runGeneration is the entry's process: it comes up (on its own sockets or
// the previous generation's), serves until a successor is up, then drains
// what it carries and leaves.
func runGeneration(c Config, dial trunk.Dial) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s, err := openSockets(c)
	if err != nil {
		if s != nil {
			s.failed(err)
		}
		return err
	}
	n, err := startOn(ctx, c, dial, s)
	if err != nil {
		s.failed(err)
		return err
	}
	if err := n.comeUp(); err != nil {
		s.failed(err)
		return err
	}
	ctl, err := listenControl(n)
	if err != nil {
		return err
	}
	log.Printf("nekko-trunk-entry: up: generation %d, exit %s (%s), %d members, tcp %v, udp %v, udp one copy %v",
		s.gen, c.Exit, c.SNI, c.Members, forwardList(c.TCP), forwardList(c.UDP), portList(c.OneCopy))
	if ctl != nil {
		go ctl.serve()
	}
	select {
	case <-ctx.Done():
		return nil
	case <-n.handed:
	}
	n.drain()
	return nil
}
