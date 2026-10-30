package entry

// moneygate_test.go is the proof for the audit of the autopilot's money path
// (AUDIT/42-autopilot.md, В3, В4, С1, С2 and the mines beside them) and for
// the owner's decisions of 27.10 (П9.11б). Every test here was red on the
// code the audit read.

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// secondHub is another process on the same database — the one-shot CLI
// (`cmd/server entry-box order`) beside the hub: its own Service, its own
// provisioner, the same saved accounts.
func (h *hostHarness) secondHub() *Provisioner {
	h.t.Helper()
	seed, err := hex.DecodeString(h.f.SeedHex)
	if err != nil {
		h.t.Fatal(err)
	}
	svc, err := NewService(h.pool, NewSigner(h.f.KeyID, seed), 2)
	if err != nil {
		h.t.Fatal(err)
	}
	svc.SetProviderConnector(h.conn)
	svc.instancePoll = 5 * time.Millisecond
	if err := svc.LoadProviders(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(svc.stopBackground)
	return NewProvisioner(svc, h.box.dial, h.t.TempDir())
}

// В3. Room in the budget for one and a half machines, and two orders racing
// for it — the hub and the CLI, two processes with nothing in memory in
// common. Exactly one is placed; the other is refused before it is.
func TestTwoOrdersRacingForTheLastRoomBuyOneMachine(t *testing.T) {
	h := newHostHarness(t)
	h.host.createDelay = 300 * time.Millisecond
	h.spendRoom(SpendHosting, 1.5*machineDayOf(hugeCents)) // one $99 machine fits, two do not
	other := h.secondHub()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	names := make([]string, 2)
	for i, p := range []*Provisioner{h.prov, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			names[i], errs[i] = p.orderAndRecord(context.Background(), OrderSpec{Plan: "huge"})
		}()
	}
	wg.Wait()
	for _, n := range names {
		if n != "" {
			h.t.Cleanup(func() { h.retire(n) })
		}
	}
	if n := h.host.orderCount(); n != 1 {
		t.Fatalf("the hoster was asked for %d machines, want 1 (errs %v)", n, errs)
	}
	budget := 0
	for _, err := range errs {
		if errors.Is(err, ErrBudget) {
			budget++
		} else if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if budget != 1 {
		t.Fatalf("refusals over budget: %d, want 1 (errs %v)", budget, errs)
	}
}

// The hub reads the caps the owner saved, not the ones it loaded at start:
// the console saved a lower cap through the hub, the CLI (another process)
// must not order past it.
func TestTheCapIsReadWhereTheOwnerSavedIt(t *testing.T) {
	h := newHostHarness(t)
	other := h.secondHub()                                 // started under the generous cap
	h.spendRoom(SpendHosting, 0.5*machineDayOf(hugeCents)) // lowered through the hub: no machine fits now
	if _, err := other.orderAndRecord(context.Background(), OrderSpec{Plan: "huge"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("an order past the saved cap: err = %v, want ErrBudget", err)
	}
	if n := h.host.orderCount(); n != 0 {
		t.Fatalf("%d machines ordered past the cap", n)
	}
}

// С1. A machine the hoster would not delete is still being paid for: the
// budget counts it, the console sees it as debt, and the mechanic's pass
// keeps asking the hoster until it goes.
func TestAMachineTheHosterWouldNotDeleteStaysInTheBudget(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box, err := h.prov.orderAndRecord(ctx, OrderSpec{})
	if err != nil {
		t.Fatal(err)
	}
	before := h.dayProjected()
	h.host.deleteErr = errors.New("vultr: http: connection reset")

	err = h.prov.handBackFresh(ctx, box, errors.New("не поднялась"))
	if err == nil || !strings.Contains(err.Error(), "не удалили") {
		t.Fatalf("hand back: %v", err)
	}
	if !h.retired(box) {
		t.Fatal("the machine was not retired")
	}
	if got := h.dayProjected(); got != before {
		t.Fatalf("the day %.2f → %.2f: a machine still rented left the budget", before, got)
	}
	list, err := h.svc.HostingInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inst := h.instanceOf(box)
	flagged := false
	for _, i := range list {
		if i.ID == inst {
			flagged = i.Orphan && i.Box == box
		}
	}
	if !flagged {
		t.Errorf("the undeleted instance of %s is not shown as debt: %+v", box, list)
	}

	// The hoster recovers; the next pass deletes it and the rest of its day
	// leaves the budget.
	h.host.deleteErr = nil
	if _, _, err := NewMechanic(h.svc, h.prov).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if !contains(h.host.deletedIDs(), inst) {
		t.Fatalf("the pass did not delete %s (deleted %v)", inst, h.host.deletedIDs())
	}
	if got := h.dayProjected(); got > before-machineDay()+machineDay()/float64(hoursLeftToday())+0.01 {
		t.Errorf("the day after the delete = %.2f, want the rest of the day off %.2f", got, before)
	}
}

// С1. An order whose machine was created and then never got an address (the
// wait gave out) is an instance the hub pays for with no box. It is handed
// back on the spot; if the hoster will not, the budget keeps counting it.
func TestAnInstanceWithNoAddressIsCountedUntilDeleted(t *testing.T) {
	h := newHostHarness(t)
	h.host.pollsBeforeAddress = 1 << 30 // never addressed
	h.host.deleteErr = errors.New("vultr: http: 502")
	before := h.dayProjected()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := h.prov.orderAndRecord(ctx, OrderSpec{}); err == nil {
		t.Fatal("an order with no address succeeded")
	}
	if n := h.host.orderCount(); n != 1 {
		t.Fatalf("orders %d, want 1", n)
	}
	if got := h.dayProjected(); !nearCents(got, before+machineDay()) {
		t.Fatalf("the day %.2f → %.2f: the instance created and kept is not in the budget", before, got)
	}

	h.host.deleteErr = nil
	if _, _, err := NewMechanic(h.svc, h.prov).Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left, _ := h.host.Instances(context.Background()); len(left) != 0 {
		t.Fatalf("the hoster still holds %d machines nobody records", len(left))
	}
	if got := h.dayProjected(); got > before+machineDay()/float64(hoursLeftToday())+0.01 {
		t.Errorf("the day after the delete = %.2f, want %.2f and the hour it ran", got, before)
	}
}

// С1, the mine beside it: an order whose box row cannot be written, and whose
// machine the hoster then will not delete, was only logged. It is counted.
func TestAnUnrecordedOrderTheHosterKeepsIsCounted(t *testing.T) {
	h := newHostHarness(t)
	h.takeNameMidOrder()
	h.host.deleteErr = errors.New("vultr: http: 502")
	before := h.dayProjected()
	if _, err := h.prov.orderAndRecord(context.Background(), OrderSpec{}); err == nil {
		t.Fatal("recording a box under a taken name succeeded")
	}
	if got := h.dayProjected(); !nearCents(got, before+machineDay()) {
		t.Fatalf("the day %.2f → %.2f: the kept instance is not counted", before, got)
	}
}

// One gate for every path that orders (owner's decisions 27.10, 28.10): a
// console order and the CLI's both count against the day's money.
func TestTheDayCapHoldsAcrossEveryPath(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	h.spendRoom(SpendHosting, 1.5*machineDayOf(hugeCents))
	huge := OrderSpec{Plan: "huge"}

	box, err := h.prov.orderAndRecord(ctx, huge)
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	h.t.Cleanup(func() { h.retire(box) })
	if _, err := h.secondHub().orderAndRecord(ctx, huge); !errors.Is(err, ErrBudget) {
		t.Fatalf("a second order past the day from the CLI: err = %v, want a refusal", err)
	}
	if _, _, err := h.prov.OrderBox(ctx, huge, ""); !errors.Is(err, ErrBudget) {
		t.Fatalf("a second order past the day from the console: err = %v, want a refusal", err)
	}
	if n := h.host.orderCount(); n != 1 {
		t.Fatalf("orders %d, want 1", n)
	}
}

// В4. The registrar took the money and the answer never came. The name is
// not `failed`: its cost stays in the month, and the next purchase asks for
// the same name with the same key — which the registrar answers with the
// order it already made, not a second charge.
func TestADomainPurchaseWithNoAnswerIsCountedAndReplayed(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	ctx := context.Background()
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0
	reg.buyLost = errors.New("porkbun: http: context deadline exceeded")
	daySpend := func() int {
		t.Helper()
		day, _, err := domainSpend(ctx, h.pool, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return day
	}
	before := daySpend()

	box, addr := h.xhttpBoxNoDomain(t)
	if _, _, err := h.svc.AcquireDomain(ctx, box, addr); err == nil {
		t.Fatal("a purchase with no answer succeeded")
	}
	if len(reg.keys) != 1 {
		t.Fatalf("buys sent: %v", reg.keys)
	}
	name, key := reg.keys[0][0], reg.keys[0][1]
	d, ok, err := h.svc.repo.domain(ctx, name)
	if err != nil || !ok {
		t.Fatalf("ledger row of %s: %v %v", name, ok, err)
	}
	if d.Status == domainFailed {
		t.Fatalf("a purchase the registrar may have charged reads %q", d.Status)
	}
	spent := daySpend()
	if spent-before != d.CostCents || d.CostCents == 0 {
		t.Fatalf("the day's spend moved by %d, want the unanswered purchase's %d", spent-before, d.CostCents)
	}

	reg.buyLost = nil
	got, _, err := h.svc.AcquireDomain(ctx, box, addr)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if len(reg.keys) != 2 || reg.keys[1] != [2]string{name, key} {
		t.Fatalf("the retry was %v, want %s with key %s again", reg.keys, name, key)
	}
	if got != name || len(reg.charged) != 1 {
		t.Fatalf("acquired %s with %d charges, want %s with one", got, len(reg.charged), name)
	}
	if after := daySpend(); after != spent {
		t.Errorf("the day's spend %d → %d on a replay", spent, after)
	}
}

// С2. The owner set the eviction of a banned machine to `off`; an operator
// picking that repair by hand on another incident does not get past it.
func TestAChosenRepairObeysTheOwnersOff(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box := h.inService()
	if err := h.svc.SetPlaybookMode(ctx, CauseIPBanned, ModeOff, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseIPBanned)
	})
	inc := h.openIncident(box, CauseProviderDown, map[string]string{box + "-1": "timeout"})
	err := NewMechanic(h.svc, h.prov).Start(ctx, inc.ID, uuid.NewString(), PlaybookEvictBox)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("evict_box chosen while the owner has it off: err = %v, want a refusal", err)
	}
	if h.aliveOn(box) == 0 {
		t.Error("the machine left the pool anyway")
	}
}

