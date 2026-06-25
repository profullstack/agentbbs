#!/usr/bin/env bash
#
# refresh-motd.sh — pull the shared Message of the Day from profullstack.com
# into Ergo's MOTD file and reload Ergo if it changed. setup.sh installs this to
# /usr/local/bin/ergo-refresh-motd and runs it from the ergo-motd.timer, so the
# IRC server's MOTD (shown on connect / via /MOTD) tracks profullstack.com/motd.
#
# Ergo resolves the `motd:` path in ircd.yaml relative to the config dir, so the
# file lives at $ERGO_CONF_DIR/ergo.motd (default /etc/ergo/ergo.motd).
set -euo pipefail

MOTD_URL="${MOTD_URL:-https://profullstack.com/motd}"
ERGO_CONF_DIR="${ERGO_CONF_DIR:-/etc/ergo}"
DST="$ERGO_CONF_DIR/ergo.motd"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

if curl -fsSL --max-time 10 "$MOTD_URL" -o "$tmp" && [ -s "$tmp" ]; then
  if ! cmp -s "$tmp" "$DST"; then
    install -m 0644 "$tmp" "$DST"
    chown ergo:ergo "$DST" 2>/dev/null || true
    echo "updated Ergo MOTD from $MOTD_URL"
    # Ergo rehashes config (incl. MOTD) on SIGHUP (systemctl reload).
    systemctl reload ergo 2>/dev/null || systemctl restart ergo 2>/dev/null || true
  else
    echo "Ergo MOTD already current"
  fi
elif [ ! -s "$DST" ]; then
  # First run and the source is unreachable — seed a minimal MOTD so Ergo has
  # something to serve; the timer replaces it once profullstack.com is reachable.
  printf '%s\n' "Welcome to AgentBBS IRC." > "$DST"
  chown ergo:ergo "$DST" 2>/dev/null || true
  echo "MOTD source unreachable; seeded placeholder"
else
  echo "MOTD source unreachable; keeping existing MOTD"
fi
