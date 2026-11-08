package entry

import (
	"strconv"
	"strings"
	"testing"
)

func TestProberEnvironmentSeparatesSharedHostPorts(t *testing.T) {
	env := proberEnv("https://api.hikkari.net", "ru-spb", "test-token", "")
	var base int
	for _, line := range strings.Split(env, "\n") {
		if value, ok := strings.CutPrefix(line, "ENTRY_PROBE_SOCKS_BASE="); ok {
			var err error
			base, err = strconv.Atoi(value)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// Each project uses base+0/+100/+200/+300 for independent engines.
	// Include room for each engine's SOCKS and UDP forwarding listeners.
	if base == 0 || base <= 11210 || base+400 > 65535 {
		t.Fatalf("hub config does not isolate Hikkari ports from Nekko: base=%d", base)
	}
}
