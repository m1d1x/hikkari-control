package entry

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// ruleBox is a receiver whose rule check prints out and exits by red.
type ruleBox struct {
	attached bool
	out      string
	red      bool
	cmds     []string
}

func (b *ruleBox) Run(_ context.Context, cmd string, stdin io.Reader) (string, error) {
	b.cmds = append(b.cmds, cmd)
	switch {
	case cmd == egressRelaysOnCmd:
		if b.attached {
			return "yes\n", nil
		}
		return "no\n", nil
	case cmd == "bash -s":
		body, _ := io.ReadAll(stdin)
		if string(body) != egressRuleCheckScript {
			return "", errors.New("not the check")
		}
		if b.red {
			return b.out, errors.New("exit status 1")
		}
		return b.out, nil
	}
	return "", nil
}
func (b *ruleBox) HostKey() string { return "" }
func (b *ruleBox) Close() error    { return nil }

// TestTheRuleCheckLooksAtWhatKeepsTheRule: the script checks the guard the
// tunnel's program loads, the relays' uid and capabilities, the probe through
// the tunnel and the ports open to it, and is red by its exit status.
func TestTheRuleCheckLooksAtWhatKeepsTheRule(t *testing.T) {
	s := egressRuleCheckScript
	for _, want := range []string{
		"meta skuid $uid ct direction original fib daddr type != local meta mark set $mark_in",
		`oifname != { \"lo\", \"$ifc\" } counter name \"guard_drop\" drop`,
		"chain guard_in",
		"mark set $mark_wg",
		"lookup 4377",
		"unreachable default",
		"egress.conf",
		"cgroup.procs",
		"CapEff",
		"resolv.conf",
		"--uid=\"$user\"",
		"open through the tunnel",
		`[ "$fails" = 0 ]`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("the check has no %q", want)
		}
	}
	// What it checks the guard for is what the tunnel's program loads.
	for _, want := range []string{
		"meta skuid $uid ct direction original fib daddr type != local meta mark set 0x4377",
		"chain guard_in {",
		`iifname "$ifc" ct state established,related accept`,
		`iifname "$ifc" drop`,
	} {
		if !strings.Contains(egressUpScript, want) {
			t.Fatalf("the tunnel's program has no %q", want)
		}
	}
	// apply reloads the guard on a receiver that is up: an older guard
	// becomes this one without the link going down.
	_, apply, _ := strings.Cut(egressUpScript, "\napply)\n")
	apply, _, _ = strings.Cut(apply, ";;")
	if !strings.Contains(apply, "receiver_guard") {
		t.Fatalf("apply does not reload the guard:\n%s", apply)
	}
	// Users' flows are counted, never named.
	if !strings.Contains(s, `echo "tunnel $net ${comm:-?}"`) {
		t.Fatal("the list of ways out may name a user's destination")
	}
}

func TestTheRuleIsCheckedOnlyWhereRelaysGoThroughTheTunnel(t *testing.T) {
	ctx := context.Background()
	b := &ruleBox{}
	ran, _, _, err := egressRuleCheck(ctx, b)
	if ran || err != nil {
		t.Fatalf("a box without relays in the tunnel: ran=%v err=%v", ran, err)
	}
	b = &ruleBox{attached: true, out: "OK   x\n== fail=0 warn=1\n"}
	ran, sum, fails, err := egressRuleCheck(ctx, b)
	if !ran || err != nil || sum != "fail=0 warn=1" || len(fails) != 0 {
		t.Fatalf("green: ran=%v sum=%q fails=%v err=%v", ran, sum, fails, err)
	}
	b = &ruleBox{attached: true, red: true, out: "FAIL open through the tunnel (no guard_in): tcp/22 udp/443@10.231.0.1 \n" +
		"FAIL the internet sees 198.51.100.10 \x1b[31m\n== fail=2 warn=0\n"}
	_, sum, fails, err = egressRuleCheck(ctx, b)
	if !errors.Is(err, ErrEgressRule) || sum != "fail=2 warn=0" || len(fails) != 2 {
		t.Fatalf("red: sum=%q fails=%v err=%v", sum, fails, err)
	}
	if strings.Contains(strings.Join(fails, "\n"), "10.231.0.1") || strings.Contains(fails[1], "198.51") {
		t.Fatalf("an address in the log: %v", fails)
	}
	// A check cut short is red, not green.
	b = &ruleBox{attached: true, red: true, out: "OK   x\n"}
	if _, _, _, err := egressRuleCheck(ctx, b); !errors.Is(err, ErrEgressRule) {
		t.Fatalf("a check that did not finish: %v", err)
	}
}

// TestARedRuleStopsTheSwapBeforeTheBoxIsTouched: a receiver that breaks the
// rule gets no new build.
func TestARedRuleStopsTheSwapBeforeTheBoxIsTouched(t *testing.T) {
	b := &ruleBox{attached: true, red: true, out: "FAIL guard_post does not drop uid 999 past hk-egress\n== fail=1 warn=0\n"}
	j := &jobRun{ctx: context.Background(), box: "unl9"}
	err := j.swap(boxOpened{r: b, relays: []provRelay{{name: "unl9-1"}}}, EdgeBuild{Arch: "amd64", SHA: strings.Repeat("a", 64)})
	if !errors.Is(err, ErrEgressRule) {
		t.Fatalf("swap on a red receiver: %v", err)
	}
	for _, c := range b.cmds {
		if c != egressRelaysOnCmd && c != "bash -s" {
			t.Fatalf("the box was touched: %q", c)
		}
	}
}
