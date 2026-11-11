package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"log"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// standCapture swaps the stand's output for a buffer.
type standCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *standCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *standCapture) lines(prefix string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, l := range strings.Split(c.buf.String(), "\n") {
		if strings.HasPrefix(l, standLog+prefix) {
			out = append(out, strings.TrimPrefix(l, standLog))
		}
	}
	return out
}

func captureStand(t *testing.T) *standCapture {
	c := &standCapture{}
	old := standOut
	standOut = log.New(c, "", 0)
	t.Cleanup(func() { standOut = old })
	return c
}

// loadSums adds up a step's minute lines.
func loadSums(t *testing.T, lines []string) map[string]int {
	t.Helper()
	sum := map[string]int{}
	for _, l := range lines {
		for _, kv := range strings.Fields(l) {
			k, v, _ := strings.Cut(kv, "=")
			if n, err := strconv.Atoi(v); err == nil {
				sum[k] += n
			}
		}
	}
	return sum
}

func TestParseLoadPlan(t *testing.T) {
	p, err := parseLoadPlan("1800000060:u1x4,0x1,g4x4,n2x1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 4 || p.Steps[1].Mode != '0' || p.Steps[2].Users != 4 || !p.Until().Equal(time.Unix(1800000060, 0).Add(10*time.Minute)) {
		t.Fatalf("%+v", p)
	}
	for _, bad := range []string{"u1x4", "60:x1x4", "60:u0x4", "60:01x1", "60:0x", "60:u17x1", "60:u1x0"} {
		if _, err := parseLoadPlan(bad); err == nil {
			t.Fatalf("%q taken", bad)
		}
	}
}

// TestLoadOverTheBareWire: two users for 3 s over the bare wire, each frame
// its own datagram, then glued, then the in-process echo: every frame comes
// back, the glued writes are fewer and fuller, the books agree.
func TestLoadOverTheBareWire(t *testing.T) {
	out := captureStand(t)
	port := strconv.Itoa(freeStandPort(t))
	env := map[string]string{"WIRESTAND_WIRE": wireUDP, "WIRESTAND_PORT": port, "WIRESTAND_SECRET": hex.EncodeToString(make([]byte, 32)), "WIRESTAND_EXIT": "127.0.0.1"}
	get := func(role string) func(string) string {
		return func(k string) string {
			if k == "WIRESTAND_ROLE" {
				return role
			}
			return env[k]
		}
	}
	exitCfg, _ := WireStandConfigFromEnv(get("exit"))
	entryCfg, _ := WireStandConfigFromEnv(get("entry"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := serveWire(ctx, exitCfg); err != nil {
		t.Fatal(err)
	}
	w, err := dialWire(ctx, entryCfg)
	if err != nil {
		t.Fatal(err)
	}
	sums := map[byte]map[string]int{}
	for _, mode := range []byte{'u', 'g', 'n'} {
		before := len(out.lines("load wire=udp from="))
		runLoadStep(ctx, wireUDP, w, loadStep{Mode: mode, Users: 2}, time.Now().Add(3*time.Second))
		s := loadSums(t, out.lines("load wire=udp from=")[before:])
		sums[mode] = s
		// 2 users × (50 + 200)/s × 3 s = 1500 frames, give or take a tick.
		if s["voice_sent"] < 280 || s["game_sent"] < 1150 || s["voice_got"] < s["voice_sent"]*95/100 || s["game_got"] < s["game_sent"]*95/100 || s["write_errs"] != 0 {
			t.Fatalf("%c: %v", mode, s)
		}
		if s["frames"] != s["voice_sent"]+s["game_sent"] {
			t.Fatalf("%c: frames %d of %d", mode, s["frames"], s["voice_sent"]+s["game_sent"])
		}
	}
	if sums['u']["writes"] != sums['u']["frames"] {
		t.Fatalf("u: writes %d frames %d", sums['u']["writes"], sums['u']["frames"])
	}
	// Glued: frames due in the same millisecond go together; two users'
	// games at 5 ms with phases apart make at least some pairs.
	if sums['g']["writes"] >= sums['g']["frames"] {
		t.Fatalf("g: writes %d frames %d", sums['g']["writes"], sums['g']["frames"])
	}
}
