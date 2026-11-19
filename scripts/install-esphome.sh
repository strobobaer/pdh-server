#!/usr/bin/env bash
# ESPPDH: Python-Umgebung mit ESPHome einrichten oder auf die festgelegte Version bringen.
#
#   sudo bash scripts/install-esphome.sh
#
# Aufrufer: der Update-Agent (Server-Einstellungen → ESPPDH → „Umgebung einrichten“) und
# install_update.sh bei jedem Update, wenn die Umgebung schon eingerichtet ist.
# Mehrfach ausführbar. Die Version in scripts/esphome.version ist die Mindestversion
# (Grundumgebung); eine neuere Version holt der Update-Agent selbst (siehe PDH_ESPHOME_VERSION)
# und ein PDH-Update stuft sie nicht herab. Am Ende gibt das Skript „Version: …“ von ESPHome aus.
#
#   PDH_ESPHOME_DIR      Python-Umgebung (Standard /opt/pdh-esphome)
#   PDH_ESPHOME_VERSION  Kandidat einrichten: genau diese Version (JJJJ.M.P) unter
#                        /opt/pdh-esphome-versions/<Version>, daneben die Grundumgebung
#                        unverändert; PDH_ESPHOME_DIR wird dann ignoriert
#   PDH_ESPHOME_WORKDIR  Arbeitsordner der Bauläufe (Standard /var/lib/pdh-esphome)
#
# Das Skript läuft als root. Es löscht nur eine Python-Umgebung (Ordner mit pyvenv.cfg) oder
# einen leeren Ordner und übernimmt keinen fremden Arbeitsordner. Die Prüffunktionen lassen
# sich einzeln aufrufen (source, Tests in cmd/update-agent/esppdh_test.go).

# Mindestversion von Python für die ESPHome-Version (PyPI „requires_python“) – beim Anheben
# von esphome.version abgleichen.
py_min="3.12"

# Ordner der zusätzlichen Umgebungen (neuere ESPHome-Versionen nebeneinander, vom Update-Agenten
# umgeschaltet) und der Platz, den eine Umgebung samt pip-Zwischenspeicher mindestens braucht.
esphome_versions_dir="/opt/pdh-esphome-versions"
esphome_min_free_mb=3072

fail() { echo "$*" >&2; exit 1; }

# esphome_path_ok NAME PFAD: absolut, mindestens zwei Ordnerebenen, ohne „.“ und „..“, ohne
# doppelten oder abschließenden Schrägstrich.
esphome_path_ok() {
    local name="$1" p="$2"
    case "$p" in
        */./* | */. | */../* | */.. | *//* | ?*/)
            echo "$name: '$p' darf weder „.“ noch „..“ noch doppelte oder abschließende „/“ enthalten." >&2
            return 1
            ;;
    esac
    if [[ ! "$p" =~ ^/[^/]+/[^/]+ ]]; then
        echo "$name: '$p' muss ein absoluter Pfad mit mindestens zwei Ordnerebenen sein." >&2
        return 1
    fi
}

# esphome_version_ok VERSION: nur eine stabile Version JJJJ.M.P (Ziffern und Punkte) – diese
# Zeichenfolge wird zum Ordnernamen, darum nichts anderes zulassen.
esphome_version_ok() {
    # Der Glob zuerst: Er sieht die ganze Zeichenfolge, auch mit Zeilenumbruch (Regex-Anker nicht).
    if [[ "$1" == *[!0-9.]* || ! "$1" =~ ^[0-9]{4}\.[0-9]{1,2}\.[0-9]{1,3}$ ]]; then
        echo "Ungültige ESPHome-Version: '$1' (erwartet JJJJ.M.P, z. B. 2026.11.1)." >&2
        return 1
    fi
}

