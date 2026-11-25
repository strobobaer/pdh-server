# Neuer Rahmen hinter dem Schalter (0.90.0 Teilpaket 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ein zweiter Seitenrahmen (Bereichsleiste, Reiter, Menüband, Strg+K, Statusleiste, Handy-Ansicht), den jeder Benutzer im Benutzermenü einschalten kann; alle Seiten laufen unverändert darin.

**Architecture:** `h.render` führt je nach Wahl `base.gohtml` (klassisch) oder `frame.gohtml` (neu) aus; beide nutzen dieselben Bausteine aus `widgets/frame_shared.gohtml` (Kopf, rechte Leiste, Überlagerungen, Statusleiste, Skripte), damit nichts doppelt gepflegt wird. Bereiche, Menüband und Befehlszeile sind Go-Tabellen bzw. eine JSON-Schnittstelle; das Verhalten im Browser steckt in `web/static/ui/frame.css` und `pdh-frame.js`, die nur der neue Rahmen lädt.

**Tech Stack:** Go (chi, html/template), PostgreSQL-Migration, Vanilla-JS, Tests mit `httptest`/`renderPage`.

**Spec:** `docs/superpowers/specs/2026-11-25-einheitliche-oberflaeche-design.md` (5.2, 5.6, 6.2, 7, 10.1–10.4, 10.7, 10.9, 10.10); Vorschau `docs/superpowers/specs/2026-11-25-oberflaeche-vorschau.html`.

## Global Constraints

- Version: nächste Korrekturversion über `origin/main` (Stand 25.11.2026: 0.82.62 → **0.82.63**); Migration: nächste freie Nummer (Stand: **225**) – vor Bau und vor Merge gegen `origin/main` prüfen.
- Klassischer Rahmen bleibt für Benutzer **unverändert**; einzige sichtbare Änderung dort: Eintrag „Neue Oberfläche ausprobieren“ im Benutzermenü.
- Schalter dreiwertig je Benutzer (`''` = Firmenvorgabe, `classic`, `new`); Firma setzt nur die Vorgabe, erzwingt nichts (Spec 10.7).
- Bereiche und Zuordnung genau nach Spec 5.2; „Neu anlegen“ → Menüband/Strg+K, „Chat“ und „Handbuch“ → rechte Leiste.
- Seitenwechsel lädt ganz neu (Spec 10.2); geöffnete Vorgangs-Reiter kommen erst mit Teil 3.
- Kopfleiste: Uhr und Benutzermenü immer ganz sichtbar, Knöpfe nur Symbol mit `title`/`aria-label`, `data-tb-hide` wie in AGENTS.md; `pdhTopbarFit` muss auch im neuen Rahmen greifen (Klasse `.topbar`).
- Regeln aus AGENTS.md „Gestaltungsgrundlage (seit 0.82.62)“: Stil/Skript nur in `web/static/ui/`, Werte aus `tokens.css`, keine Vorlagen-Syntax in `ui/`, keine neuen festen Farben.
- Rechte: Schalter, Bereichsreihenfolge, Favoriten = eigene Einstellungen ohne Recht (wie `/account/nav-layout`); Firmenvorgaben nur mit `system.ui_defaults`; Strg+K zeigt nur, was die vorhandenen Rechte erlauben.
- Beide Stile × hell/dunkel; Breiten 320, 390, 768, 1024, 1366 px, 120–160 % Vergrößerung.
- Handbuch, CHANGELOG im selben Schritt; Tests in Git Bash mit Labor-DSN (siehe Plan Teil 1).

## Review Focus

1. Benutzer mit eigener Menü-Anordnung oder mit sehr wenigen Rechten schaltet um: Bereiche ohne sichtbaren Eintrag fehlen, die Leiste ist nie leer (Start/Dashboard immer da), keine Seite wird sichtbar, die das Recht nicht erlaubt (Test in Aufgabe 3).
2. Firma stellt die Vorgabe auf „neu“, ein Benutzer hat „klassisch“ gewählt → er bleibt klassisch; „wie Firma“ folgt der Vorgabe (Test in Aufgabe 2).
3. Eingebettete Ansicht (`?embed=1`/`window.name='pdh-embed'`) und Spiegelserver/Wartungsband im neuen Rahmen: Leiste, Kopf, Menüband ausgeblendet bzw. Band und Spiegelhinweis vorhanden (Tests in Aufgabe 1/2).
4. Seiten mit eigenem Kopf (Anmeldung, Easy, Leitstand, Terminal, Druck) ignorieren den Schalter (Test in Aufgabe 2).
5. Strg+K mit Sonderzeichen, < 2 Zeichen, ohne Anlagenrecht: leere bzw. gefilterte Liste, keine Seite ohne Recht, Ausgabe JSON-sicher (Test in Aufgabe 5).

