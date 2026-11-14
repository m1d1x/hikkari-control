package entry

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The exit's end of Chromium's QUIC wire (h3dgram) for the stand over
// hk-pipe (roadmap 4.6, R6.3; hikkari-client
// plans/2026-11-11-entry-hot-path-and-unit-economics.md, «Труба на Rust»):
// hk-exit, the exit's Rust pipe, without a brain, on a UDP port of its own
// beside the box's Hikkari relay, which it reaches on the relay's trunk
// ports on loopback as the relay's own trunk exit does. It serves only the
// stand's wire under the relay's trunk key and the relay's ACME certificate
// (its domain is the trunk's SNI): no user dials it, and standing it,
// updating it or taking it off never touches the relay. It is a stand: an
// update restarts it, which cuts the stand's QUIC members, and they come
// back from the entry's side.
//
//	cmd/server entry-box trunk-h3-exit <exit-box> <port>
//	cmd/server entry-box trunk-h3-exit-off <exit-box>

const (
	trunkH3ExitUnit = "nekko-trunk-h3exit"
	trunkH3ExitDir  = "/usr/local/lib/nekko-trunk-h3exit"
	trunkH3ExitEnv  = "/etc/nekko-trunk-h3exit.env"
	// trunkH3ExitKey is the trunk key, given to the unit as a credential,
	// so that it is in no process table.
	trunkH3ExitKey = "/etc/nekko-trunk-h3exit.key"
)

// trunkH3ExitUnitFile runs hk-exit as a dynamic user on the certificate
// the start copies into its runtime directory; %d is the unit's
// credentials directory. The wire's id is 6, the entry's for h3dgram
// (trunkentry wires.go).
const trunkH3ExitUnitFile = `# Installed by the nekko hub: the trunk exit's end of Chromium's QUIC wire (stand).
[Unit]
Description=Nekko trunk exit, Chromium QUIC wire (stand)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=` + trunkH3ExitEnv + `
LoadCredential=key:` + trunkH3ExitKey + `
RuntimeDirectory=` + trunkH3ExitUnit + `
ExecStartPre=+` + trunkH3ExitDir + `/cert.sh
ExecStart=` + trunkH3ExitDir + `/hk-exit --listen 127.0.0.1:0 --key-file %d/key --h3 0.0.0.0:${EDGE_TRUNK_H3_PORT} --h3-wire-id 6 --cert /run/` + trunkH3ExitUnit + `/cert.pem --cert-key /run/` + trunkH3ExitUnit + `/key.pem --ports ${EDGE_TRUNK_PORTS}
Restart=always
RestartSec=2
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

// trunkH3ExitCertScript copies the relay's newest ACME certificate for its
// domain (Caddy's storage in the relay's state directory, hikkari-protocol
// server/certs.go CaddyCertificate) into the unit's runtime directory, owned
// by the unit's dynamic user. It runs as root before each start.
const trunkH3ExitCertScript = `#!/bin/sh
set -eu
host=$EDGE_TRUNK_H3_HOST
crt=$(ls -t /var/lib/*/certificates/*/"$host"/"$host".crt /var/lib/private/*/certificates/*/"$host"/"$host".crt 2>/dev/null | head -n 1 || true)
if [ -z "$crt" ]; then
	echo "нет сертификата релея для $host" >&2
	exit 1
fi
d=/run/` + trunkH3ExitUnit + `
u=$(stat -c %u "$d")
g=$(stat -c %g "$d")
install -m 600 -o "$u" -g "$g" "$crt" "$d/cert.pem"
install -m 600 -o "$u" -g "$g" "${crt%.crt}.key" "$d/key.pem"
`

// trunkH3ExitSwap swaps the staged directory in, opens the port in ufw,
// restarts the unit and waits until it listens on the port ($1).
const trunkH3ExitSwap = `set -eu
` + keepUnitsFromNeedrestartScript + `dir=` + trunkH3ExitDir + `
port=$1
for pid in $(ss -ulnpH "sport = :$port" | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u); do
	if ! grep -q "/` + trunkH3ExitUnit + `.service" "/proc/$pid/cgroup" 2>/dev/null; then
		echo "порт $port/udp занят: pid $pid ($(cat /proc/$pid/comm 2>/dev/null))" >&2
		rm -rf "$dir.new"
		exit 3
	fi
done
if [ -f "$dir.new/hk-exit.new" ]; then mv "$dir.new/hk-exit.new" "$dir.new/hk-exit"; fi
chmod 755 "$dir.new" "$dir.new/hk-exit" "$dir.new/cert.sh"
rm -rf "$dir.old"
if [ -d "$dir" ]; then mv "$dir" "$dir.old"; fi
mv "$dir.new" "$dir"
rm -rf "$dir.old"
old=$(sed -n 's/^EDGE_TRUNK_H3_PORT=//p' ` + trunkH3ExitEnv + ` 2>/dev/null || true)
mv ` + trunkH3ExitEnv + `.new ` + trunkH3ExitEnv + `
mv ` + trunkH3ExitKey + `.new ` + trunkH3ExitKey + `
mv /etc/systemd/system/` + trunkH3ExitUnit + `.service.new /etc/systemd/system/` + trunkH3ExitUnit + `.service
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	if [ -n "$old" ] && [ "$old" != "$port" ]; then ufw delete allow "$old/udp" >/dev/null 2>&1 || true; fi
	ufw allow "$port/udp" comment ` + trunkH3ExitUnit + ` >/dev/null
	echo "ufw: $port/udp открыт"
