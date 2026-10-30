#!/bin/sh
# Render the admin panel's runtime config from the environment.
#
# Runs inside the nginx image's /docker-entrypoint.d/ hook, before the server
# starts. The panel reads the result in src/lib/config/api.ts.
#
# NEKKO_API_URL is the origin a BROWSER must use, not a docker-network name:
# the only client of this value is the user's browser, which does not resolve
# `nekko`. Locally that means http://localhost:3300; on its own host it is
# https://api.nekkochat.com.
set -eu

if [ -z "${NEKKO_API_URL:-}" ]; then
	echo "nekko-admin: NEKKO_API_URL is required (browser-reachable API origin, e.g. http://localhost:3300)" >&2
	exit 1
fi

case "$NEKKO_API_URL" in
	http://*|https://*) ;;
	*)
		echo "nekko-admin: NEKKO_API_URL must be an absolute http(s) origin, got '$NEKKO_API_URL'" >&2
		exit 1
		;;
esac

# A single-quoted JS string, so the value may not contain a quote. Reject
# rather than escape: anything needing an escape is a malformed origin.
case "$NEKKO_API_URL" in
	*"'"*|*'\'*)
		echo "nekko-admin: NEKKO_API_URL contains an illegal character" >&2
		exit 1
		;;
esac

cat > /usr/share/nginx/html/config.js <<CONFIG
// Generated at container start by docker-entrypoint.sh. Do not edit.
window.__NEKKO_ADMIN_CONFIG__ = { apiUrl: '${NEKKO_API_URL}' };
CONFIG

echo "nekko-admin: API origin set to ${NEKKO_API_URL}"
