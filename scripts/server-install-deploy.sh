#!/bin/sh
# One-time server setup for CI deploys of the hub stack, run from a laptop
# with root SSH to the server:
#
#   scripts/server-install-deploy.sh root@<server> <public-key-file>
#
# Creates the unprivileged hub-deploy user, whose only key may run nothing but
# deploy/server/hikkari-hub-deploy through sudo. Rerun it after changing that
# script: CI never updates the script that installs CI's own releases.
set -eu
[ $# -eq 2 ] || { echo "usage: $0 root@<server> <public-key-file>" >&2; exit 2; }
target=$1
key=$(cat "$2")
# ssh joins a remote command's words into one string, so the key travels as
# one quoted word, and is checked to be only what a key may hold.
printf '%s' "$key" | grep -Eqx 'ssh-ed25519 [A-Za-z0-9+/]+=*( [A-Za-z0-9@._-]+)?' ||
	{ echo "expected one ssh-ed25519 public key" >&2; exit 2; }
cd "$(dirname "$0")/.."

ssh "$target" 'install -m 0755 -o root -g root /dev/stdin /usr/local/sbin/hikkari-hub-deploy' \
	< deploy/server/hikkari-hub-deploy

ssh "$target" "sh -s -- '$key'" <<'REMOTE'
set -eu
key=$1
id hub-deploy >/dev/null 2>&1 || useradd --system --create-home --shell /bin/sh hub-deploy
install -d -m 0700 -o hub-deploy -g hub-deploy /home/hub-deploy/.ssh
printf 'command="sudo -n /usr/local/sbin/hikkari-hub-deploy",restrict %s\n' "$key" \
	> /home/hub-deploy/.ssh/authorized_keys
chown hub-deploy:hub-deploy /home/hub-deploy/.ssh/authorized_keys
chmod 0600 /home/hub-deploy/.ssh/authorized_keys
printf 'hub-deploy ALL=(root) NOPASSWD: /usr/local/sbin/hikkari-hub-deploy ""\n' > /etc/sudoers.d/hikkari-hub-deploy.next
chmod 0440 /etc/sudoers.d/hikkari-hub-deploy.next
visudo -cf /etc/sudoers.d/hikkari-hub-deploy.next >/dev/null
mv /etc/sudoers.d/hikkari-hub-deploy.next /etc/sudoers.d/hikkari-hub-deploy
echo "hub-deploy ready"
REMOTE
