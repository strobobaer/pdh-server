#!/usr/bin/env bash
# pdh-wireguard – systemd-Dienst auf dem Host (eingerichtet über Server-Einstellungen
# → VPN → Installationsskript). Wendet die vom PDH erzeugte WireGuard-Konfiguration
# an (internal/web/vpn_conf.go) und meldet den Zustand zurück.
#
#   pdhhub.conf    Hub für Geräte und Standort-Gateways (# pdh-disabled = aus)
#   pdhhub.routes  Netze hinter den Gateways – Änderung: Hub neu starten,
#                  sonst nur „wg syncconf“ (bestehende Verbindungen bleiben)
#   pdhoutN.conf   ausgehende Tunnel (PDH als Client, mit NAT)
#   forward.allow  „Quelle Ziel“: was eingewählte Geräte erreichen dürfen
#
# Zurück: status (ohne Schlüssel), heartbeat (Unix-Zeit), error (letzter Fehler).
# Firewall: eigene Ketten PDH-FWD/PDH-NAT, nur für pdh*-Schnittstellen – Docker
# und das übrige Netz des Hosts bleiben unberührt.
set -uo pipefail

DIR="${PDH_WIREGUARD_DIR:-/srv/pdh-wireguard}"
RUN=/run/pdh-wireguard
RETRY=60 # Sekunden bis zum nächsten Versuch nach einem Fehler

mkdir -p "$DIR" "$RUN" /etc/wireguard
chown "${PDH_UID:-10001}:${PDH_GID:-10001}" "$DIR"
chmod 700 "$DIR" /etc/wireguard
sysctl -q -w net.ipv4.ip_forward=1 || true

log() { echo "$*"; }
failed=0
fail() {
	failed=1
	log "FEHLER: $*"
	printf '%s\n' "$*" >"$DIR/.error.tmp" && chmod 644 "$DIR/.error.tmp" && mv "$DIR/.error.tmp" "$DIR/error"
}
sum() { if [ -f "$1" ]; then md5sum <"$1" | cut -d' ' -f1; else echo none; fi; }
is_up() { ip link show "$1" >/dev/null 2>&1; }
down() { if is_up "$1"; then wg-quick down "$1" >/dev/null 2>&1; fi; return 0; }
disabled() { [ ! -f "$DIR/pdhhub.conf" ] || grep -q '^# pdh-disabled' "$DIR/pdhhub.conf"; }

# Netze aus einer Konfiguration: Address und AllowedIPs (IPv4)
conf_nets() { grep -iE '^\s*(Address|AllowedIPs)\s*=' "$1" | cut -d= -f2 | tr ',' '\n' | tr -d ' ' | grep -E '^[0-9.]+/[0-9]+$'; }

# Überschneidet ein Netz eine vorhandene Route des Hosts (LAN, Docker …)?
route_conflict() {
	local net hit
	for net in "$@"; do
		hit=$({ ip -4 route show table main root "$net"; ip -4 route show table main match "$net"; } 2>/dev/null |
			grep -v '^default' | grep -vE ' dev pdh(hub|out[0-9]+)( |$)' | head -n 1)
		if [ -n "$hit" ]; then
			echo "$net überschneidet das Netz des Servers ($hit)"
			return 0
		fi
	done
	return 1
}

up() { # $1 Schnittstelle
	local out msg
	if msg=$(route_conflict $(conf_nets "/etc/wireguard/$1.conf")); then
		fail "$1 nicht gestartet: $msg"
		return 1
	fi
	if ! out=$(wg-quick up "$1" 2>&1); then
		if grep -qiE 'Unknown device type|Operation not supported|Protocol not supported' <<<"$out"; then
			fail "WireGuard fehlt im Kernel des Servers – Linux ab 5.6 bzw. Paket „wireguard“ installieren"
		else
			fail "$1 lässt sich nicht starten: $(tail -n 1 <<<"$out")"
		fi
		down "$1"
		return 1
	fi
	log "$1 gestartet"
}

apply_hub() {
	if disabled; then
		down pdhhub
		rm -f /etc/wireguard/pdhhub.conf "$RUN/pdhhub.routes"
		return
	fi
	install -m 600 "$DIR/pdhhub.conf" /etc/wireguard/pdhhub.conf
	if is_up pdhhub && [ "$(sum "$DIR/pdhhub.routes")" = "$(cat "$RUN/pdhhub.routes" 2>/dev/null)" ]; then
		wg syncconf pdhhub <(wg-quick strip pdhhub) || fail "pdhhub: Konfiguration nicht übernommen"
		return
	fi
	down pdhhub
	up pdhhub && sum "$DIR/pdhhub.routes" >"$RUN/pdhhub.routes"
}