// A pause of the wave on bans also holds the same repair reached through
// provider_down: the eviction is one decision whatever broke the machine, and
// so is the purchase it leads to.
func TestAWavePauseHoldsEveryCauseLeadingToTheSameRepair(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box := h.inService()
	if err := h.svc.PauseCause(ctx, CauseIPBanned, wavePrefix+": тест", 3); err != nil {
		t.Fatal(err)
	}
	inc := h.openIncident(box, CauseProviderDown, map[string]string{box + "-1": "timeout"})
	t.Cleanup(func() { h.dropPauses(CauseIPBanned) })
	err := NewMechanic(h.svc, h.prov).Start(ctx, inc.ID, uuid.NewString(), PlaybookEvictBox)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("evict_box past a standing pause of ip_banned: err = %v", err)
	}
}

// The mines: a repair whose verdict was never written is not `running`
// forever, and an incident left `acting` with nothing running is reopened.
func TestAStuckRepairAndAStuckIncidentAreReaped(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box, _ := h.harness.box()
	inc := h.openIncident(box, "test_stuck", map[string]string{})
	if err := h.svc.Act(ctx, inc.ID, PlaybookRetirePorts, DecidedAuto); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO entry_repairs (incident_id, box, cause, playbook, decided_by, status, started_at)
		VALUES ($1, $2, 'test_stuck', $3, 'auto', 'running', $4)
	`, inc.ID, box, PlaybookRetirePorts, time.Now().Add(-repairTimeout-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NewMechanic(h.svc, h.prov).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := h.pool.QueryRow(ctx, `SELECT status FROM entry_repairs WHERE incident_id = $1`, inc.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != repairFailed {
		t.Errorf("repair past its timeout reads %q, want failed", status)
	}
	got, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != IncidentOpen {
		t.Errorf("incident with nothing running reads %q, want open", got.Status)
	}
}

// ── П9.11б ─────────────────────────────────────────────────────────────

// The surplus is handed back, all of it: a pool over what it keeps loses
// every machine over it at the end of that machine's paid hour — no hold on
// top (П9.19), never one an incident stands on, emptied first and handed back
// on the next pass (П9.17).
func TestTheWholeSurplusIsHandedBackAtTheEndOfItsPaidHours(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	first := h.inPool()
	second := h.inPool()
	third := h.inPool()
	fourth := h.inPool()
	h.target(1) // keeps two: 69 Mbit/s fits under 50 % of two 100 Mbit/s machines
	h.openIncident(first, "test_surplus", map[string]string{})

	// Every machine is ten minutes into a paid hour: its hour is paid either
	// way, and nothing leaves yet.
	m := NewMechanic(h.svc, h.prov)
	h.paidMinutesAgo(10, first, second, third, fourth)
	if _, _, err := m.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{first, second, third, fourth} {
		if h.leaving(b) {
			t.Fatalf("%s with fifty paid minutes left began to leave", b)
		}
	}

	// Five minutes before the hour turns: the two over what the pool keeps
	// are emptied at once — the oldest free ones, the one under an incident
	// skipped — and the fourth stays.
	h.paidMinutesAgo(55, first, second, third, fourth)
	if _, _, err := m.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if h.leaving(first) {
		t.Error("a machine under an incident began to leave")
	}
	for _, b := range []string{second, third} {
		if !h.leaving(b) || h.retired(b) {
			t.Fatalf("surplus machine %s: leaving %v, retired %v — want leaving, not yet handed back",
				b, h.leaving(b), h.retired(b))
		}
		var drained int
		if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_relays WHERE box = $1 AND status = 'draining'`, b).Scan(&drained); err != nil || drained == 0 {
			t.Fatalf("the leaving machine's relays are not draining (%d, %v)", drained, err)
		}
	}
	if h.leaving(fourth) {
		t.Error("more machines began to leave than the pool is over by")
	}

	// The next pass: a voice machine has no connection to wait for.
	if _, _, err := m.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{second, third} {
		if !h.retired(b) {
			t.Fatalf("the emptied machine %s was not handed back", b)
		}
		if !contains(h.host.deletedIDs(), h.instanceOf(b)) {
			t.Errorf("the handed-back %s was not deleted at the hoster (%v)", b, h.host.deletedIDs())
		}
	}
	// Two left, which is what the pool keeps: nothing more goes.
	if _, _, err := m.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if h.retired(fourth) || h.leaving(fourth) {
		t.Error("a pool at what it keeps gave back another machine")
	}
}

