package hikkariobs

import (
	"encoding/json"
	"testing"
)

func TestFlatten(t *testing.T) {
	period := json.RawMessage(`{"tcp":{"conns":2,"rtt":{"n":3,"p50":40,"p95":80,"max":120}},
		"quic":{"voice":{"loss_pm":null,"ends":{"idle":1}}},"dup":{"only_stream":7},"tunnels":{"eof":4}}`)
	got, err := Flatten(period)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"tcp.conns": 2, "tcp.rtt.n": 3, "tcp.rtt.p50": 40, "tcp.rtt.p95": 80, "tcp.rtt.max": 120,
		"quic.voice.ends.idle": 1, "dup.only_stream": 7, "tunnels.eof": 4}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v (%v)", k, got[k], v, got)
		}
	}
	for _, bad := range []string{`7`, `{"Tcp":{"conns":1}}`, `{"a":{"b":{"c":{"d":{"e":{"f":1}}}}}}`, `{`} {
		if _, err := Flatten(json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	rows, err := Rows(Row{Side: SideRelay, Relay: "unl1-2", Path: "direct", Subject: "a"}, period)
	if err != nil || len(rows) != 8 || rows[0].Metric != "dup.only_stream" || rows[0].Relay != "unl1-2" {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if p, k := SplitGroup("entry/abc"); p != "entry" || k != "abc" {
		t.Fatal("split")
	}
}

func TestParseClientLine(t *testing.T) {
	line := `2026/11/10 07:10:00.000000 [hikkari] +0300 2026-11-10 07:10:00 WARN outbound/hikkari[p-002-3]: hikkari period s=600 {"dup":{"fresh":3}}`
	c, ok := ParseClientLine(line)
	if !ok || c.Profile != "p-002-3" || c.Seconds != 600 || string(c.Period) != `{"dup":{"fresh":3}}` {
		t.Fatalf("%+v %v", c, ok)
	}
	for _, bad := range []string{"no period here", "hikkari period s=x {}", "hikkari period s=5 {", "hikkari period s=5 [1]"} {
		if _, ok := ParseClientLine(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}
