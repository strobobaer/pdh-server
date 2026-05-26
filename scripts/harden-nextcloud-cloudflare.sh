#!/usr/bin/env bash
set -Eeuo pipefail

# Harden an existing Nextcloud installation behind Cloudflare Tunnel.
# Stack: Nginx + PHP-FPM + PostgreSQL + Nextcloud.
# Public URL: https://cloud.strobl-home.net
# Local origin: https://localhost:8436

DOMAIN="cloud.strobl-home.net"
ORIGIN_PORT="8436"
NEXTCLOUD_DIR="/var/www/nextcloud"
PHP_VERSION=""
REDIS_SOCKET="/run/redis/redis-server.sock"
PHP_MEMORY_LIMIT="512M"
PHP_UPLOAD_LIMIT="1024M"
PHP_MAX_EXECUTION_TIME="360"
TRUSTED_PROXY="127.0.0.1"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\n\033[1;33mWARN: %s\033[0m\n' "$*"; }
err() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Harden Nextcloud for Cloudflare Tunnel, Nginx, PHP-FPM and PostgreSQL.

Options:
  --domain DOMAIN             Public Nextcloud domain, default: cloud.strobl-home.net
  --origin-port PORT          Local HTTPS origin port, default: 8436
  --nextcloud-dir PATH        Nextcloud install dir, default: /var/www/nextcloud
  --php-version VERSION       PHP version, e.g. 8.4. Auto-detected when omitted.
  --redis-socket PATH         Redis socket, default: /run/redis/redis-server.sock
  --memory-limit VALUE        PHP memory_limit, default: 512M
  --upload-limit VALUE        upload_max_filesize/post_max_size, default: 1024M
  --max-execution-time SEC    PHP max_execution_time, default: 360
  --trusted-proxy IP          Trusted proxy for Nextcloud, default: 127.0.0.1
  -h, --help                  Show this help
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --origin-port) ORIGIN_PORT="${2:-}"; shift 2 ;;
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --php-version) PHP_VERSION="${2:-}"; shift 2 ;;
    --redis-socket) REDIS_SOCKET="${2:-}"; shift 2 ;;
    --memory-limit) PHP_MEMORY_LIMIT="${2:-}"; shift 2 ;;
    --upload-limit) PHP_UPLOAD_LIMIT="${2:-}"; shift 2 ;;
    --max-execution-time) PHP_MAX_EXECUTION_TIME="${2:-}"; shift 2 ;;
    --trusted-proxy) TRUSTED_PROXY="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  err "Run as root, e.g. sudo bash $0"
  exit 1
fi

if [[ ! -f "${NEXTCLOUD_DIR}/occ" ]]; then
  err "Nextcloud occ not found: ${NEXTCLOUD_DIR}/occ"
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive

log "Detecting PHP-FPM"
if [[ -z "${PHP_VERSION}" ]]; then
  PHP_VERSION="$(php -r 'echo PHP_MAJOR_VERSION.".".PHP_MINOR_VERSION;')"
fi
PHP_SOCKET="/run/php/php${PHP_VERSION}-fpm.sock"
if [[ ! -S "${PHP_SOCKET}" ]]; then
  PHP_SOCKET="$(find /run/php -maxdepth 1 -type s -name 'php*-fpm.sock' | sort -V | tail -n 1 || true)"
fi
if [[ -z "${PHP_SOCKET}" || ! -S "${PHP_SOCKET}" ]]; then
  err "No PHP-FPM socket found in /run/php"
  exit 1
fi
PHP_FPM_SERVICE="$(basename "${PHP_SOCKET}" .sock)"
PHP_VERSION="$(basename "${PHP_SOCKET}" | sed -E 's/^php([0-9]+\.[0-9]+)-fpm\.sock$/\1/')"
log "Using PHP ${PHP_VERSION} via ${PHP_SOCKET}"

log "Installing hardening packages"
apt-get update
apt-get install -y php-apcu php-redis redis-server imagemagick php-imagick acl

log "Cleaning duplicate APCu extension configuration"
find "/etc/php/${PHP_VERSION}/cli/conf.d" "/etc/php/${PHP_VERSION}/fpm/conf.d" \
  -maxdepth 1 -type f -name '*nextcloud-apcu.ini' -delete 2>/dev/null || true
find "/etc/php/${PHP_VERSION}/cli/conf.d" "/etc/php/${PHP_VERSION}/fpm/conf.d" \
  -maxdepth 1 -type f -name '99-apcu.ini' -delete 2>/dev/null || true
phpenmod -v "${PHP_VERSION}" apcu redis imagick || true
printf 'apc.enable_cli=1\n' > "/etc/php/${PHP_VERSION}/cli/conf.d/99-nextcloud-apcu-cli.ini"
printf '; APCu is loaded by phpenmod. Do not load extension again here.\napc.enabled=1\n' > "/etc/php/${PHP_VERSION}/fpm/conf.d/99-nextcloud-apcu-fpm.ini"

