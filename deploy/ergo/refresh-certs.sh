#!/usr/bin/env bash
#
# refresh-certs.sh — copy Caddy's Let's Encrypt cert for $DOMAIN into Ergo's
# TLS dir and reload Ergo if it changed. setup.sh installs this to
# /usr/local/bin/ergo-refresh-certs and runs it from the ergo-certs.timer so
# the IRC server's 6697 cert tracks Caddy's auto-renewals.
#
# Ergo and Caddy share the same hostname (${DOMAIN}); Caddy is the only ACME
# client on the box, so we reuse its cert rather than running a second ACME
# client. Exits non-zero (without touching anything) if Caddy hasn't issued the
# cert yet — on first boot that's expected, and setup.sh falls back to a
# self-signed cert until this timer picks up the real one.
set -euo pipefail

DOMAIN="${DOMAIN:?set DOMAIN}"
ERGO_DATA="${ERGO_DATA:-/var/lib/ergo}"
CADDY_DATA="${CADDY_DATA:-/var/lib/caddy/.local/share/caddy}"

# Caddy stores certs under certificates/<acme-dir>/<host>/<host>.{crt,key};
# the ACME directory segment varies (prod vs staging), so glob for it.
crt="$(ls "$CADDY_DATA"/certificates/*/"$DOMAIN"/"$DOMAIN".crt 2>/dev/null | head -1 || true)"
key="$(ls "$CADDY_DATA"/certificates/*/"$DOMAIN"/"$DOMAIN".key 2>/dev/null | head -1 || true)"
if [ -z "$crt" ] || [ -z "$key" ]; then
  echo "no Caddy cert for $DOMAIN yet (looked under $CADDY_DATA/certificates)"
  exit 1
fi

dst="$ERGO_DATA/tls"
install -d -m 0755 "$dst"

changed=0
if ! cmp -s "$crt" "$dst/fullchain.pem"; then install -m 0644 "$crt" "$dst/fullchain.pem"; changed=1; fi
if ! cmp -s "$key" "$dst/privkey.pem";  then install -m 0640 "$key" "$dst/privkey.pem";  changed=1; fi
chown -R ergo:ergo "$dst" 2>/dev/null || true

if [ "$changed" = 1 ]; then
  echo "updated Ergo TLS cert for $DOMAIN"
  # Ergo rehashes config + reloads certs on SIGHUP (systemctl reload).
  systemctl reload ergo 2>/dev/null || systemctl restart ergo 2>/dev/null || true
else
  echo "Ergo TLS cert for $DOMAIN already current"
fi
