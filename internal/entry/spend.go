package entry

// spend.go is the owner's one knob (roadmap 42 П9.19, owner's decision 28.10):
// how much the autopilot may spend in a day and in a month, per spend group —
// every hosting account together, every registrar together. Whatever fits
// under both caps the hub does by itself; whatever does not waits for the
// owner or for the next period (short_of_budget, fleetbudget.go). Nothing
// else about spending is a setting: not a count of orders, not a price per
// purchase, not a number of domains in stock.
//
// # What "spent" means for a machine
//
// A machine is rented by the hour (Vultr: the monthly price spread over 672
// hours, a month's bill capped at those 672), so a machine handed back costs
// nothing from that hour on. The reading is therefore an accrual, not a sum
// of prices: every machine's hours inside the period, from its order to the
// hoster's confirmation that it is gone.
//
// The gate does not stop at what has accrued. It reads where the period ends
// if every machine running now keeps running to its end — what has accrued
// plus the current burn to the end of the day (of the month) — and lets a new
// machine in only if that plus the new machine's own hours to the same end
// still fits. So a cap is never hit halfway through a day with the fleet
// still running past it: the hub would then have nothing to do but cut people
// off, and cutting people off to save money is the owner's decision, not the
// hub's (owner's decision 28.10, variant A).
//
// # What "spent" means for a domain
//
// A purchase: its price on the day it was made. Every purchase that may have
// been charged counts (status other than `failed`, В4).
//
// # Periods
//
// Calendar days and months in UTC, as the hosters and the registrar bill.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// The spend groups (entry_spend_caps.role, migration 082). A provider kind
// joins one by its role (ProviderKind.Role), so a second registrar shares the
// first one's caps.
const (
	SpendHosting   = string(RoleHosting)
	SpendRegistrar = string(RoleRegistrar)
)

// SpendRoles is every spend group, in the order the console shows them.
var SpendRoles = []string{SpendHosting, SpendRegistrar}

// billedHoursPerMonth is how many hours of a machine one calendar month bills
// at most: Vultr spreads the monthly price over 672 hours (28 days) and stops
// charging after them.
const billedHoursPerMonth = 672

