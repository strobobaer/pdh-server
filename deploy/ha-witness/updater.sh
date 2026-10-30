#!/usr/bin/env bash
# Helfer des Zeugen: zieht den Zeugen-Container auf den Stand der beiden
# Server nach (Update im Verbund, Spec 9). Läuft als eigener Container neben
# dem Zeugen und spricht über den Docker-Socket mit dem Docker des Rechners.
#
#   PDH_HA_AUTOUPDATE   1 (Standard) = nachziehen, 0 = nichts tun
#   PDH_HA_REPO         Quelltext-Repository (Standard: GitHub)
#
# Ablauf alle 300 s: Haben beide Server denselben Stand X, der Zeuge nicht,
# und läuft kein Update-Auftrag, wird der Zeuge aus /src auf X neu gebaut.
# Sieht der Zeuge danach nicht beide Server, baut der Helfer den alten Stand
# wieder auf. Für Tests: PDH_HA_UPDATER_ONCE=1 (ein Durchlauf),
# PDH_HA_UPDATER_SRC, PDH_HA_UPDATER_VERIFY_S.
set -euo pipefail

AUTOUPDATE="${PDH_HA_AUTOUPDATE:-1}"
REPO="${PDH_HA_REPO:-https://github.com/strobobaer/pdh-server.git}"
SRC="${PDH_HA_UPDATER_SRC:-/src}"
CONTAINER="${PDH_HA_WITNESS_CONTAINER:-pdh-ha-witness-witness-1}"
COMPOSE="$SRC/deploy/ha-witness/compose.yaml"
INTERVAL=300
VERIFY_S="${PDH_HA_UPDATER_VERIFY_S:-120}"
RETRY_S=3600   # einen gescheiterten Stand erst nach einer Stunde wieder versuchen

FAILED_X="" FAILED_AT=0

log() { printf '%s %s\n' "$(date '+[%Y-%m-%d %H:%M:%S]')" "$*"; }

# Zustand des Zeugen lesen: Zeile 1 eigener Commit, Zeile 2 "true" bei
# laufendem Auftrag, danach je Server "<Commit> <gesehen>".
STATE=()
read_state() {
  local json lines
  json="$(docker exec "$CONTAINER" pdh-ha status --json 2>/dev/null)" || return 1
  lines="$(printf '%s' "$json" | jq -r '.self.commit // "", (.update.active // false | tostring), (.peers[] | select(.kind == "node") | "\(.commit // "") \(.seen // false)")')" || return 1
  mapfile -t STATE <<<"$lines"
  [[ ${#STATE[@]} -ge 2 ]]
}

# Beide Server gesehen? (Mindestens zwei Einträge.)
servers_seen() {
  local n=0 i
  for ((i = 2; i < ${#STATE[@]}; i++)); do
    [[ "${STATE[i]##* }" == "true" ]] || return 1
    n=$((n + 1))
  done
  [[ $n -ge 2 ]]
}

# Gemeinsamer Stand der Server (leer, wenn uneinig oder unbekannt).
common_commit() {
  local c="" i x
  for ((i = 2; i < ${#STATE[@]}; i++)); do
    x="${STATE[i]%% *}"
    [[ -n "$x" ]] || return 0
    if [[ -z "$c" ]]; then c="$x"; elif [[ "$c" != "$x" ]]; then return 0; fi
  done
  printf '%s' "$c"
}

build() {  # $1 = Commit
  git -C "$SRC" checkout --detach "$1" >/dev/null 2>&1 || { log "Stand $1 nicht auszuchecken"; return 1; }
  PDH_HA_COMMIT="$1" docker compose -p pdh-ha-witness -f "$COMPOSE" up -d --build witness
}

update_to() {  # $1 = neuer Stand, $2 = alter Stand
  local x="$1" old="$2" i tries ok=0
  if [[ ! -d "$SRC/.git" ]]; then
    log "Quelltext fehlt, hole ihn von $REPO"
    git clone --quiet "$REPO" "$SRC" || { log "Klonen gescheitert"; return 1; }
  fi
  [[ -n "$old" ]] || old="$(git -C "$SRC" rev-parse HEAD 2>/dev/null || true)"
  git -C "$SRC" fetch --quiet origin || { log "Abholen gescheitert, später erneut"; return 1; }
  log "Zeuge wird von ${old:-?} auf $x gebracht"
  if build "$x"; then
    tries=$((VERIFY_S / 5))
    for ((i = 0; i < tries; i++)); do
      sleep 5
      if read_state && servers_seen; then ok=1; break; fi
    done
  fi
  if ((ok)); then
    log "Zeuge läuft auf $x und sieht beide Server"
    return 0
  fi
  log "Zeuge sieht nach dem Neubau nicht beide Server – baue den alten Stand zurück"
  FAILED_X="$x"; FAILED_AT="$(date +%s)"
  if [[ -n "$old" ]] && build "$old"; then
    log "Alter Stand $old wieder aufgebaut"
  else
    log "Rückbau gescheitert – bitte den Zeugen von Hand prüfen"
  fi
  return 1
}

once() {
  local x self active now
  if ! read_state; then
    log "Zeuge nicht erreichbar (Container $CONTAINER) – nichts zu tun"
    return 0
  fi
  self="${STATE[0]}" active="${STATE[1]}"
  if ! servers_seen; then return 0; fi
  x="$(common_commit)"
  [[ -n "$x" && "$x" != "$self" ]] || return 0
  if [[ "$active" == "true" ]]; then
    log "Update-Auftrag läuft – Zeuge wartet mit dem Nachziehen auf $x"
    return 0
  fi
  now="$(date +%s)"
  if [[ "$x" == "$FAILED_X" ]] && ((now - FAILED_AT < RETRY_S)); then
    return 0
  fi
  update_to "$x" "$self" || true
}

if [[ "$AUTOUPDATE" == "0" ]]; then
  log "Selbst-Update des Zeugen ist aus (PDH_HA_AUTOUPDATE=0)"
fi
while true; do
  if [[ "$AUTOUPDATE" != "0" ]]; then
    once || log "Durchlauf mit Fehler beendet – weiter beim nächsten Takt"
  fi
  [[ "${PDH_HA_UPDATER_ONCE:-0}" == "1" ]] && exit 0
  sleep "$INTERVAL"
done
