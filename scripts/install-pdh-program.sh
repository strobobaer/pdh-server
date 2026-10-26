#!/usr/bin/env bash
# PDH – Programm ohne eigene Datenbank installieren (zweiter Server im
# Datenbank-Spiegel). Die Datenbank kommt per „pdh-ha replica-init“ vom
# aktiven Server, ebenso die .env.
#
#   sudo bash install-pdh-program.sh <PDH-Ordner> <Commit>
#
# Legt Benutzer pdh, Checkout auf <Commit> (Branch main) und die Unit
# pdh.service an. Startet pdh nicht – das übernimmt pdh-ha auf dem aktiven Server.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -euo pipefail

REPO_URL="https://github.com/strobobaer/pdh-server.git"
REPO="${1:-}" COMMIT="${2:-}"

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }

[[ -n "$REPO" && -n "$COMMIT" ]] || fail "Aufruf: sudo bash $0 <PDH-Ordner> <Commit>"
[[ "$COMMIT" =~ ^[0-9a-f]{7,40}$ ]] || fail "Ungültiger Commit: $COMMIT"
(( EUID == 0 )) || fail "Bitte als root ausführen."

id pdh >/dev/null 2>&1 || useradd --system --home-dir "$REPO" --shell /usr/sbin/nologin pdh
git_r() { git -c safe.directory="$REPO" -C "$REPO" "$@"; }
if [[ -d "$REPO/.git" ]]; then
  git_r fetch --quiet origin
else
  git clone --quiet "$REPO_URL" "$REPO"
fi
git_r checkout --quiet -B main "$COMMIT" || fail "Commit $COMMIT nicht im Repo gefunden (erst auf GitHub hochgeladen?)"
git_r branch --quiet --set-upstream-to=origin/main main || true
chown -R pdh:pdh "$REPO"
ok "PDH-Quelltext auf Stand $COMMIT ($REPO)"

if [[ ! -f /etc/systemd/system/pdh.service ]]; then
  cat >/etc/systemd/system/pdh.service <<UNIT
[Unit]
Description=PDH Server
After=network.target postgresql.service

[Service]
Type=simple
WorkingDirectory=$REPO
ExecStart=$REPO/bin/pdh
Restart=always
RestartSec=5
EnvironmentFile=$REPO/.env
User=pdh
Group=pdh

[Install]
WantedBy=multi-user.target
UNIT
fi
systemctl daemon-reload
# pdh startet nicht beim Hochfahren – im Verbund schaltet pdh-ha ihn
systemctl disable pdh >/dev/null 2>&1 || true
ok "Dienst pdh eingerichtet (startet nur auf dem aktiven Server)"