// SpendCaps is what one spend group may spend.
type SpendCaps struct {
	DailyCents   int       `json:"dailyCapCents"`
	MonthlyCents int       `json:"monthlyCapCents"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// SpendReading is one spend group against its caps.
type SpendReading struct {
	Role string `json:"role"`
	SpendCaps
	// DayCents and MonthCents are what has accrued in the period so far.
	DayCents   int `json:"dayCents"`
	MonthCents int `json:"monthCents"`
	// DayProjectedCents and MonthProjectedCents are where the period ends if
	// everything running now keeps running: what the gate reads.
	DayProjectedCents   int `json:"dayProjectedCents"`
	MonthProjectedCents int `json:"monthProjectedCents"`
	// BurnCentsPerHour is what the machines running now cost an hour; zero
	// for the registrars, whose spend is one-off.
	BurnCentsPerHour float64 `json:"burnCentsPerHour"`
	// Running is how many machines the burn is made of.
	Running int `json:"running"`
	// dayExact and monthExact are the projections before rounding: what the
	// hosting gate compares. A machine's rest of a day can be worth a cent
	// and a half, and a gate rounding each figure up on its own would let
	// two such machines through a three-cent room — or refuse one through a
	// two-cent one.
	dayExact, monthExact float64
}

// DayRoomCents and MonthRoomCents are what is left under each cap after the
// projection; negative when the projection is already past it.
func (r SpendReading) DayRoomCents() int   { return r.DailyCents - r.DayProjectedCents }
func (r SpendReading) MonthRoomCents() int { return r.MonthlyCents - r.MonthProjectedCents }

// RoomCents is what one more purchase may cost now: the smaller of the two.
func (r SpendReading) RoomCents() int { return min(r.DayRoomCents(), r.MonthRoomCents()) }

// spendCaps reads a group's caps on q. A group with no row is a broken hub,
// not an unlimited one: the rows are written by the migration.
func spendCaps(ctx context.Context, q db.DBTX, role string) (SpendCaps, error) {
	var c SpendCaps
	err := q.QueryRow(ctx, `
		SELECT daily_cap_cents, monthly_cap_cents, updated_at FROM entry_spend_caps WHERE role = $1
	`, role).Scan(&c.DailyCents, &c.MonthlyCents, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpendCaps{}, fmt.Errorf("entry: no spend caps for %s", role)
	}
	if err != nil {
		return SpendCaps{}, fmt.Errorf("entry: spend caps %s: %w", role, err)
	}
	return c, nil
}

// SaveSpendCaps stores a group's caps. The bounds (non-negative, a month never
// below a day) are the table's: naming them twice would let the two drift.
func (s *Service) SaveSpendCaps(ctx context.Context, role string, daily, monthly int, adminID string) (SpendReading, error) {
	if role != SpendHosting && role != SpendRegistrar {
		return SpendReading{}, fmt.Errorf("%w: группы трат %q нет — бывают hosting и registrar", ErrRejected, role)
	}
	if daily < 0 || monthly < 0 {
		return SpendReading{}, fmt.Errorf("%w: потолок не бывает отрицательным", ErrRejected)
	}
	if monthly < daily {
		return SpendReading{}, fmt.Errorf("%w: месячный потолок %s меньше дневного %s", ErrRejected, money(monthly), money(daily))
	}
	var actor *uuid.UUID
	if adminID != "" {
		id, err := uuid.Parse(adminID)
		if err != nil {
			return SpendReading{}, fmt.Errorf("entry: admin id %q: %w", adminID, err)
		}
		actor = &id
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE entry_spend_caps
		SET daily_cap_cents = $2, monthly_cap_cents = $3, updated_by = $4, updated_at = now()
		WHERE role = $1
	`, role, daily, monthly, actor); err != nil {
		return SpendReading{}, fmt.Errorf("entry: save spend caps: %w", err)
	}
	return s.spendOf(ctx, s.pool, role)
}

