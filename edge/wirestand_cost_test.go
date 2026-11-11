//go:build wirestand_h3

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWireStandCostLocal (HIKKARI_WIRE_COST=1) prices the wires on this
// machine: the exit runs in a child process, so the test's own CPU is the
// entry's; every step 15 s, the price per user frame against the
// in-process echo. Not a stand number: the box's CPU and steal differ.
func TestWireStandCostLocal(t *testing.T) {
	if role := os.Getenv("HIKKARI_WIRE_COST_EXIT"); role != "" {
		cfg, err := WireStandConfigFromEnv(os.Getenv)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := serveWire(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return
	}
	if os.Getenv("HIKKARI_WIRE_COST") == "" {
		t.Skip("HIKKARI_WIRE_COST=1 to price the wires here")
	}
	out := captureStand(t)
	wires := strings.Split(envOr("HIKKARI_WIRE_COST_WIRES", "h3,quic,udp"), ",")
	steps := strings.Split(envOr("HIKKARI_WIRE_COST_STEPS", "n1,n4,n16,u1,u2,u4,g4,g16"), ",")
	for _, wire := range wires {
		port := strconv.Itoa(freeStandPort(t))
		env := []string{"WIRESTAND_WIRE=" + wire, "WIRESTAND_PORT=" + port, "WIRESTAND_SECRET=" + hex.EncodeToString(make([]byte, 32)),
			"WIRESTAND_EXIT=127.0.0.1", "WIRESTAND_SNI=stand.example"}
		child := exec.Command(os.Args[0], "-test.run", "^TestWireStandCostLocal$")
		child.Env = append(append(os.Environ(), env...), "WIRESTAND_ROLE=exit", "HIKKARI_WIRE_COST_EXIT=1")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		get := func(k string) string {
			if k == "WIRESTAND_ROLE" {
				return "entry"
			}
			for _, kv := range env {
				if a, b, _ := strings.Cut(kv, "="); a == k {
					return b
				}
			}
			return ""
		}
		cfg, err := WireStandConfigFromEnv(get)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		w, err := dialWire(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range steps {
			users, _ := strconv.Atoi(st[1:])
			before := len(out.lines("load wire=" + wire + " from="))
			c0, th0 := selfCPU(), readThreads()
			t0 := time.Now()
			runLoadStep(ctx, wire, w, loadStep{Mode: st[0], Users: users}, time.Now().Add(15*time.Second))
			cpu, took := selfCPU()-c0, time.Since(t0)
			goCPU, other, top := threadSplit(mainComm(), th0, readThreads())
			s := loadSums(t, out.lines("load wire=" + wire + " from=")[before:])
			frames := s["voice_sent"] + s["game_sent"]
			back := s["voice_got"] + s["game_got"]
			fmt.Printf("COST wire=%s step=%s cpu_pct=%.1f frames=%d back=%d writes=%d us_per_frame=%.1f us_per_write=%.1f gaps60=%d go_ms=%d other_ms=%d top=%s\n",
				wire, st, 100*cpu.Seconds()/took.Seconds(), frames, back, s["writes"], float64(cpu.Microseconds())/float64(frames+back), float64(cpu.Microseconds())/float64(2*s["writes"]), s["gaps60"], goCPU.Milliseconds(), other.Milliseconds(), top)
		}
		cancel()
		_ = child.Process.Kill()
		_ = child.Wait()
		time.Sleep(time.Second)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func selfCPU() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
