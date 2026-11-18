#!/usr/bin/env bash
# Puts the owner's Telegram bot (internal/notify) into the hub's own secrets
# on hikkari-main and starts the hub on them. The file holds two lines:
#
#   HUB_TELEGRAM_BOT_TOKEN=<the token @BotFather gave>
#   HUB_TELEGRAM_CHAT_ID=<the owner's id, from @userinfobot>
#
#   scripts/hub-telegram.sh [file]      (default ../telegram.txt)
#
# The bot first sends the owner a message, so a wrong token or chat stops
# here. The values go to the server on ssh's stdin, never on a command line,
# into /opt/hikkari-control/secrets/hub.env, which a release does not touch
# (deploy/server/hikkari-hub-deploy). The file is deleted after.
set -euo pipefail
cd "$(dirname "$0")/.."
file=${1:-../telegram.txt}
[ -f "$file" ] || { echo "no $file" >&2; exit 1; }

value() { sed -n "s/^$1=//p" "$file" | tr -d '\r[:space:]'; }
token=$(value HUB_TELEGRAM_BOT_TOKEN)
chat=$(value HUB_TELEGRAM_CHAT_ID)
[[ $token =~ ^[0-9]+:[A-Za-z0-9_-]{30,}$ ]] || { echo "HUB_TELEGRAM_BOT_TOKEN in $file is not a bot token (digits:letters)" >&2; exit 1; }
[[ $chat =~ ^-?[0-9]+$ ]] || { echo "HUB_TELEGRAM_CHAT_ID in $file is not a number" >&2; exit 1; }

# The token and the text go to curl on its stdin, not in its arguments: the
# token stays off the command line, and curl on Windows reads its arguments in
# the ANSI code page, which breaks the Cyrillic of the text.
answer=$(printf 'url = "https://api.telegram.org/bot%s/sendMessage"\ndata-urlencode = "chat_id=%s"\ndata-urlencode = "text=%s"\n' \
	"$token" "$chat" "Hikkari hub: бот на связи, тревоги магистрали придут сюда." |
	curl -sS -K - || true)
case $answer in
*'"ok":true'*) echo "the bot wrote to the chat" ;;
*)
	echo "Telegram refused: $(printf '%s' "$answer" | sed -n 's/.*"description":"\([^"]*\)".*/\1/p')" >&2
	echo "write the bot any message first, and check the token and the chat id" >&2
	exit 1
	;;
esac

printf 'HUB_TELEGRAM_BOT_TOKEN=%s\nHUB_TELEGRAM_CHAT_ID=%s\n' "$token" "$chat" | ssh hikkari-main '
	set -eu
	f=/opt/hikkari-control/secrets/hub.env
	umask 077
	{ grep -v "^HUB_TELEGRAM_" "$f" || true; cat; } > "$f.new"
	mv "$f.new" "$f"
	docker compose -p hikkari-fleet -f /opt/hikkari-control/compose.hub.yaml --project-directory /opt/hikkari-control \
		up -d --no-build --wait --wait-timeout 240 hub
	docker exec hikkari-fleet-hub-1 sh -c "test -n \"\$HUB_TELEGRAM_BOT_TOKEN\" && test -n \"\$HUB_TELEGRAM_CHAT_ID\"" &&
		echo "the hub has the bot"
'
rm -f "$file"
echo "done: $file deleted"
