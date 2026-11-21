package cascadewatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// TestTheBrainsPointLinesAreRowsByBoxName: a minute of the points is a row per
// point by its box, ok when it was silent no second; a change is an event; an
// address the hub does not know stays "?" (G5).
func TestTheBrainsPointLinesAreRowsByBoxName(t *testing.T) {
	names := map[string]string{"10.0.0.1": "unl1", "10.0.0.2": "ams3"}
	at := time.Unix(1_800_000_000, 0).UTC()
	l := journalLine{TS: at, Msg: "nekko-trunk-entry: trunk: points from=1800000000 10.0.0.1 up=7/7 proof_ms=40 silent_s=0; 10.0.0.2 up=0/7 proof_ms=-1 silent_s=60; 10.0.0.9 up=1/1 proof_ms=3 silent_s=2"}
	rows, ok := classifyPoint(l, names)
	if !ok || len(rows) != 3 {
		t.Fatalf("%v %+v", ok, rows)
	}
	if rows[0].Layer != LayerPoint || rows[0].Target != "unl1" || !*rows[0].OK || rows[0].Data["proof_ms"] != 40 {
		t.Fatalf("unl1 %+v", rows[0])
	}
	if rows[1].Target != "ams3" || *rows[1].OK || rows[1].Data["silent_s"] != 60 || rows[1].Data["proof_ms"] != nil {
		t.Fatalf("ams3 %+v", rows[1])
	}
	if rows[2].Target != "?" {
		t.Fatalf("an unknown point %+v", rows[2])
	}
	for msg, kind := range map[string]string{
		"trunk: point 10.0.0.2 silent: 0 of 7 members up":          "point_silent",
		"trunk: point 10.0.0.2 back: 3 of 7 members up after 12 s": "point_back",
		"trunk: every point silent":                                "points_silent",
		"trunk: a point back after 4 s of every point silent":      "points_back",
	} {
		rows, ok := classifyPoint(journalLine{TS: at, Msg: "nekko-trunk-entry: " + msg}, names)
		if !ok || len(rows) != 1 || rows[0].Layer != LayerEvent || rows[0].Data["kind"] != kind {
			t.Fatalf("%q: %v %+v", msg, ok, rows)
		}
	}
	if _, ok := classifyPoint(journalLine{TS: at, Msg: "trunk: receivers 10.0.0.1 active up=3 proof_ms=1"}, names); ok {
		t.Fatal("a receivers line read as points")
	}
}

func pointMinutes(at time.Time, silent map[string]int) []Run {
	var rows []Run
	for _, box := range []string{"unl1", "ams3"} {
		s, ok := silent[box]
		if !ok {
			continue
		}
		rows = append(rows, Run{TS: at, Layer: LayerPoint, Target: box, OK: boolp(s == 0),
			Data: map[string]any{"silent_s": s, "members": 7, "up": 0}})
	}
	return rows
}

// TestAPointSilentAWholeMinuteTellsTheOwnerAndAllPointsOnceMore: a point
// silent a whole minute tells the owner once, a part of a minute nothing;
// every point silent a whole minute once more; the first minute a point
// carries again tells it is over.
func TestAPointSilentAWholeMinuteTellsTheOwnerAndAllPointsOnceMore(t *testing.T) {
	ctx := context.Background()
	p := entry.TrunkPair{Entry: "aezamsk1", Exit: "aeznl1", Points: []string{"unl1", "ams3"}}
	at := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	o := &toldOwner{}
	w := ownerWatch(at.Add(5*time.Minute), o)
	minute := func(i int, silent map[string]int) {
		w.hearPoints(ctx, p, pointMinutes(at.Add(time.Duration(i)*time.Minute), silent))
	}
	minute(0, map[string]int{"unl1": 0, "ams3": 30})
	if len(o.texts) != 0 {
		t.Fatalf("half a minute told: %q", o.texts)
	}
	minute(1, map[string]int{"unl1": 0, "ams3": 60})
	minute(2, map[string]int{"unl1": 0, "ams3": 60})
	if len(o.texts) != 1 || !strings.Contains(o.texts[0], "Точка перехода ams3 входа людей aezamsk1 → aeznl1 молчит с 14:01 UTC") {
		t.Fatalf("told %q", o.texts)
	}
	minute(3, map[string]int{"unl1": 60, "ams3": 60})
	if len(o.texts) != 3 || !strings.Contains(o.texts[1], "Точка перехода unl1") || !strings.Contains(o.texts[2], "Молчат все точки перехода входа людей aezamsk1 → aeznl1 (unl1, ams3) с 14:03 UTC") {
		t.Fatalf("told %q", o.texts)
	}
	minute(3, map[string]int{"unl1": 60, "ams3": 60})
	minute(4, map[string]int{"unl1": 10, "ams3": 60})
	want := []string{"Точка перехода unl1 входа людей aezamsk1 → aeznl1 снова везёт с 14:04 UTC, молчала 1 мин", "снова идёт через точки с 14:04 UTC, все молчали 1 мин"}
	if len(o.texts) != 5 {
		t.Fatalf("told %q", o.texts)
	}
	for i, s := range want {
		if !strings.Contains(o.texts[3+i], s) {
			t.Fatalf("told %q, want %q", o.texts[3+i], s)
		}
	}
	minute(5, map[string]int{"unl1": 0, "ams3": 0})
	if len(o.texts) != 6 || !strings.Contains(o.texts[5], "ams3 входа людей aezamsk1 → aeznl1 снова везёт с 14:05 UTC, молчала 4 мин (с 14:01 UTC)") {
		t.Fatalf("told %q", o.texts)
	}
}

// TestTheOwnersCallTextNamesThePoints: the people's trunk reads its core and
// the points it goes through.
func TestTheOwnersCallTextNamesThePoints(t *testing.T) {
	if got := pathName(entry.TrunkPair{Entry: "aezamsk1", Exit: "aeznl1", Points: []string{"unl1"}}); got != "aezamsk1 → aeznl1 через unl1" {
		t.Fatal(got)
	}
	if got := pathName(entry.TrunkPair{Entry: "aezamsk1", Exit: "unl1"}); got != "aezamsk1 → unl1" {
		t.Fatal(got)
	}
}
