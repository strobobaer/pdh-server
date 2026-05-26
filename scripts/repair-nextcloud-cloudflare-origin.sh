#!/usr/bin/env bash
set -Eeuo pipefail

# Repair/finish a Nextcloud Cloudflare Tunnel origin on Nginx + PostgreSQL.
# Target: https://cloud.strobl-home.net -> https://localhost:8436
# Fixes:
# - APCu available for Nextcloud occ/CLI
# - Nginx local HTTPS origin on 127.0.0.1:8436
# - Nextcloud overwrite/trusted domain config for Cloudflare HTTPS

DOMAIN="cloud.strobl-home.net"
ORIGIN_HOST="127.0.0.1"
ORIGIN_PORT="8436"
NEXTCLOUD_DIR="/var/www/nextcloud"
SSL_DIR="/etc/ssl/pdh-nextcloud"
SSL_CERT="/etc/ssl/pdh-nextcloud/cloud-origin.crt"
SSL_KEY="/etc/ssl/pdh-nextcloud/cloud-origin.key"
PHP_VERSION=""

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\n\033[1;33mWARN: %s\033[0m\n' "$*"; }
err() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Repair Nextcloud Cloudflare origin.

Defaults:
  Public URL:       https://cloud.strobl-home.net
  Cloudflare origin https://localhost:8436
  Nginx listen:     127.0.0.1:8436 ssl
  Nextcloud dir:    /var/www/nextcloud

Options:
  --domain DOMAIN
  --origin-host HOST
  --origin-port PORT
  --nextcloud-dir PATH
  --php-version VERSION     e.g. 8.3
  --ssl-cert PATH
  --ssl-key PATH
  -h, --help
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --origin-host) ORIGIN_HOST="${2:-}"; shift 2 ;;
    --origin-port) ORIGIN_PORT="${2:-}"; shift 2 ;;
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --php-version) PHP_VERSION="${2:-}"; shift 2 ;;
    --ssl-cert) SSL_CERT="${2:-}"; shift 2 ;;
    --ssl-key) SSL_KEY="${2:-}"; shift 2 ;;
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

log "Installing required packages"
apt-get update
apt-get install -y nginx openssl php-apcu php-redis redis-server

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

log "Enabling APCu for PHP CLI and FPM"
mkdir -p "/etc/php/${PHP_VERSION}/cli/conf.d" "/etc/php/${PHP_VERSION}/fpm/conf.d"
printf 'apc.enable_cli=1\n' > "/etc/php/${PHP_VERSION}/cli/conf.d/99-nextcloud-apcu.ini"
printf 'apc.enable_cli=1\n' > "/etc/php/${PHP_VERSION}/fpm/conf.d/99-nextcloud-apcu.ini"
phpenmod apcu || true
systemctl restart "${PHP_FPM_SERVICE}" 2>/dev/null || systemctl restart "php${PHP_VERSION}-fpm"

log "Preparing local HTTPS certificate"
SSL_DIR="$(dirname "${SSL_CERT}")"
mkdir -p "${SSL_DIR}"
if [[ ! -f "${SSL_CERT}" || ! -f "${SSL_KEY}" ]]; then
  openssl req -x509 -nodes -days 3650 -newkey rsa:4096 \
    -keyout "${SSL_KEY}" \
    -out "${SSL_CERT}" \
    -subj "/CN=${DOMAIN}" \
    -addext "subjectAltName=DNS:${DOMAIN},DNS:localhost,IP:127.0.0.1"
  chmod 600 "${SSL_KEY}"
  chmod 644 "${SSL_CERT}"
  warn "Generated self-signed local origin cert. In Cloudflare Tunnel set noTLSVerify=true."
fi

log "Writing Nginx Cloudflare origin site"
NGINX_SITE="/etc/nginx/sites-available/nextcloud-cloudflare.conf"
[[ -f "${NGINX_SITE}" ]] && cp "${NGINX_SITE}" "${NGINX_SITE}.bak.$(date +%Y%m%d%H%M%S)"

cat > "${NGINX_SITE}" <<NGINX
upstream nextcloud_php_handler {
    server unix:${PHP_SOCKET};
}

