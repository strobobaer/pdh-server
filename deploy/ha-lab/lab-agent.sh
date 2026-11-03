#!/bin/bash
# Platzhalter für den Update-Agenten im Testlabor (pdh-ha ruft ihn über
# PDH_HA_AGENT_CMD auf):
#   lab-agent install|install_norestart|rollback|rollback_norestart <commit>
#   lab-agent finish
# „Einspielen“ heißt hier: Commit nach $PDH_HA_COMMIT_FILE schreiben (pdh-ha
# liest ihn je Takt) und pdh-dummy neu starten. install_norestart merkt den
# Commit nur vor; finish setzt ihn ein. rollback startet danach pdh-ha neu
# (wie install-pdh-ha.sh im echten Betrieb), rollback_norestart nicht.
# Schalter (Dateien):
#   /var/lib/pdh-lab/fail-install  → install/install_norestart scheitern
#   /var/lib/pdh-lab/slow-install  → install braucht 40 s (Bau dauert)
#   /var/lib/pdh-lab/refuse-install → install/install_norestart verweigern vor
#                                    jeder Änderung (wie install_update.sh: „…; nichts geaendert.“)
# Läufe gehen nacheinander (Sperrdatei) – wie beim echten Agenten, bei dem
# pdh-ha auf einen laufenden fremden Lauf wartet und dann erneut sendet.
set -uo pipefail
dir=/var/lib/pdh-lab
cf=${PDH_HA_COMMIT_FILE:-$dir/commit}
pending=$dir/pending
mkdir -p "$dir"
op=${1:-}
c=${2:-}
exec 9>"$dir/agent.lock"
flock 9
echo "$(date +%H:%M:%S) $op $c" >>"$dir/agent.log"

# set_commit <commit>: atomar schreiben (pdh-ha liest je Takt)
set_commit() {
    printf '%s\n' "$1" >"$cf.tmp" && mv -f "$cf.tmp" "$cf"
}

case "$op" in
    install)
        [[ -n "$c" ]] || exit 2
        [[ -e $dir/fail-install ]] && { echo "Einspielen scheitert (Schalter fail-install)" >&2; exit 1; }
        [[ -e $dir/refuse-install ]] && { echo "Ziel $c ist nicht neuer als der installierte Stand $(cat "$cf" 2>/dev/null); nichts geaendert." >&2; exit 1; }
        [[ -e $dir/slow-install ]] && sleep 40
        rm -f "$pending"
        set_commit "$c"
        exec /usr/local/bin/pdh-dummy restart 9>&- # Hintergrund-Migration ohne die Sperrdatei
        ;;
    install_norestart)
        [[ -n "$c" ]] || exit 2
        [[ -e $dir/fail-install ]] && { echo "Bauen scheitert (Schalter fail-install)" >&2; exit 1; }
        [[ -e $dir/refuse-install ]] && { echo "Ziel $c ist nicht neuer als der installierte Stand $(cat "$cf" 2>/dev/null); nichts geaendert." >&2; exit 1; }
        printf '%s\n' "$c" >"$pending"
        ;;
    finish)
        [[ -s $pending ]] || { echo "kein vorbereiteter Stand" >&2; exit 1; }
        set_commit "$(cat "$pending")"
        rm -f "$pending"
        exec /usr/local/bin/pdh-dummy restart 9>&- # Hintergrund-Migration ohne die Sperrdatei
        ;;
    rollback)
        [[ -n "$c" ]] || exit 2
        rm -f "$pending"
        set_commit "$c"
        /usr/local/bin/pdh-dummy restart 9>&- || exit 1
        # Wie das echte install_update.sh --rollback (über install-pdh-ha.sh):
        # pdh-ha startet neu, bevor das Ergebnis zurückkommt – der laufende
        # Auftrag stirbt ohne Ergebnis (C2). Die Schleife im Einstiegsskript
        # startet pdh-ha wieder; der Lauf hier endet erst danach.
        touch /run/pdh-ha.restart
        pkill -TERM -x pdh-ha || true
        sleep 5
        ;;
    rollback_norestart)
        # nur der vorbereitete Stand verfällt; das laufende Programm bleibt
        rm -f "$pending"
        [[ -n "$c" ]] && set_commit "$c"
        ;;
    *)
        echo "Aufruf: $0 install|install_norestart|rollback|rollback_norestart <commit> | finish" >&2
        exit 2
        ;;
esac
