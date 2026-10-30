package entry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The reprobes of the second chance (roadmap 42 §11, П9.11, migrations 072
// and 073): what goes to a vantage about the suspects, apart from the routine
// rounds. Never in the routine watch list — a suspect there would be noise in
// the ban tempo and traffic the prober does not need — and never more often
// than the kind's probe interval, which the ledger keeps per suspect
// (secondchance.go). Covers are dialled by the hub itself and live beside
// the ledger.
//
// # Domains
//
// A name burned by domain_blocked is still ours, and a block by name does
// lift. The vantage tries each burned name as SNI against the address of a
// live XHTTP relay, beside a handshake with no SNI to the same address: the
// very pair of signals that burned the name (classifyXhttp: named ✗, bare ✓),
// on an address known to answer.
//
// # Machines
//
// A suspect machine's relays are handed to a vantage as a task of their own
// kind and answered like an admission probe: the same three signals per
// relay, the verdicts back per relay. The prober claims them on its slow loop
// beside the domain reprobes.
//
// In both, a reading that says something about the subject moves its streak
// or its failures; one that says nothing (network_down, unknown) leaves it.

const (
	// TaskDomain: a reprobe of burned domains (entry_probe_tasks.kind).
	TaskDomain = "domain"
	// TaskBox: a reprobe of one suspect machine's relays.
	TaskBox = "box"

	// domainReprobeBatch caps the names one task carries.
	domainReprobeBatch = 50
	// boxReprobeBatch caps the machines one pass queues.
	boxReprobeBatch = 20
)

const (
	// pendingReprobe: a reprobe of this kind still waiting for a vantage.
	// While one waits no other is queued — with no prober running they would
	// pile up, and the suspects they carry would be marked asked without
	// being asked.
	pendingReprobe = `
		SELECT EXISTS (SELECT 1 FROM entry_probe_tasks WHERE kind = $1 AND status = 'queued')`

	// reprobeAddressRelay picks the live XHTTP relay whose address the names
	// are tried against; at random, so one box does not carry every burned
	// SNI the hub has.
	reprobeAddressRelay = `
		SELECT r.name FROM entry_relays r JOIN entry_boxes b ON b.name = r.box
		WHERE r.status = 'alive' AND b.proto = 'vless-xhttp' AND b.retired_at IS NULL
		ORDER BY random() LIMIT 1`

	insertReprobeTask = `
		INSERT INTO entry_probe_tasks (relays, kind, params) VALUES ($1, $2, $3) RETURNING id`

	// suspectRelays: the relays a suspect machine is reprobed through.
	suspectRelays = `
		SELECT name FROM entry_relays WHERE box = $1 AND status = 'suspect' ORDER BY name`
)

// domainTaskParams is a domain task's params: the names to try.
type domainTaskParams struct {
	Domains []string `json:"domains"`
}

// boxTaskParams is a machine reprobe's params: the machine reprobed.
type boxTaskParams struct {
	Box string `json:"box"`
}

// ReprobeBurnedDomains queues one reprobe of the burned names due for one,
// returning how many it queued. Nothing is queued — and nothing is stamped
// asked — while an earlier reprobe still waits for a vantage, or while no live
// XHTTP relay is there to lend its address.
func (s *Service) ReprobeBurnedDomains(ctx context.Context) (int, error) {
	policy, err := s.DetectionPolicy(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var pending bool
	if err := tx.QueryRow(ctx, pendingReprobe, TaskDomain).Scan(&pending); err != nil {
		return 0, err
	}
	if pending {
		return 0, nil
	}
	var relay string
	err = tx.QueryRow(ctx, reprobeAddressRelay).Scan(&relay)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	names, err := markDue(ctx, tx, SuspectDomain, policy.Domain.ProbeEvery, domainReprobeBatch)
	if err != nil {
		return 0, err
	}
	if len(names) == 0 {
		return 0, nil
	}

	params, err := json.Marshal(domainTaskParams{Domains: names})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, insertReprobeTask, []string{relay}, TaskDomain, params); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	applog.Info(ctx, "entry", "burned domains queued for a reprobe",
		applog.F("count", len(names)), applog.F("relay", relay))
	return len(names), nil
}

// reprobeSuspectBoxes queues a reprobe of every suspect machine due for one,
// one task per machine, returning how many it queued. Nothing is queued while
// an earlier one still waits for a vantage.
func (s *Service) reprobeSuspectBoxes(ctx context.Context, c SecondChance) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pending bool
	if err := tx.QueryRow(ctx, pendingReprobe, TaskBox).Scan(&pending); err != nil {
		return 0, err
	}
	if pending {
		return 0, nil
	}
	boxes, err := markDue(ctx, tx, SuspectBox, c.ProbeEvery, boxReprobeBatch)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, box := range boxes {
		rows, err := tx.Query(ctx, suspectRelays, box)
		if err != nil {
			return 0, err
		}
		relays, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return 0, err
		}
		if len(relays) == 0 {
			// Retired by hand meanwhile, or its relays died: nothing to ask
			// through. The deadline writes the suspicion off.
			continue
		}
		params, err := json.Marshal(boxTaskParams{Box: box})
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, insertReprobeTask, relays, TaskBox, params); err != nil {
			return 0, err
		}
		queued++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if queued > 0 {
		applog.Info(ctx, "entry", "suspect machines queued for a reprobe", applog.F("count", queued))
	}
	return queued, nil
}

