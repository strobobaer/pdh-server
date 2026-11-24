# Betriebssystem-Updates im PDH – Entwurf

Stand: 24.11.2026 · Branch `feature/os-update` (ab `main` 0.82.56, `a883ba02`) · Version: nächste Korrekturversion beim Merge · Bau: Codex (Modell `spark`), Prüfung: Claude, zusätzlich Codex-Prüfung vor dem Merge.

## Anlass

Anwender, 24.11.2026: „baust du auch noch ein systemupdate ein … damit du immer update und upgrade machen kannst“, „da währe doch button beim upgrade über github sinnig“. Heute spielt das PDH nur sich selbst ein (Core-Einstellungen → „Update installieren“ von GitHub). Betriebssystem-Pakete (Ubuntu am Testverbund, Debian 13 auf 10.10.0.202) muss jemand per SSH aktualisieren; am 24.11.2026 waren auf 202 102 Pakete offen, am Testverbund ein Kernel-Update mit „Neustart nötig“.

## Entscheidungen des Anwenders (24.11.2026)

1. **Auslösen:** eigener Knopf „Betriebssystem aktualisieren“ **und** ein Haken „beim PDH-Update mit aktualisieren“ – mit gesetztem Haken macht „Update installieren“ beides in einem Durchgang.
2. **Neustart:** „wie beim PDH-Update“ – Einzelserver: Countdown, Neustart, danach `/health` prüfen; Verbund: automatisch nacheinander, Spiegel zuerst, mit geordnetem Rollentausch.
3. Eigenes Recht für die Rollen, die heute PDH-Updates einspielen.
4. Gebaut wird von Codex in diesem Arbeitsordner, geprüft von Claude; sparsames Modell.

## Ziele

- Core-Einstellungen zeigen je Server: System und Kernel, Zahl der offenen Pakete (davon Sicherheitsupdates), „Neustart nötig“ samt auslösenden Paketen, Zeit der letzten Prüfung, letzter Lauf mit Ergebnis.
- Knopf **„Betriebssystem prüfen“** (Paketlisten neu laden) und **„Betriebssystem aktualisieren“**; Haken **„beim PDH-Update mit aktualisieren“** (gespeicherte Einstellung, gilt auch für das Update im Wartungsfenster).
- Einzelserver und Verbund mit denselben Regeln wie das PDH-Update (Prüfung „alles grün“, Countdown, Sperre, Prüfung danach, Wartungsband und `/wartung`).
- Kein unkontrollierter Neustart von PDH, PostgreSQL oder nginx während des Betriebs.
- Ergebnis nachvollziehbar: Server-Protokoll und `/var/log/pdh-os-update.log`.

## Nicht-Ziele

- Wechsel der Distributionsversion (`do-release-upgrade`, Debian 13 → 14), Auswahl einzelner Pakete, Zurücknehmen eines Betriebssystem-Updates.
- Der Zeuge (Docker-Container auf dem Windows-Rechner) und das Docker-Testsystem (`PDH_UPDATE_MODE=docker`): dort erscheint die Karte mit „im Docker-Betrieb nicht verfügbar“.
- Die Fußleiste als Update-Anzeige (gehört zu 0.90.0, Teil „gleiche Bausteine“).

## Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)

- apt läuft, während Anwender arbeiten, und ein Paketskript startet PostgreSQL oder nginx neu.
- Der aktive Server im Verbund wird neu gestartet, statt vorher die Rolle abzugeben.
- Ein Server bleibt nach dem Neustart stumm, und das PDH meldet trotzdem „fertig“.
- Ein PDH-Update und ein Betriebssystem-Update laufen gleichzeitig oder stören sich (dpkg-Sperre, Netz-Änderung, ESPHome-/nginx-Installation).
- apt wird mit frei wählbaren Argumenten aufgerufen (wäre volles root).

## Ansätze

