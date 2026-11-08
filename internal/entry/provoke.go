package entry

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// A provoke receiver (edge EDGE_PROTO=provoke) is the measuring end of опыт 0
// of the РФ-каскад (hikkari-client plans/2026-11-05-ru-cascade.md): testers'
// clients send it stimuli ТСПУ is known to cut, and it says what reached it.
// It stands beside whatever a box already runs — a vantage's prober, a relay —
// on a port of its own, so the same software answers in Russia and abroad.
//
// It is experiment equipment, not a relay: no record, no subscriber, no
// pulse. It runs its own copy of the edge binary under its own unit
// (nekko-provoke), so standing it up never swaps the binary the box's relays
// run: that is the rollout's, with its canary and its rollback.
//
//	cmd/server entry-box provoke <box> [port]   # stand up or update
//	cmd/server entry-box provoke-off <box>      # take it down

const (
	provokeUnit    = "nekko-provoke"
	provokeBinary  = "/usr/local/bin/nekko-provoke"
	provokeEnvFile = "/etc/nekko-provoke.env"
	// DefaultProvokePort is free on every box of the fleet on 08.11 (443 is a
	// relay's on most). One port everywhere keeps the stimulus the same.
	DefaultProvokePort = 2053
)

const provokeUnitFile = `# Installed by the nekko hub: the provoke receiver of опыт 0 (РФ-каскад).
[Unit]
Description=Nekko provoke receiver
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=` + provokeEnvFile + `
ExecStart=` + provokeBinary + ` run
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

// provokeSwapScript checks the new binary against the new env before either
// replaces anything, opens the port in ufw when ufw guards the box, restarts
// the unit and waits until it listens on TCP and UDP.
const provokeSwapScript = `set -eu
port="$1"
bin=` + provokeBinary + `
env=` + provokeEnvFile + `
[ -f "$bin.new" ] || cp "$bin" "$bin.new"
chmod 755 "$bin.new"
chmod 644 "$env.new"
( set -a; . "$env.new"; set +a; "$bin.new" check ) >/dev/null
mv "$bin.new" "$bin"
mv "$env.new" "$env"
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	ufw allow "$port/tcp" comment nekko-provoke >/dev/null
	ufw allow "$port/udp" comment nekko-provoke >/dev/null
	echo "ufw: $port/tcp и $port/udp открыты"
fi
systemctl daemon-reload
systemctl enable ` + provokeUnit + ` >/dev/null 2>&1
systemctl restart ` + provokeUnit + `
for _ in $(seq 1 40); do
	if systemctl is-active --quiet ` + provokeUnit + ` && ss -tln | grep ":$port " >/dev/null && ss -uln | grep ":$port " >/dev/null; then
		echo "` + provokeUnit + ` слушает tcp и udp $port"
		exit 0
	fi
	sleep 0.5
done
journalctl -u ` + provokeUnit + ` -n 20 --no-pager >&2
exit 1
`

// provokeOffScript stops the receiver, closes its port and removes it.
const provokeOffScript = `set -eu
port=""
if [ -f ` + provokeEnvFile + ` ]; then
	port=$(sed -n 's/^EDGE_PROVOKE_LISTEN=.*:\([0-9]*\)$/\1/p' ` + provokeEnvFile + `)
