package entry

// incidents.go is the incident as a thing that happened (roadmap 42 П9.5):
// born from a diagnosis, decided by a playbook or a person, closed when the
// cause is gone.
//
// # Why an incident, when there is already a diagnosis
//
// A diagnosis is recomputed from the probes every time anyone looks, and that
// is right for a diagnosis — it should say what the evidence says now. An
// incident is the opposite. What the hub believed at 14:02, what it decided,
// whether a person overrode it and what it cost cannot be derived from the
// current state, and they are exactly the dataset that later says whether the
// autopilot's choices were the right ones.
//
// # Opening one is idempotent
//
// The detector may run every minute and say the same thing every minute. One
// open incident per (box, cause), enforced by the index, so a box whose IP is
// banned does not accumulate an incident per probe round.
//
// # This file decides nothing
//
// Which playbook fits a cause, and whether the hub may run it without asking,
// is the dispatcher's (playbooks.go). This one only records.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Incident states.
const (
	// IncidentOpen: the hub believes the cause is true now.
	IncidentOpen = "open"
	// IncidentActing: a playbook is running against it.
	IncidentActing = "acting"
	// IncidentResolved: the cause is gone, by repair or on its own.
	IncidentResolved = "resolved"
	// IncidentDismissed: a person said it is not worth acting on.
	IncidentDismissed = "dismissed"
)

// How a decision was reached. The dataset П9.8 reads to decide whether a cause
// has earned the right to be repaired without asking.
const (
	DecidedAuto   = "auto"
	DecidedAsk    = "ask"
	DecidedManual = "manual"
)

// admissionWindow is how long after an incident closed a healthy box still
// counts as that incident's replacement having landed. An hour: long enough
// for a new machine to be provisioned, probed and handed out, short enough
// that a box which is simply fine today cannot be read as proof of a repair
// made yesterday.
const admissionWindow = time.Hour

// ErrNoSuchIncident: no incident under that id, or not in a state the call
// needs.
var ErrNoSuchIncident = errors.New("entry: no such incident")

// Incident is one thing that went wrong with a box — or, for a fleet
// shortage (fleet.go), with a pool: exactly one of Box and Pool is set.
type Incident struct {
	ID  uuid.UUID `json:"id"`
	Box string    `json:"box"`
	// Pool is the pool a fleet incident is about ("<proto>" or
	// "hysteria2:<region>"), empty on an incident about a box.
	Pool      string            `json:"pool,omitempty"`
	Cause     string            `json:"cause"`
	Status    string            `json:"status"`
	Playbook  string            `json:"playbook,omitempty"`
	DecidedBy string            `json:"decidedBy,omitempty"`
	Advice    string            `json:"advice,omitempty"`
	Evidence  map[string]string `json:"evidence,omitempty"`
	CostCents int               `json:"costCents"`
	Outcome   string            `json:"outcome,omitempty"`
	// Handoff is why the hub stopped working on this by itself — three failed
	// attempts, a wave of this cause, no playbook at all. Empty while the hub
	// has not given up. It is what moves an incident into the console's queue
	// even when its playbook says `auto`.
	Handoff  string     `json:"handoff,omitempty"`
	OpenedAt time.Time  `json:"openedAt"`
	ActedAt  *time.Time `json:"actedAt,omitempty"`
	ClosedAt *time.Time `json:"closedAt,omitempty"`
	// AdmittedAt is when the replacement passed a probe and entered the pool —
	// the last stage of the console's timeline, and a later moment than
	// ClosedAt because a repair closes optimistically (see MarkAdmitted).
	AdmittedAt *time.Time `json:"admittedAt,omitempty"`
	// ReplacedBy is the machine a `scale_up` added to Pool (before П9.11 also
	// the one a `replace_box` put in instead of Box). Admission is then that
	// machine reading healthy: a pool has no machine of its own to read
	// (MarkAdmitted).
	ReplacedBy string `json:"replacedBy,omitempty"`
}

