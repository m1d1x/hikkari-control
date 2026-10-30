package downdetector

import "testing"

func TestDecide(t *testing.T) {
	const k = 2
	var (
		none  = Group{}
		alive = Group{Installs: 3, OK: 28, Fail: 2}
		dead  = Group{Installs: 3, OK: 1, Fail: 29}
		mixed = Group{Installs: 3, OK: 15, Fail: 15}
		// Enough failures, too few installs: not a dead group.
		lonelyDead  = Group{Installs: 1, OK: 0, Fail: 30}
		lonelyAlive = Group{Installs: 1, OK: 30, Fail: 0}
	)
	cases := []struct {
		name                  string
		ext                   External
		same, country, abroad Group
		want                  Verdict
	}{
		{"external down wins over everything", ExternalDown, alive, alive, alive, VerdictServerDown},
		{"external down, no data", ExternalDown, none, none, none, VerdictServerDown},
		{"unknown external, every group with data dead", ExternalUnknown, dead, none, dead, VerdictServerDown},
		{"unknown external, only same asn dead", ExternalUnknown, dead, none, none, VerdictServerDown},
		{"unknown external, dead + lonely group is not all dead", ExternalUnknown, dead, lonelyDead, none, VerdictUnknown},
		{"external up, all dead is not server_down", ExternalUp, dead, dead, dead, VerdictUnknown},
		{"country block, country dead", ExternalUp, dead, dead, alive, VerdictCountryBlock},
		{"country block, country empty", ExternalUp, dead, none, alive, VerdictCountryBlock},
		{"country block with unknown external", ExternalUnknown, dead, dead, alive, VerdictCountryBlock},
		{"isp block", ExternalUp, dead, alive, none, VerdictISPBlock},
		{"isp block, abroad alive too", ExternalUp, dead, alive, alive, VerdictISPBlock},
		{"isp block with unknown external", ExternalUnknown, dead, alive, dead, VerdictISPBlock},
		{"local: own asn works", ExternalUp, alive, none, none, VerdictLocal},
		{"local even if country is dead", ExternalUp, alive, dead, dead, VerdictLocal},
		{"same asn mixed", ExternalUp, mixed, alive, alive, VerdictUnknown},
		{"same asn dead, country mixed", ExternalUp, dead, mixed, alive, VerdictUnknown},
		{"same asn dead, abroad not alive", ExternalUp, dead, none, mixed, VerdictUnknown},
		{"too few installs to say dead", ExternalUp, lonelyDead, alive, alive, VerdictUnknown},
		{"too few installs to say alive", ExternalUp, lonelyAlive, none, none, VerdictUnknown},
		{"no data at all", ExternalUp, none, none, none, VerdictUnknown},
		{"no data, external unknown", ExternalUnknown, none, none, none, VerdictUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.ext, c.same, c.country, c.abroad, k); got != c.want {
				t.Fatalf("Decide = %s, want %s", got, c.want)
			}
		})
	}
}

func TestGroupThresholdsAtExactly70Percent(t *testing.T) {
	g := Group{Installs: 2, OK: 7, Fail: 3}
	if !g.alive(2) || g.dead(2) {
		t.Fatal("7/10 ok must be alive and not dead")
	}
	g = Group{Installs: 2, OK: 3, Fail: 7}
	if !g.dead(2) || g.alive(2) {
		t.Fatal("7/10 fail must be dead and not alive")
	}
}
