package entry

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The receiver's way to the internet through a dumb egress (hikkari-client
// plans/2026-11-16-hidden-border-receiver-and-egress.md, H1; step 3 of the
// roadmap's queue). The receiver takes the trunk from Russia and never shows
// its address to the internet: what its routed processes open goes through a
// WireGuard tunnel to the egress, which masquerades it (variant A of the
// plan) and holds no keys of users and no state of theirs.
//
// What goes into the tunnel is chosen by the kernel, not by the program: a
// socket of the user hikkari-egress (the uid a routed unit runs under) that
// opens a connection or a flow (conntrack's original direction) is marked
// and routed into the tunnel; what answers a connection made to the box (the
// trunk from the entry) goes back the way it came. When the tunnel is down
// the mark leads to an unreachable route and a firewall rule drops such a
// packet on any other interface: it never leaves from the receiver's own
// address.
//
// The egress never sends a packet of the tunnel to a Russian network (RIPE's
// delegated RU blocks, staged by the hub): the owner's word of 16.11, a
// Russian site that sees the egress may hand its address to the regulator.
// Russian destinations go past the tunnel on the client in both of its modes;
// this is the egress's own guard should one slip through.
//
//	cmd/server entry-box egress <receiver-box> <egress-box>        # stand up or update, then check
//	cmd/server entry-box egress-check <receiver-box> <egress-box>  # address, Russian cut, tunnel cut
//	cmd/server entry-box egress-off <receiver-box> <egress-box>    # take it all away

const (
	egressDir    = "/etc/hikkari-egress"
	egressScript = "/usr/local/sbin/hikkari-egress"
	egressUnit   = "hikkari-egress-tunnel"
	// egressKey is the box's own WireGuard key, made there and never read
	// by the hub. It lives in /etc/wireguard: Ubuntu's AppArmor profile of
	// wg reads keys only there (26.04: apparmor DENIED, profile wg).
	egressKey = "/etc/wireguard/hk-egress.key"
	// EgressUser is the uid whose connections the receiver routes into the
	// tunnel; a unit that must reach the internet only through the egress
	// runs as this user.
	EgressUser = "hikkari-egress"
	// EgressPort is WireGuard's UDP port on both ends.
	EgressPort = 51821
	// egressCheckV4 and egressCheckV6 answer with the address they see,
	// without a name to resolve (Cloudflare's trace on 1.1.1.1).
	egressCheckV4 = "https://1.1.1.1/cdn-cgi/trace"
	egressCheckV6 = "https://[2606:4700:4700::1111]/cdn-cgi/trace"
	// egressCheckRU is an address in a Russian network (Yandex, 77.88.0.0/18)
	// the egress must refuse.
	egressCheckRU = "http://77.88.55.242/"
)

// egressUnitFile brings the tunnel up at boot on either end; ExecStop takes
// the link down but leaves the receiver's guard and its unreachable route.
const egressUnitFile = `# Installed by the nekko hub: the tunnel between a receiver and its egress.
[Unit]
Description=Hikkari receiver to egress tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + egressScript + ` up
ExecStop=` + egressScript + ` down

[Install]
WantedBy=multi-user.target
`

// egressUpScript is the tunnel's own program on a box: up, down, off. It
// reads ROLE and the peer from the env the hub writes.
const egressUpScript = `#!/bin/sh
# Installed by the nekko hub: the tunnel between a receiver and its egress
# (hikkari-control internal/entry/egress.go).
set -eu
dir=` + egressDir + `
ifc=hk-egress
port=` + "51821" + `
key=` + egressKey + `
. "$dir/env"

link_up() {
	ip link del "$ifc" 2>/dev/null || true
	ip link add "$ifc" type wireguard
	if [ "$ROLE" = receiver ]; then
		# fwmark: the tunnel's own packets carry the socket of the packet they
		# wrap, and without a mark of their own they would be marked into the
		# tunnel again (unl1, 16.11: each lap 64 bytes longer, 300k packets).
		wg set "$ifc" private-key "$key" listen-port "$port" fwmark 0x4378 \
			peer "$PEER_PUB" endpoint "$PEER_ENDPOINT" allowed-ips 0.0.0.0/0,::/0 persistent-keepalive 25
		ip addr add 10.231.0.1/30 dev "$ifc"
		ip -6 addr add fd77:231::1/126 dev "$ifc"
	else
		wg set "$ifc" private-key "$key" listen-port "$port" \
			peer "$PEER_PUB" allowed-ips 10.231.0.1/32,fd77:231::1/128
		ip addr add 10.231.0.2/30 dev "$ifc"
		ip -6 addr add fd77:231::2/126 dev "$ifc"
	fi
	ip link set "$ifc" mtu 1420 up
}

