package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
		delivered, err := cfg.flushStandResult(ctx)
		if !delivered {
			log.Printf("entry-prober: pending result: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
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
	// Persist before attempting the network. A hub outage or worker restart
	// must not silently discard a completed measurement.
	pending := pendingStandResult{ID: task.ID.String(), Result: res}
	if err := cfg.saveStandResult(pending); err != nil {
		log.Printf("entry-prober: cannot persist stand %s: %v; attempting immediate delivery", task.ID, err)
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := cfg.hub.standFinish(fctx, pending.ID, res); err != nil {
			log.Printf("entry-prober: stand %s delivery failed without durable storage: %v", task.ID, err)
		}
		return
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := cfg.flushStandResult(fctx); err != nil {
		log.Printf("entry-prober: stand result queued for retry: %v", err)
	}
	log.Printf("entry-prober: stand %s %s measured in %s, %s", task.ID, task.Knob, time.Since(started).Round(time.Second), orNone(res.Error))
}

type pendingStandResult struct {
	ID     string       `json:"id"`
	Result stand.Result `json:"result"`
}

func (cfg config) standResultPath() string {
	return filepath.Join(cfg.stand.dir, "pending-result.json")
}
func (cfg config) saveStandResult(pending pendingStandResult) error {
	if err := os.MkdirAll(cfg.stand.dir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(cfg.stand.dir, ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), cfg.standResultPath())
}
func (cfg config) flushStandResult(ctx context.Context) (bool, error) {
	raw, err := os.ReadFile(cfg.standResultPath())
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var pending pendingStandResult
	if err := json.Unmarshal(raw, &pending); err != nil {
		return false, fmt.Errorf("invalid pending result: %w", err)
	}
	var ans struct {
		Added int `json:"added"`
	}
	code, err := cfg.hub.do(ctx, http.MethodPost, "/stand/"+pending.ID, pending.Result, &ans)
	if err != nil && code != http.StatusConflict {
		return false, err
	}
	if code == http.StatusConflict {
		log.Printf("entry-prober: result %s expired or already acknowledged", pending.ID)
	}
	if err := os.Remove(cfg.standResultPath()); err != nil {
		return false, err
	}
	return true, nil
}

func orNone(s string) string {
	if s == "" {
		return "no error"
	}
	return "error: " + s
}
