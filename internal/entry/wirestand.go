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
//	cmd/server entry-box wire-stand <entry-box> <exit-box> [sni]   # stand up or update
//	cmd/server entry-box wire-stand-off <entry-box> <exit-box>     # take it down

const (
	wireStandUnit    = "hikkari-wire-stand"
	wireStandBinary  = "/usr/local/bin/hikkari-wire-stand"
	wireStandDir     = "/etc/hikkari-wire-stand"
	wireStandSecretF = wireStandDir + "/secret"
	// DefaultWireStandSNI is the QUIC wires' server name when none is given.
	DefaultWireStandSNI = "www.example.com"
)

// WireStandPorts are the exit's UDP ports, one per wire, apart from the
// relay's 443 and the cascade's 8443 and from the provoke receiver's 2053.
var WireStandPorts = []struct {
	Wire string
	Port int
}{{"quic", 20443}, {"salamander", 20444}, {"awg", 20445}, {"udp", 20446}}

const wireStandUnitFile = `# Installed by the nekko hub: the stand of the trunk's second wire (4.1).
[Unit]
Description=Hikkari trunk second wire stand (%i)
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=` + wireStandDir + `/%i.env
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
rm -rf ` + wireStandDir + ` ` + wireStandBinary + ` /etc/systemd/system/` + wireStandUnit + `@.service
systemctl daemon-reload
echo "` + wireStandUnit + ` снят ($role)"
`

var wireStandSecretRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// wireStandEnv is one process's environment.
func wireStandEnv(role, wire string, port int, exitAddr, sni, secret string) string {
	env := fmt.Sprintf("# Written by the nekko hub: trunk second wire stand, synthetic only.\nEDGE_PROTO=wirestand\nWIRESTAND_ROLE=%s\nWIRESTAND_WIRE=%s\nWIRESTAND_PORT=%d\nWIRESTAND_SNI=%s\nWIRESTAND_SECRET=%s\n",
		role, wire, port, sni, secret)
	if role == "entry" {
		env += "WIRESTAND_EXIT=" + exitAddr + "\n"
	}
	return env
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
func (p *Provisioner) StandWireStand(ctx context.Context, entryBox, exitBox, sni string) error {
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
	applog.Info(ctx, "entry", "operator wire stand", applog.F("entry", entryBox), applog.F("exit", exitBox), applog.F("sni", sni))
	return standWireStand(ctx, ex, en, p.builds, exitAddr, sni, logf(exitBox), logf(entryBox))
}

// standWireStand is StandWireStand's work on the two open boxes.
func standWireStand(ctx context.Context, ex, en Remote, builds map[string]EdgeBuild, exitAddr netip.Addr, sni string, exitLog, entryLog func(string, ...any)) error {
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
			env := wireStandEnv(side.role, w.Wire, w.Port, exitAddr.String(), sni, secret)
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
