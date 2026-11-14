package main

// brain_linux.go: the entry as a brain over hk-pipe (roadmap 4.6, R5;
// hikkari-protocol rust/README.md, «Brain and pipe», «Generations (R4)»).
// Every packet of the entry goes through the pipe, a Rust process this one
// starts and steers over trunk/pipectl; this process keeps what is not per
// packet: the client ports (it binds them, the pipe gets them by
// SCM_RIGHTS), the pool's form and the wires from the hub, the selector over
// the pipe's ticks, the trunk's calls, the minute lines, the generations.
//
// A reload is the Go entry's (handoff.go: control socket, the next binary
// started on the same sockets), with the pipes doing what the Go
// generations' mailboxes do: before it starts the next brain, this one makes
// the reload's handoff link (pipectl.HandoffLink) and passes its new end to
// the child in the owners pipe's place. The child starts its own pipe, gives
// it the pool and wires, waits for a member, makes its pipe adopt the link,
// gives it the client ports and says "up"; this one then makes its own pipe
// drain on the link's old end. The old pipe stops reading and accepting,
// tells the new one whose flows it keeps, carries them and its streams until
// they end, and says drained; this brain then ends it and leaves, or at
// drainLimit.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// brain is one generation over its pipe.
type brain struct {
	c    Config
	s    *sockets
	ctx  context.Context
	pipe *pipectl.Pipe
	// wires are the packet wires the pipe was given from the hub's file,
	// steer whether the selector moves the route over them.
	wires []pipectl.Wire
	steer bool

	extras context.CancelFunc
	handed chan struct{}
	deaths memberDeaths
	// ending is set once this brain ends its pipe itself: the pipe's end
	// is no failure then.
	ending atomic.Bool
}

// end ends the pipe on purpose.
func (b *brain) end() {
	b.ending.Store(true)
	b.pipe.Close()
}

// memberDeaths counts the pipe's member events since the start, as the Go
// entry's minute line counts its members' (joined, stalled, broken).
type memberDeaths struct {
	joined, silent, quiet, gone atomic.Uint64
}

func (d *memberDeaths) note(up bool, why byte) {
	switch {
	case up:
		d.joined.Add(1)
	case why == pipectl.WhySilent:
		d.silent.Add(1)
	case why == pipectl.WhyQuiet:
		d.quiet.Add(1)
	default:
		d.gone.Add(1)
	}
}

func (b *brain) context() context.Context { return b.ctx }
func (b *brain) sockets() *sockets        { return b.s }

// cronetBeside is the libcronet.so the pipe loads: beside hk-pipe, as the
// hub stages them (internal/entry trunkpipe.go).
func cronetBeside(pipe string) string { return filepath.Join(filepath.Dir(pipe), "libcronet.so") }

// exitOf is the exit relay's address, the pool's end.
func exitOf(c Config) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(c.Exit)
	if err != nil {
		return ap, fmt.Errorf("EDGE_TRUNK_EXIT: %w", err)
	}
	return ap, nil
}

// pipeWires are the packet wires of the hub's file the pipe carries: udp,
// and Chromium's QUIC wire h3dgram (R6.3: the pipe opens its members from
// the pool's book of ports, through the libcronet beside it, to the exit's
// QUIC port, whose certificate names the trunk's SNI). quic and salamander
// are not carried over to Rust (the owner's 12.11).
func pipeWires(c Config, exit netip.AddrPort, roots string) ([]pipectl.Wire, bool, error) {
	f, _, err := readWires(wiresFilePath())
	if err != nil {
		return nil, false, err
	}
	var out []pipectl.Wire
	for _, w := range f.Wires {
		pw := pipectl.Wire{ID: wireIDs[w.Kind], Name: w.Name, Key: c.Key, Members: w.Members, Stall: c.Stall,
			Exit: netip.AddrPortFrom(exit.Addr(), uint16(w.Port))}
		switch w.Kind {
		case "udp":
		case "h3dgram":
			pw.H3 = &pipectl.H3Form{SNI: c.SNI, Cronet: cronetBeside(c.Pipe), Roots: roots, Usage: w.Usage,
				Ports: [2]uint16{uint16(c.PortLo), uint16(c.PortHi)}}
		default:
			log.Printf("nekko-trunk-entry: wire %s is not carried by the pipe, left out", w.Name)
			continue
		}
		out = append(out, pw)
	}
	return out, f.Steer, nil
}

