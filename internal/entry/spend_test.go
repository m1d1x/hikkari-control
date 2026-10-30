package entry

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// generousCapCents is a cap no test reaches by accident: $1M.
const generousCapCents = 100_000_000

// spendCaps sets a group's caps for this test and puts the ones before back.
func (h *harness) spendCaps(role string, daily, monthly int) {
	h.t.Helper()
	ctx := context.Background()
	before, err := spendCaps(ctx, h.pool, role)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.svc.SaveSpendCaps(ctx, role, daily, monthly, ""); err != nil {
		h.t.Fatalf("save %s caps: %v", role, err)
	}
	h.t.Cleanup(func() {
		if _, err := h.svc.SaveSpendCaps(context.Background(), role, before.DailyCents, before.MonthlyCents, ""); err != nil {
			h.t.Errorf("restore %s caps: %v", role, err)
		}
	})
}

// spendRoom sets a group's day cap to what the day is projected at now plus
// room cents (rounded down to a whole cent), and leaves the month generous:
// the test database's month is every run's, so a test about room reads it off
// the day.
func (h *harness) spendRoom(role string, room float64) {
	h.t.Helper()
	r, err := h.svc.spendOf(context.Background(), h.pool, role)
	if err != nil {
		h.t.Fatal(err)
	}
	h.spendCaps(role, int(math.Floor(r.dayExact+room)), generousCapCents)
}

// dayProjected is the hosting group's day as the gate reads it now, exact.
func (h *harness) dayProjected() float64 {
	h.t.Helper()
	r, err := h.svc.spendOf(context.Background(), h.pool, SpendHosting)
	if err != nil {
		h.t.Fatal(err)
	}
	return r.dayExact
}

// hugeCents is the fake hoster's dear plan: a test about the gate orders it,
// so that what a machine adds to the rest of a day is dollars even in the
// day's last hour — a $5 machine then adds less than a cent.
const hugeCents = 9900

// machineDayOf is what one machine of monthlyCents ordered now adds to the
// day's projection; machineDay is the fake hoster's default $5 plan.
func machineDayOf(monthlyCents int) float64 {
	day, _ := newMachineCents(monthlyCents, time.Now().UTC())
	return day
}

func machineDay() float64 { return machineDayOf(500) }

// hoursLeftToday is the hours a machine ordered now bills to the end of the
// UTC day.
func hoursLeftToday() int {
	now := time.Now().UTC()
	return billedHours(now, dayStart(now).Add(24*time.Hour), now, dayStart(now).Add(24*time.Hour))
}