receiver_up() {
	uid=$(id -u ` + EgressUser + `)
	# The guard first: from here on the routed user's connections leave
	# through the tunnel or not at all.
	nft -f - <<EOF
table inet hikkari_egress
delete table inet hikkari_egress
table inet hikkari_egress {
	counter guard_drop {}
	chain route_out {
		type route hook output priority mangle; policy accept;
		meta mark != 0x4378 meta skuid $uid ct direction original fib daddr type != local meta mark set 0x4377
	}
	# In postrouting, not output: output keeps the device it had before the
	# mark rerouted the packet, and would drop what goes into the tunnel.
	chain guard_post {
		type filter hook postrouting priority filter; policy accept;
		meta mark != 0x4378 meta skuid $uid ct direction original oifname != { "lo", "$ifc" } counter name "guard_drop" drop
	}
	chain nat_out {
		type nat hook postrouting priority srcnat; policy accept;
		oifname "$ifc" masquerade
	}
	chain mss_out {
		type filter hook postrouting priority mangle; policy accept;
		oifname "$ifc" tcp flags & (syn | rst) == syn tcp option maxseg size set rt mtu
	}
}
EOF
	for fam in -4 -6; do
		ip $fam route replace unreachable default metric 4096 table 4377
		ip $fam rule del priority 4377 2>/dev/null || true
		ip $fam rule add fwmark 0x4377 lookup 4377 priority 4377
	done
	link_up
	ip -4 route replace default dev "$ifc" table 4377
	ip -6 route replace default dev "$ifc" table 4377
}

egress_up() {
	ext=$(ip -o -4 route get 1.1.1.1 | sed -n 's/.* dev \([^ ]*\).*/\1/p')
	[ -n "$ext" ] || { echo "нет внешнего интерфейса" >&2; exit 1; }
	# The Russian cut and the table come in one transaction, before the
	# box forwards a single packet.
	{
		cat <<EOF
table inet hikkari_egress
delete table inet hikkari_egress
table inet hikkari_egress {
	set ru4 { type ipv4_addr; flags interval; auto-merge; }
	set ru6 { type ipv6_addr; flags interval; auto-merge; }
	counter ru_cut {}
	counter forwarded {}
	chain tunnel_fwd {
		type filter hook forward priority filter - 5; policy accept;
		iifname "$ifc" tcp flags & (syn | rst) == syn tcp option maxseg size set rt mtu
		oifname "$ifc" tcp flags & (syn | rst) == syn tcp option maxseg size set rt mtu
		iifname "$ifc" ip daddr @ru4 counter name "ru_cut" reject with icmpx type admin-prohibited
		iifname "$ifc" ip6 daddr @ru6 counter name "ru_cut" reject with icmpx type admin-prohibited
		iifname "$ifc" oifname != "$ext" drop
		iifname "$ifc" counter name "forwarded" accept
		oifname "$ifc" ct state established,related accept
		oifname "$ifc" drop
	}
	chain tunnel_nat {
		type nat hook postrouting priority srcnat; policy accept;
		iifname "$ifc" oifname "$ext" masquerade
	}
}
EOF
		cat "$dir/ru.nft"
	} | nft -f -
	cat > /etc/sysctl.d/60-hikkari-egress.conf <<EOF
# Written by the nekko hub: the egress forwards the tunnel to the internet.
net.ipv6.conf.$ext.accept_ra = 2
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
EOF
	sysctl -q -p /etc/sysctl.d/60-hikkari-egress.conf
	if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
		ufw allow from "$PEER_ADDR" to any port "$port" proto udp comment hikkari-egress >/dev/null
		ufw route allow in on "$ifc" out on "$ext" comment hikkari-egress >/dev/null
	fi
	link_up
}

case "${1:-}" in
up)
	if [ "$ROLE" = receiver ]; then receiver_up; else egress_up; fi
	;;
down)
	ip link del "$ifc" 2>/dev/null || true
	;;
