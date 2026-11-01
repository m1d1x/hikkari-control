package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

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

// An engine never starts on a SOCKS port another process holds: its probes
// would ride the other process's tunnels.
func TestATakenSocksPortRefusesTheEngine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := portsFree(map[string]int{"r-1": ln.Addr().(*net.TCPAddr).Port}); err == nil {
		t.Fatal("a taken port was reported free")
	}
}

// A verdict reached over a path that loses some flows is no evidence against
// the relay: eun13 was called fingerprint and ip_banned minutes apart on a path
// where half the flows died before reaching it, while the rest went through.
func TestSettlePathTakesTheAccusationOffALossyPath(t *testing.T) {
	for _, c := range []struct {
		verdict       string
		through       int
		want          string
		wantPathNoted bool
	}{
		{verdictFingerprint, 6, verdictUnknown, true},
		{verdictIPBanned, 1, verdictUnknown, true},
		{verdictDomainBlocked, 11, verdictUnknown, true},
		{verdictFingerprint, 12, verdictFingerprint, false},
		{verdictIPBanned, 0, verdictIPBanned, false},
	} {
		got := settlePath(probeResult{Verdict: c.verdict, Detail: "xudp 0/100"}, c.through, pathFlows)
		if got.Verdict != c.want {
			t.Errorf("%s with %d/%d flows = %s, want %s", c.verdict, c.through, pathFlows, got.Verdict, c.want)
		}
		if noted := strings.Contains(got.Detail, "tcp flows through"); noted != c.wantPathNoted {
			t.Errorf("%s with %d/%d flows: detail %q", c.verdict, c.through, pathFlows, got.Detail)
		}
		if !strings.Contains(got.Detail, "xudp 0/100") {
			t.Errorf("the earlier detail was lost: %q", got.Detail)
		}
	}
}

// A live address passes all its flows and a dead one none, so settlePath's two
// standing cases are what a sound path and a cut-off address measure as.
func TestProbeFlowsCountsWhatConnects(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if got := probeFlows(ln.Addr().String(), pathFlows, time.Second); got != pathFlows {
		t.Errorf("live address: %d/%d flows", got, pathFlows)
	}
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr().String()
	closed.Close()
	if got := probeFlows(addr, pathFlows, time.Second); got != 0 {
		t.Errorf("closed port: %d/%d flows", got, pathFlows)
	}
}

func TestOnlyAccusingVerdictsAreRetried(t *testing.T) {
	for v, want := range map[string]bool{
		verdictFingerprint: true, verdictIPBanned: true, verdictDomainBlocked: true,
		verdictClean: false, verdictRealityWins: false, verdictUnknown: false, verdictNetworkDown: false,
	} {
		if got := accusesRelay(v); got != want {
			t.Errorf("accusesRelay(%s) = %v", v, got)
		}
	}
}
