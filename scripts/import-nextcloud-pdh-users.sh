#!/usr/bin/env bash
set -Eeuo pipefail

# Import/sync Nextcloud users from group `pdh` into the PDH Postgres users table.
# Source of truth for membership: Nextcloud group.
# PDH username = Nextcloud userId.
# PDH roles stay managed in PDH; new users receive --default-role.
# Database writes are executed locally as Linux user `postgres` via Unix socket.

NEXTCLOUD_DIR="/var/www/nextcloud"
GROUP_ID="pdh"
DEFAULT_ROLE="viewer"
DEACTIVATE_MISSING="0"
DRY_RUN="0"
DB_NAME="${PDH_DATABASE_NAME:-pdh}"

log() { printf '\n==> %s\n' "$*"; }
warn() { printf '\nWARN: %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Import Nextcloud group members into PDH users table.

Options:
  --nextcloud-dir PATH      Default: /var/www/nextcloud
  --group ID                Default: pdh
  --default-role ROLE       Default: viewer; allowed: admin, manager, technician, worker, viewer
  --deactivate-missing      Deactivate PDH users previously synced from Nextcloud but no longer in group
  --dry-run                 Print planned changes only
  --db-name NAME            Default: env PDH_DATABASE_NAME or pdh
  -h, --help                Show help

Examples:
  sudo bash scripts/import-nextcloud-pdh-users.sh

  sudo bash scripts/import-nextcloud-pdh-users.sh \
    --default-role technician \
    --deactivate-missing

  sudo bash scripts/import-nextcloud-pdh-users.sh --dry-run
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    --group) GROUP_ID="${2:-}"; shift 2 ;;
    --default-role) DEFAULT_ROLE="${2:-}"; shift 2 ;;
    --deactivate-missing) DEACTIVATE_MISSING="1"; shift ;;
    --dry-run) DRY_RUN="1"; shift ;;
    --db-name) DB_NAME="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

case "${DEFAULT_ROLE}" in
  admin|manager|technician|worker|viewer) ;;
  *) err "Invalid role: ${DEFAULT_ROLE}"; exit 2 ;;
esac

if [[ "${EUID}" -ne 0 ]]; then
  err "Run as root, e.g. sudo bash $0"
  exit 1
fi

if [[ ! -f "${NEXTCLOUD_DIR}/occ" ]]; then
  err "Nextcloud occ not found: ${NEXTCLOUD_DIR}/occ"
  exit 1
fi

if ! command -v psql >/dev/null 2>&1; then
  err "psql missing. Install postgresql-client."
  exit 1
fi

OCC=(sudo -u www-data php -d apc.enable_cli=1 "${NEXTCLOUD_DIR}/occ")
PSQL=(sudo -u postgres psql -v ON_ERROR_STOP=1 -d "${DB_NAME}")
TMP_MEMBERS="$(mktemp)"
TMP_SQL="$(mktemp)"
trap 'rm -f "${TMP_MEMBERS}" "${TMP_SQL}" /tmp/pdh-nc-group-info.txt /tmp/pdh-nc-group-err.txt /tmp/pdh-nc-user-*.txt' EXIT

run_sql_file() {
  chmod 0644 "${TMP_SQL}"
  "${PSQL[@]}" -f "${TMP_SQL}"
}

log "Checking database access as postgres user"
if [[ "${DRY_RUN}" != "1" ]]; then
  "${PSQL[@]}" -c 'SELECT 1;' >/dev/null
fi

log "Checking Nextcloud group: ${GROUP_ID}"
if ! "${OCC[@]}" group:info "${GROUP_ID}" >/tmp/pdh-nc-group-info.txt 2>/tmp/pdh-nc-group-err.txt; then
  warn "Group missing. Creating: ${GROUP_ID}"
  "${OCC[@]}" group:add "${GROUP_ID}"
  "${OCC[@]}" group:info "${GROUP_ID}" >/tmp/pdh-nc-group-info.txt
fi

python3 - /tmp/pdh-nc-group-info.txt "${TMP_MEMBERS}" <<'PY'
import re, sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding='utf-8', errors='replace').read().splitlines()
users = []
in_users = False
for line in text:
    stripped = line.strip()
    low = stripped.lower()
    if low.startswith('- users:') or low == 'users:' or low == 'users':
        in_users = True
        continue
    if in_users:
        if re.match(r'^-\s+', stripped):
            val = re.sub(r'^-\s+', '', stripped).strip()
            if val and not val.endswith(':'):
                users.append(val)
        elif stripped.endswith(':') and not stripped.lower().startswith('users'):
            in_users = False
seen = []
for u in users:
    if u not in seen:
        seen.append(u)
open(dst, 'w', encoding='utf-8').write('\n'.join(seen) + ('\n' if seen else ''))
PY

if [[ ! -s "${TMP_MEMBERS}" ]]; then
  warn "No members in Nextcloud group: ${GROUP_ID}"
  warn "Add users first, e.g.: sudo bash scripts/sync-nextcloud-pdh-group.sh --add-users michael"
else
  log "Members found"
  sed 's/^/  - /' "${TMP_MEMBERS}"
fi

log "Ensuring PDH user sync columns exist"
cat > "${TMP_SQL}" <<'SQL'
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS nextcloud_user_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS nextcloud_display_name VARCHAR(255),
    ADD COLUMN IF NOT EXISTS nextcloud_synced BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS nextcloud_last_sync TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_nextcloud_user_id
    ON users(nextcloud_user_id)
    WHERE nextcloud_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_nextcloud_synced
    ON users(nextcloud_synced);
