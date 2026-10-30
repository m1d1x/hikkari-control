package entry

// domainpool.go is the one reason the hub buys a domain by itself (П9.11б,
// owner's decision 27.10: the domain is split like the machine).
//
// # Why the purchase left the blocked domain's repair
//
// replace_domain did two things as one repair: it took the blocked name off
// the box, which people need at once and costs nothing, and it bought a new
// name when the pool was dry, which costs money and can wait. One `ask`
// therefore held both, and the box stayed dark for as long as nobody pressed
// the button. Now the eviction is evict_domain — free, `auto`, from the pool
// only — and buying is led by a target for the pool, the same shape as the
// fleet's (fleet.go): fewer free names than the target opens short_of_domains
// on the pool, and its repair, buy_domain, buys one name into it under the
// registrar group's caps (spend.go).
//
// # The target is derived, not set (П9.19)
//
// Owner's decision 28.10: a count of domains is not the owner's knob — what
// a name costs moves tenfold, and the money caps are what bound the spend. The
// pool holds what will be drawn from it before a purchase made now is of use:
//
//   - one name for every XHTTP machine the fleet is about to order (its
//     pool's Need): provisioning takes a free name before it buys one;
//   - a buffer for the names the censor will burn meanwhile: the median of a
//     Poisson count with the burn rate of the last fleetLossWindow over
//     domainHorizon — the fleet's own estimator (fleet.go), and like it zero
//     with fewer than fleetMinLosses burns;
//   - and never less than one while the XHTTP pool keeps a machine, so the
//     first burnt name is replaced from stock rather than waiting on a
//     purchase.
//
// The detector runs it from DetectFleet, the fleet's half of its pass: the
// pass that opens and closes the pools' shortages.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// CauseShortOfDomains: the domain pool holds fewer free names than its
// target. Not a failure of anything; its subject is the pool.
const CauseShortOfDomains = "short_of_domains"

// domainHorizon is how far ahead the burn buffer looks: a day. A purchase and
// its resolvers take minutes, but the pass reads the pool once a minute and a
// wave burns several names at once; a day of burns in stock costs a few
// dollars a year.
const domainHorizon = 24 * time.Hour

// DomainPool is how an incident names the domain pool.
const DomainPool = "domains"

// DomainPoolReading is the domain pool as the console and the pass read it.
type DomainPoolReading struct {
	// Target is derived (see the file comment); Incoming, Buffer and Burns
	// are what it was read from.
	Target int `json:"target"`
	// InUse is the names on machines now.
	InUse int `json:"inUse"`
	// Incoming is the XHTTP machines the fleet is about to order.
	Incoming int `json:"incoming"`
	// Burns is the names burnt over fleetLossWindow; Buffer what they ask
	// to keep for the next domainHorizon.
	Burns  int `json:"burns"`
	Buffer int `json:"buffer"`
	// Free is the names owned, unassigned and not excluded: what the next
	// blocked domain is replaced from.
	Free int `json:"free"`
	// Unknown is purchases whose answer never came (В4): maybe owned, not
	// drawable, retried by the next purchase before anything new is bought.
	Unknown int `json:"unknown"`
	// Need is how many names the pool is short by; zero or less is fine.
	Need int    `json:"need"`
	Why  string `json:"why"`
}

