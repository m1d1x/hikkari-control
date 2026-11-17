#!/usr/bin/env bash
# egress-rule-check: does a receiver keep the rule of its egress (H6 of
# hikkari-client plans/2026-11-16-hidden-border-receiver-and-egress.md)?
# The rule: what the relays open goes to the internet only through the tunnel
# to the egress, never from the receiver's own address, and nothing on the
# box answers through the tunnel. The kernel holds it (egress.go,
# receiver_guard); this checks that it does, the way entry-blind-check.sh
# checks the entry's blindness. Read-only but for one probe through the
# tunnel, prints no secret and no destination of a user, exits 1 on any FAIL.
#
# The hub runs it before and after every swap of a relay on a receiver in
# the tunnel (rollout.go, egressRule) and in egress-check; a FAIL stops the
# rollout. By hand, through the hub:
#
#   ssh hikkari-main docker exec -i hikkari-fleet-hub-1 /usr/local/bin/hub \
#     entry-box ssh <box> 'sudo bash -s' < internal/entry/egress_rule_check.sh
#
# FAIL: a way past the tunnel or into the box through it. WARN: worth a look,
# not a breach. INFO: the box's own ways out, for the list of H6.
set -u
fails=0
warns=0
fail() { echo "FAIL $*"; fails=$((fails + 1)); }
warn() { echo "WARN $*"; warns=$((warns + 1)); }
ok() { echo "OK   $*"; }
info() { echo "INFO $*"; }

ifc=hk-egress
user=hikkari-egress
mark_in=0x00004377  # the routed user's packets, into table 4377
mark_wg=0x00004378  # the tunnel's own packets, which the guard lets by
# The only capability a relay holds: binding 80 and 443.
caps_allowed=$((1 << 10))

echo "== $(hostname) $(date -u +%FT%TZ)"

uid=$(id -u "$user" 2>/dev/null) || { fail "no user $user: not a receiver behind an egress"; echo "== fail=$fails warn=$warns"; exit 1; }
ruleset=$(nft list table inet hikkari_egress 2>/dev/null)

# 1. The guard: the routed user's new flows are marked into table 4377 and
# dropped on any interface but the tunnel and loopback.
if [ -z "$ruleset" ]; then
	fail "no nft table inet hikkari_egress: nothing keeps the relays in the tunnel"
else
	echo "$ruleset" | grep -q "meta mark != $mark_wg meta skuid $uid ct direction original fib daddr type != local meta mark set $mark_in" &&
		ok "route_out marks uid $uid into table 4377" || fail "route_out does not mark uid $uid"
	echo "$ruleset" | grep -q "meta mark != $mark_wg meta skuid $uid ct direction original oifname != { \"lo\", \"$ifc\" } counter name \"guard_drop\" drop" &&
		ok "guard_post drops uid $uid past $ifc" || fail "guard_post does not drop uid $uid past $ifc"
	if echo "$ruleset" | grep -q "chain guard_in"; then
		echo "$ruleset" | grep -q "iifname \"$ifc\" ct state established,related accept" &&
			echo "$ruleset" | grep -q "iifname \"$ifc\" drop" &&
			ok "guard_in: nothing new comes in through $ifc" || fail "guard_in is there but does not drop new flows from $ifc"
		guard_in=1
	else
		guard_in=0
	fi
	drops=$(nft list counter inet hikkari_egress guard_drop 2>/dev/null | sed -n 's/.*packets \([0-9]*\).*/\1/p')
	[ "${drops:-0}" = 0 ] && ok "guard_drop 0 packets" ||
		warn "guard_drop ${drops} packets since the guard was loaded: the relays tried past the tunnel (tunnel down?)"
fi
# Whoever else sets the tunnel's own mark lets a packet past the guard.
other_wg=$(nft list ruleset 2>/dev/null | grep -c "mark set $mark_wg")
[ "$other_wg" = 0 ] && ok "no rule sets the tunnel's mark $mark_wg" || fail "$other_wg nft rules set the tunnel's mark $mark_wg"
for fam in -4 -6; do
	ip $fam rule show | grep -q "^4377:.*fwmark 0x4377 lookup 4377" &&
		ok "ip $fam rule fwmark 0x4377 → table 4377" || fail "ip $fam: no rule fwmark 0x4377 → table 4377"
	routes=$(ip $fam route show table 4377 2>/dev/null)
	echo "$routes" | grep -q "^unreachable default.*metric 4096" &&
		ok "ip $fam table 4377: unreachable under the tunnel's route" || fail "ip $fam table 4377 has no unreachable default: a dead tunnel falls back to the main table"
	echo "$routes" | grep -q "^default dev $ifc" &&
		ok "ip $fam table 4377: default via $ifc" || warn "ip $fam table 4377: no default via $ifc (tunnel down?)"
