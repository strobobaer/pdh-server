#!/bin/bash
# Testlabor für pdh-ha (nur Testumgebung). Aufruf:
#   ha-lab.sh up|down|status|race|<Szenario>|alle
# Szenarien: aktiv-weg leitung-weg zeuge-weg hin-und-her rueckkehr
#            neustart-aktiv version-anders
#   mit Datenbank-Spiegel (Teil 3): beobachtung tausch aktiv-weg-echt
#            asynchron-zeuge asynchron-ohne-zeuge erzwingen-abgezweigt
#            rewind-fehler slot-grenze
# Jedes Szenario läuft mit Datenbank-Spiegel und Schreib-Generator; danach muss
# jede bestätigte Zeile auf dem aktiven Server liegen. Wächter: nie zwei
# schreibbare Datenbanken länger als RW_GRACE Sekunden.
# Voraussetzungen: Linux mit Docker und Compose-Plugin, python3 (oder jq).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
compose_file="$here/compose.yaml"

# Laborzeiten: Takt 1 s, Fence 5 s, Übernahme Trio 15 s / Duo 20 s, Wartezeit 0,
# 3 je Tag, asynchron nach 3 s
GW_IP=10.77.1.254
# settings_json <auto_mode> [<slot_keep_gb>]
settings_json() {
    printf '{"settings":{"tick_s":1,"fence_s":5,"takeover_trio_s":15,"takeover_duo_s":20,"cooldown_min":0,"max_per_day":3,"async_after_s":3,"lag_limit_mb":16,"slot_keep_gb":%s,"auto_mode":"%s"},"gateway_targets":["%s"]}' \
        "${2:-20}" "$1" "$GW_IP"
}
SETTINGS_SHORT=1/5/15/20/0/3
# Ein neu gestarteter früherer aktiver Server startet PostgreSQL schreibbar,
# bis pdh-ha ihn abschaltet (wie in der Produktion beim Hochfahren)
RW_GRACE=15

# Adressen: node1 meldet zusätzlich wit (für den Beitritt des Zeugen), node2 nur
# lan + vpn. wit steht zuletzt: PostgreSQL probiert die Adressen der Reihe nach
# und gibt nach einer abgelehnten Anmeldung (node1 erlaubt nur node2s lan/vpn) auf.
N1_ADDRS=10.77.1.11,10.77.2.11,10.77.3.11
N2_ADDRS=10.77.1.12,10.77.2.12
W_ADDRS=10.77.3.20
declare -A IP=(
    [node1:lan]=10.77.1.11 [node1:vpn]=10.77.2.11 [node1:wit]=10.77.3.11
    [node2:lan]=10.77.1.12 [node2:vpn]=10.77.2.12 [node2:wit]=10.77.3.12
)
SCENARIOS=(aktiv-weg leitung-weg zeuge-weg hin-und-her rueckkehr neustart-aktiv version-anders
    beobachtung tausch aktiv-weg-echt asynchron-zeuge asynchron-ohne-zeuge erzwingen-abgezweigt
    rewind-fehler slot-grenze)
# Automatik je Szenario: observe (Standard nach der Einrichtung) oder real
# (erst nach Pflicht-Tausch erlaubt; lab_up tauscht dann hin und zurück).
# Die Szenarien aus Teil 2 prüfen echte Übernahmen und laufen mit real.
declare -A SC_MODE=([beobachtung]=observe [tausch]=observe [erzwingen-abgezweigt]=observe
    [rewind-fehler]=observe [slot-grenze]=observe)
PG_LAB=0                        # 1 = Datenbank-Spiegel eingerichtet, Wächter aktiv
BOTH_RW=-1                      # seit wann zwei schreibbare Datenbanken (SECONDS)
ACKED="$(mktemp -t ha-lab-acked.XXXXXX)"
trap 'rm -f "$ACKED"' EXIT
WRITER_PID=""

export PDH_HA_COMMIT="${PDH_HA_COMMIT:-$(git -C "$repo" rev-parse --short HEAD 2>/dev/null || echo lab)}"

dc() { docker compose -f "$compose_file" "$@"; }

log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }

# Abbruch eines Szenarios (läuft in einer Subshell)
fail() {
    echo "NICHT BESTANDEN: $*"
    diag
    exit 1
}

# diag: Zustand und letzte Ereignisse aller Knoten (zur Fehlersuche)
diag() {
    local c
    for c in node1 node2 witness; do
        echo "--- $c: pdh-ha status"
        timeout 10 docker exec "$c" pdh-ha status 2>&1 | sed 's/^/    /' || echo "    (nicht erreichbar)"
        echo "--- $c: letzte Ereignisse"
        timeout 10 docker exec "$c" tail -n 25 /var/lib/pdh-ha/events.jsonl 2>&1 | cut -c1-240 | sed 's/^/    /' || true
        echo "--- $c: state.json"
        timeout 10 docker exec "$c" cat /var/lib/pdh-ha/state.json 2>&1 | tr -d '\n' | cut -c1-600 | sed 's/^/    /' || true
        echo
    done
}

