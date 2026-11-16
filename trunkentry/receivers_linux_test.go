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

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
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

// waitCount waits until written says want n times.
func waitCount(t *testing.T, written func() string, want string, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for strings.Count(written(), want) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%q %d times within %s: %d", want, n, within, strings.Count(written(), want))
		}
		time.Sleep(100 * time.Millisecond)
	}
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

// The hub's receiver file (P5): an IPv4 address or nothing.
func TestTheReceiverFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDGE_TRUNK_WIRES_FILE", filepath.Join(dir, "nekko-trunk-entry.env.wires.json"))
	path := receiverFilePath()
	if path != filepath.Join(dir, "nekko-trunk-entry.env.receiver") {
		t.Fatalf("path %s", path)
	}
	if raw, ip, err := readReceiverFile(path); raw != nil || ip.IsValid() || err != nil {
		t.Fatalf("missing file: %q %v %v", raw, ip, err)
	}
	for text, want := range map[string]string{"10.0.0.2\n": "10.0.0.2", "  \n": ""} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		_, ip, err := readReceiverFile(path)
		if err != nil || (want == "" && ip.IsValid()) || (want != "" && ip.String() != want) {
			t.Fatalf("%q: %v %v", text, ip, err)
		}
	}
	for _, bad := range []string{"::1", "ams3", "10.0.0.2,10.0.0.3"} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readReceiverFile(path); err == nil {
			t.Fatalf("%q read", bad)
		}
	}
}

// A receiver is heard by the judge's rule, its proofs aged by the time since
// the pipe said them.
func TestAReceiverIsHeardAsTheJudgeHearsIt(t *testing.T) {
	w := func(up int, ago time.Duration) pipectl.WireTick { return pipectl.WireTick{Up: up, ProofAgo: ago} }
	for _, tc := range []struct {
		wires []pipectl.WireTick
		age   time.Duration
		heard bool
	}{
		{[]pipectl.WireTick{w(2, 100*time.Millisecond)}, 0, true},
		{[]pipectl.WireTick{w(2, 100*time.Millisecond)}, 1500 * time.Millisecond, false},
		{[]pipectl.WireTick{w(0, 100*time.Millisecond)}, 0, false},
		{[]pipectl.WireTick{w(2, -1)}, 0, false},
		{[]pipectl.WireTick{w(0, 0), w(1, time.Second)}, 0, true},
	} {
		if got := receiverHeard(tc.wires, tc.age); got != tc.heard {
			t.Errorf("%+v aged %s: heard %v", tc.wires, tc.age, got)
		}
	}
	if got := receiverPart(netip.MustParseAddr("10.0.0.2"), "standby", []pipectl.WireTick{w(2, 300*time.Millisecond), w(6, -1)}, 200*time.Millisecond); got != "10.0.0.2 standby up=8 proof_ms=500" {
		t.Errorf("part %q", got)
	}
}

// TestTheHubMovesTheEntryByHand: the hub's file names the second receiver
// under a call and the entry moves there (why "by hand"), a stream goes
// through it; the file back on the first moves the entry back; a receiver
// that is not the entry's is refused and nothing moves. The file naming the
// active receiver is answered, and written again with the same bytes it is
// heard again: after the judge moved the entry, the file still names the
// receiver it left (aezamsk1 16.11, the hub's move back went unheard).
func TestTheHubMovesTheEntryByHand(t *testing.T) {
	s := newReceiverStand(t)
	s.start(t)
	s.waitWritten(t, "receiver 1 at 127.0.0.2: a standby session", 20*time.Second)
	time.Sleep(3 * time.Second) // the standby's members up
	file := strings.TrimSuffix(s.env["EDGE_TRUNK_WIRES_FILE"], ".wires.json") + ".receiver"
	k := startCall(t, s.udp)
	time.Sleep(2 * time.Second)

	if err := os.WriteFile(file, []byte("127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.waitWritten(t, "receiver 127.0.0.1 by hand: already the active one", 5*time.Second)
	time.Sleep(1500 * time.Millisecond)

	if err := os.WriteFile(file, []byte("127.0.0.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	took := s.waitWritten(t, "trunk: receiver moved from 127.0.0.1 to 127.0.0.2: by hand", 5*time.Second)
	t.Logf("moved by hand %v after the file", took)
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(s.tcp))
	if err != nil {
		t.Fatal(err)
	}
	echoTCP(t, c, "a stream after the move by hand")
	c.Close()
	time.Sleep(2 * time.Second)

	if err := os.WriteFile(file, []byte("127.0.0.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.waitWritten(t, "receiver 127.0.0.9 by hand refused", 5*time.Second)
	time.Sleep(time.Second)

	if err := os.WriteFile(file, []byte("127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.waitWritten(t, "trunk: receiver moved from 127.0.0.2 to 127.0.0.1: by hand", 5*time.Second)
	time.Sleep(1500 * time.Millisecond)

	// The same bytes again: a word of its own, answered.
	if err := os.WriteFile(file, []byte("127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitCount(t, s.written, "receiver 127.0.0.1 by hand: already the active one", 2, 5*time.Second)
	time.Sleep(time.Second)
	sent, back, gap := k.end()
	t.Logf("the call across two moves by hand: %d/%d back, the longest pause %s", back, sent, gap)
	if gap > 2*time.Second {
		t.Errorf("the call paused %s across moves by hand", gap)
	}
	if strings.Count(s.written(), "receiver moved") != 2 {
		t.Errorf("moves: %d, want the two by hand", strings.Count(s.written(), "receiver moved"))
	}
}
