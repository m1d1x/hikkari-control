package entry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// fakeRegistrar is an in-memory DomainRegistrar for the acquirer tests. Every
// quote on an allowed TLD is available at costByTLD; buys record the order and
// the A records set.
type fakeRegistrar struct {
	mu            sync.Mutex
	costByTLD     map[string]int
	notRegister   map[string]bool
	insufficient  bool
	withinMonthly bool
	balanceCents  int
	bought        []string
	aimed         map[string]string // domain -> ip
	buys          int
	// badKey makes Account refuse, as a registrar refuses a wrong key.
	badKey bool
	// onAccount is what ListDomains reports: domain → whether the registrar
	// lets the key edit its DNS (CanSetARecord).
	onAccount map[string]bool
	// buyLost makes Buy charge and then lose the answer, as a timeout after
	// the registrar took the money does; buyRefused makes it refuse outright.
	// Both stay until cleared.
	buyLost    error
	buyRefused error
	// down makes every Buy fail before it reaches the registrar — no charge,
	// no replay, the network gone.
	down error
	// keys is every (domain, idempotency key) Buy was called with, in order;
	// charged is how many of those the registrar actually took money for.
	keys    [][2]string
	charged map[string]bool
}

func (f *fakeRegistrar) ListDomains(context.Context) ([]RegistrarDomain, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	exp := time.Date(2027, 10, 21, 12, 0, 0, 0, time.UTC)
	var out []RegistrarDomain
	for d := range f.onAccount {
		out = append(out, RegistrarDomain{Domain: d, TLD: tldOf(d), ExpiresAt: &exp})
	}
	return out, nil
}

func (f *fakeRegistrar) CanSetARecord(_ context.Context, domain string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.onAccount[domain] {
		return errors.New("fake: dns edit not permitted")
	}
	return nil
}

func newFakeRegistrar() *fakeRegistrar {
	return &fakeRegistrar{
		costByTLD:     map[string]int{"lol": 154, "top": 163, "mom": 150, "autos": 180},
		notRegister:   map[string]bool{},
		withinMonthly: true,
		balanceCents:  846,
		aimed:         map[string]string{},
		onAccount:     map[string]bool{"harbor1234.lol": true, "slate7777.top": true},
	}
}

func (f *fakeRegistrar) Registerable(_ context.Context, tld string) (bool, error) {
	tld = strings.TrimPrefix(strings.ToLower(tld), ".")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notRegister[tld] {
		return false, nil
	}
	_, ok := f.costByTLD[tld]
	return ok, nil
}

func (f *fakeRegistrar) Quote(_ context.Context, domain string) (DomainQuote, error) {
	tld := tldOf(domain)
	f.mu.Lock()
	defer f.mu.Unlock()
	cost, ok := f.costByTLD[tld]
	return DomainQuote{Domain: domain, TLD: tld, Available: ok, CostCents: cost}, nil
}

func (f *fakeRegistrar) DryRun(_ context.Context, _ string, _ int) (DomainPreflight, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return DomainPreflight{
		WouldSucceed:       !f.insufficient,
		SufficientFunds:    !f.insufficient,
		WithinMonthlyLimit: f.withinMonthly,
		BalanceCents:       f.balanceCents,
	}, nil
}

func (f *fakeRegistrar) Buy(_ context.Context, domain string, costCents int, key string) (DomainOrder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, [2]string{domain, key})
	if f.down != nil {
		return DomainOrder{}, f.down
	}
	if f.buyRefused != nil {
		return DomainOrder{}, f.buyRefused
	}
	if f.charged == nil {
		f.charged = map[string]bool{}
	}
	// The key replays: a second Buy with the key of a charge already taken is
	// the first answer again, not a second charge.
	if f.charged[key] {
		return DomainOrder{Domain: domain, OrderID: "ord-replay-" + key, CostCents: costCents}, nil
	}
	f.charged[key] = true
	if f.buyLost != nil {
		return DomainOrder{}, f.buyLost
	}
	f.buys++
	f.bought = append(f.bought, domain)
	return DomainOrder{Domain: domain, OrderID: fmt.Sprintf("ord-%d", f.buys), CostCents: costCents}, nil
}

