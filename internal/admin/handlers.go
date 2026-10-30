// Package admin serves the hub's admin console API: the read-only screens of
// the fleet (entry_*.go) and the gate in front of every admin route
// (middleware.go). The routes that act on the fleet are internal/entry's.
package admin

import (
	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// Handlers holds what the console screens read: the hub database and the
// ClickHouse store with probes, pulses and reach.
type Handlers struct {
	pool      db.Pool
	analytics *analytics.Store
}

func NewHandlers(pool db.Pool, store *analytics.Store) *Handlers {
	return &Handlers{pool: pool, analytics: store}
}
