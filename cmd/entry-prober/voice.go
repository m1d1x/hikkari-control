package main

import (
	"context"
	"encoding/binary"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

// A voice relay (Hysteria2, voice plan Ф3) is probed the way a call uses it:
// a burst of voice-sized datagrams at a voice rate through the tunnel to the
// relay's loopback echo, each timed by its sequence number. A handshake that
// passes says little — a path can open and then die under 50 pps (voice plan
// §6.1) — so the verdict is the share that came back.
const (
	voicePackets  = 100
	voiceInterval = 20 * time.Millisecond // 50 pps, one Opus frame each
	voicePayload  = 160
	// voiceCleanShare of the burst must come back for a clean verdict: a
	// path that loses more than a tenth at birth is not a path for a call.
	voiceCleanShare = 0.9
)

// voiceProbe is one burst's outcome.
type voiceProbe struct {
	sent, got int
	medianMs  float32
}

func (p voiceProbe) ok() bool {
	return p.sent > 0 && float64(p.got) >= voiceCleanShare*float64(p.sent)
}

// probeVoiceTunnel sends the burst through the local door on port and waits
// up to timeout after the last packet for stragglers.
func probeVoiceTunnel(ctx context.Context, port int, timeout time.Duration) voiceProbe {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return voiceProbe{}
	}
	defer conn.Close()

	var mu sync.Mutex
	sentAt := make(map[uint32]time.Time, voicePackets)
	var rtts []time.Duration
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if n < 4 {
				continue
			}
			seq := binary.BigEndian.Uint32(buf)
			mu.Lock()
			if at, ok := sentAt[seq]; ok {
				rtts = append(rtts, time.Since(at))
				delete(sentAt, seq) // a duplicate counts once
			}
			mu.Unlock()
		}
	}()

	pkt := make([]byte, voicePayload)
	tick := time.NewTicker(voiceInterval)
	defer tick.Stop()
	sent := 0
	for seq := uint32(0); seq < voicePackets; seq++ {
		binary.BigEndian.PutUint32(pkt, seq)
		mu.Lock()
		sentAt[seq] = time.Now()
		mu.Unlock()
		if _, err := conn.Write(pkt); err == nil {
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

// classifyVoice names a voice relay's cause as far as one signal can. A voice
// relay has no K1 analogue — a masked Hysteria server answers nothing that is
// not its own protocol — so a dead tunnel on a working network stays
// `unknown`: not clean, which is what admission and the canary's health
// check need, without claiming a cause the probe never saw.
func classifyVoice(p voiceProbe, k2 bool) string {
	switch {
	case !k2:
		return verdictNetworkDown
	case p.ok():
		return verdictClean
	default:
		return verdictUnknown
	}
}

// withXUDP folds the voice burst through a stream relay (XUDP inside its
// Reality session, the TCP rung of a call, voice plan Ф5) into its row: the
// numbers go in their own columns, and the verdict is left alone.
//
// # Why the door no longer rewrites the verdict (П9.7)
//
// It used to turn a `clean` row into `unknown`, on the reasoning that a relay
// whose datagrams are dropped is not fit to carry a call. The reasoning was
// right and the place was wrong: one column then carried two independent
// claims, and `unknown` is the word that means "no evidence" to everything
// downstream. So a relay with a perfect stream path and an old edge build
// read as unjudgeable, and that one word cost three things —
//
//	the detector never saw a healthy box, so no incident was ever marked
//	admitted;
//	every repair failed its own admission probe, which is what П9.7's chaos
//	run ran into: the unit was restarted, the tunnel worked, and the hub
//	refused to admit it;
//	and the canary never gained anything, because it reads the FAILING
//	verdicts and `unknown` was excluded from those anyway.
//
// The door is a fact about the relay and it stays recorded — as the pair of
// numbers it is, in xudp_got/xudp_sent, where a reader can ask about the door
// without asking about the stream.
func withXUDP(r probeResult, p voiceProbe) probeResult {
	door := "xudp " + strconv.Itoa(p.got) + "/" + strconv.Itoa(p.sent)
	// Added to the detail, never written over it. The stream probe's own
	// detail is the only place a row says WHY it was called what it was
	// called, and overwriting it is what kept the broken K3 of П9.7 invisible:
	// every failure and every success alike read `unknown` with `xudp 0/100`
	// beside it.
	if r.Detail != "" {
		r.Detail += "; " + door
	} else {
		r.Detail = door
	}
	r.XUDPGot, r.XUDPSent = uint16(p.got), uint16(p.sent)
	return r
}

// probeVoiceEdge is probeEdge for a voice relay. The row reuses the dataset's
// columns: realityOk is "the tunnel carried the burst", realityRttMs its
// median round trip, cover the relay's SNI; the TCP and bare-TLS signals stay
// 0 — they do not exist on this path.
func probeVoiceEdge(ctx context.Context, e edge, port int, k2 bool, now time.Time, timeout time.Duration) probeResult {
	p := probeVoiceTunnel(ctx, port, timeout)
	return probeResult{
		TS:           now,
		RelayName:    e.Name,
		RealityOK:    b2u8(p.ok()),
		RealityRTTMs: p.medianMs,
		Cover:        e.ServerName,
		K2Control:    b2u8(k2),
		Verdict:      classifyVoice(p, k2),
		Detail:       "voice " + strconv.Itoa(p.got) + "/" + strconv.Itoa(p.sent),
	}
}
