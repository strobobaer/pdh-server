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
RESTORE="0"
VERBUND=""
UID_WWW_DATA=""
UID_PDH=""
AKTIV_PHP=""
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
  --restore                   Restore mode (pdh-restore.sh): files and database are
                              already back; install packages, tune PHP, adapt the
                              Nginx site and leave maintenance mode – no new install,
                              no database/user creation, no --domain/--*-pass needed
  --verbund MODE              Cluster mode (needs pdh-ha, file mirroring between the
                              two servers). Leaves PostgreSQL alone: no restart, no
                              autostart. MODE is one of:
                                aktiv-neu        new install on the ACTIVE server
                                aktiv-vorhanden  existing install on the ACTIVE server,
                                                 checks only, writes nothing
                                passiv           PASSIVE server: packages, PHP, nginx,
                                                 empty folders; files arrive by mirroring
                              At the end it prints the command for the other server.
  --uid-www-data UID          (--verbund passiv) uid of www-data on the active server
  --uid-pdh UID               (--verbund passiv) uid of pdh on the active server
  --aktiv-php VERSION         (--verbund passiv) PHP version of the active server
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
    --restore) RESTORE="1"; shift ;;
    --verbund) VERBUND="${2:-}"; shift 2 ;;
    --uid-www-data) UID_WWW_DATA="${2:-}"; shift 2 ;;
    --uid-pdh) UID_PDH="${2:-}"; shift 2 ;;
    --aktiv-php) AKTIV_PHP="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

# PHP-FPM tuning (php.ini of every installed PHP version we know).
tune_php_ini() {
  local ini
  local candidates=(
    "/etc/php/${PHP_VERSION}/fpm/php.ini"
    "/etc/php/8.4/fpm/php.ini"
    "/etc/php/8.3/fpm/php.ini"
    "/etc/php/8.2/fpm/php.ini"
    "/etc/php/8.1/fpm/php.ini"
  )
  for ini in "${candidates[@]}"; do
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
}

# ---- Cluster mode (--verbund) ------------------------------------------------
# Exit 0 if pg_hba.conf $1 lets role $3 into database $2 from this server
# (TCP to localhost or loopback: Nextcloud connects with --database-host localhost,
# so a "local" socket line does not count). Only a hint: the first matching line
# wins in PostgreSQL, we do not model that.
hba_erlaubt() {
  awk -v d="$2" -v u="$3" '
    function has(list, w,   n, a, i) {
      n = split(list, a, ",")
      for (i = 1; i <= n; i++) if (a[i] == "all" || a[i] == w) return 1
      return 0
    }
    $1 ~ /^host(ssl|nossl)?$/ && $NF != "reject" && $4 ~ /^(127\.|::1|localhost|samehost|0\.0\.0\.0\/0|::\/0)/ \
      && has($2, d) && has($3, u) { found = 1 }
    END { exit found ? 0 : 1 }
  ' "$1"
}

# Own role from the compact pdh-ha status JSON (field self.role, not peers).
verbund_rolle() {
  printf '%s' "$1" | sed -nE 's/.*"self":\{[^}]*"role":"([^"]*)".*/\1/p'
}

# Checks shared by all cluster modes: pdh-ha present, role matches the mode, same
# ids for www-data and pdh as on the first server, pg_hba.conf hint.
# $1 = aktiv | passiv
verbund_pruefen() {
  local want="$1" js role uw up hba found=0
  if ! command -v pdh-ha >/dev/null 2>&1; then
    err "--verbund needs the cluster (pdh-ha), which is not installed on this server."
    exit 1
  fi
  js="$(pdh-ha status --json 2>/dev/null | tr -d ' \n\r\t' || true)"
  if [[ "${js}" != *'"configured":true'* ]]; then
    err "--verbund: this server is not part of a cluster yet (pdh-ha status --json says not configured)."
    exit 1
  fi
  role="$(verbund_rolle "${js}")"
  case "${want}" in
    aktiv) [[ "${role}" == "aktiv" ]] || { err "--verbund ${VERBUND} runs on the ACTIVE server, this one is '${role:-unknown}'. Run it on the active server."; exit 1; } ;;
    passiv) [[ "${role}" == "passiv" ]] || { err "--verbund passiv runs on the PASSIVE server, this one is '${role:-unknown}'. Run it on the passive server."; exit 1; } ;;
  esac

  uw="$(id -u www-data 2>/dev/null || true)"
  up="$(id -u pdh 2>/dev/null || true)"
  if [[ "${want}" == "passiv" && -z "${UID_WWW_DATA}" ]]; then
    err "--uid-www-data (and --uid-pdh) are missing. Read them on the first server (id -u www-data, id -u pdh) or copy the full command printed there."
    exit 1
  fi
  if [[ -n "${UID_WWW_DATA}" && "${UID_WWW_DATA}" != "${uw}" ]]; then
    err "www-data has uid '${uw:-none}' here, but ${UID_WWW_DATA} on the first server. The mirror copies owners by number: align the uid first (usermod -u ${UID_WWW_DATA} www-data)."
    exit 1
  fi
  if [[ -n "${UID_PDH}" && "${UID_PDH}" != "${up}" ]]; then
    err "pdh has uid '${up:-none}' here, but ${UID_PDH} on the first server. The mirror copies owners by number: align the uid first."
    exit 1
  fi

  for hba in /etc/postgresql/*/*/pg_hba.conf; do
    [[ -f "${hba}" ]] || continue
    found=1
    if ! hba_erlaubt "${hba}" "${NEXTCLOUD_DB_NAME}" "${NEXTCLOUD_DB_USER}"; then
      warn "${hba} does not let role '${NEXTCLOUD_DB_USER}' into '${NEXTCLOUD_DB_NAME}' locally (socket or 127.0.0.1). Nextcloud could not log in after a takeover. This script does not change it."
    fi
  done
  (( found )) || warn "No pg_hba.conf under /etc/postgresql found – pg_hba not checked."
}

