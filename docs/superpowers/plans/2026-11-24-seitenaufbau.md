# Schneller Seitenaufbau (Teil 1 von 0.90.0) – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jede PDH-Seite baut beim ersten Aufruf in höchstens 0,5 s (4× gedrosselt 1,5 s) auf, ohne etwas aus dem Internet zu laden; alle 5 918 Symbole bleiben überall nutzbar.

**Architecture:** Die Symbolschrift (1,4 MB CSS mit eingebetteter WOFF2) wird durch SVG-Masken ersetzt: ein Go-Paket liest die SVG-Daten von Tabler Icons 3.30.0, der Server erzeugt daraus eine Stildatei mit einer Regel je Klasse – die vom PDH genutzten und die gerade gewählten Symbole eingebettet, alle übrigen als Verweis auf eine kleine SVG-Adresse. Alle Fremdbibliotheken und Schriften liegen unter `web/static/vendor/` und werden unter einer Adresse mit der Version im Pfad ausgeliefert.

**Tech Stack:** Go (chi, html/template, `embed`), CSS `mask-image`, Node + playwright-core nur für das Messskript.

**Spec:** `docs/superpowers/specs/2026-11-24-seitenaufbau-design.md` (vom Anwender bestätigt am 24.11.2026)

## Global Constraints

- Version: nächste Korrekturversion über `origin/main` (heute 0.82.55 → **0.82.56**), vor dem Merge `git show origin/main:VERSION` prüfen. Keine Migration, kein neues Recht.
- Unveränderte Versionen: Tabler Icons **3.30.0**, htmx **1.9.12**, Sortable **1.15.2**, frappe-gantt **0.6.1**, Chart.js **4.4.0**, xlsx **0.18.5**, mammoth **1.8.0**; Schriften DM Sans, Source Sans 3, Roboto, Inter, Atkinson Hyperlegible in den heute genutzten Schnitten.
- Fremddateien einmal herunterladen (npm-Registry bzw. Google Fonts), unverändert unter `web/static/vendor/` einchecken; jede in `THIRD_PARTY_LICENSES.md` mit Lizenz.
- Adressen der mitgelieferten Dateien: **Version im Pfad** – `/vendor/<appVersion>/…`. `immutable` (ein Jahr) nur bei Erfolg für die laufende Version; fremde Version, unbekannte Datei/Name und Fehler: `404` bzw. Fehler mit `Cache-Control: no-store`. Richtige `Content-Type` (`text/css`, `text/javascript`, `image/svg+xml`, `font/woff2`). gzip und ETag wie heute (`serveStaticAsset`).
- Vorlagen behalten `<i class="ti ti-…">`; kein Umbau der Seiten.
- Bestehende Dateien haben CRLF – nur mit dem Edit-Werkzeug ändern. Tests in Git Bash: `go vet ./...`, `go test ./...`; Datenbanktests mit `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'` (`-tags integration` im Paket `web`).
- Texte deutsch; Handbuch und `CHANGELOG.md` im selben Schritt; Commits deutsch, ohne Zusatzzeilen; kein Push, Merge oder Einspielen ohne Freigabe.

## Review Focus

1. **Symbole sitzen wie vorher** neben Text, in Knöpfen, Schildern und der Navigation (Größe `1em`, Grundlinie) – Bildschirmfotos vorher/nachher in Task 6.
2. **Offene Seiten während eines Updates** verweisen noch auf die alte Adresse `/vendor/tabler-icons/tabler-icons.min.css?v=<alt>`: sie bekommen eine Weiterleitung (`302`, `no-store`) auf die aktuelle Stildatei, Symbole bleiben (Test in Task 3).
3. **Druck** zeigt die Symbole (`print-color-adjust: exact`) – Test auf die Regel in Task 2, Druckansicht im Browser in Task 6.
4. **Windows-Kontrastmodus** (`forced-colors`): Symbole in `CanvasText` sichtbar – Test auf die Regel in Task 2.
5. **Eigene Seitenköpfe** (`easy`, `global_dashboard`, `push_alert`, `terminal`, `terminal_device_error`) laden dieselbe Stildatei; das Raspberry-Pi-Terminal lädt nichts aus dem Internet – Test in Task 3 bzw. Task 4.

