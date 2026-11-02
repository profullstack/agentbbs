#!/usr/bin/env bash
#
# ci-deploy.sh: the ONLY thing the GitHub Actions deploy key may run on the box.
#
# Installed by setup.sh as /usr/local/sbin/agentbbs-ci-deploy and pinned in
# root's authorized_keys as
#   restrict,command="/usr/local/sbin/agentbbs-ci-deploy" ssh-ed25519 ... github-actions-deploy@agentbbs-...
# so the key gets no shell, pty, forwarding, scp or sftp. The verb comes from
# SSH_ORIGINAL_COMMAND and must be one of:
#
#   arch                  print `uname -m`
#   upload <binary>       read one prebuilt binary (agentbbs | ascii-live) from
#                         stdin into a root-only staging dir; must be an ELF file
#   deploy <branch>       branch is main or master. stdin carries the app
#                         secrets as NAME=<base64> lines (allowlisted names only).
#                         Syncs /opt/agentbbs to origin/<branch>, installs the
#                         staged binaries and execs setup.sh with SKIP_BUILD=1.
#
# Nothing in the command line is ever evaluated by a shell.
set -euo pipefail
umask 077

REPO=https://github.com/profullstack/agentbbs.git
SRC=/opt/agentbbs
STAGE=/var/lib/agentbbs-deploy
MAX_BINARY=$((256 * 1024 * 1024))
SECRET_NAMES="COINPAY_API_KEY COINPAY_MERCHANT_ID AGENTBBS_QRYPT_ISSUER_KEY"

die() { echo "agentbbs-ci-deploy: $*" >&2; exit 2; }

read -r -a argv <<<"${SSH_ORIGINAL_COMMAND:-}"
[ "${#argv[@]}" -ge 1 ] || die "no command (expected: arch | upload <binary> | deploy <branch>)"

case "${argv[0]}" in
  arch)
    [ "${#argv[@]}" -eq 1 ] || die "arch takes no arguments"
    exec uname -m
    ;;

  upload)
    [ "${#argv[@]}" -eq 2 ] || die "usage: upload agentbbs|ascii-live"
    case "${argv[1]}" in agentbbs|ascii-live) ;; *) die "unknown binary '${argv[1]}'" ;; esac
    install -d -m 0700 "$STAGE"
    tmp="$(mktemp "$STAGE/.upload.XXXXXX")"
    trap 'rm -f "$tmp"' EXIT
    head -c "$((MAX_BINARY + 1))" > "$tmp"
    size="$(stat -c %s "$tmp")"
    [ "$size" -gt 0 ] || die "empty upload"
    [ "$size" -le "$MAX_BINARY" ] || die "upload larger than $MAX_BINARY bytes"
    [ "$(head -c 4 "$tmp" | od -An -tx1 | tr -d ' \n')" = "7f454c46" ] || die "upload is not an ELF binary"
    mv -f "$tmp" "$STAGE/${argv[1]}"
    trap - EXIT
    echo "agentbbs-ci-deploy: staged ${argv[1]} ($size bytes)"
    ;;

  deploy)
    [ "${#argv[@]}" -eq 2 ] || die "usage: deploy main|master"
    BRANCH="${argv[1]}"
    case "$BRANCH" in main|master) ;; *) die "refusing to deploy branch '$BRANCH'" ;; esac
    for b in agentbbs ascii-live; do
      [ -s "$STAGE/$b" ] || die "no staged $b binary; upload it first"
    done

    # App secrets: NAME=<base64 value> per line, allowlisted names only, 64 KiB cap.
    declare -A secret=()
    while IFS='=' read -r name value; do
      [ -n "$name" ] || continue
      case " $SECRET_NAMES " in *" $name "*) ;; *) die "unexpected secret name '$name'" ;; esac
      secret[$name]="$(printf '%s' "$value" | base64 -d 2>/dev/null)" || die "secret $name is not base64"
    done < <(head -c 65536)

    if [ ! -d "$SRC/.git" ]; then
      git clone --depth 1 -b "$BRANCH" "$REPO" "$SRC"
    fi
    git -C "$SRC" fetch --depth 1 origin "$BRANCH"
    git -C "$SRC" reset --hard "origin/$BRANCH"
    echo "agentbbs-ci-deploy: at $(git -C "$SRC" log --oneline -1)"

    # Pause the self-update timer: it rebuilds from source and can hold
    # setup.sh's flock for minutes. setup.sh re-enables it at the end.
    systemctl stop agentbbs-update.timer agentbbs-update.service 2>/dev/null || true
    install -m 0755 "$STAGE/agentbbs"   /usr/local/bin/agentbbs
    install -m 0755 "$STAGE/ascii-live" /usr/local/bin/ascii-live
    rm -f "$STAGE/agentbbs" "$STAGE/ascii-live"

    exec env BRANCH="$BRANCH" SKIP_BUILD=1 \
      COINPAY_API_KEY="${secret[COINPAY_API_KEY]:-}" \
      COINPAY_MERCHANT_ID="${secret[COINPAY_MERCHANT_ID]:-}" \
      AGENTBBS_QRYPT_ISSUER_KEY="${secret[AGENTBBS_QRYPT_ISSUER_KEY]:-}" \
      "$SRC/setup.sh"
    ;;

  *)
    die "unknown command '${argv[0]}' (expected: arch | upload <binary> | deploy <branch>)"
    ;;
esac
