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
//	cmd/server entry-box trunk-entry <box> <exit-box> <listen=port,...> [members]
//	cmd/server entry-box trunk-entry-off <box>

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

const trunkEntryUnitFile = `# Installed by the nekko hub: the cascade's trunk entry.
[Unit]
Description=Nekko trunk entry
After=network-online.target
Wants=network-online.target

[Service]
# Generations (trunkentry/handoff.go): the process says when it is up, a
# newer one takes over as the main process, and a reload hands over to the
# binary on disk without cutting what the entry carries. Timeouts cover a
# child's 30 s to come up and the old generations' quick end on a stop.
Type=notify
NotifyAccess=all
EnvironmentFile=` + trunkEntryEnvFile + `
ExecStart=` + trunkEntryBinary + ` run
ExecReload=+` + trunkEntryBinary + ` reload
RuntimeDirectory=` + trunkEntryUnit + `
TimeoutStartSec=90
TimeoutStopSec=30
Restart=always
RestartSec=2
DynamicUser=yes
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

// trunkEntrySwapScript takes the staged directory (dir.new: the running
// copy plus the files that changed, as file.new), refuses a listen port
// another process holds, checks the new binary with the new library against
// the new env, swaps the directory, opens the ports in ufw, puts the entry on
// the new build and waits until it listens on all of them.
//
// The entry goes in by `systemctl reload` (trunkentry/handoff.go): the running
// generation starts the new binary on its own sockets and drains, nothing
// is cut. A restart is left for what a reload cannot do: a unit file that
// changed (the first time, too), a build or a running process that cannot
// hand over, ports that move, a reload that failed.
const trunkEntrySwapScript = `set -eu
dir=` + trunkEntryDir + `
env=` + trunkEntryEnvFile + `
for port in "$@"; do
	holder=$(ss -tlnpH "sport = :$port" | grep -v nekko-trunk-en || true)
	if [ -n "$holder" ]; then
		echo "порт $port занят: $holder" >&2
		rm -rf "$dir.new" "$env.new" /etc/systemd/system/` + trunkEntryUnit + `.service.new
		exit 3
	fi
done
for f in nekko-trunk-entry libcronet.so; do
	if [ -f "$dir.new/$f.new" ]; then mv "$dir.new/$f.new" "$dir.new/$f"; fi
done
chmod 755 "$dir.new" "$dir.new/nekko-trunk-entry"
chmod 644 "$dir.new/libcronet.so"
chmod 600 "$env.new"
( set -a; . "$env.new"; set +a; "$dir.new/nekko-trunk-entry" check )
rm -rf "$dir.old"
if [ -d "$dir" ]; then mv "$dir" "$dir.old"; fi
mv "$dir.new" "$dir"
rm -rf "$dir.old"
mv "$env.new" "$env"
unit=/etc/systemd/system/` + trunkEntryUnit + `.service
unit_changed=0
if [ -f "$unit.new" ]; then
	if cmp -s "$unit.new" "$unit"; then rm -f "$unit.new"; else mv "$unit.new" "$unit"; unit_changed=1; fi
fi
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	for port in "$@"; do
		ufw allow "$port/tcp" comment ` + trunkEntryUnit + ` >/dev/null
		ufw allow "$port/udp" comment ` + trunkEntryUnit + ` >/dev/null
	done
	echo "ufw: $* открыты (tcp и udp)"
fi
systemctl daemon-reload
systemctl enable ` + trunkEntryUnit + ` >/dev/null 2>&1
how=restart
if [ "$unit_changed" = 0 ] && "$dir/nekko-trunk-entry" handoff-version >/dev/null 2>&1 \
	&& systemctl is-active --quiet ` + trunkEntryUnit + ` && [ -S /run/` + trunkEntryUnit + `/control.sock ]; then
	if systemctl reload ` + trunkEntryUnit + `; then
		how=reload
	else
		echo "` + trunkEntryUnit + `: передача не прошла, перезапуск" >&2
		journalctl -u ` + trunkEntryUnit + ` -n 20 --no-pager >&2
	fi
