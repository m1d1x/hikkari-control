package entry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	script = strings.ReplaceAll(script, "/etc/default/ufw", dir+"/ufw-default")
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

// ufw writes IPv6 rules only with IPV6=yes, so an image that ships it off would
// leave the machine's IPv6 address unfiltered behind a firewall that looks shut.
func TestHardenTurnsOnUfwForIPv6BeforeTheFirstRule(t *testing.T) {
	if strings.Index(hardenScript, "echo IPV6=yes") > strings.Index(hardenScript, "ufw allow") {
		t.Fatal("rules are written before ufw is told to filter IPv6")
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	file := filepath.Join(t.TempDir(), "ufw")
	if err := os.WriteFile(file, []byte("IPV6=no\nDEFAULT_INPUT_POLICY=\"DROP\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, "-s")
	cmd.Stdin = strings.NewReader("set -eu\n" + strings.ReplaceAll(ufwFiltersIPv6Script, "/etc/default/ufw", file))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "DEFAULT_INPUT_POLICY=\"DROP\"\nIPV6=yes\n" {
		t.Fatalf("/etc/default/ufw: %q", got)
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

type fakeIPv6Hoster struct {
	ip        BoxIPv6
	allocated bool
	orders    int
	readsLeft int
}

func (f *fakeIPv6Hoster) OrderIPv6(context.Context, string) error {
	f.orders++
	f.readsLeft = 1
	return nil
}

func (f *fakeIPv6Hoster) InstanceIPv6(context.Context, string) (BoxIPv6, bool, error) {
	if f.allocated {
		return f.ip, true, nil
	}
	if f.orders > 0 {
		if f.readsLeft == 0 {
			f.allocated = true
		}
		f.readsLeft--
	}
	return BoxIPv6{}, false, nil
}

// memIPv6Orders is the ledger without a database; the real one is tested with the
// rollout harness.
type memIPv6Orders struct {
	since time.Time
}

func (m *memIPv6Orders) claim(context.Context) (time.Time, bool, error) {
	if !m.since.IsZero() {
		return m.since, false, nil
	}
	m.since = time.Now()
	return m.since, true, nil
}

func (m *memIPv6Orders) clear(context.Context) error {
	m.since = time.Time{}
	return nil
}

// Running a job again on a machine the hub already runs must not add to the bill.
func TestOnlyAFreshlyOrderedMachineBuysIPv6(t *testing.T) {
	ctx := context.Background()
	f := &fakeIPv6Hoster{ip: BoxIPv6{Address: "2001:db8::1", Prefix: 64, Gateway: "fe80::1"}}
	orders := &memIPv6Orders{}
	if _, ok, err := ensureBoxIPv6(ctx, f, orders, "1", false, time.Second, time.Millisecond); err != nil || ok || f.orders != 0 {
		t.Fatalf("a running machine: ok=%v err=%v orders=%d", ok, err, f.orders)
	}
	got, ok, err := ensureBoxIPv6(ctx, f, orders, "1", true, time.Second, time.Millisecond)
	if err != nil || !ok || got != f.ip || f.orders != 1 {
		t.Fatalf("a fresh machine: %+v ok=%v err=%v orders=%d", got, ok, err, f.orders)
	}
	if !orders.since.IsZero() {
		t.Fatal("the record outlived the address")
	}
	// The machine already has its address: a rerun of the job configures it again.
	if _, ok, err := ensureBoxIPv6(ctx, f, orders, "1", true, time.Second, time.Millisecond); err != nil || !ok || f.orders != 1 {
		t.Fatalf("a rerun: ok=%v err=%v orders=%d", ok, err, f.orders)
	}
}

// The address has not come in time: the second run waits for the order it has,
// and buys nothing, until the address comes or the operator drops the record.
func TestASecondRunWaitsForTheOrderInsteadOfBuyingAnother(t *testing.T) {
	ctx := context.Background()
	stuck := &stuckIPv6Hoster{}
	orders := &memIPv6Orders{}
	for run := 1; run <= 2; run++ {
		_, ok, err := ensureBoxIPv6(ctx, stuck, orders, "1", true, 5*time.Millisecond, time.Millisecond)
		if err == nil || ok || !strings.Contains(err.Error(), "forget-order") {
			t.Fatalf("run %d: ok=%v err=%v", run, ok, err)
		}
		if stuck.orders != 1 {
			t.Fatalf("run %d: %d orders", run, stuck.orders)
		}
	}
	f := &fakeIPv6Hoster{ip: BoxIPv6{Address: "2001:db8::1", Prefix: 64, Gateway: "fe80::1"}, allocated: true}
	if _, ok, err := ensureBoxIPv6(ctx, f, orders, "1", true, time.Second, time.Millisecond); err != nil || !ok || f.orders != 0 {
		t.Fatalf("the address came: ok=%v err=%v orders=%d", ok, err, f.orders)
	}
	if !orders.since.IsZero() {
		t.Fatal("the record outlived the address")
	}
}

// A refusal means nothing was bought, so the next run may order. Any other
// failure may have bought it, so the next run may not.
func TestAFailedOrderKeepsItsRecordUnlessTheHosterRefused(t *testing.T) {
	ctx := context.Background()
	refused := &stuckIPv6Hoster{orderErr: fmt.Errorf("%w: no money", ErrProviderRefused)}
	orders := &memIPv6Orders{}
	if _, _, err := ensureBoxIPv6(ctx, refused, orders, "1", true, time.Millisecond, time.Millisecond); !errors.Is(err, ErrProviderRefused) {
		t.Fatalf("refused: %v", err)
	}
	if !orders.since.IsZero() {
		t.Fatal("a refusal left a record")
	}
	reset := &stuckIPv6Hoster{orderErr: errors.New("connection reset")}
	if _, _, err := ensureBoxIPv6(ctx, reset, orders, "1", true, time.Millisecond, time.Millisecond); err == nil {
		t.Fatal("a reset passed")
	}
	if orders.since.IsZero() {
		t.Fatal("a reset dropped the record")
	}
	if _, _, err := ensureBoxIPv6(ctx, reset, orders, "1", true, time.Millisecond, time.Millisecond); err == nil || reset.orders != 1 {
		t.Fatalf("after a reset: err=%v orders=%d", err, reset.orders)
	}
}

type stuckIPv6Hoster struct {
	orders   int
	orderErr error
}

func (s *stuckIPv6Hoster) OrderIPv6(context.Context, string) error {
	s.orders++
	return s.orderErr
}
func (*stuckIPv6Hoster) InstanceIPv6(context.Context, string) (BoxIPv6, bool, error) {
	return BoxIPv6{}, false, nil
}

func ipv6ScriptRun(t *testing.T, ip6Routes string) (string, string) {
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
	stub("ip", `case "$1" in
-6) echo "`+ip6Routes+`" ;;
-4) echo "default via 146.103.121.1 dev ens3 proto static" ;;
esac`)
	stub("netplan", `echo "$*" >> "`+dir+`/netplan.log"`)
	stub("curl", "exit 0")
	script := strings.ReplaceAll(ipv6Script, "/etc/netplan/", dir+"/")
	cmd := exec.Command(shell, "-s", "--", "2001:db8::1", "64", "fe80::1")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	yaml, _ := os.ReadFile(filepath.Join(dir, "60-nekko-ipv6.yaml"))
	return string(out), string(yaml)
}

func TestTheIPv6ScriptWritesTheAddressOnTheDefaultInterface(t *testing.T) {
	out, yaml := ipv6ScriptRun(t, "")
	for _, want := range []string{"ens3:", `["2001:db8::1/64"]`, `via: "fe80::1"`, "on-link: true"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("netplan misses %q:\n%s", want, yaml)
		}
	}
	if !strings.Contains(out, "доступен") {
		t.Errorf("output: %s", out)
	}
}

func TestTheIPv6ScriptLeavesAMachineThatRoutesIPv6Alone(t *testing.T) {
	out, yaml := ipv6ScriptRun(t, "default via fe80::1 dev ens3")
	if yaml != "" || !strings.Contains(out, "уже есть") {
		t.Fatalf("output %q, netplan %q", out, yaml)
	}
}
