package entry

import (
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
	for _, bad := range []string{"", "4", "0:4", "33:4", "4:51", "4:-1", "a:b"} {
		if _, err := ParseTrunkCopies(bad); !errors.Is(err, ErrRejected) {
			t.Errorf("%q: %v, want rejected", bad, err)
		}
	}
	if trunkCopiesFiles["people"] != "/etc/nekko-trunk-entry.env.copies.json" || trunkCopiesFiles["stand"] != "/etc/nekko-trunk-pipe.env.copies.json" {
		t.Fatalf("files %v: beside each one's wires file", trunkCopiesFiles)
	}
}
