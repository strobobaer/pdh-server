#!/usr/bin/env bash
# PDH – IPv4-Adresse dieses Servers fest einstellen (statt DHCP).
#
#   sudo bash pdh-static-ip.sh
#       übernimmt die jetzige Adresse, das Gateway und die DNS-Server fest
#       (Vorschlag anzeigen, nachfragen, dann „netplan try“).
#
#   sudo bash pdh-static-ip.sh --ip 10.10.0.14/24 --gw 10.10.0.1 --dns 10.10.0.1,1.1.1.1
#       andere Werte vorgeben.
#
# Optionen:
#   --if NAME     Netzwerkschnittstelle (Standard: die mit der Standardroute)
#   --ip A/P      Adresse mit Präfix, z. B. 10.10.0.14/24 (Standard: jetzige)
#   --gw ADRESSE  Gateway (Standard: jetziges)
#   --dns LISTE   DNS-Server, mit Komma getrennt (Standard: jetzige, sonst Gateway)
#   --dry-run     nur anzeigen, was geschrieben würde
#   --yes         ohne Rückfrage und ohne Testlauf anwenden (netplan apply)
#   --force       Adresse ändern, obwohl der Server im Ausfallschutz-Verbund ist
#
# Sicher über SSH: „netplan try“ nimmt die Änderung nach 120 s ohne Bestätigung
# selbst zurück. IPv6 bleibt unverändert. Die Adresse im Router aus dem
# DHCP-Bereich nehmen oder reservieren, sonst vergibt er sie ein zweites Mal.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -Eeuo pipefail

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }
usage() { sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; }

IFACE="" IP="" GW="" DNS="" DRY=0 YES=0 FORCE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --if) IFACE="${2:-}"; shift 2 ;;
    --ip) IP="${2:-}"; shift 2 ;;
    --gw) GW="${2:-}"; shift 2 ;;
    --dns) DNS="${2:-}"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    --yes) YES=1; shift ;;
    --force) FORCE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; fail "Unbekannte Option: $1" ;;
  esac
done

(( DRY || EUID == 0 )) || fail "Bitte als root ausführen: sudo bash $0 …"
command -v netplan >/dev/null 2>&1 || fail "netplan fehlt – dieses Skript ist für Ubuntu-Server."
# Schon die vorhandene Konfiguration fehlerhaft? Dann zuerst diese reparieren
# (sonst sähe der Fehler aus, als käme er von der neuen Datei).
if (( ! DRY )) && ! err="$(netplan generate 2>&1)"; then
  printf '%s\n' "$err" >&2
  fail "Die vorhandene Netzwerkkonfiguration unter /etc/netplan ist schon fehlerhaft (siehe oben) – bitte zuerst reparieren, nichts geändert."
fi

ipv4_re='^([0-9]{1,3}\.){3}[0-9]{1,3}$'

# Jetzige Werte als Vorschlag
[[ -n "$IFACE" ]] || IFACE="$(ip -4 route show default | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
[[ -n "$IFACE" ]] || fail "Keine Standardroute gefunden – bitte --if, --ip und --gw angeben."
ip link show "$IFACE" >/dev/null 2>&1 || fail "Schnittstelle $IFACE gibt es nicht."
CUR_IP="$(ip -4 -o addr show dev "$IFACE" scope global | awk '{print $4; exit}')"
[[ -n "$IP" ]] || IP="$CUR_IP"
[[ -n "$GW" ]] || GW="$(ip -4 route show default dev "$IFACE" | awk '{print $3; exit}')"
if [[ -z "$DNS" ]]; then
  DNS="$(resolvectl dns "$IFACE" 2>/dev/null | tr ' ' '\n' | grep -E "$ipv4_re" | paste -sd, - || true)"
  [[ -n "$DNS" ]] || DNS="$GW"
fi

[[ "$IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$ ]] || fail "Adresse bitte mit Präfix angeben, z. B. 10.10.0.14/24 (ist: ${IP:-leer})"
[[ "$GW" =~ $ipv4_re ]] || fail "Ungültiges Gateway: ${GW:-leer}"
IFS=, read -r -a DNS_LIST <<<"$DNS"
for d in "${DNS_LIST[@]}"; do [[ "$d" =~ $ipv4_re ]] || fail "Ungültiger DNS-Server: $d"; done

# Im Verbund kennen sich die Server über ihre Adressen – nicht einfach ändern
if [[ "$IP" != "$CUR_IP" && -f /etc/pdh/ha/node.json && $FORCE -eq 0 ]]; then
  fail "Dieser Server ist im Ausfallschutz-Verbund unter ${CUR_IP%/*} bekannt. Bitte die jetzige Adresse fest einstellen (ohne --ip) oder mit --force bewusst ändern und den Verbund danach anpassen."
fi

# Netplan führt Dateien zusammen; der Eintrag muss dieselbe Kennung haben wie
# die bisherige Definition der Schnittstelle (z. B. aus 50-cloud-init.yaml).
ID="$IFACE"
if ids="$(netplan get ethernets 2>/dev/null | grep -E '^[^ #][^:]*:' | sed 's/:.*//')"; then
  if ! grep -qx "$IFACE" <<<"$ids" && [[ "$(grep -c . <<<"$ids")" -eq 1 ]]; then
    ID="$ids"
  fi
fi

FILE=/etc/netplan/90-pdh-static.yaml
YAML="# PDH: feste IPv4-Adresse (pdh-static-ip.sh) – entfernen und „netplan apply“ für DHCP
network:
  version: 2
  ethernets:
    $ID:
      dhcp4: false
      addresses: [$IP]
      routes:
        - to: default
          via: $GW
      nameservers:
        addresses: [$(sed 's/,/, /g' <<<"$DNS")]"

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
  rm -f "$FILE" /etc/cloud/cloud.cfg.d/99-pdh-static-ip.cfg
  if [[ -f "$FILE.vorher" ]]; then mv "$FILE.vorher" "$FILE"; fi
}
[[ -f "$FILE" ]] && cp -p "$FILE" "$FILE.vorher"
umask 077
printf '%s\n' "$YAML" >"$FILE"
chmod 600 "$FILE"
# cloud-init würde die Netzwerkkonfiguration sonst beim nächsten Start neu schreiben
if [[ -d /etc/cloud/cloud.cfg.d ]]; then
  printf 'network: {config: disabled}\n' >/etc/cloud/cloud.cfg.d/99-pdh-static-ip.cfg
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