// incidentColumns is the select list every reader of an incident uses, in
// the order [Incident.fields] scans it. The subject columns are coalesced:
// one of the two is always NULL (migration 071).
const incidentColumns = `id, coalesce(box, ''), coalesce(pool, ''), cause, status, playbook,
	decided_by, advice, evidence, cost_cents, outcome, handoff, opened_at, acted_at,
	closed_at, admitted_at, coalesce(replaced_by, '')`

// fields is the scan targets for incidentColumns; raw receives the evidence.
func (inc *Incident) fields(raw *[]byte) []any {
	return []any{&inc.ID, &inc.Box, &inc.Pool, &inc.Cause, &inc.Status, &inc.Playbook,
		&inc.DecidedBy, &inc.Advice, raw, &inc.CostCents, &inc.Outcome, &inc.Handoff,
		&inc.OpenedAt, &inc.ActedAt, &inc.ClosedAt, &inc.AdmittedAt, &inc.ReplacedBy}
}

// Open records a diagnosis as an incident, or returns the open one that
// already stands for this (box, cause).
//
// Idempotent on purpose: the detector runs on a timer and must be able to
// repeat itself without the console filling with duplicates. A diagnosis that
// needs nobody's attention opens nothing and is not an error — most boxes are
// fine most of the time, and saying so must be free.
func (s *Service) Open(ctx context.Context, d Diagnosis) (*Incident, error) {
	if !NeedsAttention(d.Cause) {
		return nil, nil
	}
	evidence, err := json.Marshal(d.Verdicts)
	if err != nil {
		return nil, fmt.Errorf("entry: incident evidence: %w", err)
	}
	var inc Incident
	var raw []byte
	err = s.pool.QueryRow(ctx, `
		INSERT INTO entry_incidents (box, cause, advice, evidence, opened_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (box, cause) WHERE status IN ('open', 'acting')
		DO UPDATE SET box = EXCLUDED.box
		RETURNING `+incidentColumns,
		d.Box, d.Cause, d.Advice, evidence, s.now()).Scan(inc.fields(&raw)...)
	if err != nil {
		return nil, fmt.Errorf("entry: open incident: %w", err)
	}
	if err := json.Unmarshal(raw, &inc.Evidence); err != nil {
		return nil, fmt.Errorf("entry: incident evidence: %w", err)
	}
	return &inc, nil
}

// openPool is Open for an incident about a pool rather than a box (fleet.go),
// idempotent per (pool, cause) the same way. The standing incident keeps the
// advice and evidence it was opened with: a record does not change its mind
// afterwards, and the fleet pass re-reads the numbers every round anyway.
func (s *Service) openPool(ctx context.Context, pool, cause, advice string, evidence map[string]string) (*Incident, error) {
	ev, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("entry: incident evidence: %w", err)
	}
	var inc Incident
	var raw []byte
	err = s.pool.QueryRow(ctx, `
		INSERT INTO entry_incidents (pool, cause, advice, evidence, opened_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (pool, cause) WHERE status IN ('open', 'acting')
		DO UPDATE SET pool = EXCLUDED.pool
		RETURNING `+incidentColumns,
		pool, cause, advice, ev, s.now()).Scan(inc.fields(&raw)...)
	if err != nil {
		return nil, fmt.Errorf("entry: open pool incident: %w", err)
	}
	if err := json.Unmarshal(raw, &inc.Evidence); err != nil {
		return nil, fmt.Errorf("entry: incident evidence: %w", err)
	}
	return &inc, nil
}

