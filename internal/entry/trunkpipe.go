package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
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
//	cmd/server entry-box trunk-pipe <box> <listen=port,...> [members] [h3dgram=<port>[:proxy]] [voice=<wire>]   # stand up or update
//	cmd/server entry-box trunk-pipe-off <box>
//	cmd/server entry-box trunk-pipe-wires <box> <[pool:members,]kind:members[:fate],...|off> [h3dgram=<port>[:proxy]] [voice=<wire>]
//	cmd/server entry-box trunk-pipe-points <box> <point-box>,...|off
//
// core=<box> (hikkari-client plans/2026-11-19-border-forwarders.md, G2)
// gives the stand a core of its own in place of the people's exit: hk-exit
// and the Hikkari relay on that box (entry-box core), "core=-" back to the
// exit; left out, the stand keeps the core it had. trunk-pipe-points gives
// the stand's brain the border points to its core (trunkentry wires.go
// "points"): every member goes through point i mod n, the core's own address
// is dialled by none. The points are the hub's (trunk_entries.pipe_points)
// and ride every wires file the hub writes for the stand.
//
// h3dgram= gives the stand Chromium's QUIC wire (R6.3) to the exit's end of
// it at that port (hk-exit under the exit's relay, entry-box trunk-pipe-exit,
// trunkpipeexit.go: the stand reaches it by the pipe key, the Go entry the
// relay's Go exit by its own), its members'
// sessions as Chrome's to a site, or to a proxy with :proxy; the stand's
// libcronet is then the build with the CONNECT-UDP tunnel. voice= holds the
// voice class on that wire (udp or h3dgram), bulk on the pool, with no
// selector: the wire measured under load, not chosen by its numbers.
//
// trunk-pipe-wires changes the stand's wires (roadmap 4.4) by its file
// alone, with no new process: which wires, their members and fates, the
// QUIC wire's port and usage, voice held or steered. A wire's fate
// (hikkari-client plans/2026-11-15-live-udp-two-fates.md, section 1) is what
// must break on the path for it to lose a frame: udp when not given; the
// selector puts voice on two fates at once. pool:members is the pool's
// members the same way (roadmap Э7): left out, the pool is the one the stand
// was stood with; the pipe dials a pool of a new size anew, no stream reset.

const (
	trunkPipeUnit = "nekko-trunk-pipe"
	trunkPipeDir  = "/usr/local/lib/nekko-trunk-pipe"
	trunkPipeEnv  = "/etc/nekko-trunk-pipe.env"
	// CascadeLabPort is the synthetic load entry beside the country routes.
	CascadeLabPort = 18446
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
	// trunkPoolMembersMax is the most members a pool has (trunkentry
	// main.go maxMembers).
	trunkPoolMembersMax = 128
)

var trunkPipeUnitSpec = trunkUnit{name: trunkPipeUnit, dir: trunkPipeDir, env: trunkPipeEnv,
	desc: "the cascade's trunk entry over hk-pipe (stand)", title: "trunk entry over hk-pipe", reloadEnv: trunkPipeEnv}

var trunkPipeLabUnitSpec = trunkUnit{name: "nekko-trunk-pipe-lab", dir: "/usr/local/lib/nekko-trunk-pipe-lab", env: "/etc/nekko-trunk-pipe-lab.env",
	desc: "the cascade's synthetic load entry", title: "trunk entry lab over hk-pipe", reloadEnv: "/etc/nekko-trunk-pipe-lab.env"}

// TrunkPipeOptions are what the stand is stood with; its exit is the Go
// entry's on the same box.
type TrunkPipeOptions struct {
	// Forward maps a port the stand listens on to the exit's loopback port
	// it reaches, as the entry's; none may be one of the entry's.
	Forward          map[int]int
	Members          int
	HikkariUDPCopies int
	Lab              bool // a separate test listener alongside the country routes
	// H3Port, when set, is the exit's port of Chromium's QUIC wire, H3Usage
	// its members' session usage ("" is destination).
	H3Port  int
	H3Usage string
	// Voice, when set, is the wire voice is held on.
	Voice string
	// Core is the stand's own core (G2): "" keeps the one it had, "-" is
	// the people's exit.
	Core string
	// Points are the stand's border points to its core (box names), given
	// before the swap so the new generation's members go through them at
	// once; nil keeps the ones the stand had (none for a new core), empty
	// is none.
	Points []string
}

