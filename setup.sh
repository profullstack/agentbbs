#!/usr/bin/env bash
#
# setup.sh — one-shot provisioner for an AgentBBS host (Ubuntu droplet).
#
# Brings a fresh box to: agentbbs on :22 (so `ssh join@bbs.profullstack.com`
# works with no -p), the admin OpenSSH moved to :2202, rootless podman for
# pods, a persistent SSH host key + sqlite store, and a Caddy front end serving
# https://bbs.profullstack.com plus tilde.town-style /~user homepages.
#
# It is idempotent — safe to re-run to update (it pulls + rebuilds + restarts).
#
#   sudo ./setup.sh
#
# Override any default via env, e.g.:
#   sudo DOMAIN=bbs.example.com ADMIN_SSH_PORT=2222 ./setup.sh
#
# ⚠️  The admin OpenSSH port changes to ADMIN_SSH_PORT. The script verifies the
#     new port is listening BEFORE handing :22 to agentbbs and never drops your
#     current session — but open a SECOND terminal and confirm
#     `ssh -p <ADMIN_SSH_PORT> <you>@<host>` works before you log out.
set -euo pipefail

# ---- config (override via env) ---------------------------------------------
DOMAIN="${DOMAIN:-bbs.profullstack.com}"
ADMIN_SSH_PORT="${ADMIN_SSH_PORT:-2202}"
ACME_EMAIL="${ACME_EMAIL:-admin@profullstack.com}"
SVC_USER="${SVC_USER:-agentbbs}"
REPO="${REPO:-https://github.com/profullstack/agentbbs.git}"
SRC_DIR="${SRC_DIR:-/opt/agentbbs}"
DATA_DIR="${DATA_DIR:-/var/lib/agentbbs}"
ASK_ADDR="${ASK_ADDR:-127.0.0.1:8081}"   # agentbbs on-demand-TLS ask endpoint (must match agentbbs.env)
HTTP_ADDR="${HTTP_ADDR:-127.0.0.1:8088}" # agentbbs /verify endpoint (join@ email confirmation links)
GO_VERSION="${GO_VERSION:-1.26.4}"
POD_IMAGE="${POD_IMAGE:-docker.io/library/ubuntu:24.04}"
FETCH_ASSETS="${FETCH_ASSETS:-1}"   # set 0 to skip the DOOM/Freedoom arcade assets

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root (sudo ./setup.sh)"
. /etc/os-release 2>/dev/null || true
[ "${ID:-}" = "ubuntu" ] || warn "tested on Ubuntu; ${ID:-unknown} may differ"

case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) die "unsupported arch $(uname -m)" ;;
esac

# ---- 1. packages -----------------------------------------------------------
log "installing packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq \
  git ca-certificates curl ufw ffmpeg \
  podman uidmap slirp4netns fuse-overlayfs \
  debian-keyring debian-archive-keyring apt-transport-https >/dev/null

# yt-dlp from pip is fresher than apt; fall back to apt if pip is unavailable.
if ! command -v yt-dlp >/dev/null; then
  log "installing yt-dlp"
  apt-get install -y -qq python3-pip >/dev/null
  pip3 install --quiet --break-system-packages -U yt-dlp 2>/dev/null \
    || apt-get install -y -qq yt-dlp >/dev/null \
    || warn "yt-dlp not installed — YouTube sources will fail until it is"
fi

# ---- 2. Go toolchain (system go is too old; pin GO_VERSION) -----------------
GO_ROOT="/usr/local/go"
if [ "$("$GO_ROOT/bin/go" version 2>/dev/null | awk '{print $3}')" != "go${GO_VERSION}" ]; then
  log "installing Go ${GO_VERSION}"
  tmp="$(mktemp -d)"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -o "$tmp/go.tgz" \
    || die "could not download Go ${GO_VERSION}"
  rm -rf "$GO_ROOT"
  tar -C /usr/local -xzf "$tmp/go.tgz"
  rm -rf "$tmp"
fi
export PATH="$GO_ROOT/bin:$PATH"

