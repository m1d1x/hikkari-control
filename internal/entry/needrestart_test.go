package entry

import (
	"strings"
	"testing"
)

// Every script that puts a hub unit on a box keeps needrestart off it: an
// unattended libc6 restarted the trunk entry on 13.11 and cut its clients.
func TestEveryUnitSwapKeepsNeedrestartOff(t *testing.T) {
	scripts := map[string]string{
		"relay":       edgeSwapScript,
		"prober":      proberSwapScript,
		"trunk entry": goEntryUnit.swapScript(nil),
		"trunk pipe":  trunkPipeUnitSpec.swapScript(nil),
	}
	for name, s := range scripts {
		if !strings.HasPrefix(s, "set -eu\n"+keepUnitsFromNeedrestartScript) {
			t.Errorf("%s: the swap does not write the needrestart rule first", name)
		}
	}
	if !strings.Contains(keepUnitsFromNeedrestartScript, "$nrconf{override_rc}->{qr(^nekko-)} = 0;") {
		t.Error("the rule does not exclude nekko- units")
	}
}
