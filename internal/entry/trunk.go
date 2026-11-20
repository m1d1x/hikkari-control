package entry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The cascade's trunk (hikkari-protocol trunk, spec/trunk.md; hikkari-client
// plans/2026-11-05-ru-cascade.md, шаг 2), stood by the hub.
//
// The exit end is part of a Hikkari relay abroad: its identity carries a
// trunk key and the loopback ports the trunk may reach, its env says them to
// the edge (EDGE_HIKKARI_TRUNK_*), and the relay is reconfigured like any
// other change of its env. The entry end is experiment equipment on a box in
// Russia, like the provoke receiver: its own unit and binary
// (hikkari-control/trunkentry, members dialled by Chromium) with the
// libcronet.so it loads, no relay record, no subscriber, no pulse. What was
// stood is kept in trunk_entries, which the cascade watch reads
// (cascade_watch.go, internal/cascadewatch).
//
//	cmd/server entry-box trunk-exit <box> <port,...>      # trunk on the box's Hikkari relays
//	cmd/server entry-box trunk-exit-off <box>             # trunk off
//	cmd/server entry-box trunk-entry <box> <exit-box> <listen=port,...> [members] [pipe [h3dgram=<port>[:proxy]] [voice=<wire>]]
//	cmd/server entry-box trunk-entry-off <box>
//
// pipe stands the entry as the brain over hk-pipe (roadmap 4.6; trunkentry
// brain_linux.go) to hk-exit under the exit's relay by its pipe key
// (trunkpipeexit.go), with the stand's wires (trunkpipe.go): the same unit,
// ports and book of source ports, so the people's entry moves to the pipe
// and back by this one command. A move between the Go entry and the pipe is
// a restart (the two hand nothing to each other, trunkentry handoff.go), a
// cut of 1-3 s for everyone through the entry; within the pipe it is a
// reload, nothing cut.

const (
	trunkEntryUnit = "nekko-trunk-entry"
	// trunkEntryDir holds the binary and libcronet.so side by side: the
	// binary loads the library from its own directory.
	trunkEntryDir     = "/usr/local/lib/nekko-trunk-entry"
	trunkEntryBinary  = trunkEntryDir + "/nekko-trunk-entry"
	trunkEntryEnvFile = "/etc/nekko-trunk-entry.env"
	// DefaultTrunkMembers is the pool of the cascade plan (16–64 members).
	DefaultTrunkMembers = 32
)

// SetTrunkExit gives every Hikkari relay on box a trunk reaching ports on
// its loopback, keeping a key it already has; no ports takes the trunk off.
// It changes what the hub holds; Reconfigure puts it on the box.
func (s *Service) SetTrunkExit(ctx context.Context, box string, ports []int) ([]string, error) {
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("%w: порт %d", ErrRejected, p)
		}
	}
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, rel := range relays {
		h := rel.id.Hikkari
		if h == nil {
			continue
		}
		next := *h
		if len(ports) == 0 {
			next.TrunkKey, next.TrunkPorts = "", nil
		} else {
			if next.TrunkKey == "" {
				var k [24]byte
				if _, err := rand.Read(k[:]); err != nil {
					return nil, err
				}
				next.TrunkKey = hex.EncodeToString(k[:])
			}
			next.TrunkPorts = append([]int(nil), ports...)
			sort.Ints(next.TrunkPorts)
		}
		id := rel.id
		id.Hikkari = &next
		if err := s.setRelayIdentity(ctx, rel.name, id); err != nil {
			return nil, fmt.Errorf("релей %s: %w", rel.name, err)
		}
		changed = append(changed, rel.name)
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("%w: на %s нет живых Hikkari-релеев, магистрали выходить некуда", ErrRejected, box)
	}
	applog.Info(ctx, "entry", "operator trunk exit", applog.F("box", box), applog.F("ports", ports))
	return changed, nil
}

// TrunkEntryOptions are what an entry is stood with.
type TrunkEntryOptions struct {
	// Exit is the box whose Hikkari relay the trunk ends at.
	Exit string
	// Forward maps a port the entry listens on (TCP and UDP) to the exit's
	// loopback port it reaches; each must be open on the exit.
	Forward map[int]int
	// Members is the pool's size.
	Members int
	// HikkariUDPCopies is how many members carry each datagram on the ports
	// that reach the exit's Hikkari relay: 1 (the default, 0) since v1.02
	// clients copy their calls themselves, 2 as on any other port (to
	// compare, or for clients without v1.02).
	HikkariUDPCopies int
	// Pipe stands the entry over hk-pipe to the exit's hk-exit, with
	// Chromium's QUIC wire at H3Port (H3Usage its members' session usage)
	// and voice held on Voice when set, as the stand's (TrunkPipeOptions).
	Pipe    bool
	H3Port  int
	H3Usage string
	Voice   string
	// Receivers are the group's other receivers (hikkari-client
	// plans/2026-11-16-second-receiver.md, P5): boxes whose Hikkari relay is
	// Exit's again (receiver-spare), the entry over hk-pipe holds the trunk
	// to them warm and moves off a dead receiver by itself
	// (EDGE_TRUNK_RECEIVERS). ReceiverPool is the pool members of each
	// standby (0: the brain's own, 8).
	Receivers    []string
	ReceiverPool int
}

