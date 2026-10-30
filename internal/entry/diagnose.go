package entry

// diagnose.go turns what the vantages saw into one cause per box (roadmap 42
// П9.4/П9.5).
//
// # Why this is here and not in the console
//
// It used to live in a Svelte component (`censor/+page.svelte`, boxNote): ten
// lines of if-else that decided "the whole IP is banned" from "the domain is
// cut" from "the protocol was spotted". That made the diagnosis real for one
// pair of human eyes and for nothing else — no playbook could act on it, no
// incident could carry it, and nothing could be tested. A cause has to be a
// value the hub computes before it can be the thing an autopilot acts on.
//
// # What a cause is, and is not
//
// It is a reading of evidence, not a decision. Deciding what to do about a
// burnt domain is a playbook's job (П9.5); deciding that a relay is dead is
// the operator's or the pool's (П6.10). This file only says what the evidence
// most likely means, and hands back the evidence with it, so whoever acts can
// show their work.
//
// # The unit that fails is the address
//
// A censor bans an address, so the box — not the relay — is what a cause is
// about. "Every relay on this machine is banned" means the machine; "one of
// them is" means those ports. That asymmetry is the whole reason the grid is
// read per box and not per relay.

import "sort"

// Causes a box's evidence can add up to. They are ordered by how specific the
// remedy is, not by severity: a cause that names one thing to change is worth
// more than one that says the machine is gone.
const (
	// CauseHealthy: every relay the vantage could judge answered.
	CauseHealthy = "healthy"
	// CauseBlind: the vantage itself had no network, so the box is not being
	// judged at all. Deliberately not a failure — the one reading that must
	// never be mistaken for one.
	CauseBlind = "blind"
	// CauseDomainBlocked: the address answers, the name does not. Cured by a
	// new domain; the machine is innocent.
	CauseDomainBlocked = "domain_blocked"
	// CauseFingerprint: the address answers and the protocol was spotted.
	// Cured by changing the protocol's parameters; the machine is innocent.
	CauseFingerprint = "fingerprint"
	// CausePortsBanned: some of the box's ports are gone, others answer. The
	// address lives; those relays do not.
	CausePortsBanned = "ports_banned"
	// CauseIPBanned: nothing on the machine answers. A new address, which
	// means a new machine or a new IP from the provider.
	CauseIPBanned = "ip_banned"
	// CauseUnknown: there is evidence, and it does not add up to any of the
	// above. Said plainly rather than guessed at.
	CauseUnknown = "unknown"

	// The three the escalated grid adds (П9.4). They cannot be read from the
	// RU round at all: from Russia a box whose process died, a box the
	// provider lost and a box whose cover site is down all look the same, and
	// their repairs are a restart, a reboot and a new cover.

	// CauseEdgeDown: the machine answers the hub's key, and the relay's unit
	// is not running. The cheapest repair there is — and the one most easily
	// mistaken for a banned address.
	CauseEdgeDown = "edge_down"
	// CauseProviderDown: the machine answers neither Russia, nor the hub's
	// handshake, nor its ssh. Not the censor: the machine.
	CauseProviderDown = "provider_down"
	// CauseDestination: the machine and the process are fine and the site the
	// tunnel borrows is dead, so the handshake has nothing to hide behind.
	// Cured by another cover; nothing is done to the box.
	CauseDestination = "destination"
)

// Diagnosis is one box's reading, with the evidence that produced it.
//
// Evidence travels with the cause on purpose: a console that shows a verdict
// without it teaches nobody, and a playbook that acts on one has to be able
// to say what it acted on.
type Diagnosis struct {
	Box   string
	Cause string
	// Advice is the one sentence the console shows. It names what to change,
	// not what happened — a reader who wanted the latter has the cause.
	Advice string
	// Judged is how many of the box's alive relays the vantage could judge;
	// Alive is how many it has. Judged < Alive means the reading is partial,
	// which the console must be able to say.
	Judged int
	Alive  int
	// Verdicts is each judged relay's verdict, by relay name.
	Verdicts map[string]string
}

// Partial says the box was judged on less than all of its live relays.
func (d Diagnosis) Partial() bool { return d.Judged < d.Alive }

// RelayVerdicts is what one vantage last saw, by relay name. The caller reads
// it from the probe dataset; this file does no I/O, so the rules can be
// tested without one.
type RelayVerdicts map[string]string

// DiagnoseBox reads one box with the default invariant (IgnoreNetworkDown = true).
func DiagnoseBox(box string, relays []PoolRelay, verdicts RelayVerdicts) Diagnosis {
	return DiagnoseBoxWithPolicy(box, relays, verdicts, DetectionPolicy{IgnoreNetworkDown: true})
}

