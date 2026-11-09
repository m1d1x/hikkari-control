package entry

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// A lab Reality relay is the other half of опыт 0 of the РФ-каскад
// (hikkari-client plans/2026-11-05-ru-cascade.md): the same Reality-Vision
// edge our fleet runs abroad, stood on a box in Russia, so the stand and the
// testers can send the burned fp=chrome (133) to a Russian address and to ams3
// in one round. If ТСПУ cuts it to the Russian address as well, the cascade's
// entry gains the subscriber nothing against fingerprint cuts.
//
// Like the provoke receiver it is experiment equipment, not a fleet relay: no
// record, no subscriber, no pulse, its own copy of the edge binary under its
// own unit (nekko-lab-reality). One VLESS id, generated on the first stand and
// kept on the box; the hub prints the share link to the operator's shell (never
// to the log) for ops matrix and the testers' probes.
//
//	cmd/server entry-box lab-reality <box> [port] [cover]   # stand up or update
//	cmd/server entry-box lab-reality-off <box>               # take it down

const (
	labRealityUnit    = "nekko-lab-reality"
	labRealityBinary  = "/usr/local/bin/nekko-lab-reality"
	labRealityEnvFile = "/etc/nekko-lab-reality.env"
	// DefaultLabRealityPort is the port our Reality relays hold abroad.
	DefaultLabRealityPort = 443
	// DefaultLabRealityCover is ams3's cover: the experiment compares the
	// two addresses, so everything else stays the same. Опыт A (06.11) found
	// the SNI plays no part in the cut.
	DefaultLabRealityCover = "www.samsung.com"
	// Loopback ports of the per-cover inbound and the echo, apart from a
	// fleet edge's 10001 and from the provoke receiver.
	labRealityPortBase = 21001
	labRealityEchoPort = 21901
)

const labRealityUnitFile = `# Installed by the nekko hub: the lab Reality relay of опыт 0 (РФ-каскад).
[Unit]
Description=Nekko lab Reality relay
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=` + labRealityEnvFile + `
ExecStart=` + labRealityBinary + ` run
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

// labRealitySwapScript refuses a port another process holds, checks the new
// binary against the new env before either replaces anything, opens the port
// in ufw when ufw guards the box, restarts the unit and waits until it listens.
const labRealitySwapScript = `set -eu
port="$1"
bin=` + labRealityBinary + `
env=` + labRealityEnvFile + `
holder=$(ss -tlnpH "sport = :$port" | grep -v ` + labRealityUnit + ` | grep -v 'nekko-lab-real' || true)
if [ -n "$holder" ]; then
	echo "порт $port занят: $holder" >&2
	rm -f "$bin.new" "$env.new"
	exit 3
fi
[ -f "$bin.new" ] || cp "$bin" "$bin.new"
chmod 755 "$bin.new"
chmod 600 "$env.new"
( set -a; . "$env.new"; set +a; "$bin.new" check ) >/dev/null
mv "$bin.new" "$bin"
mv "$env.new" "$env"
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	ufw allow "$port/tcp" comment ` + labRealityUnit + ` >/dev/null
	echo "ufw: $port/tcp открыт"
fi
systemctl daemon-reload
systemctl enable ` + labRealityUnit + ` >/dev/null 2>&1
systemctl restart ` + labRealityUnit + `
for _ in $(seq 1 40); do
	if systemctl is-active --quiet ` + labRealityUnit + ` && ss -tln | grep ":$port " >/dev/null; then
		echo "` + labRealityUnit + ` слушает tcp $port"
		exit 0
	fi
	sleep 0.5
done
journalctl -u ` + labRealityUnit + ` -n 20 --no-pager >&2
exit 1
`

const labRealityOffScript = `set -eu
port=""
if [ -f ` + labRealityEnvFile + ` ]; then
	port=$(sed -n 's/^EDGE_LISTEN=.*:\([0-9]*\)$/\1/p' ` + labRealityEnvFile + `)
