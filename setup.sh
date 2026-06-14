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
BRANCH="${BRANCH:-main}"
SRC_DIR="${SRC_DIR:-/opt/agentbbs}"
DATA_DIR="${DATA_DIR:-/var/lib/agentbbs}"
ASK_ADDR="${ASK_ADDR:-127.0.0.1:8081}"   # agentbbs on-demand-TLS ask endpoint (must match agentbbs.env)
HTTP_ADDR="${HTTP_ADDR:-127.0.0.1:8088}" # agentbbs /verify endpoint (join@ email confirmation links)
GO_VERSION="${GO_VERSION:-1.26.4}"
POD_IMAGE="${POD_IMAGE:-docker.io/library/ubuntu:24.04}"
FETCH_ASSETS="${FETCH_ASSETS:-1}"   # set 0 to skip the DOOM/Freedoom arcade assets
SKIP_BUILD="${SKIP_BUILD:-0}"       # set 1 to use prebuilt /usr/local/bin/{agentbbs,ascii-live} (tiny droplets can't compile)
SWAP_SIZE="${SWAP_SIZE:-3G}"        # swapfile size added on low-RAM hosts (set 0 to skip)
SELF_UPDATE="${SELF_UPDATE:-1}"     # set 0 to skip the autonomous self-update systemd timer
SELF_UPDATE_INTERVAL="${SELF_UPDATE_INTERVAL:-15min}"  # how often the box polls origin for new commits
IRC="${IRC:-1}"                     # set 0 to skip the co-located Ergo IRC server (irc.${DOMAIN})
ERGO_VERSION="${ERGO_VERSION:-2.18.0}"  # Ergo IRCd release to install
IRC_NETWORK="${IRC_NETWORK:-ProfullstackBBS}"  # IRC network name shown to clients
ERGO_DATA="${ERGO_DATA:-/var/lib/ergo}"  # Ergo state dir (ircd.db, tls/)

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

# ensure_swap adds a swapfile on low-RAM hosts so the Go build (and runtime)
# aren't OOM-killed. No-op when swap already exists, RAM is ample, or SWAP_SIZE=0.
ensure_swap() {
  [ "${SWAP_SIZE:-0}" = "0" ] && return 0
  [ "$(swapon --show=NAME --noheadings 2>/dev/null | wc -l)" -gt 0 ] && return 0
  local kb; kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo)
  [ "${kb:-0}" -ge 3000000 ] && return 0   # >= ~3GB RAM: skip
  log "low RAM ($(( ${kb:-0}/1024 ))MB) — adding ${SWAP_SIZE} swap at /swapfile"
  if [ ! -f /swapfile ]; then
    fallocate -l "$SWAP_SIZE" /swapfile 2>/dev/null \
      || dd if=/dev/zero of=/swapfile bs=1M count="$(numfmt --from=iec "$SWAP_SIZE" | awk '{print int($1/1048576)}')" status=none
    chmod 600 /swapfile
    mkswap /swapfile >/dev/null
  fi
  swapon /swapfile 2>/dev/null || true
  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
}

[ "$(id -u)" -eq 0 ] || die "run as root (sudo ./setup.sh)"

# Serialize runs. A CI deploy (ssh -> setup.sh) and the self-update timer can
# fire close together; two concurrent git-reset + go-build runs would corrupt
# each other. Hold an exclusive lock for the whole run (wait up to 5 min).
if command -v flock >/dev/null; then
  exec 9>/var/lock/agentbbs-setup.lock
  flock -w 300 9 || die "another setup.sh run is in progress (lock held >5m)"
fi

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
  git ca-certificates curl ufw ffmpeg unzip jq \
  podman uidmap slirp4netns fuse-overlayfs \
  tor torsocks \
  debian-keyring debian-archive-keyring apt-transport-https >/dev/null

