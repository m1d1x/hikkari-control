package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// The brain over hk-pipe across two processes (R5): the test binary is the
// entry, as in TestHandoffKeepsWhatTheEntryCarries, its pipe the glibc
// hk-pipe with libcronet (HK_PIPE, HK_CRONET), its pool Chromium's CONNECT
// to the bench's Go exit (HK_GOEXIT: goexit -naive; hikkari-protocol
// rust/README.md, «Chromium members»), its udp wire to the same exit.
// Without them the test is skipped.

// pipeStand is a Go exit with its relay channel and udp wire, and the env
// of an entry over hk-pipe forwarding a TCP and a UDP port to echoes.
type pipeStand struct {
	env      map[string]string
	dir      string
	tcp, udp int
	// wire is the exit's udp wire port; logs the file the entry and its
	// pipe write to (a file, not a pipe: the next generation inherits it, and
	// a pipe would hold the first one's Wait until the last generation ends).
	wire int
	logs string
}

// written is what the entry and its pipe wrote so far.
func (s *pipeStand) written() string {
	raw, _ := os.ReadFile(s.logs)
	return string(raw)
}

func newPipeStand(t *testing.T) *pipeStand {
	t.Helper()
	pipe, cronet, goexit := os.Getenv("HK_PIPE"), os.Getenv("HK_CRONET"), os.Getenv("HK_GOEXIT")
	if pipe == "" || cronet == "" || goexit == "" {
		t.Skip("HK_PIPE, HK_CRONET or HK_GOEXIT is not set: no hk-pipe with Chromium and no exit for it")
	}
	dir := t.TempDir()
	// The brain finds libcronet beside hk-pipe, as the hub stages them.
	bin := filepath.Join(dir, "hk-pipe")
	for from, to := range map[string]string{pipe: bin, cronet: filepath.Join(dir, "libcronet.so")} {
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, raw, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	naive, wire := freePort(t), freePort(t)
	roots := filepath.Join(dir, "roots.pem")
	cmd := exec.Command(goexit, "-naive", "127.0.0.1:"+strconv.Itoa(naive), "-naive-roots", roots,
		"-wire", "127.0.0.1:"+strconv.Itoa(wire), "-pool", "127.0.0.1:"+strconv.Itoa(freePort(t)))
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(roots); err == nil && len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("goexit wrote no certificate in 5 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	wires, _ := json.Marshal(wiresSpec{Steer: true, Wires: []wireSpec{{Name: "udp", Kind: "udp", Port: wire, Members: 2}}})
	wiresFile := filepath.Join(dir, "wires.json")
	if err := os.WriteFile(wiresFile, wires, 0o644); err != nil {
		t.Fatal(err)
	}
	tcpEcho, udpEcho := echoes(t)
	_, tcpTo, _ := net.SplitHostPort(tcpEcho)
	_, udpTo, _ := net.SplitHostPort(udpEcho)
	s := &pipeStand{dir: dir, tcp: freePort(t), udp: freePort(t), wire: wire, logs: filepath.Join(dir, "entry.log")}
	s.env = map[string]string{
		"EDGE_TRUNK_EXIT": "127.0.0.1:" + strconv.Itoa(naive), "EDGE_TRUNK_SNI": "hk.example",
		"EDGE_TRUNK_KEY": "bench-trunk-key-0123456789abcdef", "EDGE_TRUNK_MEMBERS": "4",
		"EDGE_TRUNK_FORWARD": strconv.Itoa(s.tcp) + "=" + tcpTo, "EDGE_TRUNK_UDP": strconv.Itoa(s.udp) + "=" + udpTo,
		"EDGE_TRUNK_UDP_ONE_COPY": strconv.Itoa(s.udp),
		"EDGE_TRUNK_PIPE":         bin, "EDGE_TRUNK_ROOTS": roots, "EDGE_TRUNK_WIRES_FILE": wiresFile,
		"EDGE_TRUNK_SOURCE_PORTS": "40000-40999",
	}
	return s
}

// start runs the first generation, the test binary as the entry.
func (s *pipeStand) start(t *testing.T) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "run")
	cmd.Env = append(os.Environ(), envChild+"=1", "RUNTIME_DIRECTORY="+s.dir)
	for k, v := range s.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	logs, err := os.OpenFile(s.logs, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	cmd.Stdout, cmd.Stderr = logs, logs
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the entry wrote:\n%s", tail(s.written(), 6000))
		}
	})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(s.dir, controlName)); err == nil {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatal("the entry did not come up in 30 s")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// call is a client's call through the entry: a datagram every 20 ms, the
// longest pause between answers and how many came back.
type call struct {
	mu       sync.Mutex
	sent     int
	got      int
	last     time.Time
	gapMax   time.Duration
	gapAt    time.Time
	stop     chan struct{}
	finished chan struct{}
}

func startCall(t *testing.T, port int) *call {
	t.Helper()
	c, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	k := &call{stop: make(chan struct{}), finished: make(chan struct{})}
	go func() {
		b := make([]byte, 256)
		for {
			if _, err := c.Read(b); err != nil {
				return
			}
			now := time.Now()
			k.mu.Lock()
			if !k.last.IsZero() && now.Sub(k.last) > k.gapMax {
				k.gapMax, k.gapAt = now.Sub(k.last), now
			}
			k.last, k.got = now, k.got+1
			k.mu.Unlock()
		}
	}()
	go func() {
		defer close(k.finished)
		defer c.Close()
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		frame := make([]byte, 160)
		for {
			select {
			case <-k.stop:
				time.Sleep(500 * time.Millisecond) // the last answers
				return
			case <-tk.C:
				_, _ = c.Write(frame)
				k.mu.Lock()
				k.sent++
				k.mu.Unlock()
			}
		}
	}()
	return k
}

func (k *call) end() (sent, got int, gapMax time.Duration) {
	close(k.stop)
	<-k.finished
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sent, k.got, k.gapMax
}

// TestABrainOverThePipeReloadsIntoAnotherProcessUnderACallAndAStream: the
// first brain's pipe carries a call (one copy, the voice class, on the udp
// wire by the selector) and a stream of 256 MiB on Chromium's pool; a
// reload starts the next brain on the same sockets with the new end of the
// pipes' link, its pipe adopts, the old one drains. The call pauses no
// longer than 150 ms and loses nothing to speak of, the stream comes back
// byte for byte, and a new call and stream after the reload are the new
// pipe's.
func TestABrainOverThePipeReloadsIntoAnotherProcessUnderACallAndAStream(t *testing.T) {
	s := newPipeStand(t)
	first := s.start(t)
	k := startCall(t, s.udp)
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(s.tcp))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const size = 256 << 20
	data := make([]byte, 1<<20)
	_, _ = rand.Read(data)
	errc := make(chan error, 1)
	go func() {
		for sent := 0; sent < size; sent += len(data) {
			if _, err := conn.Write(data); err != nil {
				errc <- err
				return
			}
		}
		errc <- nil
	}()
	got := make([]byte, len(data))
	read := make(chan error, 1)
	go func() {
		for n := 0; n < size; n += len(got) {
			if _, err := io.ReadFull(conn, got); err != nil {
				read <- err
				return
			}
			if !bytes.Equal(got, data) {
				read <- io.ErrUnexpectedEOF
				return
			}
		}
		read <- nil
	}()
	time.Sleep(3 * time.Second)
	self, _ := os.Executable()
	reply, err := requestHandoff(filepath.Join(s.dir, controlName), handoffRequest{Env: s.env, Binary: self})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	t.Logf("reload: generation %d (pid %d) is up", reply.Gen, reply.PID)
	t.Cleanup(func() {
		if p, err := os.FindProcess(reply.PID); err == nil {
			_ = p.Kill()
		}
	})
	time.Sleep(3 * time.Second)
	sent, back, gapMax := k.end()
	if gapMax > 150*time.Millisecond || back < sent-sent/100 {
		t.Errorf("the call through the reload: %d/%d back, the longest pause %s; want 99%% back, no pause over 150 ms", back, sent, gapMax)
	}
	t.Logf("the call through the reload: %d/%d back, the longest pause %s", back, sent, gapMax)
	if err := <-errc; err != nil {
		t.Fatalf("the stream's writes: %v", err)
	}
	if err := <-read; err != nil {
		t.Fatalf("the stream through the reload: %v", err)
	}
	t.Logf("the stream through the reload: %d MiB byte for byte", size>>20)
	conn.Close() // the old pipe's last stream ends
	c2, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(s.tcp))
	if err != nil {
		t.Fatal(err)
	}
	echoTCP(t, c2, "a stream after the reload")
	c2.Close()
	u2, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(s.udp))
	if err != nil {
		t.Fatal(err)
	}
	echoUDP(t, u2, "a call after the reload")
	u2.Close()
	// The first generation keeps its call's flow until it ends unused (2
	// min after the reload), then its pipe says drained and the process
	// leaves.
	done := make(chan error, 1)
	go func() { done <- first.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the first generation left: %v; want it to end clean", err)
		}
	case <-time.After(3 * time.Minute):
		t.Errorf("the first generation still runs 3 min after the reload")
	}
}

