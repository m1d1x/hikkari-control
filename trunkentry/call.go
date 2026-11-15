package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
)

// The trunk's own call (hikkari-protocol trunk/call.go): a datagram every
// 20 ms to the exit's echo and back, one call with one copy (what one of a
// Hikkari client's two channels gets on the trunk) and one with two (a
// client's plain UDP). A line per call per minute, read by the hub
// (internal/cascadewatch): every second of the leg between the data centres,
// not 30 s in ten minutes.
var callCopies = []int{1, 2}

func runCalls(ctx context.Context, entry *trunk.Entry) {
	for _, copies := range callCopies {
		go trunk.RunCall(ctx, entry.Session, trunk.CallConfig{Copies: copies}, func(w trunk.CallWindow) {
			log.Print("nekko-trunk-entry: " + callLine(w))
		})
	}
}

// callLine is one call's minute. Its keys are the hub's contract: from is the
// window's start (unix seconds), times in ms, worst the longest pauses as
// <unix ms of the answer before>:<ms>, then what a listener heard
// (hearingFields).
func callLine(w trunk.CallWindow) string {
	worst := make([]string, 0, len(w.Worst))
	for _, g := range w.Worst {
		worst = append(worst, fmt.Sprintf("%d:%d", g.At.UnixMilli(), g.Len.Milliseconds()))
	}
	if len(worst) == 0 {
		worst = append(worst, "-")
	}
	return fmt.Sprintf("call copies=%d from=%d sent=%d got=%d rtt_p50=%s rtt_p99=%s rtt_max=%s gaps60=%d gaps150=%d gap_max=%d silent_ms=%d sessions=%d worst=%s",
		w.Copies, w.From.Unix(), w.Sent, w.Got, ms(w.RTTP50, w.Got), ms(w.RTTP99, w.Got), ms(w.RTTMax, w.Got),
		w.Gaps60, w.Gaps150, w.GapMax.Milliseconds(), w.Silent.Milliseconds(), w.Sessions, strings.Join(worst, ",")) +
		hearingFields(w.Up, w.Down)
}

// The voice set (hikkari-client plans/2026-11-15-live-udp-two-fates.md): the
// wire the route gives voice and the pool, the other fate. Until the route
// holds a set of its own (step A) the set is these two; voice on the pool, or
// on no wire in particular, is the pool alone.
const voiceSetName = "voice"

func voiceSet(route func() trunk.Route) trunk.CallSet {
	return trunk.CallSet{Name: voiceSetName, Wires: func() []trunk.WireID {
		v := route().Voice
		if v == trunk.AnyWire || v == trunk.PoolWire {
			return []trunk.WireID{trunk.PoolWire}
		}
		return []trunk.WireID{v, trunk.PoolWire}
	}}
}

// setLine is a set's minute: what a listener of the set heard, and how much
// later the other wires' copy came than the first wire's, in ms with one
// decimal (lag_up_*, lag_down_*; absent when no frame came on both). The hub
// reads it in internal/cascadewatch hearing.go.
func setLine(w trunk.CallSetWindow, name func(trunk.WireID) string) string {
	wires := make([]string, 0, len(w.Wires))
	for _, id := range w.Wires {
		wires = append(wires, name(id))
	}
	if len(wires) == 0 {
		wires = append(wires, "-")
	}
	line := fmt.Sprintf("call set=%s wires=%s from=%d", w.Name, strings.Join(wires, ","), w.From.Unix()) + hearingFields(w.Up, w.Down)
	for _, l := range []struct {
		dir string
		lag trunk.CallLag
	}{{"up", w.LagUp}, {"down", w.LagDown}} {
		if l.lag.N == 0 {
			continue
		}
		line += fmt.Sprintf(" lag_%s_n=%d lag_%s_p50=%s lag_%s_p95=%s lag_%s_max=%s", l.dir, l.lag.N,
			l.dir, msOf(l.lag.P50), l.dir, msOf(l.lag.P95), l.dir, msOf(l.lag.Max))
	}
	return line
}

// hearingFields is what a listener at the exit (up) and one at the entry
// (down) heard, for each buffer of trunk.CallBuffers in ms: <dir>_frames
// judged, then <dir>_holes<buffer>, <dir>_runs2_<buffer> and
// <dir>_bad_s<buffer>. A direction with nothing judged (an exit that does
// not stamp its echo) has no keys.
func hearingFields(up, down trunk.CallHearing) string {
	var b strings.Builder
	for _, d := range []struct {
		dir string
		h   trunk.CallHearing
	}{{"up", up}, {"down", down}} {
		if d.h.Frames == 0 {
			continue
		}
		fmt.Fprintf(&b, " %s_frames=%d", d.dir, d.h.Frames)
		for i, buf := range trunk.CallBuffers {
			n := buf.Milliseconds()
			fmt.Fprintf(&b, " %s_holes%d=%d %s_runs2_%d=%d %s_bad_s%d=%d", d.dir, n, d.h.Holes[i], d.dir, n, d.h.Runs2[i], d.dir, n, d.h.BadS[i])
		}
	}
	return b.String()
}

// ms is d in milliseconds with one decimal, "-" when nothing came back.
func ms(d time.Duration, got int) string {
	if got == 0 {
		return "-"
	}
	return msOf(d)
}

// msOf is d in milliseconds with one decimal.
func msOf(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64)
}
