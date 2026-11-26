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

## Gestaltungsgrundlage (seit 0.82.62)

- Stil und Skripte des Rahmens liegen in `web/static/ui/`
  (`tokens.css` = Gestaltungswerte, `pdh.css`, `pdh-*.js`) und kommen über
  `uiURL` als `/vendor/<Version>/ui/<Datei>` (ein Jahr zwischengespeichert).
  Neue Regeln und Skripte dorthin, **nicht** inline in `base.gohtml`
  (`TestBaseInlineBudget`, höchstens 16 KB inline).
- **Keine Vorlagen-Syntax in `web/static/ui/`** (`TestUIFilesNoTemplateSyntax`):
  Werte kommen aus dem Datenblock `pdh-boot` im Kopf von `base.gohtml`
  (`window.PDH_BOOT`, Eintrag im `dict` ergänzen), Texte über `pdhT("…")`
  mit Eintrag im `"t"`-Teil desselben Blocks.
- Farben, Abstände, Schriftgrößen, Ecken und Schatten nur aus `tokens.css`
  (`--sp-*`, `--fs-*`, `--r-*`, `--ok/--warn/--err/--info` samt `-soft`).
  `TestColorLiteralsRatchet`: feste Farbwerte je Datei dürfen nur sinken
  (`internal/web/testdata/color_literals.txt`; nach dem Entfernen senken mit
  `PDH_COLOR_BASELINE_UPDATE=1 go test ./internal/web -run TestColorLiteralsRatchet`).
  Ausnahmen: E-Mails, Druckblätter, Handbuch-Skizzen. Übersicht im PDH unter
  Verwaltung → Gestaltung.
- Tests, die Code des Rahmens suchen: `baseWithUI(t)` (Quelltext samt
  `web/static/ui/`), `withUIScripts(t, seite)` (Seite samt eingebundener
  Skripte), `bootValue(t, seite, schlüssel)` (Wert aus `pdh-boot`).

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
- **Neuer Rahmen** (`frame.gohtml`, seit 0.82.65, Schalter im Benutzermenü):
  dieselbe Kopfleiste (`ui-topbar` in `widgets/frame_shared.gohtml`), dazu
  zwischen Titel und Uhr die Suchzeile „Was möchtest du tun?“ – sie
  schrumpft vor allen Knöpfen, bis 760 px nur noch Symbol. Gemeinsame Teile
  beider Rahmen nur in `frame_shared.gohtml` ändern; der klassische Rahmen
  (`base.gohtml`) muss dabei unverändert aussehen.

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

Push, Merge und Einspielen nur mit ausdrücklicher Freigabe des Anwenders –
mit einer **Dauerfreigabe für `main` und den Testverbund** (Anwender
24.11.2026): Ist die Schlussprüfung eines Pakets grün (keine offenen
kritischen oder wichtigen Befunde, auch nicht aus der Codex-Prüfung) und
laufen `go vet ./...`, `go test ./...` und die Integrationstests bis auf die
bekannten roten, darf ohne weitere Rückfrage gemergt, gepusht und der
Testverbund aktualisiert werden. **Nicht davon gedeckt:** 10.10.0.202
(Einspielen nur mit dem Anwender und mit Sicherung), Force-Push, Löschen
von Migrationen.
Ist `gh` nicht verfügbar, wird wie bisher lokal gemergt:
`git checkout main && git merge --ff-only origin/main && git merge --no-ff <branch> -m "Merge: <Thema> (x.y.z)"`,
das Prüfergebnis kommt dann in den Text des Merge-Commits, danach
`git push origin main`.

## Testverbund aktualisieren

Testverbund: `pdh` 10.10.0.14 und `pdh-server` 10.10.0.17 (Server, gemeinsame
Adresse 10.10.0.15), Zeuge als Docker-Container auf dem Windows-Rechner
(10.10.0.231). **10.10.0.202 ist eine produktive Testversion** (Festlegung
des Anwenders vom 21.11.2026): Einspielen ist dort erlaubt, aber **nur mit
einer Sicherung vorher** (siehe „10.10.0.202 aktualisieren“). Andere
Änderungen an 202 (Einstellungen, Daten, Netz, Dateien am Server) weiterhin
nur nach Rückfrage.

1. Aktiven Server feststellen (nur lesend):
   `ssh michael@10.10.0.14 'sudo -n pdh-ha status'` bzw. `…@10.10.0.17`.
   Vorher mit `sudo -n pdh-ha update status` prüfen, dass kein Auftrag einer
   anderen Sitzung läuft – das gilt auch vor jedem Neustart eines Servers.
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

## 10.10.0.202 aktualisieren

202 ist ein Einzelserver (systemd, kein `pdh-ha`). Erst einspielen, wenn die
neue Version auf `main` und dem Testverbund geprüft ist.

1. Stand lesen: `curl -s http://10.10.0.202:8090/health` (Version, Commit,
   `migrations_ok`).
2. **Sicherung vorher:** in der Oberfläche von 202 unter Datensicherung
   „Jetzt sichern“ und abwarten, bis der Lauf erfolgreich ist. Ohne
   erfolgreiche Sicherung nicht einspielen. Zusätzlich legt der
   Update-Agent beim Update selbst eine Rückfallkopie der Datenbank an
   (Server-Protokoll: „Sicherung vor Update ausgelassen – Rückfallkopie
   vorhanden“).
3. Einspielen: Core-Einstellungen → „Update prüfen“, dann „Update
   installieren“. Der Agent baut, zählt herunter, startet neu, prüft und
   setzt bei einem Fehler selbst zurück.
4. Prüfen: `/health` meldet die neue Version und den Commit von `main`;
   `ssh michael@10.10.0.202 'journalctl -u pdh --since "-15 min" --no-pager | grep -E " (ERR|FTL) "'`
   zeigt keine neuen Fehler.

Zugang von hier: SSH als `michael` geht, `sudo -n` verlangt dort ein
Kennwort, das Dienstprotokoll ist ohne `sudo` lesbar. Die Zugangsdaten aus
der `.env` (Datenbank, Update-Agent) nicht auslesen. Eine Anmeldung an der
Oberfläche von 202 ist von hier nicht eingerichtet – die Knöpfe in Schritt 2
und 3 drückt bis auf Weiteres der Anwender.

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
- **Codex** (Anwender 24.11.2026: „Prüfer + zweite Sitzung“, sparsames
  Modell): zusätzlicher Prüfer jedes Pakets vor dem Merge und Bauender
  abgegrenzter Pakete in einem eigenen Arbeitsordner (Entwurf und Auftrag
  schreibt Claude, Claude prüft). Modell `gpt-6.2-sol` (`spark` geht mit
  dem ChatGPT-Konto nicht). Die Sandbox von Codex kann hier weder Dateien
  lesen noch `git` ausführen – den Diff deshalb in den Auftrag legen
  (`codex-companion.mjs task --model gpt-6.2-sol --prompt-file <datei>`).
  Code geht dabei an OpenAI; nie Zugangsdaten, nie 10.10.0.202.
- **Betriebssystem-Updates (apt):** Auf 10.10.0.14, 10.10.0.17 und
  10.10.0.202 liegt `/usr/local/sbin/pdh-os-update` (nur `check` und
  `install`, Protokoll `/var/log/pdh-os-update.log`, startet keine Dienste
  neu, meldet „NEUSTART NÖTIG“). Auf 202 erlaubt
  `/etc/sudoers.d/91-pdh-os-update` beide Aktionen ohne Kennwort;
  `install` dort nur mit Freigabe des Anwenders und Sicherung. Im
  Testverbund erst der passive Server (Pakete, Neustart, warten bis die
  Übernahmesperre weg ist), dann der aktive (Pakete, Neustart → der
  andere übernimmt) – vorher `pdh-ha update status`.

### Offene Punkte (Stand 26.11.2026)

1. **Netz-Abgleich im Browser mit zwei Quellen:** 0.80.25 ist auf `main`
   und dem Testverbund; die Vorschlagsliste mit zwei eingeschalteten
   Quellen ist nur automatisch getestet (auf dem Testverbund sind alle
   Quellen aus).
2. **0.81.0 Rüstzeit in „Neu anlegen“ und 0.81.1 Tagesgrenzen/Zeitumstellung**
   – auf `main` und dem Testverbund (mit 0.81.4 eingespielt). Offen:
   Browserprüfung (Assistent mit Kachel Rüstzeit, Wartung „heute fällig“
   nicht überfällig, Sicherungs-Formular mit 03:00).
