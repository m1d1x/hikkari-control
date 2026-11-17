package cascadewatch

import (
	"testing"
	"time"
)

// A minute of the receiver's watch is a row per egress, at the minute's
// start, green only when every second answered; the internet counts only for
// the egress that carries.
func TestAMinuteOfTheWatchIsARowPerEgress(t *testing.T) {
	l := journalLine{TS: time.Unix(1794922260, 0).UTC(),
		Msg: "egress: minute from=1794922200 aeznl1 active tun=55/60 rtt_ms=12.4 net=54/60; u1b standby tun=60/60 rtt_ms=13.0"}
	runs, ok := classifyEgress(l, "unl1")
	if !ok || len(runs) != 2 {
		t.Fatalf("%v %+v", ok, runs)
	}
	a, s := runs[0], runs[1]
	if a.Layer != LayerEgress || a.Entry != "unl1" || a.Exit != "aeznl1" || a.Target != "aeznl1" || a.Vantage != "active" || *a.OK {
		t.Fatalf("active %+v", a)
	}
	if a.TS.Unix() != 1794922200 || a.Data["net_ok"] != 54 || a.Data["rtt_ms"] != 12.4 {
		t.Fatalf("active data %+v", a.Data)
	}
	if s.Vantage != "standby" || !*s.OK || s.Data["net_n"] != nil {
		t.Fatalf("standby %+v", s)
	}
	// Silent a whole minute: no round trip to say.
	runs, _ = classifyEgress(journalLine{Msg: "egress: minute from=1 aeznl1 active tun=0/60 rtt_ms=-1 net=0/60"}, "unl1")
	if _, has := runs[0].Data["rtt_ms"]; has {
		t.Fatal("a silent minute has a round trip")
	}
}

func TestTheWatchsMovesAreEvents(t *testing.T) {
	cases := map[string]string{
		"egress: moved from aeznl1 to u1b: tunnel silent 5 s, the box silent past it too": "egress_moved",
		"egress: aeznl1 red, not moved: internet silent 5 s through a live tunnel; no spare": "egress_stuck",
		"egress: u1b carries now: by the hub":                                               "egress_by_hub",
	}
	for msg, kind := range cases {
		runs, ok := classifyEgress(journalLine{Msg: msg}, "unl1")
		if !ok || len(runs) != 1 || runs[0].Layer != LayerEvent || runs[0].Data["kind"] != kind {
			t.Fatalf("%q: %+v", msg, runs)
		}
	}
	runs, _ := classifyEgress(journalLine{Msg: "egress: moved from aeznl1 to u1b: tunnel silent 5 s"}, "unl1")
	if d := runs[0].Data; d["from"] != "aeznl1" || d["to"] != "u1b" || runs[0].Exit != "u1b" {
		t.Fatalf("%+v", d)
	}
	if _, ok := classifyEgress(journalLine{Msg: "Started hikkari-egress-watch.service"}, "unl1"); ok {
		t.Fatal("a line of systemd is a row")
	}
}
