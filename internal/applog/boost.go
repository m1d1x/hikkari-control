package applog

import (
	"context"
	"sync/atomic"

	"github.com/google/uuid"
)

// BoostStore reports whether a user_id is currently flagged for verbose
// logging. Implementations are typically Redis-backed (internal/cache) but
// the interface is small so tests can plug in trivial in-memory mocks.
type BoostStore interface {
	IsBoosted(ctx context.Context, userID uuid.UUID) bool
}

var defaultBoost atomic.Pointer[BoostStore]

// SetBoostStore wires the package-level store used by logLine to decide
// whether to keep otherwise-sampled-away debug/trace records for a given
// user. Pass nil to disable (no boost ever applies).
func SetBoostStore(s BoostStore) {
	if s == nil {
		defaultBoost.Store(nil)
		return
	}
	defaultBoost.Store(&s)
}

// isBoosted is the package-internal helper used by logLine. Cheap and
// non-blocking on the happy path (no store, no user) so the hot path stays
// fast when no boost is active.
func isBoosted(ctx context.Context, uid uuid.UUID) bool {
	if uid == uuid.Nil {
		return false
	}
	p := defaultBoost.Load()
	if p == nil {
		return false
	}
	return (*p).IsBoosted(ctx, uid)
}
