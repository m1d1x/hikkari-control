package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/sshexec"
)

// fakeHoster is a hosting provider that keeps its fleet in a map: it answers
// the catalog, takes orders, hands out addresses (after a poll or two, like a
// real one), reboots and deletes.
type fakeHoster struct {
	mu      sync.Mutex
	badKey  bool
	balance int

	regions []BoxRegion
	plans   []BoxPlan

	// pollsBeforeAddress is how many Instance calls answer with no address
	// before one appears. Zero hands it out in the Create answer, which is
	// what a hoster does when the machine was already built.
	pollsBeforeAddress int
	instanceErr        error

	createErr          error
	createErrForRegion map[string]error
	attempted          []BoxOrder
	// loseAnswer makes Create build the machine and then answer with this
	// error, as a timeout after the POST does.
	loseAnswer error
	// hidden instances exist (and bill) but are not in the listing yet.
	hidden map[string]bool
	// createDelay holds every Create this long before it answers: the window
	// two orders racing for the last room in the budget both sit in.
	createDelay time.Duration
	// deleteErr is what Delete answers while set, the instance kept.
	deleteErr error
	// onCreate runs inside Create, after the instance exists: how a test
	// makes something happen between the charge and the box row.
	onCreate func(BoxOrder)

	instances map[string]*BoxInstance
	polls     map[string]int
	keys      map[string]string // id → public key

	ordered  []BoxOrder
	rebooted []string
	deleted  []string
	nextID   int
	nextAddr int
	// addrNet makes this hoster's addresses unique to this test: the test
	// database keeps every box ever recorded, and a box IS its address — a
	// fixed range would have one run claim the address the next one is handed.
	addrNet [6]byte
}

// newFakeHoster offers two sizes in two regions whose cities give distinct box
// name prefixes per test — the test database keeps every box ever made, and a
// fixed city would have runs fighting over ams1.
func newFakeHoster(t *testing.T) *fakeHoster {
	t.Helper()
	u := uuid.New()
	city := func(b []byte) string {
		s := make([]rune, 3)
		for i := range s {
			s[i] = rune('a' + int(b[i])%26)
		}
		return strings.ToUpper(string(s[:1])) + string(s[1:])
	}
	return &fakeHoster{
		balance: 12_345,
		addrNet: [6]byte{u[6], u[7], u[8], u[9]},
		regions: []BoxRegion{
			{ID: "r1", City: city(u[0:3]), Country: "NL"},
			{ID: "r2", City: city(u[3:6]), Country: "DE"},
		},
		plans: []BoxPlan{
			{ID: "small", VCPUs: 1, RAMMB: 1024, MonthlyCents: 500, Regions: []string{"r1", "r2"}},
			{ID: "huge", VCPUs: 8, RAMMB: 16384, MonthlyCents: 9900, Regions: []string{"r1"}},
			{ID: "r2only", VCPUs: 1, RAMMB: 1024, MonthlyCents: 600, Regions: []string{"r2"}},
		},
		instances: map[string]*BoxInstance{},
		polls:     map[string]int{},
		keys:      map[string]string{},
	}
}

func (f *fakeHoster) Regions(context.Context) ([]BoxRegion, error) { return f.regions, nil }
func (f *fakeHoster) Plans(context.Context) ([]BoxPlan, error)     { return f.plans, nil }

func (f *fakeHoster) EnsureSSHKey(_ context.Context, name, publicKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, k := range f.keys {
		if k == publicKey {
			return id, nil
		}
	}
	id := fmt.Sprintf("key-%d", len(f.keys)+1)
	f.keys[id] = publicKey
	return id, nil
}

func (f *fakeHoster) Create(_ context.Context, o BoxOrder) (BoxInstance, error) {
	f.mu.Lock()
	delay := f.createDelay
	f.mu.Unlock()
	time.Sleep(delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempted = append(f.attempted, o)
	if err := f.createErrForRegion[o.Region]; err != nil {
		return BoxInstance{}, err
	}
	if f.createErr != nil {
		return BoxInstance{}, f.createErr
	}
	f.ordered = append(f.ordered, o)
	f.nextID++
	f.nextAddr++
	var price int
	for _, p := range f.plans {
		if p.ID == o.Plan {
			price = p.MonthlyCents
		}
	}
	inst := &BoxInstance{
		ID: fmt.Sprintf("inst-%s-%d", o.Hostname, f.nextID), Label: o.Label,
		Region: o.Region, Plan: o.Plan, Status: "pending", MonthlyCents: price,
		Address: testAddr(f.addrNet, uint16(f.nextAddr)),
		Tags:    []string{o.Tag},
	}
	f.instances[inst.ID] = inst
	if f.onCreate != nil {
		f.onCreate(o)
	}
	if f.loseAnswer != nil {
		return BoxInstance{}, f.loseAnswer
	}
	shown := *inst
	if f.pollsBeforeAddress > 0 {
		shown.Address = ""
	}
	return shown, nil
}

func (f *fakeHoster) Instance(_ context.Context, id string) (BoxInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.instanceErr != nil {
		return BoxInstance{}, f.instanceErr
	}
	inst, ok := f.instances[id]
	if !ok {
		return BoxInstance{}, ErrNoSuchInstance
	}
	f.polls[id]++
	out := *inst
	if f.polls[id] < f.pollsBeforeAddress {
		out.Address = ""
		return out, nil
	}
	out.Status = "active"
	return out, nil
}

func (f *fakeHoster) Instances(context.Context) ([]BoxInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []BoxInstance{}
	for id, inst := range f.instances {
		if !f.hidden[id] {
			out = append(out, *inst)
		}
	}
	return out, nil
}

func (f *fakeHoster) Reboot(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.instances[id]; !ok {
		return ErrNoSuchInstance
	}
	f.rebooted = append(f.rebooted, id)
	return nil
}

func (f *fakeHoster) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	delete(f.instances, id)
	return nil
}

