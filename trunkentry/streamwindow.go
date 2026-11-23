package main

// The window the pipe gives every stream it receives (hikkari-protocol
// spec/trunk.md, "Streams"; hikkari-client
// plans/research/2026-11-23-cascade-speed-bottleneck.md): a person's whole
// session through the entry is one stream, and its speed down is at most the
// window over the time a chunk takes to be confirmed. The hub writes the file
// beside the wires file (internal/entry/trunkwindow.go,
// `entry-box trunk-stream-window`):
//
//	{"stream_window_mib": 16}
//
// and the brain gives it to the pipe at its start and within wiresEvery of a
// change, with no new process; the streams open keep theirs. No file is the
// pipe's own 2 MiB.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

type streamWindowSpec struct {
	MiB int `json:"stream_window_mib"`
}

// streamWindowFilePath is the wires file's sibling: …wires.json →
// …window.json.
func streamWindowFilePath() string {
	return strings.TrimSuffix(wiresFilePath(), ".wires.json") + ".window.json"
}

// readStreamWindow reads the file: the window in bytes, 0 for no file.
func readStreamWindow(path string) (int, []byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	var f streamWindowSpec
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, raw, fmt.Errorf("%s: %w", path, err)
	}
	const minMiB, maxMiB = pipectl.MinStreamWindow >> 20, pipectl.MaxStreamWindow >> 20
	if f.MiB < minMiB || f.MiB > maxMiB {
		return 0, raw, fmt.Errorf("%s: stream_window_mib %d (%d-%d)", path, f.MiB, minMiB, maxMiB)
	}
	return f.MiB << 20, raw, nil
}

// setStreamWindow gives the pipe the window; 0 is the pipe's own.
func setStreamWindow(p *pipectl.Pipe, b int) error {
	if b == 0 {
		b = pipectl.DefaultStreamWindow
	}
	return p.SetStreamWindow(b)
}

// streamWindowString is the brain's log of it.
func streamWindowString(b int) string {
	if b == 0 {
		return "2 MiB (the pipe's own)"
	}
	return fmt.Sprintf("%d MiB", b>>20)
}
