#!/usr/bin/env bash
set -Eeuo pipefail

# Nextcloud-Anmeldung mit Microsoft 365 (Entra ID) über die App user_oidc.
#
# Nextcloud-Benutzer-ID = E-Mail aus Entra (mapping-uid=email). Das passt zu
# PDH: Core-Einstellungen → Microsoft 365 → „Nextcloud-Konten anlegen“ legt die
# Konten mit genau dieser ID an. Standardmäßig dürfen sich nur Konten anmelden,
# die es in Nextcloud schon gibt (auto_provision=false) – PDH entscheidet also,
# wer Nextcloud bekommt.
#
# Entra (App-Registrierung, z. B. die des PDH):
#   - Authentifizierung → Web-Redirect-URI: <Nextcloud-URL>/apps/user_oidc/code
#   - Tokenkonfiguration → optionaler Anspruch „email“ (ID-Token)
#
# Das Client-Secret wird nicht ausgegeben.

NEXTCLOUD_DIR="/var/www/nextcloud"
ENV_FILE="/etc/pdh/pdh.env"
PROVIDER="Microsoft"
TENANT_ID=""
CLIENT_ID=""
CLIENT_SECRET="${ENTRA_CLIENT_SECRET:-}"
AUTO_PROVISION="false"
ONLY_MICROSOFT="false"

log() { printf '\n==> %s\n' "$*"; }
err() { printf '\nERROR: %s\n' "$*" >&2; }

usage() {
  cat <<'USAGE'
Configure Nextcloud login with Microsoft 365 (Entra ID) via user_oidc.

Options:
  --tenant-id ID            Default: PDH_MICROSOFT_TENANT_ID from env file
  --client-id ID            Default: PDH_MICROSOFT_CLIENT_ID from env file
  --client-secret VALUE     Default: $ENTRA_CLIENT_SECRET or PDH_MICROSOFT_CLIENT_SECRET from env file
  --provider NAME           Button label on the login page. Default: Microsoft
  --auto-provision          Let every tenant user create a Nextcloud account on first login
                            (default: only accounts that already exist, e.g. created by PDH)
  --only-microsoft          Hide the password form on the Nextcloud login page
  --env-file PATH           Default: /etc/pdh/pdh.env
  --nextcloud-dir PATH      Default: /var/www/nextcloud
  -h, --help                Show help

Example:
  sudo bash scripts/configure-nextcloud-microsoft-login.sh
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tenant-id) TENANT_ID="${2:-}"; shift 2 ;;
    --client-id) CLIENT_ID="${2:-}"; shift 2 ;;
    --client-secret) CLIENT_SECRET="${2:-}"; shift 2 ;;
    --provider) PROVIDER="${2:-}"; shift 2 ;;
    --auto-provision) AUTO_PROVISION="true"; shift ;;
    --only-microsoft) ONLY_MICROSOFT="true"; shift ;;
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --nextcloud-dir) NEXTCLOUD_DIR="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "Unknown argument: $1"; usage; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  err "Run as root."
  exit 1
fi

if [[ ! -f "${NEXTCLOUD_DIR}/occ" ]]; then
  err "Nextcloud occ not found: ${NEXTCLOUD_DIR}/occ"
  exit 1
fi

env_value() {
  [[ -f "${ENV_FILE}" ]] || return 0
  grep -E "^$1=" "${ENV_FILE}" | tail -n1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//'
}
[[ -n "${TENANT_ID}" ]] || TENANT_ID="$(env_value PDH_MICROSOFT_TENANT_ID)"
[[ -n "${CLIENT_ID}" ]] || CLIENT_ID="$(env_value PDH_MICROSOFT_CLIENT_ID)"
[[ -n "${CLIENT_SECRET}" ]] || CLIENT_SECRET="$(env_value PDH_MICROSOFT_CLIENT_SECRET)"

if [[ -z "${TENANT_ID}" || -z "${CLIENT_ID}" || -z "${CLIENT_SECRET}" ]]; then
  err "Tenant-ID, Client-ID and Client-Secret required (options or ${ENV_FILE})."
  err "Values kept only in the PDH database (Server-Einstellungen): pass them as options."
  exit 1
fi

OCC=(sudo -u www-data php -d apc.enable_cli=1 "${NEXTCLOUD_DIR}/occ")

log "Installing/enabling user_oidc"
if ! "${OCC[@]}" app:list --output=json | grep -q '"user_oidc"'; then
  "${OCC[@]}" app:install user_oidc
fi
"${OCC[@]}" app:enable user_oidc >/dev/null

log "Configuring provider ${PROVIDER}"
"${OCC[@]}" user_oidc:provider "${PROVIDER}" \
  --clientid="${CLIENT_ID}" \
  --clientsecret="${CLIENT_SECRET}" \
  --discoveryuri="https://login.microsoftonline.com/${TENANT_ID}/v2.0/.well-known/openid-configuration" \
  --scope="openid email profile" \
  --unique-uid=0 \
  --mapping-uid=email \
  --mapping-display-name=name \
  --mapping-email=email >/dev/null

log "Account rules"
"${OCC[@]}" config:system:set user_oidc auto_provision --value="${AUTO_PROVISION}" --type=boolean >/dev/null
"${OCC[@]}" config:system:set user_oidc soft_auto_provision --value=true --type=boolean >/dev/null
if [[ "${ONLY_MICROSOFT}" == "true" ]]; then
  "${OCC[@]}" config:app:set user_oidc allow_multiple_user_backends --value=0 >/dev/null
else
  "${OCC[@]}" config:app:set user_oidc allow_multiple_user_backends --value=1 >/dev/null
fi

log "Checking"
"${OCC[@]}" user_oidc:provider || true

CLOUD_URL="$("${OCC[@]}" config:system:get overwrite.cli.url 2>/dev/null || true)"
log "Configured"
printf 'Login button: "Log in with %s". Secret not printed.\n' "${PROVIDER}"
printf 'Auto-provision: %s, password form hidden: %s\n' "${AUTO_PROVISION}" "${ONLY_MICROSOFT}"
printf 'Entra redirect URI: %s/apps/user_oidc/code\n' "${CLOUD_URL%/}"
printf 'Entra token configuration: add optional claim "email" (ID token).\n'