// trunkExitOf finds the Hikkari relay with a trunk on box, for an entry.
func (s *Service) trunkExitOf(ctx context.Context, box string) (addr, sni, key string, ports map[int]bool, err error) {
	acc, err := s.boxAccess(ctx, box)
	if err != nil {
		return "", "", "", nil, err
	}
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return "", "", "", nil, err
	}
	for _, rel := range relays {
		if h := rel.id.Hikkari; h != nil && h.TrunkKey != "" {
			ports = map[int]bool{}
			for _, p := range h.TrunkPorts {
				ports[p] = true
			}
			return net.JoinHostPort(acc.address, strconv.Itoa(rel.id.Port)), h.Host, h.TrunkKey, ports, nil
		}
	}
	return "", "", "", nil, fmt.Errorf("%w: у %s нет Hikkari-релея с магистралью, сначала entry-box trunk-exit", ErrRejected, box)
}

// CascadeVia is what a subscription dialling exitBox through the trunk entry
// on entryBox needs: the entry's address, and the Hikkari relays on exitBox
// whose port the trunk reaches (their records keep SNI and key, only the
// address and port the client dials change). No relay is an error: such a
// cascade would serve an empty subscription.
func (s *Service) CascadeVia(ctx context.Context, entryBox, exitBox string) (string, map[string]bool, error) {
	acc, err := s.boxAccess(ctx, entryBox)
	if err != nil {
		return "", nil, err
	}
	if acc.retired {
		return "", nil, fmt.Errorf("%w: %s", ErrBoxRetired, entryBox)
	}
	relays, err := s.relayIdentities(ctx, exitBox)
	if err != nil {
		return "", nil, err
	}
	out := map[string]bool{}
	for _, rel := range relays {
		h := rel.id.Hikkari
		if h == nil || h.TrunkKey == "" || !slices.Contains(h.TrunkPorts, rel.id.Port) {
			continue
		}
		out[rel.name] = true
	}
	if len(out) == 0 {
		return "", nil, fmt.Errorf("%w: на %s нет Hikkari-релея, чей порт открыт магистрали", ErrRejected, exitBox)
	}
	return acc.address, out, nil
}

// trunkEntryEnv renders the entry's env. CONTAINS THE TRUNK KEY. addr is the
// exit relay's address and port: a listen port forwarded to that port reaches
// the Hikkari relay itself, whose v1.02 clients copy their datagrams over both
// channels, so its datagrams cross the trunk once (EDGE_TRUNK_UDP_ONE_COPY;
// plan, «Hikkari v1.02 за входом», п. 3).
func trunkEntryEnv(addr, sni, key string, o TrunkEntryOptions) string {
	listen := make([]int, 0, len(o.Forward))
	for l := range o.Forward {
		listen = append(listen, l)
	}
	sort.Ints(listen)
	_, relayPort, _ := net.SplitHostPort(addr)
	pairs := make([]string, 0, len(listen))
	var once []string
	for _, l := range listen {
		pairs = append(pairs, fmt.Sprintf("%d=%d", l, o.Forward[l]))
		if strconv.Itoa(o.Forward[l]) == relayPort && o.HikkariUDPCopies != 2 {
			once = append(once, strconv.Itoa(l))
		}
	}
	forward := strings.Join(pairs, ",")
	return fmt.Sprintf("# Written by the nekko hub: the cascade's trunk entry.\n"+
		"EDGE_TRUNK_EXIT=%s\nEDGE_TRUNK_SNI=%s\nEDGE_TRUNK_KEY=%s\nEDGE_TRUNK_MEMBERS=%d\n"+
		"EDGE_TRUNK_FORWARD=%s\nEDGE_TRUNK_UDP=%s\nEDGE_TRUNK_UDP_ONE_COPY=%s\n",
		addr, sni, key, o.Members, forward, forward, strings.Join(once, ","))
}

