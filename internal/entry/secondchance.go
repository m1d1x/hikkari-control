package entry

// secondchance.go is the middle step of every consumable's life (roadmap 42
// П9.11, migration 073): a domain, a machine and a cover site are working,
// suspect, or dead.
//
// # Why three steps and not two
//
// Two meant the eviction threshold wrote things off. It is a fast threshold on
// purpose — a few bad rounds a minute apart — because people are already
// hurting when it trips. But a few bad minutes are also a flap, an outage of
// ours, a list that reached one operator for an hour. So the threshold now
// only evicts: the thing leaves the pool at once and the pool covers for it,
// and the question "is it banned" is answered later, slowly, by a reprobe
// apart from the routine rounds. Clean on enough separate occasions — back in
// the pool. Failed enough, or never cleared in time — dead, which is final:
// the machine handed back, the name not renewed, the cover struck.
//
// Returning is stricter than evicting. Eviction is minutes; a return is
// returnClean clean reprobes in a row, a probe interval apart, and the check
// in 073 keeps the first and the last at least 20 hours apart whatever the
// knobs say: one clean read can be a path the filter was not on that time,
// and censor lists are refreshed daily.
//
// # One ledger, three kinds
//
// The burned domain of §11 was the first case and carried its own counters
// on entry_domains. Those moved here (073), so the rules — the streak, the
// failures, the deadline, death being once — are written down once. What
// differs per kind is only how a reprobe is taken and what returning or dying
// does to the thing itself:
//
//   - domain: reprobed by a vantage as SNI against a live XHTTP address
//     (reprobe.go). Back to 'owned', or 'dead' (never drawn, not renewed).
//   - box: its relays reprobed by a vantage like an admission (reprobe.go).
//     Back to 'alive', or handed back at the hoster (the mechanic's pass,
//     retireDeadSuspects).
//   - cover: dialled from the hub, as the escalated grid does. Returned, the
//     detector may write it back onto relays (add_covers); dead, it leaves the
//     proven set.
//
// # The defaults, and why
//
//   - domain — reprobe daily, back after 3 clean, dead after 14 failed or 30
//     days. The 072 rule for returning, unchanged. A name is already paid for
//     until its renewal, so keeping it suspect costs a handshake a day; what
//     the deadline decides is whether it is worth renewing, and a block by
//     name that has not lifted in a month is a list entry, not a flap.
//   - box — reprobe daily, back after 2 clean, dead after 2 failed or 3 days.
//     A suspect machine is rent by the hour that carries nobody: three days
//     is about a tenth of a month's rent, and two failed reads a day apart
//     mean the ban outlived a list refresh — it is real. Two clean days are
//     the cheapest return the 20-hour rule allows.
//   - cover — reprobe every 12 hours, back after 3 clean, dead after 20
//     failed or 14 days. A cover is somebody else's server, free to keep
//     asking about, and a proven one (П4: it works through ТСПУ) is worth
//     waiting two weeks for; three clean reads span a day, so an outage that
//     flaps is not written back onto relays.
//
// # Why the Reality key has no second step
//
// A key costs nothing to mint and cannot be served beside its replacement on
// the same relay: a suspect key would be a burned secret kept to restore what
// a fresh one gives for free. rotate_reality replaces it, and the old one is
// dead at once — two steps are its whole life, by design. Ports are the same:
// add_ports raises a fresh one for nothing.
//
// # Shared with the repairs on the spot, but not their state
//
// edge_down and provider_down are our own outages, cured on the spot
// (restart_edge, reboot_box) with the machine left where it is: its relays
// stay `alive`, the cure takes minutes, and the next round says whether it
// worked. That is not a suspicion and needs no state. A suspect machine is
// the other thing — the hub waits days for the censor to let go — and it has
// a relay status of its own, `suspect`, rather than sharing `draining`:
// draining is the rollout's, and a rollout cut off by a restart puts every
// draining relay back in the pool (rollout.go), which would return a suspect
// machine on the strength of a restart. Where the two meet is the climb: a
// provider_down the reboot could not cure is evicted like a banned machine,
// because "the hoster's outage may end" is exactly a ban not proven.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// Kinds of suspect (entry_suspects.kind).
const (
	SuspectDomain = "domain"
	SuspectBox    = "box"
	SuspectCover  = "cover"
)

