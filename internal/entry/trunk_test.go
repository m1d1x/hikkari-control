package entry

import (
	"os"
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
		// 8443 reaches the Hikkari relay itself: its clients copy datagrams.
		"EDGE_TRUNK_UDP_ONE_COPY=8443\n",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %q:\n%s", want, env)
		}
	}
	env = trunkEntryEnv("203.0.113.8:443", "hk.example", "key", TrunkEntryOptions{Members: 32, Forward: map[int]int{8443: 443}, HikkariUDPCopies: 2})
	if !strings.Contains(env, "EDGE_TRUNK_UDP_ONE_COPY=\n") {
		t.Fatalf("two copies asked, one set:\n%s", env)
	}
}

// TestTrunkEntryEnvIsBlind: the entry is told the exit, its trunk key and
// its ports, nothing else. No subscriber's key, no relay's own secret (the
// relay's identity is never an input here), nothing that opens a client's
// TLS (roadmap 3.2, «вход слепой»). A new key in this env is a decision
// to take with the owner, not a test to update.
func TestTrunkEntryEnvIsBlind(t *testing.T) {
	env := trunkEntryEnv("203.0.113.8:443", "hk.example", "trunk-key", TrunkEntryOptions{Members: 32, Forward: map[int]int{8443: 443, 8053: 2053}})
	allowed := map[string]bool{
		"EDGE_TRUNK_EXIT": true, "EDGE_TRUNK_SNI": true, "EDGE_TRUNK_KEY": true, "EDGE_TRUNK_MEMBERS": true,
		"EDGE_TRUNK_FORWARD": true, "EDGE_TRUNK_UDP": true, "EDGE_TRUNK_UDP_ONE_COPY": true,
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(env, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, _, ok := strings.Cut(line, "=")
		if !ok || !allowed[k] {
			t.Fatalf("the entry is told %q", line)
		}
		seen[k] = true
	}
	if len(seen) != len(allowed) {
		t.Fatalf("keys %v", seen)
	}
}

// TestStandTrunkEntry: the binary and libcronet.so are staged beside the
// running copy, the env is written private, and the swap gets the ports.
func TestStandTrunkEntry(t *testing.T) {
	r := &provokeRecorder{stdin: map[string]string{}}
	files := []trunkFile{{"nekko-trunk-entry", testBuild(t)}, {"libcronet.so", testBuild(t)}}
	err := standTrunkUnit(t.Context(), r, goEntryUnit, files, "EDGE_TRUNK_KEY=k\n", nil, []string{"8053", "8443"}, t.Logf)
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
	// The swap reloads into a build that can hand over, on an unchanged unit.
	swap := goEntryUnit.swapScript(files)
	for _, want := range []string{"handoff-version", "systemctl reload " + trunkEntryUnit, "/run/" + trunkEntryUnit + "/control.sock",
		"передан без обрыва", "for f in nekko-trunk-entry libcronet.so;"} {
		if !strings.Contains(swap, want) {
			t.Fatalf("the swap has no %q", want)
		}
	}
}

// TestTheGoEntrysUnitIsUnchanged: the Go entry's unit file is the one the
// boxes run, byte for byte (testdata, as main had it before the stand). A
// unit that changes makes the next rollout a restart, a cut for everyone
// through the entry.
func TestTheGoEntrysUnitIsUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/nekko-trunk-entry.service")
	if err != nil {
		t.Fatal(err)
	}
	if got := goEntryUnit.unitFile(); got != string(want) {
		t.Fatalf("the Go entry's unit changed:\n%s", got)
	}
}

