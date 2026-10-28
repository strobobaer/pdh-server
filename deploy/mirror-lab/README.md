# Spiegel-Labor (PDH auf echtem PostgreSQL-Standby)

Nur Testumgebung – nie auf einem Produktivserver. Das Labor startet zwei
PostgreSQL-Server (`pg1` Primary, `pg2` Hot Standby per `pg_basebackup -R`)
und zwei PDH (`pdh1` gegen `pg1`, `pdh2` gegen `pg2`) als Container und prüft,
dass `pdh2` als Spiegel nur liest. Vor allem sucht ein Crawler versteckte
Schreibzugriffe in Seiten, die nur anzeigen sollen.

## Voraussetzungen

- Linux mit Docker und Compose-Plugin (`docker compose version`)
- Internetzugang beim ersten Bauen (Go-Module, Abbilder `postgres:18`,
  `golang:1.24-bookworm`, `ubuntu:24.04`)
- Containernamen `pdh-mirror-lab-*` und das Netz `pdh-mirror-lab-net` dürfen
  nicht belegt sein (das pdh-ha-Labor `deploy/ha-lab` stört nicht)

## Befehle

```bash
bash deploy/mirror-lab/mirror-lab.sh up          # neu bauen und aufbauen
bash deploy/mirror-lab/mirror-lab.sh <Szenario>  # ein Szenario (baut bei Bedarf auf)
bash deploy/mirror-lab/mirror-lab.sh alle        # alles nacheinander, danach abbauen
bash deploy/mirror-lab/mirror-lab.sh down        # Labor samt Volumes entfernen
```

`MIRROR_LAB_KEEP=1` lässt das Labor nach `alle` stehen (zum Nachsehen,
z. B. `docker logs pdh-mirror-lab-pdh2`). `MIRROR_LAB_PG_IMAGE=postgres:16`
nimmt eine andere PostgreSQL-Version.

## Aufbau (`up`)

1. Abbilder bauen: PDH-Server und `fakeha` aus dem Repo (`Dockerfile`,
   Ziel `app`), dazu ein Abbild mit Quellen für den Crawler (Ziel `src`).
2. `pg1` starten (`wal_level=replica`, Replikation freigegeben).
3. `pdh1` starten: legt das Schema an. Danach Administrator `laboradmin` und
   Nicht-Admin `labornutzer` (Rolle Techniker) per SQL anlegen, `pdh1` neu
   starten und über die Schnittstelle Beispieldaten anlegen (Anlage, Ticket mit
   Kommentar, Störung, Projekt, Aufgabe, Ersatzteil).
4. `pg2` per `pg_basebackup -R` von `pg1` als Standby starten und warten, bis
   er alles eingespielt hat.
5. `pdh2` starten – mit gleicher Einstellung wie `pdh1` (JWT-Secret,
   Einstellungsschlüssel) und dem Platzhalter `fakeha` für den
   pdh-ha-Socket (`PDH_HA_SOCKET`). `fakeha` meldet den Partner `pdh1` als
   `aktiv`; ob er „gesehen“ wird, steht in `/tmp/lab-seen` im Container.

Beide PDH laufen unter einer Neustart-Schleife (`pdh-loop.sh`) statt systemd:
beendet sich das PDH (z. B. Exit 3 beim Moduswechsel), startet es nach 1 s neu.

## Szenarien

Ergebnis je Szenario: `BESTANDEN` oder `NICHT BESTANDEN: <Grund>`,
Rückgabewert 0 bzw. 1.

| Szenario | Ablauf | Erwartung |
|---|---|---|
| `start` | `/health` von `pdh2`, Protokoll | `"mode":"spiegel"`, keine Zeile `migrationen geprüft` |
| `crawl` | Crawler meldet sich als Admin an `pdh2` an und besucht ab `/` alle internen `href`/`hx-get` (ohne `/logout`, höchstens 2000 Seiten) | keine Antwort ≥ 500; Protokoll von `pdh2` ohne `schreibzugriff im spiegel` |
| `posts` | 5 Schreibanfragen an `pdh2`: Ticket anlegen, Kommentar, Profil (Darstellung), Easy-Mode-Meldung, Hochladen (Profilbild) | jede mit 409 abgewiesen, nichts gespeichert |
| `anmeldung` | Nicht-Admin meldet sich an `pdh2` an, einmal mit erreichbarem, einmal mit ausgefallenem aktivem Server | erreichbar: abgewiesen (`/login?spiegel=1`, keine Sitzung); ausgefallen: zugelassen; Admin immer |
| `version` | `pdh2` mit zusätzlicher Dummy-Migration neu starten | Band „Spiegel wartet auf das Update des aktiven Servers“, `GET /tickets` leitet auf `/admin/server-config?tab=ha` um; danach aufgeräumt |
| `hochstufen` | `SELECT pg_promote()` auf `pg2` | `pdh2` startet neu und meldet innerhalb 10 s `"mode":"normal"`; ein Ticket lässt sich anlegen |

`alle` führt die Szenarien in dieser Reihenfolge aus. `hochstufen` kommt
zuletzt, weil danach kein Standby mehr da ist; ein weiteres Szenario baut das
Labor dann neu auf.

Findet `crawl` Schreibzugriffe, listet es sie mit Anzahl, Anfrage, SQL-Anfang
und Programmstelle (`GET /pfad → UPDATE …  [web.(*Handler).x datei.go:12 < …]`,
Feld `stelle` der Protokollzeile); Zugriffe ohne Anfrage stehen als
`(Hintergrund)`. Der Crawler besucht zuerst jede neue Seitenart (Pfad mit
Kennungen als `:id`), dann neue Pfade, zuletzt nur andere Abfrageparameter. Abhilfe im betroffenen Handler: im Spiegel
(`h.Mirror().On`) den Schreibschritt auslassen und einen Test dazu schreiben.

Den Crawler allein (gegen ein laufendes Labor oder einen anderen Spiegel):

```bash
PDH_MIRROR_URL=http://pdh2:8090 PDH_MIRROR_ADMIN=laboradmin PDH_MIRROR_PASS='Labor-Admin-2026!' \
  go test -tags mirrorlab -run TestMirrorCrawl -v ./internal/web
```
