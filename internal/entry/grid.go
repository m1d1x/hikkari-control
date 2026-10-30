package entry

// grid.go is the escalated look at one box (roadmap 42 П9.4): what the hub
// itself sees of it, whether the relay process is running, and whether the
// sites the tunnel hides behind are alive.
//
// # Why the hub, and not another vantage
//
// The routine round is one probe from Russia, and a failure there has at least
// four different causes with four different repairs. Telling them apart needs
// a second opinion from outside the censored network — and the hub already is
// outside it. So "the point abroad" is this process (plan §3, decision 18.10),
// which costs no machine and no deployment. The second RU point stays a thing
// the owner has to hand us a machine for.
//
// # Only on failure, never as a background
//
// The grid dials the box and logs into it. Run every minute against every box
// it would be a load of its own and a noisy ssh trail; run only when the RU
// round says something is wrong, it is the diagnostic step of an incident. The
// detector decides when (detector.go); this file only knows how.
//
// # What each signal settles
//
//	ssh          — the machine is alive at all. Fails together with reach:
//	               the provider lost it. Fails alone: a firewall or sshd.
//	unit         — nekko-edge@<relay> is active. Not active while ssh works
//	               means the process died, which is the cheapest repair there
//	               is and must not be mistaken for a banned address.
//	reach        — the hub's own TLS handshake to the relay's port. It answers
//	               the hub but not Russia: that is the censor, and the box is
//	               innocent. It answers neither: the box is down.
//	dest         — the cover site the Reality handshake borrows. A dead cover
//	               breaks the tunnel while the machine, the address and the
//	               protocol are all fine, and the repair is a new cover rather
//	               than anything done to the box.
//
// # Nothing here is a verdict
//
// The grid reports what it saw. Turning that into a cause is diagnose.go's
// (RefineCause), for the same reason the probe's verdicts are classified
// elsewhere: a reading and a decision are different things, and only the
// reading belongs next to the I/O.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/sshexec"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Grid signal states. Three and not two: "we did not get to look" is a
// different thing from "we looked and it was broken", and a diagnosis that
// confuses them condemns machines for the hub's own outage.
const (
	GridOK      = "ok"
	GridFail    = "fail"
	GridUnknown = "unknown"
	// GridHostKey: ssh answered with a host key other than the one on record
	// — the machine was reinstalled, or someone is in the middle. Not "fail":
	// the box is up, and what answers is the finding (audit 42 С6).
	GridHostKey = "hostkey"
)

// GridSnapshot is what the escalated look saw. The maps are keyed by relay
// name, except Dests, which is keyed by cover host — a cover is shared by the
// relays of a box, and probing it once per relay would say the same thing
// three times.
type GridSnapshot struct {
	// SSH is whether the hub could log into the box.
	SSH string
	// Units: relay name → the systemd instance is active. Only filled when
	// SSH is GridOK.
	Units map[string]bool
	// Reach: relay name → the hub's own handshake to its port got through. A
	// relay the hub cannot judge (a voice relay: its port is UDP and silence
	// is not an answer) is absent rather than false.
	Reach map[string]bool
	// Dests: cover host → it is alive and serves a valid certificate.
	Dests map[string]bool
	// At is when the look happened.
	At time.Time
	// Err is why the look is partial, in the words the incident carries.
	Err string
}

// Evidence renders the snapshot as the lines an incident keeps beside the
// probe's verdicts. Keys are prefixed, so a grid line can never be mistaken
// for a relay's verdict in the same map.
func (g GridSnapshot) Evidence() map[string]string {
	out := map[string]string{"ssh": g.SSH}
	for relay, active := range g.Units {
		out["unit:"+relay] = yesNo(active, "active", "inactive")
	}
	for relay, ok := range g.Reach {
		out["hub:"+relay] = yesNo(ok, "reached", "unreachable")
	}
	for host, alive := range g.Dests {
		out["dest:"+host] = yesNo(alive, "alive", "dead")
	}
	if g.Err != "" {
		out["grid"] = clip(g.Err, 200)
	}
	return out
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// gridDialTimeout bounds one handshake. Short on purpose: the grid runs while
// an incident is waiting to be named, and a cover site that needs five seconds
// to answer is a bad cover anyway.
const gridDialTimeout = 5 * time.Second

// gridTimeout bounds the whole look at one box.
const gridTimeout = 30 * time.Second

// BoxInspector runs the escalated grid against one box. Declared here on the
// consumer side: the detector must be able to escalate without this package
// growing a dependency on ssh, and must be testable without a machine.
type BoxInspector interface {
	Inspect(ctx context.Context, box string, relays []PoolRelay) GridSnapshot
}

// SetInspector wires the grid. Left unset, the detector names causes from the
// probe alone, exactly as before П9.4 — the escalation adds precision and is
// never required for a diagnosis to happen.
func (s *Service) SetInspector(i BoxInspector) { s.inspect = i }

// Inspect is the Provisioner's implementation: it owns the ssh key and the
// dialer, so it is the only thing in the package that can ask a box about
// itself.
//
// It never returns an error. Every signal that could not be read says
// "unknown", because a grid that fails halfway is still worth more than no
// grid — and a diagnosis built on it knows which half it is missing.
func (p *Provisioner) Inspect(ctx context.Context, box string, relays []PoolRelay) GridSnapshot {
	ctx, cancel := context.WithTimeout(ctx, gridTimeout)
	defer cancel()

	g := GridSnapshot{SSH: GridUnknown, Units: map[string]bool{}, Reach: map[string]bool{},
		Dests: map[string]bool{}, At: p.svc.now()}

	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		g.Err = fmt.Sprintf("машину не прочитать: %v", err)
		return g
	}
	if acc.retired {
		g.Err = "машина сдана, смотреть нечего"
		return g
	}

	alive := make([]PoolRelay, 0, len(relays))
	for _, r := range relays {
		if r.Box == box && r.Status == StatusAlive {
			alive = append(alive, r)
		}
	}

	// The hub's own look at the port, from the free internet. A voice relay
	// listens on UDP, where nothing answers a stranger and silence proves
	// nothing, so it is left unjudged rather than called unreachable.
	for _, r := range alive {
		if acc.voice() {
			continue
		}
		sni, err := p.coverOf(ctx, r.Name, acc)
		if err != nil {
			applog.Warn(ctx, "entry", "grid: cover unreadable", applog.F("box", box), applog.F("relay", r.Name), applog.F("err", err.Error()))
			continue
		}
		g.Reach[r.Name] = tlsHandshakeOK(net.JoinHostPort(acc.address, strconv.Itoa(r.Port)), sni, false)
	}

	// The covers themselves, once each: a dead one breaks the tunnel with the
	// machine, the address and the protocol all innocent.
	for _, host := range p.coversOf(ctx, alive, acc) {
		g.Dests[host] = tlsHandshakeOK(net.JoinHostPort(host, "443"), host, true)
	}

	g.SSH, g.Units = p.unitStates(ctx, box, acc, alive)
	return g
}

