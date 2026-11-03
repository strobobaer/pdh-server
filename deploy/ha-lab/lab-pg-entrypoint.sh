#!/bin/bash
# Testlabor mit PostgreSQL: Cluster starten, dann pdh-ha als Hauptprozess.
# Daten liegen im Container (bleiben bei stop/start, gehen bei down -v).
set -euo pipefail
ver="$(ls /etc/postgresql | sort -V | tail -1)"
pg_ctlcluster "$ver" main start || echo "PostgreSQL startet nicht (wird ggf. von pdh-ha repariert)" >&2
# Update im Verbund: Ausgangsstand lab0 (lab-agent schreibt später den neuen),
# /health des pdh-Platzhalters auf PDH_SERVER_PORT
cf=${PDH_HA_COMMIT_FILE:-/var/lib/pdh-lab/commit}
mkdir -p "$(dirname "$cf")"
[[ -s "$cf" ]] || echo lab0 >"$cf"
socat "TCP-LISTEN:${PDH_SERVER_PORT:-8090},bind=127.0.0.1,reuseaddr,fork" EXEC:"/usr/local/bin/pdh-dummy http" &
run_args=()
[[ -n "${PDH_HA_VERSION_OVERRIDE:-}" ]] && run_args+=(--version-override "$PDH_HA_VERSION_OVERRIDE")
# pdh-ha läuft in einer Schleife: Beendet es sich nach /run/pdh-ha.restart
# (lab-agent rollback – wie systemctl restart pdh-ha), startet es neu; sonst
# endet der Container. SIGTERM (docker stop/restart) geht an pdh-ha weiter.
rm -f /run/pdh-ha.restart
child=0
trap '[[ $child -gt 0 ]] && kill -TERM "$child" 2>/dev/null; wait "$child"; exit 143' TERM INT
while :; do
    pdh-ha run "${run_args[@]}" "$@" &
    child=$!
    rc=0
    wait "$child" || rc=$?
    [[ -e /run/pdh-ha.restart ]] || exit "$rc"
    rm -f /run/pdh-ha.restart
    echo "pdh-ha neu gestartet (lab-agent rollback)" >&2
done
