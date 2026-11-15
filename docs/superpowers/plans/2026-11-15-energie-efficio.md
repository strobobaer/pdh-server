# Energiedaten-Export für Berg Efficio (0.80.12) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Messstellen (echt und virtuell mit Formel) werden in einer Zuweisungstabelle beliebigen Import-Werten und Anlagen zugeordnet, minütlich als Messwerte abgelegt und alle 15 Minuten als Efficio-CSV zum Abholen, auf ein Netzlaufwerk oder per FTP/SFTP bereitgestellt.

**Architecture:** Neues Paket `internal/modules/energie` (Formel-Parser, Speicher, Minuten-Takt, Intervallwerte, CSV, Exporter, Ablageziele) mit eigenen Tabellen. Der Minuten-Takt liest die zuletzt importierten Werte (`import_mappings.last_value`, eine Historie gibt es dort nicht) und legt je Messstelle einen Minutenwert in `energy_values` ab; virtuelle Messstellen werden im selben Takt in Formel-Reihenfolge berechnet und gleich abgelegt. Der Efficio-Export ist eine neue Export-Art `efficio` der vorhandenen Export-Verbindungen (`import_export.go`); die Dateien liegen in der Datenbank (`energy_export_files`), damit Abholen, atomares Ersetzen und Nachlegen ohne Dateisystem-Zustand funktionieren und der Spiegel sie mitbekommt.

**Tech Stack:** Go 1.27, chi, html/template, htmx, Postgres (pgx v5), `github.com/jlaffaye/ftp`, `github.com/pkg/sftp` (+ `golang.org/x/crypto/ssh`), `golang.org/x/text/encoding/charmap`.

**Spec:** `docs/superpowers/specs/2026-11-15-energie-efficio-design.md`

## Global Constraints

- Umfang nur **0.80.12**: Schwellwerte, Alarme, Tickets und Kommentarpflicht kommen in 0.80.13. Die Übersicht hat **keine** Spalte „Schwellwerte“.
- `VERSION` **0.80.12**, `CHANGELOG.md` neuer oberster Eintrag `## [0.80.12] – 2026-11-15`. Migration **161** (`migrations/161_energy.up.sql`).
- Paket `internal/modules/energie`; Tabellen `energy_*`; Datensatztyp `meter`; Seite `/energy/meters`, `BaseData.Page` `"energy"`.
- Art einer Messstelle: `counter` „Zählerstand“, `consumption` „Verbrauch“, `instant` „Momentanwert“. Faktor Vorgabe **1**, nicht 0.
- Messstellen-ID: getrimmt 1–64 Zeichen, ohne `[`, `]`, `;`, `"`, Zeilenumbruch; eindeutig über alle Messstellen.
- Formel: Zahlen (Punkt oder Komma), `+ - * /` und `− × ÷`, Klammern, einstelliges Minus, Bezüge als `[Messstellen-ID]`, höchstens 2000 Zeichen. Eigener Parser, keine fremde Bibliothek.
- Quelle frisch, wenn `last_received_at` höchstens **15 min** alt (`StaleAfter`); sonst kein Wert. Nicht lesbarer Wert = kein Wert, nie 0.
- Minutenwerte werden **400 Tage** aufbewahrt (`ValueRetentionDays`).
- Efficio-CSV Vorgaben: Spalten `Zeitstempel;Messstellen-ID;Messwert`, Takt **15 min** (erlaubt 1, 5, 10, 15, 30, 60), Wert zum Intervallende (`end`, wahlweise `delta`), Zeitformat `TT.MM.JJJJ hh:mm` (Go `02.01.2006 15:04`, wahlweise `JJJJ-MM-TT hh:mm`), Dezimalkomma ja, Kopfzeile ja, Zeilenende CRLF, UTF-8 (wahlweise Windows-1252), Dateiname `<Präfix>_JJJJMMTT_hhmm.csv` (Präfix Vorgabe `efficio`), Dateilänge **1 Tag** (0 = eine Datei je Lauf), Aufbewahrung **30 Tage**, Werte auf 4 Nachkommastellen gerundet, Nullen am Ende weg.
- Zeitstempel = Intervallende in Ortszeit des Servers; eine Tagesdatei umfasst Intervallenden in `[00:00, 00:00 des Folgetags)`.
- Ein Intervall wird frühestens **2 min** nach seinem Ende geschrieben; nachgeholt werden höchstens **7 Tage**; der erste Lauf beginnt mit dem letzten abgeschlossenen Intervall.
- Rechte: `energy.view` „Messstellen sehen“, `energy.edit` „Messstellen pflegen“, `energy.admin` „Messstellen löschen (Bereichsadministrator)“, Kategorie „Daten & Schnittstellen“. Efficio-Export mit den vorhandenen `export.read` (sehen, herunterladen) und `export.write` (einrichten, Zugangsdaten, testen, jetzt exportieren).
- Minuten-Takt und Exporter laufen nur auf dem aktiven Server (Block `if !mirrorOn` in `cmd/server/main.go`, `Skip: h.MaintLocked`), Muster `internal/modules/ruesten/monitor.go`.
- Texte, Handbuch, Commits auf Deutsch in Anwendersprache; Fiori/Fluent und hell/dunkel über Variablen; keine Attributionszeilen; nie `git add -A`.
- Tests in Git Bash: `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable' go test ./...`; Datenbanktests ohne Variable `t.Skip`.

