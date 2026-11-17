package entry

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The test of the receiver's rule (H6 of hikkari-client
// plans/2026-11-16-hidden-border-receiver-and-egress.md): on a receiver whose
// relays go out through the egress, the relays cannot send a packet past the
// tunnel, nothing on the box answers through the tunnel, and the box's own
// ways out are listed. egress_rule_check.sh is the check itself, the same file
// an operator runs by hand.
//
// It runs before and after every swap of a relay on such a box: red before,
// the rollout does not touch the box; red after, the swap is rolled back. A
// box whose relays are not in the tunnel has no rule to keep and is skipped.

//go:embed egress_rule_check.sh
var egressRuleCheckScript string

// ErrEgressRule is a receiver that broke the rule of its egress.
var ErrEgressRule = errors.New("приёмник нарушает правило выхода")

// egressRuleLine is a FAIL line of the check as the hub may repeat it: the
// script's own words, no more than one line of them.
var (
	egressRuleLineRe = regexp.MustCompile(`^FAIL [A-Za-z0-9 @/:._,'()=<>{}"$-]{1,300}$`)
	egressRuleAddrRe = regexp.MustCompile(`\b(\d{1,3}\.){3}\d{1,3}\b|\b[0-9a-fA-F]{1,4}(:[0-9a-fA-F]{0,4}){2,7}\b`)
	egressRuleSumRe  = regexp.MustCompile(`(?m)^== fail=(\d+) warn=(\d+)$`)
)

// egressRuleFails are the check's FAIL lines fit for a log: anything else the
// box printed is left out, addresses are masked.
func egressRuleFails(out string) []string {
	var fails []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "FAIL ") {
			continue
		}
		if !egressRuleLineRe.MatchString(line) {
			fails = append(fails, "FAIL "+remoteWord(strings.TrimPrefix(line, "FAIL ")))
			continue
		}
		fails = append(fails, egressRuleAddrRe.ReplaceAllString(line, "<адрес>"))
	}
	return fails
}

// egressRuleCheck runs the check on a box. ran is false on a box whose relays
// are not in the tunnel; err wraps ErrEgressRule when the check is red or did
// not finish.
func egressRuleCheck(ctx context.Context, r Remote) (ran bool, summary string, fails []string, err error) {
	attached, err := egressRelaysAttached(ctx, r)
	if err != nil {
		return false, "", nil, fmt.Errorf("релеи в туннеле: %w", err)
	}
	if !attached {
		return false, "", nil, nil
	}
	out, runErr := r.Run(ctx, "bash -s", strings.NewReader(egressRuleCheckScript))
	fails = egressRuleFails(out)
	m := egressRuleSumRe.FindStringSubmatch(out)
	if m != nil {
		summary = "fail=" + m[1] + " warn=" + m[2]
	}
	switch {
	case runErr == nil && m != nil && m[1] == "0":
		return true, summary, nil, nil
	case m == nil:
		return true, "", fails, fmt.Errorf("%w: проверка не дошла до конца: %v", ErrEgressRule, runErr)
	default:
		return true, summary, fails, fmt.Errorf("%w: %s", ErrEgressRule, summary)
	}
}

// egressRule is the check as a step of a job: logged, and an error that
// stops the job when red.
func (j *jobRun) egressRule(r Remote, when string) error {
	j.step("egress-rule " + when)
	ran, summary, fails, err := egressRuleCheck(j.ctx, r)
	if !ran && err == nil {
		return nil
	}
	for _, f := range fails {
		j.log("правило выхода %s: %s", when, f)
	}
	if err != nil {
		return err
	}
	j.log("правило выхода %s: %s", when, summary)
	return nil
}

// checkEgressRule is the check in egress-check and egress-relays: the
// receiver's side once its relays are in the tunnel.
func checkEgressRule(ctx context.Context, e *egressEnds) error {
	ran, summary, fails, err := egressRuleCheck(ctx, e.rx)
	for _, f := range fails {
		e.rxLog("правило выхода: %s", f)
	}
	if err != nil {
		return err
	}
	if ran {
		e.rxLog("правило выхода: %s", summary)
	}
	return nil
}