# Tor SOCKS proxy for the tor-url@/tor@/tor-irc@ routes. Ships listening on
# 127.0.0.1:9050 by default; keep it loopback-only (never expose it).
log "enabling tor (SOCKS 127.0.0.1:9050)"
systemctl enable --now tor >/dev/null 2>&1 || warn "tor service not enabled — tor-url@ will be unavailable"

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

# Persistent, root-owned Go caches. Two reasons this matters on this box:
#  1. The self-update timer runs setup.sh as a systemd oneshot with no $HOME, so
#     a bare `go build` aborts with "module cache not found: neither GOMODCACHE
#     nor GOPATH is set" before it compiles a thing. Setting these explicitly
#     (not relying on $HOME) fixes timer-driven redeploys.
#  2. A warm cache means an incremental redeploy recompiles only what changed, so
#     the 458MB droplet stops OOM-killing the compiler on every build. Combined
#     with GOMAXPROCS=1 + `go build -p=1`, peak memory stays within RAM+swap.
export HOME="${HOME:-/root}"
export GOPATH="${GOPATH:-/var/cache/agentbbs/go}"
export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
export GOCACHE="${GOCACHE:-/var/cache/agentbbs/go-build}"
export GOMAXPROCS=1
mkdir -p "$GOPATH" "$GOCACHE"

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
  log "updating source in $SRC_DIR to origin/$BRANCH"
  # Hard reset (not pull --ff-only) so an automated deploy survives a force-push
  # or any local drift on the box — the box always matches origin exactly.
  git -C "$SRC_DIR" fetch --depth 1 origin "$BRANCH"
  git -C "$SRC_DIR" reset --hard "origin/$BRANCH"
else
  log "cloning $REPO ($BRANCH)"
  git clone --depth 1 -b "$BRANCH" "$REPO" "$SRC_DIR"
fi

if [ "$FETCH_ASSETS" = "1" ] && [ -x "$SRC_DIR/scripts/fetch-assets.sh" ]; then
  log "fetching arcade assets (set FETCH_ASSETS=0 to skip)"
  ( cd "$SRC_DIR" && ./scripts/fetch-assets.sh ) || warn "asset fetch failed; arcade may be limited"
fi

# Add swap on tiny droplets before the build (and for runtime headroom).
ensure_swap

if [ "$SKIP_BUILD" = "1" ]; then
  log "SKIP_BUILD=1 — using prebuilt /usr/local/bin/{agentbbs,ascii-live}"
  [ -x /usr/local/bin/agentbbs ] || die "SKIP_BUILD=1 but /usr/local/bin/agentbbs is missing — copy it first"
else
  log "building binaries (go build -p=1 to cap peak memory)"
  ( cd "$SRC_DIR" && go build -p=1 -o /usr/local/bin/agentbbs ./cmd/agentbbs )
  ( cd "$SRC_DIR" && go build -p=1 -o /usr/local/bin/ascii-live ./cmd/ascii-live )
fi
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
# Free pods + custom homepages require a verified email; set 0 to disable on a
# dev host (then any registered key gets a pod):
# AGENTBBS_REQUIRE_VERIFIED_EMAIL=1
# Every new signup is emailed here (subject "bbs"); needs SMTP configured above:
# AGENTBBS_SIGNUP_NOTIFY=anthony@profullstack.com

# Membership model:
#   Free   verified members get their own Docker pod (ssh pod@) and a homepage
#          at https://${DOMAIN}/~<name>.
#   Premium \$10 one-time, lifetime — a personal <name>@${DOMAIN} email
#          (forwardemail.net) plus custom domains (ssh domain@). Offered at join@.

# Premium payments hit the CoinPay REST API directly (no coinpay CLI needed):
# join@ creates a charge and shows the amount + deposit address; a later connect
# verifies settlement by payment id. COINPAY_API_KEY + the merchant id are
# injected by the CI deploy from GitHub secrets (COINPAY_API_KEY /
# COINPAY_MERCHANT_ID); set them here directly when provisioning by hand:
# COINPAY_API_KEY=cp_live_xxx
# AGENTBBS_COINPAY_MERCHANT_ID=<merchant/business uuid>
# AGENTBBS_COINPAY_API_URL=https://coinpayportal.com/api
# AGENTBBS_PREMIUM_AMOUNT=10
# AGENTBBS_PREMIUM_CURRENCY=USD
# AGENTBBS_PREMIUM_BLOCKCHAIN=eth