## Review Focus

1. **Zählerstand springt zurück** (Zählertausch, Überlauf): kein negativer Verbrauch, kein negativer Zuwachs bei virtuellen Zählern – das Intervall bzw. die Minute hat keinen Wert. → Task 3 `TestSamplerCounterResetNoNegative`, Task 5 `TestIntervalDeltaNegativeMissing`.
2. **Quellwert nicht als Zahl lesbar** (`"n/a"`, `""`, `"1.234,5"`, `"12,5 kWh"`): kein Wert statt 0; `"12,5"` und `"12.5"` gelten. → Task 3 `TestParseValue`.
3. **Exporter lange aus oder zwei Läufe gleichzeitig** (Neustart nach 3 h, „Jetzt exportieren“ während des Takts): alle verpassten Intervalle kommen genau einmal in die Datei. → Task 5 `TestExporterCatchUpNoDuplicates`.
4. **Zeitumstellung** (25.10.2026): Tagesdatei hat 100 Intervalle, die doppelte Stunde steht zweimal in zeitlicher Folge, die Datei endet an Ortsmitternacht. → Task 5 `TestFilePeriodDST`.
5. **Messstellen-ID geändert, Art geändert oder Messstelle gelöscht, während eine Formel sie nutzt**: abgewiesen mit „wird verwendet in [ID]“. → Task 3 `TestSaveRejectsChangeInUse`, `TestDeleteInUse`.

---

## Dateien

| Datei | Aufgabe |
|---|---|
| `migrations/161_energy.up.sql` | Tabellen, Rechte |
| `internal/modules/energie/formula.go` | Formel-Parser, Prüfung, Reihenfolge |
| `internal/modules/energie/model.go`, `store.go` | Messstellen, Filter, Speichern mit Regeln |
| `internal/modules/energie/sampler.go` | Minuten-Takt, virtuelle Berechnung, Aufräumen |
| `internal/modules/energie/interval.go`, `efficio.go` | Intervallwerte, CSV-Zeilen, Dateiname, Dateizeitraum |
| `internal/modules/energie/exporter.go`, `target.go` | Läufe, Dateien, Protokoll, Ablageziele |
| `internal/web/energy_meters.go`, `energy_wire.go` | Seite Messstellen, Rechte, Start der Dienste |
| `internal/web/export_efficio.go` | Export-Art, Einstellungen, Seite Dateien & Protokoll |
| `web/templates/energy_meters.gohtml`, `widgets/energy_meter_table.gohtml`, `export_efficio.gohtml` | Oberfläche |

---

### Task 1: Migration 161

**Files:** Create `migrations/161_energy.up.sql`, `internal/modules/energie/migration_integration_test.go`

**Interfaces – Produces (Schema):**
- `energy_meters(id uuid pk default pdh_uuid_v7(), meter_key varchar(64) not null unique, name varchar(150) not null, infrastructure_id uuid not null references infrastructure, import_mapping_id uuid references import_mappings on delete set null, formula varchar(2000) not null default '', unit varchar(20) not null default '', kind text not null default 'counter' check (kind in ('counter','consumption','instant')), factor double precision not null default 1 check (factor <> 0), export bool not null default true, active bool not null default true, last_value double precision, last_at timestamptz, created_by uuid references users, updated_by uuid references users, created_at, updated_at, check (formula = '' or import_mapping_id is null))`; Index `(infrastructure_id)`.
- `energy_values(meter_id uuid not null references energy_meters on delete cascade, ts timestamptz not null, value double precision not null, primary key (meter_id, ts))`.
- `energy_export_state(connection_id uuid pk references import_export_connections on delete cascade, last_interval_end timestamptz not null)`.
- `energy_export_files(id uuid pk default pdh_uuid_v7(), connection_id uuid not null references import_export_connections on delete cascade, name varchar(200) not null, period_start timestamptz not null, period_end timestamptz not null, content bytea not null, rows int not null default 0, version int not null default 1, delivered_version int not null default 0, delivered_at timestamptz, last_error text not null default '', created_at, updated_at, unique (connection_id, name))`.
- `energy_export_runs(id bigserial pk, connection_id uuid not null references import_export_connections on delete cascade, started_at timestamptz not null, finished_at timestamptz, intervals int not null default 0, rows int not null default 0, missing int not null default 0, target text not null default '', files text not null default '', result text not null check (result in ('ok','partial','error','nothing')), message text not null default '')`; Index `(connection_id, started_at desc)`.
- Rechte wie in den Global Constraints; `energy.view` an Rollen mit `export.read`, `energy.view` + `energy.edit` an Rollen mit `export.write` (Abteilung der Quellrolle übernehmen, `ON CONFLICT … DO UPDATE SET department_id = NULL WHERE EXCLUDED.department_id IS NULL` wie Migration 159); `energy.admin` an Rolle `admin` und Rollen mit globalem `export.write`, die schon ein anderes `*.admin` haben (wie `setup.admin` in 159).

