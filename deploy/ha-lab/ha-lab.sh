#!/bin/bash
# Testlabor für pdh-ha (nur Testumgebung). Aufruf:
#   ha-lab.sh up|down|status|race|<Szenario>|alle
# Szenarien: aktiv-weg leitung-weg zeuge-weg hin-und-her rueckkehr
#            neustart-aktiv version-anders
# Voraussetzungen: Linux mit Docker und Compose-Plugin, python3 (oder jq).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
compose_file="$here/compose.yaml"

# Laborzeiten: Takt 1 s, Fence 5 s, Übernahme Trio 15 s / Duo 20 s, Wartezeit 0, 3 je Tag
GW_IP=10.77.1.254
SETTINGS_JSON='{"settings":{"tick_s":1,"fence_s":5,"takeover_trio_s":15,"takeover_duo_s":20,"cooldown_min":0,"max_per_day":3},"gateway_targets":["'"$GW_IP"'"]}'
SETTINGS_SHORT=1/5/15/20/0/3

# Adressen: node1 meldet zuerst wit (für den Beitritt des Zeugen), node2 nur lan + vpn
N1_ADDRS=10.77.3.11,10.77.1.11,10.77.2.11
N2_ADDRS=10.77.1.12,10.77.2.12
W_ADDRS=10.77.3.20
declare -A IP=(
    [node1:lan]=10.77.1.11 [node1:vpn]=10.77.2.11 [node1:wit]=10.77.3.11
    [node2:lan]=10.77.1.12 [node2:vpn]=10.77.2.12 [node2:wit]=10.77.3.12
)
SCENARIOS=(aktiv-weg leitung-weg zeuge-weg hin-und-her rueckkehr neustart-aktiv version-anders)

export PDH_HA_COMMIT="${PDH_HA_COMMIT:-$(git -C "$repo" rev-parse --short HEAD 2>/dev/null || echo lab)}"

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

lab_up() {
    log "Labor aufbauen (Version ${PDH_HA_COMMIT})"
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
    local code
    code="$(docker exec node1 pdh-ha join-code 2>/dev/null)" || fail "Beitrittscode für node2"
    docker exec -e PDH_HA_JOIN="$code" node2 pdh-ha join --id node2 --addr "$N2_ADDRS" || fail "Beitritt node2"
    wait_for 30 eval '[[ "$(jget seen:node2 <<<"$J1")" == ja ]]' || fail "node1 sieht node2 nicht"
    code="$(docker exec node1 pdh-ha join-code --witness 2>/dev/null)" || fail "Beitrittscode für den Zeugen"
    docker exec -e PDH_HA_JOIN="$code" witness pdh-ha join --witness --id witness --addr "$W_ADDRS" || fail "Beitritt Zeuge"
    wait_for 30 eval '[[ "$(jget seen:witness <<<"$J1")" == ja && "$(jget seen:witness <<<"$J2")" == ja && "$(jget seen:node1 <<<"$J2")" == ja ]]' ||
        fail "Zeuge oder node1 von node2 aus nicht gesehen"
    docker exec node1 pdh-ha settings "$SETTINGS_JSON" || fail "Zeiten setzen"
    wait_for 30 eval '[[ "$(jget settings <<<"$J2")" == "$SETTINGS_SHORT" && "$(get witness settings)" == "$SETTINGS_SHORT" ]]' ||
        fail "Zeiten kommen auf node2 oder dem Zeugen nicht an"
    docker exec node1 pdh-ha maintenance off || fail "Wartung aus"
    wait_for 30 eval 'roles aktiv passiv && [[ "$(jget maint <<<"$J2")" == nein && "$(jget blocked <<<"$J2")" == "" ]]' ||
        fail "Cluster nicht bereit (node2 gesperrt: $(jget blocked <<<"$J2"))"
    log "Labor bereit: node1 aktiv, node2 passiv, Zeuge verbunden, Wartung aus"
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
            wait_for 60 eval 'roles passiv aktiv' || fail "Runde $round: node1 nach Rückkehr nicht passiv ($R1)"
            watch_for 3 eval 'roles passiv aktiv' || fail "Runde $round: Rollen nicht stabil (node1=$R1, node2=$R2)"
            swap_on node2 || fail "Rollentausch auf node2"
            wait_for 60 eval 'roles aktiv passiv' || fail "Runde $round: Rollentausch nicht abgeschlossen"
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
    wait_for 60 eval 'roles passiv aktiv' || fail "node1 nach 60 s nicht passiv ($R1)"
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
}

run_scenario() {
    local name=$1 fn
    fn="sc_${name//-/_}"
    declare -F "$fn" >/dev/null || { echo "Unbekanntes Szenario: $name" >&2; exit 2; }
    echo "=== Szenario $name"
    LAST_LINE="" T0=$SECONDS
    lab_up
    "$fn" || fail "Szenario $name abgebrochen"
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
