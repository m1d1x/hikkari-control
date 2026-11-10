package main

// handoff.go: a Hikkari relay is replaced without cutting its sessions
// (roadmap 2.2, hikkari-client/plans/2026-11-10-edge-hitless-rollout.md).
//
// The processes of one relay are generations of one systemd unit. A rollout
// is `systemctl reload nekko-edge@<relay>`, whose ExecReload runs
// `nekko-edge reload <relay>` as root: it reads the relay's env and hands it
// to the running process over the control socket. That process starts the
// binary now on disk as the next generation, giving it its TCP listener and
// its UDP socket (the same sockets, not bound again), and waits until the
// child says it is up. Then the child is the unit's main process (sd_notify
// MAINPID), owns the control socket and takes everything new; the old one
// drains:
//
//   - TCP: Caddy stops accepting and says GOAWAY; tunnels already open go on;
//   - UDP: the old generation stops reading the shared socket and gets its
//     own connections' packets from the new one (hikkari-protocol
//     server/generation.go);
//   - trunk: the exit tells its entries (trunk.Exit.Drain), which move to new
//     sessions on the new process and close the old ones when they are done.
//
// The old process leaves when it carries nothing, or after drainLimit, the
// sessions it still has then ending as a stopping relay's do. A child that
// does not come up in time is killed, and the old process goes on as if
// nothing happened: the reload fails, nothing was cut. A rollback is the same
// handoff to the previous binary.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// handoffVersion is what `nekko-edge handoff-version` prints: the
// generation contract (env names, descriptors, control socket) this build
// speaks.
const handoffVersion = 1

const (
	// envInherit names the descriptors a generation inherits: TCP listener,
	// UDP socket, readiness pipe.
	envInherit = "NEKKO_EDGE_INHERIT"
	// envGeneration is the generation number of the process.
	envGeneration = "NEKKO_EDGE_GEN"
	controlName   = "control.sock"
)

// drainLimit is how long an old generation keeps the sessions it has.
var drainLimit = 30 * time.Minute

// childUpWithin bounds how long a new generation may take to come up.
var childUpWithin = 30 * time.Second

// sockets are a relay's listening sockets and its generation.
type sockets struct {
	tcp *net.TCPListener
	udp *net.UDPConn
	gen byte
	// ready, for an inherited generation, is the pipe to say it is up on.
	ready *os.File
	// dir is the unit's runtime directory (control socket, mailboxes);
	// empty means no handoff: a process run by hand, or tests.
	dir string
}

// openSockets inherits the sockets from the previous generation, or binds
// them when this process is the first.
func openSockets(listen string) (*sockets, error) {
	s := &sockets{dir: os.Getenv("RUNTIME_DIRECTORY")}
	if i := strings.IndexByte(s.dir, ':'); i >= 0 {
		s.dir = s.dir[:i] // systemd lists several with ':'; ours is the first
	}
	if spec := os.Getenv(envInherit); spec != "" {
		fds := strings.Split(spec, ",")
		if len(fds) != 3 {
			return nil, fmt.Errorf("%s=%q", envInherit, spec)
		}
		var num [3]uintptr
		for i, f := range fds {
			n, err := strconv.ParseUint(f, 10, 31)
			if err != nil {
				return nil, fmt.Errorf("%s=%q", envInherit, spec)
			}
			num[i] = uintptr(n)
		}
		gen, err := strconv.ParseUint(os.Getenv(envGeneration), 10, 8)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envGeneration, err)
		}
		s.gen = byte(gen)
		tf := os.NewFile(num[0], "tcp")
		ln, err := net.FileListener(tf)
		tf.Close()
		if err != nil {
			return nil, fmt.Errorf("inherited tcp: %w", err)
		}
		uf := os.NewFile(num[1], "udp")
		pc, err := net.FilePacketConn(uf)
		uf.Close()
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("inherited udp: %w", err)
		}
		s.tcp, s.udp, s.ready = ln.(*net.TCPListener), pc.(*net.UDPConn), os.NewFile(num[2], "ready")
		_ = os.Unsetenv(envInherit)
		return s, nil
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	addr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		ln.Close()
		return nil, err
	}
	udp, err := net.ListenUDP("udp", addr)
	if err != nil {
		ln.Close()
		return nil, err
	}
	var b [1]byte
	_, _ = rand.Read(b[:])
	s.tcp, s.udp, s.gen = ln.(*net.TCPListener), udp, b[0]
	return s, nil
}

// up says this generation is serving: to the previous generation when there
// is one (it then drains), and to systemd, whose main process this becomes.
func (s *sockets) up() {
	// systemd first: the previous generation may leave as soon as it hears
	// from us, and systemd must not take that for the service's end.
	sdNotify(fmt.Sprintf("MAINPID=%d\nREADY=1", os.Getpid()))
	if s.ready != nil {
		_, _ = s.ready.WriteString("up\n")
		s.ready.Close()
		s.ready = nil
	}
}

// failed tells the previous generation why this one did not come up.
func (s *sockets) failed(err error) {
	if s.ready != nil {
		_, _ = s.ready.WriteString("failed: " + strings.ReplaceAll(err.Error(), "\n", " ") + "\n")
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
		log.Printf("nekko-edge: sd_notify: %v", err)
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(state))
}

// handoffRequest is what `nekko-edge reload` sends: the relay's env and,
// for tests, the binary to start (the one at the executable's path otherwise).
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

// controller is the newest generation's control socket.
type controller struct {
	s      *sockets
	listen string // the relay's listener: a handoff may not move it
	ln     *net.UnixListener
	once   sync.Once
	// handedTo receives the successor's generation once a child is up.
	handedTo chan byte
}