// TestTheStandOverThePipe: its own unit, directory, env and wires, beside
// the entry; it reloads with its own env file, stages hk-pipe, and its env
// is the entry's blind env plus the pipe's own keys, nothing of a client.
func TestTheStandOverThePipe(t *testing.T) {
	unit := trunkPipeUnitSpec.unitFile()
	for _, want := range []string{"ExecStart=" + trunkPipeDir + "/nekko-trunk-entry run", "ExecReload=+" + trunkPipeDir + "/nekko-trunk-entry reload " + trunkPipeEnv,
		"EnvironmentFile=" + trunkPipeEnv, "RuntimeDirectory=" + trunkPipeUnit, "Type=notify"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("the stand's unit has no %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, trunkEntryDir+"/") || strings.Contains(unit, trunkEntryEnvFile) {
		t.Fatalf("the stand's unit names the entry's files:\n%s", unit)
	}
	env := trunkPipeEnvOf("203.0.113.8:443", "hk.example", "trunk-key", TrunkPipeOptions{Members: 32, Forward: map[int]int{18443: 443}})
	allowed := map[string]bool{
		"EDGE_TRUNK_EXIT": true, "EDGE_TRUNK_SNI": true, "EDGE_TRUNK_KEY": true, "EDGE_TRUNK_MEMBERS": true,
		"EDGE_TRUNK_FORWARD": true, "EDGE_TRUNK_UDP": true, "EDGE_TRUNK_UDP_ONE_COPY": true,
		"EDGE_TRUNK_PIPE": true, "EDGE_TRUNK_WIRES_FILE": true, "EDGE_TRUNK_SOURCE_PORTS": true,
	}
	for _, line := range strings.Split(env, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, _, _ := strings.Cut(line, "="); !allowed[k] {
			t.Fatalf("the stand is told %q", line)
		}
	}
	for _, want := range []string{"EDGE_TRUNK_PIPE=" + trunkPipeDir + "/hk-pipe\n", "EDGE_TRUNK_WIRES_FILE=" + trunkPipeWires + "\n",
		"EDGE_TRUNK_SOURCE_PORTS=" + TrunkPipeSourcePorts + "\n", "EDGE_TRUNK_UDP_ONE_COPY=18443\n"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %q:\n%s", want, env)
		}
	}
	wires, err := trunkPipeWiresOf(map[string]int{"udp": 20447, "quic": 20448}, 0, "", "")
	if err != nil || wires != `{"steer":true,"wires":[{"name":"udp","kind":"udp","port":20447,"members":2}]}`+"\n" {
		t.Fatalf("wires %q %v", wires, err)
	}
	// Chromium's QUIC wire (R6.3), at the port of its end on the exit.
	h3, err := trunkPipeWiresOf(nil, 4445, "proxy", "h3dgram")
	if err != nil || h3 != `{"steer":true,"wires":[{"name":"h3dgram","kind":"h3dgram","port":4445,"members":4,"usage":"proxy"}],"voice":"h3dgram"}`+"\n" {
		t.Fatalf("wires %q %v", h3, err)
	}
	if _, err := trunkPipeWiresOf(nil, 0, "", "h3dgram"); err == nil {
		t.Fatal("voice held on a wire the stand has not")
	}
	r := &provokeRecorder{stdin: map[string]string{}}
	files := []trunkFile{{"nekko-trunk-entry", testBuild(t)}, {"libcronet.so", testBuild(t)}, {"hk-pipe", testBuild(t)}}
	if err := standTrunkUnit(t.Context(), r, trunkPipeUnitSpec, files, env, map[string]string{"wires.json": wires}, []string{"18443"}, t.Logf); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.cmds, "\n")
	for _, want := range []string{"cat > " + trunkPipeDir + ".new/hk-pipe.new", "umask 022; cat > " + trunkPipeWires + ".new", "bash -s -- 18443"} {
		if !strings.Contains(all, want) {
			t.Fatalf("no %q in:\n%s", want, all)
		}
	}
	if !strings.Contains(trunkPipeUnitSpec.swapScript(files), `EDGE_TRUNK_PIPE="$dir.new/hk-pipe"`) {
		t.Fatal("the stand's check runs a pipe other than the staged one")
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

// TestTheStandsPortsAreKeptOnTheEntrysRow: the stand records its ports on
// the entry's row of trunk_entries (migration 063) and clears them when it
// is taken off; a box with no entry has no row to put a stand beside.
func TestTheStandsPortsAreKeptOnTheEntrysRow(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	s := &Service{pool: pool}
	ctx := t.Context()
	if err := s.recordTrunkPipe(ctx, "nosuch1", map[int]int{18443: 443}); err == nil {
		t.Fatal("a stand was recorded beside no entry")
	}
	for _, box := range []string{"pipeentry1", "pipeexit1"} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, '\x00', 'test', 'test', 'hikkari') ON CONFLICT (name) DO NOTHING`, box); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.recordTrunkEntry(ctx, "pipeentry1", TrunkEntryOptions{Exit: "pipeexit1", Members: 32, Forward: map[int]int{8443: 443}}); err != nil {
		t.Fatal(err)
	}
	read := func() []byte {
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT pipe_forward FROM trunk_entries WHERE entry_box = 'pipeentry1'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if read() != nil {
		t.Fatal("an entry has a stand before one is stood")
	}
	if err := s.recordTrunkPipe(ctx, "pipeentry1", map[int]int{18443: 443}); err != nil {
		t.Fatal(err)
	}
	if fwd, err := forwardOf(read()); err != nil || fwd[18443] != 443 || len(fwd) != 1 {
		t.Fatalf("the stand's ports %v %v", fwd, err)
	}
	if p := (TrunkPair{Pipe: map[int]int{18443: 443, 18053: 2053}}); p.PipeListen(443) != 18443 || p.PipeListen(80) != 0 {
		t.Fatalf("PipeListen %d %d", p.PipeListen(443), p.PipeListen(80))
	}
	if err := s.recordTrunkPipe(ctx, "pipeentry1", nil); err != nil {
		t.Fatal(err)
	}
	if read() != nil {
		t.Fatal("the stand's ports stay after it was taken off")
	}
	if err := s.forgetTrunkEntry(ctx, "pipeentry1"); err != nil {
		t.Fatal(err)
	}
}
