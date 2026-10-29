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
# Agent und install_update.sh starten es per systemd-run außerhalb der
# Agent-Sandbox. Läuft es doch mit schreibgeschütztem /etc und muss dort etwas
# geändert werden, bricht es ab, bevor das Binary getauscht wird.

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
unit_changed=0
[[ -f "$unit_file" && "$(cat "$unit_file")" == "$unit" ]] || unit_changed=1
etc_changes=$unit_changed
[[ -d /etc/pdh/ha ]] || etc_changes=1
getent group pdh >/dev/null || etc_changes=1
# Benutzer des PDH-Dienstes braucht die Gruppe pdh für den Socket
pdh_user="$(systemctl show -p User --value pdh 2>/dev/null || true)"
add_user=0
if [[ -n "$pdh_user" && "$pdh_user" != root ]] && id "$pdh_user" >/dev/null 2>&1 && [[ " $(id -nG "$pdh_user" 2>/dev/null) " != *" pdh "* ]]; then
    add_user=1 etc_changes=1
fi
first_install=0
[[ -x /usr/local/bin/pdh-ha ]] || first_install=1
if (( etc_changes )) && [[ ! -w /etc/systemd/system ]]; then
    echo "/etc ist nicht beschreibbar (Update-Agent-Sandbox?) – pdh-ha bleibt unverändert." >&2
    echo "Bitte auf dem Server ausführen: sudo bash $repo_dir/scripts/install-pdh-ha.sh $repo_dir" >&2
    exit 1
fi

missing_packages=()
command -v git >/dev/null 2>&1 || missing_packages+=(git)
command -v go >/dev/null 2>&1 || missing_packages+=(golang-go)
command -v ping >/dev/null 2>&1 || missing_packages+=(iputils-ping)
if (( ${#missing_packages[@]} > 0 )); then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y "${missing_packages[@]}"
fi

# Mitwandernde Adresse: ip und arping (auch am ersten Server, damit
# „pdh-ha vip test“ nach einem Update ohne Handarbeit geht). Nach dem Paket
# fragen, nicht nach dem Programm – das Ubuntu-Paket „arping“ (Habets) hat
# andere Schalter. Ein Fehler hier bricht das Update von pdh-ha nicht ab:
# ohne die Pakete fehlt nur die mitwandernde Adresse.
vip_packages=()
command -v ip >/dev/null 2>&1 || vip_packages+=(iproute2)
dpkg -s iputils-arping >/dev/null 2>&1 || vip_packages+=(iputils-arping)
if (( ${#vip_packages[@]} > 0 )); then
    export DEBIAN_FRONTEND=noninteractive
    { apt-get update && apt-get install -y "${vip_packages[@]}"; } \
        || echo "WARNUNG: ${vip_packages[*]} nicht installiert – für die mitwandernde Adresse: sudo apt-get install iputils-arping iproute2" >&2
fi

# systemd-run setzt weder HOME noch Go-Caches
export HOME="${HOME:-/root}"
export GOCACHE="${GOCACHE:-/var/cache/pdh-ha/go-build}"
export GOMODCACHE="${GOMODCACHE:-/var/cache/pdh-ha/go-mod}"

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
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
ldflags="-s -w -X main.version=$build_version -X main.commit=$build_commit"
(cd "$repo_dir" && GOTOOLCHAIN=auto go build -buildvcs=false -trimpath -ldflags "$ldflags" -o "$build_dir/pdh-ha" ./cmd/pdh-ha)

# zuerst Gruppe, Ablage und Unit, dann das Binary atomar tauschen.
# Gruppe pdh: Zugriff des PDH auf den Socket /run/pdh-ha/api.sock
getent group pdh >/dev/null || groupadd --system pdh
[[ -d /etc/pdh/ha ]] || install -d -m 0700 /etc/pdh/ha
if (( add_user )); then
    usermod -aG pdh "$pdh_user"
    echo "Benutzer $pdh_user zur Gruppe pdh hinzugefügt – PDH muss neu gestartet werden, damit es pdh-ha erreicht."
fi
if (( unit_changed )); then
    printf '%s\n' "$unit" > "$unit_file"
    systemctl daemon-reload
fi
install -m 0755 "$build_dir/pdh-ha" /usr/local/bin/pdh-ha.new
mv -f /usr/local/bin/pdh-ha.new /usr/local/bin/pdh-ha

if systemctl is-active --quiet pdh-ha; then
    systemctl restart pdh-ha
    echo "pdh-ha $build_version ($build_commit) installiert, Dienst neu gestartet."
else
    systemctl enable --now pdh-ha
    echo "pdh-ha $build_version ($build_commit) installiert und gestartet."
fi
# Neue Gruppe wirkt erst nach Neustart – nur beim Erst-Install, Updates lassen PDH laufen
if (( first_install )) && (( add_user )); then
    systemctl try-restart pdh
    echo "PDH neu gestartet."
elif (( add_user )); then
    echo "Bitte PDH neu starten: sudo systemctl restart pdh"
fi