done
wg_mark=$(wg show "$ifc" fwmark 2>/dev/null)
[ "$wg_mark" = "0x4378" ] && ok "wg $ifc marks its own packets 0x4378" || fail "wg $ifc fwmark '${wg_mark:-none}': its packets would be marked into the tunnel again"

# 2. Every relay runs in the tunnel: its unit's drop-in, every process of its
# cgroup under the routed uid, no capability that steps past the guard
# (NET_ADMIN sets the tunnel's mark, NET_RAW sends past netfilter), names
# resolved through the tunnel.
relays=0
for env in /etc/nekko-edge/*.env; do
	[ -f "$env" ] || continue
	name=$(basename "$env" .env)
	unit="nekko-edge@$name.service"
	relays=$((relays + 1))
	[ -f "/etc/systemd/system/$unit.d/egress.conf" ] && ok "$unit: drop-in egress.conf" || fail "$unit has no egress.conf: it goes out from the receiver's address"
	if ! systemctl is-active --quiet "$unit"; then
		warn "$unit is not running"
		continue
	fi
	cg=$(systemctl show -p ControlGroup --value "$unit")
	procs=$(cat "/sys/fs/cgroup$cg/cgroup.procs" 2>/dev/null)
	[ -n "$procs" ] || { fail "$unit: no processes in its cgroup $cg"; continue; }
	n=0
	for p in $procs; do
		[ -d "/proc/$p" ] || continue
		n=$((n + 1))
		comm=$(cat "/proc/$p/comm" 2>/dev/null)
		puid=$(awk '/^Uid:/ {print $3}' "/proc/$p/status" 2>/dev/null)
		eff=$(awk '/^CapEff:/ {print $2}' "/proc/$p/status" 2>/dev/null)
		bnd=$(awk '/^CapBnd:/ {print $2}' "/proc/$p/status" 2>/dev/null)
		[ -n "$puid" ] || continue
		[ "$puid" = "$uid" ] || fail "$unit: pid $p $comm runs as uid $puid, not $uid"
		if [ $((0x${eff:-0} & ~caps_allowed)) != 0 ] || [ $((0x${bnd:-0} & ~caps_allowed)) != 0 ]; then
			fail "$unit: pid $p $comm holds capabilities CapEff=$eff CapBnd=$bnd beyond NET_BIND_SERVICE"
		fi
		ns=$(grep -E '^nameserver' "/proc/$p/root/etc/resolv.conf" 2>/dev/null | awk '{print $2}' | tr '\n' ' ')
		case " $ns" in
		*" 127."* | *" ::1 "* | " ") fail "$unit: pid $p $comm resolves through the box's own resolver ($ns)" ;;
		esac
	done
	ok "$unit: $n processes under uid $uid, capabilities and resolver checked"
done
[ "$relays" -gt 0 ] || warn "no relays in /etc/nekko-edge"
others=$(ps -eo uid=,pid=,comm= | awk -v u="$uid" '$1 == u {print $2}' | while read -r p; do
	grep -q 'nekko-edge@' "/proc/$p/cgroup" 2>/dev/null || echo "$p:$(cat /proc/$p/comm 2>/dev/null)"
done | tr '\n' ' ')
[ -z "$others" ] || warn "uid $uid outside the relays (in the tunnel too): $others"

# 3. Through the tunnel the internet sees the egress, not the receiver: a
# probe as the routed user, the way a relay opens its flows. The kernel's
# own route for it first.
via=$(ip -4 route get 1.1.1.1 mark 0x4377 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p')
[ "$via" = "$ifc" ] && ok "route of the routed user to 1.1.1.1: dev $ifc" || fail "route of the routed user to 1.1.1.1: dev ${via:-unreachable}"
own=" $(ip -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | tr '\n' ' ')"
endpoint=$(sed -n 's/^PEER_ENDPOINT=\(.*\):[0-9]*$/\1/p' /etc/hikkari-egress/env 2>/dev/null | tr -d '[]')
seen=$(systemd-run --quiet --wait --pipe --collect --uid="$user" -p RuntimeMaxSec=20 \
	curl -4 -s -m 10 https://1.1.1.1/cdn-cgi/trace 2>/dev/null | sed -n 's/^ip=//p')
case "$own " in
*" $seen "*) fail "the internet sees the routed user as the receiver's own address" ;;
*)
	if [ -z "$seen" ]; then
		fail "the probe through the tunnel got no answer"
	elif [ "$seen" = "$endpoint" ]; then
		ok "the internet sees the routed user as the egress ($endpoint)"
	else
		warn "the internet sees the routed user as $seen, the tunnel's endpoint is $endpoint"
	fi
	;;
esac

# 4. Nothing answers through the tunnel: without guard_in a listener on a
# wildcard or on the tunnel's address is open to whoever holds the egress.
tun_addrs=" $(ip -o addr show dev "$ifc" 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | tr '\n' ' ')"
open=$(ss -H -tuln 2>/dev/null | awk '{print $1, $5}' | while read -r net local; do
	addr=${local%:*}
	addr=${addr%\%*}
	addr=$(echo "$addr" | tr -d '[]')
	port=${local##*:}
	case "$addr" in 127.* | ::1) continue ;; esac
	case "$addr" in
	0.0.0.0 | :: | '*') echo "$net/$port" ;;
	*) case "$tun_addrs " in *" $addr "*) echo "$net/$port@$addr" ;; esac ;;
	esac
done | sort -u | tr '\n' ' ')
if [ "${guard_in:-0}" = 1 ]; then
	ok "listeners on every address are closed to the tunnel by guard_in"
elif [ -z "$open" ]; then
	ok "no listener on a wildcard or on the tunnel's address"
else
	fail "open through the tunnel (no guard_in): $open"
fi

# 5. The box's own ways out: what goes from the receiver's address. Users'
# flows (uid $uid) are counted, never named.
echo "== the box's own ways out"
listen_ports=" $(ss -H -tuln 2>/dev/null | awk '{print $5}' | sed 's/.*://' | sort -u | tr '\n' ' ')"
ss -H -tunp state established 2>/dev/null | while read -r net _ _ local peer procs; do
	lport=${local##*:}
	case "$listen_ports" in *" $lport "*) continue ;; esac
	pid=$(echo "$procs" | sed -n 's/.*pid=\([0-9]*\).*/\1/p')
	puid=$( [ -n "$pid" ] && awk '/^Uid:/ {print $2}' "/proc/$pid/status" 2>/dev/null)
	comm=$(echo "$procs" | sed -n 's/^users:(("\([^"]*\)".*/\1/p')
	if [ "$puid" = "$uid" ]; then
		echo "tunnel $net ${comm:-?}"
	else
		echo "own $net ${comm:-kernel} → $peer"
	fi
