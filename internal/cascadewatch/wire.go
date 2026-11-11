package cascadewatch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The stand of the trunk's second wire (hikkari-control edge/wirestand.go,
// hikkari-client plans/2026-11-11-trunk-second-wire.md): one process per
// candidate wire on the entry, units hikkari-wire-stand@<wire>, beside the
// entry's own process. Its call is the trunk's call over another wire, so its
// minutes go to the call layer under target wire-<wire>, next to copies1 and
// copies2; its 1 MiB and its CPU go to the wire layer.

// LayerWire is the stand's bulk and CPU (migration 059).
const LayerWire = "wire"

// wireStandUnits is what journalctl reads beside the entry's unit.
const wireStandUnits = "hikkari-wire-stand@*"

// The entry's own wires (hikkari-control trunkentry/wires.go, stage 4) say
// the same lines in its own journal: their rows go under target trunk-<wire>
// (call layer) and trunk-<wire> (wire layer), apart from the stand's
// wire-<wire> and <wire>, which may run beside them.
var (
	reWireCall = regexp.MustCompile(`(hikkari-wire-stand|nekko-trunk-entry): call wire=([a-z0-9]+) (copies=\d+ .*)$`)
	reWireBulk = regexp.MustCompile(`(hikkari-wire-stand|nekko-trunk-entry): bulk wire=([a-z0-9]+) (.*)$`)
	reWireCPU  = regexp.MustCompile(`hikkari-wire-stand: cpu wire=([a-z0-9]+) (.*)$`)
)

// wireTargets are a line's targets in the call and the wire layers.
func wireTargets(source, wire string) (call, layer string) {
	if source == "nekko-trunk-entry" {
		return "trunk-" + wire, "trunk-" + wire
	}
	return "wire-" + wire, wire
}

// classifyWire turns a line of the stand into a row; false for any other line.
func classifyWire(l journalLine) (Run, bool) {
	l.Msg = unwrapLogJSON(l.Msg)
	if m := reWireCall.FindStringSubmatch(l.Msg); m != nil {
		data := callFields(m[3])
		data["wire"] = m[2]
		target, _ := wireTargets(m[1], m[2])
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerCall, Target: target,
			OK: boolp(callOK(data)), Data: data}, true
	}
	if m := reWireBulk.FindStringSubmatch(l.Msg); m != nil {
		_, target := wireTargets(m[1], m[2])
		data := wireFields(m[3])
		kind, _ := data["kind"].(string)
		// A load holds 100 Mbit/s for its time: it is whole at 90% of
		// what that pace gives; any other bulk only when every byte came.
		ok := data["end"] == "ok" && num(data, "bytes") >= num(data, "want")
		data["mode"] = "bulk"
		if kind != "" {
			data["mode"] = "bulk_" + kind
		}
		if kind == "load" {
			ok = data["end"] == "ok" && num(data, "bytes") >= 0.9*num(data, "want")
		}
		if !ok {
			data["why"] = fmt.Sprintf("%s: %d из %d байт, %v", bulkName(kind), int64(num(data, "bytes")), int64(num(data, "want")), data["end"])
		}
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerWire, Target: target, OK: boolp(ok), Data: data}, true
	}
	if m := reWireCPU.FindStringSubmatch(l.Msg); m != nil {
		data := wireFields(m[2])
		data["mode"] = "cpu"
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerWire, Target: m[1], OK: boolp(true), Data: data}, true
	}
	return Run{}, false
}

// bulkName says which bulk a red row was.
func bulkName(kind string) string {
	switch kind {
	case "12m":
		return "12 МиБ"
	case "load":
		return "нагрузка 100 Мбит/с"
	}
	return "1 МиБ"
}

// unwrapLogJSON is the message of a line the edge's JSON logger wrote (its
// first builds of the stand logged that way), the line itself otherwise.
func unwrapLogJSON(msg string) string {
	i := strings.Index(msg, `{"level":`)
	if i < 0 {
		return msg
	}
	var v struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal([]byte(msg[i:]), &v) != nil || v.Msg == "" {
		return msg
	}
	return v.Msg
}

// wireFields reads key=value pairs: numbers as numbers, the rest as text.
func wireFields(s string) map[string]any {
	out := map[string]any{}
	for _, kv := range strings.Fields(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out[k] = f
		} else {
			out[k] = v
		}
	}
	return out
}

// The entry's selector (hikkari-protocol trunk/selector.go, Entry.Steer) says
// when a class moves to another wire, "trunk: route: <part>; <part>", one
// part per class, and when the set of red wires changes, "trunk: red wires
// udp,quic" or "none". Each part is an event: kind route, the class, the wire
// it left (none on the first choice) and the one it went to, and why: first
// (nothing chosen before), red (the old wire went red) or better (the new one
// was better by the margin for the hold). Since 12.11 the selector holds a
// red wire within 10 s of the entry's own pause, or while every wire is red,
// and says when the set of held ones changes: "trunk: held red wires udp:
// <reason>" or "none: ...", an event wires_held.
var (
	reRoute       = regexp.MustCompile(`trunk: route: (.+)$`)
	reRouteFirst  = regexp.MustCompile(`^(voice|bulk): on ([a-z0-9]+)$`)
	reRouteRed    = regexp.MustCompile(`^(voice|bulk): ([a-z0-9]+) is red, to ([a-z0-9]+)$`)
	reRouteBetter = regexp.MustCompile(`^(voice|bulk): ([a-z0-9]+) better than ([a-z0-9]+) for (\S+)$`)
	reRedWires    = regexp.MustCompile(`trunk: red wires ([a-z0-9,]+)$`)
	reHeldWires   = regexp.MustCompile(`trunk: held red wires ([a-z0-9,]+):`)
)

// classifyRoute turns a line of the selector into its events; false for any
// other line.
func classifyRoute(l journalLine) ([]Run, bool) {
	if m := reRedWires.FindStringSubmatch(l.Msg); m != nil {
		wires := []string{}
		if m[1] != "none" {
			wires = strings.Split(m[1], ",")
		}
		return []Run{{TS: l.TS, Layer: LayerEvent, Data: map[string]any{"kind": "wires_red", "wires": wires}}}, true
	}
	if m := reHeldWires.FindStringSubmatch(l.Msg); m != nil {
		wires := []string{}
		if m[1] != "none" {
			wires = strings.Split(m[1], ",")
		}
		return []Run{{TS: l.TS, Layer: LayerEvent, Data: map[string]any{"kind": "wires_held", "wires": wires}}}, true
	}
	m := reRoute.FindStringSubmatch(l.Msg)
	if m == nil {
		return nil, false
	}
	var out []Run
	for _, part := range strings.Split(m[1], "; ") {
		data := map[string]any{"kind": "route"}
		switch {
		case reRouteFirst.MatchString(part):
			p := reRouteFirst.FindStringSubmatch(part)
			data["class"], data["from"], data["to"], data["why"] = p[1], "none", p[2], "first"
		case reRouteRed.MatchString(part):
			p := reRouteRed.FindStringSubmatch(part)
			data["class"], data["from"], data["to"], data["why"] = p[1], p[2], p[3], "red"
		case reRouteBetter.MatchString(part):
			p := reRouteBetter.FindStringSubmatch(part)
			data["class"], data["from"], data["to"], data["why"], data["hold"] = p[1], p[3], p[2], "better", p[4]
		default:
			// A part this hub does not know yet: kept as said.
			data["detail"] = clip(part)
		}
		out = append(out, Run{TS: l.TS, Layer: LayerEvent, Data: data})
	}
	return out, true
}
