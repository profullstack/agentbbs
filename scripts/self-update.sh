#!/usr/bin/env bash
#
# self-update.sh — pull the tracked branch and, if it advanced (or the service
# is down, or --force), re-run the idempotent provisioner to redeploy.
#
# Designed to be safe to run on a timer: when origin has not moved and agentbbs
# is healthy it does nothing and exits 0, so it costs one `git fetch` per tick.
# setup.sh holds a flock, so this never races a concurrent CI deploy.
#
#   sudo scripts/self-update.sh          # redeploy only if origin/<branch> moved
#   sudo scripts/self-update.sh --force  # redeploy unconditionally
#
set -euo pipefail

REPO="${REPO:-https://github.com/profullstack/agentbbs.git}"
BRANCH="${BRANCH:-main}"
SRC_DIR="${SRC_DIR:-/opt/agentbbs}"

FORCE=0
[ "${1:-}" = "--force" ] && FORCE=1

[ "$(id -u)" -eq 0 ] || { echo "self-update.sh must run as root" >&2; exit 1; }

# First-ever run on a bare box: clone, then always provision.
if [ ! -d "$SRC_DIR/.git" ]; then
  git clone --depth 1 -b "$BRANCH" "$REPO" "$SRC_DIR"
  exec "$SRC_DIR/setup.sh"
fi

git -C "$SRC_DIR" fetch --depth 1 origin "$BRANCH"
local_rev="$(git -C "$SRC_DIR" rev-parse HEAD)"
remote_rev="$(git -C "$SRC_DIR" rev-parse "origin/${BRANCH}")"

if [ "$FORCE" -eq 0 ] \
  && [ "$local_rev" = "$remote_rev" ] \
  && systemctl is-active --quiet agentbbs; then
  echo "agentbbs up to date at ${remote_rev:0:12} and healthy; nothing to do"
  exit 0
fi

echo "redeploying: ${local_rev:0:12} -> ${remote_rev:0:12} (force=$FORCE)"
# setup.sh does the reset --hard, rebuild, and restart under its own lock.
exec "$SRC_DIR/setup.sh"
