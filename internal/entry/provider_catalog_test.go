package entry

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestDraftProviderCatalogDoesNotSaveOrEnableAccount(t *testing.T) {
	h := newHarness(t)
	host := newFakeHoster(t)
	h.svc.SetProviderConnector(&fakeConnector{host: host})
	ctx := context.Background()
	cat, err := h.svc.PreviewProviderCatalog(ctx, fakeHostKind, goodHostCreds)
	if err != nil || cat.Kind != fakeHostKind || len(cat.Regions) != 2 || len(cat.Plans) != 3 {
		t.Fatalf("draft catalog = %+v, %v", cat, err)
	}
	if _, exists, err := h.svc.repo.provider(ctx, fakeHostKind); err != nil || exists {
		t.Fatalf("preview persisted an account: exists %v, %v", exists, err)
	}
	if _, err := h.svc.hoster(); !errors.Is(err, ErrNoHoster) || host.orderCount() != 0 {
		t.Fatalf("preview enabled or charged hoster: %v, orders %d", err, host.orderCount())
	}
	host.badKey = true
	if _, err := h.svc.PreviewProviderCatalog(ctx, fakeHostKind, goodHostCreds); !errors.Is(err, ErrProviderCheck) {
		t.Fatalf("bad key = %v, want ErrProviderCheck", err)
	}
}

func TestProviderCatalogUsesDisabledAccountsOwnKeyWithoutStepUp(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveProvider(ctx, fakeHostKind, ProviderUpdate{Enabled: false}, ""); err != nil {
		t.Fatal(err)
	}
	touched := false
	do := h.stepUpMux(&touched)
	path := "/api/v1/admin/entry/providers/" + fakeHostKind + "/catalog"
	code, body := do("POST", path, map[string]any{})
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"regions"`)) || !bytes.Contains(body, []byte(`"monthlyCents":500`)) {
		t.Fatalf("disabled account catalog: %d %s", code, body)
	}
	for _, value := range goodHostCreds {
		if bytes.Contains(body, []byte(value)) {
			t.Fatal("catalog response disclosed credentials")
		}
	}
	if _, err := h.svc.hoster(); !errors.Is(err, ErrNoHoster) {
		t.Fatalf("preview enabled disabled account: %v", err)
	}
	if code, _ := do("POST", path, map[string]any{"credentials": map[string]string{}}); code != http.StatusBadRequest {
		t.Fatalf("incomplete key: %d", code)
	}
	if code, _ := do("POST", path, map[string]any{"unexpected": true}); code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", code)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/providers/nope/catalog", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", code)
	}
	if code, _ := do("POST", "/api/v1/admin/entry/providers/"+fakeKind+"/catalog", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("registrar catalog: %d", code)
	}
	h.host.badKey = true
	if code, _ := do("POST", path, map[string]any{"credentials": goodHostCreds}); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad draft key: %d", code)
	}
}

func TestProviderDefaultsMustMatchThatHostersCatalog(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	for _, u := range []ProviderUpdate{
		{Enabled: true},
		{Enabled: true, DefaultRegion: "invented", DefaultPlan: "small"},
		{Enabled: true, DefaultRegion: "r1", DefaultPlan: "invented"},
		{Enabled: true, DefaultRegion: "r1", DefaultPlan: "r2only"},
	} {
		if _, err := h.svc.SaveProvider(ctx, fakeHostKind, u, ""); !errors.Is(err, ErrRejected) {
			t.Fatalf("defaults %+v: %v", u, err)
		}
	}
	v, err := h.svc.providerView(ctx, fakeHostKind)
	if err != nil || !v.Active || v.DefaultRegion != "r1" || v.DefaultPlan != "small" {
		t.Fatalf("rejected defaults changed account: %+v, %v", v, err)
	}
	v, err = h.svc.SaveProvider(ctx, fakeHostKind, ProviderUpdate{Enabled: true, DefaultRegion: "r2", DefaultPlan: "r2only"}, "")
	if err != nil || v.DefaultPlan != "r2only" {
		t.Fatalf("valid selection: %+v, %v", v, err)
	}
	h.host.badKey = true
	if _, err := h.svc.SaveProvider(ctx, fakeHostKind, ProviderUpdate{Enabled: false}, ""); err != nil {
		t.Fatalf("cannot disable when provider is unavailable: %v", err)
	}
	if h.host.orderCount() != 0 {
		t.Fatal("catalog validation ordered a machine")
	}
}

func TestMissingRegistrarWaitsBeforeSpendingAndStrikes(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	if _, err := h.prov.orderAndRecord(ctx, OrderSpec{Proto: ProtoVlessXhttp}); !errors.Is(err, ErrNoRegistrar) {
		t.Fatalf("XHTTP without domains: %v", err)
	}
	if h.host.orderCount() != 0 {
		t.Fatal("rented a machine that cannot get a domain")
	}
	m := NewMechanic(h.svc, h.prov)
	for _, inc := range []Incident{{Pool: DomainPool, Cause: CauseShortOfDomains}, {Pool: FleetPool(ProtoVlessXhttp, ""), Cause: CauseShortOfFleet}} {
		why, blocked, err := m.poolRails(ctx, inc, PlaybookForCause(inc.Cause).Name, mustPolicy(t, h.svc))
		if err != nil || !blocked || why != "" {
			t.Fatalf("missing prerequisite must wait without handoff: %v, %v, %q", blocked, err, why)
		}
	}
}
