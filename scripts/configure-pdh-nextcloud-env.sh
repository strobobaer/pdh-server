#!/usr/bin/env bash
set -Eeuo pipefail

# Configure PDH -> Nextcloud WebDAV integration through /etc/pdh/pdh.env.
# No secrets are stored in git. Pass an app password at runtime.

ENV_FILE="/etc/pdh/pdh.env"
BASE_URL="https://cloud.strobl-home.net"
USERNAME=""
APP_PASSWORD=""
ROOT_PATH="PDH"
SERVICE="pdh"

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Configure PDH Nextcloud WebDAV integration.

Required:
  --username USER          Nextcloud user, e.g. michael
  --app-password PASS      Nextcloud app password

Optional:
  --base-url URL           Default: https://cloud.strobl-home.net
  --root-path PATH         Default: PDH
  --env-file PATH          Default: /etc/pdh/pdh.env
  --service NAME           Default: pdh
  -h, --help               Show help

Example:
  sudo bash scripts/configure-pdh-nextcloud-env.sh \
    --username michael \
    --app-password 'NEXTCLOUD_APP_PASSWORD' \
    --base-url https://cloud.strobl-home.net \
    --root-path PDH
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --username) USERNAME="${2:-}"; shift 2 ;;
    --app-password) APP_PASSWORD="${2:-}"; shift 2 ;;
    --base-url) BASE_URL="${2:-}"; shift 2 ;;
    --root-path) ROOT_PATH="${2:-}"; shift 2 ;;
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --service) SERVICE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  err "Run as root."
  exit 1
fi

if [[ -z "${USERNAME}" || -z "${APP_PASSWORD}" ]]; then
  err "Missing --username or --app-password"
  usage
  exit 2
fi

log "Writing ${ENV_FILE}"
mkdir -p "$(dirname "${ENV_FILE}")"
touch "${ENV_FILE}"
chmod 600 "${ENV_FILE}"

cp "${ENV_FILE}" "${ENV_FILE}.bak.nextcloud.$(date +%Y%m%d%H%M%S)"

tmp="$(mktemp)"
grep -v '^PDH_NEXTCLOUD_' "${ENV_FILE}" > "${tmp}" || true
cat >> "${tmp}" <<EOF
PDH_NEXTCLOUD_ENABLED=true
PDH_NEXTCLOUD_BASEURL=${BASE_URL}
PDH_NEXTCLOUD_USERNAME=${USERNAME}
PDH_NEXTCLOUD_PASSWORD=${APP_PASSWORD}
PDH_NEXTCLOUD_ROOTPATH=${ROOT_PATH}
EOF
cat "${tmp}" > "${ENV_FILE}"
rm -f "${tmp}"
chmod 600 "${ENV_FILE}"

log "Ensuring systemd reads env file"
mkdir -p "/etc/systemd/system/${SERVICE}.service.d"
cat > "/etc/systemd/system/${SERVICE}.service.d/20-nextcloud-env.conf" <<EOF
[Service]
EnvironmentFile=-${ENV_FILE}
EOF

systemctl daemon-reload
systemctl restart "${SERVICE}"

log "Checking PDH"
systemctl status "${SERVICE}" --no-pager -l || true
curl -fsS http://127.0.0.1:8090/health || true

log "Configured"
printf 'Nextcloud WebDAV base: %s\n' "${BASE_URL}"
printf 'Root path: %s\n' "${ROOT_PATH}"
