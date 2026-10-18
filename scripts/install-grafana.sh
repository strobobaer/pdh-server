#!/usr/bin/env bash
# Grafana (Statistik) für eine systemd-Installation des PDH einrichten.
#
#   sudo bash scripts/install-grafana.sh
#
# Was passiert:
#   * Grafana läuft als Docker-Container „pdh-grafana“ – nur lokal (127.0.0.1),
#     das PDH reicht es unter https://<pdh>/grafana/ durch (gleiche Anmeldung).
#   * Datenquelle „PDH“ (Nur-Lese-Benutzer pdh_grafana) und Dashboard
#     „PDH Übersicht“ aus grafana/provisioning.
#   * Dem PDH werden Grafana-Adresse und Datenbank-Passwort in /etc/pdh/pdh.env
#     eingetragen, danach wird der Dienst pdh neu gestartet.
# Erneut ausführen ist gefahrlos (Passwörter bleiben, Container wird neu angelegt).
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${PDH_GRAFANA_IMAGE:-grafana/grafana-oss:11.2.0}"
name="pdh-grafana"
conf_dir="/etc/pdh"
grafana_env="$conf_dir/grafana.env"
pdh_env="$conf_dir/pdh.env"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWarnung:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mFehler:\033[0m %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "Bitte mit sudo ausführen: sudo bash scripts/install-grafana.sh"
command -v docker >/dev/null || die "Docker fehlt (apt install docker.io)."
[[ -d "$repo_dir/grafana/provisioning" ]] || die "grafana/provisioning fehlt in $repo_dir – zuerst das PDH aktualisieren."

# letzter Wert eines Schlüssels aus den Dateien (ohne Anführungszeichen)
env_get() {
    local key="$1" value="" line f
    shift
    for f in "$@"; do
        [[ -r "$f" ]] || continue
        line="$(grep -E "^(export )?${key}=" "$f" | tail -n 1 || true)"
        [[ -n "$line" ]] && value="${line#*=}"
    done
    value="${value%\"}"; value="${value#\"}"; value="${value%\'}"; value="${value#\'}"
    printf '%s' "$value"
}

# Schlüssel in einer Datei setzen (ersetzt vorhandene Zeile)
set_kv() {
    local file="$1" key="$2" value="$3"
    touch "$file"
    sed -i "/^${key}=/d" "$file"
    printf '%s=%s\n' "$key" "$value" >> "$file"
}

random_secret() { openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# ── Datenbank des PDH ─────────────────────────────────────────
db_host="$(env_get PDH_DATABASE_HOST "$repo_dir/.env" "$pdh_env")"; db_host="${db_host:-localhost}"
db_port="$(env_get PDH_DATABASE_PORT "$repo_dir/.env" "$pdh_env")"; db_port="${db_port:-5432}"
db_name="$(env_get PDH_DATABASE_NAME "$repo_dir/.env" "$pdh_env")"; db_name="${db_name:-pdh}"
db_user="$(env_get PDH_DATABASE_USER "$repo_dir/.env" "$pdh_env")"; db_user="${db_user:-pdh}"
case "$db_host" in
    localhost|127.0.0.1|::1) db_url="127.0.0.1:${db_port}"; db_local=1 ;;
    *) db_url="${db_host}:${db_port}"; db_local=0 ;;
esac
log "PDH-Datenbank: ${db_name} auf ${db_url} (Eigentümer ${db_user})"

# ── Port ──────────────────────────────────────────────────────
port="${PDH_GRAFANA_PORT:-$(env_get GF_SERVER_HTTP_PORT "$grafana_env")}"
port="${port:-3000}"
if ss -ltnH "( sport = :${port} )" 2>/dev/null | grep -q . && ! docker ps --format '{{.Names}}' | grep -qx "$name"; then
    die "Port ${port} ist schon belegt. Freien Port wählen: sudo PDH_GRAFANA_PORT=3001 bash scripts/install-grafana.sh"
fi

# ── Passwörter (vorhandene bleiben) ───────────────────────────
mkdir -p "$conf_dir"
admin_pw="$(env_get GF_SECURITY_ADMIN_PASSWORD "$grafana_env")"; admin_pw="${admin_pw:-$(random_secret)}"
reader_pw="$(env_get PDH_GRAFANA_DB_PASSWORD "$grafana_env")"; reader_pw="${reader_pw:-$(random_secret)}"

# ── Nur-Lese-Benutzer in Postgres ─────────────────────────────
if [[ $db_local -eq 1 ]] && id postgres >/dev/null 2>&1 && command -v psql >/dev/null; then
    log "Lege den Nur-Lese-Benutzer pdh_grafana an (als Postgres-Superuser)"
    sudo -u postgres psql -v ON_ERROR_STOP=1 -q -d "$db_name" -v pw="$reader_pw" -v db="$db_name" -v owner="$db_user" <<'SQL'
