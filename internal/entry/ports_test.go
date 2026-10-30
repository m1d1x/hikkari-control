package entry

import (
	"context"
	"strings"
	"testing"
)

// A Reality box that answers on every relay it has but has fewer than it
// keeps is short of a port; one at its target is healthy; and nothing else
// about the reading changes.
func TestABoxWithoutItsSparePortIsShortOfOne(t *testing.T) {
	healthy := Diagnosis{Cause: CauseHealthy}
	reality := func(statuses ...string) []PoolRelay {
		out := make([]PoolRelay, len(statuses))
		for i, s := range statuses {
			out[i] = PoolRelay{Name: "r" + string(rune('1'+i)), Status: s, Proto: ProtoVlessRealityVision}
		}
		return out
	}
	cases := []struct {
		name   string
		d      Diagnosis
		relays []PoolRelay
		want   string
	}{
		{"one relay", healthy, reality(StatusAlive), CauseShortOfPorts},
		{"one relay and a dead one", healthy, reality(StatusDead, StatusAlive), CauseShortOfPorts},
		{"at target", healthy, reality(StatusAlive, StatusAlive), CauseHealthy},
		// A spare waiting for admission is already the spare: minting another
		// would put the box over its target once both are in.
		{"spare pending", healthy, reality(StatusAlive, "pending"), CauseHealthy},
		{"xhttp keeps one", healthy, []PoolRelay{{Name: "x1", Status: StatusAlive, Proto: ProtoVlessXhttp}}, CauseHealthy},
		{"voice keeps one", healthy, []PoolRelay{{Name: "v1", Status: StatusAlive, Proto: ProtoHysteria2}}, CauseHealthy},
		// A real failure is that failure first.
		{"banned and short", Diagnosis{Cause: CauseIPBanned}, reality(StatusAlive), CauseIPBanned},
	}
	for _, c := range cases {
		if got := shortOfPorts(c.d, c.relays).Cause; got != c.want {
			t.Errorf("%s: cause %q, want %q", c.name, got, c.want)
		}
	}
	if !readsHealthy(CauseShortOfPorts) || NeedsAttention(CauseShortOfPorts) == false {
		t.Error("short of a port must read recovered and still be work to do")
	}
	if p := PlaybookForCause(CauseShortOfPorts); p.Name != PlaybookAddPorts || !p.MayRunUnattended() || p.Spends {
		t.Errorf("short_of_ports proposes %+v, want add_ports, auto and free", p)
	}
}

// A port that died banned is cut on this address: the next relay on the box
// takes another one. A port freed any other way is free again.
func TestABannedPortIsNotMintedAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	first := h.relay(box, KindPersonal)
	second := h.relay(box, KindPersonal)
	h.admit(first)
	h.admit(second)
	port := func(name string) int {
		t.Helper()
		id, err := h.svc.RelayIdentity(ctx, name)
		if err != nil {
			t.Fatalf("identity %s: %v", name, err)
		}
		return id.Port
	}
	if _, err := h.svc.MarkDead(ctx, first, DeathIPBanned); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	third := h.relay(box, KindPersonal)
	if got, banned := port(third), port(first); got == banned {
		t.Fatalf("%s minted on %d, the port %s died banned on", third, got, first)
	}

	if _, err := h.svc.MarkDead(ctx, second, DeathRotated); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	fourth := h.relay(box, KindPersonal)
	if got, freed := port(fourth), port(second); got != freed {
		t.Fatalf("%s minted on %d, want %d: a port freed by rotation is free", fourth, got, freed)
	}
}

// A new box is born with its spare.
func TestANewRealityBoxIsBornWithASparePort(t *testing.T) {
	ph := newProvHarness(t)
	ph.vantage("clean")
	box, id := ph.newBox(ph.box.password)
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, ph.logText(id))
	}
	alive := 0
	relays, _ := ph.svc.List(context.Background())
	for _, r := range relays {
		if r.Box == box && r.Status == StatusAlive {
			alive++
		}
	}
	if want := relayTarget(ProtoVlessRealityVision); alive != want {
		t.Fatalf("%s has %d alive relays, want %d", box, alive, want)
	}
}

// After a port is retired the box is brought back on a fresh port, and the
// relay still carrying people is not touched: no deploy, no restart.
func TestTopUpRaisesAFreshPortAndLeavesTheWorkingOneAlone(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	ph.vantage("clean")
	box, id := ph.newBox(ph.box.password)
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, ph.logText(id))
	}
	banned, working := box+"-1", box+"-2"
	if _, err := ph.svc.MarkDead(ctx, banned, DeathIPBanned); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	before := len(ph.box.cmds)

	ph.vantage("clean")
	added, err := ph.prov.TopUp(ctx, box)
	if err != nil {
		t.Fatalf("top up: %v", err)
	}
	if len(added) != 1 || added[0] != box+"-3" {
		t.Fatalf("added %v, want [%s-3]", added, box)
	}
	swept := false
	for i, c := range ph.box.cmds[before:] {
		if strings.HasPrefix(c, "bash -s -- ") && strings.Contains(c, " "+working+" ") {
			t.Fatalf("the working relay was redeployed: %q", c)
		}
		if strings.Contains(ph.box.stdins[before+i], "ufw delete allow") && strings.Contains(c, " "+banned+" 443") {
			swept = true
		}
	}
	// The retired relay's unit, keys and port are taken off the box.
	if !swept {
		t.Fatalf("the banned relay %s was not swept off the box: %q", banned, ph.box.cmds[before:])
	}
	relays, _ := ph.svc.List(ctx)
	for _, r := range relays {
		if r.Name == box+"-3" && r.Status != StatusAlive {
			t.Fatalf("%s is %q after the top-up", r.Name, r.Status)
		}
	}

	// At target: nothing to do, and saying so is not a failure.
	again, err := ph.prov.TopUp(ctx, box)
	if err != nil || len(again) != 0 {
		t.Fatalf("second top-up: %v, %v", again, err)
	}
}

// A fresh port the vantage reads as banned is burnt on the spot, so the next
// top-up takes another port instead of being born dead on the same one.
func TestAPortBornBannedIsBurnt(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	ph.vantage("clean")
	box, id := ph.newBox(ph.box.password)
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, ph.logText(id))
	}
	if _, err := ph.svc.MarkDead(ctx, box+"-1", DeathIPBanned); err != nil {
		t.Fatalf("mark dead: %v", err)
	}

	ph.vantage("ip_banned")
	if _, err := ph.prov.TopUp(ctx, box); err == nil {
		t.Fatal("a port born banned was admitted")
	}
	relays, _ := ph.svc.List(ctx)
	for _, r := range relays {
		if r.Name == box+"-3" && (r.Status != StatusDead || r.DiedReason != DeathIPBanned) {
			t.Fatalf("%s is %q (%q), want dead ip_banned", r.Name, r.Status, r.DiedReason)
		}
	}
}
