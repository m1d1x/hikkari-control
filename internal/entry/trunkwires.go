package entry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The trunk's other wires (hikkari-client plans/2026-11-11-trunk-wires-stage4.md,
// шаги 3 и 6): besides the pool, members of other forms on UDP ports of the
// exit, and the entry's set of wires, their members and whether it steers by
// its own measurements. Two halves, as the trunk itself:
//
//	cmd/server entry-box trunk-wires <exit-box> udp=20447,quic=20448,salamander=20449|off
//	    the exit's relays serve those wires (EDGE_HIKKARI_TRUNK_WIRES, a
//	    reconfigure, which reloads the relay without a cut); ufw opens them
//	cmd/server entry-box trunk-entry-wires <entry-box> <exit-box> udp:2,quic:4,salamander:4|off [steer]
//	    the entry dials those wires (a file it reads every 10 s: no new
//	    process, no handoff); steer lets its selector choose the route, and
//	    without it every class goes on any member, as with the pool alone
//
// The wire kinds are the bench hour's leaders (11.11): udp for voice (no red
// minute of its call), quic and salamander for bulk (264 and 271 Mbit/s for
// 12 MiB against the pool's 162). awg is not offered: the slowest and the
// dearest in CPU.

// TrunkWireKinds are the wires an exit can serve.
var TrunkWireKinds = []string{"udp", "quic", "salamander"}

// TrunkEntryWiresFile is the entry's wires, read by the running process.
const TrunkEntryWiresFile = "/etc/nekko-trunk-entry.wires.json"

func knownWireKind(kind string) bool {
	for _, k := range TrunkWireKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// trunkWiresEnv renders EDGE_HIKKARI_TRUNK_WIRES, kinds in order.
func trunkWiresEnv(w map[string]int) string {
	kinds := make([]string, 0, len(w))
	for k := range w {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k+"="+strconv.Itoa(w[k]))
	}
	return strings.Join(out, ",")
}

// ParseTrunkWires reads kind=port,... ("off" or empty: none).
func ParseTrunkWires(raw string) (map[string]int, error) {
	out := map[string]int{}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "off" {
		return out, nil
	}
	ports := map[int]bool{}
	for _, item := range strings.Split(raw, ",") {
		kind, p, ok := strings.Cut(strings.TrimSpace(item), "=")
		port, err := strconv.Atoi(p)
		if !ok || err != nil || port < 1024 || port > 65535 {
			return nil, fmt.Errorf("%w: провод %q, нужно вид=порт (порт 1024-65535)", ErrRejected, item)
		}
		if !knownWireKind(kind) {
			return nil, fmt.Errorf("%w: провод %q, бывают %s", ErrRejected, kind, strings.Join(TrunkWireKinds, ", "))
		}
		if _, dup := out[kind]; dup || ports[port] {
			return nil, fmt.Errorf("%w: %q дважды", ErrRejected, item)
		}
		out[kind], ports[port] = port, true
	}
	return out, nil
}

// SetTrunkWires gives the Hikkari relays with a trunk on box the wires
// (none takes them away). It changes what the hub holds; Reconfigure puts it
// on the box, OpenTrunkWirePorts opens the ports.
func (s *Service) SetTrunkWires(ctx context.Context, box string, wires map[string]int) ([]string, error) {
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
		next.TrunkWires = nil
		if len(wires) > 0 {
			next.TrunkWires = map[string]int{}
			for k, v := range wires {
				if v == rel.id.Port {
					return nil, fmt.Errorf("%w: порт %d занят самим релеем %s", ErrRejected, v, rel.name)
				}
				next.TrunkWires[k] = v
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
		return nil, fmt.Errorf("%w: на %s %d релея с магистралью, провода только у одного", ErrRejected, box, len(changed))
	}
	applog.Info(ctx, "entry", "operator trunk wires", applog.F("box", box), applog.F("wires", trunkWiresEnv(wires)))
	return changed, nil
}

const trunkWirePortsScript = `set -eu
if ! command -v ufw >/dev/null 2>&1 || ! ufw status | grep -q "Status: active"; then
	echo "ufw не стоит или выключен, порты не трогаю"
	exit 0
fi
for port in $CLOSE; do ufw delete allow "$port/udp" >/dev/null 2>&1 || true; done
for port in $OPEN; do ufw allow "$port/udp" comment nekko-trunk-wire >/dev/null; done
echo "ufw: открыто ${OPEN:-ничего}, закрыто ${CLOSE:-ничего}"
`

// TrunkWiresOf is what the relay with a trunk on box serves now.
func (s *Service) TrunkWiresOf(ctx context.Context, box string) (map[string]int, error) {
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return nil, err
	}
	for _, rel := range relays {
		if h := rel.id.Hikkari; h != nil && h.TrunkKey != "" {
			return h.TrunkWires, nil
		}
	}
	return nil, nil
}

