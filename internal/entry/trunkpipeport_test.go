package entry

import (
	"net/netip"
	"strings"
	"testing"
)

func TestStandPortAliasMatchesOnlyTheStandAndTranslatesToTheCore(t *testing.T) {
	alias := standPortAlias{box: "entry-a", entry: netip.MustParseAddr("192.0.2.2"),
		point: netip.MustParseAddr("192.0.2.1"), core: netip.MustParseAddr("198.51.100.2"),
		public: 2053, destination: 8443}
	script := alias.installScript()
	want := "ip saddr 192.0.2.2 ip daddr 192.0.2.1 tcp sport 10000-19999 tcp dport 2053 counter dnat to 198.51.100.2:8443"
	if !strings.Contains(script, want) || strings.Contains(script, "%!") {
		t.Fatalf("expected a stand-only port alias: %s", script)
	}
	if !strings.Contains(alias.removeScript(), "table=hikkari_stand_port_entry_a") {
		t.Fatal("cleanup must target only the alias")
	}
}

func TestStandPortEditsOnlyThePortAndRequiresTheSamePoints(t *testing.T) {
	raw := `{"points":["192.0.2.1","192.0.2.2"],"steer":true,"wires":[]}`
	points := []string{"192.0.2.1", "192.0.2.2"}
	alt, old, err := standWiresAtPort(raw, points, 2053)
	if err != nil || old != 0 || !strings.Contains(alt, `"pool_port":2053`) {
		t.Fatalf("alternate wires: %q old=%d err=%v", alt, old, err)
	}
	restored, old, err := standWiresAtPort(alt, points, 0)
	if err != nil || old != 2053 || strings.TrimSpace(restored) != raw {
		t.Fatalf("restored wires: %q old=%d err=%v", restored, old, err)
	}
	if _, _, err := standWiresAtPort(raw, points[:1], 2053); err == nil {
		t.Fatal("an alias was accepted with a different point list")
	}
	if _, err := withPoints([]byte(alt), points[:1]); err == nil {
		t.Fatal("active experiment accepted a different point list")
	}
}
