#!/usr/bin/env bash
# Rebuild every AgentBBS member pod so it picks up the current container profile
# (e.g. the rootless-podman default capability set added for apt/chown/su/:80).
#
# It removes each pod CONTAINER but keeps that pod's named home volume
# (agentbbs-pod-<name>-home) and the host-side public_html, so member data and
# websites are untouched. Pods are recreated automatically — with the new
# profile — the next time each member runs `ssh pod@<host>`. Caddy serves
# public_html from the host, so sites stay up while a pod is briefly down.
#
# Anything a member installed into the pod's system rootfs (apt packages, etc.)
# is lost on rebuild; only /home/dev and public_html persist.
#
# Run this as the user that owns the pods. For rootless podman that's the
# AgentBBS service user (pods are per-user), not necessarily root.
#
# Usage:
#   scripts/rebuild-pods.sh           # list, then prompt before removing
#   scripts/rebuild-pods.sh --yes     # non-interactive (for cron/deploy)
#   AGENTBBS_POD_ENGINE=docker scripts/rebuild-pods.sh   # force engine
set -euo pipefail

ENGINE="${AGENTBBS_POD_ENGINE:-}"
if [ -z "$ENGINE" ]; then
  if command -v podman >/dev/null 2>&1; then
    ENGINE=podman
  elif command -v docker >/dev/null 2>&1; then
    ENGINE=docker
  else
    echo "rebuild-pods: neither podman nor docker found" >&2
    exit 1
  fi
fi

mapfile -t pods < <("$ENGINE" ps -a --filter 'name=agentbbs-pod-' --format '{{.Names}}' | sort)

if [ "${#pods[@]}" -eq 0 ]; then
  echo "rebuild-pods: no pods found (engine: $ENGINE)"
  exit 0
fi

echo "Found ${#pods[@]} pod(s) via $ENGINE:"
printf '  %s\n' "${pods[@]}"

if [ "${1:-}" != "--yes" ] && [ "${1:-}" != "-y" ]; then
  printf 'Remove these containers (home volumes kept)? [y/N] '
  read -r reply
  case "$reply" in
    y | Y | yes | YES) ;;
    *)
      echo "aborted"
      exit 0
      ;;
  esac
fi

for p in "${pods[@]}"; do
  # No -v: named home volumes are preserved, only the container is destroyed.
  "$ENGINE" rm -f "$p" >/dev/null && echo "removed $p"
done

echo
echo "Done. Each pod recreates with the new profile on its owner's next 'ssh pod@'."