func (f *fakeHoster) Account(context.Context) (HostingAccount, error) {
	if f.badKey {
		return HostingAccount{}, errors.New("hoster: key rejected")
	}
	return HostingAccount{BalanceCents: f.balance, PendingCents: 700}, nil
}

func (f *fakeHoster) orderCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ordered)
}

func (f *fakeHoster) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// hostHarness is a provisioning harness whose hub has a hosting account saved
// and enabled, and whose machines come up with the hub's key already in
// authorized_keys — which is what ordering through an API buys.
type hostHarness struct {
	*provHarness
	host *fakeHoster
	conn *fakeConnector
}

const fakeHostKind = "fakehost"

var goodHostCreds = map[string]string{"api_key": "vk1_rightrightright"}

func newHostHarness(t *testing.T) *hostHarness {
	t.Helper()
	ph := newProvHarness(t)
	ph.box.keyIn = true // ordered machines boot with the hub's key
	ph.box.password = ""
	host := newFakeHoster(t)
	conn := &fakeConnector{reg: newFakeRegistrar(), host: host}
	ph.svc.SetProviderConnector(conn)
	ph.svc.checkSpacing = 0
	ph.svc.instancePoll = 5 * time.Millisecond
	// A live observation point: a top-up waits for one (noLiveVantage).
	ph.svc.SetVantages(fakeVantages{regions: []string{testVantage}})
	ctx := context.Background()
	if err := ph.svc.noteAdmissionPoll(ctx, ph.pool, testVantage); err != nil {
		t.Fatal(err)
	}
	if _, err := ph.svc.SaveProvider(ctx, fakeHostKind, ProviderUpdate{
		Enabled: true, Credentials: goodHostCreds,
		DefaultRegion: "r1", DefaultPlan: "small",
	}, ""); err != nil {
		t.Fatalf("save hoster: %v", err)
	}
	return &hostHarness{provHarness: ph, host: host, conn: conn}
}

// A hoster's credentials go in through the same door the registrar's do: the
// key is checked against the provider before anything is written, sealed when
// it is, shown only as a mask, and applied to the running hub at once.
func TestSaveHosterChecksKeySealsItAndApplies(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()

	var ps []ProviderView
	ps, err := h.svc.Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var v ProviderView
	for _, p := range ps {
		if p.Kind == fakeHostKind {
			v = p
		}
	}
	switch {
	case v.Role != RoleHosting:
		t.Fatalf("role = %q", v.Role)
	case !v.Configured || !v.Enabled || !v.Active:
		t.Errorf("view = %+v, want configured, enabled, active", v)
	case v.DefaultRegion != "r1" || v.DefaultPlan != "small":
		t.Errorf("defaults = %q/%q", v.DefaultRegion, v.DefaultPlan)
	case v.Check == nil || !v.Check.OK || v.Check.BalanceCents == nil || *v.Check.BalanceCents != 12_345:
		t.Errorf("check = %+v, want ok with the hoster's credit", v.Check)
	case v.CredsHint == "" || strings.Contains(v.CredsHint, "rightright"):
		t.Errorf("hint %q shows the key", v.CredsHint)
	}

	// Sealed, not stored: the key is nowhere in the row's bytes.
	var ct []byte
	if err := h.pool.QueryRow(ctx,
		`SELECT creds_ct FROM entry_providers WHERE kind = $1`, fakeHostKind).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ct), "vk1_right") {
		t.Error("the hoster key is stored in the clear")
	}

	// A key the hoster refuses is not saved, and the working one stays.
	h.host.badKey = true
	_, err = h.svc.SaveProvider(ctx, fakeHostKind, ProviderUpdate{
		Enabled: true, Credentials: map[string]string{"api_key": "vk1_wrong"},
	}, "")
	if !errors.Is(err, ErrProviderCheck) {
		t.Fatalf("saving a refused key: err = %v, want ErrProviderCheck", err)
	}
	h.host.badKey = false
	if _, err := h.svc.HostingCatalog(ctx); err != nil {
		t.Errorf("the hub lost its working hoster: %v", err)
	}

	// Switched off, the hub has no hoster at all — a state, not an error, and
	// the one an all-by-hand fleet sits in.
	if _, err := h.svc.SaveProvider(ctx, fakeHostKind, ProviderUpdate{
		Enabled: false,
	}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.HostingCatalog(ctx); !errors.Is(err, ErrNoHoster) {
		t.Fatalf("catalog with the hoster off: err = %v, want ErrNoHoster", err)
	}
}

// The whole order: the machine is rented, waited for, recorded with its rent,
// and goes through the same provisioning and the same admission probe as one
// typed in by hand.
func TestOrderingAMachineRecordsItsRentalAndAdmitsIt(t *testing.T) {
	h := newHostHarness(t)
	h.host.pollsBeforeAddress = 2 // the first look has no address yet
	h.vantage("clean")
	ctx := context.Background()

	box, job, err := h.prov.OrderBox(ctx, OrderSpec{}, "")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	h.made = append(h.made, box+"-1")
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, h.logText(job))
	}

	// The name came from the default region's city, and the row carries the
	// rental: the instance to reboot or delete, where it was ordered, and what
	// it costs.
	prefix := strings.ToLower(h.host.regions[0].City)[:3]
	if box != prefix+"1" {
		t.Errorf("box named %q, want %s1 from the region's city", box, prefix)
	}
	boxes, err := h.svc.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got PoolBox
	for _, b := range boxes {
		if b.Name == box {
			got = b
		}
	}
	switch {
	case got.InstanceID == "":
		t.Error("the box carries no instance id — nothing could reboot or delete it")
	case got.ProviderRegion != "r1" || got.Plan != "small":
		t.Errorf("rental = %q/%q", got.ProviderRegion, got.Plan)
	case got.MonthlyCents == nil || *got.MonthlyCents != 500:
		t.Errorf("monthly = %v, want 500", got.MonthlyCents)
	case got.Provider != fakeHostKind:
		t.Errorf("provider = %q", got.Provider)
	case got.Address != h.host.instances[got.InstanceID].Address:
		t.Errorf("address %q is not the instance's", got.Address)
	}

	// The relay on it is alive, which only a clean verdict from the vantage
	// does: the autopilot does not get to skip admission.
	relays, _ := h.svc.List(ctx)
	alive := false
	for _, r := range relays {
		if r.Box == box && r.Status == StatusAlive {
			alive = true
		}
	}
	if !alive {
		t.Fatalf("no alive relay on the ordered machine\n%s", h.logText(job))
	}

	// The hub's key went up once and the order named it, so the machine came
	// up reachable without a password ever existing.
	if len(h.host.keys) != 1 {
		t.Errorf("ssh keys at the hoster: %d, want 1", len(h.host.keys))
	}
	o := h.host.ordered[0]
	if o.SSHKeyID == "" || o.Tag != hostingTag || o.Hostname != box {
		t.Errorf("order = %+v", o)
	}

	// A second order takes the next free name and does not re-upload the key.
	box2, _, err := h.prov.OrderBox(ctx, OrderSpec{}, "")
	if err != nil {
		t.Fatalf("second order: %v", err)
	}
	h.made = append(h.made, box2+"-1")
	if box2 != prefix+"2" {
		t.Errorf("second box named %q, want %s2", box2, prefix)
	}
	if len(h.host.keys) != 1 {
		t.Errorf("the hub's key was uploaded twice")
	}
}

