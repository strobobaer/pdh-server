#!/usr/bin/env bash
set -Eeuo pipefail

# Diagnose script for PDH <-> Nextcloud integration.
# Checks:
# - PDH health endpoint
# - systemd environment drop-in
# - /etc/pdh/pdh.env without printing secrets
# - Nextcloud WebDAV authentication
# - PDH root folder existence / creation
# - temporary folder create/delete via WebDAV
# - local Nextcloud app endpoint when available
# - nginx syntax when available

ENV_FILE="/etc/pdh/pdh.env"
SERVICE="pdh"
PDH_HEALTH_URL="http://127.0.0.1:8090/health"
NEXTCLOUD_ORIGIN="https://cloud.strobl-home.net"
NEXTCLOUD_APP_ORIGIN="https://localhost:8436"
SKIP_WRITE_TEST="0"

ok() { printf '\033[1;32mOK\033[0m   %s\n' "$*"; }
warn() { printf '\033[1;33mWARN\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL\033[0m %s\n' "$*"; }
info() { printf '\033[1;34mINFO\033[0m %s\n' "$*"; }

usage() {
  cat <<'USAGE'
Check PDH <-> Nextcloud integration.

Options:
  --env-file PATH             Default: /etc/pdh/pdh.env
  --service NAME              Default: pdh
  --pdh-health-url URL        Default: http://127.0.0.1:8090/health
  --nextcloud-origin URL      Default: https://cloud.strobl-home.net
  --nextcloud-app-origin URL  Default: https://localhost:8436
  --skip-write-test           Do not create/delete WebDAV test folder
  -h, --help                  Show help

Example:
  sudo bash scripts/check-pdh-nextcloud-integration.sh
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --service) SERVICE="${2:-}"; shift 2 ;;
    --pdh-health-url) PDH_HEALTH_URL="${2:-}"; shift 2 ;;
    --nextcloud-origin) NEXTCLOUD_ORIGIN="${2:-}"; shift 2 ;;
    --nextcloud-app-origin) NEXTCLOUD_APP_ORIGIN="${2:-}"; shift 2 ;;
    --skip-write-test) SKIP_WRITE_TEST="1"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "Required command missing: $1"
    return 1
  fi
  ok "Command available: $1"
}

mask_value() {
  local key="$1"
  local value="$2"
  if [[ "${key}" == *PASSWORD* || "${key}" == *SECRET* || "${key}" == *TOKEN* ]]; then
    printf '%s=***MASKED***\n' "${key}"
  else
    printf '%s=%s\n' "${key}" "${value}"
  fi
}

load_env() {
  if [[ ! -f "${ENV_FILE}" ]]; then
    fail "Env file not found: ${ENV_FILE}"
    return 1
  fi
  ok "Env file exists: ${ENV_FILE}"

  # shellcheck disable=SC1090
  set -a
  source "${ENV_FILE}"
  set +a

  local required=(PDH_NEXTCLOUD_ENABLED PDH_NEXTCLOUD_BASEURL PDH_NEXTCLOUD_USERNAME PDH_NEXTCLOUD_PASSWORD PDH_NEXTCLOUD_ROOTPATH)
  local missing=0
  for key in "${required[@]}"; do
    if [[ -z "${!key:-}" ]]; then
      fail "Missing env key: ${key}"
      missing=1
    else
      mask_value "${key}" "${!key}"
    fi
  done
  return "${missing}"
}

webdav_url() {
  local remote_path="${1:-}"
  local base="${PDH_NEXTCLOUD_BASEURL%/}"
  local user="${PDH_NEXTCLOUD_USERNAME}"
  python3 - "$base" "$user" "$remote_path" <<'PY'
import sys
from urllib.parse import quote
base, user, remote = sys.argv[1], sys.argv[2], sys.argv[3]
parts = [quote(p, safe='') for p in remote.strip('/').split('/') if p]
suffix = '/'.join(parts)
url = base + '/remote.php/dav/files/' + quote(user, safe='') + '/'
if suffix:
    url += suffix
print(url)
PY
}

curl_code() {
  local method="$1"
  local url="$2"
  curl -k -sS -o /tmp/pdh-nextcloud-check-body.txt -w '%{http_code}' \
    -u "${PDH_NEXTCLOUD_USERNAME}:${PDH_NEXTCLOUD_PASSWORD}" \
    -X "${method}" "${url}"
}

check_systemd() {
  if ! command -v systemctl >/dev/null 2>&1; then
    warn "systemctl not available; skipping systemd checks"
    return 0
  fi

  if systemctl is-active --quiet "${SERVICE}"; then
    ok "Service active: ${SERVICE}"
  else
    fail "Service not active: ${SERVICE}"
  fi

  local dropin="/etc/systemd/system/${SERVICE}.service.d/20-nextcloud-env.conf"
  if [[ -f "${dropin}" ]]; then
    ok "systemd drop-in exists: ${dropin}"
  else
    warn "systemd drop-in missing: ${dropin}"
  fi

  systemctl show "${SERVICE}" -p EnvironmentFiles 2>/dev/null || true
}

