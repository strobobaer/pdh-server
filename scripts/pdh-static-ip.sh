#!/usr/bin/env bash
# PDH – IPv4-Adresse dieses Servers fest einstellen (statt DHCP).
#
#   sudo bash pdh-static-ip.sh
#       übernimmt die jetzige Adresse, das Gateway und die DNS-Server fest
#       (Vorschlag anzeigen, nachfragen, dann „netplan try“).
#
#   sudo bash pdh-static-ip.sh --ip 192.0.2.14/24 --gw 192.0.2.1 --dns 192.0.2.1,1.1.1.1
#       andere Werte vorgeben.
#
# Optionen:
#   --if NAME     Netzwerkschnittstelle (Standard: die mit der Standardroute)
#   --ip A/P      Adresse mit Präfix, z. B. 192.0.2.14/24 (Standard: jetzige)
#   --gw ADRESSE  Gateway (Standard: jetziges)
#   --dns LISTE   DNS-Server, mit Komma getrennt (Standard: jetzige, sonst Gateway)
#   --dry-run     nur anzeigen, was geschrieben würde
#   --yes         ohne Rückfrage und ohne Testlauf anwenden (netplan apply)
#   --force       Adresse ändern, obwohl der Server im Ausfallschutz-Verbund ist
#
# Sicher über SSH: „netplan try“ nimmt die Änderung nach 120 s ohne Bestätigung
# selbst zurück. IPv6 bleibt unverändert. Die Adresse im Router aus dem
# DHCP-Bereich nehmen oder reservieren, sonst vergibt er sie ein zweites Mal.
#
# Ohne Rückfrage (Update-Agent, Seite „Netzwerk“ → Karte „Dieser Server“):
#   --auto          jetzige Adresse und jetziges Gateway fest einstellen, DNS aus
#                   --dns (ein bis drei) oder die jetzigen. --ip ist Pflicht, --ip,
#                   --if und --gw nur mit den jetzigen Werten, die Karte muss in
#                   netplan unter „ethernets“ stehen; --force, --yes, --dry-run nicht.
#                   Vor dem Schreiben stellt das Skript einen eigenen systemd-Wecker:
#                   ohne --confirm kommt nach Ablauf der Frist die vorige
#                   Einstellung zurück (auch nach einem Neustart, siehe --resume).
#   --rollback SEK  Frist bis zum Rückfall (Standard 120)
#   --ha-maint      Wartung des Ausfallschutzes bis Bestätigen bzw. Rückfall an;
#                   ausgeschaltet wird nur, solange niemand sie umgestellt hat
#   --by TEXT       wer die Änderung ausgelöst bzw. bestätigt hat
#   --confirm       wartende, angewendete Änderung behalten (--id KENNUNG: nur diese)
#   --restore       wartende Änderung zurücknehmen (ruft der Wecker auf)
#   --resume        nach einem Neustart: abgelaufene Änderung zurücknehmen, sonst
#                   den Wecker mit der Restzeit neu stellen; offene Wartung ausschalten
#   --maint-off     nur die noch offene Wartung einer früheren Änderung ausschalten
# Stand in /var/lib/pdh-net/state. Ausgang 2: unzulässige Optionen; 3: es wartet
# schon eine bzw. keine Änderung; 1 bei --maint-off: später erneut versuchen.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -Eeuo pipefail

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }
usage() { awk 'NR > 1 { if (!/^#/) exit; sub(/^# ?/, ""); print }' "${BASH_SOURCE[0]}"; }

ipv4_re='^([0-9]{1,3}\.){3}[0-9]{1,3}$'

# Ordner und Programme (Umgebung nur für Tests; Standard = Ubuntu-Server)
STATE_DIR="${PDH_NET_STATE_DIR:-/var/lib/pdh-net}"
NETPLAN_DIR="${PDH_NETPLAN_DIR:-/etc/netplan}"
CLOUD_DIR="${PDH_CLOUD_CFG_DIR:-/etc/cloud/cloud.cfg.d}"
PDHHA="${PDHHA:-/usr/local/bin/pdh-ha}"
FILE="$NETPLAN_DIR/90-pdh-static.yaml"
CLOUD_FILE="$CLOUD_DIR/99-pdh-static-ip.cfg"
BAK_YAML="$STATE_DIR/90-pdh-static.yaml.vorher"
BAK_CLOUD="$STATE_DIR/99-pdh-static-ip.cfg.vorher"
TIMER_UNIT=pdh-static-ip-rollback
# Zeitgrenzen (Sekunden): netplan darf den Rückfall nie aufhalten; der Wecker versucht die
# Sperre so lange, danach holt der Update-Agent den Rückfall nach.
NP_GENERATE_TIMEOUT="${PDH_NET_GENERATE_TIMEOUT:-60}"
NP_APPLY_TIMEOUT="${PDH_NET_APPLY_TIMEOUT:-90}"
NP_GET_TIMEOUT="${PDH_NET_GET_TIMEOUT:-30}"
RESTORE_WAIT="${PDH_NET_RESTORE_WAIT:-600}"

# Netplan führt Dateien zusammen; der Eintrag muss dieselbe Kennung haben wie
# die bisherige Definition der Schnittstelle (z. B. aus 50-cloud-init.yaml).
netplan_id() {
  local iface="$1" ids
  if ids="$(netplan get ethernets 2>/dev/null | grep -E '^[^ #][^:]*:' | sed 's/:.*//')"; then
    if ! grep -qx "$iface" <<<"$ids" && [[ "$(grep -c . <<<"$ids")" -eq 1 ]]; then
      printf '%s\n' "$ids"
      return 0
    fi
  fi
  printf '%s\n' "$iface"
}

