#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mode="${PDH_UPDATE_MODE:-}"
project_name="${PDH_COMPOSE_PROJECT_NAME:-$(basename "$repo_dir")}"

if [[ "$mode" != "docker" && "$mode" != "systemd" ]]; then
    echo "PDH_UPDATE_MODE must be docker or systemd." >&2
    exit 1
fi

# Schalter (Update im Verbund, vom Update-Agenten aus pdh-ha heraus):
#   --commit <sha>    genau diesen Stand einspielen (muss im Update-Zweig liegen)
#   --rollback <sha>  auf diesen Stand zurueckdrehen (hartes Zuruecksetzen), dann bauen
#   --no-restart      nur bauen, keine Dienste neu starten oder ersetzen
#   --finish          vorher gebaute Dateien einsetzen (kein Bau): pdh gemaess
#                     should-run-pdh neu starten, pdh-ha einsetzen, Agent ersetzen
# Ohne Schalter: aktueller Stand des Update-Zweigs, bauen und neu starten.
want_commit=""
want_rollback=""
no_restart=0
finish=0
while (( $# > 0 )); do
    case "$1" in
        --commit|--rollback)
            flag="$1"
            [[ $# -ge 2 && "$2" =~ ^[0-9a-f]{7,40}$ ]] || { echo "$flag braucht einen Commit (7 bis 40 Hex-Zeichen)." >&2; exit 1; }
            if [[ "$flag" == "--commit" ]]; then want_commit="$2"; else want_rollback="$2"; fi
            shift 2
            ;;
        --no-restart) no_restart=1; shift ;;
        --finish) finish=1; shift ;;
        *) echo "Unbekannter Schalter: $1" >&2; exit 1 ;;
    esac
done
if [[ -n "$want_commit" && -n "$want_rollback" ]]; then
    echo "--commit und --rollback schliessen sich aus." >&2
    exit 1
fi
if (( finish )) && [[ -n "$want_commit$want_rollback" || $no_restart -eq 1 ]]; then
    echo "--finish gilt nur allein." >&2
    exit 1
fi
if [[ "$mode" == "docker" ]] && (( finish || no_restart )) || [[ "$mode" == "docker" && -n "$want_commit$want_rollback" ]]; then
    echo "Gezielte Updates gibt es nur bei systemd-Installationen." >&2
    exit 1
fi
branch="${PDH_UPDATE_BRANCH:-main}"
if [[ ! "$branch" =~ ^[A-Za-z0-9._/-]+$ || "$branch" == -* || "$branch" == *..* ]]; then
    echo "Ungueltiger Update-Zweig: $branch" >&2
    exit 1
fi
repo_uid="$(stat -c '%u' "$repo_dir")"
repo_gid="$(stat -c '%g' "$repo_dir")"

git_as_checkout_owner() {
    if (( EUID == 0 && repo_uid != 0 )); then
        setpriv --reuid "$repo_uid" --regid "$repo_gid" --clear-groups -- git -C "$repo_dir" "$@"
    else
        git -C "$repo_dir" "$@"
    fi
}

if (( EUID == 0 && repo_uid != 0 )) && ! command -v setpriv >/dev/null 2>&1; then
    echo "setpriv is required to update Git as the checkout owner." >&2
    exit 1
fi

remote_url="$(git_as_checkout_owner config --get remote.origin.url || true)"
remote_url="${remote_url%.git}"
remote_url="${remote_url%/}"
case "$remote_url" in
    https://github.com/strobobaer/pdh-server) ;;
    git@github.com:strobobaer/pdh-server|ssh://git@github.com/strobobaer/pdh-server)
        git_as_checkout_owner remote set-url origin https://github.com/strobobaer/pdh-server.git
        ;;
    *)
        echo "origin must point to https://github.com/strobobaer/pdh-server." >&2
        exit 1
        ;;
esac
if [[ -n "$(git_as_checkout_owner diff --name-only)" || -n "$(git_as_checkout_owner diff --cached --name-only)" ]]; then
    echo "The checkout has local changes; refusing to update." >&2
    exit 1
fi