// coverOf is the SNI the hub dials a relay with: its own cover for a Reality
// relay, the box's domain for an XHTTP one.
func (p *Provisioner) coverOf(ctx context.Context, relay string, acc boxAccess) (string, error) {
	if acc.proto == ProtoVlessXhttp {
		if acc.domain == "" {
			return "", fmt.Errorf("%w: у xhttp-машины нет домена", ErrRejected)
		}
		return acc.domain, nil
	}
	id, err := p.svc.RelayIdentity(ctx, relay)
	if err != nil {
		return "", err
	}
	if len(id.Covers) == 0 {
		return "", fmt.Errorf("%w: у релея нет прикрытий", ErrRejected)
	}
	return id.Covers[0], nil
}

// coversOf collects the cover sites of a box's relays, deduplicated and
// sorted. Empty for an XHTTP box: its tunnel hides behind its own domain, and
// that name's health is what the probe's domain_blocked verdict already says.
func (p *Provisioner) coversOf(ctx context.Context, relays []PoolRelay, acc boxAccess) []string {
	if acc.proto != ProtoVlessRealityVision {
		return nil
	}
	seen := map[string]bool{}
	for _, r := range relays {
		id, err := p.svc.RelayIdentity(ctx, r.Name)
		if err != nil {
			continue
		}
		for _, c := range id.Covers {
			seen[c] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// unitStates logs into the box and asks systemd about each relay. One session
// and one command: the answer is a diagnosis, not a reason to hold a
// connection open per relay.
func (p *Provisioner) unitStates(ctx context.Context, box string, acc boxAccess, relays []PoolRelay) (string, map[string]bool) {
	units := map[string]bool{}
	if len(relays) == 0 {
		return GridUnknown, units
	}
	j := &jobRun{p: p, ctx: ctx, box: box, acc: acc}
	r, err := j.loginByKey()
	switch {
	case errors.Is(err, sshexec.ErrHostKeyChanged):
		applog.Warn(j.ctx, "entry", "grid: host key changed", applog.F("box", box))
		return GridHostKey, units
	case errors.Is(err, ErrHostKeyUnknown):
		// The grid never takes a key on first use: with none on record it
		// did not get to look.
		applog.Warn(j.ctx, "entry", "grid: no host key on record", applog.F("box", box))
		return GridUnknown, units
	case err != nil:
		// Deliberately not "unknown": the hub holds this box's key and the
		// box is supposed to answer it. A refused login is a finding.
		applog.Warn(j.ctx, "entry", "grid: ssh refused", applog.F("box", box), applog.F("err", err.Error()))
		return GridFail, units
	}
	defer r.Close()

	names := make([]string, 0, len(relays))
	for _, rel := range relays {
		names = append(names, rel.Name)
	}
	// The names are the hub's own relay names (boxNameRe-shaped, drawn by
	// AddRelay), never anything a box or a client said.
	out, err := r.Run(ctx, "bash -s -- "+strings.Join(names, " "), strings.NewReader(unitStateScript))
	if err != nil {
		applog.Warn(ctx, "entry", "grid: unit states unreadable", applog.F("box", box), applog.F("err", err.Error()))
		return GridOK, units
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, state, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		units[name] = state == "active"
	}
	return GridOK, units
}

// unitStateScript prints "<relay>=<state>" per relay. `is-active` exits
// non-zero on a dead unit, which is the ordinary case here, so its status is
// swallowed and the word is what counts.
const unitStateScript = `set -u
for n in "$@"; do
	echo "$n=$(systemctl is-active "nekko-edge@$n" 2>/dev/null || true)"
done
`

// tlsHandshakeOK dials addr and completes a TLS handshake with sni. verify
// says the certificate must be valid for sni as well: that is right for a
// cover site, which is a real site on the public internet, and wrong for a
// relay, which answers with whatever certificate its cover gives it.
func tlsHandshakeOK(addr, sni string, verify bool) bool {
	d := &net.Dialer{Timeout: gridDialTimeout}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: !verify, //nolint:gosec // see the doc comment: a relay's certificate is its cover's
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
