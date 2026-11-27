package main

import (
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

// TestAPointIsSilentByItsMembersAlone: a point is silent when the pipe says
// none of its members up, back when one is; every point silent is said once,
// and the minute counts each point's silent seconds (G5).
func TestAPointIsSilentByItsMembersAlone(t *testing.T) {
	a, c := netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")
	word := func(upA, upC int) []pipectl.PointTick {
		return []pipectl.PointTick{{IP: a, Members: 7, Up: upA, ProofAgo: 40 * time.Millisecond, RTT: 57 * time.Millisecond}, {IP: c, Members: 7, Up: upC, ProofAgo: -1, RTT: -1}}
	}
	b := newPointBook()
	t0 := time.Unix(1_800_000_000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	steps := []struct {
		ps   []pipectl.PointTick
		want []string
	}{
		{word(7, 7), nil},
		{word(7, 0), []string{"trunk: point 10.0.0.2 silent: 0 of 7 members up"}},
		{word(0, 0), []string{"trunk: point 10.0.0.1 silent: 0 of 7 members up", "trunk: every point silent"}},
		{word(0, 0), nil},
		{word(3, 0), []string{"trunk: point 10.0.0.1 back: 3 of 7 members up after 2 s", "trunk: a point back after 2 s of every point silent"}},
		{word(7, 7), []string{"trunk: point 10.0.0.2 back: 7 of 7 members up after 4 s"}},
	}
	for i, s := range steps {
		if got := b.step(at(i), s.ps); !reflect.DeepEqual(got, s.want) {
			t.Fatalf("step %d: %q, want %q", i, got, s.want)
		}
	}
	want := "trunk: points from=1800000000 10.0.0.1 up=7/7 proof_ms=40 silent_s=2 rtt_ms=57; 10.0.0.2 up=7/7 proof_ms=-1 silent_s=4 rtt_ms=-1"
	if got := b.minute(t0); got != want {
		t.Fatalf("minute %q, want %q", got, want)
	}
	if got := b.minute(t0); got != "trunk: points from=1800000000 10.0.0.1 up=7/7 proof_ms=40 silent_s=0 rtt_ms=57; 10.0.0.2 up=7/7 proof_ms=-1 silent_s=0 rtt_ms=-1" {
		t.Fatalf("the next minute starts its own seconds: %q", got)
	}
	// No word: the book forgets the points without a word, and says no minute.
	if got := b.step(at(9), nil); got != nil {
		t.Fatalf("points taken away said %q", got)
	}
	if got := b.minute(t0); got != "" {
		t.Fatalf("a minute of no points: %q", got)
	}
	// A point with no members (none dialled yet) is not silent.
	if got := b.step(at(10), []pipectl.PointTick{{IP: a}}); got != nil {
		t.Fatalf("an empty point said %q", got)
	}
}

// TestAPointsChangeIsALineForTheHub (runPoints): a point taken off the path
// and one brought back each say why and the path left.
func TestAPointsChangeIsALineForTheHub(t *testing.T) {
	a, b, c := netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.0.0.3")
	got := pointChangeLines(pipectl.PointChange{Path: []netip.Addr{a, b}, Left: []netip.Addr{c}, Why: "10.0.0.3 scores 184ms against 10.0.0.1's 62ms for 30s"})
	want := []string{"trunk: point 10.0.0.3 off the path: 10.0.0.3 scores 184ms against 10.0.0.1's 62ms for 30s; path 10.0.0.1 10.0.0.2"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines %q, want %q", got, want)
	}
	got = pointChangeLines(pipectl.PointChange{Path: []netip.Addr{a, b, c}, Back: []netip.Addr{c}, Why: "its hold is over: judged again"})
	want = []string{"trunk: point 10.0.0.3 back on the path: its hold is over: judged again; path 10.0.0.1 10.0.0.2 10.0.0.3"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines %q, want %q", got, want)
	}
}