log "Tuning PHP"
PHP_INI_CANDIDATES=(
  "/etc/php/${PHP_VERSION}/fpm/php.ini"
  "/etc/php/${PHP_VERSION}/cli/php.ini"
)
for ini in "${PHP_INI_CANDIDATES[@]}"; do
  [[ -f "${ini}" ]] || continue
  sed -i "s/^memory_limit = .*/memory_limit = ${PHP_MEMORY_LIMIT}/" "${ini}"
  sed -i "s/^upload_max_filesize = .*/upload_max_filesize = ${PHP_UPLOAD_LIMIT}/" "${ini}"
  sed -i "s/^post_max_size = .*/post_max_size = ${PHP_UPLOAD_LIMIT}/" "${ini}"
  sed -i "s/^max_execution_time = .*/max_execution_time = ${PHP_MAX_EXECUTION_TIME}/" "${ini}"
  sed -i 's/^;date.timezone =.*/date.timezone = Europe\/Berlin/' "${ini}" || true
  sed -i 's/^date.timezone =.*/date.timezone = Europe\/Berlin/' "${ini}" || true
  sed -i 's/^;opcache.enable=.*/opcache.enable=1/' "${ini}" || true
  sed -i 's/^;opcache.enable_cli=.*/opcache.enable_cli=1/' "${ini}" || true
  sed -i 's/^;opcache.interned_strings_buffer=.*/opcache.interned_strings_buffer=32/' "${ini}" || true
  sed -i 's/^;opcache.max_accelerated_files=.*/opcache.max_accelerated_files=10000/' "${ini}" || true
  sed -i 's/^;opcache.memory_consumption=.*/opcache.memory_consumption=128/' "${ini}" || true
  sed -i 's/^;opcache.save_comments=.*/opcache.save_comments=1/' "${ini}" || true
done

log "Configuring Redis local socket"
systemctl enable redis-server
systemctl restart redis-server
if [[ -f /etc/redis/redis.conf ]]; then
  cp /etc/redis/redis.conf "/etc/redis/redis.conf.bak.$(date +%Y%m%d%H%M%S)"
  sed -i 's/^# unixsocket .*/unixsocket \/run\/redis\/redis-server.sock/' /etc/redis/redis.conf
  sed -i 's/^unixsocket .*/unixsocket \/run\/redis\/redis-server.sock/' /etc/redis/redis.conf
  sed -i 's/^# unixsocketperm .*/unixsocketperm 770/' /etc/redis/redis.conf
  sed -i 's/^unixsocketperm .*/unixsocketperm 770/' /etc/redis/redis.conf
  systemctl restart redis-server
fi
usermod -aG redis www-data || true

log "Hardening Nginx MIME/HSTS and proxy settings"
NGINX_SITE="/etc/nginx/sites-available/nextcloud-cloudflare.conf"
if [[ -f "${NGINX_SITE}" ]]; then
  cp "${NGINX_SITE}" "${NGINX_SITE}.bak.$(date +%Y%m%d%H%M%S)"
  if ! grep -q 'application/javascript.*mjs' /etc/nginx/mime.types; then
    if grep -q 'application/javascript' /etc/nginx/mime.types; then
      sed -i 's/application\/javascript[[:space:]]\+js;/application\/javascript js mjs;/' /etc/nginx/mime.types
    elif grep -q 'text\/javascript' /etc/nginx/mime.types; then
      sed -i 's/text\/javascript[[:space:]]\+js;/text\/javascript js mjs;/' /etc/nginx/mime.types
    fi
  fi
  if ! grep -q 'Strict-Transport-Security' "${NGINX_SITE}"; then
    sed -i '/add_header X-XSS-Protection/a\    add_header Strict-Transport-Security "max-age=15552000; includeSubDomains" always;' "${NGINX_SITE}"
  fi
  if ! grep -q 'fastcgi_param HTTP_X_FORWARDED_FOR' "${NGINX_SITE}"; then
    sed -i '/fastcgi_param HTTPS on;/a\        fastcgi_param HTTP_X_FORWARDED_FOR $http_x_forwarded_for;\n        fastcgi_param HTTP_X_REAL_IP $http_x_real_ip;\n        fastcgi_param HTTP_CF_CONNECTING_IP $http_cf_connecting_ip;\n        fastcgi_param HTTP_X_FORWARDED_PROTO $http_x_forwarded_proto;\n        fastcgi_param HTTP_X_FORWARDED_HOST $http_x_forwarded_host;' "${NGINX_SITE}"
  fi
  if ! grep -q 'real_ip_header CF-Connecting-IP' "${NGINX_SITE}"; then
    sed -i '/server_name/a\
\n    set_real_ip_from 127.0.0.1;\n    set_real_ip_from ::1;\n    real_ip_header CF-Connecting-IP;\n    real_ip_recursive on;' "${NGINX_SITE}"
  fi
  if grep -q 'listen 127.0.0.1:' "${NGINX_SITE}" && grep -q 'listen .* ssl http2;' "${NGINX_SITE}"; then
    sed -i -E 's/(listen .* ssl) http2;/\1;\n    http2 on;/' "${NGINX_SITE}"
  fi