apply_outbound() {
	local f name want
	for f in /etc/wireguard/pdhout*.conf; do # entfernte Tunnel abbauen
		[ -e "$f" ] || continue
		name=$(basename "$f" .conf)
		if disabled || [ ! -f "$DIR/$name.conf" ]; then
			down "$name"
			rm -f "$f" "$RUN/$name.sum"
		fi
	done
	disabled && return
	for f in "$DIR"/pdhout*.conf; do
		[ -e "$f" ] || continue
		name=$(basename "$f" .conf)
		want=$(sum "$f")
		if is_up "$name" && [ "$want" = "$(cat "$RUN/$name.sum" 2>/dev/null)" ]; then
			continue
		fi
		down "$name"
		install -m 600 "$f" "/etc/wireguard/$name.conf"
		up "$name" && echo "$want" >"$RUN/$name.sum"
	done
}

fw_clear() {
	while iptables -D FORWARD -i pdhhub -j PDH-FWD 2>/dev/null; do :; done
	while iptables -D FORWARD -o pdhhub -j PDH-FWD 2>/dev/null; do :; done
	while iptables -D FORWARD -i pdhout+ -j PDH-FWD 2>/dev/null; do :; done
	while iptables -t nat -D POSTROUTING -j PDH-NAT 2>/dev/null; do :; done
	iptables -F PDH-FWD 2>/dev/null && iptables -X PDH-FWD 2>/dev/null
	iptables -t nat -F PDH-NAT 2>/dev/null && iptables -t nat -X PDH-NAT 2>/dev/null
	return 0
}

# Geräte nur in die freigegebenen Netze, nicht zueinander; in ausgehende Tunnel
# mit NAT (der entfernte Server kennt das VPN-Netz nicht).
firewall() {
	fw_clear
	disabled && return 0
	iptables -N PDH-FWD || return 1
	iptables -A PDH-FWD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	if [ -f "$DIR/forward.allow" ]; then
		while read -r src dst; do
			[ -n "${dst:-}" ] && iptables -A PDH-FWD -i pdhhub -s "$src" -d "$dst" -j ACCEPT
		done <"$DIR/forward.allow"
	fi
	iptables -A PDH-FWD -j DROP
	iptables -I FORWARD 1 -i pdhhub -j PDH-FWD
	iptables -I FORWARD 1 -o pdhhub -j PDH-FWD
	iptables -I FORWARD 1 -i pdhout+ -j PDH-FWD
	iptables -t nat -N PDH-NAT
	iptables -t nat -A PDH-NAT -o pdhout+ -j MASQUERADE
	iptables -t nat -I POSTROUTING 1 -j PDH-NAT
}

report() {
	# Gegenstellen: Schnittstelle Schlüssel Endpunkt Handshake empfangen gesendet
	wg show all dump 2>/dev/null | awk 'NF>=9 && $1 ~ /^pdh(hub|out[0-9]+)$/ {print $1, $2, $4, $6, $7, $8}' >"$DIR/.status.tmp"
	chmod 644 "$DIR/.status.tmp" && mv "$DIR/.status.tmp" "$DIR/status"
	date +%s >"$DIR/.heartbeat.tmp" && chmod 644 "$DIR/.heartbeat.tmp" && mv "$DIR/.heartbeat.tmp" "$DIR/heartbeat"
}

shutdown() {
	log "beende"
	for f in /etc/wireguard/pdhhub.conf /etc/wireguard/pdhout*.conf; do
		[ -e "$f" ] && down "$(basename "$f" .conf)"
	done
	fw_clear
	exit 0
}
trap shutdown TERM INT

log "pdh-wireguard gestartet, Ordner $DIR"
last="" next_retry=0
while true; do
	state=$(cat "$DIR"/pdhhub.conf "$DIR"/pdhhub.routes "$DIR"/forward.allow "$DIR"/pdhout*.conf 2>/dev/null | md5sum)
	now=$(date +%s)
	if [ "$state" != "$last" ] || { [ "$failed" = 1 ] && [ "$now" -ge "$next_retry" ]; }; then
		failed=0
		apply_hub
		apply_outbound
		firewall || fail "Firewall-Regeln nicht gesetzt (iptables)"
		if [ "$failed" = 1 ]; then
			next_retry=$((now + RETRY))
		else
			rm -f "$DIR/error"
		fi
		last=$state
	fi
	report
	sleep 5 &
	wait $!
done
