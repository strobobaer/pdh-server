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
  bash scripts/list-nextcloud-deck-ids.sh

  bash scripts/list-nextcloud-deck-ids.sh --board-id 1

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
  curl -k -fsS \
    -u "${USERNAME}:${APP_PASSWORD}" \
    -H 'Accept: application/json' \
    -H 'OCS-APIRequest: true' \
    "${BASE_URL%/}${path}"
}

print_boards() {
  python3 - <<'PY'
import json, sys
raw = sys.stdin.read()
data = json.loads(raw or '[]')
print('\nBoards:')
print('BOARD_ID\tTITLE')
for board in data:
    print(f"{board.get('id','')}\t{board.get('title','')}")
PY
}

print_stacks() {
  python3 - "$1" <<'PY'
import json, sys
board_id = sys.argv[1]
raw = sys.stdin.read()
data = json.loads(raw or '[]')
print(f'\nStacks/Listen für Board {board_id}:')
print('STACK_ID\tTITLE')
for stack in data:
    print(f"{stack.get('id','')}\t{stack.get('title','')}")
PY
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
  python3 - <<'PY' <<< "${boards_json}" > /tmp/pdh-deck-board-ids.txt
import json, sys
for board in json.load(sys.stdin):
    bid = board.get('id')
    if bid is not None:
        print(bid)
PY
  while IFS= read -r bid; do
    [[ -z "${bid}" ]] && continue
    stacks_json="$(api_get "/index.php/apps/deck/api/v1.0/boards/${bid}/stacks")"
    if [[ "${JSON_ONLY}" == "1" ]]; then
      printf '\n--- board %s stacks ---\n%s\n' "${bid}" "${stacks_json}"
    else
      printf '%s' "${stacks_json}" | print_stacks "${bid}"
    fi
  done < /tmp/pdh-deck-board-ids.txt
  rm -f /tmp/pdh-deck-board-ids.txt
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
