package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/hikkariobs"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// hikkariSubjects resolves the agent's "who" for Hikkari periods: a tester
// (its install, the client side) or an account by name (the relay side).
func hikkariSubjects(pool db.Pool, testers support.TesterStore) func(ctx context.Context, side, ref string) (string, error) {
	return func(ctx context.Context, side, ref string) (string, error) {
		if side != hikkariobs.SideRelay {
			id, _, err := testers.ResolveInstall(ctx, ref)
			if err == nil {
				return id, nil
			}
			if side == hikkariobs.SideClient {
				return "", errors.New("install not found")
			}
		}
		var id string
		err := pool.QueryRow(ctx, `SELECT id::text FROM hub_accounts WHERE name = $1`, ref).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("no tester or account by that name")
		}
		return id, err
	}
}

// hikkariNames names subjects: installs by alias, accounts by name.
func hikkariNames(pool db.Pool, testers support.TesterStore) hikkariobs.Namer {
	aliases := testerAliases(testers)
	return func(ctx context.Context) (func(side, subject string) string, error) {
		alias, err := aliases(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := pool.Query(ctx, `SELECT id::text, name FROM hub_accounts`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		accounts := map[string]string{}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return nil, err
			}
			accounts[id] = name
		}
		return func(side, subject string) string {
			if side == hikkariobs.SideClient {
				return alias(subject)
			}
			return accounts[subject]
		}, rows.Err()
	}
}
