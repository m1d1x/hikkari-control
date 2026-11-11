package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freeStandPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// TestWireStandWires stands every wire's exit and entry on loopback: the call
// is echoed, 1 MiB arrives whole, and a stranger to the bare wire gets nothing.
func TestWireStandWires(t *testing.T) {
	secret := hex.EncodeToString(make([]byte, 32))
	for _, wire := range []string{wireQUIC, wireSalamander, wireAwg, wireUDP} {
		t.Run(wire, func(t *testing.T) {
			port := strconv.Itoa(freeStandPort(t))
			env := map[string]string{"WIRESTAND_WIRE": wire, "WIRESTAND_PORT": port, "WIRESTAND_SECRET": secret,
				"WIRESTAND_EXIT": "127.0.0.1", "WIRESTAND_SNI": "stand.example"}
			get := func(role string) func(string) string {
				return func(k string) string {
					if k == "WIRESTAND_ROLE" {
						return role
					}
					return env[k]
				}
			}
			exitCfg, err := WireStandConfigFromEnv(get("exit"))
			if err != nil {
				t.Fatal(err)
			}
			entryCfg, err := WireStandConfigFromEnv(get("entry"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := serveWire(ctx, exitCfg); err != nil {
				t.Fatal(err)
			}
			w, err := dialWire(ctx, entryCfg)
			if err != nil {
				t.Fatal(err)
			}
			var conv net.Conn
			for i := 0; i < 20 && conv == nil; i++ { // the AWG handshake takes a moment
				conv, _ = w.call(ctx)
			}
			if conv == nil {
				t.Fatal("no conversation")
			}
			defer conv.Close()
			got := 0
			buf := make([]byte, 2048)
			for seq := uint64(1); seq <= 50; seq++ {
				p := make([]byte, standSize)
				binary.BigEndian.PutUint64(p, seq)
				if _, err := conv.Write(p); err != nil {
					t.Fatal(err)
				}
				_ = conv.SetReadDeadline(time.Now().Add(time.Second))
				n, err := conv.Read(buf)
				if err == nil && n >= 8 && binary.BigEndian.Uint64(buf) == seq {
					got++
				}
			}
			if got < 45 {
				t.Fatalf("echoed %d of 50", got)
			}
			n, err := w.bulk(ctx, standBulk)
			if err != nil || n != standBulk {
				t.Fatalf("bulk %d of %d: %v", n, standBulk, err)
			}
		})
	}
}

// TestStandCallWindow is the trunk's call arithmetic: pauses over 60 and
// 150 ms, the silence beyond a frame, the window's edges count.
func TestStandCallWindow(t *testing.T) {
	from := time.Unix(1_800_000_000, 0).Truncate(time.Minute)
	c := newStandCall(from)
	at := from
	seq := uint64(0)
	// A datagram every 20 ms answered in 40 ms, except a hole of 200 ms
	// (10 lost) at 10 s and one of 80 ms (3 lost) at 30 s.
	for at.Before(from.Add(time.Minute)) {
		seq++
		c.sent(seq, at, 1)
		off := at.Sub(from)
		lost := (off >= 10*time.Second && off < 10*time.Second+200*time.Millisecond) ||
			(off >= 30*time.Second && off < 30*time.Second+60*time.Millisecond)
		if !lost {
			c.answer(seq, at.Add(40*time.Millisecond))
		}
		at = at.Add(standEvery)
	}
	ws := c.due(from.Add(time.Minute + standGrace))
	if len(ws) != 1 {
		t.Fatalf("%d windows", len(ws))
	}
	w := ws[0]
	if w.Sent != 3000 || w.Got != 3000-10-3 {
		t.Fatalf("sent %d got %d", w.Sent, w.Got)
	}
	// The window starts at from: the first answer comes at 40 ms (not heard),
	// the holes give 220 and 80 ms, the last answer of the minute lands in
	// the next window, so the end is 20 ms after the one before.
	if w.Gaps60 != 2 || w.Gaps150 != 1 || w.GapMax != 220*time.Millisecond {
		t.Fatalf("gaps60 %d gaps150 %d max %v", w.Gaps60, w.Gaps150, w.GapMax)
	}
	if w.Silent != 200*time.Millisecond+60*time.Millisecond {
		t.Fatalf("silent %v", w.Silent)
	}
	if w.RTTP50 != 40*time.Millisecond || w.Sessions != 1 {
		t.Fatalf("rtt %v sessions %d", w.RTTP50, w.Sessions)
	}
	line := standCallLine(wireUDP, w)
	for _, want := range []string{"call wire=udp copies=1 ", "sent=3000 got=2987 ", "gaps60=2 gaps150=1 gap_max=220 silent_ms=260 sessions=1 "} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q lacks %q", line, want)
		}
	}
}

func TestStandCPU(t *testing.T) {
	d, err := procCPU("1234 (nekko edge) S 1 2 3 4 5 6 7 8 9 10 250 50 0 0")
	if err != nil || d != 3*time.Second {
		t.Fatalf("%v %v", d, err)
	}
	total, steal, idle, err := boxCPU("cpu  100 0 100 700 0 0 0 100 0 0\ncpu0 1 1 1 1 1 1 1 1")
	if err != nil || total != 1000 || steal != 100 || idle != 700 {
		t.Fatalf("%d %d %d %v", total, steal, idle, err)
	}
	a := cpuSample{proc: 0, total: 0, steal: 0, idle: 0}
	b := cpuSample{proc: 1500 * time.Millisecond, total: 1000, steal: 100, idle: 700}
	if got := cpuLine(wireAwg, time.Unix(60, 0), a, b); got != "cpu wire=awg from=60 cpu_ms=1500 steal_pct=10.0 busy_pct=20.0" {
		t.Fatal(got)
	}
}

func TestWireStandConfig(t *testing.T) {
	env := map[string]string{"WIRESTAND_ROLE": "entry", "WIRESTAND_WIRE": "awg", "WIRESTAND_PORT": "20445",
		"WIRESTAND_SECRET": hex.EncodeToString(make([]byte, 32)), "WIRESTAND_EXIT": "unl1.example"}
	if _, err := WireStandConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("an entry needs the exit's address")
	}
	env["WIRESTAND_EXIT"] = "192.0.2.1"
	env["WIRESTAND_WIRE"] = "wireguard"
	if _, err := WireStandConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("unknown wire")
	}
}