need() {
    command -v docker >/dev/null || { echo "docker fehlt" >&2; exit 1; }
    docker compose version >/dev/null 2>&1 || { echo "Docker-Compose-Plugin fehlt" >&2; exit 1; }
    if command -v python3 >/dev/null; then
        JSON_TOOL=python3
    elif command -v jq >/dev/null; then
        JSON_TOOL=jq
    else
        echo "python3 oder jq wird benötigt" >&2
        exit 1
    fi
}

# --- Status lesen ---

# status_json <Container>: JSON von pdh-ha status, leer wenn nicht erreichbar
status_json() {
    docker exec "$1" pdh-ha status --json 2>/dev/null || true
}

# jget <Schlüssel> < JSON. Schlüssel: role term maint blocked settings
# configured seen:<id> peerrole:<id>; nicht lesbar → "-"
jget() {
    if [[ "$JSON_TOOL" == python3 ]]; then
        python3 -c '
import json, sys
k = sys.argv[1]
yn = lambda b: "ja" if b else "nein"
try:
    d = json.loads(sys.stdin.read())
    s = d.get("settings") or {}
    me = d.get("self") or {}
    peers = {p.get("id"): p for p in d.get("peers") or []}
    if k == "sum": v = "%s %s %s" % (me.get("role") or "-", me.get("term"), yn(d.get("configured")))
    elif k == "role": v = me.get("role") or "-"
    elif k == "term": v = me.get("term")
    elif k == "maint": v = yn(d.get("maint"))
    elif k == "blocked": v = d.get("blocked") or ""
    elif k == "configured": v = yn(d.get("configured"))
    elif k == "settings":
        v = "/".join(str(s.get(x)) for x in ("tick_s", "fence_s", "takeover_trio_s", "takeover_duo_s", "cooldown_min", "max_per_day"))
    elif k == "automode": v = d.get("auto_mode") or "-"
    elif k == "swaptested": v = yn(d.get("swap_tested"))
    elif k == "pg": v = me.get("pg") or "-"
    elif k == "sync": v = yn(me.get("sync"))
    elif k == "stale": v = yn(me.get("stale"))
    elif k == "diverged": v = len(me.get("diverged") or [])
    elif k == "current": v = yn(me.get("data_current"))
    elif k.startswith("seen:"): v = yn(peers.get(k[5:], {}).get("seen"))
    elif k.startswith("peerrole:"): v = peers.get(k[9:], {}).get("role") or "-"
    else: v = "-"
except Exception:
    v = "-"
print(v)' "$1"
    else
        local f
        case "$1" in
            sum) f='"\(.self.role // "-") \(.self.term) \(if .configured then "ja" else "nein" end)"' ;;
            role) f='.self.role // "-"' ;;
            term) f='.self.term' ;;
            maint) f='if .maint then "ja" else "nein" end' ;;
            blocked) f='.blocked // ""' ;;
            configured) f='if .configured then "ja" else "nein" end' ;;
            settings) f='.settings | "\(.tick_s)/\(.fence_s)/\(.takeover_trio_s)/\(.takeover_duo_s)/\(.cooldown_min)/\(.max_per_day)"' ;;
            automode) f='.auto_mode // "-"' ;;
            swaptested) f='if .swap_tested then "ja" else "nein" end' ;;
            pg) f='.self.pg // "-"' ;;
            sync) f='if .self.sync then "ja" else "nein" end' ;;
            stale) f='if .self.stale then "ja" else "nein" end' ;;
            diverged) f='(.self.diverged // []) | length' ;;
            current) f='if .self.data_current then "ja" else "nein" end' ;;
            seen:*) f='[.peers[]? | select(.id == $id) | .seen] | if .[0] then "ja" else "nein" end' ;;
            peerrole:*) f='[.peers[]? | select(.id == $id) | .role] | .[0] // "-"' ;;
            *) echo "-"; return ;;
        esac
        jq -r --arg id "${1#*:}" "$f" 2>/dev/null || echo "-"
    fi
}

# get <Container> <Schlüssel>
get() { status_json "$1" | jget "$2"; }

# --- Beobachtung: jede Sekunde beide Server, nie zwei aktiv ---

R1=- R2=- T1=- T2=- C1=nein C2=nein J1="" J2=""
SAW_REJOIN1=0
LAST_LINE=""
T0=$SECONDS