func (f *fakeRegistrar) SetARecord(_ context.Context, domain, ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aimed[domain] = ip
	return nil
}

func (f *fakeRegistrar) Account(context.Context) (RegistrarAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.badKey {
		return RegistrarAccount{}, errors.New("fake: credentials rejected")
	}
	return RegistrarAccount{BalanceCents: f.balanceCents, LimitCents: 10000, Limited: true, SpentCents: 154}, nil
}

// xhttpBox creates a domainless XHTTP box and returns its name and address.
func (h *harness) xhttpBoxNoDomain(t *testing.T) (name, addr string) {
	t.Helper()
	u := uuid.New()
	name = fmt.Sprintf("x%d", uint64(u[0])<<24|uint64(u[1])<<16|uint64(u[2])<<8|uint64(u[3]))
	// 198.18.0.0/15 (benchmarking range): ~130k addresses, so boxes left by
	// earlier runs in the test database do not collide on one /24.
	addr = fmt.Sprintf("198.%d.%d.%d", 18+int(u[4])%2, u[5], int(u[6])%254+1)
	if err := h.svc.CreateBox(context.Background(), BoxSpec{
		Name: name, Address: addr, Provider: "vultr", Location: "amsterdam", Proto: ProtoVlessXhttp,
	}); err != nil {
		t.Fatalf("create xhttp box: %v", err)
	}
	return name, addr
}