// SuspectKinds lists them in the order the policy row stores their knobs.
var SuspectKinds = []string{SuspectDomain, SuspectBox, SuspectCover}

// Ledger states (entry_suspects.status).
const (
	suspectOpen     = "suspect"
	suspectReturned = "returned"
	suspectDead     = "dead"
	// suspectWithdrawn: the machine was reinstalled by the operator while
	// the sentence stood (Service.setHostKey) — neither carried out nor a
	// loss (migration 078).
	suspectWithdrawn = "withdrawn"
)

// StatusSuspect: a relay of a machine under suspicion (migration 073).
const StatusSuspect = "suspect"

// revokeSuspect: a grant revoked because its relay's machine is suspect.
const revokeSuspect = "suspect"

// SecondChance is one kind's knobs (migration 073).
type SecondChance struct {
	// ProbeEvery is how often one suspect is reprobed.
	ProbeEvery time.Duration
	// ReturnClean is how many clean reprobes in a row bring it back.
	ReturnClean int
	// DeadFails is how many failed reprobes write it off.
	DeadFails int
	// DeadAfter is how long it may stay suspect at all.
	DeadAfter time.Duration
}

// Chance is the knobs of one kind.
func (p DetectionPolicy) Chance(kind string) SecondChance {
	switch kind {
	case SuspectDomain:
		return p.Domain
	case SuspectBox:
		return p.Box
	default:
		return p.Cover
	}
}

// SecondChancePatch is what the console sends for one kind; seconds on the
// wire, like every duration of the policy.
type SecondChancePatch struct {
	ProbeEverySeconds *int `json:"probeEverySeconds"`
	ReturnClean       *int `json:"returnClean"`
	DeadFails         *int `json:"deadFails"`
	DeadAfterSeconds  *int `json:"deadAfterSeconds"`
}

// secondChanceWire is one kind as the console reads it.
func secondChanceWire(c SecondChance) map[string]int {
	return map[string]int{
		"probeEverySeconds": int(c.ProbeEvery / time.Second),
		"returnClean":       c.ReturnClean,
		"deadFails":         c.DeadFails,
		"deadAfterSeconds":  int(c.DeadAfter / time.Second),
	}
}

// reading is what one reprobe said about a suspect.
type reading int

const (
	// readNothing: the reprobe said nothing about it (our network, a probe
	// that could not run). Neither streak nor failures move.
	readNothing reading = iota
	readClean
	readFailed
)