| | Ansatz | Bewertung |
|---|---|---|
| **A (gewählt)** | Betriebssystem-Schritt in den bestehenden Update-Ablauf einbauen: Einzelserver im Update-Agenten (`cmd/update-agent/job.go`), Verbund in pdh-ha (`internal/ha/update.go`), jeweils mit Countdown, Sperre, Prüfung | gleiches Verhalten wie das PDH-Update, Anzeige und Wartungsband gibt es schon; mehr Arbeit im Verbund-Ablauf |
| B | Knopf startet nur das Skript im Hintergrund, ohne Sperre | einfach, aber Paketskripte (postgresql, nginx, docker) starten Dienste selbst neu → Ausfälle im Betrieb, im Verbund Umschaltung |
| C | `unattended-upgrades` einrichten | keine Rückmeldung im PDH, Neustart nicht mit dem Verbund abgestimmt |

## Bauweise

### Skript `pdh-os-update`

- Liegt künftig im Repository (`scripts/pdh-os-update`) und wird bei jedem PDH-Update nach `/usr/local/sbin/pdh-os-update` (root:root, 0755) installiert – über `install_update.sh` wie pdh-ha und der Agent. Inhalt wie das am 24.11.2026 auf 10.10.0.14/.17/.202 installierte Skript: `check` (Paketlisten laden, offene Pakete lesbar ausgeben), `install` (`apt-get … upgrade --with-new-pkgs`, `DEBIAN_FRONTEND=noninteractive`, `NEEDRESTART_MODE=l`, `--force-confdef --force-confold`, `DPkg::Lock::Timeout=300`, Protokoll `/var/log/pdh-os-update.log`, Hinweis „NEUSTART NÖTIG“).
- Neu: Aktion `status` mit maschinenlesbarer Ausgabe (JSON: offene Pakete mit Name, alter/neuer Version, Sicherheitsupdate ja/nein; `reboot_required`; auslösende Pakete; System; Kernel). Keine weiteren Aktionen, keine frei wählbaren Argumente. Die sudo-Regel auf 202 (`91-pdh-os-update`: nur `check` und `install`) bleibt gültig.
- Distributionswechsel und Paketentfernung macht das Skript nie (`upgrade`, nicht `full-upgrade`/`dist-upgrade`).

### Ausführung durch den Update-Agenten

- Der Agent läuft als root mit `ProtectSystem=full`. apt und Neustart laufen deshalb **immer** über `systemd-run --wait --pipe --collect --unit=pdh-os-update-<zeit>` außerhalb der Sandbox – Muster `install_update.sh:270-285` (nginx) und `scripts/install-esphome.sh`. So überlebt apt auch einen Neustart des Agenten.
- Eine gemeinsame Sperre im Agenten verhindert, dass Betriebssystem-Update, PDH-Update, nginx-/ESPHome-Installation und Netz-Änderung gleichzeitig laufen.
- Endpunkte (Bearer-Token wie bisher): `GET /v1/os/status` (letzte Prüfung, laufender Auftrag), `POST /v1/os/check`, `POST /v1/os/job/start {force}`; Abbrechen über den vorhandenen Abbruch-Weg, solange apt noch nicht läuft.
- Automatische Prüfung: gleicher Takt wie die GitHub-Prüfung (`update_auto_check_enabled`, `update_check_interval_hours`).

### Einzelserver (z. B. 10.10.0.202)

Eigener Auftrag „Betriebssystem“ nach dem Muster des PDH-Auftrags (`job.go`), Zustand in `/var/lib/pdh-dr/os-job.json`, damit er einen Neustart des Rechners überlebt (Muster `loadSingleJob`, `netResume`):

1. **Prüfen vorher:** „alles grün“ (`ha.UpdateReady`), kein anderer Auftrag, Plattenplatz (`/var/cache/apt`, `/boot`) ausreichend.
2. **Countdown** wie im Wartungsfenster eingestellt (`CountdownMin`), Wartungsband auf allen Seiten.
3. **Sperre** wie beim PDH-Update (Sitzungen abmelden, `/wartung`), Rückfallkopie der Datenbank (`pg_dump` wie „Sicherung vor dem Update“).
4. **Pakete einspielen** (`pdh-os-update install`).
5. **Neustart**, nur wenn `/var/run/reboot-required` existiert: Auftrag „Neustart“ speichern, Rechner neu starten.
6. **Prüfen danach:** nach dem Hochfahren nimmt der Agent den Auftrag wieder auf, wartet auf `/health` (Frist wie beim PDH-Update, 10 min).
7. **Freigabe**, Eintrag im Server-Protokoll („Betriebssystem aktualisiert: N Pakete, Neustart ja/nein“).

