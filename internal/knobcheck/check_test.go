package knobcheck

import (
	"strings"
	"testing"
	"time"
)

func codes(fs []Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code+":"+f.Knob)
	}
	return strings.Join(out, " ")
}

var settled = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

// The relays the links are rendered from: a Reality relay racing two covers,
// an XHTTP relay in stream-up as the hub draws it (entry.XhttpStreamUp).
var (
	realityRelay = Relay{Readable: true, Covers: []string{"www.samsung.com", "pine6573.lol"}}
	xhttpRelay   = Relay{Readable: true, XhttpMode: "stream-up"}
)
var at = settled.Add(12 * time.Hour)

// Knobs as a tester round of the relay dataset holds them: from the
// [diag:load] line (hikkari-client diagnostics/load.go, loadKnobs).
var (
	realityChrome = map[string]string{"fp": "chrome", "sni": "www.samsung.com", "port": "8443", "flow": "xtls-rprx-vision"}
	xhttpChrome   = map[string]string{"fp": "chrome", "sni": "x.example", "port": "443", "flow": "none", "alpn": "h2/http/1.1", "mode": "stream-up"}
)

func with(base map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for key, val := range base {
		out[key] = val
	}
	if v == "" {
		delete(out, k)
	} else {
		out[k] = v
	}
	return out
}

func TestCheck(t *testing.T) {
	chrome := DeclaredFor(protoReality, "", "", "", realityRelay, settled)
	chrome155 := DeclaredFor(protoReality, "chrome_155", "", "", realityRelay, settled)
	for _, tc := range []struct {
		name   string
		params map[string]string
		d      Declared
		state  string
		want   string
	}{
		{"clean", realityChrome, chrome, StateClean, ""},
		{"the hub hands out chrome_155, the config holds chrome", realityChrome, chrome155, StateMismatch, "knob.mismatch:fp"},
		{"the config lost fp", with(realityChrome, "fp", ""), chrome, StateMismatch, "knob.mismatch:fp"},
		{"a change the client has not fetched yet", realityChrome, DeclaredFor(protoReality, "chrome_155", "", "", realityRelay, at.Add(-time.Hour)), StateSettling, ""},
		{"a round before the change", realityChrome, DeclaredFor(protoReality, "chrome_155", "", "", realityRelay, at.Add(time.Hour)), StateSettling, ""},
		{"an SNI the node names, the config holds another", realityChrome, DeclaredFor(protoReality, "", "pine6573.lol", "", realityRelay, settled), StateMismatch, "knob.mismatch:sni"},
		{"alpn on Reality the node did not hand out", with(realityChrome, "alpn", "h2"), chrome, StateMismatch, "knob.mismatch:alpn"},
		{"XHTTP: the link's alpn spelled two ways is one", xhttpChrome, DeclaredFor(protoXhttp, "", "", "", xhttpRelay, settled), StateClean, ""},
		{"XHTTP: no SNI knob, the name is the certificate's", xhttpChrome, DeclaredFor(protoXhttp, "", "other.example", "", xhttpRelay, settled), StateClean, ""},
		{"XHTTP: http/1.1 only in the config", with(xhttpChrome, "alpn", "http/1.1"), DeclaredFor(protoXhttp, "", "", "", xhttpRelay, settled), StateMismatch, "knob.mismatch:alpn"},
		{"the node's SNI is not among the relay's covers: the link keeps the first", realityChrome, DeclaredFor(protoReality, "", "other.example", "", realityRelay, settled), StateClean, ""},
		{"the node names no SNI, the config holds another cover", with(realityChrome, "sni", "pine6573.lol"), chrome, StateMismatch, "knob.mismatch:sni"},
		{"the relay's record is unreadable: SNI is not checked", with(realityChrome, "sni", "x.example"), DeclaredFor(protoReality, "", "", "", Relay{}, settled), StateClean, ""},
		{"key_share the hub never hands out", with(realityChrome, "key_share", "classical"), chrome, StateMismatch, "knob.mismatch:key_share"},
		{"XHTTP: the config runs auto, the relay hands out stream-up", with(xhttpChrome, "mode", "auto"), DeclaredFor(protoXhttp, "", "", "", xhttpRelay, settled), StateMismatch, "knob.mismatch:mode"},
		{"XHTTP: a relay without mode is auto in the client", with(xhttpChrome, "mode", "auto"), DeclaredFor(protoXhttp, "", "", "", Relay{Readable: true}, settled), StateClean, ""},
		{"XHTTP: an older round names no mode", with(xhttpChrome, "mode", ""), DeclaredFor(protoXhttp, "", "", "", xhttpRelay, settled), StateClean, ""},
		{"a link the tester imported", realityChrome, Declared{}, StateUnknown, ""},
		{"a round that names no knobs", map[string]string{}, chrome, StateUnknown, ""},
	} {
		state, fs := Check("amsterdam-1-002", "AS8402", tc.params, tc.d, at)
		if state != tc.state || codes(fs) != tc.want {
			t.Errorf("%s: got %s %q, want %s %q", tc.name, state, codes(fs), tc.state, tc.want)
		}
	}
}

func TestMismatchSaysWhatAndWhere(t *testing.T) {
	_, fs := Check("amsterdam-1-002", "AS8402", with(realityChrome, "fp", ""), DeclaredFor(protoReality, "chrome_155", "", "", realityRelay, settled), at)
	if len(fs) != 1 || fs[0].Want != "chrome_155" || fs[0].Got != "" ||
		fs[0].Msg != "p-amsterdam-1-002: hub выдаёт fp=chrome_155 для AS8402, в конфиге клиента ничего" {
		t.Fatalf("%+v", fs)
	}
	if _, fs := Check("x", "", with(realityChrome, "fp", "firefox"), DeclaredFor(protoReality, "", "", "", realityRelay, settled), at); !strings.Contains(fs[0].Msg, "chrome у всех") {
		t.Fatalf("%+v", fs)
	}
}

func TestDeclaredForFillsTheLinkDefaults(t *testing.T) {
	if d := DeclaredFor(protoXhttp, "", "x.example", "", xhttpRelay, settled); d.FP != "chrome" || d.ALPN != "h2,http/1.1" || d.SNI != "" || d.Mode != "stream-up" || !d.Known {
		t.Fatalf("%+v", d)
	}
	if d := DeclaredFor("hysteria2", "", "", "", Relay{}, settled); d.Known {
		t.Fatal("the Hysteria2 link carries none of the knobs")
	}
}

func TestErrorFPGroupsARepeat(t *testing.T) {
	f := Finding{Code: CodeKnobMismatch, Knob: "fp", Want: "chrome_155", Got: "chrome"}
	a, b := errorFP("amsterdam-1-002", f), errorFP("amsterdam-1-002", Finding{Code: f.Code, Knob: f.Knob, Want: f.Want, Got: f.Got, Msg: "other"})
	if a != b || len(a) != 16 {
		t.Fatalf("%s %s", a, b)
	}
	if a == errorFP("amsterdam-1-003", f) {
		t.Fatal("two profiles in one group")
	}
}