// trunkPipeEnvOf is the stand's env: the entry's (trunkEntryEnv) and the
// pipe's own. CONTAINS THE TRUNK KEY.
func trunkPipeEnvOf(addr, sni, key string, o TrunkPipeOptions) string {
	env := trunkEntryEnv(addr, sni, key, TrunkEntryOptions{Forward: o.Forward, Members: o.Members, HikkariUDPCopies: o.HikkariUDPCopies})
	if o.Lab {
		// DE uses 4000-7999, the country NL backend 10000-19999 and the
		// production entry 20000-32767. The lab's members have their own book.
		return env + pipeBrainEnv(trunkPipeLabUnitSpec, "8000-9999") + "EDGE_TRUNK_REQUIRE_POINTS=1\n"
	}
	return env + pipeBrainEnv(trunkPipeUnitSpec, TrunkPipeSourcePorts)
}

// pipeBrainEnv is what makes u's entry the brain over hk-pipe: the pipe in
// u's directory, the wires file beside u's env (standTrunkUnit's extra
// "wires.json"), the members' source ports when not the entry's default.
func pipeBrainEnv(u trunkUnit, sourcePorts string) string {
	env := "EDGE_TRUNK_PIPE=" + u.dir + "/hk-pipe\nEDGE_TRUNK_WIRES_FILE=" + u.env + ".wires.json\n"
	if sourcePorts != "" {
		env += "EDGE_TRUNK_SOURCE_PORTS=" + sourcePorts + "\n"
	}
	return env
}

// pipeBrain is what an entry over hk-pipe to an exit needs from the hub:
// hk-exit's address, SNI, pipe key and open ports, the wires file, the
// libcronet build.
type pipeBrain struct {
	addr, sni, key string
	open           map[int]bool
	wires          string
	cronet         map[string]EdgeBuild
}

// pipeBrainFor checks the pipe's parts for an entry to exit: the udp wire
// when hk-exit serves it, Chromium's QUIC wire at h3Port when set.
func (p *Provisioner) pipeBrainFor(ctx context.Context, exit string, h3Port int, h3Usage, voice string, points []string, builds trunkArtifacts) (pipeBrain, error) {
	var b pipeBrain
	if len(builds.pipeBuilds) == 0 {
		return b, fmt.Errorf("%w: в образе хаба нет hk-pipe", ErrRejected)
	}
	if h3Usage != "" && h3Usage != "destination" && h3Usage != "proxy" {
		return b, fmt.Errorf("%w: форма члена %q, destination или proxy", ErrRejected, h3Usage)
	}
	if h3Port < 0 || h3Port > 65535 {
		return b, fmt.Errorf("%w: порт h3dgram %d", ErrRejected, h3Port)
	}
	b.cronet = builds.cronetBuilds
	if h3Port > 0 {
		if len(builds.cronetUDPBuilds) == 0 {
			return b, fmt.Errorf("%w: в образе хаба нет libcronet-udp.so для провода h3dgram", ErrRejected)
		}
		b.cronet = builds.cronetUDPBuilds
	}
	var err error
	if b.addr, b.sni, b.key, b.open, err = p.svc.trunkPipeExitOf(ctx, exit); err != nil {
		return b, err
	}
	wires, err := p.svc.TrunkPipeWiresOf(ctx, exit)
	if err != nil {
		return b, err
	}
	members := map[string]int{}
	if _, ok := wires["udp"]; ok {
		members["udp"] = trunkPipeWireMembers
	}
	if h3Port > 0 {
		members["h3dgram"] = trunkPipeH3Members
	}
	b.wires, err = trunkPipeWiresOf(wires, TrunkPipeWires{Members: members, H3Port: h3Port, H3Usage: h3Usage, Voice: voice, Points: points})
	return b, err
}

// TrunkPipeWires are the stand's wires file as the hub writes it: the
// members of each wire it carries (udp, h3dgram; a kind left out is no such
// wire), the exit's port and session usage of h3dgram, and the wire voice is
// held on ("" the selector's choice).
type TrunkPipeWires struct {
	Members map[string]int
	// Fates are the wires' fates by kind; a kind left out is udp.
	Fates map[string]string
	// Pool is the pool's members in place of the ones the stand was stood
	// with; 0 leaves those.
	Pool    int
	H3Port  int
	H3Usage string
	Voice   string
	// Points are the border points' addresses to the core (G2), none: the
	// core's own address.
	Points []string
}

// trunkPipeWireKinds are the wires the pipe carries (trunkentry
// brain_wires_linux.go pipeWiresOf), in the file's order.
var trunkPipeWireKinds = []string{"udp", "h3dgram"}