// The money gate is checked before the order: the machine's hours to the end
// of the day on top of the day's projection, against the day's cap. A refused
// order costs nothing, which means no instance was created at all.
func TestAnOrderOverBudgetIsRefusedBeforeItIsPlaced(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()

	// The day counts what is already rented — every priced machine, including
	// the ones other runs left in this database, which is exactly the point
	// of a group-wide cap. So the cap under test is set relative to the day
	// as it stands: room for two $99 machines and a half (a dear plan, so a
	// late hour's rest of the day is still dollars, not a cent and a half).
	h.spendRoom(SpendHosting, 2.5*machineDayOf(hugeCents))
	for i := 0; i < 2; i++ {
		box, err := h.prov.orderAndRecord(ctx, OrderSpec{Plan: "huge"})
		if err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
		h.t.Cleanup(func() { h.retire(box) })
	}
	_, _, err := h.prov.OrderBox(ctx, OrderSpec{Plan: "huge"}, "")
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("third machine over the day's cap: err = %v, want ErrBudget", err)
	}
	if n := h.host.orderCount(); n != 2 {
		t.Fatalf("%d machines ordered, want 2", n)
	}

	// A plan the hoster does not sell in that region is refused without a
	// charge too, and says so as a plan problem rather than a money one.
	if _, err := h.prov.orderAndRecord(ctx, OrderSpec{Region: "r1", Plan: "r2only"}); !errors.Is(err, ErrNoSuchPlan) {
		t.Fatalf("plan not sold in the region: err = %v, want ErrNoSuchPlan", err)
	}
}

// An order that cannot be recorded is undone: the machine was created seconds
// ago, carries nothing, and leaving it would be paying for debris.
func TestAnOrderIsUndoneWhenTheBoxCannotBeRecorded(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	h.takeNameMidOrder()

	if _, err := h.prov.orderAndRecord(ctx, OrderSpec{}); err == nil {
		t.Fatal("recording a box under a name already taken succeeded")
	}
	if n := h.host.orderCount(); n != 1 {
		t.Fatalf("orders placed: %d, want 1", n)
	}
	if got := h.host.deletedIDs(); len(got) != 1 {
		t.Fatalf("deleted instances: %v, want the one just ordered", got)
	}
	if left, _ := h.host.Instances(ctx); len(left) != 0 {
		t.Fatalf("the hoster still holds %d machines nobody records", len(left))
	}
}