// listenControl binds the control socket in the runtime directory, taking
// the path over from the previous generation. Nil without a directory.
func listenControl(s *sockets, listen string) (*controller, error) {
	if s.dir == "" {
		return nil, nil
	}
	path := filepath.Join(s.dir, controlName)
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	// The path outlives this process: the next generation binds it again.
	ln.SetUnlinkOnClose(false)
	return &controller{s: s, listen: listen, ln: ln, handedTo: make(chan byte, 1)}, nil
}

// serve answers handoff requests until one succeeds or ctx ends.
func (c *controller) serve(ctx context.Context) {
	go func() { <-ctx.Done(); c.close() }()
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
	_ = conn.SetDeadline(time.Now().Add(childUpWithin + 10*time.Second))
	var req handoffRequest
	reply := func(r handoffReply) { _ = json.NewEncoder(conn).Encode(r) }
	if err := json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&req); err != nil {
		reply(handoffReply{Error: "request: " + err.Error()})
		return false
	}
	pid, gen, err := c.spawn(req)
	if err != nil {
		log.Printf("nekko-edge: handoff refused: %v", err)
		reply(handoffReply{Error: err.Error()})
		return false
	}
	// Said again from here: whichever message systemd reads first, the
	// child is the main process before this one leaves.
	sdNotify(fmt.Sprintf("MAINPID=%d", pid))
	log.Printf("nekko-edge: generation %d (pid %d) is up, generation %d drains", gen, pid, c.s.gen)
	c.handedTo <- gen
	reply(handoffReply{OK: true, PID: pid, Gen: int(gen)})
	return true
}

// spawn starts the next generation with req's env and waits until it is up.
func (c *controller) spawn(req handoffRequest) (int, byte, error) {
	get := func(k string) string { return req.Env[k] }
	if p := get("EDGE_PROTO"); p != protoHikkari {
		return 0, 0, fmt.Errorf("EDGE_PROTO %q: a handoff is for a Hikkari relay only, restart it", p)
	}
	cfg, err := naiveFromEnv(get)
	if err != nil {
		return 0, 0, err
	}
	if cfg.Listen != c.listen {
		return 0, 0, fmt.Errorf("listener moves from %s to %s: restart it", c.listen, cfg.Listen)
	}
	binary := req.Binary
	if binary == "" {
		if binary, err = os.Executable(); err != nil {
			return 0, 0, err
		}
	}
	next := c.s.gen + 1
	tcp, err := c.s.tcp.File()
	if err != nil {
		return 0, 0, err
	}
	defer tcp.Close()
	udp, err := c.s.udp.File()
	if err != nil {
		return 0, 0, err
	}
	defer udp.Close()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return 0, 0, err
	}
	defer readyR.Close()

	cmd := exec.Command(binary, "run")
	cmd.Env = childEnv(os.Environ(), req.Env, next)
	cmd.ExtraFiles = []*os.File{tcp, udp, readyW}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Start()
	readyW.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("start %s: %w", binary, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	answer := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(readyR).ReadString('\n')
		answer <- strings.TrimSpace(line)
	}()
	fail := func(why string) (int, byte, error) {
		_ = cmd.Process.Kill()
		<-exited
		return 0, 0, fmt.Errorf("generation %d: %s", next, why)
	}
	select {
	case line := <-answer:
		if line != "up" {
			if line == "" {
				line = "exited before it was up"
			}
			return fail(line)
		}
	case err := <-exited:
		return 0, 0, fmt.Errorf("generation %d exited before it was up: %v", next, err)
	case <-time.After(childUpWithin):
		return fail("not up within " + childUpWithin.String())
	}
	return cmd.Process.Pid, next, nil
}

// childEnv is the parent's environment without its relay identity, plus the
// new one and the handoff's own names. systemd's names (NOTIFY_SOCKET,
// RUNTIME_DIRECTORY, STATE_DIRECTORY, …) pass through.
func childEnv(own []string, relay map[string]string, gen byte) []string {
	var out []string
	for _, kv := range own {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "EDGE_") || k == envInherit || k == envGeneration {
			continue
		}
		if _, set := relay[k]; set {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range relay {
		out = append(out, k+"="+v)
	}
	return append(out, envInherit+"=3,4,5", envGeneration+"="+strconv.Itoa(int(gen)))
}

// requestHandoff asks the generation behind control to hand over to env.
func requestHandoff(control string, req handoffRequest) (handoffReply, error) {
	conn, err := net.DialTimeout("unix", control, 5*time.Second)
	if err != nil {
		return handoffReply{}, fmt.Errorf("no running generation to hand over from: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(childUpWithin + 20*time.Second))
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

// reloadCommand is `nekko-edge reload <relay>`, systemd's ExecReload (as
// root): the relay's env from /etc/nekko-edge, handed to the running
// generation.
func reloadCommand(relay string) error {
	if relay == "" || strings.ContainsAny(relay, "/\x00") {
		return fmt.Errorf("reload: relay name %q", relay)
	}
	env, err := readEnvFile(filepath.Join("/etc/nekko-edge", relay+".env"))
	if err != nil {
		return err
	}
	reply, err := requestHandoff(filepath.Join("/run", "nekko-edge-"+relay, controlName), handoffRequest{Env: env})
	if err != nil {
		return err
	}
	fmt.Printf("nekko-edge@%s: generation %d is up (pid %d), the previous one drains\n", relay, reply.Gen, reply.PID)
	return nil
}

// readEnvFile reads KEY=VALUE lines as the hub writes them (swapIn): no
// quoting, '#' starts a comment line.
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