fi
systemctl daemon-reload
systemctl enable ` + trunkH3ExitUnit + ` >/dev/null 2>&1
systemctl restart ` + trunkH3ExitUnit + `
for _ in $(seq 1 40); do
	if systemctl is-active --quiet ` + trunkH3ExitUnit + ` && ss -uln | grep -q ":$port "; then
		echo "` + trunkH3ExitUnit + ` слушает $port/udp"
		exit 0
	fi
	sleep 0.5
done
journalctl -u ` + trunkH3ExitUnit + ` -n 20 --no-pager >&2
exit 1
`

const trunkH3ExitOff = `set -eu
port=$(sed -n 's/^EDGE_TRUNK_H3_PORT=//p' ` + trunkH3ExitEnv + ` 2>/dev/null || true)
systemctl disable --now ` + trunkH3ExitUnit + ` >/dev/null 2>&1 || true
if [ -n "$port" ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	ufw delete allow "$port/udp" >/dev/null 2>&1 || true
fi
rm -f /etc/systemd/system/` + trunkH3ExitUnit + `.service ` + trunkH3ExitEnv + ` ` + trunkH3ExitKey + `
rm -rf ` + trunkH3ExitDir + ` ` + trunkH3ExitDir + `.new
systemctl daemon-reload
echo "` + trunkH3ExitUnit + ` снят${port:+, порт $port/udp закрыт}"
`

// trunkH3ExitEnvOf is the unit's env: the port, the relay's domain and its
// trunk ports on loopback. The key is not in it.
func trunkH3ExitEnvOf(port int, host string, open map[int]bool) string {
	ports := make([]int, 0, len(open))
	for p := range open {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	list := make([]string, len(ports))
	for i, p := range ports {
		list[i] = strconv.Itoa(p)
	}
	return fmt.Sprintf("EDGE_TRUNK_H3_PORT=%d\nEDGE_TRUNK_H3_HOST=%s\nEDGE_TRUNK_PORTS=%s\n", port, host, strings.Join(list, ","))
}

// StandTrunkH3Exit stands up or updates the exit's end of the QUIC wire on
// exitBox at port; the box's Hikkari relay stays as it runs.
func (p *Provisioner) StandTrunkH3Exit(ctx context.Context, exitBox string, port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("%w: порт %d, 1024-65535", ErrRejected, port)
	}
	if len(p.exitBuilds) == 0 {
		return fmt.Errorf("%w: в образе хаба нет hk-exit", ErrRejected)
	}
	_, host, key, open, err := p.svc.trunkExitOf(ctx, exitBox)
	if err != nil {
		return err
	}
	if len(open) == 0 {
		return fmt.Errorf("%w: у релея %s нет портов магистрали", ErrRejected, exitBox)
	}
	r, _, err := p.provokeRemote(ctx, exitBox)
	if err != nil {
		return err
	}
	defer r.Close()
	logf := func(format string, args ...any) {
		applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", exitBox))
	}
	applog.Info(ctx, "entry", "operator trunk h3 exit on box", applog.F("box", exitBox), applog.F("port", port))
	staged := trunkH3ExitDir + ".new"
	if _, err := r.Run(ctx, "rm -rf "+staged+" && mkdir -p "+staged+" && (cp -a "+trunkH3ExitDir+"/. "+staged+"/ 2>/dev/null || true)", nil); err != nil {
		return fmt.Errorf("каталог %s: %w", trunkH3ExitUnit, err)
	}
	if err := stageLabBinary(ctx, r, p.exitBuilds, staged+"/hk-exit", logf); err != nil {
		return err
	}
	writes := []struct{ cmd, body string }{
		{"cat > " + staged + "/cert.sh", trunkH3ExitCertScript},
		{"cat > /etc/systemd/system/" + trunkH3ExitUnit + ".service.new", trunkH3ExitUnitFile},
		{"umask 077; cat > " + trunkH3ExitEnv + ".new", trunkH3ExitEnvOf(port, host, open)},
		{"umask 077; cat > " + trunkH3ExitKey + ".new", key + "\n"},
	}
	for _, w := range writes {
		if _, err := r.Run(ctx, w.cmd, strings.NewReader(w.body)); err != nil {
			return fmt.Errorf("%s: %w", trunkH3ExitUnit, err)
		}
	}
	out, err := r.Run(ctx, "bash -s -- "+strconv.Itoa(port), strings.NewReader(trunkH3ExitSwap))
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			logf("%s", line)
		}
	}
	if err != nil {
		return fmt.Errorf("запуск %s: %w", trunkH3ExitUnit, err)
	}
	return nil
}

// StopTrunkH3Exit takes the wire's end off exitBox; the relay stays.
func (p *Provisioner) StopTrunkH3Exit(ctx context.Context, exitBox string) error {
	r, _, err := p.provokeRemote(ctx, exitBox)
	if err != nil {
		return err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator trunk h3 exit off box", applog.F("box", exitBox))
	out, err := r.Run(ctx, "bash -s", strings.NewReader(trunkH3ExitOff))
	if err != nil {
		return fmt.Errorf("снятие %s: %w", trunkH3ExitUnit, err)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", exitBox))
	return nil
}