// Act marks an incident as being repaired, by whom and with what.
//
// The time is recorded separately from the opening so that time-to-repair
// splits into time-to-decide and time-to-act: when the chaos run of П9.7 asks
// "was this fast enough", those are two different answers with two different
// remedies.
func (s *Service) Act(ctx context.Context, id uuid.UUID, playbook, decidedBy string) error {
	switch decidedBy {
	case DecidedAuto, DecidedAsk, DecidedManual:
	default:
		return fmt.Errorf("%w: decided by %q", ErrRejected, decidedBy)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_incidents
		SET status = 'acting', playbook = $2, decided_by = $3, acted_at = $4
		WHERE id = $1 AND status = 'open'
	`, id, playbook, decidedBy, s.now())
	if err != nil {
		return fmt.Errorf("entry: incident act: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoSuchIncident
	}
	return nil
}

// Close ends an incident. status is resolved or dismissed; outcome is how it
// ended, in the closer's words.
//
// An incident is closed by saying so, never by the cause quietly going away:
// a box that recovers on its own still leaves a record that it was broken for
// eleven minutes, which is the thing the dataset is for.
func (s *Service) Close(ctx context.Context, id uuid.UUID, status, outcome string, costCents int) error {
	return s.closeIncident(ctx, id, status, outcome, costCents, "")
}

// closeIncident is Close, plus the machine a replacement put in the pool when
// the repair was one (empty otherwise). Written in the same statement as the
// closing: an incident closed as replaced but not yet saying by what would be
// a window in which the detector could not admit it.
func (s *Service) closeIncident(ctx context.Context, id uuid.UUID, status, outcome string, costCents int, replacedBy string) error {
	if status != IncidentResolved && status != IncidentDismissed {
		return fmt.Errorf("%w: cannot close as %q", ErrRejected, status)
	}
	if costCents < 0 {
		costCents = 0
	}
	var box, cause string
	err := s.pool.QueryRow(ctx, `
		UPDATE entry_incidents
		SET status = $2, outcome = $3, cost_cents = $4, closed_at = $5,
		    replaced_by = NULLIF($6, '')
		WHERE id = $1 AND status IN ('open', 'acting')
		RETURNING coalesce(box, pool), cause
	`, id, status, clip(outcome, 512), costCents, s.now(), replacedBy).Scan(&box, &cause)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSuchIncident
	}
	if err != nil {
		return fmt.Errorf("entry: incident close: %w", err)
	}
	applog.Info(ctx, "entry", "incident closed",
		applog.F("incident", id.String()), applog.F("box", box), applog.F("cause", cause),
		applog.F("status", status), applog.F("outcome", outcome), applog.F("replaced_by", replacedBy))
	return nil
}

// MarkAdmitted records that the pool is whole again on this box: whatever
// replaced what broke has passed a probe and is carrying traffic.
//
// # Why this is not closed_at
//
// A repair closes its incident optimistically — the proof it worked is the
// next probe round, not the repair's own exit code (repair.go). So closed_at
// is when the hub stopped working, and this is when the thing it did turned
// out to be true. On the console's timeline they are two stages with minutes
// between them, and a box that recovered on its own gets both in one pass,
// which is honest: there was no replacement to wait for.
//
// Called by the detector on a pass where the box reads healthy, so the column
// is written by the thing that observes rather than by the thing that acts.
// Bounded to incidents closed recently: a box that was broken in March and is
// fine today is not evidence that March's repair landed.
//
// The box answers for its own incidents and for the ones it was put in the
// pool for (`replaced_by`): a scale_up's incident is about a pool, which has
// nothing of its own to read healthy, so the only observation that can admit
// it is the new machine's. An evicted machine's incident stays unadmitted: the
// machine left the pool, and what makes the pool whole again is the fleet's
// own incident, admitted through the machine it bought.
func (s *Service) MarkAdmitted(ctx context.Context, box string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entry_incidents SET admitted_at = $2
		WHERE (box = $1 OR replaced_by = $1) AND status = 'resolved'
		  AND admitted_at IS NULL AND closed_at >= $3
	`, box, s.now(), s.now().Add(-admissionWindow))
	if err != nil {
		return fmt.Errorf("entry: mark admitted on %s: %w", box, err)
	}
	return nil
}

// Incidents lists the feed: everything still open first, then what closed
// recently. limit bounds the closed half only — an open incident is never
// paged out of sight, because the console's whole claim is that it shows what
// needs a person.
func (s *Service) Incidents(ctx context.Context, limit int) ([]Incident, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Two queries, deliberately. Every standing incident comes back whatever
	// the limit says, and the limit pages the closed half alone — a feed that
	// could push an open incident out of sight would break the one thing the
	// console claims, which is that it shows what needs a person.
	open, err := s.incidentsWhere(ctx, `status IN ('open', 'acting')`, 0)
	if err != nil {
		return nil, err
	}
	closed, err := s.incidentsWhere(ctx, `status NOT IN ('open', 'acting')`, limit)
	if err != nil {
		return nil, err
	}
	return append(open, closed...), nil
}