off)
	ip link del "$ifc" 2>/dev/null || true
	nft delete table inet hikkari_egress 2>/dev/null || true
	for fam in -4 -6; do
		ip $fam rule del priority 4377 2>/dev/null || true
		ip $fam route flush table 4377 2>/dev/null || true
	done
	if [ "$ROLE" = egress ]; then
		rm -f /etc/sysctl.d/60-hikkari-egress.conf
		sysctl -q -w net.ipv4.ip_forward=0 net.ipv6.conf.all.forwarding=0
		if command -v ufw >/dev/null 2>&1; then
			ufw status numbered | grep hikkari-egress | sed -n 's/^\[ *\([0-9]*\)\].*/\1/p' | sort -rn |
				while read -r n; do ufw --force delete "$n" >/dev/null; done
		fi
	fi
	;;
*)
	echo "usage: $0 up|down|off" >&2
	exit 2
	;;
esac
`

// egressPrepareScript gets a box ready: wg and curl, the routed user on the
// receiver, the box's own key (made there, it never leaves the box). It
// prints the public key.
const egressPrepareScript = aptInstallScript + keepUnitsFromNeedrestartScript + `set -eu
export NEEDRESTART_SUSPEND=1
need=
command -v wg >/dev/null || need="$need wireguard-tools"
command -v curl >/dev/null || need="$need curl"
command -v nft >/dev/null || need="$need nftables"
[ -z "$need" ] || apt_install $need
if [ "$1" = receiver ] && ! id -u ` + EgressUser + ` >/dev/null 2>&1; then
	useradd --system --no-create-home --shell /usr/sbin/nologin ` + EgressUser + `
fi
mkdir -p ` + egressDir + ` /etc/wireguard
chmod 700 /etc/wireguard
# The key once lived beside the env, where wg may not read it.
rm -f ` + egressDir + `/private
chmod 700 ` + egressDir + `
[ -s ` + egressKey + ` ] || (umask 077; wg genkey > ` + egressKey + `)
wg pubkey < ` + egressKey + `
`

// egressStartScript installs the program and the unit and brings the tunnel
// up (again: up rebuilds the link).
const egressStartScript = `set -eu
chmod 755 ` + egressScript + `
chmod 600 ` + egressDir + `/env
systemctl daemon-reload
systemctl enable ` + egressUnit + ` >/dev/null 2>&1
systemctl restart ` + egressUnit + `
systemctl is-active --quiet ` + egressUnit + `
ip -br link show hk-egress
`

// wgKeyRe is a WireGuard public key as wg prints it.
var wgKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw480]=$`)

// egressEnds are the two boxes of one tunnel, opened.
type egressEnds struct {
	rx, eg       Remote
	rxAddr       netip.Addr // the receiver's own address
	egAddr       netip.Addr // the egress's address, what the internet sees
	rxLog, egLog func(string, ...any)
	ruNets       string // nft elements of ru4 and ru6
	rxBox, egBox string
	closeRemotes func()
}

func (p *Provisioner) openEgress(ctx context.Context, rxBox, egBox string) (*egressEnds, error) {
	if rxBox == egBox {
		return nil, fmt.Errorf("%w: приёмник и выход одна машина", ErrRejected)
	}
	eg, egAcc, err := p.provokeRemote(ctx, egBox)
	if err != nil {
		return nil, err
	}
	if egAcc.role != RoleEgress {
		eg.Close()
		return nil, fmt.Errorf("%w: %s не выход (роль %q), выход заводится с role=egress", ErrRejected, egBox, egAcc.role)
	}
	rx, rxAcc, err := p.provokeRemote(ctx, rxBox)
	if err != nil {
		eg.Close()
		return nil, err
	}
	if rxAcc.role == RoleEgress {
		eg.Close()
		rx.Close()
		return nil, fmt.Errorf("%w: %s сам выход", ErrRejected, rxBox)
	}
	rxAddr, err1 := netip.ParseAddr(rxAcc.address)
	egAddr, err2 := netip.ParseAddr(egAcc.address)
	if err1 != nil || err2 != nil || !rxAddr.Is4() || !egAddr.Is4() {
		eg.Close()
		rx.Close()
		return nil, fmt.Errorf("%w: туннель ставится между адресами IPv4", ErrRejected)
	}
	logf := func(box string) func(string, ...any) {
		return func(format string, args ...any) {
			applog.Info(ctx, "entry", fmt.Sprintf(format, args...), applog.F("box", box))
		}
	}
	return &egressEnds{
		rx: rx, eg: eg, rxAddr: rxAddr, egAddr: egAddr,
		rxLog: logf(rxBox), egLog: logf(egBox), rxBox: rxBox, egBox: egBox,
		closeRemotes: func() { rx.Close(); eg.Close() },
	}, nil
}

