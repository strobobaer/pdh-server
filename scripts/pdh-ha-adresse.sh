#!/usr/bin/env bash
# PDH – eine bisherige Server-Adresse (z. B. 10.10.0.202) zur „mitwandernden
# Adresse“ des Ausfallschutzes machen. Der Server bekommt dafür eine neue eigene
# Adresse (z. B. 10.10.0.203). Geführt in fünf Schritten, jeder einzeln
# startbar und zurücknehmbar (am Ende steht „Zurücknehmen: …“):
#
#   sudo bash pdh-ha-adresse.sh neu <ip/präfix>
#       1. Neue eigene Adresse dazu (/etc/netplan/60-pdh-ha.yaml), „netplan try“.
#          Die neue Adresse steht vorn: Linux nimmt für ausgehende Verbindungen
#          (Herzschlag, Spiegel) die erste Adresse der Karte.
#   sudo bash pdh-ha-adresse.sh verbund <ip>[,<ip>…]
#       2. Verbund umstellen: „pdh-ha addrs set <dieser Server> … --neustart“
#          (PostgreSQL startet kurz neu). Geht nur am Server mit der CA – sonst
#          wird der Befehl für den ersten Server ausgegeben.
#   sudo bash pdh-ha-adresse.sh test <ip/präfix>
#       3. Mitwandernde Adresse in alle Richtungen testen (pdh-ha vip test).
#   sudo bash pdh-ha-adresse.sh pruefliste <alte-ip>
#       Vor Schritt 4: was die alte Adresse noch benutzt (nur Anzeige).
#   sudo bash pdh-ha-adresse.sh uebergabe <alte-ip/präfix>
#       4. Alte Adresse aus der festen Netzwerkkonfiguration nehmen („netplan
#          try“) und als mitwandernde Adresse einschalten (pdh-ha access).
#          Nicht über SSH auf der alten Adresse starten!
#
# Optionen (überall): --if NAME   Netzwerkkarte (Standard: Standardroute)
#                     --partner IP  Adresse des anderen Servers für die Prüfung
#                                   der Absenderadresse (Standard: aus pdh-ha)
#                     --yes       Rückfragen mit „ja“ beantworten (Skripte)
#
# Netzwerkänderungen brauchen ein Terminal (netplan try). Gesicherte Dateien
# liegen in /var/backups/pdh-ha-adresse/<Zeitstempel>/.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -Eeuo pipefail

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF'
Aufruf: sudo bash pdh-ha-adresse.sh <schritt> [werte] [--if NAME] [--partner IP] [--yes]

Schritte (der Reihe nach, jeder zurücknehmbar):
  pdh-ha-adresse.sh neu <ip/präfix>          neue eigene Adresse dazu (netplan try)
  pdh-ha-adresse.sh verbund <ip>[,<ip>…]     Verbund auf die neue Adresse umstellen
  pdh-ha-adresse.sh test <ip/präfix>         mitwandernde Adresse testen
  pdh-ha-adresse.sh pruefliste <alte-ip>     was die alte Adresse noch benutzt
  pdh-ha-adresse.sh uebergabe <alte-ip/präfix>  alte Adresse wird mitwandernd

Beispiel: neu 10.10.0.203/24 → verbund 10.10.0.203 → test 10.10.0.15/24
          → pruefliste 10.10.0.202 → (per SSH über .203 anmelden)
          → uebergabe 10.10.0.202/24
EOF
}

IFACE="" PARTNER="" YES=0
POS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --if) IFACE="${2:-}"; shift 2 ;;
    --partner) PARTNER="${2:-}"; shift 2 ;;
    --yes) YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) usage >&2; echo "Unbekannte Option: $1" >&2; exit 2 ;;
    *) POS+=("$1"); shift ;;
  esac
done
STEP="${POS[0]:-}"
ARG="${POS[1]:-}"
case "$STEP" in
  neu|verbund|test|pruefliste|uebergabe) ;;
  *) usage >&2; exit 2 ;;
