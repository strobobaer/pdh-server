#!/usr/bin/env bash
# PDH – Wiederaufbau aus dem Ausfallschutz (Server-Einstellungen → Ausfallschutz).
#
# Baut auf einem frischen Ubuntu-Server bzw. einer frischen VM das System aus dem
# Sicherungsordner auf dem Netzlaufwerk wieder auf: Postgres (inkl. Nextcloud-
# Datenbank), PDH mit Dateien und Konfiguration, Nextcloud, n8n.
#
#   sudo bash pdh-restore.sh --from /mnt/sicherung/PDH-Ausfallschutz/<server>
#   sudo bash pdh-restore.sh --from … --time "2026-10-24 14:30:00"   # Stand von damals
#
# Optionen:
#   --from DIR    Sicherungsordner (enthält base/, wal/, files/)
#   --time ZEIT   Zeitpunkt "JJJJ-MM-TT HH:MM:SS" (Ortszeit); ohne: neuester Stand
#   --base NAME   bestimmte Grundsicherung (Ordnername unter base/)
#   --yes         ohne Rückfrage (die Datenbank dieses Servers wird ersetzt)
#
# Die Freigabe vorher einbinden, z. B.:
#   sudo mount -t cifs //nas/sicherung /mnt/sicherung -o username=…
# Das Skript liegt nach jeder Grundsicherung auch im Sicherungsordner.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -Eeuo pipefail
export DEBIAN_FRONTEND=noninteractive

FROM="" TIME="" BASE="" YES=0
REPO_URL="https://github.com/strobobaer/pdh-server.git"

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }
usage() { sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from) FROM="${2:-}"; shift 2 ;;
    --time) TIME="${2:-}"; shift 2 ;;
    --base) BASE="${2:-}"; shift 2 ;;
    --yes) YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; fail "Unbekannte Option: $1" ;;
  esac
done

[[ ${EUID} -eq 0 ]] || fail "Bitte mit sudo ausführen: sudo bash $0 --from …"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID:-}" == ubuntu ]] || fail "Dieses Skript ist für Ubuntu (gefunden: ${PRETTY_NAME:-?})."
[[ -n "$FROM" ]] || { usage; fail "--from fehlt"; }
FROM="${FROM%/}"
[[ -d "$FROM/base" && -d "$FROM/wal" ]] || fail "$FROM enthält keine Sicherung (base/ und wal/ fehlen)."
[[ "$FROM" != *"'"* ]] || fail "Der Pfad darf kein ' enthalten."
command -v python3 >/dev/null || { apt-get update -qq; apt-get install -y -qq python3; }

# ── 1. Grundsicherung wählen ─────────────────────────────────────────────
pick=$(python3 - "$FROM/base" "$TIME" "$BASE" <<'PY'
import datetime, json, os, re, sys
d, t, want = sys.argv[1], sys.argv[2], sys.argv[3]
def ts(s):
    s = re.sub(r"\.\d+", "", s).replace("Z", "+00:00")
    return datetime.datetime.fromisoformat(s)
bases = []
for n in sorted(os.listdir(d)):
    p = os.path.join(d, n, "info.json")
    if n.startswith(".") or not os.path.isfile(p):
        continue
    with open(p) as f:
        i = json.load(f)
    i["name"] = n
    bases.append(i)
if not bases:
    print("ERR Keine Grundsicherung gefunden"); sys.exit()
if want:
    sel = [b for b in bases if b["name"] == want]
    if not sel:
        print("ERR Grundsicherung " + want + " nicht gefunden"); sys.exit()
    b = sel[0]
elif t:
    try:
        lim = datetime.datetime.fromisoformat(t).astimezone()
    except ValueError:
        print("ERR Zeitpunkt bitte als JJJJ-MM-TT HH:MM:SS angeben"); sys.exit()
    cand = [b for b in bases if ts(b["created"]) <= lim]
    if not cand:
        oldest = min(bases, key=lambda b: ts(b["created"]))
        print("ERR Keine Grundsicherung vor " + t + " – älteste ist " + oldest["name"]); sys.exit()
    b = max(cand, key=lambda b: ts(b["created"]))
else:
    b = max(bases, key=lambda b: ts(b["created"]))
print("OK", b["name"], b.get("pg_version_num") or 0, b.get("commit") or "-", b.get("repo_dir") or "/opt/pdh-server")
PY
)
read -r status NAME PGNUM COMMIT REPO <<<"$pick"
[[ "$status" == OK ]] || fail "${pick#ERR }"
PGVER=$((PGNUM / 10000))
(( PGVER >= 10 )) || fail "Postgres-Version der Sicherung unbekannt (info.json)."
ok "Grundsicherung $NAME (Postgres $PGVER)${TIME:+, Zielzeitpunkt $TIME}"

