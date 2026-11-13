package entry

import (
	"context"
	"fmt"
	"strings"
)

// A trunk entry's unit on a box: the Go entry (trunk.go), or the stand over
// hk-pipe beside it (trunkpipe.go). Both run the same binary
// (hikkari-control/trunkentry) in generations: Type=notify, a reload hands
// over to the binary on disk without a cut (trunkentry/handoff.go). Each has
// its own directory, env file and runtime directory, so the stand stands and
// reloads without touching the entry.
type trunkUnit struct {
	name string // the systemd unit, its runtime directory too
	dir  string // the binary and what it loads, side by side
	env  string // the env file the hub writes
	// desc names it in the unit file's comment, title its Description.
	desc, title string
	// reloadEnv is the env file ExecReload names; empty for the Go entry,
	// whose reload reads its env file without one (and whose unit must not
	// change by a byte: a changed unit is a restart, a cut for everyone).
	reloadEnv string
}

// trunkFile is a file the hub stages into a unit's directory from its
// image: name and its builds by architecture.
type trunkFile struct {
	name   string
	builds map[string]EdgeBuild
}

var goEntryUnit = trunkUnit{name: trunkEntryUnit, dir: trunkEntryDir, env: trunkEntryEnvFile, desc: "the cascade's trunk entry", title: "trunk entry"}

func (u trunkUnit) binary() string { return u.dir + "/nekko-trunk-entry" }

func (u trunkUnit) replacer() *strings.Replacer {
	reload := ""
	if u.reloadEnv != "" {
		reload = " " + u.reloadEnv
	}
	return strings.NewReplacer("@UNIT@", u.name, "@DIR@", u.dir, "@ENV@", u.env, "@BIN@", u.binary(), "@DESC@", u.desc,
		"@TITLE@", u.title, "@RELOAD@", reload)
}

func (u trunkUnit) unitFile() string { return u.replacer().Replace(trunkUnitTemplate) }

// swapScript is the unit's swap; files are the names staged into dir.new.
func (u trunkUnit) swapScript(files []trunkFile) string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.name)
	}
	return strings.NewReplacer("@FILES@", strings.Join(names, " ")).Replace(u.replacer().Replace(trunkSwapTemplate))
}

func (u trunkUnit) offScript() string { return u.replacer().Replace(trunkOffTemplate) }

const trunkUnitTemplate = `# Installed by the nekko hub: @DESC@.
[Unit]
Description=Nekko @TITLE@
After=network-online.target
Wants=network-online.target

[Service]
# Generations (trunkentry/handoff.go): the process says when it is up, a
# newer one takes over as the main process, and a reload hands over to the
# binary on disk without cutting what the entry carries. Timeouts cover a
# child's 30 s to come up and the old generations' quick end on a stop.
Type=notify
NotifyAccess=all
EnvironmentFile=@ENV@
ExecStart=@BIN@ run
ExecReload=+@BIN@ reload@RELOAD@
RuntimeDirectory=@UNIT@
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

// trunkSwapTemplate takes the staged directory (dir.new: the running copy
// plus the files that changed, as file.new), refuses a listen port a process
// of another unit holds, checks the new binary against the new env, swaps
// the directory, opens the ports in ufw, puts the unit on the new build and
// waits until it listens on all of them.
//
// It goes in by `systemctl reload` (trunkentry/handoff.go): the running
// generation starts the new binary on its own sockets and drains, nothing
// is cut. A restart is left for what a reload cannot do: a unit file that
// changed (the first time, too), a build or a running process that cannot
// hand over, ports that move, a reload that failed.
const trunkSwapTemplate = `set -eu
dir=@DIR@
env=@ENV@
unit=/etc/systemd/system/@UNIT@.service
for port in "$@"; do
	# A port is the unit's own when every process holding it is in the
	# unit's cgroup (the stand's hk-pipe holds its ports too).
	for pid in $(ss -tulnpH "sport = :$port" | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u); do
		if ! grep -q "/@UNIT@.service" "/proc/$pid/cgroup" 2>/dev/null; then
			echo "порт $port занят: pid $pid ($(cat /proc/$pid/comm 2>/dev/null))" >&2
			rm -rf "$dir.new" "$env.new" "$unit.new"
			exit 3
		fi
	done
done
for f in @FILES@; do
	if [ -f "$dir.new/$f.new" ]; then mv "$dir.new/$f.new" "$dir.new/$f"; fi
	chmod 755 "$dir.new/$f"
