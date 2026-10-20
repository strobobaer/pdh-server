#!/usr/bin/env bash
set -Eeuo pipefail

# nginx vor dem PDH: WebSockets für den Code-Editor (/code/) durchreichen.
# Ohne das lädt /code/ zwar, bricht aber mit „WebSocket close with status code 1006“ ab.
#
# Trägt in die nginx-Site des PDH vor „location / {“ einen Block
#   location ^~ /code/ { … Upgrade/Connection, HTTP/1.1, lange Zeitlimits … }
# ein (gleiches proxy_pass-Ziel wie die Site) und legt die nötige map
# $connection_upgrade an, falls es sie noch nicht gibt. Sicherung der Site
# wird angelegt; bei Fehler in „nginx -t“ wird sie zurückgespielt.

SITE=""
MAP_FILE="/etc/nginx/conf.d/pdh-websocket-map.conf"
MARKER="# PDH: WebSockets für /code/ (scripts/configure-pdh-nginx-websockets.sh)"

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nFEHLER: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
nginx: WebSockets für den PDH-Code-Editor (/code/) durchreichen.

Optionen:
  --pdh-site DATEI   nginx-Site des PDH (sonst automatisch gesucht)
  -h, --help         Hilfe

Beispiel:
  sudo bash scripts/configure-pdh-nginx-websockets.sh
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --pdh-site) SITE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unbekanntes Argument: $1"; usage; exit 2 ;;
  esac
done

[[ "${EUID}" -eq 0 ]] || { err "Bitte mit sudo ausführen."; exit 1; }
command -v nginx >/dev/null || { err "nginx nicht gefunden – läuft das PDH ohne nginx, ist dieses Skript nicht nötig."; exit 1; }

if [[ -z "$SITE" ]]; then
  for c in /etc/nginx/sites-enabled/pdh /etc/nginx/sites-available/pdh /etc/nginx/sites-enabled/pdh.conf /etc/nginx/sites-available/pdh.conf; do
    [[ -f "$c" ]] && { SITE="$(readlink -f "$c")"; break; }
  done
fi
if [[ -z "$SITE" ]]; then
  SITE="$(grep -RslE '127\.0\.0\.1:8090|localhost:8090' /etc/nginx/sites-enabled /etc/nginx/conf.d 2>/dev/null | head -n 1 || true)"
  [[ -n "$SITE" ]] && SITE="$(readlink -f "$SITE")"
fi
[[ -n "$SITE" && -f "$SITE" ]] || { err "nginx-Site des PDH nicht gefunden. Mit --pdh-site /etc/nginx/sites-available/… angeben."; exit 1; }
log "PDH-Site: $SITE"

if grep -qF "$MARKER" "$SITE"; then
  log "WebSocket-Block ist schon eingetragen – nichts zu tun."
  nginx -t && systemctl reload nginx
  exit 0
fi

UPSTREAM="$(grep -oE 'proxy_pass[[:space:]]+http://[^;[:space:]]+' "$SITE" | head -n 1 | awk '{print $2}')"
UPSTREAM="${UPSTREAM%/}"
[[ -n "$UPSTREAM" ]] || { err "Kein proxy_pass in $SITE gefunden."; exit 1; }
log "Ziel: $UPSTREAM"
grep -qE '^[[:space:]]*location[[:space:]]+/[[:space:]]*\{' "$SITE" || { err "Kein „location / {“ in $SITE – Block bitte von Hand eintragen (siehe docs)."; exit 1; }

backup="$SITE.bak.ws.$(date +%Y%m%d%H%M%S)"
cp "$SITE" "$backup"
map_created=0
if ! grep -RqsE 'map[[:space:]]+\$http_upgrade[[:space:]]+\$connection_upgrade' /etc/nginx; then
  log "map \$connection_upgrade anlegen ($MAP_FILE)"
  cat > "$MAP_FILE" <<'EOF'
# PDH: WebSocket-Upgrade (scripts/configure-pdh-nginx-websockets.sh)
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
EOF
  map_created=1
fi

log "Block für /code/ vor „location / {“ eintragen (Sicherung: $backup)"
awk -v up="$UPSTREAM" -v marker="$MARKER" '
  /^[[:space:]]*location[[:space:]]+\/[[:space:]]*\{/ {
    match($0, /^[[:space:]]*/); ind = substr($0, 1, RLENGTH)
    print ind marker
    print ind "location ^~ /code/ {"
    print ind "    proxy_pass " up ";"
    print ind "    proxy_http_version 1.1;"
    print ind "    proxy_set_header Upgrade $http_upgrade;"
    print ind "    proxy_set_header Connection $connection_upgrade;"
    print ind "    proxy_set_header Host $host;"
    print ind "    proxy_set_header X-Real-IP $remote_addr;"
    print ind "    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;"
    print ind "    proxy_set_header X-Forwarded-Proto $scheme;"
    print ind "    proxy_read_timeout 1d;"
    print ind "    proxy_send_timeout 1d;"
    print ind "    proxy_buffering off;"
    print ind "    client_max_body_size 200m;"
    print ind "}"
  }
  { print }' "$backup" > "$SITE"

if nginx -t; then
  systemctl reload nginx
  log "Fertig – /code/ im Browser neu laden (Strg+F5)."
else
  err "nginx -t meldet einen Fehler – Änderung wird zurückgenommen."
  cp "$backup" "$SITE"
  [[ $map_created -eq 1 ]] && rm -f "$MAP_FILE"
  nginx -t && systemctl reload nginx || true
  exit 1
fi
