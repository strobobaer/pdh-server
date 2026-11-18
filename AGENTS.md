# Agent Guidance

This repo uses RunQL for SQL workflows and schema exploration.

<!-- RUNQL:BEGIN -->
# RunQL Context

This workspace stores RunQL files locally under this project folder.

RunQL storage root:

./RunQL

Useful paths:

- Queries: ./RunQL/queries
- Query index: ./RunQL/system/queries/queryIndex.json (auto-updated when a query is saved)
- Schemas: ./RunQL/schemas
- Connection profiles: ./RunQL/system/connections.json
- Prompt templates: ./RunQL/system/prompts

## Required Workflow (SQL Queries)

1. Search for existing queries first — check the query index and `./RunQL/queries` (including subdirectories).
2. If nothing relevant exists, read the schema and docs under `./RunQL/schemas`. Use `./RunQL/schemas/<connection>/manifest.json` to find available schemas, then read only the relevant `./RunQL/schemas/<connection>/<schema>/schema.json` and `description.json`. Ignore `./RunQL/schemas/deleted/` and `*_deleted` folders unless the user asks for archived content.
3. Only then create a new SQL query file. Prefer to reuse or extend existing patterns. Put saved SQL under `./RunQL/queries/<connection>/`.

## Required Workflow (Documentation Requests)

1. **SQL query documentation:** follow `./RunQL/system/prompts/markdownDoc.txt`. Output goes in the same directory as the query with the same base name and a `.md` extension (e.g., `olympic_gold.sql` → `olympic_gold.md`).
2. **Schema description:** follow `./RunQL/system/prompts/describeSchema.txt`. Output goes to the matching bundle folder as `./RunQL/schemas/<connection>/<schema>/description.json`.
3. **Inline SQL comments:** follow `./RunQL/system/prompts/inlineComments.txt`.

Secrets are stored in VS Code SecretStorage and are not present in these files.
<!-- RUNQL:END -->

## Handbuch (In-App-Hilfe) mitpflegen

Das Benutzerhandbuch liegt in der Anwendung unter `/help` (Navigation → Handbuch;
Kontexthilfe im Hilfe-Reiter der rechten Seitenleiste). **Jede Anpassung oder
Änderung an Funktionen, Oberfläche oder Abläufen muss im selben Schritt in
Handbuch und Hilfe nachgezogen werden: Neues ergänzen, Geändertes anpassen,
Entfallenes oder Veraltetes bereinigen (entfernen).**

- Texte: `web/templates/help.gohtml` – ein `<section class="hb-ch" id="…" data-pages="…">`
  je Kapitel. `data-pages` listet die `BaseData.Page`-Kennungen, für die das
  Kapitel als Kontexthilfe erscheint. Neue Kapitel zusätzlich in
  `helpChapterIDs` (`internal/web/help.go`) eintragen.
- Illustrationen: `web/templates/widgets/help_figures.gohtml` (schematische SVGs) –
  bei geänderter Oberfläche die passende Skizze anpassen.
- Automatisch aktuelle Abschnitte (Berechtigungen, Feldsatz-Module, Feldtypen,
  Etikettenformate, Version) kommen aus `internal/web/help.go` und müssen nicht
  von Hand gepflegt werden.
- Schreibstil: für Anwender im Betrieb, kurze Sätze, „So geht's“-Schritte
  (`.hb-howto` / `.hb-steps`) statt Funktionslisten.
- `TestHelpCoversAllPages` schlägt fehl, wenn eine neue Seite kein Kapitel hat;
  `TestHelpChaptersMatch` prüft Kapitel-IDs und Bildschirmfoto-Bereiche.

## Versionierung mitpflegen

Die Versionsnummer steht **nur** in `VERSION` (x.y.z), die Änderungen in
`CHANGELOG.md`. Beide werden ins Programm eingebettet (`version.go`) und
erscheinen in der Kopfzeile, unter `/health`, in Sicherungen, im
Server-Protokoll, im Handbuch-Kapitel „Versionen & Updates“ und bei der
Update-Prüfung (Core-Einstellungen liest `VERSION`/`CHANGELOG.md` von GitHub main).