check_pdh_health() {
  local body code
  code="$(curl -fsS -o /tmp/pdh-health-body.txt -w '%{http_code}' "${PDH_HEALTH_URL}" 2>/tmp/pdh-health-err.txt || true)"
  body="$(cat /tmp/pdh-health-body.txt 2>/dev/null || true)"
  if [[ "${code}" == "200" && "${body}" == *'"status":"ok"'* ]]; then
    ok "PDH health ok: ${PDH_HEALTH_URL}"
  else
    fail "PDH health failed: ${PDH_HEALTH_URL} code=${code:-none} body=${body:-none}"
    if [[ -s /tmp/pdh-health-err.txt ]]; then
      sed 's/^/  /' /tmp/pdh-health-err.txt
    fi
  fi
}

check_webdav() {
  local root_url code root_path test_path test_url

  root_url="$(webdav_url '')"
  code="$(curl_code PROPFIND "${root_url}")"
  if [[ "${code}" == "207" || "${code}" == "200" ]]; then
    ok "WebDAV auth ok: ${root_url}"
  else
    fail "WebDAV auth failed: ${root_url} code=${code}"
    sed 's/^/  /' /tmp/pdh-nextcloud-check-body.txt | head -n 20
    return 1
  fi

  root_path="${PDH_NEXTCLOUD_ROOTPATH:-PDH}"
  local root_folder_url
  root_folder_url="$(webdav_url "${root_path}")"
  code="$(curl_code PROPFIND "${root_folder_url}")"
  if [[ "${code}" == "207" || "${code}" == "200" ]]; then
    ok "Nextcloud root folder exists: ${root_path}"
  elif [[ "${code}" == "404" ]]; then
    warn "Nextcloud root folder missing; trying MKCOL: ${root_path}"
    code="$(curl_code MKCOL "${root_folder_url}")"
    if [[ "${code}" == "201" || "${code}" == "405" ]]; then
      ok "Nextcloud root folder created/existed: ${root_path}"
    else
      fail "Could not create root folder: ${root_path} code=${code}"
      sed 's/^/  /' /tmp/pdh-nextcloud-check-body.txt | head -n 20
      return 1
    fi
  else
    fail "Could not check root folder: ${root_path} code=${code}"
    sed 's/^/  /' /tmp/pdh-nextcloud-check-body.txt | head -n 20
    return 1
  fi

  if [[ "${SKIP_WRITE_TEST}" == "1" ]]; then
    warn "Skipping WebDAV write/delete test"
    return 0
  fi

  test_path="${root_path}/_pdh_integration_check_$(date +%s)"
  test_url="$(webdav_url "${test_path}")"
  code="$(curl_code MKCOL "${test_url}")"
  if [[ "${code}" == "201" || "${code}" == "405" ]]; then
    ok "WebDAV test folder created: ${test_path}"
  else
    fail "WebDAV test folder create failed: ${test_path} code=${code}"
    sed 's/^/  /' /tmp/pdh-nextcloud-check-body.txt | head -n 20
    return 1
  fi

  code="$(curl_code DELETE "${test_url}")"
  if [[ "${code}" == "204" || "${code}" == "200" || "${code}" == "404" ]]; then
    ok "WebDAV test folder deleted: ${test_path}"
  else
    fail "WebDAV test folder delete failed: ${test_path} code=${code}"
    sed 's/^/  /' /tmp/pdh-nextcloud-check-body.txt | head -n 20
    return 1
  fi
}

check_nextcloud_app() {
  local code
  code="$(curl -k -sS -o /tmp/pdh-nextcloud-app-body.txt -w '%{http_code}' "${NEXTCLOUD_APP_ORIGIN}/apps/pdh/" || true)"
  if [[ "${code}" == "200" || "${code}" == "302" || "${code}" == "401" ]]; then
    ok "Nextcloud PDH app endpoint reachable: ${NEXTCLOUD_APP_ORIGIN}/apps/pdh/ code=${code}"
  else
    warn "Nextcloud PDH app endpoint unexpected code=${code}: ${NEXTCLOUD_APP_ORIGIN}/apps/pdh/"
  fi
}

check_nginx() {
  if command -v nginx >/dev/null 2>&1; then
    if nginx -t >/tmp/pdh-nginx-check.txt 2>&1; then
      ok "nginx config test passed"
    else
      fail "nginx config test failed"
      sed 's/^/  /' /tmp/pdh-nginx-check.txt
    fi
  else
    warn "nginx not available; skipping nginx check"
  fi
}

main() {
  info "PDH <-> Nextcloud integration check"
  require_cmd curl
  require_cmd python3
  load_env
  check_systemd
  check_pdh_health
  check_webdav
  check_nextcloud_app
  check_nginx
  info "Check finished"
}

main "$@"
