#!/usr/bin/env bash
set -Eeuo pipefail

NEXTCLOUD_ORIGIN="https://cloud.strobl-home.net"
PDH_ORIGIN="https://pdh.strobl-home.net"
NGINX_SNIPPET="/etc/nginx/snippets/pdh-frame-embedding.conf"
PDH_SITE=""

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Allow PDH embedding inside the Nextcloud PDH app.

Options:
  --nextcloud-origin URL   Default: https://cloud.strobl-home.net
  --pdh-origin URL         Default: https://pdh.strobl-home.net
  --pdh-site PATH          Nginx PDH site file. Auto-detected when omitted.
  -h, --help               Show help
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --nextcloud-origin) NEXTCLOUD_ORIGIN="${2:-}"; shift 2 ;;
    --pdh-origin) PDH_ORIGIN="${2:-}"; shift 2 ;;
    --pdh-site) PDH_SITE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  err "Run as root."
  exit 1
fi

if [[ -z "${PDH_SITE}" ]]; then
  PDH_SITE="$(grep -Rsl 'pdh.strobl-home.net\|127.0.0.1:8090\|localhost:8090' /etc/nginx/sites-available /etc/nginx/conf.d 2>/dev/null | head -n 1 || true)"
fi

if [[ -z "${PDH_SITE}" || ! -f "${PDH_SITE}" ]]; then
  err "Could not detect PDH Nginx site. Use --pdh-site /path/to/site.conf"
  exit 1
fi

log "Using PDH site: ${PDH_SITE}"
cp "${PDH_SITE}" "${PDH_SITE}.bak.frame.$(date +%Y%m%d%H%M%S)"

mkdir -p "$(dirname "${NGINX_SNIPPET}")"
cat > "${NGINX_SNIPPET}" <<NGINX
proxy_hide_header X-Frame-Options;
proxy_hide_header Content-Security-Policy;
add_header Content-Security-Policy "frame-ancestors 'self' ${NEXTCLOUD_ORIGIN} ${PDH_ORIGIN}" always;
add_header X-Content-Type-Options "nosniff" always;
add_header Referrer-Policy "no-referrer" always;
NGINX

if ! grep -q "${NGINX_SNIPPET}" "${PDH_SITE}"; then
  sed -i "/proxy_pass http:\/\/127.0.0.1:8090/a\        include ${NGINX_SNIPPET};" "${PDH_SITE}"
  sed -i "/proxy_pass http:\/\/localhost:8090/a\        include ${NGINX_SNIPPET};" "${PDH_SITE}"
fi

nginx -t
systemctl reload nginx

log "Header check"
curl -kI "${PDH_ORIGIN}" | grep -Ei 'content-security-policy|x-frame-options|http/' || true

log "Done"