if [[ -n "$want_rollback" ]]; then
    # Zurueckdrehen: der Stand muss lokal vorhanden sein, sonst Abbruch vor jeder Aenderung.
    if ! target="$(git_as_checkout_owner rev-parse --verify --quiet "$want_rollback^{commit}")"         || ! git_as_checkout_owner cat-file -e "$target^{commit}"; then
        echo "Der Stand $want_rollback ist nicht vorhanden; Zurueckdrehen abgebrochen." >&2
        exit 1
    fi
    git_as_checkout_owner reset --hard "$target"
    echo "Zurueckgedreht auf $target."
elif (( finish )); then
    # Einsetzen der vorher gebauten Dateien: Quelltext bleibt, wie er ist.
    target="$(git_as_checkout_owner rev-parse HEAD)"
else
    git_as_checkout_owner fetch --prune origin "$branch"
    current="$(git_as_checkout_owner rev-parse HEAD)"
    target="$(git_as_checkout_owner rev-parse "FETCH_HEAD^{commit}")"
    if [[ -n "$want_commit" ]]; then
        # Ziel muss im Update-Zweig liegen (Pruefung vor jeder Aenderung).
        if ! want_full="$(git_as_checkout_owner rev-parse --verify --quiet "$want_commit^{commit}")"             || ! git_as_checkout_owner merge-base --is-ancestor "$want_full" FETCH_HEAD; then
            echo "Ziel nicht im Update-Zweig ($branch): $want_commit; nichts geaendert." >&2
            exit 1
        fi
        target="$want_full"
        # Nur vorwärts: der installierte Stand muss Vorfahre des Ziels sein –
        # sonst endete das Vorspulen still ohne Änderung (älteres Ziel).
        if [[ "$current" != "$target" ]] && ! git_as_checkout_owner merge-base --is-ancestor "$current" "$target"; then
            echo "Ziel $want_commit ist nicht neuer als der installierte Stand $current; nichts geaendert." >&2
            exit 1
        fi
    fi
    if [[ "$current" == "$target" ]]; then
        echo "Source is already at $target; verifying/rebuilding the runtime."
    else
        if ! git_as_checkout_owner merge --ff-only "$target"; then
            echo "The local branch cannot be fast-forwarded; refusing to overwrite it." >&2
            exit 1
        fi
    fi
fi

if [[ "$mode" == "docker" ]]; then
    env_file="$repo_dir/.env.docker"
    # Grafana (Statistik, ab 0.50): fehlende Passwoerter einmalig erzeugen
    for grafana_key in PDH_GRAFANA_ADMIN_PASSWORD PDH_GRAFANA_DB_PASSWORD; do
        if [[ -f "$env_file" ]] && ! grep -q "^${grafana_key}=." "$env_file"; then
            grafana_value="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
            sed -i "/^${grafana_key}=/d" "$env_file"
            printf '\n%s=%s\n' "$grafana_key" "$grafana_value" >> "$env_file"
            echo "Generated ${grafana_key} in .env.docker."
        fi
    done
    compose=(docker compose --project-name "$project_name" --env-file "$env_file" --project-directory "$repo_dir" -f "$repo_dir/compose.yaml")
    "${compose[@]}" build --build-arg "PDH_BUILD_COMMIT=$target" app updater
    # Docker-Netz (ab 0.72.1 fest, PDH_DOCKER_SUBNET): liegt das vorhandene Netz
    # woanders, alle Dienste herunterfahren, damit Compose es neu anlegt, und die
    # Datenbank zuerst wieder starten (--no-deps unten startet sie nicht).
    # Laeuft im Container pdh-update-job, nicht im Projektnetz.
    want_subnet="$(grep '^PDH_DOCKER_SUBNET=' "$env_file" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)"
    want_subnet="${want_subnet:-172.31.254.0/24}"
    network_id="$(docker network ls -q --filter "label=com.docker.compose.project=$project_name" --filter "label=com.docker.compose.network=default" | head -n 1)"
    if [[ -n "$network_id" ]]; then
        have_subnet="$(docker network inspect "$network_id" --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}')"
        if [[ " $have_subnet " != *" $want_subnet "* ]]; then
            echo "Docker-Netz wird von ${have_subnet% } auf $want_subnet umgestellt (Dienste starten neu)."
            "${compose[@]}" down
            "${compose[@]}" up -d db
        fi
    fi
    "${compose[@]}" up -d --no-deps app updater
    # Grafana separat: ein Fehler (z. B. Image nicht ladbar) darf das Update nicht abbrechen
    "${compose[@]}" up -d --no-deps grafana || echo "Grafana konnte nicht gestartet werden (Statistik) - Update trotzdem fertig." >&2
    # code-server (/code/) ebenso separat
    { "${compose[@]}" build codeserver && "${compose[@]}" up -d --no-deps codeserver; } || echo "code-server konnte nicht gestartet werden (/code/) - Update trotzdem fertig." >&2