# ---- 3. service user + rootless podman prerequisites -----------------------
if ! id "$SVC_USER" >/dev/null 2>&1; then
  log "creating service user $SVC_USER"
  useradd --system --create-home --home-dir "/home/$SVC_USER" --shell /usr/sbin/nologin "$SVC_USER"
fi
SVC_UID="$(id -u "$SVC_USER")"
# subuid/subgid ranges let rootless podman build user namespaces for pods.
grep -q "^${SVC_USER}:" /etc/subuid || usermod --add-subuids 100000-165535 "$SVC_USER"
grep -q "^${SVC_USER}:" /etc/subgid || usermod --add-subgids 100000-165535 "$SVC_USER"
# linger keeps /run/user/$UID alive so podman works from a system service with
# no interactive login.
loginctl enable-linger "$SVC_USER" >/dev/null 2>&1 || true

# ---- 4. persistent data dir (host key + sqlite + per-user public_html) ------
log "preparing $DATA_DIR"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0751 "$DATA_DIR"          # others may traverse, not list
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0700 "$DATA_DIR/ssh"      # host key stays private
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0755 "$DATA_DIR/users"    # tilde homepages live here
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0755 "$DATA_DIR/web"      # site root
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0755 "$DATA_DIR/domains"  # symlink farm: custom domain -> users/<name>/public_html
[ -f "$DATA_DIR/web/index.html" ] || cat > "$DATA_DIR/web/index.html" <<HTML
<!doctype html><meta charset=utf-8><title>AgentBBS</title>
<style>body{background:#000;color:#33ff66;font:16px/1.6 monospace;max-width:44rem;margin:4rem auto;padding:0 1rem}a{color:#60a5fa}</style>
<h1>AgentBBS</h1>
<p>A BBS over SSH for humans and AI agents.</p>
<pre>  ssh join@${DOMAIN}     # register your key, get started
  ssh bbs@${DOMAIN}      # look around as a guest
  ssh pod@${DOMAIN}      # your personal Linux pod (\$1/mo)</pre>
<p>User homepages live at <code>/~name</code> — and members can point their own domain at one (<code>ssh domain@${DOMAIN} add yourdomain.com</code>).</p>
HTML
chown "$SVC_USER:$SVC_USER" "$DATA_DIR/web/index.html"

# ---- 5. clone/update + build agentbbs --------------------------------------
if [ -d "$SRC_DIR/.git" ]; then
  log "updating source in $SRC_DIR"
  git -C "$SRC_DIR" pull --ff-only
else
  log "cloning $REPO"
  git clone --depth 1 "$REPO" "$SRC_DIR"
fi

if [ "$FETCH_ASSETS" = "1" ] && [ -x "$SRC_DIR/fetch-assets.sh" ]; then
  log "fetching arcade assets (set FETCH_ASSETS=0 to skip)"
  ( cd "$SRC_DIR" && ./fetch-assets.sh ) || warn "asset fetch failed; arcade may be limited"
fi

log "building binaries"
( cd "$SRC_DIR" && go build -o /usr/local/bin/agentbbs ./cmd/agentbbs )
( cd "$SRC_DIR" && go build -o /usr/local/bin/ascii-live ./cmd/ascii-live )
# Pre-pull the pod base image as the service user so first pod launch is fast.
sudo -u "$SVC_USER" XDG_RUNTIME_DIR="/run/user/$SVC_UID" \
  podman pull -q "$POD_IMAGE" >/dev/null 2>&1 || warn "could not pre-pull $POD_IMAGE (pods will pull on first use)"

# ---- 6. environment file ---------------------------------------------------
ENV_DIR=/etc/agentbbs
install -d -m 0750 "$ENV_DIR"
if [ ! -f "$ENV_DIR/agentbbs.env" ]; then
  log "writing $ENV_DIR/agentbbs.env (fill in CoinPay/LiveKit before relying on pods/video)"
  cat > "$ENV_DIR/agentbbs.env" <<ENV
# AgentBBS runtime config — edit then: systemctl restart agentbbs
AGENTBBS_ADDR=:22
AGENTBBS_HOST=${DOMAIN}
AGENTBBS_DATA=${DATA_DIR}
AGENTBBS_ASSETS=${SRC_DIR}/assets
AGENTBBS_POD_IMAGE=${POD_IMAGE}

# Custom domains: Caddy on-demand-TLS asks this loopback endpoint whether a
# requested host is mapped before issuing a certificate. Must match the
# Caddyfile's on_demand_tls ask URL.
AGENTBBS_ASK_ADDR=${ASK_ADDR}

# join@ email verification. The confirm links in the mail hit
# https://${DOMAIN}/verify, which Caddy proxies to this loopback endpoint.
# Without SMTP config the link is only logged (journalctl -u agentbbs).
AGENTBBS_HTTP_ADDR=${HTTP_ADDR}
# AGENTBBS_SMTP_HOST=
# AGENTBBS_SMTP_PORT=587
# AGENTBBS_SMTP_USER=
# AGENTBBS_SMTP_PASS=
# AGENTBBS_SMTP_FROM=bbs@${DOMAIN}
# pod@ requires a verified email; set 0 to disable on a dev host:
# AGENTBBS_REQUIRE_VERIFIED_EMAIL=1

# Pods (CoinPay \$1/mo membership) — required for pod@ to charge/verify:
# AGENTBBS_COINPAY_PAY_TMPL=
# AGENTBBS_COINPAY_VERIFY_CMD=

# PairUX video calls rendered as ASCII (video@ / tv@ PairUX sources):
# AGENTBBS_LIVEKIT_URL=
# AGENTBBS_LIVEKIT_KEY=
# AGENTBBS_LIVEKIT_SECRET=

# Agent chat backend (agent@), stdin->stdout, e.g. "claude -p":
# AGENTBBS_AGENT_CMD=
ENV
  chmod 0640 "$ENV_DIR/agentbbs.env"
fi

# ---- 7. systemd unit (runs as $SVC_USER, binds :22 via ambient capability) --
log "installing agentbbs.service"
cat > /etc/systemd/system/agentbbs.service <<UNIT
[Unit]
Description=AgentBBS — BBS over SSH
After=network-online.target
Wants=network-online.target

[Service]
User=${SVC_USER}
Group=${SVC_USER}
WorkingDirectory=${SRC_DIR}
EnvironmentFile=${ENV_DIR}/agentbbs.env
Environment=XDG_RUNTIME_DIR=/run/user/${SVC_UID}
ExecStart=/usr/local/bin/agentbbs
Restart=always
RestartSec=2
# Bind :22 as a non-root user. We deliberately do NOT set NoNewPrivileges or
# the Protect*/ReadWritePaths sandbox here: rootless podman needs the setuid
# newuidmap/newgidmap helpers (blocked by NoNewPrivileges) and read-write
# access to the service user's home for container storage. The security
# boundary is the pod itself (cap-drop ALL etc. in internal/pods), not this
# orchestrator process, which already runs unprivileged as ${SVC_USER}.
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload

# ---- 8. move admin OpenSSH to ADMIN_SSH_PORT (before agentbbs takes :22) -----
log "moving admin OpenSSH to :${ADMIN_SSH_PORT}"
install -d -m 0755 /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/10-agentbbs-admin.conf <<SSHD
# Admin OpenSSH moved off :22 so agentbbs can own it.
# Reach this box for administration with: ssh -p ${ADMIN_SSH_PORT} <user>@host
Port ${ADMIN_SSH_PORT}
SSHD
# Open the new admin port FIRST so the upcoming firewall enable can't lock us out.
ufw allow "${ADMIN_SSH_PORT}/tcp" >/dev/null
if sshd -t; then
  systemctl restart ssh 2>/dev/null || systemctl restart sshd
else
  die "sshd config test failed; not restarting (you are not locked out)"
fi
# Verify the admin port is actually listening before we free :22.
for _ in 1 2 3 4 5; do
  ss -tlnp 2>/dev/null | grep -q ":${ADMIN_SSH_PORT} " && break
  sleep 1
done
ss -tlnp 2>/dev/null | grep -q ":${ADMIN_SSH_PORT} " \
  || die "admin sshd is NOT listening on ${ADMIN_SSH_PORT} — aborting before touching :22. Your current session is still up; fix sshd and re-run."

# ---- 9. Caddy front end (HTTPS + tilde /~user homepages) --------------------
if ! command -v caddy >/dev/null; then
  log "installing Caddy"
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq && apt-get install -y -qq caddy >/dev/null
fi
# Let Caddy (user 'caddy') read the per-user public_html trees.
usermod -aG "$SVC_USER" caddy 2>/dev/null || true
log "writing Caddyfile"
cat > /etc/caddy/Caddyfile <<CADDY
{
	email ${ACME_EMAIL}

	# Custom user domains get certificates on demand, but only for hosts the
	# BBS has actually mapped — agentbbs answers this ask query (200/404) so
	# this is not an open certificate relay.
	on_demand_tls {
		ask http://${ASK_ADDR}/check
	}
}

${DOMAIN} {
	encode zstd gzip

	# join@ email confirmation links (agentbbs loopback /verify endpoint)
	handle /verify {
		reverse_proxy http://${HTTP_ADDR}
	}

	# tilde.town-style homepages: /~name[/path] -> users/name/public_html/path
	@tilde path_regexp tilde ^/~([^/]+)(/.*)?\$
	handle @tilde {
		root * ${DATA_DIR}/users
		rewrite * /{re.tilde.1}/public_html{re.tilde.2}
		try_files {path} {path}/index.html
		file_server browse
	}

	# site root
	handle {
		root * ${DATA_DIR}/web
		file_server
	}
}

# Custom domains a member pointed at this host (ssh domain@${DOMAIN} add ...).
# The symlink farm in domains/ maps each host to its owner's public_html, so
# {host} resolves to the right tree; unmapped hosts 404 (and never got a cert).
https:// {
	encode zstd gzip
	tls {
		on_demand
	}
	root * ${DATA_DIR}/domains/{host}
	try_files {path} {path}/index.html
	file_server
}
CADDY
ufw allow 80/tcp  >/dev/null
ufw allow 443/tcp >/dev/null
systemctl reload caddy 2>/dev/null || systemctl restart caddy