esac
[[ -n "$ARG" ]] || { usage >&2; echo "Schritt „$STEP“ braucht einen Wert." >&2; exit 2; }

(( EUID == 0 )) || fail "Bitte als root ausführen: sudo bash $0 $STEP $ARG"

PDHHA=/usr/local/bin/pdh-ha
BAK_ROOT=/var/backups/pdh-ha-adresse
NEWFILE=/etc/netplan/60-pdh-ha.yaml
ipv4_re='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
cidr_re='^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$'

need_pdhha() { [[ -x "$PDHHA" ]] || fail "pdh-ha fehlt ($PDHHA) – zuerst dem Ausfallschutz beitreten (pdh-ha-join.sh)."; }
ask() { # ask "Frage" – Standard nein
  (( YES )) && return 0
  local a; read -r -p "$1 [j/N] " a || true
  [[ "${a:-}" =~ ^[jJyY] ]]
}
need_netplan() {
  command -v netplan >/dev/null 2>&1 || fail "netplan fehlt – dieses Skript ist für Ubuntu-Server."
  [[ -t 0 && -t 1 ]] || fail "Kein Terminal: „netplan try“ braucht eines (nicht per Skript/ohne -t starten). Nichts geändert."
  local err
  if ! err="$(netplan generate 2>&1)"; then
    printf '%s\n' "$err" >&2
    fail "Die vorhandene Netzwerkkonfiguration ist schon fehlerhaft (siehe oben) – bitte zuerst reparieren, nichts geändert."
  fi
}
pick_iface() {
  [[ -n "$IFACE" ]] || IFACE="$(ip -4 route show default | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
  [[ -n "$IFACE" ]] || fail "Keine Standardroute gefunden – bitte --if NAME angeben."
  ip link show "$IFACE" >/dev/null 2>&1 || fail "Schnittstelle $IFACE gibt es nicht."
}
# Kennung der Schnittstelle in netplan (wie pdh-static-ip.sh)
netplan_id() {
  ID="$IFACE"
  local ids
  if ids="$(netplan get ethernets 2>/dev/null | grep -E '^[^ #][^:]*:' | sed 's/:.*//')"; then
    if ! grep -qx "$IFACE" <<<"$ids" && [[ "$(grep -c . <<<"$ids")" -eq 1 ]]; then ID="$ids"; fi
  fi
}
# Feste (nicht per DHCP erhaltene) globale IPv4-Adressen der Karte, mit Präfix
static_addrs() { ip -4 -o addr show dev "$IFACE" scope global | grep -v ' dynamic ' | awk '{print $4}'; }

# Stand der Absenderadresse zum Partner (Regel R5)
partner_ip() {
  if [[ -n "$PARTNER" ]]; then printf '%s' "$PARTNER"; return; fi
  [[ -x "$PDHHA" ]] || return 0
  "$PDHHA" status --json 2>/dev/null | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
for p in d.get("peers") or []:
    for a in p.get("paths") or []:
        ad=a.get("addr","")
        if ad:
            print(ad.rsplit(":",1)[0].strip("[]")); sys.exit(0)
' 2>/dev/null || true
}
route_src() { ip -4 route get "$1" 2>/dev/null | sed -nE 's/.*[[:space:]]src[[:space:]]+([0-9.]+).*/\1/p' | head -n1; }