snap() {
    J1="$(status_json node1)"
    J2="$(status_json node2)"
    read -r R1 T1 C1 <<<"$(jget sum <<<"$J1")"
    read -r R2 T2 C2 <<<"$(jget sum <<<"$J2")"
    : "${R1:=-}" "${R2:=-}" "${T1:=-}" "${T2:=-}" "${C1:=nein}" "${C2:=nein}"
    if [[ "$R1" == wiedereingliederung ]]; then
        SAW_REJOIN1=1
    fi
    local line="node1=$R1/$T1 node2=$R2/$T2"
    if [[ "$line" != "$LAST_LINE" ]]; then
        log "  [$((SECONDS - T0)) s] $line"
        LAST_LINE="$line"
    fi
    # uneingerichtet meldet pdh-ha immer aktiv – erst eingerichtet zählt
    if [[ "$R1" == aktiv && "$R2" == aktiv && "$C1" == ja && "$C2" == ja ]]; then
        fail "zwei aktive Server (node1 Amtszeit $T1, node2 Amtszeit $T2)"
    fi
    ((PG_LAB == 1)) && watch_pg
    return 0
}

# --- Datenbank ---

# psql_on <Container> <SQL> [<Datenbank>]: Ergebnis, leer bei Fehler
psql_on() {
    timeout 10 docker exec "$1" runuser -u postgres -- psql -XAtq -d "${3:-lab}" -c "$2" 2>/dev/null || true
}

# pg_writable <Container>: Datenbank läuft, ist Primary und nicht nur lesend
pg_writable() {
    [[ "$(psql_on "$1" "SELECT NOT (pg_is_in_recovery() OR current_setting('default_transaction_read_only')::bool)" postgres)" == t ]]
}

# watch_pg: höchstens eine schreibbare Datenbank (Übergang bis RW_GRACE)
watch_pg() {
    local n=0 c
    for c in node1 node2; do
        pg_writable "$c" && n=$((n + 1))
    done
    if ((n < 2)); then
        BOTH_RW=-1
        return 0
    fi
    if ((BOTH_RW < 0)); then
        BOTH_RW=$SECONDS
        log "  Wächter: zwei schreibbare Datenbanken"
    fi
    ((SECONDS - BOTH_RW < RW_GRACE)) || fail "zwei schreibbare Datenbanken seit $RW_GRACE s"
}

# --- Schreib-Generator ---

writer_start() {
    local start=1
    [[ -s "$ACKED" ]] && start=$(($(sort -n "$ACKED" | tail -1) + 1000))
    bash "$here/writer.sh" "$ACKED" "$start" &
    WRITER_PID=$!
}

writer_stop() {
    [[ -n "$WRITER_PID" ]] || return 0
    kill "$WRITER_PID" 2>/dev/null || true
    wait "$WRITER_PID" 2>/dev/null || true
    WRITER_PID=""
}

acked_count() { wc -l <"$ACKED" | tr -d ' '; }

# check_acked: jede bestätigte Zeile liegt auf dem aktiven Server
check_acked() {
    writer_stop
    local cnt src="" missing
    cnt=$(acked_count)
    ((cnt > 0)) || fail "keine Speicherung bestätigt"
    snap
    if [[ "$R1" == aktiv ]]; then
        src=node1
    elif [[ "$R2" == aktiv ]]; then
        src=node2
    fi
    [[ -n "$src" ]] || fail "kein aktiver Server für die Prüfung der Zeilen"
    missing="$(comm -23 <(sort -u "$ACKED") <(psql_on "$src" "SELECT n FROM t" | sort -u))"
    [[ -z "$missing" ]] || fail "$(wc -l <<<"$missing") bestätigte Zeilen fehlen auf $src, z. B.: $(head -5 <<<"$missing" | tr '\n' ' ')"
    log "alle $cnt bestätigten Zeilen auf $src vorhanden"
}

# wait_acks <Anzahl> <Sekunden>: so viele neue Bestätigungen abwarten
wait_acks() {
    local want=$(($(acked_count) + $1))
    wait_for "$2" eval '(($(acked_count) >= '"$want"'))'
}

# wait_for <Sekunden> <Bedingung …>: jede Sekunde snap, dann Bedingung prüfen
wait_for() {
    local timeout=$1
    shift
    local end=$((SECONDS + timeout))
    while :; do
        snap
        if "$@"; then
            return 0
        fi
        ((SECONDS < end)) || return 1
        sleep 1
    done
}

# watch_for <Sekunden> <Bedingung …>: Bedingung muss in jeder Sekunde gelten
watch_for() {
    local end=$((SECONDS + $1))
    shift
    while ((SECONDS < end)); do
        snap
        "$@" || return 1
        sleep 1
    done
}

roles() { [[ "$R1" == "$1" && "$R2" == "$2" ]]; }

# --- Netze ---

net_off() {
    local c=$1
    shift
    for n in "$@"; do
        docker network disconnect "pdh-ha-lab-$n" "$c" || return 1
    done
}

net_on() {
    local c=$1
    shift
    for n in "$@"; do
        docker network connect --ip "${IP[$c:$n]}" "pdh-ha-lab-$n" "$c" || return 1
    done
}

# --- Aufbau ---

BUILT=0

