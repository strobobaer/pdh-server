#!/usr/bin/env bash
set -Eeuo pipefail

# Configure SMTP for an existing Nextcloud installation.
# No secrets are stored in this repository; pass credentials at runtime.
#
# Example:
#   sudo bash scripts/configure-nextcloud-smtp.sh \
#     --from-address cloud \
#     --from-domain strobl-home.net \
#     --smtp-host smtp.example.net \
#     --smtp-port 587 \
#     --smtp-secure tls \
#     --smtp-user cloud@strobl-home.net \
#     --smtp-pass 'APP_PASSWORD' \
#     --test-user michael

NEXTCLOUD_DIR="/var/www/nextcloud"
FROM_ADDRESS=""
FROM_DOMAIN=""
SMTP_HOST=""
SMTP_PORT="587"
SMTP_SECURE="tls"
SMTP_AUTH="1"
SMTP_USER=""
SMTP_PASS=""
SMTP_NAME=""
TEST_USER=""
PHP_ARGS=(-d apc.enable_cli=1)

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
err() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Configure SMTP for Nextcloud.

Required:
  --from-address NAME       Mail sender local part, e.g. cloud
  --from-domain DOMAIN      Mail sender domain, e.g. strobl-home.net
  --smtp-host HOST          SMTP host
  --smtp-user USER          SMTP username
  --smtp-pass PASSWORD      SMTP password or app password

Optional:
  --nextcloud-dir PATH      Nextcloud dir, default: /var/www/nextcloud
  --smtp-port PORT          Default: 587
  --smtp-secure MODE        tls, ssl or empty. Default: tls
  --smtp-auth 0|1           Default: 1
  --smtp-name NAME          SMTP display name / mode label, optional
  --test-user USER          Send Nextcloud test email to this user
  -h, --help                Show help

Examples:
  # STARTTLS on port 587
  sudo bash scripts/configure-nextcloud-smtp.sh \
    --from-address cloud \
    --from-domain strobl-home.net \
    --smtp-host smtp.example.net \
    --smtp-port 587 \
    --smtp-secure tls \
    --smtp-user cloud@strobl-home.net \
    --smtp-pass 'APP_PASSWORD' \
    --test-user michael

  # SSL on port 465
  sudo bash scripts/configure-nextcloud-smtp.sh \
    --from-address cloud \
    --from-domain strobl-home.net \
    --smtp-host smtp.example.net \
    --smtp-port 465 \
    --smtp-secure ssl \
    --smtp-user cloud@strobl-home.net \
    --smtp-pass 'APP_PASSWORD'
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --from-address) FROM_ADDRESS="${2:-}"; shift 2 ;;
    --from-domain) FROM_DOMAIN="${2:-}"; shift 2 ;;
    --smtp-host) SMTP_HOST="${2:-}"; shift 2 ;;
    --smtp-port) SMTP_PORT="${2:-}"; shift 2 ;;
    --smtp-secure) SMTP_SECURE="${2:-}"; shift 2 ;;
    --smtp-auth) SMTP_AUTH="${2:-}"; shift 2 ;;
    --smtp-user) SMTP_USER="${2:-}"; shift 2 ;;
    --smtp-pass) SMTP_PASS="${2:-}"; shift 2 ;;
    --smtp-name) SMTP_NAME="${2:-}"; shift 2 ;;
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

if [[ -z "${FROM_ADDRESS}" || -z "${FROM_DOMAIN}" || -z "${SMTP_HOST}" || -z "${SMTP_USER}" || -z "${SMTP_PASS}" ]]; then
  err "Missing required argument."
  usage
  exit 2
fi

if [[ "${SMTP_SECURE}" != "tls" && "${SMTP_SECURE}" != "ssl" && -n "${SMTP_SECURE}" ]]; then
  err "--smtp-secure must be tls, ssl or empty"
  exit 2
fi

OCC=(sudo -u www-data php "${PHP_ARGS[@]}" "${NEXTCLOUD_DIR}/occ")

log "Backing up Nextcloud config.php"
cp "${NEXTCLOUD_DIR}/config/config.php" "${NEXTCLOUD_DIR}/config/config.php.bak.smtp.$(date +%Y%m%d%H%M%S)"

log "Configuring SMTP"
"${OCC[@]}" config:system:set mail_smtpmode --value="smtp"
"${OCC[@]}" config:system:set mail_sendmailmode --value="smtp"
"${OCC[@]}" config:system:set mail_from_address --value="${FROM_ADDRESS}"
"${OCC[@]}" config:system:set mail_domain --value="${FROM_DOMAIN}"
"${OCC[@]}" config:system:set mail_smtphost --value="${SMTP_HOST}"
"${OCC[@]}" config:system:set mail_smtpport --type=integer --value="${SMTP_PORT}"
"${OCC[@]}" config:system:set mail_smtpauth --type=boolean --value="${SMTP_AUTH}"
"${OCC[@]}" config:system:set mail_smtpname --value="${SMTP_USER}"
"${OCC[@]}" config:system:set mail_smtppassword --value="${SMTP_PASS}"

if [[ -n "${SMTP_SECURE}" ]]; then
  "${OCC[@]}" config:system:set mail_smtpsecure --value="${SMTP_SECURE}"
else
  "${OCC[@]}" config:system:delete mail_smtpsecure || true
fi

if [[ -n "${SMTP_NAME}" ]]; then
  "${OCC[@]}" config:system:set mail_smtpstreamoptions ssl allow_self_signed --type=boolean --value=false || true
fi

log "Current mail configuration"
"${OCC[@]}" config:system:get mail_smtpmode || true
"${OCC[@]}" config:system:get mail_from_address || true
"${OCC[@]}" config:system:get mail_domain || true
"${OCC[@]}" config:system:get mail_smtphost || true
"${OCC[@]}" config:system:get mail_smtpport || true
"${OCC[@]}" config:system:get mail_smtpsecure || true
"${OCC[@]}" config:system:get mail_smtpauth || true
"${OCC[@]}" config:system:get mail_smtpname || true

if [[ -n "${TEST_USER}" ]]; then
  log "Sending test email to Nextcloud user: ${TEST_USER}"
  "${OCC[@]}" notification:test-push "${TEST_USER}" || true
  "${OCC[@]}" user:setting "${TEST_USER}" settings email || true
  "${OCC[@]}" mail:test "${TEST_USER}" || true
else
  log "No --test-user provided. Test in Nextcloud UI: Administration settings -> Basic settings -> Email server -> Send email."
fi

log "SMTP configuration finished"
printf '\nSender: %s@%s\n' "${FROM_ADDRESS}" "${FROM_DOMAIN}"
printf 'SMTP:   %s:%s secure=%s auth=%s user=%s\n' "${SMTP_HOST}" "${SMTP_PORT}" "${SMTP_SECURE}" "${SMTP_AUTH}" "${SMTP_USER}"