// StandTrunkEntry stands up or updates the trunk entry on box. Running it
// again is the update (a new exit or pool, a new build).
func (p *Provisioner) StandTrunkEntry(ctx context.Context, box string, o TrunkEntryOptions) error {
	if o.Members == 0 {
		o.Members = DefaultTrunkMembers
	}
	if o.Members < 1 || o.Members > 128 {
		return fmt.Errorf("%w: членов пула %d, 1-128", ErrRejected, o.Members)
	}
	if o.HikkariUDPCopies < 0 || o.HikkariUDPCopies > 2 {
		return fmt.Errorf("%w: копий датаграмм Hikkari %d, 1 или 2", ErrRejected, o.HikkariUDPCopies)
	}
	if len(o.Forward) == 0 {
		return fmt.Errorf("%w: входу нечего пересылать", ErrRejected)
	}
	if !o.Pipe && len(o.Receivers) > 0 {
		return fmt.Errorf("%w: группа приёмников только у входа над трубой (pipe)", ErrRejected)
	}
	if o.ReceiverPool < 0 || o.ReceiverPool > 128 {
		return fmt.Errorf("%w: членов пула запасного %d, 1-128", ErrRejected, o.ReceiverPool)
	}
	if !o.Pipe && (o.H3Port != 0 || o.H3Usage != "" || o.Voice != "") {
		return fmt.Errorf("%w: h3dgram и voice только у входа над трубой (pipe)", ErrRejected)
	}
	var (
		addr, sni, key string
		open           map[int]bool
		err            error
		pipe           pipeBrain
	)
	if o.Pipe {
		pipe, err = p.pipeBrainFor(ctx, o.Exit, o.H3Port, o.H3Usage, o.Voice, nil)
		addr, sni, key, open = pipe.addr, pipe.sni, pipe.key, pipe.open
	} else {
		addr, sni, key, open, err = p.svc.trunkExitOf(ctx, o.Exit)
	}
	if err != nil {
		return err
	}
	listen := make([]string, 0, len(o.Forward))
	for l, to := range o.Forward {
		if l < 1 || l > 65535 {
			return fmt.Errorf("%w: порт %d", ErrRejected, l)
		}
		if !open[to] {
			return fmt.Errorf("%w: порт %d не открыт магистрали на %s", ErrRejected, to, o.Exit)
		}
		listen = append(listen, strconv.Itoa(l))
	}
	sort.Strings(listen)
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	logf := func(format string, args ...any) {
		applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", box))
	}
	applog.Info(ctx, "entry", "operator trunk entry on box", applog.F("box", box), applog.F("exit", o.Exit),
		applog.F("members", o.Members), applog.F("listen", strings.Join(listen, ",")))
	files := []trunkFile{{"nekko-trunk-entry", p.trunkBuilds}, {"libcronet.so", p.cronetBuilds}}
	env := trunkEntryEnv(addr, sni, key, o)
	if len(o.Receivers) > 0 {
		ips, err := p.svc.receiverAddresses(ctx, o.Exit, o.Receivers, addr, sni, key)
		if err != nil {
			return err
		}
		env += "EDGE_TRUNK_RECEIVERS=" + strings.Join(ips, ",") + "\n"
		if o.ReceiverPool > 0 {
			env += "EDGE_TRUNK_RECEIVER_POOL=" + strconv.Itoa(o.ReceiverPool) + "\n"
		}
		applog.Info(ctx, "entry", "operator trunk entry receivers", applog.F("box", box), applog.F("receivers", strings.Join(o.Receivers, ",")))
	}
	var extra map[string]string
	if o.Pipe {
		files = []trunkFile{{"nekko-trunk-entry", p.trunkBuilds}, {"libcronet.so", pipe.cronet}, {"hk-pipe", p.pipeBuilds}}
		env += pipeBrainEnv(goEntryUnit, "")
		extra = map[string]string{"wires.json": pipe.wires}
		applog.Info(ctx, "entry", "operator trunk entry over hk-pipe", applog.F("box", box), applog.F("wires", strings.TrimSpace(pipe.wires)))
	}
	if err := standTrunkUnit(ctx, r, goEntryUnit, files, env, extra, listen, logf); err != nil {
		return err
	}
	// The cascade watch takes its pairs from here (cascade_watch.go).
	return p.svc.recordTrunkEntry(ctx, box, o)
}

// StopTrunkEntry takes the trunk entry off box.
func (p *Provisioner) StopTrunkEntry(ctx context.Context, box string) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator trunk entry off box", applog.F("box", box))
	out, err := r.Run(ctx, "bash -s", strings.NewReader(goEntryUnit.offScript()))
	if err != nil {
		return fmt.Errorf("снятие входа: %w", err)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", box))
	return p.svc.forgetTrunkEntry(ctx, box)
}
