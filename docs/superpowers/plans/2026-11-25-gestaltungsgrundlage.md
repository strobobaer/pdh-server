# Gestaltungsgrundlage (0.90.0 Teilpaket 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** CSS und JavaScript aus `base.gohtml` in versionierte, zwischenspeicherbare Dateien auslagern, die Gestaltungswerte in einer Datei bündeln, Wächter-Tests einführen und eine Bausteinkatalog-Seite anlegen – für Benutzer sichtbar nur als schnellerer Seitenaufbau.

**Architecture:** Neue Dateien unter `web/static/ui/`, ausgeliefert über `/vendor/<Version>/ui/<Datei>` (gleiches Muster wie `vendor.go`: Version im Pfad, ein Jahr zwischenspeicherbar, fremde Version → Weiterleitung ohne Zwischenspeicher). Inline-Blöcke ohne Vorlagen-Platzhalter wandern unverändert in Dateien; Blöcke mit Platzhaltern lesen ihre Werte aus einem JSON-Block `pdh-boot`. Wächter-Tests halten Inline-Anteil und feste Farbwerte klein.

**Tech Stack:** Go (chi, html/template), bestehende `serveAsset`/`loadStaticAsset`, Tests mit `httptest`.

**Spec:** `docs/superpowers/specs/2026-11-25-einheitliche-oberflaeche-design.md` (Abschnitte 5.1, 6.1, 7, 8, 10.4)

## Global Constraints

- Version: nächste Korrekturversion über `origin/main` (Stand 25.11.2026: `0.82.60` → **`0.82.61`**); vor dem Merge erneut prüfen.
- Für Benutzer **keine sichtbare Änderung**: Fiori/Fluent × hell/dunkel sehen genau aus wie vorher (CSS unverändert verschoben, nur Variablen-Blöcke umsortiert).
- Nichts aus dem Internet (`TestNoExternalAssets` bleibt grün).
- Neue Dateien in `web/static/ui/` mit LF; Go-Dateien `gofmt`.
- Kein Recht ohne Migration; Migration **223** `223_ui_defaults_permission.up.sql`, Eintrag in `migrations/checksums.txt` per `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- Neues Recht `system.ui_defaults`, Bezeichnung „Oberfläche: Vorgaben für Firma und Rollen“, Kategorie „System & Sicherheit“, an alle Rollen mit überall geltendem `system.manage_roles` (Muster wie `222_os_update_permission.up.sql`).
- Handbuch und CHANGELOG im selben Schritt; Texte Deutsch, für Anwender.
- Tests in Git Bash mit `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'`.

## Review Focus

1. Seite bleibt über ein Update offen und fordert `/vendor/<alte Version>/ui/pdh.css` an → Weiterleitung auf die laufende Version, nie 404 (Test in Aufgabe 1).
2. Ein ausgelagertes Skript nutzt eine Funktion oder Variable eines anderen Blocks: Reihenfolge und Position der `<script>`-Elemente bleiben gleich, Skripte ohne `defer`/`async` (Prüfung in Aufgabe 2 und 3, Browserprüfung ohne Konsolenfehler).
3. Eingebettete Ansicht (`?embed=1`, `html.pdh-embedded`) und Druck (`@media print`) behalten ihre Regeln (Test in Aufgabe 1: Stilverweis auch bei `embed=1`).
4. Langsame Verbindung: kein Aufblitzen ungestalteter Seiten – der Stilverweis steht im `<head>` vor `pdh-theme` (Test in Aufgabe 1).
5. Werte mit Sonderzeichen (Name mit „<“, Anführungszeichen) im Datenblock `pdh-boot` bleiben gültiges JSON und lösen kein Skript aus (Test in Aufgabe 3).

---

### Task 1: UI-Dateien ausliefern, CSS auslagern

**Files:**
- Create: `web/static/ui/pdh.css` (Inhalt von `base.gohtml` Zeilen 48–803 und 806–886, in dieser Reihenfolge, unverändert)
- Create: `internal/web/ui_assets.go`, Test `internal/web/ui_assets_test.go`
- Modify: `internal/web/vendor.go` (Route), `web/templates/base.gohtml` (Zeilen 47–887 durch einen Stilverweis ersetzen), Template-Funktionen (dort, wo `vendorURL` als Vorlagenfunktion registriert ist)