# ---- netplan-Dateien bearbeiten (Sicherung + Rücknahme) ----------------------
BAK=""
declare -a SAVED=() CREATED=()
start_backup() { BAK="$BAK_ROOT/$(date +%Y%m%d-%H%M%S)"; install -d -m 0700 "$BAK"; }
save_file() { # Datei sichern (einmal) oder als „neu angelegt“ merken
  local f="$1" s
  for s in "${SAVED[@]:-}" "${CREATED[@]:-}"; do [[ "$s" == "$f" ]] && return 0; done
  if [[ -f "$f" ]]; then cp -p "$f" "$BAK/$(basename "$f")"; SAVED+=("$f"); else CREATED+=("$f"); fi
}
restore_all() {
  local f
  for f in "${SAVED[@]:-}"; do [[ -n "$f" ]] && cp -p "$BAK/$(basename "$f")" "$f"; done
  for f in "${CREATED[@]:-}"; do [[ -n "$f" ]] && rm -f "$f"; done
}
undo_text() { # Befehle zum Zurücknehmen von Hand
  local f
  for f in "${SAVED[@]:-}"; do [[ -n "$f" ]] && echo "  cp -p $BAK/$(basename "$f") $f"; done
  for f in "${CREATED[@]:-}"; do [[ -n "$f" ]] && echo "  rm -f $f"; done
  echo "  netplan try --timeout 120"
}
# Python-Helfer: find IP DATEI… (Dateien mit dieser Adresse) | remove IP DATEI
npy() {
  python3 - "$@" <<'PY'
import sys, os
try:
    import yaml
except ImportError:
    sys.stderr.write("python3-yaml fehlt (apt-get install python3-yaml)\n"); sys.exit(4)
KINDS = ("ethernets", "bonds", "bridges", "vlans", "wifis", "tunnels")
def ipof(e):
    if isinstance(e, dict):
        e = next(iter(e), "")
    return str(e).split("/")[0]
def nodes(doc):
    net = (doc or {}).get("network") or {}
    for k in KINDS:
        for name, n in (net.get(k) or {}).items():
            if isinstance(n, dict):
                yield n
op, ip = sys.argv[1], sys.argv[2]
if op == "find":
    for f in sys.argv[3:]:
        try:
            doc = yaml.safe_load(open(f))
        except Exception:
            continue
        if any(ipof(e) == ip for n in nodes(doc) for e in (n.get("addresses") or [])):
            print(f)
elif op == "remove":
    f = sys.argv[3]
    doc = yaml.safe_load(open(f))
    for n in nodes(doc):
        if n.get("addresses"):
            n["addresses"] = [e for e in n["addresses"] if ipof(e) != ip]
            if not n["addresses"]:
                del n["addresses"]
    mode = os.stat(f).st_mode & 0o777
    with open(f, "w") as o:
        yaml.safe_dump(doc, o, default_flow_style=False, sort_keys=False, allow_unicode=True)
    os.chmod(f, mode)
PY
}
netplan_files() { compgen -G "/etc/netplan/*.yaml" || true; }

# netplan try mit Rücknahme bei Nicht-Bestätigung
apply_try() {
  if ! netplan generate; then
    restore_all
    fail "netplan meldet einen Fehler – alles zurückgestellt, nichts geändert."
  fi
  info "Testlauf: Ist die Verbindung danach noch da, mit ENTER bestätigen – sonst geht es nach 120 s automatisch zurück."
  if ! netplan try --timeout 120; then
    restore_all
    netplan apply || true
    fail "Nicht bestätigt – zurück auf die vorherige Einstellung."
  fi
}

# ---- Schritte -----------------------------------------------------------------

