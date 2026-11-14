package fx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const feed = `{"Valute":{"USD":{"Nominal":1,"Value":82.0},"EUR":{"Nominal":1,"Value":91.0}}}`

func TestARoubleAndAEuroPriceBecomeUSCents(t *testing.T) {
	r := Fixed(map[string]float64{USD: 82, EUR: 91})
	ctx := context.Background()
	for _, c := range []struct {
		amount float64
		cur    string
		want   int
	}{
		{17, "USD", 1700},
		{469, "RUB", 572},  // 469 / 82
		{469, "₽", 572},    // the rouble sign
		{1.49, "eur", 165}, // 1.49 * 91 / 82
		{5.99, "EUR", 665},
	} {
		got, err := r.USDCents(ctx, c.amount, c.cur)
		if err != nil || got != c.want {
			t.Errorf("%v %s = %d, %v; want %d", c.amount, c.cur, got, err, c.want)
		}
	}
	if _, err := r.USDCents(ctx, 1, "GBP"); err == nil {
		t.Error("an unknown currency converted")
	}
}

func TestTheRatesAreReadOnceAndAFailureKeepsTheLastGoodOnes(t *testing.T) {
	var hits atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()

	now := time.Unix(1_800_000_000, 0)
	r := New(srv.URL)
	r.now = func() time.Time { return now }
	ctx := context.Background()

	for range 3 {
		if got, err := r.USDCents(ctx, 820, RUB); err != nil || got != 1000 {
			t.Fatalf("820 RUB = %d, %v; want 1000", got, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("read the feed %d times for three prices, want once", hits.Load())
	}

	down.Store(true)
	now = now.Add(refresh + time.Minute)
	if got, err := r.USDCents(ctx, 820, RUB); err != nil || got != 1000 {
		t.Fatalf("with the feed down: %d, %v; want the last good rate", got, err)
	}
}

func TestWithNoRatesAForeignPriceIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	r := New(srv.URL)
	if _, err := r.USDCents(context.Background(), 469, RUB); err == nil {
		t.Fatal("converted with no rate ever read")
	}
	if got, err := r.USDCents(context.Background(), 17, USD); err != nil || got != 1700 {
		t.Fatalf("a dollar price needs no rate: %d, %v", got, err)
	}
}
