#!/usr/bin/env bash
set -Eeuo pipefail

# Create a test card in Nextcloud Deck using the same env config as PDH.
# This isolates Deck/API/credential problems from the PDH ticket create flow.

ENV_FILE="/etc/pdh/pdh.env"
BASE_URL=""
USERNAME=""
APP_PASSWORD=""
BOARD_ID=""
STACK_ID=""
TITLE="PDH Deck Integration Test"
DESCRIPTION="Testkarte aus scripts/test-nextcloud-deck-card.sh"

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Create a test card in Nextcloud Deck.

Options:
  --env-file PATH       Default: /etc/pdh/pdh.env
  --base-url URL        Overrides env
  --username USER       Overrides env
  --app-password PASS   Overrides env
  --board-id ID         Overrides env PDH_NEXTCLOUD_DECK_BOARD_ID
  --stack-id ID         Overrides env PDH_NEXTCLOUD_DECK_STACK_TICKETS_ID
  --title TEXT          Default: PDH Deck Integration Test
  --description TEXT    Default: Testkarte aus script
  -h, --help            Show help

Example:
  sudo bash scripts/test-nextcloud-deck-card.sh \
    --board-id 2 \
    --stack-id 5
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --base-url) BASE_URL="${2:-}"; shift 2 ;;
    --username) USERNAME="${2:-}"; shift 2 ;;
    --app-password) APP_PASSWORD="${2:-}"; shift 2 ;;
    --board-id) BOARD_ID="${2:-}"; shift 2 ;;
    --stack-id) STACK_ID="${2:-}"; shift 2 ;;
    --title) TITLE="${2:-}"; shift 2 ;;
    --description) DESCRIPTION="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ -f "${ENV_FILE}" ]]; then
  if [[ ! -r "${ENV_FILE}" ]]; then
    err "Env file is not readable: ${ENV_FILE}. Run with sudo or pass credentials explicitly."
    exit 1
  fi
  # shellcheck disable=SC1090
  set -a
  source "${ENV_FILE}"
  set +a
fi

BASE_URL="${BASE_URL:-${PDH_NEXTCLOUD_DECK_BASEURL:-${PDH_NEXTCLOUD_BASEURL:-https://cloud.strobl-home.net}}}"
USERNAME="${USERNAME:-${PDH_NEXTCLOUD_DECK_USERNAME:-${PDH_NEXTCLOUD_USERNAME:-}}}"
APP_PASSWORD="${APP_PASSWORD:-${PDH_NEXTCLOUD_DECK_PASSWORD:-${PDH_NEXTCLOUD_PASSWORD:-}}}"
BOARD_ID="${BOARD_ID:-${PDH_NEXTCLOUD_DECK_BOARD_ID:-}}"
STACK_ID="${STACK_ID:-${PDH_NEXTCLOUD_DECK_STACK_TICKETS_ID:-}}"

if [[ -z "${BASE_URL}" || -z "${USERNAME}" || -z "${APP_PASSWORD}" || -z "${BOARD_ID}" || -z "${STACK_ID}" ]]; then
  err "Missing config. Need base url, username, password, board id and stack id."
  exit 2
fi

if ! command -v python3 >/dev/null 2>&1; then
  err "Missing python3"
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  err "Missing curl"
  exit 1
fi

payload="$(python3 - "$TITLE" "$DESCRIPTION" <<'PY'
import json, sys
print(json.dumps({"title": sys.argv[1], "description": sys.argv[2]}, ensure_ascii=False))
PY
)"

url="${BASE_URL%/}/index.php/apps/deck/api/v1.0/boards/${BOARD_ID}/stacks/${STACK_ID}/cards"
body_file="$(mktemp)"
trap 'rm -f "${body_file}"' EXIT

log "Creating Deck test card"
printf 'URL: %s\n' "${url}"
printf 'User: %s\n' "${USERNAME}"
printf 'Board ID: %s\n' "${BOARD_ID}"
printf 'Stack ID: %s\n' "${STACK_ID}"

code="$(curl -k -sS -o "${body_file}" -w '%{http_code}' \
  -u "${USERNAME}:${APP_PASSWORD}" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json' \
  -H 'OCS-APIRequest: true' \
  -X POST \
  --data "${payload}" \
  "${url}" || true)"

printf 'HTTP: %s\n' "${code}"
cat "${body_file}"
printf '\n'

if [[ "${code}" -ge 200 && "${code}" -lt 300 ]]; then
  log "Deck card creation succeeded"
else
  err "Deck card creation failed"
  exit 1
fi
