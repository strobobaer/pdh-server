#!/bin/bash
# Platzhalter für den Update-Agenten im Testlabor (pdh-ha ruft ihn über
# PDH_HA_AGENT_CMD auf):
#   lab-agent install|install_norestart|rollback|rollback_norestart <commit>
#   lab-agent finish
# „Einspielen“ heißt hier: Commit nach $PDH_HA_COMMIT_FILE schreiben (pdh-ha
# liest ihn je Takt) und pdh-dummy neu starten. install_norestart merkt den
# Commit nur vor; finish setzt ihn ein. Schalter (Dateien):
#   /var/lib/pdh-lab/fail-install  → install/install_norestart scheitern
set -uo pipefail
dir=/var/lib/pdh-lab
cf=${PDH_HA_COMMIT_FILE:-$dir/commit}
pending=$dir/pending
mkdir -p "$dir"
op=${1:-}
c=${2:-}
echo "$(date +%H:%M:%S) $op $c" >>"$dir/agent.log"

# set_commit <commit>: atomar schreiben (pdh-ha liest je Takt)
set_commit() {
    printf '%s\n' "$1" >"$cf.tmp" && mv -f "$cf.tmp" "$cf"
}

case "$op" in
    install)
        [[ -n "$c" ]] || exit 2
        [[ -e $dir/fail-install ]] && { echo "Einspielen scheitert (Schalter fail-install)" >&2; exit 1; }
        rm -f "$pending"
        set_commit "$c"
        exec /usr/local/bin/pdh-dummy restart
        ;;
    install_norestart)
        [[ -n "$c" ]] || exit 2
        [[ -e $dir/fail-install ]] && { echo "Bauen scheitert (Schalter fail-install)" >&2; exit 1; }
        printf '%s\n' "$c" >"$pending"
        ;;
    finish)
        [[ -s $pending ]] || { echo "kein vorbereiteter Stand" >&2; exit 1; }
        set_commit "$(cat "$pending")"
        rm -f "$pending"
        exec /usr/local/bin/pdh-dummy restart
        ;;
    rollback)
        [[ -n "$c" ]] || exit 2
        rm -f "$pending"
        set_commit "$c"
        exec /usr/local/bin/pdh-dummy restart
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
