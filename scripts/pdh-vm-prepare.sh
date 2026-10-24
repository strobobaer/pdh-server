#!/usr/bin/env bash
# PDH – frische Ubuntu-VM bzw. frischen Ubuntu-Server vorbereiten.
#
#   sudo bash pdh-vm-prepare.sh --neu
#       Grundeinrichtung + neue PDH-Installation (Postgres, PDH, Update-Agent);
#       danach im Browser http://<server>:8090/setup aufrufen.
#
#   sudo bash pdh-vm-prepare.sh --wiederaufbau --share //nas/sicherung --user backup [--restore]
#       Grundeinrichtung + Sicherungsfreigabe (Ausfallschutz) dauerhaft einbinden;
#       mit --restore startet gleich pdh-restore.sh.
#
# Optionen:
#   --hostname NAME   Rechnernamen setzen
#   --zeitzone TZ     Zeitzone (Standard Europe/Berlin – wichtig für --time beim Wiederaufbau)
#   --share QUELLE    Sicherungsfreigabe: //server/freigabe (SMB) oder server:/pfad (NFS)
#   --user NAME       SMB-Benutzer (Passwort wird abgefragt oder aus PDH_SHARE_PASSWORD gelesen)
#   --domain NAME     SMB-Domäne (optional)
#   --server NAME     Ordner unter PDH-Ausfallschutz/ (bei nur einem: automatisch)
#   --restore         Wiederaufbau gleich starten
#   --time ZEIT       Zeitpunkt für den Wiederaufbau "JJJJ-MM-TT HH:MM:SS"
#   --repo DIR        PDH-Ordner bei --neu (Standard /opt/pdh-server)
#   --branch NAME     Git-Branch bei --neu (Standard main)
#   --yes             ohne Rückfragen

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -Eeuo pipefail
export DEBIAN_FRONTEND=noninteractive

MODE="" HOSTNAME_NEW="" TZONE="Europe/Berlin" SHARE="" SUSER="" SDOMAIN="" SERVER=""
RESTORE=0 TIME="" REPO="/opt/pdh-server" BRANCH="main" YES=0
REPO_URL="https://github.com/strobobaer/pdh-server.git"
MNT="/mnt/sicherung"
CRED="/root/.pdh-share-cred"

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }
usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --neu) MODE=neu; shift ;;
    --wiederaufbau) MODE=wiederaufbau; shift ;;
    --hostname) HOSTNAME_NEW="${2:-}"; shift 2 ;;
    --zeitzone) TZONE="${2:-}"; shift 2 ;;
    --share) SHARE="${2:-}"; shift 2 ;;
    --user) SUSER="${2:-}"; shift 2 ;;
    --domain) SDOMAIN="${2:-}"; shift 2 ;;
    --server) SERVER="${2:-}"; shift 2 ;;
    --restore) RESTORE=1; shift ;;
    --time) TIME="${2:-}"; shift 2 ;;
    --repo) REPO="${2:-}"; shift 2 ;;
    --branch) BRANCH="${2:-}"; shift 2 ;;
    --yes) YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; fail "Unbekannte Option: $1" ;;
  esac
done

