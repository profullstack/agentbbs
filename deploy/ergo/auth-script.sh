#!/usr/bin/env bash
#
# auth-script.sh — Ergo auth-script that gates the IRC network on AgentBBS
# membership. setup.sh installs this to /usr/local/bin/ergo-auth-member and
# wires it into /etc/ergo/ircd.yaml (accounts.auth-script).
#
# "Member" == a user with a home dir under the AgentBBS users dir (created when
# someone registers via `ssh join@`). IRC is members-only, so a login is
# approved iff the requested account name maps to such a dir. The passphrase is
# intentionally IGNORED — membership (a filesystem dir) IS the credential, by
# design (see docs/irc.md). Anyone who knows a member's name can connect as
# them; that tradeoff was chosen deliberately for this private, TLS-only network.
#
# Protocol (Ergo): one JSON object on stdin per attempt, one JSON line on stdout
# then exit. Input keys: accountName, passphrase, certfp, ip. Output:
# {"success":bool,"accountName":str,"error":str}.
#
#   args: ["<users-dir>"]   # defaults to /var/lib/agentbbs/users
set -uo pipefail

USERS_DIR="${1:-/var/lib/agentbbs/users}"

# Always emit valid JSON and exit 0 — Ergo reads the JSON, not the exit code;
# a non-zero exit / no output is treated as a script error, not a clean deny.
deny() { printf '{"success":false,"error":"%s"}\n' "${1:-not a member}"; exit 0; }

# Don't gate on read's exit code: a final line without a trailing newline still
# carries data (read returns non-zero at EOF but populates $line).
line=""
read -r line || true
[ -n "$line" ] || deny "no input"

acct="$(printf '%s' "$line" | jq -r '.accountName // ""' 2>/dev/null || true)"

# certfp-only attempts carry no account name; we don't support cert auth here.
[ -n "$acct" ] || deny "membership requires an account name"

# Defense in depth against path traversal. IRC account names are a restricted
# charset anyway, but never let one escape USERS_DIR.
case "$acct" in
  *[!A-Za-z0-9._-]* | "." | ".." | *..* | */* ) deny "invalid account name" ;;
esac

if [ -d "$USERS_DIR/$acct" ]; then
  printf '{"success":true,"accountName":"%s"}\n' "$acct"
else
  deny "not a member"
fi