// Spend reads every group against its caps.
func (s *Service) Spend(ctx context.Context) ([]SpendReading, error) {
	out := make([]SpendReading, 0, len(SpendRoles))
	for _, role := range SpendRoles {
		r, err := s.spendOf(ctx, s.pool, role)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// spendOf reads one group on q: the pool, or a gate's transaction under its
// lock.
func (s *Service) spendOf(ctx context.Context, q db.DBTX, role string) (SpendReading, error) {
	caps, err := spendCaps(ctx, q, role)
	if err != nil {
		return SpendReading{}, err
	}
	now := s.now().UTC()
	var r SpendReading
	switch role {
	case SpendHosting:
		rentals, err := machineRentals(ctx, q, monthStart(now))
		if err != nil {
			return SpendReading{}, err
		}
		r = hostingSpend(rentals, now)
	default:
		day, month, err := domainSpend(ctx, q, now)
		if err != nil {
			return SpendReading{}, err
		}
		r = SpendReading{DayCents: day, MonthCents: month, DayProjectedCents: day, MonthProjectedCents: month,
			dayExact: float64(day), monthExact: float64(month)}
	}
	r.Role, r.SpendCaps = role, caps
	return r, nil
}

// rental is one machine the hoster bills, or may bill: its order to the
// hoster's letting go of it, and its price. End is nil while it is paid for.
type rental struct {
	start        time.Time
	end          *time.Time
	monthlyCents int
}

// hourlyCents is what one hour of the machine costs.
func (r rental) hourlyCents() float64 { return float64(r.monthlyCents) / billedHoursPerMonth }

// machineRentals reads every rental that may have billed an hour since
// `since` (the start of the month):
//
//   - a box with an instance and a price, from its order (or, rented before
//     orders were written, its row) until the hoster confirmed it gone;
//   - an order no box row carries yet that is, or may be, a machine
//     (reserved, created, undeleted): paid for until it settles;
//   - an order that created a machine and gave it back (released with an
//     instance): the hours it lived were paid.
//
// A refused order, and one released before an instance existed, cost nothing.
func machineRentals(ctx context.Context, q db.DBTX, since time.Time) ([]rental, error) {
	rows, err := q.Query(ctx, `
		SELECT coalesce((SELECT min(o.created_at) FROM entry_box_orders o WHERE o.instance_id = b.provider_instance_id),
		                b.created_at),
		       b.released_at, b.monthly_cents
		FROM entry_boxes b
		WHERE b.provider_instance_id IS NOT NULL AND b.monthly_cents IS NOT NULL
		  AND (b.released_at IS NULL OR b.released_at >= $1)
		UNION ALL
		SELECT o.created_at,
		       CASE WHEN o.status = 'released' THEN o.updated_at END,
		       o.monthly_cents
		FROM entry_box_orders o
		WHERE (o.status IN ('reserved', 'created', 'undeleted')
		       OR (o.status = 'released' AND o.instance_id IS NOT NULL AND o.updated_at >= $1))
		  AND NOT EXISTS (SELECT 1 FROM entry_boxes x WHERE x.provider_instance_id = o.instance_id)
	`, since)
	if err != nil {
		return nil, fmt.Errorf("entry: machine rentals: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rental, error) {
		var x rental
		err := r.Scan(&x.start, &x.end, &x.monthlyCents)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("entry: machine rentals: %w", err)
	}
	return out, nil
}

// hostingSpend is the hosting group's reading at `now` from its rentals.
//
// The projection bills every rental over the whole period — a running one to
// the period's end, a finished one to its end — rather than adding "the rest"
// to the accrual, so an hour that is half past and half ahead bills once.
func hostingSpend(rentals []rental, now time.Time) SpendReading {
	dayFrom, monthFrom := dayStart(now), monthStart(now)
	dayTo, monthTo := dayFrom.Add(24*time.Hour), monthFrom.AddDate(0, 1, 0)
	var day, month, dayProj, monthProj, burn float64
	running := 0
	for _, x := range rentals {
		hr := x.hourlyCents()
		end, dayEnd, monthEnd := now, dayTo, monthTo
		if x.end != nil {
			dayEnd, monthEnd = *x.end, *x.end
			if x.end.Before(now) {
				end = *x.end
			}
		} else {
			running++
			burn += hr
		}
		day += float64(billedHours(x.start, end, dayFrom, now)) * hr
		month += float64(min(billedHoursPerMonth, billedHours(x.start, end, monthFrom, now))) * hr
		dayProj += float64(billedHours(x.start, dayEnd, dayFrom, dayTo)) * hr
		monthProj += float64(min(billedHoursPerMonth, billedHours(x.start, monthEnd, monthFrom, monthTo))) * hr
	}
	return SpendReading{
		DayCents:            ceilCents(day),
		MonthCents:          ceilCents(month),
		DayProjectedCents:   ceilCents(dayProj),
		MonthProjectedCents: ceilCents(monthProj),
		BurnCentsPerHour:    burn,
		Running:             running,
		dayExact:            dayProj,
		monthExact:          monthProj,
	}
}

// newMachineCents is what one more machine of monthlyCents adds to the
// projection of the day and of the month, ordered at `now`: its hours from
// now to each end, at least the one hour any order bills. Exact, not rounded:
// the gate compares it with the exact projection.
func newMachineCents(monthlyCents int, now time.Time) (day, month float64) {
	x := rental{start: now, monthlyCents: monthlyCents}
	dayTo, monthTo := dayStart(now).Add(24*time.Hour), monthStart(now).AddDate(0, 1, 0)
	hr := x.hourlyCents()
	return float64(max(1, billedHours(now, dayTo, now, dayTo))) * hr,
		float64(min(billedHoursPerMonth, max(1, billedHours(now, monthTo, now, monthTo)))) * hr
}

// billedHours is the whole hours of a rental [start, end) that fall in
// [from, to): a part of an hour bills as an hour. Zero without an overlap.
func billedHours(start, end, from, to time.Time) int {
	lo, hi := start, end
	if from.After(lo) {
		lo = from
	}
	if to.Before(hi) {
		hi = to
	}
	if !hi.After(lo) {
		return 0
	}
	return int(math.Ceil(hi.Sub(lo).Hours()))
}

// machineAffordable is the hosting gate: one more machine of monthlyCents
// fits under both caps of the hosting group, read on q. A refusal is
// ErrBudget with the sentence the console shows.
//
// The month is read first: when both caps refuse — on a month's last day
// the rest of the day is the rest of the month — the month is the one to
// name, since raising the day's cap alone would buy nothing.
func (s *Service) machineAffordable(ctx context.Context, q db.DBTX, monthlyCents int) error {
	if monthlyCents <= 0 {
		return fmt.Errorf("%w: хостер не назвал цену плана — без цены машину не заказываем", ErrRejected)
	}
	r, err := s.spendOf(ctx, q, SpendHosting)
	if err != nil {
		return err
	}
	day, month := newMachineCents(monthlyCents, s.now().UTC())
	switch {
	case r.monthExact+month > float64(r.MonthlyCents):
		return fmt.Errorf("%w: за месяц хостинг выйдет в %s, с новой машиной %s — потолок месяца %s",
			ErrBudget, money(r.MonthProjectedCents), money(ceilCents(r.monthExact+month)), money(r.MonthlyCents))
	case r.dayExact+day > float64(r.DailyCents):
		return fmt.Errorf("%w: за день хостинг выйдет в %s, с новой машиной %s — потолок дня %s",
			ErrBudget, money(r.DayProjectedCents), money(ceilCents(r.dayExact+day)), money(r.DailyCents))
	}
	return nil
}

// domainAffordable is the registrar gate: a purchase of costCents fits under
// both caps of the registrar group, read on q. The month is read first, for
// the reason machineAffordable gives.
func (s *Service) domainAffordable(ctx context.Context, q db.DBTX, costCents int) error {
	r, err := s.spendOf(ctx, q, SpendRegistrar)
	if err != nil {
		return err
	}
	switch {
	case r.MonthCents+costCents > r.MonthlyCents:
		return fmt.Errorf("%w: на домены за месяц потрачено %s, с этим %s — потолок месяца %s",
			ErrBudget, money(r.MonthCents), money(r.MonthCents+costCents), money(r.MonthlyCents))
	case r.DayCents+costCents > r.DailyCents:
		return fmt.Errorf("%w: на домены за день потрачено %s, с этим %s — потолок дня %s",
			ErrBudget, money(r.DayCents), money(r.DayCents+costCents), money(r.DailyCents))
	}
	return nil
}

// domainSpend is what the registrars charged today and this month: every
// purchase that may have been charged, by the day it was made.
func domainSpend(ctx context.Context, q db.DBTX, now time.Time) (day, month int, err error) {
	err = q.QueryRow(ctx, `
		SELECT coalesce(sum(cost_cents) FILTER (WHERE created_at >= $1), 0)::int,
		       coalesce(sum(cost_cents), 0)::int
		FROM entry_domains
		WHERE source = 'purchased' AND status <> 'failed' AND created_at >= $2
	`, dayStart(now), monthStart(now)).Scan(&day, &month)
	if err != nil {
		return 0, 0, fmt.Errorf("entry: domain spend: %w", err)
	}
	return day, month, nil
}

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ceilCents rounds a fractional spend up: the gate fails on the side of the
// owner's money.
func ceilCents(c float64) int { return int(math.Ceil(c - 1e-9)) }
