package entry

import (
	"strings"
	"testing"
)

func TestHikkariEnvCarriesTheTrunk(t *testing.T) {
	id := Identity{Port: 443, Hikkari: &HikkariParams{Host: "hk.example", User: "hikkari", Pass: "p"}}
	env, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, on := env["EDGE_HIKKARI_TRUNK_KEYS"]; on {
		t.Fatal("a relay without a trunk key says one")
	}
	id.Hikkari.TrunkKey, id.Hikkari.TrunkPorts = strings.Repeat("k", 48), []int{443, 2053}
	if env, err = id.EdgeEnv(); err != nil {
		t.Fatal(err)
	}
	if env["EDGE_HIKKARI_TRUNK_KEYS"] != id.Hikkari.TrunkKey || env["EDGE_HIKKARI_TRUNK_PORTS"] != "443,2053" {
		t.Fatalf("trunk env: %v", env)
	}
}

func TestTrunkEntryEnv(t *testing.T) {
	env := trunkEntryEnv("203.0.113.8:443", "hk.example", "key", TrunkEntryOptions{Members: 32, Forward: map[int]int{8443: 443, 8053: 2053}})
	for _, want := range []string{
		"EDGE_TRUNK_EXIT=203.0.113.8:443\n", "EDGE_TRUNK_SNI=hk.example\n", "EDGE_TRUNK_KEY=key\n", "EDGE_TRUNK_MEMBERS=32\n",
		"EDGE_TRUNK_FORWARD=8053=2053,8443=443\n", "EDGE_TRUNK_UDP=8053=2053,8443=443\n",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %q:\n%s", want, env)
		}
	}
}

// TestStandTrunkEntry: the binary and libcronet.so are staged beside the
// running copy, the env is written private, and the swap gets the ports.
func TestStandTrunkEntry(t *testing.T) {
	r := &provokeRecorder{stdin: map[string]string{}}
	bins, libs := testBuild(t), testBuild(t)
	err := standTrunkEntry(t.Context(), r, bins, libs, "EDGE_TRUNK_KEY=k\n", []string{"8053", "8443"}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.cmds, "\n")
	for _, want := range []string{
		"mkdir -p " + trunkEntryDir + ".new",
		"cat > " + trunkEntryDir + ".new/nekko-trunk-entry.new",
		"cat > " + trunkEntryDir + ".new/libcronet.so.new",
		"umask 077; cat > " + trunkEntryEnvFile + ".new",
		"bash -s -- 8053 8443",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("no %q in:\n%s", want, all)
		}
	}
	if !strings.Contains(r.stdin["cat > /etc/systemd/system/"+trunkEntryUnit+".service"], "ExecStart="+trunkEntryBinary+" run") {
		t.Fatal("unit does not run the staged binary")
	}
}

func TestStandTrunkEntryRefusesBeforeTheBox(t *testing.T) {
	p := &Provisioner{}
	for _, o := range []TrunkEntryOptions{
		{Exit: "unl1", Members: 500, Forward: map[int]int{8443: 443}},
		{Exit: "unl1"},
	} {
		if err := p.StandTrunkEntry(t.Context(), "box", o); err == nil {
			t.Fatalf("%+v accepted", o)
		}
	}
}
