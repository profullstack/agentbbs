#!/usr/bin/env bash
#
# provision-mailbox.sh — create or update a member mailbox on the Mailu stack.
# Run on the mail host. The agentbbs gateway opens any member's mailbox via the
# Dovecot master user, so members never need an individual IMAP password — but
# the mailbox must exist, which is what this creates.
#
# Usage:
#   provision-mailbox.sh <name>                 # create <name>@$DOMAIN (random pw)
#   provision-mailbox.sh --master <pass>        # (re)create the gateway master user
#
# Idempotent: re-running for an existing user is a no-op (or a password reset
# with --password). Wraps Mailu's admin CLI (flask mailu ...).
set -euo pipefail

MAILU_DIR="${MAILU_DIR:-/opt/agentbbs/deploy/mailu}"
DOMAIN="${MAIL_DOMAIN:-mail.profullstack.com}"
MASTER_USER="${AGENTBBS_MAIL_MASTER_USER:-gateway}"
QUOTA_BYTES="${MAIL_QUOTA_BYTES:-1000000000}" # 1 GB

cli() { ( cd "$MAILU_DIR" && docker compose exec -T admin flask mailu "$@" ); }

if [ "${1:-}" = "--master" ]; then
  pass="${2:?usage: provision-mailbox.sh --master <password>}"
  # A Dovecot master user can authenticate as any mailbox: login "<name>*gateway".
  # Implemented in Mailu as a normal user flagged for master access via an
  # override (see docs/mail.md); here we ensure the account + password exist.
  cli user "$MASTER_USER" "$DOMAIN" "$pass" 2>/dev/null \
    || cli password "$MASTER_USER" "$DOMAIN" "$pass"
  echo "gateway master user ${MASTER_USER}@${DOMAIN} set"
  exit 0
fi

name="${1:?usage: provision-mailbox.sh <name>}"
pass="${2:-$(openssl rand -hex 16)}"

if cli user-import "$name" "$DOMAIN" "$(openssl passwd -6 "$pass")" 2>/dev/null; then
  :
else
  # already exists or older CLI: fall back to `user` (no-op if present)
  cli user "$name" "$DOMAIN" "$pass" 2>/dev/null || true
fi
# Enforce a per-mailbox quota.
cli config-update <<EOF 2>/dev/null || true
users:
  - email: ${name}@${DOMAIN}
    quota_bytes: ${QUOTA_BYTES}
EOF

echo "mailbox ${name}@${DOMAIN} provisioned"
