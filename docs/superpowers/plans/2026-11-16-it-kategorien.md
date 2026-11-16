# IT-Kategorien (0.80.19) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** IT-Asset-Arten werden pflegbare Kategorien mit eigener Karenz und Überwachungs-Vorgabe; Karenz zusätzlich je Asset.

**Architecture:** Neue Tabelle `it_asset_categories` (Schlüssel = bisherige Enum-Werte); `it_assets.type` wird Text mit Fremdschlüssel. Repository-Funktionen in `internal/modules/it/categories.go`; netsync liest Überwachungs-Vorgabe und Karenz aus der Kategorie; Web-Seite `/it/categories`, alle festen Typ-Listen im Web werden durch die Tabelle ersetzt.

**Tech Stack:** Go 1.27, pgx v5, chi, html/template + htmx, PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-11-16-it-kategorien-design.md`

## Global Constraints

- Version **0.80.19** (`VERSION` + oberster CHANGELOG-Eintrag `## [0.80.19] – 2026-11-16`), Migration **165** (`migrations/165_it_categories.up.sql`).
- Deutsch in Texten/Kommentaren/Commits, ohne Attributionszeilen.
- Bestehende Schlüssel bleiben: `server, network, workstation, printer, phone, tablet, other`; `network` und `other` sind `builtin` (nicht löschbar, nicht ausblendbar).
- Vorgaben `monitor_default`: server ja, network ja, workstation nein, printer ja, phone nein, tablet nein, other ja. `grace_minutes` aller Vorbelegungen NULL.
- Karenz-Bereich 1..1440 Minuten (Kategorie und Asset); wirksam `COALESCE(asset, kategorie, allgemein)`.
- Neues Recht `it.categories` „IT-Kategorien pflegen“ (Kategorie `IT`), an Rollen mit `it.admin` (Abteilung übernehmen, „überall“ gewinnt – Muster `163_energy_thresholds.up.sql`).
- Farben nur über CSS-Variablen; beide Stile, hell/dunkel, 320–1366 px.
- Tests in Git Bash; DB-Tests mit `PDH_TEST_DSN` (Muster `testPool` in `internal/modules/it/netsync/migration_integration_test.go`). Tests berühren nur eigene Datensätze der geteilten Labor-DB.

## Review Focus

1. **Update auf einer Datenbank mit vorhandenen Assets:** jede Zeile behält ihren Typ, Index `idx_it_type` bleibt nutzbar, Migration zweimal ausführbar → Task 1 Test.
2. **Kategorie ausgeblendet, Assets nutzen sie noch:** Assets zeigen Name/Symbol weiter, Formular bietet sie beim Bearbeiten dieses Assets weiter an, neue Assets nicht → Task 5 Test.
3. **Löschen einer benutzten Kategorie** (auch Rennen: Asset wird gleichzeitig angelegt): Fremdschlüssel verhindert es, Meldung statt 500 → Task 2 Test.
4. **Karenz am Asset geleert:** fällt auf Kategorie bzw. allgemein zurück, laufender Alarm unverändert → Task 3 Test.
5. **Name mit Umlauten/Sonderzeichen oder doppelt:** Schlüssel eindeutig und gültig („Überwachungs-Kamera“ → `ueberwachungs-kamera`, zweite → `-2`) → Task 2 Test.

---

### Task 1: Migration 165

**Files:** Create `migrations/165_it_categories.up.sql`; Test `internal/modules/it/netsync/categories_migration_integration_test.go`.

**Produces:** Tabelle und Spalten laut Spec „Datenmodell“; `it_assets.type` Text + FK `it_assets_type_fkey` (`ON UPDATE CASCADE`), Default `'other'`; `DROP TYPE IF EXISTS it_asset_type`; `it_assets.net_grace_minutes`; Recht `it.categories`.

- [ ] Test `TestITCategoriesMigration`: 7 Vorbelegungen mit genannten Werten; `type` ist `text`; Insert mit unbekanntem Typ scheitert; Migration 165 ein zweites Mal ausführbar (in Transaktion, Rollback); `it.categories` existiert, jede Rolle mit `it.admin` hat es.
- [ ] Rot → Migration schreiben (Typumstellung in `DO`-Block nur wenn `data_type <> 'text'`; erst Default entfernen, dann `ALTER … TYPE TEXT USING type::text`, Default setzen, FK hinzufügen) → grün.
- [ ] Commit „Migration 165: IT-Kategorien, Karenz je Asset, Recht it.categories“.

