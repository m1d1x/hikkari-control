package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// provoke is a receiver as edge/provoke.go answers «DL n».
func provoke(t *testing.T, stallAfter int64) string {
	t.Helper()
	cert := httptest.NewTLSServer(nil)
	cfg := cert.TLS.Clone()
	cfg.NextProtos = nil
	cert.Close()
	ln, err := tls.Listen("tcp4", "127.0.0.1:0", cfg)
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
			go func() {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				var n int64
				if _, err := fmt.Sscanf(strings.TrimSpace(line), "DL %d", &n); err != nil {
					return
				}
				if stallAfter > 0 && n > stallAfter {
					_, _ = c.Write(make([]byte, stallAfter))
					select {} // the censor's freeze
				}
				_, _ = c.Write(make([]byte, n))
			}()
		}
	}()
	return ln.Addr().String()
}

func TestTrunkDeliversEveryChunk(t *testing.T) {
	addr := provoke(t, 0)
	for _, mode := range []string{"single", "pool"} {
		r, err := measureTrunk(mode, addr, 16, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		if r.Delivered != 16 || r.FirstOK != 16 || r.Ends["ok"] != 16 || r.Mbps <= 0 {
			t.Fatalf("%s: %+v", mode, r)
		}
	}
}

// A path that freezes every flow at 16 KiB: single delivers nothing and pool
// says why, by its ends.
func TestTrunkSaysWhyAChunkFailed(t *testing.T) {
	addr := provoke(t, 16<<10)
	r, err := measureTrunk("single", addr, 2, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Delivered != 0 || r.Ends["stall_mid"] != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestPathCountsOpenedAndRefusedFlows(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	p, err := measurePath(ln.Addr().String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if p.Opened != 4 || p.ConnP50MS <= 0 {
		t.Fatalf("%+v", p)
	}
	addr := ln.Addr().String()
	ln.Close()
	if p, _ = measurePath(addr, 3); p.Refused != 3 || p.Opened != 0 {
		t.Fatalf("closed port: %+v", p)
	}
	if _, err := measurePath("example.org:443", 1); err == nil {
		t.Fatal("a name was taken for an address")
	}
}

func TestVPSReadsTheBox(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	v, err := measureVPS()
	if err != nil {
		t.Fatal(err)
	}
	if v.CPUs < 1 || v.MemTotalMB <= 0 || v.Iface == "" {
		t.Fatalf("%+v", v)
	}
}
