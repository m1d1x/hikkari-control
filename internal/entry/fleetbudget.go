package entry

// fleetbudget.go is what the fleet does when the money runs out (roadmap 42
// П9.18).
//
// The owner's one knob is money: the hosting group's caps for a day and a
// month (spend.go, П9.19). The hub sizes every pool by its load, and the caps
// are the fuse. A pool that needs a machine the caps have no room for is not a
// shortage the hub can repair — scale_up would only spend attempts on a
// refusal and strike the pool — so it is a shortage of money: its own
// incident, short_of_budget, with no repair. It waits for a person (to raise a
// cap) or for the next day or month, and closes by itself when the room is
// back.
//
// "As many as fit": the check is for one more machine, on every pass. Every
// machine that fits is bought by scale_up as before; the first that does not
// turns the pool's shortage into this one.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// CauseShortOfBudget: a pool needs a machine and the hosting caps have no
// room for one more of the plan the hub orders. Its subject is the pool.
const CauseShortOfBudget = "short_of_budget"

// machineFits says one more machine of `plan` fits under the hosting caps,
// and if not, why. Without a plan to price there is nothing to say no with:
// the order's own gate (reserveOrder) still checks the price it is quoted.
func (s *Service) machineFits(ctx context.Context, plan *BoxPlan) (bool, string, error) {
	if plan == nil {
		return true, "", nil
	}
	if _, err := s.hoster(); err != nil {
		return true, "", nil
	}
	if err := s.machineAffordable(ctx, s.pool, plan.MonthlyCents); err != nil {
		if errors.Is(err, ErrBudget) {
			return false, err.Error(), nil
		}
		return false, "", err
	}
	return true, "", nil
}

// budgetAdvice is the one sentence the console shows for a pool the cap stops.
func budgetAdvice(f FleetForecast, why string) string {
	return fmt.Sprintf("пулу %s нужно ещё %d (живых %d при цели %d и запасе %d), но потолок денег не пускает: %s — поднять потолок или ждать нового дня или месяца",
		f.Pool, f.Need, f.Live, f.Target, f.Buffer, why)
}

// budgetEvidence is what the stop was read from.
func budgetEvidence(f FleetForecast, why string) map[string]string {
	ev := fleetEvidence(f)
	ev["need"] = strconv.Itoa(f.Need)
	ev["budget"] = why
	if f.Sizing.Plan != nil {
		ev["plan"] = f.Sizing.Plan.ID
		ev["planMonthlyCents"] = strconv.Itoa(f.Sizing.Plan.MonthlyCents)
	}
	return ev
}