---

### Task 1: Gemeinsame Bausteine aus base.gohtml lösen

**Files:**
- Create: `web/templates/widgets/frame_shared.gohtml`
- Modify: `web/templates/base.gohtml`, `internal/web/ui_assets_test.go` (`baseSource`), Tests, die `base.gohtml` lesen

**Interfaces:**
- Produces: Vorlagen `ui-head` (alles zwischen `<head>` und `{{block "head" .}}` außer `<title>`/Favicon: `pdh-boot`, Kopfskript, htmx, Symbole, `tokens.css`, `pdh.css`, `pdh-theme`, `pdh-ui`), `ui-overlays` (Override-Anmeldung, `.pdh-backdrop`, Assistenten), `ui-context` (`<aside class="context">` samt Reitern), `ui-footer` (`#global-status-footer`), `ui-scripts` (alle Skripte am Seitenende samt `{{block "scripts" .}}`-Stelle und der Blöcke nach `</html>`), `ui-bands` (Spiegelhinweis + `#maint-band`); Testhilfe `frameSources(t) string` = `base.gohtml` + `frame.gohtml` (falls vorhanden) + `widgets/frame_shared.gohtml` ohne CR; `baseSource` liefert künftig `base.gohtml` + `widgets/frame_shared.gohtml`.