**Interfaces:**
- Produces: `func uiURL(name string) string` → `"/vendor/" + pdh.Version() + "/ui/" + name`; Vorlagenfunktion `uiURL`; Handler `func (h *Handler) UIAssetWeb(w http.ResponseWriter, r *http.Request)`, Route `r.Get("/vendor/{ver}/ui/{file}", h.UIAssetWeb)` in `RegisterVendorRoutes` **vor** der `{lib}`-Route; erlaubte Endungen `.css`, `.js`; Quelle `loadStaticAsset("ui/" + file)`.

- [ ] **Step 1: Tests schreiben** – `TestUIAssetServed`: `GET /vendor/<pdh.Version()>/ui/pdh.css` → 200, `Content-Type` beginnt mit `text/css`, `Cache-Control` = `public, max-age=31536000, immutable`; `GET /vendor/0.0.0/ui/pdh.css` → 302 auf `uiURL("pdh.css")` mit `no-store`; `…/ui/fehlt.css`, `…/ui/..%2Fx.css`, `…/ui/pdh.txt` → 404. `TestBaseLinksUIStylesheet`: gerenderte Grundseite (wie bestehende Render-Tests) enthält `<link rel="stylesheet" href="/vendor/<ver>/ui/pdh.css">` innerhalb `<head>` und vor `<style id="pdh-theme">`; kein `<style>`-Block über 1500 Byte außer `pdh-theme`/`pdh-ui`; dasselbe mit `?embed=1`. `TestUICSSComplete`: `pdh.css` enthält `html[data-style="fiori"]`, `html[data-theme="light"]` und `@media print`.
- [ ] **Step 2:** `go test ./internal/web -run 'TestUIAsset|TestBaseLinksUI|TestUICSS' -count=1` → FAIL (Route/Datei fehlt).
- [ ] **Step 3:** Datei per Zeilenbereich aus `base.gohtml` schneiden (z. B. `sed -n '48,803p;806,886p'`), Handler nach dem Muster `VendorLibWeb`, Stilverweis an der Stelle des alten `<style>`.
- [ ] **Step 4:** Tests von Step 2 → PASS; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Gestaltung: Stildatei der Grundseite ausgelagert (0.82.61)`

### Task 2: Skripte ohne Platzhalter auslagern

**Files:**
- Create: `web/static/ui/pdh-<zweck>.js` je Block (Blöcke in `base.gohtml` ohne `{{`: heute Zeilen 2514–2750, 2752–3016, 3065–3166, 3283–3385, 3402–3434, 3435–3599, 3607–3681 – Zeilen vor dem Schneiden neu ermitteln, Task 1 verschiebt sie); Namen nach Inhalt wählen
- Modify: `web/templates/base.gohtml`, `internal/web/ui_assets_test.go`

**Interfaces:**
- Consumes: `uiURL`, `UIAssetWeb` (Task 1).
- Produces: je Block `<script src="{{uiURL "pdh-<zweck>.js"}}"></script>` an derselben Stelle, ohne `defer`/`async`.

- [ ] **Step 1: Test** `TestBaseScriptsExternal`: gerenderte Grundseite verweist auf jede Datei aus `web/static/ui/*.js`, jede Datei liefert 200 mit `text/javascript`; kein Inline-`<script>` ohne Vorlagen-Platzhalter über 1500 Byte (Prüfung am Quelltext von `base.gohtml`).
- [ ] **Step 2:** `go test ./internal/web -run TestBaseScriptsExternal -count=1` → FAIL.
- [ ] **Step 3:** Blöcke ausschneiden; vorher je Block prüfen, dass er kein `document.currentScript` und keine Vorlagen-Namen nutzt (sonst Block in Task 3 behandeln).
- [ ] **Step 4:** Test → PASS; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Gestaltung: Skripte der Grundseite ausgelagert (0.82.61)`

### Task 3: Datenblock `pdh-boot`, restliche Skripte auslagern

**Files:**
- Modify: `web/templates/base.gohtml` (Blöcke mit Platzhaltern: heute 928–1047, 1308–2513, 3029–3063, 3168–3251), die Stelle, an der `BaseData` gefüllt wird
- Create: `web/static/ui/pdh-<zweck>.js` je Block; Test in `internal/web/ui_assets_test.go`

**Interfaces:**
- Produces: Feld `Boot` an `BaseData` (Struktur mit JSON-Namen, nur die Werte, die die Blöcke heute per Platzhalter lesen); im `<head>` vor dem ersten Skript `<script id="pdh-boot" type="application/json">{{.Boot}}</script>`; das bestehende Kopfskript (Zeilen 10–43, bleibt inline) setzt `window.PDH_BOOT = JSON.parse(document.getElementById('pdh-boot').textContent)`; die ausgelagerten Skripte lesen nur `PDH_BOOT.<name>`.

- [ ] **Step 1: Tests** `TestBootDataJSON`: gerenderte Grundseite enthält genau einen `pdh-boot`-Block, dessen Inhalt mit `encoding/json` lesbar ist und `PDH_BOOT`-Werte für Version und angemeldeten Benutzer enthält; mit Benutzername `a</script><b>"x'` bleibt der Inhalt gültiges JSON und die Seite enthält kein zweites `</script>` darin. `TestBaseInlineBudget`: Summe aller Inline-`<style>`- und Inline-`<script>`-Inhalte in `base.gohtml` (Quelltext, ohne den `pdh-boot`-Block) ≤ 16384 Byte.
- [ ] **Step 2:** `go test ./internal/web -run 'TestBootData|TestBaseInlineBudget' -count=1` → FAIL.
- [ ] **Step 3:** Je Block die Platzhalter durch `PDH_BOOT.<name>` ersetzen, Werte in `Boot` füllen, Block auslagern wie in Task 2.
- [ ] **Step 4:** Tests → PASS; `go vet ./... && go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Gestaltung: Seitendaten als JSON, alle großen Skripte ausgelagert (0.82.61)`

### Task 4: Gestaltungswerte und Wächter

**Files:**
- Create: `web/static/ui/tokens.css`, `internal/web/testdata/color_literals.txt`, Test `internal/web/design_guard_test.go`
- Modify: `web/static/ui/pdh.css` (reine Variablen-Blöcke nach `tokens.css`), `web/templates/base.gohtml` (Verweis `tokens.css` vor `pdh.css`)

**Interfaces:**
- Produces: `tokens.css` mit den bisherigen Variablen (`--bg`, `--bg2`, `--bg3`, `--text`, `--muted`, `--border`, `--accent`, `--shadow-card`, `--stroke`, `--fill-hover` …) für `:root`, `html[data-theme="light"]`, `html[data-style="fiori"]`, `html[data-style="fluent"]` samt hell/dunkel – Werte und Reihenfolge der Blöcke unverändert –, dazu neu: `--sp-1:4px; --sp-2:8px; --sp-3:12px; --sp-4:16px; --sp-6:24px; --fs-xs:12px; --fs-sm:13px; --fs-md:14px; --fs-lg:16px; --fs-xl:20px; --r-s:4px; --r-m:8px; --shadow-float` sowie Statusfarben `--ok`, `--warn`, `--err`, `--info` mit `-soft`-Hintergrund je Stil und Modus (Werte aus der Vorschau `docs/superpowers/specs/2026-11-25-oberflaeche-vorschau.html`).

- [ ] **Step 1: Tests** `TestTokensComplete`: `tokens.css` definiert jede der genannten Variablen; `pdh.css` definiert keine Variable, die `tokens.css` definiert. `TestColorLiteralsRatchet`: zählt feste Farbwerte (`#` + 3/4/6/8 Hex-Zeichen, `rgb(`, `rgba(`, `hsl(`) je Datei in `web/templates/**/*.gohtml` und `web/static/ui/*.css` außer `tokens.css`; jede Zahl ≤ Wert in `color_literals.txt`, Dateien ohne Eintrag 0; **Ausnahmen** (feste Liste im Test): E-Mail-Vorlagen, Druckvorlagen (Etiketten, Kanban-Druck, Wartungsprotokoll) und `widgets/help_figures.gohtml` – genaue Dateinamen per `grep` bestimmen. Ist eine Zahl kleiner als der Eintrag, schlägt der Test mit Hinweis fehl; `PDH_COLOR_BASELINE_UPDATE=1` schreibt die Datei neu.
- [ ] **Step 2:** `go test ./internal/web -run 'TestTokens|TestColorLiterals' -count=1` → FAIL.
- [ ] **Step 3:** Variablen-Blöcke verschieben, neue Werte ergänzen, Ausgangswerte mit `PDH_COLOR_BASELINE_UPDATE=1` schreiben.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Gestaltung: Gestaltungswerte in einer Datei, Wächter für feste Farben (0.82.61)`

### Task 5: Recht, Bausteinkatalog, Handbuch, Version

**Files:**
- Create: `migrations/223_ui_defaults_permission.up.sql`, `internal/web/ui_catalog.go`, `web/templates/ui_catalog.gohtml`, Test `internal/web/ui_catalog_test.go`
- Modify: `migrations/checksums.txt`, `internal/web/nav.go` (Eintrag in Gruppe `admin`), Routen, `internal/web/help.go` (`helpChapterIDs`), `web/templates/help.gohtml` (Kapitel), `CHANGELOG.md`, `VERSION`

**Interfaces:**
- Consumes: `tokens.css` (Task 4).
- Produces: Seite `GET /admin/gestaltung`, Seitenkennung `ui-catalog`, Recht `system.ui_defaults` (Konstante `permUIDefaults`), Navigationseintrag „Gestaltung“ (`ti-palette`).

- [ ] **Step 1: Tests** `TestUICatalogPerm`: ohne `system.ui_defaults` → 403, mit → 200 und Seite enthält die Abschnitte „Farben“, „Abstände“, „Schrift“, „Ecken“, „Bausteine“; die Vorlage `ui_catalog.gohtml` hat 0 feste Farbwerte (deckt `TestColorLiteralsRatchet` ab). Bestehend: `TestHelpCoversAllPages`, `TestHelpChaptersMatch`, `TestChangelogMatchesVersion`, `TestMigrationsFrozen`.
- [ ] **Step 2:** `go test ./internal/web -run 'TestUICatalog|TestHelp' -count=1` → FAIL.
- [ ] **Step 3:** Migration nach Muster 222; Seite zeigt die Werte aus `tokens.css` für den gewählten Stil und Modus (Farbfelder mit Variablennamen, Abstandsleiste, Schriftgrößen, Ecken) und die vorhandenen Grundbausteine (Knöpfe, Schilder, Reiter, Sortierleiste, Dialog, Statusmeldung) live mit je einer Regel in einem Satz. Handbuch-Kapitel im Teil Administration mit „So geht's“; CHANGELOG `## [0.82.61] – <Datum>`: unter `### Geändert` „Seiten bauen schneller auf: Gestaltung und Skripte kommen als zwischengespeicherte Dateien“, unter `### Neu` „Seite ‚Gestaltung‘ zeigt Farben, Abstände, Schrift und Bausteine der Oberfläche“, unter `### Sicherheit` „Neues Recht ‚Oberfläche: Vorgaben für Firma und Rollen‘“; `VERSION` 0.82.61; Prüfsummen eintragen.
- [ ] **Step 4:** `go vet ./... && go test ./... -count=1` grün (bekannte rote Tests laut AGENTS.md Punkt 27 ausgenommen); `go test -tags integration ./pkg/database ./internal/web -count=1` wie vorher.
- [ ] **Step 5: Commit** `Gestaltung: Bausteinkatalog und Recht für Oberflächen-Vorgaben (0.82.61)`

## Abnahme nach dem Merge (Testverbund)

- Vorher/nachher Bildschirmfotos (Fiori/Fluent × hell/dunkel, 390 und 1366 px) von Startseite, Störungen, Ticket-Detail, Core-Einstellungen: kein sichtbarer Unterschied; keine Konsolenfehler auf rund 20 Seiten.
- Messung mit `deploy/lab/seitenaufbau/messung.js`: HTML je Aufruf mindestens 150 KB kleiner; erster Aufruf 4× gedrosselt ≤ 1,5 s; wiederholte Aufrufe schneller als vorher.
