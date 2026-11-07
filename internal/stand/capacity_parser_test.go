package stand

import (
	"context"
	"strings"
	"testing"
)

func TestCapacityParserRetainsHumanLabelsAndUsesStableNodeIdentity(t *testing.T) {
	key := strings.Repeat("a", 64)
	parser := fakeExe(t, "capacity-parser", `cat >/dev/null
cat <<'EOF'
[{"label":"Hikkari Riga 001","node":"riga-001","capacityKey":"`+key+`","config":{"type":"direct"}}]
EOF
`)
	profiles, err := Profiles(context.Background(), parser, "http://subscription.test/private")
	if err != nil || len(profiles) != 1 || profiles[0].Label != "riga-001" || profiles[0].CapacityKey != key {
		t.Fatalf("label caused profile loss: %+v %v", profiles, err)
	}
}
