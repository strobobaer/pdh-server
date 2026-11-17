# Labor: SNMP-v3-Gegenstelle

Nur zum Testen. Ein echter `snmpd` (net-snmp 5.9 auf Alpine) als Gegenstelle für
das Paket `internal/integrations/snmp`. Benutzer und Kennwörter gelten nur hier
und stehen bewusst im Klartext in `snmpd.conf`. Der Compose-Name ist
`pdh-lab-snmp`; andere Behälter werden nicht berührt.

Erreichbar nur vom eigenen Rechner: `127.0.0.1:1161` (UDP).

## Benutzer

Alle nur lesend und nur mit Anmeldung **und** Verschlüsselung (authPriv).
Es gibt kein SNMP v1/v2c.

| Benutzer     | Anmeldung | Verschlüsselung | Anmelde-Kennwort  | Verschlüsselungs-Kennwort |
|--------------|-----------|-----------------|-------------------|---------------------------|
| `pdhlab`     | SHA-256   | AES-128         | `Labor-Auth-2026` | `Labor-Priv-2026`         |
| `pdhlab512`  | SHA-512   | AES-256         | `Labor-Auth-2026` | `Labor-Priv-2026`         |
| `pdhlabsha1` | SHA-1     | AES-128         | `Labor-Auth-2026` | `Labor-Priv-2026`         |

Der Dienst meldet sich als `pdh-lab-snmp` (sysName). Er hat keine ENTITY-MIB
und keine Bridge-Adresse – er verhält sich also wie ein Drucker oder eine USV,
nicht wie ein Switch.

## Starten und stoppen

Vom Hauptordner des Projekts aus (Rechner: Windows-PC, Git Bash):

    docker compose -p pdh-lab-snmp -f deploy/lab/snmp/docker-compose.yml up -d --build

Stoppen:

    docker compose -p pdh-lab-snmp -f deploy/lab/snmp/docker-compose.yml down

Nach einer Änderung an `snmpd.conf` neu bauen (derselbe Befehl wie beim Start).

## Tests gegen das Labor

Die Prüfung läuft nur, wenn die Variable gesetzt ist (sonst wird sie übersprungen):

    PDH_TEST_SNMP=127.0.0.1:1161 go test ./internal/integrations/snmp/ -run Lab -v -count=1

Geprüft wird: Abfrage mit allen drei Benutzern, Name und Anschlüsse lesen,
falsches Kennwort / falscher Benutzer / falsches Verfahren → „Zugang abgelehnt“
(nie „keine Antwort“), falscher Port → „keine Antwort“. Der Lauf dauert etwa
20 Sekunden, weil der Dienst bei falschem Verschlüsselungs-Kennwort schweigt
und der Test die Wartezeit abwartet.

## Von Hand nachsehen

    docker exec pdh-lab-snmp-snmpd-1 sh -c 'apk add --no-cache net-snmp-tools >/dev/null && snmpget -v3 -l authPriv -u pdhlab -a SHA-256 -A Labor-Auth-2026 -x AES -X Labor-Priv-2026 127.0.0.1 1.3.6.1.2.1.1.5.0'

Erwartet: `SNMPv2-MIB::sysName.0 = STRING: pdh-lab-snmp`.
