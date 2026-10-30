package entry

// detector.go is the loop that turns probe verdicts into incidents (roadmap
// 42 П9.5): read the pool, read what the vantage last saw, diagnose every
// box, open what needs a person or a playbook, and close what recovered.
//
// # Why both halves, and why in that order
//
// Opening is the obvious half. Closing is the one that makes the console
// trustworthy: a feed that only ever grows teaches its reader to ignore it,
// and an incident still standing after the cause is gone is a lie the
// operator has to check by hand. So every pass does both, and a box that
// healed closes its own incident without anybody touching it — with the
// record of having been broken left behind, which is the point.
//
// # The detector never repairs
//
// It records and it proposes. Running a playbook is П9.6's, and keeping the
// two apart is what lets this run on a timer in production from the first day
// while the repairs are still being written.

import (
	"context"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"time"

	"github.com/google/uuid"
)

// VerdictSource is where the latest verdict per relay comes from — the probe
// dataset, read per observation point (`source_region`, roadmap 42 К3 / К3′).
// Declared here on the consumer side: the detector must be testable without
// ClickHouse.
//
// Rows are read only inside [since, until]. The upper bound is the point: a
// row's time is what the prober wrote, and one dated in the future would
// otherwise stay a relay's "latest verdict" for as long as the dataset keeps
// it (audit 42-autopilot К1). The ingest refuses such rows; the bound keeps
// whatever got in before it did from counting.
type VerdictSource interface {
	LatestVerdictsByVantage(ctx context.Context, since, until time.Time) (map[string]RelayVerdicts, error)
}

// SetVerdicts wires the probe dataset. Unset, the detector does nothing at
// all rather than diagnosing every box as unjudged — a hub with no analytics
// must not fill the console with "no evidence" incidents.
func (s *Service) SetVerdicts(v VerdictSource) { s.verdicts = v }

// clockSkew is how far ahead of the hub's clock a probe row may be dated and
// still be read: a vantage's clock is a little off, never minutes.
const clockSkew = 2 * time.Minute

// readVerdicts reads the probe dataset and reduces it under policy, over the
// active slots of entry_vantages only. A region the registry does not list as
// active is nobody the hub knows: its rows are dropped, whatever they say.
func (s *Service) readVerdicts(ctx context.Context, policy DetectionPolicy) (RelayVerdicts, error) {
	return s.readVerdictsSince(ctx, policy, s.now().Add(-policy.VerdictWindow))
}

// readVerdictsSince is readVerdicts over rows dated `since` or later: what the
// vantages said after a given moment, and nothing they said before it.
func (s *Service) readVerdictsSince(ctx context.Context, policy DetectionPolicy, since time.Time) (RelayVerdicts, error) {
	raw, err := s.verdicts.LatestVerdictsByVantage(ctx, since, s.now().Add(clockSkew))
	if err != nil {
		return nil, err
	}
	active, err := s.activeVantageSet(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]RelayVerdicts, len(raw))
	for region, rv := range raw {
		if active[region] {
			known[region] = rv
		}
	}
	return MergeVantageVerdicts(known, policy), nil
}

// How far back a verdict is believed is a knob now, not a constant: it lives
// in `entry_detection_policy` and the detection screen turns it (К3,
// policy.go). Its default — three probe rounds at a minute each — is the value
// this file used to hold.