# ---- 10. firewall + start agentbbs on :22 ----------------------------------
log "configuring firewall + starting agentbbs"
ufw allow 22/tcp >/dev/null
ufw --force enable >/dev/null
systemctl enable --now agentbbs

sleep 1
systemctl is-active --quiet agentbbs \
  || die "agentbbs failed to start — check: journalctl -u agentbbs -n50"

# ---- done ------------------------------------------------------------------
log "AgentBBS is up."
cat <<DONE

  DNS        point  ${DOMAIN}  (A record) at this droplet's public IP.
  Admin SSH  ssh -p ${ADMIN_SSH_PORT} <you>@${DOMAIN}      (your old key still works)
  Users      ssh join@${DOMAIN}     register
             ssh bbs@${DOMAIN}      guest hub
             ssh pod@${DOMAIN}      personal pod
             ssh domain@${DOMAIN} add <domain>   point your own domain at your homepage
  Web        https://${DOMAIN}/             site root
             https://${DOMAIN}/~<name>      a member's homepage
             https://<your-domain>          a member's homepage on a custom domain (auto-HTTPS)

  Config     ${ENV_DIR}/agentbbs.env   (set CoinPay + LiveKit, then: systemctl restart agentbbs)
  Logs       journalctl -u agentbbs -f
  Update     re-run this script (git pull + rebuild + restart)
DONE
warn "Before you log out: open a new terminal and confirm  ssh -p ${ADMIN_SSH_PORT} <you>@${DOMAIN}  works."
warn "If you attached a DigitalOcean Cloud Firewall, also allow ${ADMIN_SSH_PORT}, 22, 80, 443 there."