// ParseTrunkPipeWires reads the stand's wires, kind:members[:fate],... of
// trunkPipeWireKinds ("off": none): the members and the fates given; and
// pool:members (1-128, trunkentry maxMembers), the pool's own.
func ParseTrunkPipeWires(raw string) (TrunkPipeWires, error) {
	out := TrunkPipeWires{Members: map[string]int{}, Fates: map[string]string{}}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "off" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		kind, rest, ok := strings.Cut(strings.TrimSpace(item), ":")
		m, fate, fated := strings.Cut(rest, ":")
		n, err := strconv.Atoi(m)
		if kind == "pool" {
			if !ok || err != nil || n < 1 || n > trunkPoolMembersMax || fated || out.Pool != 0 {
				return out, fmt.Errorf("%w: %q, нужно pool:члены (1-%d), один раз и без судьбы", ErrRejected, item, trunkPoolMembersMax)
			}
			out.Pool = n
			continue
		}
		if !ok || err != nil || n < 1 || n > 32 {
			return out, fmt.Errorf("%w: %q, нужно вид:члены[:судьба] (1-32)", ErrRejected, item)
		}
		if kind != "udp" && kind != "h3dgram" {
			return out, fmt.Errorf("%w: провод %q, труба везёт %s", ErrRejected, kind, strings.Join(trunkPipeWireKinds, ", "))
		}
		if _, dup := out.Members[kind]; dup {
			return out, fmt.Errorf("%w: %q дважды", ErrRejected, kind)
		}
		if fated && !trunkFateWord.MatchString(fate) {
			return out, fmt.Errorf("%w: судьба %q, латиница и цифры до 16", ErrRejected, fate)
		}
		out.Members[kind] = n
		if fated {
			out.Fates[kind] = fate
		}
	}
	return out, nil
}

// trunkFateWord is a fate a wire may be given (trunkentry wires.go fateWord).
var trunkFateWord = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// trunkPipeWiresOf is the stand's wires file, steered: the exit's udp wire
// (exit: the exit's wires and their ports) and the QUIC wire at w.H3Port.
// The pipe carries no quic or salamander (owner, 12.11); no wire is the
// pool alone.
func trunkPipeWiresOf(exit map[string]int, w TrunkPipeWires) (string, error) {
	type wire struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Port    int    `json:"port"`
		Members int    `json:"members"`
		Usage   string `json:"usage,omitempty"`
		Fate    string `json:"fate,omitempty"`
	}
	f := struct {
		Steer  bool     `json:"steer"`
		Wires  []wire   `json:"wires"`
		Voice  string   `json:"voice,omitempty"`
		Pool   int      `json:"pool,omitempty"`
		Points []string `json:"points,omitempty"`
	}{Steer: true, Wires: []wire{}, Voice: w.Voice, Pool: w.Pool, Points: w.Points}
	if w.Pool < 0 || w.Pool > trunkPoolMembersMax {
		return "", fmt.Errorf("%w: членов пула %d, 1-%d", ErrRejected, w.Pool, trunkPoolMembersMax)
	}
	if w.H3Usage != "" && w.H3Usage != "destination" && w.H3Usage != "proxy" {
		return "", fmt.Errorf("%w: форма члена %q, destination или proxy", ErrRejected, w.H3Usage)
	}
	for _, kind := range trunkPipeWireKinds {
		n, ok := w.Members[kind]
		if !ok {
			continue
		}
		if n < 1 || n > 32 {
			return "", fmt.Errorf("%w: членов %s %d, 1-32", ErrRejected, kind, n)
		}
		switch kind {
		case "udp":
			port, served := exit["udp"]
			if !served {
				return "", fmt.Errorf("%w: hk-exit выхода не держит провод udp, сначала entry-box trunk-pipe-exit", ErrRejected)
			}
			f.Wires = append(f.Wires, wire{Name: kind, Kind: kind, Port: port, Members: n, Fate: w.Fates[kind]})
		case "h3dgram":
			// The wire's end is hk-exit's under the relay (trunkpipeexit.go):
			// a port the stand names must be the one it serves.
			if port, served := exit["h3dgram"]; !served {
				return "", fmt.Errorf("%w: hk-exit выхода не держит провод h3dgram, сначала entry-box trunk-pipe-exit", ErrRejected)
			} else if w.H3Port != port {
				return "", fmt.Errorf("%w: h3dgram выхода на порту %d, а не %d", ErrRejected, port, w.H3Port)
			}
			f.Wires = append(f.Wires, wire{Name: kind, Kind: kind, Port: w.H3Port, Members: n, Usage: w.H3Usage, Fate: w.Fates[kind]})
		}
	}
	if w.Voice != "" {
		if _, ok := w.Members[w.Voice]; !ok {
			return "", fmt.Errorf("%w: голос на проводе %q, которого у стенда нет", ErrRejected, w.Voice)
		}
	}
	raw, err := json.Marshal(f)
	return string(raw) + "\n", err
}