// StandEgress stands up or updates the tunnel from rxBox to egBox and then
// checks it (CheckEgress).
func (p *Provisioner) StandEgress(ctx context.Context, rxBox, egBox string) error {
	ends, err := p.openEgress(ctx, rxBox, egBox)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	applog.Info(ctx, "entry", "operator egress", applog.F("receiver", rxBox), applog.F("egress", egBox))
	if ends.ruNets, err = fetchRUNets(ctx, ripeDelegatedURL); err != nil {
		return fmt.Errorf("сети РФ для запрета на выходе: %w", err)
	}
	if err := standEgress(ctx, ends); err != nil {
		return err
	}
	return checkEgress(ctx, ends)
}

// CheckEgress checks the tunnel from rxBox to egBox as it stands: the
// internet sees the egress's address over IPv4 and IPv6, a Russian address
// is refused by the egress, and with the tunnel down the routed user reaches
// nothing.
func (p *Provisioner) CheckEgress(ctx context.Context, rxBox, egBox string) error {
	ends, err := p.openEgress(ctx, rxBox, egBox)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	return checkEgress(ctx, ends)
}

// StopEgress takes the tunnel and every rule of it off both boxes.
func (p *Provisioner) StopEgress(ctx context.Context, rxBox, egBox string) error {
	ends, err := p.openEgress(ctx, rxBox, egBox)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	applog.Info(ctx, "entry", "operator egress off", applog.F("receiver", rxBox), applog.F("egress", egBox))
	for _, side := range []struct {
		r    Remote
		logf func(string, ...any)
	}{{ends.rx, ends.rxLog}, {ends.eg, ends.egLog}} {
		cmd := "if [ -x " + egressScript + " ]; then " + egressScript + " off; fi; systemctl disable --now " + egressUnit + " >/dev/null 2>&1 || true; rm -f /etc/systemd/system/" + egressUnit + ".service " + egressScript + "; systemctl daemon-reload"
		if _, err := side.r.Run(ctx, cmd, nil); err != nil {
			return fmt.Errorf("снятие туннеля: %w", err)
		}
		side.logf("туннель выхода снят")
	}
	return nil
}

// standEgress is StandEgress's work on open boxes: keys on both ends, the
// egress first (it only listens), then the receiver.
func standEgress(ctx context.Context, e *egressEnds) error {
	rxPub, err := egressPrepare(ctx, e.rx, "receiver")
	if err != nil {
		return fmt.Errorf("%s: %w", e.rxBox, err)
	}
	egPub, err := egressPrepare(ctx, e.eg, "egress")
	if err != nil {
		return fmt.Errorf("%s: %w", e.egBox, err)
	}
	egEnv := fmt.Sprintf("# Written by the nekko hub: this box is the egress of %s.\nROLE=egress\nPEER_PUB=%s\nPEER_ADDR=%s\n", e.rxBox, rxPub, e.rxAddr)
	rxEnv := fmt.Sprintf("# Written by the nekko hub: this box goes to the internet through %s.\nROLE=receiver\nPEER_PUB=%s\nPEER_ENDPOINT=%s\n", e.egBox, egPub, net.JoinHostPort(e.egAddr.String(), fmt.Sprint(EgressPort)))
	if _, err := e.eg.Run(ctx, "umask 077; cat > "+egressDir+"/ru.nft", strings.NewReader(e.ruNets)); err != nil {
		return fmt.Errorf("%s: сети РФ: %w", e.egBox, err)
	}
	for _, side := range []struct {
		r    Remote
		box  string
		env  string
		logf func(string, ...any)
	}{{e.eg, e.egBox, egEnv, e.egLog}, {e.rx, e.rxBox, rxEnv, e.rxLog}} {
		if _, err := side.r.Run(ctx, "umask 077; cat > "+egressDir+"/env", strings.NewReader(side.env)); err != nil {
			return fmt.Errorf("%s: env: %w", side.box, err)
		}
		if _, err := side.r.Run(ctx, "cat > "+egressScript, strings.NewReader(egressUpScript)); err != nil {
			return fmt.Errorf("%s: программа туннеля: %w", side.box, err)
		}
		if _, err := side.r.Run(ctx, "cat > /etc/systemd/system/"+egressUnit+".service", strings.NewReader(egressUnitFile)); err != nil {
			return fmt.Errorf("%s: unit-файл: %w", side.box, err)
		}
		out, err := side.r.Run(ctx, "sh -s", strings.NewReader(egressStartScript))
		if err != nil {
			return fmt.Errorf("%s: туннель не встал: %w", side.box, err)
		}
		side.logf("туннель выхода поднят: %s", strings.TrimSpace(out))
	}
	return nil
}

