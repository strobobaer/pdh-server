#!/usr/bin/env bash
# PDH – Ausfallschutz-Dienst pdh-ha bauen und als systemd-Dienst einrichten.
#
#   sudo bash scripts/install-pdh-ha.sh <repo_dir>
#
# Aufgerufen vom Update-Agenten („Ausfallschutz einrichten“), von
# install_update.sh (bei jedem PDH-Update, wenn pdh-ha installiert ist) und von
# pdh-ha-join.sh. Mehrfach ausführbar: läuft der Dienst schon, wird er neu
# gestartet. Die ufw-Regeln für Port 7480 setzt pdh-ha selbst.
#
# Beim PDH-Update läuft das Skript im Update-Agenten (ProtectSystem=full,
# /etc schreibgeschützt) – daher /etc nur anfassen, wenn sich etwas ändert.

set -euo pipefail

if (( EUID != 0 )); then
    echo "Bitte als root ausführen: sudo bash scripts/install-pdh-ha.sh <repo_dir>" >&2
    exit 1
fi
repo_dir="${1:-}"
if [[ -z "$repo_dir" || ! -f "$repo_dir/go.mod" || ! -d "$repo_dir/cmd/pdh-ha" ]]; then
    echo "Aufruf: install-pdh-ha.sh <repo_dir> (PDH-Repo mit cmd/pdh-ha)" >&2
    exit 1
fi
repo_dir="$(cd "$repo_dir" && pwd)"

missing_packages=()
command -v git >/dev/null 2>&1 || missing_packages+=(git)
command -v go >/dev/null 2>&1 || missing_packages+=(golang-go)
command -v ping >/dev/null 2>&1 || missing_packages+=(iputils-ping)
if (( ${#missing_packages[@]} > 0 )); then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y "${missing_packages[@]}"
fi

# Gruppe pdh: Zugriff des PDH auf den Socket /run/pdh-ha/api.sock
getent group pdh >/dev/null || groupadd --system pdh

# Go: Das Projekt braucht die Go-Version aus go.mod (derzeit 1.24). Ab Go 1.21
# lädt go die passende Version beim Bauen selbst nach (GOTOOLCHAIN=auto).
export GOTOOLCHAIN=auto
go_minor="$(go env GOVERSION 2>/dev/null | sed -nE 's/^go1\.([0-9]+).*/\1/p')"
if [[ -z "$go_minor" || "$go_minor" -lt 21 ]]; then
    echo "Go $(go env GOVERSION 2>/dev/null || echo '?') ist zu alt: nötig ist mindestens Go 1.21 (lädt dann Go 1.24 selbst nach). Bitte Go aktualisieren." >&2
    exit 1
fi

# root liest ggf. das Repo des PDH-Benutzers – git verlangt dafür safe.directory
build_commit="$(git -c safe.directory="$repo_dir" -C "$repo_dir" rev-parse HEAD)"
build_version="$(tr -d '[:space:]' < "$repo_dir/VERSION")"
mkdir -p "$repo_dir/bin"
(cd "$repo_dir" && GOTOOLCHAIN=auto go build -buildvcs=false -trimpath \
    -ldflags "-s -w -X main.version=$build_version -X main.commit=$build_commit" \
    -o bin/pdh-ha ./cmd/pdh-ha)
install -m 0755 "$repo_dir/bin/pdh-ha" /usr/local/bin/pdh-ha

if [[ ! -d /etc/pdh/ha ]]; then
    install -d -m 0700 /etc/pdh/ha
fi

unit_file=/etc/systemd/system/pdh-ha.service
unit="$(cat <<'EOF'
[Unit]
Description=PDH Ausfallschutz (pdh-ha)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/pdh-ha run
Restart=always
RestartSec=2
RuntimeDirectory=pdh-ha
RuntimeDirectoryMode=0750
RuntimeDirectoryPreserve=yes
StateDirectory=pdh-ha

[Install]
WantedBy=multi-user.target
EOF
)"
if [[ ! -f "$unit_file" || "$(cat "$unit_file")" != "$unit" ]]; then
    printf '%s\n' "$unit" > "$unit_file"
    systemctl daemon-reload
fi

if systemctl is-active --quiet pdh-ha; then
    systemctl restart pdh-ha
    echo "pdh-ha $build_version ($build_commit) installiert, Dienst neu gestartet."
else
    systemctl enable --now pdh-ha
    echo "pdh-ha $build_version ($build_commit) installiert und gestartet."
fi
