package cascadewatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

func receiversAt(at time.Time, active, standby string) Run {
	return Run{TS: at, Layer: LayerEvent, Data: map[string]any{"kind": "receivers", "receivers": []map[string]any{
		{"box": active, "role": "active", "up": 38, "proof_ms": 190},
		{"box": standby, "role": "standby", "up": 14, "proof_ms": 220},
	}}}
}

func movedAt(at time.Time, from, to, why string) Run {
	return Run{TS: at, Layer: LayerEvent, Data: map[string]any{"kind": "receiver_moved", "from": from, "to": to, "why": why}}
}

// aezamsk1 19.11: 12:10:01 a silence of 5.4 s on every call of the entry,
// people's and stand's, 12:10:06 the entry moved to ams3, the active one
// unheard for 5.3 s; the call's red minutes after said «aezamsk1 → unl1».
func TestTheEntryOnASpareNamesItInTheCallAndTellsTheOwnerOfTheMoveAndTheStay(t *testing.T) {
	ctx := context.Background()
	p := entry.TrunkPair{Entry: "aezamsk1", Exit: "unl1"}
	at := time.Date(2026, 11, 19, 12, 10, 6, 743_000_000, time.UTC)
	o := &toldOwner{}
	w := ownerWatch(at, o)
	clock := at
	w.now = func() time.Time { return clock }
	hear := func(run Run) { clock = run.TS; w.hearReceiver(ctx, p, run) }
	if got := w.activeOf(p); got != "unl1" {
		t.Fatalf("before any word of the brain the active one is %q", got)
	}
	hear(movedAt(at, "unl1", "ams3", "dead: the active receiver unheard for 5.3s"))
	if got := w.activeOf(p); got != "ams3" {
		t.Fatalf("after the move the active one is %q", got)
	}
	if len(o.texts) != 1 || !strings.Contains(o.texts[0], "с приёмника unl1 на ams3 в 12:10:06 UTC") ||
		!strings.Contains(o.texts[0], "не слышен 5,3 с") || !strings.Contains(o.texts[0], "запасной тоже молчал около 3,8 с") {
		t.Fatalf("told %q", o.texts)
	}
	for i := 0; i < ownerSpareMinutes; i++ {
		hear(receiversAt(at.Add(time.Duration(i+1)*time.Minute-time.Second), "ams3", "unl1"))
	}
	if len(o.texts) != 1 {
		t.Fatalf("under %d minutes on the spare told %q", ownerSpareMinutes, o.texts)
	}
	hear(receiversAt(at.Add(ownerSpareMinutes*time.Minute+time.Second), "ams3", "unl1"))
	hear(receiversAt(at.Add(ownerSpareMinutes*time.Minute+time.Minute), "ams3", "unl1"))
	if len(o.texts) != 2 || !strings.Contains(o.texts[1], "на запасном приёмнике ams3 10 мин, с 12:10 UTC") {
		t.Fatalf("told %q", o.texts)
	}
	// The call's minutes name the receiver the people are on.
	for i := 0; i < ownerRedMinutes; i++ {
		run := callMinute(at.Add(time.Duration(12+i)*time.Minute), "copies1", 0)
		run.Data["receiver"] = w.activeOf(p)
		w.hearPeoplesCall(ctx, p, run)
	}
	if len(o.texts) != 3 || !strings.Contains(o.texts[2], "aezamsk1 → ams3 красный") {
		t.Fatalf("told %q", o.texts)
	}
	back := at.Add(43 * time.Minute)
	hear(movedAt(back, "ams3", "unl1", "by hand"))
	if len(o.texts) != 4 || !strings.Contains(o.texts[3], "с приёмника ams3 на unl1") || !strings.Contains(o.texts[3], "руками") ||
		!strings.Contains(o.texts[3], "На ams3 люди были 43 мин") {
		t.Fatalf("told %q", o.texts)
	}
	if got := w.activeOf(p); got != "unl1" {
		t.Fatalf("back by hand the active one is %q", got)
	}
}

func TestAMoveRightAfterDeadAfterSaysTheSpareWasHeard(t *testing.T) {
	if got := moveWhy("dead: the active receiver unheard for 1.6s"); strings.Contains(got, "запасной") || !strings.Contains(got, "1,6 с") {
		t.Fatalf("got %q", got)
	}
	if got := moveWhy("dead: the active receiver's pool unheard for 2.1s"); !strings.Contains(got, "пул активного") {
		t.Fatalf("got %q", got)
	}
}

