package entry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A freshly booted machine shares apt with PackageKit, which apt-get update
// itself wakes: update can find the lists locked, and install the dpkg lock.
// The fake apt-get answers like the real one (exit 100), failing an update
// once and any install not told to wait for the lock.
func TestInstallingUfwOutlastsAptLocksOnAFreshMachine(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	stub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("sleep", "exit 0")
	stub("apt-get", `D="`+dir+`"
case " $* " in
*" update "*)
	[ -e "$D/update-failed" ] || { : >"$D/update-failed"; exit 100; }
	;;
*" install "*)
	case " $* " in
	*" DPkg::Lock::Timeout="*) ;;
	*) exit 100 ;;
	esac
	: >"$D/installed"
	;;
esac`)

	cmd := exec.Command(shell, "-s")
	cmd.Stdin = strings.NewReader("set -eu\n" + installUfwScript)
	cmd.Env = []string{"PATH=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script gave up on a lock that would have been released: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "installed")); err != nil {
		t.Fatal("ufw was not installed")
	}
}
