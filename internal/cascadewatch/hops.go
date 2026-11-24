package cascadewatch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/hoptrace"
)

type hopBoxes interface {
	Boxes
	StageHopTrace(context.Context, entry.Remote) (string, error)
}

// hopConfigs defines observations relative to the actual neighbouring boxes.
// The configuration is also stamped into each saved second so a changed
// topology cannot turn samples from the old path into apparent packet loss.
func hopConfigs(p entry.TrunkPair) (map[string]hoptrace.Config, error) {
	id := sha256.Sum256([]byte(p.Entry))
	key := fmt.Sprintf("%x", id[:8])
	nodes := make(map[string]hoptrace.Config)
	add := func(box, peer, address string) error {
		if host, _, err := net.SplitHostPort(address); err == nil {
			address = host
		}
		ip, err := netip.ParseAddr(address)
		if err != nil || !ip.Is4() {
			return fmt.Errorf("no IPv4 for hop trace peer %s", peer)
		}
		c := nodes[box]
		c.Key, c.Ports = key, p.HopPorts
		c.Peers = append(c.Peers, hoptrace.Peer{Name: peer, IP: ip})
		nodes[box] = c
		return nil
	}
	pointIP := make(map[string]string)
	for ip, box := range p.PointNames {
		pointIP[box] = ip
	}
	for _, point := range p.Points {
		for _, link := range [][3]string{
			{p.Entry, point, pointIP[point]}, {point, p.Entry, p.EntryAddress},
			{point, p.Exit, p.ExitAddress}, {p.Exit, point, pointIP[point]},
		} {
			if err := add(link[0], link[1], link[2]); err != nil {
				return nil, err
			}
		}
	}
	return nodes, nil
}

