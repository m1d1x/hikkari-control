package cascadewatch

import (
	"testing"
	"time"
)

// TestWireStandLines: the stand's call is a minute of the call layer under its
// wire, its bulk and CPU rows of the wire layer; other lines are not its.
func TestWireStandLines(t *testing.T) {
	lines := parseJournal(`1794355262.100000 nekko-spb nekko-edge[812]: hikkari-wire-stand: call wire=awg copies=1 from=1794355200 sent=3000 got=2990 rtt_p50=45.1 rtt_p99=60.2 rtt_max=88.0 gaps60=3 gaps150=1 gap_max=180 silent_ms=240 sessions=1 worst=1794355210000:180,1794355230000:70
1794355291.000000 nekko-spb nekko-edge[812]: hikkari-wire-stand: bulk wire=awg from=1794355290 bytes=1048576 want=1048576 ms=812 end=ok
1794355591.000000 nekko-spb hikkari-wire-stand[812]: {"level":"info","ts":1794355591.4,"msg":"hikkari-wire-stand: bulk wire=udp from=1794355590 bytes=880000 want=1048576 ms=1400 end=closed"}
1794355320.000000 nekko-spb nekko-edge[812]: hikkari-wire-stand: cpu wire=awg from=1794355260 cpu_ms=1500 steal_pct=12.5 busy_pct=20.0
1794355320.000000 nekko-spb nekko-trunk-entry[90]: nekko-trunk-entry: call copies=1 from=1794355200 sent=3000 got=3000 rtt_p50=47 rtt_p99=80 rtt_max=99 gaps60=1 gaps150=0 gap_max=64 silent_ms=44 sessions=1 worst=-`)
	var runs []Run
	for _, l := range lines {
		if r, ok := classifyWire(l); ok {
			runs = append(runs, r)
		}
	}
	if len(runs) != 4 {
		t.Fatalf("%d rows: %+v", len(runs), runs)
	}
	call := runs[0]
	if call.Layer != LayerCall || call.Target != "wire-awg" || *call.OK || !call.TS.Equal(time.Unix(1794355200, 0)) ||
		num(call.Data, "lost") != 10 || num(call.Data, "gap_max_ms") != 180 || len(call.Data["worst"].([]map[string]any)) != 2 {
		t.Fatalf("call %+v", call)
	}
	if b := runs[1]; b.Layer != LayerWire || b.Target != "awg" || !*b.OK || b.Data["mode"] != "bulk" || num(b.Data, "ms") != 812 {
		t.Fatalf("bulk %+v", b)
	}
	if b := runs[2]; *b.OK || b.Data["why"] != "1 МиБ: 880000 из 1048576 байт, closed" {
		t.Fatalf("short bulk %+v", b)
	}
	if c := runs[3]; c.Layer != LayerWire || c.Data["mode"] != "cpu" || num(c.Data, "steal_pct") != 12.5 || !c.TS.Equal(time.Unix(1794355260, 0)) {
		t.Fatalf("cpu %+v", c)
	}
	// A load is whole at 90% of its pace; its slices stay as they came.
	load, ok := classifyWire(journalLine{Msg: "hikkari-wire-stand: bulk wire=quic kind=load from=1794355260 bytes=1080000000 want=1125000000 ms=90000 end=ok mbps=96.0 slices=99.1,98.0,97.2"})
	if !ok || !*load.OK || load.Data["mode"] != "bulk_load" || load.Data["slices"] != "99.1,98.0,97.2" {
		t.Fatalf("load %+v", load)
	}
	short, _ := classifyWire(journalLine{Msg: "hikkari-wire-stand: bulk wire=udp kind=load from=1794355260 bytes=500000000 want=1125000000 ms=90000 end=ok mbps=44.4 slices=99.0,20.0"})
	if *short.OK || short.Data["why"] != "нагрузка 100 Мбит/с: 500000000 из 1125000000 байт, ok" {
		t.Fatalf("short load %+v", short)
	}
	// The summary keeps the wire's bulk and CPU by mode.
	for i := range runs {
		runs[i].Entry, runs[i].Exit, runs[i].Vantage = "spb", "unl1", "entry"
	}
	s := Summarize(runs)
	layers := s["spb → unl1"].(map[string]any)["layers"].(map[string]any)
	if _, ok := layers["wire entry awg"].(map[string]any)["metrics"].(map[string]any)["bulk.ms"]; !ok {
		t.Fatalf("summary %+v", layers)
	}
}
