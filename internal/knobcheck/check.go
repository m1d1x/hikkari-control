// Package knobcheck holds the knobs a tester's config ran with against the
// knobs the hub hands out for the node and the tester's network
// (hikkari-client plans/2026-11-07-knob-truth-wire-diagnostics.md).
//
// A knob has three values: what the hub hands out in the link, what the
// client's config holds, and what leaves on the wire. The wire is set by the
// core's revision and the config alone, so the release's test pins it
// (hikkari-client diagnostics/wire_test.go); what a node receives is the node's
// to say. What is left to the testers is the middle one: whether the config is
// what the hub handed out. The relay dataset already holds it for every load
// round (relay_runs, source tester, params from the [diag:load] line or the
// [diag:config] before it), and this package reads it from there.
package knobcheck

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// CodeKnobMismatch is a finding's code, as the client errors list shows it:
// the config's knob is not what the hub hands out for the node and the
// tester's network.
const CodeKnobMismatch = "knob.mismatch"

// Finding is one knob the config does not hold as handed out.
type Finding struct {
	Code string `json:"code"`
	Knob string `json:"knob"`
	Want string `json:"want"`
	Got  string `json:"got"`
	Msg  string `json:"msg"`
}

// Declared is what the hub hands out for the profile's node in the tester's
// network. Known is false for a profile that is no node of ours (a link the
// tester imported) or a protocol with nothing to compare.
type Declared struct {
	Known bool
	Proto string
	FP    string
	SNI   string // "" when it cannot be told: the relay's covers are unreadable
	ALPN  string // as links write it: h2,http/1.1; "" for none
	Mode  string // XHTTP: the relay's mode, auto when it sets none; "" when unknown or not XHTTP
	// ChangedAt is when any of them last changed for this node or network.
	ChangedAt time.Time
}

// Relay is what the link takes from the node's relay record: Reality's covers
// in race order and XHTTP's mode. Readable is false when the hub cannot open
// the record; the knobs that come from it are then not checked.
type Relay struct {
	Readable  bool
	Covers    []string
	XhttpMode string
}

// Settle is how long after a change of the handed-out knobs a client's config
// may still hold the old ones: the subscription asks for a refresh every hour
// (Profile-Update-Interval: 1, internal/subscription), and a client may be
// asleep or offline over one of them.
const Settle = 2 * time.Hour

// Node protocols whose links carry fp, sni and alpn.
const (
	protoReality = "vless-reality-vision"
	protoXhttp   = "vless-xhttp"
)

// DeclaredFor is the link the hub renders for the node
// (internal/subscription/links.go and namedLink): fp=chrome unless the node
// names one; on Reality the node's SNI when the relay serves it, else the
// relay's first cover, and no alpn unless the node names one; on XHTTP
// alpn h2,http/1.1 unless the node names one, no SNI knob (the name is the
// relay's certificate) and the relay's mode, which the client reads as auto
// when the link has none. No link carries key_share: the client must hold none.
func DeclaredFor(proto, fp, sni, alpn string, relay Relay, changedAt time.Time) Declared {
	d := Declared{Known: proto == protoReality || proto == protoXhttp, Proto: proto, FP: fp, ALPN: alpn, ChangedAt: changedAt}
	if d.FP == "" {
		d.FP = "chrome"
	}
	switch proto {
	case protoReality:
		switch {
		case !relay.Readable || len(relay.Covers) == 0:
		case sni != "" && slices.Contains(relay.Covers, sni):
			d.SNI = sni
		default:
			d.SNI = relay.Covers[0]
		}
	case protoXhttp:
		if d.ALPN == "" {
			d.ALPN = "h2,http/1.1"
		}
		if relay.Readable {
			d.Mode = relay.XhttpMode
			if d.Mode == "" {
				d.Mode = "auto"
			}
		}
	}
	return d
}

// alpnKey folds the two spellings of an ALPN list into one: the config's
// «h2/http/1.1» (the client joins with /, which http/1.1 also holds) and the
// link's «h2,http/1.1».
func alpnKey(s string) string {
	if s == "-" {
		return ""
	}
	return strings.ReplaceAll(s, ",", "/")
}

// States of a checked round.
const (
	StateClean    = "clean"    // the config holds what was handed out
	StateMismatch = "mismatch" // at least one finding
	StateUnknown  = "unknown"  // no node of ours, or the round names no knobs
	StateSettling = "settling" // the knobs changed less than Settle before the round
)

// Check holds the knobs a round ran with (relay_runs.params) to what the hub
// handed out. at is when the round was measured, network the tester's
// («AS8402», "" when unknown).
func Check(profile, network string, params map[string]string, d Declared, at time.Time) (string, []Finding) {
	switch {
	case !d.Known || len(params) == 0:
		return StateUnknown, nil
	case at.Sub(d.ChangedAt) < Settle:
		return StateSettling, nil
	}
	tag := "p-" + profile
	where := "у всех"
	if network != "" {
		where = "для " + network
	}
	var out []Finding
	knob := func(name, want, got string) {
		if want == got {
			return
		}
		shown, wanted := got, want
		if shown == "" {
			shown = "ничего"
		}
		if wanted == "" {
			wanted = "ничего"
		}
		out = append(out, Finding{Code: CodeKnobMismatch, Knob: name, Want: want, Got: got,
			Msg: fmt.Sprintf("%s: hub выдаёт %s=%s %s, в конфиге клиента %s", tag, name, wanted, where, shown)})
	}
	knob("fp", d.FP, params["fp"])
	if d.SNI != "" {
		knob("sni", d.SNI, params["sni"])
	}
	knob("alpn", alpnKey(d.ALPN), alpnKey(params["alpn"]))
	// A client writes mode for every XHTTP outbound (the parser sets auto when
	// the link has none), so a round without it is an older client or a config
	// the hub could not read, not a missing knob.
	if got, ok := params["mode"]; ok && d.Mode != "" {
		knob("mode", d.Mode, got)
	}
	if d.Proto == protoReality {
		knob("key_share", "", params["key_share"])
	}
	if len(out) > 0 {
		return StateMismatch, out
	}
	return StateClean, nil
}