const (
	// openSuspicion records an eviction. A thing already suspect stays as it
	// is (its streak is not reset by a second eviction: the unique index, and
	// DO NOTHING), and a dead one never gets another row — death is once.
	openSuspicion = `
		INSERT INTO entry_suspects (kind, subject, reason)
		SELECT $1, $2, $3
		WHERE NOT EXISTS (SELECT 1 FROM entry_suspects
		                  WHERE kind = $1 AND subject = $2 AND status = 'dead')
		ON CONFLICT DO NOTHING`

	// markDueSuspects takes the suspects of a kind not reprobed for $2
	// seconds (counted from the eviction for one never asked), the
	// longest-unasked first, and stamps them asked. A domain the owner
	// excluded is never drawn again, so it is not asked about either.
	markDueSuspects = `
		UPDATE entry_suspects SET last_probe_at = now()
		WHERE id IN (
			SELECT s.id FROM entry_suspects s
			WHERE s.kind = $1 AND s.status = 'suspect'
			  AND coalesce(s.last_probe_at, s.since) < now() - make_interval(secs => $2)
			  AND (s.kind <> 'domain' OR NOT EXISTS (
			      SELECT 1 FROM entry_domain_exclusions x
			      WHERE s.subject = x.domain OR right(s.subject, length(x.domain) + 1) = '.' || x.domain))
			ORDER BY coalesce(s.last_probe_at, s.since)
			FOR UPDATE OF s SKIP LOCKED
			LIMIT $3
		)
		RETURNING subject`

	// judgeSuspect applies one reading ($3: clean) to an open suspicion. The
	// returnClean'th clean reading in a row returns it; the deadFails'th
	// failed one kills it unless $6 forbids dying just now (the last proven
	// cover). Returns the status it ends in.
	judgeSuspect = `
		UPDATE entry_suspects SET
			clean_streak  = CASE WHEN $3 THEN clean_streak + 1 ELSE 0 END,
			failed_probes = failed_probes + CASE WHEN $3 THEN 0 ELSE 1 END,
			status = CASE WHEN $3 AND clean_streak + 1 >= $4 THEN 'returned'
			              WHEN NOT $3 AND failed_probes + 1 >= $5 AND $6 THEN 'dead'
			              ELSE status END,
			ended_at = CASE WHEN ($3 AND clean_streak + 1 >= $4)
			                  OR (NOT $3 AND failed_probes + 1 >= $5 AND $6) THEN now() END,
			outcome = CASE WHEN $3 AND clean_streak + 1 >= $4 THEN $7
			               WHEN NOT $3 AND failed_probes + 1 >= $5 AND $6 THEN $8
			               ELSE outcome END
		WHERE kind = $1 AND subject = $2 AND status = 'suspect'
		RETURNING status`

	// expireSuspects writes off the suspects of a kind that outlived the
	// deadline ($2 seconds since the eviction), except the subjects in $4.
	expireSuspects = `
		UPDATE entry_suspects SET status = 'dead', ended_at = now(), outcome = $3
		WHERE kind = $1 AND status = 'suspect' AND since < now() - make_interval(secs => $2)
		  AND NOT (subject = ANY($4))
		RETURNING subject`

	// Returning and dying, as they touch the thing itself. A domain is out of
	// the pool as 'burned' and comes back 'owned'; dead, it is never drawn
	// and never renewed. A machine's relays come back 'alive'; its death is
	// the mechanic's to carry out (retireDeadSuspects), because handing a
	// machine back talks to the hoster.
	returnDomain   = `UPDATE entry_domains SET status = 'owned', updated_at = now() WHERE domain = $1 AND status = 'burned'`
	killDomain     = `UPDATE entry_domains SET status = 'dead', updated_at = now() WHERE domain = $1 AND status = 'burned'`
	returnBoxRelay = `
		UPDATE entry_relays r SET status = 'alive'
		FROM entry_boxes b
		WHERE r.box = $1 AND r.status = 'suspect' AND b.name = r.box AND b.retired_at IS NULL
		RETURNING r.name`

	// evictBoxRelays takes every relay of a machine that physically carries
	// traffic out of the pool as suspect.
	evictBoxRelays = `
		UPDATE entry_relays SET status = 'suspect'
		WHERE box = $1 AND status IN ('alive', 'testing', 'draining')
		RETURNING id`

	// withdrawBoxSentence ends a machine's open or dead second chance
	// without carrying it out: the machine it was passed on was reinstalled
	// (Service.setHostKey).
	withdrawBoxSentence = `
		UPDATE entry_suspects SET status = 'withdrawn', ended_at = now(), outcome = $2
		WHERE kind = 'box' AND subject = $1 AND status IN ('suspect', 'dead')`

	// readmitBoxRelays puts the relays an eviction took out of the pool back
	// to admission: the deploy that follows judges them afresh.
	readmitBoxRelays = `
		UPDATE entry_relays SET status = 'pending'
		WHERE box = $1 AND status = 'suspect'`

	// deadBoxesInService: machines whose second chance failed and that are
	// still not handed back.
	deadBoxesInService = `
		SELECT s.subject, s.reason FROM entry_suspects s
		JOIN entry_boxes b ON b.name = s.subject
		WHERE s.kind = 'box' AND s.status = 'dead' AND b.retired_at IS NULL
		  AND NOT ` + boxInOperatorsHands + `
		ORDER BY s.ended_at`

	// deadBoxesHandedBack counts the machines written off by their second
	// chance and retired within the last $1 seconds — the wave of handbacks.
	deadBoxesHandedBack = `
		SELECT count(DISTINCT b.name) FROM entry_boxes b
		JOIN entry_suspects s ON s.kind = 'box' AND s.subject = b.name AND s.status = 'dead'
		WHERE b.retired_at >= now() - make_interval(secs => $1)`

	// coverStates: every cover site the ledger has an opinion on — under
	// suspicion, or struck.
	coverStates = `
		SELECT subject, status FROM entry_suspects
		WHERE kind = 'cover' AND status IN ('suspect', 'dead')`

	selectSuspects = `
		SELECT id, kind, subject, reason, status, since, last_probe_at, clean_streak, failed_probes,
		       ended_at, outcome
		FROM entry_suspects
		WHERE status = 'suspect' OR ended_at >= now() - interval '30 days'
		ORDER BY status = 'suspect' DESC, coalesce(ended_at, since) DESC
		LIMIT $1`
)

