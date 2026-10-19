#!/usr/bin/env bash
# code-server (VS Code im Browser) für eine systemd-Installation des PDH einrichten.
#
#   sudo bash scripts/install-code-server.sh
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
port="${PDH_CODESERVER_PORT:-8443}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWarnung:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mFehler:\033[0m %s\n' "$*" >&2; exit 1; }

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
[[ "$port" =~ ^[0-9]+$ ]] || die "PDH_CODESERVER_PORT muss eine Zahl sein."

if ss -ltnH "( sport = :${port} )" 2>/dev/null | grep -q . && ! docker ps --format '{{.Names}}' | grep -qx "$name"; then
    die "Port ${port} ist schon belegt. Freien Port wählen: sudo PDH_CODESERVER_PORT=8444 bash scripts/install-code-server.sh"
fi

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
mkdir -p "$(dirname "$pdh_env")"
touch "$pdh_env"
sed -i '/^PDH_CODESERVER_INTERNAL_URL=/d' "$pdh_env"
printf 'PDH_CODESERVER_INTERNAL_URL=%s\n' "http://127.0.0.1:${port}" >> "$pdh_env"
chmod 600 "$pdh_env"
systemctl restart pdh

cat <<EOF

Fertig.
  Code-Editor:     https://<eure-pdh-adresse>/code/   (als Administrator im PDH angemeldet)
                   bzw. Core-Einstellungen → Code-Editor (code-server)
  Arbeitsordner:   ${repo_dir}
  Abschalten:      sudo bash scripts/install-code-server.sh --remove
Bei PDH-Updates wird der Container automatisch neu gebaut.
EOF