// DiagnoseBoxWithPolicy reads one box under the given DetectionPolicy. relays
// are the box's relays as the pool holds them, verdicts the merged latest
// verdict per relay.
//
// The order of the rules is the diagnosis. Each one is tried only after the
// ones above it failed, because a more specific remedy is always the better
// answer: a box with one blocked domain and one spotted protocol wants a new
// domain first, and nothing about that is a judgement on the machine.
func DiagnoseBoxWithPolicy(box string, relays []PoolRelay, verdicts RelayVerdicts, policy DetectionPolicy) Diagnosis {
	d := Diagnosis{Box: box, Cause: CauseUnknown, Verdicts: map[string]string{}}

	seen := make([]string, 0, len(relays))
	for _, r := range relays {
		if r.Status != StatusAlive {
			continue
		}
		d.Alive++
		v := verdicts[r.Name]
		// "unknown" is the absence of evidence, not evidence of absence: a
		// relay nobody has probed yet must not drag a box towards a verdict.
		if v == "" || v == "unknown" {
			continue
		}
		d.Judged++
		d.Verdicts[r.Name] = v
		seen = append(seen, v)
	}
	sort.Strings(seen)

	if d.Judged == 0 {
		d.Cause = CauseUnknown
		d.Advice = "по этой машине пока нечего читать: пробник её не судил"
		return d
	}

	every := func(v string) bool {
		for _, s := range seen {
			if s != v {
				return false
			}
		}
		return true
	}
	any := func(v string) bool {
		for _, s := range seen {
			if s == v {
				return true
			}
		}
		return false
	}

	if policy.IgnoreNetworkDown {
		// The vantage's own path, first and alone: a probe that never left the
		// building says nothing about the box, and reading it as a failure would
		// retire healthy machines on the strength of our own outage.
		if every("network_down") {
			d.Cause = CauseBlind
			d.Advice = "пробник сам без сети, по машине ничего не сказать"
			return d
		}
		// When some ports answered and another hit a local network glitch on
		// the vantage, drop the network_down readings so they do not poison
		// the remaining ports' verdict.
		filtered := seen[:0]
		for _, s := range seen {
			if s != "network_down" {
				filtered = append(filtered, s)
			}
		}
		seen = filtered
	}

	switch {
	// The name before the address, and the protocol before the machine: both
	// are cured without touching the box, so both outrank anything that says
	// the box has to go.
	case any("domain_blocked"):
		d.Cause = CauseDomainBlocked
		d.Advice = "домен заблокирован, адрес жив: новый домен, машину не трогать"
	case any("fingerprint"):
		d.Cause = CauseFingerprint
		d.Advice = "протокол спалили, адрес жив: чинить протокол, машину не трогать"

	// Only once nothing cheaper fits does the address come into question, and
	// only when every judged relay is gone does it become the machine's.
	case every("ip_banned"):
		d.Cause = CauseIPBanned
		d.Advice = "ip машины забанен целиком: сменить ip у провайдера или сдать машину"
	case any("ip_banned"):
		d.Cause = CausePortsBanned
		d.Advice = "часть портов не отвечает, остальные живы"

	case every("clean"), every("reality_wins"):
		d.Cause = CauseHealthy
		d.Advice = "все релеи машины живы"
	default:
		// Mixed clean and reality_wins is healthy too; anything else genuinely
		// is not understood, and says so.
		if onlyHealthy(seen) {
			d.Cause = CauseHealthy
			d.Advice = "все релеи машины живы"
		} else {
			d.Cause = CauseUnknown
			d.Advice = "вердикты не складываются в причину, смотреть руками"
		}
	}
	return d
}

// MergeVantageVerdicts reduces per-vantage readings (`region -> relay -> verdict`)
// into one RelayVerdicts map according to the detection policy (roadmap 42 К3 / К3′).
//
// Rules:
//  1. When policy.IgnoreNetworkDown is true, a vantage reporting "network_down"
//     for a relay is ignored whenever at least one other vantage judged that
//     relay with a real verdict; only if every judging vantage saw
//     "network_down" does the relay remain "network_down" (CauseBlind).
//  2. When policy.RequireTwoVantages is true, a blocking verdict
//     ("domain_blocked", "fingerprint", "ip_banned") on a relay is accepted
//     only when at least two distinct vantages reported a blocking verdict for
//     that relay in the window. If only one vantage saw a block while another
//     saw "clean"/"reality_wins", the relay's merged verdict stays healthy;
//     if only one vantage judged the relay at all and saw a block, the block
//     remains unconfirmed ("unknown").
func MergeVantageVerdicts(byVantage map[string]RelayVerdicts, policy DetectionPolicy) RelayVerdicts {
	out := RelayVerdicts{}
	if len(byVantage) == 0 {
		return out
	}

	regions := make([]string, 0, len(byVantage))
	relaysSeen := map[string]bool{}
	for region, rv := range byVantage {
		regions = append(regions, region)
		for relay := range rv {
			relaysSeen[relay] = true
		}
	}
	sort.Strings(regions)

	for relay := range relaysSeen {
		var (
			netDown  int
			blocking []string
			healthy  []string
			other    []string
		)
		for _, region := range regions {
			v := byVantage[region][relay]
			switch v {
			case "", "unknown":
				continue
			case "network_down":
				netDown++
			case "domain_blocked", "fingerprint", "ip_banned":
				blocking = append(blocking, v)
			case "clean", "reality_wins":
				healthy = append(healthy, v)
			default:
				other = append(other, v)
			}
		}

		if len(blocking) == 0 && len(healthy) == 0 && len(other) == 0 {
			if netDown > 0 {
				out[relay] = "network_down"
			}
			continue
		}
		if !policy.IgnoreNetworkDown && netDown > 0 && len(blocking) == 0 {
			out[relay] = "network_down"
			continue
		}

		if policy.RequireTwoVantages {
			switch {
			case len(blocking) >= 2:
				out[relay] = pickBlockingVerdict(blocking)
			case len(healthy) > 0:
				out[relay] = pickHealthyVerdict(healthy)
			case len(other) > 0:
				out[relay] = other[0]
			default:
				out[relay] = "unknown"
			}
			continue
		}

		switch {
		case len(blocking) > 0:
			out[relay] = pickBlockingVerdict(blocking)
		case len(healthy) > 0:
			out[relay] = pickHealthyVerdict(healthy)
		case len(other) > 0:
			out[relay] = other[0]
		}
	}
	return out
}

