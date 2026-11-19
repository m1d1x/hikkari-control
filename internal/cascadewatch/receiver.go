package cascadewatch

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The entry's receivers (hikkari-client plans/2026-11-16-second-receiver.md,
// P5; trunkentry receivers_linux.go). The brain says a move and, once a
// minute, how each receiver stands:
//
//	trunk: receiver moved from <ip> to <ip>: <why>
//	trunk: receiver <ip> by hand refused: <why>
//	trunk: receivers <ip> active up=N proof_ms=M; <ip> standby up=N proof_ms=M
//
// Each is an event (kind receiver_moved, receiver_refused, receivers) with the
// receivers named by box: the addresses are the boxes' sealed ones, the watch
// files names. An address the pair does not know stays "?".
var (
	reReceiverMoved   = regexp.MustCompile(`trunk: receiver moved from (\S+) to (\S+): (.+)$`)
	reReceiverRefused = regexp.MustCompile(`trunk: receiver (\S+) by hand refused: (.+)$`)
	reReceivers       = regexp.MustCompile(`trunk: receivers (.+)$`)
	reReceiverPart    = regexp.MustCompile(`^(\S+) (active|standby) up=(\d+) proof_ms=(-?\d+)$`)
	// The call held to each receiver (hikkari-protocol pipectl better.go):
	//
	//	nekko-trunk-entry: call receiver=<ip> copies=1 from=… (the call's line)
	//
	// is a call row under target rx-<box>, the receiver in data.
	reReceiverCall = regexp.MustCompile(`nekko-trunk-entry: call receiver=(\S+) (copies=\d+ .*)$`)
)

// classifyReceiverCall turns a minute of the call held to a receiver into
// its call row; false for any other line.
func classifyReceiverCall(l journalLine, names map[string]string) (Run, bool) {
	m := reReceiverCall.FindStringSubmatch(unwrapLogJSON(l.Msg))
	if m == nil {
		return Run{}, false
	}
	box, ok := names[m[1]]
	if !ok {
		box = "?"
	}
	data := callFields(m[2])
	data["receiver"] = box
	return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerCall, Target: "rx-" + box, OK: boolp(callOK(data)), Data: data}, true
}

// classifyReceiver turns a line about the receivers into its event; false
// for any other line.
func classifyReceiver(l journalLine, names map[string]string) (Run, bool) {
	name := func(ip string) string {
		if n, ok := names[ip]; ok {
			return n
		}
		return "?"
	}
	if m := reReceiverMoved.FindStringSubmatch(l.Msg); m != nil {
		why := m[3]
		switch {
		case why == "by hand":
		case strings.HasPrefix(why, "the active receiver worse"):
			why = "better: " + clip(why)
		default:
			why = "dead: " + clip(why)
		}
		return Run{TS: l.TS, Layer: LayerEvent, Data: map[string]any{"kind": "receiver_moved", "from": name(m[1]), "to": name(m[2]), "why": why}}, true
	}
	if m := reReceiverRefused.FindStringSubmatch(l.Msg); m != nil {
		return Run{TS: l.TS, Layer: LayerEvent, Data: map[string]any{"kind": "receiver_refused", "to": name(m[1]), "why": clip(m[2])}}, true
	}
	m := reReceivers.FindStringSubmatch(l.Msg)
	if m == nil {
		return Run{}, false
	}
	var rs []map[string]any
	for _, part := range strings.Split(m[1], "; ") {
		p := reReceiverPart.FindStringSubmatch(part)
		if p == nil {
			continue
		}
		up, _ := strconv.Atoi(p[3])
		proof, _ := strconv.Atoi(p[4])
		rs = append(rs, map[string]any{"box": name(p[1]), "role": p[2], "up": up, "proof_ms": proof})
	}
	return Run{TS: l.TS, Layer: LayerEvent, Data: map[string]any{"kind": "receivers", "receivers": rs}}, true
}