// egressPrepare readies one end and returns its public key.
func egressPrepare(ctx context.Context, r Remote, role string) (string, error) {
	out, err := r.Run(ctx, "bash -s -- "+role, strings.NewReader(egressPrepareScript))
	if err != nil {
		return "", fmt.Errorf("подготовка: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	pub := strings.TrimSpace(lines[len(lines)-1])
	if !wgKeyRe.MatchString(pub) {
		return "", fmt.Errorf("подготовка: ключ wg %s", remoteWord(pub))
	}
	return pub, nil
}

// egressProbe runs cmd on the receiver as the routed user, the way a routed
// unit's process would open its connection.
func egressProbe(cmd string) string {
	return "systemd-run --quiet --wait --pipe --collect --uid=" + EgressUser + " -p RuntimeMaxSec=30 " + cmd
}

// traceIP is the address Cloudflare's trace saw.
var traceIPRe = regexp.MustCompile(`(?m)^ip=(\S+)$`)

// egressCounter reads a named counter's packets on the egress.
var counterPacketsRe = regexp.MustCompile(`packets (\d+)`)

func egressCounter(ctx context.Context, r Remote, name string) (string, error) {
	out, err := r.Run(ctx, "nft list counter inet hikkari_egress "+name, nil)
	if err != nil {
		return "", err
	}
	m := counterPacketsRe.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("счётчик %s не читается", name)
	}
	return m[1], nil
}

// checkEgress is CheckEgress's work on open boxes.
func checkEgress(ctx context.Context, e *egressEnds) error {
	// 1. The internet sees the egress.
	out, err := e.rx.Run(ctx, egressProbe("curl -4 -s -m 10 "+egressCheckV4), nil)
	if err != nil {
		return fmt.Errorf("проба через выход не прошла: %w", err)
	}
	m := traceIPRe.FindStringSubmatch(out)
	if m == nil || m[1] != e.egAddr.String() {
		got := "ничего"
		if m != nil {
			got = remoteWord(m[1])
		}
		return fmt.Errorf("через туннель интернет видит %s, а не адрес выхода %s", got, e.egBox)
	}
	e.rxLog("интернет видит адрес выхода %s по IPv4", e.egBox)
	if out, err := e.rx.Run(ctx, egressProbe("curl -6 -s -m 10 "+egressCheckV6), nil); err != nil {
		e.rxLog("IPv6 через выход не прошёл: %v", err)
	} else if m := traceIPRe.FindStringSubmatch(out); m != nil {
		e.rxLog("по IPv6 интернет видит адрес %s", remoteWord(m[1]))
	}
	// 2. The egress refuses a Russian address.
	before, err := egressCounter(ctx, e.eg, "ru_cut")
	if err != nil {
		return fmt.Errorf("%s: %w", e.egBox, err)
	}
	if _, err := e.rx.Run(ctx, egressProbe("curl -4 -s -m 5 -o /dev/null "+egressCheckRU), nil); err == nil {
		return fmt.Errorf("адрес в сети РФ ответил через выход %s", e.egBox)
	}
	after, err := egressCounter(ctx, e.eg, "ru_cut")
	if err != nil {
		return fmt.Errorf("%s: %w", e.egBox, err)
	}
	if after == before {
		return fmt.Errorf("адрес в сети РФ не ответил, но запрет выхода %s его не видел", e.egBox)
	}
	e.egLog("выход отказал адресу в сети РФ (запрет: %s → %s пакетов)", before, after)
	// 3. Tunnel down: the routed user reaches nothing, the guard drops.
	if _, err := e.rx.Run(ctx, "systemctl stop "+egressUnit, nil); err != nil {
		return fmt.Errorf("остановка туннеля: %w", err)
	}
	_, probeErr := e.rx.Run(ctx, egressProbe("curl -4 -s -m 5 "+egressCheckV4), nil)
	if _, err := e.rx.Run(ctx, "systemctl start "+egressUnit, nil); err != nil {
		return fmt.Errorf("туннель не поднялся после проверки: %w", err)
	}
	if probeErr == nil {
		return fmt.Errorf("при лежащем туннеле проба ушла в интернет")
	}
	e.rxLog("при лежащем туннеле трафик встал, туннель снова поднят")
	if _, err := e.rx.Run(ctx, egressProbe("curl -4 -s -m 10 -o /dev/null "+egressCheckV4), nil); err != nil {
		return fmt.Errorf("после проверки туннель не везёт: %w", err)
	}
	return nil
}
