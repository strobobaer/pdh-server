#!/usr/bin/env bash
# ESPPDH: Python-Umgebung mit ESPHome einrichten oder auf die festgelegte Version bringen.
#
#   sudo bash scripts/install-esphome.sh
#
# Aufrufer: der Update-Agent (Server-Einstellungen → ESPPDH → „Umgebung einrichten“) und
# install_update.sh bei jedem Update, wenn die Umgebung schon eingerichtet ist.
# Mehrfach ausführbar. Die Version steht in scripts/esphome.version und kommt mit
# PDH-Updates. Am Ende gibt das Skript „Version: …“ von ESPHome aus.
#
#   PDH_ESPHOME_DIR      Python-Umgebung (Standard /opt/pdh-esphome)
#   PDH_ESPHOME_WORKDIR  Arbeitsordner der Bauläufe (Standard /var/lib/pdh-esphome)
set -Eeuo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
venv="${PDH_ESPHOME_DIR:-/opt/pdh-esphome}"
work="${PDH_ESPHOME_WORKDIR:-/var/lib/pdh-esphome}"

fail() { echo "$*" >&2; exit 1; }

(( EUID == 0 )) || fail "Bitte als root ausführen: sudo bash $0"
# Schutz vor „rm -rf“ an falscher Stelle: mindestens zwei Ordnerebenen, absolut
[[ "$venv" =~ ^/[^/]+/[^/]+ ]] || fail "Ungültiger Ordner für die Python-Umgebung: '$venv'"
version="$(tr -d '[:space:]' < "$script_dir/esphome.version")"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "Ungültige ESPHome-Version in esphome.version: '$version'"

# Nur eine Einrichtung zur Zeit (Knopf und Update gleichzeitig).
exec 9>/run/pdh-esphome-install.lock
flock -w 900 9 || fail "Eine andere Einrichtung von ESPHome läuft noch – bitte später erneut versuchen."

# apt außerhalb von Agent-cgroup und -Sandbox (schreibt nach /usr), wie in install_update.sh.
esphome_apt() {
    if command -v systemd-run >/dev/null 2>&1; then
        systemd-run --wait --pipe --collect --quiet --unit="pdh-esphome-apt-$(date +%s)" \
            env DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=120 "$@"
    else
        DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=120 "$@"
    fi
}

# Dienstbenutzer des PDH (Eigentümer des Arbeitsordners).
svc_user="$(systemctl show -p User --value pdh 2>/dev/null || true)"
[[ -n "$svc_user" ]] || svc_user=pdh
id "$svc_user" >/dev/null 2>&1 || fail "Dienstbenutzer '$svc_user' des PDH nicht gefunden."

# Mindestversion von Python für ESPHome $version (PyPI „requires_python“) – beim Anheben
# von esphome.version abgleichen.
py_min="3.12"
py_ok() { "$1" -c "import sys; sys.exit(0 if sys.version_info[:2] >= (${py_min/./, }) else 1)" >/dev/null 2>&1; }

if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 fehlt – wird mit apt nachgeholt."
    esphome_apt update || echo "WARNING: apt update ist fehlgeschlagen - die Installation wird trotzdem versucht" >&2
    esphome_apt install -y python3 python3-venv \
        || fail "python3 konnte nicht installiert werden. Von Hand: sudo apt-get install -y python3 python3-venv"
fi
# Vor dem Anlegen prüfen: Mit zu altem Python bleibt keine halbe Umgebung zurück.
py_ok python3 || fail "ESPHome $version braucht Python $py_min oder neuer. Vorhanden: $(python3 --version 2>&1). Bitte das Betriebssystem aktualisieren (z. B. Ubuntu 24.04)."

# Umgebung mit zu altem oder fehlendem Python (Abbruch, Wechsel der Python-Version): neu anlegen.
if [[ -e "$venv" ]] && ! py_ok "$venv/bin/python3"; then
    echo "Python-Umgebung $venv ist unbrauchbar – wird neu angelegt."
    rm -rf "$venv"
fi
created=0
if [[ ! -e "$venv" ]]; then
    # Debian/Ubuntu: venv steckt im eigenen Paket python3-venv
    if ! python3 -m venv "$venv" >/dev/null 2>&1; then
        rm -rf "$venv"
        echo "python3-venv fehlt – wird mit apt nachgeholt."
        esphome_apt update || echo "WARNING: apt update ist fehlgeschlagen - die Installation wird trotzdem versucht" >&2
        esphome_apt install -y python3-venv \
            || fail "python3-venv konnte nicht installiert werden. Von Hand: sudo apt-get install -y python3-venv"
        python3 -m venv "$venv" || { rm -rf "$venv"; fail "Python-Umgebung $venv konnte nicht angelegt werden."; }
    fi
    created=1
    echo "Python-Umgebung $venv angelegt."
fi

# Schon auf der Version und startfähig: kein pip (Update bleibt schnell und geht ohne Internet).
current="$("$venv/bin/esphome" version 2>/dev/null | sed -n 's/^Version: *//p' | tail -n 1 || true)"
if [[ "$current" == "$version" ]]; then
    echo "ESPHome $version ist bereits installiert."
else
    echo "ESPHome $version wird installiert${current:+ (bisher $current)} …"
    if ! "$venv/bin/python3" -m pip install --disable-pip-version-check --no-input --no-cache-dir --progress-bar off \
        --upgrade "esphome==$version"; then
        py="$("$venv/bin/python3" --version 2>&1)"
        # eben angelegt: keine halbe Umgebung zurücklassen; eine vorhandene läuft mit der alten Version weiter
        if (( created )); then rm -rf "$venv"; fi
        fail "pip konnte ESPHome $version nicht installieren ($py). Ursache siehe Ausgabe oben."
    fi
fi

# Arbeitsordner gehört dem Dienstbenutzer (install -d setzt Rechte auch bei einem vorhandenen Ordner).
install -d -m 0750 -o "$svc_user" -g "$(id -gn "$svc_user")" "$work"

"$venv/bin/esphome" version
