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

// The stand of the entry over hk-pipe (roadmap 4.6, R5; hikkari-client
// plans/2026-11-11-entry-hot-path-and-unit-economics.md, «Труба на Rust»):
// the same trunkentry binary as the entry's brain over the Rust pipe
// (EDGE_TRUNK_PIPE), a unit of its own beside the Go entry on the same box,
// to the same exit, on listen ports of its own and a book of source ports
// apart from the entry's. The cascade watch measures both (cascade_probe.go,
// path pipe), so a run reads the pipe against the Go entry of the same hour.
// Standing it, reloading it or taking it off never touches the Go entry.
//
//	cmd/server entry-box trunk-pipe <box> <listen=port,...> [members] [h3dgram=<port>[:proxy]]   # stand up or update
//	cmd/server entry-box trunk-pipe-off <box>
//
// h3dgram= gives the stand Chromium's QUIC wire (R6.3) to the exit's end of
// it at that port (entry-box trunk-h3-exit, trunkh3exit.go), its members'
// sessions as Chrome's to a site, or to a proxy with :proxy; the stand's
// libcronet is then the build with the CONNECT-UDP tunnel.

const (
	trunkPipeUnit = "nekko-trunk-pipe"
	trunkPipeDir  = "/usr/local/lib/nekko-trunk-pipe"
	trunkPipeEnv  = "/etc/nekko-trunk-pipe.env"
	// trunkPipeWires is the stand's wires file, staged and swapped in
	// beside its env (standTrunkUnit's extra files).
	trunkPipeWires = trunkPipeEnv + ".wires.json"
	// TrunkPipeSourcePorts are the stand's members' source ports: below the
	// Go entry's book (20000-32767, trunkentry main.go) and the kernel's
	// ephemeral range, so the two never race for a port to the same exit.
	TrunkPipeSourcePorts = "10000-19999"
	// trunkPipeWireMembers is the udp wire's members, as the Go entry's
	// (entry-box trunk-entry-wires); trunkPipeH3Members the QUIC wire's, as
	// R6.3's bench.
	trunkPipeWireMembers = 2
	trunkPipeH3Members   = 4
)

var trunkPipeUnitSpec = trunkUnit{name: trunkPipeUnit, dir: trunkPipeDir, env: trunkPipeEnv,
	desc: "the cascade's trunk entry over hk-pipe (stand)", title: "trunk entry over hk-pipe", reloadEnv: trunkPipeEnv}

// TrunkPipeOptions are what the stand is stood with; its exit is the Go
// entry's on the same box.
type TrunkPipeOptions struct {
	// Forward maps a port the stand listens on to the exit's loopback port
	// it reaches, as the entry's; none may be one of the entry's.
	Forward          map[int]int
	Members          int
	HikkariUDPCopies int
	// H3Port, when set, is the exit's port of Chromium's QUIC wire, H3Usage
	// its members' session usage ("" is destination).
	H3Port  int
	H3Usage string
}

// trunkPipeEnvOf is the stand's env: the entry's (trunkEntryEnv) and the
// pipe's own. CONTAINS THE TRUNK KEY.
func trunkPipeEnvOf(addr, sni, key string, o TrunkPipeOptions) string {
	env := trunkEntryEnv(addr, sni, key, TrunkEntryOptions{Forward: o.Forward, Members: o.Members, HikkariUDPCopies: o.HikkariUDPCopies})
	return env + "EDGE_TRUNK_PIPE=" + trunkPipeDir + "/hk-pipe\nEDGE_TRUNK_WIRES_FILE=" + trunkPipeWires +
		"\nEDGE_TRUNK_SOURCE_PORTS=" + TrunkPipeSourcePorts + "\n"
}

// trunkPipeWiresOf is the stand's wires file: the exit's udp wire and the
// QUIC wire at h3Port (0: none), steered. The pipe carries no quic or
// salamander (owner, 12.11); no wire is the pool alone.
func trunkPipeWiresOf(wires map[string]int, h3Port int, h3Usage string) (string, error) {
	type wire struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Port    int    `json:"port"`
		Members int    `json:"members"`
		Usage   string `json:"usage,omitempty"`
	}
	f := struct {
		Steer bool   `json:"steer"`
		Wires []wire `json:"wires"`
	}{Steer: true, Wires: []wire{}}
	if port, ok := wires["udp"]; ok {
		f.Wires = append(f.Wires, wire{Name: "udp", Kind: "udp", Port: port, Members: trunkPipeWireMembers})
	}
	if h3Port > 0 {
		f.Wires = append(f.Wires, wire{Name: "h3dgram", Kind: "h3dgram", Port: h3Port, Members: trunkPipeH3Members, Usage: h3Usage})
	}
	raw, err := json.Marshal(f)
	return string(raw) + "\n", err
}