- [ ] **Step 1: Test** `TestEnergyMigration` (`PDH_TEST_DSN`, `database.RunMigrations`): zweite Messstelle mit gleicher `meter_key` scheitert; `factor = 0` scheitert; `kind = 'x'` scheitert; Formel **und** Quelle zugleich scheitert; Löschen der Import-Zuordnung setzt `import_mapping_id` auf NULL; Rolle mit `export.write` hat `energy.view` und `energy.edit`; Rolle `admin` hat `energy.admin`; alle drei Rechte haben Kategorie „Daten & Schnittstellen“.
- [ ] **Step 2:** `go test ./internal/modules/energie/ -run TestEnergyMigration -v` → FAIL.
- [ ] **Step 3:** Migration schreiben.
- [ ] **Step 4:** → PASS; `go test ./pkg/database/` → PASS.
- [ ] **Step 5:** Commit `Energiedaten: Tabellen für Messstellen, Messwerte und Efficio-Export, Rechte (Migration 161)`.

### Task 2: Formel-Parser

**Files:** Create `internal/modules/energie/formula.go`, `formula_test.go`

**Interfaces – Produces:**
```go
type FormulaError struct{ Pos int; Msg string }        // Error(): "Formel, Zeichen 7: Klammer fehlt"
type UnknownRefError struct{ Key string }              // "unbekannte Messstelle [X]"
type CycleError struct{ Path []string }                // "Zirkelbezug: [A] → [B] → [A]"
type Formula struct{ /* Baum */ }
func ParseFormula(src string) (*Formula, error)
func (f *Formula) Refs() []string                                    // sortiert, ohne Doppelte
func (f *Formula) Eval(get func(key string) (float64, bool)) (float64, bool) // false: Bezug fehlt, Division durch 0, NaN/Inf
func CheckFormulas(formulas map[string]string, known map[string]bool) error  // Schlüssel = Messstellen-ID der virtuellen
func EvalOrder(formulas map[string]string) ([]string, error)                 // Bezüge vor Nutzern; CycleError
```
Rekursiver Abstieg: `expr = term {(+|-) term}`, `term = unary {(*|/) unary}`, `unary = [-] primary`, `primary = Zahl | [ID] | ( expr )`. Selbstbezug ist ein Zirkelbezug.

- [ ] **Step 1: Tests:** `TestParseFormulaEval` (`"[G] − [A] − [B]"` mit G=100, A=30, B=20 → 50; `"([A]+[B]) × 0,5"` → 25; `"-[A] + 2*3"` → -24; `"[A] ÷ 4"` → 7.5); `TestParseFormulaErrors` (`""`, `"[A] +"`, `"([A]"`, `"[A]]"`, `"[]"`, `"[A] [B]"`, `"5 % 2"`, 2001 Zeichen → `*FormulaError` mit Pos); `TestFormulaRefs` (`"[B]+[A]+[B]"` → `[A B]`); `TestEvalMissingRef` (B fehlt → false); `TestEvalDivZero` (→ false); `TestCheckFormulasUnknown` (→ `*UnknownRefError{Key:"X"}`); `TestCheckFormulasCycle` (A=[B], B=[C], C=[A] → `*CycleError`, Path beginnt und endet mit derselben ID; A=[A] → Zirkelbezug); `TestEvalOrderChain` (C=[B]+1, B=[A]*2 → Reihenfolge B vor C).
- [ ] **Step 2:** `go test ./internal/modules/energie/ -run Formula -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Energiedaten: Formeln für berechnete Messstellen mit Prüfung auf Zirkelbezüge`.

### Task 3: Messstellen speichern und Minuten-Takt

**Files:** Create `internal/modules/energie/model.go`, `store.go`, `sampler.go`, `store_integration_test.go`, `sampler_integration_test.go`, `value_test.go`

