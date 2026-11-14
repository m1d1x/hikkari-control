package entry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/fx"
)

// What a machine costs, kept true without a human (owner, 14.11: the console
// showed the fleet at a third of its cost because nothing kept the prices).
//
// A box holds its price the way the hoster bills it (price_amount,
// price_currency) and the same in US cents (monthly_cents), the unit every
// sum in the hub is in. Two things move the cents:
//
//   - a hoster with an API: its plan list is the truth, so every pass reads the
//     plan a box runs on and takes its price;
//   - the rate: a box billed in roubles or euros is converted at today's Bank
//     of Russia rate on every pass, so a month in the console is the month the
//     bills will add up to.
//
// A hoster with no API (U1, Aeza, eurodir) gets its price from the owner once,
// [Service.SetBoxPrice]; the rate keeps it current from there.

// SetRates sets the conversion the price passes use; nil means the bank's
// live feed.
func (s *Service) SetRates(r *fx.Rates) { s.rates = r }

func (s *Service) fxRates() *fx.Rates {
	if s.rates != nil {
		return s.rates
	}
	return fx.Default()
}

// PriceSync is what one pass did.
type PriceSync struct {
	// Changed is every box whose cents moved, "ams3 210→252".
	Changed []string
	// Failed is every hoster whose plans could not be read, with why. Its
	// boxes kept the price they had.
	Failed []string
}

// pricedBox is a live box as the price pass reads it.
type pricedBox struct {
	name, provider, region, plan string
	amount                       *float64
	currency                     *string
	cents                        *int
}

// SyncBoxPrices refreshes every live box's price from its hoster's plans and
// re-converts every price at today's rate. A hoster that cannot be read is
// reported and skipped, never zeroed: an old price is closer to the bill than
// none.
func (s *Service) SyncBoxPrices(ctx context.Context) (PriceSync, error) {
	var out PriceSync
	rows, err := s.pool.Query(ctx, `
		SELECT name, provider, coalesce(provider_region, ''), coalesce(plan, ''),
		       price_amount::float8, price_currency, monthly_cents
		FROM entry_boxes WHERE retired_at IS NULL ORDER BY name
	`)
	if err != nil {
		return out, fmt.Errorf("entry: boxes to price: %w", err)
	}
	var boxes []pricedBox
	for rows.Next() {
		var b pricedBox
		if err := rows.Scan(&b.name, &b.provider, &b.region, &b.plan, &b.amount, &b.currency, &b.cents); err != nil {
			rows.Close()
			return out, err
		}
		boxes = append(boxes, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Plans, once per hoster and region.
	type place struct{ kind, region string }
	plans := map[place]map[string]BoxPlan{}
	failed := map[place]bool{}
	for i := range boxes {
		b := &boxes[i]
		if b.plan == "" {
			continue // rented by hand: the owner's price
		}
		if k, ok := s.providerKind(b.provider); !ok || k.Role != RoleHosting {
			continue
		}
		p := place{b.provider, b.region}
		if failed[p] {
			continue
		}
		byID, read := plans[p]
		if !read {
			list, err := s.planList(ctx, p.kind, p.region)
			if err != nil {
				failed[p] = true
				out.Failed = append(out.Failed, fmt.Sprintf("%s %s: %v", p.kind, p.region, err))
				continue
			}
			byID = make(map[string]BoxPlan, len(list))
			for _, pl := range list {
				byID[pl.ID] = pl
			}
			plans[p] = byID
		}
		if pl, ok := byID[b.plan]; ok && pl.Price > 0 {
			amount, cur := pl.Price, pl.Currency
			b.amount, b.currency = &amount, &cur
		}
	}

	rates := s.fxRates()
	for _, b := range boxes {
		if b.amount == nil || b.currency == nil {
			continue
		}
		cents, err := rates.USDCents(ctx, *b.amount, *b.currency)
		if err != nil {
			return out, fmt.Errorf("entry: price of %s: %w", b.name, err)
		}
		tag, err := s.pool.Exec(ctx, `
			UPDATE entry_boxes SET price_amount = $2, price_currency = $3, monthly_cents = $4
			WHERE name = $1 AND retired_at IS NULL AND (
				price_amount IS DISTINCT FROM $2 OR price_currency IS DISTINCT FROM $3
				OR monthly_cents IS DISTINCT FROM $4)
		`, b.name, *b.amount, *b.currency, cents)
		if err != nil {
			return out, fmt.Errorf("entry: price of %s: %w", b.name, err)
		}
		if tag.RowsAffected() > 0 && (b.cents == nil || *b.cents != cents) {
			was := "—"
			if b.cents != nil {
				was = fmt.Sprint(*b.cents)
			}
			out.Changed = append(out.Changed, fmt.Sprintf("%s %s→%d", b.name, was, cents))
		}
	}
	return out, nil
}

func (s *Service) planList(ctx context.Context, kind, region string) ([]BoxPlan, error) {
	bp, err := s.hostingIn(ctx, kind, region)
	if err != nil {
		return nil, err
	}
	return bp.Plans(ctx)
}

// SetBoxPrice is the owner's price for a box rented where the hub has no API:
// amount a month in currency (USD, EUR, RUB). The price passes keep it
// converted at the day's rate. A box on a hoster with an API takes the
// hoster's price back on the next pass.
func (s *Service) SetBoxPrice(ctx context.Context, box string, amount float64, currency string) (int, error) {
	cur, err := fx.Normalize(currency)
	if err != nil {
		return 0, fmt.Errorf("%w: валюта %q: только USD, EUR, RUB", ErrRejected, currency)
	}
	if amount <= 0 {
		return 0, fmt.Errorf("%w: цена должна быть больше нуля", ErrRejected)
	}
	cents, err := s.fxRates().USDCents(ctx, amount, cur)
	if err != nil {
		return 0, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_boxes SET price_amount = $2, price_currency = $3, monthly_cents = $4
		WHERE name = $1 AND retired_at IS NULL
	`, box, amount, cur, cents)
	if err != nil {
		return 0, fmt.Errorf("entry: price of %s: %w", box, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, fmt.Errorf("%w: %s (живой машины с таким именем нет)", ErrNoSuchBox, box)
	}
	return cents, nil
}

// PriceSyncLoop runs a price pass at start and then every `every`.
func (s *Service) PriceSyncLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 6 * time.Hour
	}
	pass := func() {
		res, err := s.SyncBoxPrices(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			applog.Error(ctx, "entry", "box price pass failed", applog.F("err", err.Error()))
		}
		if len(res.Failed) > 0 {
			applog.Error(ctx, "entry", "box prices: hoster unread, its boxes keep their price",
				applog.F("hosters", strings.Join(res.Failed, "; ")))
		}
		if len(res.Changed) > 0 {
			applog.Info(ctx, "entry", "box prices changed", applog.F("boxes", strings.Join(res.Changed, ", ")))
		}
	}
	pass()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}