// TestThePipeCarriesTheUdpAndTheQuicWire: the brain gives the pipe the udp
// wire and Chromium's QUIC wire of the hub's file, the latter with the
// trunk's SNI, the libcronet beside hk-pipe, the roots and the pool's book
// of ports (R6.3).
func TestThePipeCarriesTheUdpAndTheQuicWire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wires.json")
	t.Setenv("EDGE_TRUNK_WIRES_FILE", path)
	raw := `{"steer": true, "wires": [{"name": "udp", "kind": "udp", "port": 20447, "members": 2},
		{"name": "quic", "kind": "quic", "port": 20448, "members": 4},
		{"name": "h3dgram", "kind": "h3dgram", "port": 4445, "members": 4}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Config{SNI: "hk.example", Key: "k", Pipe: "/usr/local/lib/nekko-trunk-pipe/hk-pipe", PortLo: 10000, PortHi: 19999}
	exit, _ := exitOf(Config{Exit: "192.0.2.1:443"})
	ws, f, err := pipeWires(c, exit, "PEM")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Steer || len(ws) != 2 {
		t.Fatalf("steer %v, %d wires, want the udp and the h3dgram ones", f.Steer, len(ws))
	}
	h := ws[1]
	if h.ID != 6 || h.Exit.String() != "192.0.2.1:4445" || h.H3 == nil || ws[0].H3 != nil {
		t.Fatalf("wire %+v", h)
	}
	want := pipectl.H3Form{SNI: "hk.example", Cronet: "/usr/local/lib/nekko-trunk-pipe/libcronet.so", Roots: "PEM", Ports: [2]uint16{10000, 19999}}
	if *h.H3 != want || wireShape(h) != "browser" || wireShape(ws[0]) != "random" {
		t.Fatalf("form %+v, want %+v", *h.H3, want)
	}
}
