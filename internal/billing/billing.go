// Package billing sells subscriptions (hikkari-client
// plans/2026-10-31-subscriptions.md, «Оплата и автовыдача»): an order for a
// plan, a payment at the provider, and the subscription issued or renewed when
// the provider says the money is taken.
//
// An order is settled in one transaction with what it buys, and settling is
// idempotent: a notification repeated, a lost one made up for by asking the
// provider (Check), both arriving at once — the buyer gets exactly one period
// per payment.
package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

const (
	// checkEvery is how often a pending order may cost a request to the
	// provider, however often the buyer's page asks.
	checkEvery = 15 * time.Second
	// orderLifetime is how long an unpaid order waits. A payment the
	// provider reports later still settles it: the money is taken.
	orderLifetime = 24 * time.Hour
)

var (
	// ErrClosed: no payment provider is connected.
	ErrClosed = errors.New("billing: no payment provider")
	// ErrEmail: not an address a receipt can go to.
	ErrEmail = errors.New("billing: email")
	// ErrPlan: no plan on sale under that id.
	ErrPlan = errors.New("billing: plan not on sale")
	// ErrRenewal: the subscription to renew is unknown or revoked.
	ErrRenewal = errors.New("billing: subscription to renew")
	// ErrUnknownOrder: no such order, or not the buyer's.
	ErrUnknownOrder = errors.New("billing: unknown order")
	// ErrAmount: the provider took a sum other than the order's.
	ErrAmount = errors.New("billing: paid amount differs from the order")
)

// Service takes orders and settles them.
type Service struct {
	pool db.Pool
	subs *subscription.Service
	// provider is nil while no merchant is connected: plans are shown,
	// orders refused.
	provider Provider
	// returnURL is the landing's page the buyer comes back to (/pay/done).
	returnURL string
	now       func() time.Time
}

func New(pool db.Pool, subs *subscription.Service, provider Provider, returnURL string) *Service {
	return &Service{pool: pool, subs: subs, provider: provider, returnURL: returnURL, now: time.Now}
}

// Open says whether orders are taken.
func (s *Service) Open() bool { return s.provider != nil }

// Placed is an order opened at the provider.
type Placed struct {
	ID uuid.UUID
	// Secret proves the order is the buyer's; shown once, kept as a hash.
	Secret string
	PayURL string
}

