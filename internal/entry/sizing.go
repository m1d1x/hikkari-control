package entry

// sizing.go is how many machines a pool needs for its load (roadmap 42
// П9.16): the target the owner used to set by hand in the console, now
// derived by the hub from what the pool will carry (demand.go, П9.14) and
// what one machine of the plan it orders can carry (capacity.go, П9.15).
//
// # The formula
//
//	machines = ⌈ forecast Mbit/s / (capacity Mbit/s × fleetHeadroom) ⌉, at least 1
//
// and the loss buffer of П9.10 on top, unchanged (fleet.go). The headroom is
// the room for a burst inside an hour — a constant, not a setting: the owner's
// decision of 27.10 leaves him one knob, the monthly money cap.
//
// # Never shrink blind
//
// The hub hands back what `keep` no longer covers (shrink.go, П9.17), so a
// reading low for want of data would sell machines people are on. Every case
// where the reading is not the whole truth therefore keeps the pool at least
// at what it has live:
//
//   - no dataset wired (analytics off) — the hub cannot size anything, and
//     holds exactly what it has: it does not start buying blind either;
//   - only an observed traffic floor, not a measured capacity ceiling — a
//     lightly loaded machine does not prove that a second machine is needed;
//   - no capacity for the plan the hub orders, nor for any machine of the
//     pool — a forecast in Mbit/s has nothing to be divided by;
//   - demand stale (the relays fell silent together) or incomplete (some of
//     them did) — the pool carries more than it reads. A rise the reading
//     does show still buys: what is missing can only add to it.
//
// A pool that carried nothing in the whole window holds one machine: the
// first person to come must not find it empty (cold start).
//
// # The quota floor
//
// A plan comes with a monthly traffic allowance, and the hoster bills what
// goes past it. When a machine's allowance costs less than the same traffic
// as overage, buying machines for their allowance is the cheaper way to carry
// the month, and the pool holds at least ⌈month's volume / allowance⌉ — a
// money decision, so it is read off the prices, not set by anybody (П9.18).
// Vultr pools the allowance across the account; a floor per pool adds up to
// the same account-wide sum.
//
// # Read once per bucket
//
// The detector reads every pool's forecast on every pass, and the reading is
// a fortnight of the dataset. It cannot change faster than a bucket fills,
// so it is kept for one bucket.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// fleetHeadroom is how full the hub plans a machine to run at its peak: the
// rest is a burst inside the hour, which a five-minute reading flattens.
const fleetHeadroom = 0.7

// fleetShrinkHeadroom is how full the pool must stay WITHOUT a machine for the
// hub to hand that machine back (П9.17). Lower than fleetHeadroom on purpose:
// with one line for buying and selling, a pool hovering at it would buy at
// noon and sell at five past, and pay for an admission each time.
const fleetShrinkHeadroom = 0.5

// PoolSizing is what a pool's target was read from.
type PoolSizing struct {
	// Demand is the pool's forecast; nil when the hub has no dataset.
	Demand *PoolDemand `json:"demand,omitempty"`
	// Capacity is the figure the forecast was divided by: the plan the hub
	// orders, or the best-measured plan of the pool's own machines. Nil when
	// neither is known.
	Capacity *PlanCapacity `json:"capacity,omitempty"`
	// Plan is what the hub orders a machine on, as the hoster sells it: its
	// price is what the money cap is checked against before a shortage is
	// called (fleetbudget.go), its traffic allowance what the quota floor is
	// read from. Nil without a hoster or a default plan.
	Plan *BoxPlan `json:"plan,omitempty"`
}