func TestAReceiversLineReadBackTellsNothing(t *testing.T) {
	ctx := context.Background()
	p := entry.TrunkPair{Entry: "aezamsk1", Exit: "unl1"}
	at := time.Date(2026, 11, 19, 12, 10, 6, 0, time.UTC)
	o := &toldOwner{}
	w := ownerWatch(at.Add(2*time.Hour), o)
	w.hearReceiver(ctx, p, movedAt(at, "unl1", "ams3", "dead: the active receiver unheard for 5.3s"))
	for i := 0; i < 30; i++ {
		w.hearReceiver(ctx, p, receiversAt(at.Add(time.Duration(i)*time.Minute), "ams3", "unl1"))
	}
	if len(o.texts) != 0 {
		t.Fatalf("old lines told %q", o.texts)
	}
	if got := w.activeOf(p); got != "ams3" {
		t.Fatalf("old lines still say the active one: %q", got)
	}
}

// The judge's move to a better receiver (hikkari-protocol pipectl
// better.go) and the call held to each receiver, as the hub reads them.
func TestAMoveToABetterReceiverAndTheCallToEachReceiverReadAsSuch(t *testing.T) {
	names := map[string]string{"10.0.0.1": "unl1", "10.0.0.2": "ams3"}
	at := time.Date(2026, 11, 19, 12, 14, 2, 0, time.UTC)
	moved, ok := classifyReceiver(journalLine{TS: at, Msg: "nekko-trunk-entry: trunk: receiver moved from 10.0.0.2 to 10.0.0.1: the active receiver worse for 3 min (12 pauses over 150 ms, 0.0% lost), receiver 0 clean the same minutes"}, names)
	if !ok || moved.Data["why"] != "better: the active receiver worse for 3 min (12 pauses over 150 ms, 0.0% lost), receiver 0 clean the same minutes" {
		t.Fatalf("moved %+v", moved.Data)
	}
	if got := moveWhy(moved.Data["why"].(string)); !strings.Contains(got, "хуже 3 мин подряд (пауз больше 150 мс 12, потеряно 0.0%)") {
		t.Fatalf("why %q", got)
	}
	call, ok := classifyReceiverCall(journalLine{TS: at, Msg: "nekko-trunk-entry: call receiver=10.0.0.2 copies=1 from=1795133340 sent=3000 got=3000 rtt_p50=42.6 rtt_p99=170.1 rtt_max=312.0 gaps60=9 gaps150=5 gap_max=312 silent_ms=1500 sessions=1 worst=-"}, names)
	if !ok || call.Target != "rx-ams3" || call.Layer != LayerCall || call.Data["receiver"] != "ams3" || num(call.Data, "gaps150") != 5 || *call.OK {
		t.Fatalf("call %+v ok=%v", call, ok)
	}
	if _, ok := classifyReceiverCall(journalLine{TS: at, Msg: "nekko-trunk-entry: call copies=1 from=1795133340 sent=3000 got=3000"}, names); ok {
		t.Fatal("the people's call read as a receiver's")
	}
}

func TestTheMatrixLinesReadAsCallsAndSilences(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 11, 2, 0, time.UTC)
	run, ok := classifyMatrix(journalLine{TS: at, Msg: "nekko-trunk-entry: matrix target=ru-eurodir kind=ru every_ms=20 from=1795133400 sent=3000 got=2730 rtt_p50=8.1 rtt_p99=9.0 rtt_max=12.0 gaps60=1 gaps150=1 gap_max=5400 silent_ms=5380 worst=1795133461421:5400"})
	if !ok || run.Target != "mx-ru-eurodir" || run.Layer != LayerCall || run.Data["kind"] != "ru" || num(run.Data, "gap_max_ms") != 5400 || *run.OK {
		t.Fatalf("target %+v", run)
	}
	box, ok := classifyMatrix(journalLine{TS: at, Msg: "nekko-trunk-entry: matrix box from=1795133400 late_max_ms=40 late_over100=0 late_over500=0 steal_max_pct=0.0 steal_avg_pct=0.00 rx_drop=0 tx_drop=0"})
	if !ok || box.Target != "mx-box" || !*box.OK || num(box.Data, "gap_max_ms") != 40 {
		t.Fatalf("box %+v", box)
	}
	sil, ok := classifyMatrix(journalLine{TS: at, Msg: "nekko-trunk-entry: matrix silence at=1795133401421 len_ms=5322 silent=unl1,ams3,eu-fra-ripe heard=ru-eurodir unknown=- box_late_ms=0 verdict=border"})
	if !ok || sil.Layer != LayerEvent || sil.Data["verdict"] != "border" || sil.Data["kind"] != "silence" || len(sil.Data["silent"].([]string)) != 3 || sil.Data["unknown"] != nil ||
		!sil.TS.Equal(time.UnixMilli(1795133401421).UTC()) {
		t.Fatalf("silence %+v", sil)
	}
}
