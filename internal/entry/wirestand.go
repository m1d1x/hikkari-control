package entry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The stand of the trunk's second wire (edge/wirestand.go, hikkari-client
// plans/2026-11-11-trunk-second-wire.md, roadmap 4.1): one process per
// candidate wire on the cascade's entry and on its exit, synthetic only. Like
// the lab Reality relay it is experiment equipment: its own copy of the edge
// binary, a template unit hikkari-wire-stand@<wire>, its own UDP ports on the
// exit. Standing it up or taking it down restarts neither the entry's
// nekko-trunk-entry nor the exit's relay.
//
//	cmd/server entry-box wire-stand <entry-box> <exit-box> [sni] [bench=<minutes>] [load=<wires>] [steps=<steps>] [cc=<h3 congestion>]   # stand up or update
//	cmd/server entry-box wire-stand-off <entry-box> <exit-box>     # take it down

const (
	wireStandUnit    = "hikkari-wire-stand"
	wireStandBinary  = "/usr/local/bin/hikkari-wire-stand"
	wireStandDir     = "/etc/hikkari-wire-stand"
	wireStandSecretF = wireStandDir + "/secret"
	// wireStandLibDir holds the libcronet.so the entry's wire h3 loads
	// (Chromium's QUIC); the entry's stand runs the edge built with cronet
	// (linux-<arch>/hikkari-wire-stand-h3), the exit's the relay's edge.
	wireStandLibDir = "/usr/local/lib/hikkari-wire-stand"
	wireStandCronet = wireStandLibDir + "/libcronet.so"
	// WireStandH3Build is the cronet edge's name in the hub's image.
	WireStandH3Build = "hikkari-wire-stand-h3"
	// DefaultWireStandSNI is the QUIC wires' server name when none is given.
	DefaultWireStandSNI = "www.example.com"
)

// WireStandPorts are the exit's UDP ports, one per wire, apart from the
// relay's 443 and the cascade's 8443 and from the provoke receiver's 2053.
var WireStandPorts = []struct {
	Wire string
	Port int
}{{"quic", 20443}, {"salamander", 20444}, {"awg", 20445}, {"udp", 20446}, {"h3", 20450}}