// nearCents compares two spends to a hundredth of a cent.
func nearCents(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func atp(s string) *time.Time {
	t := at(s)
	return &t
}

// A machine bills whole hours, from its order to its release, and a month
// bills no more than 672 of them. The projection runs a machine still running
// to the end of the period and stops one already let go where it stopped.
func TestHostingSpendAccruesHoursAndProjectsTheRunningOnes(t *testing.T) {
	now := at("2026-10-28T12:30:00Z")
	const monthly = 672 // a cent an hour
	rentals := []rental{
		// Running since yesterday 22:00: 14.5 h today → 15 billed, and to
		// midnight 26 h.
		{start: at("2026-10-27T22:00:00Z"), monthlyCents: monthly},
		// Today 08:00–09:10: two hours, done.
		{start: at("2026-10-28T08:00:00Z"), end: atp("2026-10-28T09:10:00Z"), monthlyCents: monthly},
		// Running since the 1st: the month's 672 hours run out before its end.
		{start: at("2026-10-01T00:00:00Z"), monthlyCents: monthly},
	}
	r := hostingSpend(rentals, now)
	// Day: 13 h of the first (since 00:00) + 2 + 13 of the third.
	if r.DayCents != 13+2+13 {
		t.Errorf("day = %d, want 28", r.DayCents)
	}
	// Day ahead: 24 + 2 + 24.
	if r.DayProjectedCents != 24+2+24 {
		t.Errorf("day projected = %d, want 50", r.DayProjectedCents)
	}
	// Month: the first since the 27th 22:00 (14.5 → 15) + 2 + the third
	// since the 1st (660.5 → 661, under the 672).
	if r.MonthCents != 15+2+661 {
		t.Errorf("month = %d, want 678", r.MonthCents)
	}
	// Month ahead: the first runs to Nov 1st (98 h) + 2 + 672.
	if r.MonthProjectedCents != 98+2+672 {
		t.Errorf("month projected = %d, want 772", r.MonthProjectedCents)
	}
	if r.Running != 2 || r.BurnCentsPerHour != 2 {
		t.Errorf("running %d burning %.2f/h, want 2 and 2", r.Running, r.BurnCentsPerHour)
	}
}

// A new machine adds its hours to the period's end, at least the hour any
// order bills.
func TestANewMachineAddsItsHoursToThePeriodsEnd(t *testing.T) {
	day, month := newMachineCents(672, at("2026-10-28T23:59:00Z"))
	if day != 1 || month != 73 {
		t.Errorf("at 23:59: day %.0f month %.0f, want 1 and 73", day, month)
	}
	day, month = newMachineCents(672, at("2026-10-01T00:00:00Z"))
	if day != 24 || month != 672 {
		t.Errorf("on the 1st: day %.0f month %.0f, want 24 and the 672 cap", day, month)
	}
}

// The day's cap stops an order the day's projection has no room for, and
// says so in money; the month's does the same. A month under a day is
// refused by the hub before the table sees it.
func TestTheHostingCapsHoldAnOrder(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	huge := OrderSpec{Plan: "huge"}
	h.spendRoom(SpendHosting, 0.5*machineDayOf(hugeCents))
	if _, err := h.prov.orderAndRecord(ctx, huge); !errors.Is(err, ErrBudget) {
		t.Fatalf("an order past the day: err = %v, want ErrBudget", err)
	}
	if n := h.host.orderCount(); n != 0 {
		t.Fatalf("%d machines ordered past the day", n)
	}
	r, err := h.svc.spendOf(ctx, h.pool, SpendHosting)
	if err != nil {
		t.Fatal(err)
	}
	_, month := newMachineCents(hugeCents, time.Now().UTC())
	monthCap := int(math.Floor(r.monthExact + month/2))
	// The day as high as the month allows: only the month can refuse.
	h.spendCaps(SpendHosting, monthCap, monthCap)
	if _, err := h.prov.orderAndRecord(ctx, huge); !errors.Is(err, ErrBudget) || !strings.Contains(err.Error(), "месяц") {
		t.Fatalf("an order past the month: err = %v, want ErrBudget on the month", err)
	}
	if _, err := h.svc.SaveSpendCaps(ctx, SpendHosting, 200, 100, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("a month under a day: err = %v, want ErrRejected", err)
	}
}

// With no room left in the day, a purchase is refused for money before a
// single name is looked at.
func TestTheRegistrarCapsHoldAPurchase(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.spendRoom(SpendRegistrar, 0)
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0
	if _, err := h.svc.buyDomain(ctx, h.svc.currentBuyer(), "", ""); !errors.Is(err, ErrBudget) {
		t.Fatalf("no room left: err = %v, want ErrBudget", err)
	}
	if reg.buys != 0 {
		t.Fatalf("%d purchases with no room to buy", reg.buys)
	}
}

// Repeated budget refusals must not exhaust the repair's attempts. When the
// cap is raised the same shortage remains eligible for automatic purchase.
func TestBudgetRefusalsNeverBecomeRepairStrikes(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.svc.primeSizing(map[string]PoolSizing{FleetPool(ProtoVlessXhttp, ""): sizingFor(1)})
	h.svc.SetDomainRegistrar(newFakeRegistrar(), DefaultDomainPolicy())
	h.spendCaps(SpendRegistrar, 0, 0)
	inc, err := h.svc.openPool(ctx, DomainPool, CauseShortOfDomains, "need domain", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMechanic(h.svc, h.prov)
	for i := 0; i < 4; i++ {
		if err := m.run(ctx, *inc, PlaybookBuyDomain, ModeAuto); err != nil {
			t.Fatal(err)
		}
		if got := h.settle(inc.ID); got.Status != IncidentOpen {
			t.Fatalf("budget refusal closed shortage: %+v", got)
		}
	}
	var held, failed int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'held'), count(*) FILTER (WHERE status = 'failed') FROM entry_repairs WHERE incident_id = $1`, inc.ID).Scan(&held, &failed); err != nil {
		t.Fatal(err)
	}
	if held != 4 || failed != 0 {
		t.Fatalf("budget refusals: held %d, failed %d", held, failed)
	}
	// Let the ordinary retry cooldown pass; there must be no human handoff.
	if _, err := h.pool.Exec(ctx, `UPDATE entry_repairs SET started_at = now() - interval '1 hour' WHERE incident_id = $1`, inc.ID); err != nil {
		t.Fatal(err)
	}
	h.spendCaps(SpendRegistrar, generousCapCents, generousCapCents)
	if why, blocked, err := m.poolRails(ctx, *inc, PlaybookBuyDomain, mustPolicy(t, h.svc)); err != nil || blocked {
		t.Fatalf("money restored: blocked %v (%s), %v", blocked, why, err)
	}
}
