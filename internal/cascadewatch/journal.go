package cascadewatch

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The entry's journal (hikkari-control trunkentry): the minute line of its
// counters and a line per event of the pool. Read over SSH with
// `journalctl -o short-unix`, so every line starts with its time.

// journalLine is one line: when, and what was said after the identifier.
type journalLine struct {
	TS  time.Time
	Msg string
}

// parseJournal reads `journalctl -o short-unix` output: "<unix.micro> <host>
// <ident>[pid]: <message>". Lines it cannot read are skipped.
func parseJournal(out string) []journalLine {
	var lines []journalLine
	for _, raw := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(raw), " ", 3)
		if len(f) < 3 {
			continue
		}
		sec, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		_, msg, ok := strings.Cut(f[2], ": ")
		if !ok {
			continue
		}
		ts := time.Unix(0, int64(sec*1e6)*1e3).UTC()
		lines = append(lines, journalLine{TS: ts, Msg: msg})
	}
	return lines
}

var (
	reStats      = regexp.MustCompile(`nekko-trunk-entry: (members=\d+ .*)$`)
	reCall       = regexp.MustCompile(`nekko-trunk-entry: call (copies=\d+ .*)$`)
	reMemberDown = regexp.MustCompile(`trunk: member (\d+): down after (\S+): (.*)$`)
	reMemberFail = regexp.MustCompile(`trunk: member (\d+): (dial|hello): (.*)$`)
	rePaused     = regexp.MustCompile(`trunk: paused (\S+)$`)
	reEpochReset = regexp.MustCompile(`trunk: exit epoch ([0-9a-f]+) after ([0-9a-f]+): streams reset`)
	reEpoch      = regexp.MustCompile(`trunk: exit epoch ([0-9a-f]+)$`)
	reUp         = regexp.MustCompile(`nekko-trunk-entry: up: (.*)$`)
	reUnit       = regexp.MustCompile(`nekko-trunk-entry\.service: (.*)$`)
	// A hitless rollout of the entry (trunkentry/handoff.go): the new
	// generation took over, an old one drains, an old one left.
	reHandoff = regexp.MustCompile(`nekko-trunk-entry: generation (\d+) \(pid \d+\) is up, generation (\d+) drains$`)
	reDrains  = regexp.MustCompile(`nekko-trunk-entry: generation (\d+) drains with (\d+) stream\(s\) and (\d+) flow\(s\)$`)
	reLeft    = regexp.MustCompile(`nekko-trunk-entry: generation (\d+) (?:leaves after \S+ with (\d+) stream\(s\) and (\d+) flow\(s\)|carries nothing more and leaves)$`)
	// A hitless rollout of the exit as the entry's pool lives it (trunk
	// entry.go): a new session on the exit's drain, a retired one closed at
	// its limit with what it still carried, and what the entry's process
	// cut when it stopped.
	reExitDrains = regexp.MustCompile(`trunk: exit drains: new session, the old one keeps (\d+) stream\(s\) and (\d+) flow\(s\)$`)
	reRetiredCut = regexp.MustCompile(`trunk: retired session closed after \S+ with (\d+) stream\(s\) and (\d+) flow\(s\)$`)
	reClosedCut  = regexp.MustCompile(`trunk: closed with (\d+) stream\(s\) and (\d+) flow\(s\) cut$`)
)