// OpenTrunkWirePorts opens the wires' UDP ports on box in ufw and closes
// the ones it served before.
func (p *Provisioner) OpenTrunkWirePorts(ctx context.Context, box string, open, closed map[string]int) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	list := func(m map[string]int, skip map[string]int) string {
		var out []string
		for k, v := range m {
			if skip != nil && skip[k] == v {
				continue
			}
			out = append(out, strconv.Itoa(v))
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	cmd := fmt.Sprintf("OPEN=%q CLOSE=%q bash -s", list(open, nil), list(closed, open))
	out, err := r.Run(ctx, cmd, strings.NewReader(trunkWirePortsScript))
	if err != nil {
		return fmt.Errorf("ufw на %s: %w: %s", box, err, out)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(string(out)), applog.F("box", box))
	return nil
}

// TrunkEntryWire is one wire of the entry's file.
type TrunkEntryWire struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Port    int    `json:"port"`
	Members int    `json:"members"`
}

// TrunkEntryWires is the entry's file (trunkentry/wires.go reads it).
type TrunkEntryWires struct {
	Steer bool             `json:"steer"`
	Wires []TrunkEntryWire `json:"wires"`
}

// ParseTrunkEntryWires reads kind:members,... ("off": none).
func ParseTrunkEntryWires(raw string) (map[string]int, error) {
	out := map[string]int{}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "off" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		kind, m, ok := strings.Cut(strings.TrimSpace(item), ":")
		n, err := strconv.Atoi(m)
		if !ok || err != nil || n < 1 || n > 32 {
			return nil, fmt.Errorf("%w: %q, нужно вид:члены (1-32)", ErrRejected, item)
		}
		if !knownWireKind(kind) {
			return nil, fmt.Errorf("%w: провод %q, бывают %s", ErrRejected, kind, strings.Join(TrunkWireKinds, ", "))
		}
		if _, dup := out[kind]; dup {
			return nil, fmt.Errorf("%w: %q дважды", ErrRejected, kind)
		}
		out[kind] = n
	}
	return out, nil
}

// trunkEntryWiresFor is the entry's file for the exit's wires: every wire
// asked for must be one the exit serves.
func trunkEntryWiresFor(exit map[string]int, members map[string]int, steer bool) (TrunkEntryWires, error) {
	f := TrunkEntryWires{Steer: steer, Wires: []TrunkEntryWire{}}
	for _, kind := range TrunkWireKinds {
		n, ok := members[kind]
		if !ok {
			continue
		}
		port, served := exit[kind]
		if !served {
			return f, fmt.Errorf("%w: выход не держит провод %s, сначала entry-box trunk-wires", ErrRejected, kind)
		}
		f.Wires = append(f.Wires, TrunkEntryWire{Name: kind, Kind: kind, Port: port, Members: n})
	}
	return f, nil
}

// SetTrunkEntryWires writes the entry's wires on entryBox, for the exit's
// on exitBox. The running entry picks them up within 10 s.
func (p *Provisioner) SetTrunkEntryWires(ctx context.Context, entryBox, exitBox string, members map[string]int, steer bool) error {
	relays, err := p.svc.relayIdentities(ctx, exitBox)
	if err != nil {
		return err
	}
	var exit map[string]int
	for _, rel := range relays {
		if h := rel.id.Hikkari; h != nil && h.TrunkKey != "" {
			exit = h.TrunkWires
			break
		}
	}
	f, err := trunkEntryWiresFor(exit, members, steer)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	r, _, err := p.provokeRemote(ctx, entryBox)
	if err != nil {
		return err
	}
	cmd := "umask 022; cat > " + TrunkEntryWiresFile + ".new && mv " + TrunkEntryWiresFile + ".new " + TrunkEntryWiresFile
	if out, err := r.Run(ctx, cmd, strings.NewReader(string(raw)+"\n")); err != nil {
		return fmt.Errorf("файл проводов на %s: %w: %s", entryBox, err, out)
	}
	applog.Info(ctx, "entry", "operator trunk entry wires", applog.F("box", entryBox), applog.F("wires", trunkWiresEnv(members)), applog.F("steer", steer))
	return nil
}