// applyBoxVerdicts folds a finished machine reprobe into the ledger, in the
// transaction that finishes the task. Read over the relays the task carried
// only: a verdict about anything else is not an answer to this task.
func (s *Service) applyBoxVerdicts(ctx context.Context, tx pgx.Tx, params []byte, relays []string, verdicts map[string]string) error {
	var p boxTaskParams
	if err := json.Unmarshal(params, &p); err != nil || p.Box == "" {
		return fmt.Errorf("entry: box task params: %v", err)
	}
	policy, err := s.DetectionPolicy(ctx)
	if err != nil {
		return err
	}
	status, err := judge(ctx, tx, SuspectBox, p.Box, boxReading(verdicts, relays), policy.Box, true)
	if err != nil {
		return err
	}
	switch status {
	case suspectReturned:
		applog.Info(ctx, "entry", "suspect machine back in the pool",
			applog.F("box", p.Box), applog.F("clean_reprobes", policy.Box.ReturnClean))
	case suspectDead:
		applog.Info(ctx, "entry", "suspect machine written off",
			applog.F("box", p.Box), applog.F("failed_reprobes", policy.Box.DeadFails))
	}
	return nil
}

// applyDomainVerdicts folds a finished reprobe into the ledger, in the
// transaction that finishes the task. Only the names the task carried are
// read; a verdict about anything else is not an answer to this task. The log
// carries counts, not names: the ledger is the one place names are kept plain.
func (s *Service) applyDomainVerdicts(ctx context.Context, tx pgx.Tx, params []byte, verdicts map[string]string) error {
	var p domainTaskParams
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("entry: domain task params: %w", err)
	}
	policy, err := s.DetectionPolicy(ctx)
	if err != nil {
		return err
	}
	returned, dead := 0, 0
	for _, d := range p.Domains {
		r := readNothing
		switch verdicts[d] {
		case "clean":
			r = readClean
		case CauseDomainBlocked:
			r = readFailed
		}
		status, err := judge(ctx, tx, SuspectDomain, d, r, policy.Domain, true)
		if err != nil {
			return err
		}
		switch status {
		case suspectReturned:
			returned++
		case suspectDead:
			dead++
		}
	}
	if returned > 0 || dead > 0 {
		applog.Info(ctx, "entry", "burned domains settled",
			applog.F("returned", returned), applog.F("dead", dead),
			applog.F("clean_reprobes", policy.Domain.ReturnClean), applog.F("failed_reprobes", policy.Domain.DeadFails))
	}
	return nil
}
