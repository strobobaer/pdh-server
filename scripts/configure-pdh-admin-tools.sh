#!/usr/bin/env bash
# n8n und pgAdmin unter derselben Adresse wie das PDH einrichten:
#   https://<pdh>/n8n/      (Webhooks ohne Anmeldung, sonst nur PDH-Administratoren)
#   https://<pdh>/pgadmin/  (nur PDH-Administratoren, pgAdmin-Anmeldung bleibt)
# und die alte, ungeschützte code-server-Site in nginx abschalten (optional).
#
#   sudo bash scripts/configure-pdh-admin-tools.sh
#   sudo bash scripts/configure-pdh-admin-tools.sh --disable-old-code-site
#
# Was passiert:
#   * Ports der Container (Standard: pdh-admin-n8n, pdh-admin-pgadmin) ermitteln
#     und PDH_N8N_INTERNAL_URL / PDH_PGADMIN_INTERNAL_URL in /etc/pdh/pdh.env eintragen
#   * prüfen, ob n8n für den Unterpfad /n8n/ eingestellt ist – sonst die nötigen
#     Umgebungsvariablen ausgeben (die Compose-Datei wird nicht verändert)
#   * nginx: WebSockets für /code/, /n8n/, /pgadmin/ (configure-pdh-nginx-websockets.sh)
#   * PDH neu starten
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pdh_env="/etc/pdh/pdh.env"
n8n_name="pdh-admin-n8n"
pg_name="pdh-admin-pgadmin"
n8n_url=""
pg_url=""
public_url=""
disable_old=0
skip_n8n=0
skip_pg=0

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWarnung:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mFehler:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
n8n und pgAdmin unter /n8n/ und /pgadmin/ des PDH einrichten.

Optionen:
  --n8n URL                 interne Adresse von n8n (sonst per „docker port“ ermittelt)
  --pgadmin URL             interne Adresse von pgAdmin (sonst per „docker port“)
  --n8n-container NAME      Standard: pdh-admin-n8n
  --pgadmin-container NAME  Standard: pdh-admin-pgadmin
  --no-n8n / --no-pgadmin   Werkzeug auslassen
  --public-url URL          öffentliche PDH-Adresse (Standard: PDH_PUBLIC_URL bzw. https://pdh.strobl-home.net)
  --disable-old-code-site   alte nginx-Site „code-server“ abschalten (Code-Editor nur noch unter /code/)
  -h, --help                Hilfe
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --n8n) n8n_url="${2:-}"; shift 2 ;;
    --pgadmin) pg_url="${2:-}"; shift 2 ;;
    --n8n-container) n8n_name="${2:-}"; shift 2 ;;
    --pgadmin-container) pg_name="${2:-}"; shift 2 ;;
    --no-n8n) skip_n8n=1; shift ;;
    --no-pgadmin) skip_pg=1; shift ;;
    --public-url) public_url="${2:-}"; shift 2 ;;
    --disable-old-code-site) disable_old=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "Unbekanntes Argument: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "Bitte mit sudo ausführen: sudo bash scripts/configure-pdh-admin-tools.sh"

set_kv() {
  local key="$1" value="$2"
  mkdir -p "$(dirname "$pdh_env")"; touch "$pdh_env"
  sed -i "/^${key}=/d" "$pdh_env"
  printf '%s=%s\n' "$key" "$value" >> "$pdh_env"
  chmod 600 "$pdh_env"
}
env_get() { grep -E "^$1=" "$pdh_env" 2>/dev/null | tail -n1 | cut -d= -f2- || true; }

# „127.0.0.1:5678“ bzw. „0.0.0.0:5678“ → http://127.0.0.1:5678
container_url() {
  local name="$1" port="$2" hp
  command -v docker >/dev/null || return 0
  docker ps --format '{{.Names}}' | grep -qx "$name" || return 0
  hp="$(docker port "$name" "$port" 2>/dev/null | grep -E '^(127\.0\.0\.1|0\.0\.0\.0):' | head -n1 | cut -d: -f2)"
  [[ -n "$hp" ]] && printf 'http://127.0.0.1:%s' "$hp"
}

if [[ -z "$public_url" ]]; then public_url="$(env_get PDH_PUBLIC_URL)"; fi
public_url="${public_url:-https://pdh.strobl-home.net}"
public_url="${public_url%/}"
changed=0

