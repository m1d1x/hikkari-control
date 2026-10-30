package auth

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Skipped unless TEST_DATABASE_URL is set: the property under test is
// DURABILITY, which only a real scope_events table can demonstrate.
const testDatabaseEnv = "TEST_DATABASE_URL"

var (
	poolOnce sync.Once
	poolRef  *pgxpool.Pool
	poolErr  error
)

func sharedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testDatabaseEnv)
	if url == "" {
		t.Skipf("set %s to run auth-spine integration tests", testDatabaseEnv)
	}
	poolOnce.Do(func() {
		ctx := context.Background()
		p, err := db.Connect(ctx, url)
		if err != nil {
			poolErr = err
			return
		}
		if err := db.RunMigrations(ctx, p); err != nil {
			poolErr = err
			p.Close()
			return
		}
		poolRef = p
	})
	if poolErr != nil {
		t.Fatalf("test pool init: %v", poolErr)
	}
	return poolRef
}