3. **0.81.2 Terminal-Paket** (Kiosk-Terminals, Anlagenterminal, Kopplung,
   Kartensitzung, Anmeldung ohne Karte) – auf `main`, Migrationen
   169–176 (vorher 155–162 auf den Branches `feat/kiosk-terminal`,
   `feat/anlagenterminal`, `feat/kartensitzung`, `feat/terminal-kopplung`;
   diese Branches nicht mehr mergen). Neu: `migrations/checksums.txt` –
   neue Migrationen mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database
   -run TestMigrationsFrozen` eintragen. Auf dem Testverbund (mit 0.81.4
   eingespielt). Offen: Browserprüfung, Abnahme am Raspberry Pi mit Kartenleser.
4. **0.81.3 SFTP mit Schlüssel** (Efficio-Export) und **0.81.4 Seite
   „Netzwerk“** (VLANs und WLANs, Migrationen 177 und 178, Rechte
   `system.network` und `system.network_admin`) – auf `main` und dem
   Testverbund, im Browser geprüft (18.11.2026).
5. **0.81.5 Fingerabdruck bricht um, Netzwerk-Kennungen** (Efficio-Export
   auf schmalen Bildschirmen; neue Kennungen mit `pdh_uuid_v7()`,
   Migration 178) – auf `main` und dem Testverbund (18.11.2026).
6. **ESPPDH Paket 1 = Version 0.82.0** (vom Anwender festgelegt): auf `main`
   (19.11.2026; Migrationen 179 bis 181, Rechte `esppdh.view`, `esppdh.edit`,
   `esppdh.admin`; Entwurf `docs/superpowers/specs/2026-11-18-esppdh-design.md`,
   Plan `docs/superpowers/plans/2026-11-18-esppdh-paket1.md`). **0.82.0 bis
   0.82.10 sind auf dem Testverbund** (zuletzt 21.11.2026, Verbund-Update auf
   `72a0e3e`; beide Server 0.82.10, Migrationen 179–185 da, aktiv
   jetzt `pdh` 10.10.0.14). Offen: am Testverbund auf der Seite „Netzwerk“ den
   Haken „Für ESPPDH-Geräte nutzbar“ am Geräte-WLAN setzen, auf beiden Servern
   die Umgebung einrichten, ein Gerät bauen, die Rollen umschalten, die
   Firmware am zweiten Server herunterladen und im Browser prüfen. Paket 2 und
   3 sind nicht begonnen. Geparkt aus der Schlussprüfung: Bau ohne
   `ionice`/Speicherschutz (am Testverbund beobachten), eigener Benutzer für
   den Bau, gekürzter Gerätename bei Kennungen über 24 Zeichen, gespeicherte
   Statusnachricht bleibt nach dem Löschen eines Geräts am Broker.
   **0.82.1: ESPHome hält sich selbst aktuell** – Entwurf
   `docs/superpowers/specs/2026-11-19-esphome-aktuell-design.md`, Plan
   `docs/superpowers/plans/2026-11-19-esphome-aktuell.md` (vom Anwender am
   19.11.2026 freigegeben). Auf `main` (19.11.2026, Merge `ad75fb0`; keine
   Migration, kein neues Recht); auf dem Testverbund. Offen am Testverbund
   (zusammen mit 0.82.0): Umgebung einrichten, „Jetzt
   prüfen“ auf beiden Servern, „Zurück zur vorigen Version“ am Spiegel, eine
   Probe während `systemctl restart pdh`, Reiter „ESPPDH“ im Browser in Fiori
   und Fluent, hell/dunkel, 320–1366 px. Geparkt: Hinweis am Gerät bleibt alt,
   wenn der Schalter aus ist und ein PDH-Update die Grundumgebung anhebt;
   PlatformIO-Zwischenspeicher wächst mit verworfenen Versionen (beobachten);
   Uhrzeit der täglichen Prüfung liegt 24 h nach der letzten (kann in die
   Schicht fallen).
   **Danach 0.82.2: Sortieren in Historie, Maßnahmen und Kommentaren** –
   Entwurf `docs/superpowers/specs/2026-11-19-sortierung-design.md` am
   19.11.2026 freigegeben (vier Festlegungen dort), Plan
   `docs/superpowers/plans/2026-11-19-sortierung.md` (8 Aufgaben, Migration
   182, Branch `feature/sortierung` ab `main` mit 0.82.1). Der Plan behebt
   zwei Lücken im Bestand mit: Löschen einer Maßnahme prüft den Datensatz
   nicht; Historie von Projekt, KVP, Anlage und Lagerort ist ohne
   Ansichtsrecht abrufbar. Auf `main` (19.11.2026, Merge `1cfbe6e`;
   Migration 182, kein neues Recht); auf dem Testverbund, im Browser
   geprüft (19.11.2026: Störung, Ticket, KVP, Anlage, zwei Protokolle,
   Netzwerk, ESPPDH; Fiori/Fluent, hell/dunkel, 320–1366 px; kein
   Überlauf, keine Konsolenfehler; „Weitere laden“ mangels langer Historie
   nicht gesehen). Kosmetische Befunde, mit 0.82.3 behoben: Fiori-Knopf
   mit 2 px Unterrand; KVP-Karte „Do“ bricht die Leiste auf 2–3 Zeilen um;
   Einzelzeile direkt unter einer aufgeklappten Gruppe ohne Abstand;
   Server-Protokoll: Feld „Stufe“ überlappt „bis“ (vermutlich Bestand).
   Auswahlfeld am Handy volle Breite – passt. Testdaten: Ticket und
   KVP-Idee gelöscht; Störung „Abnahme Sortierung Störung“
   (`01a1f566-b4d2-783f-b0f7-e6e42ee3deae`) ist noch da – Störungen haben
   keine Löschfunktion; löschen nur per Datenbank mit Freigabe.
   Geparkt aus der Schlussprüfung (klein): Test zur gemischten Gruppe in der
   Anlagen-Historie greift den Fehlerfall nicht; Hinweis „neueste N“ fehlt,
   wenn der Abteilungsfilter alle geladenen Zeilen entfernt, und liest sich
   mit der Anzahl widersprüchlich; Namen nur aus Satzzeichen sortieren bei
   „Nach Person“ nach vorn; Umlaute sortieren in Listen aus der Datenbank
   anders als in Listen aus dem Programm; Gruppen laden alle Einzeleinträge
   (bis 5000) mit; Gruppentitel der Historie nur deutsch.
   **Nächste Pakete (Entscheidungen des Anwenders vom 19.11.2026):**
   (a) **0.82.3 Rechte:** eigenes Ansichtsrecht für Projekte und für KVP
   (ja); Historie von Aufgaben und Wartungsaufträgen sieht jeder, der den
   Vorgang öffnen kann (ja). Branch `feature/rechte-projekte-kvp`. Entwurf
   `docs/superpowers/specs/2026-11-19-rechte-projekte-kvp-design.md` (mit
   zwei Sicherheitspunkten: Leitstand-Kalender für Gäste, Ordnerlisten unter
   `/uploads/`) – am 19.11.2026 freigegeben („Passt“). Gebaut auf dem Branch
   `feature/rechte-projekte-kvp` (Migration 183, Rechte `projects.view`,
   `kvp.view`). Auf `main` (20.11.2026, Merge `9ee7ac8`) und dem
   Testverbund (Verbund-Update auf `f64c940`; Migration 183, beide Rechte
   bei allen 6 Rollen). Am Testverbund geprüft (20.11.2026): Leitstand-
   Kalender als Gast mit veränderter Adresse liefert dasselbe wie ohne;
   `GET /uploads/` → 404; Rolle ohne die neuen Rechte (Betrachter ohne
   `projects.view`/`kvp.view`, fremdes Projekt und fremder Vorschlag):
   Menü ohne „Projekte“, Liste „Meine Projekte“ ohne das fremde Projekt,
   KVP nur „Meine Vorschläge“ und „Regeln & Vorlagen“ mit „Idee
   einreichen“, Detailseiten und KVP-Druck 403; Fiori/Fluent, hell/dunkel,
   320–1366 px ohne Überlauf. Testdaten und Testrolle entfernt. Der
   Kleinpunkt „Neues Projekt“ ohne `projects.edit` ist mit 0.82.9 behoben.
   Geparkt:
   Copilot-Suche über alle Vorgangsarten scheitert an einem Feld der
   Störungen (Bestand); Partner-Auswahl zeigt höchstens 500 Vorgänge vor dem
   Rechtefilter; KVP-Kennzahlen für Rollen mit Recht nur in einer Abteilung
   ausgeblendet (Erweiterung möglich); Kalender-Personenzeilen lassen
   Vorgänge von Personen außerhalb der Leitstand-Abteilung weg (Bestand);
   `recordDepartments` und `deptScopeFor` geben bei Datenbankfehlern „keine
   Einschränkung“ zurück (Bestand; betrifft Bearbeiten-Rechte im Gate und die
   Abteilungssperre); Benutzer, deren Rolle gelöscht wurde, sehen nach 0.82.3
   nur noch eigene Projekte/Vorschläge – vor dem Einspielen prüfen (am
   Testverbund geprüft: keiner). Aus der Schlussprüfung geparkt: Schreibt
   jemand mit abgelaufener Anmeldung über eine gesperrte Route, kommt eine
   Textmeldung „Bitte neu anmelden“ statt der Weiterleitung (sperrend lassen,
   nur das Aussehen wie `authMiddleware`); `webActor` prüft die bei Wartung
   zurückgezogenen Sitzungen nicht (Bestand); Pfad-Aufteilung auf `/api/v1/`
   enger fassen; `middleware.Auth` prüft nicht, ob der Benutzer noch aktiv
   ist – Schlüssel deaktivierter Benutzer gelten an Schnittstellen-Wegen ohne
   Rechte-Regel bis zum Ablauf (Bestand, Systembenutzer 10 Jahre).
   Vorschlag für später: Anhänge nur mit Anmeldung.
   (b) **0.82.4 Seite „Netzwerk“:** Netze und WLANs aus UniFi und Omada
   vorschlagen, der Anwender bestätigt nur noch; WLAN-Kennwörter werden mit
   übernommen (ja, Handeingabe als Rückfall); Karte „Dieser Server“ mit
   Adresse, Gateway, DNS, fest/automatisch, „Jetzige Adresse fest einstellen“
   und DNS ändern (Stufe 1 reicht, ja). Eine andere Adresse vergeben (Stufe 2)
   ist kein Auftrag. Entwurf
   `docs/superpowers/specs/2026-11-19-netzwerk-vorschlaege-server-design.md`
   – am 19.11.2026 freigegeben („Passt“). Gebaut auf dem Branch
   `feature/netzwerk-vorschlaege` (auf 0.82.3; Migration 184, kein neues
   Recht; pdh-ha meldet `net` und `maint_seq`; Schlussprüfung mit Fix-Welle
   am 20.11.2026 erledigt). Auf `main` (20.11.2026, Merge `f64c940`) und
   dem Testverbund (Migration 184, `net` beider Server und `maint_seq` im
   Status). Am Testverbund geprüft (20.11.2026): Karte beider Server in
   Fiori/Fluent, hell/dunkel, 320–1366 px ohne Überlauf und
   Konsolenfehler; „DNS ändern“ am Spiegel (gleiche Werte, netplan mit
   `00-installer-config.yaml` + `90-pdh-static.yaml`) angewendet nach 3 s,
   bestätigt, keine Ausfälle, keine Wartung am Spiegel; ohne Bestätigung
   nach 2 Minuten zurückgestellt (Dateien wie vorher, Karte meldet es);
   am aktiven Server über die eigene Adresse: Wartung an/aus
   (`maint_seq` 25 → 26 → 27), gemeinsame Adresse rund 2 s weg (IPv4 und
   IPv6 danach wieder da). Der Kleinpunkt „schnelles Bestätigen, bevor die
   gemeinsame Adresse zurück ist“ ist mit 0.82.9 behoben.
   Offen: Vorschläge mit
   echtem UniFi/Omada (Kennwort im Klartext?, Feldnamen, Versionen) – am
   Testverbund sind die Quellen aus. Geparkt: Gemeinsamer Wartungszähler:
   gleichzeitige Änderungen an beiden Servern (später `if_seq` in
   `POST /v1/maintenance`); Konsolen-Dialog nimmt keine Sperre und löscht beim
   Zurücksetzen eine vorher vorhandene cloud-init-Datei (Bestand); WPA2 mit
   mehreren Schlüsseln (UniFi `presharedKeys`) und Omada-PPSK werden nicht
   übernommen; scheitert bei UniFi die Einzelabfrage eines WLANs, gilt
   „versteckt“ als „nein“ (Angleichen könnte den Haken entfernen – „unbekannt“
   wäre richtig); gleiche SSID auf zwei VLANs (zweite wird NEU, Übernehmen
   scheitert sauber); fehlendes `totalCount` bei UniFi liest nur Seite 1;
   netplan-Zusammenführung mit einer festen Adresse in einer anderen Datei
   (`00-installer-config`/`50-cloud-init`) nur am echten System prüfbar – vor
   dem Einsatz auf 10.10.0.202 `/etc/netplan` nur lesend ansehen;
   Bestätigen-Frist 30 s kürzer als der schlimmste Fall des Skripts (Web liest
   neu).
   (c) **0.82.5 Netzlaufwerk-Test und Zeuge** – auf `main` (20.11.2026,
   Merge `7388415`) und dem Testverbund (beide Server und Zeuge 0.82.5; der
   Zeuge zog selbst nach und sieht beide Server): „Testen“ nennt den echten Grund,
   wenn PDH nicht schreiben kann; Bau-Rezept des Zeugen kopiert
   `internal/hostnet` (seit 0.82.4 scheiterte der Bau, der Helfer setzte den
   Zeugen auf 0.82.2 zurück; `TestDockerfileCopiesAllPdhHaPackages`);
   SMB-Laufwerke gehören dem Benutzer der Einheit `pdh` (`User=`/`Group=`
   per `systemctl show`, Rückfall Benutzer „pdh“, sonst 10001, nie root;
   `PDH_MOUNT_UID` hat Vorrang; **0.82.6**: eine gesetzte `PDH_MOUNT_GID`
   gilt auch dann – auf `main`, Merge `bd22fe9`, und dem Testverbund, Zeuge
   selbst nachgezogen). Anlass: Meldung „pdhnas: erreichbar, aber
   PDH darf nicht schreiben“ – am Testverbund schreibt PDH auf „pdhnas“
   (`//10.10.0.202/pdh-nas`) einwandfrei; vermutet auf 10.10.0.202: Dienst
   läuft dort nicht als „pdh“ (prüft der Anwender selbst nur lesend; nach
   dem Einspielen dort das Laufwerk öffnen und „Speichern“ – nicht „Trennen“,
   das schaltet „automatisch einbinden“ aus). **Am 20.11.2026 auf 202
   bestätigt:** `User=michael`, Laufwerk `uid=10001,gid=10001`. **0.82.7**:
   Hinweis in Handbuch und CHANGELOG berichtigt – auf `main` (Merge
   `5154bda`) und dem Testverbund. Auf 10.10.0.202 erledigt (20.11.2026,
   vom Anwender): „pdhnas“ ist verbunden – Punkt abgeschlossen.
   (d) **0.82.8 Einstellungsdatei** – auf `main` (Merge `b303fd4`) und dem
   Testverbund: Seite „Server-Einstellungen“ nennt, warum die Einstellungsdatei
   nicht beschreibbar ist (Besitzer und Dienstbenutzer, auch der Ordner für
   `<datei>.tmp`), samt Befehl, und warnt bei Rechten über 600
   (`internal/web/envfile_check.go`). Anlass: auf 10.10.0.202 gehörte
   `/home/michael/pdh/.env` seit 15.11.2026 04:09 `root:root` mit 644 (vom
   Anwender am 20.11.2026 auf `michael:michael` 600 gestellt); kein Skript im
   Repository legt die Datei so an – vermutlich von Hand als root neu
   geschrieben. Die beiden Meldungen sind nur im Seitentest gesehen (am
   Testverbund ist die Datei in Ordnung, sie erscheinen dort nicht).
   (e) **0.82.9 Reste** – Branch `fix/reste-0829`: Tagesgrenzen mit reinem
   Datum über `localDate` (heutiger Ortstag als Mitternacht UTC, wie pgx
   DATE liefert), Startseiten-Liste über `localDayStart`, Wächter
   `TestNoUTCDayTruncate`; „Neues Projekt“ nur mit `projects.edit`
   (`mayCreateProject` = Rechte-Regel), Migration 185 benennt das Recht in
   „Projekte bearbeiten“ um; „Einstellung behalten“ am aktiven Server erst,
   wenn die gemeinsame Adresse zurück ist (`VIPMissing`, auch im Handler).
   Auf `main` (21.11.2026, Merge `d244bc4`) und dem Testverbund. Am
   Testverbund geprüft (21.11.2026): als Administrator Knopf und Formular
   „Neues Projekt“ da, Recht heißt unter „Rollen & Rechte“ „Projekte
   bearbeiten“; mit einer Rolle ohne `projects.edit` fehlen Knopf und
   Formular; Startseite, Schulungen, Rahmenverträge, Bestellungen,
   Netzwerk und Handbuch in Fiori/Fluent, hell/dunkel, 320–1366 px ohne
   Überlauf und Konsolenfehler; „DNS-Server ändern“ am aktiven Server
   (gleiche Werte): Hinweis „Die gemeinsame Adresse … ist noch nicht
   zurück“ rund 4,5 s, danach erst „Einstellung behalten“, bestätigt,
   Wartung wieder aus. Testrolle entfernt, Prüfkonto gesperrt. Nicht im
   Browser gesehen: Tagesgrenzen zwischen 0 und 1 Uhr (nur automatisch
   getestet). 10.10.0.202 hat der Anwender am 21.11.2026 um 07:20 selbst
   über „Update installieren“ auf 0.82.9 gebracht (`d244bc4`,
   `migrations_ok`; „Sicherung vor Update ausgelassen – Rückfallkopie
   vorhanden“, also Sicherung durch den Update-Agenten).
   Geparkt (Tests, klein): Laufen alle Pakete gleichzeitig gegen die
   Labor-Datenbank, kann ein Test ein Gerät `w-test-…` auf den
   Lasttest-Benutzer `last01` buchen; dann scheitert
   `TestSetupAndCleanupLab` am Aufräumen (einzeln grün; Reste mit
   `go run ./deploy/lab/last -dsn "$PDH_TEST_DSN" -cleanup` entfernen).
   (f) **0.82.10 Bestellliste** – Branch `fix/lagerbestand-widget`: Das
   Startseiten-Widget `list_stock` fragte noch `spare_parts.storage_location`
   ab (seit Migration 027 `storage_location_legacy`) und scheiterte überall;
   Lagerort jetzt aus `spare_part_stock` mit Pfad (`storagePaths`). Gefunden
   im Server-Protokoll von 10.10.0.202 (14× in 3 Tagen). Auf `main`
   (21.11.2026, Merge `72a0e3e`) und dem Testverbund; im Browser geprüft
   (Katalog „Bestellliste“ hinzufügen ohne Speichern, Testteil mit Pfad
   „Lager › Fach 7“ und rot; Fiori/Fluent, hell/dunkel, 320–1366 px, am
   Handy Pfad mit „…“ gekürzt; keine Konsolenfehler, keine Widget-Warnung
   im Protokoll; Testdaten entfernt, Prüfkonto gesperrt). Offen: 202 per
   „Update installieren“ (Anwender). Weitere Befunde dort
   (21.11.2026, nur gelesen), nicht behoben: MQTT-Wertübernahme will bei
   jedem Start einen Broker auf `127.0.0.1:1883`, der nicht läuft (PDH-Broker
   lauscht auf 1888 – Einstellung der Verbindung, Anwender); Grafana-Live
   `/grafana/api/live/ws` antwortet 400 (nginx ohne WebSocket-Weiterleitung,
   vgl. `scripts/configure-pdh-nginx-websockets.sh`); Leitstand meldet
   abgebrochene Anfragen (`context canceled`) als ERR und 500 (nur
   Protokoll-Lärm). SSH an 202 geht als `michael` ohne `sudo -n`
   (Kennwort nötig); Datenbank dort von hier nicht lesbar.
