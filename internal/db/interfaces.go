package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the common interface for query execution shared by pool, connection, and transaction.
// Both *pgxpool.Pool and pgx.Tx satisfy this interface.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pool combines query execution with transaction management.
// *pgxpool.Pool satisfies this interface.
type Pool interface {
	DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}
