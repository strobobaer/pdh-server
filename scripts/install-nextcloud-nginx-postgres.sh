#!/usr/bin/env bash
set -Eeuo pipefail

# Nextcloud installer for PDH servers.
# Stack: Nginx + PHP-FPM + PostgreSQL + Nextcloud, no Docker, no Apache, no MariaDB.
#
# Example:
#   sudo bash scripts/install-nextcloud-nginx-postgres.sh \
#     --domain cloud.example.local \
#     --admin-user michael \
#     --admin-pass 'ChangeMeNow!' \
#     --db-pass 'ChangeMeDb!' \
#     --data-dir /var/ncdata
#
# Optional cleanup of Apache/MariaDB:
#   sudo bash scripts/install-nextcloud-nginx-postgres.sh --purge-apache-mariadb ...

NEXTCLOUD_DOMAIN=""
NEXTCLOUD_ADMIN_USER="admin"
NEXTCLOUD_ADMIN_PASS=""
NEXTCLOUD_DB_NAME="nextcloud"
NEXTCLOUD_DB_USER="nextcloud"
NEXTCLOUD_DB_PASS=""
NEXTCLOUD_DIR="/var/www/nextcloud"
NEXTCLOUD_DATA_DIR="/var/ncdata"
PHP_VERSION=""
PURGE_APACHE_MARIADB="0"
INSTALL_LATEST_URL="https://download.nextcloud.com/server/releases/latest.tar.bz2"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\n\033[1;33mWARN: %s\033[0m\n' "$*"; }
err() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Install Nextcloud with Nginx, PHP-FPM and PostgreSQL.

Required:
  --domain DOMAIN             Nextcloud domain, e.g. cloud.example.local
  --admin-pass PASSWORD       Initial Nextcloud admin password
  --db-pass PASSWORD          PostgreSQL password for the nextcloud DB user

Optional:
  --admin-user USER           Initial Nextcloud admin user, default: admin
  --db-name NAME              PostgreSQL database name, default: nextcloud
  --db-user USER              PostgreSQL database user, default: nextcloud
  --install-dir PATH          Nextcloud install dir, default: /var/www/nextcloud
  --data-dir PATH             Nextcloud data dir, default: /var/ncdata
  --php-version VERSION       Override PHP version, e.g. 8.3
  --purge-apache-mariadb      Stop and purge Apache/MariaDB packages first
  -h, --help                  Show this help

Notes:
  - Run as root: sudo bash scripts/install-nextcloud-nginx-postgres.sh ...
  - This script does not request TLS certificates. Use certbot after DNS works.
  - This script is designed for Debian/Ubuntu-style systems.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) NEXTCLOUD_DOMAIN="${2:-}"; shift 2 ;;
    --admin-user) NEXTCLOUD_ADMIN_USER="${2:-}"; shift 2 ;;
    --admin-pass) NEXTCLOUD_ADMIN_PASS="${2:-}"; shift 2 ;;
    --db-name) NEXTCLOUD_DB_NAME="${2:-}"; shift 2 ;;
    --db-user) NEXTCLOUD_DB_USER="${2:-}"; shift 2 ;;
    --db-pass) NEXTCLOUD_DB_PASS="${2:-}"; shift 2 ;;
    --install-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --data-dir) NEXTCLOUD_DATA_DIR="${2:-}"; shift 2 ;;
    --php-version) PHP_VERSION="${2:-}"; shift 2 ;;
    --purge-apache-mariadb) PURGE_APACHE_MARIADB="1"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  err "Run this script as root, e.g. with sudo."
  exit 1
fi

if [[ -z "${NEXTCLOUD_DOMAIN}" || -z "${NEXTCLOUD_ADMIN_PASS}" || -z "${NEXTCLOUD_DB_PASS}" ]]; then
  err "Missing required argument."
  usage
  exit 2
fi

if [[ -e "${NEXTCLOUD_DIR}/config/config.php" ]]; then
  err "Nextcloud already appears to be installed at ${NEXTCLOUD_DIR}. Aborting to avoid overwrite."
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive

if [[ "${PURGE_APACHE_MARIADB}" == "1" ]]; then
  log "Stopping and purging Apache/MariaDB packages"
  systemctl stop apache2 2>/dev/null || true
  systemctl disable apache2 2>/dev/null || true
  systemctl stop mariadb 2>/dev/null || true
  systemctl disable mariadb 2>/dev/null || true
  systemctl stop mysql 2>/dev/null || true
  systemctl disable mysql 2>/dev/null || true
  apt-get purge -y 'apache2*' 'libapache2-mod-php*' 'mariadb-*' 'mysql-server*' 'mysql-client*' || true
  apt-get autoremove -y || true
fi

