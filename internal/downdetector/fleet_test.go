package downdetector

import (
	"testing"
	"time"
)

func TestFleetVerdictsRequireTunnelQuorumAndForgetOldBindings(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	start := alignedStart(now) - WindowSec
	s := NewStore()
	report := func(install, country string, asn uint32, ok, fail int) {
		t.Helper()
		if err := s.Add(Report{Install: install, Country: country, ASN: asn, Window: [2]int64{start, start + WindowSec}, Servers: []ServerStat{{ID: "n:slot", Proto: "vless", Src: SrcTunnel, OK: ok, Fail: fail}}}); err != nil {
			t.Fatal(err)
		}
	}
	report("a", "RU", 1, 0, 10)
	if got := s.FleetVerdict("n:slot", ExternalUp, now, time.Time{}, 3); got != "unknown" {
		t.Fatalf("one install triggered %s", got)
	}
	report("b", "RU", 1, 0, 10)
	report("c", "RU", 1, 0, 10)
	for _, id := range []string{"d", "e", "f"} {
		report(id, "DE", 2, 10, 0)
	}
	if got := s.FleetVerdict("n:slot", ExternalUp, now, time.Time{}, 3); got != "country_block" {
		t.Fatalf("country block: %s", got)
	}
	if got := s.FleetVerdict("n:slot", ExternalUp, now, now.Add(-time.Second), 3); got != "unknown" {
		t.Fatalf("old reports judged replacement: %s", got)
	}
	if got := s.FleetVerdict("n:slot", ExternalUp, now.Add(time.Hour), time.Time{}, 3); got != "unknown" {
		t.Fatalf("stale evidence acted: %s", got)
	}
	if got := s.FleetVerdict("n:slot", ExternalDown, now, time.Time{}, 3); got != "server_down" {
		t.Fatalf("external outage: %s", got)
	}
}
func TestMovingANodeResetsProbesAndRejectsInflightOldResults(t *testing.T) {
	old := Target{ID: "n:slot", Host: "old.example", Port: 443}
	fresh := Target{ID: old.ID, Host: "fresh.example", Port: 443}
	p := NewProber([]Target{old})
	p.record(old.ID, false)
	p.record(old.ID, false)
	p.record(old.ID, false)
	if p.External(old.ID) != ExternalDown {
		t.Fatal("test setup did not fail")
	}
	p.ReplaceTargets([]Target{fresh})
	p.recordTarget(old, false)
	if p.External(old.ID) != ExternalUnknown {
		t.Fatal("old endpoint poisoned new binding")
	}
	p.recordTarget(fresh, true)
	if p.External(old.ID) != ExternalUp {
		t.Fatal("new endpoint not probed")
	}
	p.mu.Lock()
	p.state[old.ID].at = time.Now().Add(-10 * time.Minute)
	p.mu.Unlock()
	if p.External(old.ID) != ExternalUnknown {
		t.Fatal("stale probe looks live")
	}
}