fi
if [ "$how" = restart ]; then systemctl restart ` + trunkEntryUnit + `; fi
for _ in $(seq 1 40); do
	up=1
	systemctl is-active --quiet ` + trunkEntryUnit + ` || up=0
	for port in "$@"; do
		ss -tln | grep ":$port " >/dev/null || up=0
	done
	if [ $up = 1 ]; then
		if [ "$how" = reload ]; then
			echo "` + trunkEntryUnit + ` слушает $*, передан без обрыва"
		else
			echo "` + trunkEntryUnit + ` слушает $*"
		fi
		exit 0
	fi
	sleep 0.5
done
journalctl -u ` + trunkEntryUnit + ` -n 20 --no-pager >&2
exit 1
`

const trunkEntryOffScript = `set -eu
ports=""
if [ -f ` + trunkEntryEnvFile + ` ]; then
	ports=$(sed -n 's/^EDGE_TRUNK_FORWARD=//p' ` + trunkEntryEnvFile + ` | tr ',' '\n' | cut -d= -f1 | tr '\n' ' ')
fi
systemctl disable --now ` + trunkEntryUnit + ` >/dev/null 2>&1 || true
if [ -n "$ports" ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	for port in $ports; do
		ufw delete allow "$port/tcp" >/dev/null 2>&1 || true
		ufw delete allow "$port/udp" >/dev/null 2>&1 || true
	done
fi
rm -f /etc/systemd/system/` + trunkEntryUnit + `.service /etc/systemd/system/` + trunkEntryUnit + `.service.new ` + trunkEntryEnvFile + `
rm -rf ` + trunkEntryDir + ` ` + trunkEntryDir + `.new
systemctl daemon-reload
echo "` + trunkEntryUnit + ` снят${ports:+, порты $ports закрыты}"
`

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
	addr, sni, key, open, err := p.svc.trunkExitOf(ctx, o.Exit)
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
	if err := standTrunkEntry(ctx, r, p.trunkBuilds, p.cronetBuilds, trunkEntryEnv(addr, sni, key, o), listen, logf); err != nil {
		return err
	}
	// The cascade watch takes its pairs from here (cascade_watch.go).
	return p.svc.recordTrunkEntry(ctx, box, o)
}

// standTrunkEntry is StandTrunkEntry's work on an open box. A file the box
// already has is not sent again; a failed check leaves the running entry as
// it was.
func standTrunkEntry(ctx context.Context, r Remote, bins, libs map[string]EdgeBuild, env string, listen []string, logf func(string, ...any)) error {
	staged := trunkEntryDir + ".new"
	if _, err := r.Run(ctx, "rm -rf "+staged+" && mkdir -p "+staged+" && (cp -a "+trunkEntryDir+"/. "+staged+"/ 2>/dev/null || true)", nil); err != nil {
		return fmt.Errorf("каталог входа: %w", err)
	}
	if err := stageLabBinary(ctx, r, bins, staged+"/nekko-trunk-entry", logf); err != nil {
		return err
	}
	if err := stageLabBinary(ctx, r, libs, staged+"/libcronet.so", logf); err != nil {
		return err
	}
	// Beside the running unit: the swap compares, and a unit that changed
	// is a restart, not a reload.
	if _, err := r.Run(ctx, "cat > /etc/systemd/system/"+trunkEntryUnit+".service.new", strings.NewReader(trunkEntryUnitFile)); err != nil {
		return fmt.Errorf("unit-файл: %w", err)
	}
	if _, err := r.Run(ctx, "umask 077; cat > "+trunkEntryEnvFile+".new", strings.NewReader(env)); err != nil {
		return fmt.Errorf("env: %w", err)
	}
	out, err := r.Run(ctx, "bash -s -- "+strings.Join(listen, " "), strings.NewReader(trunkEntrySwapScript))
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			logf("%s", line)
		}
	}
	if err != nil {
		return fmt.Errorf("запуск входа: %w", err)
	}
	return nil
}

// StopTrunkEntry takes the trunk entry off box.
func (p *Provisioner) StopTrunkEntry(ctx context.Context, box string) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator trunk entry off box", applog.F("box", box))
	out, err := r.Run(ctx, "bash -s", strings.NewReader(trunkEntryOffScript))
	if err != nil {
		return fmt.Errorf("снятие входа: %w", err)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", box))
	return p.svc.forgetTrunkEntry(ctx, box)
}
