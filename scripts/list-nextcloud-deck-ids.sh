#!/usr/bin/env bash
set -Eeuo pipefail

# List Nextcloud Deck boards and stacks/lists with their IDs.
# Reads credentials from /etc/pdh/pdh.env by default:
#   PDH_NEXTCLOUD_DECK_BASEURL or PDH_NEXTCLOUD_BASEURL
#   PDH_NEXTCLOUD_DECK_USERNAME or PDH_NEXTCLOUD_USERNAME
#   PDH_NEXTCLOUD_DECK_PASSWORD or PDH_NEXTCLOUD_PASSWORD
#
# Output is intended for scripts/configure-pdh-nextcloud-deck.sh.

ENV_FILE="/etc/pdh/pdh.env"
BASE_URL=""
USERNAME=""
APP_PASSWORD=""
BOARD_ID=""
JSON_ONLY="0"

log() { printf '\n==> %s\n' "$*"; }
warn() { printf '\nWARN: %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
List Nextcloud Deck board and stack IDs.

Options:
  --env-file PATH       Default: /etc/pdh/pdh.env
  --base-url URL        Overrides env; e.g. https://cloud.strobl-home.net
  --username USER       Overrides env
  --app-password PASS   Overrides env
  --board-id ID         Show stacks only for this board
  --json                Print raw JSON responses
  -h, --help            Show help

Examples:
  sudo bash scripts/list-nextcloud-deck-ids.sh

  sudo bash scripts/list-nextcloud-deck-ids.sh --board-id 1

  bash scripts/list-nextcloud-deck-ids.sh \
    --base-url https://cloud.strobl-home.net \
    --username michael \
    --app-password 'NEXTCLOUD_APP_PASSWORD'
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --base-url) BASE_URL="${2:-}"; shift 2 ;;
    --username) USERNAME="${2:-}"; shift 2 ;;
    --app-password) APP_PASSWORD="${2:-}"; shift 2 ;;
    --board-id) BOARD_ID="${2:-}"; shift 2 ;;
    --json) JSON_ONLY="1"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ -f "${ENV_FILE}" ]]; then
  if [[ ! -r "${ENV_FILE}" ]]; then
    err "Env file is not readable: ${ENV_FILE}. Run with sudo or pass --username/--app-password."
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

if [[ -z "${BASE_URL}" || -z "${USERNAME}" || -z "${APP_PASSWORD}" ]]; then
  err "Missing Nextcloud credentials. Configure /etc/pdh/pdh.env or pass --username and --app-password."
  exit 2
fi

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "Missing command: $1"
    exit 1
  fi
}

require_cmd curl
require_cmd python3

api_get() {
  local path="$1"
  local body_file code
  body_file="$(mktemp)"
  code="$(curl -k -sS -o "${body_file}" -w '%{http_code}' \
    -u "${USERNAME}:${APP_PASSWORD}" \
    -H 'Accept: application/json' \
    -H 'OCS-APIRequest: true' \
    "${BASE_URL%/}${path}" || true)"
  if [[ "${code}" -lt 200 || "${code}" -ge 300 ]]; then
    err "Deck API request failed: ${path} HTTP ${code}"
    sed 's/^/  /' "${body_file}" | head -n 30 >&2
    rm -f "${body_file}"
    return 1
  fi
  cat "${body_file}"
  rm -f "${body_file}"
}

extract_board_ids() {
  python3 -c '
import json, sys
raw = sys.stdin.read().strip()
if not raw:
    sys.exit(0)
data = json.loads(raw)
if isinstance(data, dict) and "ocs" in data:
    data = data.get("ocs", {}).get("data", [])
if isinstance(data, dict) and "data" in data:
    data = data.get("data", [])
for board in data if isinstance(data, list) else []:
    bid = board.get("id")
    if bid is not None:
        print(bid)
'
}

print_boards() {
  python3 -c '
import json, sys
raw = sys.stdin.read().strip()
data = json.loads(raw or "[]")
if isinstance(data, dict) and "ocs" in data:
    data = data.get("ocs", {}).get("data", [])
if isinstance(data, dict) and "data" in data:
    data = data.get("data", [])
print("\nBoards:")
print("BOARD_ID\tTITLE")
count = 0
for board in data if isinstance(data, list) else []:
    print(f"{board.get("id", "")}\t{board.get("title", "")}")
    count += 1
if count == 0:
    print("-\tKeine Boards gefunden")
'
}

print_stacks() {
  python3 -c '
import json, sys
board_id = sys.argv[1]
raw = sys.stdin.read().strip()
data = json.loads(raw or "[]")
if isinstance(data, dict) and "ocs" in data:
    data = data.get("ocs", {}).get("data", [])
if isinstance(data, dict) and "data" in data:
    data = data.get("data", [])
print(f"\nStacks/Listen fuer Board {board_id}:")
print("STACK_ID\tTITLE")
count = 0
for stack in data if isinstance(data, list) else []:
    print(f"{stack.get("id", "")}\t{stack.get("title", "")}")
    count += 1
if count == 0:
    print("-\tKeine Stacks/Listen gefunden")
' "$1"
}

log "Nextcloud Deck API"
printf 'Base URL: %s\n' "${BASE_URL}"
printf 'User: %s\n' "${USERNAME}"

if [[ -z "${BOARD_ID}" ]]; then
  log "Listing boards"
  boards_json="$(api_get '/index.php/apps/deck/api/v1.0/boards')"
  if [[ "${JSON_ONLY}" == "1" ]]; then
    printf '%s\n' "${boards_json}"
  else
    printf '%s' "${boards_json}" | print_boards
  fi

  log "Listing stacks for all boards"
  board_ids="$(printf '%s' "${boards_json}" | extract_board_ids)"
  if [[ -z "${board_ids}" ]]; then
    warn "No Deck boards returned. Check that the Deck app is enabled and that user ${USERNAME} has access to at least one board."
  fi
  while IFS= read -r bid; do
    [[ -z "${bid}" ]] && continue
    stacks_json="$(api_get "/index.php/apps/deck/api/v1.0/boards/${bid}/stacks")"
    if [[ "${JSON_ONLY}" == "1" ]]; then
      printf '\n--- board %s stacks ---\n%s\n' "${bid}" "${stacks_json}"
    else
      printf '%s' "${stacks_json}" | print_stacks "${bid}"
    fi
  done <<< "${board_ids}"
else
  log "Listing stacks for board ${BOARD_ID}"
  stacks_json="$(api_get "/index.php/apps/deck/api/v1.0/boards/${BOARD_ID}/stacks")"
  if [[ "${JSON_ONLY}" == "1" ]]; then
    printf '%s\n' "${stacks_json}"
  else
    printf '%s' "${stacks_json}" | print_stacks "${BOARD_ID}"
  fi
fi

log "Use these IDs with configure-pdh-nextcloud-deck.sh"
cat <<'NEXT'
Example:
  sudo bash scripts/configure-pdh-nextcloud-deck.sh \
    --board-id BOARD_ID \
    --tickets-stack-id STACK_ID_TICKETS \
    --faults-stack-id STACK_ID_STOERUNGEN \
    --maintenance-stack-id STACK_ID_WARTUNG
NEXT