# static_yaml KENNUNG ADRESSE GATEWAY DNS-LISTE
static_yaml() {
  printf '%s\n' "# PDH: feste IPv4-Adresse (pdh-static-ip.sh) – entfernen und „netplan apply“ für DHCP
network:
  version: 2
  ethernets:
    $1:
      dhcp4: false
      addresses: [$2]
      routes:
        - to: default
          via: $3
      nameservers:
        addresses: [$(sed 's/,/, /g' <<<"$4")]"
}

# ── Dialog an der Konsole (wie bisher) ─────────────────────────────────────────
dialog_apply() {
  (( DRY || EUID == 0 )) || fail "Bitte als root ausführen: sudo bash $0 …"
  if (( ! DRY )) && [[ "$(state_get state)" == pending ]]; then
    fail "Eine Änderung über die Seite „Netzwerk“ wartet noch auf Bestätigung – bitte dort bestätigen oder 2 Minuten abwarten, nichts geändert."
  fi
  command -v netplan >/dev/null 2>&1 || fail "netplan fehlt – dieses Skript ist für Ubuntu-Server."
  # Schon die vorhandene Konfiguration fehlerhaft? Dann zuerst diese reparieren
  # (sonst sähe der Fehler aus, als käme er von der neuen Datei).
  if (( ! DRY )) && ! err="$(netplan generate 2>&1)"; then
    printf '%s\n' "$err" >&2
    fail "Die vorhandene Netzwerkkonfiguration unter /etc/netplan ist schon fehlerhaft (siehe oben) – bitte zuerst reparieren, nichts geändert."
  fi

  # Jetzige Werte als Vorschlag
  [[ -n "$IFACE" ]] || IFACE="$(ip -4 route show default | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
  [[ -n "$IFACE" ]] || fail "Keine Standardroute gefunden – bitte --if, --ip und --gw angeben."
  ip link show "$IFACE" >/dev/null 2>&1 || fail "Schnittstelle $IFACE gibt es nicht."
  # /32 ist die mitwandernde Adresse von pdh-ha, nicht die Adresse des Servers
  CUR_IP="$(ip -4 -o addr show dev "$IFACE" scope global | awk '$4 !~ /\/32$/ {print $4; exit}')"
  [[ -n "$IP" ]] || IP="$CUR_IP"
  [[ -n "$GW" ]] || GW="$(ip -4 route show default dev "$IFACE" | awk '{print $3; exit}')"
  if [[ -z "$DNS" ]]; then
    DNS="$(resolvectl dns "$IFACE" 2>/dev/null | tr ' ' '\n' | grep -E "$ipv4_re" | paste -sd, - || true)"
    [[ -n "$DNS" ]] || DNS="$GW"
  fi

  [[ "$IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$ ]] || fail "Adresse bitte mit Präfix angeben, z. B. 192.0.2.14/24 (ist: ${IP:-leer})"
  [[ "$GW" =~ $ipv4_re ]] || fail "Ungültiges Gateway: ${GW:-leer}"
  IFS=, read -r -a DNS_LIST <<<"$DNS"
  for d in "${DNS_LIST[@]}"; do [[ "$d" =~ $ipv4_re ]] || fail "Ungültiger DNS-Server: $d"; done

  # Im Verbund kennen sich die Server über ihre Adressen – nicht einfach ändern
  if [[ "$IP" != "$CUR_IP" && -f /etc/pdh/ha/node.json && $FORCE -eq 0 ]]; then
    fail "Dieser Server ist im Ausfallschutz-Verbund unter ${CUR_IP%/*} bekannt. Bitte die jetzige Adresse fest einstellen (ohne --ip) oder mit --force bewusst ändern und den Verbund danach anpassen."
  fi

  ID="$(netplan_id "$IFACE")"
  YAML="$(static_yaml "$ID" "$IP" "$GW" "$DNS")"

  info "Feste Adresse für $IFACE"
  echo "  Adresse:  $IP   (jetzt: ${CUR_IP:-keine})"
  echo "  Gateway:  $GW"
  echo "  DNS:      $DNS"
  echo "  Datei:    $FILE"
  if (( DRY )); then
    echo "$YAML"
    exit 0
  fi
  if (( ! YES )); then
    read -r -p "So übernehmen? [j/N] " a || true
    [[ "${a:-}" =~ ^[jJyY] ]] || fail "Abgebrochen – nichts geändert."
  fi

  restore() {
    rm -f "$FILE" "$CLOUD_FILE"
    if [[ -f "$FILE.vorher" ]]; then mv "$FILE.vorher" "$FILE"; fi
  }
  [[ -f "$FILE" ]] && cp -p "$FILE" "$FILE.vorher"
  umask 077
  printf '%s\n' "$YAML" >"$FILE"
  chmod 600 "$FILE"
  # cloud-init würde die Netzwerkkonfiguration sonst beim nächsten Start neu schreiben
  if [[ -d "$CLOUD_DIR" ]]; then
    printf 'network: {config: disabled}\n' >"$CLOUD_FILE"
  fi
  netplan generate || { restore; fail "netplan meldet einen Fehler – nichts geändert."; }

  if (( YES )); then
    netplan apply
  else
    info "Testlauf: Ist die Verbindung danach noch da, mit ENTER bestätigen – sonst geht es nach 120 s automatisch zurück."
    if ! netplan try --timeout 120; then
      restore
      netplan apply || true
      fail "Nicht bestätigt – zurück auf die vorherige Einstellung."
    fi
  fi
  rm -f "$FILE.vorher"
  ok "Feste Adresse eingestellt: $IP über $IFACE"
  warn "Im Router die Adresse ${IP%/*} aus dem DHCP-Bereich nehmen oder für diesen Server reservieren."
}

# ── Ohne Rückfrage mit Rückfall-Wecker (--auto, --confirm, --restore, --resume, --maint-off) ──
#
# Die Ausgabe geht über systemd-run --pipe an den Update-Agenten. Startet der Agent
# neu, ist die Leitung weg: SIGPIPE ist aus, jede Ausgabe darf scheitern, und die
# Ausgaben der Befehle werden aufgefangen. Kein „set -e“: jeder Schritt prüft selbst,
# ein Abbruch mitten im Ablauf wäre schlimmer als ein gemeldeter Fehler.
#
# Zustandsdatei $STATE_DIR/state, Zeilen „schlüssel=wert“: id, state (pending, confirmed,
# restored, failed), action (fix, dns), by, confirmed_by, started, deadline, applied (1 =
# netplan apply gelungen), had_yaml, had_cloud, bak (keep = Sicherungen nach gescheitertem
# Rückfall), msg, maint (0, on-requested, 1), maint_seq (Stand der Wartung, den diese Änderung
# gesetzt hat), maint_off (pending, done, skipped), maint_msg.

say() { printf '%s\n' "$*" 2>/dev/null || true; }
# nfail AUSGANG TEXT: Meldung (eine Zeile, die letzte der Ausgabe) und Ende
nfail() {
  local code="$1"
  shift
  printf '%s\n' "$*" >&2 2>/dev/null || true
  exit "$code"
}
am_root() { (( EUID == 0 )); }
# oneline TEXT: mehrzeilige Ausgabe eines Befehls für eine Meldung
oneline() { local s="${1//[$'\r\n\t']/ }"; s="${s#"${s%%[! ]*}"}"; printf '%s' "${s:0:300}"; }
flush() { sync -- "$@" >/dev/null 2>&1 || true; }

# netplan mit Zeitgrenze: ein hängendes netplan darf weder Anwenden noch Rückfall aufhalten.
# Die vom Skript ignorierten Signale gelten für netplan wieder (sonst wirkte erst das KILL).
np_generate() { ( trap - PIPE HUP INT TERM; exec timeout -k 10 "$NP_GENERATE_TIMEOUT" netplan generate ) 2>&1; }
np_apply() { ( trap - PIPE HUP INT TERM; exec timeout -k 10 "$NP_APPLY_TIMEOUT" netplan apply ) 2>&1; }
# np_get SCHLÜSSEL: nur die Ausgabe (Warnungen von netplan auf stderr fallen weg)
np_get() { ( trap - PIPE HUP INT TERM; exec timeout -k 10 "$NP_GET_TIMEOUT" netplan get "$@" ) 2>/dev/null; }
# np_err AUSGANG AUSGABE: Grund für die Meldung
np_err() {
  if [[ "$1" == 124 || "$1" == 137 ]]; then
    printf 'keine Antwort innerhalb der Zeitgrenze'
  else
    oneline "$2"
  fi
}

# state_get SCHLÜSSEL → Wert aus $STATE_DIR/state ("" ohne Datei oder Schlüssel)
state_get() {
  local f="$STATE_DIR/state"
  [[ -f "$f" ]] || return 0
  awk -v k="$1=" 'index($0, k) == 1 { v = substr($0, length(k) + 1) } END { printf "%s", v }' "$f"
}

# state_write SCHLÜSSEL=WERT … – setzt bzw. ergänzt Schlüssel; atomar über Zwischendatei und mv
state_write() {
  local f="$STATE_DIR/state" tmp kv k line keep
  tmp="$(mktemp "$STATE_DIR/.state.XXXXXX" 2>/dev/null)" || return 1
  {
    if [[ -f "$f" ]]; then
      while IFS= read -r line || [[ -n "$line" ]]; do
        keep=1
        for kv in "$@"; do
          k="${kv%%=*}"
          if [[ "$line" == "$k="* ]]; then keep=0; fi
        done
        if (( keep )); then printf '%s\n' "$line"; fi
      done <"$f"
    fi
    for kv in "$@"; do printf '%s\n' "${kv//[$'\r\n']/ }"; done
  } >"$tmp" 2>/dev/null || { rm -f -- "$tmp"; return 1; }
  chmod 600 -- "$tmp" 2>/dev/null
  flush "$tmp"
  mv -f -- "$tmp" "$f" 2>/dev/null || { rm -f -- "$tmp"; return 1; }
  flush "$STATE_DIR"
}

# Sperre: eine Änderung zur Zeit (Anwenden, Bestätigen, Rückfall, Wiederaufnahme, Wartung aus)
NET_LOCKED=0
net_lock_open() {
  mkdir -p -- "$STATE_DIR" 2>/dev/null
  chmod 700 -- "$STATE_DIR" 2>/dev/null
  [[ -d "$STATE_DIR" ]] || nfail 1 "Ordner $STATE_DIR lässt sich nicht anlegen – es wurde nichts geändert."
  exec 9>>"$STATE_DIR/lock" || nfail 1 "Sperrdatei $STATE_DIR/lock lässt sich nicht öffnen – es wurde nichts geändert."
}
# net_lock [WARTEZEIT]
net_lock() {
  (( NET_LOCKED )) && return 0
  net_lock_open
  flock -w "${1:-120}" 9 || nfail 3 "Eine andere Änderung der Netzwerkeinstellung läuft gerade – bitte gleich erneut versuchen."
  NET_LOCKED=1
}
# net_lock_patient: für den Wecker – versucht die Sperre bis zu RESTORE_WAIT Sekunden, solange die
# Änderung noch wartet; danach holt der Update-Agent den Rückfall nach.
net_lock_patient() {
  local start=$SECONDS
  (( NET_LOCKED )) && return 0
  net_lock_open
  until flock -w 30 9; do
    if [[ "$(state_get state)" != pending ]]; then
      say "Keine Änderung wartet mehr – nichts zurückzunehmen."
      exit 0
    fi
    (( SECONDS - start < RESTORE_WAIT )) || nfail 3 "Sperre seit $RESTORE_WAIT s belegt – der Update-Agent holt den Rückfall nach."
    sleep 1
  done
  NET_LOCKED=1
}

# timer_waiting: Wecker gestellt und noch nicht abgelaufen
timer_waiting() {
  [[ "$(systemctl show -p SubState --value "$TIMER_UNIT.timer" 2>/dev/null)" == waiting ]]
}

# arm_timer SEK: Rückfall-Wecker als eigener systemd-Auftrag (übersteht Neustart von Agent und
# PDH). Er ruft eine Kopie dieses Skripts auf, damit ein Update des Repositorys ihn nicht stört.
ARM_ERR=""
arm_timer() {
  local secs="$1" tmp="$STATE_DIR/.restore.sh.$$" out
  ARM_ERR=""
  if ! [[ "$secs" =~ ^[0-9]+$ ]] || (( secs < 1 )); then secs=1; fi
  if ! { cp -- "${BASH_SOURCE[0]}" "$tmp" && chmod 700 -- "$tmp" && mv -f -- "$tmp" "$STATE_DIR/restore.sh"; } >/dev/null 2>&1; then
    rm -f -- "$tmp" 2>/dev/null
    ARM_ERR="Kopie des Skripts nach $STATE_DIR nicht möglich"
    return 1
  fi
  flush "$STATE_DIR/restore.sh"
  systemctl stop "$TIMER_UNIT.timer" >/dev/null 2>&1
  systemctl reset-failed "$TIMER_UNIT.service" >/dev/null 2>&1
  if ! out="$(systemd-run --unit="$TIMER_UNIT" --on-active="${secs}s" --timer-property=AccuracySec=1s \
    --setenv=PDH_NET_STATE_DIR="$STATE_DIR" --setenv=PDH_NETPLAN_DIR="$NETPLAN_DIR" \
    --setenv=PDH_CLOUD_CFG_DIR="$CLOUD_DIR" --setenv=PDHHA="$PDHHA" --setenv=PDH_NET_ID="$(state_get id)" \
    /bin/bash "$STATE_DIR/restore.sh" --restore 2>&1)"; then
    ARM_ERR="$(oneline "$out")"
    return 1
  fi
}

disarm_timer() {
  systemctl stop "$TIMER_UNIT.timer" >/dev/null 2>&1
  return 0
}

# ── Wartung des Ausfallschutzes ──
#
# Ein- und Ausschalten zählen in pdh-ha den Stand (maint_seq) hoch. Die Änderung merkt sich den
# Stand nach ihrem Einschalten und schaltet nur aus, solange er noch gilt – eine Wartung, die
# inzwischen jemand anders gestellt hat, bleibt unberührt. Gelingt das Ausschalten nicht (z. B.
# während eines Updates im Verbund), bleibt maint_off=pending; --maint-off holt es nach.

# ha_maint_status → „on STAND“ bzw. „off STAND“; Fehler, wenn pdh-ha nicht antwortet oder
# keinen Stand nennt (zu alt).
ha_maint_status() {
  local js m s
  js="$("$PDHHA" status --json 2>/dev/null)" || return 1
  m="$(grep -oE '"maint":(true|false)' <<<"$js" | head -n 1)"
  s="$(grep -oE '"maint_seq":[0-9]+' <<<"$js" | head -n 1)"
  [[ -n "$m" && -n "$s" ]] || return 1
  if [[ "$m" == '"maint":true' ]]; then printf 'on %s\n' "${s#*:}"; else printf 'off %s\n' "${s#*:}"; fi
}

# maint_open: Die Änderung wartet nicht mehr, hat aber eine Wartung eingeschaltet (oder wollte es),
# die noch nicht geklärt ist.
maint_open() {
  local m o
  [[ "$(state_get state)" != pending ]] || return 1
  m="$(state_get maint)"
  o="$(state_get maint_off)"
  [[ "$o" == pending ]] && return 0
  [[ -z "$o" && ( "$m" == 1 || "$m" == on-requested ) ]]
}

# maint_release: eigene Wartung ausschalten (Vergleich des Stands). 0 = erledigt (aus oder nicht
# mehr unsere), 1 = später erneut versuchen. Zustand bestmöglich mitschreiben.
maint_release() {
  local cur on seq want out
  want="$(state_get maint_seq)"
  [[ "$(state_get maint_off)" == pending ]] || state_write maint_off=pending
  if ! cur="$(ha_maint_status)"; then
    state_write maint_msg="Wartung des Ausfallschutzes noch an – pdh-ha antwortet nicht, wird automatisch erneut versucht"
    return 1
  fi
  on="${cur%% *}" seq="${cur#* }"
  if [[ "$on" != on ]]; then
    state_write maint_off=done maint_msg=
    say "Wartung des Ausfallschutzes ist aus."
    return 0
  fi
  if [[ -z "$want" || "$seq" != "$want" ]]; then
    state_write maint_off=skipped maint_msg="Wartung des Ausfallschutzes wurde inzwischen anders gestellt – sie bleibt, wie sie ist"
    say "$(state_get maint_msg)"
    return 0
  fi
  if ! out="$("$PDHHA" maintenance off 2>&1)"; then
    state_write maint_msg="Wartung des Ausfallschutzes noch an ($(oneline "$out")) – wird automatisch ausgeschaltet, sobald möglich"
    return 1
  fi
  state_write maint_off=done maint_msg=
  say "Wartung des Ausfallschutzes ausgeschaltet."
}

# maint_finish: nach Bestätigen oder Rückfall – sofort versuchen, sonst Hinweis
maint_finish() {
  maint_open || return 0
  maint_release || say "$(state_get maint_msg)"
  return 0
}

# ── Dateien ──

# restore_file SICHERUNG HATTE ZIEL: vorige Datei byte-gleich zurück bzw. eigene entfernen
restore_file() {
  local bak="$1" had="$2" dst="$3"
  if [[ "$had" == 1 ]]; then
    [[ -f "$bak" ]] || return 1
    { cp -p -- "$bak" "$dst.pdh-neu" && mv -f -- "$dst.pdh-neu" "$dst"; } >/dev/null 2>&1 || { rm -f -- "$dst.pdh-neu"; return 1; }
  else
    rm -f -- "$dst" 2>/dev/null || return 1
  fi
}

# write_file ZIEL INHALT: Datei (0600) über Zwischendatei
write_file() {
  local dst="$1" content="$2"
  { ( umask 077 && printf '%s\n' "$content" >"$dst.pdh-neu" ) && chmod 600 -- "$dst.pdh-neu" && mv -f -- "$dst.pdh-neu" "$dst"; } >/dev/null 2>&1 \
    || { rm -f -- "$dst.pdh-neu" 2>/dev/null; return 1; }
  flush "$dst"
}

# maint_flag: maint_off für den Endzustand (pending, wenn diese Änderung Wartung eingeschaltet hat)
maint_flag() {
  case "$(state_get maint)" in
    1 | on-requested) printf 'pending' ;;
  esac
}

# ── Abläufe ──

# do_restore [GRUND]: nur aus „pending“; Dateien zurück, netplan apply, Wecker aus, Wartung aus.
# Der Zustand wechselt erst nach den Dateien – bricht der Lauf ab, wiederholen Wecker, --resume
# oder der Update-Agent ihn.
do_restore() {
  local msg="${1:-Nicht bestätigt – vorige Einstellung wiederhergestellt}" st=restored out rc bak=""
  net_lock
  if [[ "$(state_get state)" != pending ]]; then
    say "Keine Änderung wartet – nichts zurückzunehmen."
    maint_finish
    return 0
  fi
  # Wecker einer früheren Änderung (z. B. kurz nach dem Bestätigen gestartet): nichts tun
  if [[ -n "${PDH_NET_ID:-}" && "$PDH_NET_ID" != "$(state_get id)" ]]; then
    say "Wecker einer früheren Änderung – nichts zu tun."
    return 0
  fi
  if ! restore_file "$BAK_YAML" "$(state_get had_yaml)" "$FILE"; then
    st=failed msg="Vorige Datei $FILE nicht wiederherstellbar (Sicherung in $STATE_DIR) – bitte von Hand prüfen"
  fi
  if ! restore_file "$BAK_CLOUD" "$(state_get had_cloud)" "$CLOUD_FILE"; then
    st=failed msg="Vorige Datei $CLOUD_FILE nicht wiederherstellbar (Sicherung in $STATE_DIR) – bitte von Hand prüfen"
  fi
  out="$(np_apply)"
  rc=$?
  if (( rc )); then
    st=failed msg="Vorige Einstellung zurückgelegt, aber netplan apply ist gescheitert ($(np_err "$rc" "$out")) – bitte den Server neu starten"
  fi
  [[ "$st" == restored ]] || bak=keep
  if ! state_write state="$st" msg="$msg" bak="$bak" maint_off="$(maint_flag)"; then
    nfail 1 "Zustand in $STATE_DIR nicht speicherbar – $msg"
  fi
  disarm_timer
  if [[ "$st" == restored ]]; then rm -f -- "$BAK_YAML" "$BAK_CLOUD"; fi
  say "$msg"
  maint_finish
  [[ "$st" == restored ]]
}

# do_confirm: nur aus „pending“, nach gelungenem Anwenden (applied=1), mit passender Kennung
# (--id) und vor Ablauf der Frist; danach Wecker aus, Sicherungen weg, Wartung aus.
do_confirm() {
  local now dl
  net_lock 5
  [[ "$(state_get state)" == pending ]] || nfail 3 "Keine Änderung wartet auf Bestätigung"
  if [[ -n "$CONFIRM_ID" && "$CONFIRM_ID" != "$(state_get id)" ]]; then
    nfail 3 "Diese Änderung wartet nicht mehr auf Bestätigung"
  fi
  [[ "$(state_get applied)" == 1 ]] || nfail 3 "Die Änderung ist noch nicht angewendet – bitte gleich erneut bestätigen"
  now="$(date +%s)"
  dl="$(state_get deadline)"
  if ! [[ "$dl" =~ ^[0-9]+$ ]] || (( now >= dl )); then
    # zu spät: der Rückfall kommt – ist der Wecker weg, sofort
    timer_waiting || arm_timer 1
    nfail 3 "Frist abgelaufen – die vorige Einstellung wird wiederhergestellt"
  fi
  state_write state=confirmed confirmed_by="$BY" msg= maint_off="$(maint_flag)" ||
    nfail 1 "Zustand in $STATE_DIR nicht speicherbar – nicht bestätigt"
  disarm_timer
  rm -f -- "$BAK_YAML" "$BAK_CLOUD"
  say "Einstellung behalten.${BY:+ Bestätigt von $BY.}"
  maint_finish
  return 0
}

# do_resume: beim Start des Update-Agenten. Frist vorbei → Rückfall; sonst Wecker mit der
# Restzeit stellen, falls er fehlt (nach einem Neustart des Servers gibt es ihn nicht mehr).
# Ohne wartende Änderung: offene Wartung einer früheren Änderung ausschalten.
do_resume() {
  local now dl
  net_lock
  if [[ "$(state_get state)" != pending ]]; then
    say "Keine Änderung wartet."
    maint_finish
    return 0
  fi
  now="$(date +%s)"
  dl="$(state_get deadline)"
  if ! [[ "$dl" =~ ^[0-9]+$ ]] || (( now >= dl )); then
    do_restore
    return
  fi
  if timer_waiting; then
    say "Rückfall-Wecker läuft (noch $((dl - now)) s)."
    return 0
  fi
  if ! arm_timer "$((dl - now))"; then
    do_restore "Rückfall-Wecker ließ sich nicht neu stellen ($ARM_ERR) – vorige Einstellung wiederhergestellt"
    return
  fi
  say "Rückfall-Wecker neu gestellt (noch $((dl - now)) s)."
}

# do_maint_off: nur die offene Wartung einer früheren Änderung (Update-Agent, alle 30 s).
# Ausgang 1: noch an, später erneut versuchen.
do_maint_off() {
  net_lock
  if [[ "$(state_get state)" == pending ]]; then
    say "Eine Änderung wartet – die Wartung bleibt bis zum Bestätigen bzw. Rückfall an."
    return 0
  fi
  if ! maint_open; then
    say "Keine Wartung auszuschalten."
    return 0
  fi
  maint_release && return 0
  say "$(state_get maint_msg)"
  return 1
}

# auto_apply: Reihenfolge bestimmt die Sicherheit – erst Sicherungen, Wartung, Zustand und
# Wecker, dann schreiben und anwenden; jeder Fehler danach nimmt alles zurück.
auto_apply() {
  local out rc cur_ips cur_gw d ids yaml action now dl id cur seq="" had_yaml=0 had_cloud=0 maint=0
  local -a dns_list base
  command -v netplan >/dev/null 2>&1 || nfail 1 "netplan fehlt – dieses Skript ist für Ubuntu-Server."
  net_lock
  # Zuerst, was der Zustand verbietet – vor jedem Aufruf von netplan unter der Sperre
  [[ "$(state_get state)" != pending ]] || nfail 3 "Eine Änderung wartet noch auf Bestätigung"
  if maint_open; then
    nfail 1 "Die Wartung des Ausfallschutzes aus der vorigen Änderung ist noch an – sie wird automatisch ausgeschaltet, bitte danach erneut versuchen. Es wurde nichts geändert."
  fi
  # Sicherungen nach gescheitertem Rückfall sind die einzige Kopie der vorigen Einstellung
  if [[ "$(state_get bak)" == keep ]] && [[ -e "$BAK_YAML" || -e "$BAK_CLOUD" ]]; then
    nfail 1 "Eine frühere Änderung ließ sich nicht vollständig zurücknehmen – bitte zuerst von Hand prüfen und die Sicherungen in $STATE_DIR danach löschen, es wurde nichts geändert."
  fi
  out="$(np_generate)"
  rc=$?
  if (( rc )); then
    nfail 1 "Die vorhandene Netzwerkkonfiguration unter $NETPLAN_DIR ist schon fehlerhaft ($(np_err "$rc" "$out")) – bitte zuerst reparieren, es wurde nichts geändert."
  fi

  # Jetzige Werte – angegebene müssen genau die jetzigen sein (nie eine andere Adresse)
  [[ -n "$IFACE" ]] || IFACE="$(ip -4 route show default | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
  [[ -n "$IFACE" ]] || nfail 1 "Keine Standardroute gefunden – es wurde nichts geändert."
  ip link show "$IFACE" >/dev/null 2>&1 || nfail 1 "Schnittstelle $IFACE gibt es nicht – es wurde nichts geändert."
  # /32 ist die mitwandernde Adresse von pdh-ha, nicht die Adresse des Servers
  cur_ips="$(ip -4 -o addr show dev "$IFACE" scope global | awk '$4 !~ /\/32$/ {print $4}')"
  grep -qxF -- "$IP" <<<"$cur_ips" || nfail 2 "Eine andere Adresse (--ip $IP) ist mit --auto nicht erlaubt – nur eine jetzige Adresse von $IFACE."
  cur_gw="$(ip -4 route show default dev "$IFACE" | awk '{print $3; exit}')"
  if [[ -n "$GW" ]]; then
    [[ "$GW" == "$cur_gw" ]] || nfail 2 "Ein anderes Gateway (--gw $GW) ist mit --auto nicht erlaubt – jetzt: ${cur_gw:-keins}."
  else
    GW="$cur_gw"
  fi
  [[ "$IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$ ]] || nfail 1 "Keine jetzige IPv4-Adresse auf $IFACE erkannt (${IP:-leer}) – es wurde nichts geändert."
  [[ "$GW" =~ $ipv4_re ]] || nfail 1 "Kein jetziges Gateway auf $IFACE erkannt (${GW:-leer}) – es wurde nichts geändert."
  if [[ -n "$DNS" ]]; then
    action=dns
    IFS=, read -r -a dns_list <<<"$DNS"
    (( ${#dns_list[@]} >= 1 && ${#dns_list[@]} <= 3 )) || nfail 2 "Bitte ein bis drei DNS-Server angeben."
  else
    action=fix
    DNS="$(resolvectl dns "$IFACE" 2>/dev/null | tr ' ' '\n' | grep -E "$ipv4_re" | paste -sd, -)"
    [[ -n "$DNS" ]] || DNS="$GW"
    IFS=, read -r -a dns_list <<<"$DNS"
  fi
  for d in "${dns_list[@]}"; do [[ "$d" =~ $ipv4_re ]] || nfail 2 "Ungültiger DNS-Server: $d"; done
  # Nur eine Karte, die netplan unter genau diesem Namen als „ethernets“ führt – keine Brücke,
  # kein Bond, kein Raten einer anderen Kennung.
  out="$(np_get ethernets)"
  rc=$?
  if [[ "$rc" == 124 || "$rc" == 137 ]]; then
    nfail 1 "netplan get antwortet nicht innerhalb der Zeitgrenze – es wurde nichts geändert."
  fi
  ids="$(grep -E '^[^ #][^:]*:' <<<"$out" | sed 's/:.*//')"
  grep -qxF -- "$IFACE" <<<"$ids" ||
    nfail 1 "Netzwerkkarte $IFACE steht in netplan nicht unter „ethernets“ (Brücke, Bond oder andere Kennung) – bitte von Hand einstellen, es wurde nichts geändert."
  yaml="$(static_yaml "$IFACE" "$IP" "$GW" "$DNS")"

  if (( HA_MAINT )); then
    cur="$(ha_maint_status)" || nfail 1 "Ausfallschutz (pdh-ha) antwortet nicht oder nennt keinen Wartungsstand – es wurde nichts geändert."
    [[ "${cur%% *}" == off ]] || nfail 1 "Die Wartung des Ausfallschutzes ist schon an – bitte danach erneut versuchen, es wurde nichts geändert."
    seq=$(( ${cur#* } + 1 )) # Stand nach dem eigenen Einschalten
  fi

  # Sicherungen der eigenen Dateien (falls vorhanden) – genau das kommt beim Rückfall zurück
  rm -f -- "$BAK_YAML" "$BAK_CLOUD"
  if [[ -f "$FILE" ]]; then
    cp -p -- "$FILE" "$BAK_YAML" 2>/dev/null || nfail 1 "Sicherung von $FILE nicht möglich – es wurde nichts geändert."
    had_yaml=1
  fi
  if [[ -f "$CLOUD_FILE" ]]; then
    if ! cp -p -- "$CLOUD_FILE" "$BAK_CLOUD" 2>/dev/null; then
      rm -f -- "$BAK_YAML"
      nfail 1 "Sicherung von $CLOUD_FILE nicht möglich – es wurde nichts geändert."
    fi
    had_cloud=1
  fi
  flush "$BAK_YAML" "$BAK_CLOUD"

  now="$(date +%s)"
  id="$(date +%Y%m%d%H%M%S)-$$"
  base=(id="$id" action="$action" by="$BY" started="$now" had_yaml="$had_yaml" had_cloud="$had_cloud"
    applied= bak= confirmed_by= maint_off= maint_msg=)

  if (( HA_MAINT )); then
    # Vormerkung vor dem Einschalten: stirbt das Skript danach, findet --maint-off die Wartung.
    if ! state_write "${base[@]}" state=failed deadline= maint=on-requested maint_seq="$seq" \
      msg="Abgebrochen beim Einschalten der Wartung des Ausfallschutzes – es wurde nichts geändert"; then
      rm -f -- "$BAK_YAML" "$BAK_CLOUD"
      nfail 1 "Zustand in $STATE_DIR nicht speicherbar – es wurde nichts geändert."
    fi
    if ! out="$("$PDHHA" maintenance on 2>&1)"; then
      rm -f -- "$BAK_YAML" "$BAK_CLOUD"
      out="Wartung des Ausfallschutzes ließ sich nicht einschalten ($(oneline "$out")) – es wurde nichts geändert"
      state_write msg="$out"
      maint_finish # falls sie doch an ist: nur die eigene ausschalten
      nfail 1 "$out"
    fi
    maint=1
    if cur="$(ha_maint_status)" && [[ "${cur%% *}" == on ]]; then seq="${cur#* }"; fi
  fi
  dl=$(( $(date +%s) + ROLLBACK ))
  if ! state_write "${base[@]}" state=pending deadline="$dl" maint="$maint" maint_seq="$seq" msg=; then
    rm -f -- "$BAK_YAML" "$BAK_CLOUD"
    maint_finish
    nfail 1 "Zustand in $STATE_DIR nicht speicherbar – es wurde nichts geändert."
  fi
  if ! arm_timer "$ROLLBACK"; then
    out="Rückfall-Wecker ließ sich nicht stellen ($ARM_ERR) – es wurde nichts geändert"
    state_write state=failed msg="$out" maint_off="$(maint_flag)"
    rm -f -- "$BAK_YAML" "$BAK_CLOUD"
    maint_finish
    nfail 1 "$out"
  fi

  # Ab hier ist der Wecker gestellt: auch ein Abbruch des Skripts endet im Rückfall.
  if ! write_file "$FILE" "$yaml"; then
    do_restore "$FILE ließ sich nicht schreiben – vorige Einstellung wiederhergestellt"
    nfail 1 "$(state_get msg)"
  fi
  # cloud-init würde die Netzwerkkonfiguration sonst beim nächsten Start neu schreiben
  if [[ -d "$CLOUD_DIR" ]] && ! write_file "$CLOUD_FILE" 'network: {config: disabled}'; then
    do_restore "$CLOUD_FILE ließ sich nicht schreiben – vorige Einstellung wiederhergestellt"
    nfail 1 "$(state_get msg)"
  fi
  out="$(np_generate)"
  rc=$?
  if (( rc )); then
    do_restore "netplan meldet einen Fehler ($(np_err "$rc" "$out")) – vorige Einstellung wiederhergestellt"
    nfail 1 "$(state_get msg)"
  fi
  out="$(np_apply)"
  rc=$?
  if (( rc )); then
    do_restore "netplan apply ist gescheitert ($(np_err "$rc" "$out")) – vorige Einstellung wiederhergestellt"
    nfail 1 "$(state_get msg)"
  fi
  # Erst jetzt lässt sich bestätigen
  state_write applied=1 || nfail 1 "Angewendet, aber Zustand nicht speicherbar – ohne Bestätigung kommt die vorige Einstellung zurück."
  say "Feste Adresse eingestellt: $IP über $IFACE, Gateway $GW, DNS $DNS."
  say "Bitte innerhalb von $ROLLBACK s bestätigen – sonst kommt die vorige Einstellung zurück."
}

# net_main: Prüfung der Optionen und Aufruf der Abläufe ohne Rückfrage
net_main() {
  set +e
  trap '' PIPE HUP INT TERM
  local o v
  if (( AUTO )) && [[ -n "$MODE" ]]; then nfail 2 "--auto und --$MODE nicht zusammen angeben."; fi
  if [[ -n "$CONFIRM_ID" && "$MODE" != confirm ]]; then nfail 2 "--id nur mit --confirm."; fi
  if [[ -n "$CONFIRM_ID" && ! "$CONFIRM_ID" =~ ^[0-9A-Za-z-]{1,64}$ ]]; then nfail 2 "Ungültige Kennung der Änderung."; fi
  if (( AUTO )); then
    for o in FORCE:--force YES:--yes DRY:--dry-run; do
      v="${o%%:*}"
      if (( ${!v} )); then nfail 2 "${o#*:} ist mit --auto nicht erlaubt."; fi
    done
    if ! [[ "$ROLLBACK" =~ ^[0-9]+$ ]] || (( ROLLBACK < 10 || ROLLBACK > 3600 )); then
      nfail 2 "--rollback: Frist in Sekunden, 10 bis 3600 (ist: ${ROLLBACK:-leer})."
    fi
    [[ -n "$IP" ]] || nfail 2 "--auto braucht --ip mit der jetzigen Adresse."
    if [[ -n "$IFACE" && ! "$IFACE" =~ ^[A-Za-z0-9_.@:-]{1,15}$ ]]; then nfail 2 "Ungültige Netzwerkkarte: $IFACE"; fi
  else
    for o in "$IFACE:--if" "$IP:--ip" "$GW:--gw" "$DNS:--dns" "$ROLLBACK_SET:--rollback"; do
      if [[ -n "${o%:*}" ]]; then nfail 2 "${o##*:} ist mit --$MODE nicht erlaubt."; fi
    done
    for o in FORCE:--force YES:--yes DRY:--dry-run HA_MAINT:--ha-maint; do
      v="${o%%:*}"
      if (( ${!v} )); then nfail 2 "${o#*:} ist mit --$MODE nicht erlaubt."; fi
    done
  fi
  # Zeitgrenzen nur als Zahl (Umgebung für Tests)
  [[ "$NP_GENERATE_TIMEOUT" =~ ^[0-9]+$ ]] || NP_GENERATE_TIMEOUT=60
  [[ "$NP_APPLY_TIMEOUT" =~ ^[0-9]+$ ]] || NP_APPLY_TIMEOUT=90
  [[ "$NP_GET_TIMEOUT" =~ ^[0-9]+$ ]] || NP_GET_TIMEOUT=30
  [[ "$RESTORE_WAIT" =~ ^[0-9]+$ ]] || RESTORE_WAIT=600
  am_root || nfail 1 "Bitte als root ausführen: sudo bash $0 …"
  case "$MODE" in
    confirm) do_confirm ;;
    restore)
      net_lock_patient
      do_restore
      ;;
    resume) do_resume ;;
    maint-off) do_maint_off ;;
    *) auto_apply ;;
  esac
  exit $?
}

main() {
  IFACE="" IP="" GW="" DNS="" DRY=0 YES=0 FORCE=0
  AUTO=0 ROLLBACK=120 ROLLBACK_SET="" HA_MAINT=0 BY="" MODE="" NEWOPT="" CONFIRM_ID=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --if) IFACE="${2:-}"; shift 2 ;;
      --ip) IP="${2:-}"; shift 2 ;;
      --gw) GW="${2:-}"; shift 2 ;;
      --dns) DNS="${2:-}"; shift 2 ;;
      --dry-run) DRY=1; shift ;;
      --yes) YES=1; shift ;;
      --force) FORCE=1; shift ;;
      --auto) AUTO=1; shift ;;
      --rollback) ROLLBACK="${2:-}"; ROLLBACK_SET=1; NEWOPT="--rollback"; shift 2 ;;
      --ha-maint) HA_MAINT=1; NEWOPT="--ha-maint"; shift ;;
      --by) BY="${2:-}"; NEWOPT="--by"; shift 2 ;;
      --id) CONFIRM_ID="${2:-}"; NEWOPT="--id"; shift 2 ;;
      --confirm|--restore|--resume|--maint-off)
        if [[ -n "$MODE" && "$MODE" != "${1#--}" ]]; then
          usage; printf 'Nur eine von --confirm, --restore, --resume, --maint-off angeben.\n' >&2; exit 2
        fi
        MODE="${1#--}"; shift ;;
      -h|--help) usage; exit 0 ;;
      *) usage; fail "Unbekannte Option: $1" ;;
    esac
  done
  # wer: ohne Steuerzeichen und „=“, höchstens 80 Zeichen (steht in der Zustandsdatei)
  BY="${BY//[[:cntrl:]=]/}"
  BY="${BY:0:80}"
  if (( AUTO )) || [[ -n "$MODE" ]]; then
    net_main
  fi
  if [[ -n "$NEWOPT" ]]; then
    usage
    printf '%s nur mit --auto, --confirm, --restore, --resume oder --maint-off.\n' "$NEWOPT" >&2
    exit 2
  fi
  dialog_apply
}

# Nur beim Aufruf ausführen, nicht beim Einlesen mit source (Tests in
# cmd/update-agent/net_script_test.go rufen main mit Attrappen der Systembefehle).
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