SELECT format('CREATE ROLE pdh_grafana LOGIN PASSWORD %L', :'pw') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pdh_grafana') \gexec
SELECT format('ALTER ROLE pdh_grafana LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE', :'pw') \gexec
ALTER ROLE pdh_grafana SET default_transaction_read_only = on;
SELECT format('GRANT CONNECT ON DATABASE %I TO pdh_grafana', :'db') \gexec
GRANT USAGE ON SCHEMA public TO pdh_grafana;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pdh_grafana;
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT SELECT ON TABLES TO pdh_grafana', :'owner') \gexec
-- Passwörter, Schlüssel, Tokens: Tabellen mit solchen Spalten nur spaltenweise freigeben
-- (gleiche Regel wie internal/core/grafanadb.Sensitive; das PDH wiederholt das bei jedem Start)
DO $$
DECLARE t record; cols text;
BEGIN
    FOR t IN
        SELECT DISTINCT c.table_name FROM information_schema.columns c
        JOIN information_schema.tables x ON x.table_schema = c.table_schema AND x.table_name = c.table_name AND x.table_type = 'BASE TABLE'
        WHERE c.table_schema = 'public'
          AND c.column_name ~* '(password|passwd|secret|token|hash|credential|private_key|api_key|_key_enc|encrypted|p256dh|auth_key|salt)'
    LOOP
        EXECUTE format('REVOKE SELECT ON public.%I FROM pdh_grafana', t.table_name);
        SELECT string_agg(format('%I', column_name), ', ' ORDER BY ordinal_position) INTO cols
        FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = t.table_name
          AND column_name !~* '(password|passwd|secret|token|hash|credential|private_key|api_key|_key_enc|encrypted|p256dh|auth_key|salt)';
        IF cols IS NOT NULL THEN
            EXECUTE format('GRANT SELECT (%s) ON public.%I TO pdh_grafana', cols, t.table_name);
        END IF;
    END LOOP;
END $$;
SQL
else
    warn "Datenbank liegt nicht lokal – das PDH legt den Benutzer beim Start selbst an (braucht CREATEROLE)."
fi

# ── Konfiguration für den Container ───────────────────────────
log "Schreibe ${grafana_env}"
umask 077
cat > "$grafana_env" <<EOF
# Grafana für das PDH (scripts/install-grafana.sh) – nur lokal, Zugang über https://<pdh>/grafana/
GF_SERVER_HTTP_ADDR=127.0.0.1
GF_SERVER_HTTP_PORT=${port}
GF_SERVER_SERVE_FROM_SUB_PATH=true
GF_SERVER_ROOT_URL=%(protocol)s://%(domain)s:%(http_port)s/grafana/
GF_AUTH_PROXY_ENABLED=true
GF_AUTH_PROXY_HEADER_NAME=X-WEBAUTH-USER
GF_AUTH_PROXY_HEADER_PROPERTY=username
GF_AUTH_PROXY_AUTO_SIGN_UP=true
GF_AUTH_PROXY_HEADERS=Name:X-WEBAUTH-NAME Email:X-WEBAUTH-EMAIL Role:X-WEBAUTH-ROLE
GF_AUTH_PROXY_ENABLE_LOGIN_TOKEN=false
GF_SECURITY_ADMIN_USER=admin
GF_SECURITY_ADMIN_PASSWORD=${admin_pw}
GF_SECURITY_ALLOW_EMBEDDING=true
GF_USERS_ALLOW_SIGN_UP=false
GF_USERS_DEFAULT_LANGUAGE=de-DE
GF_ANALYTICS_REPORTING_ENABLED=false
GF_ANALYTICS_CHECK_FOR_UPDATES=false
GF_NEWS_NEWS_FEED_ENABLED=false
PDH_DATABASE_NAME=${db_name}
PDH_GRAFANA_DB_URL=${db_url}
PDH_GRAFANA_DB_PASSWORD=${reader_pw}
EOF
chmod 600 "$grafana_env"

# ── Container ─────────────────────────────────────────────────
log "Starte Grafana (${image}) als Container ${name}"
docker pull -q "$image" >/dev/null
docker rm -f "$name" >/dev/null 2>&1 || true
docker volume create pdh_grafana >/dev/null
docker run -d --name "$name" --restart unless-stopped --network host \
    --env-file "$grafana_env" \
    -v pdh_grafana:/var/lib/grafana \
    -v "$repo_dir/grafana/provisioning:/etc/grafana/provisioning:ro" \
    "$image" >/dev/null

printf 'Warte auf Grafana '
for _ in $(seq 1 60); do
    if curl -fs "http://127.0.0.1:${port}/grafana/api/health" >/dev/null 2>&1; then ok=1; break; fi
    printf '.'; sleep 2
done
echo
[[ "${ok:-0}" -eq 1 ]] || { docker logs --tail 40 "$name" >&2 || true; die "Grafana antwortet nicht – siehe Protokoll oben (docker logs ${name})."; }
log "Grafana läuft auf http://127.0.0.1:${port}/grafana/"

# ── PDH verbinden ─────────────────────────────────────────────
log "Trage Grafana in ${pdh_env} ein und starte das PDH neu"
set_kv "$pdh_env" PDH_GRAFANA_INTERNAL_URL "http://127.0.0.1:${port}"
set_kv "$pdh_env" PDH_GRAFANA_DB_PASSWORD "$reader_pw"
chmod 600 "$pdh_env"
systemctl restart pdh
sleep 3
if journalctl -u pdh --since "-30s" --no-pager 2>/dev/null | grep -q "nur-lese-benutzer eingerichtet"; then
    log "PDH: Nur-Lese-Benutzer eingerichtet (ohne Passwort- und Schlüsselspalten)."
else
    warn "PDH meldet keine Einrichtung des Nur-Lese-Benutzers – prüfen: journalctl -u pdh -n 80 | grep -i grafana"
fi

cat <<EOF

Fertig.
  Statistik im PDH:      Navigation → Statistik
  Grafana direkt:        https://<eure-pdh-adresse>/grafana/   (Anmeldung über das PDH)
  Notfall am Server:     http://127.0.0.1:${port}/grafana/  – admin / Passwort in ${grafana_env}
In Core-Einstellungen → Grafana & Statistik sollte der Punkt jetzt grün sein.
EOF