step_neu() {
  local new="$ARG" newip="${ARG%/*}" olds=() a f list
  [[ "$new" =~ $cidr_re ]] || fail "Adresse bitte mit Präfix angeben, z. B. 10.10.0.203/24 (ist: $new)"
  need_netplan; pick_iface; netplan_id
  mapfile -t NPFILES < <(netplan_files)
  mapfile -t olds < <(static_addrs)
  for a in "${olds[@]:-}"; do
    [[ "${a%/*}" != "$newip" ]] || fail "$newip ist schon eine Adresse von $IFACE – nichts zu tun."
  done
  [[ ${#olds[@]} -gt 0 && -n "${olds[0]}" ]] || warn "Die Karte hat keine feste Adresse (DHCP?) – die neue wird zusätzlich eingetragen."
  if command -v arping >/dev/null 2>&1; then
    info "Prüfe, ob $newip frei ist"
    arping -D -q -c 2 -w 4 -I "$IFACE" "$newip" || fail "$newip wird im Netz schon benutzt – andere Adresse wählen."
  else
    warn "arping fehlt (pdh-ha-join.sh --update …) – Freiheit der Adresse nicht geprüft."
  fi

  info "Neue eigene Adresse $new für $IFACE (Datei $NEWFILE)"
  start_backup
  save_file "$NEWFILE"
  # Alte feste Adressen aus anderen Dateien nach vorn/in die neue Datei verschieben,
  # damit die neue Adresse in der Liste an erster Stelle steht (Regel R5).
  for a in "${olds[@]:-}"; do
    [[ -n "$a" ]] || continue
    while read -r f; do
      [[ -n "$f" && "$f" != "$NEWFILE" ]] || continue
      save_file "$f"
      npy remove "${a%/*}" "$f" || { restore_all; fail "Konnte $f nicht bearbeiten – nichts geändert."; }
      echo "  $a aus $f nach $NEWFILE verschoben (Sicherung: $BAK/$(basename "$f"))"
    done < <(npy find "${a%/*}" "${NPFILES[@]}")
  done
  list="$new"
  for a in "${olds[@]:-}"; do [[ -n "$a" ]] && list="$list, $a"; done
  umask 077
  cat >"$NEWFILE" <<EOF
# PDH Ausfallschutz (pdh-ha-adresse.sh): eigene Adressen dieses Servers.
# Die neue Adresse steht vorn – sie ist Absender für Herzschlag und Spiegel.
network:
  version: 2
  ethernets:
    $ID:
      addresses: [$list]
EOF
  chmod 600 "$NEWFILE"
  echo "  Adressen: $list"
  ask "So übernehmen?" || { restore_all; fail "Abgebrochen – nichts geändert."; }
  apply_try
  ok "Neue Adresse $new aktiv (alte bleibt)."

  local p src
  p="$(partner_ip)"
  if [[ -n "$p" ]]; then
    src="$(route_src "$p")"
    if [[ "$src" == "$newip" ]]; then ok "Absenderadresse zum Partner $p: $src (richtig)"
    else
      warn "Absenderadresse zum Partner $p ist $src, nicht $newip."
      warn "Nach dem Umstellen im Verbund würde der Partner den Herzschlag/Spiegel von $src ablehnen."
      warn "Die Reihenfolge der Adressen gilt für schon vorhandene Adressen erst nach einem Neustart des Servers (oder Netzwerkdienstes) –"
      warn "„verbund“ erst ausführen, wenn „ip route get $p“ als src $newip zeigt. Oder jetzt zurücknehmen (unten)."
    fi
  else
    warn "Partner unbekannt – Absenderadresse nicht geprüft. Danach selbst prüfen: ip route get <Partner-IP>  (src muss $newip sein; --partner IP angeben)."
  fi
  echo
  echo "Zurücknehmen:"
  undo_text
  echo "  (Sicherung in $BAK)"
  echo "Weiter mit: sudo bash $0 verbund $newip"
}

step_verbund() {
  need_pdhha; pick_iface
  local addrs="$ARG" ip j self ca p src cur="" a first
  IFS=, read -r -a _l <<<"$addrs"
  for ip in "${_l[@]}"; do [[ "$ip" =~ $ipv4_re ]] || fail "Ungültige Adresse: $ip (nur IP, ohne /Präfix)"; done
  j="$("$PDHHA" status --json)" || fail "pdh-ha antwortet nicht (läuft der Dienst?)"
  self="$(python3 -c 'import json,sys;print(json.load(sys.stdin)["self"]["id"])' <<<"$j")" || fail "Eigene Kennung nicht lesbar."
  ca="$(python3 -c 'import json,sys;print("ja" if json.load(sys.stdin).get("ca_holder") else "nein")' <<<"$j")"
  [[ -n "$self" ]] || fail "Eigene Kennung nicht lesbar."
  for ip in "${_l[@]}"; do
    ip -4 -o addr show dev "$IFACE" | awk '{print $4}' | grep -q "^$ip/" || fail "$ip liegt nicht auf $IFACE – zuerst Schritt „neu“."
  done
  # bisherige Adressen für die Rücknahme
  while read -r a; do
    local in=0; for ip in "${_l[@]}"; do [[ "${a%/*}" == "$ip" ]] && in=1; done
    (( in )) || cur="${cur:+$cur,}${a%/*}"
  done < <(static_addrs)

  p="$(partner_ip)"
  if [[ -n "$p" ]]; then
    src="$(route_src "$p")"
    first="${_l[0]}"
    if [[ "$src" != "$first" ]]; then
      warn "Zum Partner $p ginge die Verbindung von ${src:-?} aus, nicht von $first."
      warn "Der Partner erlaubt nach der Umstellung nur noch die neue Adresse – Herzschlag und Spiegel bräche ab."
      ask "Trotzdem fortfahren?" || fail "Abgebrochen – nichts geändert. Erst die Reihenfolge der Adressen in Ordnung bringen (Neustart nach Schritt „neu“)."
    fi
  fi

  local cmd="$PDHHA addrs set $self $addrs --neustart"
  if [[ "$ca" == "ja" ]]; then
    info "Dieser Server ($self) hält die CA: Verbund-Adressen auf $addrs umstellen"
    warn "PostgreSQL startet dabei kurz neu (Verbindungen brechen für Sekunden ab)."
    ask "Jetzt ausführen?" || fail "Abgebrochen – nichts geändert."
    $cmd
    ok "Verbund-Adressen von $self: $addrs"
    echo
    echo "Zurücknehmen:"
    echo "  $PDHHA addrs set $self ${cur:-<bisherige-adressen>} --neustart"
  else
    info "Dieser Server hält nicht die CA – die Umstellung geht nur am ersten Server."
    echo "am ersten Server ausführen: $cmd"
    echo
    echo "Zurücknehmen (ebenfalls am ersten Server):"
    echo "  $PDHHA addrs set $self ${cur:-<bisherige-adressen>} --neustart"
  fi
  echo "Weiter mit: sudo bash $0 test <freie-testadresse/präfix>"
}

step_test() {
  need_pdhha
  [[ "$ARG" =~ $cidr_re ]] || fail "Adresse bitte mit Präfix angeben, z. B. 10.10.0.15/24"
  info "Teste mitwandernde Adresse $ARG"
  local rc=0
  "$PDHHA" vip test --adresse "$ARG" || rc=$?
  echo
  echo "Zurücknehmen: nichts nötig – der Test ändert nichts dauerhaft."
  if (( rc != 0 )); then fail "Test fehlgeschlagen (Ausgang $rc, siehe oben)."; fi
  ok "Test bestanden"
  echo "Weiter mit: sudo bash $0 pruefliste <alte-ip>"
}

step_pruefliste() {
  local old="${ARG%/*}"
  [[ "$old" =~ $ipv4_re ]] || fail "Ungültige Adresse: $ARG"
  info "Fundstellen der alten Adresse $old (nur Anzeige)"
  echo "-- Offene Verbindungen (ss -tn):"
  ss -tn 2>/dev/null | grep -F "$old" || echo "  keine"
  echo "-- /etc/fstab:"
  grep -nF "$old" /etc/fstab 2>/dev/null || echo "  keine"
  echo "-- /etc/samba/smb.conf (Freigabe pdhnas):"
  if [[ -f /etc/samba/smb.conf ]]; then
    grep -nF "$old" /etc/samba/smb.conf || echo "  Adresse nicht eingetragen"
    grep -n -i 'pdhnas' /etc/samba/smb.conf || echo "  keine Freigabe pdhnas"
  else echo "  keine smb.conf"; fi
  echo "-- ufw status:"
  if command -v ufw >/dev/null 2>&1; then ufw status 2>/dev/null | grep -F "$old" || echo "  keine Regel mit $old"; else echo "  ufw fehlt"; fi
  echo
  warn "Alles, was $old als „genau diesen Server“ meint, muss auf die neue Adresse:"
  echo "  - SSH-Zugänge (Lesezeichen, ~/.ssh/config, Schlüssel-Einträge) auf die neue Adresse"
  echo "  - Überwachung und Firewall-Freigaben nur für $old"
  echo "  - die Netzlaufwerk-Freigabe pdhnas: Sicherungsziel darf nicht mitwandern!"
  echo
  echo "Zurücknehmen: nichts nötig – es wurde nur angezeigt."
  echo "Weiter mit (per SSH über die neue Adresse angemeldet): sudo bash $0 uebergabe $old/<präfix>"
}

step_uebergabe() {
  local oldc="$ARG" old="${ARG%/*}" srv f files=() newcnt
  [[ "$oldc" =~ $cidr_re ]] || fail "Adresse bitte mit Präfix angeben, z. B. 10.10.0.202/24"
  srv="$(awk '{print $3}' <<<"${SSH_CONNECTION:-}")"
  if [[ "$srv" == "$old" ]]; then
    fail "Diese SSH-Sitzung läuft über die alte Adresse $old – sie risse ab. Bitte über die neue Adresse anmelden und neu starten. Nichts geändert."
  fi
  need_pdhha; need_netplan; pick_iface
  mapfile -t NPFILES < <(netplan_files)
  mapfile -t files < <(npy find "$old" "${NPFILES[@]}")
  [[ ${#files[@]} -gt 0 && -n "${files[0]}" ]] || fail "$old steht in keiner Datei unter /etc/netplan als feste Adresse (DHCP?) – nichts zu entfernen."
  # Der Server muss danach noch eine eigene Adresse haben
  newcnt="$(netplan get 2>/dev/null | grep -E '^[[:space:]]*-[[:space:]]*.?[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+' | grep -vcF "$old" || true)"
  (( newcnt > 0 )) || fail "Außer $old gibt es keine feste eigene Adresse – der Server stünde ohne Adresse da. Zuerst Schritt „neu“."

  info "Alte Adresse $oldc: aus der festen Netzwerkkonfiguration nehmen, mitwandernd machen"
  warn "Dateien: ${files[*]}"
  warn "Die Adresse ist ab der Bestätigung kurz weg, bis pdh-ha sie übernimmt (wenige Sekunden)."
  ask "So fortfahren?" || fail "Abgebrochen – nichts geändert."
  start_backup
  for f in "${files[@]}"; do
    save_file "$f"
    npy remove "$old" "$f" || { restore_all; fail "Konnte $f nicht bearbeiten – nichts geändert."; }
    echo "  $old aus $f entfernt (Sicherung: $BAK/$(basename "$f"))"
  done
  apply_try
  if ! "$PDHHA" access --vip "$oldc" --vip-on; then
    warn "pdh-ha hat die mitwandernde Adresse nicht eingeschaltet – stelle die Netzwerkkonfiguration zurück."
    restore_all
    netplan apply || true
    fail "Zurückgestellt: $old ist wieder fest eingetragen. Ursache oben (Test bestanden? „pdh-ha vip test“)."
  fi
  ok "$old ist jetzt die mitwandernde Adresse."
  echo
  echo "Zurücknehmen:"
  echo "  $PDHHA access --vip-off"
  undo_text
  echo "  (Sicherung in $BAK)"
}

"step_$STEP"
