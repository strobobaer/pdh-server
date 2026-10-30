#!/usr/bin/env bash
# PDH – weiteren Server oder Zeugen dem Ausfallschutz (pdh-ha) hinzufügen.
#
#   sudo bash pdh-ha-join.sh [<Code>]           als zweiter Server beitreten
#   sudo bash pdh-ha-join.sh --witness [<Code>] als Zeuge beitreten
#   sudo bash pdh-ha-join.sh --update <Commit>  pdh-ha auf diesen Stand bringen
#                                               (auch mit --witness: baut nur pdh-ha)
#   … --voll                                    zusätzlich PDH + Datenbank-Spiegel
#                                               (Standby des aktiven Servers)
#
# Den Beitrittscode erzeugt das PDH (Server-Einstellungen → Ausfallschutz) oder
# „pdh-ha join-code [--witness]“ auf dem ersten Server. Das Skript installiert
# git/Go, holt das PDH-Repo auf den Commit aus dem Code nach /opt/pdh-ha-src,
# baut pdh-ha (scripts/install-pdh-ha.sh) und tritt bei. Ohne <Code> kommt er
# aus PDH_HA_JOIN oder wird abgefragt (dann nicht in der Prozessliste sichtbar).
# Mit --voll ersetzt die Kopie des aktiven Servers die Datenbank hier – liegen
# schon Datenbanken vor, werden sie gesichert und der Hostname abgefragt.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

REPO_URL="https://github.com/strobobaer/pdh-server.git"
SRC="/opt/pdh-ha-src"
ETC="${PDH_HA_ETC:-/etc/pdh/ha}"

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }
usage() { sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; }

WITNESS=0 VOLL=0 UPDATE="" CODE="${PDH_HA_JOIN:-}" GOT_CODE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --witness) WITNESS=1; shift ;;
    --voll) VOLL=1; shift ;;
    --update) UPDATE="${2:-}"; [[ -n "$UPDATE" ]] || fail "--update braucht einen Commit"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    -*) usage; fail "Unbekannte Option: $1" ;;
    *) (( ! GOT_CODE )) || fail "Nur einen Beitrittscode angeben"; CODE="$1"; GOT_CODE=1; shift ;;
  esac
done
(( ! (VOLL && WITNESS) )) || fail "--voll nicht zusammen mit --witness (der Zeuge hat keine Datenbank)"
if [[ -n "$UPDATE" ]]; then
  CODE=""
elif [[ -z "$CODE" ]]; then
  read -r -p "Beitrittscode: " CODE || true
  [[ -n "$CODE" ]] || { usage; fail "Beitrittscode fehlt"; }
fi

(( EUID == 0 )) || fail "Bitte als root ausführen: sudo bash $0 …"

. /etc/os-release
[[ "${ID:-}" == ubuntu ]] || fail "Dieses Skript ist für Ubuntu (gefunden: ${PRETTY_NAME:-?})."
case "$(uname -m)" in x86_64|aarch64) ;; *) warn "Getestet mit amd64/arm64, hier $(uname -m)." ;; esac

info "Pakete installieren"
missing=()
for p in git iputils-ping ufw python3 iproute2 iputils-arping; do
  dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p")
