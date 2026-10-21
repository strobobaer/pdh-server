#!/usr/bin/env bash
# „Update-Agent antwortet mit 401 Unauthorized“ beheben (systemd-Installation):
# PDH und Update-Agent auf dasselbe Token bringen.
#
#   sudo bash scripts/repair-update-agent-token.sh
#
# Maßgeblich ist das Token aus der Datei des Agenten (<PDH-Verzeichnis>/.env,
# siehe install-update-agent.sh). Jede weitere Datei, aus der der Dienst pdh
# Umgebungsvariablen liest (z. B. /etc/pdh/pdh.env), bekommt denselben Wert.
# Ein in der Datenbank gespeicherter Wert wird ab Version 0.55.0 ignoriert und
# beim Start entfernt (Migration 131). Das Token wird nicht ausgegeben.
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
agent_env="$repo_dir/.env"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWarnung:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mFehler:\033[0m %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "Bitte mit sudo ausführen: sudo bash scripts/repair-update-agent-token.sh"
[[ -f "$agent_env" ]] || die "$agent_env fehlt – zuerst sudo ./install-update-agent.sh ausführen."

get_token() { grep -E '^PDH_UPDATE_AGENT_TOKEN=' "$1" 2>/dev/null | tail -n1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' || true; }
fp() { if [[ -n "$1" ]]; then printf '%s' "$1" | sha256sum | cut -c1-8; else printf '(leer)'; fi; }

token="$(get_token "$agent_env")"
if [[ ${#token} -lt 32 ]]; then
  log "Kein gültiges Token in $agent_env – neues erzeugen"
  token="$(openssl rand -hex 32)"
  sed -i '/^PDH_UPDATE_AGENT_TOKEN=/d' "$agent_env"
  printf 'PDH_UPDATE_AGENT_TOKEN=%s\n' "$token" >> "$agent_env"
fi
log "Token des Agenten: Fingerabdruck $(fp "$token") ($agent_env)"

# Adresse des Agenten: vor 0.55.2 konnte die Übernahme in die Datenbank die
# Zeile auskommentieren – dann ist das PDH „ohne Update-Agent“.
if ! grep -qE '^PDH_UPDATE_AGENT_URL=.+' "$agent_env"; then
  log "PDH_UPDATE_AGENT_URL fehlt in $agent_env → http://127.0.0.1:8091 eingetragen"
  sed -i '/^PDH_UPDATE_AGENT_URL=/d' "$agent_env"
  printf 'PDH_UPDATE_AGENT_URL=http://127.0.0.1:8091\n' >> "$agent_env"
fi

# laufender Agent: welches Token hat er geladen?
agent_pid="$(systemctl show -p MainPID --value pdh-update-agent 2>/dev/null || echo 0)"
if [[ "$agent_pid" != "0" && -r "/proc/$agent_pid/environ" ]]; then
  running="$(tr '\0' '\n' < "/proc/$agent_pid/environ" | sed -n 's/^PDH_UPDATE_AGENT_TOKEN=//p' | tail -n1)"
  log "Laufender Agent: Fingerabdruck $(fp "$running")"
fi

# Dateien, aus denen der Dienst pdh liest
mapfile -t files < <(systemctl show -p EnvironmentFiles --value pdh 2>/dev/null | tr ' ' '\n' | sed -n 's/^-\{0,1\}\(\/[^ (]*\).*/\1/p' | sort -u)
[[ -f /etc/pdh/pdh.env ]] && files+=("/etc/pdh/pdh.env")
fixed=0
for f in $(printf '%s\n' "${files[@]}" | sort -u); do
  [[ -f "$f" && "$(readlink -f "$f")" != "$(readlink -f "$agent_env")" ]] || continue
  cur="$(get_token "$f")"
  [[ -z "$cur" ]] && continue
  if [[ "$cur" != "$token" ]]; then
    log "$f: abweichendes Token ($(fp "$cur")) → angeglichen"
    cp -a "$f" "$f.bak.agenttoken.$(date +%Y%m%d%H%M%S)"
    sed -i '/^PDH_UPDATE_AGENT_TOKEN=/d' "$f"
    printf 'PDH_UPDATE_AGENT_TOKEN=%s\n' "$token" >> "$f"
    fixed=1
  else
    log "$f: passt"
  fi
done

log "Agent und PDH neu starten"
systemctl restart pdh-update-agent
systemctl restart pdh
sleep 3
if curl -fsS -H "Authorization: Bearer $token" http://127.0.0.1:8091/v1/status >/dev/null 2>&1; then
  log "Agent nimmt das Token an."
else
  warn "Agent antwortet nicht wie erwartet – journalctl -u pdh-update-agent -n 30"
fi
cat <<EOF

Fertig$( [[ $fixed -eq 1 ]] && printf ' – Dateien angeglichen' ).
Kommt im PDH weiter „401 Unauthorized“, steht noch ein abweichendes Token in der
Datenbank (vor Version 0.55.0). Dann einmal aktualisieren:
  sudo PDH_UPDATE_MODE=systemd bash install_update.sh
Das Update braucht den Agenten nicht; beim Start entfernt Migration 131 den Datenbank-Wert.
EOF
