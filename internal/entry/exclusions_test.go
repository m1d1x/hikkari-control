package entry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/config"
)

func TestExclusionsCoverSubdomainsAndEveryEntrance(t *testing.T) {
	h, c := fakeProviders(t)
	ctx := context.Background()
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds,
	}, ""); err != nil {
		t.Fatal(err)
	}
	c.reg.onAccount = map[string]bool{"oldbrand.lol": true, "mail.oldbrand.lol": true, "free5555.lol": true}

	if err := h.svc.ExcludeDomain(ctx, " OldBrand.LOL. ", "legacy brand", ""); err != nil {
		t.Fatalf("exclude: %v", err)
	}
	// Idempotent, the first note stays.
	if err := h.svc.ExcludeDomain(ctx, "oldbrand.lol", "second", ""); err != nil {
		t.Fatalf("exclude twice: %v", err)
	}
	if err := h.svc.ExcludeDomain(ctx, "not a domain", "", ""); !errors.Is(err, ErrRejected) {
		t.Errorf("junk: err = %v, want ErrRejected", err)
	}

	// Every way a name enters relay use refuses it, subdomains included.
	for _, d := range []string{"oldbrand.lol", "mail.oldbrand.lol"} {
		if err := h.svc.SeedDomain(ctx, d); !errors.Is(err, ErrRejected) {
			t.Errorf("seed %s: err = %v, want ErrRejected", d, err)
		}
	}
	if _, err := h.svc.QuoteDomain(ctx, "oldbrand.lol"); !errors.Is(err, ErrRejected) {
		t.Errorf("quote: err = %v, want ErrRejected", err)
	}
	if _, err := h.svc.BuyDomain(ctx, "oldbrand.lol"); !errors.Is(err, ErrRejected) {
		t.Errorf("buy: err = %v, want ErrRejected", err)
	}
	box, addr := h.xhttpBoxNoDomain(t)
	if err := h.svc.CreateBox(ctx, BoxSpec{Name: box + "x", Address: addr + "0", Provider: "vultr",
		Location: "amsterdam", Proto: ProtoVlessXhttp, Domain: "oldbrand.lol"}); !errors.Is(err, ErrRejected) {
		t.Errorf("box with an excluded domain: err = %v, want ErrRejected", err)
	}

	// The account view marks it, and the brand stays brand.
	brand := strings.ToLower(config.Default().Base)
	c.reg.onAccount[brand] = true
	ds, err := h.svc.AccountDomains(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		want := ""
		switch d.Domain {
		case "oldbrand.lol", "mail.oldbrand.lol":
			want = blockedExcluded
		case brand:
			want = blockedBrand
		}
		if d.Blocked != want {
			t.Errorf("%s blocked = %q, want %q", d.Domain, d.Blocked, want)
		}
	}

	// The list: the brand first and built in, then the owner's with its note.
	xs, err := h.svc.Exclusions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(xs) != 2 || !xs[0].BuiltIn || xs[0].Domain != brand || xs[1].Domain != "oldbrand.lol" || xs[1].Note != "legacy brand" {
		t.Fatalf("exclusions = %+v", xs)
	}
	if err := h.svc.UnexcludeDomain(ctx, brand); !errors.Is(err, ErrRejected) {
		t.Errorf("unexclude the brand: err = %v, want ErrRejected", err)
	}

	// Taken off, it is usable again.
	if err := h.svc.UnexcludeDomain(ctx, "oldbrand.lol"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.UnexcludeDomain(ctx, "oldbrand.lol"); !errors.Is(err, ErrNoSuchExclusion) {
		t.Errorf("unexclude twice: err = %v, want ErrNoSuchExclusion", err)
	}
	if err := h.svc.SeedDomain(ctx, "oldbrand.lol"); err != nil {
		t.Errorf("seed after unexclude: %v", err)
	}
}

func TestPoolNeverDrawsAnExcludedName(t *testing.T) {
	h, c := fakeProviders(t)
	allowDomainSpend(h.t, h.pool)
	ctx := context.Background()
	if _, err := h.svc.SaveProvider(ctx, fakeKind, ProviderUpdate{
		Enabled: true, Credentials: goodCreds,
	}, ""); err != nil {
		t.Fatal(err)
	}
	c.reg.onAccount = map[string]bool{"personal7777.lol": true}
	// Pooled first, excluded after: the owner changed their mind.
	if err := h.svc.SeedDomain(ctx, "personal7777.lol"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ExcludeDomain(ctx, "personal7777.lol", "mine", ""); err != nil {
		t.Fatal(err)
	}
	xs, err := h.svc.Exclusions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if xs[len(xs)-1].HubStatus != domainOwned {
		t.Errorf("exclusion of a pooled name shows hub status %q, want owned", xs[len(xs)-1].HubStatus)
	}

	box, addr := h.xhttpBoxNoDomain(t)
	got, bought, err := h.svc.AcquireDomain(ctx, box, addr)
	if err != nil {
		t.Fatal(err)
	}
	if got == "personal7777.lol" || !bought {
		t.Fatalf("acquire = %q bought=%v, want a purchase past the excluded pooled name", got, bought)
	}
}
