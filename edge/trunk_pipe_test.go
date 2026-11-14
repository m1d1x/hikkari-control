package main

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

// hk-exit under the relay (trunk_pipe.go; roadmap 4.6, R6.4.3). Needs a build
// of hikkari-protocol rust/exit: HK_EXIT=/path/to/hk-exit.

func hkExitBin(t *testing.T) string {
	bin := os.Getenv("HK_EXIT")
	if bin == "" {
		t.Skip("HK_EXIT is not set: no hk-exit build")
	}
	return bin
}

func TestTrunkPipeFromEnv(t *testing.T) {
	const goKey, pipeKey = "trunk-key-0123456789abcdef-xyz", "pipe-key-0123456789abcdef-xyzw"
	base := map[string]string{
		"EDGE_PROTO": protoHikkari, "EDGE_LISTEN": "0.0.0.0:443", "EDGE_HIKKARI_HOST": "hk.example",
		"EDGE_HIKKARI_USER": "probe", "EDGE_HIKKARI_PASS": "probe-secret-long-enough", "EDGE_HIKKARI_TRUNK_PORTS": "8443",
	}
	with := func(kv ...string) func(string) string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	// Only hk-exit's keys: the ports are its, the Go exit has none.
	cfg, err := naiveFromEnv(with("EDGE_HIKKARI_TRUNK_PIPE_KEYS", pipeKey, "EDGE_HIKKARI_TRUNK_PIPE_WIRES", "udp=20461,h3dgram=20460",
		"EDGE_HIKKARI_TRUNK_PIPE_BIN", "/opt/hk-exit"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Trunk.Keys) != 0 || !cfg.Trunk.Ports[8443] || !cfg.Pipe.admits(pipeKey) || cfg.Pipe.admits(goKey) ||
		cfg.Pipe.Wires["udp"] != 20461 || cfg.Pipe.Wires["h3dgram"] != 20460 || cfg.Pipe.Bin != "/opt/hk-exit" {
		t.Fatalf("pipe only: %+v %+v", cfg.Trunk, cfg.Pipe)
	}
	// Both exits, each its own keys.
	cfg, err = naiveFromEnv(with("EDGE_HIKKARI_TRUNK_KEYS", goKey, "EDGE_HIKKARI_TRUNK_PIPE_KEYS", pipeKey))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Trunk.admits(goKey) || cfg.Trunk.admits(pipeKey) || !cfg.Pipe.admits(pipeKey) || len(cfg.Pipe.Wires) != 0 {
		t.Fatalf("both: %+v %+v", cfg.Trunk, cfg.Pipe)
	}
	for name, get := range map[string]func(string) string{
		"a key of both exits":   with("EDGE_HIKKARI_TRUNK_KEYS", goKey, "EDGE_HIKKARI_TRUNK_PIPE_KEYS", goKey),
		"wires without keys":    with("EDGE_HIKKARI_TRUNK_PIPE_WIRES", "udp=20461"),
		"a wire of no kind":     with("EDGE_HIKKARI_TRUNK_PIPE_KEYS", pipeKey, "EDGE_HIKKARI_TRUNK_PIPE_WIRES", "quic=20448"),
		"one port twice":        with("EDGE_HIKKARI_TRUNK_PIPE_KEYS", pipeKey, "EDGE_HIKKARI_TRUNK_PIPE_WIRES", "udp=20461,h3dgram=20461"),
		"a key not a key":       with("EDGE_HIKKARI_TRUNK_PIPE_KEYS", "short"),
		"the Go wires alone":    with("EDGE_HIKKARI_TRUNK_PIPE_KEYS", pipeKey, "EDGE_HIKKARI_TRUNK_WIRES", "udp=20447"),
		"hk-exit without ports": with("EDGE_HIKKARI_TRUNK_PIPE_KEYS", pipeKey, "EDGE_HIKKARI_TRUNK_PORTS", ""),
	} {
		if _, err := naiveFromEnv(get); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	naive := func(k string) string {
		return map[string]string{"EDGE_LISTEN": "0.0.0.0:443", "EDGE_NAIVE_HOST": "nv.example", "EDGE_NAIVE_USER": "probe",
			"EDGE_NAIVE_PASS": "probe-secret-long-enough", "EDGE_HIKKARI_TRUNK_PIPE_KEYS": pipeKey}[k]
	}
	if _, err := naiveFromEnv(naive); err == nil {
		t.Error("hk-exit's keys on a Naive relay: no error")
	}
}

// poolCall is a call over the trunk's pool to the UDP echo: a numbered
// datagram every 20 ms, the longest pause between answers by the monotonic
// clock.
type poolCall struct {
	mu        sync.Mutex
	start     time.Time
	last, gap time.Duration
	sent, got int
	stop      chan struct{}
	done      chan struct{}
}

func startPoolCall(s *trunk.Session, port uint16) *poolCall {
	c := &poolCall{start: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	f := s.OpenFlow(port, func([]byte) {
		now := time.Since(c.start)
		c.mu.Lock()
		if c.last != 0 && now-c.last > c.gap {
			c.gap = now - c.last
		}
		c.last = now
		c.got++
		c.mu.Unlock()
	}, nil)
	go func() {
		defer close(c.done)
		defer f.Close()
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		b := make([]byte, 64)
		for n := uint32(1); ; n++ {
			select {
			case <-c.stop:
				return
			case <-tk.C:
			}
			binary.BigEndian.PutUint32(b, n)
			f.Send(b)
			c.mu.Lock()
			c.sent++
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *poolCall) hangUp() (sent, got int, gap time.Duration) {
	close(c.stop)
	<-c.done
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent, c.got, c.gap
}

// TestHandoffKeepsThePipesTrunk: an entry whose key is hk-exit's reaches it
// through the relay's TLS (each member a CONNECT, linked to hk-exit by a
// socket pair), and a stream goes on through three handoffs of the relay in
// a row, a call on the pool through the last two: each generation's hk-exit
// adopts the sockets and the previous one drains to it. The call hears no
// pause over 150 ms, the stream echoes after each handoff, a stream after
// each is the newest hk-exit's. The first generation leaves once its stream
// ended.
func TestHandoffKeepsThePipesTrunk(t *testing.T) {
	if testing.Short() {
		t.Skip("starts relay processes")
	}
	bin := hkExitBin(t)
	const pipeKey = "pipe-key-0123456789abcdef-xyzw"
	const probeUser, probePass = "probe", "probe-secret-long-enough"
	certPEM, keyPEM := hikkariTestCert(t, "hk.example")
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(certPEM))

	echoTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoTCP.Close()
	go func() {
		for {
			c, err := echoTCP.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	echoPort := echoTCP.Addr().(*net.TCPAddr).Port
	echoUDP, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(echoPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer echoUDP.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := echoUDP.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = echoUDP.WriteTo(b[:n], from)
		}
	}()

	relay := freeAddr(t)
	run, err := os.MkdirTemp("/tmp", "hkpp") // a unix socket path must be short
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	storage := t.TempDir()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>site</html>"), 0o644)
	env := map[string]string{
		"EDGE_PROTO": protoHikkari, "EDGE_LISTEN": relay, "EDGE_HIKKARI_HOST": "hk.example",
		"EDGE_HIKKARI_USER": probeUser, "EDGE_HIKKARI_PASS": probePass, "EDGE_HIKKARI_ROOT": root,
		"EDGE_HIKKARI_TRUNK_PIPE_KEYS": pipeKey, "EDGE_HIKKARI_TRUNK_PORTS": strconv.Itoa(echoPort),
		"EDGE_HIKKARI_TRUNK_PIPE_WIRES": fmt.Sprintf("udp=%d", freePort(t, "udp")), "EDGE_HIKKARI_TRUNK_PIPE_BIN": bin,
		"EDGE_TEST_GENERATION": "1", "EDGE_TEST_CERT_PEM": certPEM, "EDGE_TEST_KEY_PEM": keyPEM,
	}
	first := exec.Command(os.Args[0])
	first.Env = append(os.Environ(), "RUNTIME_DIRECTORY="+run, "STATE_DIRECTORY="+storage)
	for k, v := range env {
		first.Env = append(first.Env, k+"="+v)
	}
	first.Stdout, first.Stderr = os.Stderr, os.Stderr
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	firstGone := make(chan struct{})
	go func() { _ = first.Wait(); close(firstGone) }()
	pids := []int{first.Process.Pid}
	defer func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	control := filepath.Join(run, controlName)
	waitUntil(t, 15*time.Second, "first generation up", func() bool {
		c, err := net.Dial("unix", control)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ports, _ := trunk.NewPortBook(46000, 47000, time.Minute)
	entry, err := trunk.NewEntry(trunk.Config{}, 4, func(ctx context.Context, port int) (io.ReadWriteCloser, error) {
		return goMember(relay, roots, pipeKey, port)
	}, ports, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	go entry.Run(ctx)
	waitUntil(t, 15*time.Second, "trunk pool at hk-exit", func() bool { return entry.Session().Stats().Members == 4 })
	cascaded, edgeSide := net.Pipe()
	go entry.Session().Carry(edgeSide, uint16(echoPort))
	defer cascaded.Close()
	echoes(t, "first generation: trunk stream", cascaded, 256<<10)

	var call *poolCall
	for i := 1; i <= 3; i++ {
		// The session before the handoff keeps its members while it retires.
		prev := entry.Session()
		reply, err := requestHandoff(control, handoffRequest{Env: env, Binary: os.Args[0]})
		if err != nil {
			t.Fatalf("handoff %d: %v", i, err)
		}
		pids = append(pids, reply.PID)
		stage := fmt.Sprintf("after handoff %d", i)
		echoes(t, stage+": trunk stream", cascaded, 256<<10)
		waitUntil(t, 15*time.Second, stage+": trunk on a new session", func() bool {
			s := entry.Session()
			return s != prev && s.Stats().Members == 4
		})
		c2, e2 := net.Pipe()
		go entry.Session().Carry(e2, uint16(echoPort))
		echoes(t, stage+": new trunk stream", c2, 64<<10)
		c2.Close()
		// The call starts on the second generation's session: a flow ends
		// only idle (2 minutes at the exit), and the first generation is to
		// leave once its stream ends.
		if call == nil {
			call = startPoolCall(entry.Session(), uint16(echoPort))
		}
		time.Sleep(time.Second)
	}
	sent, got, gap := call.hangUp()
	t.Logf("the call through two handoffs: sent %d, answered %d, longest pause %v", sent, got, gap.Round(time.Millisecond))
	if got < sent-sent/100 || gap > 150*time.Millisecond {
		t.Errorf("the call: %d of %d answered, a pause of %v (want 99%% and no pause over 150 ms)", got, sent, gap)
	}
	echoes(t, "after three handoffs: trunk stream", cascaded, 256<<10)

	cascaded.Close()
	select {
	case <-firstGone:
	case <-time.After(30 * time.Second):
		t.Fatal("the first generation did not leave after its trunk's stream ended")
	}
}