**Interfaces:**
- Consumes: `ParseFormula`, `CheckFormulas`, `EvalOrder` (Task 2).
- Produces:
```go
const (KindCounter = "counter"; KindConsumption = "consumption"; KindInstant = "instant")
const (StaleAfter = 15 * time.Minute; ValueRetentionDays = 400)
var ErrNotFound, ErrBadKey, ErrKeyTaken, ErrBadKind, ErrKindMix, ErrNoInfra error
type InUseError struct{ Users []string }        // "wird verwendet in [C]"
type Meter struct {
    ID, Key, Name, InfraID, InfraPath, SourceID, SourceLabel, ConnectionID, ConnectionName, Formula, Unit, Kind string
    Factor float64; Export, Active bool; LastValue *float64; LastAt *time.Time
}
func (m Meter) Virtual() bool                    // Formula != ""
type Filter struct {
    InfraID string /* mit Unteranlagen */; Virtual, Export, Active *bool
    Unit, Kind, ConnectionID, Query string; Stale bool /* LastAt leer oder älter StaleAfter */
    Sort string /* key|name|infra|unit|kind|last */; Desc bool; AllowedIDs map[string]bool /* nil = alle */
}
type Store struct{ DB *pgxpool.Pool; Now func() time.Time }
func NewStore(db *pgxpool.Pool) *Store
func ParseValue(raw string) (float64, bool)
func (s *Store) List(ctx context.Context, f Filter) ([]Meter, error)
func (s *Store) Get(ctx context.Context, id string) (*Meter, error)
func (s *Store) Save(ctx context.Context, m *Meter, userID string) (created bool, err error)
func (s *Store) Delete(ctx context.Context, id string) error
func (s *Store) Units(ctx context.Context) ([]string, error)
type Sampler struct{ Store *Store; Skip func() bool }
func (sm *Sampler) Pass(ctx context.Context, minute time.Time) (stored, missing int, err error)
func (sm *Sampler) Start(ctx context.Context) // Takt 60 s, Panik abgefangen, einmal täglich Werte älter 400 Tage löschen
```
- `Save`: ohne `ID` und `Key` schon an derselben Anlage → diese aktualisieren (`created=false`); an anderer Anlage → `ErrKeyTaken`; sonst anlegen (`created=true`). Mit Formel `Quelle` leeren; alle Formeln (gespeicherte + neue) mit `CheckFormulas`/`EvalOrder` prüfen; Bezüge `instant` nur mit `instant`, `counter`/`consumption` untereinander (`ErrKindMix`). ID- oder Art-Änderung sowie `Delete` einer Messstelle, die in einer Formel steht → `*InUseError`. In einer Transaktion mit `LOCK TABLE energy_meters IN SHARE ROW EXCLUSIVE MODE`, damit zwei gleichzeitige Formeln keinen Zirkel bilden.
- `ParseValue`: getrimmt; genau eine Zahl, Dezimalpunkt oder -komma, kein Tausenderpunkt neben Komma, keine Einheit.
- `Sampler.Pass` je `minute` (auf Minute abgeschnitten), nur aktive Messstellen, `INSERT … ON CONFLICT DO NOTHING`:
  - echt `counter`/`instant`: Quelle frisch und lesbar → `Wert × Faktor`.
  - echt `consumption`: Quelle frisch → bei neuer Lesung seit dem letzten Minutenwert `Wert × Faktor`, sonst 0.
  - virtuell in `EvalOrder`: Bezüge `counter` tragen ihren Zuwachs seit dem vorigen Minutenwert (höchstens 15 min zurück) bei, `consumption`/`instant` ihren Minutenwert. Ergebnis: `instant`/`consumption` = Formelwert; `counter` = eigener letzter Stand (Start 0) + Formelwert. Fehlt ein Bezug oder ist ein Zuwachs negativ → kein Wert (`missing++`).
  - danach `last_value`/`last_at` an der Messstelle. `Skip()` wahr → nichts.

- [ ] **Step 1: Tests:** `TestParseValue` (`"12,5"`→12.5, `"12.5"`→12.5, `"-3"`→-3; `""`, `"n/a"`, `"1.234,5"`, `"12,5 kWh"` → false); `TestSaveCreatesForInfra` (neue Zeile an Anlage → `created=true`, zweites Speichern derselben ID an derselben Anlage ohne `ID` → `created=false`, gleiche ID an anderer Anlage → `ErrKeyTaken`); `TestSaveRejectsBadKey` (`"a;b"`, `"[x]"`, 65 Zeichen); `TestSaveVirtualRejectsCycleAndUnknown`; `TestSaveRejectsKindMix`; `TestSaveRejectsChangeInUse` (ID und Art); `TestDeleteInUse`; `TestListFilters` (Anlage mit Unteranlagen, virtuell, exportieren, inaktiv, Einheit, Art, Verbindung, Freitext, `Stale`, Sortierung `last` absteigend, `AllowedIDs`); `TestSamplerRealValues` (Faktor 0,001; veraltete Quelle 16 min → kein Wert); `TestSamplerConsumptionCountsOnce` (eine Lesung, drei Minuten → Werte x, 0, 0); `TestSamplerVirtualCounter` (G 1000→1010, A 500→503, Formel `[G]-[A]` → virtueller Stand 0 dann 7); `TestSamplerVirtualMissingRef` (A veraltet → keine Zeile für V, `missing=1`, keine 0); `TestSamplerCounterResetNoNegative` (G 1000→5 → keine Zeile für V); `TestSamplerTwoPassesSameMinute` (keine Doppelten, virtueller Stand nicht doppelt erhöht); `TestSamplerSkip`.
- [ ] **Step 2:** `go test ./internal/modules/energie/ -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Energiedaten: Messstellen speichern, Minutenwerte ablegen, berechnete Messstellen im Importtakt`.

### Task 4: Seite Messstellen (Zuweisungstabelle und Übersicht)

