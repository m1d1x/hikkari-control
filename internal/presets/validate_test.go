package presets

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/rule-parity.json comes from the client's own normalizer
// (hikkari-client engine/core/routing parity_test.go). Every rule must be
// judged and canonicalized the same, or the server could release what the
// client rejects.
func TestRulesAreJudgedAsTheClientJudgesThem(t *testing.T) {
	raw, err := os.ReadFile("testdata/rule-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Category, Value, Normal string
		OK                      bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("only %d parity cases", len(cases))
	}
	for _, c := range cases {
		normal, err := NormalizeRule(c.Category, c.Value)
		if (err == nil) != c.OK || normal != c.Normal {
			t.Errorf("%s %q: server %q/%v, client %q/%v", c.Category, c.Value, normal, err, c.Normal, c.OK)
		}
		if err := CanonicalRule(c.Category, c.Value); (err == nil) != (c.OK && c.Value == c.Normal) {
			t.Errorf("%s %q: canonical check disagrees: %v", c.Category, c.Value, err)
		}
	}
}

// The client's bundled catalog is the seed the admin starts from; it must
// pass as it is.
func TestTheClientsBundledCatalogPasses(t *testing.T) {
	raw, err := os.ReadFile("testdata/client-defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc catalogDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Services) == 0 {
		t.Fatal("no services in the bundled catalog")
	}
	if err := ValidateCatalog(doc.Services); err != nil {
		t.Fatal(err)
	}
}

func TestAGroupTheClientWouldRejectIsRefused(t *testing.T) {
	ok := steam()
	if err := ValidateGroup(ok); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Group){
		"non-canonical domain": func(g *Group) { g.Rules.Domains = []string{"Steampowered.com"} },
		"duplicate":            func(g *Group) { g.Rules.Domains = []string{"a.com", "a.com"} },
		"unknown platform":     func(g *Group) { g.Rules.Processes = map[string][]string{"android": {"x"}} },
		"bare folder":          func(g *Group) { g.Rules.Folders = map[string][]string{"windows": {"steam"}} },
		"empty":                func(g *Group) { g.Rules = Rules{} },
		"bad id":               func(g *Group) { g.ID = "Steam" },
		"bad icon":             func(g *Group) { g.Icon = "steam.png" },
		"blank name":           func(g *Group) { g.Name = "  " },
	} {
		g := steam()
		mutate(&g)
		if err := ValidateGroup(g); err == nil {
			t.Errorf("%s passed", name)
		}
	}
	if err := ValidateCatalog([]Service{{ID: "a", Name: "a", Domains: []string{"a.com"}, DefaultModes: []string{"direct"}}}); err == nil || !strings.Contains(err.Error(), "режим") {
		t.Errorf("unknown mode: %v", err)
	}
}