// SetTrunkPipeWires rewrites the wires file of the stand on box, and only
// it: the running brain follows the file within 10 s, with no new process
// and no flow or stream cut (trunkentry brain_wires_linux.go, roadmap 4.4).
func (p *Provisioner) SetTrunkPipeWires(ctx context.Context, box string, w TrunkPipeWires) error {
	pair, err := p.svc.trunkPairOf(ctx, box)
	if err != nil {
		return err
	}
	exit, err := p.svc.TrunkPipeWiresOf(ctx, pair.PipeExit())
	if err != nil {
		return err
	}
	if w.Points, err = p.svc.pointAddresses(ctx, pair.PipePoints); err != nil {
		return err
	}
	raw, err := trunkPipeWiresOf(exit, w)
	if err != nil {
		return err
	}
	raw, err = p.svc.pointTLSWires(ctx, raw, box, pair.PipeExit(), true, pair.PipePoints, w.Points)
	if err != nil {
		return err
	}
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	if out, err := r.Run(ctx, trunkPipeWiresScript, strings.NewReader(raw)); err != nil {
		return fmt.Errorf("файл проводов стенда на %s: %w: %s", box, err, out)
	}
	applog.Info(ctx, "entry", "operator trunk pipe wires", applog.F("box", box), applog.F("wires", strings.TrimSpace(raw)))
	return nil
}

// trunkPipeWiresScript swaps the stand's wires file in, and only on a box
// the stand stands on.
const trunkPipeWiresScript = "test -f " + trunkPipeEnv + " || { echo 'стенда нет: сначала entry-box trunk-pipe' >&2; exit 1; }; " +
	"umask 022; cat > " + trunkPipeWires + ".new && mv " + trunkPipeWires + ".new " + trunkPipeWires

