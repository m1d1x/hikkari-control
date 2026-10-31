// Command hub runs the fleet orchestrator carried over from Nekko
// (internal/entry): provisioning over ssh, ordering machines and domains
// under the spend caps, the incident detector and the autopilot's repairs,
// the prober ingest, and the admin console API on its own listener.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-control/internal/admin"
	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/auth"
	"github.com/m1d1x/hikkari-control/internal/billing"
	"github.com/m1d1x/hikkari-control/internal/config"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/geoip"
	"github.com/m1d1x/hikkari-control/internal/presets"
	"github.com/m1d1x/hikkari-control/internal/probesink"
	"github.com/m1d1x/hikkari-control/internal/subscription"
	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		// keygen: a fresh key for NEKKO_CONTENT_KEY, which seals machine
		// addresses, ssh credentials and provider keys at rest.
		case "keygen":
			key, err := cryptobox.GenerateKey()
			if err != nil {
				fmt.Fprintln(os.Stderr, "keygen failed:", err)
				os.Exit(1)
			}
			fmt.Println("NEKKO_CONTENT_KEY=" + key)
			os.Exit(0)
		case "admin-create":
			runAdminCreate(os.Args[2:])
		case "admin-passwd":
			runAdminPasswd(os.Args[2:])
		case "admin-disable":
			runAdminDisable(os.Args[2:])
		case "admin-2fa-reset":
			runAdmin2FAReset(os.Args[2:])
		case "admin-role":
			runAdminRole(os.Args[2:])
		case "entry-box":
			runEntryBox(os.Args[2:])
		case "entry-bench":
			runEntryBench(os.Args[2:])
		case "entry-vantage-token":
			runEntryVantageToken(os.Args[2:])
		case "subscriber-add", "subscriber-renew", "subscriber-status", "subscriber-revoke", "plan-set", "plan-retire":
			runSubscriber(os.Args[1], os.Args[2:])
		}
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := validateFleetSecrets(os.Getenv); err != nil {
		return err
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is not set")
	}
	db.RequireSecureDSNOrFatal("cmd/hub", dbURL)
	if err := cryptobox.Configure(os.Getenv); err != nil {
		return fmt.Errorf("cryptobox: %w — run 'hub keygen' to generate a key", err)
	}
	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	if err := db.RunMigrations(ctx, pool); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	adminKeys, err := auth.LoadAdminKeySet()
	if err != nil {
		return fmt.Errorf("admin keys: %w", err)
	}
	adminWA, err := auth.AdminWebAuthnFromEnv()
	if err != nil {
		return fmt.Errorf("admin webauthn: %w", err)
	}

	var store *analytics.Store
	if dsn := os.Getenv("CLICKHOUSE_DSN"); dsn != "" {
		store, err = analytics.NewStore(dsn)
		if err != nil {
			return fmt.Errorf("clickhouse: %w", err)
		}
		if err := store.RunMigrations(ctx); err != nil {
			return fmt.Errorf("clickhouse migrations: %w", err)
		}
	} else {
		log.Println("WARNING: CLICKHOUSE_DSN not set — probes, pulse and the autopilot are disabled")
	}

	// ── The fleet ─────────────────────────────────────────
	signer, err := entry.LoadSigner(os.Getenv, entry.PinnedKeys())
	if err != nil {
		return fmt.Errorf("entry pool: %w", err)
	}
	if signer == nil {
		log.Printf("entry pool: %s not set — relays cannot be admitted or re-signed", entry.SigningKeyEnv)
	}
	svc, err := entry.NewService(pool, signer, entry.DefaultBatchSize)
	if err != nil {
		return fmt.Errorf("entry pool: %w", err)
	}
	prov := entry.NewProvisioner(svc, entry.SSHDialer, entry.DefaultArtifactDir)
	if token := os.Getenv("EDGE_PULSE_TOKEN"); token != "" {
		prov.SetPulse(config.Default().APIURL()+"/api/v1/internal/entry-pulse", token)
	}
	prov.SetProber(config.Default().APIURL())
	mechanic := wireEntryDomains(ctx, svc, prov)
	if n, err := svc.AbandonRunningJobs(ctx); err != nil {
		log.Printf("entry pool: abandon running jobs: %v", err)
	} else if n > 0 {
		log.Printf("entry pool: %d provisioning job(s) were cut off by the restart", n)
	}
	if n, err := svc.AbandonRunningRepairs(ctx); err != nil {
		log.Printf("entry pool: abandon running repairs: %v", err)
	} else if n > 0 {
		log.Printf("entry pool: %d repair(s) were cut off by the restart", n)
	}
	rollout := entry.NewOrchestrator(svc, prov)
	if store != nil {
		rollout.SetHealth(store)
	}
	go rollout.Run(ctx)
	go svc.RunResigner(ctx)

	// ── Public plane: what probers and relays talk to ─────
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	closeDD, err := wireDowndetector(ctx, svc, mux)
	if err != nil {
		return fmt.Errorf("downdetector: %w", err)
	}
	defer func() { stop(); closeDD() }()
	// Routing presets clients fetch (internal/presets). The country picks a
	// preset; without GEOIP_DB (a City MMDB) every device gets the default.
	geo, err := geoip.New(os.Getenv("GEOIP_DB"))
	if err != nil {
		return fmt.Errorf("geoip: %w", err)
	}
	defer func() { _ = geo.Close() }()
	presetsPublic := presets.NewPublic(presets.NewStore(pool), func(ip string) string { return geo.Lookup(ip).CountryCode })
	presetsPublic.Register(mux)
	go presetsPublic.Watch(ctx)
	// Subscribers' links (internal/subscription): their batch of the pool.
	subs := subscription.New(pool, svc, entry.PinnedKeys())
	mux.HandleFunc("GET /sub/{token}", subs.Handler())
	// Relays learn subscribers' access keys from the answer to their pulse.
	svc.SetRelayKeys(subs)
	// Selling subscriptions (internal/billing): the landing's checkout and the
	// payment provider's notifications.
	payments, err := billingProvider(os.Getenv)
	if err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	billingSvc := billing.New(pool, subs, payments, config.Default().BaseURL()+"/pay/done")
	billing.NewPublic(billingSvc, config.Default().AllWebOrigins()).Register(mux)
	go billingSvc.Run(ctx, time.Minute)
	if store != nil {
		// Client telemetry (hikkari-client docs/TELEMETRY.md). Shares /v1/
		// with the downdetector; the more specific pattern wins.
		mux.HandleFunc("POST /v1/events", (&telemetry.Server{Sink: store}).Handler())
		mux.HandleFunc("POST /api/v1/internal/entry-probes", probesink.IngestEntry(store, svc))
		entry.RegisterProberRoutes(mux, svc)
		entry.RegisterPulseRoutes(mux, svc, os.Getenv("EDGE_PULSE_TOKEN"))
		entry.RegisterBenchRoutes(mux, svc)
		svc.SetBenchSink(benchSink{store})
		svc.SetReachSink(reachSink{store})
		svc.SetPulseSink(pulseSink{store})
		svc.SetUserTrafficSink(userTrafficSink{store})
		svc.SetLoadSource(loadSource{store})
		svc.SetVerdicts(verdictSource{store})
		svc.SetVantages(verdictSource{store})
		svc.SetInspector(prov)
		if os.Getenv("ENTRY_AUTOPILOT_ENABLED") == "1" {
			go svc.DetectLoop(ctx, time.Minute)
			go mechanic.Run(ctx, time.Minute)
			go svc.SecondChanceLoop(ctx, time.Hour)
		} else {
			log.Printf("entry: automatic fleet management disabled on this process (ENTRY_AUTOPILOT_ENABLED != 1)")
		}
	}

	// ── Admin plane: its own listener, never the public one ─
	adminMux := http.NewServeMux()
	authHandlers := auth.NewHandlers(pool)
	authHandlers.SetAdminKeys(adminKeys)
	authHandlers.SetAdminWebAuthn(adminWA)
	adminMux.HandleFunc("GET /api/auth/admin/csrf", auth.AdminCSRFToken)
	adminMux.HandleFunc("POST /api/auth/admin/login", authHandlers.AdminDirectLogin)
	adminMux.HandleFunc("POST /api/auth/admin/logout",
		admin.RequireAdmin(adminKeys.Public, pool, authHandlers.AdminDirectLogout))
	adminMux.HandleFunc("POST /api/auth/admin/refresh", authHandlers.AdminRefresh)
	adminMux.HandleFunc("POST /api/auth/admin/login/passkey", authHandlers.AdminLoginPasskey)
	adminMux.HandleFunc("POST /api/auth/admin/login/recovery", authHandlers.AdminLoginRecovery)
	adminMux.HandleFunc("POST /api/auth/admin/enroll/begin", authHandlers.AdminEnrollBegin)
	adminMux.HandleFunc("POST /api/auth/admin/enroll/finish", authHandlers.AdminEnrollFinish)
	adminSecurity := func(h http.HandlerFunc) http.HandlerFunc {
		return admin.RequireAdmin(adminKeys.Public, pool, h)
	}
	adminSecurityStepUp := func(h http.HandlerFunc) http.HandlerFunc {
		return admin.RequireAdmin(adminKeys.Public, pool, admin.RequireStepUp(h))
	}
	adminMux.HandleFunc("GET /api/v1/admin/security", adminSecurity(authHandlers.AdminSecurity))
	adminMux.HandleFunc("POST /api/v1/admin/security/step-up/begin", adminSecurity(authHandlers.AdminStepUpBegin))
	adminMux.HandleFunc("POST /api/v1/admin/security/step-up/finish", adminSecurity(authHandlers.AdminStepUpFinish))
	adminMux.HandleFunc("POST /api/v1/admin/security/passkeys/begin", adminSecurityStepUp(authHandlers.AdminPasskeyAddBegin))
	adminMux.HandleFunc("POST /api/v1/admin/security/passkeys/finish", adminSecurityStepUp(authHandlers.AdminPasskeyAddFinish))
	adminMux.HandleFunc("DELETE /api/v1/admin/security/passkeys/{id}", adminSecurityStepUp(authHandlers.AdminPasskeyDelete))
	adminMux.HandleFunc("POST /api/v1/admin/security/recovery-codes", adminSecurityStepUp(authHandlers.AdminRecoveryCodesRegenerate))
	adminMux.HandleFunc("POST /api/v1/admin/security/password", adminSecurityStepUp(authHandlers.AdminPasswordChange))
	go runAdminCeremonyPurger(ctx, pool)

	if store != nil {
		admin.RegisterRoutes(adminMux, admin.NewHandlers(pool, store), adminKeys, pool)
	}
	// Routing presets (hikkari-client engine/core/presets): editing reaches
	// no device; what changes what devices are served is the owner's.
	presets.RegisterAdminRoutes(adminMux, presets.NewAdminHandlers(presets.NewStore(pool), pool),
		func(next http.HandlerFunc) http.HandlerFunc {
			return admin.RequireAdmin(adminKeys.Public, pool, next)
		},
		func(next http.HandlerFunc) http.HandlerFunc {
			return admin.RequireAdmin(adminKeys.Public, pool, admin.RequireOwner(next))
		})
	entry.RegisterAdminRoutes(adminMux, entry.NewAdminHandlers(svc, prov, pool),
		func(next http.HandlerFunc) http.HandlerFunc {
			return admin.RequireAdmin(adminKeys.Public, pool, admin.RequireOwner(next))
		},
		func(next http.HandlerFunc) http.HandlerFunc {
			return admin.RequireAdmin(adminKeys.Public, pool, admin.RequireOwner(admin.RequireStepUp(next)))
		})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	adminSrv := newAdminServer(adminListenAddr(), adminMux)
	errc := make(chan error, 2)
	go func() { errc <- srv.ListenAndServe() }()
	go func() { errc <- adminSrv.ListenAndServe() }()
	log.Printf("hub: public on :%s, admin on %s", port, adminSrv.Addr)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	_ = adminSrv.Shutdown(shutdownCtx)
	return nil
}

// The per-box pulse tokens are derived from this master; a short master can
// be guessed offline even though the resulting tokens are full-length HMACs.
func validateFleetSecrets(getenv func(string) string) error {
	if v := getenv("EDGE_PULSE_TOKEN"); v != "" && len(v) < 32 {
		return errors.New("EDGE_PULSE_TOKEN must contain at least 32 random characters")
	}
	return nil
}
