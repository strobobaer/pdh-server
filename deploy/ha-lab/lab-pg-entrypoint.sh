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
exec pdh-ha run "${run_args[@]}" "$@"
