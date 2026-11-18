package cascadewatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

type toldOwner struct{ texts []string }

func (o *toldOwner) Notify(_ context.Context, text string) error {
	o.texts = append(o.texts, text)
	return nil
}

// ownerWatch is a watch up for an hour at now, telling o.
func ownerWatch(now time.Time, o *toldOwner) *Watch {
	w := &Watch{now: func() time.Time { return now }, started: now.Add(-time.Hour),
		alerted: map[string]time.Time{}, calls: map[string]*callStreak{}}
	w.SetOwner(o)
	return w
}

func callMinute(at time.Time, target string, got int) Run {
	gaps150 := 0
	if got < 3000 {
		gaps150 = 1
	}
	data := map[string]any{"sent": float64(3000), "got": float64(got), "gaps150": float64(gaps150), "gap_max_ms": float64(60000)}
	return Run{TS: at, Layer: LayerCall, Target: target, OK: boolp(callOK(data)), Data: data}
}

// aezamsk1 18.11: the people's call red from 16:27 UTC, the owner heard of
// it from a tester at 17:15.
func TestThreeRedMinutesOfThePeoplesCallTellTheOwnerOnceAndTheCleanOneAfter(t *testing.T) {
	ctx := context.Background()
	p := entry.TrunkPair{Entry: "aezamsk1", Exit: "unl1"}
	at := time.Date(2026, 11, 18, 16, 27, 0, 0, time.UTC)
	o := &toldOwner{}
	w := ownerWatch(at.Add(5*time.Minute), o)
	for i := 0; i < 2; i++ {
		w.hearPeoplesCall(ctx, p, callMinute(at.Add(time.Duration(i)*time.Minute), "copies1", 0))
	}
	if len(o.texts) != 0 {
		t.Fatalf("two red minutes told the owner: %q", o.texts)
	}
	for i := 2; i < 6; i++ {
		w.hearPeoplesCall(ctx, p, callMinute(at.Add(time.Duration(i)*time.Minute), "copies1", 0))
	}
	if len(o.texts) != 1 || !strings.Contains(o.texts[0], "красный 3 мин подряд, с 16:27 UTC") || !strings.Contains(o.texts[0], "дошло 0 из 3000") {
		t.Fatalf("told %q", o.texts)
	}
	w.hearPeoplesCall(ctx, p, callMinute(at.Add(5*time.Minute), "copies1", 0))
	w.hearPeoplesCall(ctx, p, callMinute(at.Add(6*time.Minute), "pipe-copies1", 3000))
	w.hearPeoplesCall(ctx, p, callMinute(at.Add(6*time.Minute), "copies2", 3000))
	if len(o.texts) != 1 {
		t.Fatalf("a minute read again or another call told the owner: %q", o.texts)
	}
	w.hearPeoplesCall(ctx, p, callMinute(at.Add(6*time.Minute), "copies1", 3000))
	if len(o.texts) != 2 || !strings.Contains(o.texts[1], "снова чистый с 16:33 UTC, был красным 6 мин (с 16:27 UTC)") {
		t.Fatalf("told %q", o.texts)
	}
	w.hearPeoplesCall(ctx, p, callMinute(at.Add(7*time.Minute), "copies1", 3000))
	w.hearPeoplesCall(ctx, p, callMinute(at.Add(8*time.Minute), "copies1", 0))
	if len(o.texts) != 2 {
		t.Fatalf("a clean minute or one red after it told the owner: %q", o.texts)
	}
}

func TestOldMinutesReadBackTellTheOwnerNothing(t *testing.T) {
	ctx := context.Background()
	p := entry.TrunkPair{Entry: "aezamsk1", Exit: "unl1"}
	at := time.Date(2026, 11, 18, 16, 27, 0, 0, time.UTC)
	o := &toldOwner{}
	w := ownerWatch(at.Add(time.Hour), o)
	for i := 0; i < 5; i++ {
		w.hearPeoplesCall(ctx, p, callMinute(at.Add(time.Duration(i)*time.Minute), "copies1", 0))
	}
	w.hearPeoplesCall(ctx, p, callMinute(at.Add(5*time.Minute), "copies1", 3000))
	if len(o.texts) != 0 {
		t.Fatalf("an hour-old streak told the owner: %q", o.texts)
	}
	up := &toldOwner{}
	w = ownerWatch(at.Add(4*time.Minute), up)
	w.started = at.Add(3 * time.Minute)
	for i := 0; i < 4; i++ {
		w.hearPeoplesCall(ctx, p, callMinute(at.Add(time.Duration(i)*time.Minute), "copies1", 0))
	}
	if len(up.texts) != 0 {
		t.Fatalf("a hub just up told the owner of the journal it reads back: %q", up.texts)
	}
}

func TestARedPathTellsTheOwnerOncePerAlertEvery(t *testing.T) {
	ctx := context.Background()
	p := entry.TrunkPair{Entry: "aezamsk1", Exit: "unl1"}
	round := time.Date(2026, 11, 18, 16, 21, 0, 0, time.UTC)
	o := &toldOwner{}
	w := ownerWatch(round.Add(4*time.Minute), o)
	v := Verdict{FirstRed: LayerPath, Red: map[string][]string{LayerPath: {"вход → выход: ICMP теряет 3 из 20"}}}
	w.hearPath(ctx, p, round, v)
	w.hearPath(ctx, p, round.Add(Every), v)
	w.hearPath(ctx, p, round, Verdict{FirstRed: LayerProtocol})
	if len(o.texts) != 1 || !strings.Contains(o.texts[0], "раунд 16:21 UTC: вход → выход: ICMP теряет 3 из 20") {
		t.Fatalf("told %q", o.texts)
	}
}

func TestNoOwnerTellsNobody(t *testing.T) {
	at := time.Date(2026, 11, 18, 16, 27, 0, 0, time.UTC)
	w := ownerWatch(at.Add(3*time.Minute), &toldOwner{})
	w.SetOwner(nil)
	for i := 0; i < 3; i++ {
		w.hearPeoplesCall(context.Background(), entry.TrunkPair{Entry: "aezamsk1"}, callMinute(at.Add(time.Duration(i)*time.Minute), "copies1", 0))
	}
}