func (w *Watch) watchHops(ctx context.Context, boxes hopBoxes) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	staged := make(map[string]string)
	for ctx.Err() == nil {
		pairs, err := w.fleet.TrunkPairs(ctx)
		if err != nil {
			applog.Warn(ctx, "cascade", "hop trace pairs", applog.F("err", err.Error()))
		}
		for _, pair := range pairs {
			if len(pair.Points) == 0 || len(pair.HopPorts) == 0 {
				continue
			}
			if err := w.hopCycle(ctx, boxes, pair, staged); err != nil && ctx.Err() == nil {
				applog.Warn(ctx, "cascade", "hop trace", applog.F("entry", pair.Entry), applog.F("err", err.Error()))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Watch) hopCycle(ctx context.Context, boxes hopBoxes, p entry.TrunkPair, staged map[string]string) error {
	configs, err := hopConfigs(p)
	if err != nil {
		return err
	}
	incidents, err := w.store.pendingHops(ctx, p, w.now().Add(-5*time.Minute))
	if err != nil {
		return err
	}
	type capture struct {
		box, path string
		windows   map[int]hoptrace.Window
		err       error
	}
	results := make(chan capture, len(configs))
	for box, config := range configs {
		path := staged[box]
		go func() {
			windows := make(map[int]hoptrace.Window)
			path, err := hopRemote(ctx, boxes, box, path, config, incidents, windows)
			results <- capture{box: box, path: path, windows: windows, err: err}
		}()
	}
	byIncident := make([]map[string]hoptrace.Window, len(incidents))
	for i := range byIncident {
		byIncident[i] = make(map[string]hoptrace.Window)
	}
	for range configs {
		result := <-results
		staged[result.box] = result.path
		if result.err != nil && ctx.Err() == nil {
			applog.Warn(ctx, "cascade", "hop trace box", applog.F("box", result.box), applog.F("err", result.err.Error()))
		}
		for i, window := range result.windows {
			byIncident[i][result.box] = window
		}
	}
	for i, incident := range incidents {
		// A temporarily unreachable machine can still have its buffer when
		// SSH returns. File partial evidence before the six-minute ring rolls.
		if len(byIncident[i]) != len(configs) && w.now().Sub(incident.minute) < 4*time.Minute {
			continue
		}
		data := summarizeHops(p, incident, byIncident[i], configs)
		run := Run{TS: incident.minute, Entry: p.Entry, Exit: p.Exit, Layer: LayerEvent, Vantage: "entry", Target: "hop_trace", Data: data}
		if err := w.store.Insert(ctx, run); err != nil {
			return err
		}
		applog.Info(ctx, "cascade", "call gap hop trace", applog.F("entry", p.Entry), applog.F("exit", p.Exit), applog.F("trace", data))
	}
	return nil
}

func hopRemote(ctx context.Context, boxes hopBoxes, box, path string, config hoptrace.Config, incidents []hopIncident, windows map[int]hoptrace.Window) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	r, err := boxes.WatchRemote(ctx, box)
	if err != nil {
		return path, err
	}
	defer r.Close()
	if path == "" {
		path, err = boxes.StageHopTrace(ctx, r)
		if err != nil {
			return "", err
		}
	}
	encoded := config.Encode()
	// Read before reconciliation, which may restart a collector after a hub
	// rollout. The buffers are independent of the collector's process.
	var readErr error
	for i, incident := range incidents {
		from, until := incident.captureWindow()
		out, err := r.Run(ctx, fmt.Sprintf("%s hop-read %s %d %d", path, encoded, from, until), nil)
		var window hoptrace.Window
		if err == nil {
			err = json.Unmarshal([]byte(out), &window)
		}
		if err != nil {
			readErr = fmt.Errorf("read hop trace: %w", err)
			continue
		}
		windows[i] = window
	}
	if _, err := r.Run(ctx, "sh -s", strings.NewReader(hopService(path, config))); err != nil {
		return "", fmt.Errorf("start hop trace: %w", err)
	}
	return path, readErr
}

func hopService(path string, config hoptrace.Config) string {
	dir := hoptrace.Dir(config.Key)
	unit := "hikkari-hoptrace-" + config.Key
	encoded := config.Encode()
	return fmt.Sprintf(`set -eu
install -d -m 0700 %[1]s
touch %[1]s/lease
version=$(stat -c '%%i:%%Y' %[2]s)
wanted="%[3]s-$version"
if systemctl is-active --quiet %[4]s && [ "$(cat %[1]s/config 2>/dev/null)" = "$wanted" ]; then
    exit 0
fi
if systemctl is-active --quiet %[4]s; then
    systemctl stop %[4]s
fi
systemd-run --quiet --collect --unit=%[4]s --property=Type=exec --property=Restart=on-failure --property=RestartSec=2s --property=Nice=10 --property=MemoryMax=64M --property=NoNewPrivileges=yes --property=CapabilityBoundingSet=CAP_NET_RAW --property=ProtectSystem=strict --property=ReadWritePaths=%[1]s --setenv=GOMEMLIMIT=32MiB %[2]s hop-capture %[3]s
printf '%%s\n' "$wanted" > %[1]s/config
`, dir, path, encoded, unit)
}

type hopIncident struct {
	minute time.Time
	at     time.Time
	gap    time.Duration
}

func (i hopIncident) end() time.Time { return i.at.Add(min(i.gap, 20*time.Second)) }

func (i hopIncident) captureWindow() (int64, int64) {
	return i.at.Unix() - 3, i.end().Unix() + 4
}

// Run after call rows reached the DB so a hub restart between recording a
// call and querying the four observers does not lose the trigger.
func (s *Store) pendingHops(ctx context.Context, p entry.TrunkPair, since time.Time) ([]hopIncident, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.ts, c.data->'worst'->0 FROM trunk_runs c
		WHERE c.entry_box=$1 AND c.exit_box=$2 AND c.ts >= $3
		AND c.layer='call' AND c.target='copies1' AND (c.data->>'gap_max_ms')::float8 >= $4::integer
		AND NOT EXISTS (SELECT 1 FROM trunk_runs d WHERE d.entry_box=c.entry_box AND d.exit_box=c.exit_box
		    AND d.ts=c.ts AND d.layer='event' AND d.target='hop_trace')
		ORDER BY c.ts`, p.Entry, p.Exit, since, hopTraceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var incidents []hopIncident
	for rows.Next() {
		var minute time.Time
		var raw []byte
		if err := rows.Scan(&minute, &raw); err != nil {
			return nil, err
		}
		var gap struct {
			At time.Time `json:"at"`
			MS int64     `json:"ms"`
		}
		if err := json.Unmarshal(raw, &gap); err == nil && !gap.At.IsZero() && gap.MS >= hopTraceMs {
			incidents = append(incidents, hopIncident{minute, gap.At, time.Duration(gap.MS) * time.Millisecond})
		}
	}
	return incidents, rows.Err()
}
