package main

import (
	"strings"
	"testing"
)

// Silence on UDP cannot tell a censor from a lost path, so it never accuses.
func TestClassifyAwg(t *testing.T) {
	for _, c := range []struct {
		ok, k2 bool
		want   string
	}{
		{true, true, verdictClean},
		{false, true, verdictUnknown},
		{true, false, verdictNetworkDown},
		{false, false, verdictNetworkDown},
	} {
		if got := classifyAwg(c.ok, c.k2); got != c.want {
			t.Errorf("classifyAwg(%v, %v) = %q, want %q", c.ok, c.k2, got, c.want)
		}
		if accusesRelay(classifyAwg(c.ok, c.k2)) {
			t.Errorf("classifyAwg(%v, %v) accuses the relay", c.ok, c.k2)
		}
	}
}

func TestAwgConfigCarriesTheObfuscation(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	e := edge{Name: "x1-1", Address: "203.0.113.9", Port: 31000, Proto: protoAwg, Awg: &awgEdge{
		ServerPublicKey: key, ClientPrivateKey: key, ClientAddress: "10.66.0.1",
		Jc: 4, Jmin: 40, Jmax: 70, S1: 33, S2: 57, H1: "11", H2: "12", H3: "13", H4: "14",
	}}
	got, err := awgConfig(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jc=4\n", "jmax=70\n", "s2=57\n", "h3=13\n", "endpoint=203.0.113.9:31000\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("config lacks %q:\n%s", want, got)
		}
	}
	e.Awg.ClientPrivateKey = "short"
	if _, err := awgConfig(e); err == nil {
		t.Error("a bad key was accepted")
	}
}

// Xray cannot dial this protocol: it must not reach the engine's config, or the
// whole engine would refuse to start and take every other probe with it.
func TestAwgStaysOutOfTheEngine(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	edges := []edge{{Name: "x1-1", Address: "203.0.113.9", Port: 31000, Proto: protoAwg,
		Awg: &awgEdge{ServerPublicKey: key, ClientPrivateKey: key, ClientAddress: "10.66.0.1"}}}
	if err := validateEdges(edges); err != nil {
		t.Fatal(err)
	}
	edges[0].Awg = nil
	if err := validateEdges(edges); err == nil {
		t.Fatal("an awg relay without keys was accepted")
	}
}