done
chmod 755 "$dir.new"
chmod 600 "$env.new"
# Checked as staged: a pipe the env names is the staged one.
( set -a; . "$env.new"; set +a; if [ -n "${EDGE_TRUNK_PIPE:-}" ]; then EDGE_TRUNK_PIPE="$dir.new/hk-pipe"; fi; "$dir.new/nekko-trunk-entry" check )
rm -rf "$dir.old"
if [ -d "$dir" ]; then mv "$dir" "$dir.old"; fi
mv "$dir.new" "$dir"
rm -rf "$dir.old"
mv "$env.new" "$env"
for f in "$env".*.new; do
	if [ -f "$f" ]; then mv "$f" "${f%.new}"; fi
done
unit_changed=0
if [ -f "$unit.new" ]; then
	if cmp -s "$unit.new" "$unit"; then rm -f "$unit.new"; else mv "$unit.new" "$unit"; unit_changed=1; fi
fi
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	for port in "$@"; do
		ufw allow "$port/tcp" comment @UNIT@ >/dev/null
		ufw allow "$port/udp" comment @UNIT@ >/dev/null
	done
	echo "ufw: $* открыты (tcp и udp)"
fi
systemctl daemon-reload
systemctl enable @UNIT@ >/dev/null 2>&1
how=restart
if [ "$unit_changed" = 0 ] && "$dir/nekko-trunk-entry" handoff-version >/dev/null 2>&1 \
	&& systemctl is-active --quiet @UNIT@ && [ -S /run/@UNIT@/control.sock ]; then
	if systemctl reload @UNIT@; then
		how=reload
	else
		echo "@UNIT@: передача не прошла, перезапуск" >&2
		journalctl -u @UNIT@ -n 20 --no-pager >&2
	fi
fi
if [ "$how" = restart ]; then systemctl restart @UNIT@; fi
for _ in $(seq 1 40); do
	up=1
	systemctl is-active --quiet @UNIT@ || up=0
	for port in "$@"; do
		ss -tln | grep ":$port " >/dev/null || up=0
	done
	if [ $up = 1 ]; then
		if [ "$how" = reload ]; then
			echo "@UNIT@ слушает $*, передан без обрыва"
		else
			echo "@UNIT@ слушает $*"
		fi
		exit 0
	fi
	sleep 0.5
done
journalctl -u @UNIT@ -n 20 --no-pager >&2
exit 1
`

const trunkOffTemplate = `set -eu
ports=""
if [ -f @ENV@ ]; then
	ports=$(sed -n 's/^EDGE_TRUNK_FORWARD=//p' @ENV@ | tr ',' '\n' | cut -d= -f1 | tr '\n' ' ')
fi
systemctl disable --now @UNIT@ >/dev/null 2>&1 || true
if [ -n "$ports" ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	for port in $ports; do
		ufw delete allow "$port/tcp" >/dev/null 2>&1 || true
		ufw delete allow "$port/udp" >/dev/null 2>&1 || true
	done
fi
rm -f /etc/systemd/system/@UNIT@.service /etc/systemd/system/@UNIT@.service.new @ENV@ @ENV@.*
rm -rf @DIR@ @DIR@.new
systemctl daemon-reload
echo "@UNIT@ снят${ports:+, порты $ports закрыты}"
`

// standTrunkUnit is the work of standing u on an open box: files staged
// beside the running copy (a file the box already has is not sent again),
// the unit file, the env written private, extra files beside the env
// (env.<name>, swapped in with it), then the swap. A failed check leaves
// what runs as it was.
func standTrunkUnit(ctx context.Context, r Remote, u trunkUnit, files []trunkFile, env string, extra map[string]string, listen []string, logf func(string, ...any)) error {
	staged := u.dir + ".new"
	if _, err := r.Run(ctx, "rm -rf "+staged+" && mkdir -p "+staged+" && (cp -a "+u.dir+"/. "+staged+"/ 2>/dev/null || true)", nil); err != nil {
		return fmt.Errorf("каталог %s: %w", u.name, err)
	}
	for _, f := range files {
		if err := stageLabBinary(ctx, r, f.builds, staged+"/"+f.name, logf); err != nil {
			return err
		}
	}
	// Beside the running unit: the swap compares, and a unit that changed
	// is a restart, not a reload.
	if _, err := r.Run(ctx, "cat > /etc/systemd/system/"+u.name+".service.new", strings.NewReader(u.unitFile())); err != nil {
		return fmt.Errorf("unit-файл: %w", err)
	}
	if _, err := r.Run(ctx, "umask 077; cat > "+u.env+".new", strings.NewReader(env)); err != nil {
		return fmt.Errorf("env: %w", err)
	}
	for name, body := range extra {
		if _, err := r.Run(ctx, "umask 022; cat > "+u.env+"."+name+".new", strings.NewReader(body)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	out, err := r.Run(ctx, "bash -s -- "+strings.Join(listen, " "), strings.NewReader(u.swapScript(files)))
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			logf("%s", line)
		}
	}
	if err != nil {
		return fmt.Errorf("запуск %s: %w", u.name, err)
	}
	return nil
}