# lab_up [observe|real]: Trio mit Datenbank-Spiegel, node1 aktiv
lab_up() {
    local mode=${1:-observe}
    log "Labor aufbauen (Version ${PDH_HA_COMMIT}, Automatik $mode)"
    PG_LAB=0
    dc down -v --remove-orphans >/dev/null 2>&1 || true
    if ((BUILT == 0)); then
        dc build || fail "Image bauen"
        BUILT=1
    fi
    dc up -d || fail "Labor starten"
    local c
    for c in node1 node2 witness; do
        wait_for 30 eval '[[ -n "$(status_json '"$c"')" ]]' || fail "pdh-ha auf $c startet nicht"
    done
    docker exec node1 pdh-ha setup --id node1 --addr "$N1_ADDRS" || fail "setup auf node1"
    wait_for 60 eval '[[ "$(psql_on node1 "SELECT 1" postgres)" == 1 ]]' || fail "PostgreSQL auf node1 nicht bereit"
    psql_on node1 "CREATE DATABASE lab" postgres >/dev/null
    docker exec node1 runuser -u postgres -- psql -XAtq -d lab -v ON_ERROR_STOP=1 \
        -c "CREATE TABLE t (n bigint PRIMARY KEY)" >/dev/null || fail "Labordatenbank anlegen"
    local code
    code="$(docker exec node1 pdh-ha join-code 2>/dev/null)" || fail "Beitrittscode für node2"
    docker exec -e PDH_HA_JOIN="$code" node2 pdh-ha join --id node2 --addr "$N2_ADDRS" || fail "Beitritt node2"
    wait_for 30 eval '[[ "$(jget seen:node2 <<<"$J1")" == ja ]]' || fail "node1 sieht node2 nicht"
    log "Datenbank-Spiegel einrichten (replica-init auf node2)"
    docker exec node2 pdh-ha replica-init --confirm-host node2 || fail "replica-init auf node2"
    PG_LAB=1
    code="$(docker exec node1 pdh-ha join-code --witness 2>/dev/null)" || fail "Beitrittscode für den Zeugen"
    docker exec -e PDH_HA_JOIN="$code" witness pdh-ha join --witness --id witness --addr "$W_ADDRS" || fail "Beitritt Zeuge"
    wait_for 30 eval '[[ "$(jget seen:witness <<<"$J1")" == ja && "$(jget seen:witness <<<"$J2")" == ja && "$(jget seen:node1 <<<"$J2")" == ja ]]' ||
        fail "Zeuge oder node1 von node2 aus nicht gesehen"
    docker exec node1 pdh-ha settings "$(settings_json observe)" || fail "Zeiten setzen"
    wait_for 30 eval '[[ "$(jget settings <<<"$J2")" == "$SETTINGS_SHORT" && "$(get witness settings)" == "$SETTINGS_SHORT" ]]' ||
        fail "Zeiten kommen auf node2 oder dem Zeugen nicht an"
    docker exec node1 pdh-ha maintenance off || fail "Wartung aus"
    wait_for 30 eval 'roles aktiv passiv && [[ "$(jget maint <<<"$J2")" == nein && "$(jget blocked <<<"$J2")" == "" ]]' ||
        fail "Cluster nicht bereit (node2 gesperrt: $(jget blocked <<<"$J2"))"
    wait_for 60 mirror_ok || fail "Spiegel nicht synchron (node1 sync=$(jget sync <<<"$J1"), node2 pg=$(jget pg <<<"$J2"), aktuell=$(jget current <<<"$J2"))"
    if [[ "$mode" == real ]]; then
        pflicht_tausch
        docker exec node1 pdh-ha settings "$(settings_json real)" || fail "Automatik echt setzen"
        wait_for 30 eval '[[ "$(jget automode <<<"$J2")" == real && "$(get witness automode)" == real ]]' ||
            fail "Automatik echt kommt nicht auf node2 oder dem Zeugen an"
    fi
    log "Labor bereit: node1 aktiv, node2 Standby synchron, Zeuge verbunden, Automatik $mode"
}

# mirror_ok: node1 aktiv und synchron, node2 Standby mit aktuellem Datenstand
mirror_ok() {
    roles aktiv passiv && [[ "$(jget sync <<<"$J1")" == ja && "$(jget pg <<<"$J1")" == primary &&
        "$(jget pg <<<"$J2")" == standby && "$(jget current <<<"$J2")" == ja ]]
}

# mirror_ok_rev: wie mirror_ok mit vertauschten Rollen
mirror_ok_rev() {
    roles passiv aktiv && [[ "$(jget sync <<<"$J2")" == ja && "$(jget pg <<<"$J2")" == primary &&
        "$(jget pg <<<"$J1")" == standby && "$(jget current <<<"$J1")" == ja ]]
}

