package entry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// providerRow is one saved provider account (migration 056).
type providerRow struct {
	Kind              string
	Enabled           bool
	CredsCT           []byte
	CredsHint         string
	TLDs              []string
	CheckedAt         *time.Time
	CheckOK           *bool
	CheckError        string
	BalanceCents      *int
	AccountSpentCents *int
	AccountLimitCents *int
	DefaultRegion     string
	DefaultPlan       string
	UpdatedAt         time.Time
}

// policy is the purchase policy this registrar account sets: its TLD list
// (the built-in one when none is saved) and the built-in words.
func (r providerRow) policy() DomainPolicy {
	p := DefaultDomainPolicy()
	if len(r.TLDs) > 0 {
		p.TLDs = append([]string(nil), r.TLDs...)
	}
	return p
}

// hostingPolicy is the order policy this hosting account sets: where and what
// to order by default (П9.2).
func (r providerRow) hostingPolicy() HostingPolicy {
	return HostingPolicy{Region: r.DefaultRegion, Plan: r.DefaultPlan}
}

// check is the last recorded account check, nil before the first one.
func (r providerRow) check() *ProviderCheck {
	if r.CheckedAt == nil || r.CheckOK == nil {
		return nil
	}
	return &ProviderCheck{
		At: *r.CheckedAt, OK: *r.CheckOK, Error: r.CheckError,
		BalanceCents: r.BalanceCents, AccountSpentCents: r.AccountSpentCents, AccountLimitCents: r.AccountLimitCents,
	}
}

const (
	selectProviders = `
		SELECT kind, enabled, creds_ct, creds_hint, tlds,
		       checked_at, check_ok, check_error, balance_cents, account_spent_cents, account_limit_cents,
		       default_region, default_plan, updated_at
		FROM entry_providers`

	selectProvider = selectProviders + ` WHERE kind = $1`

	// ensureProvider makes the row exist, switched off and keyless, so the
	// save is always an UPDATE: an INSERT … ON CONFLICT checks its proposed
	// row before it knows it conflicts, and a settings-only save of an
	// enabled provider would propose an enabled row with no key.
	ensureProvider = `
		INSERT INTO entry_providers (kind) VALUES ($1) ON CONFLICT (kind) DO NOTHING`

	// updateProvider writes the owner's settings and, when $5 is not NULL, new
	// credentials with their hint — in one statement, so the row is never seen
	// enabled without the key it was enabled with. A NULL $5 keeps the saved
	// key.
	updateProvider = `
		UPDATE entry_providers
		SET enabled = $2, tlds = $3, updated_by = $4,
		    creds_ct = coalesce($5, creds_ct), creds_hint = coalesce($6, creds_hint),
		    default_region = $7, default_plan = $8,
		    updated_at = now()
		WHERE kind = $1`

	recordProviderCheck = `
		UPDATE entry_providers
		SET checked_at = $2, check_ok = $3, check_error = $4,
		    balance_cents = coalesce($5, balance_cents),
		    account_spent_cents = coalesce($6, account_spent_cents),
		    account_limit_cents = CASE WHEN $3 THEN $7 ELSE account_limit_cents END
		WHERE kind = $1`
)

func scanProvider(row pgx.Row) (providerRow, error) {
	var p providerRow
	err := row.Scan(&p.Kind, &p.Enabled, &p.CredsCT, &p.CredsHint, &p.TLDs,
		&p.CheckedAt, &p.CheckOK, &p.CheckError, &p.BalanceCents, &p.AccountSpentCents, &p.AccountLimitCents,
		&p.DefaultRegion, &p.DefaultPlan, &p.UpdatedAt)
	return p, err
}

// providers returns every saved account by kind.
func (r *Repository) providers(ctx context.Context) (map[string]providerRow, error) {
	rows, err := r.pool.Query(ctx, selectProviders)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]providerRow{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out[p.Kind] = p
	}
	return out, rows.Err()
}

// provider returns one saved account; ok is false when kind has none.
func (r *Repository) provider(ctx context.Context, kind string) (providerRow, bool, error) {
	p, err := scanProvider(r.pool.QueryRow(ctx, selectProvider, kind))
	if err == pgx.ErrNoRows {
		return providerRow{}, false, nil
	}
	if err != nil {
		return providerRow{}, false, err
	}
	return p, true, nil
}

// upsertProvider saves the settings of kind; credsCT nil keeps the saved key.
// by is the admin who saved, empty for the hub itself (a test, a CLI).
func (r *Repository) upsertProvider(ctx context.Context, tx pgx.Tx, kind string, u ProviderUpdate,
	tlds []string, by string, credsCT []byte, hint string) error {
	var actor *uuid.UUID
	if by != "" {
		id, err := uuid.Parse(by)
		if err != nil {
			return fmt.Errorf("entry: admin id %q: %w", by, err)
		}
		actor = &id
	}
	var hintArg *string
	if credsCT != nil {
		hintArg = &hint
	}
	if _, err := tx.Exec(ctx, ensureProvider, kind); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, updateProvider, kind, u.Enabled, tlds, actor,
		credsCT, hintArg, u.DefaultRegion, u.DefaultPlan)
	return err
}

// recordProviderCheck stores a check. A failed check keeps the last known
// money (it is still the best figure there is) and only flips the verdict.
func (r *Repository) recordProviderCheck(ctx context.Context, tx pgx.Tx, kind string, c *ProviderCheck) error {
	if c == nil {
		return nil
	}
	_, err := tx.Exec(ctx, recordProviderCheck, kind, c.At, c.OK, c.Error,
		c.BalanceCents, c.AccountSpentCents, c.AccountLimitCents)
	return err
}
