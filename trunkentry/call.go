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
// <unix ms of the answer before>:<ms>.
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
		w.Gaps60, w.Gaps150, w.GapMax.Milliseconds(), w.Silent.Milliseconds(), w.Sessions, strings.Join(worst, ","))
}

// ms is d in milliseconds with one decimal, "-" when nothing came back.
func ms(d time.Duration, got int) string {
	if got == 0 {
		return "-"
	}
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64)
}
