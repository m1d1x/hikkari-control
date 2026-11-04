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

// hardenRun runs the whole script with sshd, systemctl and ufw stood in for and
// returns the ufw commands in order.
func hardenRun(t *testing.T, sshConnection string, ports ...string) ([]string, error) {
	t.Helper()
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
	stub("sshd", `[ "$1" = -T ] && echo "passwordauthentication no"; exit 0`)
	stub("systemctl", "exit 0")
	stub("ufw", `echo "$*" >> "`+dir+`/ufw.log"`)
	// The script writes the sshd drop-in under /etc: a test machine has none to
	// write, so the path is pointed at the temporary directory.
	script := strings.ReplaceAll(hardenScript, "/etc/ssh/sshd_config.d/", dir+"/")
	cmd := exec.Command(shell, append([]string{"-s", "--"}, ports...)...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	if sshConnection != "" {
		cmd.Env = append(cmd.Env, "SSH_CONNECTION="+sshConnection)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Logf("%s", out)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "ufw.log"))
	var cmds []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			cmds = append(cmds, l)
		}
	}
	return cmds, runErr
}

func TestHardenLetsSSHInFromTheHubAlone(t *testing.T) {
	cmds, err := hardenRun(t, "203.0.113.7 51234 198.51.100.9 22", "443/tcp", "8443/tcp")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"allow from 203.0.113.7 to any port 22 proto tcp",
		"allow 443/tcp",
		"allow 8443/tcp",
		"--force enable",
		"delete allow 22/tcp",
	}
	if strings.Join(cmds, "|") != strings.Join(want, "|") {
		t.Fatalf("ufw:\n%s", strings.Join(cmds, "\n"))
	}
}

// Without knowing the hub the script must stop before it touches the firewall:
// going on would either lock the hub out or leave SSH open and say it is shut.
func TestHardenStopsWhenItCannotNameTheHub(t *testing.T) {
	cmds, err := hardenRun(t, "", "443/tcp")
	if err == nil {
		t.Fatal("hardened with no idea who the hub is")
	}
	if len(cmds) != 0 {
		t.Fatalf("touched the firewall: %v", cmds)
	}
}