7. **0.81.6 Nummern ab 10000** (Rüstzeit, KVP, Bestellung und Partner waren auf
   vier Stellen abgeschnitten) – mit 0.82.0 auf `main`; der lokale Branch
   `fix/ruestzeit-nummer` ist damit erledigt.
8. **Tagesgrenzen aufräumen:** erledigt mit 0.82.9 (siehe 6 (e)).
9. **Efficio klein:** FTPS ist ungetestet. (Abbruch der Zustellung nach dem
   ersten Fehler ist Absicht – Reihenfolge der Dateien; kein offener Punkt.)
10. **MQTT klein:** Bei älterem Update-Agenten versucht der tägliche Lauf
    die nginx-Seite jeden Tag neu (harmlos).
11. **Abnahmen durch den Anwender:** SNMP an echten Geräten, Omada an
    echtem Controller, TIA-Baustein in PLCSIM, Datennetz mit echtem Tailnet,
    SFTP mit Schlüssel an einem echten Server; ESPPDH an echter Hardware
    (USB-Flash auf einer echten Platine, ein echter Sensor je Baustein, Gerät
    im eigenen VLAN).
12. **Zeitzone im Docker-Testsystem** (Vorschlag, kein Auftrag): Container
    laufen in UTC, Produktion (systemd) in Europe/Berlin. Umstellung nur als
    Paket: `TZ` für app und updater in `compose.yaml`, `time/tzdata` in
    `cmd/server` und `cmd/update-agent`, Postgres-Sitzungszone über pgx
    `RuntimeParams["timezone"]`; nie nur `TZ` an der App, kein `PGTZ`.
13. **Internet-Abhängigkeit der Oberfläche (klein):** Mit 0.82.57 (Branch
    `feature/seitenaufbau`, noch nicht auf `main`) sind htmx, Sortable,
    Gantt, Chart.js, Excel-/Word-Vorschau und alle Schriften der Darstellung
    mitgeliefert (`web/static/vendor/`, `/vendor/<Version>/…`; Wächter
    `TestNoExternalAssets`). Offen bleibt die Content-Security-Policy.
14. **UUID-Regel (klein):** `go test -tags integration ./pkg/database -run
    TestUUIDv7Policy` schlägt auf `main` fehl für `energy_readings`,
    `energy_export_runs`, `datanet_events`, `sync_outbox` (Zahlenfolge) und
    `sync_profiles`, `sync_field_maps`, `sync_links`, `sync_runs`
    (`gen_random_uuid()`). Die ESPPDH-Tabellen halten die Regel ein.
    Umstellen per Migration oder in die Ausnahmen des Tests aufnehmen –
    Entscheidung offen.
15. **Zurückgestellt (kein Auftrag):** Abteilungsfeinheiten Rüstzeiten,
    Datennetz Paket 2/3, Buchungssystem/Lizenzen.
16. **ESPPDH: alle Sensoren, Webserver, Werte an Anlage und Terminal,
    Handshake, Plausibilität** (Wünsche des Anwenders vom 21.11.2026) –
    Entwurf `docs/superpowers/specs/2026-11-21-esppdh-sensoren-webserver-design.md`
    auf Branch `feature/esppdh-sensoren`, **Teil 1 und 2 freigegeben
    (21.11.2026)**, Handshake auf der Anlagen-Schnellübersicht. Drei
    Teile: 0.82.11 Webserver v3 (ohne Durchreiche – Anmelde-Cookie
    `pdh_token` ist im Browser lesbar), Adressmeldung, Reiter „ESPPDH“,
    Sensorwerte am Anlagenterminal (neues Recht `esppdh.values`),
    Handshake-Zähler überall; danach Sensorkatalog aus ESPHome (Ansatz C,
    nicht mehr 0.82.12 – die Nummer ging an Lizenz und Name, siehe 18);
    danach Plausibilität mit Betriebszeiten der Anlage – noch zu besprechen
    (Quelle der Betriebszeiten, „Minimal- und Maximalzeit“). Bauplan Teil 1:
    `docs/superpowers/plans/2026-11-21-esppdh-teil1.md` (8 Aufgaben,
    Migration 186; Werte-Speicher im Broker statt eigenem Abo, weil
    `reconcileMqttConsumer` alle Abos löscht). **Teil 1 (0.82.11) auf
    `main`** (21.11.2026, Merge `be6f57c`) und dem Testverbund, im Browser
    geprüft (20 Ansichten: Reiter „ESPPDH“, Terminal mit und ohne Karte,
    Schnellübersicht; Fiori/Fluent, hell/dunkel). 14 kleine Befunde der
    Schlussprüfung stehen im Ledger `.superpowers/sdd/2026-11-21-esppdh-teil1/`.
    Wichtig: `web_server` braucht `ota: false` – ESPHome lädt sonst über
    `captive_portal` von selbst `ota.web_server` (mit `esphome config`
    2026.10.2 per Docker geprüft). Terminal-Tests laufen mit
    `-tags integration`. Offen: Soll der Kasten einer Unteranlage auf der
    Schnellübersicht gelb werden, wenn ihr Handshake steht? (Anwender);
    Bestand: Karte bei 320 px, Reiterleiste, Punkt in Fiori bei 768 px;
    echte Platine (Anwender). Danach Teil 2.
17. **Eigenes Projekt: ESPPDH-Vorlagen für Messaufgaben** (Wunsch des
    Anwenders vom 21.11.2026, vom Anwender ausdrücklich als separates
    Projekt eingeordnet, noch nicht begonnen): eigene Vorlagen „mit Hilfe“
    erstellen, z. B. Plattenstärke mit zwei Abstandssensoren (Eingabe
    Sollstärke und Toleranz, Kalibrierfunktion, Inbetriebnahmeanleitung mit
    Piktogrammen) oder Längenmessung. Entschieden (21.11.2026, „B“):
    Vorlagen-Editor in ESPPDH, der Copilot im PDH schlägt Sensoren,
    Rechnung und Anleitung vor, der Anwender bestätigt – ohne Update.
    Offen: wer Vorlagen anlegen darf; Vorschlag: Gerät misst, das PDH rechnet und bewertet (kein
    `lambda` auf dem Gerät). Baut auf 16 (Teil 1 und 2) auf.
18. **0.82.12 Lizenz und Name** (Entscheidungen des Anwenders vom
    21.11.2026: „lizenz A“, „pdh pulse data hub“, „Ip Adressen Xx en“):
    AGPL-3.0-or-later mit Ausnahme für erzeugte Ausgaben
    (`LICENSE-EXCEPTION.md`), Lizenzen der Fremdbestandteile in
    `THIRD_PARTY_LICENSES.md`; Name „PDH – Pulse Data Hub“ (Kurzname „PDH“
    in der Kopfleiste bleibt); interne Adressen in allen Dateien außer
    dieser durch Beispieladressen ersetzt (`10.10.0.x` → `192.0.2.x`,
    IPv6-Präfix → `2001:db8::`, `/home/michael` → `/home/pdh`). Hier in
    AGENTS.md bleiben die echten Adressen. **Lastprüfung:** vor Läufen
    gegen den Testverbund `export PDH_LAST_PRODUCTION=10.10.0.202` setzen –
    ohne die Angabe lässt `deploy/lab/last` nur Ziele auf diesem Rechner zu,
    eine Angabe, die keine IP-Adresse ist, stoppt den Lauf, Rechnernamen
    werden aufgelöst. **Auf `main`** (21.11.2026, Merge `0cb3b6e`, Sperre
    `2523579`) und dem Testverbund (beide Server und Zeuge 0.82.12, aktiv
    jetzt `pdh` 10.10.0.14, Spiegelung läuft, Anmeldeseite zeigt „PDH –
    Pulse Data Hub“).
    Offen vor einer Veröffentlichung: private Domain in 29 Dateien (u. a.
    Nextcloud-App), Name/Benutzer in Tests, Handbuch-Skizze und
    `tiamqtt/testdata/Daten.db`, VLAN-Beispiele `10.10.x.0/24` (echte
    Netze?), Git-Verlauf (ZIPs), AGENTS.md und `docs/superpowers`; vier
    Lizenzfragen (englischer Text maßgeblich, Link „Quellcode“ in der
    Oberfläche, `go-modules-licenses.txt` behalten, Test für die
    Vollständigkeit von `THIRD_PARTY_LICENSES.md`); Markenprüfung
    „Pulse Data Hub“ angeboten.
19. **Firmenstammdaten = 0.82.21, Korrektur 0.82.22** (0.82.21 legte nach dem Öffnen des Reiters „Firma“ alle Seiten mit 500 lahm – `h.tmpl` direkt ausgeführt; Wächter `TestNoDirectSharedTemplateExecute`) (Wunsch des Anwenders vom 21.11.2026; 0.82.16 und 0.82.18 gingen an andere Pakete, zuletzt die PDH-App):
    Fenster am obersten Knoten der Infrastruktur; die Daten gelten
    unternehmensweit im Mail- und Postverkehr, der angemeldete Benutzer ist
    Ansprechpartner. Entschieden: „A jetzt, B danach“ (A: alle Mails mit
    Absendername und Fußzeile samt Pflichtangaben, Briefkopf in Druck und
    Wartungsprotokoll, {Firma} der M365-Signaturen; B: Bestellung als PDF an
    den Lieferanten als eigenes Paket); Variante 1: eine Firma, Werke werden
    untergeordnet (eigene Angaben eines Werks später mit B am Werk-Knoten).
    M365: {Firma} bleibt `%%Company%%` („B“), neue Platzhalter als fester
    Text. Entwurf `docs/superpowers/specs/2026-11-21-firmenstammdaten-design.md`,
    Plan `docs/superpowers/plans/2026-11-21-firmenstammdaten.md` (beide vom
    Anwender bestätigt), gebaut auf `feature/firmenstammdaten` (Migration 193,
    Recht `company.edit`, lokal; Schlussprüfung mit Fix-Welle erledigt). Offen: Freigabe Push/Merge,
    Verbund-Update, Browserprüfung (Reiter, Druckkopf, Mail-Fußzeile,
    Wartungsprotokoll, M365-Seite). Danach Paket B (Bestellung als PDF). Befund: mehrere oberste Knoten
    möglich, kein Typ „Unternehmen“, keine eigenen Adress-, Steuer- oder
    Bankdaten; Firmenname liegt doppelt (`branding.app_name`,
    `m365_company_name`).