# Herkunft der Dateien: files/sources.json (vom Agenten bei jedem Abgleich geschrieben)
SOURCES="$FROM/files/sources.json"
src_path() { # Label → ursprünglicher Pfad
  [[ -f "$SOURCES" ]] || return 0
  python3 - "$SOURCES" "$1" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for s in d.get("sources") or []:
    if s.get("label") == sys.argv[2]:
        print(s.get("path", "")); break
PY
}
if [[ -f "$SOURCES" ]]; then
  r=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("repo_dir") or "")' "$SOURCES")
  [[ -n "$r" ]] && REPO="$r"
fi

if (( ! YES )); then
  warn "Die Postgres-Datenbank dieses Servers (Version $PGVER) wird durch die Sicherung ersetzt."
  read -r -p "Fortfahren? (ja/nein) " answer
  [[ "$answer" == ja ]] || fail "Abgebrochen."
fi

# ── 2. Postgres in der Version der Sicherung ─────────────────────────────
info "Installiere Postgres $PGVER …"
apt-get update -qq
if ! apt-cache show "postgresql-$PGVER" >/dev/null 2>&1; then
  info "Postgres $PGVER fehlt in Ubuntu – richte das Paketarchiv apt.postgresql.org ein"
  apt-get install -y -qq curl ca-certificates lsb-release
  install -d /usr/share/postgresql-common/pgdg
  curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc
  echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" \
    >/etc/apt/sources.list.d/pgdg.list
  apt-get update -qq
fi
apt-get install -y -qq "postgresql-$PGVER" rsync git make golang-go openssl cifs-utils nfs-common
pg_lsclusters -h | awk '{print $1, $2}' | grep -qx "$PGVER main" || pg_createcluster "$PGVER" main
DATA="/var/lib/postgresql/$PGVER/main"

# ── 3. Datenbank zurückspielen (Grundsicherung + WAL bis zum Zeitpunkt) ──
info "Spiele die Datenbank zurück …"
systemctl stop "postgresql@$PGVER-main" || true
find "$DATA" -mindepth 1 -delete
tar -xzf "$FROM/base/$NAME/base.tar.gz" -C "$DATA"
# WAL lokal bereitstellen – postgres darf die Freigabe oft nicht lesen
WAL="/var/lib/postgresql/pdh-restore-wal"
rm -rf "$WAL"; mkdir -p "$WAL"
rsync -rt "$FROM/wal/" "$WAL/"
chown -R postgres:postgres "$DATA" "$WAL"; chmod 700 "$DATA"
touch "$DATA/recovery.signal"
{
  echo "# pdh-restore (wird nach der Wiederherstellung entfernt)"
  echo "restore_command = 'gunzip -c $WAL/%f.gz > %p'"
  echo "recovery_target_action = 'promote'"
  if [[ -n "$TIME" ]]; then echo "recovery_target_time = '$TIME'"; fi
} >>"$DATA/postgresql.auto.conf"
chown postgres:postgres "$DATA/recovery.signal" "$DATA/postgresql.auto.conf"
systemctl start "postgresql@$PGVER-main"
done_=0
for i in $(seq 1 1800); do
  r=$(runuser -u postgres -- psql -XAtqc 'SELECT pg_is_in_recovery()' 2>/dev/null || true)
  if [[ "$r" == f ]]; then done_=1; break; fi
  (( i % 30 == 0 )) && info "… Datenbank wird noch wiederhergestellt ($((i / 60)) min)"
  sleep 1
done
(( done_ )) || fail "Wiederherstellung nicht abgeschlossen – journalctl -u postgresql@$PGVER-main prüfen."
sed -i -E '/^# pdh-restore/d; /^restore_command = /d; /^recovery_target_(action|time) = /d' "$DATA/postgresql.auto.conf"
runuser -u postgres -- psql -XAtqc 'SELECT pg_reload_conf()' >/dev/null
rm -rf "$WAL"
ok "Datenbank wiederhergestellt"

# ── 4. PDH ───────────────────────────────────────────────────────────────
info "Richte PDH in $REPO ein …"
id pdh >/dev/null 2>&1 || useradd --system --home-dir "$REPO" --shell /usr/sbin/nologin pdh
if [[ ! -d "$REPO/.git" ]]; then
  git clone -q "$REPO_URL" "$REPO"
fi
gitr() { git -c safe.directory="$REPO" -C "$REPO" "$@"; } # Repo gehört später pdh
gitr fetch -q origin
if [[ "$COMMIT" != "-" ]]; then
  gitr checkout -q -B main "$COMMIT"
  gitr branch -q -u origin/main main || true