### Task 2: Kategorien im IT-Modul

**Files:** Create `internal/modules/it/categories.go`, Test `internal/modules/it/categories_integration_test.go` (eigene `testPool`-Kopie, Migrationspfad `../../../migrations`).

**Produces:**
```go
type Category struct { Key, Name, Icon string; SortOrder int; Active, MonitorDefault, Builtin bool; GraceMinutes *int; Assets int }
var ErrCategoryInUse, ErrCategoryBuiltin, ErrCategoryInvalid error
func CategoryKey(name string, taken func(string) bool) string
func (r *Repository) Categories(ctx context.Context, withInactive bool) ([]Category, error) // sortiert sort_order, name; Assets = Anzahl it_assets
func (r *Repository) Category(ctx context.Context, key string) (*Category, error)
func (r *Repository) CreateCategory(ctx context.Context, c *Category) error        // erzeugt Key
func (r *Repository) UpdateCategory(ctx context.Context, c *Category) error        // Name, Icon, SortOrder, MonitorDefault, GraceMinutes
func (r *Repository) SetCategoryActive(ctx context.Context, key string, on bool) error // builtin → ErrCategoryBuiltin
func (r *Repository) DeleteCategory(ctx context.Context, key string) error         // builtin → ErrCategoryBuiltin; benutzt / FK-Verletzung 23503 → ErrCategoryInUse
func (r *Repository) SetAssetGrace(ctx context.Context, assetID string, minutes *int) error
```
Prüfung: Name 1..60 Zeichen getrimmt, Icon leer oder `^ti-[a-z0-9-]{1,60}$`, Grace nil oder 1..1440 → sonst `ErrCategoryInvalid`. `Create`/`Update` der Assets (it.go) prüfen den Typ nicht selbst – der FK tut es; Fehler 23503 wird zu `ErrCategoryInvalid`.

- [ ] Tests: `TestCategoryKey` (Tabelle: Umlaute, Leerzeichen, Sonderzeichen, Kollision `-2`, Länge 40, leerer Name → `kategorie`), DB: Anlegen/Umbenennen (Key bleibt), Ausblenden builtin abgewiesen, Löschen benutzt → `ErrCategoryInUse`, Löschen unbenutzt ok, `SetAssetGrace` setzen/leeren, ungültige Werte.
- [ ] Rot → umsetzen → grün → Commit „IT-Kategorien: Speicher und Prüfregeln“.

### Task 3: Abgleich und Wächter nutzen die Kategorie

**Files:** Modify `internal/modules/it/netsync/sync.go`, `alarms.go`, Tests dort; Modify `internal/web/it_net_web.go` (Reattach), `internal/web/it_netsync.go` (Tickettext).

**Produces:** `MonitoredByTypeSQL` entfällt; neu `func MonitoredForCategorySQL(keyExpr string) string` → `COALESCE((SELECT monitor_default FROM it_asset_categories WHERE key = <keyExpr>), true)`, genutzt von Adopt (beide Zweige) und Reattach. `Adopt` weist ab: unbekannte oder ausgeblendete Kategorie und `network` (vorhandenen netsync-Fehler für ungültigen Typ nutzen bzw. als `ErrBadType` exportieren). `AlarmAsset` bekommt `GraceMinutes int`; `Watch.Pass` ermittelt je Asset `COALESCE(a.net_grace_minutes, c.grace_minutes, <allgemein in Minuten>)`; `Grace func` bleibt der allgemeine Wert. Tickettext (`itNetAlarmText`) nennt „Karenz N min“.

- [ ] Tests: Adopt mit eigener Kategorie `monitor_default=false` → unüberwacht; Adopt `network`/ausgeblendet → Fehler; Wächter: Kategorie-Karenz 10 min → bei 6 min kein Alarm, bei 10 min Alarm; Asset-Karenz 2 min schlägt Kategorie; Asset-Karenz geleert → Kategorie; bestehende Tests angepasst (keine Regression bei Vorgaben).
- [ ] Rot → umsetzen → grün (`go test ./internal/modules/it/... ./internal/web/ -run 'ITNet|Unifi' -count=1`) → Commit „Netz-Abgleich: Überwachung und Karenz aus Kategorie und Asset“.

