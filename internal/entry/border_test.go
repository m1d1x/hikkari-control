package entry

import (
	"net/netip"
	"strings"
	"testing"
)

// ams3 forwarding aezamsk1's trunk to unl1: TCP of the relay and the pool,
// UDP of the pipe's wires, only from the entry's address.
func TestABorderPointForwardsTheEntriesTrunkToTheCoreAlone(t *testing.T) {
	b := borderPlan{Entries: []netip.Addr{netip.MustParseAddr("10.0.0.5")}, Core: netip.MustParseAddr("10.0.1.1"),
		TCP: []int{443, 2053}, UDP: []int{20460, 20461}}
	nft := b.nft()
	for _, want := range []string{
		"delete table ip hikkari_border",
		"ip saddr { 10.0.0.5 } tcp dport { 443, 2053 } dnat to 10.0.1.1",
		"ip saddr { 10.0.0.5 } udp dport { 20460, 20461 } dnat to 10.0.1.1",
		"ip daddr 10.0.1.1 ct status dnat masquerade",
	} {
		if !strings.Contains(nft, want) {
			t.Errorf("no %q in\n%s", want, nft)
		}
	}
	ufw := strings.Join(b.ufw(""), "\n")
	if !strings.Contains(ufw, "ufw route allow proto udp from 10.0.0.5 to 10.0.1.1 port 20460,20461 comment hikkari-border") ||
		!strings.Contains(ufw, "ufw route allow proto tcp from 10.0.0.5 to 10.0.1.1 port 443,2053") {
		t.Errorf("ufw %s", ufw)
	}
	stand := b.standScript()
	for _, want := range []string{"sysctl -q -w net.ipv4.ip_forward=1", "systemctl enable hikkari-border.service", "conntrack -D -p udp -s 10.0.0.5 --dport 20461"} {
		if !strings.Contains(stand, want) {
			t.Errorf("no %q in the script", want)
		}
	}
	if strings.Contains(stand, "dnat to 10.0.0.5") {
		t.Error("forwards to the entry")
	}
}
