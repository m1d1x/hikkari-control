package entry

// account_nodes.go is a subscription's fleet picked by hand (migration 060):
// the logical nodes (entry_nodes) the owner chose for one account in the admin
// console. An account with any is pinned: serve grants it exactly the relays
// standing behind those nodes, not a batch of the pool, and revokes its grants
// elsewhere with reason fleet. A node outlives the relay behind it, so the
// choice stays right when a relay is replaced. A tester's account
// (all_relays) keeps grants on the whole fleet; its links are cut to the
// choice by the subscription (internal/subscription).

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// triggerFleet: a grant made because the relay stands behind a chosen node.
const triggerFleet = "fleet"

// ErrUnknownNode: a node chosen for a fleet that the registry does not hold.
var ErrUnknownNode = errors.New("entry: unknown node")

const (
	selectPinned = `SELECT EXISTS (SELECT 1 FROM hub_account_nodes WHERE account_id = $1)`
	// The relays behind the account's chosen nodes it does not hold yet:
	// alive, personal, not past expiry. No slots, no one-per-machine rule and
	// no machine's capacity: the owner chose them.
	grantCandidatesPinned = `
		SELECT r.id, r.name, b.proto, coalesce(b.region, ''), b.name
		FROM hub_account_nodes p
		JOIN entry_nodes n ON n.slug = p.slug
		JOIN entry_relays r ON r.id = n.relay_id
		JOIN entry_boxes b ON b.name = r.box
		WHERE p.account_id = $1 AND r.kind = 'personal' AND r.status = 'alive' AND r.expires_at > $2
		  AND NOT EXISTS (SELECT 1 FROM entry_grants g
		                  WHERE g.account_id = $1 AND g.relay_id = r.id AND g.revoked_at IS NULL)`
	// The account's live grants on relays that stand behind none of its
	// chosen nodes go: the relay stops taking its key at the next pulse.
	revokeOffFleet = `
		UPDATE entry_grants g SET revoked_at = now(), revoke_reason = 'fleet'
		WHERE g.account_id = $1 AND g.revoked_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM hub_account_nodes p JOIN entry_nodes n ON n.slug = p.slug
		                  WHERE p.account_id = $1 AND n.relay_id = g.relay_id)`
)

// pinned: the account's fleet was picked by hand.
func (r *Repository) pinned(ctx context.Context, tx pgx.Tx, account uuid.UUID) (bool, error) {
	var p bool
	err := tx.QueryRow(ctx, selectPinned, account).Scan(&p)
	return p, err
}

func (r *Repository) pinnedCandidates(ctx context.Context, tx pgx.Tx, account uuid.UUID, now time.Time) ([]candidate, error) {
	rows, err := tx.Query(ctx, grantCandidatesPinned, account, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.name, &c.lane.proto, &c.lane.region, &c.box); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetAccountNodes makes slugs the account's fleet inside the caller's
// transaction; none returns it to the pool's batch. A pinned account that is
// not a tester's loses its grants off the fleet at once, so the relays stop
// taking its key at their next pulse, not at the subscriber's next fetch.
// ErrUnknownNode for a slug the registry does not hold.
func SetAccountNodes(ctx context.Context, tx pgx.Tx, account uuid.UUID, slugs []string) error {
	slugs = slices.Clone(slugs)
	slices.Sort(slugs)
	slugs = slices.Compact(slugs)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM hub_accounts WHERE id = $1 FOR UPDATE`, account); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hub_account_nodes WHERE account_id = $1 AND NOT (slug = ANY($2))`, account, slugs); err != nil {
		return err
	}
	if len(slugs) > 0 {
		_, err := tx.Exec(ctx, `
			INSERT INTO hub_account_nodes (account_id, slug)
			SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`, account, slugs)
		if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "23503" {
			return fmt.Errorf("%w: %v", ErrUnknownNode, slugs)
		}
		if err != nil {
			return err
		}
	}
	var all bool
	if err := tx.QueryRow(ctx, selectAllRelays, account).Scan(&all); err != nil {
		return err
	}
	if len(slugs) > 0 && !all {
		if _, err := tx.Exec(ctx, revokeOffFleet, account); err != nil {
			return err
		}
	}
	return nil
}

// AccountNodes is the account's fleet picked by hand, nil for the pool's batch.
func AccountNodes(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, account uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT slug FROM hub_account_nodes WHERE account_id = $1 ORDER BY slug`, account)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// FleetChoice is one node the owner can put in a subscription's fleet.
type FleetChoice struct {
	Slug   string
	Label  string
	Proto  string
	Region string
	Box    string
	// Alive: a relay that serves stands behind the node now.
	Alive bool
}

// FleetChoices is every node of the registry with what stands behind it,
// alive ones first, by label.
func (s *Service) FleetChoices(ctx context.Context) ([]FleetChoice, error) {
	if err := s.syncFleetNodes(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT n.slug, coalesce(n.label, ''), n.proto, n.region, r.box,
		       r.status = 'alive' AND r.kind = 'personal'
		FROM entry_nodes n JOIN entry_relays r ON r.id = n.relay_id
		ORDER BY 6 DESC, coalesce(n.label, n.slug)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (FleetChoice, error) {
		var c FleetChoice
		err := row.Scan(&c.Slug, &c.Label, &c.Proto, &c.Region, &c.Box, &c.Alive)
		return c, err
	})
}