const wireStandUnitFile = `# Installed by the nekko hub: the stand of the trunk's second wire (4.1).
[Unit]
Description=Hikkari trunk second wire stand (%i)
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=` + wireStandDir + `/%i.env
Environment=WIRESTAND_UNIT=%n
# The wire's packets in and out, read by the process for its CPU line.
IPAccounting=yes
ExecStart=` + wireStandBinary + ` run
Restart=always
RestartSec=5
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

// wireStandSwapScript: $1 is "exit" or "entry", then wire=port pairs. It
// refuses a port another process holds (exit), checks every env against the
// new binary, opens the ports in ufw when ufw guards the box (exit), and
// (re)starts every instance.
const wireStandSwapScript = `set -eu
role="$1"; shift
bin=` + wireStandBinary + `
dir=` + wireStandDir + `
if [ "$role" = exit ]; then
	for pair in "$@"; do
		port=${pair#*=}
		holder=$(ss -ulnpH "sport = :$port" | grep -v hikkari-wire || true)
		if [ -n "$holder" ]; then
			echo "порт udp/$port занят: $holder" >&2
			exit 3
		fi
	done
fi
[ -f "$bin.new" ] || cp "$bin" "$bin.new"
chmod 755 "$bin.new"
for pair in "$@"; do
	wire=${pair%%=*}
	chmod 600 "$dir/$wire.env.new"
	( set -a; . "$dir/$wire.env.new"; set +a; "$bin.new" check ) >/dev/null
done
mv "$bin.new" "$bin"
systemctl daemon-reload
for pair in "$@"; do
	wire=${pair%%=*}; port=${pair#*=}
	mv "$dir/$wire.env.new" "$dir/$wire.env"
	if [ "$role" = exit ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
		ufw allow "$port/udp" comment ` + wireStandUnit + ` >/dev/null
	fi
	systemctl enable ` + wireStandUnit + `@"$wire" >/dev/null 2>&1
	systemctl restart ` + wireStandUnit + `@"$wire"
done
sleep 2
for pair in "$@"; do
	wire=${pair%%=*}
	if systemctl is-active --quiet ` + wireStandUnit + `@"$wire"; then
		echo "` + wireStandUnit + `@$wire работает ($role)"
	else
		journalctl -u ` + wireStandUnit + `@"$wire" -n 20 --no-pager >&2
		exit 1
	fi
done
`

// wireStandOffScript: $1 is "exit" or "entry", then wire=port pairs.
const wireStandOffScript = `set -eu
role="$1"; shift
for pair in "$@"; do
	wire=${pair%%=*}; port=${pair#*=}
	systemctl disable --now ` + wireStandUnit + `@"$wire" >/dev/null 2>&1 || true
	if [ "$role" = exit ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
		ufw delete allow "$port/udp" >/dev/null 2>&1 || true
	fi
done
rm -rf ` + wireStandDir + ` ` + wireStandBinary + ` ` + wireStandLibDir + ` /etc/systemd/system/` + wireStandUnit + `@.service
systemctl daemon-reload
echo "` + wireStandUnit + ` снят ($role)"
`

var wireStandSecretRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// wireStandEnv is one process's environment; load is the wire's synthetic
// users' plan (WIRESTAND_LOAD, the entry's only), cc Chromium's congestion
// controller on h3.
func wireStandEnv(role, wire string, port int, exitAddr, sni, secret, bench, load, cc string) string {
	env := fmt.Sprintf("# Written by the nekko hub: trunk second wire stand, synthetic only.\nEDGE_PROTO=wirestand\nWIRESTAND_ROLE=%s\nWIRESTAND_WIRE=%s\nWIRESTAND_PORT=%d\nWIRESTAND_SNI=%s\nWIRESTAND_SECRET=%s\n",
		role, wire, port, sni, secret)
	if role == "entry" {
		env += "WIRESTAND_EXIT=" + exitAddr + "\n"
		if bench != "" {
			env += "WIRESTAND_BENCH=" + bench + "\n"
		}
		if load != "" {
			env += "WIRESTAND_LOAD=" + load + "\n"
		}
		if wire == "h3" {
			env += "WIRESTAND_CRONET=" + wireStandCronet + "\n"
			if cc != "" {
				env += "WIRESTAND_H3_CC=" + cc + "\n"
			}
		}
	}
	return env
}

// WireLoad is the synthetic users' bench of the stand (edge
// wirestand_load.go): each step of Steps on each of Wires, interleaved, in
// slots of five minutes from the clock's :x1 (four minutes of load, the
// fifth, :x5 or :x0, idle: the hub's own round). Steps are u<N> (each frame
// its own write), g<N> (glued) or n<N> (no wire).
type WireLoad struct {
	Wires []string
	Steps []string
	// CC is Chromium's congestion controller on h3 ("" its default).
	CC string
}

// DefaultWireLoadSteps price a wire as it is, glued, and without it.
var DefaultWireLoadSteps = []string{"u1", "u2", "u4", "g4", "g16", "n4", "n16"}

var wireLoadStepRe = regexp.MustCompile(`^[ugn]([1-9]|1[0-6])$`)

// Check says what is wrong with the bench.
func (l WireLoad) Check() error {
	if len(l.Wires) == 0 || len(l.Steps) == 0 {
		return fmt.Errorf("%w: load: no wires or steps", ErrRejected)
	}
	seen := map[string]bool{}
	for _, w := range l.Wires {
		ok := false
		for _, p := range WireStandPorts {
			ok = ok || p.Wire == w
		}
		if !ok || w == "awg" || seen[w] {
			return fmt.Errorf("%w: load: wire %q (h3, quic, salamander, udp, each once)", ErrRejected, w)
		}
		seen[w] = true
	}
	for _, s := range l.Steps {
		if !wireLoadStepRe.MatchString(s) {
			return fmt.Errorf("%w: load: step %q is not u|g|n and 1–16 users", ErrRejected, s)
		}
	}
	switch l.CC {
	case "", "bbr", "bbr2", "cubic", "reno":
	default:
		return fmt.Errorf("%w: cc %q is not bbr, bbr2, cubic or reno", ErrRejected, l.CC)
	}
	return nil
}

const wireLoadSlot = 5 * time.Minute

// plans are each wire's WIRESTAND_LOAD: the first :x1 at least 5 minutes
// after now, then slot k·len(wires)+j to step k on wire j.
func (l WireLoad) plans(now time.Time) (map[string]string, time.Time, time.Time) {
	from := now.Add(5 * time.Minute).Truncate(10 * time.Minute).Add(time.Minute)
	for from.Before(now.Add(5 * time.Minute)) {
		from = from.Add(wireLoadSlot)
	}
	out := map[string]string{}
	for j, w := range l.Wires {
		var parts []string
		idle := j * 5 // minutes before this wire's first slot
		for k, s := range l.Steps {
			if k > 0 {
				idle = (len(l.Wires) - 1) * 5
			}
			if idle > 0 {
				parts = append(parts, fmt.Sprintf("0x%d", idle))
			}
			parts = append(parts, s+"x4", "0x1")
		}
		out[w] = fmt.Sprintf("%d:%s", from.Unix(), strings.Join(parts, ","))
	}
	until := from.Add(time.Duration(len(l.Wires)*len(l.Steps)) * wireLoadSlot)
	return out, from, until
}

func wireStandPairs() []string {
	pairs := make([]string, 0, len(WireStandPorts))
	for _, w := range WireStandPorts {
		pairs = append(pairs, w.Wire+"="+strconv.Itoa(w.Port))
	}
	return pairs
}

// StandWireStand stands up or updates the stand on both boxes: the exit's
// first, so the entry finds it listening. The secret is kept from the exit's
// last stand, so an update does not change keys under a running entry.
//
// bench, when set, is a bench window of that length on the entry: it starts
// at the first ten minutes of the clock at least 5 minutes away (the stand is
// up by then) and gives each wire 12 MiB every ten minutes and one load of
// 100 Mbit/s for 90 s (edge wirestand.go nextBulk).
func (p *Provisioner) StandWireStand(ctx context.Context, entryBox, exitBox, sni string, bench time.Duration, load *WireLoad) error {
	if sni == "" || strings.ContainsAny(sni, " \n=,/:") {
		return fmt.Errorf("%w: sni %q", ErrRejected, sni)
	}
	ex, exAcc, err := p.provokeRemote(ctx, exitBox)
	if err != nil {
		return err
	}
	defer ex.Close()
	en, _, err := p.provokeRemote(ctx, entryBox)
	if err != nil {
		return err
	}
	defer en.Close()
	exitAddr, err := netip.ParseAddr(exAcc.address)
	if err != nil {
		return fmt.Errorf("адрес выхода %q: %w", exAcc.address, err)
	}
	logf := func(box string) func(string, ...any) {
		return func(format string, args ...any) {
			applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", box))
		}
	}
	window := ""
	if bench > 0 {
		from, until := wireBenchWindow(time.Now(), bench)
		window = fmt.Sprintf("%d-%d", from.Unix(), until.Unix())
		logf(entryBox)("wire stand: бенч %s – %s UTC", from.UTC().Format("15:04"), until.UTC().Format("15:04"))
	}
	var plans map[string]string
	cc := ""
	if load != nil {
		if err := load.Check(); err != nil {
			return err
		}
		var from, until time.Time
		plans, from, until = load.plans(time.Now())
		cc = load.CC
		logf(entryBox)("wire stand: нагрузка %s × %s, %s – %s UTC", strings.Join(load.Wires, ","), strings.Join(load.Steps, ","), from.UTC().Format("15:04"), until.UTC().Format("15:04"))
	}
	applog.Info(ctx, "entry", "operator wire stand", applog.F("entry", entryBox), applog.F("exit", exitBox), applog.F("sni", sni), applog.F("bench", window))
	return standWireStand(ctx, ex, en, wireStandKit{edge: p.builds, h3: p.wireStandH3Builds, cronet: p.cronetBuilds}, exitAddr, sni, window, plans, cc, logf(exitBox), logf(entryBox))
}

// wireStandKit is what the stand stages: the relay's edge on the exit, the
// cronet edge and libcronet.so on the entry (its wire h3 is Chromium's).
type wireStandKit struct {
	edge, h3, cronet map[string]EdgeBuild
}

// wireBenchWindow starts at the first ten minutes of the clock at least
// 5 minutes after now and lasts d.
func wireBenchWindow(now time.Time, d time.Duration) (time.Time, time.Time) {
	from := now.Add(5 * time.Minute).Truncate(10 * time.Minute)
	if from.Before(now.Add(5 * time.Minute)) {
		from = from.Add(10 * time.Minute)
	}
	return from, from.Add(d)
}

// standWireStand is StandWireStand's work on the two open boxes.
func standWireStand(ctx context.Context, ex, en Remote, kit wireStandKit, exitAddr netip.Addr, sni, bench string, plans map[string]string, cc string, exitLog, entryLog func(string, ...any)) error {
	have, _ := ex.Run(ctx, "cat "+wireStandSecretF+" 2>/dev/null || true", nil)
	secret := strings.TrimSpace(have)
	if !wireStandSecretRe.MatchString(secret) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		secret = hex.EncodeToString(b)
		exitLog("wire stand: новый секрет")
	}
	for _, side := range []struct {
		role string
		r    Remote
		say  func(string, ...any)
	}{{"exit", ex, exitLog}, {"entry", en, entryLog}} {
		builds := kit.edge
		if side.role == "entry" {
			builds = kit.h3
			if _, err := side.r.Run(ctx, "mkdir -p "+wireStandLibDir, nil); err != nil {
				return fmt.Errorf("%s: %w", side.role, err)
			}
			if err := stageLabBinary(ctx, side.r, kit.cronet, wireStandCronet, side.say); err != nil {
				return fmt.Errorf("%s: libcronet: %w", side.role, err)
			}
			if _, err := side.r.Run(ctx, "if [ -f "+wireStandCronet+".new ]; then chmod 644 "+wireStandCronet+".new && mv "+wireStandCronet+".new "+wireStandCronet+"; fi", nil); err != nil {
				return fmt.Errorf("%s: libcronet: %w", side.role, err)
			}
		}
		if err := stageLabBinary(ctx, side.r, builds, wireStandBinary, side.say); err != nil {
			return fmt.Errorf("%s: %w", side.role, err)
		}
		if _, err := side.r.Run(ctx, "mkdir -p "+wireStandDir+" && chmod 700 "+wireStandDir+" && cat > /etc/systemd/system/"+wireStandUnit+"@.service", strings.NewReader(wireStandUnitFile)); err != nil {
			return fmt.Errorf("%s: unit-файл: %w", side.role, err)
		}
		if side.role == "exit" {
			if _, err := side.r.Run(ctx, "umask 077; cat > "+wireStandSecretF, strings.NewReader(secret+"\n")); err != nil {
				return fmt.Errorf("%s: секрет: %w", side.role, err)
			}
		}
		for _, w := range WireStandPorts {
			env := wireStandEnv(side.role, w.Wire, w.Port, exitAddr.String(), sni, secret, bench, plans[w.Wire], cc)
			if _, err := side.r.Run(ctx, "umask 077; cat > "+wireStandDir+"/"+w.Wire+".env.new", strings.NewReader(env)); err != nil {
				return fmt.Errorf("%s: env %s: %w", side.role, w.Wire, err)
			}
		}
		out, err := side.r.Run(ctx, "bash -s -- "+side.role+" "+strings.Join(wireStandPairs(), " "), strings.NewReader(wireStandSwapScript))
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line != "" {
				side.say("%s", line)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: запуск стенда: %w", side.role, err)
		}
	}
	return nil
}

// StopWireStand takes the stand off both boxes, the entry's first.
func (p *Provisioner) StopWireStand(ctx context.Context, entryBox, exitBox string) error {
	applog.Info(ctx, "entry", "operator wire stand off", applog.F("entry", entryBox), applog.F("exit", exitBox))
	for _, side := range []struct{ role, box string }{{"entry", entryBox}, {"exit", exitBox}} {
		r, _, err := p.provokeRemote(ctx, side.box)
		if err != nil {
			return err
		}
		out, err := r.Run(ctx, "bash -s -- "+side.role+" "+strings.Join(wireStandPairs(), " "), strings.NewReader(wireStandOffScript))
		r.Close()
		if err != nil {
			return fmt.Errorf("%s: снятие стенда: %w", side.box, err)
		}
		applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", side.box))
	}
	return nil
}