fi

log "Hardening filesystem permissions"
chown -R www-data:www-data "${NEXTCLOUD_DIR}"
find "${NEXTCLOUD_DIR}" -type d -exec chmod 750 {} \;
find "${NEXTCLOUD_DIR}" -type f -exec chmod 640 {} \;
chmod 750 "${NEXTCLOUD_DIR}/occ" || true

log "Applying Nextcloud system configuration"
OCC=(sudo -u www-data php -d apc.enable_cli=1 "${NEXTCLOUD_DIR}/occ")
"${OCC[@]}" config:system:set trusted_domains 0 --value="${DOMAIN}"
"${OCC[@]}" config:system:set trusted_domains 1 --value="localhost:${ORIGIN_PORT}"
"${OCC[@]}" config:system:set trusted_proxies 0 --value="${TRUSTED_PROXY}"
"${OCC[@]}" config:system:set trusted_proxies 1 --value="::1"
"${OCC[@]}" config:system:set forwarded_for_headers 0 --value="HTTP_CF_CONNECTING_IP"
"${OCC[@]}" config:system:set forwarded_for_headers 1 --value="HTTP_X_FORWARDED_FOR"
"${OCC[@]}" config:system:set overwrite.cli.url --value="https://${DOMAIN}"
"${OCC[@]}" config:system:set overwritehost --value="${DOMAIN}"
"${OCC[@]}" config:system:set overwriteprotocol --value="https"
"${OCC[@]}" config:system:set default_phone_region --value="DE"
"${OCC[@]}" config:system:set maintenance_window_start --type=integer --value=1
"${OCC[@]}" config:system:set memcache.local --value='\OC\Memcache\APCu'
"${OCC[@]}" config:system:set memcache.locking --value='\OC\Memcache\Redis'
"${OCC[@]}" config:system:set redis host --value="${REDIS_SOCKET}"
"${OCC[@]}" config:system:set redis port --type=integer --value=0
"${OCC[@]}" config:system:set filelocking.enabled --type=boolean --value=true
"${OCC[@]}" config:system:set logtimezone --value="Europe/Berlin"
"${OCC[@]}" config:system:set loglevel --type=integer --value=2
"${OCC[@]}" config:system:set trashbin_retention_obligation --value="auto, 30"
"${OCC[@]}" config:system:set versions_retention_obligation --value="auto, 30"
"${OCC[@]}" config:system:set skeletondirectory --value=""
"${OCC[@]}" background:cron

log "Configuring Nextcloud apps and maintenance"
"${OCC[@]}" app:enable files_pdfviewer || true
"${OCC[@]}" app:enable activity || true
"${OCC[@]}" db:add-missing-indices || true
"${OCC[@]}" db:add-missing-columns || true
"${OCC[@]}" maintenance:repair --include-expensive || true

log "Ensuring cron job"
cat > /etc/cron.d/nextcloud <<CRON
*/5 * * * * www-data php -d apc.enable_cli=1 -f ${NEXTCLOUD_DIR}/cron.php
CRON
chmod 644 /etc/cron.d/nextcloud

log "Restarting services"
systemctl restart redis-server
systemctl restart "${PHP_FPM_SERVICE}" 2>/dev/null || systemctl restart "php${PHP_VERSION}-fpm"
nginx -t
systemctl reload nginx

log "Checks"
printf '\nPHP modules:\n'
php -d apc.enable_cli=1 -m | grep -Ei 'apcu|redis|imagick' || true
printf '\nNextcloud proxy config:\n'
"${OCC[@]}" config:system:get trusted_proxies || true
"${OCC[@]}" config:system:get forwarded_for_headers || true
printf '\nNextcloud status:\n'
"${OCC[@]}" status || true
printf '\nNextcloud security overview / setup checks:\n'
"${OCC[@]}" setupchecks || true
printf '\nLocal origin:\n'
curl -kI "https://localhost:${ORIGIN_PORT}/status.php" || true
printf '\nListening ports:\n'
ss -tulpn | grep -E ":${ORIGIN_PORT}|:8090|:5432" || true

log "Hardening finished"
printf '\nPublic URL:       https://%s\n' "${DOMAIN}"
printf 'Cloudflare origin: https://localhost:%s\n' "${ORIGIN_PORT}"
printf 'Next step: check Nextcloud Admin Overview in the browser.\n'
