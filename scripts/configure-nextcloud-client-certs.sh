#!/usr/bin/env bash
set -Eeuo pipefail

# nginx vor Nextcloud: nur Geräte mit Zertifikat aus der PDH-Zertifizierungsstelle
# (Core-Einstellungen → Zertifikate) hereinlassen.
#
# Schreibt:
#   /etc/nginx/pdh-pki/ca.crt, crl.pem          CA und Sperrliste aus dem PDH
#   /etc/nginx/snippets/pdh-client-cert.conf    in den server-Block von Nextcloud einbinden
#   systemd-Timer pdh-pki-crl (täglich)         Sperrliste holen, nginx neu laden
#
# Ausgenommen sind Zugriffe von lokalen Adressen (Standard: 127.0.0.1/::1) –
# dazu gehören der Cloudflare-Tunnel (cloudflared) und das PDH selbst.

PDH_URL="http://127.0.0.1:8090"
DIR="/etc/nginx/pdh-pki"
SNIPPET="/etc/nginx/snippets/pdh-client-cert.conf"
EXEMPT='^(127\.|::1$)'
SITE=""

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
nginx: Client-Zertifikate aus dem PDH für Nextcloud verlangen.

Optionen:
  --pdh-url URL        PDH-Adresse (Standard: http://127.0.0.1:8090)
  --exempt REGEX       Adressen ohne Zertifikatspflicht (Standard: '^(127\.|::1$)'),
                       z. B. '^(127\.|::1$|192\.168\.1\.)' für das Firmennetz
  --site DATEI         nginx-Site von Nextcloud: Snippet automatisch einbinden
                       (z. B. /etc/nginx/sites-available/nextcloud)
  --remove             Zertifikatspflicht wieder entfernen
  -h, --help           Hilfe

Beispiel:
  sudo bash scripts/configure-nextcloud-client-certs.sh --site /etc/nginx/sites-available/nextcloud
USAGE
}

REMOVE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pdh-url) PDH_URL="${2:-}"; shift 2 ;;
    --exempt) EXEMPT="${2:-}"; shift 2 ;;
    --site) SITE="${2:-}"; shift 2 ;;
    --remove) REMOVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unbekanntes Argument: $1"; usage; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  err "Bitte als root ausführen (sudo)."
  exit 1
fi
command -v nginx >/dev/null || { err "nginx nicht gefunden"; exit 1; }

if [[ "$REMOVE" == "1" ]]; then
  log "Zertifikatspflicht entfernen"
  systemctl disable --now pdh-pki-crl.timer 2>/dev/null || true
  rm -f /etc/systemd/system/pdh-pki-crl.service /etc/systemd/system/pdh-pki-crl.timer
  systemctl daemon-reload
  if [[ -n "$SITE" && -f "$SITE" ]]; then
    sed -i '\#include /etc/nginx/snippets/pdh-client-cert.conf;#d' "$SITE"
  fi
  : > "$SNIPPET"   # leer lassen, damit vergessene include-Zeilen nicht brechen
  nginx -t && systemctl reload nginx
  log "Fertig – Nextcloud verlangt kein Zertifikat mehr."
  exit 0
fi

log "CA und Sperrliste vom PDH holen ($PDH_URL)"
mkdir -p "$DIR" "$(dirname "$SNIPPET")"
curl -fsS "$PDH_URL/pki/ca.crt" -o "$DIR/ca.crt.new"
curl -fsS "$PDH_URL/pki/crl.pem" -o "$DIR/crl.pem.new"
grep -q "BEGIN CERTIFICATE" "$DIR/ca.crt.new" || { err "Antwort ist kein Zertifikat"; exit 1; }
grep -q "BEGIN X509 CRL" "$DIR/crl.pem.new" || { err "Antwort ist keine Sperrliste"; exit 1; }
mv "$DIR/ca.crt.new" "$DIR/ca.crt"
mv "$DIR/crl.pem.new" "$DIR/crl.pem"
chmod 644 "$DIR/ca.crt" "$DIR/crl.pem"

log "nginx-Snippet schreiben ($SNIPPET)"
cat > "$SNIPPET" <<EOF
# PDH: Client-Zertifikate für Nextcloud (scripts/configure-nextcloud-client-certs.sh)
ssl_client_certificate $DIR/ca.crt;
ssl_crl $DIR/crl.pem;
ssl_verify_client optional;
ssl_verify_depth 2;
set \$pdh_cert_ok \$ssl_client_verify;
if (\$remote_addr ~ "$EXEMPT") { set \$pdh_cert_ok SUCCESS; }
if (\$pdh_cert_ok != SUCCESS) { return 403; }
EOF

if [[ -n "$SITE" ]]; then
  [[ -f "$SITE" ]] || { err "Site nicht gefunden: $SITE"; exit 1; }
  if ! grep -q "pdh-client-cert.conf" "$SITE"; then
    log "Snippet in $SITE einbinden (Sicherung: $SITE.bak.pki)"
    cp "$SITE" "$SITE.bak.pki"
    # je server-Block mit „listen … ssl“ einmal, hinter die erste listen-Zeile
    # (IPv4 und IPv6 im selben Block → trotzdem nur ein include)
    awk '/^[[:space:]]*server[[:space:]]*\{/ { done = 0 }
         { print }
         /^[[:space:]]*listen[[:space:]].*ssl/ && !done { print "    include /etc/nginx/snippets/pdh-client-cert.conf;"; done = 1 }' "$SITE.bak.pki" > "$SITE"
  fi
fi

log "Täglichen Abruf der Sperrliste einrichten (systemd-Timer pdh-pki-crl)"
cat > /etc/systemd/system/pdh-pki-crl.service <<EOF
[Unit]
Description=PDH: Sperrliste für Nextcloud-Client-Zertifikate holen
After=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'curl -fsS "$PDH_URL/pki/crl.pem" -o "$DIR/crl.pem.new" && grep -q "BEGIN X509 CRL" "$DIR/crl.pem.new" && mv "$DIR/crl.pem.new" "$DIR/crl.pem" && nginx -t -q && systemctl reload nginx'
EOF
cat > /etc/systemd/system/pdh-pki-crl.timer <<'EOF'
[Unit]
Description=PDH: Sperrliste täglich holen

[Timer]
OnBootSec=2min
OnUnitActiveSec=6h
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now pdh-pki-crl.timer

log "nginx prüfen und neu laden"
if nginx -t; then
  systemctl reload nginx
else
  err "nginx-Konfiguration fehlerhaft – nichts neu geladen. Sicherung: ${SITE:-(keine)}.bak.pki"
  exit 1
fi

log "Fertig"
if [[ -z "$SITE" ]]; then
  printf 'Jetzt im server-Block von Nextcloud (der mit „listen 443 ssl“) einfügen:\n    include %s;\nund dann: sudo nginx -t && sudo systemctl reload nginx\n' "$SNIPPET"
fi
printf 'Ohne Zertifikat antwortet Nextcloud mit 403 – außer für Adressen passend zu %s.\n' "$EXEMPT"
printf 'Gesperrte Zertifikate gelten spätestens nach 6 Stunden nicht mehr (sofort: sudo systemctl start pdh-pki-crl).\n'
