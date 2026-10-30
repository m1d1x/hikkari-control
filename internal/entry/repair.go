package entry

// repair.go is the hands of the autopilot (roadmap 42 П9.6): the loop that
// takes a standing incident, asks the dispatcher what to do about it, and does
// it — or stops and says why it will not.
//
// # What was missing until this file
//
// The eyes (the prober), the brain (diagnose.go, playbooks.go, incidents.go)
// and most of the hands (the provisioner, the retirer, the domain buyer) were
// all in place, and nothing joined them: PlaybookForCause was read by the
// console and by nobody else, so every repair the hub could name was still one
// a person had to go and perform. This is the join.
//
// # Three things, kept apart on purpose
//
//  1. The rails (railsFor) — may a repair be started at all.
//  2. The bookkeeping (start, finish) — one row per attempt, so the rails have
//     something to read and П9.8 has a dataset.
//  3. The repairs themselves (retirePorts, rotateReality, evictDomain) —
//     each one does one thing to one box.
//
// The dispatcher's table stays where it is. This file never decides what fits
// a cause; it asks, and it obeys the mode.
//
// # Why a repair that succeeded still closes its incident optimistically
//
// The proof a repair worked is the next probe round, a minute away, not the
// repair's own exit code. So a successful repair closes the incident with what
// it did, and the detector opens it again — idempotently, same (box, cause) —
// if the cause still holds. The alternative, holding the incident open until a
// clean probe, leaves the queue full of incidents nobody has to act on and
// makes the console's one promise false.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The rails of §4 — how many attempts, how long a cooldown, what counts as a
// wave — used to be constants here, with a comment saying a knob nobody tunes
// is a knob everybody has to read. К3 overturned that: the owner turns these
// while looking at the ban chart, which made the rebuild they required the
// problem. They live in `entry_detection_policy` now and the mechanic reads
// them per pass (policy.go). The defaults in migration 065 are the numbers
// that used to be written here.

// wavePrefix opens every handoff the wave fuse writes, and is how lifting a
// pause tells its own incidents apart from the ones handed over for reasons of
// their own (pauses.go, [Service.LiftPause]). A marker in the text rather than
// a column: the text is what the console shows anyway, and a second column
// saying the same thing would be one more place to keep in step.
const wavePrefix = "предохранитель волны"

// Repair statuses (entry_repairs.status).
const (
	repairRunning = "running"
	repairDone    = "done"
	repairFailed  = "failed"
	// repairHeld: admission could not be judged (errNoVerdict), or a
	// purchase must wait for money (ErrBudget). Neither says the repair
	// failed; both remain eligible for automatic retries without a strike.
	repairHeld = "held"
)

// ErrRepairBusy: this box already has a repair running.
var ErrRepairBusy = errors.New("entry: the box is already being repaired")

// Mechanic carries out playbooks. It holds the provisioner because the repairs
// that touch a machine are provisioning jobs — the same ssh, the same live log
// in the console, the same one-job-per-box lock.
type Mechanic struct {
	svc  *Service
	prov *Provisioner
	// timeout bounds one attempt (repairTimeout); a field so a test can end
	// an attempt in the middle of what it waits for.
	timeout time.Duration
}

// NewMechanic builds the repairer. prov may be nil: the hub then runs only the
// repairs that need no machine touched (taking banned ports out of the pool),
// and says so instead of pretending.
func NewMechanic(svc *Service, prov *Provisioner) *Mechanic {
	return &Mechanic{svc: svc, prov: prov, timeout: repairTimeout}
}

// Pass runs one round: every standing incident the hub may act on alone.
// Returns how many repairs it started and how many incidents it handed over.
//
// Called on a timer beside the detector. Safe to repeat: an incident with a
// repair running is skipped by the unique index, one inside its cooldown by the
// rails, and one already handed over by its own column.
func (m *Mechanic) Pass(ctx context.Context) (started, handed int, err error) {
	// What a lost verdict left behind, first: a repair `running` past its
	// timeout holds its box's only lock, an incident `acting` with nothing
	// running is a card nobody will ever act on (audit mines).
	if err := m.svc.reapStuck(ctx); err != nil {
		applog.Error(ctx, "entry", "reaping stuck repairs failed", applog.F("err", err.Error()))
	}
	// Machines the hub still pays for and does not use: a retired box the
	// hoster would not delete, an order that left one behind (С1).
	if n, err := m.svc.ReleaseUndeleted(ctx); err != nil {
		applog.Error(ctx, "entry", "undeleted machines pass failed", applog.F("err", err.Error()))
	} else if n > 0 {
		applog.Info(ctx, "entry", "undeleted machines released", applog.F("count", n))
	}
	if m.prov != nil {
		// Idle machines first: one that sat past idleHold is rent nobody
		// stops, and handing it back needs no incident to start from.
		if gone, err := m.prov.handBackIdle(ctx); err != nil {
			applog.Error(ctx, "entry", "idle machines pass failed", applog.F("err", err.Error()))
		} else if len(gone) > 0 {
			applog.Info(ctx, "entry", "idle machines handed back", applog.F("boxes", gone))
		}
		// And the machines whose second chance failed: the ledger decided,
		// and handing back talks to the hoster, so it is done here.
		if gone, err := m.prov.retireDeadSuspects(ctx); err != nil {
			applog.Error(ctx, "entry", "dead suspects pass failed", applog.F("err", err.Error()))
		} else if len(gone) > 0 {
			applog.Info(ctx, "entry", "dead suspects handed back", applog.F("boxes", gone))
		}
		// And the machines pools no longer need: hourly rent that would
		// otherwise pile up (П9.11б, П9.19).
		if gone, err := m.prov.handBackSurplus(ctx); err != nil {
			applog.Error(ctx, "entry", "surplus pass failed", applog.F("err", err.Error()))
		} else if len(gone) > 0 {
			applog.Info(ctx, "entry", "surplus machines handed back", applog.F("boxes", gone))
		}
	}
	incidents, err := m.svc.Incidents(ctx, 0)
	if err != nil {
		return 0, 0, err
	}
	for _, inc := range incidents {
		ran, gave := m.consider(ctx, inc)
		if ran {
			started++
		}
		if gave {
			handed++
		}
	}
	return started, handed, nil
}