fi
restore_dir() { # Label Zielordner [rsync-Filter …]
  [[ -d "$FROM/files/$1" ]] || return 0
  mkdir -p "$2"; rsync -rtl "${@:3}" "$FROM/files/$1/" "$2/"
}
restore_dir uploads "$REPO/uploads"
restore_dir chat_files "$REPO/chat_files"
if [[ -f "$FROM/files/env" ]]; then install -m 600 "$FROM/files/env" "$REPO/.env"; fi
chown -R pdh:pdh "$REPO"
if [[ -d "$FROM/files/etc-pdh" ]]; then
  # Schlüssel von pdh-ha (ältere Sicherungen) nie zurückspielen
  restore_dir etc-pdh /etc/pdh --exclude=/ha/
  find /etc/pdh -path /etc/pdh/ha -prune -o -exec chown root:pdh {} + -exec chmod u=rwX,g=rX,o= {} +
fi
if compgen -G "$FROM/files/systemd/pdh*.service" >/dev/null; then
  for f in "$FROM"/files/systemd/pdh*.service; do
    [[ "$(basename "$f")" == pdh-ha.service ]] || cp "$f" /etc/systemd/system/
  done
fi
if [[ -f "$FROM/files/wireguard-bin" ]]; then install -m 755 "$FROM/files/wireguard-bin" /usr/local/bin/pdh-wireguard; fi
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
# baut PDH und Update-Agent, startet beide (pdh wird dabei neu gestartet)
bash "$REPO/install-update-agent.sh"
ok "PDH läuft (systemctl status pdh)"

# ── 5. Nextcloud ─────────────────────────────────────────────────────────
if [[ -d "$FROM/files/nextcloud-app" ]]; then
  info "Stelle Nextcloud wieder her …"
  NCDIR=$(src_path nextcloud-app); NCDIR="${NCDIR:-/var/www/nextcloud}"
  NCDATA=$(src_path nextcloud-data); NCDATA="${NCDATA:-$NCDIR/data}"
  apt-get install -y -qq nginx
  restore_dir nextcloud-app "$NCDIR"
  restore_dir nextcloud-data "$NCDATA"
  # Zertifikate vor nginx – die Seiten verweisen darauf (nginx -t)
  restore_dir ssl-pdh-nextcloud /etc/ssl/pdh-nextcloud
  restore_dir letsencrypt /etc/letsencrypt
  for d in /etc/ssl/pdh-nextcloud /etc/letsencrypt; do [[ -d "$d" ]] && chmod -R go-rwx "$d"; done
  restore_dir nginx /etc/nginx
  bash "$REPO/scripts/install-nextcloud-nginx-postgres.sh" --restore --install-dir "$NCDIR" --data-dir "$NCDATA"
  ok "Nextcloud läuft"
fi

# ── 6. n8n ───────────────────────────────────────────────────────────────
if [[ -f "$SOURCES" ]] && python3 -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1])).get("n8n") else 1)' "$SOURCES"; then
  info "Stelle n8n wieder her …"
  apt-get install -y -qq docker.io docker-compose-v2
  systemctl enable --now docker >/dev/null 2>&1
  COMPOSE=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["n8n"].get("compose_dir") or "")' "$SOURCES")
  [[ -n "$COMPOSE" ]] && restore_dir n8n-compose "$COMPOSE"
  while read -r vol; do
    [[ -n "$vol" ]] || continue
    docker volume create "$vol" >/dev/null
    mp=$(docker volume inspect -f '{{.Mountpoint}}' "$vol")
    restore_dir "n8n-vol-$vol" "$mp"
    chown -R 1000:1000 "$mp" # n8n läuft im Container als Benutzer node (1000)
  done < <(python3 -c 'import json,sys; [print(v["name"]) for v in json.load(open(sys.argv[1]))["n8n"].get("volumes") or []]' "$SOURCES")
  if [[ -n "$COMPOSE" && -d "$COMPOSE" ]]; then
    (cd "$COMPOSE" && docker compose up -d)
    ok "n8n läuft"
  else
    warn "n8n: Compose-Ordner fehlt – Container von Hand starten."
  fi
fi

echo
ok "Wiederaufbau fertig. Noch zu tun:"
echo "  • VPN: Server-Einstellungen → VPN → Installationsskript erneut ausführen"
echo "  • Cloudflare-Tunnel prüfen (Server-Einstellungen → Cloudflare)"
echo "  • Admin-Werkzeuge: sudo bash $REPO/scripts/configure-pdh-admin-tools.sh"
echo "  • Ausfallschutz auf diesem Server wieder einschalten (Server-Einstellungen → Ausfallschutz)"
echo "  • Ausfallschutz mit zwei Servern danach neu einrichten (pdh-ha wird nicht mitgesichert)"