// The domain is split like the machine: a blocked one is evicted at once and
// for free, from a pooled name, and never by a purchase.
func TestABlockedDomainIsEvictedForFreeFromThePool(t *testing.T) {
	if p := PlaybookForCause(CauseDomainBlocked); p.Name != "evict_domain" || p.Mode != ModeAuto || p.Spends {
		t.Fatalf("domain_blocked → %+v, want evict_domain on auto, free", p)
	}
	if p := PlaybookForCause("short_of_domains"); p.Name != "buy_domain" || p.Mode != ModeAuto || !p.Spends {
		t.Fatalf("short_of_domains → %+v, want buy_domain on auto, spending", p)
	}

	h := newProvHarness(t)
	ctx := context.Background()
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0
	box, addr := h.xhttpBoxNoDomain(t)
	if err := h.svc.SeedDomain(ctx, "harbor1234.lol"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.AcquireDomain(ctx, box, addr); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SeedDomain(ctx, "slate7777.top"); err != nil {
		t.Fatal(err)
	}
	inc := h.openIncident(box, CauseDomainBlocked, map[string]string{})
	_, _ = NewMechanic(h.svc, h.prov).perform(ctx, inc, "evict_domain")
	if got := boxDomain(t, h.pool, box); got != "slate7777.top" {
		t.Errorf("box carries %q, want the pooled name", got)
	}
	if d, _, _ := h.svc.repo.domain(ctx, "harbor1234.lol"); d.Status != domainBurned {
		t.Errorf("the blocked name is %q, want burned (suspect)", d.Status)
	}
	if reg.buys != 0 || len(reg.keys) != 0 {
		t.Errorf("an eviction bought %d domains", reg.buys)
	}

	// With the pool dry the eviction still buys nothing.
	inc2 := h.openIncident(box, CauseDomainBlocked+"_again", map[string]string{})
	inc2.Cause = CauseDomainBlocked
	_, err := NewMechanic(h.svc, h.prov).perform(ctx, inc2, "evict_domain")
	if err == nil {
		t.Error("an eviction with a dry pool reported success")
	}
	if len(reg.keys) != 0 {
		t.Errorf("an eviction with a dry pool bought: %v", reg.keys)
	}
}

// The purchase is led by the domain pool's derived target: XHTTP machines on
// their way need more names than the pool holds — an incident on the pool,
// and its repair buys one name into the pool.
func TestTheDomainPoolTargetBuysIntoThePool(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0
	xhttp := FleetPool(ProtoVlessXhttp, "")
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_incidents WHERE pool IN ('domains', $1)`, xhttp)
	})
	// The test database holds every run's machines and names: the XHTTP load
	// is set to ask for one machine more than every free name covers.
	f, err := h.svc.poolForecast(ctx, mustPolicy(t, h.svc), xhttp)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.svc.DomainPoolNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.svc.primeSizing(map[string]PoolSizing{xhttp: sizingFor(f.Live + before.Free + 1)})
	if r, err := h.svc.DomainPoolNow(ctx); err != nil || r.Need <= 0 || r.Incoming <= before.Free {
		t.Fatalf("domain pool with XHTTP machines on the way = %+v, %v", r, err)
	}
	if _, _, err := h.svc.DetectFleet(ctx); err != nil {
		t.Fatal(err)
	}
	incs, err := h.svc.Incidents(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var short *Incident
	for i := range incs {
		if incs[i].Pool == "domains" && incs[i].Cause == "short_of_domains" && incs[i].Status == IncidentOpen {
			short = &incs[i]
		}
	}
	if short == nil {
		t.Fatalf("a domain pool short of what the XHTTP machines on the way need opened no incident: %+v", incs)
	}
	res, err := NewMechanic(h.svc, nil).perform(ctx, *short, "buy_domain")
	if err != nil {
		t.Fatalf("buy_domain: %v", err)
	}
	if reg.buys != 1 || res.cost == 0 {
		t.Fatalf("buys %d, cost %d — want one purchase, priced", reg.buys, res.cost)
	}
	d, ok, _ := h.svc.repo.domain(ctx, reg.bought[0])
	if !ok || d.Status != domainOwned || d.Box != "" {
		t.Errorf("bought %s lands as %+v, want free in the pool", reg.bought[0], d)
	}
}
