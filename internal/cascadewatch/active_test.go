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
