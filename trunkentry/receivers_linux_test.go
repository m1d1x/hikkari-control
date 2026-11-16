package main

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestReceiversFromTheEnv: EDGE_TRUNK_RECEIVERS and EDGE_TRUNK_RECEIVER_POOL
// as the brain reads them, and what it refuses.
func TestReceiversFromTheEnv(t *testing.T) {
	base := map[string]string{
		"EDGE_TRUNK_EXIT": "10.0.0.1:443", "EDGE_TRUNK_SNI": "hk.example", "EDGE_TRUNK_KEY": "bench-trunk-key-0123456789abcdef",
		"EDGE_TRUNK_FORWARD": "443=443", "EDGE_TRUNK_PIPE": "/opt/hk-pipe",
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
	c, err := ConfigFromEnv(with("EDGE_TRUNK_RECEIVERS", "10.0.0.2, 10.0.0.3", "EDGE_TRUNK_RECEIVER_POOL", "4"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Receivers) != 2 || c.Receivers[0] != netip.MustParseAddr("10.0.0.2") || c.ReceiverPool != 4 {
		t.Fatalf("receivers %v, pool %d", c.Receivers, c.ReceiverPool)
	}
	if c, err := ConfigFromEnv(with()); err != nil || len(c.Receivers) != 0 || c.ReceiverPool != defaultReceiverPool {
		t.Fatalf("no receivers: %v %d %v", c.Receivers, c.ReceiverPool, err)
	}
	for _, bad := range [][]string{
		{"EDGE_TRUNK_RECEIVERS", "10.0.0.1"},
		{"EDGE_TRUNK_RECEIVERS", "10.0.0.2,10.0.0.2"},
		{"EDGE_TRUNK_RECEIVERS", "::1"},
		{"EDGE_TRUNK_RECEIVERS", "10.0.0.2", "EDGE_TRUNK_RECEIVER_POOL", "0"},
		{"EDGE_TRUNK_RECEIVERS", "10.0.0.2", "EDGE_TRUNK_PIPE", ""},
	} {
		if _, err := ConfigFromEnv(with(bad...)); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
	exit, rest := netip.MustParseAddr("10.0.0.1"), []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")}
	if got := receiverOrder(exit, rest, rest[1]); got[0] != rest[1] || got[1] != exit || got[2] != rest[0] {
		t.Errorf("order starting on %s: %v", rest[1], got)
	}
	if got := startReceiver(exit, rest, "10.0.0.3"); got != rest[1] {
		t.Errorf("a generation handed 10.0.0.3 starts on %s", got)
	}
	if got := startReceiver(exit, rest, "10.9.9.9"); got != exit {
		t.Errorf("a generation handed an address of no receiver starts on %s", got)
	}
}

// receiverStand is pipeStand with two receivers: two Go exits on 127.0.0.1
// and 127.0.0.2 on the same ports, both certificates in the roots.
type receiverStand struct {
	*pipeStand
	exits [2]*exec.Cmd
}

func newReceiverStand(t *testing.T) *receiverStand {
	t.Helper()
	s := newPipeStand(t)
	goexit := os.Getenv("HK_GOEXIT")
	_, naive, _ := net.SplitHostPort(s.env["EDGE_TRUNK_EXIT"])
	roots := filepath.Join(s.dir, "roots-b.pem")
	cmd := exec.Command(goexit, "-naive", "127.0.0.2:"+naive, "-naive-roots", roots,
		"-wire", "127.0.0.2:"+strconv.Itoa(s.wire), "-pool", "127.0.0.2:"+strconv.Itoa(freePort(t)))
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var b []byte
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(roots); err == nil && len(raw) > 0 {
			b = raw
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second goexit wrote no certificate in 5 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	a, err := os.ReadFile(s.env["EDGE_TRUNK_ROOTS"])
	if err != nil {
		t.Fatal(err)
	}
	both := filepath.Join(s.dir, "roots-both.pem")
	if err := os.WriteFile(both, append(append(a, '\n'), b...), 0o644); err != nil {
		t.Fatal(err)
	}
	s.env["EDGE_TRUNK_ROOTS"] = both
	s.env["EDGE_TRUNK_RECEIVERS"] = "127.0.0.2"
	s.env["EDGE_TRUNK_RECEIVER_POOL"] = "2"
	return &receiverStand{pipeStand: s, exits: [2]*exec.Cmd{nil, cmd}}
}

// waitWritten waits until the entry's log says want.
func (s *pipeStand) waitWritten(t *testing.T, want string, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for !strings.Contains(s.written(), want) {
		if time.Since(start) > within {
			t.Fatalf("the entry did not say %q in %s", want, within)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return time.Since(start)
}

// TestTheBrainMovesOffADeadReceiverAndAReloadStartsOnTheNewOne: an entry
// over hk-pipe with receivers 127.0.0.1 (EDGE_TRUNK_EXIT) and 127.0.0.2,
// Chromium's pool to both. The first receiver dies under a call: the brain
// moves to the second by itself and says so in its line, a new stream and a
// new call go through; a reload starts the next generation on the second
// receiver, not on the dead one it was first given.
func TestTheBrainMovesOffADeadReceiverAndAReloadStartsOnTheNewOne(t *testing.T) {
	s := newReceiverStand(t)
	s.start(t)
	s.waitWritten(t, "receiver 1 at 127.0.0.2: a standby session", 20*time.Second)
	time.Sleep(3 * time.Second) // the standby's members up
	k := startCall(t, s.udp)
	time.Sleep(2 * time.Second)
	if strings.Contains(s.written(), "receiver moved") {
		t.Fatal("moved with both receivers alive")
	}

	// The first receiver's process goes: its relay, wire and pool with it.
	killFirstExit(t, s.env["EDGE_TRUNK_EXIT"])
	took := s.waitWritten(t, "trunk: receiver moved from 127.0.0.1 to 127.0.0.2", 15*time.Second)
	t.Logf("moved %v after the first receiver died", took)
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(s.tcp))
	if err != nil {
		t.Fatal(err)
	}
	echoTCP(t, c, "a stream after the move")
	c.Close()
	time.Sleep(time.Second)
	sent, back, gap := k.end()
	t.Logf("the call across the move: %d/%d back, the longest pause %s", back, sent, gap)
	if gap > 10*time.Second {
		t.Errorf("the call paused %s", gap)
	}

	self, _ := os.Executable()
	reply, err := requestHandoff(filepath.Join(s.dir, controlName), handoffRequest{Env: s.env, Binary: self})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(reply.PID); err == nil {
			_ = p.Kill()
		}
	})
	s.waitWritten(t, "generation 2, exit 127.0.0.2:", 10*time.Second)
	u, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(s.udp))
	if err != nil {
		t.Fatal(err)
	}
	echoUDP(t, u, "a call after the reload")
	u.Close()
}

// killFirstExit ends the goexit newPipeStand started with its relay at
// exit.
func killFirstExit(t *testing.T, exit string) {
	t.Helper()
	out, err := exec.Command("pkill", "-f", "--", "-naive "+exit+" ").CombinedOutput()
	if err != nil {
		t.Fatalf("pkill: %v %s", err, out)
	}
}