// loadTarget is what a pool's load asks of it: `target`, the live machines it
// must have (below it the hub buys), and `keep`, the machines it holds on to
// (above it, for the hold, the hub hands one back) — the same forecast over
// fleetHeadroom and fleetShrinkHeadroom. keep ≥ target always; every blind
// reading sets both to what it holds. And the sentence that says why.
func loadTarget(sz PoolSizing, live int) (target, keep int, why string) {
	hold := max(1, live)
	d, c := sz.Demand, sz.Capacity
	switch {
	case d == nil:
		return live, live, fmt.Sprintf("нагрузки не видно (нет датасета) — держим что есть: %d", live)
	case d.Basis == DemandNone:
		return 1, 1, "пул ничего не вёз за две недели — держим одну машину на первого пришедшего"
	case c != nil && c.Source == CapacityFloor:
		// Organic traffic is a lower bound on capacity, never a ceiling. In
		// particular health probes must not buy more boxes just because their
		// aggregate traffic exceeds one lightly used box's historical median.
		n, k := hold, hold
		why := fmt.Sprintf("видели минимум %.3f Мбит/с, предел мощности не измерен — держим %d, по нижней границе парк не наращиваем", c.Mbps, hold)
		if q, qwhy := quotaFloor(d, sz.Plan); qwhy != "" {
			n, k = max(n, q), max(k, q)
			why += "; " + qwhy
		}
		return n, k, why
	case c == nil || c.Mbps <= 0:
		return hold, hold, fmt.Sprintf("ёмкость машины не мерена — спрос %.1f Мбит/с делить не на что, держим что есть: %d",
			d.ForecastMbps, hold)
	}
	n := max(1, int(math.Ceil(d.ForecastMbps/(c.Mbps*fleetHeadroom))))
	k := max(n, int(math.Ceil(d.ForecastMbps/(c.Mbps*fleetShrinkHeadroom))))
	why = fmt.Sprintf("спрос %.1f Мбит/с к %s, машина держит %.0f (%s), по %.0f%% — нужно %d, сдавать ниже %.0f%% — держим до %d",
		d.ForecastMbps, d.PeakAt.Format("02.01 15:04"), c.Mbps, c.Source, fleetHeadroom*100, n, fleetShrinkHeadroom*100, k)
	if q, qwhy := quotaFloor(d, sz.Plan); qwhy != "" {
		why += "; " + qwhy
		n, k = max(n, q), max(k, q)
	}
	switch {
	case !d.Fresh && k < live:
		return n, live, why + fmt.Sprintf("; но данные старые (последний замер %s) — не сжимаем, держим %d",
			d.LastAt.Format("02.01 15:04"), live)
	case !d.Complete && k < live:
		return n, live, why + fmt.Sprintf("; но молчат %d релеев — не сжимаем, держим %d", len(d.Silent), live)
	}
	return n, k, why
}

// quotaFloor is the machines a pool holds for their traffic allowance alone,
// and the sentence that says why — or why not, when overage is the cheaper
// way. Zero and nothing to say without the figures.
func quotaFloor(d *PoolDemand, plan *BoxPlan) (int, string) {
	if plan == nil || plan.BandwidthGB <= 0 || plan.OverageCentsPerTB <= 0 || d.MonthGB <= 0 {
		return 0, ""
	}
	q := int(math.Ceil(d.MonthGB / float64(plan.BandwidthGB)))
	// Cents per TB both ways: the machine's price over its allowance against
	// the hoster's price for traffic past it.
	if plan.MonthlyCents*1000 >= plan.BandwidthGB*plan.OverageCentsPerTB {
		return 0, fmt.Sprintf("трафик %.0f ГБ в месяц — превышение квоты дешевле машины, докупать ради квоты не будем", d.MonthGB)
	}
	return q, fmt.Sprintf("трафик %.0f ГБ в месяц, квота машины %d ГБ — машина дешевле превышения, ради квоты не меньше %d",
		d.MonthGB, plan.BandwidthGB, q)
}

// fleetSizing is one reading of every pool's sizing inputs.
type fleetSizing struct {
	at    time.Time
	pools map[string]PoolSizing
	// primed: set by a test, never re-read.
	primed bool
}

// poolSizings reads the sizing inputs of `pools`, from the bucket-old reading
// when it covers them all. Nil when the hub has no dataset or could not read
// it; every pool then holds what it has.
func (s *Service) poolSizings(ctx context.Context, policy DetectionPolicy, pools []string) (map[string]PoolSizing, error) {
	s.sizingMu.Lock()
	cached := s.sizing
	s.sizingMu.Unlock()
	if cached != nil && (cached.primed || s.now().Sub(cached.at) < demandBucket && coversPools(cached.pools, pools)) {
		return cached.pools, nil
	}
	if s.load == nil {
		return nil, nil
	}
	out, err := s.readSizings(ctx, policy, pools)
	if err != nil {
		// The dataset going down must not stop the fleet: every pool holds
		// what it has until it is back (loadTarget with no demand), and the
		// failure is not kept, so the next pass asks again.
		applog.Error(ctx, "entry", "fleet sizing read failed", applog.F("err", err.Error()))
		return nil, nil
	}
	s.sizingMu.Lock()
	s.sizing = &fleetSizing{at: s.now(), pools: out}
	s.sizingMu.Unlock()
	return out, nil
}

func coversPools(have map[string]PoolSizing, want []string) bool {
	for _, p := range want {
		if _, ok := have[p]; !ok {
			return false
		}
	}
	return true
}