SQL
if [[ "${DRY_RUN}" == "1" ]]; then
  cat "${TMP_SQL}"
else
  run_sql_file
fi

escape_sql() {
  printf "%s" "$1" | sed "s/'/''/g"
}

split_display_name() {
  local display="$1"
  local first last
  display="$(printf '%s' "${display}" | xargs || true)"
  if [[ -z "${display}" ]]; then
    printf '\t'
    return
  fi
  first="${display%% *}"
  if [[ "${display}" == *" "* ]]; then
    last="${display#* }"
  else
    last=""
  fi
  printf '%s\t%s' "${first}" "${last}"
}

IMPORTED=0
while IFS= read -r nc_user; do
  [[ -z "${nc_user}" ]] && continue
  info_file="/tmp/pdh-nc-user-${nc_user}.txt"
  if ! "${OCC[@]}" user:info "${nc_user}" > "${info_file}" 2>/dev/null; then
    warn "Skipping missing Nextcloud user: ${nc_user}"
    continue
  fi

  display="$(grep -E '^[[:space:]]*- display-name:' "${info_file}" | sed 's/^[[:space:]]*- display-name:[[:space:]]*//' | head -n1 || true)"
  email="$(grep -E '^[[:space:]]*- email:' "${info_file}" | sed 's/^[[:space:]]*- email:[[:space:]]*//' | head -n1 || true)"
  enabled="$(grep -E '^[[:space:]]*- enabled:' "${info_file}" | sed 's/^[[:space:]]*- enabled:[[:space:]]*//' | head -n1 || true)"

  [[ -z "${display}" || "${display}" == "-" ]] && display="${nc_user}"
  [[ -z "${email}" || "${email}" == "-" ]] && email="${nc_user}@local.invalid"
  [[ -z "${enabled}" || "${enabled}" == "-" ]] && enabled="true"

  IFS=$'\t' read -r first last < <(split_display_name "${display}")
  [[ -z "${first}" ]] && first="${nc_user}"

  nc_user_sql="$(escape_sql "${nc_user}")"
  display_sql="$(escape_sql "${display}")"
  email_sql="$(escape_sql "${email}")"
  first_sql="$(escape_sql "${first}")"
  last_sql="$(escape_sql "${last}")"
  active_sql="true"
  [[ "${enabled}" == "false" ]] && active_sql="false"

  cat > "${TMP_SQL}" <<SQL
INSERT INTO users (
  username, email, password_hash, first_name, last_name, role, department, phone, active,
  nextcloud_user_id, nextcloud_display_name, nextcloud_synced, nextcloud_last_sync
) VALUES (
  '${nc_user_sql}', '${email_sql}', 'nextcloud-synced-disabled-login', '${first_sql}', '${last_sql}', '${DEFAULT_ROLE}', 'Nextcloud', '', ${active_sql},
  '${nc_user_sql}', '${display_sql}', true, NOW()
)
ON CONFLICT (username) DO UPDATE SET
  email = EXCLUDED.email,
  first_name = EXCLUDED.first_name,
  last_name = EXCLUDED.last_name,
  department = CASE WHEN users.department IS NULL OR users.department = '' OR users.department = 'Nextcloud' THEN EXCLUDED.department ELSE users.department END,
  active = EXCLUDED.active,
  nextcloud_user_id = EXCLUDED.nextcloud_user_id,
  nextcloud_display_name = EXCLUDED.nextcloud_display_name,
  nextcloud_synced = true,
  nextcloud_last_sync = NOW(),
  updated_at = NOW();
SQL

  if [[ "${DRY_RUN}" == "1" ]]; then
    printf '\n-- user: %s\n' "${nc_user}"
    cat "${TMP_SQL}"
  else
    run_sql_file
  fi
  IMPORTED=$((IMPORTED + 1))
done < "${TMP_MEMBERS}"

if [[ "${DEACTIVATE_MISSING}" == "1" ]]; then
  log "Deactivating previously synced users missing from Nextcloud group"
  member_list_sql=""
  while IFS= read -r nc_user; do
    [[ -z "${nc_user}" ]] && continue
    item="'$(escape_sql "${nc_user}")'"
    if [[ -z "${member_list_sql}" ]]; then
      member_list_sql="${item}"
    else
      member_list_sql="${member_list_sql},${item}"
    fi
  done < "${TMP_MEMBERS}"
  if [[ -z "${member_list_sql}" ]]; then
    member_list_sql="''"
  fi
  cat > "${TMP_SQL}" <<SQL
UPDATE users
SET active=false, updated_at=NOW(), nextcloud_last_sync=NOW()
WHERE nextcloud_synced=true
  AND COALESCE(nextcloud_user_id, username) NOT IN (${member_list_sql});
SQL
  if [[ "${DRY_RUN}" == "1" ]]; then
    cat "${TMP_SQL}"
  else
    run_sql_file
  fi
fi

log "Import finished"
printf 'Imported/updated users: %s\n' "${IMPORTED}"
printf 'Group: %s\n' "${GROUP_ID}"
printf 'Default role for new users: %s\n' "${DEFAULT_ROLE}"
if [[ "${DEACTIVATE_MISSING}" == "1" ]]; then
  printf 'Deactivate missing: yes\n'
else
  printf 'Deactivate missing: no\n'
fi
