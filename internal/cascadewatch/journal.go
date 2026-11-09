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
	reMemberDown = regexp.MustCompile(`trunk: member (\d+): down after (\S+): (.*)$`)
	reMemberFail = regexp.MustCompile(`trunk: member (\d+): (dial|hello): (.*)$`)
	reEpochReset = regexp.MustCompile(`trunk: exit epoch ([0-9a-f]+) after ([0-9a-f]+): streams reset`)
	reEpoch      = regexp.MustCompile(`trunk: exit epoch ([0-9a-f]+)$`)
	reUp         = regexp.MustCompile(`nekko-trunk-entry: up: (.*)$`)
	reUnit       = regexp.MustCompile(`nekko-trunk-entry\.service: (.*)$`)
)

// classify turns a journal line into a row of the entry or event layer;
// false for a line that says nothing the watch keeps.
func classify(l journalLine) (layer string, data map[string]any, ok bool) {
	if m := reStats.FindStringSubmatch(l.Msg); m != nil {
		return LayerEntry, statsFields(m[1]), true
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
	if m := reEpochReset.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "exit_epoch_changed", "epoch": m[1], "was": m[2]}, true
	}
	if m := reEpoch.FindStringSubmatch(l.Msg); m != nil {
		return LayerEvent, map[string]any{"kind": "exit_epoch", "epoch": m[1]}, true
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

func atoi(s string) int     { n, _ := strconv.Atoi(s); return n }
func atoi64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func clip(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200])
	}
	return s
}