done | sort | uniq -c | while read -r n kind rest; do
	if [ "$kind" = tunnel ]; then
		info "through the tunnel: $n flows of $rest"
	else
		info "from the box's address: $n× $rest"
	fi
done
info "wg $ifc → $endpoint:51821 (kernel)"
info "DNS of the box: $(resolvectl dns 2>/dev/null | awk -F': ' 'NF > 1 && $2 != "" {print $2}' | tr '\n' ' ')$(systemctl is-active --quiet unbound && echo ' unbound (recursive from the box)')"
info "NTP: $(timedatectl show-timesync -p ServerName --value 2>/dev/null || echo none)"
info "apt sources: $(grep -rhoE '(https?://[^ /]+)|^URIs: *[^ ]+' /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null | sed 's/^URIs: *//' | sort -u | tr '\n' ' ')"
timers=$(systemctl list-timers --all --no-legend 2>/dev/null | grep -oE '[a-z0-9@.-]+\.timer' |
	grep -E 'apt|motd-news|snapd|fwupd|update-notifier|ua-|ubuntu-advantage|popularity|esm|man-db' | tr '\n' ' ')
info "timers that go online: ${timers:-none}"
for u in snapd ubuntu-advantage nekko-entry-prober nekko-provoke; do
	systemctl is-active --quiet "$u" 2>/dev/null && info "service $u is running (its own way out)"
done
crons=$(ls /etc/cron.d /var/spool/cron/crontabs 2>/dev/null | grep -v '^$\|:$\|placeholder' | tr '\n' ' ')
info "cron: ${crons:-none}"

echo "== fail=$fails warn=$warns"
[ "$fails" = 0 ]
