package entry

import (
	"context"
	"fmt"
	"strings"
)

// The relays of a receiver go out to the internet through its egress
// (H1 of hikkari-client plans/2026-11-16-hidden-border-receiver-and-egress.md):
// every nekko-edge instance on the box runs as EgressUser, so whatever its
// process and its hk-exit open is marked into the tunnel by the kernel
// (egress.go), and its names are resolved by public resolvers through the
// tunnel, not by the box's own resolver from the receiver's address. A relay
// does not start while the receiver's guard is not loaded: before the guard
// it would go out from its own address.
//
// Moving a relay in or out changes its unit, so it is a restart, not a
// hand-over: every session of the relay is cut once.
//
//	cmd/server entry-box egress-relays <receiver-box> <egress-box> on|off

// egressResolv is the resolver a relay in the tunnel reads in place of the
// box's: Cloudflare sends no client subnet on to the authorities.
const egressResolv = `# Written by the nekko hub: a relay that goes out through the egress resolves through it.
nameserver 1.1.1.1
nameserver 1.0.0.1
nameserver 2606:4700:4700::1111
options edns0
`

// egressRelayDropIn is the drop-in that puts one relay into the tunnel.
const egressRelayDropIn = `# Installed by the nekko hub: this relay goes to the internet only through the egress tunnel.
[Unit]
Wants=` + egressUnit + `.service
After=` + egressUnit + `.service

[Service]
User=` + EgressUser + `
Group=` + EgressUser + `
ExecStartPre=+/usr/sbin/nft list chain inet hikkari_egress guard_post
BindReadOnlyPaths=` + egressDir + `/resolv.conf:/etc/resolv.conf
`

// egressRelaysScript moves every relay instance of the box in (on) or out
// (off) and prints, per relay, the uid its processes run under.
const egressRelaysScript = `set -eu
how="$1"
uid=$(id -u ` + EgressUser + `)
found=0
for env in /etc/nekko-edge/*.env; do
	[ -f "$env" ] || continue
	name=$(basename "$env" .env)
	found=1
	dir="/etc/systemd/system/nekko-edge@$name.service.d"
	if [ "$how" = on ]; then
		mkdir -p "$dir"
		cat > "$dir/egress.conf" <<'DROPIN'
` + egressRelayDropIn + `DROPIN
	else
		rm -f "$dir/egress.conf"
	fi
done
[ "$found" = 1 ] || { echo "на машине нет релеев" >&2; exit 3; }
systemctl daemon-reload
for env in /etc/nekko-edge/*.env; do
	[ -f "$env" ] || continue
	name=$(basename "$env" .env)
	systemctl restart "nekko-edge@$name"
	ok=0
	for _ in $(seq 1 60); do
		if systemctl is-active --quiet "nekko-edge@$name"; then ok=1; break; fi
		sleep 0.5
	done
	if [ "$ok" = 0 ]; then
		journalctl -u "nekko-edge@$name" -n 20 --no-pager >&2
		exit 1
	fi
	pid=$(systemctl show -p MainPID --value "nekko-edge@$name")
	uids=$(ps -o uid= --ppid "$pid" -p "$pid" | tr -d ' ' | sort -u | tr '\n' ' ')
	in=no
	for u in $uids; do [ "$u" = "$uid" ] && in=yes; done
	echo "$name in=$in uids=$uids"
done
`

// egressRelaysOn reports whether any relay of the box is in the tunnel.
const egressRelaysOnCmd = `ls /etc/systemd/system/nekko-edge@*.service.d/egress.conf >/dev/null 2>&1 && echo yes || echo no`

func egressRelaysAttached(ctx context.Context, r Remote) (bool, error) {
	out, err := r.Run(ctx, egressRelaysOnCmd, nil)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "yes", nil
}

// EgressRelays moves the relays of rxBox into the tunnel to egBox (on) or
// back to the receiver's own address (off). On, it first checks that the
// tunnel carries and stops if it does not, so no relay is restarted onto a
// dead way out.
func (p *Provisioner) EgressRelays(ctx context.Context, rxBox, egBox string, on bool) error {
	ends, err := p.openEgress(ctx, rxBox, egBox)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	return egressRelays(ctx, ends, on)
}

func egressRelays(ctx context.Context, e *egressEnds, on bool) error {
	how := "off"
	if on {
		how = "on"
		if err := checkEgressAddress(ctx, e); err != nil {
			return fmt.Errorf("релеи не переведены: %w", err)
		}
		if _, err := e.rx.Run(ctx, "nft list chain inet hikkari_egress guard_post >/dev/null", nil); err != nil {
			return fmt.Errorf("релеи не переведены: на приёмнике нет защиты туннеля: %w", err)
		}
		// The directory stays closed (the env is in it): systemd binds the
		// file as root, and the relay reads it at /etc/resolv.conf.
		if _, err := e.rx.Run(ctx, "umask 022; cat > "+egressDir+"/resolv.conf", strings.NewReader(egressResolv)); err != nil {
			return fmt.Errorf("резолвер релеев: %w", err)
		}
	}
	out, err := e.rx.Run(ctx, "sh -s -- "+how, strings.NewReader(egressRelaysScript))
	if err != nil {
		return fmt.Errorf("перевод релеев: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		in := f[1] == "in=yes"
		if in != on {
			return fmt.Errorf("релей %s: %s", remoteWord(f[0]), remoteWord(f[1]))
		}
		if on {
			e.rxLog("релей %s ходит в интернет через выход %s", f[0], e.egBox)
		} else {
			e.rxLog("релей %s ходит в интернет своим адресом", f[0])
		}
	}
	if on {
		return checkEgressAddress(ctx, e)
	}
	return nil
}

// checkEgressAddress is the first step of checkEgress alone: the routed
// user is seen by the internet as the egress.
func checkEgressAddress(ctx context.Context, e *egressEnds) error {
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
	return nil
}
