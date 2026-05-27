#!/usr/bin/env bash
set -Eeuo pipefail

# Configure shared HMAC secret for Nextcloud App -> PDH SSO launch.
# Writes:
#   /etc/pdh/pdh.env: PDH_NEXTCLOUD_SSO_SECRET=...
#   Nextcloud app config: pdh sso_secret=...
#
# No secret is printed.

ENV_FILE="/etc/pdh/pdh.env"
NEXTCLOUD_DIR="/var/www/nextcloud"
SERVICE="pdh"
SECRET=""
GROUP_ID="pdh"
PDH_PUBLIC_URL="https://pdh.strobl-home.net"

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Configure PDH Nextcloud SSO shared secret.

Options:
  --secret VALUE            Optional. Generated when omitted.
  --group ID                Default: pdh
  --pdh-public-url URL      Default: https://pdh.strobl-home.net
  --env-file PATH           Default: /etc/pdh/pdh.env
  --nextcloud-dir PATH      Default: /var/www/nextcloud
  --service NAME            Default: pdh
  -h, --help                Show help

Example:
  sudo bash scripts/configure-pdh-nextcloud-sso.sh
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --secret) SECRET="${2:-}"; shift 2 ;;
    --group) GROUP_ID="${2:-}"; shift 2 ;;
    --pdh-public-url) PDH_PUBLIC_URL="${2:-}"; shift 2 ;;
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --service) SERVICE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  err "Run as root."
  exit 1
fi

if [[ -z "${SECRET}" ]]; then
  SECRET="$(openssl rand -hex 32)"
fi

if [[ ! -f "${NEXTCLOUD_DIR}/occ" ]]; then
  err "Nextcloud occ not found: ${NEXTCLOUD_DIR}/occ"
  exit 1
fi

log "Writing PDH env secret"
mkdir -p "$(dirname "${ENV_FILE}")"
touch "${ENV_FILE}"
chmod 600 "${ENV_FILE}"
cp "${ENV_FILE}" "${ENV_FILE}.bak.sso.$(date +%Y%m%d%H%M%S)"

tmp="$(mktemp)"
grep -v '^PDH_NEXTCLOUD_SSO_SECRET=' "${ENV_FILE}" > "${tmp}" || true
cat >> "${tmp}" <<EOF
PDH_NEXTCLOUD_SSO_SECRET=${SECRET}
EOF
cat "${tmp}" > "${ENV_FILE}"
rm -f "${tmp}"
chmod 600 "${ENV_FILE}"

log "Ensuring systemd env drop-in"
mkdir -p "/etc/systemd/system/${SERVICE}.service.d"
cat > "/etc/systemd/system/${SERVICE}.service.d/20-nextcloud-env.conf" <<EOF
[Service]
EnvironmentFile=-${ENV_FILE}
EOF

log "Writing Nextcloud app config"
OCC=(sudo -u www-data php -d apc.enable_cli=1 "${NEXTCLOUD_DIR}/occ")
"${OCC[@]}" config:app:set pdh sso_secret --value="${SECRET}" >/dev/null
"${OCC[@]}" config:app:set pdh sso_group --value="${GROUP_ID}" >/dev/null
"${OCC[@]}" config:app:set pdh public_url --value="${PDH_PUBLIC_URL}" >/dev/null

log "Reloading services"
systemctl daemon-reload
systemctl restart "${SERVICE}"

log "Checking"
sleep 1
curl -fsS http://127.0.0.1:8090/health || true
"${OCC[@]}" config:app:get pdh sso_group || true
"${OCC[@]}" config:app:get pdh public_url || true

log "Configured"
printf 'SSO secret configured for PDH and Nextcloud app. Secret not printed.\n'
printf 'Launch URL: https://cloud.strobl-home.net/apps/pdh/\n'
