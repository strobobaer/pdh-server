# Rahmen-Umbau und Bereich Start (0.90.0 Teil 2b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Der neue Rahmen bekommt den vom Anwender am 26.11.2026 bestätigten Aufbau (Kopfzeile nur Logo/Titel/Uhr, Benutzer links unten, Menüband statt Reiterleiste, Fußzeile `V… · 12/40`), der Bereich Start wird mit Dashboard, Leitstand und Zeiterfassung fertig umgestellt, Rückgängig/Speichern/Verwerfen gibt es für das Dashboard, und das PDH lässt sich als App ohne Browserleisten installieren.

**Architecture:** Alles bleibt hinter dem Schalter „neuer Rahmen“ (`frameTemplateName`); der klassische Rahmen (`base.gohtml`) sieht unverändert aus. Das Menüband entsteht weiter serverseitig in `ribbonFor`, erweitert um Bänder (Unterseiten des Bereichs als Reiter mit Link), Dropdowns und seiteneigene Aktionen aus einem Register (`pageActions`). Ein kleines Browser-Modul `pdh-edit.js` verwaltet Bearbeitungsschritte (Rückgängig, Anzeige, Nachfrage beim Wechseln); das Dashboard ist die erste Seite, die es nutzt.

**Tech Stack:** Go 1.27, chi, html/template, pgx; JavaScript ohne Bibliothek in `web/static/ui/`; Tests mit `go test` (Render-Tests über `renderFrameForTest`), Browserprüfung mit Edge über `playwright-core`.

**Spec:** `docs/superpowers/specs/2026-11-25-einheitliche-oberflaeche-design.md`, Abschnitte 10, 11 (11.1–11.12) und 12; klickbare Vorschau `docs/superpowers/specs/2026-11-26-rahmen-start-vorschau.html` (vom Anwender am 26.11.2026 bestätigt: „Passt“).

## Global Constraints

- Version **0.82.67** (`VERSION`, oberster Eintrag `## [0.82.67] – 2026-11-26` in `CHANGELOG.md`); vor dem Merge gegen `origin/main` prüfen und ggf. die nächste Korrekturversion nehmen (AGENTS.md „Prüfung vor jedem Merge“).
- Branch `feature/rahmen-start` ab `main`, nachdem `docs/oberflaeche-rahmen-nachtrag` (Entwurf, Vorschau, dieser Plan) nach `main` gemergt ist.
- **Keine Migration.** Neue Einstellungen in `app_settings`. Kein neues Recht; genutzt werden `system.ui_defaults` (Gestaltung, Vorlagen-Hinweis), `system.defaults` (Dashboard als Standard), `leitstand.manage` (Leitstand bearbeiten, als Standard), Zuweisen wie heute (`b.IsBroker || b.CanManageRoles`). Die Zeiterfassung hat heute kein Recht; ihre Tasten folgen den vorhandenen Routen (`/time/start-web`, `/time/{id}/stop-web`, `/time/manual-web`) ohne neues Recht.
- Klassischer Rahmen unverändert (gemeinsame Teile nur in `widgets/frame_shared.gohtml`, neue Teile des neuen Rahmens in einer neuen Datei `widgets/frame_new.gohtml`).
- Gestaltungsgrundlage: Werte nur aus `web/static/ui/tokens.css`, keine Vorlagen-Syntax in `web/static/ui/`, Texte über `pdhT("…")` mit Eintrag im `"t"`-Teil von `pdh-boot`, `TestColorLiteralsRatchet` darf nicht steigen.
- Neu-anlegen- und Fertigstellen-Assistent bleiben erreichbar (`TestFrameHasWizards` muss grün bleiben; „Neu“ im Band Start öffnet `pdhCreate`).
- Texte für Anwender auf Deutsch, kurze Sätze; Handbuch (`web/templates/help.gohtml`, Kapitel des neuen Rahmens) und Skizze in `widgets/help_figures.gohtml` im selben Schritt nachziehen.
- Wortlaute aus der Vorschau übernehmen: „Rückgängig“, „Neu“, „Zuweisen“, „Zeit starten“, „Zeit stoppen“, „Manuell erfassen“, „Suchen“, „Copilot“, „Darstellung“, „Anpassen“, „Einstellungen“, „Hilfe“, „Bearbeiten“, „Speichern“, „Abbrechen“, „Als Standard für alle setzen“, „Auf Vorgabe zurücksetzen“, „Vollbild (Wandbildschirm)“, „Änderungen speichern?“, „Verwerfen“, „● N nicht gespeichert“, „Anmelden“.
- Nicht in diesem Paket (bewusst): Dropdown „Geöffnet“ und Vorgangs-Reiter (Teil 3), Bausteine des Leitstands direkt im Mittelfeld ziehen (Ausbau der Vorlage Cockpit), Zuordnung der Firma auf der Verwaltungsseite (Entwurf 12.4 Schritt 3), Einstellungen der übrigen Bereiche (Teil 6), untere Leiste am Handy.
- Tests in Git Bash: `go vet ./...`, `go test ./...`, Integration mit `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable' go test -tags integration ./internal/web/`.

## Review Focus

1. **Terminal mit Systembenutzer:** Die Knöpfe „Kurzzugang beenden“ und „Zurück zum Systemnutzer“ standen in der Kopfleiste (`frame_shared.gohtml` 377–386) – sie müssen im neuen Rahmen im Band Start stehen (`TestRibbonTerminalButtons`, Task 3).
2. **Bearbeiten offen und Wechsel über einen anderen Weg** (Favorit, Bereich, Band, Strg+K-Treffer, Link im Inhalt, Abmelden): immer Nachfrage, Esc/daneben = verwerfen, danach weiter zum Ziel (`TestFrameEditGuardsAllExits` in Task 5, Browserprüfung Task 9).
3. **Rolle ohne Rechte:** Einstellungen-Reiter leer → ausgeblendet; „Zuweisen“, „Als Standard für alle setzen“, Vorlagen-Hinweis fehlen (Tests in Task 1, 3, 6, 8).
4. **Schmale Fenster und große Schrift** (320 px, 160 %): Titel kürzt sich, Uhr bleibt ganz sichtbar, Benutzerknopf links unten bleibt sichtbar, Menüband scrollt waagerecht statt umzubrechen (`TestFrameTopNewCSS` in Task 4, Browserprüfung Task 9).
5. **Zeit läuft schon** (an anderem Gerät gestartet): Die Taste zeigt sofort „Zeit stoppen“ mit der Dauer ab Startzeit des Servers, Stoppen stoppt genau diesen Eintrag (`TestTimeRunningFromServer`, Task 7).

