package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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
		reported := reportedID(n)
		out[n.Relay] = entry.NodeSignal{ID: id, At: now, Verdict: f.store.FleetVerdict(reported, f.prober.External(id), now, n.BoundAt, f.k)}
	}
	return out, nil
}

// reportedID is the id clients report a node under: the hash of its link,
// where the outside probe uses its slug. A node with no client id falls back
// to the slug, which only the probe fills.
func reportedID(n entry.FleetNode) string {
	if id := dd.FleetClientID(n.Host, n.Port, n.Proto); id != "" {
		return id
	}
	return dd.NodeID(n.Slug)
}

// intakeLine is what clients reported lately, in totals, and how each node of
// ours fares in it: the operator's proof that reports reach the hub and land
// on the nodes, which nothing else shows, since reports are never stored per
// install. Node slugs are ours; nothing in the line names a client.
func intakeLine(store *dd.Store, nodes []entry.FleetNode, now time.Time) string {
	installs, servers := store.Intake(now)
	var b strings.Builder
	fmt.Fprintf(&b, "installs=%d servers=%d", installs, servers)
	for _, n := range nodes {
		g := store.TunnelTotal(reportedID(n), now, n.BoundAt)
		fmt.Fprintf(&b, " %s=%d/%d/%d", n.Slug, g.Installs, g.OK, g.Fail)
	}
	return b.String()
}

func nodeTargets(nodes []entry.FleetNode) []dd.Target {
	out := make([]dd.Target, 0, len(nodes))
	for _, n := range nodes {
		if n.Proto == entry.ProtoHysteria2 || n.Proto == entry.ProtoAmneziaWG {
			continue
		} // UDP cannot be judged by a TCP connect.
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
	source := &fleetSignals{store: store, prober: prober, k: cfg.FleetMinInstalls()}
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
		lastIntake := ""
		for {
			nodes, err := svc.FleetNodes(ctx)
			if err == nil {
				prober.ReplaceTargets(append(cfg.Targets(), nodeTargets(nodes)...))
				prober.Round(ctx)
				// Logged when it changes, not every round: a quiet fleet
				// writes one line, a live one a line per new window.
				if line := intakeLine(store, nodes, time.Now()); line != lastIntake {
					log.Printf("downdetector: intake %s (node=installs/ok/fail)", line)
					lastIntake = line
				}
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