// StandTrunkPipe stands up or updates the stand over hk-pipe on box, beside
// its Go entry. Running it again is the update (a new build goes in by
// reload, nothing cut).
func (p *Provisioner) StandTrunkPipe(ctx context.Context, box string, o TrunkPipeOptions) error {
	if o.Members == 0 {
		o.Members = DefaultTrunkMembers
	}
	if o.Members < 1 || o.Members > trunkPoolMembersMax {
		return fmt.Errorf("%w: членов пула %d, 1-128", ErrRejected, o.Members)
	}
	if o.HikkariUDPCopies < 0 || o.HikkariUDPCopies > 2 {
		return fmt.Errorf("%w: копий датаграмм Hikkari %d, 1 или 2", ErrRejected, o.HikkariUDPCopies)
	}
	if len(o.Forward) == 0 {
		return fmt.Errorf("%w: стенду нечего пересылать", ErrRejected)
	}
	pair, err := p.svc.trunkPairOf(ctx, box)
	if err != nil {
		return err
	}
	core := pair.PipeCore
	switch o.Core {
	case "":
	case "-":
		core = ""
	default:
		if o.Core == box {
			return fmt.Errorf("%w: ядро стенда не сам вход", ErrRejected)
		}
		core = o.Core
	}
	exitBox := pair.Exit
	if core != "" {
		exitBox = core
	}
	points := pair.PipePoints
	if core != pair.PipeCore {
		// Points are a core's: another core starts with none.
		points = nil
	}
	if o.Points != nil {
		if points, err = p.svc.checkPipePoints(ctx, box, exitBox, o.Points); err != nil {
			return err
		}
	}
	pts, err := p.svc.pointAddresses(ctx, points)
	if err != nil {
		return err
	}
	unit, builds := trunkPipeUnitSpec, p.trunkArtifacts
	if o.Lab {
		if len(o.Forward) != 1 || o.Forward[CascadeLabPort] != pair.RelayPort ||
			pair.Pipe[CascadeLabPort] != 0 || exitBox != pair.Exit || len(points) == 0 || !slices.Equal(points, pair.Points) {
			return fmt.Errorf("%w: лабораторный вход требует порт %d и действующий выход через общие точки", ErrRejected, CascadeLabPort)
		}
		unit, builds = trunkPipeLabUnitSpec, p.standTrunkArtifacts
	}
	pipe, err := p.pipeBrainFor(ctx, exitBox, o.H3Port, o.H3Usage, o.Voice, pts, builds)
	if err != nil {
		return err
	}
	addr, sni, key, open := pipe.addr, pipe.sni, pipe.key, pipe.open
	listen := make([]string, 0, len(o.Forward))
	for l, to := range o.Forward {
		if l < 1 || l > 65535 {
			return fmt.Errorf("%w: порт %d", ErrRejected, l)
		}
		if _, taken := pair.Forward[l]; taken {
			return fmt.Errorf("%w: порт %d слушает сам вход %s", ErrRejected, l, box)
		}
		if !open[to] {
			return fmt.Errorf("%w: порт %d не открыт магистрали на %s", ErrRejected, to, exitBox)
		}
		listen = append(listen, strconv.Itoa(l))
	}
	sort.Strings(listen)
	env := trunkPipeEnvOf(addr, sni, key, o)
	var nlPort, dePort int
	err = p.svc.pool.QueryRow(ctx, `SELECT nl_port, de_port FROM entry_country_egress WHERE entry_box=$1`, box).Scan(&nlPort, &dePort)
	if err == nil {
		if o.Lab {
			if nlPort == CascadeLabPort || dePort == CascadeLabPort {
				return fmt.Errorf("%w: порт лаборатории %d используется шлюзом страны", ErrRejected, CascadeLabPort)
			}
		} else {
			relay := o.Forward[nlPort]
			if relay == 0 || relay != pair.Pipe[nlPort] || len(o.Forward) != 1 ||
				exitBox != pair.Exit || !slices.Equal(points, pair.Points) {
				return fmt.Errorf("%w: стенд используется шлюзами стран", ErrRejected)
			}
			backend := o
			backend.Forward = map[int]int{countryNLBackendPort: relay}
			env = trunkPipeEnvOf(addr, sni, key, backend) + countryBackendEnv
			listen = []string{strconv.Itoa(countryNLBackendPort)}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	wiresFile, cronet := pipe.wires, pipe.cronet
	wiresFile, err = p.svc.pointTLSWires(ctx, wiresFile, box, exitBox, true, points, pts)
	if err != nil {
		return err
	}
	// The core and its points go on record before the swap: a border point
	// forwards the core's trunk from the entries the hub knows go there
	// (borderPlanOf), and a stand whose core answers only its points cannot
	// bring a member up until a point forwards (G2, 20.11: the reload failed
	// on "no member came up" while border-forward knew no entry to aeznl1).
	if !o.Lab {
		if err := p.svc.recordTrunkPipeCore(ctx, box, core, points); err != nil {
			return err
		}
	}
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	logf := func(format string, args ...any) {
		applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", box))
	}
	applog.Info(ctx, "entry", "operator trunk pipe on box", applog.F("box", box), applog.F("exit", exitBox),
		applog.F("unit", unit.name), applog.F("members", o.Members), applog.F("listen", strings.Join(listen, ",")))
	files := []trunkFile{{"nekko-trunk-entry", builds.trunkBuilds}, {"libcronet.so", cronet}, {"hk-pipe", builds.pipeBuilds}}
	if err := standTrunkUnit(ctx, r, unit, files, env,
		map[string]string{"wires.json": wiresFile}, listen, logf); err != nil {
		return err
	}
	if o.Lab {
		return nil
	}
	return p.svc.recordTrunkPipe(ctx, box, o.Forward)
}

// StopTrunkPipeLab removes the synthetic load listener and its open port.
func (p *Provisioner) StopTrunkPipeLab(ctx context.Context, box string) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator trunk pipe lab off", applog.F("box", box))
	if out, err := r.Run(ctx, "bash -s", strings.NewReader(trunkPipeLabUnitSpec.offScript())); err != nil {
		return fmt.Errorf("снятие лабораторного входа: %w: %s", err, out)
	}
	return nil
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
	if err := p.svc.recordTrunkPipe(ctx, box, nil); err != nil {
		return err
	}
	return p.svc.recordTrunkPipeCore(ctx, box, "", nil)
}

// SetTrunkPipePoints gives the stand on box the border points to its core
// (G2; none: the core's own address): the hub keeps them and puts them into
// the stand's wires file as it stands, the rest of the file untouched; the
// brain follows within 10 s, no new process. A point is a box that forwards
// the core's trunk (entry-box border-forward <point> <core>), never the
// entry itself.
func (p *Provisioner) SetTrunkPipePoints(ctx context.Context, box string, points []string) error {
	pair, err := p.svc.trunkPairOf(ctx, box)
	if err != nil {
		return err
	}
	if pair.Pipe == nil {
		return fmt.Errorf("%w: на %s нет стенда, сначала entry-box trunk-pipe", ErrRejected, box)
	}
	if points, err = p.svc.checkPipePoints(ctx, box, pair.PipeExit(), points); err != nil {
		return err
	}
	addrs, err := p.svc.pointAddresses(ctx, points)
	if err != nil {
		return err
	}
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	raw, err := r.Run(ctx, "cat "+trunkPipeWires, nil)
	if err != nil {
		return fmt.Errorf("файл проводов стенда на %s: %w: %s", box, err, raw)
	}
	file, err := p.svc.pointTLSWires(ctx, raw, box, pair.PipeExit(), true, points, addrs)
	if err != nil {
		return fmt.Errorf("файл проводов стенда на %s: %w", box, err)
	}
	if out, err := r.Run(ctx, trunkPipeWiresScript, strings.NewReader(file)); err != nil {
		return fmt.Errorf("файл проводов стенда на %s: %w: %s", box, err, out)
	}
	applog.Info(ctx, "entry", "operator trunk pipe points", applog.F("box", box), applog.F("core", pair.PipeExit()),
		applog.F("points", strings.Join(points, ",")))
	return p.svc.recordTrunkPipeCore(ctx, box, pair.PipeCore, points)
}

// trunkPipePointsMax is the most border points a stand is given (trunkentry
// wires.go maxPoints).
const trunkPipePointsMax = 8

// checkPipePoints checks the stand's border points to core: boxes the hub
// knows, never the entry or the core itself, each once, at most
// trunkPipePointsMax. An empty list is none, not nil.
func (s *Service) checkPipePoints(ctx context.Context, box, core string, points []string) ([]string, error) {
	if len(points) > trunkPipePointsMax {
		return nil, fmt.Errorf("%w: точек %d, не больше %d", ErrRejected, len(points), trunkPipePointsMax)
	}
	seen := map[string]bool{}
	for _, pt := range points {
		if pt == box || pt == core || seen[pt] {
			return nil, fmt.Errorf("%w: точка %q: сам вход, само ядро или дважды", ErrRejected, pt)
		}
		seen[pt] = true
	}
	if _, err := s.pointAddresses(ctx, points); err != nil {
		return nil, err
	}
	return append([]string{}, points...), nil
}

// withPoints is the wires file raw with its points in place of the ones it
// had; every other key stays as the hub wrote it.
func withPoints(raw []byte, points []string) (string, error) {
	f := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", err
	}
	if len(points) == 0 {
		delete(f, "points")
	} else {
		b, err := json.Marshal(points)
		if err != nil {
			return "", err
		}
		f["points"] = b
	}
	out, err := json.Marshal(f)
	return string(out) + "\n", err
}