// incidentsWhere reads one half of the feed, newest first. where is a
// compile-time constant from the two call sites above — nothing from a
// request reaches it.
func (s *Service) incidentsWhere(ctx context.Context, where string, limit int) ([]Incident, error) {
	const head = `
		SELECT ` + incidentColumns + `
		FROM entry_incidents WHERE `
	const tail = ` ORDER BY opened_at DESC`
	q := head + where + tail
	args := []any{}
	if limit > 0 {
		q += ` LIMIT $1`
		args = append(args, limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("entry: incidents: %w", err)
	}
	defer rows.Close()

	out := []Incident{}
	for rows.Next() {
		var inc Incident
		var raw []byte
		if err := rows.Scan(inc.fields(&raw)...); err != nil {
			return nil, fmt.Errorf("entry: scan incident: %w", err)
		}
		if err := json.Unmarshal(raw, &inc.Evidence); err != nil {
			return nil, fmt.Errorf("entry: incident evidence: %w", err)
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// Incident reads one.
func (s *Service) Incident(ctx context.Context, id uuid.UUID) (Incident, error) {
	var inc Incident
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT `+incidentColumns+`
		FROM entry_incidents WHERE id = $1
	`, id).Scan(inc.fields(&raw)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Incident{}, ErrNoSuchIncident
	}
	if err != nil {
		return Incident{}, fmt.Errorf("entry: incident: %w", err)
	}
	if err := json.Unmarshal(raw, &inc.Evidence); err != nil {
		return Incident{}, fmt.Errorf("entry: incident evidence: %w", err)
	}
	return inc, nil
}

// HandOff records that the hub will not work on this incident by itself, and
// why — three failed repairs, a wave of this cause across the fleet, or no
// playbook for it at all.
//
// The incident stays open. Nothing about it became less true; what changed is
// who is expected to act, and that is exactly what moves it from the console's
// log into the console's queue. An incident parked as `acting` with nobody
// acting would be the lie this column exists to avoid.
func (s *Service) HandOff(ctx context.Context, id uuid.UUID, why string) error {
	if why == "" {
		return fmt.Errorf("%w: передавать человеку без причины нельзя", ErrRejected)
	}
	var box, cause string
	err := s.pool.QueryRow(ctx, `
		UPDATE entry_incidents SET handoff = $2, status = 'open', acted_at = NULL
		WHERE id = $1 AND status IN ('open', 'acting')
		RETURNING coalesce(box, pool), cause
	`, id, clip(why, 512)).Scan(&box, &cause)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSuchIncident
	}
	if err != nil {
		return fmt.Errorf("entry: hand over incident: %w", err)
	}
	applog.Warn(ctx, "entry", "incident handed to a person",
		applog.F("incident", id.String()), applog.F("box", box), applog.F("cause", cause), applog.F("why", why))
	return nil
}

// Reopen puts an incident whose repair failed back to open, so the next pass
// can try again (or the rails can give up on it).
//
// acted_at is cleared with it: it means "when the repair that is running
// started", and a failed attempt leaves none running. The attempt itself is
// not lost — it is a row in entry_repairs, which is where the history of
// trying lives.
func (s *Service) Reopen(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_incidents SET status = 'open', acted_at = NULL
		WHERE id = $1 AND status = 'acting'
	`, id)
	if err != nil {
		return fmt.Errorf("entry: reopen incident: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoSuchIncident
	}
	return nil
}

// Dismiss is the console's cross: a person says this incident is not worth
// acting on.
//
// Kept apart from [Service.Close] even though it is one of the two statuses
// Close takes, because the two are asked by different people for different
// reasons: Close is how a repair reports, Dismiss is how a person overrules
// the hub. The dataset П9.8 reads has to be able to tell them apart.
func (s *Service) Dismiss(ctx context.Context, id uuid.UUID, outcome string) error {
	if outcome == "" {
		outcome = "оператор: не актуально"
	}
	return s.Close(ctx, id, IncidentDismissed, outcome, 0)
}