**Files:** Create `internal/web/energy_meters.go`, `energy_wire.go`, `energy_meters_test.go`, `web/templates/energy_meters.gohtml`, `widgets/energy_meter_table.gohtml`. Modify `handler.go` (Routen, `BaseData.CanEnergy`), `nav.go` (Gruppe `data`, „Messstellen“, `ti-gauge`), `perm_gate.go` (`recordDeptSource["meter"] = {"energy_meters", false}`, `areaAdminPerm["meter"] = "energy.admin"`, `gateRules`), `dept_scope.go` (`scopeTables` `r.created_by = $1::uuid`), `export_mappings.go` (`availableExportSources` füllt zusätzlich `ConnectionID`, `ConnectionName`; Felder in `ImportMappingView`), `cmd/server/main.go` (`webHandler.StartEnergySampler` im Block `if !mirrorOn`), `web/i18n/*.json`, `web/templates/help.gohtml` (Kapitel `energiedaten`, `data-pages="energy"`, Abschnitte Messstellen, Zuweisungstabelle, berechnete Messstellen), `widgets/help_figures.gohtml` (Skizze Import-Wert → Messstelle → Formel), `internal/web/help.go` (`helpChapterIDs` nach `connections`).

**Interfaces:**
- Consumes: `energie.Store`, `Filter`, `Meter`, `Sampler`, Fehler aus Task 3.
- Produces: `const meterRef = "meter"`; `permEnergyView, permEnergyEdit, permEnergyAdmin`; `func (h *Handler) energyStore() *energie.Store` (nil ohne DB); `func (h *Handler) StartEnergySampler(ctx context.Context)` (`Skip: h.MaintLocked`); `func meterFilterFromQuery(q url.Values) energie.Filter`; `func meterSaveMessage(err error) string`.

| Route | Recht |
|---|---|
| `GET /energy/meters` | `energy.view` |
| `POST /energy/meters/save-web` (JSON; neu) | `energy.edit` an der gewählten Anlage (`hasPermRec(…, "infrastructure", infraID)`) |
| `POST /energy/meters/save-web` (JSON; mit `id`) | `energy.edit` (Datensatz `meter`) |
| `POST /energy/meters/{id}/delete-web` | `energy.admin` (Datensatz) |

Seite: Filterleiste (Anlage per `infra-tree-picker`, echt/berechnet, exportieren, aktiv, Einheit, Art, Verbindung, Freitext, „ohne aktuellen Wert“), Filter und Sortierung in der Adresse (`infra, virtual, export, active, unit, kind, conn, q, stale, sort, dir`). Tabelle: ID, Bezeichnung, Anlage, Quelle (Verbindung · Datenpunkt; bei berechneten die Formel und Kennzeichen „berechnet“), Einheit, Art, Faktor, letzter Wert mit Zeitpunkt, exportieren, aktiv. Bearbeiten in der Zeile mit Recht: Anlage per Infra-Picker wie `widgets/import_mapping.gohtml`, Quelle als Auswahl aller Import-Werte oder Umschalter „Formel“; Zeile „Neue Messstelle“ am Tabellenende. Fehlermeldungen in Anwendersprache aus `meterSaveMessage`.

