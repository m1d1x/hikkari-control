package entry

// hosting.go is the hoster under key (roadmap 42 П9.2): the hub's side of
// [BoxProvider]. It holds the enabled hosting account, enforces the money
// gate, orders a machine and hands one back.
//
// # Why this looks so much like domains.go
//
// Because it is the same shape of problem, and the owner's rule about it is
// the same: an account whose credentials are sealed in Postgres, a policy the
// console edits, caps the hub checks before it spends, and a seam so the
// entry package never imports a provider. A machine differs from a domain in
// one way that matters — it is a recurring charge, not a purchase — which is
// why the cap is read against the fleet's monthly bill and not against a
// month's receipts.
//
// # Make before break
//
// Nothing here retires a machine to pay for its replacement. An order is
// refused when the fleet would go over budget, and the replacement repair
// admits the new box before the old one is drained (§4, rail 5). A hub that
// freed capacity first would be a hub that can take the pool below its floor
// by failing halfway.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// HostingPolicy is what a hosting account orders when an order names
// nothing: where and which plan. What it may spend is the hosting group's
// (spend.go), not the account's.
type HostingPolicy struct {
	Region string
	Plan   string
}

// hostingAccount is the hoster in force now, swapped whole when the owner
// saves a provider — so an order in flight keeps the client and the caps it
// started with, as a domain purchase does.
type hostingAccount struct {
	kind   string
	bp     BoxProvider
	policy HostingPolicy
}

// SetHoster swaps the hosting provider and its policy. LoadProviders calls it
// from the saved accounts; a nil provider leaves the hub unable to order or
// hand back a machine ([ErrNoHoster]), which is where a hub whose boxes were
// all rented by hand sits.
func (s *Service) SetHoster(kind string, bp BoxProvider, p HostingPolicy) {
	s.hosting.Store(&hostingAccount{kind: kind, bp: bp, policy: p})
}

// hoster is the account in force, or ErrNoHoster.
func (s *Service) hoster() (*hostingAccount, error) {
	h := s.hosting.Load()
	if h == nil || h.bp == nil {
		return nil, ErrNoHoster
	}
	return h, nil
}

// hosterKind is the kind of the active hosting account, empty when there is
// none.
func (s *Service) hosterKind() string {
	if h := s.hosting.Load(); h != nil && h.bp != nil {
		return h.kind
	}
	return ""
}

// HostingCatalog is what the console needs to offer an order.
type HostingCatalog struct {
	// Kind is the active hoster ("vultr"), empty when none is enabled.
	Kind    string      `json:"kind"`
	Regions []BoxRegion `json:"regions"`
	Plans   []BoxPlan   `json:"plans"`
	// Region and Plan are the saved defaults, pre-selected by the console.
	Region string `json:"defaultRegion,omitempty"`
	Plan   string `json:"defaultPlan,omitempty"`
	// Spend is the hosting group against its caps: what an order is
	// checked against.
	Spend SpendReading `json:"spend"`
}

// HostingCatalog reads the hoster's offer and the hub's budget. One call, two
// round trips to the provider: the console's order form needs both lists, and
// neither is worth caching for a screen that is opened by hand.
func (s *Service) HostingCatalog(ctx context.Context) (HostingCatalog, error) {
	h, err := s.hoster()
	if err != nil {
		return HostingCatalog{}, err
	}
	regions, err := h.bp.Regions(ctx)
	if err != nil {
		return HostingCatalog{}, err
	}
	plans, err := h.bp.Plans(ctx)
	if err != nil {
		return HostingCatalog{}, err
	}
	spend, err := s.spendOf(ctx, s.pool, SpendHosting)
	if err != nil {
		return HostingCatalog{}, err
	}
	if regions == nil {
		regions = []BoxRegion{}
	}
	if plans == nil {
		plans = []BoxPlan{}
	}
	return HostingCatalog{
		Kind: h.kind, Regions: regions, Plans: plans,
		Region: h.policy.Region, Plan: h.policy.Plan, Spend: spend,
	}, nil
}

// savedHostingPolicy is the hoster's policy as the owner saved it, read on q.
//
// Not the copy in memory: a save in the console reaches this process at
// once, but the one-shot CLI is a process of its own, and a switch the owner
// turned there must hold here too (audit mine: "лимиты хостера в памяти").
// A hoster wired by code with no saved account — a test — keeps the policy
// it was wired with; one switched off since is no hoster at all.
func (s *Service) savedHostingPolicy(ctx context.Context, q db.DBTX, h *hostingAccount) (HostingPolicy, error) {
	var p HostingPolicy
	var enabled bool
	err := q.QueryRow(ctx, `
		SELECT enabled, default_region, default_plan
		FROM entry_providers WHERE kind = $1
	`, h.kind).Scan(&enabled, &p.Region, &p.Plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return h.policy, nil
	}
	if err != nil {
		return HostingPolicy{}, fmt.Errorf("entry: hosting policy: %w", err)
	}
	if !enabled {
		return HostingPolicy{}, ErrNoHoster
	}
	return p, nil
}

// HostingInstance is one machine at the hoster next to the hub's own record of
// it.
type HostingInstance struct {
	BoxInstance
	// Box is the box this instance belongs to, empty when no row names it.
	Box string `json:"box,omitempty"`
	// Orphan: the hub is being charged for a machine no box in service uses.
	// Either an order died between the charge and the record, or the box was
	// retired and the hoster has not deleted it yet (Retired says which) —
	// the one thing about the hoster the console cannot work out for itself.
	Orphan  bool `json:"orphan"`
	Retired bool `json:"retired,omitempty"`
}

// HostingInstances lists the account's machines and says which of them the hub
// has a box for.
//
// Ours is decided by the instance id in entry_boxes, never by the label: the
// label is a convenience for the owner's eyes in the hoster's panel, and a
// machine the owner renamed there must not become invisible to the orphan
// check.
func (s *Service) HostingInstances(ctx context.Context) ([]HostingInstance, error) {
	h, err := s.hoster()
	if err != nil {
		return nil, err
	}
	list, err := h.bp.Instances(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT provider_instance_id, name, retired_at IS NOT NULL
		FROM entry_boxes WHERE provider_instance_id IS NOT NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("entry: boxes by instance: %w", err)
	}
	defer rows.Close()
	type owner struct {
		box     string
		retired bool
	}
	byInstance := map[string]owner{}
	for rows.Next() {
		var id string
		var o owner
		if err := rows.Scan(&id, &o.box, &o.retired); err != nil {
			return nil, err
		}
		byInstance[id] = o
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]HostingInstance, 0, len(list))
	for _, inst := range list {
		o := byInstance[inst.ID]
		hi := HostingInstance{BoxInstance: inst, Box: o.box, Retired: o.retired}
		hi.Orphan = o.box == "" || o.retired
		out = append(out, hi)
	}
	return out, nil
}

// OrderSpec is a machine the console (or a repair) asks for. Everything may be
// left empty: the region and plan then come from the hoster's saved defaults,
// and the name from the region.
type OrderSpec struct {
	Region string
	Plan   string
	// Name is the box name; generated from the region's city when empty.
	Name string
	// Proto and Region of the pool sense (what the relays on it speak, which
	// voice region it serves) — the same fields CreateBox takes.
	Proto       string
	VoiceRegion string
}

// orderedBox is a machine the hoster has handed over, before the hub has a row
// for it.
type orderedBox struct {
	instance     BoxInstance
	plan         BoxPlan
	name         string
	location     string
	monthlyCents int
	// order is the reservation (entry_box_orders) the machine was bought
	// under, which the caller settles once the box row is or is not written.
	order uuid.UUID
}