---

### Task 1: Symbol-Quelle und Paket `tablericons`

**Files:**
- Create: `web/static/vendor/tabler-icons/tabler-nodes-outline.json`, `web/static/vendor/tabler-icons/tabler-nodes-filled.json` (aus dem npm-Paket `@tabler/icons@3.30.0`; fehlen sie dort, einmalig aus `icons/outline|filled/*.svg` erzeugt und so eingecheckt), `web/static/vendor/tabler-icons/names.txt` (die 5 918 Klassen aus der heutigen `tabler-icons.min.css`, eine je Zeile, sortiert)
- Create: `internal/web/tablericons/tablericons.go`, `internal/web/tablericons/tablericons_test.go`

**Interfaces:**
- Produces:
  - `type Set struct{…}`; `func Load(fsys fs.FS) (*Set, error)` (liest die beiden JSON-Dateien und `names.txt` aus `web/static/vendor/tabler-icons/`), `func Default() *Set` (einmal aus `pdh.Static`, Fehler → Panik beim Start).
  - `func (s *Set) Names() []string` – die Klassen aus `names.txt` (`ti-…`), sortiert.
  - `func (s *Set) Has(class string) bool`
  - `func (s *Set) SVG(class string) ([]byte, bool)` – vollständige SVG (`xmlns`, `viewBox="0 0 24 24"`, Umriss: `fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"`, gefüllt: `fill="currentColor"`); `ti-x-filled` → gefüllter Satz `x`, sonst Umriss `x`.

- [ ] **Step 1: Failing tests** `TestSetHasAllNames` (jede Zeile aus `names.txt` hat eine SVG; Anzahl 5 918), `TestSVGWellFormed` (je 50 zufällige Namen: `encoding/xml` liest sie, `viewBox` 0 0 24 24), `TestSVGFilled` (`ti-star-filled` hat `fill="currentColor"`, `ti-star` `fill="none"`), `TestUnknownName` (`ti-gibt-es-nicht` → false).
- [ ] **Step 2:** `go test ./internal/web/tablericons` → FAIL (Paket fehlt).
- [ ] **Step 3:** Dateien herunterladen/erzeugen und einchecken; Paket umsetzen.
- [ ] **Step 4:** `go test ./internal/web/tablericons` → PASS.
- [ ] **Step 5:** Commit `Seitenaufbau: Tabler-Symbole als SVG-Quelle (3.30.0)`.

### Task 2: Stildatei und SVG-Adressen

**Files:**
- Create: `internal/web/iconcss.go`, `internal/web/iconcss_test.go`, `internal/web/icons_used.txt` (alle vom PDH genutzten Klassen), `internal/web/iconcss_integration_test.go`
- Modify: `internal/web/favicon.go` (Auslieferung), `internal/web/template_funcs.go` (`iconCSSHref`), `cmd/server/main.go` (Routen)

