#!/usr/bin/env bash
set -Eeuo pipefail

# Install a systemd service + timer for regular Nextcloud group -> PDH user sync.
# The sync imports members of the Nextcloud group `pdh` into the PDH users table.
# Database writes are executed by scripts/import-nextcloud-pdh-users.sh as Linux user postgres.

PDH_DIR="/home/michael/pdh"
GROUP_ID="pdh"
DEFAULT_ROLE="viewer"
DEACTIVATE_MISSING="0"
ON_CALENDAR="*:0/15"
SERVICE_NAME="pdh-nextcloud-user-sync"

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Install systemd timer for Nextcloud group -> PDH user sync.

Options:
  --pdh-dir PATH            Default: /home/michael/pdh
  --group ID                Default: pdh
  --default-role ROLE       Default: viewer; allowed: admin, manager, technician, worker, viewer
  --deactivate-missing      Deactivate PDH users previously synced from Nextcloud but no longer in group
  --on-calendar VALUE       Default: *:0/15  (every 15 minutes)
  --service-name NAME       Default: pdh-nextcloud-user-sync
  -h, --help                Show help

Examples:
  sudo bash scripts/install-nextcloud-pdh-user-sync-timer.sh

  sudo bash scripts/install-nextcloud-pdh-user-sync-timer.sh \
    --default-role technician \
    --deactivate-missing \
    --on-calendar '*:0/10'
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --pdh-dir) PDH_DIR="${2:-}"; shift 2 ;;
    --group) GROUP_ID="${2:-}"; shift 2 ;;
    --default-role) DEFAULT_ROLE="${2:-}"; shift 2 ;;
    --deactivate-missing) DEACTIVATE_MISSING="1"; shift ;;
    --on-calendar) ON_CALENDAR="${2:-}"; shift 2 ;;
    --service-name) SERVICE_NAME="${2:-}"; shift 2 ;;
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

if [[ ! -x "${PDH_DIR}/scripts/import-nextcloud-pdh-users.sh" ]]; then
  err "Import script not executable: ${PDH_DIR}/scripts/import-nextcloud-pdh-users.sh"
  err "Run: chmod +x ${PDH_DIR}/scripts/import-nextcloud-pdh-users.sh"
  exit 1
fi

SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
TIMER_FILE="/etc/systemd/system/${SERVICE_NAME}.timer"
ARGS=(--group "${GROUP_ID}" --default-role "${DEFAULT_ROLE}")
if [[ "${DEACTIVATE_MISSING}" == "1" ]]; then
  ARGS+=(--deactivate-missing)
fi

log "Writing ${SERVICE_FILE}"
cat > "${SERVICE_FILE}" <<EOF
[Unit]
Description=PDH - Sync Nextcloud group members into PDH users
Wants=postgresql.service
After=postgresql.service

[Service]
Type=oneshot
WorkingDirectory=${PDH_DIR}
ExecStart=/usr/bin/env bash ${PDH_DIR}/scripts/import-nextcloud-pdh-users.sh ${ARGS[*]}
Nice=5
IOSchedulingClass=best-effort
EOF

log "Writing ${TIMER_FILE}"
cat > "${TIMER_FILE}" <<EOF
[Unit]
Description=Run PDH Nextcloud user sync regularly

[Timer]
OnCalendar=${ON_CALENDAR}
Persistent=true
AccuracySec=1min
Unit=${SERVICE_NAME}.service

[Install]
WantedBy=timers.target
EOF

log "Enabling timer"
systemctl daemon-reload
systemctl enable --now "${SERVICE_NAME}.timer"

log "Running one sync now"
systemctl start "${SERVICE_NAME}.service"

log "Status"
systemctl status "${SERVICE_NAME}.timer" --no-pager -l || true
systemctl status "${SERVICE_NAME}.service" --no-pager -l || true

log "Done"
printf 'Timer: %s.timer\n' "${SERVICE_NAME}"
printf 'Logs:  journalctl -u %s.service -n 100 --no-pager\n' "${SERVICE_NAME}"