// instanceWaitTimeout is how long a freshly ordered machine has to show an
// address. Vultr builds one in under a minute; five is slack for a region
// under load, and failing beats waiting forever on a machine that is already
// being charged for.
const instanceWaitTimeout = 5 * time.Minute

// instanceWaitPoll is how often the order asks; Service.instancePoll holds it
// so a test need not wait out five seconds of a machine that is already up.
const instanceWaitPoll = 5 * time.Second

// ErrProviderRefused: the provider answered and said no — nothing was created
// and nothing charged. An adapter wraps its provider's definite refusals in
// it; any other failure of a call that spends is read as "nobody knows",
// which is the only safe reading of a lost answer.
var ErrProviderRefused = errors.New("entry: the provider refused")

// ErrRegionCapacity is a definite refusal caused only by exhausted regional capacity.
var ErrRegionCapacity = errors.New("entry: region capacity exhausted")

// lockMachineOrders serializes order reservations across every process on
// this database — the hub and the one-shot CLI alike: a transaction-scoped
// advisory lock (42·07, beside the domain purchases' 42·06), held only while
// the budget is read and the reservation is written.
const lockMachineOrders = `SELECT pg_advisory_xact_lock(4207)`

// order places one order against the hoster: it resolves the region and plan,
// reserves the money, puts the hub's ssh key on the account, orders, and
// waits for the address.
//
// # Where the money gate sits
//
// In Postgres, as the domain purchase's does (audit В3). Under an advisory
// lock the order checks the hosting group's day and month with this machine
// added (machineAffordable, spend.go), and writes a reservation carrying the
// rent — then commits and asks the hoster. The spend counts the reservation
// from that moment, so a second order racing this one, from this process or
// from the CLI, reads it and is refused.
//
// The lock is not held across the hoster's answer and the boot: the
// reservation is what holds the money, and a lock held for minutes would
// stall every other reader of the gate. The reservation was the hard part —
// it needs a name and a price before there is an instance — and both are
// known here: the name is chosen under the same lock.
//
// # What happens if nobody hears the answer
//
// The reservation stays `reserved` and counted, and the mechanic's pass
// (ReleaseUndeleted) settles it later by looking for the instance under the
// label the order gave it: deleted if it exists, released if not. The same
// for a hub that dies mid-order.
func (s *Service) order(ctx context.Context, spec OrderSpec, hubPub string) (orderedBox, error) {
	h, err := s.hoster()
	if err != nil {
		return orderedBox{}, err
	}
	saved, err := s.savedHostingPolicy(ctx, s.pool, h)
	if err != nil {
		return orderedBox{}, err
	}
	region := strings.TrimSpace(spec.Region)
	if region == "" {
		region = saved.Region
	}
	planID := strings.TrimSpace(spec.Plan)
	if planID == "" {
		planID = saved.Plan
	}
	if region == "" || planID == "" {
		return orderedBox{}, fmt.Errorf("%w: у хостера не выбран регион или план по умолчанию", ErrRejected)
	}

	regions, err := h.bp.Regions(ctx)
	if err != nil {
		return orderedBox{}, err
	}
	var reg BoxRegion
	for _, r := range regions {
		if r.ID == region {
			reg = r
			break
		}
	}
	if reg.ID == "" {
		return orderedBox{}, fmt.Errorf("%w: регион %q", ErrNoSuchPlan, region)
	}
	plans, err := h.bp.Plans(ctx)
	if err != nil {
		return orderedBox{}, err
	}
	var plan BoxPlan
	for _, p := range plans {
		if p.ID == planID {
			plan = p
			break
		}
	}
	switch {
	case plan.ID == "":
		return orderedBox{}, fmt.Errorf("%w: план %q", ErrNoSuchPlan, planID)
	case !plan.AvailableIn(reg.ID):
		return orderedBox{}, fmt.Errorf("%w: план %s не продаётся в %s", ErrNoSuchPlan, plan.ID, reg.ID)
	}

	// A default region is a preference. A definite capacity refusal can try
	// another location on the same continent at the same price. Explicit
	// operator regions, unknown geography and uncertain orders never move.
	candidates := []BoxRegion{reg}
	if strings.TrimSpace(spec.Region) == "" && reg.Continent != "" {
		for _, alternative := range regions {
			if alternative.ID != reg.ID && alternative.Continent == reg.Continent && plan.AvailableIn(alternative.ID) {
				candidates = append(candidates, alternative)
			}
		}
		slices.SortStableFunc(candidates[1:], func(a, b BoxRegion) int {
			if (a.Country == reg.Country) != (b.Country == reg.Country) {
				if a.Country == reg.Country {
					return -1
				}
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
	}
	for i, candidate := range candidates {
		ordered, err := s.orderInRegion(ctx, h, candidate, plan, spec, hubPub)
		if !errors.Is(err, ErrRegionCapacity) || !errors.Is(err, ErrProviderRefused) || i == len(candidates)-1 {
			return ordered, err
		}
		applog.Info(ctx, "entry", "hosting region sold out, trying same-price alternative",
			applog.F("region", candidate.ID), applog.F("next_region", candidates[i+1].ID), applog.F("plan", plan.ID))
	}
	return orderedBox{}, ErrRegionCapacity
}

// Every regional attempt gets its own locked budget reservation. A refused
// attempt is released before another region can reserve or spend anything.
func (s *Service) orderInRegion(ctx context.Context, h *hostingAccount, reg BoxRegion, plan BoxPlan, spec OrderSpec, hubPub string) (orderedBox, error) {
	id, name, err := s.reserveOrder(ctx, h, reg, plan, strings.TrimSpace(spec.Name))
	if err != nil {
		return orderedBox{}, err
	}
	keyID, err := h.bp.EnsureSSHKey(ctx, "nekko-hub", hubPub)
	if err != nil {
		s.settleOrder(ctx, id, orderReleased, "", err)
		return orderedBox{}, fmt.Errorf("ssh-ключ хаба у хостера: %w", err)
	}
	inst, err := h.bp.Create(ctx, BoxOrder{
		Region: reg.ID, Plan: plan.ID, Label: "nekko-" + name, Hostname: name,
		SSHKeyID: keyID, Tag: hostingTag,
	})
	if err != nil {
		if errors.Is(err, ErrProviderRefused) {
			s.settleOrder(ctx, id, orderRefused, "", err)
			return orderedBox{}, fmt.Errorf("заказ машины: %w", err)
		}
		// Nobody knows whether the hoster created it: the reservation stays
		// counted until the pass looks for the label.
		s.settleOrder(ctx, id, orderReserved, "", err)
		return orderedBox{}, fmt.Errorf("заказ машины: ответ хостера не получен — заказ остаётся в бюджете, пока хаб не выяснит, создана ли машина: %w", err)
	}
	s.settleOrder(ctx, id, orderCreated, inst.ID, nil)
	addressed, err := s.awaitAddress(ctx, h.bp, inst)
	if err != nil {
		return orderedBox{}, s.dropOrder(ctx, h.bp, id, inst.ID, err)
	}
	return orderedBox{
		instance: addressed, plan: plan, name: name,
		location:     location(reg),
		monthlyCents: plan.MonthlyCents,
		order:        id,
	}, nil
}

// Reservation statuses (entry_box_orders.status, migration 075).
const (
	orderReserved  = "reserved"
	orderCreated   = "created"
	orderUndeleted = "undeleted"
	orderRecorded  = "recorded"
	orderReleased  = "released"
	orderRefused   = "refused"
)

// reserveOrder is the money gate: under lockMachineOrders, the hosting
// group's caps read on this transaction, and the reservation that holds the
// money until the order settles. The name is chosen here too, so two orders
// racing cannot take one.
func (s *Service) reserveOrder(ctx context.Context, h *hostingAccount, reg BoxRegion, plan BoxPlan, name string) (uuid.UUID, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, lockMachineOrders); err != nil {
		return uuid.Nil, "", fmt.Errorf("entry: lock machine orders: %w", err)
	}
	// The account must still be switched on: one switched off since the
	// order began is no hoster to order from.
	if _, err := s.savedHostingPolicy(ctx, tx, h); err != nil {
		return uuid.Nil, "", err
	}
	if err := s.machineAffordable(ctx, tx, plan.MonthlyCents); err != nil {
		return uuid.Nil, "", err
	}
	if name == "" {
		if name, err = nextBoxName(ctx, tx, reg); err != nil {
			return uuid.Nil, "", err
		}
	} else if err := nameFree(ctx, tx, name); err != nil {
		return uuid.Nil, "", err
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO entry_box_orders (name, provider, provider_region, plan, monthly_cents)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, name, h.kind, reg.ID, plan.ID, plan.MonthlyCents).Scan(&id); err != nil {
		if isUniqueViolation(err) {
			return uuid.Nil, "", fmt.Errorf("%w: имя %s уже заказывается", ErrRejected, name)
		}
		return uuid.Nil, "", fmt.Errorf("%w: заказ %s: %v", ErrRejected, name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, "", err
	}
	return id, name, nil
}

// settleOrder moves a reservation on. cause, when set, is kept as its error —
// what the console reads about a reservation still counted.
func (s *Service) settleOrder(ctx context.Context, id uuid.UUID, status, instanceID string, cause error) {
	msg := ""
	if cause != nil {
		msg = clip(cause.Error(), 512)
	}
	if _, err := s.pool.Exec(context.WithoutCancel(ctx), `
		UPDATE entry_box_orders
		SET status = $2, instance_id = coalesce(NULLIF($3, ''), instance_id), error = $4, updated_at = now()
		WHERE id = $1
	`, id, status, instanceID, msg); err != nil {
		// The row stays as it was — counted, if it was counted — and the
		// pass settles it: over-counting is the side to fail on.
		applog.Error(ctx, "entry", "machine order not settled",
			applog.F("order", id.String()), applog.F("status", status), applog.F("err", err.Error()))
	}
}

// dropOrder hands back a machine an order created and the hub will not keep:
// no address in time, or no box row for it. Deleted, the reservation is
// released; not, it is `undeleted` — counted, and deleted again by the pass.
// Returns cause with what became of the machine.
func (s *Service) dropOrder(ctx context.Context, bp BoxProvider, id uuid.UUID, instanceID string, cause error) error {
	if err := bp.Delete(context.WithoutCancel(ctx), instanceID); err != nil && !errors.Is(err, ErrNoSuchInstance) {
		s.settleOrder(ctx, id, orderUndeleted, instanceID, err)
		applog.Error(ctx, "entry", "ordered instance not kept and not deleted",
			applog.F("instance", instanceID), applog.F("err", err.Error()))
		return fmt.Errorf("%w; машину %s у хостера удалить не вышло (%v) — она в бюджете, хаб удалит её позже", cause, instanceID, err)
	}
	s.settleOrder(ctx, id, orderReleased, instanceID, cause)
	return fmt.Errorf("%w; машина %s удалена у хостера", cause, instanceID)
}

// money renders whole cents the way the console shows them, so a refusal the
// owner reads names a price and not a count of cents.
func money(cents int) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// hostingTag marks every instance the hub ordered.
const hostingTag = "nekko-entry"

// location is how a region reads on a box row.
func location(r BoxRegion) string {
	if r.Country != "" {
		return r.City + ", " + r.Country
	}
	return r.City
}

// awaitAddress polls the hoster until the machine has a public address.
//
// A machine with no address is a machine nothing can be deployed to, and the
// hoster answers an order before it has built one. The wait is here rather
// than inside the provider adapter so the caller can be a job that logs it.
func (s *Service) awaitAddress(ctx context.Context, bp BoxProvider, inst BoxInstance) (BoxInstance, error) {
	if inst.Address != "" {
		return inst, nil
	}
	deadline := s.now().Add(instanceWaitTimeout)
	for {
		select {
		case <-ctx.Done():
			return BoxInstance{}, ctx.Err()
		case <-time.After(s.instancePoll):
		}
		got, err := bp.Instance(ctx, inst.ID)
		if err != nil {
			return BoxInstance{}, fmt.Errorf("машина %s у хостера: %w", inst.ID, err)
		}
		if got.Address != "" {
			return got, nil
		}
		if s.now().After(deadline) {
			return BoxInstance{}, fmt.Errorf(
				"машина %s за %s не получила адрес (состояние %q) — инстанс заказан, его видно в списке хостера",
				inst.ID, instanceWaitTimeout, got.Status)
		}
	}
}

// nextBoxName invents the next free name for a machine in this region:
// <city letters><number>, as the pool's names have always read (ams1, fra2).
//
// The number is the first free one for that prefix, not "one past the
// largest": names are handed back when a box is retired, and a fleet that has
// churned twenty machines in Amsterdam should not be calling the next one
// ams21. Retired boxes keep their row, so the name is only free if no row at
// all holds it — and no order in flight has it either. Read on the
// reservation's transaction, under the orders' lock.
func nextBoxName(ctx context.Context, q db.DBTX, reg BoxRegion) (string, error) {
	prefix := cityPrefix(reg)
	if prefix == "" {
		return "", fmt.Errorf("%w: из региона %q не вывести имя машины, назови его сам", ErrRejected, reg.ID)
	}
	rows, err := q.Query(ctx, selectTakenNames, prefix+"%")
	if err != nil {
		return "", fmt.Errorf("entry: box names: %w", err)
	}
	defer rows.Close()
	taken := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		taken[n] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for i := 1; i <= 99; i++ {
		name := fmt.Sprintf("%s%d", prefix, i)
		if !taken[name] {
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: в %s заняты все имена до %s99", ErrRejected, reg.ID, prefix)
}

// selectTakenNames is every name a box row or an order in flight holds.
const selectTakenNames = `
	SELECT name FROM entry_boxes WHERE name LIKE $1
	UNION
	SELECT name FROM entry_box_orders
	WHERE name LIKE $1 AND status IN ('reserved', 'created', 'undeleted')`

// nameFree refuses a name the order was given that a box or another order
// already holds — before anything is paid for, not after (the box row's key
// used to be what refused it, with the machine already rented).
func nameFree(ctx context.Context, q db.DBTX, name string) error {
	var taken bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (`+selectTakenNames+`)`, name).Scan(&taken); err != nil {
		return fmt.Errorf("entry: box name %s: %w", name, err)
	}
	if taken {
		return fmt.Errorf("%w: имя %s уже занято", ErrRejected, name)
	}
	return nil
}

// cityPrefix is the first three ascii letters of the region's city, which is
// where names like ams1 come from. Falls back to the hoster's region id when
// the city gives nothing usable.
func cityPrefix(reg BoxRegion) string {
	for _, src := range []string{reg.City, reg.ID} {
		letters := make([]rune, 0, 3)
		for _, r := range strings.ToLower(src) {
			if r >= 'a' && r <= 'z' {
				letters = append(letters, r)
			}
			if len(letters) == 3 {
				break
			}
		}
		if len(letters) == 3 {
			return string(letters)
		}
	}
	return ""
}

// DeleteBoxInstance hands a retired box's machine back to the hoster and stops
// the charge. A machine the hoster no longer has counts as handed back.
//
// Refuses a box still in service: the pool decides when a machine stops
// carrying people, and deleting one under a live relay would take its holders
// down without the make-before-break the pool requires. A box with no
// instance id was rented by hand — there is nothing to delete, which this
// call says rather than treating as success.
func (s *Service) DeleteBoxInstance(ctx context.Context, box string) error {
	h, err := s.hoster()
	if err != nil {
		return err
	}
	var instanceID *string
	var retired *time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT provider_instance_id, retired_at FROM entry_boxes WHERE name = $1`, box,
	).Scan(&instanceID, &retired)
	if err != nil {
		return fmt.Errorf("entry: box %s: %w", box, err)
	}
	switch {
	case retired == nil:
		return fmt.Errorf("%w: машина %s в строю — сперва сдать её", ErrRejected, box)
	case instanceID == nil || *instanceID == "":
		return fmt.Errorf("%w: машина %s заведена руками, у хостера её удалять нечем", ErrRejected, box)
	}
	if err := h.bp.Delete(ctx, *instanceID); err != nil && !errors.Is(err, ErrNoSuchInstance) {
		return fmt.Errorf("удалить инстанс %s: %w", *instanceID, err)
	}
	// The charge has stopped: the box leaves the budget (С1). Until this is
	// written the retired box is counted and the pass deletes it again.
	if _, err := s.pool.Exec(context.WithoutCancel(ctx),
		`UPDATE entry_boxes SET released_at = now() WHERE name = $1 AND released_at IS NULL`, box); err != nil {
		return fmt.Errorf("entry: release %s: %w", box, err)
	}
	return nil
}

// RebootInstance restarts a box's machine through the hoster's API. The repair
// that uses it waits for ssh afterwards; this call only presses the button.
func (s *Service) RebootInstance(ctx context.Context, box string) error {
	h, err := s.hoster()
	if err != nil {
		return err
	}
	id, err := s.boxInstanceID(ctx, box)
	if err != nil {
		return err
	}
	if err := h.bp.Reboot(ctx, id); err != nil {
		return fmt.Errorf("перезагрузить %s: %w", box, err)
	}
	return nil
}

// boxInstanceID is a box's instance at the hoster, or a refusal naming why
// there is none.
func (s *Service) boxInstanceID(ctx context.Context, box string) (string, error) {
	var id *string
	err := s.pool.QueryRow(ctx,
		`SELECT provider_instance_id FROM entry_boxes WHERE name = $1`, box).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("entry: box %s: %w", box, err)
	}
	if id == nil || *id == "" {
		return "", fmt.Errorf("%w: машина %s заведена руками, у хостера её нет", ErrRejected, box)
	}
	return *id, nil
}

// boxRental is what a box costs and where it was ordered, for a replacement
// that should land on the same shelf.
type boxRental struct {
	instanceID   string
	region       string
	plan         string
	monthlyCents int
	proto        string
	voiceRegion  string
}

// boxRental reads the rental of a box. A hand-rented one comes back with an
// empty instance id and no price, which is exactly what the replacement repair
// branches on.
func (s *Service) boxRental(ctx context.Context, box string) (boxRental, error) {
	var r boxRental
	var instanceID, plan *string
	var monthly *int
	err := s.pool.QueryRow(ctx, `
		SELECT provider_instance_id, provider_region, plan, monthly_cents, proto, coalesce(region, '')
		FROM entry_boxes WHERE name = $1
	`, box).Scan(&instanceID, &r.region, &plan, &monthly, &r.proto, &r.voiceRegion)
	if err != nil {
		return boxRental{}, fmt.Errorf("entry: box %s: %w", box, err)
	}
	if instanceID != nil {
		r.instanceID = *instanceID
	}
	if plan != nil {
		r.plan = *plan
	}
	if monthly != nil {
		r.monthlyCents = *monthly
	}
	return r, nil
}

// ── Ordering a machine and standing it up ───────────────────────────────

// OrderBox orders a machine from the hoster, records it, and starts its first
// provisioning job — the console's "order a machine" (П9.2), which is the
// hand-rented path of [Provisioner.CreateAndProvision] with the renting done
// by the hub instead of by a person in a panel.
//
// Returns the box's name and its job; the job runs on its own, as it does for
// a machine typed in by hand, and the console follows it through the same live
// log.
func (p *Provisioner) OrderBox(ctx context.Context, spec OrderSpec, adminID string) (string, uuid.UUID, error) {
	name, err := p.orderAndRecord(ctx, spec)
	if err != nil {
		return "", uuid.Nil, err
	}
	// No password: the machine boots with the hub's key in authorized_keys,
	// which is the whole point of registering it with the hoster first.
	//
	// The job waits for that first boot before it logs in, as a repair's
	// standUp does. Without the wait it dialled a machine the hoster had
	// handed over seconds earlier and failed on an ssh timeout every time
	// (stand 26.10: `ams5`, the login 15 s after the order).
	job, err := p.svc.startJob(ctx, name, adminID)
	if err != nil {
		return name, uuid.Nil, err
	}
	p.svc.spawn(func(ctx context.Context) { p.runOrdered(ctx, job, name) })
	return name, job, nil
}

// runOrdered is the job body of a machine the hub has just ordered: wait for
// its first boot, then the ordinary provisioning.
func (p *Provisioner) runOrdered(ctx context.Context, id uuid.UUID, box string) {
	ctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
	defer cancel()
	// The hub ordered this machine moments ago and meets it for the first
	// time: its first login may take the host key (jobRun.firstUse).
	j := &jobRun{p: p, ctx: ctx, id: id, box: box, firstUse: trustIfNeverMet}
	err := func() error {
		acc, err := p.svc.boxAccess(ctx, box)
		if err != nil {
			return err
		}
		j.acc = acc
		j.step("boot")
		j.log("жду первую загрузку заказанной машины (до %s)", p.bootWait)
		r, err := j.awaitSSHFor(p.bootWait)
		if err != nil {
			return err
		}
		_ = r.Close()
		return j.provision("")
	}()
	j.finish(err, "готово: "+box+" в пуле")
}

// orderAndRecord places the order and writes the box row for it.
//
// The order is undone when the row cannot be written. That is the one failure
// where undoing is right: the machine was created seconds ago, carries
// nothing, and nothing in the hub refers to it — leaving it would be paying
// for debris. Every later failure (a deploy that would not come up, an
// admission that said ip_banned) leaves the box recorded, because by then the
// machine is a thing the console can see and act on.
func (p *Provisioner) orderAndRecord(ctx context.Context, spec OrderSpec) (string, error) {
	if spec.Proto == ProtoVlessXhttp {
		if err := p.svc.xhttpDomainReady(ctx); err != nil {
			return "", err
		}
	}
	svc := p.svc
	h, err := svc.hoster()
	if err != nil {
		return "", err
	}
	_, hubPub, err := svc.HubSSHKey(ctx)
	if err != nil {
		return "", fmt.Errorf("ssh-ключ хаба: %w", err)
	}
	ob, err := svc.order(ctx, spec, hubPub)
	if err != nil {
		return "", err
	}
	box := BoxSpec{
		Name: ob.name, Address: ob.instance.Address,
		Provider: h.kind, Location: ob.location, SSHUser: "root",
		Proto: spec.Proto, Region: spec.VoiceRegion,
		InstanceID: ob.instance.ID, ProviderRegion: ob.instance.Region,
		Plan: ob.plan.ID, MonthlyCents: ob.monthlyCents,
	}
	if err := svc.CreateBox(ctx, box); err != nil {
		// The machine stays in the budget until the hoster has let it go:
		// dropOrder leaves the reservation `undeleted` when it will not, and
		// the pass deletes it then.
		return "", svc.dropOrder(ctx, h.bp, ob.order, ob.instance.ID, fmt.Errorf("записать машину %s: %w", ob.name, err))
	}
	// The box row carries the price now; the budget already stopped counting
	// the reservation the moment the row named its instance.
	svc.settleOrder(ctx, ob.order, orderRecorded, ob.instance.ID, nil)
	return ob.name, nil
}

// orderAndProvision is the repair's version: order, record, deploy and admit,
// all before returning. Synchronous for the same reason
// [Provisioner.Reprovision] is — a repair has to be able to say whether it
// worked.
//
// Admission is not skipped and cannot be: provision() ends in the same probe
// from inside Russia that every relay passes, so a replacement machine enters
// the pool on a `clean` verdict and on nothing else (§10).
//
// # The machine is waited for, and handed back when it never comes
//
// The hoster's address arrives before the machine has finished its first
// boot, and sshd is the last thing up. On the stand (25.10) the first live
// replace_box dialled 22 within a second of the address and failed on a
// timeout — the machine came up a minute later, recorded, paid for, and
// carrying nothing. So the deploy starts only once ssh lets the hub in, and a
// machine that does not within bootWait, or that does and then fails its
// deploy or admission, is retired and deleted at the hoster by the hub
// itself. It carries nobody; keeping it was a bill nobody would stop, since
// the next attempt orders a new machine anyway.
func (p *Provisioner) orderAndProvision(ctx context.Context, spec OrderSpec) (string, error) {
	name, err := p.orderAndRecord(ctx, spec)
	if err != nil {
		return "", err
	}
	if err := p.standUp(ctx, name, "машина заказана, жду первой загрузки"); err != nil {
		if errors.Is(err, errNoVerdict) {
			return name, keptForAVantage(name, err)
		}
		return name, p.handBackFresh(ctx, name, err)
	}
	return name, nil
}

// keptForAVantage is the error of a machine whose admission nobody could
// judge — a deaf vantage, or a deadline not the machine's own (standUp): it
// stays rented, its relays pending, and it is the idle shelf's —
// the next top-up with a live vantage takes it before buying (adoptIdle),
// and idleHold hands it back if none comes.
func keptForAVantage(box string, cause error) error {
	return fmt.Errorf("%w; машина %s оставлена в аренде: её возьмёт следующий добор, иначе её сдадут по простою", cause, box)
}

// standUp is one job on a machine the hub rents and that carries nobody:
// wait until ssh lets the hub in, then deploy and admit. first is the job's
// opening line, which is the only thing that differs between a machine just
// ordered and an idle one taken off the shelf.
//
// Both are machines the hub rented itself, so a first login may take the host
// key of one the hub never met — an idle machine whose first deploy died
// before ssh is exactly that. Not one whose key the operator forgot: that
// waits for the operator's own job (jobRun.firstUse).
//
// # A deadline that is not the machine's own judges nothing
//
// The caller's context — a repair's twenty minutes, the hub stopping — ends
// the attempt wherever it happens to be: in the boot wait, the deploy, the
// admission probe. None of that is evidence against the machine: it was given
// less than its own bootWait and probeTimeout, and a deaf vantage's probe in
// particular is cut before it can say "no evidence" itself. So an attempt the
// caller's context ended is errNoVerdict, which every caller already reads as
// "keep the machine, not a strike" (owner's decision 27.10, §8.3). Before
// audit 42's third pass (НС-3-1) the bare context error reached
// handBackFresh: the machine nobody judged was deleted and the repair struck.
//
// The machine's own limits stay evidence: a boot past bootWait, a deploy that
// failed, a vantage that refused. A refusal outranks the cut — it was said.
func (p *Provisioner) standUp(ctx context.Context, box, first string) (err error) {
	defer func() {
		var refused admissionRefused
		if err != nil && ctx.Err() != nil && !errors.Is(err, errNoVerdict) && !errors.As(err, &refused) {
			err = fmt.Errorf("%w: попытку оборвал срок вызывающего (%v) раньше, чем машину рассудили: %v", errNoVerdict, ctx.Err(), err)
		}
	}()
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return err
	}
	return p.execJob(ctx, JobProvision, box, uuid.Nil, func(j *jobRun) error {
		j.acc = acc
		j.firstUse = trustIfNeverMet
		j.step("boot")
		j.log("%s (до %s)", first, p.bootWait)
		r, err := j.awaitSSHFor(p.bootWait)
		if err != nil {
			return err
		}
		_ = r.Close()
		return j.provision("")
	})
}

// bootWait is how long a freshly ordered machine has to let the hub in. A
// first boot is slower than a reboot (rebootWait): the hoster images the disk
// and runs its own first-boot scripts before sshd. Ten minutes is well past
// what Vultr takes on the stand, and short of a repair waiting forever.
const bootWait = 10 * time.Minute

// handBackFresh retires a machine the hub rents that never entered the pool —
// one just ordered, or an idle one that failed when a repair took it — and
// deletes it at the hoster. cause is why it did not make it; the returned
// error carries both, and whether the machine was handed back.
func (p *Provisioner) handBackFresh(ctx context.Context, box string, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if held, err := p.svc.inOperatorsHands(ctx, box); err != nil {
		return fmt.Errorf("%w; машину %s не сдавали: %v", cause, box, err)
	} else if held {
		return fmt.Errorf("%w; машину %s не сдавали: %v", cause, box, errOperatorsMachine)
	}
	reason := DeathProviderDown
	var refused admissionRefused
	if errors.As(cause, &refused) {
		for _, v := range refused {
			if v == "ip_banned" {
				reason = DeathIPBanned
			}
		}
	}
	if _, _, err := p.svc.Retire(ctx, box, reason); err != nil {
		return fmt.Errorf("%w; сдать машину %s не удалось: %v — она осталась в аренде", cause, box, err)
	}
	if err := p.svc.DeleteBoxInstance(ctx, box); err != nil {
		return fmt.Errorf("%w; машину %s сдали, но у хостера не удалили: %v — она в бюджете, хаб удалит её позже", cause, box, err)
	}
	return fmt.Errorf("%w; машина %s сдана и удалена у хостера", cause, box)
}

// ── What the autopilot may hand back ────────────────────────────────────

// boxInOperatorsHands is the one predicate every automatic pass reads before
// it takes a machine off the shelf or hands one back: the idle shelf
// (adoptIdle, handBackIdle), the surplus, the second chance's write-off, and
// the handback of a fresh machine that did not make it. Over entry_boxes
// aliased b; a const fragment so every query stays compile-time text.
//
// A machine is the operator's, not the autopilot's, when
//
//   - its host key was forgotten (ForgetHostKey): it is being reinstalled,
//     and only the operator's own job meets the new key (jobRun.firstUse);
//   - an active observation slot points at it (entry_vantages.box): the
//     slot's prober lives there with no relay by design, whether or not its
//     deploy job ever started;
//   - it ever hosted a prober job.
//
// Found by the recheck of audit 42 (НС3, НН1): each of these is state a fix
// introduced or relied on, and none had reached the passes that delete
// machines at the hoster — they were guards on the doors a person uses, while
// the autopilot came in through its own.
const boxInOperatorsHands = `(b.host_key_forgotten_at IS NOT NULL
	OR EXISTS (SELECT 1 FROM entry_vantages v WHERE v.box = b.name AND v.status = 'active')
	OR EXISTS (SELECT 1 FROM entry_jobs pj WHERE pj.box = b.name AND pj.kind = 'prober'))`

// errOperatorsMachine is a handback refused because the machine is in the
// operator's hands (boxInOperatorsHands) — read again inside the job, since a
// key can be forgotten between the pass listing a machine and retiring it.
var errOperatorsMachine = errors.New("машина в руках оператора — автоматика её не сдаёт")

// inOperatorsHands reads boxInOperatorsHands for one machine.
func (s *Service) inOperatorsHands(ctx context.Context, box string) (bool, error) {
	var held bool
	err := s.pool.QueryRow(ctx, `SELECT `+boxInOperatorsHands+` FROM entry_boxes b WHERE b.name = $1`, box).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	}
	return held, err
}

// autoHandBack is how an automatic pass hands back the job's machine: the
// operator's-hands check, the retire with reason, then the hoster's delete —
// or, for a machine rented by hand, its keys wiped over ssh, the cancelling of
// the rent being the one step left to the owner. One body for every pass, so
// a condition added here holds for all of them.
func (j *jobRun) autoHandBack(reason string) error {
	svc := j.p.svc
	if held, err := svc.inOperatorsHands(j.ctx, j.box); err != nil {
		return err
	} else if held {
		j.log("%v", errOperatorsMachine)
		return errOperatorsMachine
	}
	if _, _, err := svc.Retire(j.ctx, j.box, reason); err != nil {
		return err
	}
	rental, err := svc.boxRental(j.ctx, j.box)
	if err != nil {
		return err
	}
	if rental.instanceID == "" {
		// A machine already gone has nothing to wipe, and that is not a
		// failure of the handback.
		if err := j.wipe(); err != nil {
			j.log("стереть не удалось: %v", err)
		}
		j.log("машина заведена руками — аренду отменить в панели провайдера")
		return nil
	}
	if err := svc.DeleteBoxInstance(j.ctx, j.box); err != nil && !errors.Is(err, ErrNoSuchInstance) {
		return fmt.Errorf("сдана, но у хостера не удалена (в бюджете, хаб удалит позже): %w", err)
	}
	j.log("инстанс у хостера удалён")
	return nil
}

// ── A machine the hub already pays for ──────────────────────────────────

// idleBox is a machine the hub ordered, still pays for, and that carries
// nobody.
type idleBox struct {
	name         string
	instanceID   string
	monthlyCents int
	// since is when anything last happened to it: its row, its last job, its
	// last relay's death — whichever is latest.
	since time.Time
}

// selectIdleBoxes is what "idle" means, in one place for both its readers
// (the repair that takes one, the pass that hands one back):
//
//   - the hub ordered it from this hoster ($1) and it is still rented;
//   - nothing on it can carry a person: every relay is pending (minted, never
//     admitted) or dead;
//   - no job runs on it, and it is not in the operator's hands
//     (boxInOperatorsHands) — an observation point sits on a box with no
//     relays by design, a box whose host key was forgotten is being
//     reinstalled;
//   - no incident stands on it: a box whose relays all died of a ban is under
//     repair, not idle, and its address is the last one to reuse;
//   - its first job has run, or the row is an hour old: between
//     orderAndRecord and the job that follows it, a fresh box looks exactly
//     like an idle one, and only the hour covers a hub that died in that gap.
//
// $2 and $3 narrow it to one pool sense (proto, voice region); NULL takes any.
const selectIdleBoxes = `
	SELECT b.name, b.provider_instance_id, coalesce(b.monthly_cents, 0),
	       greatest(b.created_at,
	                coalesce((SELECT max(j.finished_at) FROM entry_jobs j WHERE j.box = b.name), b.created_at),
	                coalesce((SELECT max(r.died_at) FROM entry_relays r WHERE r.box = b.name), b.created_at))
	FROM entry_boxes b
	WHERE b.retired_at IS NULL
	  AND b.provider_instance_id IS NOT NULL
	  AND b.provider = $1
	  AND ($2::text IS NULL OR b.proto = $2)
	  AND ($3::text IS NULL OR coalesce(b.region, '') = $3)
	  AND NOT EXISTS (SELECT 1 FROM entry_relays r
	                  WHERE r.box = b.name AND r.status NOT IN ('pending', 'dead'))
	  AND NOT EXISTS (SELECT 1 FROM entry_jobs j WHERE j.box = b.name AND j.status = 'running')
	  AND NOT EXISTS (SELECT 1 FROM entry_incidents i
	                  WHERE i.box = b.name AND i.status IN ('open', 'acting'))
	  AND NOT ` + boxInOperatorsHands + `
	  AND (EXISTS (SELECT 1 FROM entry_jobs j WHERE j.box = b.name)
	       OR b.created_at < now() - interval '1 hour')
	ORDER BY 4 DESC, b.name`

// idleBoxes lists the hoster's idle machines, the most recently touched
// first. proto and voiceRegion narrow it when non-nil.
func (s *Service) idleBoxes(ctx context.Context, hoster string, proto, voiceRegion *string) ([]idleBox, error) {
	rows, err := s.pool.Query(ctx, selectIdleBoxes, hoster, proto, voiceRegion)
	if err != nil {
		return nil, fmt.Errorf("entry: idle boxes: %w", err)
	}
	defer rows.Close()
	var out []idleBox
	for rows.Next() {
		var b idleBox
		if err := rows.Scan(&b.name, &b.instanceID, &b.monthlyCents, &b.since); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// obtainMachines is how a repair gets machines into a pool: idle ones the hub
// already pays for while there are any, new orders for the rest (scale_up,
// fleet.go — which is also how an evicted machine is replaced). Up to n at
// once (П9.19: a surge is served by as many machines as it needs, not one per
// pass).
//
// Returns each machine it tried — its name, empty when no order went
// through, and why it is not in, nil when it is; a failed order's machine is
// already handed back, as orderAndProvision does — and the notes for the
// repair's outcome (what was taken, what was handed back on the way).
//
// The shelf is taken one machine after another: two adoptions in parallel
// could take the same machine, and the shelf is rarely more than one deep.
// The orders run in parallel: each passes the money gate under its own lock
// (reserveOrder), so the caps hold however many race, and one the caps refuse
// ends with ErrBudget and nothing rented.
//
// # Why the shelf comes before the hoster
//
// Found on the stand 25.10: ams2, ordered by the hub, paid for, booted, had no
// relay — its first deploy died before it got that far. The next outage
// bought ams3 next to it. An idle machine is rent nobody stops and a first
// boot already paid in minutes; taking it costs nothing the budget has not
// already counted, so it goes through no money gate.
//
// An idle machine whose admission nobody could judge ends the attempt there
// (errNoVerdict): the vantage that could not judge it would not judge a new
// one either, and a purchase would only put a second machine on the shelf. The
// same holds for any idle machine that fails: what failed there is what an
// order would meet.
func (p *Provisioner) obtainMachines(ctx context.Context, spec OrderSpec, n int) (got []obtained, notes []string) {
	for len(got) < n {
		box, note, err := p.adoptIdle(ctx, spec)
		if note != "" {
			notes = append(notes, note)
		}
		if box == "" && err == nil {
			break
		}
		got = append(got, obtained{box: box, err: err})
		if err != nil {
			return got, notes
		}
	}
	orders := make([]obtained, n-len(got))
	var wg sync.WaitGroup
	for i := range orders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			box, err := p.orderAndProvision(ctx, spec)
			orders[i] = obtained{box: box, err: err}
		}()
	}
	wg.Wait()
	return append(got, orders...), notes
}

// obtained is one machine obtainMachines tried to bring into a pool: its name
// (empty when no order went through) and why it is not in, nil when it is.
type obtained struct {
	box string
	err error
}

// adoptIdle stands up the first idle machine of the spec's pool sense. It
// gives up on the shelf after one machine that fails: each attempt can cost a
// full bootWait, and a repair has an outage to end — the order is the
// fallback, not the next idle one.
//
// A machine the hoster no longer has is retired on the spot: the row is all
// that is left of it, and leaving it would have every later repair ask about
// it again. Any other answer from the hoster leaves the machine alone — a
// failing API is not evidence that the machine is gone.
func (p *Provisioner) adoptIdle(ctx context.Context, spec OrderSpec) (box, note string, err error) {
	h, err := p.svc.hoster()
	if err != nil {
		return "", "", nil
	}
	proto := spec.Proto
	if proto == "" {
		proto = ProtoVlessRealityVision
	}
	var voice *string
	if proto == ProtoHysteria2 {
		voice = &spec.VoiceRegion
	}
	idle, err := p.svc.idleBoxes(ctx, h.kind, &proto, voice)
	if err != nil {
		applog.Error(ctx, "entry", "idle machines unreadable", applog.F("err", err.Error()))
		return "", "", nil
	}
	var notes []string
	for _, c := range idle {
		_, err := h.bp.Instance(ctx, c.instanceID)
		switch {
		case errors.Is(err, ErrNoSuchInstance):
			if _, _, rerr := p.svc.Retire(context.WithoutCancel(ctx), c.name, DeathProviderDown); rerr != nil {
				applog.Error(ctx, "entry", "retire of a machine gone at the hoster failed", applog.F("box", c.name), applog.F("err", rerr.Error()))
			}
			notes = append(notes, fmt.Sprintf("простаивающей %s у хостера уже нет — снята с учёта", c.name))
			continue
		case err != nil:
			applog.Warn(ctx, "entry", "idle machine unreadable at the hoster", applog.F("box", c.name), applog.F("err", err.Error()))
			continue
		}
		err = p.standUp(ctx, c.name, "беру простаивающую машину хаба, жду ssh")
		if errors.Is(err, ErrBoxBusy) {
			continue // another repair took it a moment ago
		}
		if errors.Is(err, errNoVerdict) {
			return "", strings.Join(notes, "; "), keptForAVantage(c.name, err)
		}
		if err != nil {
			notes = append(notes, "простаивающая не поднялась: "+p.handBackFresh(ctx, c.name, err).Error())
			break
		}
		notes = append(notes, fmt.Sprintf("взята простаивающая %s, новую не покупали", c.name))
		return c.name, strings.Join(notes, "; "), nil
	}
	return "", strings.Join(notes, "; "), nil
}

// idleHold is how long an idle machine is kept for a repair to take before
// the hub hands it back.
//
// Long enough to bridge one wave: a ban that takes several machines takes
// them within the hour, and an idle one on the shelf saves that repair a
// purchase and a first boot. Short enough that forgetting it is cheap — six
// hours of a $5 machine is four cents, a month of it is the $5. The hub
// decides this by itself, with no button: the owner's rule is that the hub
// holds its own state, and a machine nobody uses is state.
const idleHold = 6 * time.Hour

// handBackIdle retires and deletes at the hoster every idle machine that has
// sat for longer than idleHold. Run from the mechanic's pass; returns the
// boxes handed back.
//
// Each one is a retire job on the box, not two bare calls: the job is the
// per-box lock, so a repair taking the same machine at the same moment gets
// ErrBoxBusy on one side or the other instead of having it retired under its
// deploy. A machine the hoster cannot delete is still retired — retired is
// what stops the hub from counting it — and the job says what is left.
func (p *Provisioner) handBackIdle(ctx context.Context) ([]string, error) {
	svc := p.svc
	h, err := svc.hoster()
	if err != nil {
		return nil, nil // nothing the hub rents through an API
	}
	idle, err := svc.idleBoxes(ctx, h.kind, nil, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, b := range idle {
		if svc.now().Sub(b.since) < idleHold {
			continue
		}
		err := p.execJob(ctx, JobRetire, b.name, uuid.Nil, func(j *jobRun) error {
			j.step("retire")
			j.log("машина простаивает с %s без единого релея в строю — сдаю", b.since.Format(time.RFC3339))
			return j.autoHandBack(DeathOther)
		})
		if errors.Is(err, ErrBoxBusy) || errors.Is(err, errOperatorsMachine) {
			continue // a repair took it a moment ago, or the operator did
		}
		if err != nil {
			applog.Error(ctx, "entry", "hand back idle machine failed", applog.F("box", b.name), applog.F("err", err.Error()))
			continue
		}
		out = append(out, b.name)
	}
	return out, nil
}

// RebootBox restarts a box through the hoster and waits for it to come back,
// as a job the console can watch (П9.2). Asynchronous, like the other console
// buttons; the repair calls [Provisioner.rebootAndWait] instead.
func (p *Provisioner) RebootBox(ctx context.Context, box, adminID string) (uuid.UUID, error) {
	if _, err := p.svc.boxInstanceID(ctx, box); err != nil {
		return uuid.Nil, err
	}
	id, err := p.svc.startJobOf(ctx, JobReboot, box, adminID, uuid.Nil)
	if err != nil {
		return uuid.Nil, err
	}
	p.svc.spawn(func(ctx context.Context) {
		jctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
		j := &jobRun{p: p, ctx: jctx, id: id, box: box}
		j.finish(j.rebootAndWait(), "готово: "+box+" перезагружен и отвечает")
	})
	return id, nil
}

// rebootAndWait is the job body, and the repair's whole work: press the
// hoster's button, wait until the machine answers ssh again, then re-probe
// admission.
//
// The hoster's 204 proves nothing — it says the request was accepted, not that
// the machine came back. What proves the repair is ssh answering and the
// vantage seeing the relays again, which is why this is a job with steps and
// not a one-line API call.
func (p *Provisioner) rebootAndWait(ctx context.Context, box string) error {
	return p.execJob(ctx, JobReboot, box, uuid.Nil, func(j *jobRun) error {
		return j.rebootAndWait()
	})
}

func (j *jobRun) rebootAndWait() error {
	j.step("reboot")
	if err := j.p.svc.RebootInstance(j.ctx, j.box); err != nil {
		return err
	}
	j.log("хостер принял перезагрузку, ждём ssh")

	j.step("wait")
	r, err := j.awaitSSH()
	if err != nil {
		return err
	}
	_ = r.Close()

	relays, err := j.liveRelays()
	if err != nil {
		return err
	}
	if len(relays) == 0 {
		// Nothing was handed out from this machine, so there is nothing to
		// re-admit. The reboot still did what it was asked.
		j.log("живых релеев на машине нет — пробовать допуск нечего")
		return nil
	}
	j.step("probe")
	return j.probe(relays)
}

// rebootWait is how long a machine has to answer ssh after a reboot before the
// repair calls it dead. A reboot is seconds; three minutes is the point past
// which the machine is not coming back and a replacement is the answer.
const rebootWait = 3 * time.Minute

// awaitSSH logs in as soon as the machine lets the hub in again.
func (j *jobRun) awaitSSH() (Remote, error) { return j.awaitSSHFor(rebootWait) }

// awaitSSHFor logs in as soon as the machine lets the hub in, giving up after
// wait.
func (j *jobRun) awaitSSHFor(wait time.Duration) (Remote, error) {
	deadline := j.p.svc.now().Add(wait)
	var last error
	for {
		if r, err := j.loginByKey(); err == nil {
			return r, nil
		} else {
			last = err
		}
		if j.p.svc.now().After(deadline) {
			return nil, fmt.Errorf("машина %s не ответила по ssh за %s: %w", j.box, wait, last)
		}
		select {
		case <-j.ctx.Done():
			return nil, j.ctx.Err()
		case <-time.After(j.p.drainPoll):
		}
	}
}

// ── What the hub still pays for and does not use ────────────────────────

// orderSettleAfter is how long a reservation may stay `reserved` or
// `created` before the pass decides it was abandoned: longer than an order
// takes (the address wait, then the box row), so an order still running in
// another process — the CLI — is never settled under it.
const orderSettleAfter = instanceWaitTimeout + 10*time.Minute

// unaccountedTagged counts the listed machines that carry the hub's tag and
// that neither a box nor an order of the hub names: debt the console shows as
// orphans (HostingInstances), and the reason a lost order is not written off.
func (s *Service) unaccountedTagged(ctx context.Context, listed []BoxInstance) (int, error) {
	var ids []string
	for _, inst := range listed {
		if slices.Contains(inst.Tags, hostingTag) {
			ids = append(ids, inst.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM unnest($1::text[]) AS i(id)
		WHERE NOT EXISTS (SELECT 1 FROM entry_boxes b WHERE b.provider_instance_id = i.id)
		  AND NOT EXISTS (SELECT 1 FROM entry_box_orders o WHERE o.instance_id = i.id)
	`, ids).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("entry: unaccounted instances: %w", err)
	}
	return n, nil
}

// noteUnseen counts one full listing that showed nothing of a lost order, and
// answers whether that is now enough to write it off (orderUnseenPasses over
// orderUnseenFor).
func (s *Service) noteUnseen(ctx context.Context, id uuid.UUID) (bool, error) {
	var seen int
	var since time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE entry_box_orders
		SET unseen_listings = unseen_listings + 1, unseen_since = coalesce(unseen_since, $2)
		WHERE id = $1
		RETURNING unseen_listings, unseen_since
	`, id, s.now()).Scan(&seen, &since)
	if err != nil {
		return false, fmt.Errorf("entry: order %s unseen: %w", id, err)
	}
	return seen >= orderUnseenPasses && s.now().Sub(since) >= orderUnseenFor, nil
}

// resetUnseen starts the count of a lost order over: a listing that could not
// say "nothing" is not one more look that did. why is what the console reads
// on the reservation.
func (s *Service) resetUnseen(ctx context.Context, id uuid.UUID, why string) {
	if _, err := s.pool.Exec(ctx, `
		UPDATE entry_box_orders SET unseen_listings = 0, unseen_since = NULL, error = $2 WHERE id = $1
	`, id, clip(why, 512)); err != nil {
		applog.Error(ctx, "entry", "lost order count not reset", applog.F("order", id.String()), applog.F("err", err.Error()))
	}
}

// orderUnseenPasses and orderUnseenFor are what writes a lost order off as
// "the hoster made nothing": this many full listings in a row that showed no
// machine of it, the first at least this long ago (audit 42 recheck НС2). One
// look is not evidence — the list lags, and an answer lost after the POST is
// exactly the order whose machine appears in it late.
const (
	orderUnseenPasses = 3
	orderUnseenFor    = 30 * time.Minute
)

// ReleaseUndeleted is the background repeat of every delete the hoster did
// not do (С1): retired boxes whose machine it still holds, orders whose
// machine was created and not kept, and orders nobody heard the answer to —
// which it settles by looking for the instance under the label the order
// gave it. Run from the mechanic's pass; returns how many were let go.
//
// One attempt per thing per pass, so a hoster that refuses every delete is
// asked once a minute, not in a loop. Every machine it leaves stays in the
// budget, which is what stops the hub from renting past the cap meanwhile.
func (s *Service) ReleaseUndeleted(ctx context.Context) (int, error) {
	return s.releaseUndeleted(ctx, nil)
}

// releaseUndeleted restricts reconciliation to only when a non-nil scope is supplied.
func (s *Service) releaseUndeleted(ctx context.Context, only []string) (int, error) {
	h, err := s.hoster()
	if err != nil {
		return 0, nil // nothing the hub rents through an API
	}
	released := 0

	// Retired boxes the hoster has not let go of.
	rows, err := s.pool.Query(ctx, `
		SELECT name FROM entry_boxes
		WHERE retired_at IS NOT NULL AND provider_instance_id IS NOT NULL AND released_at IS NULL
		  AND provider = $1 AND ($2::text[] IS NULL OR name = ANY($2))
		ORDER BY retired_at LIMIT 20
	`, h.kind, only)
	if err != nil {
		return 0, fmt.Errorf("entry: undeleted boxes: %w", err)
	}
	boxes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("entry: undeleted boxes: %w", err)
	}
	for _, box := range boxes {
		if err := s.DeleteBoxInstance(ctx, box); err != nil {
			applog.Warn(ctx, "entry", "retired machine still not deleted",
				applog.F("box", box), applog.F("err", err.Error()))
			continue
		}
		released++
	}

	// Orders that may have left a machine behind.
	type open struct {
		id         uuid.UUID
		name       string
		status     string
		instanceID *string
	}
	rows, err = s.pool.Query(ctx, `
		SELECT id, name, status, instance_id FROM entry_box_orders
		WHERE provider = $1 AND ($3::text[] IS NULL OR name = ANY($3))
		  AND (status = 'undeleted' OR (status IN ('reserved', 'created') AND updated_at < $2))
		ORDER BY created_at LIMIT 20
	`, h.kind, s.now().Add(-orderSettleAfter), only)
	if err != nil {
		return released, fmt.Errorf("entry: open orders: %w", err)
	}
	var orders []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.name, &o.status, &o.instanceID); err != nil {
			rows.Close()
			return released, err
		}
		orders = append(orders, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return released, err
	}
	var listed []BoxInstance // read once, only when an answer was lost
	listedOK := false
	unaccounted := 0
	for _, o := range orders {
		instanceID := ""
		if o.instanceID != nil {
			instanceID = *o.instanceID
		}
		if instanceID == "" {
			// The hoster's answer never came: its own list says whether it
			// made the machine, by the label the order put on it and the tag
			// every machine of the hub carries.
			if !listedOK {
				if listed, err = h.bp.Instances(ctx); err != nil {
					return released, fmt.Errorf("entry: hoster instances: %w", err)
				}
				if unaccounted, err = s.unaccountedTagged(ctx, listed); err != nil {
					return released, err
				}
				listedOK = true
			}
			var matches []string
			for _, inst := range listed {
				if inst.Label == "nekko-"+o.name && slices.Contains(inst.Tags, hostingTag) {
					matches = append(matches, inst.ID)
				}
			}
			switch {
			case len(matches) == 1:
				instanceID = matches[0]
			case len(matches) > 1:
				// Two machines answer to one order's label: which one it
				// made is not the hub's to guess, and deleting the wrong one
				// cuts a machine that may carry people.
				s.resetUnseen(ctx, o.id, fmt.Sprintf("у хостера %d машины с меткой nekko-%s — какая из них этого заказа, решает человек", len(matches), o.name))
				continue
			case unaccounted > 0:
				// A machine with the hub's tag that no box and no order
				// accounts for may be this order's, renamed in the panel.
				s.resetUnseen(ctx, o.id, fmt.Sprintf("у хостера %d машин с тегом хаба, которых хаб не знает — заказ остаётся в бюджете, пока их не разберут", unaccounted))
				continue
			default:
				gone, err := s.noteUnseen(ctx, o.id)
				if err != nil {
					return released, err
				}
				if gone {
					s.settleOrder(ctx, o.id, orderReleased, "", fmt.Errorf("хостер машину по этому заказу не создал: её нет в %d листингах подряд за %s", orderUnseenPasses, orderUnseenFor))
					released++
				}
				continue
			}
		}
		var recorded bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM entry_boxes WHERE provider_instance_id = $1)`, instanceID,
		).Scan(&recorded); err != nil {
			return released, fmt.Errorf("entry: order %s: %w", o.id, err)
		}
		if recorded {
			s.settleOrder(ctx, o.id, orderRecorded, instanceID, nil)
			continue
		}
		if err := h.bp.Delete(ctx, instanceID); err != nil && !errors.Is(err, ErrNoSuchInstance) {
			s.settleOrder(ctx, o.id, orderUndeleted, instanceID, err)
			applog.Warn(ctx, "entry", "ordered machine still not deleted",
				applog.F("instance", instanceID), applog.F("err", err.Error()))
			continue
		}
		s.settleOrder(ctx, o.id, orderReleased, instanceID, errors.New("машина заказа не вошла в парк и удалена у хостера"))
		released++
	}
	return released, nil
}