// wireShape is a pipe wire's shape for the selector's warm pair: Chromium's
// QUIC looks like a browser, the bare UDP wire like nothing.
func wireShape(w pipectl.Wire) string {
	if w.H3 != nil {
		return "browser"
	}
	return "random"
}

// startBrain starts the pipe and gives it the pool and the wires. No
// client is served yet (serve).
func startBrain(ctx context.Context, c Config, s *sockets) (*brain, error) {
	exit, err := exitOf(c)
	if err != nil {
		return nil, err
	}
	roots := ""
	if c.Roots != "" {
		raw, err := os.ReadFile(c.Roots)
		if err != nil {
			return nil, fmt.Errorf("EDGE_TRUNK_ROOTS: %w", err)
		}
		roots = string(raw)
	}
	b := &brain{c: c, s: s, ctx: ctx, handed: make(chan struct{})}
	p, err := pipectl.Start(pipectl.Config{Bin: c.Pipe, Stderr: os.Stderr,
		Logf:     func(format string, a ...any) { log.Printf("nekko-trunk-entry: "+format, a...) },
		OnMember: func(_ trunk.WireID, _ int, up bool, why byte) { b.deaths.note(up, why) }})
	if err != nil {
		return nil, err
	}
	b.pipe = p
	pool := pipectl.Pool{ID: trunk.PoolWire, Members: c.Members, Stall: c.Stall, Exit: exit,
		Ports:    [2]uint16{uint16(c.PortLo), uint16(c.PortHi)},
		Chromium: &pipectl.ChromiumForm{SNI: c.SNI, Key: c.Key, Cronet: cronetBeside(c.Pipe), Roots: roots}}
	if err := p.SetPool(pool); err != nil {
		p.Close()
		return nil, fmt.Errorf("pool: %w", err)
	}
	// A file that cannot be read leaves the pool alone, as the Go entry's
	// addWires: a wire less, not an entry less.
	ws, steer, err := pipeWires(c, exit, roots)
	if err != nil {
		log.Printf("nekko-trunk-entry: wires: %v (the pool alone)", err)
	}
	for _, w := range ws {
		if err := p.SetWire(w); err != nil {
			p.Close()
			return nil, fmt.Errorf("wire %s: %w", w.Name, err)
		}
	}
	b.wires, b.steer = ws, steer
	go func() {
		select {
		case <-ctx.Done():
		case <-p.Done():
			// The pipe is every packet: a brain without it serves nothing,
			// and systemd starts the entry anew.
			if ctx.Err() == nil && !b.ending.Load() {
				log.Fatalf("nekko-trunk-entry: the pipe ended: %v", p.Err())
			}
		}
	}()
	return b, nil
}

// poolUp waits until the pipe's pool has a member up: a client the old
// generation stops taking must find one.
func (b *brain) poolUp(within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if w, ok := b.pipe.Wires()[trunk.PoolWire]; ok && w.Members > 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("no member came up")
}

// serve gives the pipe the client ports: UDP ports as flows (one copy and
// the voice class on a port whose sender copies itself, as handoff.go
// udpPort.open), TCP ports as streams on the pool. The ids follow the
// ports' order, the same in every generation.
func (b *brain) serve() error {
	for i, port := range sortedPorts(b.s.udp) {
		copies, class := trunk.DefaultCopies, trunk.ClassBulk
		if b.c.OneCopy[port] {
			copies, class = 1, trunk.ClassVoice
		}
		if err := b.pipe.Listen(byte(i+1), b.s.udp[port], class, b.c.UDP[port], copies); err != nil {
			return fmt.Errorf("udp :%d: %w", port, err)
		}
	}
	for i, port := range sortedPorts(b.s.tcp) {
		if err := b.pipe.TCPListen(byte(i+1), b.s.tcp[port], b.c.TCP[port]); err != nil {
			return fmt.Errorf("tcp :%d: %w", port, err)
		}
	}
	return nil
}