# What to do next on the other server, as a complete command.
verbund_weiter() {
  local uw up phpv cmd
  uw="$(id -u www-data 2>/dev/null || true)"
  up="$(id -u pdh 2>/dev/null || true)"
  phpv="$(php -r 'echo PHP_MAJOR_VERSION . "." . PHP_MINOR_VERSION;' 2>/dev/null || true)"
  cmd="sudo bash scripts/install-nextcloud-nginx-postgres.sh --verbund passiv --install-dir ${NEXTCLOUD_DIR} --data-dir ${NEXTCLOUD_DATA_DIR} --uid-www-data ${uw}"
  [[ -z "${up}" ]] || cmd+=" --uid-pdh ${up}"
  [[ -z "${phpv}" ]] || cmd+=" --aktiv-php ${phpv}"
  log "Next steps on the OTHER (passive) server"
  printf '  uid www-data: %s   uid pdh: %s   PHP: %s\n' "${uw:-?}" "${up:-none}" "${phpv:-?}"
  printf '  1. The Nextcloud data folder %s must exist there (mount the disk first if it is its own volume).\n' "${NEXTCLOUD_DATA_DIR}"
  printf '  2. Run on the passive server, from the PDH folder:\n\n     %s\n\n' "${cmd}"
  printf '  3. In the cluster settings (PDH) switch on file mirroring and "take Nextcloud along".\n'
  printf '     The first full sync fills %s and %s on the passive server.\n' "${NEXTCLOUD_DIR}" "${NEXTCLOUD_DATA_DIR}"
}

# Existing Nextcloud on the active server: checks only, nothing is changed.
verbund_vorhanden() {
  verbund_pruefen aktiv
  if [[ ! -e "${NEXTCLOUD_DIR}/config/config.php" ]]; then
    err "--verbund aktiv-vorhanden: ${NEXTCLOUD_DIR}/config/config.php is missing – there is no Nextcloud to check here (new install: --verbund aktiv-neu)."
    exit 1
  fi
  if [[ ! -e "${NEXTCLOUD_DIR}/cron.php" ]]; then
    warn "${NEXTCLOUD_DIR}/cron.php is missing – is --install-dir right?"
  fi
  if [[ ! -d "${NEXTCLOUD_DATA_DIR}" ]]; then
    warn "Data folder ${NEXTCLOUD_DATA_DIR} does not exist – is --data-dir right?"
  fi
  log "Checks passed, nothing was changed"
  verbund_weiter
}

