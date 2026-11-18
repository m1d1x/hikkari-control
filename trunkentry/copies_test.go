package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

func TestTheCopiesFileIsBesideTheWiresFileAndNoneWithout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDGE_TRUNK_WIRES_FILE", filepath.Join(dir, "nekko-trunk-pipe.env.wires.json"))
	path := copiesFilePath()
	if path != filepath.Join(dir, "nekko-trunk-pipe.env.copies.json") {
		t.Fatalf("copies file %s", path)
	}
	if c, raw, err := readCopies(path); err != nil || raw != nil || c != (copiesFile{}) {
		t.Fatalf("no file: %+v %q %v, want none", c, raw, err)
	}
	if err := os.WriteFile(path, []byte(`{"members": 4, "window_ms": 4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, _, err := readCopies(path); err != nil || c != (copiesFile{copies: pipectl.Copies{Members: 4, Window: 4 * time.Millisecond}}) {
		t.Fatalf("4 members, 4 ms, a copy of every datagram: %+v %v", c, err)
	}
	if err := os.WriteFile(path, []byte(`{"members": 40, "window_ms": 4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCopies(path); err == nil {
		t.Fatal("40 members read")
	}
}

func TestTheCopiesFileHoldsTheCopiesOnDemand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.copies.json")
	if err := os.WriteFile(path, []byte(`{"members":0,"window_ms":0,"on_demand_s":300}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, _, err := readCopies(path); err != nil || c != (copiesFile{hold: 5 * time.Minute}) {
		t.Fatalf("on demand 300 s: %+v %v", c, err)
	}
	for _, bad := range []string{`{"on_demand_s":-1}`, `{"on_demand_s":3601}`} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readCopies(path); err == nil {
			t.Errorf("%s read", bad)
		}
	}
}
