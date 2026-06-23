#!/usr/bin/env python3
#
# ergo-auth-member — Ergo auth-script for the AgentBBS members-only IRC network.
#
# A SASL login is approved iff BOTH hold:
#   1. the account name maps to an existing AgentBBS member (queried via the
#      loopback /irc-auth endpoint — the single user-level source of truth), AND
#   2. the supplied passphrase matches the member's stored IRC password hash.
#
# This replaces the earlier membership-only gate (which ignored the passphrase
# and let anyone who knew a member name connect as them). BBS membership is
# still the source of truth for *who* may have an account; the password proves
# *you are* that member.
#
# Password store (ERGO_IRC_PASSWD, default /var/lib/ergo/irc-passwd), one line
# per member, '#' comments allowed:
#     <account>:pbkdf2_sha256$<iterations>$<salt_hex>$<hash_hex>
# Provision/rotate with scripts/set-irc-password.sh (see docs/irc.md).
#
# Protocol (Ergo): one JSON object on stdin per attempt, one JSON line on
# stdout, then exit 0. Input: accountName, passphrase, certfp, ip.
# Output: {"success":bool,"accountName":str,"error":str}.
#
#   args: ["<auth-url>"]   # defaults to http://127.0.0.1:8088/irc-auth
import sys, os, re, json, hmac, hashlib, urllib.parse, urllib.request

AUTH_URL = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8088/irc-auth"
PASSWD_FILE = os.environ.get("ERGO_IRC_PASSWD", "/var/lib/ergo/irc-passwd")


def emit(success, account=None, error=None):
    # Always emit valid JSON and exit 0 — Ergo reads the JSON, not the exit
    # code; a non-zero exit / no output is treated as a script error.
    obj = {"success": bool(success)}
    if success and account:
        obj["accountName"] = account
    if not success:
        obj["error"] = error or "denied"
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()
    sys.exit(0)


def is_member(acct):
    # Fail closed: any error/timeout denies.
    url = AUTH_URL + "?" + urllib.parse.urlencode({"account": acct})
    with urllib.request.urlopen(url, timeout=5) as r:
        return json.loads(r.read().decode()).get("member") is True


def lookup_hash(acct):
    with open(PASSWD_FILE) as f:
        for ln in f:
            ln = ln.strip()
            if not ln or ln.startswith("#"):
                continue
            name, sep, h = ln.partition(":")
            if sep and name == acct:
                return h
    return None


def password_ok(passphrase, stored):
    scheme, iters, salt_hex, hash_hex = stored.split("$")
    if scheme != "pbkdf2_sha256":
        return False
    dk = hashlib.pbkdf2_hmac(
        "sha256", passphrase.encode("utf-8"), bytes.fromhex(salt_hex), int(iters)
    )
    return hmac.compare_digest(dk.hex(), hash_hex)


def main():
    line = sys.stdin.readline()
    if not line.strip():
        emit(False, error="no input")
    try:
        data = json.loads(line)
    except Exception:
        emit(False, error="bad json")

    acct = (data.get("accountName") or "").strip()
    passphrase = data.get("passphrase") or ""

    # certfp-only attempts carry no account name; cert auth unsupported here.
    if not acct:
        emit(False, error="account name required")
    # Restrict to plain login names (defense in depth).
    if acct in (".", "..") or not re.fullmatch(r"[A-Za-z0-9._-]+", acct):
        emit(False, error="invalid account name")

    # 1) membership (fail closed)
    try:
        member = is_member(acct)
    except Exception:
        emit(False, error="membership check failed")
    if not member:
        emit(False, error="not a member")

    # 2) password
    try:
        stored = lookup_hash(acct)
    except Exception:
        emit(False, error="password store unavailable")
    if not stored:
        emit(False, error="no password set for this account")
    try:
        ok = password_ok(passphrase, stored)
    except Exception:
        emit(False, error="password verify error")
    if not ok:
        emit(False, error="invalid password")

    emit(True, account=acct)


if __name__ == "__main__":
    main()