else
    # Pfade (fuer Tests ueberschreibbar)
    pdh_ha_bin="${PDH_HA_BIN:-/usr/local/bin/pdh-ha}"
    agent_bin="${PDH_AGENT_BIN:-/usr/local/bin/pdh-update-agent}"
    bin_dir="$repo_dir/bin"
    mkdir -p "$bin_dir"
    if (( finish )); then
        # Vorab gebaute Dateien pruefen, bevor irgendein Dienst angefasst wird.
        if [[ ! -x "$bin_dir/pdh" || ! -x "$bin_dir/update-agent" ]]; then
            echo "Abschluss nicht moeglich: bin/pdh oder bin/update-agent fehlt (erst mit --no-restart bauen)." >&2
            exit 1
        fi
        if [[ -x "$pdh_ha_bin" && ! -x "$bin_dir/pdh-ha.neu" ]]; then
            echo "Abschluss nicht moeglich: bin/pdh-ha.neu fehlt (erst mit --no-restart bauen)." >&2
            exit 1
        fi
        built_commit="$(cat "$bin_dir/.built-commit" 2>/dev/null || true)"
        if [[ "$built_commit" != "$target" ]]; then
            echo "Abschluss nicht moeglich: gebaut wurde ${built_commit:-unbekannt}, der Quelltext steht auf $target." >&2
            exit 1
        fi
    else
        # Go: Das Projekt braucht die Go-Version aus go.mod (derzeit 1.24, wegen des
        # Anthropic-SDK). Aeltere Installationen ab Go 1.21 (z. B. Ubuntu 24.04 mit 1.22)
        # laden die passende Version beim Bauen selbst nach (GOTOOLCHAIN=auto).
        export GOTOOLCHAIN=auto
        go_minor="$(go env GOVERSION 2>/dev/null | sed -nE 's/^go1\.([0-9]+).*/\1/p')"
        if [[ -z "$go_minor" || "$go_minor" -lt 21 ]]; then
            echo "Go $(go env GOVERSION 2>/dev/null || echo '?') ist zu alt: noetig ist mindestens Go 1.21 (laedt dann Go 1.24 selbst nach). Bitte Go aktualisieren." >&2
            exit 1
        fi
        # Alles unter .neu-Namen bauen und erst nach dem letzten erfolgreichen Bau
        # umbenennen: scheitert ein Bau, bleibt die laufende Version unberuehrt.
        rm -f "$bin_dir/.built-commit" "$bin_dir/pdh-ha.neu" "$bin_dir/pdh.neu" "$bin_dir/update-agent.neu"
        (cd "$repo_dir" && go build -buildvcs=false -trimpath -ldflags="-s -w -X main.buildCommit=$target" -o bin/pdh.neu ./cmd/server)
        (cd "$repo_dir" && go build -buildvcs=false -trimpath -ldflags="-s -w" -o bin/update-agent.neu ./cmd/update-agent)
        # pdh-ha mit bauen (nach bin/pdh-ha.neu), solange noch nichts neu gestartet wird
        if (( no_restart )) && [[ -x "$pdh_ha_bin" ]]; then
            bash "$repo_dir/scripts/install-pdh-ha.sh" "$repo_dir" --build-only
        fi
        mv -f "$bin_dir/pdh.neu" "$bin_dir/pdh"
        mv -f "$bin_dir/update-agent.neu" "$bin_dir/update-agent"
        printf '%s\n' "$target" > "$bin_dir/.built-commit"
    fi
    if (( no_restart )); then
        # Nur gebaut: pdh, pdh-ha und Agent bleiben unberuehrt (spaeter --finish).
        echo "Gebaut, keine Dienste neu gestartet."
        echo "Updated to $target."
        exit 0
    fi
    # Zuerst die Anwendung neu starten: ein nicht ersetzbares Agent-Binary
    # (z.B. aeltere systemd-Unit ohne /usr/local/bin in ReadWritePaths oder
    # schreibgeschuetztes Dateisystem) darf das Update nicht halb fertig
    # abbrechen. Im Datenbank-Spiegel läuft pdh am aktiven Server und am
    # passiven als Spiegel (nur lesend) – pdh-ha should-run-pdh: Exit 1 =
    # passiver Server ohne laufenden Datenbank-Spiegel, nicht starten; ältere
    # pdh-ha ohne den Befehl = wie bisher starten.
    should_rc=0
    if [[ -x "$pdh_ha_bin" ]]; then
        "$pdh_ha_bin" should-run-pdh >/dev/null 2>&1 || should_rc=$?
    fi
    if [[ $should_rc -ne 1 ]]; then
        systemctl restart pdh
    else
        echo "pdh bleibt aus (passiver Server, Datenbank-Spiegel läuft nicht)."
    fi
    # Ausfallschutz (pdh-ha): nur wenn eingerichtet. Vor dem Agent-Neustart und
    # per systemd-run außerhalb von Agent-cgroup und -Sandbox (/etc beschreibbar);
    # ein Fehler bricht das normale Update nicht ab, den Abschluss (--finish) schon.
    if [[ -x "$pdh_ha_bin" ]]; then
        # nach --finish: die vorab gebaute Datei einsetzen; sonst bauen und einsetzen
        ha_args=()
        (( ! finish )) || ha_args=(--install-built)
        ha_rc=0
        if command -v systemd-run >/dev/null 2>&1; then
            systemd-run --wait --pipe --collect --quiet --unit=pdh-ha-update-$(date +%s) bash "$repo_dir/scripts/install-pdh-ha.sh" "$repo_dir" ${ha_args[@]+"${ha_args[@]}"} || ha_rc=$?
        else
            bash "$repo_dir/scripts/install-pdh-ha.sh" "$repo_dir" ${ha_args[@]+"${ha_args[@]}"} || ha_rc=$?
        fi
        if (( ha_rc != 0 )); then
            if (( finish )); then
                echo "pdh-ha konnte nicht eingesetzt werden (Abschluss fehlgeschlagen) – siehe Ausgabe oben" >&2
                exit 1
            fi
            echo "WARNING: pdh-ha-Update fehlgeschlagen – siehe Ausgabe oben" >&2
        fi
    fi
    if install -m 0755 "$repo_dir/bin/update-agent" "$agent_bin"; then
        systemd-run --quiet --unit=pdh-update-agent-restart --on-active=5s systemctl restart pdh-update-agent
    else
        echo "WARNING: $agent_bin konnte nicht ersetzt werden; der Update-Agent bleibt auf der alten Version." >&2
        echo "         Auf dem Server 'sudo ./install-update-agent.sh' im Repo-Verzeichnis ausfuehren (aktualisiert die systemd-Unit inkl. ReadWritePaths)." >&2
    fi
    # code-server (/code/): nur wenn eingerichtet (scripts/install-code-server.sh); Fehler bricht das Update nicht ab
    if command -v docker >/dev/null 2>&1 && docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx pdh-codeserver; then
        cs_port="$(sed -nE 's#^PDH_CODESERVER_INTERNAL_URL=http://127\.0\.0\.1:([0-9]+).*#\1#p' /etc/pdh/pdh.env 2>/dev/null | tail -n 1)"
        PDH_CODESERVER_PORT="${cs_port:-8443}" bash "$repo_dir/scripts/install-code-server.sh" >/dev/null \
            || echo "code-server konnte nicht neu gebaut werden (/code/) - Update trotzdem fertig." >&2
    fi
fi

echo "Updated to $target."