# Premium email aliases (<name>@${DOMAIN}) auto-created on forwardemail.net.
# Without an API key the address is shown but not created (add it manually).
# AGENTBBS_FORWARDEMAIL_API_KEY=
# AGENTBBS_FORWARDEMAIL_DOMAIN=${DOMAIN}
# AGENTBBS_WEBMAIL_URL=https://webmail.${DOMAIN}

# PairUX video calls rendered as ASCII (video@ / tv@ PairUX sources):
# AGENTBBS_LIVEKIT_URL=
# AGENTBBS_LIVEKIT_KEY=
# AGENTBBS_LIVEKIT_SECRET=

# Agent chat backend (agent@), stdin->stdout, e.g. "claude -p":
# AGENTBBS_AGENT_CMD=

# qrypt.chat anonymous-invite issuer (docs/qrypt-invites.md). Members mint a
# signed single-use token here that qrypt.chat redeems into an anon account.
# Run \`agentbbs qrypt-issuer-keygen\` once: paste the seed below, register the
# public key in qrypt.chat's invite_issuers row. Without the key, minting is off.
# AGENTBBS_QRYPT_ISSUER_KEY=<base64 ed25519 seed from qrypt-issuer-keygen>
# AGENTBBS_QRYPT_ISSUER_ID=agentbbs
# AGENTBBS_QRYPT_INVITE_TTL=168h
# AGENTBBS_QRYPT_REDEEM_URL=https://qrypt.chat/anon?invite=
# AGENTBBS_QRYPT_INVITE_QUOTA=5
ENV
  chmod 0640 "$ENV_DIR/agentbbs.env"
fi

# Idempotently upsert secrets passed in the environment (e.g. by the CI deploy
# from GitHub Actions secrets) into agentbbs.env, preserving everything else.
# Secrets are never committed — they live only here and in encrypted CI storage.
upsert_env() {  # KEY VALUE — skips when VALUE is empty
  local key="$1" val="$2" file="$ENV_DIR/agentbbs.env"
  [ -n "$val" ] || return 0
  touch "$file"
  if grep -qE "^${key}=" "$file"; then
    # Replace in place (| delimiter avoids clashes with / or & in the value).
    sed -i "s|^${key}=.*|${key}=${val}|" "$file"
  else
    printf '%s=%s\n' "$key" "$val" >> "$file"
  fi
  chmod 0640 "$file"
}
# CoinPay: API key (read by the coinpay CLI) + merchant/business id.
upsert_env COINPAY_API_KEY "${COINPAY_API_KEY:-}"
upsert_env AGENTBBS_COINPAY_MERCHANT_ID "${COINPAY_MERCHANT_ID:-${AGENTBBS_COINPAY_MERCHANT_ID:-}}"
upsert_env COINPAY_BUSINESS_ID "${COINPAY_MERCHANT_ID:-${AGENTBBS_COINPAY_MERCHANT_ID:-}}"

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

# ---- 7b. autonomous self-update timer (poll origin, redeploy on new commits) -
if [ "$SELF_UPDATE" = "1" ]; then
  log "installing self-update timer (every ${SELF_UPDATE_INTERVAL}; set SELF_UPDATE=0 to disable)"
  cat > /etc/systemd/system/agentbbs-update.service <<UNIT
