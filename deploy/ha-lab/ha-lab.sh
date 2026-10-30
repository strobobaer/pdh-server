#!/bin/bash
# Testlabor für pdh-ha (nur Testumgebung). Aufruf:
#   ha-lab.sh up|down|status|race|<Szenario>|alle
# Szenarien: aktiv-weg leitung-weg zeuge-weg hin-und-her rueckkehr
#            neustart-aktiv version-anders
#   mit Datenbank-Spiegel (Teil 3): beobachtung tausch aktiv-weg-echt
#            asynchron-zeuge asynchron-ohne-zeuge erzwingen-abgezweigt
#            rewind-fehler slot-grenze
#   Zugangsweg (Teil 5): vip-tausch vip-ausfall vip-abschaltung vip-duplikat
#            vip-entfernt vip-neustart hooks umstellung
#   Update im Verbund: update-ok update-b-vorher update-b-nachher update-abbruch
#            update-neustart update-nicht-gruen update-nacheinander
#            update-nacheinander-fehler update-tausch-frist
#            update-nacheinander-gleichstand
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
    rewind-fehler slot-grenze
    vip-tausch vip-ausfall vip-abschaltung vip-duplikat vip-entfernt vip-neustart hooks umstellung
    update-ok update-b-vorher update-b-nachher update-abbruch update-neustart update-nicht-gruen
    update-nacheinander update-nacheinander-fehler update-tausch-frist update-nacheinander-gleichstand)
# Automatik je Szenario: observe (Standard nach der Einrichtung) oder real
# (erst nach Pflicht-Tausch erlaubt; lab_up tauscht dann hin und zurück).
# Die Szenarien aus Teil 2 prüfen echte Übernahmen und laufen mit real.
# rewind-fehler läuft mit real: nach dem Pflicht-Tausch liegen die abgezweigten
# Daten auf einer höheren Zeitleiste (pg_waldump -t).
declare -A SC_MODE=([beobachtung]=observe [tausch]=observe [erzwingen-abgezweigt]=observe
    [slot-grenze]=observe [vip-tausch]=observe [umstellung]=observe)