---

### Task 1: Seitenvorlagen-Register und Vorlagen-Hinweis

**Files:**
- Create: `internal/web/page_templates.go`
- Test: `internal/web/page_templates_test.go`
- Modify: `internal/web/handler.go` (BaseData-Feld, Befüllung in `baseData()` neben `ribbonFor`)

**Interfaces:**
- Produces:
  - `type pageTemplate string` mit Konstanten `tplCockpit="cockpit"`, `tplListe="liste"`, `tplDetail="detail"`, `tplEinst="einstellungen"`, `tplPlanung="planung"`, `tplBaum="baum"`, `tplDienst="dienst"`, `tplAktion="aktion"`, `tplDruck="druck"`, `tplTabelle="tabelle"`.
  - `var pageTemplateOf map[string]pageTemplate` – Schlüssel = `navItem`-Schlüssel aus `navAreaDefs`; Werte nach Entwurf 12.2: `dashboard`,`leitstand`,`plantwatch`,`statistics` → Cockpit; `infrastructure`,`storage`,`orgchart` → Baum; `shifts`,`calendar` → Planung; `import`,`export`,`sync` → Tabelle; `core`,`serverconfig`,`network`,`backup`,`roles` → Einstellungen; `nextcloud` → Dienst; alle übrigen → Liste.
  - `func pageTemplateLabel(t pageTemplate) string` → „Cockpit“, „Liste“, „Detail“, „Einstellungen“, „Planung“, „Baum“, „Dienst“, „Aktion“, „Druck“, „Tabelle/Zuordnung“.
  - `type uiMeta struct{ By string; At time.Time }`, `func (h *Handler) uiMetaFor(ctx context.Context, page string) (uiMeta, bool)` – liest `app_settings` Schlüssel `defaults.ui_meta` (JSON `{"<page>":{"by":"Name","at":"RFC3339"}}`); `func (h *Handler) setUIMeta(ctx context.Context, page, byName string)` – Lesen–Ändern–Schreiben in einer Transaktion mit `SELECT … FOR UPDATE`.
  - `BaseData.TemplateTip string` – nur gefüllt, wenn `h.hasPerm(r, permUIDefaults)`: `"Vorlage: Liste"` bzw. `"Vorlage: Cockpit · zuletzt angepasst von K. Muster am 24.11.2026, 14:12"` (Ortszeit `Europe/Berlin`, Format `02.01.2006, 15:04`). Der Titel des neuen Rahmens (Task 4) setzt `title="{{.TemplateTip}}" data-tpl`, wenn gefüllt.

- [ ] **Step 1: Write the failing tests**

```go
func TestEveryAreaPageHasTemplate(t *testing.T) {
	for _, a := range navAreaDefs {
		for _, it := range a.Items {
			if _, ok := pageTemplateOf[it.Key]; !ok {
				t.Errorf("Seite %q (Bereich %s) hat keine Vorlage", it.Key, a.Key)
			}
		}
	}
}
func TestTemplateTipText(t *testing.T) {
	at := time.Date(2026, 11, 24, 13, 12, 0, 0, time.UTC) // 14:12 Ortszeit
	if got := templateTip(tplCockpit, uiMeta{By: "K. Muster", At: at}, true); got != "Vorlage: Cockpit · zuletzt angepasst von K. Muster am 24.11.2026, 14:12" { t.Errorf("%q", got) }
	if got := templateTip(tplListe, uiMeta{}, false); got != "Vorlage: Liste" { t.Errorf("%q", got) }
}
func TestUIMetaRoundTrip(t *testing.T) // -tags integration: setUIMeta("dashboard","K. Muster") → uiMetaFor liefert By=="K. Muster", At innerhalb 5 s
```

`func templateTip(t pageTemplate, m uiMeta, hasMeta bool) string` gehört zu den Produkten dieses Tasks. Für spätere Tasks eine Hilfe `frameData(t, func(*BaseData)) *BaseData` in `page_templates_test.go` auf Basis von `frameTestData(true)` anlegen.

- [ ] **Step 2: Run** `go test ./internal/web -run 'TestEveryAreaPageHasTemplate|TestTemplateTipText'` → FAIL (`pageTemplateOf` undefiniert).
- [ ] **Step 3: Implement** Register, Label, `templateTip`, `uiMetaFor`/`setUIMeta`, `TemplateTip` in `baseData()` (nur im neuen Rahmen).
- [ ] **Step 4: Run** dieselben Tests und `PDH_TEST_DSN=… go test -tags integration ./internal/web -run TestUIMetaRoundTrip` → PASS.
- [ ] **Step 5: Commit** `git commit -m "Oberfläche: Seitenvorlagen-Register und Vorlagen-Hinweis für die Gestaltung (0.82.67)"`

---

### Task 2: Bereich Start und Planung neu ordnen

**Files:**
- Modify: `internal/web/nav_areas.go` (`navAreaDefs`, `navOutsideAreas`, `buildAreas`)
- Modify: `internal/web/handler.go:1603`, `internal/web/palette.go:110` (Aufrufe `buildAreas`)
- Test: `internal/web/nav_areas_test.go`

**Interfaces:**
- Produces:
  - `navAreaDefs`: `start` = `time`, `dashboard`, `leitstand` (in dieser Reihenfolge); `planung` = `time`, `shifts`, `calendar`, `attendance`. `assignments` wandert in `navOutsideAreas` (Seite bleibt; erreichbar über „Zuweisen“, Task 3).
  - `var navSharedItems = map[string]bool{"time": true}` – Schlüssel, die in mehreren Bereichen stehen dürfen.
  - Signatur neu: `func buildAreas(b *BaseData, prefs uiAreaPrefs, want string) areaView` – `want` ist der Bereich aus dem Abfrageparameter `bereich`; er gilt nur, wenn dieser Bereich die aktuelle Seite enthält, sonst der erste Bereich, der sie enthält.
  - Links der Bänder auf Seiten aus `navSharedItems` tragen `?bereich=<area>`.