// An instance the hub pays for and has no box for is the debris of an order
// that died mid-flight. The console cannot work that out for itself, so the
// hub says which is which.
func TestOrphanInstancesAreNamed(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()

	box, err := h.prov.orderAndRecord(ctx, OrderSpec{})
	if err != nil {
		t.Fatal(err)
	}
	h.t.Cleanup(func() { h.retire(box) })
	orphan, err := h.host.Create(ctx, BoxOrder{Region: "r1", Plan: "small", Hostname: "lost", Label: "nekko-lost"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := h.svc.HostingInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]HostingInstance{}
	for _, i := range list {
		seen[i.ID] = i
	}
	if len(seen) != 2 {
		t.Fatalf("instances listed: %d, want 2", len(seen))
	}
	if o := seen[orphan.ID]; !o.Orphan || o.Box != "" {
		t.Errorf("the unrecorded instance reads as %+v", o)
	}
	for id, i := range seen {
		if id != orphan.ID && (i.Orphan || i.Box != box) {
			t.Errorf("the recorded instance reads as %+v, want box %s", i, box)
		}
	}
}

// Handing a machine back deletes it at the hoster, which is what stops the
// charge. A machine still in service is refused, and one rented by hand says
// so instead of pretending.
func TestDeletingAnInstanceNeedsARetiredBoxTheHubOrdered(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()

	byHand, _ := h.harness.box()
	if err := h.svc.DeleteBoxInstance(ctx, byHand); !errors.Is(err, ErrRejected) {
		t.Fatalf("deleting a box in service: err = %v, want a refusal", err)
	}
	if _, _, err := h.svc.Retire(ctx, byHand, DeathRotated); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteBoxInstance(ctx, byHand); !errors.Is(err, ErrRejected) {
		t.Fatalf("a hand-rented machine: err = %v, want a refusal naming it", err)
	}

	ordered, err := h.prov.orderAndRecord(ctx, OrderSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteBoxInstance(ctx, ordered); !errors.Is(err, ErrRejected) {
		t.Fatalf("an ordered machine in service: err = %v, want a refusal", err)
	}
	if _, _, err := h.svc.Retire(ctx, ordered, DeathRotated); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteBoxInstance(ctx, ordered); err != nil {
		t.Fatalf("delete after retiring: %v", err)
	}
	if got := h.host.deletedIDs(); len(got) != 1 {
		t.Fatalf("deleted = %v, want one instance", got)
	}
}

// takeNameMidOrder has a box appear under the name an order chose, between
// the charge and the order's box row — a box typed in by hand at that moment.
// The name was free when the order reserved it; the row write is what fails.
func (h *hostHarness) takeNameMidOrder() {
	h.t.Helper()
	h.host.onCreate = func(o BoxOrder) {
		h.host.onCreate = nil
		u := uuid.New()
		if err := h.svc.CreateBox(context.Background(), BoxSpec{
			Name: o.Hostname, Address: testAddr([6]byte{0xff, 0xff, u[0], u[1]}, 1),
			Provider: "vultr", Location: "amsterdam",
		}); err != nil {
			h.t.Errorf("take %s: %v", o.Hostname, err)
		}
	}
}

// retire takes a box out of service so one test's fleet does not count against
// the next one's budget.
func (h *hostHarness) retire(box string) {
	ctx := context.Background()
	if _, _, err := h.svc.Retire(ctx, box, DeathRotated); err != nil && !errors.Is(err, ErrBoxRetired) {
		h.t.Errorf("retire %s: %v", box, err)
	}
	// The machine was this test's fake hoster's, gone with it: nothing left
	// to pay for, so nothing for a later pass to delete.
	if _, err := h.pool.Exec(ctx, `UPDATE entry_boxes SET released_at = now() WHERE name = $1`, box); err != nil {
		h.t.Errorf("release %s: %v", box, err)
	}
}

// reboot_box: the hoster's button is pressed, the hub waits for ssh, and the
// relays go back through admission. It runs through the repair runner, so the
// rails and the bookkeeping apply to it like any other repair.
func TestRebootBoxRestartsTheMachineAndReprobes(t *testing.T) {
	h := newHostHarness(t)
	h.vantage("clean")
	ctx := context.Background()

	box, job, err := h.prov.OrderBox(ctx, OrderSpec{}, "")
	if err != nil {
		t.Fatal(err)
	}
	h.made = append(h.made, box+"-1")
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("provision %+v\n%s", j, h.logText(job))
	}
	h.t.Cleanup(func() { h.retire(box) })

	inc := h.openIncident(box, CausePortsBanned, map[string]string{box + "-1": "ip_banned"})
	m := NewMechanic(h.svc, h.prov)
	h.vantage("clean")
	if err := m.run(ctx, inc, PlaybookRebootBox, DecidedAuto); err != nil {
		t.Fatalf("run reboot_box: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		rs, err := h.svc.Repairs(ctx, inc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rs) == 1 && rs[0].Status != repairRunning {
			if rs[0].Status != repairDone {
				t.Fatalf("repair %+v", rs[0])
			}
			if rs[0].CostCents != 0 {
				t.Errorf("a reboot cost %d — it is free", rs[0].CostCents)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the repair never finished: %+v", rs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if got := h.host.rebooted; len(got) != 1 {
		t.Fatalf("rebooted = %v, want the box's instance", got)
	}
	// Nothing was replaced: the address is the same machine's and the box is
	// still in service.
	boxes, _ := h.svc.Boxes(ctx)
	for _, b := range boxes {
		if b.Name == box && b.RetiredAt != nil {
			t.Error("a reboot retired the box")
		}
	}
}

// evict_box, which replaced replace_box (П9.11): the machine leaves the pool
// at once — its relays suspect, their holders told to refill — but nothing is
// bought, nothing is retired and nothing is deleted at the hoster. The
// machine waits for its second chance; the fleet buys the replacement.
func TestEvictBoxTakesTheMachineOutOfThePoolAndKeepsIt(t *testing.T) {
	h := newHostHarness(t)
	h.vantage("clean")
	ctx := context.Background()

	old := h.inService()
	orders := h.host.orderCount()

	inc := h.openIncident(old, CauseIPBanned, map[string]string{old + "-1": "ip_banned"})
	res, err := NewMechanic(h.svc, h.prov).perform(ctx, inc, PlaybookEvictBox)
	if err != nil {
		t.Fatalf("evict_box: %v", err)
	}
	if res.cost != 0 || res.replacedBy != "" {
		t.Errorf("result %+v: an eviction buys nothing", res)
	}
	if n := h.host.orderCount(); n != orders {
		t.Errorf("orders %d → %d on an eviction", orders, n)
	}
	if h.retired(old) {
		t.Error("the evicted machine was retired: that is its second chance's call")
	}
	if got := h.host.deletedIDs(); len(got) != 0 {
		t.Errorf("deleted = %v on an eviction", got)
	}
	if h.aliveOn(old) != 0 {
		t.Error("the evicted machine still hands out relays")
	}
	relays, _ := h.svc.List(ctx)
	for _, r := range relays {
		if r.Box == old && r.Status != StatusSuspect && r.Status != StatusDead {
			t.Errorf("relay %s is %s, want suspect", r.Name, r.Status)
		}
	}
	var status, reason string
	if err := h.pool.QueryRow(ctx,
		`SELECT status, reason FROM entry_suspects WHERE kind = 'box' AND subject = $1`, old,
	).Scan(&status, &reason); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if status != suspectOpen || reason != CauseIPBanned {
		t.Errorf("ledger = %s/%s, want suspect/ip_banned", status, reason)
	}
	if !strings.Contains(res.outcome, "снята с выдачи") || !strings.Contains(res.outcome, "замену добирает парк пула") {
		t.Errorf("outcome = %q, want the eviction and who replaces it", res.outcome)
	}
	// Evicting it again is refused: it has nothing left in the pool.
	if _, err := NewMechanic(h.svc, h.prov).perform(ctx, inc, PlaybookEvictBox); !errors.Is(err, ErrRejected) {
		t.Errorf("second eviction: err = %v, want a refusal", err)
	}
}

// scaleUpReality runs one scale_up on the default pool (Reality, the shelf
// the idle-machine helpers fill), with a target far above what any run leaves
// alive, and returns what it did. The pool is shared by every test, so the
// target and the incident are removed afterwards.
func (h *hostHarness) scaleUpReality() (repairResult, error) {
	h.t.Helper()
	ctx := context.Background()
	pool := FleetPool(ProtoVlessRealityVision, "")
	// The reality pool is every run's: its load is set to ask for exactly
	// one machine more than it holds, loss buffer included.
	for n := 1; ; n++ {
		h.svc.primeSizing(map[string]PoolSizing{pool: sizingFor(n)})
		f, err := h.svc.poolForecast(ctx, mustPolicy(h.t, h.svc), pool)
		if err != nil {
			h.t.Fatal(err)
		}
		if f.Need == 1 {
			break
		}
		if n > 1000 {
			h.t.Fatalf("no load asks the reality pool for one machine: %+v", f)
		}
	}
	inc, err := h.svc.openPool(ctx, pool, CauseShortOfFleet, "тест", map[string]string{})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = h.pool.Exec(ctx, `DELETE FROM entry_incidents WHERE pool = $1`, pool)
	})
	return NewMechanic(h.svc, h.prov).perform(ctx, *inc, PlaybookScaleUp)
}

// A machine the hoster has addressed but not finished booting is waited for.
// Found on the stand 25.10: the first live replace_box dialled ssh a second
// after the address and failed on a timeout while the machine was still in
// its first boot.
// An order from the console waits for the machine's first boot before it
// logs in. Found on the stand 26.10: the job dialled `ams5` fifteen seconds
// after the hoster handed it over and failed on an ssh timeout.
func TestAnOrderedMachineIsWaitedForBeforeItsFirstLogin(t *testing.T) {
	h := newHostHarness(t)
	h.vantage("clean")
	booting := 3
	real := h.prov.dial
	h.prov.dial = func(ctx context.Context, tg sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
		if booting > 0 {
			booting--
			return nil, errors.New("dial tcp: i/o timeout")
		}
		return real(ctx, tg, auth...)
	}
	h.prov.drainPoll, h.prov.bootWait = time.Millisecond, 5*time.Second

	box, job, err := h.prov.OrderBox(context.Background(), OrderSpec{}, "")
	if err != nil {
		t.Fatal(err)
	}
	h.made = append(h.made, box+"-1")
	h.t.Cleanup(func() { h.retire(box) })
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, h.logText(job))
	}
	if booting != 0 {
		t.Fatalf("the order never had to wait (%d timeouts left)", booting)
	}
}

