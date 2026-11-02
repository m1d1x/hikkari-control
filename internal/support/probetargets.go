package support

// The addresses of a probe_targets command. A tester's machine will open TCP
// connections to them, so what may be named is narrow: a few public IPv4
// addresses and a label that stands in for the address in the log, which leaves
// the client with addresses stripped. The client checks the same rules again.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
)

// MaxProbeTargets bounds one command: a round of them has to fit the client's
// two minutes.
const MaxProbeTargets = 16

// ProbeTarget is one address to count flows to.
type ProbeTarget struct {
	Label string `json:"label"`
	Addr  string `json:"addr"`
}

var probeLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// notPublic is what a global unicast check lets through and a probe must not
// reach: carrier NAT and the benchmarking range sing-box uses for fake IPs.
var notPublic = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("198.18.0.0/15")}

func validateProbeTargets(targets []ProbeTarget) error {
	if len(targets) == 0 {
		return errors.New("probe_targets needs targets")
	}
	if len(targets) > MaxProbeTargets {
		return fmt.Errorf("at most %d targets", MaxProbeTargets)
	}
	for _, t := range targets {
		if !probeLabel.MatchString(t.Label) {
			return fmt.Errorf("label %q: lowercase letters, digits and dashes", t.Label)
		}
		addr, err := netip.ParseAddrPort(t.Addr)
		if err != nil || !addr.Addr().Is4() || addr.Port() == 0 {
			return fmt.Errorf("target %s: want ipv4:port", t.Label)
		}
		ip := addr.Addr()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return fmt.Errorf("target %s: not a public address", t.Label)
		}
		for _, p := range notPublic {
			if p.Contains(ip) {
				return fmt.Errorf("target %s: not a public address", t.Label)
			}
		}
	}
	return nil
}

// commandParams checks and encodes what a kind carries.
func commandParams(kind string, targets []ProbeTarget) (json.RawMessage, error) {
	if kind != TesterCommandProbe {
		if len(targets) > 0 {
			return nil, fmt.Errorf("%s takes no targets", kind)
		}
		return nil, nil
	}
	if err := validateProbeTargets(targets); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"targets": targets})
}

// nullableJSON keeps "no params" a NULL, which IS NOT DISTINCT FROM can match.
func nullableJSON(params json.RawMessage) any {
	if len(params) == 0 {
		return nil
	}
	return string(params)
}
