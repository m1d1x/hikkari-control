package entry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestARollbackPutsBackTheHkExitOfTheBuildBefore: the edge and its hk-exit
// speak one socket version (hikkari-protocol trunk/pipectl ExitVersion). On
// 15.11 a canary's rollback put nekko-edge.prev back under the image's new
// hk-exit, the old edge refused it, and the trunk's Rust exit was gone in
// every «before» run. A deploy keeps the running hk-exit as hk-exit.prev; a
// rollback puts it back and does not stage the image's over it.
func TestARollbackPutsBackTheHkExitOfTheBuildBefore(t *testing.T) {
	newExit := []byte("hk-exit v4")
	path := filepath.Join(t.TempDir(), "hk-exit")
	if err := os.WriteFile(path, newExit, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(newExit)
	box := &fakeBox{exit: []byte("hk-exit v3")}
	r := &fakeRemote{b: box}
	j := &jobRun{p: &Provisioner{exitBuilds: map[string]EdgeBuild{"amd64": {Arch: "amd64", SHA: hex.EncodeToString(sum[:]), path: path}}}, ctx: context.Background()}

	if err := j.stageExit(r, "amd64"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(box.exit, newExit) || !bytes.Equal(box.exitPrev, []byte("hk-exit v3")) {
		t.Fatalf("deploy: hk-exit %q, prev %q; want the image's, the old kept", box.exit, box.exitPrev)
	}
	if err := j.exitToPrev(r); err != nil {
		t.Fatal(err)
	}
	j.exitBack = true
	if err := j.stageExit(r, "amd64"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(box.exit, []byte("hk-exit v3")) {
		t.Fatalf("rollback: hk-exit %q, want the build before's", box.exit)
	}
	// The next deploy (a canary's next «after») stages the image's again,
	// the one put back kept for the next rollback.
	j.exitBack = false
	if err := j.stageExit(r, "amd64"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(box.exit, newExit) || !bytes.Equal(box.exitPrev, []byte("hk-exit v3")) {
		t.Fatalf("redeploy: hk-exit %q, prev %q", box.exit, box.exitPrev)
	}
}
