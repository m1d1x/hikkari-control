#!/bin/sh
# Decrypts deploy/secrets.enc.yaml into secrets/secrets.yaml for
# docker compose. Needs the age key (SOPS_AGE_KEY_FILE or
# ~/.config/sops/age/keys.txt).
set -eu
cd "$(dirname "$0")/.."
# sops on macOS looks in ~/Library/Application Support by default.
if [ -z "${SOPS_AGE_KEY_FILE:-}" ] && [ -f "$HOME/.config/sops/age/keys.txt" ]; then
	export SOPS_AGE_KEY_FILE="$HOME/.config/sops/age/keys.txt"
fi
mkdir -p secrets
chmod 700 secrets
umask 022
# 0644: the container runs as an unprivileged user; the 0700 directory
# keeps it private on the host.
sops --decrypt deploy/secrets.enc.yaml > secrets/secrets.yaml.tmp
mv secrets/secrets.yaml.tmp secrets/secrets.yaml
echo "secrets/secrets.yaml ready"