# pflicht_tausch: Rollen hin und zurück tauschen – erlaubt danach „echt“
pflicht_tausch() {
    log "Rollen tauschen: node1 → node2"
    swap_on node1 || fail "Rollentausch auf node1"
    wait_for 120 mirror_ok_rev || fail "Tausch zu node2 nicht abgeschlossen (node1=$R1 pg=$(jget pg <<<"$J1"), node2=$R2 pg=$(jget pg <<<"$J2"))"
    log "Rollen tauschen: node2 → node1"
    swap_on node2 || fail "Rollentausch auf node2"
    wait_for 120 mirror_ok || fail "Tausch zurück zu node1 nicht abgeschlossen (node1=$R1 pg=$(jget pg <<<"$J1"), node2=$R2 pg=$(jget pg <<<"$J2"))"
    wait_for 30 eval '[[ "$(jget swaptested <<<"$J1")" == ja ]]' || fail "Pflicht-Testumschaltung nicht vermerkt"
}

lab_down() {
    dc down -v --remove-orphans
}

lab_status() {
    local c
    for c in node1 node2 witness; do
        echo "== $c"
        docker exec "$c" pdh-ha status 2>&1 || echo "(nicht erreichbar)"
    done
}

# Rollentausch über den Socket (kein CLI-Befehl dafür)
swap_on() {
    docker exec "$1" curl -fsS --unix-socket /run/pdh-ha/api.sock \
        -H 'Content-Type: application/json' -d '{"cmd":"swap"}' http://pdh-ha/v1/command >/dev/null
}

# --- Szenarien ---

sc_aktiv_weg() {
    log "node1 stoppen – node2 muss übernehmen"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    wait_for 60 eval '[[ "$R2" == aktiv ]]' || fail "node2 nach 60 s nicht aktiv (node2=$R2)"
    watch_for 5 eval '[[ "$R2" == aktiv ]]' || fail "node2 bleibt nicht aktiv"
}

sc_leitung_weg() {
    log "node2 von lan und vpn trennen – nichts darf umschalten"
    net_off node2 lan vpn || fail "Netz off node2"
    wait_for 15 eval '[[ "$(jget seen:node1 <<<"$J2")" == nein ]] && roles aktiv passiv' ||
        fail "Trennung wirkt nicht (node2 sieht node1 noch) oder Rollen geändert (node1=$R1, node2=$R2)"
    watch_for 45 eval 'roles aktiv passiv' || fail "Rollen geändert (node1=$R1, node2=$R2)"
}

sc_zeuge_weg() {
    log "Zeugen stoppen – node1 bleibt aktiv"
    docker stop -t 2 witness >/dev/null || fail "docker stop witness"
    watch_for 40 eval 'roles aktiv passiv' || fail "Rollen geändert (node1=$R1, node2=$R2)"
}

sc_hin_und_her() {
    # Je Runde: node1 (aktiv) vollständig trennen → node2 übernimmt, node1
    # schaltet ab; verbinden → node1 passiv; Rollentausch (zählt nicht) → node1
    # wieder aktiv. Nach 3 Übernahmen sperrt die Tageshöchstzahl: Wartung an.
    local round takeovers=0
    for round in 1 2 3 4 5; do
        log "Runde $round: node1 trennen"
        net_off node1 lan vpn wit || fail "Netz off node1"
        if ! wait_for 60 eval '[[ "$R2" == aktiv || "$(jget maint <<<"$J2")" == ja ]]'; then
            fail "Runde $round: weder Übernahme noch Wartung nach 60 s"
        fi
        if [[ "$R2" == aktiv ]]; then
            takeovers=$((takeovers + 1))
            log "Runde $round: node2 hat übernommen ($takeovers. Umschaltung)"
            ((takeovers <= 3)) || fail "mehr als 3 Umschaltungen"
            net_on node1 lan vpn wit || fail "Netz on node1"
            # Wiedereingliederung: pg_rewind, Standby, wieder synchron
            wait_for 120 mirror_ok_rev || fail "Runde $round: node1 nach Rückkehr nicht synchroner Standby ($R1, pg=$(jget pg <<<"$J1"))"
            watch_for 3 eval 'roles passiv aktiv' || fail "Runde $round: Rollen nicht stabil (node1=$R1, node2=$R2)"
            swap_on node2 || fail "Rollentausch auf node2"
            wait_for 120 mirror_ok || fail "Runde $round: Rollentausch nicht abgeschlossen"
            watch_for 3 eval 'roles aktiv passiv' || fail "Runde $round: Rollen nach Tausch nicht stabil (node1=$R1, node2=$R2)"
        else
            log "Runde $round: Tageshöchstzahl erreicht, Wartung an"
            watch_for 10 eval '[[ "$R2" != aktiv ]]' || fail "node2 übernimmt trotz Wartung"
            net_on node1 lan vpn wit || fail "Netz on node1"
            wait_for 60 eval '[[ "$(jget maint <<<"$J1")" == ja && "$(jget maint <<<"$J2")" == ja ]] && roles aktiv passiv' ||
                fail "nach Rückkehr: Wartung nicht auf beiden an oder Rollen falsch (node1=$R1, node2=$R2)"
            log "Umschaltungen: $takeovers"
            ((takeovers == 3)) || fail "erwartet 3 Umschaltungen vor der Sperre, gezählt $takeovers"
            return 0
        fi
    done
    fail "Wartung nach $takeovers Umschaltungen nicht eingeschaltet"
}