fi
systemctl disable --now ` + labRealityUnit + ` >/dev/null 2>&1 || true
if [ -n "$port" ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	ufw delete allow "$port/tcp" >/dev/null 2>&1 || true
fi
rm -f /etc/systemd/system/` + labRealityUnit + `.service ` + labRealityEnvFile + ` ` + labRealityBinary + `
systemctl daemon-reload
echo "` + labRealityUnit + ` снят${port:+, порт $port закрыт}"
`

// labRealityIdentity is what makes the relay the same relay across updates.
type labRealityIdentity struct {
	PrivateKey string // base64url, as Xray takes it
	ShortID    string // hex
	Client     string // VLESS id
}

func newLabRealityIdentity() (labRealityIdentity, error) {
	priv := make([]byte, curve25519.ScalarSize)
	sid := make([]byte, 8)
	id := make([]byte, 16)
	for _, b := range [][]byte{priv, sid, id} {
		if _, err := rand.Read(b); err != nil {
			return labRealityIdentity{}, err
		}
	}
	priv[0] &= 248
	priv[31] = priv[31]&127 | 64
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return labRealityIdentity{
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv),
		ShortID:    hex.EncodeToString(sid),
		Client:     fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]),
	}, nil
}

// publicKey is the X25519 public key a client's pbk names.
func (id labRealityIdentity) publicKey() (string, error) {
	priv, err := base64.RawURLEncoding.DecodeString(id.PrivateKey)
	if err != nil || len(priv) != curve25519.ScalarSize {
		return "", errors.New("ключ Reality в env не читается")
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(pub), nil
}

// labRealityIdentityOf reads the identity back from the box's env file; ok is
// false when the file is missing or incomplete (a first stand).
func labRealityIdentityOf(env string) (labRealityIdentity, bool) {
	var id labRealityIdentity
	sc := bufio.NewScanner(strings.NewReader(env))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "EDGE_PRIVATE_KEY":
			id.PrivateKey = v
		case "EDGE_SHORT_IDS":
			id.ShortID, _, _ = strings.Cut(v, ",")
		case "EDGE_CLIENTS":
			id.Client, _, _ = strings.Cut(v, ",")
		}
	}
	return id, id.PrivateKey != "" && id.ShortID != "" && id.Client != ""
}

func labRealityEnv(id labRealityIdentity, port int, cover string) string {
	return fmt.Sprintf(`# Written by the nekko hub: lab Reality relay (опыт 0), no pulse, no record.
EDGE_PROTO=vless-reality-vision
EDGE_LISTEN=0.0.0.0:%d
EDGE_PRIVATE_KEY=%s
EDGE_SHORT_IDS=%s
EDGE_CLIENTS=%s
EDGE_COVERS=%s
EDGE_LOCAL_PORT_BASE=%d
EDGE_ECHO_PORT=%d
`, port, id.PrivateKey, id.ShortID, id.Client, cover, labRealityPortBase, labRealityEchoPort)
}

// labRealityLink is the share link the client's parser reads, fp=chrome:
// the stand varies fp itself.
func labRealityLink(id labRealityIdentity, address string, port int, cover, label string) (string, error) {
	pub, err := id.publicKey()
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("flow", "xtls-rprx-vision")
	q.Set("security", "reality")
	q.Set("sni", cover)
	q.Set("fp", "chrome")
	q.Set("pbk", pub)
	q.Set("sid", id.ShortID)
	q.Set("type", "tcp")
	u := url.URL{Scheme: "vless", User: url.User(id.Client), Host: net.JoinHostPort(address, strconv.Itoa(port)),
		RawQuery: q.Encode(), Fragment: label}
	return u.String(), nil
}

// StandLabReality stands up or updates the lab Reality relay on box, checks
// from the hub that a TLS handshake with the cover's name completes on it,
// and returns the share link. Running it again keeps the identity.
func (p *Provisioner) StandLabReality(ctx context.Context, box string, port int, cover string) (string, error) {
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("%w: порт %d", ErrRejected, port)
	}
	if cover == "" || strings.ContainsAny(cover, " \n=,/:") {
		return "", fmt.Errorf("%w: cover %q", ErrRejected, cover)
	}
	r, acc, err := p.provokeRemote(ctx, box)
	if err != nil {
		return "", err
	}
	defer r.Close()
	logf := func(format string, args ...any) {
		applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", box))
	}
	applog.Info(ctx, "entry", "operator lab reality on box", applog.F("box", box), applog.F("port", port), applog.F("cover", cover))
	id, err := standLabReality(ctx, r, p.builds, port, cover, logf)
	if err != nil {
		return "", err
	}
	addr := net.JoinHostPort(acc.address, strconv.Itoa(port))
	if err := checkLabReality(ctx, addr, cover); err != nil {
		return "", fmt.Errorf("%s стоит, но с хаба не отвечает: %w", addr, err)
	}
	logf("lab reality %s отвечает с хаба, cover %s", addr, cover)
	return labRealityLink(id, acc.address, port, cover, "lab-reality-"+box)
}

// StopLabReality takes the lab Reality relay off box.
func (p *Provisioner) StopLabReality(ctx context.Context, box string) error {
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator lab reality off box", applog.F("box", box))
	out, err := r.Run(ctx, "bash -s", strings.NewReader(labRealityOffScript))
	if err != nil {
		return fmt.Errorf("снятие lab reality: %w", err)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", box))
	return nil
}

// standLabReality is StandLabReality's work on an open box.
func standLabReality(ctx context.Context, r Remote, builds map[string]EdgeBuild, port int, cover string, logf func(string, ...any)) (labRealityIdentity, error) {
	have, _ := r.Run(ctx, "cat "+labRealityEnvFile+" 2>/dev/null || true", nil)
	id, ok := labRealityIdentityOf(have)
	if ok {
		logf("lab reality: личность с бокса сохранена")
	} else {
		var err error
		if id, err = newLabRealityIdentity(); err != nil {
			return id, err
		}
		logf("lab reality: новая личность")
	}
	if err := stageLabBinary(ctx, r, builds, labRealityBinary, logf); err != nil {
		return id, err
	}
	if _, err := r.Run(ctx, "cat > /etc/systemd/system/"+labRealityUnit+".service", strings.NewReader(labRealityUnitFile)); err != nil {
		return id, fmt.Errorf("unit-файл: %w", err)
	}
	if _, err := r.Run(ctx, "umask 077; cat > "+labRealityEnvFile+".new", strings.NewReader(labRealityEnv(id, port, cover))); err != nil {
		return id, fmt.Errorf("env: %w", err)
	}
	out, err := r.Run(ctx, "bash -s -- "+strconv.Itoa(port), strings.NewReader(labRealitySwapScript))
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			logf("%s", line)
		}
	}
	if err != nil {
		return id, fmt.Errorf("запуск lab reality: %w", err)
	}
	return id, nil
}

// checkLabReality completes a TLS handshake with the cover's name: a Reality
// edge hands a hello without its auth to the cover itself, so a handshake
// means the edge listens and reaches its cover.
func checkLabReality(ctx context.Context, addr, cover string) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	raw, err := d.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return fmt.Errorf("tcp: %w", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	c := tls.Client(raw, &tls.Config{ServerName: cover})
	if err := c.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("tls к cover %s: %w", cover, err)
	}
	return nil
}
