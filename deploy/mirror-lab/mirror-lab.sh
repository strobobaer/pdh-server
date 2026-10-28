#!/bin/bash
# Spiegel-Labor mit echtem PostgreSQL-Standby (nur Testumgebung). Aufruf:
#   mirror-lab.sh up|down|<Szenario>|alle
# Szenarien: start crawl posts anmeldung version hochstufen
# Jedes Szenario gibt BESTANDEN bzw. NICHT BESTANDEN: <Grund> aus
# (Rückgabewert 0 bzw. 1). hochstufen macht pg2 zum Primary – danach ist der
# Spiegel weg; ein weiteres Szenario baut das Labor dann neu auf.
# Voraussetzungen: Linux mit Docker und Compose-Plugin.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$here/compose.yaml"

P=pdh-mirror-lab
PG1=$P-pg1 PG2=$P-pg2 PDH1=$P-pdh1 PDH2=$P-pdh2
ADMIN=laboradmin ADMIN_PASS='Labor-Admin-2026!'
NUTZER=labornutzer NUTZER_PASS='Labor-Nutzer-2026!'
DUMMY_MIG=/app/migrations/9999_mirror_lab_dummy.up.sql
SCENARIOS=(start crawl posts anmeldung version hochstufen)

dc() { docker compose -f "$compose_file" "$@"; }

log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }

# Abbruch eines Szenarios (läuft in einer Subshell)
fail() {
    echo "NICHT BESTANDEN: $*"
    exit 1
}

need() {
    command -v docker >/dev/null || { echo "docker fehlt" >&2; exit 1; }
    docker compose version >/dev/null 2>&1 || { echo "Docker-Compose-Plugin fehlt" >&2; exit 1; }
}

# --- Hilfen ---

# psql1/psql2 <SQL>: eine Abfrage auf pg1 bzw. pg2, Ergebnis ohne Rahmen
psql1() { docker exec -i "$PG1" psql -U pdh -d pdh -tAX -v ON_ERROR_STOP=1 -c "$1"; }
psql2() { docker exec -i "$PG2" psql -U pdh -d pdh -tAX -v ON_ERROR_STOP=1 -c "$1"; }

# lcurl: curl im Labornetz (aus dem Container pdh1, dort ist curl installiert)
lcurl() { docker exec "$PDH1" curl -sS --max-time 15 "$@"; }

# health <pdh1|pdh2>: Antwort von /health, leer wenn nicht erreichbar
health() { lcurl "http://$1:8090/health" 2>/dev/null || true; }

# wait_until <Sekunden> <Befehl…>: wiederholt den Befehl, bis er gelingt
wait_until() {
    local n=$1
    shift
    local end=$((SECONDS + n))
    until "$@" >/dev/null 2>&1; do
        ((SECONDS < end)) || return 1
        sleep 0.5
    done
}

mode_is() { health "$1" | grep -q "\"mode\":\"$2\""; }
pg_ready() { docker exec "$1" pg_isready -q -h 127.0.0.1 -U pdh; }
in_recovery() { [[ "$(psql2 'SELECT pg_is_in_recovery()' 2>/dev/null)" == t ]]; }
log_has() { docker logs "$1" 2>&1 | grep -q "$2"; }

# login <host> <jar> <Benutzer> <Passwort>: Formular-Anmeldung; gibt
# „<Status> <Location>“ aus. Cookies landen in /tmp/<jar> im Container pdh1.
login() {
    docker exec "$PDH1" rm -f "/tmp/$2"
    lcurl -o /dev/null -c "/tmp/$2" -w '%{http_code} %{redirect_url}' \
        --data-urlencode "email=$3" --data-urlencode "password=$4" "http://$1:8090/login"
}

# has_session <jar>: Sitzungs-Cookie pdh_token mit Wert vorhanden
has_session() { docker exec "$PDH1" awk '$6 == "pdh_token" && $7 != "" {f=1} END {exit !f}' "/tmp/$1"; }