// Suspect is one suspicion as the console shows it.
type Suspect struct {
	ID           uuid.UUID  `json:"id"`
	Kind         string     `json:"kind"`
	Subject      string     `json:"subject"`
	Reason       string     `json:"reason"`
	Status       string     `json:"status"`
	Since        time.Time  `json:"since"`
	LastProbeAt  *time.Time `json:"lastProbeAt,omitempty"`
	CleanStreak  int        `json:"cleanStreak"`
	FailedProbes int        `json:"failedProbes"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
	Outcome      string     `json:"outcome,omitempty"`
}

// Suspects lists what is under suspicion now, then what ended in the last
// thirty days, up to limit.
func (s *Service) Suspects(ctx context.Context, limit int) ([]Suspect, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, selectSuspects, limit)
	if err != nil {
		return nil, fmt.Errorf("entry: suspects: %w", err)
	}
	defer rows.Close()
	out := []Suspect{}
	for rows.Next() {
		var x Suspect
		if err := rows.Scan(&x.ID, &x.Kind, &x.Subject, &x.Reason, &x.Status, &x.Since, &x.LastProbeAt,
			&x.CleanStreak, &x.FailedProbes, &x.EndedAt, &x.Outcome); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// suspect opens a suspicion inside the caller's transaction, the one that
// takes the thing out of the pool: the two never disagree.
func suspect(ctx context.Context, tx pgx.Tx, kind, subject, reason string) error {
	_, err := tx.Exec(ctx, openSuspicion, kind, subject, reason)
	return err
}

// markDue stamps up to limit suspects of a kind due for a reprobe as asked,
// and returns them.
func markDue(ctx context.Context, tx pgx.Tx, kind string, every time.Duration, limit int) ([]string, error) {
	rows, err := tx.Query(ctx, markDueSuspects, kind, every.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// judge folds one reading into a suspicion and carries out what it decided,
// in the caller's transaction. mayDie false keeps it suspect however many
// reprobes failed (the last proven cover). Returns the status it ends in, ""
// when it was not suspect any more.
//
// A return asks for clean reprobes on separate days, and not yet for clean
// reprobes from separate observation points (plan §12, "из разных точек"):
// the grid has one vantage, and a rule needing two would return nothing.
// Owner's decision 27.10: it waits for the second vantage — then the clean
// streak counts per point, and a return wants both.
func judge(ctx context.Context, tx pgx.Tx, kind, subject string, r reading, c SecondChance, mayDie bool) (string, error) {
	if r == readNothing {
		return suspectOpen, nil
	}
	var status string
	err := tx.QueryRow(ctx, judgeSuspect, kind, subject, r == readClean, c.ReturnClean, c.DeadFails, mayDie,
		fmt.Sprintf("чистых проб подряд: %d — возвращён в пул", c.ReturnClean),
		fmt.Sprintf("проваленных проб: %d — списан", c.DeadFails),
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // returned or written off meanwhile
	}
	if err != nil {
		return "", fmt.Errorf("entry: judge %s %s: %w", kind, subject, err)
	}
	if err := settle(ctx, tx, kind, subject, status); err != nil {
		return "", err
	}
	return status, nil
}

// settle does to the thing what its suspicion ended in.
func settle(ctx context.Context, tx pgx.Tx, kind, subject, status string) error {
	var err error
	switch {
	case kind == SuspectDomain && status == suspectReturned:
		_, err = tx.Exec(ctx, returnDomain, subject)
	case kind == SuspectDomain && status == suspectDead:
		_, err = tx.Exec(ctx, killDomain, subject)
	case kind == SuspectBox && status == suspectReturned:
		_, err = tx.Exec(ctx, returnBoxRelay, subject)
	}
	if err != nil {
		return fmt.Errorf("entry: settle %s %s as %s: %w", kind, subject, status, err)
	}
	return nil
}

// SecondChancePass is one round of the slow loop: write off what outlived
// its deadline, queue the reprobes that are due (domains and machines go to a
// vantage, covers are dialled here). Returns how many things it wrote off and
// how many it asked about.
func (s *Service) SecondChancePass(ctx context.Context) (dead, asked int, err error) {
	policy, err := s.DetectionPolicy(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, kind := range SuspectKinds {
		n, err := s.expire(ctx, kind, policy.Chance(kind))
		if err != nil {
			return dead, asked, err
		}
		dead += n
	}
	n, err := s.ReprobeBurnedDomains(ctx)
	if err != nil {
		return dead, asked, err
	}
	asked += n
	if n, err = s.reprobeSuspectBoxes(ctx, policy.Box); err != nil {
		return dead, asked, err
	}
	asked += n
	if n, err = s.reprobeSuspectCovers(ctx, policy.Cover); err != nil {
		return dead, asked, err
	}
	asked += n
	return dead, asked, nil
}

// expire writes off one kind's suspects past the deadline, each settled in
// the same transaction.
func (s *Service) expire(ctx context.Context, kind string, c SecondChance) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	spared := []string{}
	if kind == SuspectCover {
		if spared, err = lastProvenCovers(ctx, tx); err != nil {
			return 0, err
		}
	}
	rows, err := tx.Query(ctx, expireSuspects, kind, c.DeadAfter.Seconds(),
		fmt.Sprintf("не вернулся за %s — списан", c.DeadAfter), spared)
	if err != nil {
		return 0, err
	}
	gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, subject := range gone {
		if err := settle(ctx, tx, kind, subject, suspectDead); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if len(gone) > 0 {
		applog.Info(ctx, "entry", "suspects written off at the deadline",
			applog.F("kind", kind), applog.F("count", len(gone)), applog.F("deadline", c.DeadAfter.String()))
	}
	return len(gone), nil
}

// SecondChanceLoop runs SecondChancePass every `every` until ctx ends. The
// tick only looks; the rate per suspect is kept by the ledger, so a tick
// shorter than any interval costs a few cheap queries and a restart does not
// reset anything.
func (s *Service) SecondChanceLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := s.SecondChancePass(ctx); err != nil {
				applog.Error(ctx, "entry", "second chance pass failed", applog.F("err", err.Error()))
			}
		}
	}
}

// ---- machines ----

// SuspectBox evicts a machine (playbook evict_box): every relay on it that
// carries traffic leaves the pool as suspect, their live grants are revoked so
// the holders' next batch fills the slots from the rest of the pool, and the
// machine's second chance opens — one transaction. The machine stays rented:
// whether it is handed back is the second chance's to say. Returns the relays
// evicted and how many holders were told.
func (s *Service) SuspectBox(ctx context.Context, box, reason string) (relays, holders int, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if lb, ok, err := s.repo.lockBox(ctx, tx, box); err != nil {
		return 0, 0, err
	} else if !ok {
		return 0, 0, fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	} else if lb.retired {
		return 0, 0, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	rows, err := tx.Query(ctx, evictBoxRelays, box)
	if err != nil {
		return 0, 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, 0, err
	}
	if len(ids) == 0 {
		return 0, 0, fmt.Errorf("%w: у машины %s нет релеев в выдаче — снимать нечего", ErrRejected, box)
	}
	held, err := s.repo.revokeGrants(ctx, tx, ids, revokeSuspect)
	if err != nil {
		return 0, 0, err
	}
	if err := suspect(ctx, tx, SuspectBox, box, reason); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	applog.Info(ctx, "entry", "machine evicted on suspicion",
		applog.F("box", box), applog.F("reason", reason),
		applog.F("relays", len(ids)), applog.F("holders", len(held)))
	if s.notify != nil && len(held) > 0 {
		s.notify.RelaysChanged(held)
	}
	return len(ids), len(held), nil
}

// retireDeadSuspects hands back every machine whose second chance failed and
// that is still on the books: retired with the cause that evicted it, then
// deleted at the hoster (what stops the charge) or, rented by hand, wiped over
// ssh — the one step left to the owner is cancelling that rent. A retire job
// per machine, so the box's one-job lock keeps a repair off it meanwhile.
// Returns the machines handed back.
//
// # The eviction's fuse holds the handback too
//
// The eviction obeys the wave and its pause; the handback it leads to did not
// (audit 42 recheck НВ1): a slot's lying ip_banned evicted three machines,
// blew the fuse on the fourth — and two days later this pass deleted the
// three at the hoster with the fuse still standing. So a pause on any cause
// of the eviction's spend group holds the handback of a machine evicted for
// that cause, and more handbacks in the wave window than WaveLimit blow the
// same fuse: a run of write-offs is the same "new rule at the censor, or our
// bug" a run of evictions is, and it must not be cheaper than a retire a
// person makes with a passkey touch.
func (p *Provisioner) retireDeadSuspects(ctx context.Context) ([]string, error) {
	svc := p.svc
	policy, err := svc.DetectionPolicy(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := svc.pool.Query(ctx, deadBoxesInService)
	if err != nil {
		return nil, fmt.Errorf("entry: dead suspects: %w", err)
	}
	type dead struct{ box, reason string }
	var due []dead
	for rows.Next() {
		var d dead
		if err := rows.Scan(&d.box, &d.reason); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, d := range due {
		cause := d.reason
		if cause == "" {
			cause = CauseIPBanned
		}
		if _, paused, err := svc.anyCausePaused(ctx, pauseCauses(cause, PlaybookEvictBox)); err != nil {
			return out, err
		} else if paused {
			continue // a person has not looked at the wave yet
		}
		var wave int
		if err := svc.pool.QueryRow(ctx, deadBoxesHandedBack, policy.WaveWindow.Seconds()).Scan(&wave); err != nil {
			return out, fmt.Errorf("entry: handback wave: %w", err)
		}
		if wave >= policy.WaveLimit {
			why := fmt.Sprintf("%s: за %s второй шанс списал %d машины — сдачу остальных держу до того, как посмотрит человек",
				wavePrefix, policy.WaveWindow, wave)
			if err := svc.PauseCause(ctx, cause, why, wave); err != nil {
				return out, err
			}
			continue
		}
		reason := d.reason
		if !validDeathReason(reason) {
			reason = DeathOther
		}
		err := p.execJob(ctx, JobRetire, d.box, uuid.Nil, func(j *jobRun) error {
			j.step("retire")
			j.log("второй шанс провален (%s) — сдаю машину", d.reason)
			return j.autoHandBack(reason)
		})
		if errors.Is(err, ErrBoxBusy) || errors.Is(err, errOperatorsMachine) {
			continue // a job holds it, or the operator does; the next pass looks again
		}
		if err != nil {
			applog.Error(ctx, "entry", "hand back of a dead suspect failed", applog.F("box", d.box), applog.F("err", err.Error()))
			continue
		}
		out = append(out, d.box)
	}
	return out, nil
}

// boxReading reads a machine's reprobe: clean when every relay that was
// judged reads healthy, failed when any reads blocked, nothing when no relay
// could be judged. Strict on purpose — a machine half of whose ports answer
// is not the machine that was evicted coming back.
func boxReading(verdicts map[string]string, relays []string) reading {
	judged, clean := 0, 0
	for _, r := range relays {
		switch v := verdicts[r]; v {
		case "", "unknown", "network_down", "probe_failed":
			continue
		case "clean", "reality_wins":
			judged++
			clean++
		default:
			judged++
		}
	}
	switch {
	case judged == 0:
		return readNothing
	case clean == judged:
		return readClean
	default:
		return readFailed
	}
}

// ---- covers ----

// suspectCovers records the cover sites rotate_dest dropped as suspect. A
// cover is the pool's, not the box's: one row however many boxes dropped it.
func (s *Service) suspectCovers(ctx context.Context, hosts []string) error {
	if len(hosts) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, h := range hosts {
		if err := suspect(ctx, tx, SuspectCover, h, CauseDestination); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// coverLedger is what the ledger says about cover sites: suspect or dead by
// host; a host absent from it is usable.
type coverLedger map[string]string

// coverLedger reads it.
func (s *Service) coverLedger(ctx context.Context) (coverLedger, error) {
	return readCoverLedger(ctx, s.pool)
}

func readCoverLedger(ctx context.Context, q db.DBTX) (coverLedger, error) {
	rows, err := q.Query(ctx, coverStates)
	if err != nil {
		return nil, fmt.Errorf("entry: cover ledger: %w", err)
	}
	defer rows.Close()
	out := coverLedger{}
	for rows.Next() {
		var host, status string
		if err := rows.Scan(&host, &status); err != nil {
			return nil, err
		}
		out[host] = status
	}
	return out, rows.Err()
}

// usable says a cover may be written onto a relay: nothing stands against it.
func (l coverLedger) usable(host string) bool { return l[host] == "" }

// proven is the proven covers that are not struck, in race order: the set a
// relay is measured against and brought back to.
func (l coverLedger) proven() []string {
	out := make([]string, 0, len(DefaultCovers))
	for _, c := range DefaultCovers {
		if l[c] != suspectDead {
			out = append(out, c)
		}
	}
	return out
}

// forNewRelay is what a new relay is born with: the proven covers nothing
// stands against, in race order — a suspect is out of service at once
// (П9.11), the same rule add_covers keeps with usable. When every proven cover
// is suspect, the suspects are the last there are (lastProvenCovers keeps them
// from being struck) and the relay still races them rather than none.
func (l coverLedger) forNewRelay() []string {
	proven := l.proven()
	clean := slices.DeleteFunc(slices.Clone(proven), func(c string) bool { return !l.usable(c) })
	if len(clean) == 0 {
		return proven
	}
	return clean
}

// lastProvenCovers is the covers that may not die just now: when no proven
// cover is free of suspicion, the suspects are the last of them, and striking
// them would leave relays with nothing to race — a world where every cover is
// dead is our network's outage, not theirs.
func lastProvenCovers(ctx context.Context, q db.DBTX) ([]string, error) {
	l, err := readCoverLedger(ctx, q)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(DefaultCovers, l.usable) {
		return []string{}, nil
	}
	return slices.Clone(DefaultCovers), nil
}

// reprobeSuspectCovers dials the suspect covers that are due, from the hub —
// the vantage of the escalated grid, and the one rotate_dest judged them
// from — and folds each answer into the ledger. Returns how many it dialled.
func (s *Service) reprobeSuspectCovers(ctx context.Context, c SecondChance) (int, error) {
	// Stamped asked first and committed: the dials take seconds each, and a
	// transaction must not stay open across somebody else's server.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	hosts, err := markDue(ctx, tx, SuspectCover, c.ProbeEvery, len(DefaultCovers)*4)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil || len(hosts) == 0 {
		return 0, err
	}

	dial := s.coverDialer()
	answered := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		answered[h] = dial(h)
	}

	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	spared, err := lastProvenCovers(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, h := range hosts {
		r := readFailed
		if answered[h] {
			r = readClean
		}
		status, err := judge(ctx, tx, SuspectCover, h, r, c, !slices.Contains(spared, h))
		if err != nil {
			return 0, err
		}
		if status == suspectReturned || status == suspectDead {
			applog.Info(ctx, "entry", "cover second chance ended", applog.F("cover", h), applog.F("status", status))
		}
	}
	return len(hosts), tx.Commit(ctx)
}
