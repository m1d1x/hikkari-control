package cascadewatch

import "testing"

// The stand's lines are the entry's words in another unit: its calls go
// under pipe-*, its selector's moves are marked path pipe, its minute line
// and its pipe's ticks are not kept.
func TestTheStandsLinesGoUnderTargetsOfItsOwn(t *testing.T) {
	journal := `1794651602.017453 lim nekko-trunk-entry[350724]: 2026/11/14 10:20:02 nekko-trunk-entry: call wire=h3dgram copies=1 from=1794651540 sent=3000 got=3000 rtt_p50=43.1 rtt_p99=61.0 rtt_max=91.2 gaps60=1 gaps150=0 gap_max=68 silent_ms=48 sessions=1 worst=1794651554558:68
1794651602.017453 lim nekko-trunk-entry[350724]: 2026/11/14 10:20:02 nekko-trunk-entry: call copies=1 from=1794651540 sent=3000 got=0 rtt_p50=- rtt_p99=- rtt_max=- gaps60=1 gaps150=1 gap_max=60000 silent_ms=59980 sessions=1 worst=1794651540000:60000
1794651958.117375 lim nekko-trunk-entry[350724]: 2026/11/14 10:25:58 nekko-trunk-entry: trunk: route: voice: h3dgram is red, to udp
1794651617.117385 lim nekko-trunk-entry[350724]: 2026/11/14 10:20:17 nekko-trunk-entry: pipe members=32 up=32 joined=39 stalled=1 quiet=0 broken=0 streams=0 flows=0
1794651607.620609 lim nekko-trunk-entry[350731]: hk-pipe: flows=0 idle=62 dgrams=1025028/1019811 copies=0 route=bulk:0,voice:6 pauses=0 bad=0 | wire 3 up=2/2
`
	var got []Run
	for _, l := range parseJournal(journal) {
		if runs, ok := classifyPipe(l); ok {
			got = append(got, runs...)
		}
	}
	if len(got) != 3 {
		t.Fatalf("%d rows, want 3: %+v", len(got), got)
	}
	if got[0].Layer != LayerCall || got[0].Target != "pipe-h3dgram" || !*got[0].OK || num(got[0].Data, "gap_max_ms") != 68 {
		t.Fatalf("the wire's call: %+v", got[0])
	}
	if got[1].Target != "pipe-copies1" || *got[1].OK || got[1].TS.Unix() != 1794651540 {
		t.Fatalf("the route's call: %+v", got[1])
	}
	mv := got[2]
	if mv.Layer != LayerEvent || mv.Data["kind"] != "route" || mv.Data["from"] != "h3dgram" || mv.Data["to"] != "udp" || mv.Data["path"] != PathPipe {
		t.Fatalf("the move: %+v", mv)
	}
	if m := routeMark(mv.TS, mv.Data); m.Label != "труба, голос: h3dgram красный, на udp" {
		t.Fatalf("mark %q", m.Label)
	}
}

// The entry's clients line is a row of layer clients at its minute, with
// the counts as numbers and "-" kept as said.
func TestTheClientsLineIsARowOfItsMinute(t *testing.T) {
	l := parseJournal("1794651602.017453 lim nekko-trunk-entry[1]: 2026/11/14 10:20:02 nekko-trunk-entry: clients tcp from=1794651540 conns=12 closed=3 sending=9 segs=48210 retrans=37 loss_pm=0.77 rtt_p50=18.4 rtt_p90=61.0\n")[0]
	r, ok := classifyClients(l)
	if !ok || r.Layer != LayerClients || r.Target != "tcp" || r.TS.Unix() != 1794651540 {
		t.Fatalf("%+v %v", r, ok)
	}
	if num(r.Data, "segs") != 48210 || num(r.Data, "retrans") != 37 || num(r.Data, "rtt_p50") != 18.4 {
		t.Fatalf("data %+v", r.Data)
	}
	idle := parseJournal("1794651602.0 lim nekko-trunk-entry[1]: nekko-trunk-entry: clients tcp from=1794651540 conns=0 closed=0 sending=0 segs=0 retrans=0 loss_pm=- rtt_p50=- rtt_p90=-\n")[0]
	if r, _ := classifyClients(idle); r.Data["rtt_p50"] != "-" || num(r.Data, "segs") != 0 {
		t.Fatalf("idle %+v", r.Data)
	}
}