Fehler: apt scheitert → kein Neustart, Freigabe, Meldung mit den letzten Zeilen der Ausgabe. Kommt der Server nach dem Neustart nicht hoch, kann das PDH nichts tun; das Handbuch nennt den Weg über die Konsole.

**Haken gesetzt:** Der PDH-Auftrag bekommt nach „Prüfen neu“ und vor „Freigabe“ die Schritte 4 bis 6 (die Rückfallkopie hat der PDH-Auftrag schon gemacht) – **ein** Countdown, **eine** Sperre, **ein** Durchgang. Ohne offene Pakete entfällt der Schritt.

### Verbund (Testverbund)

- Betriebssystem-Pakete und Neustart gibt es **nur auf dem jeweils passiven Server**. Der aktive Server wird nie neu gestartet, ohne vorher die Rolle abgegeben zu haben.
- **Knopf „Betriebssystem aktualisieren“** startet einen Auftrag der neuen Art „Betriebssystem“, aufgebaut wie `rolling` ohne Programmwechsel: passiver Server B: Pakete + ggf. Neustart, warten bis Spiegelung und Datenbank wieder auf Stand sind → geordneter Rollentausch (vorhandener Tausch `CmdSwapFreeze`) → jetzt passiver Server A: Pakete + ggf. Neustart, warten bis auf Stand → fertig. Countdown und Sperre nur um den Rollentausch, wie bei `rolling`.
- **Haken gesetzt, Art `rolling`:** der Programm-Schritt am passiven Server (`OpInstall` in „vorbereiten“ und „nachziehen“) spielt danach die Pakete ein und startet bei Bedarf neu, bevor der Auftrag weitergeht.
- **Haken gesetzt, Art `sequential`:** kein Betriebssystem-Teil (die Art startet den aktiven Server neu); die Karte weist darauf hin, der Knopf bleibt nutzbar.
- pdh-ha muss den Neustart eines Knotens als erwarteten Schritt kennen: Der Auftrag merkt sich „Knoten X startet neu“, der Knoten meldet sich nach dem Hochfahren mit dem Ergebnis zurück (Muster `FreshStart` in `UpdateObs`), ohne dass das als Ausfall gilt. Zeitlimit: 15 min, danach „abgebrochen“ mit klarer Meldung, Rollen bleiben wie sie sind.
- Der Zeuge wird nicht angefasst.

### Oberfläche

Karte **„Betriebssystem“** in `web/templates/core_settings.gohtml` direkt unter „GitHub-Updates“:

```
Betriebssystem
pdh-server (aktiv)   Ubuntu 26.04.1 · Kernel 7.0.0-44   19 Pakete offen (3 Sicherheit) · Neustart nötig
pdh (Spiegel)        Ubuntu 26.04.1 · Kernel 7.0.0-44   aktuell · geprüft vor 2 h
letzter Lauf: 24.11.2026 16:41 – 19 Pakete, Neustart, ok
[Betriebssystem prüfen]  [Betriebssystem aktualisieren]
☐ beim PDH-Update mit aktualisieren
```

- Knöpfe und Haken nur mit dem neuen Recht; ohne Recht nur die Anzeige.
- Fortschritt über das Vorhandene: Wartungsband, `/wartung`, Kasten in „Server-Einstellungen → Ausfallschutz“ mit neuen Schrittnamen („Betriebssystem einspielen“, „Neustart von …“, „Warten auf …“).
- Fiori und Fluent, hell und dunkel, 320–1366 px (AGENTS.md).

### Recht

