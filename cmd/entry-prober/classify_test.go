package main

import "testing"

func TestClassifyMatrix(t *testing.T) {
	cases := []struct {
		name                string
		reality, k3, k1, k2 bool
		want                string
	}{
		// K2 down short-circuits regardless of the others: the vantage is cut
		// off and nothing downstream is readable.
		{"network down beats all, everything down", false, false, false, false, verdictNetworkDown},
		{"network down beats a live reality", true, true, true, false, verdictNetworkDown},
		{"network down beats a live k1", false, false, true, false, verdictNetworkDown},

		// K2 up — the real verdicts.
		{"all up is clean", true, true, true, true, verdictClean},
		{"reality up, k1 down is the best outcome", true, true, false, true, verdictRealityWins},
		{"reality down, handshake refused, k1 up is a fingerprint hit", false, false, true, true, verdictFingerprint},
		{"reality down, handshake refused, k1 down is a banned ip", false, false, false, true, verdictIPBanned},

		// K3 up with no answer behind it: the relay carried us and our own
		// upstream was silent. Said plainly, because the alternative reading
		// (fingerprint) would re-key the whole fleet over our outage — and
		// the fleet is exactly what a silent gateway affects at once.
		{"tunnel dialled, upstream silent, k1 up is not the relay", false, true, true, true, verdictUnknown},
		{"tunnel dialled, upstream silent, k1 down is still not the relay", false, true, false, true, verdictUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.reality, c.k3, c.k1, c.k2); got != c.want {
				t.Fatalf("classify(reality=%v,k3=%v,k1=%v,k2=%v) = %q, want %q",
					c.reality, c.k3, c.k1, c.k2, got, c.want)
			}
		})
	}
}

func TestClassifyXhttpMatrix(t *testing.T) {
	cases := []struct {
		name                                string
		tunnel, k3, named, bare, certOK, k2 bool
		want                                string
	}{
		{"network down beats all", true, true, true, true, true, false, verdictNetworkDown},
		{"a live tunnel is clean", true, true, true, true, true, true, verdictClean},
		{"tunnel dialled, upstream silent: not the relay", false, true, true, true, true, true, verdictUnknown},
		{"site answers by name, tunnel cut: protocol detected", false, false, true, true, true, true, verdictFingerprint},
		{"a bad certificate is ours, not the censor's", false, false, true, true, false, true, verdictUnknown},
		{"address answers, name does not: domain blocked", false, false, false, true, false, true, verdictDomainBlocked},
		{"nothing answers: banned ip", false, false, false, false, false, true, verdictIPBanned},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyXhttp(c.tunnel, c.k3, c.named, c.bare, c.certOK, c.k2); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestClassifyReprobeMatrix(t *testing.T) {
	cases := []struct {
		name            string
		named, bare, k2 bool
		want            string
	}{
		{"network down beats a passing name", true, true, false, verdictNetworkDown},
		{"the name passes", true, true, true, verdictClean},
		{"the name passes even where bare tls does not", true, false, true, verdictClean},
		{"the address answers, the name does not", false, true, true, verdictDomainBlocked},
		// The lent address is down: that says nothing about the name, and
		// must not reset its count of clean days.
		{"nothing answers on the lent address", false, false, true, verdictUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyReprobe(c.named, c.bare, c.k2); got != c.want {
				t.Fatalf("classifyReprobe(named=%v,bare=%v,k2=%v) = %q, want %q", c.named, c.bare, c.k2, got, c.want)
			}
		})
	}
}

func TestTheTunnelAnswersWithAny2xx(t *testing.T) {
	for code, want := range map[int]bool{200: true, 204: true, 301: false, 404: false, 503: false} {
		if got := tunnelAnswered(code); got != want {
			t.Errorf("tunnelAnswered(%d) = %v, want %v", code, got, want)
		}
	}
}
