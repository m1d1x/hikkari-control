package cascadewatch

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The entry's matrix of silences (trunkentry matrix.go): a minute of each
// target as a call row under target mx-<name>, the box's minute under
// mx-box (its ticker's latest turn as gap_max: the entry's own pause), and
// each receiver's silence of 500 ms or more as an event with its verdict:
//
//	nekko-trunk-entry: matrix target=<name> kind=<k> every_ms=N from=… sent=… … worst=…
//	nekko-trunk-entry: matrix box from=… late_max_ms=… late_over100=… late_over500=… steal_max_pct=… steal_avg_pct=… rx_drop=… tx_drop=…
//	nekko-trunk-entry: matrix silence at=<unix ms> len_ms=… silent=… heard=… unknown=… box_late_ms=… verdict=…
var (
	reMatrixTarget  = regexp.MustCompile(`nekko-trunk-entry: matrix target=(\S+) kind=(\S+) (.*)$`)
	reMatrixBox     = regexp.MustCompile(`nekko-trunk-entry: matrix box (.*)$`)
	reMatrixSilence = regexp.MustCompile(`nekko-trunk-entry: matrix silence (.*)$`)
)

// matrixBoxRed: the entry's ticker late past it in a minute makes the box's
// minute red, as a pause of a call over 150 ms.
const matrixBoxRed = 150

// classifyMatrix turns a matrix line into its row; false for any other.
func classifyMatrix(l journalLine) (Run, bool) {
	msg := unwrapLogJSON(l.Msg)
	if m := reMatrixTarget.FindStringSubmatch(msg); m != nil {
		data := callFields(m[3])
		data["kind"] = m[2]
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerCall, Target: "mx-" + m[1], OK: boolp(callOK(data)), Data: data}, true
	}
	if m := reMatrixBox.FindStringSubmatch(msg); m != nil {
		data := map[string]any{}
		for _, kv := range strings.Fields(m[1]) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				data[k] = f
			}
		}
		data["gap_max_ms"] = num(data, "late_max_ms")
		return Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerCall, Target: "mx-box",
			OK: boolp(num(data, "late_max_ms") <= matrixBoxRed), Data: data}, true
	}
	if m := reMatrixSilence.FindStringSubmatch(msg); m != nil {
		data := map[string]any{"kind": "silence"}
		var at int64
		for _, kv := range strings.Fields(m[1]) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "at":
				at, _ = strconv.ParseInt(v, 10, 64)
			case "len_ms", "box_late_ms":
				n, _ := strconv.ParseInt(v, 10, 64)
				data[k] = n
			case "silent", "heard", "unknown":
				if v != "-" {
					data[k] = strings.Split(v, ",")
				}
			default:
				data[k] = v
			}
		}
		return Run{TS: time.UnixMilli(at).UTC(), Layer: LayerEvent, Data: data}, true
	}
	return Run{}, false
}
