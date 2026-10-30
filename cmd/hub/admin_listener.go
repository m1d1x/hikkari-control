package main

// admin_listener.go serves the operator plane on a listener of its own.
//
// # Why a second listener and not a gate on the first
//
// Until task 77.1 every admin route hung off the same mux as the app's API, so
// on a deployed box /api/v1/admin/* answered from the internet through
// api.<base>, and the only thing standing between a stranger and a ban, an
// erasure or a support inbox was the admin cookie. A path guard on the public
// mux would keep the routes in that process's public surface and make their
// safety depend on one condition nobody may ever weaken.
//
// A route that is not registered on the public listener cannot be reached
// through it — that is the property, and it holds by construction. The admin
// listener binds loopback by default; on a box it is published on 127.0.0.1
// only (compose.prod.yml) and reached through `ssh -L` (scripts/admin.sh), so
// the operator's SSH key is the first factor before the panel asks for any.
//
// The chain is the public one minus what assumes strangers: no banlist, no
// honeypot, no per-subject rate limit (behind a tunnel every request comes from
// 127.0.0.1, so a per-IP cap would throttle the operator against themself, and
// the login carries its own lockout). CORS, CSRF, the body cap and the security
// headers stay: the panel is still a browser on its own origin.

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/middleware"
)

// defaultAdminListenAddr is loopback on purpose: a bare `go run ./cmd/server`
// on a machine with an open firewall must not hand the operator plane to the
// network. Containers set ADMIN_LISTEN_ADDR=":3310" and let the publish rule
// decide who reaches it.
const defaultAdminListenAddr = "127.0.0.1:3310"

// adminListenAddr reads ADMIN_LISTEN_ADDR, falling back to loopback.
func adminListenAddr() string {
	if v := strings.TrimSpace(os.Getenv("ADMIN_LISTEN_ADDR")); v != "" {
		return v
	}
	return defaultAdminListenAddr
}

// newAdminServer builds the operator listener over adminMux. It is not started
// here; main owns the lifecycle of both servers side by side.
func newAdminServer(addr string, adminMux *http.ServeMux) *http.Server {
	handler := middleware.Trace(
		corsMiddleware(
			securityHeadersMiddleware(
				middleware.MaxBodyBytes(
					middleware.CSRFMiddleware(
						middleware.RelaxDeadlinesForUploads(adminMux),
					),
				),
			),
		),
	)
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
}

// runAdminCeremonyPurger drops admin login and step-up challenges nobody
// answered. They are refused once expired regardless; this only keeps the
// table from collecting them.
func runAdminCeremonyPurger(ctx context.Context, pool db.Pool) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := auth.PurgeExpiredAdminCeremonies(ctx, pool); err != nil {
				log.Printf("admin ceremonies purge: %v", err)
			}
		}
	}
}
