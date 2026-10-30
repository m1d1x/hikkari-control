#!/bin/sh
set -eu
cert_dir=/var/lib/postgresql/tls
mkdir -p "$cert_dir"
if [ ! -s "$cert_dir/server.key" ]; then
    umask 077
    openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \
      -subj /CN=postgres -addext subjectAltName=DNS:postgres \
      -keyout "$cert_dir/server.key" -out "$cert_dir/server.crt" 2>/dev/null
fi
chown -R postgres:postgres "$cert_dir"
exec /usr/local/bin/docker-entrypoint.sh "$@"