// Place orders plan for email: a new subscription, or the next period of the
// one renewToken is the token of. The buyer pays at PayURL.
func (s *Service) Place(ctx context.Context, plan, email, renewToken string) (Placed, error) {
	if s.provider == nil {
		return Placed{}, ErrClosed
	}
	email, err := normalizeEmail(email)
	if err != nil {
		return Placed{}, err
	}
	var account *uuid.UUID
	if renewToken != "" {
		a, err := s.subs.Account(ctx, renewToken)
		if errors.Is(err, subscription.ErrUnknown) {
			return Placed{}, ErrRenewal
		}
		if err != nil {
			return Placed{}, err
		}
		account = &a
	}
	secret, err := randomSecret()
	if err != nil {
		return Placed{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Placed{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	p, err := s.subs.PlanIn(ctx, tx, plan)
	if errors.Is(err, subscription.ErrUnknownPlan) || (err == nil && p.PriceCents <= 0) {
		return Placed{}, ErrPlan
	}
	if err != nil {
		return Placed{}, err
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO hub_orders (secret_hash, plan_id, amount_cents, email, renewal, account_id, provider)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		hash(secret), p.ID, p.PriceCents, email, account != nil, account, s.provider.Name()).Scan(&id); err != nil {
		return Placed{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Placed{}, err
	}

	// The secret rides in the fragment: the landing reads it, no server
	// along the way (the provider's included) logs it.
	co, err := s.provider.Create(ctx, Payment{
		Order:       id,
		AmountCents: p.PriceCents,
		Description: description(p),
		Email:       email,
		ReturnURL:   s.returnURL + "?order=" + id.String() + "#key=" + secret,
	})
	if err != nil {
		if _, cerr := s.pool.Exec(ctx,
			`UPDATE hub_orders SET status = 'canceled', closed_at = now() WHERE id = $1`, id); cerr != nil {
			applog.Error(ctx, "billing", "cancel unopened order", applog.F("err", cerr.Error()))
		}
		return Placed{}, fmt.Errorf("billing: open payment: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE hub_orders SET provider_ref = $2 WHERE id = $1`, id, co.Ref); err != nil {
		return Placed{}, err
	}
	return Placed{ID: id, Secret: secret, PayURL: co.URL}, nil
}

func description(p subscription.Plan) string {
	if p.Title != "" {
		return "Подписка Hikkari, " + p.Title
	}
	return fmt.Sprintf("Подписка Hikkari, %d дн.", p.Days)
}

// Settle applies the provider's word on a payment to its order. A payment
// already applied is not applied again.
func (s *Service) Settle(ctx context.Context, ev Event) error {
	if s.provider == nil {
		return ErrClosed
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var (
		id      uuid.UUID
		plan    string
		amount  int
		email   string
		renewal bool
		account *uuid.UUID
		status  string
	)
	err = tx.QueryRow(ctx, `
		SELECT id, plan_id, amount_cents, email, renewal, account_id, status FROM hub_orders
		WHERE provider = $1 AND provider_ref = $2 FOR UPDATE`, s.provider.Name(), ev.Ref).
		Scan(&id, &plan, &amount, &email, &renewal, &account, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnknownOrder
	}
	if err != nil {
		return err
	}

	switch ev.State {
	case Pending:
		return nil
	case Canceled:
		if status != "pending" {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE hub_orders SET status = 'canceled', closed_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	case Paid:
	default:
		return fmt.Errorf("billing: event state %d", ev.State)
	}

	if status == "paid" {
		return nil
	}
	if ev.AmountCents != amount {
		return fmt.Errorf("%w: order %s is %d, paid %d", ErrAmount, id, amount, ev.AmountCents)
	}
	// Even an order this side gave up on (expired, canceled) is honoured:
	// the money is taken, so the access is given.
	if renewal {
		if account == nil {
			return fmt.Errorf("billing: renewal %s lost its account", id)
		}
		if err := s.subs.RenewIn(ctx, tx, *account, plan); err != nil {
			return fmt.Errorf("billing: renew for order %s: %w", id, err)
		}
	} else {
		_, a, err := s.subs.AddIn(ctx, tx, accountName(id), plan, email)
		if err != nil {
			return fmt.Errorf("billing: issue for order %s: %w", id, err)
		}
		account = &a
	}
	if _, err := tx.Exec(ctx, `
		UPDATE hub_orders SET status = 'paid', account_id = $2, closed_at = now() WHERE id = $1`, id, account); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// accountName is the account a paid order creates: derived from the order,
// so the admin console leads from one to the other.
func accountName(order uuid.UUID) string {
	return "p-" + strings.ReplaceAll(order.String(), "-", "")[:16]
}

// View is an order as its buyer sees it.
type View struct {
	Status    string
	Plan      subscription.Plan
	Renewal   bool
	CreatedAt time.Time
	// URL is the subscription link once the order is paid.
	URL string
}

// Status is the order id as the holder of its secret sees it. A pending
// order is checked with the provider, at most once per checkEvery, so the
// buyer is not left waiting on a lost notification.
func (s *Service) Status(ctx context.Context, id uuid.UUID, secret string) (View, error) {
	v, ref, checked, account, err := s.view(ctx, id, secret)
	if err != nil {
		return View{}, err
	}
	if v.Status == "pending" && ref != "" && s.provider != nil && s.now().Sub(checked) >= checkEvery {
		s.check(ctx, id, ref)
		if v, _, _, account, err = s.view(ctx, id, secret); err != nil {
			return View{}, err
		}
	}
	if v.Status == "paid" && account != nil {
		v.URL, err = s.subs.URL(ctx, *account)
		if err != nil && !errors.Is(err, subscription.ErrUnknown) {
			return View{}, err
		}
	}
	return v, nil
}

func (s *Service) view(ctx context.Context, id uuid.UUID, secret string) (View, string, time.Time, *uuid.UUID, error) {
	var (
		v       View
		ref     *string
		checked *time.Time
		account *uuid.UUID
	)
	err := s.pool.QueryRow(ctx, `
		SELECT o.status, o.renewal, o.created_at, o.provider_ref, o.checked_at, o.account_id,
		       p.id, p.title, p.days, o.amount_cents
		FROM hub_orders o JOIN hub_plans p ON p.id = o.plan_id
		WHERE o.id = $1 AND o.secret_hash = $2`, id, hash(secret)).
		Scan(&v.Status, &v.Renewal, &v.CreatedAt, &ref, &checked, &account,
			&v.Plan.ID, &v.Plan.Title, &v.Plan.Days, &v.Plan.PriceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, "", time.Time{}, nil, ErrUnknownOrder
	}
	if err != nil {
		return View{}, "", time.Time{}, nil, err
	}
	var r string
	if ref != nil {
		r = *ref
	}
	var c time.Time
	if checked != nil {
		c = *checked
	}
	return v, r, c, account, nil
}

// check asks the provider about one pending order and settles it. A failure
// is logged: the order stays pending and is asked about again later.
func (s *Service) check(ctx context.Context, id uuid.UUID, ref string) {
	if _, err := s.pool.Exec(ctx, `UPDATE hub_orders SET checked_at = now() WHERE id = $1`, id); err != nil {
		applog.Error(ctx, "billing", "mark order checked", applog.F("err", err.Error()))
		return
	}
	ev, err := s.provider.Check(ctx, ref)
	if err == nil {
		err = s.Settle(ctx, ev)
	}
	if err != nil {
		applog.Error(ctx, "billing", "check order", applog.F("order", id.String()), applog.F("err", err.Error()))
	}
}

// Sweep settles pending orders whose notification is late and expires those
// past orderLifetime.
func (s *Service) Sweep(ctx context.Context) error {
	if s.provider == nil {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, provider_ref FROM hub_orders
		WHERE status = 'pending' AND provider = $1 AND provider_ref IS NOT NULL
		  AND created_at < now() - interval '2 minutes'
		  AND (checked_at IS NULL OR checked_at < now() - interval '5 minutes')
		ORDER BY created_at LIMIT 100`, s.provider.Name())
	if err != nil {
		return err
	}
	type due struct {
		id  uuid.UUID
		ref string
	}
	dues, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ref)
	})
	if err != nil {
		return err
	}
	for _, d := range dues {
		s.check(ctx, d.id, d.ref)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE hub_orders SET status = 'expired', closed_at = now()
		WHERE status = 'pending' AND created_at < now() - make_interval(secs => $1)`, orderLifetime.Seconds())
	return err
}

// Run sweeps every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil {
				applog.Error(ctx, "billing", "sweep", applog.F("err", err.Error()))
			}
		}
	}
}

func normalizeEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Address != raw || len(raw) > 254 || strings.ContainsAny(raw, " \t\r\n") {
		return "", ErrEmail
	}
	return strings.ToLower(raw), nil
}

func randomSecret() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
