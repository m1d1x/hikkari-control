package entry

import (
	"strings"
	"testing"
)

// TestARelayThatIsHkExitsBrainSaysItsOwnKeyAndWires: the relay's env names
// hk-exit's key and wires apart from the Go exit's (edge/trunk_pipe.go), the
// trunk's ports for both; a relay with hk-exit alone still names the ports.
func TestARelayThatIsHkExitsBrainSaysItsOwnKeyAndWires(t *testing.T) {
	w, err := ParseTrunkPipeExitWires("h3dgram=20460, udp=20461")
	if err != nil {
		t.Fatal(err)
	}
	if env := trunkWiresEnv(w); env != "h3dgram=20460,udp=20461" {
		t.Fatalf("wires %q", env)
	}
	for _, bad := range []string{"quic=20448", "udp=80", "udp=20461,h3dgram=20461", "h3dgram"} {
		if _, err := ParseTrunkPipeExitWires(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	id := Identity{Port: 443, Hikkari: &HikkariParams{Host: "hk.example", User: "u", Pass: "p",
		TrunkKey: strings.Repeat("g", 48), TrunkPorts: []int{443, 8443}, TrunkWires: map[string]int{"udp": 20447},
		TrunkPipeKey: strings.Repeat("r", 48), TrunkPipeWires: w}}
	env, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	if env["EDGE_HIKKARI_TRUNK_KEYS"] != id.Hikkari.TrunkKey || env["EDGE_HIKKARI_TRUNK_WIRES"] != "udp=20447" ||
		env["EDGE_HIKKARI_TRUNK_PIPE_KEYS"] != id.Hikkari.TrunkPipeKey || env["EDGE_HIKKARI_TRUNK_PIPE_WIRES"] != "h3dgram=20460,udp=20461" ||
		env["EDGE_HIKKARI_TRUNK_PORTS"] != "443,8443" {
		t.Fatalf("both exits: %v", env)
	}
	id.Hikkari.TrunkKey, id.Hikkari.TrunkWires = "", nil
	if env, err = id.EdgeEnv(); err != nil {
		t.Fatal(err)
	}
	if _, on := env["EDGE_HIKKARI_TRUNK_KEYS"]; on || env["EDGE_HIKKARI_TRUNK_PORTS"] != "443,8443" || env["EDGE_HIKKARI_TRUNK_PIPE_KEYS"] == "" {
		t.Fatalf("hk-exit alone: %v", env)
	}
	id.Hikkari.TrunkPipeKey, id.Hikkari.TrunkPipeWires = "", nil
	if env, err = id.EdgeEnv(); err != nil {
		t.Fatal(err)
	}
	for k := range env {
		if strings.HasPrefix(k, "EDGE_HIKKARI_TRUNK_") {
			t.Fatalf("a relay without a trunk says %s", k)
		}
	}
}