func TestAReplacementWaitsForTheFirstBoot(t *testing.T) {
	h := newHostHarness(t)
	h.vantage("clean")
	ctx := context.Background()
	old, job, err := h.prov.OrderBox(ctx, OrderSpec{}, "")
	if err != nil {
		t.Fatal(err)
	}
	h.made = append(h.made, old+"-1")
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("provision %+v", j)
	}
	h.t.Cleanup(func() { h.retire(old) })

	// The next three dials time out, as sshd on a booting machine does.
	booting := 3
	real := h.prov.dial
	h.prov.dial = func(ctx context.Context, tg sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
		if booting > 0 {
			booting--
			return nil, errors.New("dial tcp: i/o timeout")
		}
		return real(ctx, tg, auth...)
	}
	h.prov.drainPoll, h.prov.bootWait = time.Millisecond, 5*time.Second

	h.vantage("clean")
	res, err := h.scaleUpReality()
	outcome := res.outcome
	if err != nil {
		t.Fatalf("scale_up on a slow first boot: %v", err)
	}
	if booting != 0 {
		t.Fatalf("the deploy never had to wait (%d timeouts left)", booting)
	}
	if !strings.Contains(outcome, "добавлено 1 из 1") {
		t.Errorf("outcome = %q", outcome)
	}
	for _, b := range h.boxesSince(old) {
		h.t.Cleanup(func() { h.retire(b) })
	}
}

// A machine that never lets the hub in is retired and deleted at the hoster
// by the hub, and the machines already in the pool are not touched.
func TestAReplacementThatNeverBootsIsHandedBack(t *testing.T) {
	h := newHostHarness(t)
	h.vantage("clean")
	ctx := context.Background()
	old, job, err := h.prov.OrderBox(ctx, OrderSpec{}, "")
	if err != nil {
		t.Fatal(err)
	}
	h.made = append(h.made, old+"-1")
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("provision %+v", j)
	}
	h.t.Cleanup(func() { h.retire(old) })

	h.prov.dial = func(context.Context, sshexec.Target, ...ssh.AuthMethod) (Remote, error) {
		return nil, errors.New("dial tcp: i/o timeout")
	}
	h.prov.drainPoll, h.prov.bootWait = time.Millisecond, 30*time.Millisecond

	_, err = h.scaleUpReality()
	if err == nil || !strings.Contains(err.Error(), "удалена у хостера") {
		t.Fatalf("err = %v, want the new machine handed back", err)
	}
	if got := h.host.deletedIDs(); len(got) != 1 {
		t.Errorf("deleted = %v, want the machine that never booted", got)
	}
	relays, _ := h.svc.List(ctx)
	alive := 0
	for _, r := range relays {
		if r.Box == old && r.Status == StatusAlive {
			alive++
		}
	}
	if alive == 0 {
		t.Error("a machine in the pool stopped carrying people over a failed order")
	}
}

// The eviction is the hub's to do by itself, at once — it is free, and people
// on a banned address are already cut off; what spends money is the fleet's
// purchase, which keeps its own `ask`.
func TestEvictBoxRunsUnattendedAndBuysNothing(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box := h.inService()
	orders := h.host.orderCount()
	inc := h.openIncident(box, CauseIPBanned, map[string]string{box + "-1": "ip_banned"})

	if p := PlaybookForCause(CauseIPBanned); p.Name != PlaybookEvictBox || p.Mode != ModeAuto || p.Spends {
		t.Fatalf("playbook for ip_banned = %+v, want evict_box on auto, free", p)
	}
	if p := PlaybookForCause(CauseShortOfFleet); p.Mode != ModeAuto || !p.Spends {
		t.Fatalf("playbook for short_of_fleet = %+v, want the purchase on auto, under the caps", p)
	}
	started, _ := NewMechanic(h.svc, h.prov).consider(ctx, inc)
	if !started {
		t.Fatal("the eviction did not start by itself")
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.aliveOn(box) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the machine is still in the pool")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := h.host.orderCount(); n != orders {
		t.Fatalf("orders %d → %d on the eviction", orders, n)
	}
}

