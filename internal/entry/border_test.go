package entry

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

// ams3 forwarding aezamsk1's trunk to unl1: TCP of the relay, UDP of the
// pipe's wires, only from the entry's address, in a table of unl1's own.
func TestABorderPointForwardsTheEntriesTrunkToTheCoreAlone(t *testing.T) {
	b := borderPlan{Entries: []netip.Addr{netip.MustParseAddr("10.0.0.5")}, CoreBox: "unl1", Core: netip.MustParseAddr("10.0.1.1"),
		TCP: []int{443}, UDP: []int{20460, 20461}}
	nft := b.nft()
	for _, want := range []string{
		"delete table ip hikkari_border_unl1",
		"ip saddr { 10.0.0.5 } tcp dport { 443 } dnat to 10.0.1.1",
		"ip saddr { 10.0.0.5 } udp dport { 20460, 20461 } dnat to 10.0.1.1",
		"ip daddr 10.0.1.1 ct status dnat masquerade",
	} {
		if !strings.Contains(nft, want) {
			t.Errorf("no %q in\n%s", want, nft)
		}
	}
	ufw := strings.Join(b.ufw(""), "\n")
	if !strings.Contains(ufw, "ufw route allow proto udp from 10.0.0.5 to 10.0.1.1 port 20460,20461 comment hikkari-border-unl1") ||
		!strings.Contains(ufw, "ufw route allow proto tcp from 10.0.0.5 to 10.0.1.1 port 443 comment hikkari-border-unl1") {
		t.Errorf("ufw %s", ufw)
	}
	stand := b.standScript()
	for _, want := range []string{
		"sysctl -q -w net.ipv4.ip_forward=1",
		"systemctl enable hikkari-border-unl1.service",
		"/etc/hikkari-border-unl1.nft",
		"conntrack -D -p udp -s 10.0.0.5 --dport 20461",
		// F1's single table to the same core goes, once the core's own is in.
		"grep -q 'dnat to 10.0.1.1$' /etc/hikkari-border.nft",
	} {
		if !strings.Contains(stand, want) {
			t.Errorf("no %q in the script", want)
		}
	}
	if strings.Index(stand, "nft -f /etc/hikkari-border-unl1.nft") > strings.Index(stand, "nft delete table ip hikkari_border ") {
		t.Error("F1's table goes before the core's own is in")
	}
	if strings.Contains(stand, "dnat to 10.0.0.5") {
		t.Error("forwards to the entry")
	}
}

// One point forwards to two cores at once (G2: ams3 the people's trunk to
// unl1, the stand's to aeznl1): taking one off leaves the other.
func TestABorderPointTakesOneCoreOffAndKeepsTheOther(t *testing.T) {
	a := borderPlan{CoreBox: "aeznl1"}
	stop := a.stopScript()
	for _, want := range []string{"hikkari-border-aeznl1.service", "nft delete table ip hikkari_border_aeznl1", "grep -E '# hikkari-border-aeznl1$'"} {
		if !strings.Contains(stop, want) {
			t.Errorf("no %q in\n%s", want, stop)
		}
	}
	if strings.Contains(stop, "unl1") {
		t.Errorf("taking aeznl1 off touches unl1:\n%s", stop)
	}
	if a.table() == (borderPlan{CoreBox: "unl1"}).table() {
		t.Error("two cores share a table")
	}
	// Off for every core keeps the core side's own rules (a box can be a
	// point and a core at once: unl1).
	if !strings.Contains(stopAllBorders, "grep -v '# hikkari-border-in-'") {
		t.Error("off for every core deletes the core's rules for its points")
	}
}

// The core opens its trunk ports to the point's address alone.
func TestTheCoreOpensItsTrunkToThePointAlone(t *testing.T) {
	b := borderPlan{CoreBox: "aeznl1", TCP: []int{8443}, UDP: []int{20470, 20471}}
	open := b.coreOpenScript(netip.MustParseAddr("10.0.2.2"), "unl1")
	for _, want := range []string{
		"ufw allow proto tcp from 10.0.2.2 to any port 8443 comment hikkari-border-in-unl1",
		"ufw allow proto udp from 10.0.2.2 to any port 20471 comment hikkari-border-in-unl1",
	} {
		if !strings.Contains(open, want) {
			t.Errorf("no %q in\n%s", want, open)
		}
	}
	if !strings.Contains(coreCloseScript("unl1"), "# hikkari-border-in-unl1$") {
		t.Error("closing does not find the point's rules")
	}
}