20. **0.82.15 MQTT-Name sicher einrichten** – Anlass: Störung auf 10.10.0.202
    am 21.11.2026 12:36 (nach Port-Änderung und Name `mqtt.strobl-home.net`:
    MQTT-Seite des Agenten mit `listen [::]:443`, Seite `pdh` nur IPv4, Tunnel
    `https://localhost:443` → `::1` → falsches Zertifikat → 502; Tunnel-Zeile
    MQTT blieb auf Port 8083). Sofort-Hilfe auf 202 erledigt (Zeile
    `listen [::]:443 ssl;` in `/etc/nginx/sites-enabled/pdh`, Sicherung
    `/root/nginx-pdh.bak-20261121`); vorübergehende sudo-Regel
    `/etc/sudoers.d/90-claude-wartung` – vom Anwender am 24.11.2026
    gelöscht. Offen auf 202: Tunnel-Zeile
    MQTT im Reiter Cloudflare-Tunnel nachziehen (Anwender). Entwurf
    `docs/superpowers/specs/2026-11-21-mqtt-name-sicher-design.md`, Plan
    `docs/superpowers/plans/2026-11-21-mqtt-name-sicher.md`, Branch
    `fix/mqtt-name-sicher` (lokal): Agent prüft Zertifikate anderer Namen
    vor/nach dem Neuladen (Rückfall IPv4, sonst Rücknahme), IPv6 nur wenn alle
    anderen 443-Seiten IPv6 haben, Tunnel-Ziele `127.0.0.1`, neue/geänderte
    Namen „vorgemerkt“ (`int_public_pending`) bis Zertifikat und Firewall
    grün, dann scharf (nginx + Tunnel), Port-Änderung zieht Tunnel nach. Kein
    neues Recht, keine Migration. **Auf `main`** (21.11.2026, Merge `a44e091`,
    Schlussprüfung mit Fix-Welle) und dem Testverbund (beide Server und Zeuge
    0.82.15, aktiv jetzt `pdh-server` 10.10.0.17; alle nginx-Seiten dort mit
    IPv6). **0.82.17**: Name im Haus ist keine Pflicht mehr zum Scharfschalten
    (Anwender: „MQTT ohne DNS ok“), nur Hinweis. Offen: Browserprüfung der
    Prüfliste, Einspielen auf 202 (Anwender, mit Sicherung). Geparkte kleine
    Befunde der Schlussprüfung: Seite fehlt bis zum Tageslauf, wenn der
    Zertifikatsauftrag beim Scharfschalten noch läuft; gleichzeitiges Speichern
    kann die Vormerkung zurückholen; schon veraltete Tunnel-Zeile wird nicht
    nachgezogen (Prüfliste zeigt sie); Tunnel-Ziel ohne Sperre; Knopf am
    Spiegel/in Wartung stumm; IPv6-Erkennung je Datei statt je server-Block;
    Knopf bis ca. 75 s.
21. **0.82.25 ESPPDH: Firmware übers Netz aufspielen** (Anwender 22.11.2026:
    „PDH schiebt sie“, „a + b“, „1a 2a“; 0.82.23 und 0.82.24 gingen auf `main` an die
    Rüsttasten am Terminal und die PDH-App). Entwurf
    `docs/superpowers/specs/2026-11-22-esppdh-ota-design.md`, Plan
    `docs/superpowers/plans/2026-11-22-esppdh-ota.md`, Branch
    `feature/esppdh-ota` (lokal): Firmware mit `ota: platform esphome`
    (Kennwort je Gerät, `ota_password_enc`), `safe_mode` und Info-Wert
    `info/firmware` (Kennung des Baus, nicht im `ConfigHash`); Knopf
    „Firmware aufspielen“ (Recht `esppdh.ota` am Gerät; nur bestätigte
    Adresse in freigegebenem Netz mit Adressbereich, Erfolg erst bei
    Meldung der neuen Kennung, bis 3 min); Sammelaktion „Alle veralteten
    Geräte aktualisieren“ (Recht überall; veraltet = Konfiguration geändert
    oder andere Kennung gemeldet). Migration 194, Aufträge in
    `esppdh_ota_runs`, ausgeführt vom Bau-Arbeiter. Dazu: Startseite zeigt
    Firma und Ort aus den Firmenstammdaten statt fest „Werk Augsburg“. **Auf
    `main`** (22.11.2026, Merge `cc80dbc`) und dem Testverbund (beide Server
    und Zeuge 0.82.25, Migration 194, aktiv jetzt `pdh` 10.10.0.14). Im
    Browser geprüft: Startseite mit und ohne Firmendaten, ESPPDH-Übersicht,
    Handbuch; Fiori/Fluent, hell/dunkel, 320–1366 px ohne Überlauf und
    Konsolenfehler; Testdaten entfernt, Prüfkonto gesperrt. Nicht im Browser
    gesehen (am Testverbund kein ESPPDH-Gerät, ESPPDH aus): Karte „Firmware
    übers Netz“ und Sammelaktion – nur automatisch getestet. Offen: Abnahme
    an echter Platine (einmal USB, dann übers Netz, Port 3232/8266 in der
    Firewall); 10.10.0.202 einspielen (Anwender, mit Sicherung).
    **0.82.29 Freigabe in UniFi** (Anwender 22.11.2026: „Auf Knopfdruck“,
    „Ist kein Eintrag möglich Ticket an it“; Entwurf
    `docs/superpowers/specs/2026-11-22-esppdh-unifi-freigabe-design.md`,
    Plan `docs/superpowers/plans/2026-11-22-esppdh-unifi-freigabe.md`,
    Branch `feature/esppdh-unifi-freigabe`): Knopf beim Netz der Seite
    „Netzwerk“ legt die Regel „PDH – ESPPDH-Update <Netz>“ über die offizielle
    UniFi-Schnittstelle an (`firewall/zones`, `firewall/policies`, ab Network
    10; Felder aus developer.ui.com 10.6.106), sonst Ticket an die IT
    (Empfänger der IT-Netz-Alarme, Schlagwort `esppdh-freigabe:<netz>`).
    Recht `system.network_admin`, keine Migration. **Auf `main`** (22.11.2026,
    Merge `a9c0e89`) und dem Testverbund (beide Server und Zeuge 0.82.29,
    aktiv jetzt `pdh-server` 10.10.0.17); Seite „Netzwerk“ mit Testnetz im
    Browser geprüft (Fiori/Fluent, hell/dunkel, 320–1366 px, kein Überlauf,
    keine Konsolenfehler; Knopf nicht gedrückt – sonst Mail an die IT;
    Testdaten entfernt, Prüfkonto gesperrt). Offen: Abnahme an einem
    echten UniFi-Controller (Feldnamen, besonders `protocol.name: tcp`, sind
    nur nach der Dokumentation gebaut – am Testverbund sind die Quellen aus);
    Omada nur Ticket. Hinweis:
    der oberste Infrastruktur-Knoten heißt am Testverbund noch „Werk
    Augsburg“ (Beispielknoten aus Migration 005). Geparkt aus der
    Schlussprüfung: erneutes Aufspielen desselben Baus meldet sofort „ok“;
    Beenden während des Wartens heißt „nach 3 min nicht gemeldet“;
    Sammelaktion bei ausgeschaltetem ESPPDH „fehlgeschlagen“ statt
    „abgebrochen“; Arbeiter-Kontext kann beim Start nil sein; StopESPPDH
    wartet nicht auf die Sammelaktion; Liste der Sammelaktion langsam;
    Adresswechsel während des Uploads (Restrisiko); anonymer Broker kann
    Bau-Kennung vortäuschen.
22. **PDH-App (Android/iOS):** Entwurf
    `docs/superpowers/specs/2026-11-21-pdh-app-design.md` am 21.11.2026
    freigegeben (Branch `feature/pdh-app`): eine Installations-App für alle
    Server (Domain-Abfrage), Flutter, Zugang per APP-Ticket und
    gerätegebundenem Token, Rechte `app.*`, Offline mit Schritten und
    UUIDv7 vom Telefon, zentraler Push-Dienst `pdh-push`, sieben Pakete.
    **Paket 1 (Server-Grundlage, 0.82.18) auf `main`** (21.11.2026, Merge `98d41ff`; Testverbund und 10.10.0.202 noch nicht eingespielt): Migration 189 (`189_app_grundlage`),
    Rechte `app.use`, `app.create`, `app.cockpit`, `app.sensors.view`, `app.admin` (Gruppe
    „App-Administratoren“), Schnittstelle `/api/app/v1`, Seiten
    „App-Konfiguration“ und „App-Geräte“, „App installieren“ im
    Benutzermenü, Kapitel „PDH-App“ im Handbuch. Nächster Schritt: Testverbund aktualisieren und im Browser prüfen,
    dann **Paket 2 (Flutter-App)**. Für Paket 2 merken:
    Die App erzeugt nach einer Sperre oder dem Zurücksetzen einen neuen
    Schlüssel; die erste schreibende App-Route muss `mirrorGate` und den
    Rückzug der Sitzungen bei Wartung beachten.
    **Paket 2 (Server-Schnittstellen, 0.82.24)** auf Branch
    `feature/pdh-app-paket2` (noch nicht auf `main`): Vorgänge, Scannen,
    Störung melden, Schreibwächter (Spiegel nur lesen, Wartung, einmalige
    Ausführung per `X-PDH-Once`), Handbuch „Mit der App arbeiten“. Die App
    liegt im privaten Repository `strobobaer/pdh-app` (lokal
    `C:\workplace\pdh-app`, Branch `master`; offene Punkte in
    `docs/offene-punkte.md`); iOS ist nicht gebaut (braucht einen Mac). Geparkt: Begrenzung
    je Gerät, Aufbewahrung von `request_once` gegen Offline (Paket 3),
    Fehlertypen im App-Modul, doppelter `assetOpenItems`-Code,
    Signierschlüssel für Releases.
    **Paket 3 (Arbeiten ohne Verbindung), Server fertig auf Branch
    `feature/pdh-app-paket3` (0.82.37, noch nicht auf `main`):** Migration 205
    (`205_app_offline`, ursprünglich 194, dann 195, wegen Kollisionen mit
    `194_esppdh_ota` und `195`–`204` auf `main` umnummeriert), Schritte
    genau einmal 90 Tage (`app_steps`), Kennungs-Aliase,
    Störung mit Telefon-`id` und `offline` (an offene Störung der Anlage
    angehängt), Zusammenführ-Regeln mit Stand (`base`), Hinweis und Nachtrag,
    `GET /api/app/v1/assets` (Anlagenliste für den Offline-Speicher), Handbuch
    „Mit der App ohne Verbindung arbeiten“. **Die App (Teil A, Branch
    `feature/paket3` in `pdh-app`, Version 1.1.0+2) folgt.** Testdatenbank
    `pdh_test_app` hat 194 noch als `194_app_offline` vermerkt (nicht von Hand
    geändert); `194_esppdh_ota` ist dort nicht eingespielt. Für die Prüfung
    nach dem Merge mit main dient die frische Labor-DB `pdh_test_app4`.
    Entschieden (Anwender, 21.11.2026: „Manager passt“): Manager behalten
    `app.admin` in allen Abteilungen (Freigabe/Sperre bleibt durch die
    Rangfolge begrenzt).
    Geparkt aus der Schlussprüfung (klein): Indizes auf
    `tickets.app_request_id`/`app_devices.request_id` (nur per neuer
    Migration); ein freigegebenes Gerät könnte den Nonce-Speicher (100 000 je
    10 min) füllen → Begrenzung je Gerät; Nonce-Speicher und Begrenzer liegen
    je Server (vor schreibenden Routen klären); keine Obergrenze offener
    Anfragen; `/me` liefert keine Abteilungs-ID; `last_seen_at`-Schreiben am
    Spiegel; `appStoreConfigured` je Seite ohne Zwischenspeicher; Render-Test
    für `/app/setup` ohne Integrations-Tag; Pfad-Präfix im `pdh://`-Link;
    `CloseTx` ohne Prüfung der betroffenen Zeilen; Testlabor: eigene
    Datenbank `pdh_test_app` (in `pdh_test` liegen fremde Migrationen).
