#!/usr/bin/env bash
# git.chovy.com: a public Forgejo forge for chovy projects, on chovy's build
# host dev.chovy.com (netcup, profullstack-dev-vienna).
#
# Same recipe as AgentGit (git.profullstack.com, setup.sh §9d): the upstream
# Forgejo binary under systemd, SQLite, loopback HTTP behind the host's
# reverse proxy, Forgejo's built-in SSH server on its own port, open
# registration off, anonymous read of public repos on. What differs is the
# host it lands on:
#
#   * The proxy is the host's existing nginx, not Caddy. nginx already owns
#     :80/:443 there and serves chovy's customer apps (<app>.<login>.chovy.com,
#     written into conf.d/chovy-domain-*.conf by chovy) and the dev.chovy.com
#     userdirs. This adds ONE explicit vhost, sites-available/git.chovy.com;
#     it never edits another vhost. An exact server_name outranks every
#     wildcard and regex name in nginx, so git.chovy.com cannot be captured by
#     the userdirs regexes and does not capture anything else.
#   * The cert is issued by certbot over http-01 into /var/www/acme, the same
#     webroot chovy uses for its customer domains.
#   * :3000 is taken on that box, so Forgejo listens on 127.0.0.1:3010.
#   * MemoryMax caps Forgejo so a runaway cannot starve chovy's builds.
#   * Branding: APP_NAME "chovy git" and chovy's mark as logo + favicon.
#
# Idempotent: app.ini is written once (Forgejo-managed secrets survive reruns),
# every other file is rewritten from here. Run as root on the target host:
#
#   ssh root@dev.chovy.com 'bash -s' < deploy/git-chovy/install.sh
#
# The admin account and its tokens are NOT created here, so no secret ever
# passes through this script; see the "Admin" note at the bottom.
set -euo pipefail

GIT_DOMAIN="${GIT_DOMAIN:-git.chovy.com}"
APP_NAME="${APP_NAME:-chovy git}"
FORGEJO_VERSION="${FORGEJO_VERSION:-11.0.15}"   # same 11.0.x LTS pin as agentbbs setup.sh
FORGEJO_HTTP_ADDR="${FORGEJO_HTTP_ADDR:-127.0.0.1:3010}"
FORGEJO_DATA="${FORGEJO_DATA:-/var/lib/forgejo}"
FORGEJO_SSH_PORT="${FORGEJO_SSH_PORT:-2222}"   # host :22 stays OpenSSH (chovy's platform logs in there)
FORGEJO_MEMORY_MAX="${FORGEJO_MEMORY_MAX:-2G}"
BRAND_ICON_URL="${BRAND_ICON_URL:-https://chovy.com/icons/icon-256x256.png}"  # chovy's square mark (16 KB; favicon.png is 150 KB)
ACME_ROOT="${ACME_ROOT:-/var/www/acme}"
CERTBOT_EMAIL="${CERTBOT_EMAIL:-admin@profullstack.com}"
FORGEJO_CONF=/etc/forgejo/app.ini
VHOST=/etc/nginx/sites-available/${GIT_DOMAIN}

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root"
command -v nginx >/dev/null || die "nginx not found: this recipe adds a vhost to the host's existing nginx"

