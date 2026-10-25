#!/bin/bash
# Start von pdh-ha im Container. Beim ersten Start mit PDH_HA_JOIN als Zeuge
# beitreten, danach den Dienst starten.
#   PDH_HA_JOIN               Beitrittscode (nur für den ersten Start nötig)
#   PDH_HA_ADDR               eigene Adressen, Komma-getrennt, wie die Server
#                             den Zeugen erreichen (z. B. IP des Docker-Hosts)
#   PDH_HA_ID                 Kennung (Standard: Hostname des Containers)
#   PDH_HA_VERSION_OVERRIDE   nur Testlabor: Version vortäuschen
set -euo pipefail

state_dir="${PDH_HA_STATE:-/var/lib/pdh-ha}"

if [[ ! -e "$state_dir/state.json" && -n "${PDH_HA_JOIN:-}" ]]; then
    args=(--witness)
    [[ -n "${PDH_HA_ADDR:-}" ]] && args+=(--addr "$PDH_HA_ADDR")
    [[ -n "${PDH_HA_ID:-}" ]] && args+=(--id "$PDH_HA_ID")
    if [[ -z "${PDH_HA_ADDR:-}" ]]; then
        echo "Hinweis: PDH_HA_ADDR fehlt – es wird die Container-Adresse gemeldet; die Server erreichen den Zeugen dann evtl. nicht." >&2
    fi
    # Code nur über die Umgebung, nicht als Argument (taucht sonst in ps auf)
    pdh-ha join "${args[@]}"
fi
unset PDH_HA_JOIN

run_args=()
[[ -n "${PDH_HA_VERSION_OVERRIDE:-}" ]] && run_args+=(--version-override "$PDH_HA_VERSION_OVERRIDE")
exec pdh-ha run "${run_args[@]}" "$@"
