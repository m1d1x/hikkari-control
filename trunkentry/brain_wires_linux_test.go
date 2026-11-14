package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestABrainFollowsItsWiresFileWithoutANewProcess: the hub changes the
// wires file of a running brain over hk-pipe four times, under a call (one
// copy, the voice class) and a stream of 64 MiB on the pool: voice held on
// the udp wire, its members 2 → 3 under the call, the wire taken away from
// under the call, the wire back with 2 and steered. The pipe carries each
// change (its tick says the wire's members, or no wire, and the voice
// route), the brain says so without a new generation, the call loses
// nothing to speak of and pauses no longer than 150 ms, the stream comes
// back byte for byte, and the process is the one that started.
func TestABrainFollowsItsWiresFileWithoutANewProcess(t *testing.T) {
	s := newPipeStand(t)
	entry := s.start(t)
	pid := entry.Process.Pid
	k := startCall(t, s.udp)
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(s.tcp))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const size = 64 << 20
	data := make([]byte, 1<<20)
	_, _ = rand.Read(data)
	errc := make(chan error, 1)
	go func() {
		for sent := 0; sent < size; sent += len(data) {
			if _, err := conn.Write(data); err != nil {
				errc <- err
				return
			}
			time.Sleep(400 * time.Millisecond) // the stream spans the changes
		}
		errc <- nil
	}()
	read := make(chan error, 1)
	go func() {
		got := make([]byte, len(data))
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
	s.waitWire(t, "the start", 3, 2, "")
	udp := func(members int) []wireSpec {
		return []wireSpec{{Name: "udp", Kind: "udp", Port: s.wire, Members: members}}
	}
	steps := []struct {
		what  string
		file  wiresSpec
		wire  int    // the udp wire's members in the pipe's tick, 0 none
		voice string // the voice class's wire in the tick, "" any
	}{
		{"voice held on udp", wiresSpec{Steer: true, Wires: udp(2), Voice: "udp"}, 2, "voice:3"},
		{"udp members 2 → 3 under the call", wiresSpec{Steer: true, Wires: udp(3), Voice: "udp"}, 3, "voice:3"},
		{"no wire under the call", wiresSpec{Steer: true, Wires: []wireSpec{}}, 0, ""},
		{"udp back with 2, steered", wiresSpec{Steer: true, Wires: udp(2)}, 2, ""},
	}
	for _, st := range steps {
		raw, _ := json.Marshal(st.file)
		if err := os.WriteFile(s.env["EDGE_TRUNK_WIRES_FILE"], raw, 0o644); err != nil {
			t.Fatal(err)
		}
		s.waitWire(t, st.what, 3, st.wire, st.voice)
		t.Logf("%s: the pipe carries it", st.what)
	}
	sent, back, gapMax := k.end()
	if gapMax > 150*time.Millisecond || back < sent-sent/100 {
		t.Errorf("the call through the changes: %d/%d back, the longest pause %s; want 99%% back, no pause over 150 ms", back, sent, gapMax)
	}
	t.Logf("the call through the changes: %d/%d back, the longest pause %s", back, sent, gapMax)
	if err := <-errc; err != nil {
		t.Fatalf("the stream's writes: %v", err)
	}
	if err := <-read; err != nil {
		t.Fatalf("the stream through the changes: %v", err)
	}
	t.Logf("the stream through the changes: %d MiB byte for byte", size>>20)
	logs := s.written()
	if n := strings.Count(logs, "no new process"); n != len(steps) {
		t.Errorf("the brain said %d changes without a new process, want %d", n, len(steps))
	}
	if strings.Contains(logs, "generation 2") || entry.Process.Signal(syscall.Signal(0)) != nil {
		t.Errorf("a new generation came up, or the entry %d ended", pid)
	}
}

// pipeTick finds the wires of the pipe's tick lines: "| wire 3 up=2/2".
var pipeTick = regexp.MustCompile(`(?m)^hk-pipe: flows=.*$`)
var tickWire = regexp.MustCompile(`\| wire (\d+) up=(\d+)/(\d+)`)

// waitWire waits until a tick of the pipe since the call says wire id has
// members members (0: the tick names no such wire), and has route in its
// route when it is set, within 40 s: the file is read every 10 s, the pipe
// ticks every 10 s.
func (s *pipeStand) waitWire(t *testing.T, what string, id, members int, route string) {
	t.Helper()
	from := len(s.written())
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		ticks := pipeTick.FindAllString(s.written()[from:], -1)
		if len(ticks) > 0 {
			has := 0
			for _, m := range tickWire.FindAllStringSubmatch(ticks[len(ticks)-1], -1) {
				if m[1] == strconv.Itoa(id) {
					has, _ = strconv.Atoi(m[3])
				}
			}
			if has == members && strings.Contains(ticks[len(ticks)-1], "route=") && (route == "" || strings.Contains(ticks[len(ticks)-1], route)) {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s: no tick of the pipe with wire %d of %d members in 40 s", what, id, members)
}

// tail is the last n bytes of s.
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