# Copy a file to <name>.bak-NNN.<ext> beside it before it is replaced. A file
# named after a host (sites-available/git.chovy.com) has no extension, so pass
# "noext" to get <name>.bak-NNN rather than git.chovy.bak-NNN.com.
backup() {
  local f=$1 base ext n
  [ -e "$f" ] || return 0
  base=${f%.*}; ext=${f##*.}
  if [ "${2:-}" = noext ] || [ "$base" = "$f" ] || [[ "$ext" == */* ]]; then base=$f; ext=""; fi
  n=$( (ls -d "$base".bak-[0-9][0-9][0-9]* 2>/dev/null || true) | sed 's/.*bak-\([0-9]*\).*/\1/' | sort -n | tail -1)
  n=$(printf '%03d' $(( 10#${n:-0} + 1 )))
  cp -a "$f" "$base.bak-$n${ext:+.$ext}"
  log "backed up $f -> $base.bak-$n${ext:+.$ext}"
}

# ---- 1. user, dirs, binary ---------------------------------------------------
log "installing Forgejo ${FORGEJO_VERSION} for ${GIT_DOMAIN}"
id -u forgejo >/dev/null 2>&1 \
  || useradd --system --shell /usr/sbin/nologin --home-dir "$FORGEJO_DATA" --create-home forgejo
install -d -m 0750 -o forgejo -g forgejo \
  "$FORGEJO_DATA" "$FORGEJO_DATA/data" "$FORGEJO_DATA/log" "$FORGEJO_DATA/repos" \
  "$FORGEJO_DATA/custom" /etc/forgejo

if [ ! -x /usr/local/bin/forgejo ] \
  || ! /usr/local/bin/forgejo --version 2>/dev/null | grep -qE "version ${FORGEJO_VERSION//./\\.}([+ ]|$)"; then
  case "$(uname -m)" in
    x86_64|amd64)  FJ_ARCH=amd64 ;;
    aarch64|arm64) FJ_ARCH=arm64 ;;
    *) die "unknown arch $(uname -m)" ;;
  esac
  log "downloading forgejo ${FORGEJO_VERSION} (${FJ_ARCH})"
  curl -fsSL "https://codeberg.org/forgejo/forgejo/releases/download/v${FORGEJO_VERSION}/forgejo-${FORGEJO_VERSION}-linux-${FJ_ARCH}" \
    -o /usr/local/bin/forgejo.new
  chmod 0755 /usr/local/bin/forgejo.new
  mv -f /usr/local/bin/forgejo.new /usr/local/bin/forgejo
fi

# ---- 2. app.ini (written once) -----------------------------------------------
if [ ! -f "$FORGEJO_CONF" ]; then
  FJ_SECRET_KEY=$(sudo -u forgejo /usr/local/bin/forgejo generate secret SECRET_KEY)
  FJ_INTERNAL_TOKEN=$(sudo -u forgejo /usr/local/bin/forgejo generate secret INTERNAL_TOKEN)
  FJ_JWT_SECRET=$(sudo -u forgejo /usr/local/bin/forgejo generate secret JWT_SECRET)
  cat > "$FORGEJO_CONF" <<FJ
APP_NAME = ${APP_NAME}
RUN_USER = forgejo
RUN_MODE = prod

[server]
PROTOCOL = http
HTTP_ADDR = ${FORGEJO_HTTP_ADDR%%:*}
HTTP_PORT = ${FORGEJO_HTTP_ADDR##*:}
DOMAIN = ${GIT_DOMAIN}
ROOT_URL = https://${GIT_DOMAIN}/
SSH_DOMAIN = ${GIT_DOMAIN}
# Built-in SSH server (in-process, runs as forgejo). Host :22 is OpenSSH and
# chovy's platform depends on it, so Forgejo gets its own port; clone URLs are
# ssh://git@${GIT_DOMAIN}:${FORGEJO_SSH_PORT}/<owner>/<repo>.git
DISABLE_SSH = false
START_SSH_SERVER = true
# Both are needed: SSH_USER only changes the advertised clone URL, while the
# built-in server accepts the BUILTIN_SSH_SERVER_USER name (default: RUN_USER,
# i.e. forgejo) and refuses git@ with "Invalid SSH username git".
BUILTIN_SSH_SERVER_USER = git
SSH_USER = git
SSH_PORT = ${FORGEJO_SSH_PORT}
SSH_LISTEN_PORT = ${FORGEJO_SSH_PORT}

[database]
DB_TYPE = sqlite3
PATH = ${FORGEJO_DATA}/data/forgejo.db

[repository]
ROOT = ${FORGEJO_DATA}/repos

[service]
# Same policy as git.profullstack.com: no self-serve sign-up (accounts are made
# by the admin), and anyone can browse public repos and profiles anonymously.
DISABLE_REGISTRATION = true
REQUIRE_SIGNIN_VIEW = false
DEFAULT_KEEP_EMAIL_PRIVATE = true

[security]
INSTALL_LOCK = true
SECRET_KEY = ${FJ_SECRET_KEY}
INTERNAL_TOKEN = ${FJ_INTERNAL_TOKEN}

[oauth2]
JWT_SECRET = ${FJ_JWT_SECRET}

[log]
ROOT_PATH = ${FORGEJO_DATA}/log
FJ
  chown forgejo:forgejo "$FORGEJO_CONF"
  chmod 0640 "$FORGEJO_CONF"
  log "wrote $FORGEJO_CONF"
fi

# app.ini predating BUILTIN_SSH_SERVER_USER: add it, or git@ over SSH is refused.
if ! grep -q '^BUILTIN_SSH_SERVER_USER' "$FORGEJO_CONF"; then
  backup "$FORGEJO_CONF"
  sed -i 's/^SSH_USER = git$/BUILTIN_SSH_SERVER_USER = git\nSSH_USER = git/' "$FORGEJO_CONF"
  grep -q '^BUILTIN_SSH_SERVER_USER = git' "$FORGEJO_CONF" || die "could not set BUILTIN_SSH_SERVER_USER in $FORGEJO_CONF"
  log "set BUILTIN_SSH_SERVER_USER = git in $FORGEJO_CONF"
fi

# ---- 3. branding: chovy's mark as logo + favicon -----------------------------
# Forgejo serves custom/public/assets/img/* over its built-in assets. The navbar
# uses logo.svg, so the PNG is wrapped in an SVG (an <img>-loaded SVG may embed
# a data: image).
IMG="$FORGEJO_DATA/custom/public/assets/img"
install -d -m 0755 -o forgejo -g forgejo "$FORGEJO_DATA/custom/public" "$FORGEJO_DATA/custom/public/assets" "$IMG"
if curl -fsSL "$BRAND_ICON_URL" -o "$IMG/.brand.png" && file "$IMG/.brand.png" | grep -q 'PNG image'; then
  for n in logo.png favicon.png apple-touch-icon.png avatar_default.png; do cp "$IMG/.brand.png" "$IMG/$n"; done
  B64=$(base64 -w0 "$IMG/.brand.png")
  for n in logo.svg favicon.svg; do
    printf '<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 512 512" width="512" height="512"><image width="512" height="512" href="data:image/png;base64,%s" xlink:href="data:image/png;base64,%s"/></svg>\n' "$B64" "$B64" > "$IMG/$n"
  done
  rm -f "$IMG/.brand.png"
  chown -R forgejo:forgejo "$FORGEJO_DATA/custom"
  log "branding installed from $BRAND_ICON_URL"
else
  rm -f "$IMG/.brand.png"
  warn "could not fetch $BRAND_ICON_URL; keeping whatever logo is already there"
fi

# ---- 4. systemd unit ---------------------------------------------------------
cat > /etc/systemd/system/forgejo.service <<UNIT
[Unit]
Description=Forgejo (${APP_NAME} — ${GIT_DOMAIN})
After=network-online.target
Wants=network-online.target

[Service]
User=forgejo
Group=forgejo
WorkingDirectory=${FORGEJO_DATA}
Environment=GITEA_WORK_DIR=${FORGEJO_DATA}
Environment=GITEA_CUSTOM=${FORGEJO_DATA}/custom
ExecStart=/usr/local/bin/forgejo web --config ${FORGEJO_CONF} --work-path ${FORGEJO_DATA}
Restart=always
RestartSec=2
# This host builds chovy's customer apps; never let the forge starve them.
MemoryHigh=$(( $(numfmt --from=iec "$FORGEJO_MEMORY_MAX") * 3 / 4 ))
MemoryMax=${FORGEJO_MEMORY_MAX}
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${FORGEJO_DATA} /etc/forgejo

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable forgejo >/dev/null 2>&1 || true
systemctl restart forgejo
# Wait for the loopback listener (the public HTTPS check is the vhost's job).
for _ in $(seq 1 20); do
  ss -Hltn "sport = :${FORGEJO_HTTP_ADDR##*:}" | grep -q . && break
  sleep 1
done
systemctl is-active --quiet forgejo || die "forgejo failed to start: journalctl -u forgejo -n50"
ss -Hltn "sport = :${FORGEJO_HTTP_ADDR##*:}" | grep -q . || die "forgejo is not listening on ${FORGEJO_HTTP_ADDR}"
log "forgejo up on ${FORGEJO_HTTP_ADDR}: $(/usr/local/bin/forgejo --version 2>/dev/null | head -1)"

# ---- 5. firewall: open the git SSH port only if ufw is active ----------------
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active'; then
  ufw allow "${FORGEJO_SSH_PORT}/tcp" comment "forgejo ssh (${GIT_DOMAIN})" >/dev/null
  log "ufw: allowed ${FORGEJO_SSH_PORT}/tcp"
fi

# ---- 6. nginx vhost + certificate --------------------------------------------
install -d -m 0755 "$ACME_ROOT"
CERT=/etc/letsencrypt/live/${GIT_DOMAIN}/fullchain.pem

http_block() {
  cat <<NGX
# git.chovy.com -> Forgejo on ${FORGEJO_HTTP_ADDR}. Written by
# agentbbs deploy/git-chovy/install.sh; rerun it rather than editing by hand.
# An exact server_name, so it outranks the *.dev.chovy.com regex vhosts and
# chovy's per-domain conf.d files without touching any of them.
server {
	listen 80;
	listen [::]:80;
	server_name ${GIT_DOMAIN};
	location ^~ /.well-known/acme-challenge/ {
		root ${ACME_ROOT};
		default_type "text/plain";
	}
	location / { return 301 https://\$host\$request_uri; }
}
NGX
}

https_block() {
  cat <<NGX

server {
	listen 443 ssl;
	listen [::]:443 ssl;
	http2 on;
	server_name ${GIT_DOMAIN};
	ssl_certificate     /etc/letsencrypt/live/${GIT_DOMAIN}/fullchain.pem;
	ssl_certificate_key /etc/letsencrypt/live/${GIT_DOMAIN}/privkey.pem;
	server_tokens off;

	# git pushes over HTTPS and release/LFS uploads
	client_max_body_size 512m;

	location / {
		proxy_pass http://${FORGEJO_HTTP_ADDR};
		proxy_http_version 1.1;
		proxy_set_header Host \$host;
		proxy_set_header X-Real-IP \$remote_addr;
		proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto https;
		proxy_set_header Upgrade \$http_upgrade;
		proxy_set_header Connection \$connection_upgrade;
		# smart-HTTP clone/push streams; buffering stalls large packs
		proxy_buffering off;
		proxy_request_buffering off;
		proxy_read_timeout 600s;
		proxy_send_timeout 600s;
	}
}
NGX
}

# Write the vhost, validate, reload; on a failed nginx -t put the old one back.
install_vhost() {
  local tmp
  tmp=$(mktemp)
  cat > "$tmp"
  if [ -f "$VHOST" ] && cmp -s "$tmp" "$VHOST"; then rm -f "$tmp"; return 0; fi
  backup "$VHOST" noext
  local prev=""
  [ -f "$VHOST" ] && prev=$(mktemp) && cp -a "$VHOST" "$prev"
  install -m 0644 "$tmp" "$VHOST"; rm -f "$tmp"
  ln -sfn "$VHOST" "/etc/nginx/sites-enabled/${GIT_DOMAIN}"
  if nginx -t 2>/tmp/git-chovy-nginx-t.log; then
    systemctl reload nginx
    log "nginx reloaded with $VHOST"
    if [ -n "$prev" ]; then rm -f "$prev"; fi
  else
    cat /tmp/git-chovy-nginx-t.log >&2
    if [ -n "$prev" ]; then install -m 0644 "$prev" "$VHOST"; rm -f "$prev"
    else rm -f "$VHOST" "/etc/nginx/sites-enabled/${GIT_DOMAIN}"; fi
    die "nginx -t failed; restored the previous state, nothing reloaded"
  fi
}

if [ ! -s "$CERT" ]; then
  log "no cert yet: serving the http-01 webroot for ${GIT_DOMAIN}"
  http_block | install_vhost
  certbot certonly --webroot -w "$ACME_ROOT" -d "$GIT_DOMAIN" \
    --non-interactive --agree-tos -m "$CERTBOT_EMAIL" --keep-until-expiring \
    --deploy-hook "systemctl reload nginx"
fi
{ http_block; https_block; } | install_vhost

log "done: https://${GIT_DOMAIN}  ssh://git@${GIT_DOMAIN}:${FORGEJO_SSH_PORT}/<owner>/<repo>.git"

# ---- Admin -------------------------------------------------------------------
# Not automated, so the password and tokens go straight from Forgejo into the
# vault (logicsrc team vault git-chovy-com--prod) without touching this script,
# a log, or argv:
#
#   sudo -u forgejo GITEA_WORK_DIR=/var/lib/forgejo forgejo admin user create \
#     --config /etc/forgejo/app.ini --admin --username chovy-admin \
#     --email chovy-admin@git.chovy.com --random-password --must-change-password=false
#   sudo -u forgejo GITEA_WORK_DIR=/var/lib/forgejo forgejo admin user generate-access-token \
#     --config /etc/forgejo/app.ini --username chovy-admin --token-name tea-<host> --raw \
#     --scopes write:repository,write:issue,write:organization,write:user,write:notification,write:package,read:misc
#
# Backups: none, matching git.profullstack.com (setup.sh has no forgejo dump).