### Task 4: Seite „IT-Kategorien“

**Files:** Create `web/templates/it_categories.gohtml`, `internal/web/it_categories_web.go`, Test `internal/web/it_categories_web_test.go`; Modify `internal/web/handler.go` (Routen), `internal/web/perm_gate.go`, i18n-Kataloge (Seitentitel, falls `TestI18nCatalogsComplete` es verlangt).

**Routen:** `GET /it/categories` (`it.view`; Formulare nur mit `it.categories`), `POST /it/categories` (anlegen), `POST /it/categories/{key}` (ändern), `POST /it/categories/{key}/active` (`on=1|0`), `POST /it/categories/{key}/delete` – alle POST mit `it.categories` in `gateRules`. PRG mit 303 und festen Meldungsschlüsseln (Muster `it_net_web.go`). `BaseData.Page = "it-categories"`. Symbolauswahl: Eingabefeld mit Vorschau wie `web/templates/icons.gohtml` (Muster übernehmen, keine zweite Symbolliste bauen). Knopf „Kategorien“ auf `/it` neben „Netz-Abgleich“.

- [ ] Tests: Rechte (ohne `it.view` 403; ohne `it.categories` keine Formulare, POST 403), Anlegen/Ändern/Ausblenden/Löschen über HTTP inkl. Fehlermeldung bei benutzter Kategorie, Gate-Zuordnung (`matchGate`). `TestHelpCoversAllPages` darf bis Task 6 rot sein.
- [ ] Rot → umsetzen → grün → Commit „Seite IT-Kategorien“.

### Task 5: Kategorien überall statt fester Typliste, Karenz am Asset

**Files:** Modify `internal/web/handler.go` (`ITPage`, `ITCreate`, `itAssetDetailView`, Formulare), `web/templates/it.gohtml`, `it_detail.gohtml`, `it_net.gohtml` (Typauswahl beim Übernehmen), `internal/web/icons.go` (`itTypeIcons` → Symbol aus Kategorie mit Rückfall), `internal/web/it_net_web.go` (Karte „Netz“ + Route `POST /it/{id}/net-grace`, `it.edit` per `hasPermRec`, in `gateRules`).

**Produces:** `func (h *Handler) itCategories(ctx context.Context) (list []it.Category, byKey map[string]it.Category)` – einmal je Anfrage; `func itCategoryIcon(c it.Category) string` (Icon, sonst `iconClass("it."+key)` bei den 7 bisherigen, sonst `ti-box`). Alle drei `typeLabels`-Maps entfallen. Filterknöpfe = aktive Kategorien mit ≥1 Asset. Karte „Netz“: „Karenz bis zum Alarm: N min (eigener Wert|Kategorie|allgemein)“ + Feld (leer = zurücksetzen).

- [ ] Tests: Liste/Detail zeigen Name+Symbol einer eigenen Kategorie; ausgeblendete Kategorie bleibt am Asset sichtbar und im Bearbeiten-Formular dieses Assets wählbar, im Neu-Formular nicht; Übernehmen-Auswahl ohne `network`; Karenz-Route setzt/leert, 403 ohne Datensatzrecht; Herkunftstext korrekt.
- [ ] Rot → umsetzen → grün (`go test ./internal/web/ ./internal/modules/it/... -count=1`) → Commit „IT-Kategorien in Liste, Detail und Formularen; Karenz am Asset“.

### Task 6: Handbuch, Änderungsprotokoll, Version

**Files:** `web/templates/help.gohtml` (Kapitel `infrastructure`: `data-pages` + `it-categories`, Abschnitt „IT-Kategorien“ mit `.hb-howto`/`.hb-steps`: Kategorie anlegen, Karenz setzen, Karenz am Gerät; Netz-Abgleich-Text: Karenz-Reihenfolge, Überwachung aus Kategorie; Administration: Recht `it.categories`), `web/templates/widgets/help_figures.gohtml` (kleine Skizze), `VERSION`, `CHANGELOG.md` (`### Neu`, `### Geändert`, `### Sicherheit`). Texte an die echten Beschriftungen der Vorlagen angleichen.

- [ ] `go test ./... -count=1` grün → Commit „Handbuch und Änderungsprotokoll 0.80.19: IT-Kategorien“.