// The core's identity is the primary's keys on ports of its own: the relay on
// the port given, hk-exit's wires moved by as much, no Go exit.
func TestTheCoreTakesThePrimarysKeysOnPortsOfItsOwn(t *testing.T) {
	primary := Identity{Port: 443, LocalPortBase: 30000, Hikkari: &HikkariParams{Host: "rx.example", User: "hikkari", Pass: "p",
		TrunkKey: "go", TrunkPorts: []int{443, 2053}, TrunkWires: map[string]int{"udp": 20447},
		TrunkPipeKey: "pipe", TrunkPipeWires: map[string]int{"h3dgram": 20460, "udp": 20461}}}
	core, err := coreIdentity(primary, 8443)
	if err != nil {
		t.Fatal(err)
	}
	h := core.Hikkari
	if core.Port != 8443 || h.TrunkPipeKey != "pipe" || h.Host != "rx.example" || h.Pass != "p" {
		t.Fatalf("core %+v %+v", core, h)
	}
	if h.TrunkPipeWires["h3dgram"] != 28460 || h.TrunkPipeWires["udp"] != 28461 {
		t.Fatalf("wires %v", h.TrunkPipeWires)
	}
	if h.TrunkKey != "" || h.TrunkWires != nil || len(h.TrunkPorts) != 1 || h.TrunkPorts[0] != 8443 {
		t.Fatalf("Go exit or loopback ports %+v", h)
	}
	if primary.Port != 443 || primary.Hikkari.TrunkPipeWires["udp"] != 20461 || primary.Hikkari.TrunkKey != "go" {
		t.Fatal("the primary's identity changed")
	}
	if _, err := coreIdentity(Identity{Port: 443, Hikkari: &HikkariParams{}}, 8443); err == nil {
		t.Fatal("a primary without hk-exit makes a core")
	}
}

// The stand's points go into its wires file in place of the old ones, the
// rest of the file as it was.
func TestTheStandsPointsGoIntoItsWiresFile(t *testing.T) {
	raw := `{"steer":true,"wires":[{"name":"udp","kind":"udp","port":28461,"members":2}],"pool":8,"points":["1.1.1.1"]}`
	out, err := withPoints([]byte(raw), []string{"179.253.75.148", "146.103.121.11"})
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Steer  bool
		Pool   int
		Wires  []map[string]any
		Points []string
	}
	if err := json.Unmarshal([]byte(out), &f); err != nil {
		t.Fatal(err)
	}
	if !f.Steer || f.Pool != 8 || len(f.Wires) != 1 || len(f.Points) != 2 || f.Points[1] != "146.103.121.11" {
		t.Fatalf("file %s", out)
	}
	out, err = withPoints([]byte(raw), nil)
	if err != nil || strings.Contains(out, "points") {
		t.Fatalf("no points: %s %v", out, err)
	}
	w, err := trunkPipeWiresOf(map[string]int{"udp": 28461}, TrunkPipeWires{Members: map[string]int{"udp": 2}, Points: []string{"10.0.0.1"}})
	if err != nil || !strings.Contains(w, `"points":["10.0.0.1"]`) {
		t.Fatalf("wires file %s %v", w, err)
	}
}

// A border point cuts its own new connections to Russia at the kernel, as
// the egress does, and keeps the cut across boots.
func TestABorderPointOpensNothingToRussia(t *testing.T) {
	s := noRUScript("add element inet hikkari_egress ru4 { 5.3.0.0/16 }\nadd element inet hikkari_egress ru6 { 2a02:6b8::/32 }\n")
	for _, want := range []string{
		"add element inet hikkari_noru ru4 { 5.3.0.0/16 }",
		"add element inet hikkari_noru ru6 { 2a02:6b8::/32 }",
		`ct state new ip daddr @ru4 counter name "ru_cut" reject`,
		`ct state new ip6 daddr @ru6 counter name "ru_cut" reject`,
		"systemctl enable hikkari-noru.service",
		// the tunnel to the egress is cut and counted there (H6)
		`oifname "hk-egress" return`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "hikkari_egress") {
		t.Error("the point's cut writes into the egress's table")
	}
	if strings.Contains(stopAllBorders, "hikkari_noru") || strings.Contains((borderPlan{CoreBox: "unl1"}).stopScript(), "hikkari_noru") {
		t.Error("the cut goes off with the forwarding")
	}
}