[[ -n "$MODE" ]] || { usage; fail "Bitte --neu oder --wiederaufbau angeben."; }
[[ ${EUID} -eq 0 ]] || fail "Bitte mit sudo ausführen: sudo bash $0 …"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID:-}" == ubuntu ]] || fail "Dieses Skript ist für Ubuntu (gefunden: ${PRETTY_NAME:-?})."
case "${VERSION_ID:-}" in 22.04|24.04) ;; *) warn "Getestet mit Ubuntu 22.04/24.04, hier ${VERSION_ID:-?}." ;; esac
[[ "$MODE" != wiederaufbau || -n "$SHARE" ]] || fail "--share fehlt (z. B. --share //nas/sicherung)."
[[ "$REPO" == /* ]] || fail "--repo braucht einen absoluten Pfad."

# ── 1. Prüfen ───────────────────────────────────────────────────────────
info "Prüfe Internet und Speicherplatz …"
getent hosts github.com >/dev/null || fail "Kein Internet bzw. DNS – Netzwerk der VM prüfen."
free_gb=$(df -BG --output=avail / | tail -n 1 | tr -dc '0-9')
(( free_gb >= 10 )) || warn "Nur ${free_gb} GB frei auf / – für PDH mit Nextcloud-Daten deutlich mehr einplanen."
ok "Netz da, ${free_gb} GB frei"

# ── 2. Grundeinrichtung ─────────────────────────────────────────────────
if [[ -n "$HOSTNAME_NEW" ]]; then
  hostnamectl set-hostname "$HOSTNAME_NEW"
  if grep -q '^127\.0\.1\.1' /etc/hosts; then sed -i "s/^127\.0\.1\.1.*/127.0.1.1 $HOSTNAME_NEW/" /etc/hosts
  else echo "127.0.1.1 $HOSTNAME_NEW" >>/etc/hosts; fi
  ok "Rechnername $HOSTNAME_NEW"
fi
timedatectl set-timezone "$TZONE" || fail "Zeitzone $TZONE unbekannt (timedatectl list-timezones)."
timedatectl set-ntp true || true
ok "Zeitzone $TZONE, Zeitabgleich an"

info "Aktualisiere das System und installiere Grundpakete …"
apt-get update -qq
apt-get upgrade -y -qq
apt-get install -y -qq curl ca-certificates git rsync cifs-utils nfs-common python3 ufw openssl unattended-upgrades
virt=$(systemd-detect-virt 2>/dev/null || true)
case "$virt" in
  kvm|qemu) apt-get install -y -qq qemu-guest-agent && systemctl enable --now qemu-guest-agent >/dev/null 2>&1 || true
            ok "Proxmox/KVM erkannt – qemu-guest-agent installiert" ;;
  vmware)   apt-get install -y -qq open-vm-tools && ok "VMware erkannt – open-vm-tools installiert" ;;
  microsoft) apt-get install -y -qq linux-tools-virtual linux-cloud-tools-virtual || true
             ok "Hyper-V erkannt – Integrationsdienste installiert" ;;
  none|"")  info "Keine VM erkannt (physischer Server)" ;;
  *)        info "Virtualisierung $virt – kein Gast-Agent installiert" ;;
esac

info "Firewall (ufw): SSH, HTTP/HTTPS und PDH-Port 8090 offen"
ufw allow OpenSSH >/dev/null
ufw allow 80/tcp >/dev/null
ufw allow 443/tcp >/dev/null
ufw allow 8090/tcp >/dev/null
ufw --force enable >/dev/null
ok "Grundeinrichtung fertig"

# ── 3a. Neue PDH-Installation ───────────────────────────────────────────
if [[ "$MODE" == neu ]]; then
  info "Installiere Postgres, Go und PDH nach $REPO …"
  apt-get install -y -qq postgresql postgresql-contrib make golang-go
  id pdh >/dev/null 2>&1 || useradd --system --home-dir "$REPO" --shell /usr/sbin/nologin pdh
  if [[ ! -d "$REPO/.git" ]]; then
    git clone -q --branch "$BRANCH" "$REPO_URL" "$REPO"
  else
    warn "$REPO ist schon ein Git-Checkout – bleibt wie er ist."
  fi
  if [[ ! -f "$REPO/.env" ]]; then
    dbpw=$(openssl rand -hex 24)
    # Passwort über stdin, nicht in der Prozessliste
    runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 <<SQL >/dev/null
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'pdh') THEN
    CREATE ROLE pdh LOGIN PASSWORD '$dbpw';
  ELSE
    ALTER ROLE pdh WITH LOGIN PASSWORD '$dbpw';
  END IF;
END
\$\$;
SQL
    if ! runuser -u postgres -- psql -XAtqc "SELECT 1 FROM pg_database WHERE datname='pdh'" | grep -q 1; then
      runuser -u postgres -- createdb -O pdh pdh
    fi
    umask 077
    cat >"$REPO/.env" <<ENV
PDH_SERVER_HOST=0.0.0.0
PDH_SERVER_PORT=8090
PDH_SERVER_ENV=production
PDH_DATABASE_HOST=localhost
PDH_DATABASE_PORT=5432
PDH_DATABASE_USER=pdh
PDH_DATABASE_PASSWORD=$dbpw
PDH_DATABASE_NAME=pdh
PDH_DATABASE_SSLMODE=disable
PDH_AUTH_JWTSECRET=$(openssl rand -hex 32)
ENV
    umask 022
    ok "Datenbank pdh mit eigenem Benutzer angelegt, .env geschrieben"
  else
    warn "$REPO/.env existiert – Datenbank-Zugang bleibt wie er ist."
  fi
  chown -R pdh:pdh "$REPO"
  chmod 600 "$REPO/.env"
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
  systemctl enable pdh >/dev/null 2>&1
  info "Baue PDH und richte den Update-Agenten ein (dauert ein paar Minuten) …"
  bash "$REPO/install-update-agent.sh"
  for _ in $(seq 1 60); do [[ -f "$REPO/setup-code.txt" ]] && break; sleep 1; done
  ip=$(hostname -I | awk '{print $1}')
  echo
  ok "PDH läuft (systemctl status pdh)."
  echo "  Weiter im Browser: http://${ip}:8090/setup"
  if [[ -f "$REPO/setup-code.txt" ]]; then
    echo "  Einrichtungscode:  $(cat "$REPO/setup-code.txt")"
  else
    echo "  Einrichtungscode:  in $REPO/setup-code.txt bzw. journalctl -u pdh"
  fi
  echo "  Danach: Server-Einstellungen → VPN/Cloudflare einrichten, Ausfallschutz einschalten."
  exit 0