sc_rueckkehr() {
    log "node1 stoppen – node2 übernimmt"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    wait_for 60 eval '[[ "$R2" == aktiv ]]' || fail "node2 nach 60 s nicht aktiv"
    log "node1 starten – wiedereingliederung, dann passiv"
    SAW_REJOIN1=0
    docker start node1 >/dev/null || fail "docker start node1"
    wait_for 120 mirror_ok_rev || fail "node1 nach 120 s nicht synchroner Standby ($R1, pg=$(jget pg <<<"$J1"))"
    if ((SAW_REJOIN1 == 0)) &&
        ! docker exec node1 grep -q 'Rolle: .*wiedereingliederung' /var/lib/pdh-ha/events.jsonl; then
        fail "node1 war nicht in wiedereingliederung"
    fi
    watch_for 10 eval 'roles passiv aktiv' || fail "Rollen nicht stabil (node1=$R1, node2=$R2)"
}

sc_neustart_aktiv() {
    log "node1 neu starten – bleibt aktiv"
    docker restart -t 2 node1 >/dev/null || fail "docker restart node1"
    wait_for 40 eval 'roles aktiv passiv && [[ "$(jget seen:node2 <<<"$J1")" == ja ]]' ||
        fail "nach Neustart nicht node1 aktiv / node2 passiv (node1=$R1, node2=$R2)"
    watch_for 20 eval 'roles aktiv passiv' || fail "Rollen nicht stabil (node1=$R1, node2=$R2)"
}

sc_version_anders() {
    log "node2 mit Version 0.0.0 neu erstellen"
    NODE2_VERSION_OVERRIDE=0.0.0 dc up -d --no-deps --force-recreate node2 >/dev/null || fail "node2 neu erstellen"
    wait_for 40 eval '[[ "$(jget blocked <<<"$J2")" == "Versionen unterschiedlich" ]] && roles aktiv passiv' ||
        fail "node2 meldet keine Sperre wegen Version (gesperrt: $(jget blocked <<<"$J2"))"
    log "node1 stoppen – node2 darf nicht übernehmen"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    watch_for 40 eval '[[ "$R2" == passiv && "$(jget blocked <<<"$J2")" == "Versionen unterschiedlich" ]]' ||
        fail "node2=$R2, gesperrt: $(jget blocked <<<"$J2")"
    log "node1 starten – bleibt aktiv (für die Prüfung der Zeilen)"
    docker start node1 >/dev/null || fail "docker start node1"
    wait_for 60 eval '[[ "$R1" == aktiv ]]' || fail "node1 nach Neustart nicht aktiv ($R1)"
}

# --- Szenarien mit Datenbank-Spiegel ---

sc_beobachtung() {
    snap
    local t2=$T2
    log "node1 stoppen – Beobachtung: node2 bleibt passiv, Amtszeit $t2 bleibt"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    watch_for 40 eval '[[ "$R2" == passiv && "$T2" == '"$t2"' ]]' || fail "node2=$R2 Amtszeit $T2 (erwartet passiv, $t2)"
    docker exec node2 grep -q 'würde jetzt' /var/lib/pdh-ha/events.jsonl || fail "kein Ereignis „würde jetzt“ auf node2"
    log "node1 starten – wieder aktiv, Spiegel synchron"
    docker start node1 >/dev/null || fail "docker start node1"
    wait_for 90 mirror_ok || fail "nach Rückkehr kein synchroner Spiegel (node1=$R1, node2=$R2)"
}

sc_tausch() {
    wait_acks 5 30 || fail "Schreib-Generator bestätigt nichts"
    pflicht_tausch
    wait_acks 5 30 || fail "nach dem Tausch keine Speicherungen"
}

sc_aktiv_weg_echt() {
    log "node1 stoppen – node2 übernimmt mit aktuellem Datenstand"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    wait_for 60 eval '[[ "$R2" == aktiv && "$(jget pg <<<"$J2")" == primary ]]' || fail "node2 nach 60 s nicht aktiv (node2=$R2, pg=$(jget pg <<<"$J2"))"
    wait_acks 5 30 || fail "auf node2 keine Speicherungen"
    log "node1 starten – Wiedereingliederung als Standby"
    docker start node1 >/dev/null || fail "docker start node1"
    wait_for 120 mirror_ok_rev || fail "node1 nicht synchroner Standby (node1=$R1, pg=$(jget pg <<<"$J1"))"
    [[ "$(jget diverged <<<"$J1")" == 0 ]] || fail "abgezweigte Daten trotz synchronem Betrieb"
}

