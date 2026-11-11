package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

// A test binary started with envChild is an entry generation itself: its
// members are plain TCP to the exit the test runs (envExit), not Chromium.
const (
	envChild = "NEKKO_TRUNK_TEST_CHILD"
	envExit  = "NEKKO_TRUNK_TEST_EXIT"
	envDrain = "NEKKO_TRUNK_TEST_DRAIN"
)

func TestMain(m *testing.M) {
	if os.Getenv(envChild) == "" {
		os.Exit(m.Run())
	}
	if d, err := time.ParseDuration(os.Getenv(envDrain)); err == nil {
		drainLimit = d
	}
	drainEvery = 100 * time.Millisecond
	c, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(2)
	}
	exit := os.Getenv(envExit)
	dial := func(ctx context.Context, _ int) (io.ReadWriteCloser, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", exit)
	}
	if err := runGeneration(c, dial); err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// freePort is a port free for TCP and UDP both.
func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		u, err := net.ListenPacket("udp", ":"+strconv.Itoa(port))
		if err == nil {
			u.Close()
			return port
		}
	}
	t.Fatal("no port free for tcp and udp")
	return 0
}

// echoes answers TCP and UDP with what it got.
func echoes(t *testing.T) (tcp, udp string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(b[:n], from)
		}
	}()
	return ln.Addr().String(), pc.LocalAddr().String()
}

func echoTCP(t *testing.T, c net.Conn, what string) {
	t.Helper()
	data := make([]byte, 64<<10)
	_, _ = rand.Read(data)
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	go func() { _, _ = c.Write(data) }()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%s: tcp echo: %v", what, err)
	}
}

// echoUDP sends 20 datagrams and wants most of them back.
func echoUDP(t *testing.T, c net.Conn, what string) {
	t.Helper()
	back := 0
	b := make([]byte, 256)
	for i := 0; i < 20; i++ {
		msg := what + " " + strconv.Itoa(i)
		_, _ = c.Write([]byte(msg))
		_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		for {
			n, err := c.Read(b)
			if err != nil {
				break
			}
			if string(b[:n]) == msg {
				back++
				break
			}
		}
	}
	if back < 15 {
		t.Fatalf("%s: udp echo %d/20", what, back)
	}
}

