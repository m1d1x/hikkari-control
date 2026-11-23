package entry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// A port experiment changes the public TCP port of the stand's pool only.
// The same points forward it to the same listener and TLS engine on the core.
// The source port range separates the stand from production on the same IP.
type standPortAlias struct {
	box                 string
	entry, point, core  netip.Addr
	public, destination uint16
}

func (a standPortAlias) unit() string { return "hikkari-stand-port-" + a.box }
func (a standPortAlias) table() string {
	return "hikkari_stand_port_" + strings.ReplaceAll(a.box, "-", "_")
}

func (a standPortAlias) installScript() string {
	return fmt.Sprintf(`set -eu
table=%[1]s
unit=%[2]s
rules=/etc/$unit.nft
cat > "$rules.new" <<'HKPORT'
table ip %[1]s
delete table ip %[1]s
table ip %[1]s {
	chain pre {
		type nat hook prerouting priority dstnat - 1; policy accept;
		ip saddr %[3]s ip daddr %[4]s tcp sport %[5]s tcp dport %[6]d counter dnat to %[7]s:%[8]d
	}
}
HKPORT
nft -c -f "$rules.new"
mv "$rules.new" "$rules"
cat > "/etc/systemd/system/$unit.service" <<HKUNIT
[Unit]
Description=Hikkari stand TCP port alias
After=network-pre.target
Wants=network-pre.target
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f $rules
ExecStop=/usr/sbin/nft delete table ip $table
[Install]
WantedBy=multi-user.target
HKUNIT
systemctl daemon-reload
systemctl enable "$unit.service"
systemctl start "$unit.service"
nft -f "$rules"
`, a.table(), a.unit(), a.entry, a.point, TrunkPipeSourcePorts, a.public, a.core, a.destination)
}

func (a standPortAlias) removeScript() string {
	return fmt.Sprintf(`set -eu
unit=%s
table=%s
if test -f "/etc/systemd/system/$unit.service"; then
	systemctl disable --now "$unit.service"
	rm -f "/etc/systemd/system/$unit.service" "/etc/$unit.nft"
	systemctl daemon-reload
fi
if nft list table ip "$table" >/dev/null 2>&1; then
	nft delete table ip "$table"
fi
`, a.unit(), a.table())
}

// SetTrunkPipePort arms the alias on all of the stand's existing points
// before editing its wires file. On rollback it confirms TCP on the usual
// port before taking the aliases away. An interrupted operation can be retried.
func (p *Provisioner) SetTrunkPipePort(ctx context.Context, box string, port uint16) error {
	pair, err := p.svc.trunkPairOf(ctx, box)
	if err != nil {
		return err
	}
	if !borderCoreName.MatchString(box) || len(pair.Pipe) == 0 || len(pair.PipePoints) == 0 {
		return fmt.Errorf("%w: у %s нужен стенд с точками", ErrRejected, box)
	}
	if port != 0 && !slices.Contains(relayPorts, int(port)) {
		return fmt.Errorf("%w: порт %d вне набора %v", ErrRejected, port, relayPorts)
	}
	tx, err := p.svc.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('hikkari:stand-port:' || $1::text, 0))`, box); err != nil {
		return err
	}
	core, err := p.standPortCore(ctx, pair.PipeExit(), port)
	if err != nil {
		return err
	}
	entry, err := p.svc.boxIPv4(ctx, box)
	if err != nil {
		return err
	}
	points, err := p.svc.pointAddresses(ctx, pair.PipePoints)
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
		return fmt.Errorf("%s: read stand wires: %w: %s", box, err, raw)
	}
	wires, old, err := standWiresAtPort(raw, points, port)
	if err != nil {
		return err
	}
	if port != 0 {
		if old != 0 && old != port {
			return fmt.Errorf("%w: стенд уже на %d, сначала trunk-pipe-port %s off", ErrRejected, old, box)
		}
		if err := p.updateStandPortAliases(ctx, pair, points, entry, core, port); err != nil {
			return err
		}
	}
	if old != port {
		script := fmt.Sprintf("test \"$(sha256sum %s | cut -d ' ' -f 1)\" = '%x' || { echo 'wires changed during port update' >&2; exit 1; }; ",
			trunkPipeWires, sha256.Sum256([]byte(raw))) + trunkPipeWiresScript
		if out, err := r.Run(ctx, script, strings.NewReader(wires)); err != nil {
			return fmt.Errorf("%s: write stand wires: %w: %s", box, err, out)
		}
	}
	want := port
	if want == 0 {
		want = core.Port()
	}
	if out, err := r.Run(ctx, waitStandPort(want, points), nil); err != nil {
		return fmt.Errorf("%s: стенд не подтвердил TCP на %d, правила сохранены: %w: %s", box, want, err, out)
	}
	if port == 0 {
		if err := p.updateStandPortAliases(ctx, pair, points, entry, core, 0); err != nil {
			return err
		}
	}
	applog.Info(ctx, "entry", "operator stand TCP port", applog.F("box", box), applog.F("port", want))
	return nil
}

func (p *Provisioner) standPortCore(ctx context.Context, box string, port uint16) (netip.AddrPort, error) {
	addr, _, _, _, err := p.svc.trunkPipeExitOf(ctx, box)
	if err != nil {
		return netip.AddrPort{}, err
	}
	core, err := netip.ParseAddrPort(addr)
	if err != nil || !core.Addr().Is4() {
		return netip.AddrPort{}, fmt.Errorf("%w: TCP адрес ядра %q", ErrRejected, addr)
	}
	if port == core.Port() {
		return netip.AddrPort{}, fmt.Errorf("%w: %d уже штатный порт ядра", ErrRejected, port)
	}
	return core, nil
}

func (p *Provisioner) updateStandPortAliases(ctx context.Context, pair TrunkPair, points []string, entry netip.Addr, core netip.AddrPort, port uint16) error {
	for i, name := range pair.PipePoints {
		alias := standPortAlias{box: pair.Entry, entry: entry, point: netip.MustParseAddr(points[i]),
			core: core.Addr(), public: port, destination: core.Port()}
		script := alias.removeScript()
		if port != 0 {
			script = alias.installScript()
		}
		// The ordinary border table already provides SNAT and the route
		// allowlist for the translated core port.
		if _, err := p.onCore(ctx, name, script); err != nil {
			return err
		}
	}
	return nil
}

func standWiresAtPort(raw string, points []string, port uint16) (string, uint16, error) {
	var f struct {
		Points []string `json:"points"`
		Port   uint16   `json:"pool_port"`
	}
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return "", 0, err
	}
	if !slices.Equal(f.Points, points) {
		return "", 0, fmt.Errorf("%w: точки в файле стенда расходятся с настройками hub", ErrRejected)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", 0, err
	}
	if port == 0 {
		delete(fields, "pool_port")
	} else {
		fields["pool_port"] = json.RawMessage(strconv.Itoa(int(port)))
	}
	out, err := json.Marshal(fields)
	return string(out) + "\n", f.Port, err
}

// Only an established TCP connection sourced from the stand's reserved
// range counts, and every point must have one.
func waitStandPort(port uint16, points []string) string {
	lo, hi, _ := strings.Cut(TrunkPipeSourcePorts, "-")
	return fmt.Sprintf(`set -eu
for attempt in $(seq 1 20); do
	ok=1
	for ip in %s; do
		if ! ss -4Hnt state established "( dst $ip and dport = :%d and sport >= :%s and sport <= :%s )" | grep -q .; then
			ok=0
		fi
	done
	test "$ok" = 0 || exit 0
	sleep 1
done
exit 1
`, strings.Join(points, " "), port, lo, hi)
}
