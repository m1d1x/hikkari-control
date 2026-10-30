// Command downdetector runs the Hikkari downdetector server: report intake,
// live verdicts, the outside prober and the hourly rollup to SQLite.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	dd "github.com/m1d1x/hikkari-control/internal/downdetector"
)

const rollupEvery = 5 * time.Minute

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to the YAML config")
	flag.Parse()
	if err := run(*cfgPath); err != nil {
		log.Fatal(err)
	}
}

func run(cfgPath string) error {
	cfg, err := dd.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	secrets, err := dd.LoadSecrets(cfg.Secrets)
	if err != nil {
		return err
	}
	db, err := dd.OpenDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := dd.NewStore()
	prober := dd.NewProber(cfg.Targets())
	go prober.Run(ctx)

	srv := &dd.Server{
		Store:            store,
		Prober:           prober,
		DB:               db,
		Auth:             dd.NewAuth(secrets, cfg.TokenRatePerMin),
		K:                cfg.K,
		OverviewMaxHours: cfg.OverviewMaxHours,
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		// net/http's own error log prints client addresses; keep it silent.
		ErrorLog: log.New(io.Discard, "", 0),
	}

	rolled := make(chan struct{})
	go func() {
		defer close(rolled)
		t := time.NewTicker(rollupEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				flush(db, store, cfg.K, false)
			}
		}
	}()

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	log.Printf("downdetector: listening on %s, k=%d, %d registry nodes", cfg.Listen, cfg.K, len(cfg.Nodes))

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	<-rolled
	// Whatever is still in memory would be lost anyway: write it out.
	flush(db, store, cfg.K, true)
	return nil
}

func flush(db *dd.DB, store *dd.Store, k int, all bool) {
	for hour, cells := range store.TakeHours(time.Now(), all) {
		if err := db.WriteHour(context.Background(), hour, dd.Rollup(hour, cells, k)); err != nil {
			log.Printf("downdetector: rollup of hour %d failed: %v", hour, err)
		}
	}
}
