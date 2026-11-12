#!/bin/bash
# Platzhalter für Nextcloud im Testlabor (PHP-FPM, Cron, Certbot; kein systemd im
# Container). pdh-ha ruft ihn über PDH_HA_NC_CMD mit start|stop|restart|is-active|
# enable|disable auf, aber nur, wenn es ein occ im Programmordner gibt und
# „Nextcloud mitnehmen“ eingeschaltet ist.
#   /var/lib/pdh-lab/fail-nc-start → start scheitert (Alarm, keine Umschaltung)
#   LAB_HAND=1                     → Start von Hand (zählt nicht als Start durch pdh-ha)
# Das Protokoll /var/lib/pdh-lab/nc.log hält bei jedem Start fest, ob die Datenbank
# schon schreibbar war (Spec Abschnitt 6: erst die Datenbank, dann Nextcloud).
set -uo pipefail
flag=/run/nc-dummy.running
dir=/var/lib/pdh-lab
log=$dir/nc.log

note() { echo "$(date +%H:%M:%S) $*" >>"$log"; }

writable() {
    timeout 3 runuser -u postgres -- psql -XAtq -d postgres -c \
        "SELECT NOT (pg_is_in_recovery() OR current_setting('default_transaction_read_only')::bool)" 2>/dev/null
}

start() {
    if [[ -e $dir/fail-nc-start ]]; then
        note "start scheitert"
        echo "Nextcloud startet nicht (Schalter fail-nc-start)" >&2
        exit 1
    fi
    if [[ ! -f $flag ]]; then
        if [[ -n "${LAB_HAND:-}" ]]; then
            note "start von Hand"
        else
            note "start schreibbar=$(writable)"
        fi
        touch "$flag"
    fi
}

case "${1:-}" in
    start) start ;;
    restart)
        rm -f "$flag"
        start
        ;;
    stop)
        [[ -f $flag ]] && note stop
        rm -f "$flag"
        ;;
    is-active) [[ -f $flag ]] ;;
    enable | disable) note "$1" ;;
    *)
        echo "Aufruf: $0 start|stop|restart|is-active|enable|disable" >&2
        exit 2
        ;;
esac
