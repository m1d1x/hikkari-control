package entry

import "testing"

// The cascade reaches a relay whose own port its trunk opens, whichever exit
// carries the trunk: the Go exit's key or hk-exit's (the people's trunk on
// aeznl1 since G3 of 21.11, when the stand's cascade links went 503 for it).
func TestTheTrunkReachesARelayBehindEitherExit(t *testing.T) {
	for _, c := range []struct {
		name string
		h    *HikkariParams
		port int
		want bool
	}{
		{"Go exit", &HikkariParams{TrunkKey: "k", TrunkPorts: []int{8443}}, 8443, true},
		{"hk-exit", &HikkariParams{TrunkPipeKey: "p", TrunkPorts: []int{8443, 2053}}, 8443, true},
		{"no trunk", &HikkariParams{TrunkPorts: []int{8443}}, 8443, false},
		{"port not opened", &HikkariParams{TrunkPipeKey: "p", TrunkPorts: []int{2053}}, 8443, false},
		{"not Hikkari", nil, 8443, false},
	} {
		if got := trunkReaches(Identity{Port: c.port, Hikkari: c.h}); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