- Jede für Anwender sichtbare Änderung bekommt einen Stichpunkt in
  `CHANGELOG.md` unter `### Neu`, `### Geändert`, `### Behoben` oder
  `### Sicherheit` – in Anwendersprache, ein Satz je Punkt.
- Ist die aktuelle Version schon auf GitHub (`git show origin/main:VERSION`
  gleich `VERSION`), eine neue Version anlegen: **immer die nächste
  Korrekturversion** (0.76.3 → 0.76.4 → 0.76.5), auch bei neuen Funktionen.
  Nebenversionen (0.77.0, 0.78.0 …) vergibt nur der Anwender selbst –
  0.77.0 ist für den Spiegelserver reserviert. Sonst in den obersten, noch
  nicht veröffentlichten Eintrag ergänzen.
- Kommt während der Arbeit ein weiteres Funktionspaket dazu, das der
  Anwender als eigene Version nennt, bekommt es die nächste
  Korrekturversion mit eigenem Eintrag.
- Neuer Eintrag: `## [x.y.z] – JJJJ-MM-TT` ganz oben, `VERSION` gleichzeitig ändern.
- `TestChangelogMatchesVersion` prüft, dass `VERSION` und oberster Eintrag
  übereinstimmen und die Versionen absteigend sortiert sind.

## Design-Stil (Fiori/Fluent) mitpflegen

Die Oberfläche hat zwei Design-Stile: **Fiori** (wie SAP, Standard) und
**Fluent** (wie Windows 11). Logik in `internal/web/ui_style.go`, Aussehen
allein in CSS über `html[data-style="fiori"]` bzw. `"fluent"`.

- Neue oder geänderte Oberfläche in **beiden Stilen und hell/dunkel**
  prüfen. Farben, Ecken und Schatten über die Variablen (`--bg`, `--bg2`,
  `--text`, `--muted`, `--border`, `--accent`, `--shadow-card`) statt fester
  Werte, dann passen beide Stile automatisch.
- Seiten mit **eigenem Seitenkopf** (nicht `base.gohtml`, z. B.
  `login.gohtml`, `easy.gohtml`, `global_dashboard.gohtml`) müssen
  `document.documentElement.dataset.style` setzen und eine Fiori-Variante
  ihrer Regeln haben.
- Der Akzent kommt immer aus dem Farbschema (`--accent`), nie aus dem Stil.

## Kopfleiste

- **Uhr und Benutzermenü müssen immer ganz sichtbar sein** – auf jedem
  Gerät, bei jeder Vergrößerung. Sie schrumpfen nie.
- Wird es eng, kürzt sich zuerst der Seitentitel; danach blendet
  `pdhTopbarFit()` (in `base.gohtml`) Knöpfe nach `data-tb-hide` aus,
  kleinste Zahl zuerst: 1 Im Chat teilen, 2 Copilot, 3 Hell/Dunkel,
  4 Chat, 5 rechte Seitenleiste, 6 Sprache (soll möglichst bleiben).
- Neue Kopfleisten-Knöpfe: nur Symbol, Erklärung im `title`-Tooltip und
  `aria-label`, kein Text neben dem Knopf; eine passende `data-tb-hide`-Zahl
  vergeben. Was ausgeblendet werden kann, muss anders erreichbar bleiben
  (Eintrag im Benutzermenü, sichtbar über `html.tb-off-N`, oder Reiter in
  der rechten Seitenleiste).
- Änderungen an der Kopfleiste bei 320, 390, 768, 1024 und 1366 px Breite
  sowie mit 120–160 % Größe prüfen.

## Rechte mitpflegen

- **Alles neu Hinzugefügte braucht ein Recht.** Neue Seiten, Widgets,
  Aktionen und Schnittstellen prüfen ein Recht (`h.hasPerm`, für einen
  bestimmten Datensatz `h.hasPermRec`); das Recht per Migration anlegen
  (`permissions`, passende Kategorie aus `permCategoryOrder`) und den
  Rollen geben, die es bisher stillschweigend durften – niemand soll
  durch ein Update ausgesperrt werden.
