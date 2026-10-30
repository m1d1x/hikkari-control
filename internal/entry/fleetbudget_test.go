package entry

import (
	"context"
	"strings"
	"testing"
)

// poolIncident is the pool's standing incident of a cause, if any.
func (h *fleetHarness) poolIncident(cause string) (Incident, bool) {
	h.t.Helper()
	incs, err := h.svc.Incidents(context.Background(), 1)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, inc := range incs {
		if inc.Pool == h.key && inc.Cause == cause && (inc.Status == IncidentOpen || inc.Status == IncidentActing) {
			return inc, true
		}
	}
	return Incident{}, false
}

// pricedTarget is target(n) with the plan the hub orders priced, so the money
// cap has something to say no to.
func (h *fleetHarness) pricedTarget(n int) {
	h.t.Helper()
	h.target(n)
	sz := sizingFor(n)
	// The dear plan: what a machine adds to the rest of a day is dollars even
	// in its last hour, so a room of one and a half machines is unambiguous.
	sz.Plan = &BoxPlan{ID: "huge", MonthlyCents: hugeCents, BandwidthGB: 1000, Regions: []string{"r1"}}
	h.svc.primeSizing(map[string]PoolSizing{h.key: sz})
}

// A pool that needs a machine the cap has no room for is short of money, not
// of a machine: no scale_up to strike the pool on a refusal, a person or a
// new day or month instead — and the moment room comes back, the fleet buys again.
func TestAPoolTheCapStopsIsShortOfBudgetNotOfAMachine(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.mode(ModeAuto)
	h.inPool()
	h.pricedTarget(3)

	// Room for one, then the cap: the shortage opens as a machine shortage.
	h.spendRoom(SpendHosting, 1.5*machineDayOf(hugeCents))
	if _, _, err := h.svc.DetectFleet(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.poolIncident(CauseShortOfFleet); !ok {
		t.Fatal("a pool with room for a machine is not short of one")
	}

	// The cap comes down below the next machine: the same pool is now short
	// of money, and the machine shortage is closed rather than left to fail.
	h.spendRoom(SpendHosting, 0.5*machineDayOf(hugeCents))
	if _, _, err := h.svc.DetectFleet(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.poolIncident(CauseShortOfFleet); ok {
		t.Error("the machine shortage stayed open under a cap with no room")
	}
	inc, ok := h.poolIncident(CauseShortOfBudget)
	if !ok {
		t.Fatal("no short_of_budget on a pool the cap stops")
	}
	if !strings.Contains(inc.Advice, "потолок") || inc.Evidence["plan"] != "huge" {
		t.Errorf("budget incident: advice %q, evidence %v", inc.Advice, inc.Evidence)
	}
	before := h.host.orderCount()
	if _, _, err := NewMechanic(h.svc, h.prov).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.host.orderCount(); got != before {
		t.Fatalf("the mechanic ordered %d machines on a budget incident", got-before)
	}
	var repairs int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_repairs WHERE pool = $1`, h.key).Scan(&repairs); err != nil {
		t.Fatal(err)
	}
	if repairs != 0 {
		t.Errorf("%d repairs ran on a shortage of money", repairs)
	}

	// The owner raises the cap: money is no longer the shortage, a machine is.
	h.spendRoom(SpendHosting, 10*machineDayOf(hugeCents))
	if _, _, err := h.svc.DetectFleet(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.poolIncident(CauseShortOfBudget); ok {
		t.Error("short_of_budget stayed open with room under the cap")
	}
	if _, ok := h.poolIncident(CauseShortOfFleet); !ok {
		t.Error("with room again the pool is not short of a machine")
	}
}