- [ ] **Step 1: Gerenderte Ausgabe festhalten** – vor jeder Änderung die vier Seiten aus dem Wegwerf-Test der Abnahme 0.82.62 (`server_config`, `work_board`, `inventory`, `user_detail`) in das Arbeitsverzeichnis rendern (Ordner `vorher/`, nicht committen).
- [ ] **Step 2:** Bausteine ausschneiden und in `base.gohtml` durch `{{template "ui-…" .}}` ersetzen; `{{block "scripts" .}}` und `{{block "context-list" .}}` bleiben über die Bausteine erreichbar.
- [ ] **Step 3: Nachweis** – dieselben vier Seiten neu rendern (`nachher/`), `diff -r vorher nachher` → keine Abweichung außer `data-server-ms`/Uhrzeit. Ergebnis ins Ledger.
- [ ] **Step 4:** Tests, die `base.gohtml` als Quelltext lesen (`baseSource`, `baseWithUI`, `TestBaseScriptsExternal`, `TestBaseHasMaintBand`, `TestBaseInlineBudget`), auf `baseSource` = base + shared umstellen; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Oberfläche: gemeinsame Bausteine des Rahmens (0.82.63)`

### Task 2: Schalter, Firmenvorgabe, Rahmenwahl, Gerüst `frame.gohtml`

**Files:**
- Create: `migrations/225_ui_frame.up.sql`, `internal/web/ui_frame.go`, `web/templates/frame.gohtml`, Test `internal/web/ui_frame_test.go`
- Modify: `internal/web/handler.go` (`render`, `baseData`, Routen), `cmd/server/main.go` und `loadTestTemplates` (`frame.gohtml` mitparsen), `web/templates/base.gohtml` (Benutzermenü), `web/templates/ui_catalog.gohtml` (Firmenvorgabe), `internal/web/perm_gate.go`, `migrations/checksums.txt`

**Interfaces:**
- Produces: Spalten `users.ui_frame text NOT NULL DEFAULT ''` (`''`|`classic`|`new`) und `users.ui_areas jsonb` (für Aufgabe 3); Einstellung `app_settings` Schlüssel `defaults.ui_frame` (`classic`|`new`, fehlt = `classic`); `BaseData.NewFrame bool` und `BaseData.UIFrame string` (gewählter Wert des Benutzers); `func (h *Handler) frameFor(ctx context.Context, userFrame string) bool`; `h.render` führt `frame.gohtml` aus, wenn `NewFrame`; Routen `POST /account/ui-frame` (Formular `frame` = `default|classic|new`, zurück auf `Referer`) und `POST /admin/gestaltung/frame` (Formular `frame` = `classic|new`, Recht `system.ui_defaults`, Gate-Regel); `frame.gohtml` = Wurzel mit `ui-head`, Kopfleiste (`.topbar` mit Suchfeld-Platzhalter, Copilot, Uhr, Benutzermenü wie klassisch), leerer `<nav class="rail">`, `ui-bands`, `<main class="main">` mit `{{block "content" .}}`, `ui-context`, `ui-footer`, `ui-scripts`, Verweise `frame.css`/`pdh-frame.js`.

- [ ] **Step 1: Tests** `TestUIFrameChoice`: Firmenvorgabe fehlt → `frameFor("")` false; Vorgabe `new` → `frameFor("")` true, `frameFor("classic")` false, `frameFor("new")` true. `TestUIFrameSave`: `POST /account/ui-frame` mit `frame=new` speichert `new`, `default` speichert `''`, unbekannter Wert → 400. `TestUIFrameFirmDefaultPerm`: `POST /admin/gestaltung/frame` ohne `system.ui_defaults` → 403; Gate-Regel vorhanden. `TestFrameRenders`: Seite mit `NewFrame` enthält `class="rail"`, `id="global-status-footer"`, `id="maint-band"`, `id="pdh-boot"`, `href="/time" class="topbar-clock"`, `class="topbar-user`, Verweise auf `frame.css` und `pdh-frame.js`; ohne `NewFrame` nicht. `TestClassicMenuOffersNewFrame`: klassisches Benutzermenü enthält `action="/account/ui-frame"` und „Neue Oberfläche ausprobieren“. `TestOwnHeadPagesIgnoreFrame`: Anmeldeseite und Easy rendern ohne `class="rail"`, auch mit `NewFrame`. `TestBaseHasMaintBand` zusätzlich für `frame.gohtml`.
- [ ] **Step 2:** `go test ./internal/web -run 'TestUIFrame|TestFrameRenders|TestClassicMenu|TestOwnHeadPages' -count=1` → FAIL.
- [ ] **Step 3:** Migration, `frameFor` (liest `defaults.ui_frame` über den vorhandenen Einstellungs-Zwischenspeicher), `baseData` füllt `UIFrame`/`NewFrame` aus `users.ui_frame`, `render` wählt den Namen; Gerüst-Vorlage; Firmenvorgabe als kleine Karte oben auf `/admin/gestaltung` (Klassisch/Neu, Speichern). Prüfsumme eintragen.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web ./pkg/database -count=1` grün.
- [ ] **Step 5: Commit** `Oberfläche: Schalter „Neue Oberfläche“ und Firmenvorgabe (0.82.63)`

### Task 3: Bereichsleiste, Reiter der Unterseiten, Favoriten, Handy-Leiste

**Files:**
- Create: `internal/web/nav_areas.go`, Test `internal/web/nav_areas_test.go`, `web/static/ui/frame.css`
- Modify: `web/templates/frame.gohtml`, `internal/web/ui_frame.go` (Route), `internal/web/handler.go`

**Interfaces:**
- Produces: `type navArea struct{ Key, Label, Icon string; Items []navItem }`; `var navAreaDefs = []struct{ Key, Label, Icon string; Items []string }` mit genau dieser Zuordnung (Schlüssel aus `navDefs`): `start` Start `ti-home` {dashboard, leitstand, assignments}; `vorgaenge` Vorgänge `ti-list-details` {faults, tickets, tasks, maintenance, setup, downtime, kvp, projects}; `planung` Planung `ti-calendar` {shifts, calendar, time, attendance}; `anlagen` Anlagen `ti-building-factory-2` {infrastructure, plantwatch, it, vpn, esppdh, energy}; `material` Material `ti-package` {inventory, storage, procurement, purchases, directory}; `wissen` Wissen `ti-books` {ms, ms-handbook, ms-process, ms-instruction, ms-form, m365, trainings}; `daten` Daten `ti-chart-bar` {statistics, import, export, sync, datanet}; `verwaltung` Verwaltung `ti-settings` {users, orgunits, orgchart, roles, core, serverconfig, network, backup, cleanup, printers, appdevices, kiosk, leitstaende, terminal-slides, costcenters, categories, icons, ui-catalog}; `dienste` Dienste `ti-apps` {nextcloud}; außerhalb `navOutsideAreas = {assign, chat, help}`. `func buildAreas(b *BaseData, prefs uiAreaPrefs) (areas []navArea, current string, favorites []navItem)` (sichtbar nur mit `navDef.show(b)`, Bereich ohne Eintrag fehlt; `current` = Bereich der Seite `b.Page`); `type uiAreaPrefs struct{ Order []string `json:"order"`; Favorites []string `json:"favorites"` }` in `users.ui_areas` (höchstens 9 bzw. 12 Einträge, unbekannte Schlüssel verworfen); `BaseData.Areas`, `.AreaCurrent`, `.Favorites`; Route `POST /account/ui-areas` (JSON wie `uiAreaPrefs`, 8 KB Grenze, oder `{"reset":true}`). Firmenvorgabe: `app_settings` Schlüssel `defaults.ui_areas` (gleiches JSON), gilt, wenn der Benutzer keine eigene hat; Route `POST /admin/gestaltung/areas` (gleiches JSON, Recht `system.ui_defaults`, Gate-Regel) – das Menüband speichert damit die eigene Anordnung als Vorgabe.
- Symbole vorher in `icons_used.txt` prüfen, fehlende eintragen.

- [ ] **Step 1: Tests** `TestNavAreasCoverAllDefs`: jeder Schlüssel aus `navDefs` steht genau einmal in `navAreaDefs` oder `navOutsideAreas`, kein unbekannter Schlüssel. `TestBuildAreasPermissions`: ohne Rechte (navCan immer false) nur Bereich `start` mit `dashboard`; mit `system.network` erscheint `verwaltung` mit `network`; `current` für Seite `network` = `verwaltung`. `TestBuildAreasPrefs`: Reihenfolge `["material","start"]` stellt diese vorn an, Rest in Vorgabe-Reihenfolge; Favorit `network` ohne Recht entfällt. `TestUIAreasSave`: gültiges JSON speichert, > 8 KB → 400, `reset` löscht; ohne eigene Anordnung gilt `defaults.ui_areas`; `POST /admin/gestaltung/areas` ohne `system.ui_defaults` → 403. `TestFrameRail`: neuer Rahmen enthält je sichtbarem Bereich `data-area="<key>"`, für den aktuellen Bereich eine Reiterleiste mit den Unterseiten (`class="frame-tabs"`, aktive mit `aria-selected="true"`), Favoriten über den Bereichen.
- [ ] **Step 2:** `go test ./internal/web -run 'TestNavAreas|TestBuildAreas|TestUIAreas|TestFrameRail' -count=1` → FAIL.
- [ ] **Step 3:** Tabelle, `buildAreas`, Vorlage; `frame.css`: Leiste links 64 px wie Vorschau, Reiter über dem Menüband, ab ≤ 760 px untere Leiste mit den ersten 4 Bereichen + „Mehr“ (Überlagerung mit allen), Bedienflächen ≥ 44 px, `html.pdh-embedded` blendet Leiste/Reiter aus; nur Werte aus `tokens.css`.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Oberfläche: Bereichsleiste, Reiter, Favoriten (0.82.63)`