**Interfaces:**
- Consumes: `tablericons.Default()`, `Set.Names/Has/SVG` (Task 1).
- Produces:
  - `func buildIconCSS(set *tablericons.Set, embedded []string, version string) []byte` – Grundregeln `.ti{display:inline-block;line-height:1}` und `.ti::before{content:"";display:inline-block;width:1em;height:1em;vertical-align:-.125em;background-color:currentColor;-webkit-mask:var(--ti) center/contain no-repeat;mask:var(--ti) center/contain no-repeat;-webkit-print-color-adjust:exact;print-color-adjust:exact}`, `@media (forced-colors:active){.ti::before{background-color:CanvasText}}`; je Klasse eine Regel `.ti-x{--ti:url("data:image/svg+xml,…")}` (eingebettet, URL-kodiert) bzw. `.ti-x{--ti:url(/vendor/<version>/icons/x.svg)}`.
  - `func usedIconNames() []string` – `icons_used.txt` (eingebettet per `//go:embed`) plus die Vorgaben aller Symbol-Schlüssel (`iconGroupsStatic`, `navDefs`).
  - `func (h *Handler) chosenIconNames(ctx context.Context) ([]string, error)` – Klassen aus `app_settings` (`KeyIcons`), `categories.icon`, `grafana_dashboards.icon`, `plant_watch_boards.icon`, `it_asset_categories.icon`, nur gültige (`Has`).
  - `func (h *Handler) iconCSS(ctx context.Context) (css []byte, key string)` – zwischengespeichert, höchstens 60 s alt; `key` = erste 10 Hex-Zeichen von SHA-256 über die eingebetteten Namen.
  - Routen (öffentlich, wie heute die Symbole): `GET /vendor/{ver}/icons-{key}.css`, `GET /vendor/{ver}/icons/{name}.svg`; Template-Funktion `iconCSSHref` → `/vendor/<appVersion>/icons-<key>.css` (in `template_funcs.go`; der Handler stellt den aktuellen `key` bereit).
- Regeln: Stildatei mit fremdem `key` der laufenden Version → die aktuelle Stildatei, `no-store` (kein Kästchen nach einer Symbolwahl); fremde Version → `404`, `no-store`.

- [ ] **Step 1: Failing tests** (`iconcss_test.go`, ohne DB):
  - `TestIconCSSHasRuleForEveryName`: `buildIconCSS` enthält für jede der 5 918 Klassen genau eine Regel.
  - `TestIconCSSEmbedsUsed`: jede Klasse aus `usedIconNames()` steht als `data:image/svg+xml` in der Datei; `ti-ad-2` (nicht genutzt) als `/vendor/<ver>/icons/ti-ad-2.svg`.
  - `TestIconsUsedListComplete`: alle `ti-[a-z0-9-]+` in `web/templates/**/*.gohtml`, `internal/**/*.go` (außer Tests) und `web/static/*.js`, die `Has` kennt, stehen in `icons_used.txt`.
  - `TestIconCSSPrintAndContrast`: Datei enthält `print-color-adjust:exact` und `forced-colors`.
  - `TestIconRoutesCaching`: `/vendor/<ver>/icons/ti-plus.svg` → 200, `image/svg+xml`, `immutable`; `/vendor/<ver>/icons/ti-gibt-es-nicht.svg` → 404 `no-store`; `/vendor/0.0.1/icons/ti-plus.svg` → 404 `no-store`; Stildatei mit gzip und ETag.
- [ ] **Step 2: Failing test** `TestIconCSSChosen` (integration): Kategorie mit `ti-ad-2` anlegen → nach `iconCSS` (Zwischenspeicher geleert) steht `ti-ad-2` eingebettet, `key` ist ein anderer.
- [ ] **Step 3:** `go test ./internal/web -run 'TestIconCSS|TestIconsUsed|TestIconRoutes'` und `go test -tags integration ./internal/web -run TestIconCSSChosen` → FAIL.
- [ ] **Step 4:** Umsetzen; `icons_used.txt` aus dem Bestand erzeugen und einchecken.
- [ ] **Step 5:** Tests → PASS.
- [ ] **Step 6:** Commit `Seitenaufbau: Stildatei der Symbole aus SVG, eingebettet und einzeln`.

### Task 3: Seiten auf die neue Stildatei umstellen

**Files:**
- Modify: `web/templates/base.gohtml` (Zeile 46 `<link>`; `ensureIconFont` und die Regeln `html.icons-fallback …` entfernen), `web/templates/easy.gohtml`, `web/templates/global_dashboard.gohtml`, `web/templates/push_alert.gohtml`, `web/templates/terminal.gohtml`, `web/templates/terminal_device_error.gohtml`, `internal/web/icons.go` (`loadIconNames` aus `tablericons.Default().Names()`), `internal/web/icons_test.go`, `internal/web/favicon_test.go`, `cmd/server/main.go`
- Delete: `web/static/vendor/tabler-icons/tabler-icons.min.css` (nach dem Umstellen; `names.txt` bleibt die Namensliste)

