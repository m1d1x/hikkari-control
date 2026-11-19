package entry

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

// The entry's matrix of silences (trunkentry matrix.go; hikkari-client plans
// research 2026-11-19-entry-on-ams3-after-path-silence.md): besides the
// trunk, the brain pings its receivers, the probers inside Russia and two
// neutral hosts in the EU, and says whose each silence of a receiver was.
// The hub gives it the targets by name (EDGE_TRUNK_MATRIX).

// matrixNeutral are the EU hosts outside the fleet the entry pings every
// 100 ms: a RIPE Atlas anchor (there to be pinged) in Frankfurt and
// Leaseweb's speed test in Amsterdam, two so that one's own hiccup is not
// read as the border. 19.11 from aezamsk1: 34.9 and 36.9 ms.
var matrixNeutral = []struct{ name, ip string }{
	{"eu-fra-ripe", "217.243.20.18"},
	{"eu-ams-leaseweb", "95.211.20.80"},
}

const (
	matrixRxEvery = 20  // ms: our receivers and probers, as the trunk's call
	matrixEUEvery = 100 // ms: hosts not ours
)

// trunkMatrixEnv is EDGE_TRUNK_MATRIX for an entry whose receivers are
// named (exit first) at ips, with the active Russian probers.
func (s *Service) trunkMatrixEnv(ctx context.Context, names, ips []string) (string, error) {
	var items []string
	for i, name := range names {
		items = append(items, fmt.Sprintf("rx:%s=%s/%d", name, ips[i], matrixRxEvery))
	}
	slots, err := s.VantageSlots(ctx)
	if err != nil {
		return "", err
	}
	sort.Slice(slots, func(a, b int) bool { return slots[a].Slot < slots[b].Slot })
	for _, v := range slots {
		if !strings.HasPrefix(v.Slot, "ru-") || v.Status != "active" || v.Host == "" {
			continue
		}
		host := v.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if a, err := netip.ParseAddr(host); err == nil && a.Is4() {
			items = append(items, fmt.Sprintf("ru:%s=%s/%d", v.Slot, a, matrixRxEvery))
		}
	}
	for _, n := range matrixNeutral {
		items = append(items, fmt.Sprintf("eu:%s=%s/%d", n.name, n.ip, matrixEUEvery))
	}
	return "EDGE_TRUNK_MATRIX=" + strings.Join(items, ",") + "\n", nil
}
