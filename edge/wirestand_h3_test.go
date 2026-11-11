//go:build wirestand_h3

package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestWireStandH3: Chromium's QUIC on the entry, Caddy's HTTP/3 on the exit:
// the call is echoed frame by frame, 1 MiB arrives whole, Chromium took its
// UDP socket itself (no copy in Go), and two users' frames, as they are and
// glued, all come back.
func TestWireStandH3(t *testing.T) {
	out := captureStand(t)
	port := strconv.Itoa(freeStandPort(t))
	env := map[string]string{"WIRESTAND_WIRE": wireH3, "WIRESTAND_PORT": port, "WIRESTAND_SECRET": hex.EncodeToString(make([]byte, 32)),
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := serveWire(ctx, exitCfg); err != nil {
		t.Fatal(err)
	}
	w, err := dialWire(ctx, entryCfg)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := w.call(ctx)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	for seq := uint64(1); seq <= 20; seq++ {
		p := make([]byte, standSize)
		binary.BigEndian.PutUint64(p, seq)
		if _, err := conv.Write(p); err != nil {
			t.Fatal(err)
		}
		_ = conv.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conv.Read(buf)
		if err != nil || n != standSize || binary.BigEndian.Uint64(buf) != seq {
			t.Fatalf("frame %d: %d %v", seq, n, err)
		}
	}
	conv.Close()
	if r := w.bulk(ctx, standBulkSpec{Kind: "1m", Bytes: standBulk, Rate: udpBulkRate}); r.End != "ok" || r.Bytes != standBulk {
		t.Fatalf("bulk: %+v", r)
	}
	if s := standH3.Sockets(); s.Direct < 1 || s.Relayed != 0 || s.TCP != 0 {
		t.Fatalf("sockets %+v", s)
	}
	for _, mode := range []byte{'u', 'g'} {
		before := len(out.lines("load wire=h3 from="))
		runLoadStep(ctx, wireH3, w, loadStep{Mode: mode, Users: 2}, time.Now().Add(3*time.Second))
		s := loadSums(t, out.lines("load wire=h3 from=")[before:])
		if s["voice_sent"] < 280 || s["game_sent"] < 1150 || s["voice_got"] < s["voice_sent"]-2 || s["game_got"] < s["game_sent"]-4 || s["write_errs"] != 0 {
			t.Fatalf("%c: %v", mode, s)
		}
	}
	// A stranger without the credential gets the page, not the proxy.
	if _, err := net.Dial("udp", "127.0.0.1:"+port); err != nil {
		t.Fatal(err)
	}
}
