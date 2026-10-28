#!/bin/bash
# Spiegel-Labor: PDH unter einer Neustart-Schleife (ersetzt systemd
# Restart=always). Beendet sich das PDH (z. B. Exit 3 beim Moduswechsel),
# startet es nach 1 s neu. PID des laufenden PDH in /tmp/pdh.pid.
# LAB_FAKE_HA=1: zusätzlich der Platzhalter für den pdh-ha-Socket (fakeha).
cd /app || exit 1
if [[ "${LAB_FAKE_HA:-}" == 1 ]]; then
    [[ -f /tmp/lab-seen ]] || echo true >/tmp/lab-seen
    /app/fakeha -socket "${PDH_HA_SOCKET:-/run/pdh-ha/api.sock}" -seen /tmp/lab-seen &
fi
while true; do
    /app/pdh &
    pid=$!
    echo "$pid" >/tmp/pdh.pid
    wait "$pid"
    rc=$?
    echo "lab: pdh beendet (exit $rc) – neustart in 1 s"
    sleep 1
done
