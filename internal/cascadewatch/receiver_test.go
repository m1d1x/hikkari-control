package cascadewatch

import (
	"testing"
	"time"
)

// The brain's lines about receivers become events with box names, never the
// boxes' addresses.
func TestTheReceiversLinesBecomeEventsByBox(t *testing.T) {
	names := map[string]string{"10.0.0.1": "unl1", "10.0.0.2": "ams3"}
	ts := time.Unix(1794840000, 0)
	for msg, want := range map[string]map[string]any{
		"nekko-trunk-entry: trunk: receiver moved from 10.0.0.1 to 10.0.0.2: by hand":                   {"kind": "receiver_moved", "from": "unl1", "to": "ams3", "why": "by hand"},
		"nekko-trunk-entry: trunk: receiver moved from 10.0.0.1 to 10.0.0.2: 10.0.0.1 unheard for 1.5s": {"kind": "receiver_moved", "from": "unl1", "to": "ams3", "why": "dead: 10.0.0.1 unheard for 1.5s"},
		"nekko-trunk-entry: trunk: receiver 10.0.0.9 by hand refused: not one of the entry's receivers": {"kind": "receiver_refused", "to": "?"},
	} {
		run, ok := classifyReceiver(journalLine{TS: ts, Msg: msg}, names)
		if !ok || run.Layer != LayerEvent {
			t.Fatalf("%q: %v %+v", msg, ok, run)
		}
		for k, v := range want {
			if run.Data[k] != v {
				t.Errorf("%q: %s = %v, want %v", msg, k, run.Data[k], v)
			}
		}
	}
	run, ok := classifyReceiver(journalLine{TS: ts, Msg: "nekko-trunk-entry: trunk: receivers 10.0.0.2 active up=38 proof_ms=12; 10.0.0.1 standby up=0 proof_ms=-1"}, names)
	if !ok {
		t.Fatal("the minute line not read")
	}
	rs := run.Data["receivers"].([]map[string]any)
	if len(rs) != 2 || rs[0]["box"] != "ams3" || rs[0]["role"] != "active" || rs[0]["up"] != 38 || rs[1]["box"] != "unl1" || rs[1]["proof_ms"] != -1 {
		t.Fatalf("receivers %v", rs)
	}
	if _, ok := classifyReceiver(journalLine{TS: ts, Msg: "nekko-trunk-entry: trunk: route: voice: on udp"}, names); ok {
		t.Fatal("a route line read as receivers")
	}
}