# Stop and disable every installed PHP-FPM version (pdh-ha starts it on the
# active server only).
php_fpm_aus() {
  local d v
  for d in /etc/php/*/fpm; do
    [[ -d "${d}" ]] || continue
    v="$(basename "$(dirname "${d}")")"
    systemctl disable --now "php${v}-fpm" 2>/dev/null || true
  done
}

# Passive server cleanup, also on abort (trap): PHP-FPM off, policy-rc.d back as it was.
passiv_aufraeumen() {
  php_fpm_aus
  if [[ -n "${PASSIV_RC_SAVE:-}" && -e "${PASSIV_RC_SAVE}" ]]; then
    mv -f "${PASSIV_RC_SAVE}" /usr/sbin/policy-rc.d
  elif [[ "${PASSIV_RC_SET:-0}" == "1" ]]; then
    rm -f /usr/sbin/policy-rc.d
  fi
  PASSIV_RC_SAVE=""
  PASSIV_RC_SET=0
}

# Passive server: packages, PHP and nginx only. No database access, no Nextcloud
# files (they arrive by mirroring), PHP-FPM stopped and without autostart.
verbund_passiv() {
  verbund_pruefen passiv
  export DEBIAN_FRONTEND=noninteractive
  # The packages must not start or enable services on a passive server: for the
  # duration of the install policy-rc.d forbids every start (exit 101). An existing
  # policy-rc.d is saved and put back.
  PASSIV_RC_SAVE=""
  PASSIV_RC_SET=0
  trap passiv_aufraeumen EXIT
  # A file with our marker line is a leftover of an aborted run (kill, power loss):
  # not saved, simply replaced and removed again at the end.
  if [[ -e /usr/sbin/policy-rc.d ]] && ! grep -q '^# pdh-nextcloud-verbund-policy' /usr/sbin/policy-rc.d; then
    local rcsave
    rcsave="$(mktemp /var/tmp/policy-rc.d.XXXXXX)"
    cp -p /usr/sbin/policy-rc.d "${rcsave}"
    PASSIV_RC_SAVE="${rcsave}"
  fi
  printf '#!/bin/sh\n# pdh-nextcloud-verbund-policy (temporary, removed after the install)\nexit 101\n' > /usr/sbin/policy-rc.d
  chmod 755 /usr/sbin/policy-rc.d
  PASSIV_RC_SET=1
  log "Installing packages (passive server: no database)"
  apt-get update
  apt-get install -y \
    nginx rsync \
    php-fpm php-cli php-pgsql php-xml php-mbstring php-curl php-zip \
    php-gd php-intl php-bcmath php-gmp php-imagick php-apcu \
    unzip curl wget bzip2 ca-certificates acl redis-server php-redis

  # The package scripts may still have enabled PHP-FPM: off before any check.
  php_fpm_aus

  if [[ -z "${PHP_VERSION}" ]]; then
    PHP_VERSION="$(find /etc/php -mindepth 2 -maxdepth 2 -type d -name fpm 2>/dev/null | sed -E 's#/etc/php/([^/]+)/fpm#\1#' | sort -V | tail -n 1)"
  fi
  if [[ -z "${PHP_VERSION}" ]]; then
    err "No PHP-FPM found under /etc/php. Check the php-fpm installation."
    exit 1
  fi
  PHP_FPM_SERVICE="php${PHP_VERSION}-fpm"
  PHP_SOCKET="/run/php/${PHP_FPM_SERVICE}.sock"
  if [[ -n "${AKTIV_PHP}" && "${AKTIV_PHP}" != "${PHP_VERSION}" ]]; then
    err "PHP ${PHP_VERSION} here, but ${AKTIV_PHP} on the active server. Nextcloud must run on the same PHP after a takeover: install the same version (--php-version ${AKTIV_PHP})."
    exit 1
  fi

  log "Tuning PHP-FPM"
  tune_php_ini

  log "Preparing empty folders for the mirror"
  mkdir -p "${NEXTCLOUD_DIR}" "${NEXTCLOUD_DATA_DIR}"
  chown www-data:www-data "${NEXTCLOUD_DIR}" "${NEXTCLOUD_DATA_DIR}"
  chmod 750 "${NEXTCLOUD_DATA_DIR}"

  log "Cron job: kept as a backup for pdh-ha, not active"
  mkdir -p "$(dirname /var/lib/pdh-ha/nextcloud.cron)"
  printf '*/5 * * * * www-data php -f %s/cron.php\n' "${NEXTCLOUD_DIR}" > /var/lib/pdh-ha/nextcloud.cron
  chmod 600 /var/lib/pdh-ha/nextcloud.cron
  # An active job would run cron.php against the read-only database.
  rm -f /etc/cron.d/nextcloud

  local site="/etc/nginx/sites-available/nextcloud.conf"
  if [[ -f "${site}" ]]; then
    log "Nginx site present: adapting the PHP-FPM socket"
    sed -i -E "s#unix:/run/php/php[0-9.]*-fpm\.sock#unix:${PHP_SOCKET}#" "${site}"
    ln -sfn "${site}" /etc/nginx/sites-enabled/nextcloud.conf
    nginx -t
  else
    log "No Nginx site file – nginx stays as it is"
  fi

  # Install done: policy-rc.d back, then start what the policy held back.
  # nginx and Redis may run on the passive server.
  passiv_aufraeumen
  trap - EXIT
  systemctl reload-or-restart nginx || warn "nginx could not be started or reloaded – check: systemctl status nginx"
  systemctl start redis-server || warn "redis-server could not be started – check: systemctl status redis-server"

  log "Passive server ready"
  printf '\nFolders: %s and %s (empty, owner www-data) – file mirroring fills them.\n' "${NEXTCLOUD_DIR}" "${NEXTCLOUD_DATA_DIR}"
  printf 'PHP-FPM (%s) is stopped and not started at boot; pdh-ha starts it on takeover.\n' "${PHP_FPM_SERVICE}"
  printf 'Next: switch on file mirroring and "take Nextcloud along" in the cluster settings (PDH).\n'
}