23. **0.82.32 Herkunft und Kachelfarbe** (Wunsch des Anwenders vom 22.11.2026,
    Entwurf `docs/superpowers/specs/2026-11-22-herkunft-kachelfarbe-design.md`,
    Plan `docs/superpowers/plans/2026-11-22-herkunft-kachelfarbe.md`, bestätigt
    „Sonst war alles ok“; Branch `feature/herkunft-kachelfarbe`, lokal):
    Spalten `origin`/`origin_label` an Tickets und Störungen (Migration 202,
    vorher 201 – die Nummer ging an die Terminal-Funktionen), Paket
    `internal/core/origin`, jeder Anlege-Weg setzt sie; Anzeige über
    `record_origin.go` (Kachelzeile „davon …“, Schilder, Detailseite;
    Profilname nur mit `sync.view`); Kachelfarbe als `bg` in der
    Widget-Einstellung (acht helle Töne, dunkel gedämpft). **Auf `main`**
    (22.11.2026, Merge `6b6524a`) und dem Testverbund (beide Server und Zeuge
    0.82.32, aktiv jetzt `pdh` 10.10.0.14; Nachtrag: 7 Abgleich-Tickets,
    keine Änderungsmeldungen). Im Browser geprüft: farbige Kacheln,
    Herkunftszeile „davon 6 aus Störmeldungen ERP (Abnahme) · 1 aus der App ·
    1 vom Terminal“, Schilder in der Ticket-Liste, Herkunft auf der
    Ticket-Seite; Fiori/Fluent, hell/dunkel, 320–1366 px ohne Überlauf und
    Konsolenfehler; Test-Tickets still entfernt, Startseite des Prüfkontos
    zurückgesetzt, Prüfkonto gesperrt. Beim Terminal steht dessen Anlage,
    nicht der Gerätename. Geparkt: REST-Anlage ohne Herkunft, Texte nur
    deutsch, Herkunft in der JSON-Schnittstelle nur beim Anlegen,
    Zählabfrage bei vielen Abgleich-Vorgängen, Fehltreffer im Nachtrag bei
    kopiertem Terminal-Text. 10.10.0.202 hat der Anwender selbst auf 0.82.32
    gebracht (22.11.2026: „202 ist uptodate“).
