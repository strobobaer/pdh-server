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

## Sprache

Antworten, Handbuch, Änderungsprotokoll, Commit-Nachrichten und
Pull-Request-Texte auf **Deutsch**, für Anwender im Betrieb verständlich.