- [ ] **Step 1: Write the failing tests**

```go
func TestStartAreaBands(t *testing.T) {
	if keys := areaItemKeys("start"); !reflect.DeepEqual(keys, []string{"time", "dashboard", "leitstand"}) { t.Fatalf("Start: %v", keys) }
	if areaItemKeys("planung")[0] != "time" { t.Error("Planung beginnt nicht mit Zeiterfassung") }
	if !navOutsideAreas["assignments"] { t.Error("Zuweisung ist noch ein Band") }
}
func TestSharedPageKeepsArea(t *testing.T) {
	b := frameData(t, func(b *BaseData) { b.Page = "time" })
	if v := buildAreas(b, uiAreaPrefs{}, "planung"); v.Current != "planung" { t.Errorf("Bereich %q", v.Current) }
	if v := buildAreas(b, uiAreaPrefs{}, ""); v.Current != "start" { t.Errorf("ohne Angabe %q", v.Current) }
	if v := buildAreas(b, uiAreaPrefs{}, "material"); v.Current != "start" { t.Errorf("fremder Bereich %q", v.Current) }
}
```

`TestNavAreasCoverAllDefs` so anpassen, dass ein Schlüssel nur doppelt vorkommen darf, wenn er in `navSharedItems` steht.

- [ ] **Step 2: Run** `go test ./internal/web -run 'TestStartAreaBands|TestSharedPageKeepsArea|TestNavAreasCoverAllDefs'` → FAIL.
- [ ] **Step 3: Implement** wie oben; `want` in `handler.go` aus `r.URL.Query().Get("bereich")`, in `palette.go` leer.
- [ ] **Step 4: Run** dieselben Tests plus `TestBuildAreasPermissions TestBuildAreasPrefs TestFrameRail` → PASS.
- [ ] **Step 5: Commit** `git commit -m "Oberfläche: Bereich Start mit Zeiterfassung, Dashboard, Leitstand; Zuweisung kein Band mehr (0.82.67)"`

---

### Task 3: Menüband mit Bändern, Dropdowns und Band Start

**Files:**
- Modify: `internal/web/ribbon.go`
- Create: `internal/web/page_actions.go`
- Modify: `web/static/ui/pdh-frame.js` (Dropdowns, Reiterwechsel ohne Neuladen für Start/Ansicht/Einstellungen/Hilfe, neue Aktionsnamen)
- Modify: `web/static/ui/frame.css`
- Modify: `web/templates/frame.gohtml:27-31` (Unterseiten-Reiter `.frame-tabs` entfernen; Menüband mit Bändern rendern)
- Test: `internal/web/ribbon_test.go`

**Interfaces:**
- Consumes: `areaView.Tabs` (Unterseiten des aktuellen Bereichs, Task 2).
- Produces:
  - `ribbonItem` zusätzlich: `Menu []ribbonItem` (Dropdown), `When string` (`""` immer, `"idle"` nur ohne offene Bearbeitung, `"editing"` nur während Bearbeitung – sichtbar über `html.pdh-editing`), `Count int`, `Primary bool`, `Disabled bool`, `Tip string`.
  - `ribbonTab` zusätzlich: `Href string` (Band = Unterseite, Link), `Band bool`, `Page bool` (Band der offenen Seite), `Selected bool` (beim Laden gewählt: Band der offenen Seite, sonst Start).
  - Reiterfolge: `start` | ein Band je `areaView.Tabs` | `ansicht` | `einstellungen` (nur mit Gruppen) | `hilfe`. Die Reiter „Anpassen“ und „Darstellung“ entfallen; ihr heutiger Inhalt steht als Dropdowns **Darstellung** und **Anpassen** im Reiter Ansicht, dazu dort die Gruppe „Leisten“ (rechte Leiste, schmal, Vollbild).
  - Band Start, Gruppen in dieser Reihenfolge: **Rückgängig** (`Action:"undo"`, Task 5) · **Neu** (`Neu` mit `Menu` je Vorgangsart, Einträge wie die `#create:<typ>`-Treffer der Palette – `Action:"create"`, `Arg:<typ>`; `Zuweisen` mit `Href:"/assignments"` nur bei `b.IsBroker || b.CanManageRoles`) · **Seite** (aus `pageActions(b).Start`, Gruppenname = Seitentitel; fehlt, wenn leer) · **Zeit** (Task 7) · **Finden** (`Suchen` → `palette`, Tip „Strg+K“; `Copilot` → `toggle-right` mit `Arg:"copilot"`) · **Mitteilungen** (`Chat` → `toggle-right` `Arg:"chat"`, „Im Chat teilen“ mit demselben Ziel wie der heutige Kopfleisten-Knopf, `frame_shared.gohtml:366`; nur bei `b.CanChat`) · **Terminal** (nur bei `b.IsSystemUser || b.IsOverrideSession || b.IsBoardVisit`: die Knöpfe aus `frame_shared.gohtml` 377–386 mit denselben Zielen und Formularen).
  - `func areaSettings(b *BaseData, area string) []ribbonItem` für den Reiter Einstellungen – Start: „Startseite-Vorgaben“ → `/core/settings?tab=defaults` (Recht `system.defaults`), „Leitstände“ → `/leitstaende` (`leitstand.manage`); Verwaltung: „Core-Einstellungen“ → `/core/settings`, „Server-Einstellungen“ → `/admin/server-config` (jeweils mit der Prüfung, die `navDefs` für diese Seiten nutzt); andere Bereiche leer.
  - Reiter Hilfe: „Hilfe zu dieser Seite“ (`toggle-right` `Arg:"help"`), „Handbuch“ (`/help`), „Versionen & Updates“ (`/help#` + ID des Kapitels „Versionen & Updates“ aus `helpChapterIDs`).
  - `page_actions.go`: `type pageActs struct{ Band []ribbonGroup; Start []ribbonItem }`, `func pageActions(b *BaseData) pageActs` mit `switch b.Page`; hier nur Gerüst und `default` (leer). Die Gruppen aus `pageActs.Band` stehen im Band der offenen Seite.
  - `ribbonActions` ergänzt um `undo`, `save-edit`, `cancel-edit`, `time-start`, `time-stop`, `time-manual`, `dash-standard`, `leit-standard`.