// consider is one incident's turn: may the hub act on it alone, do the rails
// allow it, and if not, is this the moment to give up on it.
//
// Shared by the timed pass and by the domain replacement that arrives as a
// death rather than as a probe verdict, so both obey the same mode and the
// same rails. Failures are logged and swallowed: one incident must not cost
// the pass, the rest of the fleet still needs looking at.
func (m *Mechanic) consider(ctx context.Context, inc Incident) (started, handed bool) {
	if inc.Status != IncidentOpen || inc.Handoff != "" {
		return false, false
	}
	p, err := m.playbook(ctx, inc)
	if err != nil {
		applog.Error(ctx, "entry", "playbook unreadable", applog.F("box", inc.Box), applog.F("cause", inc.Cause), applog.F("err", err.Error()))
		return false, false
	}
	if !p.MayRunUnattended() {
		// `ask` and `off` are the console's queue, not ours. A cause with no
		// playbook at all is there too, which is the honest place for it.
		return false, false
	}
	why, blocked, err := m.rails(ctx, inc, p.Name)
	if err != nil {
		applog.Error(ctx, "entry", "rails unreadable", applog.F("box", inc.Box), applog.F("cause", inc.Cause), applog.F("err", err.Error()))
		return false, false
	}
	if blocked {
		if why == "" {
			return false, false // cooldown, or a repair running: just wait
		}
		if err := m.svc.HandOff(ctx, inc.ID, why); err != nil {
			applog.Error(ctx, "entry", "hand over failed", applog.F("incident", inc.ID.String()), applog.F("err", err.Error()))
			return false, false
		}
		return false, true
	}
	if p.Name == PlaybookRetirePorts {
		settled, err := m.portsSettled(ctx, inc)
		if err != nil {
			applog.Error(ctx, "entry", "ports reading unreadable", applog.F("box", inc.Box), applog.F("err", err.Error()))
			return false, false
		}
		if !settled {
			return false, false // the round that settles it has not landed: wait
		}
	}
	if err := m.run(ctx, inc, p.Name, DecidedAuto); err != nil {
		if !errors.Is(err, ErrRepairBusy) && !errors.Is(err, ErrBoxBusy) {
			applog.Error(ctx, "entry", "start repair failed", applog.F("box", inc.Box), applog.F("err", err.Error()))
		}
		return false, false
	}
	return true, false
}

// ReplaceDomain implements [DomainReplacer]: the last relay of a box died of a
// blocked domain, so there is something to repair.
//
// It does not repair anything itself. The death opens an incident, and the
// dispatcher decides: since П9.11б the repair is evict_domain — free, on
// `auto`, from the pool only — so the same call starts it at once; a domain
// is bought by the domain pool's target, never by a death.
//
// A death is a trigger the prober cannot give us: a box with no live relays
// has nothing left to probe, so the detector would never see this cause at
// all.
func (m *Mechanic) ReplaceDomain(box string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inc, err := m.svc.Open(ctx, Diagnosis{
		Box:      box,
		Cause:    CauseDomainBlocked,
		Advice:   "домен заблокирован, адрес жив: новый домен, машину не трогать",
		Verdicts: map[string]string{},
	})
	if err != nil {
		applog.Error(ctx, "entry", "open domain_blocked incident failed", applog.F("box", box), applog.F("err", err.Error()))
		return
	}
	if inc == nil {
		return
	}
	m.consider(ctx, *inc)
}

// Run loops Pass until ctx ends. One pass per probe round is enough: the
// incidents it reads only change when the detector has run.
func (m *Mechanic) Run(ctx context.Context, every time.Duration) {
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
			if _, _, err := m.Pass(ctx); err != nil {
				applog.Error(ctx, "entry", "repair pass failed", applog.F("err", err.Error()))
			}
		}
	}
}

// Start is the console's button: run a playbook against this incident,
// because a person said so. An empty `playbook` means the one the hub
// proposed.
//
// The rails still apply. They are not there to protect the hub from itself —
// they are there to stop three machines being bought over one ТСПУ rule
// change, and a person pressing the button has no more information about that
// than the hub does. What a person does override is the mode: `ask` is exactly
// what this call is for.
//
// # Choosing another repair
//
// `playbook` is how an operator overrules the dispatcher (К2 §4): the hub
// diagnosed a dead cover site, the person knows the unit is wedged, and
// `restart_edge` is the repair they want. It is a choice from what the hub has
// code for and never a free-form command, so the attempt lands in
// entry_repairs and in the П9.8 dataset like any other. Picking a repair the
// cause did not call for is allowed and recorded as `manual`: the repair
// itself still checks its own preconditions and refuses if the box is not in a
// state it can work with.
func (m *Mechanic) Start(ctx context.Context, id uuid.UUID, adminID, playbook string) error {
	inc, err := m.svc.Incident(ctx, id)
	if err != nil {
		return err
	}
	if inc.Status != IncidentOpen {
		return fmt.Errorf("%w: инцидент в состоянии %q, запускать нечего", ErrRejected, inc.Status)
	}
	p, err := m.playbook(ctx, inc)
	if err != nil {
		return err
	}
	decidedBy := DecidedAsk
	if playbook != "" && playbook != p.Name {
		chosen, ok := KnownPlaybook(playbook)
		if !ok {
			return fmt.Errorf("%w: ремонта %q у хаба нет", ErrRejected, playbook)
		}
		// A person choosing against the proposal is the definition of
		// `manual`, and П9.8 reads the difference: a cause whose operators
		// keep overruling the dispatcher has the wrong playbook, not an
		// impatient owner.
		//
		// The owner's mode goes with the repair (audit С2): `off` on the
		// cause whose repair this is means the hub never does it, and a
		// hand choosing it on another incident must not get round that.
		modes, err := LoadPlaybookModes(ctx, m.svc.pool)
		if err != nil {
			return err
		}
		p, decidedBy = modes.For(chosen.Cause), DecidedManual
	}
	if p.Name == "" {
		return fmt.Errorf("%w: для причины %q хаб не знает ремонта", ErrRejected, inc.Cause)
	}
	if err := fitsSubject(inc, p.Name); err != nil {
		return err
	}
	if p.Mode == ModeOff {
		return fmt.Errorf("%w: ремонт %q выключен", ErrRejected, p.Name)
	}
	if p.Name == PlaybookScaleUp {
		policy, err := m.svc.DetectionPolicy(ctx)
		if err != nil {
			return err
		}
		if wait, err := m.svc.noLiveVantage(ctx, policy); err != nil {
			return err
		} else if wait != "" {
			return fmt.Errorf("%w: %s", ErrRejected, wait)
		}
	}
	why, blocked, err := m.rails(ctx, inc, p.Name)
	if err != nil {
		return err
	}
	if blocked {
		if why == "" {
			why = "по этой машине ремонт уже идёт или ещё не вышел кулдаун"
		}
		return fmt.Errorf("%w: %s", ErrRejected, why)
	}
	applog.Info(ctx, "entry", "repair asked for by an operator", applog.F("admin_id", adminID), applog.F("playbook", p.Name), applog.F("box", inc.Box))
	return m.run(ctx, inc, p.Name, decidedBy)
}

