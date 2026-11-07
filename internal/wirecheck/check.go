package wirecheck

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Codes of a finding, as the client errors list shows them.
const (
	// CodeWireMismatch: the hello is not the one this core sends for the
	// fingerprint, or its ALPN is not the config's.
	CodeWireMismatch = "wire.mismatch"
	// CodeGoHello: the wire carries crypto/tls's own hello, whatever the
	// config names: a TLS profile without fp, or a fingerprint the core
	// dropped (chrome_155 in the xray client, 06.11).
	CodeGoHello = "wire.go_hello"
	// CodeNoReference: no hello of this core revision is on record for the
	// fingerprint, so what left cannot be held to anything.
	CodeNoReference = "wire.no_reference"
	// CodeUnread: the mirror got no hello from a profile that should send one.
	CodeUnread = "wire.unread"
	// CodeKnobMismatch: the config's knob is not what the hub hands out for
	// the node and the tester's network.
	CodeKnobMismatch = "knob.mismatch"
)

// FPGo is the reference name of a TLS hello without uTLS.
const FPGo = "go"

// Finding is one thing a line does not hold to.
type Finding struct {
	Code string `json:"code"`
	Knob string `json:"knob,omitempty"`
	Want string `json:"want,omitempty"`
	Got  string `json:"got,omitempty"`
	Msg  string `json:"msg"`
}

// Reference is what a core revision sends for each fingerprint name: fp → hk.
// Nil is a revision with nothing on record.
type Reference map[string]RefHello

// RefHello is one recorded hello.
type RefHello struct {
	HK  string `json:"hk"`
	JA4 string `json:"ja4"`
}

// Declared is what the hub hands out for the profile's node in the tester's
// network. Known is false for a profile that is no node of ours (a link the
// tester imported) or a protocol with nothing to compare.
type Declared struct {
	Known bool
	Proto string
	FP    string
	SNI   string // "" when the node names none: the link keeps the relay's first cover
	ALPN  string // as links write it: h2,http/1.1; "" for none
	// ChangedAt is when any of them last changed for this node or network.
	ChangedAt time.Time
}

// Settle is how long after a change of the handed-out knobs a client's config
// may still hold the old ones: the subscription asks for a refresh every hour
// (Profile-Update-Interval: 1, internal/subscription), and a client may be
// asleep or offline over one of them.
const Settle = 2 * time.Hour

// Node protocols the hub hands out with a TLS ClientHello over TCP.
const (
	protoReality = "vless-reality-vision"
	protoXhttp   = "vless-xhttp"
)

// DeclaredFor fills in what the link carries when the node sets nothing
// (hikkari-control internal/subscription/links.go): fp=chrome on both,
// alpn h2,http/1.1 on XHTTP and none on Reality.
func DeclaredFor(proto, fp, sni, alpn string, changedAt time.Time) Declared {
	d := Declared{Known: proto == protoReality || proto == protoXhttp, Proto: proto, FP: fp, SNI: sni, ALPN: alpn, ChangedAt: changedAt}
	if d.FP == "" {
		d.FP = "chrome"
	}
	if d.ALPN == "" && proto == protoXhttp {
		d.ALPN = "h2,http/1.1"
	}
	if proto != protoReality {
		d.SNI = "" // on XHTTP the name is the relay's certificate, no knob
	}
	return d
}

// perProcess are fingerprints the core draws anew per process: no recorded
// hello stands for them.
var perProcess = map[string]bool{"random": true, "randomized": true}

// alpnKey folds the two spellings of an ALPN list into one: the config's
// «h2/http/1.1» (the client joins with /, which http/1.1 also holds) and the
// wire's or the link's «h2,http/1.1».
func alpnKey(s string) string {
	if s == "-" {
		return ""
	}
	return strings.ReplaceAll(s, ",", "/")
}

// Check holds one line to the reference of its core and to what the hub
// handed out. at is when the line was measured.
func Check(l Line, ref Reference, d Declared, at time.Time) []Finding {
	var out []Finding
	tag := "p-" + l.Profile
	switch l.State {
	case StateSkip:
		return nil
	case StateError:
		return []Finding{{Code: CodeUnread, Got: l.Error,
			Msg: fmt.Sprintf("%s: зеркало не получило ClientHello: %s", tag, l.Error)}}
	}
	fp := l.Knobs["fp"]
	switch {
	case fp == "":
		out = append(out, Finding{Code: CodeGoHello, Knob: "fp", Got: l.HK,
			Msg: fmt.Sprintf("%s: в конфиге нет fp, на провод уходит хелло crypto/tls (%s)", tag, l.JA4)})
	case ref != nil && ref[FPGo].HK == l.HK:
		out = append(out, Finding{Code: CodeGoHello, Knob: "fp", Want: fp, Got: l.HK,
			Msg: fmt.Sprintf("%s: fp=%s, а на провод уходит хелло crypto/tls (%s)", tag, fp, l.JA4)})
	case perProcess[fp]:
	case l.Core == "":
		out = append(out, Finding{Code: CodeNoReference, Knob: "fp", Want: fp,
			Msg: fmt.Sprintf("%s: клиент не назвал ревизию ядра, хелло fp=%s не с чем сверить", tag, fp)})
	case ref == nil || ref[fp].HK == "":
		out = append(out, Finding{Code: CodeNoReference, Knob: "fp", Want: fp, Got: l.HK,
			Msg: fmt.Sprintf("нет эталона хелло ядра %s для fp=%s: ушло %s (%s)", l.Core, fp, l.HK, l.JA4)})
	case ref[fp].HK != l.HK:
		out = append(out, Finding{Code: CodeWireMismatch, Knob: "fp", Want: ref[fp].HK, Got: l.HK,
			Msg: fmt.Sprintf("%s: ядро %s при fp=%s шлёт %s (%s), эталон %s (%s)%s",
				tag, l.Core, fp, l.HK, l.JA4, ref[fp].HK, ref[fp].JA4, sameAs(ref, l.HK))})
	}
	if want := alpnKey(l.Knobs["alpn"]); want != "" && want != alpnKey(l.ALPNSent) {
		out = append(out, Finding{Code: CodeWireMismatch, Knob: "alpn", Want: want, Got: l.ALPNSent,
			Msg: fmt.Sprintf("%s: alpn в конфиге %s, в ClientHello %s", tag, l.Knobs["alpn"], l.ALPNSent)})
	}
	if !d.Known || at.Sub(d.ChangedAt) < Settle {
		return out
	}
	where := "у всех"
	if l.Network != "" {
		where = "для " + l.Network
	}
	knob := func(name, want, got string) {
		if want == got {
			return
		}
		shown := got
		if shown == "" {
			shown = "ничего"
		}
		out = append(out, Finding{Code: CodeKnobMismatch, Knob: name, Want: want, Got: got,
			Msg: fmt.Sprintf("%s: hub выдаёт %s=%s %s, в конфиге клиента %s", tag, name, want, where, shown)})
	}
	knob("fp", d.FP, fp)
	if d.SNI != "" {
		knob("sni", d.SNI, l.Knobs["sni"])
	}
	knob("alpn", alpnKey(d.ALPN), alpnKey(l.Knobs["alpn"]))
	return out
}

// sameAs names the fingerprint whose recorded hello this one is, if any: «sends
// what firefox sends» says more than two hashes.
func sameAs(ref Reference, hk string) string {
	names := make([]string, 0, len(ref))
	for name, r := range ref {
		if r.HK == hk {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return ", это хелло fp=" + strings.Join(names, "/")
}
