package cascadewatch

import (
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

var (
	reWireCall = regexp.MustCompile(`hikkari-wire-stand: call wire=([a-z0-9]+) (copies=\d+ .*)$`)
	reWireBulk = regexp.MustCompile(`hikkari-wire-stand: bulk wire=([a-z0-9]+) (.*)$`)
	reWireCPU  = regexp.MustCompile(`hikkari-wire-stand: cpu wire=([a-z0-9]+) (.*)$`)
)

// classifyWire turns a line of the stand into a row; false for any other line.
func classifyWire(l journalLine) (Run, bool) {
	if m := reWireCall.FindStringSubmatch(l.Msg); m != nil {
		data := callFields(m[2])
		data["wire"] = m[1]
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerCall, Target: "wire-" + m[1],
			OK: boolp(callOK(data)), Data: data}, true
	}
	if m := reWireBulk.FindStringSubmatch(l.Msg); m != nil {
		data := wireFields(m[2])
		data["mode"] = "bulk"
		ok := num(data, "bytes") >= num(data, "want") && data["end"] == "ok"
		if !ok {
			data["why"] = fmt.Sprintf("1 МиБ: %d байт, %v", int64(num(data, "bytes")), data["end"])
		}
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerWire, Target: m[1], OK: boolp(ok), Data: data}, true
	}
	if m := reWireCPU.FindStringSubmatch(l.Msg); m != nil {
		data := wireFields(m[2])
		data["mode"] = "cpu"
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerWire, Target: m[1], OK: boolp(true), Data: data}, true
	}
	return Run{}, false
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