### Task 4: Menüband mit Grundfunktionen und „Darstellung“

**Files:**
- Create: `internal/web/ribbon.go`, Test `internal/web/ribbon_test.go`, `web/static/ui/pdh-frame.js`
- Modify: `web/templates/frame.gohtml`, `web/static/ui/frame.css`

**Interfaces:**
- Produces: `type ribbonItem struct{ Key, Label, Icon, Href, Action string }` (`Action` = Name einer Funktion in `pdh-frame.js`, kein freier JS-Text), `type ribbonGroup struct{ Label string; Items []ribbonItem }`, `type ribbonTab struct{ Key, Label string; Groups []ribbonGroup }`; `func ribbonFor(b *BaseData) []ribbonTab` mit Reitern **Start** (Gruppe „Vorgang“: Neu → `pdhCreate`), **Ansicht** (rechte Leiste ein/aus, Leiste schmal, Vollbild), **Anpassen** (Seite als Favorit anheften/lösen, Bereiche ordnen, Startseite anpassen → `pdhDashboardEdit` nur auf `dashboard`, „Als Firmenvorgabe speichern“ nur mit `system.ui_defaults`), **Darstellung** (Stil Fiori/Fluent nur wenn `Look.Allow`, Modus hell/dunkel/wie System, Farbschema, Dichte kompakt/normal/Berührung = Zeilenhöhe 90/100/115 über die vorhandene `lookSet('row', …)`); `BaseData.Ribbon`; am Handy die ersten 3 Einträge von „Start“ + „…“.
- `pdh-frame.js` stellt `window.pdhFrame = {run(action, el), …}` bereit; Aktionen über `data-ribbon-action`, kein `onclick`-Text aus Go.

