#!/usr/bin/env bash
# Dienst „wireguard“ (compose.yaml): wendet die vom PDH erzeugte WireGuard-
# Konfiguration an (internal/web/vpn_conf.go) und meldet den Zustand zurück.
#
#   wg0.conf       Hub für Geräte und Standort-Gateways (# pdh-disabled = aus)
#   wg0.routes     Netze hinter den Gateways – Änderung: Hub neu starten,
#                  sonst nur „wg syncconf“ (bestehende Verbindungen bleiben)
#   pdhoN.conf     ausgehende Tunnel (PDH als Client, mit NAT)
#   forward.allow  „Quelle Ziel“: was eingewählte Geräte erreichen dürfen
#
# Zurück: status (ohne Schlüssel), heartbeat (Unix-Zeit), error (letzter Fehler).
set -uo pipefail

DIR="${PDH_WIREGUARD_DIR:-/pdh-config/wireguard}"
RUN=/run/pdh-wg
RETRY=60 # Sekunden bis zum nächsten Versuch nach einem Fehler

mkdir -p "$DIR" "$RUN" /etc/wireguard
chown "${PDH_UID:-10001}:${PDH_GID:-10001}" "$DIR"
chmod 700 "$DIR" /etc/wireguard

log() { echo "$(date '+%F %T') $*"; }
failed=0
fail() {
	failed=1
	log "FEHLER: $*"
	printf '%s\n' "$*" >"$DIR/.error.tmp" && chmod 644 "$DIR/.error.tmp" && mv "$DIR/.error.tmp" "$DIR/error"
}
sum() { if [ -f "$1" ]; then md5sum <"$1" | cut -d' ' -f1; else echo none; fi; }
is_up() { ip link show "$1" >/dev/null 2>&1; }
down() { if is_up "$1"; then wg-quick down "$1" >/dev/null 2>&1; fi; return 0; }
disabled() { [ ! -f "$DIR/wg0.conf" ] || grep -q '^# pdh-disabled' "$DIR/wg0.conf"; }

up() {
	local out
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
		down wg0
		rm -f /etc/wireguard/wg0.conf "$RUN/wg0.routes"
		return
	fi
	install -m 600 "$DIR/wg0.conf" /etc/wireguard/wg0.conf
	if is_up wg0 && [ "$(sum "$DIR/wg0.routes")" = "$(cat "$RUN/wg0.routes" 2>/dev/null)" ]; then
		wg syncconf wg0 <(wg-quick strip wg0) || fail "wg0: Konfiguration nicht übernommen"
		return
	fi
	down wg0
	up wg0 && sum "$DIR/wg0.routes" >"$RUN/wg0.routes"
}

apply_outbound() {
	local f name want
	for f in /etc/wireguard/pdho*.conf; do # entfernte Tunnel abbauen
		[ -e "$f" ] || continue
		name=$(basename "$f" .conf)
		if disabled || [ ! -f "$DIR/$name.conf" ]; then
			down "$name"
			rm -f "$f" "$RUN/$name.sum"
		fi
	done
	disabled && return
	for f in "$DIR"/pdho*.conf; do
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

# Weiterleitung: Geräte nur in die freigegebenen Netze, nicht zueinander;
# in ausgehende Tunnel mit NAT (der entfernte Server kennt das VPN-Netz nicht).
firewall() {
	iptables -N PDH-FWD 2>/dev/null || iptables -F PDH-FWD
	iptables -C FORWARD -j PDH-FWD 2>/dev/null || iptables -I FORWARD -j PDH-FWD
	iptables -P FORWARD DROP
	iptables -A PDH-FWD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	if [ -f "$DIR/forward.allow" ] && ! disabled; then
		while read -r src dst; do
			[ -n "${dst:-}" ] && iptables -A PDH-FWD -i wg0 -s "$src" -d "$dst" -j ACCEPT
		done <"$DIR/forward.allow"
	fi
	iptables -A PDH-FWD -j DROP
	iptables -t nat -N PDH-NAT 2>/dev/null || iptables -t nat -F PDH-NAT
	iptables -t nat -C POSTROUTING -j PDH-NAT 2>/dev/null || iptables -t nat -A POSTROUTING -j PDH-NAT
	iptables -t nat -A PDH-NAT -o pdho+ -j MASQUERADE
}

report() {
	# Gegenstellen: Schnittstelle Schlüssel Endpunkt Handshake empfangen gesendet
	wg show all dump 2>/dev/null | awk 'NF>=9 {print $1, $2, $4, $6, $7, $8}' >"$DIR/.status.tmp"
	chmod 644 "$DIR/.status.tmp" && mv "$DIR/.status.tmp" "$DIR/status"
	date +%s >"$DIR/.heartbeat.tmp" && chmod 644 "$DIR/.heartbeat.tmp" && mv "$DIR/.heartbeat.tmp" "$DIR/heartbeat"
}

shutdown() {
	log "beende"
	for f in /etc/wireguard/*.conf; do
		[ -e "$f" ] && down "$(basename "$f" .conf)"
	done
	exit 0
}
trap shutdown TERM INT

log "wireguard-dienst gestartet, ordner $DIR"
last="" next_retry=0
while true; do
	state=$(cat "$DIR"/wg0.conf "$DIR"/wg0.routes "$DIR"/forward.allow "$DIR"/pdho*.conf 2>/dev/null | md5sum)
	now=$(date +%s)
	if [ "$state" != "$last" ] || { [ "$failed" = 1 ] && [ "$now" -ge "$next_retry" ]; }; then
		failed=0
		apply_hub
		apply_outbound
		firewall || fail "Firewall-Regeln nicht gesetzt"
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
