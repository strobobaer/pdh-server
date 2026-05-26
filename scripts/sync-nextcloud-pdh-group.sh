#!/usr/bin/env bash
set -Eeuo pipefail

# Create/check the Nextcloud group used for PDH access and list members.
# This script is intentionally conservative: it does not modify PDH users yet.
# Next step is importing members of this group into the PDH users table.

NEXTCLOUD_DIR="/var/www/nextcloud"
GROUP_ID="pdh"
ADD_USERS=""
LIST_ONLY="0"

log() { printf '\n==> %s\n' "$*"; }
warn() { printf '\nWARN: %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Create/check the Nextcloud group for PDH users.

Options:
  --nextcloud-dir PATH     Default: /var/www/nextcloud
  --group ID               Default: pdh
  --add-users CSV          Comma-separated Nextcloud user IDs to add to the group
  --list-only              Do not create group or add users; only list
  -h, --help               Show help

Examples:
  sudo bash scripts/sync-nextcloud-pdh-group.sh

  sudo bash scripts/sync-nextcloud-pdh-group.sh \
    --add-users michael,techniker1

  sudo bash scripts/sync-nextcloud-pdh-group.sh \
    --group pdh \
    --list-only
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --group) GROUP_ID="${2:-}"; shift 2 ;;
    --add-users) ADD_USERS="${2:-}"; shift 2 ;;
    --list-only) LIST_ONLY="1"; shift ;;
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

OCC=(sudo -u www-data php -d apc.enable_cli=1 "${NEXTCLOUD_DIR}/occ")

log "Nextcloud status"
"${OCC[@]}" status

if [[ "${LIST_ONLY}" != "1" ]]; then
  log "Ensuring Nextcloud group exists: ${GROUP_ID}"
  if "${OCC[@]}" group:list | grep -Eq "^[[:space:]]*-[[:space:]]*${GROUP_ID}(:|$)|^[[:space:]]*${GROUP_ID}(:|$)"; then
    log "Group already exists: ${GROUP_ID}"
  else
    "${OCC[@]}" group:add "${GROUP_ID}"
  fi

  if [[ -n "${ADD_USERS}" ]]; then
    log "Adding users to group: ${GROUP_ID}"
    IFS=',' read -ra USERS <<< "${ADD_USERS}"
    for user in "${USERS[@]}"; do
      user="$(printf '%s' "${user}" | xargs)"
      [[ -z "${user}" ]] && continue
      if "${OCC[@]}" user:info "${user}" >/dev/null 2>&1; then
        "${OCC[@]}" group:adduser "${GROUP_ID}" "${user}" || true
      else
        warn "Nextcloud user does not exist, skipped: ${user}"
      fi
    done
  fi
else
  log "List-only mode: no changes"
fi

log "PDH group members"
"${OCC[@]}" group:info "${GROUP_ID}" || {
  warn "Group not found: ${GROUP_ID}"
  exit 0
}

log "All Nextcloud groups containing pdh"
"${OCC[@]}" group:list | grep -i "pdh" || true

log "Done"
printf '\nNext step: import members of group %s into PDH users table.\n' "${GROUP_ID}"
