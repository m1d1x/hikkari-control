#!/usr/bin/env bash
# Open the admin panel. One command, no URL to remember.
#
#   scripts/admin.sh <ssh-host>   → the hub on that box, through an SSH tunnel
#   scripts/admin.sh local        → the local compose stack (no tunnel)
#
# First passkey, or every device and recovery code lost — print a one-time
# enrolment code on the box:
#
#   scripts/admin.sh enroll <login> <ssh-host|local>
#
# It wipes that operator's passkeys, recovery codes and sessions. Running it
# takes your SSH key — which is the point: the box is the root of trust.
#
# First owner on a new box — asks for the password, prints the enrolment code:
#
#   scripts/admin.sh create <login> <ssh-host|local>
#
# Forgotten password — set a new one on the box:
#
#   scripts/admin.sh passwd <login> <ssh-host|local>
#
# Asks for the new password without echoing it; an empty answer generates one
# and copies it to the clipboard. Every session of that operator is signed
# out; passkeys stay.
#
# The panel and its API share one Caddy port bound to 127.0.0.1 of the box
# (compose.hub.yaml), so the way in is a tunnel carried by your SSH key: no
# key, no panel, whatever the password. The script holds the tunnel in the
# foreground — Ctrl-C closes it, and with it the panel.
#
# The port is the same on both ends: the panel's runtime config names
# https://localhost:9443 as its API origin, and the passkeys are bound to it.
set -euo pipefail

PORT="${ADMIN_HTTPS_PORT:-9443}"
URL="https://localhost:${PORT}"
BOX_DIR="${HIKKARI_DEPLOY_DIR:-/opt/hikkari-control}"
COMPOSE="docker compose -f compose.hub.yaml"

say() { printf '\033[1;34m▸\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; exit 1; }

# on runs a hub CLI command on the target; stdin passes through.
on() {
	local where="$1"
	shift
	if [[ "$where" == "local" ]]; then
		cd "$(dirname "$0")/.." && $COMPOSE exec -T hub /usr/local/bin/hub "$@"
	else
		ssh -o BatchMode=yes "$where" "cd $BOX_DIR && $COMPOSE exec -T hub /usr/local/bin/hub $*"
	fi
}

check_login() {
	[[ -n "$1" ]] || die "usage: scripts/admin.sh $2 <login> <ssh-host|local>"
	[[ "$1" =~ ^[A-Za-z0-9._-]+$ ]] || die "login may only contain letters, digits, dot, dash, underscore"
	[[ -n "${3:-}" ]] || die "usage: scripts/admin.sh $2 <login> <ssh-host|local>"
}

if [[ "${1:-}" == "enroll" ]]; then
	check_login "${2:-}" enroll "${3:-}"
	on "$3" admin-2fa-reset "$2"
	exit 0
fi

if [[ "${1:-}" == "passwd" || "${1:-}" == "create" ]]; then
	ACTION="$1"
	check_login "${2:-}" "$ACTION" "${3:-}"
	LOGIN="$2"
	read -rsp "new password for $LOGIN (empty = generate): " PW
	echo
	GENERATED=0
	if [[ -z "$PW" ]]; then
		PW="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 24 || true)"
		GENERATED=1
	else
		read -rsp "again: " PW2
		echo
		[[ "$PW" == "$PW2" ]] || die "passwords do not match"
	fi
	# The hub enforces the same floor; checking here saves a round trip.
	((${#PW} >= 12)) || die "password must be at least 12 characters"

	# The password travels on stdin, never in argv: argv is visible in ps on
	# both ends.
	if [[ "$ACTION" == "create" ]]; then
		# Prints the one-time enrolment code for the first passkey.
		printf '%s\n' "$PW" | on "$3" admin-create --role=owner "$LOGIN"
	else
		printf '%s\n' "$PW" | on "$3" admin-passwd "$LOGIN"
	fi

	if ((GENERATED)); then
		if command -v pbcopy >/dev/null 2>&1; then
			printf '%s' "$PW" | pbcopy
			say "generated password copied to the clipboard — save it before copying anything else"
		else
			say "generated password: $PW"
		fi
	fi
	exit 0
fi

TARGET="${1:-}"
[[ -n "$TARGET" ]] || die "usage: scripts/admin.sh <ssh-host|local>"

port_busy() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

open_browser() {
	if command -v open >/dev/null 2>&1; then
		open "$URL"
	elif command -v xdg-open >/dev/null 2>&1; then
		xdg-open "$URL" >/dev/null 2>&1 &
	else
		say "open $URL"
	fi
}

# Caddy's certificate on this port is its internal CA's: the check here is
# "something answers", not trust — the browser does that.
wait_for_panel() {
	for _ in $(seq 1 50); do
		if curl -ksS -o /dev/null --max-time 1 "$URL" 2>/dev/null; then
			return 0
		fi
		sleep 0.2
	done
	return 1
}

# Passkeys work only on a page the browser trusts, and the panel's certificate
# is issued by the box's own Caddy CA. On macOS, trust that root once (sudo
# asks for the Mac's password; the browser must be restarted after). The root
# is fetched over SSH from the box itself, so the key is the trust anchor.
trust_box_ca() {
	[[ "$(uname)" == "Darwin" ]] || return 0
	local dir crt
	dir="$(cd "$(dirname "$0")/.." && pwd)/secrets"
	mkdir -p "$dir"
	crt="$dir/$1-caddy-root.crt"
	ssh -o BatchMode=yes "$1" \
		"cd $BOX_DIR && $COMPOSE exec -T caddy cat /data/caddy/pki/authorities/local/root.crt" >"$crt.tmp" ||
		die "could not read the panel's root certificate from $1"
	mv "$crt.tmp" "$crt"
	if security verify-cert -c "$crt" >/dev/null 2>&1; then
		return 0
	fi
	say "trusting the panel certificate of $1 (asks for this Mac's password)"
	sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "$crt"
	say "restart the browser (Cmd+Q) so it picks up the new trust"
}

if [[ "$TARGET" == "local" ]]; then
	wait_for_panel || die "panel is not answering on $URL — run: $COMPOSE up -d"
	open_browser
	say "panel: $URL"
	exit 0
fi

# A local stack holds the same port. Tunnelling over it would either fail or,
# worse, open the LOCAL panel while you think you are on the box.
if port_busy "$PORT"; then
	die "port $PORT is taken on this machine (local stack?). Stop it first: $COMPOSE stop caddy"
fi

say "tunnel to $TARGET (Ctrl-C to close)"
ssh -N \
	-o ExitOnForwardFailure=yes \
	-o ServerAliveInterval=30 \
	-L "${PORT}:127.0.0.1:${PORT}" \
	"$TARGET" &
SSH_PID=$!
trap 'kill "$SSH_PID" 2>/dev/null || true' EXIT INT TERM

if ! wait_for_panel; then
	kill -0 "$SSH_PID" 2>/dev/null || die "ssh exited — check: ssh $TARGET true"
	die "tunnel is up but the panel does not answer — is the stack running on $TARGET?"
fi

trust_box_ca "$TARGET"
open_browser
say "panel: $URL"
wait "$SSH_PID"