24. **Copilot beim Anlegen und Zuordnen** (Wunsch 22.11.2026: „C“,
    „Anthropic“, dazu MySQL, InfluxDB, SQLite, Vorlagen aus dem Internet,
    berechnete Werte mit Formelgenerator und Gültigkeit von/bis; Entwurf
    `docs/superpowers/specs/2026-11-22-copilot-zuordnen-design.md`, bestätigt
    „Sonst war alles ok“). Pakete: 1 Zuordnen (Vorschläge am Formular ohne
    Sprachmodell + Assistent mit Änderungsliste), 2 berechnete Werte, 3
    InfluxDB, 4 Anlegen von Verbindungen und Abgleich. InfluxDB: alle
    Versionen (1.x InfluxQL, 2.x Flux, 3.x SQL; Anwender 22.11.2026:
    „influxdb alle möglich“). **Paket 1 = 0.82.35** (geplant als 0.82.33; main hatte inzwischen 0.82.34 „Folie Website“ mit Migration 203) auf Branch
    `feature/copilot-zuordnen` gebaut (23.11.2026, lokal; Plan
    `docs/superpowers/plans/2026-11-22-copilot-zuordnen-paket1.md`, 9 Aufgaben,
    Migration 204 `copilot_proposals` (vorher 203), kein neues Recht): Knopf „Zuordnungen
    vorschlagen“ im Reiter Zuordnungen/Felder (Rechenkern
    `internal/core/mapsuggest`), Sammel-Anlage `…/mappings/bulk`,
    Copilot-Werkzeuge auf der Verbindungsseite (`copilot_mapping.go`),
    Änderungsliste mit „Übernehmen“ (`POST /copilot/proposals/{id}/apply`),
    Websuche nach Vorlagen. Neu für alle Wege (von Hand, Vorschlagsliste,
    Copilot, TIA-Baustein, „Vorlage anwenden“, Löschen): Zuordnen nur an
    Anlagen der eigenen Abteilung (`assetGuardFor`); doppelte Zuordnung von
    Hand wird gemeldet. Mit behoben: „Vorlage anwenden“ legte keine
    Zuordnungen an (Abfrage ohne Typangaben). Schlussprüfung mit Fix-Welle
    erledigt (23.11.2026); geparkte kleine Befunde im Bericht
    `.superpowers/sdd/2026-11-22-copilot-zuordnen-paket1/final-review.md`
    (u. a. Einheit „kW“ nur geraten, MQTT-Mitschnitt 3 s).
    **0.82.35 auf `main`** (23.11.2026, Merge `3034875a`) und dem Testverbund
    (beide Server und Zeuge 0.82.35, Migrationen 203 und 204 da, aktiv jetzt
    `pdh-server` 10.10.0.17, Spiegelung läuft). Am Testverbund im Browser
    geprüft (23.11.2026): Knopf „Zuordnungen vorschlagen“ an „MQTT-Broker
    Test“ liefert den Hinweis „In 3 Sekunden kam keine Nachricht“ (dort
    sendet nichts), 20 Ansichten ohne Überlauf und Konsolenfehler; Prüfkonto
    gesperrt, keine Testdaten. **Der Copilot läuft am Testverbund mit Ollama
    (nicht erreichbar)** – Änderungsliste, „Übernehmen“ und Websuche sind dort
    nicht echt gelaufen, nur automatisch getestet und an einer lokal
    gerenderten Seite angesehen (20 Ansichten). **0.82.36 auf `main`**
    (23.11.2026, Merge `e111380f`) und dem Testverbund (beide Server und
    Zeuge 0.82.36, aktiv jetzt `pdh` 10.10.0.14, Spiegelung läuft):
    Vorschlagsliste bis 900 px Breite untereinander statt breiter Tabelle;
    Anthropic-Hinweis nur mit Claude (`copilotConnRef`); **Internetsuche
    technisch getrennt**: nur mit Haken „Im Internet nach einer Vorlage
    suchen“, als eigener Aufruf ohne PDH-Werkzeuge
    (`faults.AskWithWebTemplate`; Websuche zusammen mit eigenen Werkzeugen
    lehnt `AskWithToolsOpts` ab). Am Testverbund im Browser geprüft
    (23.11.2026): Vorschlagsliste in 20 Ansichten ohne Überlauf und
    Konsolenfehler, Hinweis und Haken fehlen dort richtig (Ollama); Prüfkonto
    gesperrt, keine Testdaten. Offen: Abnahme des Copilot mit echtem
    Anthropic-Schlüssel (Anwender, z. B. auf 10.10.0.202: Auftrag „Ordne … zu“,
    Änderungsliste, „Übernehmen“, Haken für die Vorlage aus dem Internet);
    Vorschlagsliste mit echten MQTT-Themen und OPC-UA-Knoten; 10.10.0.202
    einspielen (Anwender, mit Sicherung – erst ab 0.82.36). Geparkt (Tests):
    `go run ./deploy/lab/last -cleanup` scheitert an
    `it_assets_created_by_fkey`, Reste des Lasttests liegen in der
    Labor-Datenbank.
    **Paket 2 (berechnete Werte = virtuelle Messwerte):** Anwender
    23.11.2026: „verschiedenste Berechnungen … als virtuellen Messwert“,
    „A passt“ (Weg A: berechneter Wert ist eine Zuordnung an der eingebauten
    Quelle „Berechnete Werte“). Entwurf
    `docs/superpowers/specs/2026-11-23-berechnete-werte-design.md` auf Branch
    `feature/berechnete-werte` (lokal, geprüft von einem unabhängigen
    Prüfer) – **vom Anwender am 23.11.2026 bestätigt („passt“)**. Damit
    entschieden: Schnitt 2a (jetzt) und 2b (Gültigkeit und rückwirkendes
    Neurechnen für die berechneten Energie-Messstellen danach, eigener
    kurzer Entwurf); Schnellübersicht bleibt bei der heutigen Regel; kein
    neues Recht; Energie-Messstellen behalten in 2a ihren Rechenumfang.
    Bauplan `docs/superpowers/plans/2026-11-23-berechnete-werte.md`
    (9 Aufgaben). **Paket 2a = 0.82.45** auf Branch
    `feature/berechnete-werte` gebaut (23.11.2026; geplant als 0.82.37 mit
    Migration 205 – `main` hatte inzwischen 0.82.44 und die Migrationen 205
    bis 210, deshalb **Migration 211** `211_calc_values`; Push und Merge vom
    Anwender für 0.82.45 freigegeben): Rechenkern in `internal/core/formula`
    (Namen ohne Klammern, Vergleiche, `^`, Funktionen; die Energie-Messstellen
    lehnen den neuen Umfang ab, `ErrFormulaScope`); Modul
    `internal/modules/calc` (Speicher mit Fassungen, Rechenwerk alle 10 s und
    nach jedem Anstoß); alle Konnektoren schreiben Werte nur noch über
    `storeMappingValue` (Wächter `TestMappingValueSingleWriter`); Seite der
    eingebauten Verbindung „Berechnete Werte“ (`calc_values.go`, Wege
    `/import/calc/…`); genutzte Eingangswerte sind auf allen drei Löschwegen
    geschützt (Wächter `TestMappingDeletesGuarded`); Copilot-Werkzeuge
    `calc_values`, `calc_inputs`, `propose_calc` (`calc_copilot.go`). Kein
    neues Recht. Offen: Freigabe Push/Merge, Verbund-Update, Browserprüfung
    am Testverbund (Seite, Formular am Handy, Änderungsliste des Copilot –
    dort läuft der Copilot mit Ollama, also wieder nur lokal ansehbar),
    10.10.0.202 (Anwender). Danach Paket 2b (Gültigkeit und rückwirkendes
    Neurechnen für die berechneten Energie-Messstellen, eigener kurzer
    Entwurf; dort auch klären, ob Messstellen den neuen Rechenumfang
    bekommen). Kleine Befunde der Schlussprüfung stehen im Ledger
    `.superpowers/sdd/2026-11-23-berechnete-werte/`.
    **0.82.45 ist auf `main`** (23.11.2026, Merge `d935c5e9`, vom Anwender
    freigegeben) und dem Testverbund (beide Server 0.82.45, Migration 211);
    im Browser geprüft (23.11.2026, 40 Ansichten ohne Überlauf und
    Konsolenfehler; Prüfkonto gesperrt, keine Testdaten). Offen: 10.10.0.202
    (Anwender, mit Sicherung), Copilot-Abnahme mit Anthropic-Schlüssel.
    **Paket 2b (Fassungen der Messstellen-Formel, rückwirkendes Neurechnen):**
    Entwurf `docs/superpowers/specs/2026-11-24-energie-fassungen-design.md`
    auf Branch `feature/energie-fassungen`, vom Anwender am 24.11.2026
    bestätigt („2b: 1 Vorschlag, 2a, 3–5 passt“): rückwirkend bis zum
    ältesten gespeicherten Wert; nicht verschickte Efficio-Dateien neu
    erzeugen, verschickte bleiben und werden gelistet; geplante Fassungen mit
    `energy.edit`, vergangene nur `energy.admin`; erweiterter Rechenumfang
    nur für Momentanwerte; Copilot schlägt nur vor, nie eine vergangene
    Fassung. Bauplan `docs/superpowers/plans/2026-11-24-energie-fassungen.md`
    (8 Aufgaben; geplant als Migration 214, Version 0.82.50 – `main` hatte
    inzwischen 0.82.53 und die Migrationen 214 bis 217 der Terminal-Teile,
    deshalb **0.82.54, Migrationen 218 und 219**). **Gebaut** (24.11.2026,
    Anwender: „Selbst“; Freigabe Push/Merge erteilt):
    Migrationen 218 (`energy_formula_versions`, `energy_recalc_runs`,
    `first_end`/`last_end`/`fetched_at` an den Efficio-Dateien) und 219
    (`energy_rebases`, Zeitpunkte der Basis-Verschiebung); kein neues Recht.
    Labor-Datenbank `pdh_test`: Einträge 214/215 → 218/219 umgeschrieben (Schema war da).
    Ein Rechenweg je Minute (`evalMinute`), Sperre `minuteLock` für Minutentakt
    und Schlussschritt, Neurechnung Tag für Tag (`recalc.go`), Efficio-Dateien
    neu erzeugen (`rebuild.go`), Oberfläche „Fassungen“ an der Messstelle
    (`energy_versions.go`, `widgets/energy_meter_versions.gohtml`).
    Schlussprüfung mit Korrekturrunde erledigt (W1 Übergabe unter der Sperre,
    W2 Basis-Verschiebungen, W3 Warnung bei späten Bezügen, K4 Formular behält
    geltende Formel, K5 Werte vor der ersten Fassung bleiben); kleine Befunde
    K1–K3, K6–K10 und alle Rulings im Ledger
    `.superpowers/sdd/2026-11-24-energie-fassungen/` (`final-review.md`).
    Fragment lokal gerendert in 20 Ansichten geprüft (Fiori/Fluent, hell/dunkel,
    320–1366 px, kein Seitenüberlauf). **Auf `main`** (24.11.2026, Merge
    `c2352abe`; Prüfung: main stand bei 0.82.53, eingearbeitet, Nummern
    umgestellt) und dem Testverbund (Verbund-Update auf `c2352abe`; beide Server
    und Zeuge 0.82.54, Migrationen 214–219, aktiv jetzt `pdh-server` 10.10.0.17,
    Spiegelung läuft). Damit sind auch die Terminal-Teile 0.82.50–0.82.53 der
    anderen Sitzung am Testverbund. Im Browser geprüft (Testmessstellen
    EVTEST-A/-V per SQL): Fragment „Fassungen“ in 20 Ansichten ohne Überlauf,
    geplante Fassung anlegen und löschen, „Prüfen“ (Vorschau 3 Tage, 1 Messstelle),
    „Neu rechnen“ nach 10 s fertig, Werte stimmen (V = 1 + 2 × Zuwachs A);
    Testdaten entfernt, Prüfkonto gesperrt. Offen: 10.10.0.202 (Anwender, mit
    Sicherung).
    **Danach (Anwender 24.11.2026):** (1) „Copilot soll das können ja“ –
    Copilot schlägt Fassungen vor (Änderungsliste, „Übernehmen“, nie in der
    Vergangenheit); eigenes Paket, erst Entwurf. (2) „Es muss schneller Seiten
    aufbauen, vielleicht die Icons nicht immer laden, sondern erst ohne und im
    laufenden Betrieb nachladen“ – Befund: `tabler-icons.min.css` 1,4 MB mit
    eingebetteter Schrift blockiert das Zeichnen, dazu htmx von unpkg.com und
    die Schrift von Google Fonts (siehe Punkt 13). Gemessen am Testverbund
    (24.11.2026, Edge, angemeldet): im Zwischenspeicher 0,06–0,1 s (4× gedrosselt
    0,35–0,4 s); **erster Aufruf mit Symbolen 6,7–7,0 s, 4× gedrosselt 29–33 s**;
    erster Aufruf ohne Symbole 0,14–0,2 s (0,46–0,53 s). Die Datei lädt in
    0,04 s – die Zeit geht ins Entpacken der eingebetteten Schrift (5 918 Symbole,
    genutzt werden einige Hundert). **Teil 1 (Seitenaufbau) = 0.82.57**
    gebaut auf Branch `feature/seitenaufbau` (24.11.2026, lokal; keine
    Migration, kein neues Recht): Symbole als SVG-Masken statt der
    Symbolschrift (`internal/web/iconcss.go`), Bibliotheken und Schriften
    mitgeliefert, Messskript `deploy/lab/seitenaufbau/messung.js`.
    Schlussprüfung (Claude Opus, dann Korrekturrunde: Rückfall-Maske gegen
    gefüllte Quadrate, gewählte Symbole sofort eingebettet, fremde Version
    302 `no-store` statt 404, `/vendor/` leise im Protokoll) und
    Codex-Prüfung erledigt; kleine Befunde und alle Entscheidungen im
    Ledger `docs/superpowers/notes/2026-11-24-seitenaufbau-ledger.md`
    (u. a. HEAD für `/vendor` 405, `gzip;q=0` nicht beachtet, Infoleiste
    lädt Einzel-SVG, `adoptGrafana` ohne `iconChoicesChanged`). **Auf
    `main`** (24.11.2026, Merge `f808cecf`, Dauerfreigabe) und dem
    Testverbund (beide Server und Zeuge 0.82.57, aktiv jetzt `pdh-server`
    10.10.0.17). Abnahme am Testverbund (Bericht
    `docs/superpowers/notes/2026-11-24-seitenaufbau-abnahme.md`): erster
    Aufruf 1× CPU load 0,23–0,28 s, Symbole 0,29–0,44 s (vorher 6,7–7,0 s)
    – Ziel erfüllt; 4× CPU Symbole 1,32–1,83 s (vorher 29–33 s) – Ziel
    1,5 s auf `/`, `/energy/meters`, `/tickets` knapp verfehlt; nur Host
    10.10.0.15:8090, 605 `/vendor`-Antworten alle 200; 120 Ansichten ohne
    Überlauf, Konsolenfehler oder Symbol ohne Maske; unbekannter Name in der
    Symbolauswahl ohne Quadrat; Kontrastmodus und Druck mit Symbolen; alte
    Adressen 302 `no-store`. Stildatei 590 KB roh, 64 KB gzip. Offen:
    10.10.0.202 über die Cloudflare-Adresse (Anwender; nach dem Einspielen
    offene Seiten einmal neu laden).
    **0.82.58 Symbole nachladen** (Anwender „Ja“; Entwurf
    `docs/superpowers/specs/2026-11-24-symbole-nachladen-design.md`): Hauptdatei
    `icons-<key>.css` nur Grundregeln + eingebettete Symbole (187 KB roh,
    22 KB gzip), Restdatei `/vendor/<ver>/icons-rest.css` mit
    `:where(.ti-x)`-Verweisen für alle 5 918 Symbole (unabhängig von der
    Auswahl, nach `load` nachgeladen, `<noscript>`-Rückfall). Auf `main`
    (24.11.2026, Merge `63682850`) und dem Testverbund (aktiv jetzt `pdh`
    10.10.0.14). Messung danach (Median aus 3, erster Aufruf): 1× load
    0,26–0,46 s, Symbole 0,34–0,53 s; 4× load 1,06–1,46 s (alle ≤ 1,5 s),
    Symbole 1,25–1,66 s (enthält 200 ms Ruhefenster des Messskripts). Der
    Rest liegt nicht mehr an den Symbolen: `base.gohtml` schickt bei jedem
    Aufruf rund 72 KB CSS und 130 KB JavaScript inline mit, nicht
    zwischenspeicherbar – Auslagern in versionierte Dateien gehört zur
    Gestaltungsgrundlage (0.90.0 Teil 2).
    **Tempo (Anwender 24.11.2026):** Dauerfreigabe Testverbund (siehe
    „Prüfung vor jedem Merge“); **ein** gemeinsamer Entwurf für die Teile
    Gestaltungsgrundlage, gleiche Bausteine und Navigation mit klickbarer
    Vorschau; Stammdaten und Betriebssystem-Updates parallel (eigener
    Arbeitsordner; Codex kann hier nicht bauen, Claude baut, Codex prüft);
    Folien wie PowerPoint erst nach 0.90.0 (0.90.x).
    Bestandsaufnahme für „Stammdaten zusammenführen“:
    `docs/superpowers/notes/2026-11-24-stammdaten-bestandsaufnahme.md`.
    (3) **Einheitliche Oberfläche** (Anwender 24.11.2026, auf den Vorschlag
    „kein Menüband wie Word/Excel, sondern Fiori-Muster plus Schnellstart“):
    „Zusammenfassen was zusammen gehört und trennen was getrennt werden muss,
    überall gleich. Gleiche Funktionen und Buttons. Mit Ingress-Funktion für
    Addons und Optionalem wie Nextcloud … das wäre dann Stand 0.9.0“. Inhalt:
    Suchzeile „Was möchtest du tun?“ (Strg+K), Schnellstart-Kacheln je Rolle,
    gleiche Seitenmuster (Liste mit Filterleiste, Detailseite mit Kopf und
    Reitern, Aktionen immer an derselben Stelle), Ingress für Zusatzdienste
    (Nextcloud, Grafana … im PDH-Rahmen hinter der PDH-Anmeldung). Großes
    Paket: erst Entwurf, in Teilpakete geschnitten. Version: **0.90.0**
    (Anwender 24.11.2026: „2 ja 0.90.0“); die Teile kommen vorher als
    Korrekturversionen. Mitliefern statt Internet: ja („1 ja mitliefern“).
    Vorschau (Entwurfsfläche „PDH – neue Oberfläche: Vorschau Fiori und
    Outlook“, A Fiori-Muster, B Outlook-Aufbau). **Richtung entschieden**
    (24.11.2026): „SAP Grundgerüst und Outlook Listen usw. In SAP viele Widgets
    die einfach zu kombinieren sind“ – Rahmen und Seitenmuster nach Fiori,
    Vorgangslisten am breiten Bildschirm mit Ordnern links und Detail rechts
    (Handy/Terminal: Detailseite), Start- und Übersichtsseiten aus
    gleich gebauten, frei kombinierbaren Widgets im Raster, je Rolle vorbelegt.
    Teile: 1 schneller Seitenaufbau (Entwurf
    `docs/superpowers/specs/2026-11-24-seitenaufbau-design.md`, Weg 1 „Symbole
    als SVG“ gewählt), 2 gleiche Bausteine, 3 Ordnung der Navigation,
    4 Schnellstart und Widgets, 5 Ingress für Zusatzdienste, 6 Folien wie
    PowerPoint. Weitere Vorgaben (Anwender 24.11.2026): „Copilot wie in
    Microsoft rechte Seitenleiste. Reiter für Untermenüs wie die Tabs von Edge.
    Navigation wie die Nennleistung in Microsoft, Ingress als Mittelframe,
    Filter wie Tabs und Folien wie PowerPoint-Präsentation und erstellen wie in
    PowerPoint mit viel Schnickschnack zum Einblenden und Überblenden, bedienbar
    per Touch und herkömmlich“. Nachgefragt und entschieden: Navigation
    „Beides – Menüband für Funktionen wie Speichern, Öffnen, Einstellungen,
    Ansichten, Anpassung, Darstellungsbearbeitung“ (links Navigationsleiste für
    die Bereiche, oben Menüband mit den Funktionen der Seite); Reiter wie Edge
    „Beides“ (Unterseiten als feste Reiter, geöffnete Vorgänge als eigene,
    schließbare Reiter). Vorschau C „So wird es“ auf der Entwurfsfläche.
    Weitere Vorgaben (Anwender 24.11.2026): „Alle Bremsklötze und Doubletten in
    Datenbank und Ansicht ausmerzen, zusammenführen und so weiter – Lieferant
    und Hersteller nur einmal, Kostenstelle nur einmal usw. Alle Auswahlfelder
    sollen über einen Menübandeintrag erweitert/gefüllt werden können.
    Berechtigung blendet ein oder aus. Designauswahl verfeinern.“ Bestandsaufnahme
    der Doppelungen läuft (Lese-Agent), daraus Teil „Stammdaten zusammenführen“.
    Statusleiste (Anwender 24.11.2026): „Den Fortschrittsbalken weiterhin
    verwenden, nur soll er auch beim Update den Status anzeigen, Bearbeitungen
    und Warnungen als Text ausgeben“ – die vorhandene Fußleiste
    (`global-status-footer` in `base.gohtml`) zeigt künftig Update-Stand
    (Einzelserver und Verbund, Neustart ohne „Netzwerkfehler“), jede Aktion in
    Klartext mit echtem Fortschritt bei langen Läufen und dauerhafte Warnungen
    (Wartung, Spiegel, Verbindung); Text auch am Handy/Terminal, Tippen zeigt die
    letzten Meldungen. Gehört zu Teil „gleiche Bausteine“.
    Gestaltung (Anwender 24.11.2026): „Mache die Gestaltung so, dass es nicht
    nach KI aussieht, sondern einfach nur besser als SAP und Microsoft, aber
    trotzdem echt, und das fixiert, dass alles neu aufgebaut werden kann.“ –
    eigener Teil **Gestaltungsgrundlage** vor den Bausteinen: Gestaltungswerte
    als Datei im Repository (Farben Fiori/Fluent × hell/dunkel, Abstände 4/8 px,
    eine Schrift mit wenigen Größen, Rundungen, Schatten nur für Schwebendes),
    Bausteinkatalog mit Regeln, Tests erzwingen „nur Werte und Bausteine aus
    der Grundlage“; zum Ansehen auch im Design System des Anwenders in Claude.
    Kein KI-Look: keine Verläufe, Glas, bunten Schatten, Deko, Emojis,
    Seitenstreifen-Karten, Fülltexte; Farbe nur mit Bedeutung; echte Dichte.
    Listen (Anwender 24.11.2026: „ganz wichtig, bei der Sortierung sollte auch
    die Infrastruktur erscheinen“): jede Vorgangsliste sortiert und gruppiert
    nach Infrastruktur in Reihenfolge des Anlagenbaums (Unteranlagen unter
    ihrer Anlage), Gruppenköpfe mit Anlagenpfad und Anzahl, aufklappbar; jede
    Zeile zeigt die Anlage mit Pfad (am Handy gekürzt); Infrastruktur auch als
    Filter-Reiter/Ordner, immer mit Unteranlagen. Gehört zu „gleiche Bausteine“.
