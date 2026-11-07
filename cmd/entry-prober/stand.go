package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-control/internal/stand"
)

// The matrix stand on the hub's schedule (internal/stand): the hub queues a
// run of one knob over every relay of the fleet for this
// point every interval; this loop claims it, brings the client's kit (the core
// users run and their link parser) from the update channel, runs the stand's
// script here and sends the rows back. The rows are this point's by the token
// that sends them.
//
// One run at a time, profiles one after another: a run opens up to 50
// connections at once and takes minutes, and it must not crowd the routine
// rounds of the pool beside it.

// runStandOnly is the prober of a point that measures for the stand alone
// (ENTRY_PROBE_ROLE=stand): no xray, no pool, only the stand's runs.
func runStandOnly() {
	cfg := config{
		hub: hubClient{
			base:  strings.TrimRight(mustEnv("PROBE_INGEST_URL"), "/"),
			token: mustEnv("PROBE_INGEST_TOKEN"),
			http:  &http.Client{Timeout: 10 * time.Second},
		},
		stand: standFromEnv(),
	}
	cfg.stand.enabled = true
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("entry-prober: stand only, hub=%s kit=%s", cfg.hub.base, cfg.stand.kitURL)
	go cfg.serveUpdates(ctx)
	cfg.serveStand(ctx)
	log.Println("entry-prober: shutdown")
}

// standConfig is the stand's part of the environment.
type standConfig struct {
	enabled  bool
	kitURL   string
	dir      string
	poll     time.Duration
	download *http.Client
}

func standFromEnv() standConfig {
	return standConfig{
		enabled:  envStr("ENTRY_PROBE_STAND", "1") != "0",
		kitURL:   envStr("ENTRY_PROBE_STAND_KIT", "https://update.hikkari.net/stand/linux-amd64.json"),
		dir:      envStr("ENTRY_PROBE_STAND_DIR", "/tmp/hikkari-stand"),
		poll:     time.Duration(envInt("ENTRY_PROBE_STAND_POLL_SEC", 60)) * time.Second,
		download: &http.Client{Timeout: 10 * time.Minute},
	}
}

// standTask is a run the hub handed out (stand.Task).
type standTask = stand.Task

// standClaim takes this point's oldest queued run, or nil.
func (h hubClient) standClaim(ctx context.Context) (*standTask, error) {
	var t standTask
	code, err := h.do(ctx, http.MethodPost, "/stand/claim", map[string]string{}, &t)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	return &t, nil
}

// standFinish sends a run's result.
func (h hubClient) standFinish(ctx context.Context, id string, res stand.Result) (int, error) {
	var ans struct {
		Added int `json:"added"`
	}
	_, err := h.do(ctx, http.MethodPost, "/stand/"+id, res, &ans)
	return ans.Added, err
}

// serveStand claims and runs the stand's runs until ctx ends.
func (cfg config) serveStand(ctx context.Context) {
	if !cfg.stand.enabled {
		log.Printf("entry-prober: stand off (ENTRY_PROBE_STAND=0)")
		return
	}
	ticker := time.NewTicker(cfg.stand.poll)
	defer ticker.Stop()
	for {
		task, err := cfg.hub.standClaim(ctx)
		if err != nil {
			log.Printf("entry-prober: stand claim: %v", err)
		} else if task != nil {
			cfg.runStand(ctx, task)
			continue // there may be another knob queued
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (cfg config) runStand(ctx context.Context, task *standTask) {
	started := time.Now()
	kit, err := stand.Fetch(ctx, cfg.stand.download, cfg.stand.kitURL, cfg.stand.dir)
	var res stand.Result
	switch {
	case err != nil && kit.Core == "":
		// No kit at all: nothing this point runs would be the user's core.
		res = stand.Result{Error: err.Error()}
	default:
		if err != nil {
			log.Printf("entry-prober: stand: %v", err)
		}
		res = stand.Runner{Kit: kit, Log: log.Printf}.Run(ctx, *task)
	}
	// The result goes back even when ctx is ending: the rows are measured.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	added, err := cfg.hub.standFinish(fctx, task.ID.String(), res)
	if err != nil {
		log.Printf("entry-prober: stand %s %s: %d rows not recorded: %v", task.ID, task.Knob, len(res.Rows), err)
		return
	}
	log.Printf("entry-prober: stand %s %s: %d rows, %d new, %s, %s", task.ID, task.Knob, len(res.Rows), added,
		time.Since(started).Round(time.Second), orNone(res.Error))
}

func orNone(s string) string {
	if s == "" {
		return "no error"
	}
	return "error: " + s
}