[Unit]
Description=AgentBBS self-update (pull origin/${BRANCH} + redeploy if changed)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
# systemd oneshots start with no HOME; set one so git (and Go, via setup.sh)
# behave the same as an interactive root deploy instead of erroring.
Environment=HOME=/root
Environment=REPO=${REPO}
Environment=BRANCH=${BRANCH}
Environment=SRC_DIR=${SRC_DIR}
# Pass through the same overrides this provisioner ran with so a timer-driven
# redeploy is identical to this one.
Environment=DOMAIN=${DOMAIN}
Environment=ADMIN_SSH_PORT=${ADMIN_SSH_PORT}
ExecStart=${SRC_DIR}/scripts/self-update.sh
UNIT
  cat > /etc/systemd/system/agentbbs-update.timer <<UNIT
[Unit]
Description=Poll for AgentBBS updates and redeploy

[Timer]
OnBootSec=3min
OnUnitActiveSec=${SELF_UPDATE_INTERVAL}
Persistent=true

[Install]
WantedBy=timers.target
UNIT
  systemctl daemon-reload
  systemctl enable --now agentbbs-update.timer >/dev/null 2>&1 || true
else
  systemctl disable --now agentbbs-update.timer >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/agentbbs-update.service /etc/systemd/system/agentbbs-update.timer
  systemctl daemon-reload
fi

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
sshd -t || die "sshd config test failed; not restarting (you are not locked out)"

# Ubuntu 22.10+/24.04 socket-activate sshd via ssh.socket, which OWNS the listen
# port and ignores sshd_config's Port. Override the socket's ListenStream so the
# admin port actually moves; otherwise (classic sshd) just restart the service.
if systemctl cat ssh.socket >/dev/null 2>&1; then
  log "ssh is socket-activated (Ubuntu 24.04) — moving the socket to :${ADMIN_SSH_PORT}"
  install -d -m 0755 /etc/systemd/system/ssh.socket.d
  cat > /etc/systemd/system/ssh.socket.d/10-agentbbs-port.conf <<SOCK
[Socket]
ListenStream=
ListenStream=${ADMIN_SSH_PORT}
SOCK
  systemctl daemon-reload
  systemctl restart ssh.socket
else
  systemctl restart ssh 2>/dev/null || systemctl restart sshd
