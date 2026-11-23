package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheStreamWindowFileIsBesideTheWiresFileAndThePipesOwnWithout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EDGE_TRUNK_WIRES_FILE", filepath.Join(dir, "nekko-trunk-pipe.env.wires.json"))
	path := streamWindowFilePath()
	if path != filepath.Join(dir, "nekko-trunk-pipe.env.window.json") {
		t.Fatalf("window file %s", path)
	}
	if w, raw, err := readStreamWindow(path); err != nil || raw != nil || w != 0 {
		t.Fatalf("no file: %d %q %v, want the pipe's own", w, raw, err)
	}
	for body, want := range map[string]int{`{"stream_window_mib": 16}`: 16 << 20, `{"stream_window_mib": 2}`: 2 << 20, `{"stream_window_mib": 64}`: 64 << 20} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if w, _, err := readStreamWindow(path); err != nil || w != want {
			t.Fatalf("%s: %d %v, want %d", body, w, err, want)
		}
	}
	for _, body := range []string{`{"stream_window_mib": 1}`, `{"stream_window_mib": 65}`, `{"stream_window_mib": 17592186044418}`, `{`} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readStreamWindow(path); err == nil {
			t.Fatalf("%s read", body)
		}
	}
}
