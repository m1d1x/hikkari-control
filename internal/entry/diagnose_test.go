package entry

import "testing"

// relaysOf builds a box's relays, all alive.
func relaysOf(names ...string) []PoolRelay {
	out := make([]PoolRelay, 0, len(names))
	for _, n := range names {
		out = append(out, PoolRelay{Name: n, Status: StatusAlive})
	}
	return out
}

// The vantage's own outage is the one reading that must never be mistaken for
// a failure of the box: treating it as one would retire healthy machines on
// the strength of our own network going down.
func TestDiagnoseBlindVantageIsNotABoxFailure(t *testing.T) {
	d := DiagnoseBox("ams1", relaysOf("ams1-1", "ams1-2"), RelayVerdicts{
		"ams1-1": "network_down",
		"ams1-2": "network_down",
	})
	if d.Cause != CauseBlind {
		t.Fatalf("cause = %q, want %q", d.Cause, CauseBlind)
	}
	if NeedsAttention(d.Cause) {
		t.Error("our own outage was raised as something to act on")
	}
}

// A cheaper remedy always wins. A box with one blocked domain and one spotted
// protocol wants a new domain first — and neither is a judgement on the
// machine, so neither may read as one.
func TestDiagnosePrefersTheRemedyThatDoesNotTouchTheMachine(t *testing.T) {
	cases := []struct {
		name     string
		verdicts RelayVerdicts
		want     string
	}{
		{
			name:     "domain outranks fingerprint",
			verdicts: RelayVerdicts{"b1-1": "domain_blocked", "b1-2": "fingerprint"},
			want:     CauseDomainBlocked,
		},
		{
			name:     "domain outranks a banned port",
			verdicts: RelayVerdicts{"b1-1": "domain_blocked", "b1-2": "ip_banned"},
			want:     CauseDomainBlocked,
		},
		{
			name:     "fingerprint outranks a banned port",
			verdicts: RelayVerdicts{"b1-1": "fingerprint", "b1-2": "ip_banned"},
			want:     CauseFingerprint,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := DiagnoseBox("b1", relaysOf("b1-1", "b1-2"), c.verdicts)
			if d.Cause != c.want {
				t.Fatalf("cause = %q, want %q — advice was %q", d.Cause, c.want, d.Advice)
			}
		})
	}
}

// The unit that fails is the address: every judged relay gone means the
// machine, some of them gone means those ports. Reading the second as the
// first would retire a box that still works.
func TestDiagnoseTellsTheMachineFromItsPorts(t *testing.T) {
	all := DiagnoseBox("b2", relaysOf("b2-1", "b2-2"), RelayVerdicts{
		"b2-1": "ip_banned", "b2-2": "ip_banned",
	})
	if all.Cause != CauseIPBanned {
		t.Fatalf("every relay banned: cause = %q, want %q", all.Cause, CauseIPBanned)
	}

	some := DiagnoseBox("b2", relaysOf("b2-1", "b2-2"), RelayVerdicts{
		"b2-1": "ip_banned", "b2-2": "clean",
	})
	if some.Cause != CausePortsBanned {
		t.Fatalf("one relay banned: cause = %q, want %q", some.Cause, CausePortsBanned)
	}
}

// A relay nobody has probed yet is the absence of evidence, not evidence of
// absence: it must not drag a box towards a verdict.
func TestDiagnoseIgnoresUnprobedRelaysAndSaysTheReadingIsPartial(t *testing.T) {
	d := DiagnoseBox("b3", relaysOf("b3-1", "b3-2", "b3-3"), RelayVerdicts{
		"b3-1": "ip_banned",
		"b3-2": "unknown",
		// b3-3 was never probed at all.
	})
	if d.Cause != CauseIPBanned {
		t.Fatalf("cause = %q, want %q from the one relay that was judged", d.Cause, CauseIPBanned)
	}
	if d.Judged != 1 || d.Alive != 3 {
		t.Fatalf("judged %d of %d alive, want 1 of 3", d.Judged, d.Alive)
	}
	if !d.Partial() {
		t.Error("a box judged on one relay of three does not say its reading is partial")
	}
}

// With nothing judged at all there is no cause to state, and stating one
// anyway would be inventing evidence.
func TestDiagnoseWithNoEvidenceSaysSoAndAsksForNothing(t *testing.T) {
	d := DiagnoseBox("b4", relaysOf("b4-1"), RelayVerdicts{})
	if d.Cause != CauseUnknown {
		t.Fatalf("cause = %q, want %q", d.Cause, CauseUnknown)
	}
	if NeedsAttention(d.Cause) {
		t.Error("a box with no evidence was raised as something to act on")
	}
	if d.Advice == "" {
		t.Error("the console is given no sentence to show")
	}
}

// Dead relays are out of the reading: they have already been decided about,
// and counting them would keep a retired box permanently ill.
func TestDiagnoseReadsOnlyLiveRelays(t *testing.T) {
	relays := []PoolRelay{
		{Name: "b5-1", Status: StatusAlive},
		{Name: "b5-2", Status: StatusDead},
	}
	d := DiagnoseBox("b5", relays, RelayVerdicts{
		"b5-1": "clean",
		"b5-2": "ip_banned",
	})
	if d.Cause != CauseHealthy {
		t.Fatalf("cause = %q, want %q — a dead relay's verdict was counted", d.Cause, CauseHealthy)
	}
	if d.Alive != 1 {
		t.Fatalf("alive = %d, want 1", d.Alive)
	}
}

// reality_wins means the tunnel got through where bare TLS did not — the best
// possible reading, and it must not fall through to "not understood".
func TestDiagnoseCountsRealityWinsAsHealthy(t *testing.T) {
	d := DiagnoseBox("b6", relaysOf("b6-1", "b6-2"), RelayVerdicts{
		"b6-1": "reality_wins",
		"b6-2": "clean",
	})
	if d.Cause != CauseHealthy {
		t.Fatalf("cause = %q, want %q", d.Cause, CauseHealthy)
	}
	if NeedsAttention(d.Cause) {
		t.Error("a healthy box was raised as something to act on")
	}
}

// Evidence travels with the cause: a console that shows a verdict without it
// teaches nobody, and a playbook acting on one must be able to say what it
// acted on.
func TestDiagnoseCarriesItsEvidence(t *testing.T) {
	d := DiagnoseBox("b7", relaysOf("b7-1", "b7-2"), RelayVerdicts{
		"b7-1": "fingerprint", "b7-2": "clean",
	})
	if len(d.Verdicts) != 2 || d.Verdicts["b7-1"] != "fingerprint" {
		t.Fatalf("evidence = %v", d.Verdicts)
	}
}
