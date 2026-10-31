package subscription

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// unlimited is a plan of a month and no traffic limit.
func unlimited(t *testing.T, s *Service) string {
	t.Helper()
	if err := s.SetPlan(context.Background(), Plan{ID: "t-unlimited", Days: 30}); err != nil {
		t.Fatal(err)
	}
	return "t-unlimited"
}

// A subscription stops on the relays when its period ends or its traffic
// runs out, and a renewal brings it back with the counter started afresh.
func TestASubscriptionLivesWhileItHasTimeAndTraffic(t *testing.T) {
	pool := livePool(t)
	s := New(pool, &fakeBatch{}, nil)
	ctx := context.Background()
	if err := s.SetPlan(ctx, Plan{ID: "t-small", Days: 30, TrafficBytes: 1000, PriceCents: 199}); err != nil {
		t.Fatal(err)
	}
	name := "t-" + uuid.NewString()[:8]
	if _, err := s.Add(ctx, name, "t-small"); err != nil {
		t.Fatal(err)
	}
	k := keyOf(t, pool, name)
	relay, _ := grantRelay(t, pool, entry.ProtoVlessXhttp, name)
	live := func() bool {
		t.Helper()
		keys, err := s.KeysOf(ctx, relay, entry.ProtoVlessXhttp)
		if err != nil {
			t.Fatal(err)
		}
		return len(keys) == 1
	}

	terms, err := s.Status(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if terms.Plan != "t-small" || terms.TrafficBytes != 1000 || time.Until(terms.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("terms %+v", terms)
	}
	if !live() {
		t.Fatal("a fresh subscription is not on its relay")
	}

	boot := strings.Repeat("c", 32)
	if err := s.RecordUsage(ctx, relay, boot, map[string]entry.KeyUsage{k.vlessID: {Up: 400, Down: 600}}); err != nil {
		t.Fatal(err)
	}
	if live() {
		t.Fatal("a subscription out of traffic is still on its relay")
	}

	if err := s.Renew(ctx, name, "t-small"); err != nil {
		t.Fatal(err)
	}
	renewed, _ := s.Status(ctx, name)
	if !live() || renewed.Up+renewed.Down != 0 {
		t.Fatalf("after renewal: live %v, terms %+v", live(), renewed)
	}
	// Time paid for is not lost: the new period runs on from the old end.
	if d := renewed.ExpiresAt.Sub(terms.ExpiresAt); d < 29*24*time.Hour {
		t.Fatalf("renewal added %v", d)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE hub_subscriptions SET expires_at = now() - interval '1 second'
		WHERE account_id = (SELECT id FROM hub_accounts WHERE name = $1)`, name); err != nil {
		t.Fatal(err)
	}
	if live() {
		t.Fatal("an expired subscription is still on its relay")
	}
}

// The link answers with its terms, also after the period ended, so the client
// can say why it stopped.
func TestTheLinkCarriesItsTerms(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	s := New(pool, &fakeBatch{envelopes: [][]byte{signed(t, signer, xhttpRecord())}}, []entry.Key{signer.Key()})
	ctx := context.Background()
	if err := s.SetPlan(ctx, Plan{ID: "t-gb", Days: 30, TrafficBytes: 5 << 30}); err != nil {
		t.Fatal(err)
	}
	name := "t-" + uuid.NewString()[:8]
	token, err := s.Add(ctx, name, "t-gb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE hub_subscriptions SET expires_at = now() - interval '1 day'
		WHERE account_id = (SELECT id FROM hub_accounts WHERE name = $1)`, name); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sub/{token}", s.Handler())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/sub/"+token, nil))
	info := rec.Header().Get("Subscription-Userinfo")
	if rec.Code != http.StatusOK || !strings.Contains(info, "total=5368709120") || !strings.Contains(info, "expire=") {
		t.Fatalf("%d %q", rec.Code, info)
	}
}

// Only plans on sale are sold.
func TestARetiredPlanIsNotSold(t *testing.T) {
	pool := livePool(t)
	s := New(pool, &fakeBatch{}, nil)
	ctx := context.Background()
	if err := s.SetPlan(ctx, Plan{ID: "t-old", Days: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetirePlan(ctx, "t-old"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "t-"+uuid.NewString()[:8], "t-old"); err != ErrUnknownPlan {
		t.Fatalf("sold on a retired plan: %v", err)
	}
}
