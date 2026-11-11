package entry

import (
	"strings"
	"testing"
)

func TestTrunkWiresParseAndEnv(t *testing.T) {
	w, err := ParseTrunkWires("quic=20448, udp=20447")
	if err != nil {
		t.Fatal(err)
	}
	if env := trunkWiresEnv(w); env != "quic=20448,udp=20447" {
		t.Fatalf("env %q", env)
	}
	for _, bad := range []string{"awg=20450", "udp=80", "udp=20447,quic=20447", "udp"} {
		if _, err := ParseTrunkWires(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if w, err := ParseTrunkWires("off"); err != nil || len(w) != 0 {
		t.Fatalf("off: %v %v", w, err)
	}
	id := Identity{Port: 443, Hikkari: &HikkariParams{Host: "hk.example", User: "u", Pass: "p", TrunkKey: "k", TrunkPorts: []int{443}, TrunkWires: w}}
	id.Hikkari.TrunkWires = map[string]int{"udp": 20447}
	env, err := id.EdgeEnv()
	if err != nil || env["EDGE_HIKKARI_TRUNK_WIRES"] != "udp=20447" {
		t.Fatalf("edge env %v %v", env, err)
	}
}

func TestTrunkEntryWiresFor(t *testing.T) {
	m, err := ParseTrunkEntryWires("udp:2,quic:4")
	if err != nil {
		t.Fatal(err)
	}
	f, err := trunkEntryWiresFor(map[string]int{"udp": 20447, "quic": 20448, "salamander": 20449}, m, true)
	if err != nil || !f.Steer || len(f.Wires) != 2 || f.Wires[0] != (TrunkEntryWire{"udp", "udp", 20447, 2}) || f.Wires[1].Port != 20448 {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := trunkEntryWiresFor(map[string]int{"udp": 20447}, m, false); err == nil || !strings.Contains(err.Error(), "quic") {
		t.Fatalf("a wire the exit does not serve: %v", err)
	}
	for _, bad := range []string{"udp:0", "awg:2", "udp:2,udp:3", "udp"} {
		if _, err := ParseTrunkEntryWires(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