25. **0.82.48 Wartung: Jahresplan im Kalender, Auftrag mit Vorlauf** (Wunsch des
    Anwenders vom 23.11.2026: Personalplanung; Knopf „Jahresplan erzeugen“,
    nur der nächste Termin wird 5 Arbeitstage vorher automatisch zum
    Durchführungsauftrag, einstellbar unter „Wartungsregeln“; geplante
    Termine verschieb- und besetzbar „Ja“; Einzelheiten „Passt“; Weg A
    eigene Tabelle; nach der Prüfung „1a 2a 3a 4a“). Entwurf
    `docs/superpowers/specs/2026-11-23-wartung-jahresplan-design.md` auf
    Branch `feature/wartung-jahresplan` (vom Anwender bestätigt „Passt“), Plan
    `docs/superpowers/plans/2026-11-23-wartung-jahresplan.md` (11 Aufgaben),
    gebaut (23.11.2026, lokal). Geplant als 0.82.46 mit Migration 212 –
    `main` hatte inzwischen 0.82.47 und `212_terminal_free_user`, deshalb
    **0.82.48, Migration 213** (`213_maintenance_yearplan`; Tabelle
    `maintenance_visits`, `lead_workdays`, `upcoming_from` u. a.; kein neues
    Recht – Jahresplan und Termine mit `maintenance.edit` am Plan,
    Wartungsregeln mit `maintenance.admin` überall). Eine Feiertagsliste
    `internal/core/workdays`. Der Folgeauftrag entsteht erst mit dem Vorlauf
    (Test `TestBoardMaintenanceFlowIntegration` angepasst). Ledger mit allen
    Entscheidungen: `.superpowers/sdd/2026-11-23-wartung-jahresplan/`.
    Schlussprüfung erledigt (1 kritisch, 3 wichtig – behoben mit Tests;
    13 kleine im Ledger zurückgestellt, Bericht `final-review.md`).
    **Auf `main`** (24.11.2026, vom Anwender freigegeben, Merge `859d2db9`).
    Der Merge-Commit trägt versehentlich die Nachricht des Merges von
    0.82.36 (alte Nachrichtendatei); Inhalt und Eltern stimmen, nicht per
    Force-Push berichtigt. Richtiges Prüfergebnis: origin/main 0.82.47
    eingearbeitet, Migration 212 → 213, Version → 0.82.48, keine Dopplungen,
    Tests grün. Künftig die Nachrichtendatei vor dem Merge neu schreiben.
    Auf dem Testverbund (24.11.2026, Verbund-Update auf `859d2db9`; beide
    Server und Zeuge 0.82.48, Migrationen 212 und 213, aktiv jetzt
    `pdh-server` 10.10.0.17, Spiegelung läuft). Im Browser geprüft:
    Plan anlegen, „Jahresplan erzeugen“ („12 Termine geplant“), Plan-Editor,
    Pläne, Wartungsregeln, Jahresplan, Kalender, Planer – je 20 Ansichten
    (Fiori/Fluent, hell/dunkel, 320–1366 px) ohne Überlauf, keine
    Konsolenfehler; Fenster zum Besetzen öffnet bei 1366 und 390 px und passt;
    Zeitstrahl am Leitstand zeigt die Termine. Testplan entfernt, Prüfkonto
    gesperrt. Offen: 10.10.0.202 (Anwender, mit Sicherung). Labor-Datenbank
    `pdh_test`: Eintrag 212 → 213 umgeschrieben (Schema war schon da).
    **0.82.49 Nacharbeit** auf Branch `fix/wartung-nacharbeit` (24.11.2026,
    lokal; keine Migration, kein neues Recht): die drei Befunde der
    Browserprüfung (Titel des Besetzen-Fensters ohne Zieh-Hinweis über
    `data-tip`; Fenster mittig – `base.gohtml` setzt alle Ränder auf 0,
    deshalb `margin:auto`; Leitstand `/global` bricht die Kopfzeile bis
    1050 px um) und aus der Schlussprüfung M2 (Planer merkt „von Hand
    besetzt“ nur bei wirklich anderer Person, keine zweite Nachricht), M3
    (Termin inzwischen beauftragt wird gemeldet, `visitChanged`), M4
    (`pdhGridPost` liefert true/false, Fenster bleibt nach Fehler offen), M9
    (ein Plan mit Fehler hält den stündlichen Abgleich nicht auf) und M13
    (Verlauf mit Namen, `staffText`). **Auf `main`** (24.11.2026, vom
    Anwender freigegeben, Merge `1da297fe`; Prüfung: origin/main unverändert,
    keine Dopplungen) und dem Testverbund (beide Server und Zeuge 0.82.49,
    aktiv jetzt `pdh` 10.10.0.14, Spiegelung läuft). Im Browser geprüft
    (angemeldet): Leitstand in 20 Ansichten (Fiori/Fluent, hell/dunkel,
    320–1366 px) ohne Überlauf; Besetzen-Fenster im Kalender bei 390 und
    1366 px mittig, Titel ohne Zieh-Hinweis, bleibt bei einem Fehler offen
    („Nicht gespeichert – das Fenster bleibt offen.“), schließt nach dem
    Speichern; keine Konsolenfehler außer dem absichtlich erzeugten.
    Testplan entfernt, Prüfkonto gesperrt. Offen: 10.10.0.202 (Anwender,
    mit Sicherung). Weiter zurückgestellt (Ledger): M1, M5 bis M8, M10 bis
    M12.
26. **Betriebssystem-Updates im PDH** (Anwender 24.11.2026: „Eigener Knopf +
    Haken“, „Wie beim PDH-Update“, „Ja, bei Sicherheitsupdates“, Entwurf
    „Passt“): Karte „Betriebssystem“ in den Core-Einstellungen, Knopf
    „Betriebssystem aktualisieren“ und Haken „beim PDH-Update mit
    aktualisieren“; Einzelserver wie das PDH-Update (Countdown, Sperre,
    Rückfallkopie, Pakete, Neustart nur bei `/var/run/reboot-required`,
    `/health`), im Verbund nur am passiven Server mit Rollentausch;
    Wartungsfenster spielt mit Haken Sicherheitsupdates auch ohne PDH-Update
    ein; neues Recht `system.os_update`. Entwurf
    `docs/superpowers/specs/2026-11-24-betriebssystem-updates-design.md` auf
    Branch `feature/os-update` im Arbeitsordner `D:/pdh-os-update` (ab
    `main` 0.82.56). **Teil A (Einzelserver, Karte, Recht) = 0.82.59 auf
    `main`** (24.11.2026, Merge `a2d61851`, Dauerfreigabe) und dem
    Testverbund (beide Server 0.82.59, Migration 222, Recht da, aktiv jetzt
    `pdh-server` 10.10.0.17). Wartungsfenster (Anwender 24.11.2026: „Dann alle
    offenen Pakete“): sind Sicherheitsupdates offen, spielt es mit Haken alle
    offenen Pakete ein. „Neustart nötig“ ohne offene Pakete: der Knopf startet
    geordnet neu. apt läuft entkoppelt vom Agenten (Ausgabe nach
    `/run/pdh-os/`, kein `--pipe`). Schlussprüfung: Codex + Claude-Gegenprüfung,
    Schlusskorrektur nachgeprüft; Ledger
    `docs/superpowers/notes/2026-11-24-betriebssystem-updates-teil-a-ledger.md`.
    **Wichtig:** Das erste Update auf 0.82.59 läuft noch mit dem alten
    `install_update.sh` und installiert `/usr/local/sbin/pdh-os-update` noch
    nicht (erst ab dem nächsten Update von selbst; bis dahin meldet die Karte
    „veraltet“). Am Testverbund am 24.11.2026 von Hand eingespielt
    (`sudo install -m 0755 -o root -g root /opt/pdh-server/scripts/pdh-os-update
    /usr/local/sbin/pdh-os-update`). Auf 10.10.0.202 nach dem Update auf
    0.82.59 (Anwender, mit Sicherung):
    `sudo install -m 0755 -o root -g root /home/michael/pdh/scripts/pdh-os-update
    /usr/local/sbin/pdh-os-update`. Im Verbund zeigt die Karte nur den Hinweis
    „kommt mit einer späteren Version“ (noch kein Paketstand) – mit Teil B
    nachziehen. Zurückgestellt (Ledger): Einmal-Token nicht an die Art
    gebunden, Migration ohne Einzelrechte, „Letzter Lauf: 0 Pakete … ok“ nach
    jedem PDH-Update mit Haken, Warten im Wartungsfenster hält `drTick` bis
    15 min, `StandardOutput=append:` braucht systemd ≥ 240. **Teil B
    (Verbund: nur am passiven Server mit Rollentausch) ist nicht begonnen.**
    **0.82.60 Neustart unter Debian, zurückgehaltene Pakete** (Anwender
    25.11.2026: „Ja, 0.82.60 bauen“, NVIDIA „Zurückhalten“; Anlass: Prüfung
    von 10.10.0.202): Skript Version 3 meldet „Neustart nötig“ auch bei einem
    strikt neueren installierten Kernel derselben Variante (Debian legt keine
    `/var/run/reboot-required` an), zählt `apt-mark hold` getrennt
    (`held_count`, je Architektur) und spielt sie nicht ein. Ohne eingespielte
    Pakete startet nur der Knopf neu (`osSteps.AllowBareReboot`, Nullwert =
    nie), nie das PDH-Update mit Haken oder das Wartungsfenster. Keine
    Migration, kein neues Recht. Codex zweimal, alle wichtigen Befunde behoben;
    zurückgestellt: `sort -V` wertet „045“ und „45“ gleich, Variante bei Namen
    wie `6.12.118-rt2-amd64`. Ledger
    `.superpowers/sdd/2026-11-25-os-neustart-debian/`. Auf `main`
    (25.11.2026, Merge `a58186c6`) und dem Testverbund (beide Server und
    Zeuge 0.82.60, aktiv jetzt `pdh` 10.10.0.14, Spiegelung läuft). Das Update hat
    `/usr/local/sbin/pdh-os-update` Version 3 auf beiden Servern **von selbst**
    eingespielt – ab einem Stand mit 0.82.59 entfällt die Handarbeit.
27. **10.10.0.202 Stand 25.11.2026** (Prüfung auf Wunsch des Anwenders):
    Port 443 ist im Haus wieder erreichbar (Firewall vom Anwender). Im Haus
    zeigen `pdh.strobl-home.net` und `cloud.strobl-home.net` per DNS (IPv4)
    direkt auf 202, IPv6 weiter auf Cloudflare. Die Seite `pdh` nutzt jetzt
    das Let's-Encrypt-Zertifikat des PDH über
    `/etc/nginx/snippets/pdh-le-ssl.conf` statt `cloudflare-ssl.conf`
    (Origin-Zertifikat, Browser vertrauen ihm nicht; Gitea nutzt es weiter;
    Sicherung `/root/nginx-pdh.bak-20261125`). Die Nextcloud-Seite
    `nextcloud-cloudflare.conf` lauscht zusätzlich auf `443`/`[::]:443`
    (Sicherung `/root/nginx-nextcloud.bak-20261125`) – im Haus ohne
    Gerätezertifikat, nur Anmeldung; `scripts/repair-nextcloud-cloudflare-origin.sh`
    würde die beiden Zeilen wieder entfernen. Beides vom Anwender eingetragen,
    im Browser „sicher“. NVIDIA: Quadro P400 (Pascal) wird vom installierten
    Treiber 620 (CUDA-Archiv, nur 615/620 verfügbar) nicht mehr unterstützt
    („No such device“); `nvidia-persistenced` abgeschaltet, Ollama läuft auf
    dem Prozessor. Debians Treiber 550 (Bereich `non-free`) könnte die Karte –
    nur auf Wunsch. 202 steht inzwischen auf 0.82.65 (26.11.2026 gelesen, vom
    Anwender eingespielt). Offen (Stand 25.11.2026): 57 offene Pakete (2
    Sicherheitsupdates `zstd`, `libzstd1`; Docker-Pakete starten Container neu).
    **Tests:** `TestITNetRunOnceNotConfigured` und
    `TestTerminalGlobalSlidesJSON` sind auf `main` rot (auch einzeln, gegen die
    Labor-Datenbank `pdh_test`; vermutlich Testdaten) – noch nicht untersucht.
    `TestITNetRecipientsFallback` ist im vollen Lauf ab und zu rot (einzeln
    grün; parallele Tests legen `it.admin`-Benutzer in `pdh_test` an).
    Labor-Datenbank: Am 25.11.2026 stand dort eine alte `223_ui_defaults_permission`
    (abgebrochener Testlauf vor dem Umnummerieren) – Eintrag gelöscht, danach
    `223_terminal_shift_plan` und `224` eingespielt.