func sortedPorts[T any](m map[int]T) []int {
	out := make([]int, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// comeUp serves: at once for the first generation; for an inherited one
// after its pipe adopted the reload's link, whose old end the previous
// brain gives its own pipe once this one said "up".
func (b *brain) comeUp() error {
	if err := b.poolUp(childUpWithin - 5*time.Second); err != nil {
		return err
	}
	if b.s.ready != nil {
		if err := b.pipe.Adopt(b.s.owners); err != nil {
			return fmt.Errorf("adopt: %w", err)
		}
		b.s.owners.Close()
	}
	if err := b.serve(); err != nil {
		return err
	}
	b.startExtras()
	// systemd first: the previous generation may leave as soon as it hears
	// from us, and systemd must not take that for the service's end.
	sdNotify(fmt.Sprintf("MAINPID=%d\nREADY=1", os.Getpid()))
	if b.s.ready != nil {
		b.s.tell("up")
		b.s.ready.Close()
		b.s.ready = nil
	}
	return nil
}

// startExtras starts the newest generation's minute line, the trunk's
// calls and the selector over the wires.
func (b *brain) startExtras() {
	ctx, cancel := context.WithCancel(b.ctx)
	b.extras = cancel
	carrier := func() trunk.CallCarrier { return b.pipe }
	go b.logMinutes(ctx)
	for _, copies := range callCopies {
		go trunk.RunCallOn(ctx, carrier, trunk.CallConfig{Copies: copies}, func(w trunk.CallWindow) {
			log.Print("nekko-trunk-entry: " + callLine(w))
		})
	}
	if len(b.wires) == 0 {
		b.pipe.SetRoute(trunk.AnyRoute)
		return
	}
	all := []trunk.Wire{poolWire()}
	ids := []trunk.WireID{trunk.PoolWire}
	names := map[trunk.WireID]string{trunk.PoolWire: "pool"}
	for _, w := range b.wires {
		all = append(all, trunk.Wire{ID: w.ID, Name: w.Name, Shape: wireShape(w), Packet: true})
		ids = append(ids, w.ID)
		names[w.ID] = w.Name
	}
	line := func(id trunk.WireID, cw trunk.CallWindow) {
		log.Print("nekko-trunk-entry: " + wireCallLine(names[id], cw))
	}
	if !b.steer {
		b.pipe.SetRoute(trunk.AnyRoute)
		for _, id := range ids {
			go trunk.RunCallOn(ctx, carrier, trunk.CallConfig{Copies: 1, OnWire: true, Wire: id}, func(cw trunk.CallWindow) { line(id, cw) })
		}
		return
	}
	sel := trunk.NewSelector(trunk.SelectorConfig{}, all...)
	go b.pipe.Run(ctx, sel, ids, line, func(format string, a ...any) { log.Printf("nekko-trunk-entry: "+format, a...) })
}

// handOver makes this generation an old one (its pipe already drains): no
// minute line, no call, no selector; what the pipe carries goes on.
func (b *brain) handOver() {
	if b.extras != nil {
		b.extras()
	}
	close(b.handed)
}

// drain waits until the old pipe carries nothing (it says drained), or
// drainLimit, then ends it.
func (b *brain) drain() {
	t, _ := b.pipe.Tick()
	log.Printf("nekko-trunk-entry: generation %d drains with %d stream(s) and %d flow(s)", b.s.gen, t.Streams, t.Flows)
	select {
	case <-b.ctx.Done():
	case <-b.pipe.Drained():
		log.Printf("nekko-trunk-entry: generation %d carries nothing more and leaves", b.s.gen)
	case <-time.After(drainLimit):
		t, _ := b.pipe.Tick()
		log.Printf("nekko-trunk-entry: generation %d leaves after %s with %d stream(s) and %d flow(s)", b.s.gen, drainLimit, t.Streams, t.Flows)
	}
	b.end()
}

// spawn starts the next brain on this one's sockets with the new end of a
// reload's link, waits until it is up, then makes this pipe drain to it.
func (b *brain) spawn(req handoffRequest) (int, int, error) {
	cfg, err := ConfigFromEnv(func(k string) string { return req.Env[k] })
	if err != nil {
		return 0, 0, err
	}
	if err := b.s.matches(cfg); err != nil {
		return 0, 0, err
	}
	if cfg.Pipe == "" {
		return 0, 0, errors.New("the next generation is a Go entry, this one a brain over hk-pipe: restart it")
	}
	binary := req.Binary
	if binary == "" {
		if binary, err = os.Executable(); err != nil {
			return 0, 0, err
		}
	}
	next := b.s.gen + 1
	spec := b.s.spec()
	files, err := b.s.files(spec)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	oldEnd, newEnd, err := pipectl.HandoffLink()
	if err != nil {
		return 0, 0, err
	}
	defer oldEnd.Close()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		newEnd.Close()
		return 0, 0, err
	}
	defer readyR.Close()
	cmd := exec.Command(binary, "run")
	cmd.Env = childEnv(os.Environ(), req.Env, spec, next)
	cmd.ExtraFiles = append(files, readyW, newEnd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Start()
	readyW.Close()
	newEnd.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("start %s: %w", binary, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := awaitLine(readyR, "up", childUpWithin); err != nil {
		_ = cmd.Process.Kill()
		<-exited
		return 0, 0, fmt.Errorf("generation %d: %v", next, err)
	}
	if err := b.pipe.Drain(oldEnd); err != nil {
		// The new pipe adopted a link no old pipe drains on: it reads its
		// client ports once the link closes (an older pipe gone), so the
		// child serves, and this generation must not.
		log.Printf("nekko-trunk-entry: drain: %v (this pipe ends)", err)
		b.end()
	}
	return cmd.Process.Pid, next, nil
}

// files are the descriptors of the sockets in spec order, for a child.
func (s *sockets) files(spec []string) ([]*os.File, error) {
	out := make([]*os.File, 0, len(spec)+2)
	for _, name := range spec {
		port, _ := strconv.Atoi(name[1:])
		var f *os.File
		var err error
		if name[0] == 't' {
			f, err = s.tcp[port].File()
		} else {
			f, err = s.udp[port].File()
		}
		if err != nil {
			for _, g := range out {
				g.Close()
			}
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// awaitLine waits for the child's line want on r: anything else is why it
// failed.
func awaitLine(r *os.File, want string, within time.Duration) error {
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case line := <-lines:
		switch line {
		case want:
			return nil
		case "":
			return errors.New("exited before it was up")
		default:
			return errors.New(line)
		}
	case <-time.After(within):
		return fmt.Errorf("no %q within %s", want, within)
	}
}

// runPipeGeneration is the brain's process: it comes up on its own sockets
// or the previous generation's, serves until a successor is up, then
// drains its pipe and leaves.
func runPipeGeneration(c Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s, err := openSockets(c)
	if err != nil {
		if s != nil {
			s.failed(err)
		}
		return err
	}
	b, err := startBrain(ctx, c, s)
	if err != nil {
		s.failed(err)
		return err
	}
	if err := b.comeUp(); err != nil {
		s.failed(err)
		b.end()
		return err
	}
	ctl, err := listenControl(b)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(b.wires))
	for _, w := range b.wires {
		names = append(names, fmt.Sprintf("%s:%d", w.Name, w.Members))
	}
	log.Printf("nekko-trunk-entry: up over hk-pipe (pid %d): generation %d, exit %s (%s), %d members, wires %v steer=%v, tcp %v, udp %v, udp one copy %v",
		b.pipe.Pid(), s.gen, c.Exit, c.SNI, c.Members, names, b.steer, forwardList(c.TCP), forwardList(c.UDP), portList(c.OneCopy))
	if ctl != nil {
		go ctl.serve()
	}
	select {
	case <-ctx.Done():
		b.end()
		return nil
	case <-b.handed:
	}
	b.drain()
	return nil
}

// checkPipe is `check` over hk-pipe: the pipe starts on this box and says
// its hello, its libcronet is there.
func checkPipe(c Config) error {
	if _, err := os.Stat(cronetBeside(c.Pipe)); err != nil {
		return fmt.Errorf("the pipe's libcronet: %w", err)
	}
	p, err := pipectl.Start(pipectl.Config{Bin: c.Pipe, Stderr: os.Stderr})
	if err != nil {
		return err
	}
	return p.Close()
}