PG_LAB=0                        # 1 = Datenbank-Spiegel eingerichtet, Wächter aktiv
BOTH_RW=-1                      # seit wann zwei schreibbare Datenbanken (SECONDS)
BOTH_ACT=-1                     # seit wann zwei aktive Server verschiedener Amtszeit
ACKED="$(mktemp -t ha-lab-acked.XXXXXX)"
# Mitwandernde Adresse (Teil 5): Wächter schreibt jeden Moment, in dem beide
# Server sie gleichzeitig tragen, nach VIP_BAD
VIP=10.77.1.50/24
VIP_IP=${VIP%/*}
VIP_BAD="$(mktemp -t ha-lab-vip.XXXXXX)"
VIP_WATCH_PID=""
trap 'rm -f "$ACKED" "$VIP_BAD"' EXIT
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
        if [[ "$c" != witness ]]; then
            echo "--- $c: IPv4-Adressen, Skript-Protokoll"
            timeout 10 docker exec "$c" ip -o -4 addr show 2>&1 | cut -c1-120 | sed 's/^/    /' || true
            timeout 10 docker exec "$c" tail -n 5 /var/lib/pdh-ha/hook.log 2>/dev/null | sed 's/^/    /' || true
            echo "--- $c: Update-Auftrag, Datenbank, Labor-Agent, /health"
            timeout 10 docker exec "$c" pdh-ha update status 2>&1 | sed 's/^/    /' || true
            timeout 10 docker exec "$c" cat /var/lib/pdh-ha/update-job.json 2>/dev/null | tr -d '\n' | cut -c1-900 | sed 's/^/    /' || true
            echo
            echo "    pg=$(get "$c" pg) sync=$(get "$c" sync) aktuell=$(get "$c" current) commit=$(get "$c" commit)" \
                "pdh=$(docker exec "$c" test -f /run/pdh-dummy.running && echo läuft || echo aus)" \
                "busy=$(status_json "$c" | grep -o '"busy":[a-z]*')"
            timeout 10 docker exec "$c" sh -c 'ls /var/lib/pdh-lab; tail -n 8 /var/lib/pdh-lab/agent.log' 2>&1 | sed 's/^/    /' || true
            timeout 10 docker exec "$c" curl -s -m 3 -w ' %{http_code}' http://127.0.0.1:8090/health 2>&1 | sed 's/^/    health: /' || true
            echo
            timeout 10 docker exec "$c" sh -c 'tail -n 15 /var/log/postgresql/*.log' 2>&1 | cut -c1-200 | sed 's/^/    /' || true
        fi
        echo
    done
    echo "--- gw: IPv4-Adressen"
    timeout 10 docker exec gw ip -o -4 addr show 2>&1 | cut -c1-120 | sed 's/^/    /' || true
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
# configured seen:<id> peerrole:<id> holder hookson hookssame hooksn commit
# peercommit:<id> upphase upresult faulty window; nicht lesbar → "-"
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
    acc = d.get("access") or {}
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
    elif k == "holder": v = acc.get("holder") or "-"
    elif k == "hookson": v = yn(acc.get("hooks_on"))
    elif k == "hookssame": v = yn((acc.get("hooks") or {}).get("same"))
    elif k == "hooksn": v = len((acc.get("hooks") or {}).get("names") or [])
    elif k == "commit": v = me.get("commit") or "-"
    elif k.startswith("peercommit:"): v = peers.get(k[11:], {}).get("commit") or "-"
    elif k == "upphase": v = (d.get("update") or {}).get("phase") or "-"
    elif k == "upresult": v = (d.get("update") or {}).get("result") or ""
    elif k == "faulty": v = " ".join(d.get("faulty") or [])
    elif k == "window":
        w = d.get("window") or {}
        v = "%s/%s" % (w.get("countdown_min"), w.get("lock_max_min"))
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
            holder) f='if (.access.holder // "") == "" then "-" else .access.holder end' ;;
            hookson) f='if .access.hooks_on then "ja" else "nein" end' ;;
            hookssame) f='if .access.hooks.same then "ja" else "nein" end' ;;
            hooksn) f='(.access.hooks.names // []) | length' ;;
            commit) f='.self.commit // "-" | if . == "" then "-" else . end' ;;
            peercommit:*) f='[.peers[]? | select(.id == $id) | .commit] | .[0] // "-" | if . == "" then "-" else . end' ;;
            upphase) f='.update.phase // "-" | if . == "" then "-" else . end' ;;
            upresult) f='.update.result // ""' ;;
            faulty) f='(.faulty // []) | join(" ")' ;;
            window) f='"\(.window.countdown_min)/\(.window.lock_max_min)"' ;;
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
    # Gleiche Amtszeit: sofort nicht bestanden. Verschiedene: ein neu
    # gestarteter früherer aktiver Server (Beobachtung schaltet ihn nicht ab)
    # meldet kurz seine alte Rolle, bis er die höhere Amtszeit sieht.
    if [[ "$R1" == aktiv && "$R2" == aktiv && "$C1" == ja && "$C2" == ja ]]; then
        [[ "$T1" != "$T2" ]] || fail "zwei aktive Server mit gleicher Amtszeit $T1"
        ((BOTH_ACT >= 0)) || BOTH_ACT=$SECONDS
        ((SECONDS - BOTH_ACT < RW_GRACE)) || fail "zwei aktive Server seit $RW_GRACE s (node1 Amtszeit $T1, node2 Amtszeit $T2)"
    else
        BOTH_ACT=-1
    fi
    if [[ -s "$VIP_BAD" ]]; then
        fail "Adresse gleichzeitig auf node1 und node2: $(head -1 "$VIP_BAD")"
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
    wait_for 60 eval 'pdh_on node1 && pdh_on node2' || fail "pdh läuft nicht auf beiden Servern (am passiven als Spiegel)"
    if [[ "$mode" == real ]]; then
        pflicht_tausch
        docker exec node1 pdh-ha settings "$(settings_json real)" || fail "Automatik echt setzen"
        wait_for 30 eval '[[ "$(jget automode <<<"$J2")" == real && "$(get witness automode)" == real ]]' ||
            fail "Automatik echt kommt nicht auf node2 oder dem Zeugen an"
    fi
    log "Labor bereit: node1 aktiv, node2 Standby synchron, Zeuge verbunden, Automatik $mode"
}

# pdh_on <Container>: Platzhalter für pdh läuft (am passiven Server als Spiegel)
pdh_on() { docker exec "$1" test -f /run/pdh-dummy.running; }

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
    wait_for 60 eval 'pdh_on node1 && pdh_on node2' || fail "nach dem Tausch läuft pdh nicht auf beiden Servern"
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

# --- Zugangsweg (Teil 5): mitwandernde Adresse, Skripte, Umstellung ---

# vip_here <Container> [<IP>]: Adresse liegt auf einer Karte des Containers
# (gestoppt oder nicht erreichbar = nein)
vip_here() {
    local out
    out="$(timeout 5 docker exec "$1" ip -o -4 addr show 2>/dev/null)" || return 1
    [[ "$out" == *" inet ${2:-$VIP_IP}/"* ]]
}

# lan_if <Container> <IP>: Name der Karte mit dieser Adresse
lan_if() {
    docker exec "$1" ip -o -4 addr show 2>/dev/null | awk -v ip="$2/" 'index($4, ip) == 1 { print $2; exit }'
}

# holder_is <Container>: nur dieser Server trägt die Adresse, und sein Status
# nennt ihn als Halter
holder_is() {
    local other=node1
    [[ "$1" == node1 ]] && other=node2
    vip_here "$1" && ! vip_here "$other" && [[ "$(get "$1" holder)" == "$1" ]]
}

# gw_ping [<IP>]: das Gateway erreicht die Adresse
gw_ping() { docker exec gw ping -c1 -W1 "${1:-$VIP_IP}" >/dev/null 2>&1; }

# vip_where: Kurzbefund für Fehlermeldungen
vip_where() {
    local c out=""
    for c in node1 node2 gw; do
        vip_here "$c" && out+="$c "
    done
    echo "Adresse $VIP_IP auf: ${out:-niemand} (Halter laut node1: $(get node1 holder), node2: $(get node2 holder))"
}

# vip_watch_start: alle 0,5 s die Karten beider Server lesen (gleichzeitig);
# tragen beide die Adresse, Befund nach VIP_BAD – snap bricht dann ab. Runden,
# die länger als 1,5 s dauern, zählen nicht (die Übernahme wartet nach der
# Abgabe mindestens 2 s auf die Duplikat-Prüfung).
vip_watch_start() {
    vip_watch_stop
    : >"$VIP_BAD"
    (
        set +e
        trap - EXIT
        while :; do
            t0=$(date +%s%3N)
            vip_here node1 &
            p1=$!
            vip_here node2 &
            p2=$!
            a=0 b=0
            wait "$p1" && a=1
            wait "$p2" && b=1
            if ((a == 1 && b == 1 && $(date +%s%3N) - t0 <= 1500)); then
                echo "$(date +%H:%M:%S.%3N) beide tragen $VIP_IP" >>"$VIP_BAD"
            fi
            sleep 0.5
        done
    ) &
    VIP_WATCH_PID=$!
    log "  Wächter Adresse $VIP_IP läuft (alle 0,5 s)"
}

vip_watch_stop() {
    [[ -n "$VIP_WATCH_PID" ]] || return 0
    kill "$VIP_WATCH_PID" 2>/dev/null || true
    wait "$VIP_WATCH_PID" 2>/dev/null || true
    VIP_WATCH_PID=""
}

# vip_wait <Container> <Sekunden>: wartet (alle 0,2 s), bis die Adresse dort
# liegt; WAITED = Millisekunden
WAITED=0
vip_wait() {
    local t0 end
    t0=$(date +%s%3N)
    end=$((t0 + $2 * 1000))
    while :; do
        WAITED=$(($(date +%s%3N) - t0))
        vip_here "$1" && return 0
        (($(date +%s%3N) < end)) || return 1
        sleep 0.2
    done
}

# vip_test [<Adresse>]: Adresstest am ersten Server (node1), muss gelingen
vip_test() {
    local out rc=0
    log "Adresstest ${1:-$VIP} in alle Richtungen"
    out="$(docker exec node1 pdh-ha vip test ${1:+--adresse "$1"} 2>&1)" || rc=$?
    sed 's/^/    /' <<<"$out"
    ((rc == 0)) || fail "Adresstest ${1:-$VIP} nicht erfolgreich"
}

# vip_enter: Adresse eintragen (noch nicht einschalten) – wie im PDH:
# eintragen, testen, einschalten
vip_enter() {
    docker exec node1 pdh-ha access --vip "$VIP" || fail "Adresse $VIP eintragen"
}

# vip_setup: eintragen, testen, einschalten, node1 (aktiv) hält die Adresse, Wächter an
vip_setup() {
    vip_enter
    vip_test
    docker exec node1 pdh-ha access --vip-on || fail "mitwandernde Adresse einschalten"
    wait_for 20 eval 'holder_is node1' || fail "node1 hält die Adresse nicht – $(vip_where)"
    vip_watch_start
    wait_for 10 gw_ping || fail "gw erreicht $VIP_IP bei node1 nicht"
}

# event_on <Container> <Text>: Text steht im Ereignisprotokoll
event_on() { docker exec "$1" grep -qF -- "$2" /var/lib/pdh-ha/events.jsonl; }

sc_vip_tausch() {
    vip_setup
    log "Rollen tauschen – binnen 10 s hält node2 die Adresse, gw erreicht sie"
    swap_on node1 || fail "Rollentausch auf node1"
    vip_wait node2 10 || fail "node2 trägt die Adresse nach 10 s nicht – $(vip_where)"
    log "node2 trägt die Adresse nach $WAITED ms"
    wait_for 10 eval 'holder_is node2 && gw_ping' || fail "nach dem Tausch – $(vip_where)"
    # der alte aktive Server gibt vor der Übergabe ab – kein Duplikat-Alarm
    ! event_on node2 "wird noch von einem anderen Gerät benutzt" || fail "Duplikat-Alarm auf node2 beim Tausch"
    wait_for 120 mirror_ok_rev || fail "Tausch zu node2 nicht abgeschlossen (node1=$R1 pg=$(jget pg <<<"$J1"), node2=$R2 pg=$(jget pg <<<"$J2"))"
    watch_for 5 eval 'holder_is node2' || fail "Adresse bleibt nicht bei node2 – $(vip_where)"
}

sc_vip_ausfall() {
    vip_setup
    log "node1 anhalten – node2 übernimmt mit der Adresse"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    wait_for 60 eval '[[ "$R2" == aktiv ]] && holder_is node2 && gw_ping' ||
        fail "node2 nach 60 s nicht aktiv mit Adresse (node2=$R2) – $(vip_where)"
    watch_for 5 eval 'holder_is node2' || fail "Adresse bleibt nicht bei node2 – $(vip_where)"
}

# Läuft auf dem Server: Karte abschalten, dann alle 0,1 s Rolle und Adresse
# lesen. Ausgabe: Millisekunden bis „nicht mehr aktiv“ und bis „Adresse weg“.
ABSCHALT_SH='
ifc=$1 ip=$2 max=$(($3 * 1000))
ip link set dev "$ifc" down || exit 2
t0=$(date +%s%3N) tf=-1
while :; do
    el=$(($(date +%s%3N) - t0))
    role=$(pdh-ha status --json 2>/dev/null | grep -o "\"role\":\"[^\"]*\"" | head -1)
    if ((tf < 0)) && [[ -n "$role" && "$role" != *\"aktiv\" ]]; then tf=$el; fi
    if ! ip -o -4 addr show dev "$ifc" | grep -q " inet $ip/"; then echo "$tf $el"; exit 0; fi
    ((el < max)) || { echo "$tf $el"; exit 1; }
    sleep 0.1
done'

sc_vip_abschaltung() {
    vip_setup
    local ifc res tf tr
    ifc="$(lan_if node1 "${IP[node1:lan]}")"
    [[ -n "$ifc" ]] || fail "Karte von node1 im lan nicht gefunden"
    # Die Karte bleibt (abgeschaltet) im Container – nur so lässt sich sehen,
    # dass pdh-ha die Adresse selbst abgibt. docker network disconnect nähme
    # die Karte samt Adresse weg.
    log "node1 von vpn und wit trennen, Karte $ifc (lan) abschalten – node1 schaltet ab und gibt die Adresse ab"
    net_off node1 vpn wit || fail "Netz off node1"
    res="$(docker exec node1 bash -c "$ABSCHALT_SH" _ "$ifc" "$VIP_IP" 30)" || fail "node1 gibt die Adresse nicht ab (abgeschaltet nach ${res% *} ms, Ende nach ${res#* } ms)"
    read -r tf tr <<<"$res"
    log "node1 nach $tf ms nicht mehr aktiv, Adresse nach $tr ms abgegeben"
    # Abschaltung: Partner nicht mehr gesehen (3 Takte) + Selbst-Abschaltung
    # (fence_s 5 s); die Adresse folgt binnen 2 s
    ((tf >= 0)) || fail "node1 gab die Adresse ab, ohne abzuschalten"
    ((tr - tf <= 2000)) || fail "Adresse erst $((tr - tf)) ms nach der Abschaltung abgegeben"
    ((tr <= (3 + 5 + 2) * 1000)) || fail "Adresse erst nach $tr ms abgegeben (erwartet höchstens Abschaltzeit 5 s + 3 Takte + 2 s)"
    wait_for 60 eval '[[ "$R2" == aktiv ]] && holder_is node2' || fail "node2 übernimmt nicht mit Adresse (node2=$R2) – $(vip_where)"
    log "node1 wieder verbinden – Wiedereingliederung als Standby"
    docker exec node1 ip link set dev "$ifc" up || fail "Karte $ifc auf node1 einschalten"
    net_on node1 vpn wit || fail "Netz on node1"
    wait_for 120 mirror_ok_rev || fail "node1 nach Rückkehr nicht synchroner Standby ($R1, pg=$(jget pg <<<"$J1"))"
    watch_for 5 eval 'holder_is node2' || fail "Adresse bleibt nicht bei node2 – $(vip_where)"
}

sc_vip_duplikat() {
    vip_enter
    vip_test
    local gif
    gif="$(lan_if gw "$GW_IP")"
    [[ -n "$gif" ]] || fail "Karte von gw nicht gefunden"
    log "gw belegt $VIP selbst, dann einschalten – node1 (aktiv) darf nicht übernehmen"
    docker exec gw ip addr add "$VIP" dev "$gif" || fail "Adresse auf gw setzen"
    docker exec node1 pdh-ha access --vip "$VIP" --vip-on || fail "mitwandernde Adresse einschalten"
    wait_for 20 eval 'event_on node1 "wird noch von einem anderen Gerät benutzt"' ||
        fail "kein Alarm „wird noch von einem anderen Gerät benutzt“ auf node1"
    watch_for 8 eval '! vip_here node1 && ! vip_here node2' || fail "Adresse trotz Belegung übernommen – $(vip_where)"
    vip_watch_start
    log "gw gibt die Adresse ab – node1 übernimmt im nächsten Takt"
    docker exec gw ip addr del "$VIP" dev "$gif" || fail "Adresse auf gw entfernen"
    # Takt 1 s + Duplikat-Prüfung 2 s
    vip_wait node1 5 || fail "node1 übernimmt nicht binnen 5 s – $(vip_where)"
    log "node1 trägt die Adresse nach $WAITED ms"
    wait_for 10 eval 'holder_is node1 && gw_ping' || fail "nach der Übernahme – $(vip_where)"
}

# Läuft auf dem Halter: Adresse entfernen, dann alle 0,1 s prüfen, ob sie
# wieder da ist (Grenze in Millisekunden). Ausgabe: Millisekunden.
WIEDER_SH='
ifc=$1 cidr=$2 max=$3
ip addr del "$cidr" dev "$ifc" || exit 2
t0=$(date +%s%3N)
while :; do
    el=$(($(date +%s%3N) - t0))
    if ip -o -4 addr show dev "$ifc" | grep -q " inet $cidr "; then echo "$el"; exit 0; fi
    ((el < max)) || { echo "$el"; exit 1; }
    sleep 0.1
done'

sc_vip_entfernt() {
    vip_setup
    local ifc ms
    ifc="$(lan_if node1 "$VIP_IP")"
    [[ -n "$ifc" ]] || fail "Karte mit $VIP_IP auf node1 nicht gefunden"
    # Grenze 3,5 s: bis 1 s bis zum nächsten Abgleich (Takt des Arbeiters),
    # 2 s Duplikat-Prüfung (arping -D -c 2), Rest für Setzen und Messen
    log "Adresse auf node1 von Hand entfernen – binnen 3,5 s wieder da"
    ms="$(docker exec node1 bash -c "$WIEDER_SH" _ "$ifc" "$VIP" 3500)" || fail "Adresse nach $ms ms nicht wieder da – $(vip_where)"
    log "Adresse nach $ms ms wieder gesetzt"
    wait_for 10 eval 'event_on node1 "war nicht mehr gesetzt – wird wieder übernommen"' || fail "kein Ereignis „war nicht mehr gesetzt“ auf node1"
    wait_for 10 eval 'holder_is node1 && gw_ping' || fail "nach der Rückkehr – $(vip_where)"
}

# vip_never <Container>: bricht ab, sobald der Server die Adresse trägt
vip_never() {
    if vip_here "$1"; then
        fail "$1 holt die Adresse – $(vip_where)"
    fi
    return 0
}

sc_vip_neustart() {
    vip_setup
    log "node1 (aktiv) anhalten – node2 übernimmt die Adresse"
    docker stop -t 2 node1 >/dev/null || fail "docker stop node1"
    wait_for 60 eval '[[ "$R2" == aktiv ]] && holder_is node2' || fail "node2 nach 60 s nicht aktiv mit Adresse (node2=$R2) – $(vip_where)"
    log "node1 starten – holt die Adresse nicht, wird Standby"
    docker start node1 >/dev/null || fail "docker start node1"
    wait_for 120 eval 'vip_never node1 && mirror_ok_rev' || fail "node1 nicht synchroner Standby ($R1, pg=$(jget pg <<<"$J1"))"
    watch_for 10 eval 'vip_never node1 && holder_is node2' || fail "Adresse bleibt nicht bei node2 – $(vip_where)"
}

HOOK_SH='#!/bin/sh
echo "$PDH_HA_ROLLE $PDH_HA_AMTSZEIT $PDH_HA_VIP" >>/var/lib/pdh-ha/hook.log
'

# hook_last <Container>: letzte Zeile des Skript-Protokolls
hook_last() { docker exec "$1" tail -n 1 /var/lib/pdh-ha/hook.log 2>/dev/null || true; }

# hooks_logged: node2 meldet aktiv, node1 passiv – beide mit Amtszeit von node2
hooks_logged() {
    [[ "$(hook_last node2)" == "aktiv $T2 $VIP_IP" && "$(hook_last node1)" == "passiv $T2 $VIP_IP" ]]
}

sc_hooks() {
    local c
    log "Skript 10-log auf beiden Servern ablegen, Skripte einschalten"
    for c in node1 node2; do
        docker exec -i "$c" sh -c 'install -d -m 0755 /etc/pdh/ha-hooks && cat >/etc/pdh/ha-hooks/10-log && chmod 0755 /etc/pdh/ha-hooks/10-log' \
            <<<"$HOOK_SH" || fail "Skript auf $c ablegen"
    done
    # Adresse nur eintragen (nicht einschalten): die Skripte kennen sie trotzdem
    docker exec node1 pdh-ha access --vip "$VIP" --hooks-on || fail "Skripte einschalten"
    wait_for 30 eval '[[ "$(jget hookson <<<"$J1")" == ja && "$(jget hookson <<<"$J2")" == ja ]]' ||
        fail "Skripte nicht auf beiden eingeschaltet"
    log "Rollen tauschen – node2 meldet aktiv, node1 passiv mit gleicher Amtszeit"
    swap_on node1 || fail "Rollentausch auf node1"
    wait_for 120 mirror_ok_rev || fail "Tausch zu node2 nicht abgeschlossen (node1=$R1, node2=$R2)"
    wait_for 30 hooks_logged ||
        fail "Skript-Protokoll: node1 „$(hook_last node1)“, node2 „$(hook_last node2)“ (erwartet passiv/aktiv $T2 $VIP_IP)"
    log "node1: $(hook_last node1) – node2: $(hook_last node2)"
    # Ausgangslage: beide kennen das Skript und melden „gleich“ – sonst wäre
    # „nein“ danach nicht eindeutig (fehlender Block ergibt ebenfalls „nein“).
    # Der Ordner wird alle 30 s neu gelesen.
    wait_for 45 eval '[[ "$(jget hooksn <<<"$J1")" == 1 && "$(jget hooksn <<<"$J2")" == 1 &&
        "$(jget hookssame <<<"$J1")" == ja && "$(jget hookssame <<<"$J2")" == ja ]]' ||
        fail "Ausgangslage: Skripte nicht auf beiden gleich (node1 $(jget hooksn <<<"$J1") Skript(e) same=$(jget hookssame <<<"$J1"), node2 $(jget hooksn <<<"$J2") same=$(jget hookssame <<<"$J2"))"
    log "Skript nur auf node2 ändern – beide melden „anders als beim Partner“"
    docker exec node2 sh -c 'echo "# geändert" >>/etc/pdh/ha-hooks/10-log' || fail "Skript auf node2 ändern"
    wait_for 90 eval '[[ "$(jget hookssame <<<"$J1")" == nein && "$(jget hookssame <<<"$J2")" == nein ]]' ||
        fail "Status zeigt keine Abweichung (node1 same=$(jget hookssame <<<"$J1"), node2 same=$(jget hookssame <<<"$J2"))"
}

sc_umstellung() {
    local ifc old=10.77.1.11 new=10.77.1.13 c
    ifc="$(lan_if node1 "$old")"
    [[ -n "$ifc" ]] || fail "Karte von node1 im lan nicht gefunden"
    log "Schritt 1: node1 bekommt zusätzlich $new auf $ifc und nimmt sie als Absenderadresse"
    docker exec node1 ip addr add "$new/24" dev "$ifc" || fail "$new auf node1 setzen"
    docker exec node1 ip route replace 10.77.1.0/24 dev "$ifc" proto kernel scope link src "$new" ||
        fail "Absenderadresse auf node1 setzen"
    log "Schritt 2: Adressen von node1 ändern (aktiv, mit Neustart von PostgreSQL)"
    docker exec node1 pdh-ha addrs set node1 "$new,${IP[node1:vpn]},${IP[node1:wit]}" --neustart || fail "addrs set auf node1"
    wait_for 60 eval 'event_on node1 "wird für neue Adressen neu gestartet"' || fail "PostgreSQL auf node1 nicht neu gestartet"
    # der Spiegel holt die Daten jetzt über die neue Adresse
    wait_for 90 eval 'mirror_ok && [[ "$(psql_on node2 "SELECT sender_host FROM pg_stat_wal_receiver" postgres)" == "$new" ]]' ||
        fail "Spiegel nach dem Adresswechsel nicht synchron über $new (node1 sync=$(jget sync <<<"$J1"), node2 pg=$(jget pg <<<"$J2"), Quelle $(psql_on node2 "SELECT sender_host FROM pg_stat_wal_receiver" postgres))"
    for c in node1 node2; do
        ! event_on "$c" "nicht auf die neuen Adressen umgestellt" || fail "Alarm auf $c: PostgreSQL nicht umgestellt"
    done
    log "Schritt 3: Test mit freier Testadresse"
    vip_test 10.77.1.60/24
    log "Schritt 4: $old auf node1 entfernen – wird zur mitwandernden Adresse"
    docker exec node1 ip addr del "$old/24" dev "$ifc" || fail "$old auf node1 entfernen"
    # ohne promote_secondaries ginge $new mit weg
    vip_here node1 "$new" || docker exec node1 ip addr add "$new/24" dev "$ifc" || fail "$new auf node1 wieder setzen"
    docker exec node1 ip route replace 10.77.1.0/24 dev "$ifc" proto kernel scope link src "$new" ||
        fail "Absenderadresse auf node1 setzen"
    VIP=$old/24 VIP_IP=$old
    docker exec node1 pdh-ha access --vip "$VIP" --vip-on || fail "mitwandernde Adresse $VIP einschalten"
    wait_for 20 eval 'holder_is node1' || fail "node1 hält $VIP_IP nicht – $(vip_where)"
    vip_watch_start
    wait_for 10 gw_ping || fail "gw erreicht $VIP_IP bei node1 nicht"
    log "Schritt 5: Rollen tauschen – node2 hält $VIP_IP"
    swap_on node1 || fail "Rollentausch auf node1"
    wait_for 120 eval 'mirror_ok_rev && holder_is node2' ||
        fail "Tausch nicht abgeschlossen (node1=$R1 pg=$(jget pg <<<"$J1"), node2=$R2) – $(vip_where)"
    wait_for 10 gw_ping || fail "gw erreicht $VIP_IP bei node2 nicht"
    # node1 spiegelt jetzt von node2 – mit Absenderadresse $new (pg_hba auf
    # node2 kennt nur noch die neuen Adressen von node1)
    local src
    src="$(psql_on node2 "SELECT client_addr FROM pg_stat_replication" postgres)"
    [[ "$src" == "$new" ]] || fail "node1 spiegelt von $src statt von $new"
}

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

# --- Update im Verbund (Labor-Agent lab-agent, pdh-Platzhalter pdh-dummy) ---
# Stand je Server in /var/lib/pdh-lab/commit (Ausgangsstand lab0), Schalter
# dort als Dateien: fail-install, fail-start-<Commit>, fail-health-<Commit>.
# Prüffrist im Labor 60 s (PDH_HA_UPDATE_VERIFY), Countdown 1 min.
LAB_DIR=/var/lib/pdh-lab

# lab_switch <Container> <Schalter> on|off
lab_switch() {
    if [[ "$3" == on ]]; then
        docker exec "$1" touch "$LAB_DIR/$2"
    else
        docker exec "$1" rm -f "$LAB_DIR/$2"
    fi
}

# up_api <Container> <Methode> <Pfad> [<JSON>]: Socket-API, Ausgabe „<HTTP-Code> <Rumpf>“
up_api() {
    local out
    out="$(docker exec "$1" curl -sS --unix-socket /run/pdh-ha/api.sock -X "$2" -H 'Content-Type: application/json' \
        ${4:+-d "$4"} -w '\n%{http_code}' "http://pdh-ha$3" 2>&1)"
    printf '%s %s\n' "$(tail -n 1 <<<"$out")" "$(sed '$d' <<<"$out" | tr '\n' ' ')"
}

# up_prepare <Countdown min> <Höchstdauer min>: Wartungsfenster setzen, auf
# beiden Servern abwarten; Ausgangsstand lab0 auf beiden
up_prepare() {
    local r
    r="$(up_api node1 PUT /v1/window '{"mode":"off","weekday":0,"begin":"after_files","clock":"02:00","latest":"05:00","install":true,"kind":"rolling","swap":false,"countdown_min":'"$1"',"lock_max_min":'"$2"'}')"
    [[ "$r" == 200* ]] || fail "Wartungsfenster setzen: $r"
    UP_WIN="$1/$2"
    wait_for 30 eval '[[ "$(jget window <<<"$J1")" == "$UP_WIN" && "$(jget window <<<"$J2")" == "$UP_WIN" ]]' ||
        fail "Wartungsfenster kommt nicht an (node1 $(jget window <<<"$J1"), node2 $(jget window <<<"$J2"))"
    wait_for 30 eval 'commits_are lab0 lab0' || fail "Ausgangsstand nicht lab0 auf beiden"
}

# up_start <Container> <Commit> <Art>
up_start() {
    log "Update auf $2 starten ($3, auf $1)"
    docker exec "$1" pdh-ha update start "$2" --kind "$3" || fail "Update-Auftrag auf $1 nicht angelegt"
}

# up_get <Schlüssel>: Auftrag aus Sicht des aktiven Servers (letzter snap)
up_get() {
    if [[ "$R2" == aktiv ]]; then jget "$1" <<<"$J2"; else jget "$1" <<<"$J1"; fi
}

# up_phase_in <Phase …>: Phase des Auftrags (aktiver Server) ist eine davon;
# jeder Wechsel kommt ins Protokoll
UP_LAST=""
up_phase_in() {
    local p
    p="$(up_get upphase)"
    if [[ "$p" != "$UP_LAST" ]]; then
        log "  Phase: $p"
        UP_LAST=$p
    fi
    [[ " $* " == *" $p "* ]]
}

# up_wait <Sekunden> <Phase …>: bis der Auftrag eine der Phasen erreicht
up_wait() {
    local t=$1
    shift
    log "warten auf Phase: $*"
    wait_for "$t" up_phase_in "$@" ||
        fail "Phase $* nicht erreicht (zuletzt $(up_get upphase), Ergebnis: $(up_get upresult))"
}

# up_result_has <Text>: Ergebnis in „pdh-ha update status“ des aktiven Servers
up_result_has() {
    local c=node1 out
    [[ "$R2" == aktiv ]] && c=node2
    out="$(docker exec "$c" pdh-ha update status 2>&1)"
    log "update status ($c): $(tr '\n' ';' <<<"$out")"
    grep -q "Ergebnis: .*$1" <<<"$out" || fail "Ergebnis ohne „$1“: $out"
}

# commits_are <node1> <node2>: eigener Stand und Sicht des Partners
commits_are() {
    [[ "$(jget commit <<<"$J1")" == "$1" && "$(jget commit <<<"$J2")" == "$2" &&
        "$(jget peercommit:node2 <<<"$J1")" == "$2" && "$(jget peercommit:node1 <<<"$J2")" == "$1" ]]
}

# faulty_has <Commit>: als fehlerhaft gemerkt (Sicht beider Server)
faulty_has() {
    [[ " $(jget faulty <<<"$J1") " == *" $1 "* && " $(jget faulty <<<"$J2") " == *" $1 "* ]]
}

# mig_on <Container> <Commit>: Tabelle mig_<Commit> in lab vorhanden (t/f)
mig_on() { psql_on "$1" "SELECT to_regclass('public.mig_$2') IS NOT NULL"; }

# no_mig <Commit>: Datenbank-Änderung auf keinem Server
no_mig() { [[ "$(mig_on node1 "$1")" == f && "$(mig_on node2 "$1")" == f ]]; }

# up_lock_pause: In der Sperre schreibt niemand (pdh sperrt im Betrieb alle
# Zugänge) – der Schreib-Generator ruht vom Countdown bis zum Ende. Sonst
# gingen beim Zurücksetzen gewollt die Zeilen aus der Sperre verloren.
up_lock_pause() {
    log "Countdown – Schreib-Generator ruht während der Sperre"
    writer_stop
}

up_resume() {
    writer_start
    wait_acks 3 60 || fail "nach dem Auftrag keine Speicherungen"
}

# up_clean: nach dem Auftrag Wartung aus, keine gescheiterte Aktion
# (Automatik aus) auf einem der Server; Update-Alarme ins Protokoll
up_clean() {
    local c bad
    snap
    [[ "$(jget maint <<<"$J1")" == nein && "$(jget maint <<<"$J2")" == nein ]] ||
        fail "Wartung nach dem Auftrag an (node1 $(jget maint <<<"$J1"), node2 $(jget maint <<<"$J2"))"
    for c in node1 node2; do
        bad="$(docker exec "$c" grep -E 'fehlgeschlagen: .*Automatik aus|Zwei aktive Server' /var/lib/pdh-ha/events.jsonl 2>/dev/null | cut -c1-240)" || true
        [[ -z "$bad" ]] || fail "$c: $bad"
        docker exec "$c" grep -E 'Update-Auftrag .* fehlgeschlagen|Dringend|braucht Hilfe' /var/lib/pdh-ha/events.jsonl 2>/dev/null |
            cut -c1-240 | sed "s/^/  $c: /" || true
    done
}

# lab_ver <Stand>: Version, die pdh-dummy für den Stand meldet (lab0 = VERSION)
lab_ver() {
    local base
    base="$(tr -d '[:space:]' <"$repo/VERSION")"
    if [[ "$1" == lab0 ]]; then echo "$base"; else echo "$base-$1"; fi
}

# no_event <Muster>: kein Ereignis dieser Art auf einem der Server
no_event() {
    local c hit
    for c in node1 node2; do
        hit="$(docker exec "$c" grep -E -- "$1" /var/lib/pdh-ha/events.jsonl 2>/dev/null | head -1 | cut -c1-240)" || true
        [[ -z "$hit" ]] || fail "$c: unerwartetes Ereignis: $hit"
    done
}

# agent_runs <Container> <Op> <Stand>: wie oft lab-agent diese Op ausführte
agent_runs() {
    docker exec "$1" grep -cE " $2 $3\$" /var/lib/pdh-lab/agent.log 2>/dev/null || true
}

# rollback_once <Container>: genau ein Zurückdrehen auf lab0 (C2: keine
# Wiederholungsschleife, obwohl pdh-ha dabei neu startet)
rollback_once() {
    local n
    n="$(agent_runs "$1" rollback lab0)"
    log "$1: Zurückdrehen auf lab0 lief ${n:-0}-mal"
    [[ "$n" == 1 ]] || fail "$1: Zurückdrehen lief ${n:-0}-mal statt einmal"
}

sc_update_ok() {
    up_prepare 1 30
    # C1: Start ohne --version; node2 läuft nach dem Einspielen als
    # eingeschränkter Spiegel (migrations_ok=false), der Auftrag übernimmt
    # dessen Version und zeigt sie im Ergebnis
    up_start node1 lab1 rolling
    up_wait 600 fertig abgebrochen
    up_result_has "Update auf $(lab_ver lab1) abgeschlossen"
    wait_for 120 eval 'mirror_ok_rev && commits_are lab1 lab1' ||
        fail "nicht beide auf lab1 mit node2 aktiv (node1=$R1 $(jget commit <<<"$J1"), node2=$R2 $(jget commit <<<"$J2"))"
    wait_for 30 eval '[[ "$(mig_on node2 lab1)" == t && "$(mig_on node1 lab1)" == t ]]' || fail "Datenbank-Änderung lab1 fehlt"
    wait_acks 5 30 || fail "nach dem Update keine Speicherungen"
    up_clean
}

sc_update_b_vorher() {
    up_prepare 1 30
    log "pdh auf node2 mit lab1 meldet sich nicht gesund"
    lab_switch node2 fail-health-lab1 on || fail "Schalter setzen"
    up_start node1 lab1 rolling
    up_wait 600 fertig abgebrochen
    up_result_has "Prüffrist überschritten"
    wait_for 120 eval 'mirror_ok && commits_are lab0 lab0' || fail "nicht beide zurück auf lab0 mit node1 aktiv"
    faulty_has lab1 || fail "lab1 nicht als fehlerhaft gemerkt ($(jget faulty <<<"$J1"))"
    no_mig lab1 || fail "Datenbank-Änderung lab1 vorhanden"
    # C2: pdh-ha auf node2 startet beim Zurückdrehen neu – trotzdem kein
    # falscher Alarm und kein zweiter Lauf
    no_event 'braucht Hilfe'
    rollback_once node2
    up_clean
}

sc_update_b_nachher() {
    up_prepare 1 30
    up_start node1 lab1 rolling
    up_wait 600 countdown
    up_lock_pause
    log "pdh auf node2 mit lab1 wird nach dem Tausch krank"
    lab_switch node2 fail-health-lab1 on || fail "Schalter setzen"
    up_wait 300 pruefen_neu
    wait_for 60 eval '[[ "$(mig_on node2 lab1)" == t ]]' || fail "Datenbank-Änderung lab1 auf node2 nicht eingespielt"
    up_wait 300 zuruecksetzen
    up_abort_refused "Zurücksetzen läuft bereits"
    up_wait 600 fertig abgebrochen
    up_result_has "Prüfung der neuen Version fehlgeschlagen"
    wait_for 180 eval 'mirror_ok && commits_are lab0 lab0' || fail "nicht beide zurück auf lab0 mit node1 aktiv"
    faulty_has lab1 || fail "lab1 nicht als fehlerhaft gemerkt"
    no_mig lab1 || fail "Datenbank-Änderung lab1 nach dem Zurücksetzen noch vorhanden"
    # C2: Zurückdrehen mit Neustart von pdh-ha auf node2 – kein Dringend-Alarm,
    # keine Wiederholungsschleife bis zur Höchstdauer
    no_event 'Dringend'
    rollback_once node2
    up_resume
    up_clean
}

# up_abort_refused <Text>: „pdh-ha update abort“ am aktiven Server wird mit
# diesem Grund abgelehnt (I7). Wechselt gerade der aktive Server, erneut versuchen.
up_abort_refused() {
    local i c out
    for ((i = 0; i < 15; i++)); do
        snap
        c=node1
        [[ "$R2" == aktiv ]] && c=node2
        if out="$(docker exec "$c" pdh-ha update abort 2>&1)"; then
            fail "Abbruch angenommen statt abgelehnt ($c): $out"
        fi
        log "update abort auf $c: $out"
        [[ "$out" == *"$1"* ]] && return 0
        sleep 1
    done
    fail "Abbruch nicht mit „$1“ abgelehnt: $out"
}

sc_update_abbruch() {
    up_prepare 1 30
    up_start node1 lab1 rolling
    up_wait 600 countdown
    up_lock_pause
    lab_switch node2 fail-health-lab1 on || fail "Schalter setzen"
    up_wait 300 pruefen_neu
    # I6: Handtausch, Übernahme erzwingen und Wartung neben dem Auftrag abgewiesen
    local r
    r="$(up_api node2 POST /v1/command '{"cmd":"swap"}')"
    log "Handtausch während des Auftrags: $r"
    [[ "$r" != 200* && "$r" == *"update abort"* ]] || fail "Handtausch nicht abgewiesen: $r"
    r="$(up_api node1 POST /v1/command '{"cmd":"force_takeover"}')"
    log "Übernahme erzwingen während des Auftrags: $r"
    [[ "$r" != 200* ]] || fail "Übernahme erzwingen nicht abgewiesen: $r"
    if r="$(docker exec node2 pdh-ha maintenance on 2>&1)"; then
        fail "Wartung während des Auftrags angenommen: $r"
    fi
    [[ "$r" == *"update abort"* ]] || fail "Wartung ohne Hinweis abgewiesen: $r"
    log "Abbruch in der Sperre auf dem aktiven node2"
    docker exec node2 pdh-ha update abort || fail "update abort auf node2"
    up_wait 600 fertig abgebrochen
    up_result_has "abgebrochen von Befehlszeile"
    wait_for 180 eval 'mirror_ok && commits_are lab0 lab0' || fail "nicht beide zurück auf lab0 mit node1 aktiv"
    ! faulty_has lab1 || fail "lab1 nach Abbruch als fehlerhaft gemerkt"
    no_mig lab1 || fail "Datenbank-Änderung lab1 nach dem Zurücksetzen noch vorhanden"
    up_resume
    up_clean
}

sc_update_neustart() {
    up_prepare 1 30
    log "Teil 1: node1 (dirigiert) vor der Sperre neu starten → zurückdrehen, abbrechen"
    lab_switch node2 fail-health-lab1 on || fail "Schalter setzen"
    up_start node1 lab1 rolling
    up_wait 600 pruefen_vorher
    docker restart -t 2 node1 >/dev/null || fail "docker restart node1"
    up_wait 300 fertig abgebrochen
    up_result_has "abgebrochen von Neustart"
    lab_switch node2 fail-health-lab1 off || fail "Schalter entfernen"
    wait_for 180 eval 'mirror_ok && commits_are lab0 lab0' || fail "nach Teil 1 nicht beide auf lab0 mit node1 aktiv"
    log "Teil 2: node2 (dirigiert) in der Sperre neu starten → Auftrag setzt fort"
    up_start node1 lab1 rolling
    up_wait 600 countdown
    up_lock_pause
    lab_switch node2 fail-health-lab1 on || fail "Schalter setzen"
    up_wait 300 pruefen_neu
    lab_switch node2 fail-health-lab1 off || fail "Schalter entfernen"
    docker restart -t 2 node2 >/dev/null || fail "docker restart node2"
    up_wait 600 fertig abgebrochen
    up_result_has "abgeschlossen"
    wait_for 180 eval 'mirror_ok_rev && commits_are lab1 lab1' || fail "nicht beide auf lab1 mit node2 aktiv"
    up_resume
    up_clean
}

sc_update_nicht_gruen() {
    up_prepare 1 30
    log "node2 von lan und vpn trennen – Spiegel getrennt"
    net_off node2 lan vpn || fail "Netz off node2"
    wait_for 30 eval '[[ "$(jget seen:node2 <<<"$J1")" == nein ]]' || fail "node1 sieht node2 noch"
    local r
    r="$(up_api node1 POST /v1/update/start '{"commit":"lab1","source":"zeitplan","kind":"rolling"}')"
    log "Zeitplan: $r"
    [[ "$r" == 409* && "$r" == *'"ok":false'* ]] || fail "Zeitplan nicht abgelehnt: $r"
    ! docker exec node1 pdh-ha update start lab1 --kind rolling || fail "Knopf ohne Rückfrage angenommen"
    snap
    [[ "$(up_get upphase)" == - ]] || fail "Auftrag trotzdem angelegt (Phase $(up_get upphase))"
    net_on node2 lan vpn || fail "Netz on node2"
    wait_for 120 eval 'mirror_ok && commits_are lab0 lab0' || fail "nach der Trennung kein synchroner Spiegel auf lab0"
    up_clean
}

sc_update_nacheinander() {
    up_prepare 1 30
    up_start node1 lab1 sequential
    up_wait 900 fertig abgebrochen
    up_result_has "abgeschlossen"
    wait_for 180 eval 'mirror_ok && commits_are lab1 lab1' || fail "nicht beide auf lab1 mit node1 aktiv"
    wait_for 30 eval '[[ "$(mig_on node1 lab1)" == t && "$(mig_on node2 lab1)" == t ]]' || fail "Datenbank-Änderung lab1 fehlt"
    wait_acks 5 30 || fail "nach dem Update keine Speicherungen"
    up_clean
}

sc_update_nacheinander_fehler() {
    up_prepare 1 30
    log "pdh auf node1 mit lab1 meldet sich nicht gesund"
    lab_switch node1 fail-health-lab1 on || fail "Schalter setzen"
    up_start node1 lab1 sequential
    up_wait 600 countdown
    up_lock_pause
    up_wait 300 pruefen_neu
    wait_for 60 eval '[[ "$(mig_on node1 lab1)" == t ]]' || fail "Datenbank-Änderung lab1 auf node1 nicht eingespielt"
    up_wait 600 fertig abgebrochen
    up_result_has "Prüfung der neuen Version fehlgeschlagen"
    wait_for 180 eval 'mirror_ok_rev && commits_are lab0 lab0' || fail "nicht beide zurück auf lab0 mit node2 aktiv"
    faulty_has lab1 || fail "lab1 nicht als fehlerhaft gemerkt"
    no_mig lab1 || fail "Datenbank-Änderung lab1 nach dem Zurücksetzen noch vorhanden"
    up_resume
    up_clean
}

# I2: Der Tausch findet nicht statt (node2 getrennt). Der Auftrag wiederholt
# den Tausch-Befehl und setzt nach seiner eigenen Tauschfrist (3 min) zurück –
# nicht erst nach der Höchstdauer der Sperre (10 min).
sc_update_tausch_frist() {
    up_prepare 1 10
    up_start node1 lab1 rolling
    up_wait 600 countdown
    up_lock_pause
    log "node2 von lan und vpn trennen – der Tausch kann nicht stattfinden"
    net_off node2 lan vpn || fail "Netz off node2"
    up_wait 180 gesperrt
    local t=$SECONDS
    up_wait 330 zuruecksetzen
    ((SECONDS - t < 330)) || fail "Zurücksetzen erst nach $((SECONDS - t)) s"
    log "nach $((SECONDS - t)) s Zurücksetzen (Tauschfrist): node2 wieder verbinden"
    net_on node2 lan vpn || fail "Netz on node2"
    up_wait 600 fertig abgebrochen
    up_result_has "Rollentausch nicht gelungen"
    ((SECONDS - t < 600)) || fail "Sperre dauerte bis zur Höchstdauer ($((SECONDS - t)) s)"
    wait_for 180 eval 'mirror_ok && commits_are lab0 lab0' || fail "nicht beide zurück auf lab0 mit node1 aktiv"
    ! faulty_has lab1 || fail "lab1 als fehlerhaft gemerkt (nur der Tausch scheiterte)"
    no_mig lab1 || fail "Datenbank-Änderung lab1 vorhanden"
    no_event 'Dringend'
    up_resume
    up_clean
}

# C3: „alle nacheinander“ mit „Trotzdem“, der Spiegel hinkt hinterher (WAL-
# Einspielen auf node2 angehalten). node1 wird abgeschottet, der Gleichstand
# kommt nicht – node2 wird NICHT eingefroren, node1 arbeitet weiter, nichts
# geht verloren (Zeile nach dem Anhalten bleibt erhalten), lab1 nicht fehlerhaft.
sc_update_nacheinander_gleichstand() {
    up_prepare 1 30
    local q_tab="SELECT to_regclass('public.c3_probe') IS NOT NULL"
    local q_row="SELECT count(*) FROM c3_probe WHERE x = 'nach-anhalten'"
    psql_on node1 "CREATE TABLE IF NOT EXISTS c3_probe (x text)" >/dev/null
    wait_for 30 eval '[[ "$(psql_on node2 "$q_tab")" == t ]]' || fail "Prüftabelle nicht auf node2"
    log "WAL-Einspielen auf node2 anhalten – Spiegel empfängt, spielt aber nicht ein"
    psql_on node2 "SELECT pg_wal_replay_pause()" >/dev/null
    wait_for 10 eval '[[ "$(psql_on node2 "SELECT pg_get_wal_replay_pause_state()")" == paused ]]' ||
        fail "Einspielen auf node2 nicht angehalten"
    psql_on node1 "INSERT INTO c3_probe VALUES ('nach-anhalten')" >/dev/null
    [[ "$(psql_on node1 "$q_row")" == 1 ]] || fail "Zeile nach dem Anhalten nicht gespeichert"
    local r
    r="$(up_api node1 POST /v1/update/start '{"commit":"lab1","kind":"sequential","force":true}')"
    log "Start mit Trotzdem: $r"
    [[ "$r" == 200* ]] || fail "Auftrag mit Trotzdem nicht angelegt: $r"
    up_wait 600 countdown
    up_lock_pause
    up_wait 600 fertig abgebrochen
    up_result_has "Spiegel nicht auf Gleichstand"
    [[ "$(psql_on node2 "SELECT pg_is_in_recovery()")" == t ]] || fail "node2 nicht mehr als Standby erreichbar (eingefroren?)"
    log "WAL-Einspielen auf node2 fortsetzen"
    psql_on node2 "SELECT pg_wal_replay_resume()" >/dev/null
    wait_for 180 eval 'mirror_ok && commits_are lab0 lab0' || fail "nicht beide auf lab0 mit node1 aktiv"
    [[ "$(psql_on node1 "$q_row")" == 1 ]] || fail "Zeile nach dem Anhalten auf node1 verloren"
    wait_for 60 eval '[[ "$(psql_on node2 "$q_row")" == 1 ]]' || fail "Zeile nach dem Anhalten kommt nicht auf node2 an"
    ! faulty_has lab1 || fail "lab1 als fehlerhaft gemerkt (nur der Spiegel hinkte)"
    no_mig lab1 || fail "Datenbank-Änderung lab1 vorhanden"
    no_event 'Dringend'
    up_resume
    up_clean
}

run_scenario() {
    local name=$1 fn
    fn="sc_${name//-/_}"
    declare -F "$fn" >/dev/null || { echo "Unbekanntes Szenario: $name" >&2; exit 2; }
    echo "=== Szenario $name"
    LAST_LINE="" T0=$SECONDS
    : >"$ACKED"
    : >"$VIP_BAD"
    trap 'writer_stop; vip_watch_stop' EXIT
    lab_up "${SC_MODE[$name]:-real}"
    writer_start
    wait_acks 3 30 || fail "Schreib-Generator bestätigt nichts"
    "$fn" || fail "Szenario $name abgebrochen"
    vip_watch_stop
    # Befund des Wächters nach dem letzten snap
    if [[ -s "$VIP_BAD" ]]; then
        fail "Adresse gleichzeitig auf node1 und node2: $(head -1 "$VIP_BAD")"
    fi
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
    # Quellen als tar über stdin statt Bind-Mount: läuft so auch aus einem
    # Hilfscontainer heraus (dessen Pfade kennt der Docker-Dienst nicht)
    tar -C "$repo" -cf - go.mod go.sum internal/ha cmd/pdh-ha scripts/pdh-ha-adresse.sh scripts/pdh-ha-join.sh |
        docker run --rm -i -e CGO_ENABLED=1 golang:1.24 bash -c '
        set -euo pipefail
        mkdir -p /work
        tar -xf - -C /work
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