28. **0.90.0 Einheitliche Oberfläche** (Anwender 25.11.2026: Übergang „A und
    beides bleibt“ – „bis alles fertig ist“, Menüs „Bereiche gleich,
    Favoriten“, Entwurf „OK“, Plan „passt“): Entwurf
    `docs/superpowers/specs/2026-11-25-einheitliche-oberflaeche-design.md`
    (9 Bereiche, Menüband, Reiter wie Edge, Strg+K, Statusleiste, Listen nach
    Infrastruktur, Startseite je Rolle, Dienste im Mittelrahmen; Festlegungen
    in Abschnitt 10), klickbare Vorschau
    `docs/superpowers/specs/2026-11-25-oberflaeche-vorschau.html` („Das sieht
    doch spitze aus“), sieben Teilpakete. **Teil 1 Gestaltungsgrundlage =
    0.82.62** (Plan `docs/superpowers/plans/2026-11-25-gestaltungsgrundlage.md`,
    Ledger `.superpowers/sdd/2026-11-25-gestaltungsgrundlage/`; geplant als
    0.82.61 mit Migration 223 – `main` hatte inzwischen 0.82.61 Terminal mit
    `223_terminal_shift_plan`, deshalb Migration 224, Recht
    `system.ui_defaults`): **auf `main`** (25.11.2026, Merge `52633bb3`) und dem
    Testverbund (beide Server und Zeuge 0.82.62, aktiv jetzt `pdh-server`
    10.10.0.17, Spiegelung läuft). Abnahme: 4 gerenderte Seiten alt/neu
    verglichen, 8640 berechnete Stilwerte in Fiori/Fluent × hell/dunkel ×
    390/1366 px ohne Unterschied (nur die Uhrzeit beim Rendern); HTML je Seite
    rund 164 KB kleiner; am Testverbund 58 Menüseiten × 2 Breiten ohne
    Konsolen- oder Skriptfehler und ohne Überlauf; Seite „Gestaltung“ geprüft;
    Prüfkonto wieder gesperrt. Regeln siehe Abschnitt „Gestaltungsgrundlage
    (seit 0.82.62)“. Zurückgestellt (Ledger): `pdh-boot` ohne try/catch,
    Cache-Schlüssel nur die Version (einmalig beim Rollentausch 404 auf
    `/vendor/…/ui/` möglich), Farben-Wächter zählt `ui/*.js` nicht,
    Inline-Budget 16 KB großzügig, Katalog-Test ohne Route mit Recht,
    `color-mix()` erst ab Chromium 111, `widgets/kvp.gohtml` ganz ausgenommen,
    alte Seite + neue Skripte während eines Updates (ein Seitenaufruf).
    „Darstellung verfeinern“ (Dichte, Modus wie System) kommt mit Teil 2.
    Nicht gemessen: 4×-Drosselung mit `deploy/lab/seitenaufbau/messung.js`.
    **Teil 2 „Neuer Rahmen hinter dem Schalter“ = 0.82.65** (gebaut als 0.82.63; main hatte inzwischen 0.82.64 mit Migration 225, deshalb 0.82.65 und Migration 226) (Plan
    `docs/superpowers/plans/2026-11-25-rahmen-neu.md`, Ledger
    `.superpowers/sdd/2026-11-25-rahmen-neu/`; Migration 226 `users.ui_frame`,
    `users.ui_areas`; kein neues Recht, Firmenvorgabe mit `system.ui_defaults`):
    zweite Wurzelvorlage `frame.gohtml` (Auswahl `frameTemplateName`, Wächter
    `TestNoDirectBaseRender`), gemeinsame Teile `widgets/frame_shared.gohtml`;
    Bereichsleiste mit 9 Bereichen (`nav_areas.go`, Vorgabe Start, Vorgänge,
    Anlagen, Material …), Favoriten und Reihenfolge je Benutzer (Firmenvorgabe nur
    Reihenfolge), Reiter, Menüband (`ribbon.go`), Strg+K (`/api/palette`,
    Anlagen mit `hasPermRec`), Statusleiste mit Meldungen und Update-Stand
    (`pdh:maint`). Schalter im Benutzermenü (klassisch / neu / wie
    Firmenvorgabe), nicht am Terminal des Systembenutzers (auch am Server
    gesperrt); Firmenvorgabe unter Verwaltung → Gestaltung. Schlussprüfung durch
    Claude und Codex, danach vier Korrekturrunden mit Gegenprüfung (Sicherheit, CSS, JS)
    und Browser-Nachmessung (lokal gerendert, Edge: rund 500 Ansichten, 320–1366
    px, 70–160 %, Fiori/Fluent, hell/dunkel; Kopfleiste: Uhr und Benutzermenü
    immer ganz sichtbar; klassischer Rahmen pixelgleich). Zurückgestellt
    (Ledger): Version/Logo fehlen im neuen Rahmen, feste Ecken/Abstände in
    `frame.css`, Strg+K ohne „Lädt …“/Zeitlimit und mit Rechteabfrage je
    Treffer, gleiche Fehler in „Letzte Meldungen“ nicht zusammengefasst,
    „Update läuft“ schon in der Nachfrage-Phase, Strg+K im eingebetteten
    iframe. Bestand (auch klassisch): Statusleiste und Kopfleiste mit langem
    Titel bei 768 px und Größe 130–160 %. **Auf `main`** (25.11.2026, Merge
    `0943c7f8`) und dem Testverbund (beide Server und Zeuge 0.82.65, Migrationen
    225 und 226, aktiv jetzt `pdh` 10.10.0.14, Spiegelung läuft). Am Testverbund
    im Browser geprüft (246 Ansichten): klassisch → neu → klassisch → wie
    Firmenvorgabe; alle 55 Unterseiten der Bereichsleiste bei 390/1366 px ohne
    Fehler und Überlauf; Startseite, Störungen, Anlagen-Detail, Server-Einstellungen
    bei 320–1366 px in Fiori/Fluent, hell/dunkel, dazu 130/160 %; Strg+K mit
    Anlagenpfad, Favorit, Bereiche ordnen, „Wie System“, Statusleiste und
    simulierter Countdown; Neu-anlegen- und Fertigstellen-Assistent geöffnet;
    Prüfkonto zurückgestellt und gesperrt, keine Testdaten. Kleinbefunde (mit Teil 3
    beheben): Balkenbeschriftung „Fertig100 %“ ohne Abstand (frame.css
    `display:block` nimmt das `gap`), „Verwaltung“ in der Leiste gekürzt,
    Statusleiste am Handy ohne Text (pdh.css blendet Status und Fehler aus –
    Entwurf 5.2 verlangt Text), Neu-anlegen-Assistent schließt nach Öffnen über
    „Neu“ nicht per Esc (Fokus außerhalb, Bestand), Schatten der eingeklappten
    rechten Leiste ragt bis 1024 px in den Rand (Bestand). **Nächster Schritt: Teil 3** (Listen
    mit Detail, Vorgangs-Reiter, Vorgänge in Strg+K, „Fertigstellen“ im
    Menüband – Punkt 10.11 des Entwurfs). 10.10.0.202 ist auf 0.82.65
    (26.11.2026 gelesen: Commit `bf7da911`, `migrations_ok`; vom Anwender
    eingespielt).

29. **Rundgang/Wartung pausieren und auf anderem Gerät fortsetzen** (Wunsch des
    Anwenders vom 25.11.2026: „wenn dieses aus mehreren Stationen besteht
    zwischendurch zu pausieren und an einem anderen Terminal, App, Smartphone
    oder in pdh selbst weiter zu machen bis zum Ende“). Erst Entwurf. Bestand
    (Erkundung 25.11.2026): Rundgang = Plan mit `is_round`, Stationen an
    `maintenance_plan_checklists.infrastructure_id`/`maintenance_task_steps.station_infra_id`;
    jeder Punkt, Foto und Material wird sofort gespeichert, der
    Fertigstellen-Assistent springt beim Wiederöffnen zum ersten offenen Punkt.
    Lücken: kein Zustand „pausiert“ („Geht noch weiter“ verlangt Kommentar und
    legt eine Maßnahme an), Zeit/Team/Kommentar nur im Browser bis zum Absenden,
    Terminal findet Rundgänge nur über die Bereichs-Anlage (nicht die Station),
    Terminal-Token 5 min und Kartenwechsel verwirft den Assistenten, App ohne
    Stationen und ohne Zeitbuchung, Schritte werden bis zum ersten Wert bei
    jedem Öffnen neu angelegt (zweites Gerät, Spiegel), keine Sperre (letzter
    Schreiber gewinnt), Zeit an drei Stellen (Timer, `work_sessions`, App).
    Offene Frage an den Anwender: macht nach der Pause nur dieselbe Person
    weiter oder auch ein Kollege (Schichtwechsel)?
31. **0.82.66 Abmelden zieht das Token am Server zurück** (Anwender 25.11.2026:
    „2 ok“; bei der Abnahme von 0.82.65 gefunden: `/logout` löschte nur die
    Cookies, ein kopiertes Token galt bis zum Ablauf). Fingerabdruck (SHA-256
    über Kopf und Inhalt des Tokens, ohne Signatur) in `session_revocations`
    (Migration 227) bis zu dessen Ablauf, gemeinsam für beide Server; 30 s
    Zwischenspeicher je Server (`session_revoke.go`). Geprüft wird in der
    Oberfläche (`sessionToken`, `webActor`) **und an der Schnittstelle**
    (`middleware.Auth` über `SetSessionRevokedCheck`, angemeldet in
    `terminalRoutes`; `apiTokenClaims`). Zurückgezogen wird bei „Abmelden“, bei
    „Zurück zum Systemnutzer“ und am Ende der Kurzsitzung aus dem Leitstand;
    das Token, zu dem zurückgekehrt wird, nie. Jedes Token hat seitdem eine
    zufällige Kennung (`jti`). Nur dieses Gerät, andere bleiben angemeldet; am
    Spiegel (nur lesen) kennt nur dieser Server den Rückzug. Kein neues Recht.
    Schlussprüfung (26.11.2026, Claude und Codex in zwei Runden) fand drei
    wichtige Lücken – Schnittstelle prüfte nicht, Fingerabdruck hing an der
    Schreibweise der Signatur, Schutz des Rückkehr-Tokens verglich den ganzen
    String – alle behoben. **Auf `main`** (26.11.2026, Merge `96d11792`,
    Dauerfreigabe) und dem Testverbund (beide Server 0.82.66, Migration 227,
    aktiv jetzt `pdh-server` 10.10.0.17, Spiegelung läuft, keine Fehler im
    Protokoll). Am Testverbund abgenommen (ohne Browser, mit dem Prüfkonto):
    zwei Anmeldungen ergeben verschiedene Tokens; nach dem Abmelden gilt das
    kopierte Token weder in der Oberfläche (302) noch an der Schnittstelle
    (401, als Bearer und als Cookie), auch nicht mit anderer Schreibweise der
    Signatur und auch nicht direkt am Spiegel; das zweite Gerät bleibt
    angemeldet; Handbuch zeigt den neuen Satz; Prüfkonto wieder gesperrt.
    Zurückgestellt (klein): über 10 000 gleichzeitig geltende Rückzüge im
    Zwischenspeicher eines Servers (dann fragt jede Prüfung die Datenbank);
    Prüfung je Prozess statt je Handler (wie die Terminal-Prüfungen); vor dem
    Update in derselben Sekunde ausgestellte gleiche Tokens; offene
    Live-Verbindungen laufen nach dem Abmelden weiter, bis sie getrennt
    werden. „Überall abmelden“ wäre ein eigener Wunsch. Offen: 10.10.0.202
    (Anwender, mit Sicherung). Tests: `TestUpdateSaveFailsBacksOff` und
    `TestITNetRecipientsFallback` waren nach einem abgebrochenen Testlauf
    einmal rot (Reste in `pdh_test`), danach auf Branch und `main` je dreimal
    grün.
30. **Vorgabe Assistenten (Anwender 25.11.2026):** Neu-anlegen- und
    Fertigstellen-Assistent dürfen in keinem Teil der neuen Oberfläche fehlen
    (Entwurf 0.90.0 Abschnitt 10.11, `TestFrameHasWizards`).

Docker Desktop startet nach einem Neustart des Windows-Rechners nicht von
selbst – ohne ihn fehlen der Zeuge und die Labor-Datenbank für die Tests.

## Sprache

Antworten, Handbuch, Änderungsprotokoll, Commit-Nachrichten und
Pull-Request-Texte auf **Deutsch**, für Anwender im Betrieb verständlich.