// Detect runs one pass. Returns how many incidents it opened and closed.
//
// Safe to call as often as the probes refresh: opening is idempotent per
// (box, cause), and closing only touches what no longer holds.
func (s *Service) Detect(ctx context.Context) (opened, closed int, err error) {
	if s.verdicts == nil {
		return 0, 0, nil
	}
	policy, err := s.DetectionPolicy(ctx)
	if err != nil {
		return 0, 0, err
	}
	verdicts, err := s.readVerdicts(ctx, policy)
	if err != nil {
		return 0, 0, err
	}
	relays, err := s.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	byBox := map[string][]PoolRelay{}
	for _, r := range relays {
		byBox[r.Box] = append(byBox[r.Box], r)
	}

	// What already stands, read before anything is diagnosed: whether a box
	// has an incident, and whether that incident was ever looked at closely,
	// is what decides if the grid runs this round.
	standing, err := s.Incidents(ctx, 200)
	if err != nil {
		return 0, 0, err
	}
	openOn := map[string]Incident{}
	for _, inc := range standing {
		if inc.Pool == "" && (inc.Status == IncidentOpen || inc.Status == IncidentActing) {
			openOn[inc.Box] = inc
		}
	}

	// What the evidence says right now, per box.
	causes := map[string]string{}
	// Which boxes got the escalated look this round. The closing pass needs
	// it: a cause only the grid can name must not be un-said by a probe that
	// cannot see it.
	gridRan := map[string]bool{}
	covers, err := s.coverLedger(ctx)
	if err != nil {
		return 0, 0, err
	}
	for box, rs := range byBox {
		d := shortOfCovers(shortOfPorts(DiagnoseBoxWithPolicy(box, rs, verdicts, policy), rs), rs, covers)
		causes[box] = d.Cause
		// A box reading healthy is the proof a recent repair landed: what
		// replaced what broke has passed a probe and is in the pool. That
		// moment is the last stage of the console's timeline, and it is
		// later than the repair's own closing, which is optimistic by design
		// (incidents.go, MarkAdmitted). A box short of a spare port reads
		// healthy for this: every relay it has answers.
		if readsHealthy(d.Cause) {
			if err := s.MarkAdmitted(ctx, box); err != nil {
				applog.Error(ctx, "entry", "mark admitted failed", applog.F("box", box), applog.F("err", err.Error()))
			}
		}
		if !NeedsAttention(d.Cause) {
			continue
		}
		// Escalate (П9.4): a box reading badly for the first time, or one
		// whose standing incident was named before anybody looked closely.
		// Not every round — the grid is an ssh session and four handshakes,
		// and a cause already refined does not change because a minute
		// passed; what changes it is a repair or a healthy round.
		// Never for a box short of a port or a cover: it read healthy, and
		// the grid would ssh in to look for a failure there is none of.
		if s.inspect != nil && !readsHealthy(d.Cause) && !lookedAt(openOn[box]) {
			g := s.inspect.Inspect(ctx, box, rs)
			d = RefineCause(d, &g)
			causes[box], gridRan[box] = d.Cause, true
		}
		// A standing incident that the grid already read more precisely, and
		// no grid this round: the probe's own guess is the shallower reading
		// of the same failure, so opening it would put two incidents on one
		// box — and the mechanic would then work the cheap one with a
		// playbook that spends money while the real repair waits. The refined
		// incident stands, and the closing pass below keeps it.
		if !gridRan[box] && isGridCause(openOn[box].Cause) {
			continue
		}
		inc, err := s.Open(ctx, d)
		if err != nil {
			// One box's incident failing must not cost the pass: the rest of
			// the fleet still needs looking at.
			applog.Error(ctx, "entry", "open incident failed", applog.F("box", box), applog.F("err", err.Error()))
			continue
		}
		// Open is idempotent, so a repeat pass returns the standing incident
		// rather than a new one; only a genuinely new one is counted.
		if inc != nil && inc.OpenedAt.After(s.now().Add(-time.Second)) {
			opened++
			applog.Info(ctx, "entry", "incident opened",
				applog.F("incident", inc.ID.String()), applog.F("box", box),
				applog.F("cause", inc.Cause), applog.F("advice", inc.Advice))
		}
	}

	// And what no longer holds. An incident whose box now reads differently —
	// healthy, or a different cause entirely — is over as far as this cause
	// goes, and says so in its own words.
	for _, inc := range standing {
		if inc.Status != IncidentOpen && inc.Status != IncidentActing {
			continue
		}
		// A shortage of the fleet is about a pool, not a box: no box reading
		// says anything about it, and DetectFleet is what opens and closes it.
		if inc.Pool != "" {
			continue
		}
		// A repair is running against this one (П9.6). Only the repair gets
		// to say how it ended: a box being re-provisioned has no live relays
		// for minutes, which reads here as "no evidence" and would close the
		// incident in the middle of curing it.
		if inc.Status == IncidentActing {
			continue
		}
		now, judged := causes[inc.Box]
		// Handed to a person, and the evidence has not turned healthy: keep
		// it. A queue that empties itself while nobody acted is worse than a
		// queue, and "there is no evidence any more" is not the same as "it
		// is fine now" — a box whose relays are all dead has no evidence at
		// all.
		if inc.Handoff != "" && (!judged || !readsHealthy(now)) {
			continue
		}
		switch {
		case !judged:
			// The box left the pool under an open incident. Closing it as
			// resolved would claim a repair that never happened.
			if err := s.Close(ctx, inc.ID, IncidentResolved, "машина ушла из пула", inc.CostCents); err != nil {
				applog.Error(ctx, "entry", "close incident failed", applog.F("incident", inc.ID.String()), applog.F("err", err.Error()))
				continue
			}
			closed++
		// A cause only the escalated grid can name, and no grid this round:
		// the probe alone says nothing about a stopped process or a dead
		// cover, so it must not close a finding it cannot see. A box that
		// reads healthy again falls through to the clause below and closes.
		case isGridCause(inc.Cause) && !gridRan[inc.Box] && NeedsAttention(now) && !readsHealthy(now):
			continue
		case now != inc.Cause:
			if err := s.Close(ctx, inc.ID, IncidentResolved, closingNote(now), inc.CostCents); err != nil {
				applog.Error(ctx, "entry", "close incident failed", applog.F("incident", inc.ID.String()), applog.F("err", err.Error()))
				continue
			}
			closed++
		}
	}
	return opened, closed, nil
}

