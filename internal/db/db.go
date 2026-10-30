package db

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/metrics"
)

// envIntOrDefault reads a positive integer from the environment, falling back
// to def when the var is unset, non-numeric, or non-positive.
func envIntOrDefault(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// withTx runs fn inside a transaction. Rolls back if fn errors or
// Commit fails — ensures partial migration writes never persist.
func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Connect creates a PostgreSQL connection pool with production-ready settings.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse database config: %w", err)
	}

	// Pool size is env-tunable per process. Default stays at 25 (the alpha
	// value) so unconfigured deploys don't change behaviour. Sizing rule when
	// you raise it: every backend that calls Connect (cmd/sfu, cmd/server,
	// cmd/sfu) opens its OWN pool against the SAME Postgres, and
	// a deploy keeps the previous instance alive while the new one warms — so the
	// worst-case connection count is sum(MaxConns over all processes) × 2.
	// That total must stay under Postgres `max_connections` (minus the
	// superuser-reserved slots). For real scale put PgBouncer (transaction
	// pooling) in front instead of growing the app pool unbounded.
	config.MaxConns = int32(envIntOrDefault("DB_MAX_CONNS", 25))
	config.MinConns = int32(envIntOrDefault("DB_MIN_CONNS", 5))
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	// Bound the dial + TLS + auth handshake for a brand-new backend
	// connection. Without this a wedged or unreachable Postgres makes any
	// request that needs a fresh connection hang on the dial instead of
	// failing fast (surfacing as a retryable 5xx). Pool *acquisition* —
	// waiting for a free slot once MaxConns is saturated — is bounded
	// separately by the caller's context (see dbCtx() in internal/sfu),
	// since pgxpool has no pool-wide acquire-timeout knob.
	if config.ConnConfig.ConnectTimeout == 0 {
		config.ConnConfig.ConnectTimeout = 5 * time.Second
	}

	// JIT off for every session. The workload is OLTP: short indexed reads
	// whose run time is single-digit milliseconds. JIT kicks in on the
	// PLANNER'S cost estimate (jit_above_cost, 100k by default), and LATERAL
	// subqueries such as the dialog list's unread counters are estimated
	// pessimistically enough to cross it. Measured on the dialog bench
	// (scripts/bench/dialogs-bench.sh): 265 ms of a 303 ms ListChats DM page
	// was LLVM compiling a query that executes in 22 ms — paid on every call.
	// Set per session rather than in postgresql.conf so it holds on any
	// Postgres the binary is pointed at.
	config.ConnConfig.RuntimeParams["jit"] = "off"

	// Hook pgx into metrics so every Query/Exec contributes to the
	// per-request Server-Timing "db" bucket. No-op for contexts without a
	// timings accumulator (migrations, background jobs).
	config.ConnConfig.Tracer = metrics.PGXTracer{}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to ping database: %w", err)
	}
	log.Println("Connected to PostgreSQL")
	return pool, nil
}

// RunMigrations atomically applies the control-plane schema. A database lock
// serializes startup and CLI processes before either checks migration history.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	filenames := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			filenames = append(filenames, e.Name())
		}
	}
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84001)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			filename text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("create schema_migrations table: %w", err)
		}
		// A fresh baseline must never silently coexist with the experimental
		// messenger schema. Refuse startup; leave the operator's data intact.
		var foreign bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations
			WHERE NOT (filename = ANY($1::text[])))`, filenames).Scan(&foreign); err != nil {
			return err
		}
		if foreign {
			return fmt.Errorf("legacy schema detected: configure a fresh Hikkari database; existing data was left intact")
		}
		for _, name := range filenames {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename=$1)`, name).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			data, err := migrationsFS.ReadFile("migrations/" + name)
			if err != nil {
				return fmt.Errorf("read migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, string(data)); err != nil {
				return fmt.Errorf("execute migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
				return fmt.Errorf("mark migration %s: %w", name, err)
			}
		}
		return nil
	})
}