- [ ] **Step 1: Tests** `TestRibbonForBasics`: vier Reiter in der Reihenfolge Start, Ansicht, Anpassen, Darstellung; „Neu“ in Start; Stilwahl fehlt bei `Look.Allow=false`; „Als Firmenvorgabe speichern“ nur mit `system.ui_defaults`; jede `Action` steht in der Liste `ribbonActions` (Test prüft, dass `pdh-frame.js` jede davon als `case`/Schlüssel enthält). `TestFrameRibbon`: neuer Rahmen enthält `role="tablist" aria-label="Menüband"`, je Eintrag `data-ribbon-action` bzw. `href`, `title` und `aria-label`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** `ribbon.go`, Vorlage, `pdh-frame.js` (Reiterwechsel, Aktionen, Favorit/Bereiche über `POST /account/ui-areas`, Modus über `localStorage pdh_theme`), Stile in `frame.css`.
- [ ] **Step 4:** Tests → PASS; `node --check web/static/ui/pdh-frame.js`; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Oberfläche: Menüband mit Grundfunktionen und Darstellung (0.82.63)`

### Task 5: Befehlszeile Strg+K

**Files:**
- Create: `internal/web/palette.go`, Test `internal/web/palette_test.go`
- Modify: `internal/web/handler.go` (Route), `web/static/ui/pdh-frame.js`, `web/static/ui/frame.css`, `web/templates/frame.gohtml`

**Interfaces:**
- Produces: `GET /api/palette?q=` (angemeldet) → JSON `{"items":[{"kind":"action|page|asset","label":"…","href":"…","hint":"…"}]}`, höchstens 20; Aktionen: „Störung melden“, „Ticket anlegen“, „Aufgabe anlegen“, „Wartung planen“ (je nur mit dem Anlegen-Recht der Art, `href` auf die vorhandene Anlegen-Seite bzw. `#create:<art>` für `pdhCreate`), Seiten aus `buildAreas` (Label enthält q, ohne Groß-/Kleinschreibung, Umlaute wie gespeichert), Anlagen über `h.infra.Search` nur mit `CanViewInfra` (`hint` = Pfad); q < 2 Zeichen → nur Aktionen und Favoriten. Vorgänge (Nummer/Titel) kommen mit Teil 3 (Spec 5.2 – Ruling im Ledger).
- `pdh-frame.js`: Strg+K / ⌘K und Klick ins Suchfeld öffnen die Überlagerung „Was möchtest du tun?“, Pfeile, Enter, Esc, Abfrage verzögert 200 ms, `pdhQuiet`.

