#!/usr/bin/env bash
#
# refresh-certs.sh — copy Caddy's Let's Encrypt cert for mail.$DOMAIN into the
# Mailu certs dir (TLS_FLAVOR=mail), so Postfix/Dovecot TLS on 25/465/587/993
# track Caddy's auto-renewals. Mirrors deploy/news-refresh-certs.sh: Caddy is the
# only ACME client on the box (it serves the mail.$DOMAIN site block), and we
# reuse that cert rather than running a second ACME client inside Mailu.
#
# Install to /usr/local/bin/agentbbs-mailu-certs and run from a timer. Reloads
# the Mailu front so the new cert is picked up. Exits non-zero (touching
# nothing) until Caddy has issued the cert.
#
# Copying the file is not the whole job. Mailu keeps serving whatever it loaded
# at container start, so a copy whose reload silently failed leaves a fresh cert
# on disk and an expiring one on the wire. That is exactly how join@ stopped
# being able to email confirmation codes: Caddy renewed on 2026-08-14, Mailu went
# on serving the Jun 15 cert, and registration broke when it expired on Sep 13.
# So we also compare what the relay ACTUALLY serves against the file and force a
# reload when they disagree, and we no longer swallow reload failures.
set -euo pipefail

DOMAIN="${DOMAIN:?set DOMAIN}"
MAIL_HOST="${MAIL_HOST:-mail.${DOMAIN}}"
MAILU_DIR="${MAILU_DIR:-/opt/agentbbs/deploy/mailu}"
CERT_DIR="${CERT_DIR:-$MAILU_DIR/certs}"
CADDY_DATA="${CADDY_DATA:-/var/lib/caddy/.local/share/caddy}"
# Port used to read back the cert the relay is really serving (loopback SMTP).
PROBE_ADDR="${PROBE_ADDR:-127.0.0.1:25}"

# notAfter of a PEM file, or empty if it can't be read.
cert_not_after() {
  openssl x509 -noout -enddate -in "$1" 2>/dev/null | sed 's/^notAfter=//'
}

# notAfter of the cert the running relay serves over STARTTLS, or empty if the
# probe can't be made (openssl missing, port closed, Mailu down).
served_not_after() {
  command -v openssl >/dev/null 2>&1 || return 0
  printf 'QUIT\r\n' \
    | timeout 10 openssl s_client -quiet -starttls smtp \
        -connect "$PROBE_ADDR" -servername "$MAIL_HOST" 2>/dev/null \
    | openssl x509 -noout -enddate 2>/dev/null | sed 's/^notAfter=//'
}

# Caddy stores certs under certificates/<acme-dir>/<host>/<host>.{crt,key};
# the ACME directory segment varies (prod vs staging), so glob for it.
crt="$(ls "$CADDY_DATA"/certificates/*/"$MAIL_HOST"/"$MAIL_HOST".crt 2>/dev/null | head -1 || true)"
key="$(ls "$CADDY_DATA"/certificates/*/"$MAIL_HOST"/"$MAIL_HOST".key 2>/dev/null | head -1 || true)"
if [ -z "$crt" ] || [ -z "$key" ]; then
  echo "no Caddy cert for $MAIL_HOST yet (looked under $CADDY_DATA/certificates)" >&2
  exit 1
fi

# A source cert that is itself expired means Caddy's renewal is broken, which is
# a different fault and one no amount of copying fixes. Say so loudly.
if ! openssl x509 -checkend 0 -noout -in "$crt" >/dev/null 2>&1; then
  echo "Caddy's cert for $MAIL_HOST is EXPIRED ($(cert_not_after "$crt")) — fix Caddy's renewal; not copying" >&2
  exit 1
fi

install -d -m 0750 "$CERT_DIR"

changed=0
# Mailu (TLS_FLAVOR=mail) reads cert.pem / key.pem from its /certs mount.
if ! cmp -s "$crt" "$CERT_DIR/cert.pem"; then install -m 0644 "$crt" "$CERT_DIR/cert.pem"; changed=1; fi
if ! cmp -s "$key" "$CERT_DIR/key.pem"; then install -m 0640 "$key" "$CERT_DIR/key.pem"; changed=1; fi

# The file can be current while the running container still serves an older one
# (a reload that never happened, or failed). Trust the wire, not the filesystem.
on_disk="$(cert_not_after "$CERT_DIR/cert.pem")"
on_wire="$(served_not_after)"
stale_on_wire=0
if [ -n "$on_wire" ] && [ -n "$on_disk" ] && [ "$on_wire" != "$on_disk" ]; then
  echo "Mailu is serving a cert that expires '$on_wire' but /certs holds one that expires '$on_disk' — forcing reload" >&2
  stale_on_wire=1
fi

if [ "$changed" = 1 ] || [ "$stale_on_wire" = 1 ]; then
  echo "updating Mailu TLS cert for $MAIL_HOST (expires $on_disk); reloading Mailu"
  # No `|| true`: a reload that fails is the failure mode this whole script
  # exists to prevent, so it must surface in `systemctl status` / the journal.
  ( cd "$MAILU_DIR" && docker compose restart front )
else
  echo "Mailu TLS cert for $MAIL_HOST already current (expires $on_disk)"
fi
