package entry

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/config"
)

// fakeConnector is the provider catalog of the tests: one registrar kind whose
// clients are the shared fakeRegistrar, built only from the right key.
type fakeConnector struct {
	reg  *fakeRegistrar
	host *fakeHoster
	keys []map[string]string // every set of credentials a client was built from
}

const fakeKind = "fakereg"

func (c *fakeConnector) Kinds() []ProviderKind {
	return []ProviderKind{{
		Kind: fakeKind, Title: "Fake", Role: RoleRegistrar,
		Fields: []CredentialField{
			{Name: "api_key", Label: "api key"},
			{Name: "secret", Label: "secret", Secret: true},
		},
	}, {
		Kind: fakeHostKind, Title: "FakeHost", Role: RoleHosting,
		Fields: []CredentialField{
			{Name: "api_key", Label: "api key", Secret: true},
		},
	}}
}

// Hosting builds the shared fakeHoster; a harness with no hoster in it has
// none, which is the state of a hub whose machines were all rented by hand.
func (c *fakeConnector) Hosting(kind string, creds map[string]string) (BoxProvider, error) {
	if kind != fakeHostKind || c.host == nil {
		return nil, ErrUnknownProvider
	}
	c.keys = append(c.keys, creds)
	return c.host, nil
}

func (c *fakeConnector) Registrar(kind string, creds map[string]string) (DomainRegistrar, error) {
	if kind != fakeKind {
		return nil, ErrUnknownProvider
	}
	c.keys = append(c.keys, creds)
	if creds["secret"] != "right-secret" {
		return &fakeRegistrar{badKey: true}, nil
	}
	return c.reg, nil
}

// fakeProviders builds a harness whose hub knows the fake registrar kind and
// has no account saved yet.
func fakeProviders(t *testing.T) (*harness, *fakeConnector) {
	h := newHarness(t)
	c := &fakeConnector{reg: newFakeRegistrar()}
	h.svc.SetProviderConnector(c)
	h.svc.checkSpacing = 0
	if err := h.svc.LoadProviders(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h, c
}

var goodCreds = map[string]string{"api_key": "pk1_fakefakefake", "secret": "right-secret"}

func TestSaveProviderChecksSealsAndApplies(t *testing.T) {
	h, c := fakeProviders(t)
	allowDomainSpend(h.t, h.pool)
	ctx := context.Background()

	// Before: pool-only.
	box, addr := h.xhttpBoxNoDomain(t)
	if _, _, err := h.svc.AcquireDomain(ctx, box, addr); !errors.Is(err, ErrNoRegistrar) {
		t.Fatalf("acquire before any account: err = %v, want ErrNoRegistrar", err)
	}

	v, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds, TLDs: []string{".LOL", "top", "lol"},
	}, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !v.Configured || !v.Enabled || !v.Active {
		t.Errorf("view = %+v, want configured, enabled, active", v)
	}
	if v.CredsHint != "pk1_…fake" {
		t.Errorf("hint = %q", v.CredsHint)
	}
	if len(v.TLDs) != 2 || v.TLDs[0] != "lol" || v.TLDs[1] != "top" {
		t.Errorf("tlds = %v, want [lol top]", v.TLDs)
	}
	if v.Check == nil || !v.Check.OK || v.Check.BalanceCents == nil || *v.Check.BalanceCents != 846 {
		t.Errorf("check = %+v, want ok with balance 846", v.Check)
	}

	// The key is sealed: neither the key nor the secret is in the row.
	var ct []byte
	if err := h.pool.QueryRow(ctx, `SELECT creds_ct FROM entry_providers WHERE kind = $1`, fakeKind).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("right-secret")) || bytes.Contains(ct, []byte("pk1_fakefake")) {
		t.Error("credentials stored in the clear")
	}

	// Applied at once: the hub now buys through it, with its TLDs.
	if _, bought, err := h.svc.AcquireDomain(ctx, box, addr); err != nil || !bought {
		t.Fatalf("acquire after save: bought=%v err=%v", bought, err)
	}
	if got := h.svc.currentBuyer().policy; len(got.TLDs) != 2 || got.TLDs[0] != "lol" {
		t.Errorf("policy TLDs = %v, want [lol top]", got.TLDs)
	}

	// A restarted hub reads the same account back from the database.
	svc2, err := NewService(h.pool, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	svc2.SetProviderConnector(c)
	if err := svc2.LoadProviders(ctx); err != nil {
		t.Fatal(err)
	}
	if svc2.currentBuyer().reg != c.reg {
		t.Error("restarted hub did not load the saved registrar")
	}
	last := c.keys[len(c.keys)-1]
	if last["secret"] != "right-secret" || last["api_key"] != goodCreds["api_key"] {
		t.Errorf("restarted hub built its client from %v", last)
	}
}

