#!/usr/bin/env bash
# code-server (VS Code im Browser) für eine systemd-Installation des PDH einrichten.
#
#   sudo bash scripts/install-code-server.sh             freien Port suchen, neu einrichten
#   sudo bash scripts/install-code-server.sh --existing 8443   vorhandenen code-server übernehmen
#   sudo bash scripts/install-code-server.sh --new       trotz vorhandenem code-server einen eigenen starten
#   sudo PDH_CODESERVER_PORT=9443 bash scripts/install-code-server.sh   Port vorgeben
#   sudo bash scripts/install-code-server.sh --remove    abschalten
#
# Was passiert:
#   * Baut das Image pdh-codeserver:local (Dockerfile, Stufe „codeserver“, mit Go)
#     und startet es als Container „pdh-codeserver“ – nur lokal (127.0.0.1),
#     ohne eigenes Passwort. Das PDH reicht ihn unter https://<pdh>/code/ durch
#     und lässt nur Administratoren hinein.
#   * Arbeitsordner ist dieses Repository (/workspace/pdh im Container).
#   * Trägt PDH_CODESERVER_INTERNAL_URL in /etc/pdh/pdh.env ein und startet
#     den Dienst pdh neu.
# Erneut ausführen ist gefahrlos (Einstellungen und Erweiterungen bleiben im Volume pdh_codeserver).
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="pdh-codeserver:local"
name="pdh-codeserver"
pdh_env="/etc/pdh/pdh.env"
port="${PDH_CODESERVER_PORT:-}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWarnung:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mFehler:\033[0m %s\n' "$*" >&2; exit 1; }

port_busy() { ss -ltnH "( sport = :$1 )" 2>/dev/null | grep -q .; }
# Programm, das auf dem Port lauscht (braucht root)
port_owner() { ss -ltnpH "( sport = :$1 )" 2>/dev/null | sed -nE 's/.*users:\(\("([^"]+)",pid=([0-9]+).*/\1 (PID \2)/p' | head -n 1; }
# antwortet dort ein code-server?
is_codeserver() { curl -fsS --max-time 3 "http://127.0.0.1:$1/healthz" 2>/dev/null | grep -q '"status"'; }
own_port() { sed -nE 's#^PDH_CODESERVER_INTERNAL_URL=http://127\.0\.0\.1:([0-9]+).*#\1#p' "$pdh_env" 2>/dev/null | tail -n 1; }

register() { # PDH auf die Adresse zeigen lassen und neu starten
    mkdir -p "$(dirname "$pdh_env")"
    touch "$pdh_env"
    sed -i '/^PDH_CODESERVER_INTERNAL_URL=/d' "$pdh_env"
    printf 'PDH_CODESERVER_INTERNAL_URL=%s\n' "$1" >> "$pdh_env"
    chmod 600 "$pdh_env"
    systemctl restart pdh
}

