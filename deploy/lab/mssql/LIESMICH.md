# Labor: Fremdsystem-Abgleich (SQL Server und Test-Postgres)

Nur zum Testen. Die Kennwörter gelten nur hier und stehen bewusst im Klartext.
Der Compose-Name ist `pdh-lab-mssql`; andere Behälter (z. B. `pdh-ha-witness-*`)
werden nicht berührt.

## Was startet

| Dienst     | Zweck                                               | Erreichbar unter                    |
|------------|-----------------------------------------------------|-------------------------------------|
| `db`       | SQL Server 2022, Datenbank `Fremd`                  | `0.0.0.0:14330` (auch im LAN)       |
| `seed`     | einmaliger Helfer: legt Tabellen, Beispieldaten und Anmeldung `pdh_sync` an | – (beendet sich) |
| `postgres` | Postgres 16 für die Go-Tests (`pdh_test`)           | nur `127.0.0.1:54330`               |

SQL Server ist absichtlich auch aus dem LAN erreichbar, weil die spätere
Abnahme von den Testservern 10.10.0.14 und 10.10.0.17 aus verbindet. Die
Windows-Firewall muss Port 14330 dafür zulassen. Den Postgres erreicht nur der
eigene Rechner.

## Starten und stoppen

Vom Hauptordner des Projekts aus (Rechner: Windows-PC, Git Bash):

    docker compose -p pdh-lab-mssql -f deploy/lab/mssql/docker-compose.yml up -d

Der Seed läuft beim ersten Start von selbst (er wartet auf den Server) und
kann gefahrlos wiederholt werden:

    docker compose -p pdh-lab-mssql -f deploy/lab/mssql/docker-compose.yml run --rm seed

Stoppen (Daten bleiben erhalten):

    docker compose -p pdh-lab-mssql -f deploy/lab/mssql/docker-compose.yml down

Alles samt Daten löschen und neu beginnen:

    docker compose -p pdh-lab-mssql -f deploy/lab/mssql/docker-compose.yml down -v

## Variablen für die Tests

    export PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'
    export PDH_TEST_MSSQL_DSN='sqlserver://pdh_sync:Lab-Sync-Kennwort-2026!@127.0.0.1:14330?database=Fremd&encrypt=disable'

Ohne diese Variablen werden die Integrationstests übersprungen.

## Inhalt der Datenbank `Fremd`

- `dbo.Meldungen` (5 Beispielzeilen, Auslöser `trg_Meldungen_Geaendert` setzt `Geaendert`)
- `dbo.MeldungKommentare`
- `dbo.MeldungenGuid` (2 Zeilen, Schlüssel `Id` + `Werk`)
- `dbo.MeldungenAlt` (10 Zeilen, Spalte `Geaendert` vom Typ `DATETIME`, alle NULL; Testtabelle für Zeitstempel-Cursor mit Gleichstand, `pdh_sync` darf lesen und ändern)
- `dbo.MeldungenText` (9 Zeilen, Schlüssel `Kennung` vom Typ `VARCHAR(40)`, Testtabelle für Schlüsselvergleich unter der SQL-Kollation; `pdh_sync` darf lesen und ändern)

Die Anmeldung `pdh_sync` darf lesen (alle drei Tabellen), ändern (`Meldungen`,
`MeldungenGuid`) und Kommentare anfügen. Löschen darf sie nichts. Die
Verwaltung läuft über `sa` (Kennwort in `docker-compose.yml`).

Prüfung (Git Bash braucht `MSYS_NO_PATHCONV=1`, sonst wird der Pfad verfälscht):

    MSYS_NO_PATHCONV=1 docker exec pdh-lab-mssql-db-1 /opt/mssql-tools18/bin/sqlcmd -C -U pdh_sync -P 'Lab-Sync-Kennwort-2026!' -d Fremd -Q "SELECT COUNT(*) FROM dbo.Meldungen"

Erwartet: 5. Ein `DELETE` als `pdh_sync` muss mit „permission was denied“ scheitern.
