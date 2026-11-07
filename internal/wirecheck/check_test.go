package wirecheck

import (
	"strings"
	"testing"
	"time"
)

// Lines as hikkari-client diagnostics/wire.go writes them, with the hellos of
// its testdata (sing-box-lx f350f1e6fea7).
const (
	lineChrome = `2026/11/07 12:00:01.234 [diag:wire] p-amsterdam-1-002 proto=vless-reality fp=chrome sni=www.samsung.com port=8443 flow=xtls-rprx-vision core=f350f1e6fea7 net=AS12389 ja4=t13d1516h2_8daaf6152771_d8a2da3f94cd hk=h1e431ec7444af069a tls=13 sni_sent=1 alpn_sent=h2,http/1.1 groups=grease,x25519mlkem768,x25519,p256,p384 shares=grease,x25519mlkem768,x25519 ext=16 ciphers=15 grease=1 ech=1 psk=0 pad=-1 len=1756 records=1`
	lineGo     = `2026/11/07 12:00:02.000 [diag:wire] p-imported proto=vless-ws sni=cdn.example.org port=443 flow=none core=f350f1e6fea7 ja4=t13d131100_f57a46bbacb6_ab7e3b40a677 hk=h11c409d9ffa0e9507 tls=13 sni_sent=1 alpn_sent=- groups=x25519mlkem768,x25519,p256,p384,p521 shares=x25519mlkem768,x25519 ext=11 ciphers=13 grease=0 ech=0 psk=0 pad=-1 len=1502 records=1`
	lineSkip   = `2026/11/07 12:00:03.000 [diag:wire] p-hy2 proto=hysteria2 port=443 core=f350f1e6fea7 net=AS12389 wire=skip why=udp`
	lineError  = `2026/11/07 12:00:04.000 [diag:wire] p-amsterdam-1-002 proto=vless-reality fp=chrome port=8443 flow=xtls-rprx-vision core=f350f1e6fea7 wire_error="the core sent nothing"`
)

var ref = Reference{
	"chrome":     {HK: "h1e431ec7444af069a", JA4: "t13d1516h2_8daaf6152771_d8a2da3f94cd"},
	"chrome_155": {HK: "h1121e2b99d2744911", JA4: "t13d1517h2_8daaf6152771_cb7bf5808d99"},
	"firefox":    {HK: "h1a7f29bb57cd8c350", JA4: "t13d1715h2_5b57614c22b0_a54fffd0eb61"},
	FPGo:         {HK: "h11c409d9ffa0e9507", JA4: "t13d131100_f57a46bbacb6_ab7e3b40a677"},
}

func mustParse(t *testing.T, text string) Line {
	t.Helper()
	l, ok := ParseLine(text)
	if !ok {
		t.Fatalf("not read: %s", text)
	}
	return l
}

func TestParseLine(t *testing.T) {
	l := mustParse(t, lineChrome)
	if l.ClientTS != "2026/11/07 12:00:01.234" || l.Profile != "amsterdam-1-002" || l.Proto != "vless-reality" ||
		l.Core != "f350f1e6fea7" || l.Network != "AS12389" || l.State != StateHello ||
		l.HK != "h1e431ec7444af069a" || l.ALPNSent != "h2,http/1.1" {
		t.Fatalf("%+v", l)
	}
	if l.Knobs["fp"] != "chrome" || l.Knobs["sni"] != "www.samsung.com" || l.Knobs["flow"] != "xtls-rprx-vision" || len(l.Knobs) != 4 {
		t.Fatalf("knobs %v", l.Knobs)
	}
	if l.Wire["groups"] != "grease,x25519mlkem768,x25519,p256,p384" || l.Wire["len"] != "1756" || l.Wire["fp"] != "" {
		t.Fatalf("wire %v", l.Wire)
	}
	if s := mustParse(t, lineSkip); s.State != StateSkip || s.Wire["why"] != "udp" {
		t.Fatalf("%+v", s)
	}
	if e := mustParse(t, lineError); e.State != StateError || e.Error != "the core sent nothing" {
		t.Fatalf("%+v", e)
	}
	for _, bad := range []string{
		`[diag:load] p-x proto=vless dl=1/1`,
		`[diag:wire] p-x proto=vless fp=chrome ja4=bad hk=h1e431ec7444af069a`, // a hello without a readable JA4
		`[diag:wire] p-x proto=vless fp=chrome ja4=t13d1516h2_8daaf6152771_d8a2da3f94cd`,
	} {
		if _, ok := ParseLine(bad); ok {
			t.Errorf("read: %s", bad)
		}
	}
}