- **Rechte haben eine Abteilungsebene.** Ein Recht gilt überall oder nur
  in einer Abteilung samt Unterabteilungen (`role_permissions.department_id`,
  `user_permissions.department_id`, `internal/core/rbac/scope.go`). Bei
  Aktionen an einem Datensatz immer `h.hasPermRec(r, recht, typ, id)`
  statt `h.hasPerm` verwenden, damit die Abteilung zählt.
- **Jeder Bereich hat einen Bereichsadministrator** (`<bereich>.admin`). Er
  hat alle Rechte des Bereichs und darf als Einziger endgültig löschen
  (`h.isAreaAdmin`, Löschrouten in `gateRules`, `perm_gate.go`). Neue
  Bereiche bekommen ein eigenes `.admin`-Recht, neue Lösch-Aktionen prüfen es.
- Rechte nur weitergeben, die man selbst hat (`mayGrant` / `mayGrantIn`).
- Handbuch-Kapitel „Administration“ und das Änderungsprotokoll (`### Sicherheit`)
  mitpflegen.

## Prüfung vor jedem Merge (GitHub main ist führend)

Der Branch `main` auf GitHub ist **immer führend**. Es arbeiten mehrere
Sitzungen gleichzeitig am PDH-Server. Deshalb vor **jedem** Merge prüfen,
ob es Dopplungen oder Abweichungen zum aktuellen Stand gibt:

1. `git fetch origin main` und den eigenen Branch auf den neuesten Stand
   von `origin/main` bringen (mergen). Konflikte zugunsten von `main`
   auflösen und die eigene Änderung darauf neu aufsetzen.
2. **Dopplungen suchen:** Gibt es auf `main` schon dieselbe oder eine
   ähnliche Funktion, Route, Migration, Recht, Vorlage, Hilfsfunktion
   oder denselben Handbuch-Abschnitt? Dann das Vorhandene nutzen bzw.
   erweitern statt doppelt anlegen (`git diff origin/main...HEAD`,
   `grep` nach Namen, Routen und Rechte-Schlüsseln).
3. **Abweichungen klären:**
   - Migrationsnummern dürfen nicht doppelt vergeben sein; sonst die
     eigene umnummerieren.
   - `VERSION` und `CHANGELOG.md` müssen über `main` liegen; sonst die
     nächste Korrekturversion nehmen und den Eintrag nach oben setzen.
   - Geänderte Funktionen, Signaturen und Vorlagen von `main` übernehmen
     und den eigenen Code daran anpassen.
4. Gefundene Dopplungen und Abweichungen **gleich beheben** und
   einarbeiten, nicht nur melden.
5. Danach Tests (`go test ./...`), Build und ggf. Browser-Prüfung erneut
   laufen lassen und erst dann mergen.
6. Im Pull-Request-Text kurz festhalten, was die Prüfung ergeben hat
   (z. B. „keine Dopplungen“ oder „Migration 152 → 154 umnummeriert“).

Push, Merge und Einspielen nur mit ausdrücklicher Freigabe des Anwenders.
Ist `gh` nicht verfügbar, wird wie bisher lokal gemergt:
`git checkout main && git merge --ff-only origin/main && git merge --no-ff <branch> -m "Merge: <Thema> (x.y.z)"`,
das Prüfergebnis kommt dann in den Text des Merge-Commits, danach
`git push origin main`.

## Testverbund aktualisieren

Testverbund: `pdh` 10.10.0.14 und `pdh-server` 10.10.0.17 (Server, gemeinsame
Adresse 10.10.0.15), Zeuge als Docker-Container auf dem Windows-Rechner
(10.10.0.231). **Produktiv ist nur 10.10.0.202 – dort nie etwas einspielen
oder ändern.**

1. Aktiven Server feststellen (nur lesend):
   `ssh michael@10.10.0.14 'sudo -n pdh-ha status'` bzw. `…@10.10.0.17`.