case "${VERBUND}" in
  ""|aktiv-neu|aktiv-vorhanden|passiv) ;;
  *) err "--verbund: unknown mode '${VERBUND}' (aktiv-neu, aktiv-vorhanden or passiv)."; exit 2 ;;
esac
if [[ -n "${VERBUND}" && "${RESTORE}" == "1" ]]; then
  err "--verbund cannot be combined with --restore."
  exit 2
fi

if [[ "${EUID}" -ne 0 ]]; then
  err "Run this script as root, e.g. with sudo."
  exit 1
fi

case "${VERBUND}" in
  passiv) verbund_passiv; exit 0 ;;
  aktiv-vorhanden) verbund_vorhanden; exit 0 ;;
  aktiv-neu)
    verbund_pruefen aktiv
    if ! pg_isready -q; then
      err "--verbund aktiv-neu: PostgreSQL does not answer. It is running on the active server; this script does not start it."
      exit 1
    fi
    ;;
esac

if [[ "${RESTORE}" == "1" ]]; then
  if [[ ! -e "${NEXTCLOUD_DIR}/config/config.php" ]]; then
    err "--restore: ${NEXTCLOUD_DIR}/config/config.php is missing – restore the files first."
    exit 1
  fi
else
  if [[ -z "${NEXTCLOUD_DOMAIN}" || -z "${NEXTCLOUD_ADMIN_PASS}" || -z "${NEXTCLOUD_DB_PASS}" ]]; then
    err "Missing required argument."
    usage
    exit 2
  fi
  if [[ -e "${NEXTCLOUD_DIR}/config/config.php" ]]; then
    err "Nextcloud already appears to be installed at ${NEXTCLOUD_DIR}. Aborting to avoid overwrite."
    exit 1
  fi
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
# Wiederherstellung: Postgres ist schon in der passenden Version da – das
# Metapaket könnte sonst eine zweite Hauptversion samt Cluster mitbringen.
PG_PACKAGES=(postgresql postgresql-contrib)
[[ "${RESTORE}" == "1" ]] && PG_PACKAGES=()
# Verbund: Postgres gehört pdh-ha und läuft schon – nichts nachinstallieren.
[[ -n "${VERBUND}" ]] && PG_PACKAGES=()
apt-get update
apt-get install -y \
  nginx \
  "${PG_PACKAGES[@]}" \
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

if [[ "${RESTORE}" != "1" ]]; then
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
fi

if [[ "${RESTORE}" != "1" ]]; then
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
else
log "Restore: setting file owners"
mkdir -p "${NEXTCLOUD_DATA_DIR}"
chown -R www-data:www-data "${NEXTCLOUD_DIR}" "${NEXTCLOUD_DATA_DIR}"
chmod 750 "${NEXTCLOUD_DATA_DIR}"
fi

log "Tuning PHP-FPM"
tune_php_ini

systemctl restart "${PHP_FPM_SERVICE}" 2>/dev/null || systemctl restart php*-fpm

NGINX_SITE="/etc/nginx/sites-available/nextcloud.conf"
if [[ "${RESTORE}" == "1" && -f "${NGINX_SITE}" ]]; then
log "Restore: keeping Nginx site, adapting PHP-FPM socket"
sed -i -E "s#unix:/run/php/php[0-9.]*-fpm\.sock#unix:${PHP_SOCKET}#" "${NGINX_SITE}"
else
log "Writing Nginx site"
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
fi

ln -sfn "${NGINX_SITE}" /etc/nginx/sites-enabled/nextcloud.conf
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable nginx
systemctl reload nginx

if [[ "${RESTORE}" != "1" ]]; then
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
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" config:system:set memcache.local --value='\OC\Memcache\APCu'
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" background:cron
else
log "Restore: leaving maintenance mode"
# Datenbank und Dateien stammen aus verschiedenen Zeitpunkten – Clients neu abgleichen lassen
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" maintenance:data-fingerprint
sudo -u www-data php "${NEXTCLOUD_DIR}/occ" maintenance:mode --off
fi

log "Installing cron job"
cat > /etc/cron.d/nextcloud <<CRON
*/5 * * * * www-data php -f ${NEXTCLOUD_DIR}/cron.php
CRON

if [[ -z "${VERBUND}" ]]; then
  systemctl enable postgresql
  [[ "${RESTORE}" == "1" ]] || systemctl restart postgresql
fi
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

if [[ -n "${VERBUND}" ]]; then
  verbund_weiter
fi
