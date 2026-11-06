package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/entry"
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
		Jc: 4, Jmin: 40, Jmax: 70, S1: 33, S2: 57, S3: 21, S4: 16, H1: "11", H2: "12", H3: "13-19", H4: "24", I1: "<r 8>",
	}}
	got, err := awgConfig(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jc=4\n", "jmax=70\n", "s2=57\n", "h3=13-19\n", "s3=21\n", "s4=16\n", "i1=<r 8>\n", "endpoint=203.0.113.9:31000\n"} {
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

	// No door on the engine either: startEngine waits for every door, and a
	// wait for one that never opens would blind the round for the whole fleet
	// (eun15 read probe_failed that way).
	edges[0].Awg = &awgEdge{ServerPublicKey: key, ClientPrivateKey: key, ClientAddress: "10.66.0.1"}
	raw, ports, _, err := buildXrayConfig(edges, 11810, "chrome")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ports["x1-1"]; ok {
		t.Fatalf("an awg relay has an engine port: %v", ports)
	}
	if strings.Contains(string(raw), "x1-1") {
		t.Fatalf("an awg relay reached the engine's config: %s", raw)
	}
}

// The prober dials a 3.1 relay with every 3.1 field of the hub's record, by
// the contract on the JSON (internal/entry AwgParams).
func TestAwg31RecordReachesTheProbersDevice(t *testing.T) {
	key := "pBNbQ3vIFMTZCbCkQtLObNUtsM9hAi+ZhlrgZbH0Xgc="
	raw, err := json.Marshal(entry.AwgParams{
		ServerPublicKey: key, ClientPrivateKey: key, ClientAddress: "10.66.0.1",
		Jc: 4, Jmin: 40, Jmax: 70, S1: 16, S2: 16, S3: 16, S4: 16, H1: "1", H2: "2", H3: "3", H4: "4",
		AwgV3: entry.AwgV3{
			HeaderProtectionKey: key, ContentPaddingAddition: "10-50",
			RekeyAfterTime: "100-120", RekeyTimeout: "5-7", RejectAfterTime: "170-190",
			KeepaliveTimeout: "10-15", MaxHandshakeAttempts: "15-20",
			RandomTrailers: true, DisableCookies: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var a awgEdge
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	got, err := awgConfig(edge{Name: "x1-1", Address: "203.0.113.9", Port: 31000, Proto: protoAwg, Awg: &a})
	if err != nil {
		t.Fatal(err)
	}
	keyHex, _ := awgHex(key)
	for _, want := range []string{
		"header_protection_key=" + keyHex, "content_padding_addition=10-50", "rekey_after_time=100-120",
		"rekey_timeout=5-7", "reject_after_time=170-190", "keepalive_timeout=10-15",
		"max_handshake_attempts=15-20", "random_trailers=true", "disable_cookies=true",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("config lacks %q:\n%s", want, got)
		}
	}
	// The device keys go before the peer's public_key, where the device reads
	// them as its own.
	if strings.Index(got, "disable_cookies=") > strings.Index(got, "public_key=") {
		t.Errorf("3.1 keys after the peer:\n%s", got)
	}
}