// A repair that cannot apply to the incident in hand says so instead of
// reporting a success nobody performed: rotate_dest and restart_edge find
// nothing in the evidence of a banned address to act on, and scale_up adds a
// machine to a pool — an incident about one box is not one it can answer
// (П9.10 implemented it; before that it was refused as unwritten).
func TestRepairsRefuseAnIncidentTheyDoNotFit(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	box, _ := h.harness.box()
	inc := h.openIncident(box, CauseIPBanned, map[string]string{box + "-1": "ip_banned"})
	for _, playbook := range []string{"rotate_dest", "restart_edge", "scale_up"} {
		if _, err := NewMechanic(h.svc, h.prov).perform(ctx, inc, playbook); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: err = %v, want a refusal", playbook, err)
		}
	}
}

// A machine the hub ordered burns its hourly price from its order to the end
// of the day in the projection; a machine rented by hand has no price and
// adds nothing. Handed back, it stops: what it ran stays, the rest of the day
// goes.
func TestTheDayCountsWhatTheHubOrderedUntilItIsHandedBack(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()

	before := h.dayProjected()
	byHand, _ := h.harness.box()
	h.t.Cleanup(func() { h.retire(byHand) })
	ordered, err := h.prov.orderAndRecord(ctx, OrderSpec{})
	if err != nil {
		t.Fatal(err)
	}
	h.t.Cleanup(func() { h.retire(ordered) })
	if got := h.dayProjected() - before; !nearCents(got, machineDay()) {
		t.Errorf("the day moved by %.2f, want the ordered machine's %.2f", got, machineDay())
	}

	h.retire(ordered)
	if got := h.dayProjected() - before; got > machineDayOf(500)/float64(max(1, hoursLeftToday()))+0.01 {
		t.Errorf("the day stays %.2f up after the handback, want only the hour it ran", got)
	}
}

// The catalog is what the console's order form needs, and it comes with the
// money gate beside it so the form can refuse before a person presses.
func TestTheCatalogCarriesTheOfferAndTheBudget(t *testing.T) {
	h := newHostHarness(t)
	cat, err := h.svc.HostingCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case cat.Kind != fakeHostKind:
		t.Errorf("kind = %q", cat.Kind)
	case len(cat.Regions) != 2 || len(cat.Plans) != 3:
		t.Errorf("offer = %d regions, %d plans", len(cat.Regions), len(cat.Plans))
	case cat.Region != "r1" || cat.Plan != "small":
		t.Errorf("defaults = %q/%q", cat.Region, cat.Plan)
	case cat.Spend.Role != SpendHosting || cat.Spend.DailyCents != generousCapCents:
		t.Errorf("spend = %+v", cat.Spend)
	}
	if !cat.Plans[0].AvailableIn("r1") || cat.Plans[2].AvailableIn("r1") {
		t.Error("a plan's regions are not read")
	}
	if _, err := fmt.Sscanf(money(1234), "$12.34"); err != nil {
		t.Errorf("money(1234) = %q", money(1234))
	}
}

// The console surface of the hoster (П9.2): renting a machine is behind the
// step-up, because it is the most expensive thing the console can do; looking
// at the offer and rebooting a machine are not.
func TestAdminHostingSurface(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	var adminID string
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, 'entry host test', 'x', 'owner') RETURNING id::text`,
		"host-"+uuid.NewString()[:8]).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	steppedUp := false
	gate := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: adminID})))
		}
	}
	stepUp := func(next http.HandlerFunc) http.HandlerFunc {
		return gate(func(w http.ResponseWriter, r *http.Request) {
			if !steppedUp {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "step_up_required"})
				return
			}
			next(w, r)
		})
	}
	RegisterAdminRoutes(mux, NewAdminHandlers(h.svc, h.prov, h.pool), gate, stepUp)
	do := func(method, path string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		return rec.Code, rec.Body.Bytes()
	}

	// The offer, readable without a touch, and it never carries the key.
	code, body := do("GET", "/api/v1/admin/entry/hosting", nil)
	if code != 200 {
		t.Fatalf("hosting: %d %s", code, body)
	}
	if !bytes.Contains(body, []byte(`"defaultPlan":"small"`)) || !bytes.Contains(body, []byte(`"spend":{"role":"hosting"`)) {
		t.Fatalf("hosting: %s", body)
	}
	if bytes.Contains(body, []byte("vk1_right")) {
		t.Fatalf("the catalog carries the key: %s", body)
	}

	// Ordering is a step-up, and the refusal costs nothing.
	order := map[string]any{"proto": ProtoVlessRealityVision}
	if code, _ := do("POST", "/api/v1/admin/entry/boxes/order", order); code != http.StatusForbidden {
		t.Fatalf("order without a touch: %d, want 403", code)
	}
	if n := h.host.orderCount(); n != 0 {
		t.Fatalf("%d machines ordered without a touch", n)
	}
	steppedUp = true
	h.vantage("clean")
	code, body = do("POST", "/api/v1/admin/entry/boxes/order", order)
	if code != 200 {
		t.Fatalf("order: %d %s", code, body)
	}
	var ordered struct {
		Box   string `json:"box"`
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(body, &ordered); err != nil {
		t.Fatal(err)
	}
	if ordered.Box == "" || ordered.JobID == "" {
		t.Fatalf("order answered %s", body)
	}
	h.made = append(h.made, ordered.Box+"-1")
	job, err := uuid.Parse(ordered.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if j := h.wait(job); j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, h.logText(job))
	}
	h.t.Cleanup(func() { h.retire(ordered.Box) })

	// An order over the cap is a conflict with a reason, not a 500.
	h.spendRoom(SpendHosting, 0.5*machineDayOf(hugeCents))
	if code, body := do("POST", "/api/v1/admin/entry/boxes/order", map[string]any{"plan": "huge"}); code != http.StatusConflict ||
		!bytes.Contains(body, []byte("потолок")) {
		t.Fatalf("order over budget: %d %s", code, body)
	}

	// A reboot is free, so it needs no touch — and the instances listing says
	// which machines the hub has a row for.
	steppedUp = false
	h.vantage("clean")
	code, body = do("POST", "/api/v1/admin/entry/boxes/"+ordered.Box+"/reboot", nil)
	var rebooted struct {
		JobID string `json:"jobId"`
	}
	if code != 200 || json.Unmarshal(body, &rebooted) != nil {
		t.Fatalf("reboot: %d %s", code, body)
	}
	// The reboot re-probes the machine; it has to be over before the machine
	// is handed back below, or the hand-back races the job over the box.
	rebootJob, err := uuid.Parse(rebooted.JobID)
	if err != nil {
		t.Fatalf("reboot answered %s", body)
	}
	if j := h.wait(rebootJob); j.Status != "done" {
		t.Fatalf("reboot job %+v\n%s", j, h.logText(rebootJob))
	}
	code, body = do("GET", "/api/v1/admin/entry/hosting/instances", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"orphan":false`)) {
		t.Fatalf("instances: %d %s", code, body)
	}

	// Handing the machine back deletes it at the hoster instead of wiping it:
	// the disk goes with the instance, and that is what stops the charge.
	// Behind the step-up (audit 42 С5): there is no way back from it.
	steppedUp = true
	if code, body := do("POST", "/api/v1/admin/entry/boxes/"+ordered.Box+"/retire",
		map[string]string{"reason": DeathIPBanned}); code != 200 ||
		!bytes.Contains(body, []byte(`"instanceDeleted":true`)) {
		t.Fatalf("retire: %d %s", code, body)
	}
	if got := h.host.deletedIDs(); len(got) != 1 {
		t.Fatalf("deleted = %v, want the retired machine's instance", got)
	}
}

