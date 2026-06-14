#!/usr/bin/env bash
#
# auth-script.sh — Ergo auth-script that gates the IRC network on AgentBBS
# membership. setup.sh installs this to /usr/local/bin/ergo-auth-member and
# wires it into /etc/ergo/ircd.yaml (accounts.auth-script).
#
# AgentBBS members are real OS users (tilde.town model; setup.sh provisions an
# OS account per member). IRC is members-only, so a login is approved iff the
# requested account name is a real OS user with uid >= MIN_UID (which excludes
# system accounts like root/ergo/agentbbs). The passphrase is intentionally
# IGNORED — membership (being an OS user) IS the credential, by design (see
# docs/irc.md). Anyone who knows a member's name can connect as them; that
# tradeoff was chosen deliberately for this private, TLS-only network.
#
# Protocol (Ergo): one JSON object on stdin per attempt, one JSON line on stdout
# then exit. Input keys: accountName, passphrase, certfp, ip. Output:
# {"success":bool,"accountName":str,"error":str}.
#
#   args: ["<min-uid>"]   # defaults to 1000
set -uo pipefail

MIN_UID="${1:-1000}"

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

# Restrict to plain login names (defense in depth; IRC names are limited anyway).
case "$acct" in
  *[!A-Za-z0-9._-]* | "." | ".." ) deny "invalid account name" ;;
esac

# Resolve the OS account; getent passwd returns name:passwd:uid:gid:...
entry="$(getent passwd "$acct" 2>/dev/null || true)"
[ -n "$entry" ] || deny "not a member"

uid="$(printf '%s' "$entry" | cut -d: -f3)"
case "$uid" in
  ''|*[!0-9]*) deny "not a member" ;;
esac

if [ "$uid" -ge "$MIN_UID" ]; then
  printf '{"success":true,"accountName":"%s"}\n' "$acct"
else
  deny "system accounts cannot use IRC"
fi
