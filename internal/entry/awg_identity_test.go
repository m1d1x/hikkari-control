package entry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A minted relay's numbers obey what the protocol needs, and differ per relay:
// no two relays may share what a censor would look for.
func TestNewAwgIdentityObeysTheProtocol(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := newAwgIdentity(31000)
		if err != nil {
			t.Fatal(err)
		}
		a := id.Awg
		if id.proto() != ProtoAmneziaWG || id.echoPort() != 0 {
			t.Fatalf("proto %s, echo %d", id.proto(), id.echoPort())
		}
		if a.S1+148 == a.S2+92 {
			t.Fatalf("handshake messages of one size: s1=%d s2=%d", a.S1, a.S2)
		}
		if a.Jc < 1 || a.Jmin < 1 || a.Jmax <= a.Jmin {
			t.Fatalf("junk %d %d %d", a.Jc, a.Jmin, a.Jmax)
		}
		hs := map[string]bool{a.H1: true, a.H2: true, a.H3: true, a.H4: true, "1": true, "2": true, "3": true, "4": true}
		if len(hs) != 8 {
			t.Fatalf("headers not distinct from each other and from WireGuard's: %v", hs)
		}
		if raw, err := base64.StdEncoding.DecodeString(a.ServerPublicKey); err != nil || len(raw) != 32 {
			t.Fatalf("server key %q", a.ServerPublicKey)
		}
		if seen[a.ServerPrivateKey] || seen[a.ProbePrivateKey] {
			t.Fatal("a key was drawn twice")
		}
		seen[a.ServerPrivateKey], seen[a.ProbePrivateKey] = true, true
	}
}

// The record round-trips, and a record of this protocol passes the same checks
// as any other; one lacking its half does not.
func TestAwgRecordRoundTrips(t *testing.T) {
	id, err := newAwgIdentity(31000)
	if err != nil {
		t.Fatal(err)
	}
	rec := id.record("x1-1", "203.0.113.9", "", 1, 2)
	if err := rec.halves(); err != nil {
		t.Fatal(err)
	}
	if err := rec.check(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back RelayRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.AwgParams == nil || back.AwgParams.ServerPublicKey != id.Awg.ServerPublicKey || back.AwgParams.ClientAddress != AwgProbeAddress {
		t.Fatalf("%+v", back.AwgParams)
	}
	rec.AwgParams = nil
	if err := rec.halves(); err == nil {
		t.Fatal("a record without its half passed")
	}
}

// The env the hub plants is what the edge reads (edge/awg.go, by contract).
func TestAwgEdgeEnvNamesEveryField(t *testing.T) {
	id, err := newAwgIdentity(31000)
	if err != nil {
		t.Fatal(err)
	}
	env, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"EDGE_PROTO", "EDGE_PORT", "EDGE_AWG_PRIVATE_KEY", "EDGE_AWG_PROBE_KEY", "EDGE_AWG_PROBE_ADDRESS", "EDGE_AWG_JC", "EDGE_AWG_JMIN", "EDGE_AWG_JMAX", "EDGE_AWG_S1", "EDGE_AWG_S2", "EDGE_AWG_H1", "EDGE_AWG_H2", "EDGE_AWG_H3", "EDGE_AWG_H4"} {
		if env[k] == "" {
			t.Errorf("%s is empty", k)
		}
	}
	// The swap script expands the env through a shell: a space in a value
	// makes its second half a command (eun15, exit 127).
	for k, v := range env {
		if strings.ContainsAny(v, " \t\n\"'$`\\") {
			t.Errorf("%s=%q does not survive the swap script", k, v)
		}
	}
	if env["EDGE_PROTO"] != ProtoAmneziaWG || env["EDGE_PORT"] != "31000" {
		t.Errorf("%v", env)
	}
}

// A box of this protocol holds one relay, on a high UDP port drawn for it, and
// one key per relay.
func TestAwgRelayGetsAHighPortOfItsOwn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box := boxName("w", uuid.New())
	u := uuid.New()
	addr := testAddr([6]byte(u[:6]), 1)
	if err := h.svc.CreateBox(ctx, BoxSpec{Name: box, Address: addr, Provider: "vultr", Location: "amsterdam", Proto: ProtoAmneziaWG}); err != nil {
		t.Fatal(err)
	}
	name, err := h.svc.AddRelay(ctx, box, KindPersonal)
	if err != nil {
		t.Fatal(err)
	}
	var ct []byte
	if err := h.pool.QueryRow(ctx, `SELECT identity_ct FROM entry_relays WHERE name = $1`, name).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	id, err := openIdentity(ct)
	if err != nil {
		t.Fatal(err)
	}
	if id.Awg == nil || id.Port < awgPortMin || id.Port > awgPortMax {
		t.Fatalf("port %d, identity %+v", id.Port, id.Awg)
	}
	if relayTarget(ProtoAmneziaWG) != 1 {
		t.Fatal("more than one relay per machine")
	}
	if h.svc.protoSlots(ProtoAmneziaWG) != AwgSlots || h.svc.protoSlots(ProtoHysteria2) != VoiceSlotsPerRegion {
		t.Fatal("slots per protocol")
	}
}
