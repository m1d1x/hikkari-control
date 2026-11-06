package entry

// vantage_control.go judges the observation point itself, from inside Russia.
//
// # Why there is no probation
//
// A week of waiting says nothing about the next hour: a machine that behaved
// for seven days can drop out on the eighth. What tells a prober that is
// failing from a prober that is fine is a measurement taken now, so the hub
// asks for it all the time instead of once. A new point is admitted on a clean
// Russia-to-Russia run (done by the operator before the slot is opened) and
// from then on reports a control every few minutes: how many of a handful of
// Russian sites, up for everyone inside the country, it could reach.
//
// # What a failed control means
//
// The control is the prober's own path, not any relay's. When it fails the
// prober's verdicts on the fleet are worth nothing (K2 already makes them
// `network_down`, but only for a foreign site), and a prober that is slow or
// half-blind inside Russia cannot be trusted to judge a machine at all. So
// ControlKickAfter failed controls in a row retire the slot, at once and
// without the owner's touch: the fleet loses a witness, not a machine. A
// passed control clears the count. Putting the slot back is the owner's call
// (it needs the step-up like any new slot).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// ControlKickAfter is how many failed controls in a row take a slot out of
// the fleet. Two: the prober retries each unreached site itself before it
// reports, so one failed report is already a real spell, and the second keeps
// a single bad minute of ours from retiring a witness.
const ControlKickAfter = 2

// controlMaxSites bounds a report: it is a handful of sites, not a crawl.
const controlMaxSites = 32

// VantageControl is one control a prober reports: Total sites inside Russia
// tried, Reached of them answered, Failed the names of those that did not.
type VantageControl struct {
	Reached int      `json:"reached"`
	Total   int      `json:"total"`
	Failed  []string `json:"failed"`
}

// Passed: at least three in five reached. One site down for everyone (an
// outage at the site, an anti-bot wall) must not read as the prober's fault.
func (c VantageControl) Passed() bool {
	return c.Total > 0 && c.Reached*5 >= c.Total*3
}

func (c VantageControl) validate() error {
	switch {
	case c.Total < 1 || c.Total > controlMaxSites:
		return fmt.Errorf("%w: в контроле %d сайтов, нужно от 1 до %d", ErrRejected, c.Total, controlMaxSites)
	case c.Reached < 0 || c.Reached > c.Total:
		return fmt.Errorf("%w: ответили %d сайтов из %d", ErrRejected, c.Reached, c.Total)
	}
	return nil
}

// note is the control as the owner reads it on the slot.
func (c VantageControl) note() string {
	s := fmt.Sprintf("%d из %d сайтов РФ ответили", c.Reached, c.Total)
	if len(c.Failed) > 0 {
		failed := c.Failed
		if len(failed) > 8 {
			failed = failed[:8]
		}
		s += "; молчат: " + clip(strings.Join(failed, ", "), 200)
	}
	return s
}

// RecordVantageControl takes a control from the slot's prober. It returns
// kicked = true when this report was the one that took the slot out of the
// fleet; the prober's next request will be refused, as for any retired slot.
func (s *Service) RecordVantageControl(ctx context.Context, slot string, c VantageControl) (kicked bool, err error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	now := s.now()
	var fails int
	err = s.pool.QueryRow(ctx, `
		UPDATE entry_vantages SET
			control_at    = $2,
			control_fails = CASE WHEN $3 THEN 0 ELSE control_fails + 1 END,
			control_note  = $4
		WHERE slot = $1 AND status = 'active'
		RETURNING control_fails`,
		slot, now, c.Passed(), c.note()).Scan(&fails)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: %s", ErrNoSuchVantage, slot)
	}
	if err != nil {
		return false, fmt.Errorf("entry: vantage control %s: %w", slot, err)
	}
	if c.Passed() || fails < ControlKickAfter {
		return false, nil
	}
	reason := fmt.Sprintf("контроль РФ→РФ провален %d раз подряд: %s", fails, c.note())
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_vantages SET
			status        = 'retired',
			token_hash    = NULL,
			retired_at    = COALESCE(retired_at, $2),
			updated_at    = $2,
			updated_by    = 'hub:control',
			kicked_reason = $3
		WHERE slot = $1 AND status = 'active'`, slot, now, clip(reason, 400))
	if err != nil {
		return false, fmt.Errorf("entry: kick vantage %s: %w", slot, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	applog.Error(ctx, "entry", "vantage kicked from the fleet by its own control",
		applog.F("slot", slot), applog.F("reason", reason))
	return true, nil
}
