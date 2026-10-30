package entry

// pauses.go is the wave fuse as a thing that can be seen and lifted (roadmap
// 42 К2).
//
// # What the fuse is
//
// One cause breaking three machines inside an hour is not three faults. It is
// a new rule at the censor, or a bug in our own probe, and the worst possible
// answer to it is to buy three machines. So the mechanic stops acting on that
// cause (repair.go, `rails`) — and before this file it stopped by writing the
// reason onto each incident separately, which left the console showing three
// look-alike incidents and nothing about the fuse itself.
//
// # Why the pause outlives the hour
//
// The wave used to be counted over a rolling window, so the fuse un-blew
// itself: an hour after the third machine the autopilot resumed, with nobody
// having looked at why it had stopped. That is the one behaviour a fuse must
// not have. A pause now ends when a person ends it, and lifting it is the
// operator saying "I looked at the four of them, carry on".
//
// # Lifting clears the handoffs with it
//
// The incidents the fuse stood down on carry `handoff`, and the mechanic skips
// anything handed over (repair.go, `consider`). Lifting the pause without
// clearing them would be a button that changes a row and nothing else. So the
// two go together, in one transaction: the cause is live again and the
// incidents under it are the autopilot's to work.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoSuchPause: no standing pause on that cause.
var ErrNoSuchPause = errors.New("entry: no standing pause on this cause")

// CausePause is the fuse, blown.
type CausePause struct {
	ID    string `json:"id"`
	Cause string `json:"cause"`
	// Reason is the hub's own sentence about why it stood down.
	Reason string `json:"reason"`
	// Boxes is how many machines the cause had broken when the fuse blew.
	Boxes    int        `json:"boxes"`
	PausedAt time.Time  `json:"pausedAt"`
	LiftedAt *time.Time `json:"liftedAt,omitempty"`
	LiftedBy string     `json:"liftedBy,omitempty"`
}

// PauseCause blows the fuse on a cause, or leaves the standing pause alone.
//
// Idempotent on purpose, like opening an incident: the mechanic reaches this
// on every pass while the wave holds, and a second row would make the console
// show the fuse twice.
func (s *Service) PauseCause(ctx context.Context, cause, reason string, boxes int) error {
	if cause == "" {
		return fmt.Errorf("%w: пауза без причины", ErrRejected)
	}
	if boxes < 0 {
		boxes = 0
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO entry_cause_pauses (cause, reason, boxes, paused_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (cause) WHERE lifted_at IS NULL DO NOTHING
	`, cause, clip(reason, 512), boxes, s.now())
	if err != nil {
		return fmt.Errorf("entry: pause cause: %w", err)
	}
	return nil
}

// CausePaused answers whether the autopilot is standing down on this cause.
func (s *Service) CausePaused(ctx context.Context, cause string) (CausePause, bool, error) {
	var p CausePause
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, cause, reason, boxes, paused_at
		FROM entry_cause_pauses WHERE cause = $1 AND lifted_at IS NULL
	`, cause).Scan(&p.ID, &p.Cause, &p.Reason, &p.Boxes, &p.PausedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CausePause{}, false, nil
	}
	if err != nil {
		return CausePause{}, false, fmt.Errorf("entry: cause pause: %w", err)
	}
	return p, true, nil
}

// anyCausePaused is CausePaused over several causes: the oldest standing
// pause of any of them (repair.go, spendGroups).
func (s *Service) anyCausePaused(ctx context.Context, causes []string) (CausePause, bool, error) {
	var p CausePause
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, cause, reason, boxes, paused_at
		FROM entry_cause_pauses WHERE cause = ANY($1) AND lifted_at IS NULL
		ORDER BY paused_at LIMIT 1
	`, causes).Scan(&p.ID, &p.Cause, &p.Reason, &p.Boxes, &p.PausedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CausePause{}, false, nil
	}
	if err != nil {
		return CausePause{}, false, fmt.Errorf("entry: cause pause: %w", err)
	}
	return p, true, nil
}

// Pauses lists the standing ones, oldest first — the order of a queue, since
// the oldest pause is the one that has been holding the autopilot back
// longest.
func (s *Service) Pauses(ctx context.Context) ([]CausePause, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, cause, reason, boxes, paused_at
		FROM entry_cause_pauses WHERE lifted_at IS NULL
		ORDER BY paused_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("entry: pauses: %w", err)
	}
	defer rows.Close()

	out := []CausePause{}
	for rows.Next() {
		var p CausePause
		if err := rows.Scan(&p.ID, &p.Cause, &p.Reason, &p.Boxes, &p.PausedAt); err != nil {
			return nil, fmt.Errorf("entry: scan pause: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LiftPause ends a pause and hands the cause's incidents back to the
// autopilot. Returns how many incidents were handed back.
//
// The two writes are one transaction. A pause lifted while the incidents under
// it still carry `handoff` would be a button that changed nothing a person can
// see: the mechanic skips handed-over incidents, so the cause would read live
// and still be worked by nobody.
func (s *Service) LiftPause(ctx context.Context, cause, by string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("entry: lift pause: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE entry_cause_pauses SET lifted_at = $2, lifted_by = $3
		WHERE cause = $1 AND lifted_at IS NULL
	`, cause, s.now(), clip(by, 64))
	if err != nil {
		return 0, fmt.Errorf("entry: lift pause: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrNoSuchPause
	}
	// Only the incidents the fuse itself stood down on: one whose own repairs
	// failed three times was handed over for a reason of its own, and lifting
	// the wave must not quietly send the hub back at it. The fuse holds every
	// cause leading to the same spend (repair.go, spendGroups), so it frees
	// them all; one still under a pause of its own is stood down again on
	// the next pass.
	freed, err := tx.Exec(ctx, `
		UPDATE entry_incidents SET handoff = ''
		WHERE cause = ANY($1) AND status = 'open' AND handoff LIKE $2
	`, pausedTogether(cause), wavePrefix+"%")
	if err != nil {
		return 0, fmt.Errorf("entry: free incidents of %q: %w", cause, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("entry: lift pause: %w", err)
	}
	return int(freed.RowsAffected()), nil
}