- [ ] **Step 1: Tests:** `TestEnergyPagesRequirePermission` (ohne `energy.view` Weiterleitung; ohne `energy.edit` keine Bearbeiten-Knöpfe); `TestEnergyGateRules` (Löschen nur `energy.admin`); `TestEnergyInRegistries` (`recordDeptSource`, `areaAdminPerm["meter"] == "energy.admin"`); `TestMeterFilterFromQuery` (alle Parameter, unbekannte Sortierung → `key`); `TestMeterSaveWeb` (`PDH_TEST_DSN`: neue Zeile mit Anlage legt an; Formel mit Zirkel → 400 „Zirkelbezug: [A] → [B] → [A]“; Bearbeiten in fremder Abteilung → 403); `TestMeterTableRender` (Kennzeichen „berechnet“, Formel in Spalte „Quelle“, keine Spalte „Schwellwerte“); `TestMeterDeptScope`; dazu `TestHelpCoversAllPages`, `TestHelpChaptersMatch`, `TestPagesUseBaseData`, `TestI18nCatalogsComplete`.
- [ ] **Step 2:** `go test ./internal/web/ -run 'Energy|Meter|Help|I18n|PagesUseBaseData' -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS; `go build ./...`.
- [ ] **Step 5:** Im Browser (Labor-PDH): Messstelle über die Tabelle anlegen, Quelle wählen, berechnete Messstelle mit Formel, Filter und Sortierung über die Adresse; Fiori und Fluent, hell und dunkel, 390 und 1366 px.
- [ ] **Step 6:** Commit `Energiedaten: Seite Messstellen mit Zuweisungstabelle, Filtern und Handbuch`.

### Task 5: Intervallwerte, Efficio-CSV und Exporter

**Files:** Create `internal/modules/energie/interval.go`, `efficio.go`, `exporter.go`, `efficio_test.go`, `exporter_integration_test.go`

**Interfaces:**
- Consumes: `Store`, `Meter`, `energy_values` (Task 3).
- Produces:
```go
const (ModeEnd = "end"; ModeDelta = "delta")
type Point struct{ Key string; At time.Time; Value float64 }
func (s *Store) IntervalValues(ctx context.Context, meters []Meter, end time.Time, interval time.Duration, mode string) (pts []Point, missing []string, err error)
type CSVOptions struct{ TimeFormat string /* "dmy" | "iso" */; DecimalComma, Header bool; Encoding string /* "utf-8" | "windows-1252" */; Loc *time.Location }
func HeaderLine(o CSVOptions) []byte                                   // "Zeitstempel;Messstellen-ID;Messwert\r\n"
func FormatRows(pts []Point, o CSVOptions) ([]byte, error)             // sortiert nach Zeit, dann ID
func FormatValue(v float64, decimalComma bool) string                  // 4 Nachkommastellen, Nullen am Ende weg
func FilePeriod(t time.Time, days int, loc *time.Location) (start, end time.Time) // days=0: start=end=t
func FileName(prefix string, start time.Time, loc *time.Location) string
type Target interface{ Label() string; Put(ctx context.Context, name string, content []byte) error; Test(ctx context.Context) (string, error) }
type ExportConfig struct {
    ConnectionID, Prefix, Mode string; Interval time.Duration; FileDays, RetentionDays int
    CSV CSVOptions; InfraIDs, MeterKeys []string; Target Target
}
type RunResult struct{ Intervals, Rows, Missing, Delivered int; Files, Notes []string; Result string }
type Exporter struct {
    Store *Store; Configs func(ctx context.Context) ([]ExportConfig, error); Skip func() bool
    Delay time.Duration /* 2 min */; MaxCatchUp time.Duration /* 7 × 24 h */
}
func (e *Exporter) Run(ctx context.Context, c ExportConfig) (RunResult, error)
func (e *Exporter) Pass(ctx context.Context) error
func (e *Exporter) Start(ctx context.Context) // Takt 60 s, Panik abgefangen
```
- `IntervalValues`: Wert = letzter Minutenwert mit `ts` in `(end−interval, end]`. `counter`: `end` → dieser Wert, `delta` → Differenz zum Vorintervall, negativ oder fehlend → `missing`. `consumption`: Summe der Minutenwerte. `instant`: Wert am Ende. Nur Messstellen mit `export` und `active`, eingegrenzt durch `InfraIDs` (mit Unteranlagen) und `MeterKeys`.
- `Run` in einer Transaktion mit `SELECT … FROM energy_export_state … FOR UPDATE` (fehlt die Zeile: mit dem letzten abgeschlossenen Intervall anlegen): alle Intervallenden `T` mit `T ≤ now − Delay`, höchstens `MaxCatchUp` zurück; Zeilen an die Datei aus `FilePeriod(T, FileDays)` anhängen (neue Datei mit Kopfzeile, `version+1`); bei `FileDays = 0` eine Datei je Lauf, Name aus dem letzten `T`. Danach außerhalb der Transaktion alle Dateien mit `version > delivered_version` nach `created_at` an `Target.Put` (Fehler: `last_error`, nächster Lauf legt nach). Protokollzeile in `energy_export_runs` (`ok`, `partial` bei fehlenden Werten, `error`, `nothing`); `Notes` nennt je Intervall die fehlenden IDs („15.11.2026 10:15: kein Wert für [A], [V]“). Danach Dateien und Protokoll älter `RetentionDays` löschen.
- Hier nur das Ziel „Abholen“: `type PickupTarget struct{}` (`Put` tut nichts, `Label` „Zum Abholen im PDH“).

- [ ] **Step 1: Tests:** `TestFormatValue` (12.5→`"12,5"`, 0.30000000000000004→`"0,3"`, 1000→`"1000"`, -0.00004→`"0"`; Punkt bei `decimalComma=false`); `TestFormatRowsEfficio` (zwei Messstellen, zwei Zeitpunkte → `"15.11.2026 10:15;Z1;12,5\r\n"` …, sortiert); `TestFormatRowsISO` (`"2026-11-15 10:15"`); `TestFormatRowsWindows1252` (`"Zähler-Ä"` als Byte `0xC4`); `TestHeaderLine`; `TestFileNameAndPeriod` (`efficio_20261115_0000.csv`; 2 Tage → Ende 17.11. 00:00); `TestFilePeriodDST` (25.10.2026, 15 min → 100 Intervalle in einer Datei, 02:15 zweimal, letzte Zeile 23:45); `TestIntervalValuesModes` (Zählerstand, Verbrauch-Summe, Momentanwert); `TestIntervalDeltaNegativeMissing`; `TestExporterAppendsToDayFile` (zwei Läufe → eine Datei, eine Kopfzeile, `version` 2); `TestExporterFileDaysZero` (zwei Läufe → zwei Dateien); `TestExporterMissingRowAndNote` (fehlender Wert → Zeile fehlt, `Result` `partial`, Notiz mit ID); `TestExporterOnlyExportFlag` (Messstelle ohne „exportieren“ und Rohwerte von Import-Zuordnungen nie in der Datei); `TestExporterCatchUpNoDuplicates` (Zustand 3 h zurück, zwei `Run` gleichzeitig → jede Zeile genau einmal, 12 Intervalle); `TestExporterRetention` (Datei mit `period_end` vor 31 Tagen gelöscht); `TestExporterSkip`.
- [ ] **Step 2:** `go test ./internal/modules/energie/ -v` → FAIL.
- [ ] **Step 3:** `go get golang.org/x/text@latest` (direkte Abhängigkeit für `charmap.Windows1252`); umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Energiedaten: Efficio-CSV mit Intervallwerten, Tagesdateien, Protokoll und Aufbewahrung`.

