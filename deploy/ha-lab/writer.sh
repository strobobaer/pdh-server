#!/bin/bash
# Schreib-Generator des Testlabors: fügt fortlaufend Zeilen in lab.t der gerade
# schreibbaren Datenbank ein und merkt jede bestätigte Zahl in <Datei>.
#   writer.sh <Datei> [<Startzahl>]
# Bestätigt heißt: psql endet ohne Fehler und ohne Warnung. Bricht PostgreSQL
# das Warten auf den synchronen Standby ab, ist die Zeile nur lokal gespeichert
# (Warnung) – sie zählt dann nicht. Hängende Speicherungen beendet timeout,
# ohne dass sie als bestätigt gelten.
set -uo pipefail

acked=$1
n=${2:-1}
nodes=(node1 node2)
cur=0

while :; do
    for i in 0 1; do
        c=${nodes[$(((cur + i) % 2))]}
        if out="$(timeout 8 docker exec "$c" runuser -u postgres -- \
            psql -XAtq -d lab -v ON_ERROR_STOP=1 -c "INSERT INTO t VALUES ($n)" 2>&1)" &&
            [[ "$out" != *WARN* ]]; then
            echo "$n" >>"$acked"
            cur=$(((cur + i) % 2))
            break
        fi
    done
    n=$((n + 1))
    sleep 0.2
done