// TestHandoffKeepsWhatTheEntryCarries (roadmap 2.2б): real processes, the
// test binary itself is the entry. Two handoffs in a row: after each, a TCP
// connection and a UDP flow the first generation took still echo, and new
// ones go to the newest. Then the exit drains too, while the first
// generation still carries: only the newest opens a new pool, the old ones
// take nothing new. The first generation leaves at its drain limit and says
// what it cut, and its UDP client goes on with the newest.
func TestHandoffKeepsWhatTheEntryCarries(t *testing.T) {
	tcpEcho, udpEcho := echoes(t)
	x := trunk.NewExit(trunk.Config{}, loop{tcpEcho, udpEcho})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go x.Run(ctx)
	var exit atomic.Pointer[trunk.Exit] // where new members go
	exit.Store(x)
	members, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	go func() {
		for {
			c, err := members.Accept()
			if err != nil {
				return
			}
			to := exit.Load()
			go func() { _ = to.ServeMember(c) }()
		}
	}()

	dir, err := os.MkdirTemp("/tmp", "te")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	port := freePort(t)
	env := map[string]string{
		"EDGE_TRUNK_EXIT": "203.0.113.8:443", "EDGE_TRUNK_SNI": "hk.example", "EDGE_TRUNK_KEY": "trunk-key-0123456789abcdef-xyz",
		"EDGE_TRUNK_MEMBERS": "3", "EDGE_TRUNK_FORWARD": fmt.Sprintf("%d=443", port), "EDGE_TRUNK_UDP": fmt.Sprintf("%d=443", port),
	}
	// A file, not a pipe: the generations share it, and Wait on a pipe
	// would wait for the last of them.
	out, err := os.CreateTemp(dir, "journal")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	journal := func() string { b, _ := os.ReadFile(out.Name()); return string(b) }
	defer func() {
		if t.Failed() {
			t.Log(journal())
		}
	}()
	first := exec.Command(os.Args[0], "run")
	first.Env = append(os.Environ(), envChild+"=1", envExit+"="+members.Addr().String(), envDrain+"=6s", "RUNTIME_DIRECTORY="+dir)
	for k, v := range env {
		first.Env = append(first.Env, k+"="+v)
	}
	first.Stdout, first.Stderr = out, out
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	firstGone := make(chan struct{})
	go func() { _ = first.Wait(); close(firstGone) }()
	var pids []int
	defer func() {
		_ = first.Process.Kill()
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()

	entryAddr := "127.0.0.1:" + strconv.Itoa(port)
	var oldTCP net.Conn
	for deadline := time.Now().Add(15 * time.Second); oldTCP == nil; {
		if c, err := net.Dial("tcp", entryAddr); err == nil {
			oldTCP = c
		} else if time.Now().After(deadline) {
			t.Fatalf("the first generation is not up: %v", err)
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	defer oldTCP.Close()
	echoTCP(t, oldTCP, "first generation")
	oldUDP, err := net.Dial("udp", entryAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer oldUDP.Close()
	echoUDP(t, oldUDP, "first-udp")
	for _, err := os.Stat(filepath.Join(dir, controlName)); err != nil; _, err = os.Stat(filepath.Join(dir, controlName)) {
		time.Sleep(20 * time.Millisecond)
	}

	sessionsBefore := x.Sessions()
	for gen := 2; gen <= 3; gen++ {
		reply, err := requestHandoff(filepath.Join(dir, controlName), handoffRequest{Env: env, Binary: os.Args[0]})
		if err != nil {
			t.Fatalf("handoff to %d: %v", gen, err)
		}
		if reply.Gen != gen {
			t.Fatalf("handoff: generation %d, want %d", reply.Gen, gen)
		}
		pids = append(pids, reply.PID)
		echoTCP(t, oldTCP, fmt.Sprintf("the first generation's connection after handoff %d", gen-1))
		echoUDP(t, oldUDP, fmt.Sprintf("first-udp-after-%d", gen))
		nc, err := net.Dial("tcp", entryAddr)
		if err != nil {
			t.Fatal(err)
		}
		echoTCP(t, nc, fmt.Sprintf("new connection on generation %d", gen))
		nc.Close()
		nu, err := net.Dial("udp", entryAddr)
		if err != nil {
			t.Fatal(err)
		}
		echoUDP(t, nu, fmt.Sprintf("new-udp-%d", gen))
		nu.Close()
	}
	if x.Sessions() < sessionsBefore+2 {
		t.Fatalf("the exit holds %d sessions, want a session per generation (%d before)", x.Sessions(), sessionsBefore)
	}
	if !strings.Contains(journal(), "generation 3 (pid") {
		t.Fatalf("no handoff to generation 3 in the journal")
	}

	// The exit's process is replaced too, while the first generation still
	// carries. Only the newest generation opens a session on the new one.
	next := trunk.NewExit(trunk.Config{}, loop{tcpEcho, udpEcho})
	go next.Run(ctx)
	exit.Store(next)
	x.Drain()
	for deadline := time.Now().Add(10 * time.Second); next.Sessions() < 1; {
		if time.Now().After(deadline) {
			t.Fatal("the newest generation opened no session on the exit's new process")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if n := next.Sessions(); n != 1 {
		t.Fatalf("the exit's new process holds %d sessions, want the newest generation's only", n)
	}
	if n := strings.Count(journal(), "exit drains: new session"); n != 1 {
		t.Fatalf("%d generations answered the exit's drain with a new pool, want 1", n)
	}
	echoTCP(t, oldTCP, "the first generation's connection after the exit drained")
	nc, err := net.Dial("tcp", entryAddr)
	if err != nil {
		t.Fatal(err)
	}
	echoTCP(t, nc, "new connection after the exit drained")
	nc.Close()

	// The first generation leaves at its limit; its UDP client goes on with
	// the newest, which takes the address once the mailbox is gone.
	select {
	case <-firstGone:
	case <-time.After(15 * time.Second):
		t.Fatal("the first generation did not leave at its drain limit")
	}
	if !strings.Contains(journal(), "generation 1 leaves after 6s with 1 stream(s)") {
		t.Fatalf("the first generation did not say why it left and what it cut")
	}
	echoUDP(t, oldUDP, "first-udp-after-it-left")

	// A handoff that cannot be done leaves the newest serving.
	bad := map[string]string{}
	for k, v := range env {
		bad[k] = v
	}
	bad["EDGE_TRUNK_FORWARD"] = fmt.Sprintf("%d=443", port+1)
	if _, err := requestHandoff(filepath.Join(dir, controlName), handoffRequest{Env: bad, Binary: os.Args[0]}); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("a handoff that moves a port: %v", err)
	}
	nc, err = net.Dial("tcp", entryAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	echoTCP(t, nc, "after a refused handoff")
}

func TestMailboxMessage(t *testing.T) {
	msg := encodeMsg(msgRelease, 7, 8443, "[2001:db8::1]:51000", []byte("voice"))
	kind, gen, port, addr, data, ok := decodeMsg(msg)
	if !ok || kind != msgRelease || gen != 7 || port != 8443 || addr != "[2001:db8::1]:51000" || string(data) != "voice" {
		t.Fatalf("%c %d %d %q %q %v", kind, gen, port, addr, data, ok)
	}
	if _, _, _, _, _, ok := decodeMsg(msg[:9]); ok {
		t.Fatal("a cut message read")
	}
}
