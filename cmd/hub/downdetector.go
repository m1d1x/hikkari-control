package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	dd "github.com/m1d1x/hikkari-control/internal/downdetector"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// The hub owns the fleet registry and the anonymous aggregation process.
// Reports never cross a new trust boundary or get copied into ClickHouse.
type fleetSignals struct {
	store  *dd.Store
	prober *dd.Prober
	k      int
}

func (f *fleetSignals) FleetSignals(_ context.Context, nodes []entry.FleetNode, now time.Time) (map[string]entry.NodeSignal, error) {
	out := map[string]entry.NodeSignal{}
	for _, n := range nodes {
		id := dd.NodeID(n.Slug)
		out[n.Relay] = entry.NodeSignal{ID: id, At: now, Verdict: f.store.FleetVerdict(id, f.prober.External(id), now, n.BoundAt, f.k)}
	}
	return out, nil
}
func nodeTargets(nodes []entry.FleetNode) []dd.Target {
	out := make([]dd.Target, 0, len(nodes))
	for _, n := range nodes {
		if n.Proto==entry.ProtoHysteria2 {continue} // UDP cannot be judged by a TCP connect.
		out = append(out, dd.Target{ID: dd.NodeID(n.Slug), Name: n.Slug, Host: n.Host, Port: n.Port})
	}
	return out
}
func wireDowndetector(ctx context.Context, svc *entry.Service, mux *http.ServeMux) (func(), error) {
	path := os.Getenv("HUB_DOWNDETECTOR_CONFIG")
	if path == "" {
		return func() {}, nil
	}
	cfg, err := dd.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	secrets, err := dd.LoadSecrets(cfg.Secrets)
	if err != nil {
		return nil, err
	}
	database, err := dd.OpenDB(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	store := dd.NewStore()
	prober := dd.NewProber(nil)
	source := &fleetSignals{store: store, prober: prober, k: cfg.K}
	svc.SetNodeSignals(source)
	server := &dd.Server{Store: store, Prober: prober, DB: database, Auth: dd.NewAuth(secrets, cfg.TokenRatePerMin), K: cfg.K, OverviewMaxHours: cfg.OverviewMaxHours}
	mux.Handle("/v1/", server.Handler())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(dd.ProbeInterval)
		defer tick.Stop()
		flush := func(all bool) {
			for hour, cells := range store.TakeHours(time.Now(), all) {
				if err := database.WriteHour(context.Background(), hour, dd.Rollup(hour, cells, cfg.K)); err != nil {
					log.Print("downdetector: aggregate write failed")
				}
			}
		}
		for {
			nodes, err := svc.FleetNodes(ctx)
			if err == nil {
				prober.ReplaceTargets(append(cfg.Targets(), nodeTargets(nodes)...))
				prober.Round(ctx)
			} else if ctx.Err() == nil {
				log.Print("downdetector: fleet registry unavailable")
			}
			flush(false)
			select {
			case <-ctx.Done():
				flush(true)
				return
			case <-tick.C:
			}
		}
	}()
	return func() {
		<-stopped
		if err := database.Close(); err != nil {
			log.Print(fmt.Errorf("downdetector: close aggregates: %w", err))
		}
	}, nil
}
