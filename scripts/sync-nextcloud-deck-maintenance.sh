#!/usr/bin/env bash
set -Eeuo pipefail

ENV_FILE="/etc/pdh/pdh.env"
DB_NAME="${PDH_DATABASE_NAME:-pdh}"
LIMIT="25"
DRY_RUN="0"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --db-name) DB_NAME="${2:-}"; shift 2 ;;
    --limit) LIMIT="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN="1"; shift ;;
    -h|--help)
      echo "Usage: sudo bash scripts/sync-nextcloud-deck-maintenance.sh [--dry-run] [--limit N]"
      exit 0 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root." >&2
  exit 1
fi
if [[ ! -r "${ENV_FILE}" ]]; then
  echo "Env file not readable: ${ENV_FILE}" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "${ENV_FILE}"
set +a

export PDH_DECK_BASE_URL="${PDH_NEXTCLOUD_DECK_BASEURL:-${PDH_NEXTCLOUD_BASEURL:-https://cloud.strobl-home.net}}"
export PDH_DECK_USER="${PDH_NEXTCLOUD_DECK_USERNAME:-${PDH_NEXTCLOUD_USERNAME:-}}"
export PDH_DECK_PASS="${PDH_NEXTCLOUD_DECK_PASSWORD:-${PDH_NEXTCLOUD_PASSWORD:-}}"
export PDH_DECK_BOARD_ID="${PDH_NEXTCLOUD_DECK_BOARD_ID:-}"
export PDH_DECK_STACK_ID="${PDH_NEXTCLOUD_DECK_STACK_MAINTENANCE_ID:-}"
export PDH_PUBLIC_URL="${PDH_PUBLIC_URL:-https://pdh.strobl-home.net}"
export PDH_DB_NAME="${DB_NAME}"
export PDH_SYNC_LIMIT="${LIMIT}"
export PDH_SYNC_DRY_RUN="${DRY_RUN}"

if [[ "${PDH_NEXTCLOUD_DECK_ENABLED:-false}" != "true" && "${PDH_NEXTCLOUD_DECK_ENABLED:-0}" != "1" ]]; then
  echo "Deck integration disabled."
  exit 0
fi
if [[ -z "${PDH_DECK_USER}" || -z "${PDH_DECK_PASS}" || -z "${PDH_DECK_BOARD_ID}" || -z "${PDH_DECK_STACK_ID}" ]]; then
  echo "Deck configuration incomplete." >&2
  exit 2
fi

python3 scripts/sync_nextcloud_deck_maintenance.py