func codes(fs []Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code+":"+f.Knob)
	}
	return strings.Join(out, " ")
}

var settled = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
var at = settled.Add(12 * time.Hour)

func TestCheck(t *testing.T) {
	chrome := DeclaredFor(protoReality, "", "", "", settled)
	chrome155 := DeclaredFor(protoReality, "chrome_155", "", "", settled)
	for _, tc := range []struct {
		name string
		line string
		ref  Reference
		d    Declared
		at   time.Time
		want string
	}{
		{"clean", lineChrome, ref, chrome, at, ""},
		{"a fork update changed the hello", lineChrome, Reference{"chrome": ref["chrome_155"]}, chrome, at, "wire.mismatch:fp"},
		{"no reference for the core", lineChrome, nil, chrome, at, "wire.no_reference:fp"},
		{"no reference for the fp", lineChrome, Reference{"firefox": ref["firefox"]}, chrome, at, "wire.no_reference:fp"},
		{"TLS without fp sends Go", lineGo, ref, Declared{}, at, "wire.go_hello:fp"},
		{"a dropped fp sends Go", strings.Replace(strings.Replace(lineChrome, "hk=h1e431ec7444af069a", "hk=h11c409d9ffa0e9507", 1), "fp=chrome ", "fp=chrome_155 ", 1),
			ref, chrome155, at, "wire.go_hello:fp"},
		{"the hub hands out chrome_155, the config holds chrome", lineChrome, ref, chrome155, at, "knob.mismatch:fp"},
		{"a change the client has not fetched yet", lineChrome, ref, DeclaredFor(protoReality, "chrome_155", "", "", at.Add(-time.Hour)), at, ""},
		{"an SNI the relay does not serve stays the first cover", lineChrome, ref, DeclaredFor(protoReality, "", "pine6573.lol", "", settled), at, "knob.mismatch:sni"},
		{"the config's alpn is not the wire's", strings.Replace(lineChrome, "flow=xtls-rprx-vision", "flow=xtls-rprx-vision alpn=h2", 1), ref, chrome, at,
			"wire.mismatch:alpn knob.mismatch:alpn"},
		{"XHTTP: the link's alpn spelled two ways is one", strings.Replace(lineChrome, "flow=xtls-rprx-vision", "flow=none alpn=h2/http/1.1", 1), ref,
			DeclaredFor(protoXhttp, "", "", "", settled), at, ""},
		{"randomized has no reference", strings.Replace(lineChrome, "fp=chrome ", "fp=randomized ", 1), ref, Declared{}, at, ""},
		{"no core named", strings.Replace(lineChrome, "core=f350f1e6fea7 ", "", 1), nil, Declared{}, at, "wire.no_reference:fp"},
		{"skip", lineSkip, ref, chrome, at, ""},
		{"mirror got nothing", lineError, ref, chrome, at, "wire.unread:"},
	} {
		got := codes(Check(mustParse(t, tc.line), tc.ref, tc.d, tc.at))
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMismatchNamesWhatWasSent(t *testing.T) {
	line := strings.Replace(lineChrome, "hk=h1e431ec7444af069a", "hk=h1a7f29bb57cd8c350", 1)
	fs := Check(mustParse(t, line), ref, Declared{}, at)
	if len(fs) != 1 || !strings.Contains(fs[0].Msg, "это хелло fp=firefox") || !strings.Contains(fs[0].Msg, "f350f1e6fea7") {
		t.Fatalf("%+v", fs)
	}
}

func TestDeclaredForFillsTheLinkDefaults(t *testing.T) {
	if d := DeclaredFor(protoXhttp, "", "x.example", "", settled); d.FP != "chrome" || d.ALPN != "h2,http/1.1" || d.SNI != "" || !d.Known {
		t.Fatalf("%+v", d)
	}
	if d := DeclaredFor("hysteria2", "", "", "", settled); d.Known {
		t.Fatal("Hysteria2 has no TCP hello to hold")
	}
}

func TestErrorFPGroupsARepeat(t *testing.T) {
	f := Finding{Code: CodeWireMismatch, Knob: "fp", Want: "h1e431ec7444af069a", Got: "h1"}
	a, b := errorFP("amsterdam-1-002", "f350f1e6fea7", f), errorFP("amsterdam-1-002", "f350f1e6fea7", Finding{Code: f.Code, Knob: f.Knob, Want: f.Want, Got: "other"})
	if a != b || len(a) != 16 {
		t.Fatalf("%s %s", a, b)
	}
	if a == errorFP("amsterdam-1-003", "f350f1e6fea7", f) {
		t.Fatal("two profiles in one group")
	}
}
