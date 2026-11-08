package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// A Hikkari relay has a second channel the stream probe never touches: UDP as
// QUIC datagrams on udp/<port> of the same host (hikkari-protocol spec/v1.md).
// Admission sends a call-shaped burst through it, the same 100 × 160 B at 50
// pps as a voice relay's, to a public STUN server: the relay's exit refuses
// loopback, so there is no echo on the box to aim at.
//
// The client core runs the datagram channel alone, as a hysteria2 outbound:
// the Hikkari outbound would fall back to UDP-over-TCP inside Naive and hide a
// dead channel behind a live stream. The burst's count goes into the row as
// the voice door of a stream relay does (withXUDP): its own columns, the
// verdict left to the stream probe.

// datagramTarget answers STUN binding requests from anywhere; the client's
// voice probe (diag:voice) aims at it too, so the two read alike.
var datagramTarget = "stun.cloudflare.com:3478"

// datagramWarmup bounds the wait for the channel's first answer: the QUIC
// handshake and auth ride the first packet, and a burst started before them
// would count the handshake as loss.
const datagramWarmup = 5 * time.Second

// probeDatagram runs the burst through e's datagram channel with the client
// core at binary and folds it into res.
func probeDatagram(ctx context.Context, binary string, e edge, res probeResult, timeout time.Duration) probeResult {
	port, stop, err := startDatagramDoor(ctx, binary, e)
	if err != nil {
		return withDoor(res, "datagram", voiceProbe{})
	}
	defer stop()
	return withDoor(res, "datagram", stunBurst(ctx, port, timeout))
}

// startDatagramDoor runs the core with a hysteria2 outbound to the relay's
// datagram channel behind a local UDP door forwarded to datagramTarget, and
// returns the door's port. A mixed inbound beside it says when the core is up.
func startDatagramDoor(ctx context.Context, binary string, e edge) (int, func(), error) {
	if e.Naive == nil {
		return 0, nil, fmt.Errorf("no credential")
	}
	host, targetPort, err := net.SplitHostPort(datagramTarget)
	if err != nil {
		return 0, nil, err
	}
	tp, _ := strconv.Atoi(targetPort)
	ready, err := freeTCPPort()
	if err != nil {
		return 0, nil, err
	}
	door, err := freeUDPPort()
	if err != nil {
		return 0, nil, err
	}
	dir, err := os.MkdirTemp("", "hikkari-datagram-probe-")
	if err != nil {
		return 0, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	config := map[string]any{
		"log": map[string]any{"disabled": true},
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "ready", "listen": "127.0.0.1", "listen_port": ready},
			map[string]any{"type": "direct", "tag": "door", "listen": "127.0.0.1", "listen_port": door, "network": "udp", "override_address": host, "override_port": tp},
		},
		"outbounds": []any{map[string]any{
			"type": "hysteria2", "tag": "datagram", "server": e.Address, "server_port": e.Port,
			"password": e.Naive.Pass, "network": "udp",
			"tls": map[string]any{"enabled": true, "server_name": e.Naive.Host, "alpn": []string{"h3"}},
		}},
		"route": map[string]any{"final": "datagram"},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		cleanup()
		return 0, nil, err
	}
	path := filepath.Join(dir, "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		cleanup()
		return 0, nil, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(processCtx, binary, "run", "-c", path)
	cmd.Dir = filepath.Dir(binary)
	if err = cmd.Start(); err != nil {
		cancel()
		cleanup()
		return 0, nil, fmt.Errorf("client start failed")
	}
	stop := func() { cancel(); _ = cmd.Wait(); cleanup() }
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && processCtx.Err() == nil; time.Sleep(50 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ready)), 100*time.Millisecond); err == nil {
			c.Close()
			return door, stop, nil
		}
	}
	stop()
	return 0, nil, fmt.Errorf("client not ready")
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func freeUDPPort() (int, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port, nil
}

// stunMagic is RFC 5389's magic cookie.
const stunMagic = 0x2112A442

// stunRequest is a binding request of voicePayload bytes: the 20-byte header
// with seq in the transaction id, padded by a PADDING attribute (0x0026) so
// the datagram is the size of an Opus frame.
func stunRequest(seq uint32, nonce [8]byte) []byte {
	b := make([]byte, voicePayload)
	binary.BigEndian.PutUint16(b[0:], 0x0001)
	binary.BigEndian.PutUint16(b[2:], uint16(voicePayload-20))
	binary.BigEndian.PutUint32(b[4:], stunMagic)
	copy(b[8:16], nonce[:])
	binary.BigEndian.PutUint32(b[16:], seq)
	binary.BigEndian.PutUint16(b[20:], 0x0026)
	binary.BigEndian.PutUint16(b[22:], uint16(voicePayload-24))
	return b
}

// stunSeq reads seq back from a binding response of this burst.
func stunSeq(b []byte, nonce [8]byte) (uint32, bool) {
	if len(b) < 20 || binary.BigEndian.Uint32(b[4:]) != stunMagic || string(b[8:16]) != string(nonce[:]) {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[16:]), true
}

// stunBurst warms the channel up with one request, then sends voicePackets
// binding requests every voiceInterval through the door and counts the
// answers, as probeVoiceTunnel does with an echo.
func stunBurst(ctx context.Context, port int, timeout time.Duration) voiceProbe {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return voiceProbe{}
	}
	defer conn.Close()
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	buf := make([]byte, 2048)

	// Warm-up: seq 1<<31 is outside the burst's range and does not count.
	warmed := false
	for deadline := time.Now().Add(datagramWarmup); !warmed && time.Now().Before(deadline) && ctx.Err() == nil; {
		_, _ = conn.Write(stunRequest(1<<31, nonce))
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if n, err := conn.Read(buf); err == nil {
			_, warmed = stunSeq(buf[:n], nonce)
		}
	}
	if !warmed {
		return voiceProbe{sent: voicePackets}
	}
	_ = conn.SetReadDeadline(time.Time{})

	var mu sync.Mutex
	sentAt := make(map[uint32]time.Time, voicePackets)
	var rtts []time.Duration
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 2048)
		for {
			n, err := conn.Read(b)
			if err != nil {
				return
			}
			seq, ok := stunSeq(b[:n], nonce)
			if !ok {
				continue
			}
			mu.Lock()
			if at, ok := sentAt[seq]; ok {
				rtts = append(rtts, time.Since(at))
				delete(sentAt, seq)
			}
			mu.Unlock()
		}
	}()
	tick := time.NewTicker(voiceInterval)
	defer tick.Stop()
	sent := 0
	for seq := uint32(0); seq < voicePackets; seq++ {
		mu.Lock()
		sentAt[seq] = time.Now()
		mu.Unlock()
		if _, err := conn.Write(stunRequest(seq, nonce)); err == nil {
			sent++
		}
		select {
		case <-ctx.Done():
			return voiceProbe{}
		case <-tick.C:
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	<-done
	mu.Lock()
	defer mu.Unlock()
	p := voiceProbe{sent: sent, got: len(rtts)}
	if len(rtts) > 0 {
		sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
		p.medianMs = float32(rtts[len(rtts)/2].Microseconds()) / 1000
	}
	return p
}