**Interfaces:**
- Consumes: `iconCSSHref`, Routen aus Task 2.

- [ ] **Step 1: Failing tests**
  - `TestPagesUseIconCSS`: `base`, `easy`, `global_dashboard`, `push_alert`, `terminal`, `terminal_device_error` gerendert enthalten `href="/vendor/<ver>/icons-` und nicht `tabler-icons.min.css`.
  - `TestOldIconCSSRedirects`: `GET /vendor/tabler-icons/tabler-icons.min.css?v=0.82.55` → 302 auf die aktuelle Stildatei, `no-store`.
  - `TestIconsServedLocally` angepasst: erwartet die neue Adresse; die Prüfung „Schrift in der CSS-Datei“ entfällt, stattdessen „keine Schriftdatei für Symbole“ (`@font-face` mit `tabler` kommt nicht vor).
  - `TestNoIconFontFallback`: `base.gohtml` enthält weder `ensureIconFont` noch `icons-fallback`.
- [ ] **Step 2:** `go test ./internal/web -run 'TestPagesUseIconCSS|TestOldIconCSS|TestIcons|TestNoIconFont|TestIconCSSCompressed'` → FAIL.
- [ ] **Step 3:** Umstellen; alte CSS-Datei löschen.
- [ ] **Step 4:** `go test ./internal/web` → PASS.
- [ ] **Step 5:** Commit `Seitenaufbau: alle Seiten mit der neuen Symbol-Stildatei`.

### Task 4: Bibliotheken mitliefern

**Files:**
- Create: `web/static/vendor/htmx/htmx.min.js`, `web/static/vendor/sortablejs/Sortable.min.js`, `web/static/vendor/frappe-gantt/frappe-gantt.min.js`, `web/static/vendor/frappe-gantt/frappe-gantt.css`, `web/static/vendor/chartjs/chart.umd.min.js`, `web/static/vendor/xlsx/xlsx.full.min.js`, `web/static/vendor/mammoth/mammoth.browser.min.js`, `internal/web/vendor_test.go`
- Modify: `internal/web/favicon.go` (Auslieferung `/vendor/{ver}/{lib}/{file}`), `internal/web/template_funcs.go` (`vendorURL`), `cmd/server/main.go`, `web/templates/base.gohtml`, `dashboard.gohtml`, `global_dashboard.gohtml`, `leitstaende.gohtml`, `project_detail.gohtml`, `timetracking.gohtml`, `widgets/dashboard_widgets.gohtml`, `widgets/doc_view.gohtml`, `internal/web/timeline_style.go`, `THIRD_PARTY_LICENSES.md`

**Interfaces:**
- Produces: Template-Funktion `vendorURL "htmx/htmx.min.js"` → `/vendor/<appVersion>/htmx/htmx.min.js`; Route `GET /vendor/{ver}/{lib}/{file}` nur für Dateien unter `web/static/vendor/<lib>/` (kein `..`), Regeln wie in Global Constraints.

- [ ] **Step 1: Failing tests** `TestNoExternalAssets` (keine Vorlage, kein Go-Code außer Tests enthält `unpkg.com`, `cdn.jsdelivr.net`, `cdnjs.cloudflare.com`; die beiden Schrift-Hosts nimmt Task 5 dazu), `TestVendorRoute` (`/vendor/<ver>/htmx/htmx.min.js` → 200 `text/javascript` `immutable`; `/vendor/<ver>/htmx/../../x` → 404; fremde Version → 404 `no-store`), `TestVendorFilesPinned` (jede Datei, die ihre Version im Kopf nennt, nennt die aus Global Constraints).
- [ ] **Step 2:** `go test ./internal/web -run 'TestNoExternalAssets|TestVendor'` → FAIL.
- [ ] **Step 3:** Dateien herunterladen, Verweise umstellen, Lizenzen eintragen.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `Seitenaufbau: htmx, Sortable, Gantt, Chart.js, Excel- und Word-Vorschau mitgeliefert`.