func TestSaveProviderBadKeyKeepsNothing(t *testing.T) {
	h, _ := fakeProviders(t)
	ctx := context.Background()
	_, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: map[string]string{"api_key": "pk1_fakefakefake", "secret": "wrong"},
	}, "")
	if !errors.Is(err, ErrProviderCheck) {
		t.Fatalf("err = %v, want ErrProviderCheck", err)
	}
	var n int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM entry_providers`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows saved after a refused key", n)
	}
	if h.svc.currentBuyer().reg != nil {
		t.Error("a refused key became the registrar")
	}
}

func TestSaveProviderRefusals(t *testing.T) {
	h, _ := fakeProviders(t)
	ctx := context.Background()
	cases := []struct {
		name string
		kind string
		u    ProviderUpdate
		want error
	}{
		{"enable with no key", fakeKind, ProviderUpdate{Enabled: true}, ErrRejected},
		{"half a key", fakeKind, ProviderUpdate{Credentials: map[string]string{"api_key": "pk1_x"}}, ErrRejected},
		{"bad tld", fakeKind, ProviderUpdate{TLDs: []string{"l/ol"}}, ErrRejected},
		{"unknown kind", "nope", ProviderUpdate{}, ErrUnknownProvider},
	}
	for _, tc := range cases {
		if _, err := h.svc.SaveProvider(ctx, tc.kind, tc.u, ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestSaveProviderKeepsKeyAndSwitchesOff(t *testing.T) {
	h, c := fakeProviders(t)
	ctx := context.Background()
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds,
	}, ""); err != nil {
		t.Fatal(err)
	}
	// Saving without credentials keeps the saved key.
	v, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{Enabled: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Configured || !v.Active || h.svc.currentBuyer().reg != c.reg {
		t.Errorf("a save without credentials lost the key: %+v", v)
	}
	// Switching off makes the hub pool-only again, key still saved.
	v, err = h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{Enabled: false}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Active || !v.Configured || h.svc.currentBuyer().reg != nil {
		t.Errorf("switched off but still active: %+v", v)
	}
}

func TestCheckProviderRecordsFailure(t *testing.T) {
	h, c := fakeProviders(t)
	ctx := context.Background()
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds,
	}, ""); err != nil {
		t.Fatal(err)
	}
	// The key is revoked at the registrar after it was saved.
	c.reg.badKey = true
	v, err := h.svc.CheckProvider(ctx, fakeKind)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if v.Check == nil || v.Check.OK || v.Check.Error == "" {
		t.Fatalf("check = %+v, want a recorded failure", v.Check)
	}
	// The last known balance survives a failed check.
	if v.Check.BalanceCents == nil || *v.Check.BalanceCents != 846 {
		t.Errorf("balance after failed check = %v, want the last known 846", v.Check.BalanceCents)
	}
}

func TestBuyDomainIntoPool(t *testing.T) {
	h, c := fakeProviders(t)
	allowDomainSpend(h.t, h.pool)
	ctx := context.Background()
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds,
	}, ""); err != nil {
		t.Fatal(err)
	}

	h.spendRoom(SpendRegistrar, 300)
	co, err := h.svc.CheckoutDomain(ctx, "Cedar4821.LOL.")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if co.Domain != "cedar4821.lol" || !co.Available || co.CostCents != 154 || !co.WithinHubCaps || !co.WouldSucceed {
		t.Errorf("checkout = %+v", co)
	}
	if c.reg.buys != 0 {
		t.Fatal("checkout spent money")
	}

	d, err := h.svc.BuyDomain(ctx, "cedar4821.lol")
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	if d.Status != domainOwned || d.Box != "" || d.Source != domainPurchased || d.OrderID == "" {
		t.Errorf("bought row = %+v, want owned, free, purchased, with an order", d)
	}
	if _, aimed := c.reg.aimed["cedar4821.lol"]; aimed {
		t.Error("a pool purchase got an A record before any box")
	}

	// The next XHTTP box draws it from the pool, no second purchase.
	box, addr := h.xhttpBoxNoDomain(t)
	got, bought, err := h.svc.AcquireDomain(ctx, box, addr)
	if err != nil || bought || got != "cedar4821.lol" {
		t.Fatalf("acquire = %q bought=%v err=%v, want the pooled name", got, bought, err)
	}

	// 154 spent of the 300 of room: a second 154 breaks the day.
	before := co.Spend.DayCents
	if _, err := h.svc.BuyDomain(ctx, "maple1000.lol"); !errors.Is(err, ErrBudget) {
		t.Errorf("second buy err = %v, want ErrBudget", err)
	}
	spend, err := h.svc.DomainSpendNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if spend.DayCents != before+154 || spend.Registrar != fakeKind {
		t.Errorf("spend = %+v, want the day up by 154", spend)
	}
}

func TestBuyDomainRefusals(t *testing.T) {
	h, c := fakeProviders(t)
	ctx := context.Background()
	if _, err := h.svc.BuyDomain(ctx, "cedar4821.lol"); !errors.Is(err, ErrNoRegistrar) {
		t.Errorf("no account: err = %v, want ErrNoRegistrar", err)
	}
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds,
	}, ""); err != nil {
		t.Fatal(err)
	}
	h.spendRoom(SpendRegistrar, 160)
	if _, err := h.svc.BuyDomain(ctx, "cedar4821.autos"); !errors.Is(err, ErrBudget) {
		t.Errorf("over what is left of the day (180 > 160): err = %v, want ErrBudget", err)
	}
	if _, err := h.svc.BuyDomain(ctx, "cedar4821.xyz"); !errors.Is(err, ErrDomainUnavailable) {
		t.Errorf("unsold tld: err = %v, want ErrDomainUnavailable", err)
	}
	if _, err := h.svc.BuyDomain(ctx, "a.b.lol"); !errors.Is(err, ErrRejected) {
		t.Errorf("subdomain: err = %v, want ErrRejected", err)
	}
	if c.reg.buys != 0 {
		t.Errorf("%d purchases went through refusals", c.reg.buys)
	}
}

func TestAccountDomainsAgainstTheHub(t *testing.T) {
	h, c := fakeProviders(t)
	ctx := context.Background()
	if _, err := h.svc.AccountDomains(ctx); !errors.Is(err, ErrNoRegistrar) {
		t.Fatalf("no account: err = %v, want ErrNoRegistrar", err)
	}
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds,
	}, ""); err != nil {
		t.Fatal(err)
	}
	brand := strings.ToLower(config.Default().Base)
	c.reg.onAccount = map[string]bool{
		"harbor1234.lol": true,  // free to seed
		"slate7777.top":  true,  // already in the pool
		"closed1111.lol": false, // the registrar refuses DNS edits for it
		brand:            true,  // the brand's own
	}
	if err := h.svc.SeedDomain(ctx, "slate7777.top"); err != nil {
		t.Fatal(err)
	}

	ds, err := h.svc.AccountDomains(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AccountDomain{}
	for _, d := range ds {
		got[d.Domain] = d
	}
	if d := got["harbor1234.lol"]; d.Blocked != "" || d.HubStatus != "" || d.ExpiresAt == nil {
		t.Errorf("free domain = %+v, want seedable and not in the hub", d)
	}
	if d := got["slate7777.top"]; d.HubStatus != domainOwned {
		t.Errorf("seeded domain = %+v, want hub status owned", d)
	}
	// DNS rights are not judged from the list: the seed asks the registrar.
	if d := got["closed1111.lol"]; d.Blocked != "" {
		t.Errorf("closed domain = %+v, want unjudged in the list", d)
	}
	if d := got[brand]; d.Blocked != blockedBrand {
		t.Errorf("brand domain = %+v, want blocked brand", d)
	}

	// Seeding checks the account too: not on it, or DNS edits refused, is refused.
	if err := h.svc.SeedDomain(ctx, "closed1111.lol"); !errors.Is(err, ErrRejected) {
		t.Errorf("seed with DNS edits refused: err = %v, want ErrRejected", err)
	}
	if err := h.svc.SeedDomain(ctx, "nowhere4242.lol"); !errors.Is(err, ErrRejected) {
		t.Errorf("seed of a name not on the account: err = %v, want ErrRejected", err)
	}
	if err := h.svc.SeedDomain(ctx, "harbor1234.lol"); err != nil {
		t.Errorf("seed of a free account domain: %v", err)
	}
}

func TestSuggestDomainsUnderCap(t *testing.T) {
	h, _ := fakeProviders(t)
	ctx := context.Background()
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds, TLDs: []string{"lol", "top"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	offers, err := h.svc.SuggestDomains(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 2 {
		t.Fatalf("offers = %+v, want one per tld", offers)
	}
	for _, o := range offers {
		if !o.Available || o.OverCap || (o.TLD != "lol" && o.TLD != "top") {
			t.Errorf("offer %+v", o)
		}
	}
}

// The hub re-reads every saved account by itself, switched on or not: the
// money on the console is the hub's reading, and nobody presses "check" for
// the autopilot. Found on the stand 25.10: the balance stayed whatever the
// save had read, with no way to refresh it but a button.
func TestTheHubRechecksSavedAccountsByItself(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	h.host.mu.Lock()
	h.host.balance = 366
	h.host.mu.Unlock()

	h.svc.checkProviders(ctx)

	v, err := h.svc.providerView(ctx, fakeHostKind)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if v.Check == nil || v.Check.BalanceCents == nil || *v.Check.BalanceCents != 366 {
		t.Fatalf("check after the pass = %+v, want a balance of 366", v.Check)
	}
}