fi
# Verify the admin port is actually listening before we free :22.
for _ in 1 2 3 4 5 6 7 8; do
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
# Caddy indexes host labels from the right (com=0, profullstack=1, bbs=2, …);
# the user subdomain is the next label left, i.e. index = DOMAIN's label count.
USER_LABEL_IDX=$(printf '%s' "$DOMAIN" | awk -F. '{print NF}')
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

	# IRC over WebSocket: Caddy terminates TLS and proxies to Ergo's loopback
	# WebSocket listener, so web clients hit wss://${DOMAIN}/irc and agents get a
	# WebSocket transport without exposing another public port. (No-op if IRC=0;
	# Ergo just isn't listening on 8097, so /irc returns 502.)
	handle /irc {
		reverse_proxy 127.0.0.1:8097
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

# Free per-user homepages at <name>.${DOMAIN} (needs wildcard DNS
# *.${DOMAIN} -> this host). On-demand TLS mints a cert only when agentbbs's
# ask endpoint confirms <name> is a registered member, so random subdomains
# can't trigger certificate issuance.
*.${DOMAIN} {
	encode zstd gzip
	tls {
		on_demand
	}
	# Only registered members get an on-demand cert (the ask endpoint gates it),
	# so {labels.${USER_LABEL_IDX}} is always a real user's public_html.
	root * ${DATA_DIR}/users/{http.request.host.labels.${USER_LABEL_IDX}}/public_html
	try_files {path} {path}/index.html
	file_server browse
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

# ---- 9b. Ergo IRC server (co-located irc.${DOMAIN}; humans + agents) --------
# A lightweight single-binary IRC network on its own ports, sharing this box and
# this hostname's TLS cert. Native clients hit irc.${DOMAIN}:6697 (TLS); web
# clients and agents hit wss://${DOMAIN}/irc (Caddy fronts Ergo's loopback
# WebSocket). See docs/irc.md. Disable with IRC=0.
if [ "$IRC" = "1" ]; then
  log "installing Ergo IRC server v${ERGO_VERSION} (irc.${DOMAIN})"
  case "$GOARCH" in
    amd64) ERGO_ARCH=x86_64 ;;
    arm64) ERGO_ARCH=arm64 ;;
    *)     ERGO_ARCH="$GOARCH" ;;
  esac
  id ergo >/dev/null 2>&1 || useradd --system --home-dir "$ERGO_DATA" --shell /usr/sbin/nologin ergo
  install -d -m 0755 /opt/ergo "$ERGO_DATA" "$ERGO_DATA/tls" /etc/ergo

  # Install/upgrade the binary + bundled languages (idempotent: only on version change).
  if [ "$(/opt/ergo/ergo --version 2>/dev/null)" != "ergo-${ERGO_VERSION}" ]; then
    tmp="$(mktemp -d)"
    curl -fsSL "https://github.com/ergochat/ergo/releases/download/v${ERGO_VERSION}/ergo-${ERGO_VERSION}-linux-${ERGO_ARCH}.tar.gz" -o "$tmp/ergo.tgz" \
      || die "could not download Ergo ${ERGO_VERSION}"
    tar -C "$tmp" -xzf "$tmp/ergo.tgz"
    d="$tmp/ergo-${ERGO_VERSION}-linux-${ERGO_ARCH}"
    install -m 0755 "$d/ergo" /opt/ergo/ergo
    rm -rf /opt/ergo/languages && cp -r "$d/languages" /opt/ergo/languages
    rm -rf "$tmp"
  fi

  # Operator password: generate once, keep the plaintext root-only, embed only the hash.
  if [ ! -f "$ENV_DIR/ergo-oper.txt" ]; then
    OPER_PASS="$(head -c18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c24)"
    printf '%s\n' "$OPER_PASS" > "$ENV_DIR/ergo-oper.txt"
    chmod 600 "$ENV_DIR/ergo-oper.txt"
  fi
  OPER_PASS="$(cat "$ENV_DIR/ergo-oper.txt")"
  OPER_HASH="$(printf '%s\n%s\n' "$OPER_PASS" "$OPER_PASS" | /opt/ergo/ergo genpasswd 2>/dev/null | tail -1)"

  # Render the config template from the repo (the __TOKENS__ become real values).
  sed -e "s|__NETWORK__|${IRC_NETWORK}|g" \
      -e "s|__DOMAIN__|${DOMAIN}|g" \
      -e "s|__DATA__|${ERGO_DATA}|g" \
      -e "s|__TLS_DIR__|${ERGO_DATA}/tls|g" \
      -e "s|__LANG_DIR__|/opt/ergo/languages|g" \
      -e "s|__OPER_PASSWORD_HASH__|${OPER_HASH}|g" \
      -e "s|__USERS_DIR__|${DATA_DIR}/users|g" \
      "${SRC_DIR}/deploy/ergo/ircd.yaml" > /etc/ergo/ircd.yaml
  chmod 640 /etc/ergo/ircd.yaml

  # IRC is members-only: this auth-script approves a SASL login only if the
  # account name maps to an AgentBBS member home dir under ${DATA_DIR}/users.
  install -m 0755 "${SRC_DIR}/deploy/ergo/auth-script.sh" /usr/local/bin/ergo-auth-member

  # TLS for 6697: reuse Caddy's Let's Encrypt cert for ${DOMAIN}; self-signed
  # fallback on first run before Caddy has issued it (the timer swaps it in).
  install -m 0755 "${SRC_DIR}/deploy/ergo/refresh-certs.sh" /usr/local/bin/ergo-refresh-certs
  DOMAIN="$DOMAIN" ERGO_DATA="$ERGO_DATA" /usr/local/bin/ergo-refresh-certs || true
  if [ ! -s "$ERGO_DATA/tls/fullchain.pem" ]; then
    warn "no Caddy cert for ${DOMAIN} yet — using a self-signed cert on 6697 until the ergo-certs timer swaps in the real one"
    ( cd /etc/ergo && /opt/ergo/ergo mkcerts --conf /etc/ergo/ircd.yaml --quiet 2>/dev/null ) \
      || openssl req -newkey rsa:2048 -nodes -days 90 -x509 \
           -keyout "$ERGO_DATA/tls/privkey.pem" -out "$ERGO_DATA/tls/fullchain.pem" \
           -subj "/CN=irc.${DOMAIN}" 2>/dev/null
  fi
  chown -R ergo:ergo "$ERGO_DATA" /etc/ergo

  # Initialize the datastore once.
  [ -f "$ERGO_DATA/ircd.db" ] || sudo -u ergo /opt/ergo/ergo initdb --conf /etc/ergo/ircd.yaml --quiet

  log "installing ergo.service"
  cat > /etc/systemd/system/ergo.service <<UNIT