// boxDomain decrypts the sealed domain on a box, empty when it carries none.
func boxDomain(t *testing.T, pool *pgxpool.Pool, box string) string {
	t.Helper()
	var ct []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT domain_ct FROM entry_boxes WHERE name = $1`, box).Scan(&ct); err != nil {
		t.Fatalf("read box domain_ct: %v", err)
	}
	if ct == nil {
		return ""
	}
	d, err := cryptobox.Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt box domain: %v", err)
	}
	return string(d)
}

func TestAcquireDomainFromPool(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0 // no rate-limit wait in tests

	if err := h.svc.SeedDomain(context.Background(), "harbor1234.lol"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	box, addr := h.xhttpBoxNoDomain(t)

	domain, bought, err := h.svc.AcquireDomain(context.Background(), box, addr)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if bought {
		t.Error("claimed a pooled domain but reported bought")
	}
	if domain != "harbor1234.lol" {
		t.Errorf("domain = %q, want the pooled name", domain)
	}
	if reg.buys != 0 {
		t.Errorf("bought %d domains with a free one in the pool", reg.buys)
	}
	if got := boxDomain(t, h.pool, box); got != domain {
		t.Errorf("box domain sealed as %q, want %q", got, domain)
	}
	if reg.aimed[domain] != addr {
		t.Errorf("A record = %q, want %q", reg.aimed[domain], addr)
	}
}

func TestAcquireDomainBuysWhenPoolDry(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0 // no rate-limit wait in tests

	box, addr := h.xhttpBoxNoDomain(t)
	domain, bought, err := h.svc.AcquireDomain(context.Background(), box, addr)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !bought {
		t.Error("pool was empty but acquire did not buy")
	}
	if reg.buys != 1 {
		t.Errorf("buys = %d, want 1", reg.buys)
	}
	if got := boxDomain(t, h.pool, box); got != domain {
		t.Errorf("box domain sealed as %q, want %q", got, domain)
	}
	if reg.aimed[domain] != addr {
		t.Errorf("A record = %q, want %q", reg.aimed[domain], addr)
	}
	// The purchase is journalled, assigned to the box.
	ds, err := h.svc.Domains(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range ds {
		if d.Domain == domain {
			found = true
			if d.Status != domainAssigned || d.Box != box || d.Source != domainPurchased {
				t.Errorf("journal row = %+v, want assigned/%s/purchased", d, box)
			}
			if d.OrderID == "" {
				t.Error("journal row has no order id")
			}
		}
	}
	if !found {
		t.Error("purchase not in the journal")
	}
}

// A TLD dearer than what is left of the day's domain budget is passed over
// for a cheaper one: the TLDs are a preference, not a price list.
func TestAcquireDomainSkipsATLDTheBudgetCannotPayFor(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	h.spendRoom(SpendRegistrar, 200)
	reg := newFakeRegistrar()
	reg.costByTLD = map[string]int{"lol": 500, "top": 163}
	pol := DefaultDomainPolicy()
	pol.TLDs = []string{"lol", "top"}
	h.svc.SetDomainRegistrar(reg, pol)
	h.svc.checkSpacing = 0 // no rate-limit wait in tests

	box, addr := h.xhttpBoxNoDomain(t)
	d, bought, err := h.svc.AcquireDomain(context.Background(), box, addr)
	if err != nil || !bought || tldOf(d) != "top" {
		t.Fatalf("acquired %q (bought %v, %v), want a .top name", d, bought, err)
	}
}

// Nothing the budget has room for: no purchase, and the refusal is about
// money — what short_of_domains is waiting on — not about the names.
func TestAcquireDomainStopsAtTheDomainCaps(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	h.spendRoom(SpendRegistrar, 100) // below any candidate's cost
	reg := newFakeRegistrar()
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0 // no rate-limit wait in tests

	box, addr := h.xhttpBoxNoDomain(t)
	_, _, err := h.svc.AcquireDomain(context.Background(), box, addr)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	if reg.buys != 0 {
		t.Errorf("bought %d domains past the cap", reg.buys)
	}
}

func TestAcquireDomainInsufficientFunds(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	reg := newFakeRegistrar()
	reg.insufficient = true
	h.svc.SetDomainRegistrar(reg, DefaultDomainPolicy())
	h.svc.checkSpacing = 0 // no rate-limit wait in tests

	box, addr := h.xhttpBoxNoDomain(t)
	_, _, err := h.svc.AcquireDomain(context.Background(), box, addr)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
}

func TestAcquireDomainNoRegistrar(t *testing.T) {
	h := newHarness(t)
	allowDomainSpend(h.t, h.pool)
	box, addr := h.xhttpBoxNoDomain(t)
	_, _, err := h.svc.AcquireDomain(context.Background(), box, addr)
	if !errors.Is(err, ErrNoRegistrar) {
		t.Fatalf("err = %v, want ErrNoRegistrar", err)
	}
}

// fakeReplacer records the boxes a domain_blocked death asks to re-provision.
type fakeReplacer struct{ ch chan string }

func (f fakeReplacer) ReplaceDomain(box string) { f.ch <- box }

func TestDomainBlockedDeathTriggersReplacement(t *testing.T) {
	h := newHarness(t)
	rep := fakeReplacer{ch: make(chan string, 1)}
	h.svc.SetDomainReplacer(rep)

	box, _ := h.xhttpBox()
	relay := h.relay(box, KindPersonal)
	if _, err := h.svc.MarkDead(context.Background(), relay, DeathDomainBlocked); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	select {
	case got := <-rep.ch:
		if got != box {
			t.Errorf("replace asked for %q, want %q", got, box)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("domain_blocked death did not trigger a replacement")
	}
}

func TestNonDomainBlockedDeathDoesNotReplace(t *testing.T) {
	h := newHarness(t)
	rep := fakeReplacer{ch: make(chan string, 1)}
	h.svc.SetDomainReplacer(rep)

	box, _ := h.xhttpBox()
	relay := h.relay(box, KindPersonal)
	if _, err := h.svc.MarkDead(context.Background(), relay, DeathIPBanned); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	select {
	case got := <-rep.ch:
		t.Errorf("ip_banned death triggered a replacement of %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestDomainBlockedWithSurvivorDoesNotReplace(t *testing.T) {
	h := newHarness(t)
	rep := fakeReplacer{ch: make(chan string, 1)}
	h.svc.SetDomainReplacer(rep)

	// A box with two relays: an XHTTP box mints one at a time, so use a plain
	// box to exercise the survivor guard (it is proto-independent).
	box, _ := h.box()
	dying := h.relay(box, KindPersonal)
	_ = h.relay(box, KindPersonal) // a second relay survives the block
	if _, err := h.svc.MarkDead(context.Background(), dying, DeathDomainBlocked); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	select {
	case got := <-rep.ch:
		t.Errorf("replaced %q while a relay still survives on the box", got)
	case <-time.After(300 * time.Millisecond):
	}
}
