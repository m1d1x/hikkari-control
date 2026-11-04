package stand

// The stand on the hub's schedule (hikkari-client
// plans/2026-11-04-tspu-detection-research.md, «Что делать дальше», 1).
//
// # Who owns what
//
// The hub owns the plan: stand_settings and stand_knobs say what is measured,
// stand_tasks hold every run (migration 025). A vantage's prober owns the
// measurement: it claims a task, parses the stand's subscription with the
// client's own parser, runs Script locally on the core users run and sends the
// rows back. The vantage a row is from is the slot of the prober's token, never
// a field of the request.
//
// # When things fail
//
// A point that does not claim its task loses it after an interval: the queue
// never grows behind a silent point, and the slot's silence is the registry's
// to show. A claimed task not finished within claimTTL is expired the same
// way. A subscription the hub cannot open fails the task at the claim, with
// the reason, and the prober runs nothing. Rows of a run that failed half-way
// are kept: each one is a measurement in its own right.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

// Task states (stand_tasks.status).
const (
	TaskQueued  = "queued"
	TaskClaimed = "claimed"
	TaskDone    = "done"
	TaskFailed  = "failed"
	TaskExpired = "expired"
)

// claimTTL is the longest a claimed run may take: 5 profiles × 5 values × 3
// rounds at 45 s each is under an hour.
const claimTTL = 75 * time.Minute

// maxTaskRows bounds what one finish may carry.
const maxTaskRows = 2000

// ErrNoSuchTask: no task under that id claimed by this point.
var ErrNoSuchTask = errors.New("stand: no such task claimed by this point")

// Slots lists the active observation points (entry.Service in production).
type Slots interface {
	ActiveSlots(ctx context.Context) ([]string, error)
}

// Subscriptions opens the stand's subscription link by account name
// (subscription.Service in production).
type Subscriptions interface {
	URLByName(ctx context.Context, name string) (string, error)
}

// Rows is where the runs' rows go (relayruns.Store in production).
type Rows interface {
	Insert(ctx context.Context, rows []relayruns.Row) (int, error)
}

// Scheduler queues and hands out the stand's runs.
type Scheduler struct {
	pool  db.Pool
	slots Slots
	subs  Subscriptions
	rows  Rows
}

// NewScheduler is the stand on pool.
func NewScheduler(pool db.Pool, slots Slots, subs Subscriptions, rows Rows) *Scheduler {
	return &Scheduler{pool: pool, slots: slots, subs: subs, rows: rows}
}

// Settings is stand_settings.
type Settings struct {
	Subscription string `json:"subscription"`
	Interval     int    `json:"interval_minutes"`
	Rounds       int    `json:"rounds"`
	BaselinePath string `json:"baseline_path"`
}

func (s *Scheduler) settings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.pool.QueryRow(ctx, `SELECT subscription, interval_minutes, rounds, baseline_path FROM stand_settings WHERE id`).
		Scan(&st.Subscription, &st.Interval, &st.Rounds, &st.BaselinePath)
	if err != nil {
		return Settings{}, fmt.Errorf("stand settings: %w", err)
	}
	return st, nil
}