2. Am **aktiven** Server das Verbund-Update starten:
   `sudo -n pdh-ha update start <voller Commit von main> --version x.y.z`.
3. Verlauf mit `sudo -n pdh-ha update status` verfolgen, bis
   „Update auf x.y.z abgeschlossen“. Die Zeile „Fehlerhafte Versionen: …“
   nennt alte, zurückgesetzte Stände und ist kein Fehler des laufenden
   Updates. Die Server tauschen dabei die Rollen.
4. Danach prüfen: beide Server melden die neue Version; Migration und neue
   Rechte sind da (`sudo -n -u postgres psql -d pdh …`); die Spiegelung
   läuft wieder (ein rsync-Fehler direkt nach dem Rollentausch ist
   vorübergehend).
5. Der Zeuge (`pdh-ha-witness-witness-1`) zieht über seinen Helfer-Container
   `pdh-ha-witness-updater-1` innerhalb von etwa 5 Minuten selbst nach.
   Kein SSH nötig; Stand: `docker exec pdh-ha-witness-witness-1 pdh-ha status`.

## Browserprüfung auf dem Testverbund

- Das PDH ist im Testverbund unter `http://10.10.0.15:8090` erreichbar
  (`https://10.10.0.15` ist Nextcloud). Ist kein Chrome installiert:
  Edge über `playwright-core` (`channel: 'msedge'`) aus einem eigenen
  Ordner im Scratchpad steuern; auf das Laden der Seite warten, nicht auf
  „networkidle“ (Live-Verbindungen bleiben offen).
- Anmeldung mit dem Prüfkonto `abnahme-abgleich` (Rolle admin). Es ist
  außerhalb von Prüfungen gesperrt (`active = false`, Kennwort-Hash
  durch einen Sperrwert ersetzt).
- Zum Prüfen am aktiven Server entsperren: zufälliges Einmal-Kennwort
  erzeugen, bcrypt-Hash (`bcrypt.DefaultCost`) setzen, `active = true`.
  Kennwort nur im Scratchpad ablegen, nie in Dateien des Repositorys,
  Commits oder Antworten.
- Prüfen in Fiori und Fluent, hell und dunkel, bei den in diesem Dokument
  genannten Breiten. Testdaten, die dabei entstehen, danach entfernen.
- Danach das Konto **wieder sperren** (alten Sperrwert zurückschreiben,
  `active = false`).

## Arbeitsweise und offene Punkte

- Zu Beginn die **Liste der offenen Punkte** unten lesen und mit dem
  tatsächlichen Stand abgleichen (`git status`, `git fetch origin main`,
  `VERSION`, Testverbund nur lesend).
- Die Punkte **Schritt für Schritt** abarbeiten wie ein Programmierer:
  erst am Code prüfen, ob der Punkt noch stimmt, dann bauen, testen
  (`go vet ./...`, `go test ./...` in Git Bash mit Labor-Datenbank),
  Handbuch und Änderungsprotokoll nachziehen, lokal committen.
- **Nachfragen**, wenn eine Entscheidung dem Anwender gehört (Umfang,
  Bedienung, Rechte, alles mit Wirkung nach außen). Bis zur Antwort mit
  dem weitermachen, was davon nicht abhängt.
- Größere Pakete: erst Entwurf (`docs/superpowers/specs/`), vom Anwender
  bestätigen lassen, dann Plan und Bau.
- Die Liste **im selben Schritt pflegen**: Erledigtes streichen, Neues
  eintragen, Stand und Datum anpassen.

### Offene Punkte (Stand 18.11.2026)

1. **0.81.3 SFTP mit Schlüssel** (Efficio-Export) und **0.81.4 Seite
   „Netzwerk“** (VLANs und WLANs, Migration 177, Rechte `system.network`
   und `system.network_admin`) – auf `main` (gebaut als 0.80.26/0.80.27,
   vor dem Merge auf 0.81.3/0.81.4 und Migration 169 → 177 umgestellt).
   Offen: Abnahme SFTP mit Schlüssel an einem echten Server.
