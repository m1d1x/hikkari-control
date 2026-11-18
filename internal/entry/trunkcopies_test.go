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
