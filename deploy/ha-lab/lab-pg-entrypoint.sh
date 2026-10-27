#!/bin/bash
# Testlabor mit PostgreSQL: Cluster starten, dann pdh-ha als Hauptprozess.
# Daten liegen im Container (bleiben bei stop/start, gehen bei down -v).
set -euo pipefail
ver="$(ls /etc/postgresql | sort -V | tail -1)"
pg_ctlcluster "$ver" main start || echo "PostgreSQL startet nicht (wird ggf. von pdh-ha repariert)" >&2
run_args=()
[[ -n "${PDH_HA_VERSION_OVERRIDE:-}" ]] && run_args+=(--version-override "$PDH_HA_VERSION_OVERRIDE")
exec pdh-ha run "${run_args[@]}" "$@"