// classify turns a journal line into a row of the entry or event layer;
// false for a line that says nothing the watch keeps.
func classify(l journalLine) (layer string, data map[string]any, ok bool) {
	if m := reStats.FindStringSubmatch(l.Msg); m != nil {
		return LayerEntry, statsFields(m[1]), true
	}
	if m := reCall.FindStringSubmatch(l.Msg); m != nil {
		return LayerCall, callFields(m[1]), true
	}
	if m := reMemberDown.FindStringSubmatch(l.Msg); m != nil {
		d, _ := time.ParseDuration(m[2])
		kind := "broken"
		if strings.Contains(m[3], "stalled") {
			kind = "stalled"
		}
		return LayerEvent, map[string]any{"kind": "member_down", "member": atoi(m[1]), "after_s": int(d.Seconds()), "why": kind, "err": clip(m[3])}, true
	}
	if m := reMemberFail.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "member_" + m[2] + "_failed", "member": atoi(m[1]), "err": clip(m[3])}, true
	}
	if m := rePaused.FindStringSubmatch(l.Msg); m != nil {
		// The entry's box did not run it: the pool waits it out instead of
		// burying its members, so this is the lag users felt.
		d, _ := time.ParseDuration(m[1])
		return LayerEvent, map[string]any{"kind": "entry_paused", "ms": d.Milliseconds()}, true
	}
	if m := reEpochReset.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "exit_epoch_changed", "epoch": m[1], "was": m[2]}, true
	}
	if m := reEpoch.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "exit_epoch", "epoch": m[1]}, true
	}
	if m := reHandoff.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "entry_handoff", "gen": atoi(m[1]), "was": atoi(m[2])}, true
	}
	if m := reDrains.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "entry_drains", "gen": atoi(m[1]), "streams": atoi(m[2]), "flows": atoi(m[3])}, true
	}
	if m := reLeft.FindStringSubmatch(l.Msg); m != nil {
		// Cut is what the old generation still carried at its limit: those
		// streams and flows ended there.
		return LayerEvent, map[string]any{"kind": "entry_generation_left", "gen": atoi(m[1]), "cut_streams": atoi(m[2]), "cut_flows": atoi(m[3])}, true
	}
	if m := reExitDrains.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "exit_drains", "streams": atoi(m[1]), "flows": atoi(m[2])}, true
	}
	if m := reRetiredCut.FindStringSubmatch(l.Msg); m != nil {
		// The exit's old process gave up its sessions at the same limit:
		// these ended there.
		return LayerEvent, map[string]any{"kind": "retired_session_cut", "cut_streams": atoi(m[1]), "cut_flows": atoi(m[2])}, true
	}
	if m := reClosedCut.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "entry_closed_cut", "cut_streams": atoi(m[1]), "cut_flows": atoi(m[2])}, true
	}
	if m := reUp.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "entry_up", "detail": clip(m[1])}, true
	}
	if m := reUnit.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "unit", "detail": clip(m[1])}, true
	}
	return "", nil, false
}

// statsFields reads the minute line's key=value pairs: counters as numbers,
// a/b pairs split (streams=open/opened, dgrams=out/in), rtt_* in ms or absent.
func statsFields(s string) map[string]any {
	out := map[string]any{}
	for _, kv := range strings.Fields(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case k == "streams" || k == "dgrams":
			a, b, _ := strings.Cut(v, "/")
			first, second := "open", "opened"
			if k == "dgrams" {
				first, second = "out", "in"
			}
			out[k+"_"+first] = atoi64(a)
			out[k+"_"+second] = atoi64(b)
		case strings.HasPrefix(k, "rtt_"):
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out[k+"_ms"] = f
			}
		default:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out[k] = n
			}
		}
	}
	return out
}

// callFields reads the call's minute line (trunkentry call.go): counts as
// numbers, rtt_* and the pauses in ms (rtt_* absent when nothing came back),
// lost added, worst as a list of {at, ms}.
func callFields(s string) map[string]any {
	out := map[string]any{}
	for _, kv := range strings.Fields(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case k == "worst":
			var worst []map[string]any
			for _, g := range strings.Split(v, ",") {
				at, ms, ok := strings.Cut(g, ":")
				if !ok {
					continue
				}
				worst = append(worst, map[string]any{"at": time.UnixMilli(atoi64(at)).UTC(), "ms": atoi64(ms)})
			}
			if len(worst) > 0 {
				out["worst"] = worst
			}
		case strings.HasPrefix(k, "rtt_"):
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out[k+"_ms"] = f
			}
		case k == "gap_max":
			out["gap_max_ms"] = atoi64(v)
		default:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out[k] = n
			}
		}
	}
	out["lost"] = num(out, "sent") - num(out, "got")
	return out
}

func atoi(s string) int     { n, _ := strconv.Atoi(s); return n }
func atoi64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func clip(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200])
	}
	return s
}
