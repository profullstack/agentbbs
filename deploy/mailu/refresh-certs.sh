#!/usr/bin/env bash
#
# refresh-certs.sh — copy Caddy's Let's Encrypt cert for mail.$DOMAIN into the
# Mailu certs dir (TLS_FLAVOR=mail), so Postfix/Dovecot TLS on 465/587/993 track
# Caddy's auto-renewals. Mirrors deploy/news-refresh-certs.sh: Caddy is the only
# ACME client on the box (it serves the mail.$DOMAIN site block), and we reuse
# that cert rather than running a second ACME client inside Mailu.
#
# Install to /usr/local/bin/agentbbs-mailu-certs and run from a timer. Reloads
# the Mailu front/smtp/imap so the new cert is picked up. Exits non-zero
# (touching nothing) until Caddy has issued the cert.
set -euo pipefail

DOMAIN="${DOMAIN:?set DOMAIN}"
MAIL_HOST="${MAIL_HOST:-mail.${DOMAIN}}"
MAILU_DIR="${MAILU_DIR:-/opt/agentbbs/deploy/mailu}"
CERT_DIR="${CERT_DIR:-$MAILU_DIR/certs}"
CADDY_DATA="${CADDY_DATA:-/var/lib/caddy/.local/share/caddy}"

# Caddy stores certs under certificates/<acme-dir>/<host>/<host>.{crt,key};
# the ACME directory segment varies (prod vs staging), so glob for it.
crt="$(ls "$CADDY_DATA"/certificates/*/"$MAIL_HOST"/"$MAIL_HOST".crt 2>/dev/null | head -1 || true)"
key="$(ls "$CADDY_DATA"/certificates/*/"$MAIL_HOST"/"$MAIL_HOST".key 2>/dev/null | head -1 || true)"
if [ -z "$crt" ] || [ -z "$key" ]; then
  echo "no Caddy cert for $MAIL_HOST yet (looked under $CADDY_DATA/certificates)"
  exit 1
fi

install -d -m 0750 "$CERT_DIR"

changed=0
# Mailu (TLS_FLAVOR=mail) reads cert.pem / key.pem from its /certs mount.
if ! cmp -s "$crt" "$CERT_DIR/cert.pem"; then install -m 0644 "$crt" "$CERT_DIR/cert.pem"; changed=1; fi
if ! cmp -s "$key" "$CERT_DIR/key.pem"; then install -m 0640 "$key" "$CERT_DIR/key.pem"; changed=1; fi

if [ "$changed" = 1 ]; then
  echo "updated Mailu TLS cert for $MAIL_HOST; reloading Mailu"
  ( cd "$MAILU_DIR" && docker compose restart front smtp imap >/dev/null 2>&1 || true )
else
  echo "Mailu TLS cert for $MAIL_HOST already current"
fi
