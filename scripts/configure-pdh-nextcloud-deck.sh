#!/usr/bin/env bash
set -Eeuo pipefail

# Configure PDH -> Nextcloud Deck integration through /etc/pdh/pdh.env.
# Uses the existing Nextcloud app password unless explicitly overridden.

ENV_FILE="/etc/pdh/pdh.env"
SERVICE="pdh"
BASE_URL="https://cloud.strobl-home.net"
USERNAME=""
APP_PASSWORD=""
BOARD_ID=""
STACK_TICKETS_ID=""
STACK_FAULTS_ID=""
STACK_MAINTENANCE_ID=""
PDH_PUBLIC_URL="https://pdh.strobl-home.net"

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Configure PDH -> Nextcloud Deck integration.

Required:
  --board-id ID
  --tickets-stack-id ID

Optional:
  --faults-stack-id ID
  --maintenance-stack-id ID
  --username USER             Defaults to existing PDH_NEXTCLOUD_USERNAME from env file
  --app-password PASS         Defaults to existing PDH_NEXTCLOUD_PASSWORD from env file
  --base-url URL              Default: https://cloud.strobl-home.net
  --pdh-public-url URL        Default: https://pdh.strobl-home.net
  --env-file PATH             Default: /etc/pdh/pdh.env
  --service NAME              Default: pdh
  -h, --help                  Show help

Example:
  sudo bash scripts/configure-pdh-nextcloud-deck.sh \
    --board-id 1 \
    --tickets-stack-id 3 \
    --faults-stack-id 4 \
    --maintenance-stack-id 5
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --board-id) BOARD_ID="${2:-}"; shift 2 ;;
    --tickets-stack-id) STACK_TICKETS_ID="${2:-}"; shift 2 ;;
    --faults-stack-id) STACK_FAULTS_ID="${2:-}"; shift 2 ;;
    --maintenance-stack-id) STACK_MAINTENANCE_ID="${2:-}"; shift 2 ;;
    --username) USERNAME="${2:-}"; shift 2 ;;
    --app-password) APP_PASSWORD="${2:-}"; shift 2 ;;
    --base-url) BASE_URL="${2:-}"; shift 2 ;;
    --pdh-public-url) PDH_PUBLIC_URL="${2:-}"; shift 2 ;;
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

if [[ -z "${BOARD_ID}" || -z "${STACK_TICKETS_ID}" ]]; then
  err "Missing --board-id or --tickets-stack-id"
  usage
  exit 2
fi

if [[ -f "${ENV_FILE}" ]]; then
  # shellcheck disable=SC1090
  set -a
  source "${ENV_FILE}"
  set +a
fi

USERNAME="${USERNAME:-${PDH_NEXTCLOUD_USERNAME:-}}"
APP_PASSWORD="${APP_PASSWORD:-${PDH_NEXTCLOUD_PASSWORD:-}}"
BASE_URL="${BASE_URL:-${PDH_NEXTCLOUD_BASEURL:-https://cloud.strobl-home.net}}"

if [[ -z "${USERNAME}" || -z "${APP_PASSWORD}" ]]; then
  err "Missing Nextcloud credentials. Pass --username and --app-password or configure PDH_NEXTCLOUD_USERNAME/PASSWORD first."
  exit 2
fi

log "Writing ${ENV_FILE}"
mkdir -p "$(dirname "${ENV_FILE}")"
touch "${ENV_FILE}"
chmod 600 "${ENV_FILE}"
cp "${ENV_FILE}" "${ENV_FILE}.bak.deck.$(date +%Y%m%d%H%M%S)"

tmp="$(mktemp)"
grep -v '^PDH_NEXTCLOUD_DECK_' "${ENV_FILE}" | grep -v '^PDH_PUBLIC_URL=' > "${tmp}" || true
cat >> "${tmp}" <<EOF
PDH_NEXTCLOUD_DECK_ENABLED=true
PDH_NEXTCLOUD_DECK_BASEURL=${BASE_URL}
PDH_NEXTCLOUD_DECK_USERNAME=${USERNAME}
PDH_NEXTCLOUD_DECK_PASSWORD=${APP_PASSWORD}
PDH_NEXTCLOUD_DECK_BOARD_ID=${BOARD_ID}
PDH_NEXTCLOUD_DECK_STACK_TICKETS_ID=${STACK_TICKETS_ID}
PDH_NEXTCLOUD_DECK_STACK_FAULTS_ID=${STACK_FAULTS_ID}
PDH_NEXTCLOUD_DECK_STACK_MAINTENANCE_ID=${STACK_MAINTENANCE_ID}
PDH_PUBLIC_URL=${PDH_PUBLIC_URL}
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

systemctl daemon-reload
systemctl restart "${SERVICE}"
sleep 1

log "Checking PDH"
curl -fsS http://127.0.0.1:8090/health || true

log "Configured"
printf 'Deck enabled: yes\n'
printf 'Board ID: %s\n' "${BOARD_ID}"
printf 'Tickets Stack ID: %s\n' "${STACK_TICKETS_ID}"
printf 'Faults Stack ID: %s\n' "${STACK_FAULTS_ID:-not set}\n"
printf 'Maintenance Stack ID: %s\n' "${STACK_MAINTENANCE_ID:-not set}\n"
