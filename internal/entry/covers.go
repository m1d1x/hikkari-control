package entry

// covers.go keeps a Reality box at its full set of cover sites (roadmap 42
// П9.7, cause short_of_covers, playbook add_covers).
//
// # Why this exists next to rotate_dest
//
// rotate_dest answers a dead cover by narrowing: the relays drop it and race
// the ones still answering. That is the right repair for the moment and the
// wrong place to stay. A cover site is somebody else's server, and the usual
// way one dies is an outage that ends — after which the box keeps racing two
// covers out of three for the rest of its life, and the next dead cover takes
// it to one, then to none, which is a box that answers nobody. The chaos run
// of 25.10 left `ams1-1` exactly there: two covers of three, with the third
// alive again an hour later.
//
// So the pair is the same shape as ports (ports.go): the failure is repaired
// by taking away what broke, and the box is brought back to its target by a
// second, independent repair once it reads healthy again.
//
// # Only proven names come back, and only cleared ones
//
// What the box may be brought back to is DefaultCovers — the sites proven
// through ТСПУ (plan §9, П4) — and nothing else. Reaching for a name outside
// that set would be the hub vetting a cover by itself, which is a decision
// about the pool and not a repair.
//
// And of those, only the ones the second chance has no word against (П9.11,
// secondchance.go): a dropped cover is suspect, reprobed apart from the
// detector's minute, and written back once enough reprobes on separate
// occasions read it alive — not on the first handshake that answers, which
// for a flapping site is a coin toss. A cover whose second chance failed is
// struck: it leaves the proven set, new relays do not borrow it, and a relay
// without it is not short of anything.

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/google/uuid"
)

// CauseShortOfCovers: every relay the box has answers, and one of its
// Reality relays races fewer cover sites than DefaultCovers — rotate_dest
// dropped a dead one, or the relay was minted before the set grew. Read only
// on a box that is otherwise healthy and holds its ports: a real failure is
// that failure first, and a missing port is the cheaper thing to fix first.
const CauseShortOfCovers = "short_of_covers"

// shortOfCovers turns a healthy reading into CauseShortOfCovers when a live
// Reality relay of the box races fewer covers than the proven set and one it
// is missing is usable again — the ledger has no word against it. relays are
// the box's relays as the pool holds them.
//
// # Why the ledger and not a dial
//
// This used to dial the missing covers on every pass, and the first
// handshake that answered wrote a cover back. That made the detector's
// minute the cover's reprobe — a return on one clean read, and the active
// round doing the slow loop's work. The second chance reprobes suspects on
// their own interval; here only its verdict is read, which costs no network
// and asks nothing of somebody else's server.
//
// A relay the hub could not open is skipped: its covers are unknown, and
// "unknown" must never read as "missing".
func shortOfCovers(d Diagnosis, relays []PoolRelay, covers coverLedger) Diagnosis {
	if d.Cause != CauseHealthy {
		return d
	}
	proven := covers.proven()
	for _, r := range relays {
		if r.Status == StatusDead || r.Unreadable || r.Proto != ProtoVlessRealityVision {
			continue
		}
		has := make(map[string]bool, len(r.Covers))
		for _, c := range r.Covers {
			has[c] = true
		}
		for _, c := range proven {
			if has[c] || !covers.usable(c) {
				continue
			}
			d.Cause = CauseShortOfCovers
			d.Advice = fmt.Sprintf("у релея %s прикрытий %d из %d, %s снова в строю: вернуть", r.Name, len(r.Covers), len(proven), c)
			return d
		}
	}
	return d
}

// coverDialer is the hub's view of cover liveness for one pass: each host
// dialled at most once however many ask for it. The second chance's cover
// reprobe, rotate_dest and add_covers all dial through it.
func (s *Service) coverDialer() func(host string) bool {
	dial := s.coverAnswers
	if dial == nil {
		dial = func(host string) bool {
			return tlsHandshakeOK(net.JoinHostPort(host, "443"), host, true)
		}
	}
	seen := map[string]bool{}
	return func(host string) bool {
		if v, ok := seen[host]; ok {
			return v
		}
		v := dial(host)
		seen[host] = v
		return v
	}
}

// restoreCovers is the cover list a relay goes back to: the proven sites it
// has or that answer again, in DefaultCovers' race order, followed by any
// cover it carries from outside that set (kept as it is — removing a name is
// rotate_dest's call, not this one's). alive holds what the job dialled.
//
// Separated from the dialling for the same reason narrowCovers is: which
// covers a relay ends up with has to be checkable without a network.
func restoreCovers(current []string, alive map[string]bool) (restored, added []string) {
	have := make(map[string]bool, len(current))
	for _, c := range current {
		have[c] = true
	}
	proven := make(map[string]bool, len(DefaultCovers))
	restored = make([]string, 0, len(DefaultCovers))
	for _, c := range DefaultCovers {
		proven[c] = true
		switch {
		case have[c]:
			restored = append(restored, c)
		case alive[c]:
			restored = append(restored, c)
			added = append(added, c)
		}
	}
	for _, c := range current {
		if !proven[c] {
			restored = append(restored, c)
		}
	}
	return restored, added
}