### Task 5: Schriften mitliefern

**Files:**
- Create: `web/static/vendor/fonts/<id>.css` und die WOFF2-Dateien je Schrift (alle Teilmengen, die Google Fonts für die heute genutzten Schnitte ausliefert – mit `unicode-range`, damit nur Benötigtes lädt), `internal/web/fonts_test.go`
- Modify: `internal/web/appearance.go` (`uiFonts[].Href` → `/vendor/<appVersion>/fonts/<id>.css`), `THIRD_PARTY_LICENSES.md`, `internal/web/vendor_test.go` (`TestNoExternalAssets` mit allen fünf Hosts)

- [ ] **Step 1: Failing tests** `TestFontsLocal` (jede `uiFonts`-Schrift mit `Href` zeigt auf `/vendor/<ver>/fonts/`; jede `url(…)` in deren CSS-Datei gibt es unter `web/static/vendor/fonts/`; `font/woff2` bei Abruf), `TestNoExternalAssets` mit `fonts.googleapis.com` und `fonts.gstatic.com`.
- [ ] **Step 2:** `go test ./internal/web -run 'TestFontsLocal|TestNoExternalAssets'` → FAIL.
- [ ] **Step 3:** Schriften herunterladen (Google Fonts `css2` mit aktuellem Browser-Kennzeichen, Adressen in der CSS auf die lokalen Dateien umschreiben), `appearance.go` umstellen, Lizenzen (OFL bzw. Apache 2.0) eintragen.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `Seitenaufbau: Schriften der Darstellung mitgeliefert`.

### Task 6: Messung, Handbuch, Version, Gesamtprüfung

**Files:**
- Create: `deploy/lab/seitenaufbau/messung.js`, `deploy/lab/seitenaufbau/README.md`
- Modify: `web/templates/help.gohtml` (Kapitel zu Darstellung bzw. Symbolen und „Versionen & Updates“: ein Satz, dass das PDH nichts mehr aus dem Internet lädt und schneller aufbaut), `CHANGELOG.md` (`## [0.82.56]` mit `### Geändert`), `VERSION`, `AGENTS.md` (offener Punkt 13 für die Oberfläche erledigt, Stand)

- [ ] **Step 1:** `messung.js` nach dem Entwurf (je Seite neuer Browser-Kontext, angemeldet; `DOMContentLoaded`, `load`, „alle Symbole sichtbar“; CPU 1× und 4×; dazu gefüllter Zwischenspeicher; Seiten `/`, `/energy/meters`, `/tickets`, `/maintenance`, `/infrastructure`); README mit Aufruf und Prüfkonto-Hinweis aus AGENTS.md.
- [ ] **Step 2:** Lokal: Seite mit vielen Symbolen gerendert vorher/nachher in Fiori/Fluent, hell/dunkel, 320–1366 px vergleichen (Bildschirmfotos), Druckvorschau einer Seite mit Symbolen.
- [ ] **Step 3:** `go test ./internal/web -run 'TestHelp|TestChangelogMatchesVersion'` → PASS; `go vet ./...`; `go test ./...`; `go test -tags integration ./internal/web` (bekannt rot: `TestTerminalGlobalSlidesJSON`).
- [ ] **Step 4:** Schlussprüfung des ganzen Branches durch einen frischen Prüfer mit dem Review Focus oben; Befunde beheben oder zurückstellen.
- [ ] **Step 5:** Commit `Seitenaufbau: Messung, Handbuch, Änderungsprotokoll (0.82.56)`; Freigabe für Push, Merge und Verbund-Update beim Anwender einholen. Nach dem Einspielen: Messung am Testverbund (Ziel 0,5 s / 1,5 s), Netzprotokoll ohne fremde Adressen, Browserprüfung; auf 10.10.0.202 prüft der Anwender über die Cloudflare-Adresse.
