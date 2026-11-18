package main

// The pool's members of copies (hikkari-client
// plans/2026-11-15-pool-copy-coalescing.md; hikkari-protocol spec/trunk.md,
// "Copies on the pool"): K members of the pool carry the voice set's copies
// and no client's stream, the copies of each window written at once. The hub
// writes the file beside the wires file (internal/entry/trunkcopies.go,
// `entry-box trunk-copies`):
//
//	{"members": 4, "window_ms": 4, "on_demand_s": 300}
//
// and the brain over hk-pipe gives it to the pipe at its start and within
// wiresEvery of a change, with no new process. No file is none: the copies
// go round every member of the pool, as before.
//
// on_demand_s is the copies on demand (hikkari-client
// plans/2026-11-18-copies-on-demand.md; spec/trunk.md, "Copies on demand"):
// the voice class's copy on the pool goes only while a packet wire loses,
// held this many seconds after a loss. No field, or 0, is a copy of every
// datagram, as before.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

type copiesSpec struct {
	Members   int `json:"members"`
	WindowMS  int `json:"window_ms"`
	OnDemandS int `json:"on_demand_s"`
}

// copiesFile is what the file says: the members of copies and the hold of
// the copies on demand (0: a copy of every datagram).
type copiesFile struct {
	copies pipectl.Copies
	hold   time.Duration
}

// maxOnDemandS is the longest hold the file takes (pipectl MaxDemandHold is
// an hour).
const maxOnDemandS = 3600

// setCopies gives the pipe both.
func setCopies(p *pipectl.Pipe, c copiesFile) error {
	if err := p.SetCopies(c.copies); err != nil {
		return err
	}
	return p.SetDemand(c.hold)
}

// String is the brain's log of it.
func (c copiesFile) String() string {
	s := fmt.Sprintf("copies on %d members of the pool, a window of %v", c.copies.Members, c.copies.Window)
	if c.hold == 0 {
		return s + ", a copy of every datagram"
	}
	return s + fmt.Sprintf(", on demand held %v", c.hold)
}

// copiesFilePath is the wires file's sibling: …wires.json → …copies.json.
func copiesFilePath() string {
	return strings.TrimSuffix(wiresFilePath(), ".wires.json") + ".copies.json"
}

// readCopies reads the file; a missing one is no members of copies and a
// copy of every datagram.
func readCopies(path string) (copiesFile, []byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return copiesFile{}, nil, nil
	}
	if err != nil {
		return copiesFile{}, nil, err
	}
	var f copiesSpec
	if err := json.Unmarshal(raw, &f); err != nil {
		return copiesFile{}, raw, fmt.Errorf("%s: %w", path, err)
	}
	if f.Members < 0 || f.Members > 32 || f.WindowMS < 0 || f.WindowMS > 50 || f.OnDemandS < 0 || f.OnDemandS > maxOnDemandS {
		return copiesFile{}, raw, fmt.Errorf("%s: members %d (0-32), window_ms %d (0-50), on_demand_s %d (0-%d)",
			path, f.Members, f.WindowMS, f.OnDemandS, maxOnDemandS)
	}
	return copiesFile{
		copies: pipectl.Copies{Members: f.Members, Window: time.Duration(f.WindowMS) * time.Millisecond},
		hold:   time.Duration(f.OnDemandS) * time.Second,
	}, raw, nil
}
