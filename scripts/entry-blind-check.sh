#!/usr/bin/env bash
# entry-blind-check: is a cascade entry blind (hikkari-client plans/ROADMAP.md,
# 3.2)? On the entry there must be no subscriber's key, no record of where
# users go, and nothing that opens their traffic. Read-only, prints no secret
# values, exits 1 on any FAIL.
#
# Runs on the box, through the hub (its key, its audit log):
#
#   ssh hikkari-main docker exec -i hikkari-fleet-hub-1 /usr/local/bin/hub \
#     entry-box ssh <box> 'sudo bash -s' < scripts/entry-blind-check.sh
#
# FAIL: what breaks the promise. WARN: stand equipment on the same box
# (probers, lab clients): their own accounts, not users', but not the
# entry's business either; a production entry has none.
set -u
fails=0
warns=0
fail() { echo "FAIL $*"; fails=$((fails + 1)); }
warn() { echo "WARN $*"; warns=$((warns + 1)); }
ok() { echo "OK   $*"; }

echo "== $(hostname) $(date -u +%FT%TZ)"

# 1. The trunk entry is told the exit, its trunk key and its ports only.
env=/etc/nekko-trunk-entry.env
if [ -f "$env" ]; then
	extra=$(grep -Ev '^(#|$|EDGE_TRUNK_(EXIT|SNI|KEY|MEMBERS|FORWARD|UDP|UDP_ONE_COPY)=)' "$env" | sed 's/=.*//')
	[ -z "$extra" ] && ok "trunk entry env: EDGE_TRUNK_* only" || fail "trunk entry env has more: $extra"
	[ "$(stat -c %a "$env")" = 600 ] && ok "trunk entry env 600" || fail "trunk entry env mode $(stat -c %a "$env")"
else
	warn "no $env: not a trunk entry?"
fi

# 2. Its journal names no client and no destination: the only address in it
# is the exit's.
exit_ip=$(sed -n 's/^EDGE_TRUNK_EXIT=\([^:]*\):.*/\1/p' "$env" 2>/dev/null)
ips=$(journalctl -u nekko-trunk-entry --since -7d -o cat 2>/dev/null | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' | sort -u | grep -vx "${exit_ip:-none}")
[ -z "$ips" ] && ok "trunk entry journal 7d: no address but the exit's" || fail "trunk entry journal 7d names $(echo "$ips" | wc -l) other addresses"

# 3. No server of any proxy: a server holds the keys that open clients' TLS.
# A proxy listening beyond loopback serves others; one on loopback only is a
# client (a prober's or a lab's socks port).
for p in $(pgrep -x 'xray|sing-box|sing-box-lx|naive|caddy|nekko-edge|hysteria|tuic-server|amneziawg-go' 2>/dev/null); do
	cmd=$(tr '\0' ' ' </proc/"$p"/cmdline | cut -c1-120)
	# TCP listeners only: a client's outbound UDP socket shows bound to *.
	public=$(ss -tlnpH 2>/dev/null | grep "pid=$p," | awk '{print $4}' | grep -vE '^(127\.|\[::1\]|::1)' | tr '\n' ' ')
	if [ -n "$public" ]; then
		fail "proxy server: pid $p on $public: $cmd"
	else
		warn "client core on loopback (own account, not a user's): pid $p $cmd"
	fi
done
for u in nekko-edge caddy; do
	systemctl is-active --quiet "$u" 2>/dev/null && fail "relay unit $u is running"
done

# 4. No record of where anyone went: proxy access logs, captures.
for f in /var/log/xray/access.log /var/log/sing-box/*.log /var/log/caddy/*.log; do
	[ -s "$f" ] && fail "non-empty access log $f ($(stat -c %s "$f") B)"
done
# One pass over the root filesystem for captures and dumps.
found=$(find / -xdev \( -path /proc -o -path /usr/share -o -path /usr/lib -o -path /var/lib/docker \) -prune -o -type f \
	\( -name '*.pcap' -o -name '*.pcapng' -o -name '*.cap' -o \( -size +10k \( -name '*.dump' -o -name '*.sql' -o -name '*.sql.gz' -o -name '*.backup' \) \) \) -print 2>/dev/null)
pcaps=$(echo "$found" | grep -E '\.(pcap|pcapng|cap)$' || true)
[ -z "$pcaps" ] && ok "no captures" || fail "captures: $(echo "$pcaps" | wc -l) files, $(echo "$pcaps" | xargs dirname | sort -u | tr '\n' ' ')"
pgrep -x tcpdump >/dev/null && fail "tcpdump running" || ok "no tcpdump"

# 5. Nothing that decrypts: private keys, database dumps, env files with
# secrets, beyond the box's own SSH host keys and the allowed envs.
# Key files are small: only files under 20 KiB are read.
keys=$(find /etc /opt /root /home /srv /var/lib /usr/local -xdev -path /var/lib/docker -prune -o -type f -size -20k -print0 2>/dev/null |
	xargs -0 -r grep -lE -- '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----' 2>/dev/null |
	grep -Ev '^/etc/ssh/ssh_host_|^/etc/ssl/|^/usr/local/share/ca-certificates/|^/var/lib/docker/|^/var/lib/fwupd/')
ssh_keys=$(echo "$keys" | grep -E '^/(root|home/[^/]+)/\.ssh/' || true)
keys=$(echo "$keys" | grep -vE '^/(root|home/[^/]+)/\.ssh/|^$' || true)
[ -n "$ssh_keys" ] && warn "ssh client keys (reach other boxes, not traffic): $(echo "$ssh_keys" | tr '\n' ' ')"
[ -z "$keys" ] && ok "no private keys" || fail "private keys: $(echo "$keys" | wc -l) files, $(echo "$keys" | xargs -n1 dirname | sort -u | tr '\n' ' ')"
dumps=$(echo "$found" | grep -E '\.(dump|sql|sql\.gz|backup)$' || true)
[ -z "$dumps" ] && ok "no database dumps" || fail "database dumps: $(echo "$dumps" | wc -l) files, $(echo "$dumps" | xargs -n1 dirname | sort -u | tr '\n' ' ')"
for f in $(find /etc /opt /root /home /srv /usr/local -xdev \( -name '*.env' -o -name '.env' \) 2>/dev/null); do
	case "$f" in
	/etc/nekko-trunk-entry.env | /etc/nekko-provoke.env) continue ;;
	/etc/*entry-prober/prober.env) warn "prober env $f: $(grep -oE '^[A-Z_0-9]*(KEY|TOKEN|SECRET|PASS)[A-Z_0-9]*' "$f" | tr '\n' ' ')"; continue ;;
	esac
	s=$(grep -oE '^[A-Z_0-9]*(KEY|TOKEN|SECRET|PASS)[A-Z_0-9]*=.' "$f" 2>/dev/null | sed 's/=.$//' | tr '\n' ' ')
	[ -n "$s" ] && fail "secrets in $f: $s"
done

echo "== fail=$fails warn=$warns"
[ "$fails" = 0 ]
