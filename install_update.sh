#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mode="${PDH_UPDATE_MODE:-}"
project_name="${PDH_COMPOSE_PROJECT_NAME:-$(basename "$repo_dir")}"

if [[ "$mode" != "docker" && "$mode" != "systemd" ]]; then
    echo "PDH_UPDATE_MODE must be docker or systemd." >&2
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

git_as_checkout_owner fetch --prune origin main
current="$(git_as_checkout_owner rev-parse HEAD)"
target="$(git_as_checkout_owner rev-parse "FETCH_HEAD^{commit}")"
if [[ "$current" == "$target" ]]; then
    echo "Source is already at $target; verifying/rebuilding the runtime."
else
    if ! git_as_checkout_owner merge --ff-only "$target"; then
        echo "The local branch cannot be fast-forwarded; refusing to overwrite it." >&2
        exit 1
    fi
fi

if [[ "$mode" == "docker" ]]; then
    compose=(docker compose --project-name "$project_name" --env-file "$repo_dir/.env.docker" --project-directory "$repo_dir" -f "$repo_dir/compose.yaml")
    "${compose[@]}" build --build-arg "PDH_BUILD_COMMIT=$target" app updater
    "${compose[@]}" up -d --no-deps app updater
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
    mkdir -p "$repo_dir/bin"
    (cd "$repo_dir" && go build -buildvcs=false -trimpath -ldflags="-s -w -X main.buildCommit=$target" -o bin/pdh ./cmd/server)
    (cd "$repo_dir" && go build -buildvcs=false -trimpath -ldflags="-s -w" -o bin/update-agent ./cmd/update-agent)
    # Zuerst die Anwendung neu starten: ein nicht ersetzbares Agent-Binary
    # (z.B. aeltere systemd-Unit ohne /usr/local/bin in ReadWritePaths oder
    # schreibgeschuetztes Dateisystem) darf das Update nicht halb fertig
    # abbrechen.
    systemctl restart pdh
    if install -m 0755 "$repo_dir/bin/update-agent" /usr/local/bin/pdh-update-agent; then
        systemd-run --quiet --unit=pdh-update-agent-restart --on-active=5s systemctl restart pdh-update-agent
    else
        echo "WARNING: /usr/local/bin/pdh-update-agent konnte nicht ersetzt werden; der Update-Agent bleibt auf der alten Version." >&2
        echo "         Auf dem Server 'sudo ./install-update-agent.sh' im Repo-Verzeichnis ausfuehren (aktualisiert die systemd-Unit inkl. ReadWritePaths)." >&2
    fi
fi

echo "Updated to $target."