// pointAddresses are the boxes' IPv4 addresses, in their order.
func (s *Service) pointAddresses(ctx context.Context, boxes []string) ([]string, error) {
	out := make([]string, 0, len(boxes))
	for _, b := range boxes {
		ip, err := s.boxIPv4(ctx, b)
		if err != nil {
			return nil, err
		}
		out = append(out, ip.String())
	}
	return out, nil
}

// boxIPv4 is the box's public IPv4 address.
func (s *Service) boxIPv4(ctx context.Context, box string) (netip.Addr, error) {
	acc, err := s.boxAccess(ctx, box)
	if err != nil {
		return netip.Addr{}, err
	}
	host := acc.address
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() {
		return netip.Addr{}, fmt.Errorf("%w: у %s нет IPv4-адреса", ErrRejected, box)
	}
	return ip, nil
}

// recordTrunkPipeCore keeps the stand's core ("" the people's exit) and its
// border points on the entry's row.
func (s *Service) recordTrunkPipeCore(ctx context.Context, box, core string, points []string) error {
	var c any
	if core != "" {
		c = core
	}
	if points == nil {
		points = []string{}
	}
	_, err := s.pool.Exec(ctx, `UPDATE trunk_entries SET pipe_core = $2, pipe_points = $3 WHERE entry_box = $1`, box, c, points)
	return err
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