sc_asynchron_zeuge() {
    log "node2 von lan und vpn trennen – Standby empfängt nichts, Zeuge bestätigt, node1 asynchron"
    net_off node2 lan vpn || fail "Netz off node2"
    wait_for 40 eval '[[ "$(jget sync <<<"$J1")" == nein && "$(jget stale <<<"$J1")" == ja ]]' ||
        fail "node1 nicht asynchron (sync=$(jget sync <<<"$J1"), stale=$(jget stale <<<"$J1"))"
    wait_acks 5 30 || fail "asynchron keine Speicherungen"
    log "node1 stoppen – node2 ist nicht aktuell und darf nicht übernehmen"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    watch_for 40 eval '[[ "$R2" == passiv ]]' || fail "veralteter node2 übernimmt ($R2)"
    log "node1 starten, node2 verbinden – wieder synchron"
    docker start node1 >/dev/null || fail "docker start node1"
    net_on node2 lan vpn || fail "Netz on node2"
    wait_for 120 eval 'mirror_ok && [[ "$(jget stale <<<"$J1")" == nein ]]' ||
        fail "nicht wieder synchron (node1=$R1 sync=$(jget sync <<<"$J1") stale=$(jget stale <<<"$J1"), node2=$R2)"
}

sc_asynchron_ohne_zeuge() {
    log "Zeugen stoppen, node2 trennen – ohne Bestätigung bleibt node1 synchron"
    docker stop -t 2 witness >/dev/null || fail "docker stop witness"
    net_off node2 lan vpn || fail "Netz off node2"
    sleep 10
    local n
    n=$(acked_count)
    watch_for 30 eval '[[ "$(jget sync <<<"$J1")" != nein && "$R2" != aktiv ]]' ||
        fail "node1 asynchron ohne Zeugen-Bestätigung oder node2 aktiv (sync=$(jget sync <<<"$J1"), node2=$R2)"
    (($(acked_count) == n)) || fail "Speicherungen bestätigt, obwohl der Standby nichts empfängt"
    log "Zeugen starten, node2 verbinden – Speicherungen laufen wieder"
    docker start witness >/dev/null || fail "docker start witness"
    net_on node2 lan vpn || fail "Netz on node2"
    wait_for 120 mirror_ok || fail "Spiegel nicht wieder synchron (node1=$R1, node2=$R2)"
    wait_acks 5 60 || fail "keine Speicherungen nach der Rückkehr"
}

# erzwingen_ablauf [rewind-fehler]: node2 fällt aus, node1 schreibt asynchron
# weiter und fällt dann aus; node2 übernimmt per Handbefehl, node1 kommt
# zurück und muss die Zwischenzeit als abgezweigte Daten sichern.
erzwingen_ablauf() {
    local fault=${1:-} before
    if [[ "$fault" == rewind-fehler ]]; then
        log "pg_rewind auf node1 schlägt ab jetzt fehl (Probelauf geht)"
        docker exec node1 bash -c 'b="$(ls -d /usr/lib/postgresql/*/bin | sort -V | tail -1)/pg_rewind"
            mv "$b" "$b.real"
            printf "#!/bin/sh\ncase \" \$* \" in *\" --dry-run \"*) exec \"\$0.real\" \"\$@\" ;; esac\necho \"pg_rewind: Laborfehler\" >&2\nexit 1\n" >"$b"
            chmod 0755 "$b"' || fail "pg_rewind ersetzen"
    fi
    log "node2 stoppen – node1 schreibt nach Zeugen-Bestätigung asynchron weiter"
    before=$(acked_count)
    docker stop -t 2 node2 >/dev/null || fail "docker stop node2"
    wait_for 40 eval '[[ "$(jget sync <<<"$J1")" == nein ]]' || fail "node1 nicht asynchron"
    wait_acks 10 30 || fail "asynchron keine Speicherungen"
    writer_stop
    # nur auf node1 gespeichert: gehen mit dem Erzwingen in die abgezweigten Daten
    head -n "$before" "$ACKED" >"$ACKED.neu" && mv "$ACKED.neu" "$ACKED"
    log "node1 stoppen, node2 starten, Übernahme erzwingen"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    docker start node2 >/dev/null || fail "docker start node2"
    wait_for 60 eval '[[ "$R2" == passiv && "$(jget pg <<<"$J2")" == standby ]]' || fail "node2 nicht passiv mit Standby ($R2)"
    docker exec node2 curl -fsS --unix-socket /run/pdh-ha/api.sock -H 'Content-Type: application/json' \
        -d '{"cmd":"force_takeover"}' http://pdh-ha/v1/command >/dev/null || fail "Übernahme erzwingen auf node2"
    wait_for 60 eval '[[ "$R2" == aktiv && "$(jget pg <<<"$J2")" == primary ]]' || fail "node2 nicht aktiv nach Erzwingen ($R2)"
    log "node1 starten – Wiedereingliederung mit abgezweigten Daten"
    docker start node1 >/dev/null || fail "docker start node1"
    wait_for 180 eval 'mirror_ok_rev && (($(jget diverged <<<"$J1") >= 1))' ||
        fail "node1 nicht Standby mit abgezweigten Daten (node1=$R1 pg=$(jget pg <<<"$J1") abgezweigt=$(jget diverged <<<"$J1"))"
    if [[ "$fault" == rewind-fehler ]]; then
        docker exec node1 grep -q 'neu vom aktiven Server kopiert' /var/lib/pdh-ha/events.jsonl ||
            fail "keine Neukopie nach fehlgeschlagenem pg_rewind"
    fi
    writer_start
    wait_acks 5 60 || fail "auf node2 keine Speicherungen"
}