# ── pgAdmin ──────────────────────────────────────────────────
if [[ $skip_pg -eq 0 ]]; then
  [[ -n "$pg_url" ]] || pg_url="$(container_url "$pg_name" 80)"
  if [[ -n "$pg_url" ]]; then
    log "pgAdmin: $pg_url → $public_url/pgadmin/"
    set_kv PDH_PGADMIN_INTERNAL_URL "$pg_url"; changed=1
  else
    warn "pgAdmin-Container „$pg_name“ mit lokalem Port 80 nicht gefunden – mit --pgadmin http://127.0.0.1:PORT angeben."
  fi
fi

# ── n8n ──────────────────────────────────────────────────────
if [[ $skip_n8n -eq 0 ]]; then
  [[ -n "$n8n_url" ]] || n8n_url="$(container_url "$n8n_name" 5678)"
  if [[ -n "$n8n_url" ]]; then
    log "n8n: $n8n_url → $public_url/n8n/"
    set_kv PDH_N8N_INTERNAL_URL "$n8n_url"; changed=1
    path="$(docker inspect "$n8n_name" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | sed -n 's/^N8N_PATH=//p' | head -n1 || true)"
    if [[ "$path" != "/n8n/" ]]; then
      dir="$(docker inspect "$n8n_name" --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' 2>/dev/null || true)"
      warn "n8n ist noch nicht für den Unterpfad /n8n/ eingestellt (N8N_PATH=${path:-nicht gesetzt})."
      cat <<EOF
    In der Compose-Datei von n8n${dir:+ ($dir)} beim Dienst n8n unter environment ergänzen:
        N8N_PATH: /n8n/
        N8N_EDITOR_BASE_URL: $public_url/n8n/
        WEBHOOK_URL: $public_url/n8n/
        N8N_PROXY_HOPS: "1"
    danach dort:  docker compose up -d
    Achtung: Webhook-Adressen in n8n lauten dann $public_url/n8n/webhook/… –
    externe Dienste, die n8n bisher unter einer anderen Adresse aufrufen, umstellen.
EOF
    fi
  else
    warn "n8n-Container „$n8n_name“ mit lokalem Port 5678 nicht gefunden – mit --n8n http://127.0.0.1:PORT angeben."
  fi
fi

# ── alte code-server-Site ────────────────────────────────────
old_site=""
for c in /etc/nginx/sites-enabled/code-server /etc/nginx/sites-enabled/code-server.conf; do
  [[ -e "$c" ]] && old_site="$c"
done
if [[ -n "$old_site" ]]; then
  names="$(grep -hE '^[[:space:]]*server_name' "$old_site" | sed 's/server_name//; s/;//' | xargs || true)"
  if [[ $disable_old -eq 1 ]]; then
    log "Alte code-server-Site abschalten: $old_site (${names:-ohne server_name})"
    mkdir -p /etc/nginx/pdh-disabled
    cp -a "$old_site" "/etc/nginx/pdh-disabled/$(basename "$old_site").$(date +%Y%m%d%H%M%S)"
    rm -f "$old_site"
    if nginx -t; then
      systemctl reload nginx
      log "Abgeschaltet. Den Code-Editor gibt es weiter unter $public_url/code/ (nur Administratoren)."
      [[ -n "$names" ]] && warn "Im Cloudflare-Tunnel den öffentlichen Hostnamen „$names“ entfernen, sonst zeigt er auf eine nicht mehr vorhandene Seite."
    else
      cp -a "/etc/nginx/pdh-disabled/$(ls -t /etc/nginx/pdh-disabled | head -n1)" "$old_site"
      die "nginx -t meldet einen Fehler – alte Site wiederhergestellt."
    fi
  else
    warn "Die alte nginx-Site „$old_site“ (${names:-ohne server_name}) macht den Code-Editor ohne PDH-Anmeldung erreichbar."
    echo "    Abschalten mit: sudo bash scripts/configure-pdh-admin-tools.sh --disable-old-code-site"
  fi
fi

# ── nginx-WebSockets und Neustart ────────────────────────────
if command -v nginx >/dev/null && [[ -f "$repo_dir/scripts/configure-pdh-nginx-websockets.sh" ]]; then
  log "nginx: WebSockets für /code/, /n8n/, /pgadmin/"
  bash "$repo_dir/scripts/configure-pdh-nginx-websockets.sh" || warn "nginx-WebSockets nicht eingerichtet – siehe Meldung oben."
fi
if [[ $changed -eq 1 ]]; then
  log "PDH neu starten"
  systemctl restart pdh
fi
cat <<EOF

Fertig. Für Administratoren unter Core-Einstellungen verlinkt:
  $public_url/n8n/       $public_url/pgadmin/
EOF