// readSizings reads demand and capacity for every pool from the dataset.
func (s *Service) readSizings(ctx context.Context, policy DetectionPolicy, pools []string) (map[string]PoolSizing, error) {
	hoster := s.hosterKind()
	horizons := make(map[string]time.Duration, len(pools))
	for _, pool := range pools {
		var h time.Duration
		if hoster != "" {
			proto, region := splitPool(pool)
			var err error
			if h, err = s.fleetHorizon(ctx, hoster, proto, region); err != nil {
				return nil, fmt.Errorf("entry: horizon of %s: %w", pool, err)
			}
		}
		horizons[pool] = h
	}
	demands, err := s.poolDemands(ctx, horizons)
	if err != nil {
		return nil, err
	}
	capacities, err := s.PlanCapacities(ctx)
	if err != nil {
		return nil, err
	}
	ordered, err := s.orderedPlan(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := s.orderedPlanSpec(ctx, ordered)
	if err != nil {
		return nil, err
	}
	relays, err := s.fleetRelays(ctx)
	if err != nil {
		return nil, err
	}
	poolPlans := map[string]map[planRef]bool{}
	for _, r := range relays {
		if r.plan == "" {
			continue
		}
		if poolPlans[r.pool] == nil {
			poolPlans[r.pool] = map[planRef]bool{}
		}
		poolPlans[r.pool][planRef{r.provider, r.plan}] = true
	}
	byPlan := map[planRef]PlanCapacity{}
	for _, c := range capacities {
		byPlan[planRef{c.Provider, c.Plan}] = c
	}
	out := make(map[string]PoolSizing, len(pools))
	for _, pool := range pools {
		var sz PoolSizing
		if d, ok := demands[pool]; ok {
			sz.Demand = &d
		}
		if c, ok := poolCapacity(byPlan, ordered, poolPlans[pool]); ok {
			sz.Capacity = &c
		}
		sz.Plan = plan
		out[pool] = sz
	}
	return out, nil
}

// planRef names a plan at a hoster.
type planRef struct{ provider, plan string }

// orderedPlan is the plan the hub orders a new machine on: the hoster's saved
// default. Zero when there is no hoster or no default.
func (s *Service) orderedPlan(ctx context.Context) (planRef, error) {
	h, err := s.hoster()
	if err != nil {
		return planRef{}, nil
	}
	p, err := s.savedHostingPolicy(ctx, s.pool, h)
	if err != nil {
		return planRef{}, err
	}
	return planRef{h.kind, p.Plan}, nil
}

// orderedPlanSpec is the ordered plan as the hoster sells it now. Read once
// per sizing reading (a bucket), not per detector pass: it is a call to the
// hoster's API.
func (s *Service) orderedPlanSpec(ctx context.Context, ordered planRef) (*BoxPlan, error) {
	h, err := s.hoster()
	if err != nil || ordered.plan == "" {
		return nil, nil
	}
	plans, err := h.bp.Plans(ctx)
	if err != nil {
		return nil, fmt.Errorf("entry: plans of %s: %w", h.kind, err)
	}
	for _, p := range plans {
		if p.ID == ordered.plan {
			return &p, nil
		}
	}
	return nil, nil
}

// poolCapacity is the capacity a pool's forecast is divided by: the plan the
// hub would order, when measured; otherwise the best-measured plan among the
// pool's own machines, the smaller figure on a tie — what is being estimated is
// how many machines are enough, and a high guess is the one that lags people.
func poolCapacity(byPlan map[planRef]PlanCapacity, ordered planRef, own map[planRef]bool) (PlanCapacity, bool) {
	if c, ok := byPlan[ordered]; ok && c.Source != CapacityNone && c.Source != CapacityFloor && c.Mbps > 0 {
		return c, true
	}
	var cands []PlanCapacity
	if c, ok := byPlan[ordered]; ok && c.Source == CapacityFloor && c.Mbps > 0 {
		cands = append(cands, c)
	}
	for ref := range own {
		if c, ok := byPlan[ref]; ok && c.Source != CapacityNone && c.Mbps > 0 {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return PlanCapacity{}, false
	}
	sort.Slice(cands, func(i, j int) bool {
		if ri, rj := capacityRank[cands[i].Source], capacityRank[cands[j].Source]; ri != rj {
			return ri < rj
		}
		return cands[i].Mbps < cands[j].Mbps
	})
	return cands[0], true
}

// primeSizing fixes every pool's sizing inputs for a test, as the dataset
// would have read them.
func (s *Service) primeSizing(pools map[string]PoolSizing) {
	s.sizingMu.Lock()
	s.sizing = &fleetSizing{at: s.now(), pools: pools, primed: true}
	s.sizingMu.Unlock()
}
