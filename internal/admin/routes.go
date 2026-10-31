package admin

import (
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// RegisterRoutes wires the console's read-only screens: the fleet's and the
// product section's. The routes that
// act on the fleet are registered by entry.RegisterAdminRoutes.
func RegisterRoutes(mux *http.ServeMux, h *Handlers, adminKeys *auth.KeySet, pool db.Pool) {
	adminAuth := func(handler http.HandlerFunc) http.HandlerFunc {
		return RequireAdmin(adminKeys.Public, pool, handler)
	}
	ownerOnly := func(handler http.HandlerFunc) http.HandlerFunc {
		return RequireAdmin(adminKeys.Public, pool, RequireOwner(handler))
	}
	// The entry-points console's first screen in one request (plan 42 П9.9):
	// did people get through, what it cost, how fast what we buy is burnt.
	// Read-only — the tools that act are their own pages. See entry_overview.go.
	mux.HandleFunc("GET /api/v1/admin/entry/overview", adminAuth(h.EntryOverview))
	// The incidents screen in one request (plan 42 К2): how the autopilot is
	// doing, how fast it notices and repairs, the queue and the feed. Also
	// read-only — the buttons on a card are internal/entry's. See
	// entry_incidents.go.
	mux.HandleFunc("GET /api/v1/admin/entry/incidents", adminAuth(h.EntryIncidents))
	// The fleet screen in one request (plan 42 К1): the pool size and health,
	// mortality vs replacement, rollouts, and machines grouped by provider.
	// Not /entry/fleet: that is the autopilot's pools and targets
	// (internal/entry), and two handlers on one pattern panic the mux at
	// startup. See entry_fleet.go.
	mux.HandleFunc("GET /api/v1/admin/entry/machines", adminAuth(h.EntryFleet))
	// One machine's card: its life, what it cost, and its relays' pulse. The
	// one place the machine's address is opened, so owner-only, like the
	// entry plane's own surface. See entry_box_card.go.
	mux.HandleFunc("GET /api/v1/admin/entry/machines/{name}", ownerOnly(h.EntryBoxCard))
	// The domain screen's figures (plan 42 К4): how long a name survives on a
	// box, how fast the pool drains, how much buffer is left. The ledger and
	// the buy button are internal/entry's. See entry_domains.go.
	mux.HandleFunc("GET /api/v1/admin/entry/domains/metrics", adminAuth(h.EntryDomainMetrics))
	// The provider comparison (plan 42 К6): winrate over the 30-day cohort,
	// median machine life, cost of a living day, ip bans per day. The keys
	// themselves are internal/entry's. See entry_providers.go.
	mux.HandleFunc("GET /api/v1/admin/entry/providers/compare", adminAuth(h.EntryProviderCompare))
	// The detection screen's ban tempo (plan 42 К3): transitions into a
	// blocking verdict per bucket, split by cause, with the wave markers read
	// from the fuse's own record. The thresholds beside the chart are
	// internal/entry's, because turning one acts. See entry_pressure.go.
	mux.HandleFunc("GET /api/v1/admin/entry/pressure", adminAuth(h.EntryPressure))
	// The product section's screens, counted from the clients' telemetry:
	// people, connects and activation (product_metrics.go), and the error
	// groups (product_errors.go). Read-only.
	mux.HandleFunc("GET /api/v1/admin/product/metrics", adminAuth(h.ProductMetrics))
	mux.HandleFunc("GET /api/v1/admin/product/errors", adminAuth(h.ProductErrors))
}