// boxesSince lists boxes created after the named one, for cleanup.
func (h *hostHarness) boxesSince(name string) []string {
	h.t.Helper()
	var out []string
	_ = h.pool.QueryRow(context.Background(), `
		SELECT coalesce(array_agg(name), '{}') FROM entry_boxes
		WHERE created_at > (SELECT created_at FROM entry_boxes WHERE name = $1)`, name).Scan(&out)
	return out
}

// ── Idle machines: taken before a purchase, handed back when nobody does ──

// idleMachine records a machine the hub ordered whose first deploy died
// before a relay was admitted — ams2 on the stand, 25.10. Returns the box and
// its instance at this test's hoster.
func (h *hostHarness) idleMachine() (box, instance string) {
	h.t.Helper()
	ctx := context.Background()
	box, err := h.prov.orderAndRecord(ctx, OrderSpec{})
	if err != nil {
		h.t.Fatalf("order idle machine: %v", err)
	}
	h.t.Cleanup(func() { h.retire(box) })
	_ = h.prov.execJob(ctx, JobProvision, box, uuid.Nil, func(*jobRun) error {
		return errors.New("разворот упал до релеев")
	})
	return box, h.instanceOf(box)
}

func (h *hostHarness) instanceOf(box string) string {
	h.t.Helper()
	var id string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT provider_instance_id FROM entry_boxes WHERE name = $1`, box).Scan(&id); err != nil {
		h.t.Fatalf("instance of %s: %v", box, err)
	}
	return id
}

func (h *hostHarness) retired(box string) bool {
	h.t.Helper()
	var retired bool
	if err := h.pool.QueryRow(context.Background(),
		`SELECT retired_at IS NOT NULL FROM entry_boxes WHERE name = $1`, box).Scan(&retired); err != nil {
		h.t.Fatalf("box %s: %v", box, err)
	}
	return retired
}

func (h *hostHarness) aliveOn(box string) int {
	h.t.Helper()
	relays, _ := h.svc.List(context.Background())
	n := 0
	for _, r := range relays {
		if r.Box == box && r.Status == StatusAlive {
			n++
		}
	}
	return n
}

// inService orders a machine and waits until it carries people.
func (h *hostHarness) inService() string {
	h.t.Helper()
	h.vantage("clean")
	box, job, err := h.prov.OrderBox(context.Background(), OrderSpec{}, "")
	if err != nil {
		h.t.Fatal(err)
	}
	h.made = append(h.made, box+"-1")
	if j := h.wait(job); j.Status != "done" {
		h.t.Fatalf("provision %+v\n%s", j, h.logText(job))
	}
	h.t.Cleanup(func() { h.retire(box) })
	return box
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// The case from the stand: an idle machine is on the shelf, and the
// replacement takes it instead of buying another.
func TestAReplacementTakesTheIdleMachineInsteadOfBuying(t *testing.T) {
	h := newHostHarness(t)
	idle, _ := h.idleMachine()
	h.made = append(h.made, idle+"-1")
	orders := h.host.orderCount()

	h.vantage("clean")
	res, err := h.scaleUpReality()
	outcome, cost := res.outcome, res.cost
	if err != nil {
		t.Fatalf("scale_up: %v", err)
	}
	if n := h.host.orderCount(); n != orders {
		t.Errorf("orders %d → %d: a machine was bought with one idle on the shelf", orders, n)
	}
	if h.aliveOn(idle) == 0 || res.replacedBy != idle {
		t.Errorf("the idle machine %s carries nobody after the replacement: %q", idle, outcome)
	}
	if got := h.host.deletedIDs(); len(got) != 0 {
		t.Errorf("deleted = %v, want nothing handed back", got)
	}
	if cost != 500 {
		t.Errorf("cost = %d, want the idle machine's rent", cost)
	}
	if !strings.Contains(outcome, "взята простаивающая "+idle) {
		t.Errorf("outcome = %q, want it to name the idle machine taken", outcome)
	}
}

// An idle machine that will not come up is handed back, and the repair goes
// on to buy a new one.
func TestAnIdleMachineThatFailsIsHandedBackAndANewOneBought(t *testing.T) {
	h := newHostHarness(t)
	idle, idleInstance := h.idleMachine()
	idleAddr := h.host.instances[idleInstance].Address
	orders := h.host.orderCount()

	// The idle machine never answers ssh; everything else does.
	real := h.prov.dial
	h.prov.dial = func(ctx context.Context, tg sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
		if host, _, _ := net.SplitHostPort(tg.Addr); host == idleAddr {
			return nil, errors.New("dial tcp: i/o timeout")
		}
		return real(ctx, tg, auth...)
	}
	h.prov.drainPoll, h.prov.bootWait = time.Millisecond, 30*time.Millisecond

	h.vantage("clean")
	res, err := h.scaleUpReality()
	outcome := res.outcome
	if err != nil {
		t.Fatalf("scale_up: %v", err)
	}
	for _, b := range h.boxesSince(idle) {
		h.made = append(h.made, b+"-1")
		h.t.Cleanup(func() { h.retire(b) })
	}
	if n := h.host.orderCount(); n != orders+1 {
		t.Errorf("orders %d → %d, want one purchase after the idle machine failed", orders, n)
	}
	if !h.retired(idle) || !contains(h.host.deletedIDs(), idleInstance) {
		t.Errorf("the failed idle machine is not handed back: retired=%v deleted=%v",
			h.retired(idle), h.host.deletedIDs())
	}
	if !strings.Contains(outcome, "не поднялась") || !strings.Contains(outcome, "добавлено 1 из 1") {
		t.Errorf("outcome = %q", outcome)
	}
}

// An idle machine the hoster no longer has is struck off and skipped.
func TestAnIdleMachineGoneAtTheHosterIsSkipped(t *testing.T) {
	h := newHostHarness(t)
	idle, idleInstance := h.idleMachine()
	// Deleted in the hoster's panel by hand: the hub was not told.
	h.host.mu.Lock()
	delete(h.host.instances, idleInstance)
	h.host.mu.Unlock()
	orders := h.host.orderCount()

	h.vantage("clean")
	res, err := h.scaleUpReality()
	outcome := res.outcome
	if err != nil {
		t.Fatalf("scale_up: %v", err)
	}
	for _, b := range h.boxesSince(idle) {
		h.made = append(h.made, b+"-1")
		h.t.Cleanup(func() { h.retire(b) })
	}
	if n := h.host.orderCount(); n != orders+1 {
		t.Errorf("orders %d → %d, want a purchase", orders, n)
	}
	if !h.retired(idle) {
		t.Error("a box whose instance is gone stays on record as rented")
	}
	if !strings.Contains(outcome, "уже нет") {
		t.Errorf("outcome = %q, want it to say the idle machine was gone", outcome)
	}
}

// A machine that carries people, or that has a job running, is not idle
// whatever else it looks like.
func TestBusyMachinesAreNotTakenForIdle(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	carrying := h.inService()
	working, _ := h.idleMachine()
	job, err := h.svc.startJobOf(ctx, JobProvision, working, "", uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.svc.finishJob(context.Background(), job, nil) })
	orders := h.host.orderCount()

	h.vantage("clean")
	res, err := h.scaleUpReality()
	outcome := res.outcome
	if err != nil {
		t.Fatalf("scale_up: %v", err)
	}
	for _, b := range h.boxesSince(working) {
		h.made = append(h.made, b+"-1")
		h.t.Cleanup(func() { h.retire(b) })
	}
	if n := h.host.orderCount(); n != orders+1 {
		t.Errorf("orders %d → %d, want a purchase: nothing on the shelf was idle", orders, n)
	}
	if strings.Contains(outcome, "простаивающ") {
		t.Errorf("outcome = %q, a busy machine was taken for idle", outcome)
	}
	if h.retired(carrying) || h.aliveOn(carrying) == 0 {
		t.Error("the machine carrying people was touched")
	}
	if h.retired(working) {
		t.Error("the machine with a job running was handed back")
	}
}

// Nobody took it: past idleHold the pass hands the machine back by itself,
// and a machine idle for a moment stays on the shelf.
func TestTheMechanicHandsBackAMachineIdleTooLong(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	stale, staleInstance := h.idleMachine()
	fresh, freshInstance := h.idleMachine()
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_jobs SET created_at = now() - interval '7 hours', finished_at = now() - interval '7 hours'
		WHERE box = $1`, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx,
		`UPDATE entry_boxes SET created_at = now() - interval '7 hours' WHERE name = $1`, stale); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NewMechanic(h.svc, h.prov).Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if !h.retired(stale) || !contains(h.host.deletedIDs(), staleInstance) {
		t.Errorf("the machine idle for 7h is still rented: retired=%v deleted=%v",
			h.retired(stale), h.host.deletedIDs())
	}
	if h.retired(fresh) || contains(h.host.deletedIDs(), freshInstance) {
		t.Error("a machine idle for a moment was handed back")
	}
}

// Audit 42 Н2: an unavailable provider API cannot justify a second rental.
func TestAnUnreadableIdleMachineStopsNewOrders(t *testing.T) {
	h := newHostHarness(t)
	idle, _ := h.idleMachine()
	before := h.host.orderCount()
	h.host.instanceErr = errors.New("provider unavailable")
	_, _, err := h.prov.adoptIdle(context.Background(), OrderSpec{})
	if err == nil {
		t.Fatal("unreadable idle machine accepted as an empty shelf")
	}
	got, _ := h.prov.obtainMachines(context.Background(), OrderSpec{}, 1)
	if len(got) != 1 || got[0].err == nil {
		t.Fatal("purchase continued after idle lookup failure")
	}
	if h.host.orderCount() != before || h.retired(idle) {
		t.Fatal("provider outage bought a machine or retired the paid one")
	}
}