- [ ] **Step 1: Write the failing tests**

```go
func TestRibbonTabsOrder(t *testing.T) {
	b := frameData(t, func(b *BaseData) { b.Page = "dashboard" })
	if got := tabKeys(ribbonFor(b)); !reflect.DeepEqual(got, []string{"start", "time", "dashboard", "leitstand", "ansicht", "einstellungen", "hilfe"}) { t.Fatalf("Reiter %v", got) }
	if sel := selectedTab(ribbonFor(b)); sel != "dashboard" { t.Errorf("gewählt %q", sel) }
}
func TestRibbonStartGroups(t *testing.T) {
	b := frameData(t, func(b *BaseData) { b.Page = "faults"; b.IsBroker, b.CanManageRoles = false, false })
	st := tab(ribbonFor(b), "start")
	for _, l := range []string{"Rückgängig", "Neu", "Zeit", "Finden"} { if !slices.Contains(groupLabels(st), l) { t.Errorf("Gruppe %q fehlt", l) } }
	if hasItem(st, "Zuweisen") { t.Error("Zuweisen ohne Recht") }
}
func TestRibbonTerminalButtons(t *testing.T) {
	html := renderFrameForTest(t, "dashboard", frameData(t, func(b *BaseData) { b.IsSystemUser, b.IsOverrideSession = true, true }))
	if !strings.Contains(between(html, `<section class="ribbon"`, `</section>`), `action="/override-logout"`) { t.Error("„Zurück zum Systemnutzer“ fehlt im Menüband") }
}
func TestRibbonSettingsHiddenWithoutRights(t *testing.T) {
	b := frameData(t, func(b *BaseData) { b.Page = "dashboard"; b.navCan = func(string) bool { return false } })
	if slices.Contains(tabKeys(ribbonFor(b)), "einstellungen") { t.Error("leerer Reiter Einstellungen sichtbar") }
}
func TestFrameHasNoTabBar(t *testing.T) {
	html := renderFrameForTest(t, "dashboard", frameData(t, nil))
	if strings.Contains(html, `class="frame-tabs"`) { t.Error("Reiterleiste über dem Menüband noch da") }
	if !strings.Contains(html, `data-ribbon-band href="/time`) { t.Error("Band Zeiterfassung fehlt") }
}
```

(`tabKeys`, `tab`, `group`, `groupLabels`, `hasItem`, `hasAction`, `hasHref`, `keysWhen`, `selectedTab`, `between` als kleine Hilfen in `ribbon_test.go`; wie `navCan` im Test gesetzt wird, steht in `TestBuildAreasPermissions`.)

- [ ] **Step 2: Run** `go test ./internal/web -run 'TestRibbon|TestFrameHasNoTabBar|TestFrameRibbon'` → FAIL.
- [ ] **Step 3: Implement** Typen, Reiterfolge, Gruppen, `areaSettings`, Gerüst `pageActions`, Vorlage und JS. Bänder sind `<a data-ribbon-band href=…>`; Start/Ansicht/Einstellungen/Hilfe schalten im Browser ohne Neuladen; Dropdowns öffnen unter dem Knopf, Esc schließt.
- [ ] **Step 4: Run** dieselben Tests plus `TestFrameHasWizards TestRibbonForBasics TestFrameRibbon TestFramePaletteHooks` (alte Erwartungen an die Reiter „Anpassen“/„Darstellung“ auf die Dropdowns umstellen) → PASS.
- [ ] **Step 5: Commit** `git commit -m "Oberfläche: Menüband mit Bändern des Bereichs, Band Start, Ansicht/Einstellungen/Hilfe (0.82.67)"`

---

### Task 4: Kopfzeile, Benutzer links unten, Fußzeile

**Files:**
- Create: `web/templates/widgets/frame_new.gohtml` (`{{define "frame-top"}}`, `{{define "frame-user"}}`, `{{define "frame-foot"}}`)
- Modify: `web/templates/widgets/frame_shared.gohtml` (Einträge des Benutzermenüs 393–436 in `{{define "ui-usermenu-items"}}` auslagern; `ui-topbar` nutzt sie – klassisch unverändert)
- Modify: `web/templates/frame.gohtml` (`ui-topbar` → `frame-top`; `frame-user` als Fuß der `.rail`; `frame-foot` statt der eigenen Statusleiste 43–54, IDs der Statusleiste bleiben gleich)
- Modify: `internal/web/presence.go`, `internal/web/handler.go` (BaseData)
- Modify: `web/static/ui/frame.css`, `web/static/ui/pdh-frame.js` (Benutzermenü öffnet nach oben bzw. rechts daneben)
- Test: `internal/web/frame_new_test.go`

**Interfaces:**
- Produces:
  - `func (h *Handler) presenceCounts(ctx context.Context) (online, total int)` – `online` = Benutzer, die `presenceOnline` meldet, ohne Systembenutzer; `total` = `SELECT count(*) FROM users WHERE active AND NOT is_system_user`; 30 s Zwischenspeicher je Server.
  - `BaseData.UsersOnline, UsersTotal int` (nur im neuen Rahmen und für Angemeldete).
  - Fußzeile rechts: `V{{appVersion}}`, angemeldet zusätzlich ` · <span title="12 angemeldet (Aktivität in den letzten 2 Minuten, ohne Terminals) von 40 aktiven Benutzern">12/40</span>`.
  - `frame-top`: nur Logo (`.Brand`), Titel (`.Title`, mit `TemplateTip` aus Task 1), Uhr `<div id="pdh-clock" class="topbar-clock" data-server-ms=…>` **ohne Link** (der Uhr-Code in den Skripten darf kein `<a>` voraussetzen). Keine Suchzeile, kein Chat-, Copilot-, Hell/Dunkel- oder Leisten-Knopf.
  - `frame-user`: Knopf mit Bild bzw. Kürzel und Vornamen; Menü = `ui-usermenu-items` plus Sprache.

- [ ] **Step 1: Golden-Datei des klassischen Rahmens erzeugen**, bevor `frame_shared.gohtml` angefasst wird: Test `TestClassicTopbarUnchanged` schreiben (rendert `base.gohtml` mit `frameTestData(false)`, schneidet `ui-topbar` heraus, vergleicht mit `testdata/classic_topbar.golden`; mit `PDH_GOLDEN_UPDATE=1` schreibt er die Datei), einmal mit `PDH_GOLDEN_UPDATE=1` laufen lassen, Golden-Datei committen.
- [ ] **Step 2: Write the failing tests**

```go
func TestFrameTopNew(t *testing.T) {
	top := between(renderFrameForTest(t, "dashboard", frameData(t, nil)), `<header class="ft-top"`, `</header>`)
	for _, gone := range []string{"data-palette-open", `href="/time"`, "theme-toggle", "topbar-user"} {
		if strings.Contains(top, gone) { t.Errorf("Kopfzeile enthält noch %q", gone) }
	}
	if !strings.Contains(top, `id="pdh-clock"`) { t.Error("Uhr fehlt") }
}
func TestFrameTopNewCSS(t *testing.T) {
	css := readUIFile(t, "frame.css")
	for _, s := range []string{".ft-top .topbar-clock{flex:none", ".ft-title{", "text-overflow:ellipsis", ".ft-user{flex:none"} {
		if !strings.Contains(strings.ReplaceAll(css, " ", ""), strings.ReplaceAll(s, " ", "")) { t.Errorf("frame.css: %q fehlt", s) }
	}
}
func TestFrameUserBottomLeft(t *testing.T) {
	rail := between(renderFrameForTest(t, "dashboard", frameData(t, nil)), `<nav class="rail"`, `</nav>`)
	if !strings.Contains(rail, `class="ft-user"`) || !strings.Contains(rail, `href="/logout"`) { t.Error("Benutzermenü nicht links unten") }
}
func TestFrameFootCounts(t *testing.T) {
	html := renderFrameForTest(t, "dashboard", frameData(t, func(b *BaseData) { b.UsersOnline, b.UsersTotal = 12, 40 }))
	if !strings.Contains(html, ">12/40<") || !strings.Contains(html, ">V"+pdh.Version()) { t.Error("Fußzeile ohne Version oder Zahl") }
}
func TestPresenceCounts(t *testing.T) // -tags integration: ein aktiver Systembenutzer zählt weder in online noch in total
```

(`readUIFile(t, name)` liest `../../web/static/ui/<name>`; gibt es die Hilfe noch nicht, hier anlegen.)

- [ ] **Step 3: Run** `go test ./internal/web -run 'TestFrameTopNew|TestFrameUserBottomLeft|TestFrameFootCounts'` → FAIL.
- [ ] **Step 4: Implement** Vorlagen, `presenceCounts`, CSS (Kopfzeile 44 px; Titel kürzt mit „…“; Uhr und Benutzerknopf schrumpfen nie; Benutzerknopf auch bei eingeklappter Leiste sichtbar).
- [ ] **Step 5: Run** dieselben Tests, `TestClassicTopbarUnchanged`, `TestFrameStatusbar`, `TestFrameTopbarFits` (auf die neue Kopfzeile umstellen), Integration `TestPresenceCounts` → PASS.
- [ ] **Step 6: Commit** `git commit -m "Oberfläche: Kopfzeile nur Logo, Titel, Uhr; Benutzer links unten; Fußzeile mit Version und Angemeldeten (0.82.67)"`

---

### Task 5: Bearbeitungsschritte – Rückgängig, Anzeige, Nachfrage

**Files:**
- Create: `web/static/ui/pdh-edit.js`
- Modify: `web/templates/frame.gohtml` (Skript einbinden), Texte im `"t"`-Teil von `pdh-boot` (`frame_shared.gohtml` `ui-head`)
- Modify: `web/static/ui/pdh-frame.js` (Aktionen `undo`, `save-edit`, `cancel-edit` an `pdhEdit`; Wächter an allen Wegen weg von der Seite)
- Modify: `web/static/ui/frame.css` (`[data-when="editing"]` nur bei `html.pdh-editing`, `[data-when="idle"]` nur ohne)
- Test: `internal/web/frame_edit_test.go`

**Interfaces:**
- Produces (Browser, `window.pdhEdit`):
  - `begin({name, save, discard})` – `save: () => Promise<boolean>`, `discard: () => void`; setzt `html.pdh-editing`.
  - `step(label, undo)` – merkt einen Schritt; Statusleiste „● N nicht gespeichert“, Taste „Rückgängig (N)“ aktiv.
  - `undo()` – letzten Schritt zurück; auch Strg+Z, außer der Fokus steht in einem Eingabefeld.
  - `save()` – ruft `save`; bei `true` Schritte leeren, `pdh-editing` entfernen, Statusleiste „Gespeichert: <name>.“
  - `cancel()` – alle Schritte rückwärts zurück, dann `discard()`.
  - `guard(go)` – ohne offene Schritte sofort `go()`; sonst Fenster „Änderungen speichern?“ mit „Verwerfen“ und „Speichern“; Esc, Klick daneben oder „Verwerfen“ → `cancel()`, dann `go()`, Statusleiste „Nicht bestätigt – Eingaben verworfen.“; „Speichern“ → `save()`, bei Erfolg `go()`.
  - Der Wächter fängt (Erfassung in der Capture-Phase) Klicks auf `a[href]` außerhalb von `[data-edit-keep]`, Bänder, Bereiche, Favoriten, Strg+K-Treffer, Absenden von Formularen außerhalb von `[data-edit-keep]` und „Abmelden“. Beim Schließen des Fensters (`pagehide`) wird nichts gespeichert und nicht nachgefragt (Entwurf 11.12).

- [ ] **Step 1: Write the failing tests**

```go
func TestFrameEditScript(t *testing.T) {
	html := renderFrameForTest(t, "dashboard", frameData(t, nil))
	if !strings.Contains(html, uiURL("pdh-edit.js")) { t.Fatal("pdh-edit.js nicht eingebunden") }
	js := readUIFile(t, "pdh-edit.js")
	for _, s := range []string{"window.pdhEdit", "begin", "step", "undo", "guard", "pdh-editing", `pdhT("Änderungen speichern?")`, `pdhT("Verwerfen")`} {
		if !strings.Contains(js, s) { t.Errorf("pdh-edit.js: %q fehlt", s) }
	}
	if strings.Contains(js, "beforeunload") { t.Error("beim Schließen wird nicht nachgefragt (11.12)") }
}
func TestFrameEditGuardsAllExits(t *testing.T) {
	js := readUIFile(t, "pdh-edit.js") + readUIFile(t, "pdh-frame.js")
	for _, s := range []string{"data-edit-keep", "addEventListener('click'", "addEventListener('submit'", "pdhEdit.guard("} {
		if !strings.Contains(js, s) { t.Errorf("Wächter: %q fehlt", s) }
	}
}
func TestFrameEditTextsInBoot(t *testing.T) // bootValue(t, html, "t") enthält „Änderungen speichern?“, „Verwerfen“, „Speichern“, „nicht gespeichert“, „Rückgängig“, „Nicht bestätigt – Eingaben verworfen.“
```

- [ ] **Step 2: Run** `go test ./internal/web -run 'TestFrameEdit'` → FAIL.
- [ ] **Step 3: Implement** `pdh-edit.js` (keine Vorlagen-Syntax, Texte über `pdhT`), Texte in `pdh-boot`, Anbindung in `pdh-frame.js`, CSS.
- [ ] **Step 4: Run** `go test ./internal/web -run 'TestFrameEdit|TestUIFilesNoTemplateSyntax|TestColorLiteralsRatchet'` → PASS.
- [ ] **Step 5: Commit** `git commit -m "Oberfläche: Rückgängig, Anzeige ungespeicherter Schritte und Nachfrage vor dem Verwerfen (0.82.67)"`

---

### Task 6: Dashboard als Vorlage Cockpit

**Files:**
- Modify: `internal/web/page_actions.go` (`case "dashboard"`)
- Modify: `web/templates/widgets/dashboard_widgets.gohtml` (Knöpfe „Bearbeiten“/„Fertig“ im Inhalt nur im klassischen Rahmen; Schritte an `pdhEdit` melden; `dwSave` gibt `true`/`false` zurück)
- Modify: `internal/web/user_defaults.go` (`DefaultsSaveWeb` mit `what=dashboard&action=set` ruft `setUIMeta(ctx, "dashboard", <Name des Benutzers>)`)
- Test: `internal/web/page_actions_test.go`

**Interfaces:**
- Consumes: `pdhEdit` (Task 5), `pageActs` (Task 3), `setUIMeta` (Task 1), `window.dwEdit(on)`, `window.dwSave()`, `POST /dashboard/widgets`, `POST /core/settings/defaults`.
- Produces:
  - Band „Dashboard“, Gruppe „Startseite“: `Bearbeiten` (`When:"idle"`, Aktion `dashboard-edit`), `Widget hinzufügen` (`When:"editing"`, Aktion `addw`, öffnet den vorhandenen Katalog), `Speichern` (`When:"editing"`, `save-edit`, `Primary`), `Abbrechen` (`When:"editing"`, `cancel-edit`), `Auf Vorgabe zurücksetzen` (heutige Rücksetzung `{reset:true}`); Gruppe „Firma“ nur mit `system.defaults`: `Als Standard für alle setzen` (`dash-standard`, `When:"idle"`, Rückfrage: „Die jetzige Anordnung wird die Vorgabe der Firma. Wer seine Startseite schon angepasst hat, behält sie, bis er „Auf Vorgabe zurücksetzen“ drückt.“ → `POST /core/settings/defaults` mit `what=dashboard&action=set`).
  - `pageActs.Start` für das Dashboard: `Bearbeiten` (`idle`), `Speichern` und `Abbrechen` (`editing`).
  - Im Bearbeiten-Modus meldet jedes Hinzufügen, Entfernen, Verschieben und jede Breitenänderung eines Widgets `pdhEdit.step(label, undo)`; Start mit `pdhEdit.begin({name: "Dashboard", save: dwSave, discard: () => location.reload()})`.

- [ ] **Step 1: Write the failing tests**

```go
func TestDashboardActions(t *testing.T) {
	b := frameData(t, func(b *BaseData) { b.Page = "dashboard"; b.navCan = func(p string) bool { return p == "system.defaults" } })
	a := pageActions(b)
	if got := keysWhen(a.Band, "editing"); got != "addw,save-edit,cancel-edit" { t.Errorf("Bearbeiten: %s", got) }
	if !hasAction(a.Band, "dash-standard") { t.Error("„Als Standard für alle setzen“ fehlt") }
	b.navCan = func(string) bool { return false }
	if hasAction(pageActions(b).Band, "dash-standard") { t.Error("„Als Standard“ ohne Recht") }
}
func TestDashboardHidesOwnEditButtonsInNewFrame(t *testing.T) // neuer Rahmen: kein `id="dw-save-btn"` im Inhalt; klassisch: vorhanden
func TestDashboardStandardWritesMeta(t *testing.T) // -tags integration: DefaultsSaveWeb(what=dashboard, action=set) → uiMetaFor("dashboard").By == Name des Benutzers
```

- [ ] **Step 2: Run** `go test ./internal/web -run 'TestDashboard(Actions|HidesOwn)'` → FAIL.
- [ ] **Step 3: Implement** Einträge, Anbindung an `pdhEdit`, Rückgabewert von `dwSave`, Meta beim Standard.
- [ ] **Step 4: Run** dieselben Tests und Integration `TestDashboardStandardWritesMeta` → PASS.
- [ ] **Step 5: Commit** `git commit -m "Oberfläche: Dashboard bearbeiten, speichern, abbrechen und als Standard setzen im Menüband (0.82.67)"`

---

### Task 7: Zeiterfassung im Band Start

**Files:**
- Modify: `internal/web/handler.go` (BaseData `TimeRunning`), `internal/web/page_actions.go` (`case "time"`), `internal/web/ribbon.go` (Gruppe „Zeit“)
- Create: `web/templates/widgets/frame_time.gohtml` (`{{define "frame-time-manual"}}` – Fenster „Zeit manuell erfassen“, Vorlage Aktion)
- Modify: `web/static/ui/pdh-frame.js` (Aktionen `time-start`, `time-stop`, `time-manual`, laufende Dauer jede Sekunde)
- Test: `internal/web/frame_time_test.go`

**Interfaces:**
- Consumes: `timetracking` `GetRunning` (`internal/modules/timetracking/timetracking.go:548`), `POST /time/start-web`, `POST /time/{id}/stop-web`, `POST /time/manual-web` (Felder genau wie das Formular auf `/time`; Handler `TimeManualWeb`).
- Produces:
  - `type timeRunning struct{ ID string; Since time.Time }`, `BaseData.TimeRunning *timeRunning` – im neuen Rahmen für Angemeldete außer dem Systembenutzer.
  - Gruppe „Zeit“ im Band Start in jedem Bereich: ohne laufende Zeit `Zeit starten` (`time-start`); mit laufender Zeit `Zeit stoppen` mit `data-since="<RFC3339 UTC>"` und Dauer `H:MM:SS` (`time-stop`, `Arg:<ID>`); immer `Manuell erfassen` (`time-manual`). Nicht für den Systembenutzer.
  - `pageActs.Band` für `time`: dieselbe Gruppe „Zeit“.
  - Nach Start, Stopp oder Buchen lädt die Seite neu (10.2) und meldet in der Statusleiste: „Zeit läuft seit 06:43.“, „Zeit gestoppt und gebucht.“, „Gebucht: 1 h 30 min am 26.11.2026.“
  - Die Dauer rechnet das Skript aus `data-since` und dem Serverzeit-Versatz der Uhr (`data-server-ms`), nicht aus der Rechneruhr.

- [ ] **Step 1: Write the failing tests**

```go
func TestTimeButtonsIdle(t *testing.T) {
	g := group(tab(ribbonFor(frameData(t, func(b *BaseData) { b.Page = "faults" })), "start"), "Zeit")
	if !hasAction(g, "time-start") || !hasAction(g, "time-manual") { t.Error("Zeit starten/Manuell erfassen fehlen") }
}
func TestTimeButtonsRunning(t *testing.T) {
	since := time.Date(2026, 11, 26, 5, 43, 0, 0, time.UTC)
	html := renderFrameForTest(t, "dashboard", frameData(t, func(b *BaseData) { b.TimeRunning = &timeRunning{ID: "01a2-stop", Since: since} }))
	if !strings.Contains(html, `data-ribbon-action="time-stop" data-ribbon-arg="01a2-stop"`) || !strings.Contains(html, `data-since="2026-11-26T05:43:00Z"`) { t.Error("„Zeit stoppen“ für die laufende Zeit fehlt") }
}
func TestTimeNotForSystemUser(t *testing.T) // IsSystemUser → keine Gruppe „Zeit“ im Band Start
func TestTimeManualFormFields(t *testing.T) // frame-time-manual postet an /time/manual-web mit genau den Feldnamen, die TimeManualWeb liest (aus dem Quelltext des Handlers per Regex r.FormValue\("(\w+)"\) gesammelt)
func TestTimeRunningFromServer(t *testing.T) // -tags integration: Start über /time/start-web, danach baseData → TimeRunning.ID = laufender Eintrag, Since = dessen Startzeit
```

- [ ] **Step 2: Run** `go test ./internal/web -run 'TestTime(ButtonsIdle|ButtonsRunning|NotForSystemUser|ManualFormFields)'` → FAIL.
- [ ] **Step 3: Implement** Feld, Gruppe, Fenster, JS.
- [ ] **Step 4: Run** dieselben Tests und Integration `TestTimeRunningFromServer` → PASS.
- [ ] **Step 5: Commit** `git commit -m "Oberfläche: Zeit starten, stoppen und manuell erfassen im Band Start (0.82.67)"`

---

### Task 8: Leitstand im Rahmen und ohne Anmeldung

**Files:**
- Create: `internal/web/leitstand_frame.go` (Seite `GET /leitstand`, Seitenkennung `leitstand`), `web/templates/leitstand_frame.gohtml`
- Modify: `internal/web/nav.go:61` (`leitstand` → Ziel `/leitstand`), `internal/web/handler.go` (Route im angemeldeten Teil)
- Modify: `internal/web/global_dashboard.go`, `web/templates/global_dashboard.gohtml` (Parameter `eingebettet=1` blendet den eigenen Kopf aus; `LoginCorner`)
- Modify: `internal/web/leitstand_boards.go` (Route `POST /leitstaende/{id}/default`, Recht `leitstand.manage`, setzt `is_default` für genau diesen Leitstand in einer Transaktion und ruft `setUIMeta(ctx, "leitstand", <Name>)`), `internal/web/perm_gate.go` (`gateRules`-Eintrag für die Route)
- Modify: `internal/web/page_actions.go` (`case "leitstand"`)
- Test: `internal/web/leitstand_frame_test.go`

**Interfaces:**
- Consumes: `leitstandWhere`, `canManageLeitstand`, Firmenvorgabe `defaults.ui_frame` (`ui_frame.go:14`).
- Produces:
  - `/leitstand`: neuer Rahmen mit `<iframe class="ft-embed" src="/global/?eingebettet=1" title="Leitstand">` im Mittelfeld (gleiche Herkunft; vorher prüfen, dass `/global/` kein `X-Frame-Options: DENY` bzw. `frame-ancestors 'none'` sendet).
  - Band „Leitstand“: Gruppe „Leitstand“ `Bearbeiten` → `/leitstaende/{id des angezeigten Leitstands}` (nur `leitstand.manage`); Gruppe „Firma“ `Als Standard für alle setzen` (`leit-standard`, Rückfrage, nur `leitstand.manage`); Gruppe „Anzeige“ `Vollbild (Wandbildschirm)` → `/global/?vollbild=1` in neuem Fenster.
  - `GlobalDashboardPageData.LoginCorner bool` – true, wenn nicht angemeldet, Firmenvorgabe = `new` und weder `vollbild=1` noch `eingebettet=1`; zeigt unten links nur den Knopf „Anmelden“ (`/login?next=/global/`, Klasse `gd-login-corner`).

- [ ] **Step 1: Write the failing tests**

```go
func TestLeitstandBandActions(t *testing.T) {
	b := frameData(t, func(b *BaseData) { b.Page = "leitstand"; b.navCan = func(p string) bool { return p == "leitstand.manage" } })
	a := pageActions(b)
	if !hasAction(a.Band, "leit-standard") || !hasHref(a.Band, "/global/?vollbild=1") { t.Error("Leitstand-Band unvollständig") }
	b.navCan = func(string) bool { return false }
	if hasAction(pageActions(b).Band, "leit-standard") { t.Error("ohne Recht") }
}
func TestGlobalEmbeddedHidesHeader(t *testing.T) // eingebettet=1 → kein eigener Kopf, kein `gd-login-corner`
func TestGlobalLoginCorner(t *testing.T) // nicht angemeldet + Vorgabe neu → `gd-login-corner`; mit vollbild=1 oder Vorgabe klassisch → nicht vorhanden
func TestLeitstandDefaultRoute(t *testing.T) // -tags integration: ohne Recht 403; mit Recht genau ein is_default, uiMetaFor("leitstand") gesetzt
```

`TestOwnHeadPagesIgnoreFrame` bleibt grün (`/global/` bleibt eigene Seite).

- [ ] **Step 2: Run** `go test ./internal/web -run 'TestLeitstandBandActions|TestGlobalEmbeddedHidesHeader|TestGlobalLoginCorner'` → FAIL.
- [ ] **Step 3: Implement** Seite, Routen, Parameter, Eck-Knopf (Farben über Variablen; Fiori-Variante nach AGENTS.md „eigener Seitenkopf“), Standard-Route mit Recht.
- [ ] **Step 4: Run** dieselben Tests, `TestOwnHeadPagesIgnoreFrame`, Integration `TestLeitstandDefaultRoute` → PASS.
- [ ] **Step 5: Commit** `git commit -m "Oberfläche: Leitstand im Rahmen, als Standard setzen, ohne Anmeldung nur „Anmelden“ (0.82.67)"`

---

### Task 9: App ohne Browserleisten, Handbuch, Version, Browserprüfung

**Files:**
- Modify: `web/static/site.webmanifest` (zusätzlich `"id": "/"`, `"scope": "/"`, `"lang": "de"`, `"description": "PDH – Pulse Data Hub"`)
- Modify: Eintrag „App installieren“ in `ui-usermenu-items`: am Rechner „Als App installieren“ (nutzt `beforeinstallprompt`, wenn der Browser es anbietet, sonst Hinweis „Im Browser-Menü „App installieren“ wählen. Nur über HTTPS möglich.“), am Handy wie heute die PDH-App
- Modify: `web/templates/help.gohtml` (Kapitel des neuen Rahmens: Kopfzeile, Benutzer links unten, Menüband Start | Bänder | Ansicht | Einstellungen | Hilfe, Band Start wächst mit der Seite, Rückgängig/Speichern/Verwerfen, Zeiterfassung, Leitstand, Fußzeile; neu „So installierst du das PDH als App“ mit `.hb-howto`/`.hb-steps`), `web/templates/widgets/help_figures.gohtml` (Skizze nach der Vorschau)
- Modify: `VERSION`, `CHANGELOG.md` (`### Neu`/`### Geändert`, ein Satz je Punkt), `AGENTS.md` (Punkt 32 Stand; Abschnitt „Kopfleiste“ um die Regeln des neuen Rahmens ergänzen: Uhr und Benutzerknopf immer ganz sichtbar, Benutzer links unten, keine Knöpfe in der Kopfzeile)
- Test: `internal/web/manifest_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestWebManifestInstallable(t *testing.T) {
	var m map[string]any
	raw, err := os.ReadFile("../../web/static/site.webmanifest")
	if err != nil { t.Fatal(err) }
	if err := json.Unmarshal(raw, &m); err != nil { t.Fatal(err) }
	for k, want := range map[string]string{"id": "/", "scope": "/", "start_url": "/", "display": "standalone", "lang": "de"} {
		if m[k] != want { t.Errorf("%s = %v, erwartet %q", k, m[k], want) }
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/web -run TestWebManifestInstallable` → FAIL.
- [ ] **Step 3: Implement** Manifest, Menüeintrag, Handbuch, Skizze, VERSION/CHANGELOG/AGENTS.
- [ ] **Step 4: Run** `go vet ./...`, `go test ./...`, `go test -tags integration ./internal/web/` → PASS bis auf die in AGENTS.md genannten bekannten roten; dabei `TestHelpCoversAllPages`, `TestHelpChaptersMatch`, `TestChangelogMatchesVersion`, `TestFrameHasWizards` grün.
- [ ] **Step 5: Browserprüfung lokal** (gerenderte Seiten wie bei 0.82.65, Edge über `playwright-core`): Fiori/Fluent × hell/dunkel × 320/390/768/1024/1366 px, dazu 120–160 %. Prüfen: Uhr und Benutzerknopf ganz sichtbar, Titel gekürzt, Menüband scrollt; Band Start in Start, Vorgänge und Material; Dashboard bearbeiten → zwei Widgets entfernen → „Rückgängig (2)“ → einmal zurück → Bereich wechseln → Nachfrage → Esc → verworfen; Zeit starten und stoppen; Neu-anlegen- und Fertigstellen-Assistent öffnen; Terminal mit Systembenutzer: „Zurück zum Systemnutzer“ im Band Start. Keine Konsolenfehler, kein waagerechter Überlauf.
- [ ] **Step 6: Commit** `git commit -m "Oberfläche: PDH als App installierbar, Handbuch und Änderungsprotokoll (0.82.67)"`

Danach Schlussprüfung (Claude und Codex), Merge, Verbund-Update und Browserprüfung am Testverbund nach AGENTS.md. Die App-Installation lässt sich am Testverbund nicht prüfen (nur HTTP), sondern auf 10.10.0.202 über den Namen mit Zertifikat (Anwender).