- [ ] **Step 1: Tests** `TestPaletteSearch`: q=`net` mit `system.network` liefert Seite „Netzwerk“, ohne Recht nicht; q=`a` liefert nur Aktionen/Favoriten; Anlagen nur mit `CanViewInfra`; Ausgabe mit Label `"<b>"` ist gültiges JSON und `Content-Type` `application/json`; ohne Anmeldung 401/Weiterleitung wie andere `/api/`-Wege. `TestFramePaletteHooks`: neuer Rahmen enthält Suchfeld mit `placeholder="Was möchtest du tun?  (Strg+K)"` und `aria-label`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Endpunkt, Client, Stile.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Oberfläche: Befehlszeile Strg+K (0.82.63)`

### Task 6: Statusleiste im neuen Rahmen

**Files:**
- Modify: `web/static/ui/pdh-maint-band.js` (Ereignis), `web/static/ui/pdh-frame.js`, `web/static/ui/frame.css`, `web/templates/frame.gohtml`, Test `internal/web/ui_frame_test.go`

**Interfaces:**
- Consumes: Antwort von `GET /wartung/status` (Felder `locked`, `active`, `countdown_until`, für Berechtigte `phase`, `to_version`), `BaseData.Mirror`, `setGlobalStatus`.
- Produces: `pdh-maint-band.js` löst nach jeder Abfrage `document.dispatchEvent(new CustomEvent('pdh:maint', {detail: status}))` aus (klassischer Rahmen unverändert); im neuen Rahmen zeigt die Statusleiste als Text „Update läuft – PDH ist gleich kurz weg“ bzw. „Automatische Wartung in N min“ samt Balken, Einzelheiten (`to_version`, Schritt) nur, wenn die Antwort sie enthält (Spec 10.9); `setGlobalStatus`-Meldungen landen in einer Liste der letzten 20 (Sitzungsspeicher); Tippen/Klick auf die Leiste öffnet sie; Spiegelserver als dauerhafte Warnung „Spiegelserver – nur lesen“.

- [ ] **Step 1: Tests** `TestFrameStatusbar`: neuer Rahmen enthält `#global-status-footer` mit `role="button"`, `aria-label="Letzte Meldungen"`, Ziel für die Meldungsliste; mit `Mirror` den Warntext; `pdh-maint-band.js` enthält `'pdh:maint'`; `pdh-frame.js` reagiert darauf (Zeichenkette `addEventListener('pdh:maint'`).
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Umsetzung.
- [ ] **Step 4:** Tests → PASS; `node --check` aller geänderten Skripte; `go test ./internal/web -count=1` grün.
- [ ] **Step 5: Commit** `Oberfläche: Statusleiste mit Update-Stand und Meldungen (0.82.63)`

### Task 7: Handbuch, Version, Schlussprüfung vorbereiten

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel „Start“ oder eigenes Kapitel „Neue Oberfläche“ samt `helpChapterIDs`, falls eigene Seiten-ID), `web/templates/widgets/help_figures.gohtml` (Skizze des neuen Rahmens), `CHANGELOG.md`, `VERSION`, `AGENTS.md` (Kopfleisten-Regel um den neuen Rahmen ergänzen)

- [ ] **Step 1:** Handbuch „So probierst du die neue Oberfläche aus“ (Schalter, Bereiche, Favoriten, Menüband, Strg+K, Statusleiste, Zurück zur klassischen), Skizze nur mit Werten aus `tokens.css`.
- [ ] **Step 2:** CHANGELOG `## [0.82.63]`: Neu – „Neue Oberfläche zum Ausprobieren …“, „Befehlszeile Strg+K …“; Sicherheit – keine neuen Rechte (Firmenvorgabe mit „Oberfläche: Vorgaben für Firma und Rollen“); `VERSION`.
- [ ] **Step 3:** `go vet ./...`, `go test ./...`, Integrationstests (bekannte rote laut AGENTS.md Punkt 27 ausgenommen) → grün.
- [ ] **Step 4: Commit** `Oberfläche: Handbuch und Version (0.82.63)`

## Abnahme nach dem Merge (Testverbund)

- Klassischer Rahmen: Stilvergleich wie bei 0.82.62 (gerenderte Seiten alt/neu, berechnete Werte gleich) – nur das neue Menü-Element.
- Neuer Rahmen: alle Menüseiten bei 320, 390, 768, 1024, 1366 px in Fiori/Fluent × hell/dunkel ohne Konsolenfehler und Überlauf; Uhr und Benutzermenü immer ganz sichtbar; Strg+K, Favorit anheften, Bereiche ordnen, Modus „wie System“, Statusleiste während eines simulierten Wartungs-Countdowns.