server {
    listen ${ORIGIN_HOST}:${ORIGIN_PORT} ssl http2;
    server_name ${DOMAIN} localhost;

    ssl_certificate ${SSL_CERT};
    ssl_certificate_key ${SSL_KEY};
    ssl_protocols TLSv1.2 TLSv1.3;

    root ${NEXTCLOUD_DIR};
    index index.php index.html /index.php\$request_uri;

    client_max_body_size 1024M;
    client_body_timeout 300s;
    fastcgi_buffers 64 4K;

    add_header Referrer-Policy "no-referrer" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Permitted-Cross-Domain-Policies "none" always;
    add_header X-Robots-Tag "noindex, nofollow" always;
    add_header X-XSS-Protection "1; mode=block" always;
    fastcgi_hide_header X-Powered-By;

    location = /robots.txt {
        allow all;
        log_not_found off;
        access_log off;
    }

    location ^~ /.well-known {
        location = /.well-known/carddav { return 301 /remote.php/dav/; }
        location = /.well-known/caldav  { return 301 /remote.php/dav/; }
        location /.well-known/acme-challenge { try_files \$uri \$uri/ =404; }
        location /.well-known/pki-validation { try_files \$uri \$uri/ =404; }
        return 301 /index.php\$request_uri;
    }

    location ~ ^/(?:build|tests|config|lib|3rdparty|templates|data)(?:\$|/) { return 404; }
    location ~ ^/(?:\.|autotest|occ|issue|indie|db_|console) { return 404; }

    location ~ \.php(?:\$|/) {
        rewrite ^/(?!index|remote|public|cron|core/ajax/update|status|ocs/v[12]|updater/.+|ocs-provider/.+|.+/richdocumentscode/proxy) /index.php\$request_uri;
        fastcgi_split_path_info ^(.+?\.php)(/.*)\$;
        set \$path_info \$fastcgi_path_info;
        try_files \$fastcgi_script_name =404;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
        fastcgi_param PATH_INFO \$path_info;
        fastcgi_param HTTPS on;
        fastcgi_param modHeadersAvailable true;
        fastcgi_param front_controller_active true;
        fastcgi_pass nextcloud_php_handler;
        fastcgi_intercept_errors on;
        fastcgi_request_buffering off;
        fastcgi_max_temp_file_size 0;
    }

    location ~ \.(?:css|js|mjs|svg|gif|png|jpg|ico|wasm|tflite|map|ogg|flac|woff2?)\$ {
        try_files \$uri /index.php\$request_uri;
        expires 6M;
        access_log off;
    }

    location / {
        try_files \$uri \$uri/ /index.php\$request_uri;
    }
}
NGINX

ln -sfn "${NGINX_SITE}" /etc/nginx/sites-enabled/nextcloud-cloudflare.conf
nginx -t
systemctl enable nginx
systemctl reload nginx

log "Applying Nextcloud Cloudflare config"
OCC=(sudo -u www-data php -d apc.enable_cli=1 "${NEXTCLOUD_DIR}/occ")
"${OCC[@]}" config:system:set trusted_domains 0 --value="${DOMAIN}"
"${OCC[@]}" config:system:set trusted_domains 1 --value="localhost:${ORIGIN_PORT}"
"${OCC[@]}" config:system:set overwrite.cli.url --value="https://${DOMAIN}"
"${OCC[@]}" config:system:set overwritehost --value="${DOMAIN}"
"${OCC[@]}" config:system:set overwriteprotocol --value="https"
"${OCC[@]}" config:system:set default_phone_region --value="DE"
"${OCC[@]}" config:system:set maintenance_window_start --type=integer --value=1
"${OCC[@]}" config:system:set memcache.local --value='\OC\Memcache\APCu'
"${OCC[@]}" background:cron || true

log "Installing cron job"
cat > /etc/cron.d/nextcloud <<CRON
*/5 * * * * www-data php -d apc.enable_cli=1 -f ${NEXTCLOUD_DIR}/cron.php
CRON

systemctl restart "${PHP_FPM_SERVICE}" 2>/dev/null || systemctl restart "php${PHP_VERSION}-fpm"
systemctl reload nginx

log "Checks"
ss -tulpn | grep -E ":${ORIGIN_PORT}|:8090|:5432" || true
curl -kI "https://localhost:${ORIGIN_PORT}/status.php" || true
"${OCC[@]}" status || true

log "Repair finished"
printf '\nCloudflare public URL: https://%s\n' "${DOMAIN}"
printf 'Cloudflare origin:     https://localhost:%s\n' "${ORIGIN_PORT}"
printf 'If using generated self-signed cert: noTLSVerify=true\n'