- Neu: `system.os_update` – „Betriebssystem aktualisieren“, Kategorie „System & Sicherheit“, per Migration (nächste freie Nummer, `migrations/checksums.txt`). Bekommen alle Rollen, die heute `system.manage_roles` überall haben (das Recht hinter „Update installieren“) – Muster `migrations/154_verbund_update.up.sql`. Gilt nur überall, nicht je Abteilung.
- Knopf, Haken und `POST`-Wege prüfen das Recht; die Bestätigung einer Nachfrage im Wartungsfenster bleibt wie heute.

### Wartungsfenster

- Ist der Haken gesetzt und spielt das Wartungsfenster ein PDH-Update ein, kommen die Pakete mit (wie Knopf mit Haken).
- **Offene Frage an den Anwender (siehe unten):** Betriebssystem-Updates im Wartungsfenster auch ohne PDH-Update.

## Tests

- Agent: Tests mit Platzhaltern für `systemd-run`, `apt-get`, `systemctl reboot` (Muster `cmd/update-agent/update_test.go:271,436-489`); Auftrag überlebt „Neustart“ (Zustandsdatei); gegenseitiger Ausschluss mit PDH-Update; Docker-Betrieb gesperrt.
- pdh-ha: Zustandsmaschine „Betriebssystem“ (B zuerst, Tausch, A; Neustart eines Knotens gilt nicht als Ausfall; Zeitlimit; aktiver Server bekommt nie apt/Neustart); `rolling` mit Haken.
- Web: Karte mit und ohne Recht, Haken speichern, Routen verlangen das Recht, Migration (Recht bei den richtigen Rollen), Handbuch-Tests.
- Skript: `bash -n`, Aufruf mit unbekannter Aktion endet mit Code 2, `status` liefert gültiges JSON (Test mit Platzhalter-apt).
- Abnahme am Testverbund mit echten offenen Paketen (Knopf allein und mit Haken); 202 spielt der Anwender ein, mit Sicherung.

## Handbuch und Änderungsprotokoll

- `help.gohtml` `#versions`: „So aktualisierst du das Betriebssystem“ (Knopf, Haken, was passiert, Neustart, was bei einem Fehler zu tun ist); `#hb-verbund-update`: Ablauf im Verbund; Kapitel „Administration“: neues Recht. Skizze `hb-fig-update` um die Karte ergänzen.
- `CHANGELOG.md`: `### Neu` (Knopf, Haken, Anzeige), `### Sicherheit` (neues Recht).

## Lieferung in zwei Teilen

1. **Teil A:** Skript im Repository, Agent (Status, Prüfen, Auftrag Einzelserver, Haken im PDH-Auftrag), Karte, Recht, Handbuch – nützt sofort auf 10.10.0.202.
2. **Teil B:** Verbund (Art „Betriebssystem“, Haken bei `rolling`, Neustart als erwarteter Schritt).

Jeder Teil mit eigener Korrekturversion, Schlussprüfung (Claude + Codex) und Einspielen am Testverbund nach der Dauerfreigabe.

## Dem Bauenden überlassen

Namen der Go-Typen und Dateien (Vorschlag: `cmd/update-agent/os_update.go`, `internal/web/os_updates.go`), genaue Form des JSON von `status`, Erkennung der Sicherheitsupdates (Paketquelle `-security`), Texte der Schrittnamen, Aufteilung in Commits.

## Meine Festlegungen (bitte prüfen)

- Rückfallkopie der Datenbank auch vor einem reinen Betriebssystem-Update – kostet ein paar Sekunden bis Minuten, schützt bei einem missglückten Neustart.
- `upgrade --with-new-pkgs` statt `full-upgrade`: neue Kernel kommen mit, entfernt wird nichts.
- Haken mit `sequential` wirkt nicht (Hinweis statt Neustart des aktiven Servers).
- Zeitlimit für den Neustart eines Knotens 15 min.
- Nur die beiden Server, nie der Zeuge.

## Offene Frage an den Anwender

1. Sollen Betriebssystem-Updates im Wartungsfenster auch laufen, **wenn kein PDH-Update ansteht** (z. B. wöchentlich die Sicherheitsupdates)? Vorschlag: ja, wenn der Haken gesetzt ist und Sicherheitsupdates offen sind.
