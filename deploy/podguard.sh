#!/bin/sh
# agentbbs-podguard: keep member pods away from this host's private services
# and from the operator's other servers.
#
# Pods are rootless podman under the service user's systemd slice and egress
# through pasta AS THIS HOST. Without this guard a pod can reach every
# 0.0.0.0 listener here (ufw does not filter local delivery; pasta also maps
# the host at 169.254.1.2) and can open connections to the operator's other
# boxes from this box's IP, which those boxes may trust. Both were abused on
# 2026-11-19.
#
# Matching is by socket cgroup, so the agentbbs daemon itself (system.slice)
# is unaffected. nft resolves the cgroup path to an id at load time, so this
# re-runs after user@<uid>.service starts and on a short timer.
#
# Config (optional): /etc/agentbbs/podguard.env
#   PODGUARD_BLOCK4="1.2.3.4, 5.6.7.8"   # other servers pods must never reach
#   PODGUARD_BLOCK6="2001:db8::1"
#   PODGUARD_PUBPORTS="22, 70, 80, 119, 443, 563, 2222, 6697"  # host ports pods MAY use
set -eu
[ -f /etc/agentbbs/podguard.env ] && . /etc/agentbbs/podguard.env
SVC_USER="${SVC_USER:-agentbbs}"
U=$(id -u "$SVC_USER")
CG="user.slice/user-$U.slice"
[ -d "/sys/fs/cgroup/$CG" ] || { echo "podguard: $CG not present yet"; exit 0; }

SELF4="127.0.0.0/8, 169.254.0.0/16"
for a in $(hostname -I 2>/dev/null); do
  case "$a" in *:*) ;; *) SELF4="$SELF4, $a" ;; esac
done
PUBPORTS="${PODGUARD_PUBPORTS:-22, 70, 80, 119, 443, 563, 2222, 6697}"

B4=""; B6=""
[ -n "${PODGUARD_BLOCK4:-}" ] && B4="set block4 { type ipv4_addr; flags interval; elements = { $PODGUARD_BLOCK4 } }"
[ -n "${PODGUARD_BLOCK6:-}" ] && B6="set block6 { type ipv6_addr; flags interval; elements = { $PODGUARD_BLOCK6 } }"
R4=""; R6=""
[ -n "$B4" ] && R4="ip daddr @block4 counter drop"
[ -n "$B6" ] && R6="ip6 daddr @block6 counter drop"

nft -f - <<NFT
table inet agentbbs_podguard
delete table inet agentbbs_podguard
table inet agentbbs_podguard {
  $B4
  $B6
  set self4 { type ipv4_addr; flags interval; elements = { $SELF4 } }
  set pubports { type inet_service; elements = { $PUBPORTS } }
  chain out {
    type filter hook output priority 0; policy accept;
    socket cgroupv2 level 2 "$CG" jump pod
  }
  chain pod {
    $R4
    $R6
    ip daddr @self4 udp dport 53 accept
    ip daddr @self4 tcp dport @pubports accept
    ip daddr @self4 counter drop
    ip6 daddr ::1 counter drop
  }
}
NFT
echo "podguard: loaded for $CG"