2. **ESPPDH = Version 0.82.0** (vom Anwender festgelegt). Entwurf
   `docs/superpowers/specs/2026-11-18-esppdh-design.md` auf `feature/esppdh`,
   am 18.11.2026 bestätigt (Paketschnitt, kein ESPPDH im Docker-Betrieb,
   OTA als Abruf, freier Editor nur Bereichsadministrator in Paket 3,
   ESPHome-Version fest im Repository; Platinen verschieden; Netze: alles).
   Die WLANs der Geräte kommen von der Seite „Netzwerk“
   (`h.networkWLANSecret`). Nächster Schritt: Plan für Paket 1, dann Bau.
3. **Netz-Abgleich im Browser mit zwei Quellen:** 0.80.25 ist auf `main`
   und dem Testverbund; die Vorschlagsliste mit zwei eingeschalteten
   Quellen ist nur automatisch getestet (auf dem Testverbund sind alle
   Quellen aus).
4. **Efficio klein:** FTPS ist ungetestet. (Abbruch der Zustellung nach dem
   ersten Fehler ist Absicht – Reihenfolge der Dateien; kein offener Punkt.)
5. **MQTT klein:** Bei älterem Update-Agenten versucht der tägliche Lauf
   die nginx-Seite jeden Tag neu (harmlos).
6. **Abnahmen durch den Anwender:** SNMP an echten Geräten, Omada an
   echtem Controller, TIA-Baustein in PLCSIM, Datennetz mit echtem Tailnet,
   SFTP mit Schlüssel an einem echten Server.
7. **Zurückgestellt (kein Auftrag):** Abteilungsfeinheiten Rüstzeiten,
   Datennetz Paket 2/3, Buchungssystem/Lizenzen, Firmenstammdaten.
8. **0.81.0 Rüstzeit in „Neu anlegen“ und 0.81.1 Tagesgrenzen/Zeitumstellung**
   – auf `main`. Offen: Verbund-Update und Browserprüfung (Assistent mit
   Kachel Rüstzeit, Wartung „heute fällig“ nicht überfällig, Sicherungs-
   Formular mit 03:00).
9. **0.81.2 Terminal-Paket** (Kiosk-Terminals, Anlagenterminal, Kopplung,
   Kartensitzung, Anmeldung ohne Karte) – auf `main`, Migrationen
   169–176 (vorher 155–162 auf den Branches `feat/kiosk-terminal`,
   `feat/anlagenterminal`, `feat/kartensitzung`, `feat/terminal-kopplung`;
   diese Branches nicht mehr mergen). Neu: `migrations/checksums.txt` –
   neue Migrationen mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database
   -run TestMigrationsFrozen` eintragen. Offen: Verbund-Update,
   Browserprüfung, Abnahme am Raspberry Pi mit Kartenleser.
10. **Zeitzone im Docker-Testsystem** (Vorschlag, kein Auftrag): Container
   laufen in UTC, Produktion (systemd) in Europe/Berlin. Umstellung nur als
   Paket: `TZ` für app und updater in `compose.yaml`, `time/tzdata` in
   `cmd/server` und `cmd/update-agent`, Postgres-Sitzungszone über pgx
   `RuntimeParams["timezone"]`; nie nur `TZ` an der App, kein `PGTZ`.
11. **Tagesgrenzen aufräumen (klein):** übrige `Truncate(24*time.Hour)`
    gegen DATE-Spalten (`kanban.go`, `purchases.go`, `trainings.go`,
    `user_qualifications.go`, `procurement.go`, `projects_handler.go`,
    `partners.go`) auf `localDayStart` umstellen.

Docker Desktop startet nach einem Neustart des Windows-Rechners nicht von
selbst – ohne ihn fehlen der Zeuge und die Labor-Datenbank für die Tests.

## Sprache

Antworten, Handbuch, Änderungsprotokoll, Commit-Nachrichten und
Pull-Request-Texte auf **Deutsch**, für Anwender im Betrieb verständlich.