// StandTrunkPipe stands up or updates the stand over hk-pipe on box, beside
// its Go entry. Running it again is the update (a new build goes in by
// reload, nothing cut).
func (p *Provisioner) StandTrunkPipe(ctx context.Context, box string, o TrunkPipeOptions) error {
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
		return fmt.Errorf("%w: стенду нечего пересылать", ErrRejected)
	}
	if len(p.pipeBuilds) == 0 {
		return fmt.Errorf("%w: в образе хаба нет hk-pipe", ErrRejected)
	}
	if o.H3Usage != "" && o.H3Usage != "destination" && o.H3Usage != "proxy" {
		return fmt.Errorf("%w: форма члена %q, destination или proxy", ErrRejected, o.H3Usage)
	}
	if o.H3Port < 0 || o.H3Port > 65535 {
		return fmt.Errorf("%w: порт h3dgram %d", ErrRejected, o.H3Port)
	}
	cronet := p.cronetBuilds
	if o.H3Port > 0 {
		if len(p.cronetUDPBuilds) == 0 {
			return fmt.Errorf("%w: в образе хаба нет libcronet-udp.so для провода h3dgram", ErrRejected)
		}
		cronet = p.cronetUDPBuilds
	}
	pair, err := p.svc.trunkPairOf(ctx, box)
	if err != nil {
		return err
	}
	addr, sni, key, open, err := p.svc.trunkExitOf(ctx, pair.Exit)
	if err != nil {
		return err
	}
	listen := make([]string, 0, len(o.Forward))
	for l, to := range o.Forward {
		if l < 1 || l > 65535 {
			return fmt.Errorf("%w: порт %d", ErrRejected, l)
		}
		if _, taken := pair.Forward[l]; taken {
			return fmt.Errorf("%w: порт %d слушает сам вход %s", ErrRejected, l, box)
		}
		if !open[to] {
			return fmt.Errorf("%w: порт %d не открыт магистрали на %s", ErrRejected, to, pair.Exit)
		}
		listen = append(listen, strconv.Itoa(l))
	}
	sort.Strings(listen)
	wires, err := p.svc.TrunkWiresOf(ctx, pair.Exit)
	if err != nil {
		return err
	}
	wiresFile, err := trunkPipeWiresOf(wires, o.H3Port, o.H3Usage)
	if err != nil {
		return err
	}
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	logf := func(format string, args ...any) {
		applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", box))
	}
	applog.Info(ctx, "entry", "operator trunk pipe on box", applog.F("box", box), applog.F("exit", pair.Exit),
		applog.F("members", o.Members), applog.F("listen", strings.Join(listen, ",")))
	files := []trunkFile{{"nekko-trunk-entry", p.trunkBuilds}, {"libcronet.so", cronet}, {"hk-pipe", p.pipeBuilds}}
	if err := standTrunkUnit(ctx, r, trunkPipeUnitSpec, files, trunkPipeEnvOf(addr, sni, key, o),
		map[string]string{"wires.json": wiresFile}, listen, logf); err != nil {
		return err
	}
	return p.svc.recordTrunkPipe(ctx, box, o.Forward)
}

// StopTrunkPipe takes the stand off box; the Go entry stays.
func (p *Provisioner) StopTrunkPipe(ctx context.Context, box string) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator trunk pipe off box", applog.F("box", box))
	out, err := r.Run(ctx, "bash -s", strings.NewReader(trunkPipeUnitSpec.offScript()))
	if err != nil {
		return fmt.Errorf("снятие стенда: %w", err)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", box))
	return p.svc.recordTrunkPipe(ctx, box, nil)
}

// trunkPairOf is the trunk entry the hub stood on box.
func (s *Service) trunkPairOf(ctx context.Context, box string) (TrunkPair, error) {
	pairs, err := s.TrunkPairs(ctx)
	if err != nil {
		return TrunkPair{}, err
	}
	for _, p := range pairs {
		if p.Entry == box {
			return p, nil
		}
	}
	return TrunkPair{}, fmt.Errorf("%w: на %s нет входа магистрали, сначала entry-box trunk-entry", ErrRejected, box)
}

// recordTrunkPipe keeps the stand's ports on the entry's row; nil is none.
func (s *Service) recordTrunkPipe(ctx context.Context, box string, forward map[int]int) error {
	var raw []byte
	if forward != nil {
		fwd := map[string]int{}
		for l, to := range forward {
			fwd[strconv.Itoa(l)] = to
		}
		var err error
		if raw, err = json.Marshal(fwd); err != nil {
			return err
		}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE trunk_entries SET pipe_forward = $2 WHERE entry_box = $1`, box, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: входа %s нет в trunk_entries", ErrRejected, box)
	}
	return nil
}
