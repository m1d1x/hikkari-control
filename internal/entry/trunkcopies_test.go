package entry

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTrunkCopiesAreMembersAndAWindowOrOff(t *testing.T) {
	c, err := ParseTrunkCopies("4:4")
	if err != nil || c != (TrunkCopies{Members: 4, WindowMS: 4}) {
		t.Fatalf("4:4 is %+v, %v", c, err)
	}
	if c, err := ParseTrunkCopies("off"); err != nil || c != (TrunkCopies{}) {
		t.Fatalf("off is %+v, %v", c, err)
	}
	if c, err := ParseTrunkCopies("demand:300"); err != nil || c != (TrunkCopies{OnDemandS: 300}) {
		t.Fatalf("demand:300 is %+v, %v", c, err)
	}
	if raw, _ := json.Marshal(TrunkCopies{OnDemandS: 300}); string(raw) != `{"members":0,"window_ms":0,"on_demand_s":300}` {
		t.Fatalf("demand file %s", raw)
	}
	if raw, _ := json.Marshal(TrunkCopies{}); string(raw) != `{"members":0,"window_ms":0}` {
		t.Fatalf("off file %s: a copy of every datagram", raw)
	}
	for _, bad := range []string{"", "4", "0:4", "33:4", "4:51", "4:-1", "a:b", "demand:", "demand:0", "demand:3601", "demand:x"} {
		if _, err := ParseTrunkCopies(bad); !errors.Is(err, ErrRejected) {
			t.Errorf("%q: %v, want rejected", bad, err)
		}
	}
	if trunkCopiesFiles["people"] != "/etc/nekko-trunk-entry.env.copies.json" || trunkCopiesFiles["stand"] != "/etc/nekko-trunk-pipe.env.copies.json" {
		t.Fatalf("files %v: beside each one's wires file", trunkCopiesFiles)
	}
}

func TestTheStreamWindowIsWholeMiBFromTwoToSixtyFourOrOff(t *testing.T) {
	for raw, want := range map[string]int{"16": 16, " 2 ": 2, "64": 64, "off": 0} {
		if w, err := ParseTrunkStreamWindow(raw); err != nil || w.MiB != want {
			t.Fatalf("%q: %+v %v, want %d MiB", raw, w, err, want)
		}
	}
	for _, raw := range []string{"1", "65", "8.5", "", "16MiB"} {
		if _, err := ParseTrunkStreamWindow(raw); err == nil {
			t.Fatalf("%q went", raw)
		}
	}
	for who, env := range trunkStreamWindowUnitEnv {
		if env == "" {
			t.Fatalf("%s: no env", who)
		}
	}
}
