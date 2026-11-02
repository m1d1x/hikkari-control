package downdetector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFleetKDefaultsToK(t *testing.T) {
	load := func(body string) (Config, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "dd.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return LoadConfig(path)
	}
	c, err := load("k: 3\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.FleetMinInstalls() != 3 {
		t.Fatalf("fleet k without fleet_k = %d", c.FleetMinInstalls())
	}
	c, err = load("k: 3\nfleet_k: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.FleetMinInstalls() != 1 || c.K != 3 {
		t.Fatalf("fleet k = %d, k = %d", c.FleetMinInstalls(), c.K)
	}
	if _, err := load("k: 3\nfleet_k: -1\n"); err == nil {
		t.Fatal("negative fleet_k accepted")
	}
}
