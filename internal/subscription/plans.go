package subscription

// plans.go is what a subscription is sold on: a plan (days, traffic, price)
// and the terms a subscription carries for its current period.
//
// A subscription is live while it is not revoked, its period has not ended
// and it has traffic left. Only live subscriptions' keys reach the relays
// (keys.go), so an expired or spent subscription stops on every relay within
// one pulse, while its link keeps answering with its terms: the client shows
// "expired" or "no traffic left" instead of a broken link.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Plan is one thing sold.
type Plan struct {
	ID string
	// Title is what the landing shows the plan as.
	Title string
	// Days is the length of one period.
	Days int
	// TrafficBytes is the period's allowance; 0 is no limit.
	TrafficBytes int64
	PriceCents   int
}

var planRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ErrUnknownPlan: no plan on sale under that id.
var ErrUnknownPlan = errors.New("subscription: unknown plan")

// SetPlan puts a plan on sale, or changes one. Subscriptions already sold keep
// the terms they were sold on.
func (s *Service) SetPlan(ctx context.Context, p Plan) error {
	if !planRe.MatchString(p.ID) || p.Days <= 0 || p.TrafficBytes < 0 || p.PriceCents < 0 {
		return fmt.Errorf("subscription: plan %+v", p)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO hub_plans (id, title, days, traffic_bytes, price_cents) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE
		SET title = EXCLUDED.title, days = EXCLUDED.days, traffic_bytes = EXCLUDED.traffic_bytes,
		    price_cents = EXCLUDED.price_cents, retired_at = NULL`,
		p.ID, p.Title, p.Days, p.TrafficBytes, p.PriceCents)
	return err
}

// RetirePlan takes a plan off sale.
func (s *Service) RetirePlan(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE hub_plans SET retired_at = now() WHERE id = $1 AND retired_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknownPlan
	}
	return nil
}

// Plans is every plan on sale, shortest first.
func (s *Service) Plans(ctx context.Context) ([]Plan, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, title, days, traffic_bytes, price_cents FROM hub_plans
		WHERE retired_at IS NULL ORDER BY days, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Plan, error) {
		var p Plan
		return p, r.Scan(&p.ID, &p.Title, &p.Days, &p.TrafficBytes, &p.PriceCents)
	})
}

// PlanIn is the plan on sale under id, read inside tx.
func (s *Service) PlanIn(ctx context.Context, tx pgx.Tx, id string) (Plan, error) {
	return s.plan(ctx, tx, id, true)
}

// plan reads a plan; onSale refuses a retired one. What was bought while a
// plan was on sale is still delivered on it after it is retired.
func (s *Service) plan(ctx context.Context, tx pgx.Tx, id string, onSale bool) (Plan, error) {
	p := Plan{ID: id}
	err := tx.QueryRow(ctx,
		`SELECT title, days, traffic_bytes, price_cents FROM hub_plans WHERE id = $1 AND ($2 = false OR retired_at IS NULL)`, id, onSale).
		Scan(&p.Title, &p.Days, &p.TrafficBytes, &p.PriceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrUnknownPlan
	}
	return p, err
}

// startPeriod puts account's live subscription on plan for one period: it
// ends plan.Days after the later of now and the current end (time paid for is
// never lost), and its traffic counts afresh from the account's usage now.
func (s *Service) startPeriod(ctx context.Context, tx pgx.Tx, account uuid.UUID, p Plan) error {
	tag, err := tx.Exec(ctx, `
		UPDATE hub_subscriptions s
		SET plan_id = $2,
		    expires_at = GREATEST(now(), coalesce(s.expires_at, now())) + make_interval(days => $3),
		    traffic_bytes = $4,
		    baseline_up = u.up, baseline_down = u.down
		FROM (SELECT coalesce(sum(bytes_up), 0) AS up, coalesce(sum(bytes_down), 0) AS down
		      FROM hub_key_usage WHERE account_id = $1) u
		WHERE s.account_id = $1 AND s.revoked_at IS NULL`,
		account, p.ID, p.Days, p.TrafficBytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknown
	}
	return nil
}

// Renew starts name's next period on plan.
func (s *Service) Renew(ctx context.Context, name, plan string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := s.plan(ctx, tx, plan, true); err != nil {
		return err
	}
	var account uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM hub_accounts WHERE name = $1`, name).Scan(&account); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnknown
		}
		return err
	}
	if err := s.RenewIn(ctx, tx, account, plan); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RenewIn starts account's next period on plan inside the caller's
// transaction (internal/billing), on sale or retired since it was bought.
// ErrUnknown when the account has no unrevoked subscription.
func (s *Service) RenewIn(ctx context.Context, tx pgx.Tx, account uuid.UUID, plan string) error {
	p, err := s.plan(ctx, tx, plan, false)
	if err != nil {
		return err
	}
	return s.startPeriod(ctx, tx, account, p)
}

// Terms is where a subscription stands in its period.
type Terms struct {
	Plan string
	// ExpiresAt is the end of the period; zero for a subscription sold before
	// plans, which has none.
	ExpiresAt time.Time
	// TrafficBytes is the period's allowance, 0 for none.
	TrafficBytes int64
	// Up and Down are what the period has used so far.
	Up, Down int64
}

// liveTerms is the condition a subscription row s is live under: the one
// place that says so, shared by the relays' key lists and the terms served.
const liveTerms = `s.revoked_at IS NULL
	AND (s.expires_at IS NULL OR s.expires_at > now())
	AND (s.traffic_bytes = 0 OR
	     (SELECT coalesce(sum(u.bytes_up + u.bytes_down), 0) FROM hub_key_usage u WHERE u.account_id = s.account_id)
	     - s.baseline_up - s.baseline_down < s.traffic_bytes)`

// LiveTerms is the same condition for a reader outside the package, over
// hub_subscriptions aliased s.
const LiveTerms = liveTerms

// terms reads the terms of a live-or-ended, unrevoked subscription.
func (s *Service) terms(ctx context.Context, account uuid.UUID) (Terms, error) {
	var t Terms
	var plan *string
	var expires *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT s.plan_id, s.expires_at, s.traffic_bytes,
		       GREATEST(coalesce(sum(u.bytes_up), 0) - s.baseline_up, 0),
		       GREATEST(coalesce(sum(u.bytes_down), 0) - s.baseline_down, 0)
		FROM hub_subscriptions s
		LEFT JOIN hub_key_usage u ON u.account_id = s.account_id
		WHERE s.account_id = $1 AND s.revoked_at IS NULL
		GROUP BY s.token_hash`, account).Scan(&plan, &expires, &t.TrafficBytes, &t.Up, &t.Down)
	if errors.Is(err, pgx.ErrNoRows) {
		return Terms{}, ErrUnknown
	}
	if err != nil {
		return Terms{}, err
	}
	if plan != nil {
		t.Plan = *plan
	}
	if expires != nil {
		t.ExpiresAt = *expires
	}
	return t, nil
}

// Status is name's terms.
func (s *Service) Status(ctx context.Context, name string) (Terms, error) {
	var account uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM hub_accounts WHERE name = $1`, name).Scan(&account); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Terms{}, ErrUnknown
		}
		return Terms{}, err
	}
	return s.terms(ctx, account)
}

// userinfo is the Subscription-Userinfo header clients read terms from.
func (t Terms) userinfo() string {
	v := fmt.Sprintf("upload=%d; download=%d; total=%d", t.Up, t.Down, t.TrafficBytes)
	if !t.ExpiresAt.IsZero() {
		v += fmt.Sprintf("; expire=%d", t.ExpiresAt.Unix())
	}
	return v
}