# pdh_restart <Container>: PDH-Prozess beenden; die Neustart-Schleife startet
# ihn nach 1 s neu. Wartet, bis der neue Prozess /health beantwortet.
pdh_restart() {
    local c=$1 host=${1#"$P-"} old
    old="$(docker exec "$c" cat /tmp/pdh.pid)"
    docker exec "$c" kill "$old"
    wait_until 30 bash -c "[[ \"\$(docker exec $c cat /tmp/pdh.pid)\" != $old ]]" || return 1
    wait_until 60 bash -c "docker exec $PDH1 curl -sf --max-time 5 http://$host:8090/health"
}

# writes_in_log: „schreibzugriff im spiegel“ aus dem Protokoll von pdh2, je mit
# der darauf folgenden Anfrage (der Crawler fragt nacheinander)
writes_in_log() {
    docker logs "$PDH2" 2>&1 | awk '
        /schreibzugriff im spiegel/ {
            s = $0; sub(/.*"sql":"/, "", s); sub(/".*/, "", s)
            if ($0 ~ /"stelle":"/) { w = $0; sub(/.*"stelle":"/, "", w); sub(/".*/, "", w); s = s "  [" w "]" }
            if ($0 ~ /"anfrage":false/) print "(Hintergrund)  →  " s
            else pend[++n] = s
            next
        }
        /"bereich":"http"/ && n > 0 {
            p = $0; sub(/.*"path":"/, "", p); sub(/".*/, "", p)
            m = $0; sub(/.*"method":"/, "", m); sub(/".*/, "", m)
            for (i = 1; i <= n; i++) print m " " p "  →  " pend[i]
            n = 0
        }
        END { for (i = 1; i <= n; i++) print "(ohne Anfrage)  →  " pend[i] }' | sort | uniq -c | sort -rn
}

# --- Aufbau ---

lab_down() {
    dc --profile werkzeug down -v --remove-orphans >/dev/null 2>&1 || true
}

# seed: Beispieldaten über pdh1 (API), damit der Crawler Detailseiten findet
seed() {
    local tok infra
    tok="$(lcurl -H 'Content-Type: application/json' \
        -d "{\"email\":\"$ADMIN\",\"password\":\"$ADMIN_PASS\"}" http://pdh1:8090/api/v1/users/login |
        sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
    [[ -n "$tok" ]] || { log "Beispieldaten: API-Anmeldung fehlgeschlagen"; return 1; }
    api() { lcurl -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d "$2" "http://pdh1:8090/api/v1$1"; }
    infra="$(api /infrastructure '{"name":"Laboranlage","type":"plant","location":"Halle 1"}' |
        sed -n 's/.*"id":"\([0-9a-f-]\{36\}\)".*/\1/p' | head -n1)"
    log "  Anlage: ${infra:-–}"
    local ij=""
    [[ -n "$infra" ]] && ij=",\"infrastructure_id\":\"$infra\""
    api /tickets "{\"title\":\"Laborticket\",\"description\":\"aus dem Spiegel-Labor\",\"priority\":\"medium\",\"tags\":[]$ij}" >/dev/null || true
    api /faults "{\"title\":\"Laborstörung\",\"description\":\"Lager läuft heiß\",\"severity\":\"medium\",\"symptoms\":[]$ij}" >/dev/null || true
    api /projects '{"name":"Laborprojekt","description":"Spiegel-Labor"}' >/dev/null || true
    api /tasks "{\"title\":\"Laboraufgabe\",\"description\":\"prüfen\",\"priority\":\"medium\"$ij}" >/dev/null || true
    api /inventory '{"part_number":"LAB-1","name":"Laborteil","unit":"Stk"}' >/dev/null || true
    # Kommentar am Ticket über die Weboberfläche
    login pdh1 jar-seed "$ADMIN" "$ADMIN_PASS" >/dev/null
    local tid
    tid="$(psql1 "SELECT id FROM tickets ORDER BY created_at LIMIT 1")"
    [[ -n "$tid" ]] && lcurl -o /dev/null -b /tmp/jar-seed -d 'content=Laborkommentar' "http://pdh1:8090/tickets/$tid/comment" || true
    log "  Beispieldaten: $(psql1 "SELECT (SELECT count(*) FROM tickets) || ' Tickets, ' || (SELECT count(*) FROM faults) || ' Störungen, ' || (SELECT count(*) FROM infrastructure) || ' Anlagen'")"
}

lab_up() {
    need
    log "Labor neu aufbauen (Projekt $P)"
    lab_down
    log "Abbilder bauen (PDH-Server, fakeha, Crawler)"
    dc --profile werkzeug build -q pdh1 crawler || { echo "Bauen gescheitert"; return 1; }
    log "pg1 (Primary) starten"
    dc up -d pg1 >/dev/null
    wait_until 120 pg_ready "$PG1" || { echo "pg1 startet nicht"; docker logs "$PG1" 2>&1 | tail -n 30; return 1; }
    log "pdh1 starten (legt das Schema an)"
    dc up -d --no-build pdh1 >/dev/null
    wait_until 180 log_has "$PDH1" "noch kein benutzer vorhanden" ||
        { echo "pdh1: Einrichtung startet nicht"; docker logs "$PDH1" 2>&1 | tail -n 40; return 1; }
    log "Benutzer anlegen: $ADMIN (admin), $NUTZER (technician)"
    local h1 h2
    h1="$(docker exec -i "$PG1" psql -U pdh -d postgres -tAX -v ON_ERROR_STOP=1 \
        -c "CREATE EXTENSION IF NOT EXISTS pgcrypto" -c "SELECT crypt('$ADMIN_PASS', gen_salt('bf', 10))" | tail -n1)"
    h2="$(docker exec -i "$PG1" psql -U pdh -d postgres -tAX -v ON_ERROR_STOP=1 \
        -c "SELECT crypt('$NUTZER_PASS', gen_salt('bf', 10))")"
    psql1 "INSERT INTO users (username, email, password_hash, first_name, last_name, role, active, department, phone) VALUES
        ('$ADMIN', 'admin@labor.invalid', '$h1', 'Labor', 'Admin', 'admin', true, '', ''),
        ('$NUTZER', 'nutzer@labor.invalid', '$h2', 'Labor', 'Nutzer', 'technician', true, '', '')" >/dev/null ||
        { echo "Benutzer anlegen gescheitert"; return 1; }
    docker restart "$PDH1" >/dev/null # Einrichtungsassistent beenden
    wait_until 120 mode_is pdh1 normal || { echo "pdh1 startet nicht"; docker logs "$PDH1" 2>&1 | tail -n 40; return 1; }
    log "Beispieldaten über pdh1"
    seed || true
    log "pg2 (Standby per pg_basebackup -R) starten"
    dc up -d pg2 >/dev/null
    wait_until 180 in_recovery || { echo "pg2 wird kein Standby"; docker logs "$PG2" 2>&1 | tail -n 30; return 1; }
    local lsn
    lsn="$(psql1 'SELECT pg_current_wal_lsn()')"
    wait_until 60 bash -c "[[ \"\$(docker exec $PG2 psql -U pdh -d pdh -tAX -c \"SELECT pg_last_wal_replay_lsn() >= '$lsn'\")\" == t ]]" ||
        { echo "pg2 holt pg1 nicht ein"; return 1; }
    log "pdh2 (Spiegel) starten"
    dc up -d --no-build pdh2 >/dev/null
    wait_until 120 mode_is pdh2 spiegel || { echo "pdh2 startet nicht im Spiegelmodus"; docker logs "$PDH2" 2>&1 | tail -n 40; return 1; }
    log "Labor bereit: $(docker exec "$PG1" postgres --version)"
}

# lab_ready: pdh2 läuft als Spiegel auf einem Standby (sonst neu aufbauen)
lab_ready() {
    docker inspect -f '{{.State.Running}}' "$PDH2" 2>/dev/null | grep -q true && in_recovery && mode_is pdh2 spiegel
}

# --- Szenarien ---

sc_start() {
    local h
    h="$(health pdh2)"
    [[ "$h" == *'"mode":"spiegel"'* ]] || fail "pdh2 /health ohne \"mode\":\"spiegel\": $h"
    log "  pdh2 /health: $h"
    ! log_has "$PDH2" "migrationen geprüft" || fail "pdh2 hat Migrationen geprüft (darf im Spiegel nicht schreiben)"
    log_has "$PDH2" "spiegelmodus: PDH startet nur lesend" || fail "pdh2 meldet den Spiegelmodus nicht"
}

sc_crawl() {
    local out rc=0 w
    out="$(dc --profile werkzeug run --rm --no-deps -e PDH_MIRROR_ADMIN="$ADMIN" -e PDH_MIRROR_PASS="$ADMIN_PASS" \
        crawler go test -tags mirrorlab -count=1 -v -timeout 30m -run TestMirrorCrawl ./internal/web 2>&1)" || rc=$?
    grep -E 'besucht:|keine Antwort:|über 5 MB|Serverfehler:|Sitzung|^(ok|FAIL|---)' <<<"$out" | sed 's/^/  /' || true
    ((rc == 0)) || fail "Crawler meldet Fehler (siehe oben)"
    w="$(writes_in_log)"
    if [[ -n "$w" ]]; then
        echo "  Schreibzugriffe im Spiegel (Anzahl, Anfrage → SQL):"
        sed 's/^/    /' <<<"$w"
        fail "Protokoll von pdh2 enthält „schreibzugriff im spiegel“"
    fi
}

sc_posts() {
    local r tid aid infra codes="" bad=""
    r="$(login pdh2 jar-posts "$ADMIN" "$ADMIN_PASS")"
    has_session jar-posts || fail "Admin-Anmeldung am Spiegel: $r"
    tid="$(psql2 "SELECT id FROM tickets ORDER BY created_at LIMIT 1")"
    aid="$(psql2 "SELECT id FROM users WHERE username = '$ADMIN'")"
    infra="$(psql2 "SELECT id FROM infrastructure ORDER BY created_at LIMIT 1")"
    [[ -n "$tid" && -n "$infra" ]] || fail "Beispieldaten fehlen am Spiegel (Ticket/Anlage)"
    docker exec "$PDH1" sh -c 'echo labor > /tmp/upload.txt'
    post() { # <Name> <curl-Argumente…>
        local name=$1 c
        shift
        c="$(lcurl -o /dev/null -w '%{http_code}' -b /tmp/jar-posts "$@")"
        codes+="$name=$c "
        [[ "$c" == 409 ]] || bad+="$name→$c "
    }
    post ticket -d 'title=Im Spiegel&priority=medium' http://pdh2:8090/tickets
    post kommentar -d 'content=Im Spiegel' "http://pdh2:8090/tickets/$tid/comment"
    post profil -d 'theme=dark' http://pdh2:8090/account/appearance
    post easy-mode -d 'description=Im Spiegel gemeldet&name=Labor' "http://pdh2:8090/e/$infra/report"
    post upload -F 'file=@/tmp/upload.txt' "http://pdh2:8090/users/$aid/avatar"
    log "  $codes"
    [[ -z "$bad" ]] || fail "nicht abgewiesen (erwartet 409): $bad"
    [[ "$(psql2 "SELECT count(*) FROM tickets WHERE title = 'Im Spiegel'")" == 0 ]] || fail "Ticket im Spiegel angelegt"
}

# set_seen <true|false>: Platzhalter-Status umschalten; pdh2 liest ihn höchstens
# 5 s zwischengespeichert
set_seen() {
    docker exec "$PDH2" sh -c "echo $1 > /tmp/lab-seen"
    sleep 6
}

sc_anmeldung() {
    local r
    set_seen true
    r="$(login pdh2 jar-nutzer "$NUTZER" "$NUTZER_PASS")"
    log "  aktiver Server erreichbar: $r"
    [[ "$r" == "302 "*"/login?spiegel=1" ]] || fail "Nicht-Admin bei erreichbarem aktivem Server nicht abgewiesen: $r"
    ! has_session jar-nutzer || fail "Nicht-Admin hat trotz Abweisung eine Sitzung"
    r="$(login pdh2 jar-admin "$ADMIN" "$ADMIN_PASS")"
    has_session jar-admin || fail "Admin bei erreichbarem aktivem Server abgewiesen: $r"
    set_seen false
    r="$(login pdh2 jar-nutzer "$NUTZER" "$NUTZER_PASS")"
    log "  aktiver Server weg: $r"
    has_session jar-nutzer || { set_seen true; fail "Nicht-Admin bei ausgefallenem aktivem Server abgewiesen: $r"; }
    set_seen true
}

sc_version() {
    local body r
    # Bei Abbruch (fail beendet die Subshell): Dummy-Migration entfernen, pdh2 neu starten
    trap 'docker exec "$PDH2" rm -f "$DUMMY_MIG" >/dev/null 2>&1; pdh_restart "$PDH2" >/dev/null 2>&1' EXIT
    log "  Dummy-Migration $DUMMY_MIG in pdh2, Neustart"
    docker exec "$PDH2" sh -c "echo 'SELECT 1;' > $DUMMY_MIG"
    pdh_restart "$PDH2" || fail "pdh2 startet mit Dummy-Migration nicht neu"
    mode_is pdh2 spiegel || fail "pdh2 nicht im Spiegelmodus: $(health pdh2)"
    docker logs --tail 200 "$PDH2" 2>&1 | grep 'spiegelmodus: PDH startet nur lesend' | tail -n1 | grep -q '"schema_veraltet":true' ||
        fail "pdh2 meldet kein veraltetes Schema"
    login pdh2 jar-version "$ADMIN" "$ADMIN_PASS" >/dev/null
    has_session jar-version || fail "Admin-Anmeldung im eingeschränkten Spiegel abgewiesen"
    body="$(lcurl -b /tmp/jar-version -H 'Accept: text/html' 'http://pdh2:8090/admin/server-config?tab=ha')"
    grep -q 'Spiegel wartet auf das Update des aktiven Servers' <<<"$body" || fail "Band „wartet auf das Update“ fehlt"
    r="$(lcurl -o /dev/null -b /tmp/jar-version -H 'Accept: text/html' -w '%{http_code} %{redirect_url}' http://pdh2:8090/tickets)"
    log "  GET /tickets: $r"
    [[ "$r" == "302 "*"/admin/server-config?tab=ha" ]] || fail "GET /tickets leitet nicht auf den Ausfallschutz um: $r"
    log "  Dummy-Migration entfernen, Neustart"
    docker exec "$PDH2" rm -f "$DUMMY_MIG"
    pdh_restart "$PDH2" || fail "pdh2 startet ohne Dummy-Migration nicht neu"
    mode_is pdh2 spiegel || fail "pdh2 nach dem Aufräumen nicht im Spiegelmodus"
    trap - EXIT
}

sc_hochstufen() {
    local t0 dt c
    log "  pg_promote() auf pg2"
    t0=$(date +%s%N)
    [[ "$(psql2 'SELECT pg_promote()')" == t ]] || fail "pg_promote() gescheitert"
    until mode_is pdh2 normal; do
        dt=$((($(date +%s%N) - t0) / 1000000))
        ((dt < 10000)) || fail "pdh2 nach 10 s nicht im Normalmodus: $(health pdh2)"
        sleep 0.5
    done
    dt=$((($(date +%s%N) - t0) / 1000000))
    log "  pdh2 \"mode\":\"normal\" nach $((dt / 1000)),$((dt % 1000 / 100)) s"
    docker logs "$PDH2" 2>&1 | grep -q 'spiegelmodus gewechselt' || fail "pdh2 meldet keinen Moduswechsel"
    login pdh2 jar-hoch "$ADMIN" "$ADMIN_PASS" >/dev/null
    has_session jar-hoch || fail "Anmeldung nach dem Hochstufen gescheitert"
    c="$(lcurl -o /dev/null -b /tmp/jar-hoch -w '%{http_code}' -d 'title=Nach dem Hochstufen&priority=medium' http://pdh2:8090/tickets)"
    log "  POST /tickets: $c"
    [[ "$c" =~ ^(200|201|302|303)$ ]] || fail "POST /tickets nach dem Hochstufen: $c"
    [[ "$(psql2 "SELECT count(*) FROM tickets WHERE title = 'Nach dem Hochstufen'")" == 1 ]] || fail "Ticket nach dem Hochstufen nicht gespeichert"
}

# run_scenario <Name>: in einer Subshell, Ergebnis BESTANDEN/NICHT BESTANDEN
run_scenario() {
    local s=$1 rc=0
    log "=== Szenario $s"
    if ! lab_ready; then
        lab_up || { echo "NICHT BESTANDEN: Labor ließ sich nicht aufbauen"; return 1; }
    fi
    ("sc_$s") || rc=$?
    if ((rc == 0)); then
        echo "BESTANDEN"
    fi
    return $rc
}

main() {
    need
    case "${1:-}" in
    up) lab_up ;;
    down) lab_down ;;
    alle)
        local s fails=() res=()
        lab_up || { echo "NICHT BESTANDEN: Labor ließ sich nicht aufbauen"; exit 1; }
        for s in "${SCENARIOS[@]}"; do
            if run_scenario "$s"; then res+=("$s: BESTANDEN"); else res+=("$s: NICHT BESTANDEN") fails+=("$s"); fi
        done
        echo "=== Ergebnis"
        printf '  %s\n' "${res[@]}"
        [[ "${MIRROR_LAB_KEEP:-}" == 1 ]] || lab_down
        if ((${#fails[@]})); then
            echo "NICHT BESTANDEN: ${fails[*]}"
            exit 1
        fi
        echo "BESTANDEN"
        ;;
    *)
        local s
        for s in "${SCENARIOS[@]}"; do
            if [[ "$1" == "$s" ]]; then
                run_scenario "$s"
                exit $?
            fi
        done
        echo "Aufruf: $0 up|down|alle|$(IFS='|'; echo "${SCENARIOS[*]}")" >&2
        exit 2
        ;;
    esac
}

main "$@"
