#!/bin/bash
# Platzhalter für den Dienst pdh im Testlabor (kein systemd im Container).
# pdh-ha ruft ihn über PDH_HA_SERVICE_CMD mit start|stop|restart|is-active|enable|disable auf.
flag=/run/pdh-dummy.running
case "${1:-}" in
    start | restart) touch "$flag" ;;
    stop) rm -f "$flag" ;;
    is-active) [[ -f "$flag" ]] ;;
    enable | disable) : ;;
    *) echo "Aufruf: $0 start|stop|restart|is-active|enable|disable" >&2; exit 2 ;;
esac