### Task 6: Ablage auf Netzlaufwerk, FTP und SFTP

**Files:** Create `internal/modules/energie/target.go`, `target_test.go`, `target_ftp_test.go`. Modify `go.mod`, `go.sum`.

**Interfaces:**
- Consumes: `Target`, `Exporter.Run` (Task 5).
- Produces:
```go
type DirTarget struct{ Dir string }                       // Put: <name>.tmp schreiben, fsync, os.Rename
type FTPTarget struct{ Host string; Port int; User, Password, Folder string; ExplicitTLS bool; Timeout time.Duration }
type SFTPTarget struct{ Host string; Port int; User, Password, Folder, HostKey string; Timeout time.Duration }
func HostKeyFingerprint(key ssh.PublicKey) string         // "SHA256:…" wie ssh-keygen
var ErrHostKeyMismatch, ErrHostKeyMissing error
```
- FTP (`github.com/jlaffaye/ftp`, aktuelle Version per `go get …@latest`): `Stor` nach `.<name>.tmp`, alte Datei löschen falls `Rename` scheitert, dann `Rename`. Port Vorgabe 21, Zeitlimit 30 s.
- SFTP (`github.com/pkg/sftp`, aktuelle Version): Anmeldung mit Kennwort, Server-Schlüssel nur bei passendem `HostKey` (`ssh.FixedHostKey`-Prinzip); leer → `ErrHostKeyMissing`, `Test` liefert dann den gesehenen Fingerabdruck. Schreiben nach `.<name>.tmp`, dann `PosixRename` (ohne Erweiterung: `Remove` + `Rename`). Port Vorgabe 22.
- `Test` schreibt und löscht `.pdh-test.tmp` im Zielordner und liefert „Verbindung in Ordnung (Ordner …)“.

- [ ] **Step 1: Tests:** `TestDirTargetAtomic` (während `Put` existiert nie eine halbe `name.csv`; danach Inhalt gleich, keine `.tmp` übrig; Ersetzen einer vorhandenen Datei); `TestDirTargetMissingDir` (Fehler, keine Panik); `TestFTPTargetPutRename` (Test-FTP-Server im Test mit `net.Listen`, der `STOR`, `RNFR`/`RNTO`, `DELE` protokolliert: Reihenfolge `STOR .x.tmp` → `RNFR .x.tmp` → `RNTO x`); `TestSFTPTargetHostKey` (Server mit `golang.org/x/crypto/ssh` + `sftp.NewServer` im Test: falscher Fingerabdruck → `ErrHostKeyMismatch`; leer → `ErrHostKeyMissing` und `Test` nennt den Fingerabdruck; richtig → Datei da, keine `.tmp`); `TestExporterRedeliversMissedFiles` (Ziel scheitert in Lauf 1 und 2, gelingt in Lauf 3 → alle drei Dateistände in Reihenfolge abgelegt, `delivered_version == version`, Protokoll `error`, `error`, `ok`).
- [ ] **Step 2:** `go test ./internal/modules/energie/ -run 'Target|Redeliver' -v` → FAIL.
- [ ] **Step 3:** `go get github.com/jlaffaye/ftp@latest github.com/pkg/sftp@latest`; umsetzen.
- [ ] **Step 4:** → PASS; `go vet ./internal/modules/energie/`.
- [ ] **Step 5:** Commit `Energiedaten: Ablage auf Netzlaufwerk, FTP und SFTP, verpasste Dateien werden nachgelegt`.

### Task 7: Efficio-Export einrichten, Handbuch, Version

**Files:** Create `internal/web/export_efficio.go`, `export_efficio_test.go`, `web/templates/export_efficio.gohtml`. Modify `import_export.go` (`exportKindOptions` `{"efficio", "Efficio-CSV (Energiedaten)"}`, `exportKindFields["efficio"]`), `export_preview.go` (`exportableKind`, `exportKindLabel`, `exportDestinationLabel`, `runExport` → Efficio-Lauf), `connection_detail.go` (`connSecretFields["ftp_password"]`, `connectionTools` für `efficio`: „Dateien & Protokoll“, „Messstellen“), `web/templates/export.gohtml` (Block `fields-efficio`), `energy_wire.go` (`StartEnergyExporter`), `cmd/server/main.go` (Block `if !mirrorOn`), `help.gohtml` (Kapitel `energiedaten`: Abschnitt „Efficio-Export einrichten“ mit `.hb-howto`/`.hb-steps`; Kapitel `connections`: Export-Art erwähnen; Kapitel `admin`: neue Rechte), `widgets/help_figures.gohtml` (Skizze Messstellen → Tagesdatei → Ziel), `web/i18n/*.json`, `CHANGELOG.md`, `VERSION`.