# esphome_venv_dir: Zielordner der Python-Umgebung. Mit PDH_ESPHOME_VERSION immer
# <Versionsordner>/<Version> (ein mitgegebenes PDH_ESPHOME_DIR wird ignoriert), sonst
# PDH_ESPHOME_DIR bzw. die Grundumgebung.
esphome_venv_dir() {
    if [[ -n "${PDH_ESPHOME_VERSION:-}" ]]; then
        echo "$esphome_versions_dir/$PDH_ESPHOME_VERSION"
    else
        echo "${PDH_ESPHOME_DIR:-/opt/pdh-esphome}"
    fi
}

# esphome_remove_venv ORDNER: entfernt nur eine Python-Umgebung (pyvenv.cfg) oder einen leeren
# Ordner – nie einen anderen Ordner, eine Datei oder einen Verweis.
esphome_remove_venv() {
    local d="$1"
    if [[ -L "$d" || ! -d "$d" ]] || { [[ ! -f "$d/pyvenv.cfg" ]] && [[ -n "$(ls -A "$d" 2>/dev/null)" ]]; }; then
        echo "$d ist keine Python-Umgebung (pyvenv.cfg fehlt) und wird nicht gelöscht. Bitte den Ordner prüfen oder PDH_ESPHOME_DIR auf einen eigenen, neuen Ordner setzen." >&2
        return 1
    fi
    rm -rf -- "$d"
}

# esphome_workdir_ok ORDNER BENUTZER: neu, schon dem Dienstbenutzer gehörend oder ein root
# gehörender Ordner namens pdh-esphome – sonst nicht anfassen (install -d setzt Eigentümer und Rechte).
esphome_workdir_ok() {
    local d="$1" user="$2" owner
    [[ -e "$d" || -L "$d" ]] || return 0
    if [[ -L "$d" || ! -d "$d" ]]; then
        echo "Arbeitsordner $d ist kein Ordner (Datei oder Verweis) und wird nicht übernommen. Bitte PDH_ESPHOME_WORKDIR auf einen eigenen Ordner setzen." >&2
        return 1
    fi
    owner="$(stat -c %U "$d")"
    if [[ "$owner" == "$user" || ( "$owner" == root && "$(basename "$d")" == pdh-esphome ) ]]; then
        return 0
    fi
    echo "Arbeitsordner $d gehört $owner und wird nicht übernommen. Bitte PDH_ESPHOME_WORKDIR auf einen eigenen Ordner setzen." >&2
    return 1
}

# py_ok PYTHON: mindestens py_min?
py_ok() { "$1" -c "import sys; sys.exit(0 if sys.version_info[:2] >= (${py_min/./, }) else 1)" >/dev/null 2>&1; }

# apt außerhalb von Agent-cgroup und -Sandbox (schreibt nach /usr), wie in install_update.sh.
esphome_apt() {
    if command -v systemd-run >/dev/null 2>&1; then
        systemd-run --wait --pipe --collect --quiet --unit="pdh-esphome-apt-$(date +%s)" \
            env DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=120 "$@"
    else
        DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=120 "$@"
    fi
}

