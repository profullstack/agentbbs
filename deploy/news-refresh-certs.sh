#!/usr/bin/env bash
#
# news-refresh-certs.sh — copy Caddy's Let's Encrypt cert for news.$DOMAIN into
# the dir the agentbbs NNTP server reads, so NNTPS on :563 tracks Caddy's
# auto-renewals. setup.sh installs this to /usr/local/bin/agentbbs-news-certs
# and runs it from agentbbs-news-certs.timer.
#
# The NNTP server runs INSIDE the agentbbs process (not a separate service like
# Ergo) and re-stats its cert files every 30s, so this script only has to copy
# the files into place — no service reload is needed. Caddy is the only ACME
# client on the box and obtains the news.$DOMAIN cert from the `news.$DOMAIN`
# site block in the Caddyfile; we reuse that cert rather than running a second
# ACME client. Exits non-zero (touching nothing) until Caddy has issued it; on
# first boot setup.sh drops in a self-signed cert until this timer swaps it in.
set -euo pipefail

DOMAIN="${DOMAIN:?set DOMAIN}"
NEWS_HOST="${NEWS_HOST:-news.${DOMAIN}}"
NEWS_TLS_DIR="${NEWS_TLS_DIR:-/var/lib/agentbbs/news-tls}"
SVC_USER="${SVC_USER:-agentbbs}"
CADDY_DATA="${CADDY_DATA:-/var/lib/caddy/.local/share/caddy}"

# Caddy stores certs under certificates/<acme-dir>/<host>/<host>.{crt,key};
# the ACME directory segment varies (prod vs staging), so glob for it.
crt="$(ls "$CADDY_DATA"/certificates/*/"$NEWS_HOST"/"$NEWS_HOST".crt 2>/dev/null | head -1 || true)"
key="$(ls "$CADDY_DATA"/certificates/*/"$NEWS_HOST"/"$NEWS_HOST".key 2>/dev/null | head -1 || true)"
if [ -z "$crt" ] || [ -z "$key" ]; then
  echo "no Caddy cert for $NEWS_HOST yet (looked under $CADDY_DATA/certificates)"
  exit 1
fi

install -d -m 0750 "$NEWS_TLS_DIR"

changed=0
if ! cmp -s "$crt" "$NEWS_TLS_DIR/fullchain.pem"; then install -m 0644 "$crt" "$NEWS_TLS_DIR/fullchain.pem"; changed=1; fi
if ! cmp -s "$key" "$NEWS_TLS_DIR/privkey.pem";  then install -m 0640 "$key" "$NEWS_TLS_DIR/privkey.pem";  changed=1; fi
chown -R "$SVC_USER:$SVC_USER" "$NEWS_TLS_DIR" 2>/dev/null || true

if [ "$changed" = 1 ]; then
  echo "updated news TLS cert for $NEWS_HOST (agentbbs auto-reloads within 30s)"
else
  echo "news TLS cert for $NEWS_HOST already current"
fi