sc_erzwingen_abgezweigt() { erzwingen_ablauf; }

sc_rewind_fehler() { erzwingen_ablauf rewind-fehler; }

# slot_status: Checkpoint auf node1, dann wal_status des Slots von node2
slot_status() {
    psql_on node1 "CHECKPOINT; SELECT wal_status FROM pg_replication_slots WHERE slot_name = 'pdh_ha_node2'" postgres
}

sc_slot_grenze() {
    docker exec node1 pdh-ha settings "$(settings_json observe 1)" || fail "Höchstmenge 1 GB setzen"
    log "node2 stoppen, auf node1 1,3 GB schreiben – Slot wird ungültig"
    docker stop -t 2 node2 >/dev/null || fail "docker stop node2"
    wait_for 40 eval '[[ "$(jget sync <<<"$J1")" == nein ]]' || fail "node1 nicht asynchron"
    docker exec node1 runuser -u postgres -- psql -XAtq -d lab -v ON_ERROR_STOP=1 \
        -c "CREATE TABLE fill AS SELECT g, repeat('x', 1000) AS pad FROM generate_series(1, 1300000) g" \
        -c "CHECKPOINT" -c "SELECT pg_switch_wal()" -c "CHECKPOINT" >/dev/null || fail "Fülldaten schreiben"
    wait_for 60 eval '[[ "$(slot_status)" == lost ]]' || fail "Slot nicht ungültig ($(slot_status))"
    log "node2 starten – Neukopie, dann synchron"
    docker start node2 >/dev/null || fail "docker start node2"
    wait_for 300 mirror_ok || fail "node2 nach Neukopie kein synchroner Standby (pg=$(jget pg <<<"$J2"))"
    docker exec node2 grep -q 'neu vom aktiven Server kopiert' /var/lib/pdh-ha/events.jsonl || fail "keine Neukopie auf node2"
}

run_scenario() {
    local name=$1 fn
    fn="sc_${name//-/_}"
    declare -F "$fn" >/dev/null || { echo "Unbekanntes Szenario: $name" >&2; exit 2; }
    echo "=== Szenario $name"
    LAST_LINE="" T0=$SECONDS
    : >"$ACKED"
    trap writer_stop EXIT
    lab_up "${SC_MODE[$name]:-real}"
    writer_start
    wait_acks 3 30 || fail "Schreib-Generator bestätigt nichts"
    "$fn" || fail "Szenario $name abgebrochen"
    check_acked
    echo "BESTANDEN: $name"
}

usage() {
    echo "Aufruf: $0 up|down|status|race|alle|<Szenario>"
    echo "Szenarien: ${SCENARIOS[*]}"
}

cmd_race() {
    command -v docker >/dev/null || { echo "docker fehlt" >&2; exit 1; }
    log "go test -race in golang:1.24"
    docker run --rm -v "$repo:/src:ro" -e CGO_ENABLED=1 golang:1.24 bash -c '
        set -euo pipefail
        mkdir -p /work/internal /work/cmd
        cp /src/go.mod /src/go.sum /work/
        cp -r /src/internal/ha /work/internal/ha
        cp -r /src/cmd/pdh-ha /work/cmd/pdh-ha
        cd /work
        go test -race -count=1 ./internal/ha/ ./cmd/pdh-ha/'
}

main() {
    local cmd=${1:-}
    case "$cmd" in
        race)
            cmd_race
            return
            ;;
        "" | -h | --help)
            usage
            return 2
            ;;
    esac
    need
    case "$cmd" in
        up) (lab_up) ;;
        down) lab_down ;;
        status) lab_status ;;
        alle)
            local s failed=()
            for s in "${SCENARIOS[@]}"; do
                if ! (run_scenario "$s"); then
                    failed+=("$s")
                fi
                BUILT=1
            done
            lab_down >/dev/null 2>&1 || true
            echo
            if ((${#failed[@]} == 0)); then
                echo "BESTANDEN: alle ${#SCENARIOS[@]} Szenarien"
            else
                echo "NICHT BESTANDEN: ${failed[*]}"
                return 1
            fi
            ;;
        *)
            local rc=0
            (run_scenario "$cmd") || rc=$?
            return "$rc"
            ;;
    esac
}

main "$@"