// DomainPoolNow reads the domain pool against the target it derives.
func (s *Service) DomainPoolNow(ctx context.Context) (DomainPoolReading, error) {
	policy, err := s.DetectionPolicy(ctx)
	if err != nil {
		return DomainPoolReading{}, err
	}
	var r DomainPoolReading
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE d.status = 'owned' AND NOT EXISTS (
		           SELECT 1 FROM entry_domain_exclusions x
		           WHERE d.domain = x.domain OR right(d.domain, length(x.domain) + 1) = '.' || x.domain)),
		       count(*) FILTER (WHERE d.status = 'unknown'),
		       count(*) FILTER (WHERE d.status = 'assigned'),
		       count(*) FILTER (WHERE d.burned_at >= $1)
		FROM entry_domains d
	`, s.now().Add(-fleetLossWindow)).Scan(&r.Free, &r.Unknown, &r.InUse, &r.Burns); err != nil {
		return DomainPoolReading{}, fmt.Errorf("entry: domain pool: %w", err)
	}
	xhttp, err := s.poolForecast(ctx, policy, FleetPool(ProtoVlessXhttp, ""))
	if err != nil {
		return DomainPoolReading{}, err
	}
	r.Incoming = max(0, xhttp.Need)
	if r.Burns >= fleetMinLosses {
		perHour := float64(r.Burns) / fleetLossWindow.Hours()
		r.Buffer = poissonMedian(perHour * domainHorizon.Hours())
	}
	r.Target = r.Incoming + r.Buffer
	if xhttp.Target > 0 {
		r.Target = max(r.Target, 1)
	}
	r.Need = r.Target - r.Free
	parts := fmt.Sprintf("на подходе машин XHTTP %d, сгорело за %d дней %d — запас на сутки %d",
		r.Incoming, int(fleetLossWindow.Hours()/24), r.Burns, r.Buffer)
	switch {
	case r.Target == 0:
		r.Why = "пул XHTTP не держит машин — домены не нужны"
	case r.Need > 0:
		r.Why = fmt.Sprintf("свободных %d из %d (%s) — нужно ещё %d", r.Free, r.Target, parts, r.Need)
	default:
		r.Why = fmt.Sprintf("свободных %d при цели %d (%s)", r.Free, r.Target, parts)
	}
	return r, nil
}

// detectDomainPool opens short_of_domains when the pool is under its target
// and closes it when it is not. Idempotent per (pool, cause), like the fleet's.
func (s *Service) detectDomainPool(ctx context.Context) (opened, closed int, err error) {
	r, err := s.DomainPoolNow(ctx)
	if err != nil {
		return 0, 0, err
	}
	if r.Need > 0 {
		inc, err := s.openPool(ctx, DomainPool, CauseShortOfDomains,
			fmt.Sprintf("в пуле доменов свободных %d из %d — нужен ещё один", r.Free, r.Target),
			map[string]string{"free": strconv.Itoa(r.Free), "target": strconv.Itoa(r.Target)})
		if err != nil {
			return 0, 0, err
		}
		if inc.OpenedAt.After(s.now().Add(-time.Second)) {
			opened++
			applog.Info(ctx, "entry", "incident opened",
				applog.F("incident", inc.ID.String()), applog.F("pool", DomainPool), applog.F("cause", inc.Cause))
		}
		return opened, 0, nil
	}
	standing, err := s.Incidents(ctx, 1)
	if err != nil {
		return 0, 0, err
	}
	for _, inc := range standing {
		if inc.Pool != DomainPool || inc.Cause != CauseShortOfDomains || inc.Status != IncidentOpen {
			continue
		}
		note := "пул доменов добран"
		if r.Target == 0 {
			note = "домены больше не нужны"
		}
		if err := s.Close(ctx, inc.ID, IncidentResolved, note, inc.CostCents); err != nil {
			return 0, closed, err
		}
		closed++
	}
	return 0, closed, nil
}

// buyDomainForPool is the playbook buy_domain: one name into the pool, through
// the purchase's own gate (the registrar group's caps, the registrar's
// dry-run). The shortage is read again first — the owner may have seeded
// names by hand meanwhile.
func (m *Mechanic) buyDomainForPool(ctx context.Context, inc Incident) (repairResult, error) {
	r, err := m.svc.DomainPoolNow(ctx)
	if err != nil {
		return repairResult{}, err
	}
	if r.Need <= 0 {
		return repairResult{outcome: fmt.Sprintf("пул доменов уже добран (%s) — не покупали", r.Why)}, nil
	}
	b := m.svc.currentBuyer()
	if b.reg == nil {
		return repairResult{}, fmt.Errorf("%w: регистратора нет — покупать не через кого", ErrRejected)
	}
	name, err := m.svc.buyDomain(ctx, b, "", "")
	if err != nil {
		return repairResult{}, err
	}
	d, _, err := m.svc.repo.domain(ctx, name)
	if err != nil {
		return repairResult{}, err
	}
	return repairResult{
		outcome: fmt.Sprintf("в пул доменов куплен %s (%s), свободных было %d при цели %d",
			name, money(d.CostCents), r.Free, r.Target),
		cost: d.CostCents,
	}, nil
}

// domainPoolNote says, for an eviction that found the pool dry, who fills it.
func (s *Service) domainPoolNote(ctx context.Context) string {
	r, err := s.DomainPoolNow(ctx)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("; пул доменов добирает сам (цель %d: %s)", r.Target, r.Why)
}

// xhttpDomainReady checks a non-financial prerequisite before renting a box.
// A saved domain can be used without a registrar. Otherwise the registrar
// must exist before a machine can be put to use.
func (s *Service) xhttpDomainReady(ctx context.Context) error {
	if s.currentBuyer().reg != nil {
		return nil
	}
	r, err := s.DomainPoolNow(ctx)
	if err != nil {
		return err
	}
	if r.Free <= 0 {
		return fmt.Errorf("%w: для XHTTP нет свободного домена — добавь готовый домен или подключи регистратора", ErrNoRegistrar)
	}
	return nil
}