func pickBlockingVerdict(vs []string) string {
	for _, v := range vs {
		if v == "domain_blocked" {
			return v
		}
	}
	for _, v := range vs {
		if v == "fingerprint" {
			return v
		}
	}
	return "ip_banned"
}

func pickHealthyVerdict(vs []string) string {
	for _, v := range vs {
		if v == "clean" {
			return v
		}
	}
	return "reality_wins"
}

// onlyHealthy says every verdict is one of the two that mean "got through".
func onlyHealthy(seen []string) bool {
	for _, v := range seen {
		if v != "clean" && v != "reality_wins" {
			return false
		}
	}
	return len(seen) > 0
}

// RefineCause sharpens a probe-only diagnosis with what the escalated grid
// saw (П9.4). A nil snapshot leaves it exactly as it was.
//
// # Why this is a second pass and not one bigger rule set
//
// The grid costs an ssh session and four handshakes, so it runs only for a box
// that already read badly (detector.go). That makes the probe-only diagnosis
// the thing that decides whether to look closer — it has to stand on its own,
// and it does. This pass can then assume the grid it is given is about a box
// somebody is already worried about.
//
// # The order is the diagnosis, again
//
// A dead process outranks a dead machine, which outranks a dead cover, for the
// same reason the rules above are ordered: the repair that touches least wins.
// A restart is free and local; a reboot is free and disruptive; a new cover is
// free but moves every client's handshake. And if the grid finds nothing
// wrong, the probe's own cause stands — a box that answers the hub and not
// Russia IS a banned address, and that is the finding, not a failure to
// explain one.
func RefineCause(d Diagnosis, g *GridSnapshot) Diagnosis {
	if g == nil {
		return d
	}
	for k, v := range g.Evidence() {
		if d.Verdicts == nil {
			d.Verdicts = map[string]string{}
		}
		d.Verdicts[k] = v
	}

	switch {
	// The process. Only readable when ssh worked, which is also what makes it
	// the box's own problem and not the provider's.
	case g.SSH == GridOK && anyFalse(g.Units):
		d.Cause = CauseEdgeDown
		d.Advice = "релей не запущен, машина жива: рестарт юнита"

	// The machine. ssh refused AND the hub's own handshake got nowhere — one
	// of those alone is a firewall or a UDP port that answers nobody.
	case g.SSH == GridFail && noneTrue(g.Reach):
		d.Cause = CauseProviderDown
		d.Advice = "машина не отвечает ни на ssh, ни хабу: ждать провайдера, потом перезагрузка"

	// The cover. Last of the three, and only when the box itself is fine:
	// a dead cover on a machine that is also down explains nothing.
	case anyFalse(g.Dests):
		d.Cause = CauseDestination
		d.Advice = "прикрытие мертво, машина и протокол живы: сменить дестинацию"
	}
	return d
}

// anyFalse says at least one thing was looked at and found broken. An empty
// map is not a finding: it means nothing was read.
func anyFalse(m map[string]bool) bool {
	for _, ok := range m {
		if !ok {
			return true
		}
	}
	return false
}

// noneTrue says nothing that was looked at answered. Also false for an empty
// map, for the same reason.
func noneTrue(m map[string]bool) bool {
	if len(m) == 0 {
		return false
	}
	for _, ok := range m {
		if ok {
			return false
		}
	}
	return true
}

// NeedsAttention says a cause is one somebody — a playbook or a person — has
// to act on. Healthy needs nothing; blind is our own outage, not the box's,
// and acting on it would be acting on noise.
func NeedsAttention(cause string) bool {
	switch cause {
	case CauseHealthy, CauseBlind, CauseUnknown:
		return false
	}
	return true
}