// strikeWindow is how far back the attempts on one (box, cause) are counted
// against [DetectionPolicy.FailStrikes].
const strikeWindow = 24 * time.Hour

// playbook is the repair for this incident, the owner's mode applied, and
// escalated where the cause's own repair cannot help.
//
// # provider_down climbs to a new machine
//
// The plan's ladder for a machine that answers nobody is wait, reboot, then
// replace (§3), and only the first two rungs existed: on the stand 25.10 the
// owner deleted ams1 at the hoster, the detector read provider_down rightly,
// reboot_box was refused — the box was rented by hand and the hoster has no
// instance of it — and the hub stood there with an empty pool. So the
// incident climbs to evict_box when the reboot cannot be done (no instance
// id) or has been done on this incident and the machine is still gone: out of
// the pool at once, replaced by the fleet, and handed back only when its
// second chance fails — a hoster's outage that ends is a ban not proven.
//
// The climb takes evict_box's own mode, the one the owner set for the cause
// whose repair it is (ip_banned): taking a machine out of the pool is the same
// decision whatever broke it.
func (m *Mechanic) playbook(ctx context.Context, inc Incident) (Playbook, error) {
	modes, err := LoadPlaybookModes(ctx, m.svc.pool)
	if err != nil {
		return Playbook{}, err
	}
	p := modes.For(inc.Cause)
	if inc.Cause != CauseProviderDown {
		return p, nil
	}
	why, err := m.rebootWillNotDo(ctx, inc)
	if err != nil || why == "" {
		return p, err
	}
	evict := modes.For(CauseIPBanned)
	if evict.Name != PlaybookEvictBox {
		return p, nil
	}
	evict.Why = why
	return evict, nil
}

// rebootWillNotDo says why a reboot is not the repair for this provider_down
// incident any more, or "" when it still is.
func (m *Mechanic) rebootWillNotDo(ctx context.Context, inc Incident) (string, error) {
	rental, err := m.svc.boxRental(ctx, inc.Box)
	if err != nil {
		return "", err
	}
	if rental.instanceID == "" {
		return "машина заведена руками, перезагрузить её у хостера нечем: снять с выдачи, замену докупает парк", nil
	}
	var rebooted int
	if err := m.svc.pool.QueryRow(ctx, `
		SELECT count(*) FROM entry_repairs
		WHERE incident_id = $1 AND playbook = $2 AND status <> 'running'
	`, inc.ID, PlaybookRebootBox).Scan(&rebooted); err != nil {
		return "", fmt.Errorf("entry: reboots on %s: %w", inc.ID, err)
	}
	if rebooted > 0 {
		return "перезагрузка не вернула машину: снять с выдачи, замену докупает парк", nil
	}
	return "", nil
}

// spendGroups are the repairs that end in one purchase, with every cause that
// reaches them: a ban evicts a machine, provider_down climbs to the same
// eviction, and the shortage both leave is what buys the replacement. A wave
// paused on any of the causes holds every repair of the group — a pause is a
// person not having looked yet, and the spend it guards does not care which
// door it came through (audit mine: the climb took ip_banned's mode and not
// its pause).
var spendGroups = []struct{ playbooks, causes []string }{
	{
		playbooks: []string{PlaybookEvictBox, PlaybookScaleUp},
		causes:    []string{CauseIPBanned, CauseProviderDown, CauseShortOfFleet},
	},
	{
		playbooks: []string{PlaybookEvictDomain, PlaybookBuyDomain},
		causes:    []string{CauseDomainBlocked, CauseShortOfDomains},
	},
}