[Unit]
Description=Ergo IRC server (AgentBBS — irc.${DOMAIN})
After=network-online.target
Wants=network-online.target

[Service]
User=ergo
Group=ergo
WorkingDirectory=/opt/ergo
ExecStart=/opt/ergo/ergo run --conf /etc/ergo/ircd.yaml
# Ergo rehashes config + reloads TLS certs on SIGHUP.
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${ERGO_DATA} /etc/ergo

[Install]
WantedBy=multi-user.target
UNIT

  # Daily cert refresh from Caddy (tracks auto-renewals).
  cat > /etc/systemd/system/ergo-certs.service <<UNIT
[Unit]
Description=Refresh Ergo TLS cert from Caddy for ${DOMAIN}

[Service]
Type=oneshot
Environment=DOMAIN=${DOMAIN}
Environment=ERGO_DATA=${ERGO_DATA}
ExecStart=/usr/local/bin/ergo-refresh-certs
UNIT
  cat > /etc/systemd/system/ergo-certs.timer <<UNIT
[Unit]
Description=Periodic Ergo TLS cert refresh from Caddy

[Timer]
OnBootSec=5min
OnUnitActiveSec=12h
Persistent=true

[Install]
WantedBy=timers.target
UNIT

  systemctl daemon-reload
  systemctl enable ergo >/dev/null 2>&1 || true
  systemctl restart ergo
  systemctl enable --now ergo-certs.timer >/dev/null 2>&1 || true
  ufw allow 6697/tcp >/dev/null
  sleep 1
  systemctl is-active --quiet ergo \
    || warn "ergo failed to start — check: journalctl -u ergo -n50"
else
  systemctl disable --now ergo ergo-certs.timer >/dev/null 2>&1 || true
fi

# ---- 10. firewall + start agentbbs on :22 ----------------------------------
log "configuring firewall + starting agentbbs"
ufw allow 22/tcp >/dev/null
ufw --force enable >/dev/null
# enable + restart (not just `enable --now`): a redeploy rebuilds the binary, and
# `enable --now` would leave the OLD process running, so the new build wouldn't load.
systemctl enable agentbbs >/dev/null 2>&1 || true
systemctl restart agentbbs

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
  IRC        irc.${DOMAIN}:6697 (TLS)       native clients   ${IRC:+(set IRC=0 to disable)}
             wss://${DOMAIN}/irc            web clients + agents over WebSocket
             /OPER admin <pw>               oper password in ${ENV_DIR}/ergo-oper.txt

  Config     ${ENV_DIR}/agentbbs.env   (set CoinPay + LiveKit, then: systemctl restart agentbbs)
  Logs       journalctl -u agentbbs -f          (IRC: journalctl -u ergo -f)
  Update     re-run this script (git pull + rebuild + restart)
DONE
warn "Before you log out: open a new terminal and confirm  ssh -p ${ADMIN_SSH_PORT} <you>@${DOMAIN}  works."
warn "If you attached a DigitalOcean Cloud Firewall, also allow ${ADMIN_SSH_PORT}, 22, 80, 443 there."
