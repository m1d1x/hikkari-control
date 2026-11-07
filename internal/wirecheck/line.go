// Package wirecheck holds a tester's ClientHello to what it should have been
// (hikkari-client plans/2026-11-07-knob-truth-wire-diagnostics.md, W3).
//
// A knob has three values: what the hub handed out in the link, what the
// client's config holds after parsing it, and what left on the wire. 03–06.11
// went to two of them parting silently: fp=chrome is Chrome 133 in uTLS, and
// chrome_155 in the xray client was Go's own hello. The client now writes a
// [diag:wire] line per TCP+TLS profile with the config's knobs and the hello a
// loopback mirror took from its core (hikkari-client
// engine/core/engine/singbox/diagnostics/wire.go). Here each line is held
// against the hello the same core revision sends for that fingerprint
// (wire_reference) and against the knobs the hub hands out for the node and
// the tester's network. What parts is a group in the client errors, not a line
// someone has to think of reading.
package wirecheck

import (
	"regexp"
	"strings"
)

var (
	reLineTS   = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+`)
	reWireLine = regexp.MustCompile(`\[diag:wire\] (p-\S+) (.*)$`)
	reCore     = regexp.MustCompile(`^[0-9a-f]{12}$`)
	reNetwork  = regexp.MustCompile(`^AS[1-9][0-9]{0,9}$`)
	reHK       = regexp.MustCompile(`^h1[0-9a-f]{16}$`)
	reJA4      = regexp.MustCompile(`^[tq][0-9s][0-9][di][0-9]{4}[0-9a-z]{2}_[0-9a-f]{12}_[0-9a-f]{12}$`)
	// reValue is what a knob or a wire field may hold: the client writes one
	// token per field and leaves out anything else.
	reValue = regexp.MustCompile(`^[A-Za-z0-9._:,/-]{1,128}$`)
)

// Knobs are the config's knobs the client writes on the line (loadKnobs).
var Knobs = []string{"fp", "sni", "port", "flow", "alpn"}

// States of a line.
const (
	StateHello = "hello" // a hello was read
	StateSkip  = "skip"  // the profile has no ClientHello over TCP (UDP, no TLS, XHTTP over h3)
	StateError = "error" // the mirror got nothing the core should have sent
)

// Line is one parsed [diag:wire] line.
type Line struct {
	ClientTS string
	Profile  string // the tag without p-: the node's label
	Proto    string
	Core     string
	Network  string
	Knobs    map[string]string
	State    string
	JA4      string
	HK       string
	ALPNSent string
	// Wire is every other field of the line, for the record.
	Wire map[string]string
	// Error is the mirror's own text after wire_error=.
	Error string
}

// ParseLine reads «2026/11/07 12:00:01.234 [diag:wire] p-x proto=vless-reality
// fp=chrome sni=a.b port=443 flow=xtls-rprx-vision core=f350f1e6fea7
// net=AS12389 ja4=… hk=… alpn_sent=h2,http/1.1 groups=… len=1756 records=1»,
// or the same head with «wire=skip why=udp» or «wire_error="…"».
func ParseLine(text string) (Line, bool) {
	m := reWireLine.FindStringSubmatch(text)
	if m == nil {
		return Line{}, false
	}
	l := Line{Profile: strings.TrimPrefix(m[1], "p-"), Knobs: map[string]string{}, Wire: map[string]string{}, State: StateHello}
	if ts := reLineTS.FindStringSubmatch(text); ts != nil {
		l.ClientTS = ts[1]
	}
	rest := m[2]
	if i := strings.Index(rest, "wire_error="); i >= 0 {
		l.State = StateError
		l.Error = strings.Trim(rest[i+len("wire_error="):], `" `)
		if len(l.Error) > 200 {
			l.Error = l.Error[:200]
		}
		rest = rest[:i]
	}
	fields := map[string]string{}
	for _, f := range strings.Fields(rest) {
		k, v, ok := strings.Cut(f, "=")
		if !ok || !reValue.MatchString(v) {
			continue
		}
		if _, seen := fields[k]; !seen {
			fields[k] = v
		}
	}
	for _, k := range Knobs {
		if v, ok := fields[k]; ok {
			l.Knobs[k] = v
		}
		delete(fields, k)
	}
	take := func(k string, re *regexp.Regexp) string {
		v := fields[k]
		delete(fields, k)
		if re != nil && !re.MatchString(v) {
			return ""
		}
		return v
	}
	l.Proto = take("proto", nil)
	l.Core = take("core", reCore)
	l.Network = take("net", reNetwork)
	l.JA4 = take("ja4", reJA4)
	l.HK = take("hk", reHK)
	l.ALPNSent = take("alpn_sent", nil)
	if fields["wire"] == "skip" {
		l.State = StateSkip
	}
	l.Wire = fields
	if l.State == StateHello && (l.HK == "" || l.JA4 == "") {
		// A hello line without its fingerprints is not one this hub can read.
		return Line{}, false
	}
	return l, true
}
