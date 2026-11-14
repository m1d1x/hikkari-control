package entry

import (
	"context"
	"errors"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/fx"
)

// What a machine costs follows its hoster and the rate without a human: a box
// the hub ordered takes its plan's price from the hoster's list, a box rented
// by hand keeps the owner's price, and both are re-converted at the day's rate.
// A hoster that cannot be read leaves its boxes as they were (owner, 14.11:
// the console showed the fleet at a third of its cost).
func TestBoxPricesFollowTheHosterAndTheRate(t *testing.T) {
	h := newHostHarness(t)
	ctx := context.Background()
	// €1 = 91 ₽, $1 = 82 ₽.
	h.svc.SetRates(fx.Fixed(map[string]float64{fx.USD: 82, fx.EUR: 91}))

	byHand, _ := h.harness.box()
	h.t.Cleanup(func() { h.retire(byHand) })
	ordered, err := h.prov.orderAndRecord(ctx, OrderSpec{})
	if err != nil {
		t.Fatal(err)
	}
	h.t.Cleanup(func() { h.retire(ordered) })

	cents := func(box string) int {
		t.Helper()
		var c *int
		if err := h.pool.QueryRow(ctx, `SELECT monthly_cents FROM entry_boxes WHERE name = $1`, box).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c == nil {
			return -1
		}
		return *c
	}

	// The owner prices the hand-rented box in roubles: 469 / 82 = $5.72.
	if got, err := h.svc.SetBoxPrice(ctx, byHand, 469, "rub"); err != nil || got != 572 {
		t.Fatalf("SetBoxPrice = %d, %v; want 572", got, err)
	}

	// The hoster now bills the ordered box's plan €1.49: 1.49 * 91 / 82 = $1.65.
	h.host.mu.Lock()
	h.host.plans[0].Price, h.host.plans[0].Currency = 1.49, fx.EUR
	h.host.mu.Unlock()
	res, err := h.svc.SyncBoxPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := cents(ordered); got != 165 {
		t.Errorf("ordered box = %d cents after the pass, want 165 (changed %v, failed %v)", got, res.Changed, res.Failed)
	}
	if got := cents(byHand); got != 572 {
		t.Errorf("hand-rented box = %d, want the owner's 572 kept", got)
	}

	// The rouble falls to 100 a dollar: both follow on the next pass.
	h.svc.SetRates(fx.Fixed(map[string]float64{fx.USD: 100, fx.EUR: 91}))
	if _, err := h.svc.SyncBoxPrices(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cents(byHand); got != 469 {
		t.Errorf("hand-rented box at 100 ₽/$ = %d, want 469", got)
	}
	if got := cents(ordered); got != 136 {
		t.Errorf("ordered box at 100 ₽/$ = %d, want 136", got)
	}

	// The hoster refuses the key: reported, and the box keeps its price.
	h.host.mu.Lock()
	h.host.plansErr = errors.New("credentials rejected")
	h.host.mu.Unlock()
	res, err = h.svc.SyncBoxPrices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) == 0 {
		t.Error("an unread hoster was not reported")
	}
	if got := cents(ordered); got != 136 {
		t.Errorf("ordered box with its hoster unread = %d, want 136 kept", got)
	}

	if _, err := h.svc.SetBoxPrice(ctx, "no-such-box", 1, "USD"); !errors.Is(err, ErrNoSuchBox) {
		t.Errorf("unknown box: %v", err)
	}
	if _, err := h.svc.SetBoxPrice(ctx, byHand, 1, "GBP"); !errors.Is(err, ErrRejected) {
		t.Errorf("unknown currency: %v", err)
	}
}