done
command -v go >/dev/null 2>&1 || missing+=(golang-go)
if (( ${#missing[@]} > 0 )); then
  apt-get update -qq
  apt-get install -y -qq "${missing[@]}"
fi
ok "Pakete vorhanden"

# Ordner für Skripte beim Rollenwechsel (pdh-ha führt nur Dateien daraus aus, die root gehören)
install -d -m 0755 -o root -g root /etc/pdh/ha-hooks

# Tunnel (Zugangsweg): cloudflared nur auf vollen Servern; pdh-ha startet/stoppt ihn selbst.
# Wie in internal/web/cloudflare_tunnel.go: offizielles .deb von GitHub (keine Paketquelle nötig).
install_cloudflared() {
  local arch tmp
  if command -v cloudflared >/dev/null 2>&1; then ok "cloudflared vorhanden"; return 0; fi
  case "$(dpkg --print-architecture)" in amd64|arm64) arch="$(dpkg --print-architecture)" ;; *) warn "cloudflared: Architektur $(dpkg --print-architecture) nicht unterstützt – übersprungen"; return 0 ;; esac
  info "Installiere cloudflared"
  apt-get install -y -qq curl ca-certificates
  tmp="$(mktemp -d)"
  if curl -fsSL -o "$tmp/cloudflared.deb" "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${arch}.deb" \
    && dpkg -i "$tmp/cloudflared.deb" >/dev/null; then
    # pdh-ha steuert den Dienst – nicht von selbst starten
    systemctl disable --now cloudflared >/dev/null 2>&1 || true
    ok "cloudflared installiert"
  else
    # Nicht auf „--update … --voll“ verweisen: das legt die Datenbank-Kopie neu an (full_node).
    warn "cloudflared konnte nicht installiert werden – später von Hand nachholen (ohne „cloudflared service install“, nicht erneut mit --voll):"
    warn "  curl -fsSL -o /tmp/cloudflared.deb https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${arch}.deb && sudo dpkg -i /tmp/cloudflared.deb"
  fi
  rm -rf "$tmp"
}
(( ! VOLL )) || install_cloudflared

# GOTOOLCHAIN=auto (lädt die Go-Version aus go.mod) braucht mindestens Go 1.21
go_minor="$(go env GOVERSION 2>/dev/null | sed -nE 's/^go1\.([0-9]+).*/\1/p')"
if [[ -z "$go_minor" || "$go_minor" -lt 21 ]]; then
  fail "Go ist zu alt (Ubuntu 22.04). Bitte Go 1.21 oder neuer installieren, z. B.: sudo snap install go --classic"
fi

# Commit aus dem Code: base64url ohne Auffüllung → JSON, Feld "c"
code_commit() {
  local b64 pad
  b64="$(printf '%s' "$1" | tr '_-' '/+')"
  pad=$(( (4 - ${#b64} % 4) % 4 ))
  b64="$b64$(printf '%*s' "$pad" '' | tr ' ' '=')"
  printf '%s' "$b64" | base64 -d 2>/dev/null \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["c"])' 2>/dev/null
}

# Repo nach $SRC auf den Commit bringen
checkout() {
  local commit="$1"
  [[ "$commit" =~ ^[0-9a-f]{7,40}$ ]] || fail "Ungültiger Commit: $commit"
  if [[ -d "$SRC/.git" ]]; then
    git -c safe.directory="$SRC" -C "$SRC" fetch --quiet origin
  else
    rm -rf "$SRC"
    git clone --quiet "$REPO_URL" "$SRC"
  fi
  git -c safe.directory="$SRC" -C "$SRC" -c advice.detachedHead=false checkout --quiet "$commit" \
    || fail "Commit $commit nicht im Repo gefunden (erst auf GitHub hochgeladen?)"
  ok "Quelltext auf Stand $commit ($SRC)"
}

# Datenbank-Spiegel: PostgreSQL in der Hauptversion des aktiven Servers, PDH
# ohne eigene Datenbank, Erstkopie per replica-init, dann Agent und PDH bauen.
full_node() {
  local commit="$1" major dbs confirm=""
  info "Datenbank-Spiegel einrichten"
  systemctl restart pdh-ha
  sleep 3
  major="$(/usr/local/bin/pdh-ha peer-pg-major)" || fail "Aktiver Server nicht erreichbar (siehe oben)."
  [[ "$major" =~ ^[0-9]+$ ]] || fail "Unerwartete PostgreSQL-Version vom aktiven Server: $major"
  if ! dpkg -s "postgresql-$major" >/dev/null 2>&1; then
    info "Installiere PostgreSQL $major (wie auf dem aktiven Server)"
    apt-get update -qq
    if ! apt-cache show "postgresql-$major" >/dev/null 2>&1; then
      info "PostgreSQL $major fehlt in Ubuntu – richte das Paketarchiv apt.postgresql.org ein"
      apt-get install -y -qq curl ca-certificates lsb-release
      install -d /usr/share/postgresql-common/pgdg
      curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc
      echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" \
        >/etc/apt/sources.list.d/pgdg.list
      apt-get update -qq
    fi
    apt-get install -y -qq "postgresql-$major"
  fi
  ok "PostgreSQL $major vorhanden"
  bash "$SRC/scripts/install-pdh-program.sh" /opt/pdh-server "$commit"
  dbs="$(runuser -u postgres -- psql -XAtqc "SELECT datname FROM pg_database WHERE datname NOT IN ('postgres','template0','template1')" 2>/dev/null | tr '\n' ' ' || true)"
  if [[ -n "${dbs// /}" ]]; then
    warn "Auf diesem Server liegen schon Datenbanken: $dbs"
    warn "Sie werden gesichert (/var/lib/pdh-ha/vorher) und durch die Kopie des aktiven Servers ersetzt."
    read -r -p "Zum Bestätigen den Hostnamen dieses Servers ($(hostname)) eintippen: " confirm || true
  fi
  /usr/local/bin/pdh-ha replica-init --confirm-host "$confirm" || fail "Datenbank-Spiegel nicht eingerichtet (siehe oben)."
  info "PDH und Update-Agent bauen (dauert ein paar Minuten)"
  bash /opt/pdh-server/install-update-agent.sh
  systemctl restart pdh-ha
  ok "Datenbank-Spiegel läuft – im PDH Rollen tauschen (hin und zurück) testen"
}

if [[ -n "$UPDATE" ]]; then
  info "pdh-ha aktualisieren"
  checkout "$UPDATE"
  bash "$SRC/scripts/install-pdh-ha.sh" "$SRC"
  ok "pdh-ha aktualisiert: $(/usr/local/bin/pdh-ha version)"
  (( VOLL )) && full_node "$UPDATE"
  exit 0
fi

if [[ -x /usr/local/bin/pdh-ha && -f "$ETC/node.json" ]]; then
  fail "Dieser Server ist schon im Ausfallschutz eingerichtet. Zum Neu-Beitreten zuerst beim anderen Server entfernen und hier „pdh-ha reset --yes“ ausführen."
fi

commit="$(code_commit "$CODE" || true)"
[[ -n "$commit" ]] || fail "Beitrittscode nicht lesbar oder ohne Commit – bitte vollständig kopieren (eine Zeile)."

info "Quelltext holen"
checkout "$commit"

info "pdh-ha bauen und installieren"
bash "$SRC/scripts/install-pdh-ha.sh" "$SRC"

info "Beitreten"
args=(join)
(( WITNESS )) && args+=(--witness)
out="$(PDH_HA_JOIN="$CODE" /usr/local/bin/pdh-ha "${args[@]}")" || fail "Beitritt fehlgeschlagen (siehe oben)."
# Dienst lädt die neue Konfiguration
systemctl restart pdh-ha
ok "$out"
(( VOLL )) && full_node "$commit"
exit 0