# ── vorhandenen code-server übernehmen statt einen neuen zu starten ──
if [[ "${1:-}" == "--existing" ]]; then
    [[ $EUID -eq 0 ]] || die "Bitte mit sudo ausführen."
    target="${2:-}"
    [[ "$target" =~ ^[0-9]+$ ]] && target="http://127.0.0.1:${target}"
    [[ "$target" =~ ^http://[A-Za-z0-9.:-]+(:[0-9]+)?/?$ ]] || die "Aufruf: sudo bash scripts/install-code-server.sh --existing 8443   (Port oder http://adresse:port)"
    target="${target%/}"
    curl -fsS --max-time 5 "${target}/healthz" 2>/dev/null | grep -q '"status"' || die "Unter ${target} antwortet kein code-server (${target}/healthz)."
    log "Übernehme vorhandenen code-server ${target}"
    register "$target"
    cat <<EOF

Fertig. Der vorhandene code-server ist jetzt unter https://<eure-pdh-adresse>/code/ erreichbar (nur PDH-Administratoren).
Hinweise:
  * Hat er ein eigenes Passwort (--auth password), fragt er danach zusätzlich zur PDH-Anmeldung.
  * Lauscht er auf 0.0.0.0, ist er weiterhin auch direkt über den Port erreichbar – für nur /code/
    in seiner config.yaml „bind-addr: 127.0.0.1:PORT“ setzen.
EOF
    exit 0
fi

if [[ "${1:-}" == "--remove" ]]; then
    [[ $EUID -eq 0 ]] || die "Bitte mit sudo ausführen."
    log "Entferne code-server"
    docker rm -f "$name" >/dev/null 2>&1 || true
    if [[ -f "$pdh_env" ]]; then sed -i '/^PDH_CODESERVER_INTERNAL_URL=/d' "$pdh_env"; fi
    systemctl restart pdh
    log "Fertig – /code/ ist abgeschaltet (Volume pdh_codeserver bleibt; löschen: docker volume rm pdh_codeserver)."
    exit 0
fi

[[ $EUID -eq 0 ]] || die "Bitte mit sudo ausführen: sudo bash scripts/install-code-server.sh"
command -v docker >/dev/null || die "Docker fehlt (sudo apt install docker.io)."
[[ -f "$repo_dir/Dockerfile" ]] && grep -q "AS codeserver" "$repo_dir/Dockerfile" || die "Dockerfile ohne Stufe „codeserver“ – zuerst das PDH aktualisieren."
new_ok=0
[[ "${1:-}" == "--new" ]] && new_ok=1

ours=0
docker ps -a --format '{{.Names}}' | grep -qx "$name" && ours=1

if [[ -z "$port" && $ours -eq 1 ]]; then
    port="$(own_port)" # bei erneutem Lauf / Update den bisherigen Port behalten
fi

if [[ -n "$port" ]]; then
    [[ "$port" =~ ^[0-9]+$ ]] || die "PDH_CODESERVER_PORT muss eine Zahl sein."
    if port_busy "$port" && ! { [[ $ours -eq 1 ]] && [[ "$port" == "$(own_port)" ]]; }; then
        owner="$(port_owner "$port")"
        die "Port ${port} ist schon belegt${owner:+ von ${owner}}. Ohne PDH_CODESERVER_PORT sucht das Skript selbst einen freien Port."
    fi
else
    # schon ein code-server auf dem Server? (typisch 8443 bzw. 8080) – dann nachfragen statt doppelt starten
    if [[ $new_ok -eq 0 && $ours -eq 0 ]]; then
        for p in 8443 8444 8080 8081; do
            if port_busy "$p" && is_codeserver "$p"; then
                owner="$(port_owner "$p")"
                cat >&2 <<EOF
Auf Port ${p} läuft schon ein code-server${owner:+ (${owner})}.

  Diesen übernehmen (empfohlen, wenn er schon passt):
      sudo bash scripts/install-code-server.sh --existing ${p}

  Oder trotzdem einen eigenen PDH-code-server starten (freier Port wird gesucht):
      sudo bash scripts/install-code-server.sh --new
EOF
                exit 2
            fi
        done
    fi
    for p in $(seq 8443 8499); do
        if ! port_busy "$p"; then port="$p"; break; fi
    done
    [[ -n "$port" ]] || die "Kein freier Port zwischen 8443 und 8499 – Port angeben: sudo PDH_CODESERVER_PORT=9443 bash scripts/install-code-server.sh"
    log "Freier Port: ${port}"
fi

# belegte Ports zur Information
for p in 8443 8444; do
    if [[ "$p" != "$port" ]] && port_busy "$p"; then
        owner="$(port_owner "$p")"
        log "Port ${p} ist belegt${owner:+ von ${owner}}"
    fi
done

log "Baue ${image} (dauert beim ersten Mal einige Minuten)"
docker build -q --target codeserver -t "$image" "$repo_dir" >/dev/null

log "Starte ${name} auf 127.0.0.1:${port}"
docker rm -f "$name" >/dev/null 2>&1 || true
docker volume create pdh_codeserver >/dev/null
docker run -d --name "$name" --restart unless-stopped --network host \
    --user 0:0 \
    -e HOME=/data -e XDG_CONFIG_HOME=/data/config \
    -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0='*' \
    -v "$repo_dir:/workspace/pdh" \
    -v pdh_codeserver:/data \
    --entrypoint /usr/bin/entrypoint.sh \
    "$image" \
    --auth none --bind-addr "127.0.0.1:${port}" --disable-telemetry --disable-update-check \
    --user-data-dir /data/user --extensions-dir /data/extensions --app-name "PDH Code" /workspace/pdh >/dev/null

printf 'Warte auf code-server '
for _ in $(seq 1 45); do
    if curl -fs "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then ok=1; break; fi
    printf '.'; sleep 2
done
echo
[[ "${ok:-0}" -eq 1 ]] || { docker logs --tail 40 "$name" >&2 || true; die "code-server antwortet nicht – siehe Protokoll oben (docker logs ${name})."; }

log "Trage code-server in ${pdh_env} ein und starte das PDH neu"
register "http://127.0.0.1:${port}"

cat <<EOF

Fertig.
  Code-Editor:     https://<eure-pdh-adresse>/code/   (als Administrator im PDH angemeldet)
                   bzw. Core-Einstellungen → Code-Editor (code-server)
  Arbeitsordner:   ${repo_dir}
  Abschalten:      sudo bash scripts/install-code-server.sh --remove
Bei PDH-Updates wird der Container automatisch neu gebaut.
EOF
