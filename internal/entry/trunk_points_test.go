package entry

import (
	"strings"
	"testing"
)

// The people's points go into the file their brain over hk-pipe reads
// (pipeBrainEnv beside the entry's env), not the Go entry's wires file: a
// write to the other one would change nothing and say it did (G3).
func TestThePeoplesPointsGoIntoTheFileTheirBrainReads(t *testing.T) {
	env := pipeBrainEnv(goEntryUnit, "")
	if !strings.Contains(env, "EDGE_TRUNK_WIRES_FILE="+trunkEntryPipeWires+"\n") {
		t.Fatalf("the brain reads another file than %s: %q", trunkEntryPipeWires, env)
	}
	if trunkEntryPipeWires == TrunkEntryWiresFile {
		t.Fatalf("the pipe's wires file is the Go entry's %s", TrunkEntryWiresFile)
	}
	if !strings.Contains(trunkEntryWiresScript, "> "+trunkEntryPipeWires+".new") {
		t.Fatalf("the script writes elsewhere: %q", trunkEntryWiresScript)
	}
}