// pauseCauses is every cause whose pause holds this repair of this incident.
func pauseCauses(cause, playbook string) []string {
	out := []string{cause}
	for _, g := range spendGroups {
		if slices.Contains(g.playbooks, playbook) {
			for _, c := range g.causes {
				if c != cause {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// pausedTogether is every cause a pause of this one holds: itself, and the
// causes it shares a spend group with.
func pausedTogether(cause string) []string {
	out := []string{cause}
	for _, g := range spendGroups {
		if slices.Contains(g.causes, cause) {
			for _, c := range g.causes {
				if !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// waveByPlaybook are the repairs whose wave is counted by the repair rather
// than by the cause: an eviction is one decision whatever broke the machine,
// so three evictions in an hour are a wave even when one came from
// provider_down.
var waveByPlaybook = []string{PlaybookEvictBox, PlaybookEvictDomain}

// rails answers whether a repair of this incident may start. blocked with an
// empty why means "not now" (a cooldown, a repair in flight); blocked with a
// why means the hub is giving up and the reason goes on the incident.
func (m *Mechanic) rails(ctx context.Context, inc Incident, playbook string) (why string, blocked bool, err error) {
	// The fuse, if it is already blown on this cause or on one that leads
	// to the same spend. Checked before the per-box history because it
	// outranks it: a standing pause means a person has not yet looked at a
	// wave, and no single box's clean record is an argument against that.
	pause, paused, err := m.svc.anyCausePaused(ctx, pauseCauses(inc.Cause, playbook))
	if err != nil {
		return "", false, err
	}
	if paused {
		return pause.Reason, true, nil
	}

	policy, err := m.svc.DetectionPolicy(ctx)
	if err != nil {
		return "", false, err
	}
	if inc.Pool != "" {
		return m.poolRails(ctx, inc, playbook, policy)
	}

	// The strikes are counted over a day, not over the box's life. Counted
	// forever, three repairs that each worked would retire the cause on that
	// box for good: the fourth stopped unit on a machine that has carried
	// people for months would go to a person on the strength of history that
	// says the repair is right. A day is the plan's own horizon (§3: a second
	// restart in a day means the binary, a third means a person), and it
	// still catches both ways a repair can fail to help — attempts that end
	// in an error, and attempts that end in `done` with the box breaking
	// again the same way an hour later.
	//
	// The cooldown runs from the last attempt that did not take: one that
	// failed, or any attempt on this very incident (a `done` repair whose
	// incident is still standing did not cure it). A repair that worked and
	// closed its incident is not a reason to wait. Found on the stand 25.10:
	// retire_ports took a banned port off ams1, the box came up short of its
	// spare, and add_ports waited out fifteen minutes because add_ports had
	// raised a port on the same box — successfully — four minutes before.
	// The fifteen minutes were the box one ban away from a new machine. A
	// cause that keeps coming back after repairs that work is the strikes'
	// to stop, not the cooldown's.
	var running int
	var tries int
	var last *time.Time
	err = m.svc.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'running'),
		       count(*) FILTER (WHERE status IN ('done', 'failed') AND started_at >= $3),
		       max(started_at) FILTER (WHERE status <> 'running'
		                                 AND (status <> 'done' OR incident_id = $4))
		FROM entry_repairs WHERE box = $1 AND cause = $2
	`, inc.Box, inc.Cause, m.svc.now().Add(-strikeWindow), inc.ID).Scan(&running, &tries, &last)
	if err != nil {
		return "", false, fmt.Errorf("entry: repair history: %w", err)
	}
	if running > 0 {
		return "", true, nil
	}
	if tries >= policy.FailStrikes {
		return fmt.Sprintf("ремонт %q пробовали %d раза и не помогло — дальше смотреть руками", inc.Playbook, tries), true, nil
	}
	if last != nil && m.svc.now().Sub(*last) < policy.RepairCooldown {
		return "", true, nil
	}

	// The wave: this cause across the whole fleet. Counted over repairs and
	// not over incidents, because what must not happen three times in an hour
	// is the acting, not the noticing.
	byPlaybook := ""
	if slices.Contains(waveByPlaybook, playbook) {
		byPlaybook = playbook
	}
	var wave int
	err = m.svc.pool.QueryRow(ctx, `
		SELECT count(DISTINCT box) FROM entry_repairs
		WHERE (cause = $1 OR playbook = NULLIF($3, '')) AND started_at >= $2
	`, inc.Cause, m.svc.now().Add(-policy.WaveWindow), byPlaybook).Scan(&wave)
	if err != nil {
		return "", false, fmt.Errorf("entry: repair wave: %w", err)
	}
	if wave >= policy.WaveLimit {
		why := fmt.Sprintf("%s: за %s причина %q сломала %d машины — это похоже на новое правило у цензора или на нашу ошибку, а не на %d поломок",
			wavePrefix, policy.WaveWindow, inc.Cause, wave, wave)
		// The fuse is written down, not just acted on. Before it was a row the
		// pause lived in the rolling window alone, which meant the autopilot
		// resumed an hour later with nobody having looked at why it stopped.
		if err := m.svc.PauseCause(ctx, inc.Cause, why, wave); err != nil {
			return "", false, err
		}
		return why, true, nil
	}
	return "", false, nil
}

// poolRails is rails for an incident about a pool (scale_up, fleet.go;
// buy_domain, domainpool.go).
//
// The strikes and the cooldown are the box's, read per (pool, cause): a
// machine born banned fails its admission, the attempt ends `failed`, and that
// is a strike like any other — three of them and a person looks at why every
// address this hoster hands out is dead on arrival.
//
// The wave fuse's count is not here. It counts machines one cause broke, and
// a shortage breaks none; what stops a run of purchases instead is:
//
//   - one acquisition of a kind at a time: no other repair of this playbook
//     running (one scale_up buys everything its pool is short of at once);
//   - money: every order and every purchase passes its group's day and month
//     caps (spend.go), and a shortage the caps stop is short_of_budget, not
//     a strike.
func (m *Mechanic) poolRails(ctx context.Context, inc Incident, playbook string, policy DetectionPolicy) (why string, blocked bool, err error) {
	// Missing configuration is a prerequisite to wait for, not a broken
	// repair to strike or a reason to rent an unusable machine.
	if playbook == PlaybookBuyDomain && m.svc.currentBuyer().reg == nil {
		return "", true, nil
	}
	proto, _ := splitPool(inc.Pool)
	if playbook == PlaybookScaleUp && proto == ProtoVlessXhttp {
		if err := m.svc.xhttpDomainReady(ctx); errors.Is(err, ErrNoRegistrar) {
			return "", true, nil
		} else if err != nil {
			return "", false, err
		}
	}
	now := m.svc.now()
	var running, tries, acquiring int
	var last *time.Time
	// A strike is an attempt that did not take: one that failed, or one that
	// ended done while its shortage still stands. A scale_up that bought a
	// machine and closed its shortage worked, however many came before it —
	// found on the stand 27.10, where three purchases that each worked, on
	// three bans that day, handed the fourth shortage to a person as "tried
	// and it did not work". What bounds a run of purchases is the money gate,
	// not the strikes.
	err = m.svc.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE r.pool = $1 AND r.cause = $2 AND r.status = 'running'),
		       count(*) FILTER (WHERE r.pool = $1 AND r.cause = $2 AND r.started_at >= $3
		                          AND (r.status = 'failed'
		                               OR (r.status = 'done' AND i.status IN ('open', 'acting')))),
		       max(r.started_at) FILTER (WHERE r.pool = $1 AND r.cause = $2 AND r.status <> 'running'
		                                   AND (r.status <> 'done' OR r.incident_id = $4)),
		       count(*) FILTER (WHERE r.playbook = $5 AND r.status = 'running')
		FROM entry_repairs r
		LEFT JOIN entry_incidents i ON i.id = r.incident_id
		WHERE (r.pool = $1 AND r.cause = $2) OR r.playbook = $5
	`, inc.Pool, inc.Cause, now.Add(-strikeWindow), inc.ID, playbook).Scan(&running, &tries, &last, &acquiring)
	if err != nil {
		return "", false, fmt.Errorf("entry: pool repair history: %w", err)
	}
	if running > 0 || acquiring > 0 {
		return "", true, nil
	}
	if tries >= policy.FailStrikes {
		return fmt.Sprintf("добор пула %s пробовали %d раза и не вышло — дальше смотреть руками", inc.Pool, tries), true, nil
	}
	if last != nil && now.Sub(*last) < policy.RepairCooldown {
		return "", true, nil
	}
	if playbook == PlaybookScaleUp {
		if wait, err := m.svc.noLiveVantage(ctx, policy); err != nil {
			return "", false, err
		} else if wait != "" {
			return "", true, nil
		}
	}
	return "", false, nil
}

// noLiveVantage waits for an active admission worker, independently of the
// relay dataset. An empty fleet has no verdicts but still needs its first box.
func (s *Service) noLiveVantage(ctx context.Context, policy DetectionPolicy) (string, error) {
	var live bool
	now := s.now()
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM entry_vantages
		WHERE status = 'active' AND last_admission_poll_at >= $1 AND last_admission_poll_at <= $2)`,
		now.Add(-policy.VerdictWindow), now.Add(clockSkew)).Scan(&live)
	if err != nil {
		return "", fmt.Errorf("entry: admission worker readiness: %w", err)
	}
	if live {
		return "", nil
	}
	return fmt.Sprintf("ни один активный пробник не запрашивал задания допуска за %s — добор ждёт готовый пробник", policy.VerdictWindow), nil
}

// fitsSubject refuses a repair that cannot apply to what the incident is
// about: scale_up adds a machine to a fleet pool, buy_domain a name to the
// domain pool, every other playbook works on one box. The console's "another
// action" menu offers them all, so the check is here and in perform rather
// than in the menu.
func fitsSubject(inc Incident, playbook string) error {
	switch {
	case playbook == PlaybookBuyDomain && inc.Pool == DomainPool:
		return nil
	case playbook == PlaybookScaleUp && inc.Pool != "" && inc.Pool != DomainPool:
		return nil
	case playbook != PlaybookScaleUp && playbook != PlaybookBuyDomain && inc.Pool == "":
		return nil
	case inc.Pool == "":
		return fmt.Errorf("%w: %q добирает пул, а инцидент про машину %s", ErrRejected, playbook, inc.Box)
	case playbook == PlaybookScaleUp || playbook == PlaybookBuyDomain:
		return fmt.Errorf("%w: %q добирает другой пул, а инцидент про пул %s", ErrRejected, playbook, inc.Pool)
	}
	return fmt.Errorf("%w: инцидент про пул %s, а %q чинит машину", ErrRejected, inc.Pool, playbook)
}

// run books an attempt and carries it out in the background.
//
// The row is inserted before anything happens, so a hub that dies mid-repair
// leaves evidence that it tried — the alternative is a box that was half
// repaired and a history that says nothing was.
func (m *Mechanic) run(ctx context.Context, inc Incident, playbook, decidedBy string) error {
	var id uuid.UUID
	err := m.svc.pool.QueryRow(ctx, `
		INSERT INTO entry_repairs (incident_id, box, pool, cause, playbook, decided_by)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, $6) RETURNING id
	`, inc.ID, inc.Box, inc.Pool, inc.Cause, playbook, decidedBy).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s%s", ErrRepairBusy, inc.Box, inc.Pool)
		}
		return fmt.Errorf("entry: book repair: %w", err)
	}
	if err := m.svc.Act(ctx, inc.ID, playbook, decidedBy); err != nil {
		// The incident moved under us (closed, or already acting). Give the
		// booking back rather than leaving a row that blocks the box.
		if ferr := m.finish(context.WithoutCancel(ctx), id, 0, errors.New("инцидент уже не ждёт ремонта")); ferr != nil {
			applog.Error(ctx, "entry", "release repair failed", applog.F("repair", id.String()), applog.F("err", ferr.Error()))
		}
		return err
	}
	applog.Info(ctx, "entry", "repair started",
		applog.F("repair", id.String()), applog.F("box", inc.Box), applog.F("cause", inc.Cause),
		applog.F("playbook", playbook), applog.F("decided_by", decidedBy))
	m.svc.spawn(func(ctx context.Context) { m.carry(ctx, id, inc, playbook) })
	return nil
}

// carry performs the repair and records how it went. Runs on the service's
// background context ([Service.spawn]): the request that started it is long
// gone by the time a machine answers.
func (m *Mechanic) carry(ctx context.Context, repair uuid.UUID, inc Incident, playbook string) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	res, err := m.perform(ctx, inc, playbook)
	fin, cancelFin := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFin()

	// A verdict not written leaves the row `running`, which holds the box's
	// only lock: tried again, and reaped by the pass if it never lands.
	for attempt := 0; attempt < 3; attempt++ {
		ferr := m.finish(fin, repair, res.cost, err)
		if ferr == nil {
			break
		}
		applog.Error(fin, "entry", "repair verdict not recorded",
			applog.F("repair", repair.String()), applog.F("attempt", attempt+1), applog.F("err", ferr.Error()))
	}
	if err != nil {
		// Budget refusals are held, never failed: running out of money is
		// not a broken repair and must not exhaust the automatic retries.
		// Back to open so the next pass can try again — or hand it over, once
		// the strikes run out. Already open means the pass's reaper got
		// there first, which is the same outcome.
		if rerr := m.svc.Reopen(fin, inc.ID); rerr != nil && !errors.Is(rerr, ErrNoSuchIncident) {
			applog.Error(fin, "entry", "reopen incident failed", applog.F("incident", inc.ID.String()), applog.F("err", rerr.Error()))
		}
		applog.Warn(fin, "entry", "repair failed", applog.F("box", inc.Box), applog.F("cause", inc.Cause), applog.F("playbook", playbook), applog.F("err", err.Error()))
		return
	}
	applog.Info(fin, "entry", "repair done",
		applog.F("repair", repair.String()), applog.F("box", inc.Box), applog.F("cause", inc.Cause),
		applog.F("playbook", playbook), applog.F("outcome", res.outcome), applog.F("cost_cents", res.cost))
	if cerr := m.svc.closeIncident(fin, inc.ID, IncidentResolved, res.outcome, inc.CostCents+res.cost, res.replacedBy); cerr != nil {
		applog.Error(fin, "entry", "close incident failed", applog.F("incident", inc.ID.String()), applog.F("err", cerr.Error()))
	}
}

// repairTimeout bounds one attempt. Generous: a re-provisioning after a
// blocked domain waits for public resolvers, which is minutes on a fresh name.
const repairTimeout = 20 * time.Minute

// repairResult is what one repair did: the words the console shows, what it
// cost, and — for a replacement only — the machine it put in the pool, which
// is where the incident's admission will be observed (MarkAdmitted).
type repairResult struct {
	outcome    string
	cost       int
	replacedBy string
}

// perform dispatches to the one repair this playbook names, and returns what
// it did. A playbook with no implementation is an error and not a silent
// success — the console would otherwise show a repair that never happened.
func (m *Mechanic) perform(ctx context.Context, inc Incident, playbook string) (repairResult, error) {
	free := func(out string, err error) (repairResult, error) {
		return repairResult{outcome: out}, err
	}
	if err := fitsSubject(inc, playbook); err != nil {
		return repairResult{}, err
	}
	switch playbook {
	case PlaybookRetirePorts:
		return free(m.retirePorts(ctx, inc))
	case PlaybookAddPorts:
		return free(m.addPorts(ctx, inc))
	case PlaybookAddCovers:
		return free(m.addCovers(ctx, inc))
	case PlaybookRotateReality:
		return free(m.rotateReality(ctx, inc))
	case PlaybookEvictDomain:
		return free(m.evictDomain(ctx, inc))
	case PlaybookBuyDomain:
		return m.buyDomainForPool(ctx, inc)
	case PlaybookRebootBox:
		return free(m.rebootBox(ctx, inc))
	case PlaybookEvictBox:
		return free(m.evictBox(ctx, inc))
	case PlaybookRestartEdge:
		return free(m.restartEdge(ctx, inc))
	case PlaybookRotateDest:
		return free(m.rotateDest(ctx, inc))
	case PlaybookScaleUp:
		return m.scaleUp(ctx, inc)
	default:
		// Every playbook the dispatcher names now has a body. A name that
		// reaches here is a playbook somebody added to the table without one,
		// and saying so is better than the hub claiming it tried.
		return repairResult{}, fmt.Errorf("%w: ремонт %q ещё не реализован", ErrRejected, playbook)
	}
}

// portsReading is the box as the vantages read it after the incident opened:
// only rows dated later than OpenedAt, so only rounds that started after the
// reading the incident was opened on had already landed. landed is false
// until every relay the incident judged has such a row — until then there is
// nothing newer to go on.
func (m *Mechanic) portsReading(ctx context.Context, inc Incident) (d Diagnosis, landed bool, err error) {
	if m.svc.verdicts == nil {
		return Diagnosis{}, false, nil
	}
	policy, err := m.svc.DetectionPolicy(ctx)
	if err != nil {
		return Diagnosis{}, false, err
	}
	verdicts, err := m.svc.readVerdictsSince(ctx, policy, inc.OpenedAt)
	if err != nil {
		return Diagnosis{}, false, err
	}
	relays, err := m.svc.List(ctx)
	if err != nil {
		return Diagnosis{}, false, err
	}
	var rs []PoolRelay
	alive := map[string]bool{}
	for _, r := range relays {
		if r.Box == inc.Box {
			rs = append(rs, r)
			alive[r.Name] = r.Status == StatusAlive
		}
	}
	for name := range inc.Evidence {
		if _, ok := verdicts[name]; alive[name] && !ok {
			return Diagnosis{}, false, nil
		}
	}
	return DiagnoseBoxWithPolicy(inc.Box, rs, verdicts, policy), true, nil
}

// portsSettled says whether a ports_banned incident may be acted on by the hub
// alone: a full round after it opened still reads the box as some ports
// banned and others alive.
//
// # Why one round is not a diagnosis here
//
// A prober round walks a box's relays one after another, so a ban of the whole
// address that lands while the round is under way reads as exactly this: the
// relays probed before it clean, the ones after it banned. Found on the stand
// 27.10 (П9.7 ip-banned, ams6): the mixed round opened ports_banned,
// retire_ports wrote the "banned" port dead for good, add_ports raised a port
// that was born banned and spent a strike, and the next round named the
// address two minutes later. The reading that tells the two apart is the next
// one: a banned port stays mixed, a banned address reads every port gone — and
// the detector then opens ip_banned and closes this incident by itself. So the
// hub waits for that round instead of acting on the first; the cost is a real
// port ban retired one round later, on a box whose other ports still carry
// people. A person pressing the button in the console is not held: they read
// the evidence themselves.
func (m *Mechanic) portsSettled(ctx context.Context, inc Incident) (bool, error) {
	d, landed, err := m.portsReading(ctx, inc)
	if err != nil {
		return false, err
	}
	return landed && d.Cause == CausePortsBanned, nil
}

// retirePorts takes the box's banned relays out of the pool and leaves it the
// ones that answer (cause ports_banned).
//
// Which relays are banned is read from the newest rounds when one has landed
// since the incident opened (portsReading), and from the incident's evidence
// only when none has: the evidence is the reading the incident was opened on,
// and a port that read clean then may be gone since.
//
// The rail that matters here is the floor: if every live relay of the box is
// in the evidence, this is not a port problem — it is the address, and the
// repair for that is a machine. Refusing beats quietly emptying a box, which
// would retire it by accident and without the make-before-break the pool
// requires.
func (m *Mechanic) retirePorts(ctx context.Context, inc Incident) (string, error) {
	evidence := inc.Evidence
	if fresh, landed, err := m.portsReading(ctx, inc); err != nil {
		return "", err
	} else if landed {
		evidence = fresh.Verdicts
	}
	relays, err := m.svc.List(ctx)
	if err != nil {
		return "", err
	}
	alive := 0
	for _, r := range relays {
		if r.Box == inc.Box && r.Status == StatusAlive {
			alive++
		}
	}
	doomed := make([]string, 0, len(inc.Evidence))
	for _, r := range relays {
		if r.Box != inc.Box || r.Status != StatusAlive {
			continue
		}
		if evidence[r.Name] == "ip_banned" {
			doomed = append(doomed, r.Name)
		}
	}
	if len(doomed) == 0 {
		return "", fmt.Errorf("%w: в уликах нет ни одного забаненного живого релея", ErrRejected)
	}
	if len(doomed) >= alive {
		return "", fmt.Errorf("%w: забанены все %d живых релеев машины — это не порты, а адрес", ErrRejected, alive)
	}
	for _, name := range doomed {
		if _, err := m.svc.MarkDead(ctx, name, DeathIPBanned); err != nil {
			return "", fmt.Errorf("снять %s с выдачи: %w", name, err)
		}
	}
	return fmt.Sprintf("снято с выдачи портов: %d, живыми осталось %d", len(doomed), alive-len(doomed)), nil
}

// restartEdge restarts the relay units the grid found stopped (cause
// edge_down, П9.6б).
//
// # The evidence says which units, and that is a rail
//
// Only the relays the grid recorded as `inactive` are restarted — a box's
// healthy relay is carrying people, and a repair that cut them to fix its
// neighbour would cost exactly what it saved. No inactive unit in the evidence
// means the incident was not what this playbook is for, and refusing beats
// restarting a whole box on a guess.
func (m *Mechanic) restartEdge(ctx context.Context, inc Incident) (string, error) {
	// The rail first, and the hands after: whether this incident is even what
	// the playbook is for does not depend on the hub having a provisioner,
	// and asking in that order is what makes the rail checkable without a
	// machine.
	dead := stoppedUnits(inc.Evidence)
	if len(dead) == 0 {
		return "", fmt.Errorf("%w: в уликах нет ни одного остановленного юнита", ErrRejected)
	}
	if m.prov == nil {
		return "", fmt.Errorf("%w: у хаба нет провизионера, поднимать юниты нечем", ErrRejected)
	}
	restarted, err := m.prov.RestartEdge(ctx, inc.Box, dead)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("юниты подняты и прошли допуск: %s", strings.Join(restarted, ", ")), nil
}

// stoppedUnits reads the relays the grid found stopped out of an incident's
// evidence (the "unit:<relay>" lines of GridSnapshot.Evidence), sorted.
//
// Only `inactive` counts. A unit the grid could not read is absent from the
// evidence entirely, and one it read as active is working — restarting either
// would cut live connections to fix nothing.
func stoppedUnits(evidence map[string]string) []string {
	var dead []string
	for key, state := range evidence {
		name, ok := strings.CutPrefix(key, "unit:")
		if !ok || state != "inactive" {
			continue
		}
		dead = append(dead, name)
	}
	sort.Strings(dead)
	return dead
}

// rotateDest moves the box's relays off a dead cover site onto the ones still
// answering (cause destination, П9.6б). The covers are dialled again inside
// the repair rather than read off the incident: a cover site is somebody
// else's server and can be back by now, in which case the repair refuses and
// the next probe round closes the incident on its own.
func (m *Mechanic) rotateDest(ctx context.Context, inc Incident) (string, error) {
	if m.prov == nil {
		return "", fmt.Errorf("%w: у хаба нет провизионера, трогать машину нечем", ErrRejected)
	}
	n, dropped, err := m.prov.RotateDest(ctx, inc.Box)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("прикрытия %s сняты, релеев переведено: %d", strings.Join(dropped, ", "), n), nil
}

// rotateReality gives the box's relays new Reality credentials on the same
// machine (cause fingerprint): new keys in the hub, new env on the box, the
// units restarted, admission re-probed, records re-signed and the holders told
// to come back for them.
func (m *Mechanic) rotateReality(ctx context.Context, inc Incident) (string, error) {
	if m.prov == nil {
		return "", fmt.Errorf("%w: у хаба нет провизионера, трогать машину нечем", ErrRejected)
	}
	rotated, err := m.prov.RotateReality(ctx, inc.Box)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ключи reality сменены на релеях: %d", rotated), nil
}

// evictDomain takes the blocked name off the box and gives the box a free
// one from the pool (cause domain_blocked, П9.11б): the name burned — suspect,
// with its second chance — and the box re-provisioned with the pooled name.
// Free and immediate, like the machine's eviction: the purchase of a new name
// is the domain pool's (buy_domain, under its own target and mode), never
// this repair's.
//
// With the pool dry the name is still taken off and the repair fails, saying
// so: the box waits, and its next attempt (after the cooldown) finds the name
// the pool's target bought meanwhile. Burning is idempotent — a box that
// carries no name any more has nothing to burn on the retry.
func (m *Mechanic) evictDomain(ctx context.Context, inc Incident) (string, error) {
	if m.prov == nil {
		return "", fmt.Errorf("%w: у хаба нет провизионера, разворачивать нечем", ErrRejected)
	}
	burned, err := m.svc.burnBoxDomainClear(ctx, inc.Box)
	if err != nil {
		return "", fmt.Errorf("сжечь домен: %w", err)
	}
	acc, err := m.svc.boxAccess(ctx, inc.Box)
	if err != nil {
		return "", err
	}
	head := "домен снят с выдачи"
	if burned != "" {
		head = fmt.Sprintf("домен %s снят с выдачи (под вопросом)", burned)
	}
	pooled, ok, err := m.svc.claimPooledDomain(ctx, m.svc.currentBuyer(), inc.Box, acc.address)
	if err != nil {
		return "", fmt.Errorf("%s; домен из пула: %w", head, err)
	}
	if !ok {
		return "", fmt.Errorf("%s; в пуле доменов нет свободного — машина ждёт домен%s",
			head, m.svc.domainPoolNote(ctx))
	}
	if err := m.prov.Reprovision(ctx, inc.Box); err != nil {
		return "", fmt.Errorf("%s, взят %s из пула; переразвернуть: %w", head, pooled, err)
	}
	return fmt.Sprintf("%s, машина переразвёрнута с %s из пула", head, pooled), nil
}

// rebootBox restarts the machine through the hoster and waits for it to answer
// (П9.2, playbook reboot_box): free, reversible, and it changes no address, so
// nobody is moved and nothing is handed out again.
func (m *Mechanic) rebootBox(ctx context.Context, inc Incident) (string, error) {
	if m.prov == nil {
		return "", fmt.Errorf("%w: у хаба нет провизионера, ждать ssh нечем", ErrRejected)
	}
	if err := m.prov.rebootAndWait(ctx, inc.Box); err != nil {
		return "", err
	}
	return "машина перезагружена, ssh отвечает, релеи снова прошли допуск", nil
}

// evictBox takes the machine out of the pool on suspicion (cause ip_banned,
// and provider_down once a reboot cannot cure it): its relays leave every
// batch at once and the holders refill from the rest of the pool, and the
// machine waits, still rented, for its second chance (secondchance.go).
//
// # Break before make, on purpose
//
// replace_box, which this replaced, admitted a new machine before the old
// one stopped carrying people (§4 rail 5, the pool's floor). That rail is for
// a machine that still works. One whose address every vantage reads as
// banned carries nobody: keeping it in the batches until a new machine is
// admitted keeps people's slots pointed at a dead address, which is the
// outage itself. The floor is the fleet's to restore (scale_up), and the wave
// fuse is what stops an eviction of the whole fleet on a misreading of ours.
func (m *Mechanic) evictBox(ctx context.Context, inc Incident) (string, error) {
	reason := inc.Cause
	if !validDeathReason(reason) {
		reason = DeathOther
	}
	relays, holders, err := m.svc.SuspectBox(ctx, inc.Box, reason)
	if err != nil {
		return "", err
	}
	policy, err := m.svc.DetectionPolicy(ctx)
	if err != nil {
		return "", err
	}
	c := policy.Box
	out := fmt.Sprintf("машина снята с выдачи: релеев %d, держателей переселено %d; второй шанс — проба раз в %s, "+
		"назад после %d чистых подряд, списание после %d проваленных или через %s",
		relays, holders, c.ProbeEvery, c.ReturnClean, c.DeadFails, c.DeadAfter)
	return out + m.svc.replacementNote(ctx, inc.Box), nil
}

// replacementNote says who replaces an evicted machine: the fleet, which
// sizes the pool by its load (П9.16) and so reads the eviction as a machine
// short — said where the owner reads it.
func (s *Service) replacementNote(ctx context.Context, box string) string {
	rental, err := s.boxRental(ctx, box)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("; замену добирает парк пула %s по нагрузке", FleetPool(rental.proto, rental.voiceRegion))
}

// finish closes the attempt's row, with what it cost.
//
// The cost is written by the repair that spent it and not derived afterwards:
// a machine's rent is a figure the hoster quoted at order time, and reading it
// back from a price list later would answer a different question.
func (m *Mechanic) finish(ctx context.Context, id uuid.UUID, costCents int, repairErr error) error {
	status, msg := repairDone, ""
	switch {
	case errors.Is(repairErr, errNoVerdict), errors.Is(repairErr, ErrBudget), errors.Is(repairErr, ErrNoRegistrar):
		status, msg = repairHeld, clip(repairErr.Error(), 512)
	case repairErr != nil:
		status, msg = repairFailed, clip(repairErr.Error(), 512)
	}
	_, err := m.svc.pool.Exec(ctx, `
		UPDATE entry_repairs SET status = $2, error = $3, cost_cents = $4, finished_at = now()
		WHERE id = $1 AND status = 'running'
	`, id, status, msg, costCents)
	return err
}

// Repair is one attempt as the console shows it.
type Repair struct {
	ID         uuid.UUID  `json:"id"`
	IncidentID uuid.UUID  `json:"incidentId"`
	Box        string     `json:"box"`
	Pool       string     `json:"pool,omitempty"`
	Cause      string     `json:"cause"`
	Playbook   string     `json:"playbook"`
	DecidedBy  string     `json:"decidedBy"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	CostCents  int        `json:"costCents"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Repairs lists the attempts made against one incident, oldest first — the
// order a person reads them in when asking why the hub gave up.
func (s *Service) Repairs(ctx context.Context, incident uuid.UUID) ([]Repair, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, incident_id, coalesce(box, ''), coalesce(pool, ''), cause, playbook,
		       decided_by, status, error, cost_cents, started_at, finished_at
		FROM entry_repairs WHERE incident_id = $1 ORDER BY started_at
	`, incident)
	if err != nil {
		return nil, fmt.Errorf("entry: repairs: %w", err)
	}
	defer rows.Close()
	out := []Repair{}
	for rows.Next() {
		var r Repair
		if err := rows.Scan(&r.ID, &r.IncidentID, &r.Box, &r.Pool, &r.Cause, &r.Playbook,
			&r.DecidedBy, &r.Status, &r.Error, &r.CostCents, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, fmt.Errorf("entry: scan repair: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// repairGrace is how long past its own timeout a repair may stay `running`
// before the pass calls its verdict lost, and how long an incident may stand
// `acting` after its last repair ended before the pass calls the close lost.
const repairGrace = 2 * time.Minute

// reapStuck ends what a lost write left standing (audit mines): a repair still
// `running` past repairTimeout — its goroutine ended and its verdict was
// never written, and the row holds its box's (or the fleet's) only lock — and
// an incident `acting` with no repair running, whose reopen or close was
// lost. The incident goes back to `open`: the rails then decide, as for any
// attempt that did not take, whether to try again or hand it over.
//
// AbandonRunningRepairs does the first at start-up for a hub that restarted;
// this is the same for a hub that did not.
func (s *Service) reapStuck(ctx context.Context) error {
	now := s.now()
	if _, err := s.pool.Exec(ctx, `
		UPDATE entry_repairs
		SET status = 'failed', error = 'исход ремонта не записан — снят по таймауту', finished_at = $2
		WHERE status = 'running' AND started_at < $1
	`, now.Add(-repairTimeout-repairGrace), now); err != nil {
		return fmt.Errorf("entry: reap repairs: %w", err)
	}
	// A failed last attempt is safe to reopen at once — carry reopens it
	// itself, and only a lost write leaves it acting. A done one waits out
	// the grace: carry closes it right after writing the verdict.
	if _, err := s.pool.Exec(ctx, `
		UPDATE entry_incidents i SET status = 'open', acted_at = NULL
		WHERE i.status = 'acting'
		  AND NOT EXISTS (SELECT 1 FROM entry_repairs r WHERE r.incident_id = i.id AND r.status = 'running')
		  AND coalesce((SELECT r.status IN ('failed', 'held') OR r.finished_at < $1
		                FROM entry_repairs r WHERE r.incident_id = i.id
		                ORDER BY r.started_at DESC LIMIT 1),
		               i.acted_at < $1)
	`, now.Add(-repairGrace)); err != nil {
		return fmt.Errorf("entry: reap incidents: %w", err)
	}
	return nil
}

// AbandonRunningRepairs fails every repair left running by a previous process,
// for the same reason entry_jobs does it: the goroutine that was carrying it
// is gone, and a row that says `running` forever holds the box's only lock.
func (s *Service) AbandonRunningRepairs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_repairs
		SET status = 'failed', error = 'хаб перезапустился посреди ремонта', finished_at = now()
		WHERE status = 'running'
	`)
	return tag.RowsAffected(), err
}

// setRelayIdentity writes a relay's new identity. Used by the re-key repair;
// the relay keeps its name, its state and its grants — what changed is the
// secrets behind them.
func (s *Service) setRelayIdentity(ctx context.Context, name string, id Identity) error {
	ct, err := sealIdentity(id)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_relays SET identity_ct = $2 WHERE name = $1 AND status <> 'dead'
	`, name, ct)
	if err != nil {
		return fmt.Errorf("entry: set identity of %s: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrNoSuchRelay, name)
	}
	return nil
}

// nudgeHolders tells the accounts holding these relays to ask for their batch
// again (П6.11). A no-op with no notifier wired, and never an error the caller
// has to care about beyond logging: the records still travel in the bundle,
// so the worst case is a client that waits for its next fetch.
func (s *Service) nudgeHolders(ctx context.Context, relays []string) error {
	if s.notify == nil || len(relays) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT g.account_id
		FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id
		WHERE r.name = ANY($1) AND g.revoked_at IS NULL
	`, relays)
	if err != nil {
		return fmt.Errorf("entry: holders of %v: %w", relays, err)
	}
	holders, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return fmt.Errorf("entry: holders of %v: %w", relays, err)
	}
	if len(holders) > 0 {
		s.notify.RelaysChanged(holders)
	}
	return nil
}
