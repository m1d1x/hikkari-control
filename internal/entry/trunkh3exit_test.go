package entry

import (
	"os/exec"
	"testing"
)

// TestTheFormerQuicWireUnitsOffScriptIsSoundShell: what takes the R6.3 unit
// off a box parses.
func TestTheFormerQuicWireUnitsOffScriptIsSoundShell(t *testing.T) {
	if out, err := exec.Command("sh", "-n", "-c", trunkH3ExitOff).CombinedOutput(); err != nil {
		t.Fatalf("off script: %v %s", err, out)
	}
}
