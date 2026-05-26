#!/usr/bin/env bash
set -Eeuo pipefail

APP_ID="pdh"
SRC_DIR="nextcloud-app/${APP_ID}"
DIST_DIR="dist"
ZIP_FILE="${DIST_DIR}/${APP_ID}-nextcloud-app.zip"
TMP_DIR=""

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
err() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; }

if [[ ! -d "${SRC_DIR}" ]]; then
  err "Source dir not found: ${SRC_DIR}"
  exit 1
fi

command -v zip >/dev/null 2>&1 || {
  err "zip is required. Install with: sudo apt install -y zip"
  exit 1
}

log "Checking duplicate paths"
DUPES="$(find "${SRC_DIR}" -type f | sed "s#^${SRC_DIR}/##" | sort | uniq -d || true)"
if [[ -n "${DUPES}" ]]; then
  err "Duplicate file paths detected:"
  printf '%s\n' "${DUPES}" >&2
  exit 1
fi

log "Checking blocked files"
BLOCKED="$(find "${SRC_DIR}" \( -name '.git' -o -name '.DS_Store' -o -name 'Thumbs.db' -o -name '*.bak' -o -name '*.tmp' \) -print || true)"
if [[ -n "${BLOCKED}" ]]; then
  err "Blocked files detected:"
  printf '%s\n' "${BLOCKED}" >&2
  exit 1
fi

log "Creating package"
rm -rf "${DIST_DIR}"
mkdir -p "${DIST_DIR}"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
mkdir -p "${TMP_DIR}/${APP_ID}"
cp -a "${SRC_DIR}/." "${TMP_DIR}/${APP_ID}/"

(
  cd "${TMP_DIR}"
  zip -qr "${OLDPWD}/${ZIP_FILE}" "${APP_ID}"
)

log "Verifying zip paths"
unzip -l "${ZIP_FILE}" >/tmp/pdh-nextcloud-app-zip-list.txt
if awk '{print $4}' /tmp/pdh-nextcloud-app-zip-list.txt | grep -E '(^|/)\.git(/|$)|\.DS_Store$|Thumbs.db$|\.bak$|\.tmp$' >/dev/null; then
  err "Blocked files found inside zip"
  exit 1
fi

log "Package ready"
printf '%s\n' "${ZIP_FILE}"
