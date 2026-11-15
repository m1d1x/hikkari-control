package main

// The pool's members of copies (hikkari-client
// plans/2026-11-15-pool-copy-coalescing.md; hikkari-protocol spec/trunk.md,
// "Copies on the pool"): K members of the pool carry the voice set's copies
// and no client's stream, the copies of each window written at once. The hub
// writes the file beside the wires file (internal/entry/trunkcopies.go,
// `entry-box trunk-copies`):
//
//	{"members": 4, "window_ms": 4}
//
// and the brain over hk-pipe gives it to the pipe at its start and within
// wiresEvery of a change, with no new process. No file is none: the copies
// go round every member of the pool, as before.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

type copiesSpec struct {
	Members  int `json:"members"`
	WindowMS int `json:"window_ms"`
}

// copiesFilePath is the wires file's sibling: …wires.json → …copies.json.
func copiesFilePath() string {
	return strings.TrimSuffix(wiresFilePath(), ".wires.json") + ".copies.json"
}

// readCopies reads the file; a missing one is no members of copies.
func readCopies(path string) (pipectl.Copies, []byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return pipectl.Copies{}, nil, nil
	}
	if err != nil {
		return pipectl.Copies{}, nil, err
	}
	var f copiesSpec
	if err := json.Unmarshal(raw, &f); err != nil {
		return pipectl.Copies{}, raw, fmt.Errorf("%s: %w", path, err)
	}
	if f.Members < 0 || f.Members > 32 || f.WindowMS < 0 || f.WindowMS > 50 {
		return pipectl.Copies{}, raw, fmt.Errorf("%s: members %d (0-32), window_ms %d (0-50)", path, f.Members, f.WindowMS)
	}
	return pipectl.Copies{Members: f.Members, Window: time.Duration(f.WindowMS) * time.Millisecond}, raw, nil
}
