#!/usr/bin/env python3
# set-irc-password.sh — set or rotate a member's AgentBBS IRC password.
#
# Writes a pbkdf2-sha256 hash to the Ergo password store
# (/var/lib/ergo/irc-passwd, ergo:ergo 0600) that ergo-auth-member verifies on
# SASL login, and (if a The Lounge user file exists for the member) updates that
# user's saslPassword so the web client keeps working without member action.
#
# Usage:
#   set-irc-password.sh <member> [password]      # password generated if omitted
#   set-irc-password.sh <member> -               # read the password from stdin
#   set-irc-password.sh --all                    # provision any member missing one
#
# The "-" form is how the (non-root) BBS passwd@ flow calls this under sudo: the
# password arrives on stdin so it never lands in the process table or sudo's log.
#
# Run as root on the BBS box. The member must already be a BBS member; this only
# sets the secret — membership itself is still gated by /irc-auth.
import sys, os, json, glob, secrets, hashlib, pwd, grp

PASSWD_FILE = os.environ.get("ERGO_IRC_PASSWD", "/var/lib/ergo/irc-passwd")
LOUNGE_USERS = os.environ.get("AGENTBBS_LOUNGE_USERS", "/var/lib/thelounge/users")
ITERS = 200_000


def hash_pw(pw):
    salt = secrets.token_bytes(16)
    dk = hashlib.pbkdf2_hmac("sha256", pw.encode(), salt, ITERS)
    return f"pbkdf2_sha256${ITERS}${salt.hex()}${dk.hex()}"


def load_store():
    store = {}
    if os.path.exists(PASSWD_FILE):
        for ln in open(PASSWD_FILE):
            ln = ln.strip()
            if ln and not ln.startswith("#"):
                name, sep, h = ln.partition(":")
                if sep:
                    store[name] = h
    return store


def write_store(store):
    uid = pwd.getpwnam("ergo").pw_uid
    gid = grp.getgrnam("ergo").gr_gid
    os.makedirs(os.path.dirname(PASSWD_FILE), exist_ok=True)
    tmp = PASSWD_FILE + ".tmp"
    with open(tmp, "w") as f:
        f.write("# <account>:pbkdf2_sha256$<iterations>$<salt_hex>$<hash_hex>\n")
        for name in sorted(store):
            f.write(f"{name}:{store[name]}\n")
    os.chmod(tmp, 0o600)
    os.chown(tmp, uid, gid)
    os.replace(tmp, PASSWD_FILE)


def sync_lounge(member, pw):
    p = os.path.join(LOUNGE_USERS, member + ".json")
    if not os.path.exists(p):
        return
    try:
        d = json.load(open(p))
    except Exception as e:
        print(f"WARN: could not update Lounge config for {member}: {e}", file=sys.stderr)
        return
    changed = False
    for n in d.get("networks", []):
        if "saslPassword" in n or n.get("saslAccount"):
            n["saslPassword"] = pw
            changed = True
    if changed:
        st = os.stat(p)
        with open(p, "w") as f:
            json.dump(d, f, indent=2)
        os.chown(p, st.st_uid, st.st_gid)
        os.chmod(p, 0o600)
        print(f"  (updated The Lounge saslPassword for {member}; restart thelounge to apply)")


def set_one(member, pw, store):
    store[member] = hash_pw(pw)
    sync_lounge(member, pw)


def main(argv):
    if not argv:
        print(__doc__.strip())
        return 2
    store = load_store()
    if argv[0] == "--all":
        members = sorted(os.path.basename(p)[:-5] for p in glob.glob(os.path.join(LOUNGE_USERS, "*.json")))
        done = []
        for m in members:
            if m not in store:
                pw = secrets.token_urlsafe(9)
                set_one(m, pw, store)
                done.append((m, pw))
        write_store(store)
        for m, pw in done:
            print(f"{m}\t{pw}")
        print(f"provisioned {len(done)} member(s) that were missing a password")
        return 0
    member = argv[0]
    from_stdin = len(argv) > 1 and argv[1] == "-"
    if from_stdin:
        pw = sys.stdin.readline().rstrip("\n")
        if not pw:
            print("empty password on stdin", file=sys.stderr)
            return 2
    elif len(argv) > 1:
        pw = argv[1]
    else:
        pw = secrets.token_urlsafe(9)
    set_one(member, pw, store)
    write_store(store)
    # When the password came from stdin (BBS passwd@ flow) the caller already
    # knows it — don't echo it back into their captured output. Otherwise print
    # it so an operator running this by hand sees the generated/set value.
    print(member if from_stdin else f"{member}\t{pw}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