log "Installing packages"
apt-get update
apt-get install -y \
  nginx \
  postgresql postgresql-contrib \
  php-fpm php-cli php-pgsql php-xml php-mbstring php-curl php-zip \
  php-gd php-intl php-bcmath php-gmp php-imagick php-apcu \
  unzip curl wget bzip2 ca-certificates acl redis-server php-redis

if [[ -z "${PHP_VERSION}" ]]; then
  PHP_SOCKET="$(find /run/php -maxdepth 1 -type s -name 'php*-fpm.sock' | sort -V | tail -n 1 || true)"
  if [[ -z "${PHP_SOCKET}" ]]; then
    err "Could not find PHP-FPM socket in /run/php. Check php-fpm installation."
    exit 1
  fi
else
  PHP_SOCKET="/run/php/php${PHP_VERSION}-fpm.sock"
  if [[ ! -S "${PHP_SOCKET}" ]]; then
    err "Configured PHP socket does not exist: ${PHP_SOCKET}"
    exit 1
  fi
fi

PHP_FPM_SERVICE="$(basename "${PHP_SOCKET}" .sock)"
log "Using PHP-FPM socket: ${PHP_SOCKET}"

log "Creating PostgreSQL database and user"
sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
DO \$\$
BEGIN
   IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${NEXTCLOUD_DB_USER}') THEN
      CREATE ROLE ${NEXTCLOUD_DB_USER} LOGIN PASSWORD '${NEXTCLOUD_DB_PASS}';
   ELSE
      ALTER ROLE ${NEXTCLOUD_DB_USER} WITH PASSWORD '${NEXTCLOUD_DB_PASS}';
   END IF;
END
\$\$;
SQL

if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='${NEXTCLOUD_DB_NAME}'" | grep -q 1; then
  sudo -u postgres createdb -O "${NEXTCLOUD_DB_USER}" "${NEXTCLOUD_DB_NAME}"
else
  warn "Database ${NEXTCLOUD_DB_NAME} already exists; leaving it in place."
fi

log "Downloading Nextcloud"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
wget -O "${TMP_DIR}/nextcloud.tar.bz2" "${INSTALL_LATEST_URL}"
tar -xjf "${TMP_DIR}/nextcloud.tar.bz2" -C "${TMP_DIR}"

log "Installing files"
mkdir -p "$(dirname "${NEXTCLOUD_DIR}")" "${NEXTCLOUD_DATA_DIR}"
rsync -a "${TMP_DIR}/nextcloud/" "${NEXTCLOUD_DIR}/"
chown -R www-data:www-data "${NEXTCLOUD_DIR}" "${NEXTCLOUD_DATA_DIR}"
chmod 750 "${NEXTCLOUD_DATA_DIR}"

log "Tuning PHP-FPM"
PHP_INI_CANDIDATES=(
  "/etc/php/${PHP_VERSION}/fpm/php.ini"
  "/etc/php/8.4/fpm/php.ini"
  "/etc/php/8.3/fpm/php.ini"
  "/etc/php/8.2/fpm/php.ini"
  "/etc/php/8.1/fpm/php.ini"
)
for ini in "${PHP_INI_CANDIDATES[@]}"; do
  [[ -f "${ini}" ]] || continue
  sed -i 's/^memory_limit = .*/memory_limit = 512M/' "${ini}"
  sed -i 's/^upload_max_filesize = .*/upload_max_filesize = 1024M/' "${ini}"
  sed -i 's/^post_max_size = .*/post_max_size = 1024M/' "${ini}"
  sed -i 's/^max_execution_time = .*/max_execution_time = 360/' "${ini}"
  sed -i 's/^;opcache.enable=.*/opcache.enable=1/' "${ini}" || true
  sed -i 's/^;opcache.interned_strings_buffer=.*/opcache.interned_strings_buffer=32/' "${ini}" || true
  sed -i 's/^;opcache.max_accelerated_files=.*/opcache.max_accelerated_files=10000/' "${ini}" || true
  sed -i 's/^;opcache.memory_consumption=.*/opcache.memory_consumption=128/' "${ini}" || true
  sed -i 's/^;opcache.save_comments=.*/opcache.save_comments=1/' "${ini}" || true
done

systemctl restart "${PHP_FPM_SERVICE}" 2>/dev/null || systemctl restart php*-fpm

log "Writing Nginx site"
NGINX_SITE="/etc/nginx/sites-available/nextcloud.conf"
if [[ -f "${NGINX_SITE}" ]]; then
  cp "${NGINX_SITE}" "${NGINX_SITE}.bak.$(date +%Y%m%d%H%M%S)"
fi

cat > "${NGINX_SITE}" <<NGINX
upstream nextcloud_php_handler {
    server unix:${PHP_SOCKET};
}

