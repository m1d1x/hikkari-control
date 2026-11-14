package entry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The trunk's Rust exit (hk-exit) under a box's Hikkari relay (roadmap 4.6,
// R6.4.4; hikkari-client plans/2026-11-11-entry-hot-path-and-unit-economics.md,
// «Труба на Rust»; owner's decision of 2026-11-14): every wire of an entry
// over hk-pipe ends in one process, the relay its brain (edge/trunk_pipe.go).
// It has a trunk key of its own, so the Go entry of people, on the relay's
// Go exit by TrunkKey, and the stand over hk-pipe, on hk-exit by
// TrunkPipeKey, never reach one exit both; the loopback ports open to the
// trunk are the relay's TrunkPorts for both. hk-exit's binary goes beside
// nekko-edge with every deploy of such a relay (jobRun.stageExit).
//
//	cmd/server entry-box trunk-pipe-exit <exit-box> udp=<port>[,h3dgram=<port>]|off

// trunkPipeExitKinds are the wires hk-exit serves (edge trunk_pipe.go).
var trunkPipeExitKinds = map[string]bool{"udp": true, "h3dgram": true}

// ParseTrunkPipeExitWires reads hk-exit's wires, kind=port,... of udp and
// h3dgram ("off" or empty: none, hk-exit off).
func ParseTrunkPipeExitWires(raw string) (map[string]int, error) {
	return parseWirePorts(raw, func(k string) bool { return trunkPipeExitKinds[k] }, []string{"udp", "h3dgram"})
}

// SetTrunkPipeExit makes the Hikkari relay with a trunk on box the brain of
// hk-exit with these wires, kind to UDP port, keeping a key it already has;
// no wires takes hk-exit off. It changes what the hub holds; Reconfigure
// puts it on the box.
func (s *Service) SetTrunkPipeExit(ctx context.Context, box string, wires map[string]int) ([]string, error) {
	for kind, port := range wires {
		if !trunkPipeExitKinds[kind] {
			return nil, fmt.Errorf("%w: провод %q, у hk-exit только udp и h3dgram", ErrRejected, kind)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: порт %d", ErrRejected, port)
		}
	}
	if len(wires) == 2 && wires["udp"] == wires["h3dgram"] {
		return nil, fmt.Errorf("%w: udp и h3dgram на одном порту %d", ErrRejected, wires["udp"])
	}
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, rel := range relays {
		h := rel.id.Hikkari
		if h == nil || h.TrunkKey == "" {
			continue
		}
		next := *h
		if len(wires) == 0 {
			next.TrunkPipeKey, next.TrunkPipeWires = "", nil
		} else {
			for kind, port := range wires {
				if port == rel.id.Port {
					return nil, fmt.Errorf("%w: порт %d занят самим релеем %s", ErrRejected, port, rel.name)
				}
				for goKind, goPort := range h.TrunkWires {
					if goPort == port {
						return nil, fmt.Errorf("%w: порт %d провода %s занят проводом %s Go-выхода", ErrRejected, port, kind, goKind)
					}
				}
			}
			if next.TrunkPipeKey == "" {
				var k [24]byte
				if _, err := rand.Read(k[:]); err != nil {
					return nil, err
				}
				next.TrunkPipeKey = hex.EncodeToString(k[:])
			}
			next.TrunkPipeWires = map[string]int{}
			for kind, port := range wires {
				next.TrunkPipeWires[kind] = port
			}
		}
		id := rel.id
		id.Hikkari = &next
		if err := s.setRelayIdentity(ctx, rel.name, id); err != nil {
			return nil, fmt.Errorf("релей %s: %w", rel.name, err)
		}
		changed = append(changed, rel.name)
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("%w: на %s нет Hikkari-релея с магистралью, сначала entry-box trunk-exit", ErrRejected, box)
	}
	if len(changed) > 1 && len(wires) > 0 {
		// One socket per port on the box: two relays cannot both bind them.
		return nil, fmt.Errorf("%w: на %s %d релея с магистралью, hk-exit только у одного", ErrRejected, box, len(changed))
	}
	applog.Info(ctx, "entry", "operator trunk pipe exit", applog.F("box", box), applog.F("wires", trunkWiresEnv(wires)))
	return changed, nil
}

// TrunkPipeWiresOf is what hk-exit under the relay on box serves now, kind
// to UDP port (nil: no hk-exit).
func (s *Service) TrunkPipeWiresOf(ctx context.Context, box string) (map[string]int, error) {
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return nil, err
	}
	for _, rel := range relays {
		if h := rel.id.Hikkari; h != nil && h.TrunkPipeKey != "" {
			return h.TrunkPipeWires, nil
		}
	}
	return nil, nil
}

// trunkPipeExitOf finds the Hikkari relay on box that is hk-exit's brain,
// for the stand over hk-pipe: its address, SNI and pipe key, and the ports
// open to the trunk.
func (s *Service) trunkPipeExitOf(ctx context.Context, box string) (addr, sni, key string, ports map[int]bool, err error) {
	acc, err := s.boxAccess(ctx, box)
	if err != nil {
		return "", "", "", nil, err
	}
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return "", "", "", nil, err
	}
	for _, rel := range relays {
		if h := rel.id.Hikkari; h != nil && h.TrunkPipeKey != "" {
			ports = map[int]bool{}
			for _, p := range h.TrunkPorts {
				ports[p] = true
			}
			return net.JoinHostPort(acc.address, strconv.Itoa(rel.id.Port)), h.Host, h.TrunkPipeKey, ports, nil
		}
	}
	return "", "", "", nil, fmt.Errorf("%w: у %s нет hk-exit под релеем, сначала entry-box trunk-pipe-exit", ErrRejected, box)
}