**Interfaces:**
- Consumes: `Exporter`, `ExportConfig`, `RunResult`, `PickupTarget`, `DirTarget`, `FTPTarget`, `SFTPTarget`, `HostKeyFingerprint` (Task 5, 6); `h.loadDrive` (`drives.go`, Ziel `drive.Path()/drive_folder`, nur Laufwerke mit „Import & Export“, `..` nicht erlaubt).
- Produces: `exportKindFields["efficio"] = {"interval_minutes", "value_mode", "file_days", "retention_days", "file_prefix", "time_format", "decimal_comma", "has_header", "encoding", "target", "drive_id", "drive_folder", "ftp_host", "ftp_port", "ftp_username", "ftp_password", "ftp_folder", "ftp_tls", "sftp_host_key", "filter_infra_ids", "filter_meter_keys"}`; `target` ∈ `pickup | drive | ftp | sftp`; `func efficioConfig(id string, cfg map[string]string, drive func(id string) (*networkDrive, error)) (energie.ExportConfig, error)` (Vorgaben aus den Global Constraints, ungültige Werte → Fehler in Anwendersprache); `func (h *Handler) StartEnergyExporter(ctx context.Context)`.

| Route | Recht |
|---|---|
| `GET /export/connections/{id}/efficio` (Dateien, Protokoll der letzten 30 Läufe) | `export.read` |
| `GET /export/connections/{id}/efficio/files/{fileId}` (Herunterladen, `text/csv`, Zeichensatz je Einstellung) | `export.read` |
| `POST /export/connections/{id}/efficio/run-web` (Jetzt exportieren) | `export.write` |
| `POST /export/connections/{id}/efficio/test-web` (Verbindung testen; SFTP ohne Fingerabdruck: gesehenen Fingerabdruck speichern und anzeigen) | `export.write` |

- [ ] **Step 1: Tests:** `TestEfficioConfigDefaults` (leer → 15 min, `end`, 1 Tag, 30 Tage, `efficio`, `dmy`, Komma, Kopfzeile, UTF-8, `pickup`); `TestEfficioConfigRejects` (Takt 7, Tage −1, `drive_folder` mit `..`, FTP ohne Host); `TestEfficioPasswordSealed` (`ftp_password` landet als `ftp_password_enc`, Seite zeigt es nicht); `TestEfficioPagesRequirePermission` (Herunterladen ohne `export.read` → 403; Testen/Jetzt ohne `export.write` → 403); `TestEfficioDownload` (`PDH_TEST_DSN`: Inhalt = gespeicherte Datei, `Content-Disposition` mit Dateiname); `TestEfficioRunWeb` (Protokollzeile sichtbar); `TestEfficioNotOnMirror` (Spiegel: `StartEnergyExporter` startet nichts, POST abgewiesen); `TestChangelogMatchesVersion`; `TestHelpCoversAllPages`; `TestHelpChaptersMatch`; `TestI18nCatalogsComplete`.
- [ ] **Step 2:** → FAIL.
- [ ] **Step 3:** Umsetzen; `VERSION` → `0.80.12`; `CHANGELOG.md` `## [0.80.12] – 2026-11-15` mit `### Neu` (Messstellen und Zuweisungstabelle; berechnete Messstellen; Übersicht mit Filtern; Efficio-CSV-Export mit Ablagezielen) und `### Sicherheit` (neue Rechte `energy.*`, FTP/SFTP-Kennwort verschlüsselt, SFTP nur mit bestätigtem Server-Schlüssel).
- [ ] **Step 4:** `go vet ./...`, `go build ./...`, `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable' go test ./...` (Git Bash) → grün.
- [ ] **Step 5:** Im Browser (Labor-PDH): Efficio-Export mit Ziel „Abholen“ anlegen, „Jetzt exportieren“, Datei herunterladen und prüfen; Ziel Netzlaufwerk; Fiori und Fluent, hell und dunkel, 390 und 1366 px.
- [ ] **Step 6:** Prüfung vor dem Merge nach `AGENTS.md`: `git fetch origin main`, Migrationsnummer 161 frei, `VERSION` über `origin/main`, Dopplungen per `grep` nach `energy_`, `/energy/`, `energy.view`, `efficio`; danach Step 4 erneut.
- [ ] **Step 7:** Commit `Energiedaten: Efficio-Export einrichten, Handbuch und Änderungsprotokoll 0.80.12`.
- [ ] **Step 8: Anhalten** – Push, Testverbund und Merge nur mit ausdrücklicher Freigabe; Befehle vollständig nennen (Rechner, erwartetes Ergebnis).