server {
    listen 80;
    listen [::]:80;
    server_name ${NEXTCLOUD_DOMAIN};

    root ${NEXTCLOUD_DIR};
    index index.php index.html /index.php\$request_uri;

    client_max_body_size 1024M;
    client_body_timeout 300s;
    fastcgi_buffers 64 4K;

    gzip on;
    gzip_vary on;
    gzip_comp_level 4;
    gzip_min_length 256;
    gzip_proxied expired no-cache no-store private no_last_modified no_etag auth;
    gzip_types application/atom+xml text/javascript application/javascript application/json application/ld+json application/manifest+json application/rss+xml application/vnd.geo+json application/vnd.ms-fontobject application/wasm application/x-font-ttf application/x-web-app-manifest+json application/xhtml+xml application/xml font/opentype image/bmp image/svg+xml image/x-icon text/cache-manifest text/css text/plain text/vcard text/vnd.rim.location.xloc text/vtt text/x-component text/x-cross-domain-policy;

    add_header Referrer-Policy                      "no-referrer"       always;
    add_header X-Content-Type-Options               "nosniff"           always;
    add_header X-Frame-Options                      "SAMEORIGIN"        always;
    add_header X-Permitted-Cross-Domain-Policies    "none"              always;
    add_header X-Robots-Tag                         "noindex, nofollow" always;
    add_header X-XSS-Protection                     "1; mode=block"     always;

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
        fastcgi_param HTTPS off;
        fastcgi_param modHeadersAvailable true;
        fastcgi_param front_controller_active true;
        fastcgi_pass nextcloud_php_handler;
        fastcgi_intercept_errors on;
        fastcgi_request_buffering off;
        fastcgi_max_temp_file_size 0;
    }

    location ~ \.(?:css|js|mjs|svg|gif|png|jpg|ico|wasm|tflite|map|ogg|flac)\$ {
        try_files \$uri /index.php\$request_uri;
        expires 6M;
        access_log off;
        location ~ \.wasm\$ { default_type application/wasm; }
    }

    location ~ \.woff2?\$ {
        try_files \$uri /index.php\$request_uri;
        expires 7d;
        access_log off;
    }

    location / {
        try_files \$uri \$uri/ /index.php\$request_uri;
    }
}
NGINX

ln -sfn "${NGINX_SITE}" /etc/nginx/sites-enabled/nextcloud.conf
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable nginx
systemctl reload nginx

log "Running Nextcloud occ installation"
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" maintenance:install \
  --database "pgsql" \
  --database-host "localhost" \
  --database-name "${NEXTCLOUD_DB_NAME}" \
  --database-user "${NEXTCLOUD_DB_USER}" \
  --database-pass "${NEXTCLOUD_DB_PASS}" \
  --admin-user "${NEXTCLOUD_ADMIN_USER}" \
  --admin-pass "${NEXTCLOUD_ADMIN_PASS}" \
  --data-dir "${NEXTCLOUD_DATA_DIR}"

log "Applying Nextcloud configuration"
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" config:system:set trusted_domains 0 --value="${NEXTCLOUD_DOMAIN}"
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" config:system:set overwrite.cli.url --value="http://${NEXTCLOUD_DOMAIN}"
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" config:system:set default_phone_region --value="DE"
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" config:system:set maintenance_window_start --type=integer --value=1
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" config:system:set memcache.local --value='\\OC\\Memcache\\APCu'
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" background:cron

log "Installing cron job"
cat > /etc/cron.d/nextcloud <<CRON
*/5 * * * * www-data php -f ${NEXTCLOUD_DIR}/cron.php
CRON

systemctl enable postgresql
systemctl restart postgresql
systemctl restart "${PHP_FPM_SERVICE}" 2>/dev/null || systemctl restart php*-fpm
systemctl reload nginx

log "Installation finished"
printf '\nNextcloud URL: http://%s\n' "${NEXTCLOUD_DOMAIN}"
printf 'Install dir:    %s\n' "${NEXTCLOUD_DIR}"
printf 'Data dir:       %s\n' "${NEXTCLOUD_DATA_DIR}"
printf 'Database:       %s / %s\n' "${NEXTCLOUD_DB_NAME}" "${NEXTCLOUD_DB_USER}"
printf '\nNext step for HTTPS, once DNS points here:\n'
printf '  sudo apt install -y certbot python3-certbot-nginx\n'
printf '  sudo certbot --nginx -d %s\n' "${NEXTCLOUD_DOMAIN}"
printf '\nCheck:\n'
printf '  sudo -u www-data php %s/occ status\n' "${NEXTCLOUD_DIR}"
printf '  sudo nginx -t\n'