// RestoreCovers brings a box's Reality relays back to the full set of proven
// cover sites (playbook add_covers): the missing ones are dialled live, those
// that answer are written back in race order, the units redeployed and
// admission re-probed. Returns the relays changed and the covers brought back.
func (p *Provisioner) RestoreCovers(ctx context.Context, box string) (int, []string, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return 0, nil, err
	}
	if acc.retired {
		return 0, nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if acc.proto != ProtoVlessRealityVision {
		return 0, nil, fmt.Errorf("%w: машина %s говорит %s, прикрытий у неё нет",
			ErrRejected, box, acc.proto)
	}
	var (
		changed int
		back    []string
	)
	err = p.execJob(ctx, JobRedest, box, uuid.Nil, func(j *jobRun) error {
		n, b, err := j.restoreCovers()
		changed, back = n, b
		return err
	})
	return changed, back, err
}

// restoreCovers is the job body.
func (j *jobRun) restoreCovers() (int, []string, error) {
	svc := j.p.svc
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return 0, nil, err
	}
	defer r.Close()

	relays, err := j.liveRelays()
	if err != nil {
		return 0, nil, err
	}

	j.step("dests")
	ledger, err := svc.coverLedger(j.ctx)
	if err != nil {
		return 0, nil, err
	}
	// Only the proven names a relay is missing and the second chance has
	// cleared are dialled, once per host: the dial is the last look before
	// writing, not the verdict.
	dial := svc.coverDialer()
	alive := map[string]bool{}
	for _, rel := range relays {
		has := map[string]bool{}
		for _, c := range rel.id.Covers {
			has[c] = true
		}
		for _, c := range ledger.proven() {
			if _, seen := alive[c]; seen || has[c] || !ledger.usable(c) {
				continue
			}
			alive[c] = dial(c)
			j.log("прикрытие %s: %s", c, yesNo(alive[c], "ожило", "всё ещё мертво"))
		}
	}
	if len(alive) == 0 {
		return 0, nil, fmt.Errorf("%w: у релеев машины полный набор прикрытий, возвращать нечего", ErrRejected)
	}
	// A cover the ledger had no word against and that does not answer is
	// suspect from now on — one dropped before the ledger existed, or one that
	// died since it was cleared — so the detector stops calling the box short
	// of it and the second chance takes over.
	var deadNow []string
	for _, c := range DefaultCovers {
		if state, asked := alive[c]; asked && !state {
			deadNow = append(deadNow, c)
		}
	}
	if err := svc.suspectCovers(j.ctx, deadNow); err != nil {
		return 0, nil, fmt.Errorf("прикрытия %s в реестр второго шанса: %w", strings.Join(deadNow, ", "), err)
	}

	fresh := make([]provRelay, 0, len(relays))
	backSet := map[string]bool{}
	for _, rel := range relays {
		restored, added := restoreCovers(rel.id.Covers, alive)
		if len(added) == 0 {
			continue
		}
		id, err := rel.id.withCovers(restored)
		if err != nil {
			return 0, nil, fmt.Errorf("релей %s: %w", rel.name, err)
		}
		if err := svc.setRelayIdentity(j.ctx, rel.name, id); err != nil {
			return 0, nil, fmt.Errorf("прикрытия %s: %w", rel.name, err)
		}
		for _, c := range added {
			backSet[c] = true
		}
		j.log("%s: прикрытия теперь %s", rel.name, strings.Join(restored, ", "))
		fresh = append(fresh, rel)
	}
	if len(fresh) == 0 {
		dead := make([]string, 0, len(alive))
		for _, c := range DefaultCovers {
			if state, asked := alive[c]; asked && !state {
				dead = append(dead, c)
			}
		}
		return 0, nil, fmt.Errorf(
			"%w: недостающие прикрытия (%s) всё ещё мертвы — пул проверенных прикрытий надо расширять, это решение человека",
			ErrRejected, strings.Join(dead, ", "))
	}
	changed, err := j.redeployCovers(r, fresh)
	if err != nil {
		return 0, nil, err
	}
	back := make([]string, 0, len(backSet))
	for _, c := range DefaultCovers {
		if backSet[c] {
			back = append(back, c)
		}
	}
	return len(changed), back, nil
}

// addCovers brings a box back to its full set of cover sites (cause
// short_of_covers).
func (m *Mechanic) addCovers(ctx context.Context, inc Incident) (string, error) {
	if m.prov == nil {
		return "", fmt.Errorf("%w: у хаба нет провизионера, трогать машину нечем", ErrRejected)
	}
	n, back, err := m.prov.RestoreCovers(ctx, inc.Box)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("прикрытия %s возвращены, релеев переведено: %d", strings.Join(back, ", "), n), nil
}