// Run queues tasks every tick until ctx ends.
func (s *Scheduler) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if n, err := s.Tick(ctx); err != nil {
			applog.Error(ctx, "stand", "schedule failed", applog.F("err", err.Error()))
		} else if n > 0 {
			applog.Info(ctx, "stand", "runs queued", applog.F("n", n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick expires what was left behind and queues, for every active point and
// enabled knob, a run unless one was queued within the interval. It returns
// how many it queued.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	st, err := s.settings(ctx)
	if err != nil {
		return 0, err
	}
	interval := time.Duration(st.Interval) * time.Minute
	if _, err := s.pool.Exec(ctx, `
		UPDATE stand_tasks SET status = 'expired', finished_at = now(),
		       error = CASE status WHEN 'queued' THEN 'точка не взяла прогон за интервал'
		                           ELSE 'точка не закончила прогон' END
		WHERE (status = 'queued' AND created_at < now() - $1::interval)
		   OR (status = 'claimed' AND claimed_at < now() - $2::interval)`,
		interval.String(), claimTTL.String()); err != nil {
		return 0, fmt.Errorf("expire stand tasks: %w", err)
	}
	if st.Subscription == "" {
		return 0, nil // the stand is off
	}
	slots, err := s.slots.ActiveSlots(ctx)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, slot := range slots {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO stand_tasks (slot, knob, vals, base, rounds, baseline_path)
			SELECT $1, k.knob, k.vals, k.base, $2, $3 FROM stand_knobs k
			WHERE k.enabled AND NOT EXISTS (
				SELECT 1 FROM stand_tasks t
				WHERE t.slot = $1 AND t.knob = k.knob
				  AND (t.status IN ('queued', 'claimed') OR t.created_at > now() - $4::interval))`,
			slot, st.Rounds, st.BaselinePath, interval.String())
		if err != nil {
			return queued, fmt.Errorf("queue stand tasks for %s: %w", slot, err)
		}
		queued += int(tag.RowsAffected())
	}
	return queued, nil
}

// Task is a run handed to a point. SubURL holds the subscription's token:
// it goes to the prober that claimed the task and nowhere else.
type Task struct {
	ID           uuid.UUID `json:"id"`
	Knob         string    `json:"knob"`
	Values       []string  `json:"values"`
	Base         string    `json:"base,omitempty"`
	Rounds       int       `json:"rounds"`
	BaselinePath string    `json:"baseline_path"`
	SubURL       string    `json:"sub_url"`
}

// Claim hands slot its oldest queued run, or nil. A run whose subscription
// the hub cannot open is failed here, with the reason, and the next one is
// tried.
func (s *Scheduler) Claim(ctx context.Context, slot string) (*Task, error) {
	for {
		var t Task
		err := s.pool.QueryRow(ctx, `
			UPDATE stand_tasks SET status = 'claimed', claimed_at = now()
			WHERE id = (SELECT id FROM stand_tasks WHERE slot = $1 AND status = 'queued'
			            ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
			RETURNING id, knob, vals, base, rounds, baseline_path`, slot).
			Scan(&t.ID, &t.Knob, &t.Values, &t.Base, &t.Rounds, &t.BaselinePath)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("claim stand task: %w", err)
		}
		st, err := s.settings(ctx)
		if err == nil && st.Subscription == "" {
			err = errors.New("стенд выключен: в stand_settings не названа подписка")
		}
		if err == nil {
			t.SubURL, err = s.subs.URLByName(ctx, st.Subscription)
			if err != nil {
				err = fmt.Errorf("подписка стенда %q: %w", st.Subscription, err)
			}
		}
		if err == nil {
			return &t, nil
		}
		if _, ferr := s.pool.Exec(ctx, `UPDATE stand_tasks SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`,
			t.ID, err.Error()); ferr != nil {
			return nil, fmt.Errorf("fail stand task: %w", ferr)
		}
		applog.Error(ctx, "stand", "run not handed out", applog.F("slot", slot), applog.F("err", err.Error()))
	}
}

// Result is what a point sends back for a run: its rows, the core it ran,
// and why it stopped short, if it did.
type Result struct {
	Core  string `json:"core"`
	Rows  []Row  `json:"rows"`
	Error string `json:"error,omitempty"`
}

// Finish records slot's result for task id: the rows go to the dataset as
// rows of slot, whatever vantage they name, and the task is done, or failed
// when the point says why it stopped. It returns how many rows were new.
func (s *Scheduler) Finish(ctx context.Context, slot string, id uuid.UUID, res Result) (int, error) {
	if len(res.Rows) > maxTaskRows {
		return 0, fmt.Errorf("%w: %d rows, at most %d", ErrNotAResult, len(res.Rows), maxTaskRows)
	}
	var knob string
	err := s.pool.QueryRow(ctx, `SELECT knob FROM stand_tasks WHERE id = $1 AND slot = $2 AND status = 'claimed'`, id, slot).Scan(&knob)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoSuchTask
	}
	if err != nil {
		return 0, fmt.Errorf("stand task: %w", err)
	}
	rows := make([]Row, len(res.Rows))
	for i, r := range res.Rows {
		if r.Knob != knob && r.Knob != Baseline {
			return 0, fmt.Errorf("%w: row of knob %q in a run of %q", ErrNotAResult, r.Knob, knob)
		}
		r.Vantage = slot
		r.Core = res.Core
		rows[i] = r
	}
	data, err := DatasetRows(rows)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNotAResult, err)
	}
	// The point's network is the operator's (entry_vantages.network): the hub
	// never sees where a point connects from, and what the point would say
	// about itself is no more than what the operator set.
	var network string
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(network, '') FROM entry_vantages WHERE slot = $1`, slot).Scan(&network); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("stand point network: %w", err)
	}
	for i := range data {
		data[i].Network = network
		if err := data[i].Validate(); err != nil {
			return 0, fmt.Errorf("%w: %v", ErrNotAResult, err)
		}
	}
	added, err := s.rows.Insert(ctx, data)
	if err != nil {
		return added, err
	}
	status, reason := TaskDone, strings.TrimSpace(res.Error)
	if reason != "" {
		status = TaskFailed
		if len(reason) > 2000 {
			reason = reason[:2000]
		}
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE stand_tasks SET status = $2, error = $3, core = $4, rows_sent = $5, rows_added = $6, finished_at = now()
		WHERE id = $1`, id, status, reason, res.Core, len(rows), added); err != nil {
		return added, fmt.Errorf("finish stand task: %w", err)
	}
	return added, nil
}

// ErrNotAResult: what a point sent cannot be a run's result.
var ErrNotAResult = errors.New("stand: not a result")

// TaskView is a run as the agent reads it.
type TaskView struct {
	ID         uuid.UUID  `json:"id"`
	Slot       string     `json:"slot"`
	Knob       string     `json:"knob"`
	Values     []string   `json:"values"`
	Base       string     `json:"base,omitempty"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	Core       string     `json:"core,omitempty"`
	RowsSent   int        `json:"rows_sent"`
	RowsAdded  int        `json:"rows_added"`
	CreatedAt  time.Time  `json:"created_at"`
	ClaimedAt  *time.Time `json:"claimed_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Recent lists the latest runs, newest first.
func (s *Scheduler) Recent(ctx context.Context, limit int) ([]TaskView, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, slot, knob, vals, base, status, error, core, rows_sent, rows_added, created_at, claimed_at, finished_at
		FROM stand_tasks ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("stand tasks: %w", err)
	}
	defer rows.Close()
	out := []TaskView{}
	for rows.Next() {
		var v TaskView
		if err := rows.Scan(&v.ID, &v.Slot, &v.Knob, &v.Values, &v.Base, &v.Status, &v.Error, &v.Core, &v.RowsSent,
			&v.RowsAdded, &v.CreatedAt, &v.ClaimedAt, &v.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Plan is the stand's settings and knobs, as the agent reads them.
func (s *Scheduler) Plan(ctx context.Context) (Settings, map[string][]string, error) {
	st, err := s.settings(ctx)
	if err != nil {
		return Settings{}, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT knob, vals FROM stand_knobs WHERE enabled ORDER BY knob`)
	if err != nil {
		return st, nil, fmt.Errorf("stand knobs: %w", err)
	}
	defer rows.Close()
	knobs := map[string][]string{}
	for rows.Next() {
		var k string
		var v []string
		if err := rows.Scan(&k, &v); err != nil {
			return st, nil, err
		}
		knobs[k] = v
	}
	return st, knobs, rows.Err()
}
