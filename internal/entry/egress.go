package entry

import (
	"context"
	"fmt"
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
// An egress carries any number of receivers, and a receiver moves between
// egresses in place (egress_links.go).
//
//	cmd/server entry-box egress <receiver-box> <egress-box>        # stand up, update or move, then check
//	cmd/server entry-box egress-check <receiver-box> <egress-box>  # address, Russian cut, tunnel cut
//	cmd/server entry-box egress-off <receiver-box> <egress-box>    # the receiver off the egress

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
	// egressTunnelV4 and egressTunnelV6 are the egress's end of every tunnel
	// it carries; a receiver's end is its slot's (egressSlotAddrs).
	egressTunnelV4 = "10.231.0.2"
	egressTunnelV6 = "fd77:231::2"
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

// egressWatchUnit is the receiver's watch of its egresses (H4): the tunnel's
// program in its watch verb, for as long as the box is a receiver.
const egressWatchUnit = "hikkari-egress-watch"

const egressWatchUnitFile = `# Installed by the nekko hub: the receiver watches its egresses and moves to the spare.
[Unit]
Description=Hikkari receiver's watch of its egresses
After=` + egressUnit + `.service

[Service]
ExecStart=` + egressScript + ` watch
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`

// egressUpScript is the tunnel's own program on a box: up, apply, down, off.
// It reads ROLE and the rest from the env the hub writes; an egress reads its
// receivers from the peers file, one line each: public key, tunnel IPv4,
// tunnel IPv6, the receiver's own address.
//
// up builds the link from nothing (boot, first stand). apply brings a link
// that is up to what the files say without taking it down: on a receiver the
// new egress takes the default routes from the old one in one wg call, so
// nothing on the box restarts and the receiver's tunnel address stays; on an
// egress receivers come and go beside the ones it carries.
const egressUpScript = `#!/bin/sh
# Installed by the nekko hub: the tunnel between a receiver and its egress
# (hikkari-control internal/entry/egress.go).
set -eu
dir=` + egressDir + `
ifc=hk-egress
port=` + "51821" + `
key=` + egressKey + `
. "$dir/env"

has_link() {
	ip link show "$ifc" >/dev/null 2>&1
}

# A receiver's own tunnel address; the egress is ` + egressTunnelV4 + ` and ` + egressTunnelV6 + `.
receiver_addrs() {
	ip -4 addr show dev "$ifc" | grep -q " $ADDR4/" || ip addr add "$ADDR4/32" dev "$ifc"
	ip -6 addr show dev "$ifc" | grep -q " $ADDR6/" || ip -6 addr add "$ADDR6/128" dev "$ifc"
}

# The env's egresses by key: the one the hub named (PEER_*) and the spare
# (SPARE_*, H4), which may be empty.
endpoint_of() {
	if [ "$1" = "$PEER_PUB" ]; then echo "$PEER_ENDPOINT"; else echo "${SPARE_ENDPOINT:-}"; fi
}
host_of() {
	h=$(endpoint_of "$1")
	echo "${h%:*}"
}
box_of() {
	if [ "$1" = "$PEER_PUB" ]; then echo "${PEER_BOX:-egress}"; else echo "${SPARE_BOX:-spare}"; fi
}

# active_pub is the peer that holds the default routes: the egress the
# receiver goes out through now.
active_pub() {
	wg show "$ifc" allowed-ips 2>/dev/null | awk '/0\.0\.0\.0\/0/ { print $1; exit }'
}

# receiver_peer points the link at the egresses of the env. allowed-ips are
# one peer's alone in WireGuard: the egress that carries takes 0.0.0.0/0 and
# ::/0 from whoever had them in this one call, the spare keeps only its own
# address (the watch's probe through it, H4), and peers the env no longer
# names go after. The hub's egress carries, unless "keep" says to leave the
# spare carrying where the watch moved to it.
receiver_peer() {
	out=$PEER_PUB
	if [ "${1:-}" = keep ] && [ -n "${SPARE_PUB:-}" ] && [ "$(active_pub)" = "$SPARE_PUB" ]; then
		out=$SPARE_PUB
	fi
	set -- peer "$out" endpoint "$(endpoint_of "$out")" allowed-ips 0.0.0.0/0,::/0 persistent-keepalive 25
	if [ -n "${SPARE_PUB:-}" ]; then
		other=$SPARE_PUB
		[ "$out" = "$PEER_PUB" ] || other=$PEER_PUB
		set -- "$@" peer "$other" endpoint "$(endpoint_of "$other")" allowed-ips "$(host_of "$other")/32" persistent-keepalive 25
	fi
	# fwmark: the tunnel's own packets carry the socket of the packet they
	# wrap, and without a mark of their own they would be marked into the
	# tunnel again (unl1, 16.11: each lap 64 bytes longer, 300k packets).
	wg set "$ifc" private-key "$key" listen-port "$port" fwmark 0x4378 "$@"
	for p in $(wg show "$ifc" peers); do
		[ "$p" = "$PEER_PUB" ] || [ "$p" = "${SPARE_PUB:-}" ] || wg set "$ifc" peer "$p" remove
	done
}

# ping_rtt pings once and prints the round trip in ms, nothing when no answer
# came within a second; through the tunnel with "$ifc" first.
ping_rtt() {
	if [ "$1" = "$ifc" ]; then
		ping -n -q -c1 -W1 -I "$ifc" "$2" 2>/dev/null
	else
		ping -n -q -c1 -W1 "$1" 2>/dev/null
	fi | sed -n 's|^rtt [^=]*= [0-9.]*/\([0-9.]*\)/.*|\1|p'
}

# watch_egresses is the receiver's own watch of its egresses (H4 of the plan).
# Once a second: a ping through the tunnel to each egress's own address (the
# tunnel and the box alive), and through the egress that carries to the
# internet (NET_PROBES). The one that carries silent RED_S seconds running
# and the spare heard within the last 3 s: the spare takes the default
# routes in one wg call, nothing restarts. No move within HOLD_S of the last
# one. The hub reads the lines (hikkari-control internal/cascadewatch
# egress.go), boxes by name, no address:
#
#	egress: minute from=<unix> <box> active tun=N/M rtt_ms=R net=N/M; <box> standby tun=N/M rtt_ms=R
#	egress: moved from <box> to <box>: <why>
#	egress: <box> red, not moved: <why>; <why not>
#	egress: <box> carries now: by the hub
watch_egresses() {
	run=/run/hikkari-egress
	mkdir -p "$run"
	: > "$run/sec"
	bad=0
	sb_heard=0
	moved_at=0
	said_at=0
	# The egress that carried when the watch last looked, kept across its
	# restarts (the hub restarts it on every apply), so a move by the hub is
	# told too.
	last=$(cat "$run/last" 2>/dev/null || true)
	minute=$(($(date +%s) / 60))
	while :; do
		now=$(date +%s)
		# The hub may have taken the spare away since the last read.
		unset SPARE_PUB SPARE_ENDPOINT SPARE_BOX RED_S HOLD_S NET_PROBES
		. "$dir/env"
		red_after=${RED_S:-5}
		hold=${HOLD_S:-60}
		act=$(active_pub)
		if [ -z "$act" ]; then
			# No link or no egress holds the routes: nothing to watch,
			# the guard keeps the relays in.
			bad=0
			sleep 1
			continue
		fi
		sb=
		if [ -n "${SPARE_PUB:-}" ]; then
			sb=$SPARE_PUB
			[ "$act" != "$SPARE_PUB" ] || sb=$PEER_PUB
		fi
		if [ "$act" != "$last" ]; then
			[ -z "$last" ] || echo "egress: $(box_of "$act") carries now: by the hub"
			last=$act
			echo "$last" > "$run/last"
			bad=0
		fi
		ping_rtt "$ifc" "$(host_of "$act")" > "$run/a" &
		if [ -n "$sb" ]; then ping_rtt "$ifc" "$(host_of "$sb")" > "$run/s" & else : > "$run/s"; fi
		i=0
		for t in ${NET_PROBES:-1.1.1.1 8.8.8.8}; do
			ping_rtt "$ifc" "$t" > "$run/n$i" &
			i=$((i + 1))
		done
		wait
		a=$(cat "$run/a")
		s=$(cat "$run/s")
		net=0
		while [ "$i" -gt 0 ]; do
			i=$((i - 1))
			[ ! -s "$run/n$i" ] || net=1
		done
		echo "$(box_of "$act") active ${a:--} $net" >> "$run/sec"
		[ -z "$sb" ] || echo "$(box_of "$sb") standby ${s:--} -" >> "$run/sec"
		[ -z "$s" ] || sb_heard=$now
		if [ -n "$a" ] && [ "$net" = 1 ]; then bad=0; else bad=$((bad + 1)); fi
		if [ "$bad" -ge "$red_after" ]; then
			if [ -n "$a" ]; then
				why="internet silent $bad s through a live tunnel"
			else
				why="tunnel silent $bad s"
			fi
			if [ -n "$sb" ] && [ $((now - sb_heard)) -le 3 ] && [ $((now - moved_at)) -ge "$hold" ]; then
				wg set "$ifc" peer "$sb" allowed-ips 0.0.0.0/0,::/0 peer "$act" allowed-ips "$(host_of "$act")/32"
				moved_at=$now
				last=$sb
				echo "$last" > "$run/last"
				bad=0
				# What died is told after the move, not before it: a ping
				# past the tunnel to the box it left.
				if [ -z "$a" ]; then
					if [ -n "$(ping_rtt "$(host_of "$act")")" ]; then
						why="$why, the box answers past it"
					else
						why="$why, the box silent past it too"
					fi
				fi
				echo "egress: moved from $(box_of "$act") to $(box_of "$sb"): $why"
			elif [ $((now - said_at)) -ge 60 ]; then
				if [ -z "$sb" ]; then
					not="no spare"
				elif [ $((now - sb_heard)) -gt 3 ]; then
					not="spare $(box_of "$sb") silent"
				else
					not="moved $((now - moved_at)) s ago, holding ${hold} s"
				fi
				echo "egress: $(box_of "$act") red, not moved: $why; $not"
				said_at=$now
			fi
		fi
		m=$((now / 60))
		if [ "$m" != "$minute" ]; then
			awk -v from=$((minute * 60)) '
			!($1 in n) { order[++k] = $1 }
			{ n[$1]++; role[$1] = $2 }
			$3 != "-" { ok[$1]++; sum[$1] += $3 }
			$4 != "-" { nn[$1]++; if ($4 == 1) net[$1]++ }
			END {
				line = "egress: minute from=" from
				for (j = 1; j <= k; j++) {
					b = order[j]
					rtt = ok[b] ? sprintf("%.1f", sum[b] / ok[b]) : "-1"
					part = sprintf("%s %s tun=%d/%d rtt_ms=%s", b, role[b], ok[b], n[b], rtt)
					if (nn[b]) part = part sprintf(" net=%d/%d", net[b], nn[b])
					line = line (j == 1 ? " " : "; ") part
				}
				if (k) print line
			}' "$run/sec"
			: > "$run/sec"
			minute=$m
		fi
		while [ "$(date +%s)" = "$now" ]; do sleep 0.2; done
	done
}

# egress_peers makes the link's peers the peers file: each receiver may send
# from its own tunnel address only.
egress_peers() {
	wg set "$ifc" private-key "$key" listen-port "$port"
	keep=
	while read -r pub a4 a6 rx; do
		[ -n "$pub" ] || continue
		wg set "$ifc" peer "$pub" allowed-ips "$a4/32,$a6/128"
		keep="$keep $pub"
	done < "$dir/peers"
	for p in $(wg show "$ifc" peers); do
		case " $keep " in *" $p "*) ;; *) wg set "$ifc" peer "$p" remove ;; esac
	done
	ip -4 route replace 10.231.0.0/16 dev "$ifc"
	ip -6 route replace fd77:231::/64 dev "$ifc"
}

# egress_ufw lets each receiver of the peers file reach the port and takes the
# rules of receivers that left.
egress_ufw() {
	if ! command -v ufw >/dev/null 2>&1 || ! ufw status | grep -q "Status: active"; then
		return 0
	fi
	ufw route allow in on "$ifc" out on "$ext" comment hikkari-egress >/dev/null
	want=" $(awk 'NF == 4 { printf "%s ", $4 }' "$dir/peers")"
	for rx in $want; do
		ufw allow from "$rx" to any port "$port" proto udp comment "hikkari-egress-rx $rx" >/dev/null
	done
	ufw status numbered | sed -n 's/^\[ *\([0-9]*\)\].*# hikkari-egress-rx \([^ ]*\).*$/\1 \2/p' | sort -rn |
		while read -r n rx; do
			case "$want" in *" $rx "*) ;; *) ufw --force delete "$n" >/dev/null ;; esac
		done
}

receiver_guard() {
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
	# Nothing on the box answers through the tunnel (H6): whoever holds the
	# egress reaches no port of the receiver, only replies come back in.
	chain guard_in {
		type filter hook input priority filter; policy accept;
		iifname "$ifc" ct state established,related accept
		iifname "$ifc" drop
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
		# Kept when it stands: a guard reloaded under people leaves no
		# moment without the rule (the guard would drop, not leak, but drop).
		if ! ip $fam rule show priority 4377 | grep -q "fwmark 0x4377 lookup 4377"; then
			ip $fam rule del priority 4377 2>/dev/null || true
			ip $fam rule add fwmark 0x4377 lookup 4377 priority 4377
		fi
	done
}

receiver_up() {
	receiver_guard
	ip link del "$ifc" 2>/dev/null || true
	ip link add "$ifc" type wireguard
	receiver_peer
	receiver_addrs
	ip link set "$ifc" mtu 1420 up
	ip -4 route replace default dev "$ifc" table 4377
	ip -6 route replace default dev "$ifc" table 4377
}

egress_ext() {
	ext=$(ip -o -4 route get 1.1.1.1 | sed -n 's/.* dev \([^ ]*\).*/\1/p')
	[ -n "$ext" ] || { echo "нет внешнего интерфейса" >&2; exit 1; }
}

egress_up() {
	egress_ext
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
	ip link del "$ifc" 2>/dev/null || true
	ip link add "$ifc" type wireguard
	ip addr add ` + egressTunnelV4 + `/16 dev "$ifc"
	ip -6 addr add ` + egressTunnelV6 + `/64 dev "$ifc"
	ip link set "$ifc" mtu 1420 up
	egress_peers
	egress_ufw
}

case "${1:-}" in
up)
	if [ "$ROLE" = receiver ]; then receiver_up; else egress_up; fi
	;;
apply)
	if ! has_link; then
		if [ "$ROLE" = receiver ]; then receiver_up; else egress_up; fi
	elif [ "$ROLE" = receiver ]; then
		# The guard as this program has it, in one nft transaction: a
		# receiver stood up by an older hub gets the newer guard in place.
		receiver_guard
		receiver_peer "${2:-}"
		receiver_addrs
	else
		egress_ext
		egress_peers
		egress_ufw
	fi
	;;
down)
	ip link del "$ifc" 2>/dev/null || true
	;;
watch)
	[ "$ROLE" = receiver ] || { echo "watch: not a receiver" >&2; exit 2; }
	watch_egresses
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
	echo "usage: $0 up|apply [keep]|down|off|watch" >&2
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
	// back points the receiver at the egress it went through before a move
	// (egress_links.go); nil when the receiver is not moving.
	back func(ctx context.Context, slot int) error
	// spare is the receiver's spare egress, which its env keeps through a
	// move (H4); nil when it has none.
	spare *egressSpare
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
	if err := checkEgressAddress(ctx, e); err != nil {
		return err
	}
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
	// Not with relays in the tunnel: taking it down cuts their people.
	attached, err := egressRelaysAttached(ctx, e.rx)
	if err != nil {
		return fmt.Errorf("релеи в туннеле: %w", err)
	}
	if attached {
		e.rxLog("релеи в туннеле: проверка лежащим туннелем пропущена, она оборвала бы людей")
		return checkEgressRule(ctx, e)
	}
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