fi

# ── 3b. Wiederaufbau: Sicherungsfreigabe einbinden ──────────────────────
info "Binde die Sicherungsfreigabe $SHARE nach $MNT ein (nur lesend, dauerhaft) …"
mkdir -p "$MNT"
if [[ "$SHARE" == //* ]]; then
  [[ -n "$SUSER" ]] || { if (( YES )); then fail "--user fehlt"; fi; read -r -p "SMB-Benutzer: " SUSER; }
  pw="${PDH_SHARE_PASSWORD:-}"
  if [[ -z "$pw" ]]; then
    (( ! YES )) || fail "Passwort fehlt (PDH_SHARE_PASSWORD setzen)"
    read -r -s -p "SMB-Passwort für $SUSER: " pw; echo
  fi
  umask 077
  { echo "username=$SUSER"; echo "password=$pw"; if [[ -n "$SDOMAIN" ]]; then echo "domain=$SDOMAIN"; fi; } >"$CRED"
  umask 022
  chmod 600 "$CRED"
  line="$SHARE $MNT cifs credentials=/root/.pdh-share-cred,ro,iocharset=utf8,mfsymlinks,nofail,_netdev 0 0"
elif [[ "$SHARE" == *:/* ]]; then
  line="$SHARE $MNT nfs ro,nofail,_netdev 0 0"
else
  fail "--share bitte als //server/freigabe (SMB) oder server:/pfad (NFS) angeben."
fi
# vorhandenen Eintrag für $MNT ersetzen
sed -i "\# $MNT #d" /etc/fstab
echo "$line" >>/etc/fstab
systemctl daemon-reload
if mountpoint -q "$MNT"; then umount "$MNT"; fi
mount "$MNT" || fail "Freigabe lässt sich nicht einbinden – Adresse, Benutzer und Passwort prüfen (dmesg | tail)."
ok "Freigabe eingebunden (bleibt nach Neustart eingebunden)"

ROOT="$MNT/PDH-Ausfallschutz"
[[ -d "$ROOT" ]] || fail "Auf der Freigabe fehlt der Ordner PDH-Ausfallschutz – richtige Freigabe?"
mapfile -t servers < <(find "$ROOT" -mindepth 1 -maxdepth 1 -type d -exec test -d '{}/base' ';' -printf '%f\n' | sort)
(( ${#servers[@]} > 0 )) || fail "Keine Sicherung unter $ROOT gefunden."
if [[ -z "$SERVER" ]]; then
  if (( ${#servers[@]} == 1 )); then SERVER="${servers[0]}"
  else
    echo "Sicherungen auf der Freigabe: ${servers[*]}"
    (( ! YES )) || fail "Mehrere Sicherungen – bitte --server angeben."
    read -r -p "Welche? " SERVER
  fi
fi
DIR="$ROOT/$SERVER"
[[ -f "$DIR/pdh-restore.sh" ]] || fail "$DIR/pdh-restore.sh fehlt – Sicherung unvollständig."
ok "Sicherung $SERVER gefunden. Grundsicherungen:"
find "$DIR/base" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -printf '    %f\n' | sort

cmd=(bash "$DIR/pdh-restore.sh" --from "$DIR")
if [[ -n "$TIME" ]]; then cmd+=(--time "$TIME"); fi
if (( YES )); then cmd+=(--yes); fi
if (( RESTORE )); then
  info "Starte den Wiederaufbau …"
  exec "${cmd[@]}"
fi
echo
ok "VM vorbereitet. Wiederaufbau starten mit:"
printf '  sudo'; printf ' %q' "${cmd[@]}"; echo
echo '  (für einen früheren Stand zusätzlich: --time "JJJJ-MM-TT HH:MM:SS")'
