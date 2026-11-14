package entry

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheQuicWiresExitEndKeepsItsKeyOutOfTheEnvAndArgv: the unit's env
// names the port, the relay's domain and its trunk ports, the key goes in as
// a credential only, and the scripts are sound shell.
func TestTheQuicWiresExitEndKeepsItsKeyOutOfTheEnvAndArgv(t *testing.T) {
	env := trunkH3ExitEnvOf(4445, "hk.example", map[int]bool{8443: true, 443: true})
	if env != "EDGE_TRUNK_H3_PORT=4445\nEDGE_TRUNK_H3_HOST=hk.example\nEDGE_TRUNK_PORTS=443,8443\n" {
		t.Fatalf("env %q", env)
	}
	for _, want := range []string{"LoadCredential=key:" + trunkH3ExitKey, "--key-file %d/key", "--h3 0.0.0.0:${EDGE_TRUNK_H3_PORT} --h3-wire-id 6",
		"ExecStartPre=+" + trunkH3ExitDir + "/cert.sh", "DynamicUser=yes", "--ports ${EDGE_TRUNK_PORTS}"} {
		if !strings.Contains(trunkH3ExitUnitFile, want) {
			t.Fatalf("the unit lacks %q:\n%s", want, trunkH3ExitUnitFile)
		}
	}
	if strings.Contains(trunkH3ExitUnitFile, "--key ") {
		t.Fatal("the key is in the command line")
	}
	for name, s := range map[string]string{"cert": trunkH3ExitCertScript, "swap": trunkH3ExitSwap, "off": trunkH3ExitOff} {
		if out, err := exec.Command("sh", "-n", "-c", s).CombinedOutput(); err != nil {
			t.Fatalf("%s script: %v %s", name, err, out)
		}
	}
}
