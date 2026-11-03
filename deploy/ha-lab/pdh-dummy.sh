#!/bin/bash
# Platzhalter für den Dienst pdh im Testlabor (kein systemd im Container).
# pdh-ha ruft ihn über PDH_HA_SERVICE_CMD mit start|stop|restart|is-active|enable|disable auf.
#   http    – beantwortet eine Anfrage an /health (socat, siehe lab-pg-entrypoint.sh)
#   migrate – „Datenbank-Änderung“ der laufenden Version (im Hintergrund von start)
# Laufender Stand: $PDH_HA_COMMIT_FILE (schreibt lab-agent). Schalter (Dateien):
#   /var/lib/pdh-lab/fail-start-<commit>   → start scheitert
#   /var/lib/pdh-lab/fail-health-<commit>  → /health antwortet 500
# Als Primary mit Stand ≠ lab0 legt pdh die Tabelle mig_<commit> in der
# Datenbank lab an; bis dahin meldet /health migrations_ok=false – auch am
# Spiegel (Standby): dort wartet pdh als eingeschränkter Spiegel, bis die
# Tabelle vom Primary ankommt (wie das echte PDH mit offenen Migrationen).
# Version je Stand: lab0 = Inhalt von VERSION, sonst <VERSION>-<Stand>
# (z. B. 0.77.8-lab1) – so prüft das Labor die Zielversion des Auftrags.
set -uo pipefail
flag=/run/pdh-dummy.running
migpid=/run/pdh-dummy.migrate.pid
dir=/var/lib/pdh-lab
cf=${PDH_HA_COMMIT_FILE:-$dir/commit}
version_file=/usr/local/share/pdh-lab/VERSION

commit() { tr -d '[:space:]' <"$cf" 2>/dev/null || echo lab0; }

# q <Datenbank> <SQL>: Ergebnis; Fehler → Rückgabewert ≠ 0
q() { timeout 3 runuser -u postgres -- psql -XAtq -d "$1" -v ON_ERROR_STOP=1 -c "$2" 2>/dev/null; }

stop_migrate() {
    [[ -s $migpid ]] && kill "$(cat "$migpid")" 2>/dev/null
    rm -f "$migpid"
}

start() {
    local c
    c="$(commit)"
    [[ -e $dir/fail-start-$c ]] && { echo "pdh startet nicht (Schalter fail-start-$c)" >&2; exit 1; }
    stop_migrate
    touch "$flag"
    if [[ "$c" != lab0 ]]; then
        setsid "$0" migrate "$c" </dev/null >/dev/null 2>&1 &
        echo $! >"$migpid"
    fi
}

# migrate <commit>: bis zu 2 min versuchen, solange pdh mit diesem Stand läuft
# und die Datenbank Primary ist (am Spiegel ändert pdh nichts)
migrate() {
    local c=$1 i
    for ((i = 0; i < 120; i++)); do
        [[ -f $flag && "$(commit)" == "$c" ]] || return 0
        case "$(q postgres "SELECT pg_is_in_recovery()")" in
            t) return 0 ;;
            f) q lab "CREATE TABLE IF NOT EXISTS mig_$c (x int)" >/dev/null && return 0 ;;
        esac
        sleep 1
    done
}

# respond <Status> <Text> [<JSON>]
respond() {
    local body=${3:-}
    printf 'HTTP/1.1 %s %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
        "$1" "$2" "${#body}" "$body"
}

http() {
    local line
    IFS= read -r -t 2 line || true
    while IFS= read -r -t 2 line; do
        line=${line%$'\r'}
        [[ -z "$line" ]] && break
    done
    [[ -f $flag ]] || { respond 503 "Service Unavailable"; return; }
    local c ver mig=true rec
    c="$(commit)"
    [[ -e $dir/fail-health-$c ]] && { respond 500 "Internal Server Error"; return; }
    rec="$(q postgres "SELECT pg_is_in_recovery()")" || { respond 503 "Service Unavailable"; return; }
    if [[ "$c" != lab0 && "$(q lab "SELECT to_regclass('public.mig_$c') IS NOT NULL")" != t ]]; then
        mig=false # Primary: Migration läuft noch; Spiegel: eingeschränkt, wartet auf den Primary
    fi
    ver="$(tr -d '[:space:]' <"$version_file" 2>/dev/null)"
    [[ "$c" == lab0 ]] || ver="$ver-$c"
    local mode=normal
    [[ "$rec" == t ]] && mode=spiegel
    # wie das echte PDH (cmd/server/main.go, response.JSON): verpackt in data
    respond 200 OK "{\"success\":true,\"data\":{\"commit\":\"$c\",\"migrations_ok\":$mig,\"mode\":\"$mode\",\"service\":\"pdh\",\"status\":\"ok\",\"version\":\"$ver\"}}"
}

case "${1:-}" in
    start | restart) start ;;
    stop)
        stop_migrate
        rm -f "$flag"
        ;;
    is-active) [[ -f "$flag" ]] ;;
    enable | disable) : ;;
    http) http ;;
    migrate) migrate "${2:?}" ;;
    *) echo "Aufruf: $0 start|stop|restart|is-active|enable|disable" >&2; exit 2 ;;
esac