main() {
    set -Eeuo pipefail
    local script_dir venv work version candidate svc_user svc_group current py free created=0
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    work="${PDH_ESPHOME_WORKDIR:-/var/lib/pdh-esphome}"
    candidate="${PDH_ESPHOME_VERSION:-}"

    # Pfade zuerst prüfen – vor allem, was etwas löscht oder Rechte setzt.
    if [[ -n "$candidate" ]]; then
        esphome_version_ok "$candidate" || exit 1
        version="$candidate"
    fi
    venv="$(esphome_venv_dir)"
    esphome_path_ok PDH_ESPHOME_DIR "$venv" || exit 1
    esphome_path_ok PDH_ESPHOME_WORKDIR "$work" || exit 1
    (( EUID == 0 )) || fail "Bitte als root ausführen: sudo bash $0"
    if [[ -z "$candidate" ]]; then
        version="$(tr -d '[:space:]' < "$script_dir/esphome.version")"
        [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "Ungültige ESPHome-Version in esphome.version: '$version'"
    fi

    # Nur eine Einrichtung zur Zeit (Knopf und Update gleichzeitig).
    exec 9>/run/pdh-esphome-install.lock
    flock -w 900 9 || fail "Eine andere Einrichtung von ESPHome läuft noch – bitte später erneut versuchen."

    # Dienstbenutzer des PDH (Eigentümer des Arbeitsordners).
    svc_user="$(systemctl show -p User --value pdh 2>/dev/null || true)"
    [[ -n "$svc_user" ]] || svc_user=pdh
    id "$svc_user" >/dev/null 2>&1 || fail "Dienstbenutzer '$svc_user' des PDH nicht gefunden."
    svc_group="$(id -gn "$svc_user")"
    esphome_workdir_ok "$work" "$svc_user" || exit 1

    if ! command -v python3 >/dev/null 2>&1; then
        echo "python3 fehlt – wird mit apt nachgeholt."
        esphome_apt update || echo "WARNING: apt update ist fehlgeschlagen - die Installation wird trotzdem versucht" >&2
        esphome_apt install -y python3 python3-venv \
            || fail "python3 konnte nicht installiert werden. Von Hand: sudo apt-get install -y python3 python3-venv"
    fi
    # Vor dem Anlegen prüfen: Mit zu altem Python bleibt keine halbe Umgebung zurück.
    py_ok python3 || fail "ESPHome $version braucht Python $py_min oder neuer. Vorhanden: $(python3 --version 2>&1). Bitte das Betriebssystem aktualisieren (z. B. Ubuntu 24.04)."

    # Kandidat: genug Platz unter /opt (Umgebung samt pip-Zwischenspeicher), Elternordner anlegen.
    if [[ -n "$candidate" ]]; then
        free="$(df -Pm /opt 2>/dev/null | awk 'NR==2 {print $4}')"
        if [[ "$free" =~ ^[0-9]+$ ]] && (( free < esphome_min_free_mb )); then
            fail "Zu wenig Platz unter /opt: ${free} MB frei, für ESPHome $version werden mindestens ${esphome_min_free_mb} MB gebraucht."
        fi
        install -d -m 0755 -o root -g root "$esphome_versions_dir"
    fi

    # Umgebung mit zu altem oder fehlendem Python (Abbruch, Wechsel der Python-Version): neu anlegen.
    if [[ -e "$venv" || -L "$venv" ]] && ! py_ok "$venv/bin/python3"; then
        esphome_remove_venv "$venv" || exit 1
        echo "Unbrauchbare Python-Umgebung $venv entfernt – sie wird neu angelegt."
    fi
    if [[ ! -e "$venv" ]]; then
        # Debian/Ubuntu: venv steckt im eigenen Paket python3-venv
        if ! python3 -m venv "$venv" >/dev/null 2>&1; then
            if [[ -e "$venv" ]]; then esphome_remove_venv "$venv" || exit 1; fi
            echo "python3-venv fehlt – wird mit apt nachgeholt."
            esphome_apt update || echo "WARNING: apt update ist fehlgeschlagen - die Installation wird trotzdem versucht" >&2
            esphome_apt install -y python3-venv \
                || fail "python3-venv konnte nicht installiert werden. Von Hand: sudo apt-get install -y python3-venv"
            if ! python3 -m venv "$venv"; then
                if [[ -e "$venv" ]]; then esphome_remove_venv "$venv" || true; fi
                fail "Python-Umgebung $venv konnte nicht angelegt werden."
            fi
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
            if (( created )); then esphome_remove_venv "$venv" || true; fi
            fail "pip konnte ESPHome $version nicht installieren ($py). Ursache siehe Ausgabe oben."
        fi
    fi

    # Arbeitsordner gehört dem Dienstbenutzer (install -d setzt Rechte auch bei einem vorhandenen Ordner).
    install -d -m 0750 -o "$svc_user" -g "$svc_group" "$work"

    "$venv/bin/esphome" version
}

# Nur beim Aufruf ausführen, nicht beim Einlesen mit source (Tests rufen die Prüffunktionen einzeln).
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