fi
systemctl disable --now ` + provokeUnit + ` >/dev/null 2>&1 || true
if [ -n "$port" ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	ufw delete allow "$port/tcp" >/dev/null 2>&1 || true
	ufw delete allow "$port/udp" >/dev/null 2>&1 || true
fi
rm -f /etc/systemd/system/` + provokeUnit + `.service ` + provokeEnvFile + ` ` + provokeBinary + `
systemctl daemon-reload
echo "` + provokeUnit + ` снят${port:+, порт $port закрыт}"
`

// provokeRemote opens a box the hub already holds a key to, as RunOnBox does.
func (p *Provisioner) provokeRemote(ctx context.Context, box string) (Remote, boxAccess, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return nil, acc, err
	}
	if acc.retired {
		return nil, acc, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if acc.hostKey == "" {
		return nil, acc, fmt.Errorf("%w: ключ хоста %s ещё не записан, сначала разворот", ErrRejected, box)
	}
	hubKey, _, err := p.svc.HubSSHKey(ctx)
	if err != nil {
		return nil, acc, fmt.Errorf("ssh-ключ хаба: %w", err)
	}
	r, err := p.dialBox(ctx, box, acc, ssh.PublicKeys(hubKey))
	return r, acc, err
}

// StandProvoke stands up or updates the provoke receiver on box, then checks
// from the hub that it answers on TCP and UDP. Running it again is the update.
func (p *Provisioner) StandProvoke(ctx context.Context, box string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: порт %d", ErrRejected, port)
	}
	r, acc, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	logf := func(format string, args ...any) {
		applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", box))
	}
	applog.Info(ctx, "entry", "operator provoke on box", applog.F("box", box), applog.F("port", port))
	if err := standProvoke(ctx, r, p.builds, port, logf); err != nil {
		return err
	}
	addr := net.JoinHostPort(acc.address, strconv.Itoa(port))
	if err := checkProvoke(ctx, addr); err != nil {
		return fmt.Errorf("%s:%d стоит, но с хаба не отвечает: %w", box, port, err)
	}
	logf("приёмник %s отвечает с хаба по tcp и udp, порт %d", box, port)
	return nil
}

// StopProvoke takes the provoke receiver off box.
func (p *Provisioner) StopProvoke(ctx context.Context, box string) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator provoke off box", applog.F("box", box))
	out, err := r.Run(ctx, "bash -s", strings.NewReader(provokeOffScript))
	if err != nil {
		return fmt.Errorf("снятие приёмника: %w", err)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", box))
	return nil
}

// standProvoke is StandProvoke's work on an open box.
func standProvoke(ctx context.Context, r Remote, builds map[string]EdgeBuild, port int, logf func(string, ...any)) error {
	out, err := r.Run(ctx, "uname -m", nil)
	if err != nil {
		return err
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[strings.TrimSpace(out)]
	build, ok := builds[arch]
	if arch == "" || !ok {
		return fmt.Errorf("в образе хаба нет бинаря edge под %s", remoteWord(out))
	}
	have, _ := r.Run(ctx, "sha256sum "+provokeBinary+" 2>/dev/null | cut -d' ' -f1", nil)
	if strings.TrimSpace(have) == build.SHA {
		logf("бинарь приёмника %s… уже на месте", build.SHA[:12])
	} else {
		binary, err := build.read()
		if err != nil {
			return err
		}
		if _, err := r.Run(ctx, "cat > "+provokeBinary+".new", bytes.NewReader(binary)); err != nil {
			return fmt.Errorf("загрузка бинаря: %w", err)
		}
		logf("бинарь приёмника %s… загружен (%d МБ)", build.SHA[:12], len(binary)>>20)
	}
	if _, err := r.Run(ctx, "cat > /etc/systemd/system/"+provokeUnit+".service", strings.NewReader(provokeUnitFile)); err != nil {
		return fmt.Errorf("unit-файл: %w", err)
	}
	env := fmt.Sprintf("# Written by the nekko hub: provoke receiver.\nEDGE_PROTO=provoke\nEDGE_PROVOKE_LISTEN=0.0.0.0:%d\n", port)
	if _, err := r.Run(ctx, "cat > "+provokeEnvFile+".new", strings.NewReader(env)); err != nil {
		return fmt.Errorf("env: %w", err)
	}
	out, err = r.Run(ctx, "bash -s -- "+strconv.Itoa(port), strings.NewReader(provokeSwapScript))
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			logf("%s", line)
		}
	}
	if err != nil {
		return fmt.Errorf("запуск приёмника: %w", err)
	}
	return nil
}

// checkProvoke speaks the receiver's wire from the hub: a tagged datagram
// comes back, and SEEN over the control SNI says the datagram arrived.
func checkProvoke(ctx context.Context, addr string) error {
	tag := make([]byte, 8)
	_, _ = rand.Read(tag)
	hexTag := hex.EncodeToString(tag)

	u, err := net.DialTimeout("udp4", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("udp: %w", err)
	}
	defer u.Close()
	packet := append(append([]byte("HKP1"), tag...), make([]byte, 64)...)
	echoed := false
	buf := make([]byte, 2048)
	for i := 0; i < 5 && !echoed && ctx.Err() == nil; i++ {
		_, _ = u.Write(packet)
		_ = u.SetReadDeadline(time.Now().Add(time.Second))
		if n, err := u.Read(buf); err == nil && n == len(packet) {
			echoed = true
		}
	}
	if !echoed {
		return fmt.Errorf("udp: эхо не пришло")
	}

	d := net.Dialer{Timeout: 5 * time.Second}
	raw, err := d.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return fmt.Errorf("tcp: %w", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	c := tls.Client(raw, &tls.Config{ServerName: "www.microsoft.com", NextProtos: []string{"hk-" + hexTag}, InsecureSkipVerify: true})
	if err := c.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if _, err := fmt.Fprintf(c, "SEEN %s\n", hexTag); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return fmt.Errorf("seen: %w", err)
	}
	var seen map[string]struct {
		Hello bool  `json:"hello"`
		UDP   int64 `json:"udp"`
	}
	if err := json.Unmarshal([]byte(line), &seen); err != nil {
		return fmt.Errorf("seen: ответ не JSON")
	}
	if s := seen[hexTag]; !s.Hello || s.UDP == 0 {
		return fmt.Errorf("seen: приёмник не видел своей проверки (hello=%t udp=%d)", s.Hello, s.UDP)
	}
	return nil
}
