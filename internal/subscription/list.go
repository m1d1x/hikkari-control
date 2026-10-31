package subscription

// list.go is the subscriptions as the admin console sees them: who, on what,
// until when, how much used, on which relays.

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// State is where a subscription stands, in the order the console reads it.
type State string

const (
	StateLive    State = "live"
	StateExpired State = "expired"
	// StateSpent: the period's traffic is used up.
	StateSpent   State = "spent"
	StateRevoked State = "revoked"
)

// Summary is one subscription.
type Summary struct {
	Account      uuid.UUID
	Name         string
	Email        string
	Plan         string
	State        State
	CreatedAt    time.Time
	ExpiresAt    time.Time
	RevokedAt    time.Time
	TrafficBytes int64
	// Used is the current period's traffic.
	Used int64
	// Relays are the relays its batch is granted on now.
	Relays []string
}

const summaries = `
	WITH usage AS (
		SELECT account_id, sum(bytes_up + bytes_down) AS total FROM hub_key_usage GROUP BY account_id
	)
	SELECT a.id, a.name, coalesce(a.email, ''), coalesce(s.plan_id, ''), s.created_at, s.expires_at, s.revoked_at,
	       s.traffic_bytes,
	       GREATEST(coalesce(u.total, 0) - s.baseline_up - s.baseline_down, 0),
	       CASE
	         WHEN s.revoked_at IS NOT NULL THEN 'revoked'
	         WHEN s.expires_at IS NOT NULL AND s.expires_at <= now() THEN 'expired'
	         WHEN NOT (` + liveTerms + `) THEN 'spent'
	         ELSE 'live'
	       END,
	       coalesce((SELECT array_agg(r.name ORDER BY r.name) FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id
	                 WHERE g.account_id = a.id AND g.revoked_at IS NULL), '{}')
	FROM hub_subscriptions s
	JOIN hub_accounts a ON a.id = s.account_id
	LEFT JOIN usage u ON u.account_id = a.id`

func scanSummary(r pgx.CollectableRow) (Summary, error) {
	var (
		v                Summary
		expires, revoked *time.Time
		state            string
	)
	err := r.Scan(&v.Account, &v.Name, &v.Email, &v.Plan, &v.CreatedAt, &expires, &revoked,
		&v.TrafficBytes, &v.Used, &state, &v.Relays)
	if expires != nil {
		v.ExpiresAt = *expires
	}
	if revoked != nil {
		v.RevokedAt = *revoked
	}
	v.State = State(state)
	return v, err
}

// List is every subscription whose name or email contains query ("" for
// all), newest first, at most limit.
func (s *Service) List(ctx context.Context, query string, limit int) ([]Summary, error) {
	rows, err := s.pool.Query(ctx, summaries+`
		WHERE $1 = '' OR a.name ILIKE '%' || $1 || '%' OR a.email ILIKE '%' || $1 || '%'
		ORDER BY s.created_at DESC LIMIT $2`, query, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSummary)
}

// Get is name's subscription.
func (s *Service) Get(ctx context.Context, name string) (Summary, error) {
	rows, err := s.pool.Query(ctx, summaries+` WHERE a.name = $1 ORDER BY s.created_at DESC LIMIT 1`, name)
	if err != nil {
		return Summary{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanSummary)
	if errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, ErrUnknown
	}
	return v, err
}

// CountLive is how many subscriptions are live now.
func (s *Service) CountLive(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM hub_subscriptions s WHERE `+liveTerms).Scan(&n)
	return n, err
}

// AllPlans is every plan, retired ones included, shortest first.
func (s *Service) AllPlans(ctx context.Context) ([]PlanRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, title, days, traffic_bytes, price_cents, retired_at,
		       (SELECT count(*) FROM hub_subscriptions s WHERE s.plan_id = p.id AND s.revoked_at IS NULL)
		FROM hub_plans p ORDER BY retired_at IS NOT NULL, days, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PlanRow, error) {
		var p PlanRow
		var retired *time.Time
		err := r.Scan(&p.ID, &p.Title, &p.Days, &p.TrafficBytes, &p.PriceCents, &retired, &p.Subscriptions)
		p.Retired = retired != nil
		return p, err
	})
}

// PlanRow is a plan with what the console shows beside it.
type PlanRow struct {
	Plan
	Retired bool
	// Subscriptions is how many unrevoked subscriptions are on it.
	Subscriptions int
}
