#!/usr/bin/env bash
set -Eeuo pipefail

# Configure Nextcloud SMTP with Apple iCloud Mail parameters.
# Uses:
#   SMTP host: smtp.mail.me.com
#   SMTP port: 587
#   Security:   STARTTLS / tls
#
# Use an Apple app-specific password, not your Apple ID password.

NEXTCLOUD_DIR="/var/www/nextcloud"
FROM_ADDRESS="cloud"
FROM_DOMAIN="strobl-home.net"
ICLOUD_USER=""
ICLOUD_APP_PASS=""
TEST_USER=""

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
err() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Configure Nextcloud SMTP for Apple iCloud Mail.

Required:
  --icloud-user EMAIL          iCloud email/login, e.g. user@icloud.com or user@me.com
  --icloud-app-pass PASSWORD   Apple app-specific password

Optional:
  --from-address NAME          Sender local part shown by Nextcloud, default: cloud
  --from-domain DOMAIN         Sender domain shown by Nextcloud, default: strobl-home.net
  --nextcloud-dir PATH         Nextcloud dir, default: /var/www/nextcloud
  --test-user USER             Run Nextcloud mail:test for this user
  -h, --help                   Show help

Example:
  sudo bash scripts/configure-nextcloud-smtp-icloud.sh \
    --icloud-user 'name@icloud.com' \
    --icloud-app-pass 'xxxx-xxxx-xxxx-xxxx' \
    --from-address cloud \
    --from-domain strobl-home.net \
    --test-user michael
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --icloud-user) ICLOUD_USER="${2:-}"; shift 2 ;;
    --icloud-app-pass) ICLOUD_APP_PASS="${2:-}"; shift 2 ;;
    --from-address) FROM_ADDRESS="${2:-}"; shift 2 ;;
    --from-domain) FROM_DOMAIN="${2:-}"; shift 2 ;;
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --test-user) TEST_USER="${2:-}"; shift 2 ;;
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

if [[ -z "${ICLOUD_USER}" || -z "${ICLOUD_APP_PASS}" ]]; then
  err "Missing --icloud-user or --icloud-app-pass"
  usage
  exit 2
fi

OCC=(sudo -u www-data php -d apc.enable_cli=1 "${NEXTCLOUD_DIR}/occ")

log "Backing up Nextcloud config.php"
cp "${NEXTCLOUD_DIR}/config/config.php" "${NEXTCLOUD_DIR}/config/config.php.bak.icloud-smtp.$(date +%Y%m%d%H%M%S)"

log "Configuring iCloud SMTP"
"${OCC[@]}" config:system:set mail_smtpmode --value="smtp"
"${OCC[@]}" config:system:set mail_sendmailmode --value="smtp"
"${OCC[@]}" config:system:set mail_from_address --value="${FROM_ADDRESS}"
"${OCC[@]}" config:system:set mail_domain --value="${FROM_DOMAIN}"
"${OCC[@]}" config:system:set mail_smtphost --value="smtp.mail.me.com"
"${OCC[@]}" config:system:set mail_smtpport --type=integer --value="587"
"${OCC[@]}" config:system:set mail_smtpsecure --value="tls"
"${OCC[@]}" config:system:set mail_smtpauth --type=boolean --value=true
"${OCC[@]}" config:system:set mail_smtpname --value="${ICLOUD_USER}"
"${OCC[@]}" config:system:set mail_smtppassword --value="${ICLOUD_APP_PASS}"

log "Current visible mail configuration"
"${OCC[@]}" config:system:get mail_smtpmode || true
"${OCC[@]}" config:system:get mail_from_address || true
"${OCC[@]}" config:system:get mail_domain || true
"${OCC[@]}" config:system:get mail_smtphost || true
"${OCC[@]}" config:system:get mail_smtpport || true
"${OCC[@]}" config:system:get mail_smtpsecure || true
"${OCC[@]}" config:system:get mail_smtpname || true

if [[ -n "${TEST_USER}" ]]; then
  log "Running Nextcloud mail test for user: ${TEST_USER}"
  "${OCC[@]}" mail:test "${TEST_USER}"
else
  log "No --test-user supplied. Test in Nextcloud UI: Administration settings -> Basic settings -> Email server -> Send email."
fi

log "iCloud SMTP configuration finished"
printf '\nSMTP host: smtp.mail.me.com\n'
printf 'SMTP port: 587\n'
printf 'Security:  tls / STARTTLS\n'
printf 'SMTP user: %s\n' "${ICLOUD_USER}"
printf 'Sender:    %s@%s\n' "${FROM_ADDRESS}" "${FROM_DOMAIN}"
