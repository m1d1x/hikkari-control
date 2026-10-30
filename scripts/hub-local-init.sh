#!/bin/sh
# Generate local credentials without echoing them or overwriting existing keys.
set -eu
cd "$(dirname "$0")/.."
umask 077
mkdir -p secrets
chmod 700 secrets
if [ ! -s secrets/hub.env ]; then
    SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}" \
      sops --decrypt --input-type dotenv --output-type dotenv deploy/hub.env.enc > secrets/hub.env.tmp
    mv secrets/hub.env.tmp secrets/hub.env
fi
if [ ! -s secrets/admin-jwt.pem ]; then
    openssl genrsa -out secrets/admin-jwt.pem 2048 2>/dev/null
fi
# The hub runs as uid 1000; these files are bind-mounted read-only, and the
# secrets directory remains private on the host.
chmod 644 secrets/admin-jwt.pem
if [ ! -s secrets/hub-local.env ]; then
    pg_password=$(openssl rand -hex 24)
    ch_password=$(openssl rand -hex 24)
    pulse_master=$(openssl rand -hex 32)
    cat > secrets/hub-local.env <<CONFIG
POSTGRES_USER=hub
POSTGRES_PASSWORD=$pg_password
POSTGRES_DB=hub
CLICKHOUSE_USER=hub
CLICKHOUSE_PASSWORD=$ch_password
CLICKHOUSE_DB=hub
DATABASE_URL=postgres://hub:$pg_password@postgres:5432/hub?sslmode=require
CLICKHOUSE_DSN=clickhouse://hub:$ch_password@clickhouse:9000/hub
EDGE_PULSE_TOKEN=$pulse_master
ADMIN_JWT_PRIVATE_KEY_FILE=/run/secrets/admin-jwt.pem
CONFIG
fi
if [ ! -s secrets/hub-downdetector.yaml ]; then
    invite=$(openssl rand -hex 24)
    admin=$(openssl rand -hex 24)
    printf 'invite_tokens:\n  - "%s"\nadmin_tokens:\n  - "%s"\n' "$invite" "$admin" > secrets/hub-downdetector.yaml
fi
chmod 644 secrets/hub-downdetector.yaml
printf 'Local hub configuration ready. Run: docker compose -f compose.hub.yaml up --build -d\n'