// lookedAt says this incident already carries a grid snapshot, so the box
// behind it has been looked at closely at least once. Read off the evidence
// rather than kept in memory: a hub that restarts would otherwise re-ssh into
// every broken box in the fleet, and the evidence is the record of what was
// seen anyway.
func lookedAt(inc Incident) bool {
	if inc.ID == uuid.Nil {
		return false
	}
	_, ok := inc.Evidence["ssh"]
	return ok
}

// isGridCause says only the escalated look can name this one.
func isGridCause(cause string) bool {
	switch cause {
	case CauseEdgeDown, CauseProviderDown, CauseDestination:
		return true
	}
	return false
}

// closingNote says why an incident stopped holding, in the words the console
// shows. A box that now reads healthy recovered; one that reads differently
// did not — it has another problem, and claiming a repair would be wrong.
func closingNote(nowCause string) string {
	switch nowCause {
	case CauseHealthy, CauseShortOfPorts, CauseShortOfCovers:
		return "причина ушла: машина снова отвечает"
	case CauseBlind:
		return "пробник ослеп, по этой причине больше ничего не сказать"
	case CauseUnknown:
		return "улик по этой причине больше нет"
	default:
		return "причина сменилась на «" + nowCause + "»"
	}
}

// DetectLoop runs Detect, then DetectFleet, on a timer until ctx ends.
// Started by the hub at startup; one pass per probe round is enough, and more
// would only re-read the same verdicts.
func (s *Service) DetectLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := s.Detect(ctx); err != nil {
				applog.Error(ctx, "entry", "incident pass failed", applog.F("err", err.Error()))
			}
			// The fleet after the boxes, and whatever they said: a pool's
			// shortage needs no probe verdict, only the pool as it stands.
			if _, _, err := s.DetectFleet(ctx); err != nil {
				applog.Error(ctx, "entry", "fleet pass failed", applog.F("err", err.Error()))
			}
		}
	}
}
