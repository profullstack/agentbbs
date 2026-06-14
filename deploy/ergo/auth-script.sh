#!/usr/bin/env bash
#
# auth-script.sh — Ergo auth-script that gates the IRC network on AgentBBS
# membership. setup.sh installs this to /usr/local/bin/ergo-auth-member and
# wires it into /etc/ergo/ircd.yaml (accounts.auth-script).
#
# The single source of truth is the BBS user store (the bbs.profullstack.com
# accounts), queried via a loopback agentbbs endpoint (/irc-auth) that answers
# {"member":bool,"premium":bool}. IRC is members-only, so a login is approved iff
# the account is a member. The passphrase is intentionally IGNORED — BBS
# membership IS the credential, by design (see docs/irc.md). Anyone who knows a
# member's name can connect as them; that tradeoff was chosen deliberately for
# this private, TLS-only network.
#
# Protocol (Ergo): one JSON object on stdin per attempt, one JSON line on stdout
# then exit. Input keys: accountName, passphrase, certfp, ip. Output:
# {"success":bool,"accountName":str,"error":str}.
#
#   args: ["<auth-url>"]   # defaults to http://127.0.0.1:8088/irc-auth
set -uo pipefail

AUTH_URL="${1:-http://127.0.0.1:8088/irc-auth}"

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

# Ask the BBS store (loopback) whether this account is a member. curl URL-encodes
# the account name; a failed/timed-out request denies (fail closed).
resp="$(curl -fsS --max-time 5 --get --data-urlencode "account=${acct}" "$AUTH_URL" 2>/dev/null || true)"
member="$(printf '%s' "$resp" | jq -r '.member // false' 2>/dev/null || true)"

if [ "$member" = "true" ]; then
  printf '{"success":true,"accountName":"%s"}\n' "$acct"
else
  deny "not a member"
fi
