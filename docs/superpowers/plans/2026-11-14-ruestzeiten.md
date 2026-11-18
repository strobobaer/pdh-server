# Rüstzeiten als Vorgang – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rüstzeiten werden als eigener Vorgang erfasst – im PDH wie Tickets und an der Anlage im Easy-Modus mit eigenen Rüsttasten –, mit Höchstdauer je Anlage (begrenzt durch eine globale maximale Rüstzeit) und automatischer Störmeldung nach Höchstdauer + Karenzzeit.

**Architecture:** Neues Paket `internal/modules/ruesten` (Speicher, Regeln, Überwachung) mit eigener Tabelle `setup_times` und Katalogen `setup_reasons`, `setup_buttons`, `infra_setup_buttons`. Der gemeinsame Arbeitsbereich `workKinds` (`internal/web/work_board.go`) bekommt optionale Priorität, Fälligkeit, Broker und Spaltenausdrücke, damit die Rüstzeit denselben Arbeitsbereich wie Tickets nutzt; die übrigen Arten bleiben unverändert. Die Oberfläche liegt in `internal/web/setup_*.go`; die Rüsttasten im Easy-Modus und auf der Anlagenseite nutzen einen gemeinsamen Baustein.

**Tech Stack:** Go 1.24, chi, html/template, htmx, Postgres (pgx v5).

**Spec:** `docs/superpowers/specs/2026-11-14-stillstaende-ruesten-design.md`

## Global Constraints

- Paketname `ruesten` (nicht `setup`: `pdh/internal/setup` gibt es schon). Datensatztyp/Modulschlüssel überall `setup`; Tabelle `setup_times`; Pfade `/setup`.
- Status: `running` „Rüsten“, `done` „Beendet“, `fault` „Störung durch Rüsten“. Herkunft: `easy`, `manual`.
- Globale Einstellungen in `app_settings`: `setup_max_minutes` (Vorgabe **120**), `setup_grace_minutes` (Vorgabe **15**).
- Höchstdauer je Anlage `infrastructure.setup_max_minutes` (leer = vererbt). Wirksam = kleinerer Wert aus (eigener bzw. nächster übergeordneter Wert, sonst global) und global. Ein Anlagenwert über dem globalen wird beim Speichern abgelehnt.
- Höchstens eine `running`-Rüstzeit je Anlage (eindeutiger Teilindex). Tastenwechsel: altes Ende = neuer Beginn = derselbe Zeitpunkt.
- Doppeltes Antippen derselben Taste an derselben Anlage innerhalb **10 s** startet nicht neu. Eigene Missbrauchsgrenze **30** Rüst-Vorgänge je Anlage in **10 min**, getrennt von `easyLimiter` der Meldungen.
- Nach Höchstdauer + Karenzzeit: Rüstzeit endet in diesem Moment mit `fault`; **genau eine** Störmeldung (Schweregrad `high`, „Anlage steht“, Titel „Störung durch Rüsten: <Tastenname>“, Kategorie „Störung durch Rüsten“, Ersteller `pdhSystemUserID`), beide im Verlauf gegenseitig vermerkt.
- `setup_times` hat **keine** Auslöser auf `record_change_queue` (keine Benachrichtigung).
- Verantwortlich: `responsible_to` = Anlagenverantwortlicher (View `infrastructure_responsible`); `assigned_to` = beim Start diensthabender Schichtführer (Benutzer mit `shift_leader`, `shift_assignments` des Tages mit Schicht, deren Zeitraum den Start enthält, Plan veröffentlicht oder ohne Plan).
- Rechte `setup.view`, `setup.edit`, `setup.admin`, Kategorie „Infrastruktur & Anlagen“; Rollen mit `infrastructure.view` → `setup.view`; mit `infrastructure.edit` → zusätzlich `setup.edit`; Administratorrollen → `setup.admin`. Rüsten an der Anlage braucht kein Recht.
- Texte, Handbuch, Commits auf Deutsch in Anwendersprache; keine Attributionszeilen; nie `git add -A`.
- Tickets, Störungen, Aufgaben, Wartung verhalten sich unverändert. Ohne Rüsttaste an einer Anlage bleiben Easy-Modus und Anlagenseite unverändert.
- Migration **159** und Version **0.80.3** (beide vor dem Merge gegen `origin/main` prüfen).
- Tests in Git Bash; Datenbanktests über `PDH_TEST_DSN` (Labor: `postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable`), ohne Variable `t.Skip`.

## Review Focus

1. **Gleichzeitige Tipps an derselben Anlage** (zwei Handys, zwei Tasten): nie zwei `running`. → Task 2 `TestConcurrentStartsOneRunning`.
2. **Zwei Durchläufe der Überwachung oder Rollentausch**: genau eine Störmeldung. → Task 3 `TestMonitorConcurrentPassesOneFault`.
3. **Höchstdauer geändert, während eine Rüstzeit läuft**: laufende behält ihre Startwerte. → Task 2 `TestRunningKeepsStartValues`.
4. **Anlage ohne Höchstdauer, auch darüber keine**: globale maximale Rüstzeit greift. → Task 2 `TestEffectiveMaxInheritance`.
5. **Vorhandene Arten im Arbeitsbereich**: SQL und Seite für Ticket/Störung/Aufgabe/Wartung unverändert. → Task 4 `TestWorkKindsUnchanged`.

---

## Dateien

| Datei | Aufgabe |
|---|---|
| `migrations/159_setup_times.up.sql` | Tabellen, Anlagenfeld, Rechte, Gründe, Kategorie |
| `internal/modules/ruesten/model.go`, `store.go`, `responsible.go` | Typen, Speicher, Regeln, Verantwortliche |
| `internal/modules/ruesten/monitor.go` | Überwachung Höchstdauer + Karenz → Störmeldung |
| `internal/web/work_board.go` | optionale Teile im Arbeitsbereich, Art `setup` |
| `internal/web/setup_pages.go`, `setup_actions.go`, `setup_wire.go` | Seiten, Aktionen, Verdrahtung |
| `internal/web/setup_catalog.go` | Katalog, Zuordnung, Stammwerte |
| `internal/web/setup_easy.go` | Rüsten an der Anlage |
| `web/templates/setup_detail.gohtml`, `widgets/setup_*.gohtml` | Detailseite, Formulare, Baustein „Rüsten“, Katalog |

---

### Task 1: Migration 159

**Files:** Create `migrations/159_setup_times.up.sql`, `internal/modules/ruesten/migration_integration_test.go`

**Interfaces – Produces (Schema):**
- `setup_reasons(id uuid pk, key text unique, name text not null, grp text not null default 'setup' check (grp = 'setup'), sort int not null default 0, active bool not null default true, created_at)`; Zeilen `changeover` „Produktumstellung“, `aids` „Hilfsmittel-Tausch“, `cleaning` „Reinigung“.
- `setup_buttons(id uuid pk, name varchar(60) not null, reason_id uuid not null references setup_reasons, sort int, active bool default true, created_at, updated_at)`.
- `infra_setup_buttons(infrastructure_id uuid references infrastructure on delete cascade, button_id uuid references setup_buttons on delete cascade, primary key (infrastructure_id, button_id))`.
- `infrastructure.setup_max_minutes int null check (setup_max_minutes > 0)`.
- `setup_times(id uuid pk default pdh_uuid_v7(), number serial unique, infrastructure_id uuid not null references infrastructure, started_at timestamptz not null, ended_at timestamptz, reason_id uuid not null references setup_reasons, button_id uuid references setup_buttons on delete set null, button_name varchar(60) not null default '', origin text not null check (origin in ('easy','manual')), status text not null check (status in ('running','done','fault')), max_minutes int not null, grace_minutes int not null, fault_id uuid references faults on delete set null, note varchar(1000) not null default '', responsible_to uuid references users, assigned_to uuid references users, created_by uuid references users, updated_by uuid references users, created_at, updated_at, archived_at timestamptz, archived_by uuid references users, check (ended_at is null or ended_at > started_at), check ((status = 'running') = (ended_at is null)))`.
- Eindeutiger Teilindex `(infrastructure_id) WHERE status = 'running'`; Indizes `(infrastructure_id, started_at desc)`, `(status)`.
- Rechte wie in den Global Constraints (Abteilung der Quellrolle übernehmen wie Migration 158). Kategorie „Störung durch Rüsten“ in `categories`. **Keine** Auslöser auf `setup_times`.

- [ ] **Step 1: Test** `TestSetupMigration` (`PDH_TEST_DSN`, `database.RunMigrations`): drei Gründe da; zweites `running` an derselben Anlage scheitert; `done` ohne `ended_at` scheitert; `setup_max_minutes = 0` scheitert; Rolle mit `infrastructure.edit` hat `setup.view` und `setup.edit`; `pg_trigger` hat keinen Auslöser für `setup_times`; Kategorie vorhanden.
- [ ] **Step 2:** `go test ./internal/modules/ruesten/ -run TestSetupMigration -v` → FAIL.
- [ ] **Step 3:** Migration schreiben.
- [ ] **Step 4:** → PASS; `go test ./pkg/database/` → PASS.
- [ ] **Step 5:** Commit `Rüstzeiten: Tabellen, Höchstdauer an der Anlage, Rechte und Gründe (Migration 159)`.

### Task 2: Speicher und Regeln

**Files:** Create `internal/modules/ruesten/model.go`, `store.go`, `responsible.go`, `store_integration_test.go`

**Interfaces – Produces:**
```go
const (StatusRunning = "running"; StatusDone = "done"; StatusFault = "fault"; OriginEasy = "easy"; OriginManual = "manual")
const (KeyMaxMinutes = "setup_max_minutes"; KeyGraceMinutes = "setup_grace_minutes"; DefaultMaxMinutes = 120; DefaultGraceMinutes = 15)
var ErrNotFound, ErrTooSoon, ErrOverGlobalMax, ErrNotRunning, ErrAlreadyRunning, ErrReadOnly, ErrReasonInUse error
type Reason struct { ID, Key, Name, Group string; Sort int; Active bool }
type Button struct { ID, Name, ReasonID, ReasonName string; Sort int; Active bool; InheritedFrom string }
type Entry struct {
    ID string; Number int; InfraID, InfraName string; StartedAt time.Time; EndedAt *time.Time
    ReasonID, ReasonName, ButtonID, ButtonName, Origin, Status, Note string
    MaxMinutes, GraceMinutes int; FaultID, ResponsibleID, ShiftLeaderID *string; ResponsibleName, ShiftLeaderName string
    CreatedBy string; CreatedAt time.Time
}
type ManualInput struct { InfraID, ReasonID, Note string; StartedAt time.Time; EndedAt *time.Time }
type Filter struct { InfraID string /* mit Unteranlagen */; ReasonID, Status string; From, To *time.Time; Limit, Offset int }
type Store struct { DB *pgxpool.Pool; Now func() time.Time }
func NewStore(db *pgxpool.Pool) *Store
func Number(n int) string                                                  // "RZ-0001"
func (s *Store) Reasons(ctx) ([]Reason, error); SaveReason(ctx, *Reason) error; DeleteReason(ctx, id) error // ErrReasonInUse
func (s *Store) Buttons(ctx) ([]Button, error); SaveButton(ctx, *Button) error; MoveButton(ctx, id string, up bool) error; DeleteButton(ctx, id) error
func (s *Store) InfraButtons(ctx, infraID string) ([]Button, error)       // direkt + geerbt, nur aktive, nach sort
func (s *Store) SetInfraButtons(ctx, infraID string, buttonIDs []string) error
func (s *Store) GlobalMax(ctx) int; Grace(ctx) int; SetGlobal(ctx, maxMin, graceMin int) error
func (s *Store) EffectiveMax(ctx, infraID string) (minutes int, source string, err error) // source: Anlagenname oder "globaler Stamm"
func (s *Store) SetInfraMax(ctx, infraID string, minutes *int) error      // ErrOverGlobalMax
func (s *Store) Responsibles(ctx, infraID string, at time.Time) (responsible, shiftLeader *string, err error)
func (s *Store) Start(ctx, infraID, buttonID, userID, origin string) (*Entry, error) // ErrTooSoon
func (s *Store) Finish(ctx, id, userID, note string) error                 // ErrNotRunning
func (s *Store) Running(ctx, infraID string) (*Entry, error)               // nil, nil wenn keine
func (s *Store) Create(ctx, in ManualInput, userID string) (*Entry, error) // ohne Ende: ErrAlreadyRunning bei laufender
func (s *Store) Update(ctx, id string, in ManualInput, userID string) error // ErrReadOnly: Beginn/Ende bei fault
func (s *Store) Get(ctx, id string) (*Entry, error)
func (s *Store) List(ctx, f Filter) (entries []Entry, total, sumMinutes int, err error)
func (s *Store) Delete(ctx, id string) error
func (s *Store) Overdue(ctx, now time.Time) ([]Entry, error)              // running mit started_at + max + grace <= now
```
Regeln: `Start`/`Create` halten `max_minutes = EffectiveMax`, `grace_minutes = Grace`, Verantwortliche und `button_name` fest. `Start` in einer Transaktion mit `SELECT … FOR UPDATE` auf die Anlagenzeile; laufende Rüstzeit endet mit `ended_at = now`, neue beginnt mit `started_at = now`. Verlauf in `record_history` mit `ref_type = 'setup'` (Anlegen, Beenden, Wechsel, je geändertem Feld).

- [ ] **Step 1: Tests** (`PDH_TEST_DSN`): `TestStartFinish`; `TestStartSwitchesSeamlessly` (Ende alt == Beginn neu); `TestStartTooSoon` (gleiche Taste < 10 s → `ErrTooSoon`, andere Taste erlaubt); `TestConcurrentStartsOneRunning` (20 Goroutinen, zwei Tasten → genau eine `running`); `TestEffectiveMaxInheritance` (Anlage leer, Linie 45 → 45; alles leer → 120; Anlage 90 bei global 60 → 60, Quelle „globaler Stamm“); `TestSetInfraMaxOverGlobal`; `TestRunningKeepsStartValues`; `TestInfraButtonsInherited` (`InheritedFrom` = Linienname); `TestResponsibles` (Verantwortlicher vererbt; Schichtführer nur mit `shift_leader` und passender Schicht, sonst nil); `TestCreateManualRunningConflicts`; `TestUpdateWritesHistory`; `TestUpdateFaultReadOnly`; `TestListFilterAndSum`; `TestDeleteReasonInUse`; `TestNoChangeQueueEntries`.
- [ ] **Step 2:** `go test ./internal/modules/ruesten/ -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Rüstzeiten: Speicher, Rüsttasten je Anlage, Höchstdauer mit Vererbung und Verantwortliche`.

### Task 3: Überwachung – Störung durch Rüsten

**Files:** Create `internal/modules/ruesten/monitor.go`, `monitor_integration_test.go`

**Interfaces:**
- Consumes: `Store` (Task 2).
- Produces:
```go
type FaultCreator func(ctx context.Context, e Entry) (faultID string, err error)
type Monitor struct { Store *Store; CreateFault FaultCreator; Skip func() bool; SystemUser string }
func (m *Monitor) Pass(ctx context.Context) (created int, err error)
func (m *Monitor) Start(ctx context.Context) // Takt 60 s, Panik abgefangen
```
- Je überfälligem Eintrag: Zeilensperre, erneut `status = 'running'` prüfen, `CreateFault`, dann `status = 'fault'`, `ended_at = now`, `fault_id`, Verlauf „Störung durch Rüsten – Höchstdauer X min + Karenz Y min überschritten“. Scheitert `CreateFault`, bleibt `running`. `Skip()` wahr → nichts.

- [ ] **Step 1: Tests:** `TestMonitorBeforeGraceNothing`; `TestMonitorCreatesOneFault` (Status `fault`, `ended_at` = Zeitpunkt der Erzeugung, `fault_id` gesetzt); `TestMonitorTwoPassesOneFault`; `TestMonitorConcurrentPassesOneFault`; `TestMonitorCreatorFailsStaysRunning`; `TestMonitorSkip`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Rüstzeiten: nach Höchstdauer und Karenzzeit genau eine Störmeldung „Störung durch Rüsten“`.

### Task 4: Gemeinsamer Arbeitsbereich

**Files:** Modify `internal/web/work_board.go` (`workKind`, `workBaseSQL`, `workArchiveRows`, `buildWorkArea`), `web/templates/work_board.gohtml`; Test `internal/web/work_board_setup_test.go`

**Interfaces – Produces in `workKind`:** `NoPrio, NoDue, NoBroker, NoCompletion bool`; `DoPath string`; `TitleExpr, DescExpr, DueExpr, ResolutionExpr, RootCauseExpr, GroupExpr string` (leer = bisher `r.title`, `r.description`, `r.due_date`, `r.resolution`, `r.root_cause`, `r.assigned_group_id`).
Art `"setup"`: Module `setup`, Table `setup_times`, Base `/setup`, Detail `/setup/`, Page `setup`, Title „Rüstzeiten“, Label „Rüstzeit“, Plural „Rüstzeiten“, Icon `ti-arrows-exchange-2`, DoLabel „Beenden“, alle `No…` gesetzt, `TitleExpr` `'RZ-' || lpad(r.number::text, 4, '0') || ' · ' || r.button_name`, `DescExpr` `r.note`, `DueExpr` `NULL::timestamptz`, `GroupExpr` `NULL::uuid`, `ResolutionExpr`/`RootCauseExpr` `''`, ClosedExpr `r.ended_at`, OpenStatuses `running`, DoneStatuses `done`, `fault`, Flow `{"running", "Rüsten", "ti-arrows-exchange-2", "läuft"}`, Rules: „Fertig drücken, sobald die Anlage wieder produziert.“, „Den richtigen Grund wählen – er zählt für die Auswertung.“, „Nach Höchstdauer und Karenzzeit geht eine Störmeldung an die Instandhaltung.“

- [ ] **Step 1: Tests:** `TestWorkKindsUnchanged` (für `ticket`, `fault`, `task`, `maintenance` ist `workBaseSQL(k)` gleich der im Test festgehaltenen Fassung vor der Änderung; Render mit Karten enthält Prioritätsspalte und Broker-Hinweis); `TestWorkKindSetupSQL` (`PDH_TEST_DSN`: `workOpenCards` liefert laufende Rüstzeiten mit Titel `RZ-0001 · Formatwechsel`); `TestWorkBoardSetupRender` (keine Prioritätsspalte, kein Fälligkeitsfilter, kein Broker-Text, Knopf „Beenden“).
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS; `go test ./internal/web/ -run 'Work|Ticket|Fault|Task|Maint'` → PASS.
- [ ] **Step 5:** Commit `Arbeitsbereich: Priorität, Fälligkeit und Broker optional; Rüstzeiten als Art`.

### Task 5: Rüstzeit als Vorgang im PDH

**Files:** Create `internal/web/setup_pages.go`, `setup_actions.go`, `setup_wire.go`, `setup_pages_test.go`, `web/templates/setup_detail.gohtml`, `widgets/setup_form.gohtml`. Modify `handler.go` (Routen, `BaseData.CanSetup`), `nav.go`, `pdh_system.go` (`recordModules`, `recordModuleOrder`), `fieldsets.go` (`fieldModules`), `perm_gate.go` (`recordDeptSource` group=false, `areaAdminPerm`, `gateRules`), `dept_scope.go` (`scopeTables` people `r.created_by = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_to = $1::uuid`, `scopePaths`), `drives.go` (`driveModuleFolders` „Rüstzeiten“), `chat_actions.go` (`chatLinkTypes`, `chatLinkRe`), `icons.go`, `record_links.go` (`linkInfra`; offene Vorgänge an der Anlage: laufende Rüstzeit), `infra_history.go` (Filter + UNION: `started_at` → Zeitpunkt, `ended_at` → abgeschlossen), `dashboard_widgets.go` (Schnellaktion „Rüstzeit erfassen“; `loadListMine`: laufende bei `responsible_to`/`assigned_to`), `web/i18n/*.json`.

| Route | Recht |
|---|---|
| `GET /setup` (Arbeitsbereich) | `setup.view` |
| `GET /setup/new`, `POST /setup` | `setup.edit` |
| `GET /setup/{id}` | `setup.view` (Datensatz) |
| `POST /setup/{id}/edit-web`, `/finish-web` | `setup.edit` (Datensatz) |
| `POST /setup/{id}/delete-web` | `setup.admin` |

Detailseite: Kopf mit Nummer, Status (bei `fault` Verweis auf die Störmeldung), „Höchstdauer überschritten“ wenn jetzt > Beginn + Höchstdauer, Verantwortliche; Formular Beginn/Ende/Grund/Notiz (Beginn/Ende gesperrt bei `fault`); Bausteine `record-meta-slot`, `record-fields-slot`, `record-links-slot`, `record-history-slot`, Anhänge wie `ticket_detail.gohtml`.

- [ ] **Step 1: Tests:** `TestSetupPagesRequirePermission`; `TestSetupGateRules`; `TestSetupCreateFinishEdit` (`PDH_TEST_DSN`); `TestSetupDeptScope`; `TestSetupInRecordRegistries` (`recordModules["setup"]`, `fieldModules`, `areaAdminPerm["setup"] == "setup.admin"`); `TestInfraHistoryHasSetup`; `TestListMineShowsRunningSetup`; `TestSetupDetailRender` (alle drei Status, mit/ohne Rechte); dazu `TestHelpCoversAllPages`, `TestPagesUseBaseData`, `TestI18nCatalogsComplete`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Rüstzeiten: Vorgang mit Liste, Anlegen, Detailseite, Verlauf, Feldsätzen und Rechten`.

### Task 6: Stamm – globale Werte, Höchstdauer an der Anlage, Katalog, Zuordnung

**Files:** Create `internal/web/setup_catalog.go`, `setup_catalog_test.go`, `widgets/setup_catalog.gohtml`, `widgets/setup_infra_tab.gohtml`. Modify `core_settings.go` + Vorlage (Felder „Maximale Rüstzeit (Minuten)“, „Karenzzeit (Minuten)“, nur mit `setup.edit`), `infra_web.go` + `infrastructure.gohtml` (Katalog neben „Easy-Mode: Meldetexte“), `infra_detail.gohtml` (Reiter „Rüsttasten“; Stammfeld „Höchstdauer Rüsten“ mit wirksamem Wert und Quelle, speichern mit `infrastructure.edit`).

**Interfaces:** Consumes Katalog- und Stammfunktionen aus Task 2. Routen: `POST /setup/reasons-web`, `/setup/buttons-web`, `/setup/buttons/{id}/move-web`, `/setup/buttons/{id}/delete-web` (`setup.admin`), `POST /infrastructure/{id}/setup-buttons-web`, `/infrastructure/{id}/setup-max-web`, `POST /core-settings/setup-web`.

- [ ] **Step 1: Tests:** `TestCoreSetupSettings` (< 1 abgelehnt); `TestInfraSetupMaxRejectsOverGlobal` (Meldung „höchstens 120 Minuten (maximale Rüstzeit)“); `TestInfraSetupMaxShowsSource` („über Linie 2“ bzw. „globaler Stamm“); `TestSetupButtonCatalog` (anlegen, umbenennen, verschieben, deaktivieren; Grund Pflicht; Name ≤ 60); `TestInfraSetupButtonsAssign` (geerbte als „über …“); Rechte je Route.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Rüstzeiten: maximale Rüstzeit und Karenzzeit, Höchstdauer an der Anlage, Rüsttasten und Zuordnung`.

### Task 7: Rüsten an der Anlage, Status der Anlage, Überwachung starten

**Files:** Create `internal/web/setup_easy.go`, `setup_easy_test.go`, `widgets/setup_buttons.gohtml`. Modify `easy_mode.go` + `easy.gohtml` (Abschnitt „Rüsten“), Anlagenseite für Angemeldete (gleicher Baustein), `plant_watch.go` (Kennzeichen), `infra_web.go` (Status auf der Detailseite), `setup_wire.go` (`StartSetupMonitor`), `cmd/server/main.go` (Block `if !mirrorOn`).

**Interfaces:**
- Consumes: `Store.Start/Finish/Running/InfraButtons`, `Monitor` (Task 2, 3). Störmeldung: `h.faults.Create(ctx, &faults.CreateFaultInput{Title, Description, Severity: "high", InfrastructureID}, pdhSystemUserID)`, dann `h.markPlantStopped(ctx, "fault", id, pdhSystemUserID)`, Kategorie in `record_categories`, Verlauf an beiden Datensätzen.
- Routen öffentlich (wie Meldungen unter `/e/{id}`): `POST /e/{id}/setup/start` (`button_id`), `POST /e/{id}/setup/finish` (`note`); angemeldet: `POST /infrastructure/{id}/setup/start`, `/finish` (Recht: Anlage sehen).
- `func (h *Handler) setupPlantState(ctx context.Context, infraID string) string` – „Rüsten“, „Störung durch Rüsten“ (offene Störmeldung aus Rüstzeit) oder "".
- Eigener Zähler `setupLimiter` je Anlage (30 / 10 min).

- [ ] **Step 1: Tests:** `TestEasyWithoutButtonsUnchanged`; `TestEasyShowsButtonsTranslated`; `TestEasyStartFinish` („Rüsten: Formatwechsel seit …“ und „Fertig“; Ersteller „Service“); `TestEasyOverrunHint`; `TestEasyFaultState` („Störung durch Rüsten – Instandhaltung ist informiert“, kein „Fertig“); `TestSetupLimiterSeparate` (31. Vorgang abgewiesen, „Störung +“ an derselben Adresse weiter möglich); `TestEasySetupNoNotification`; `TestInfraPageButtonsForLoggedIn` (Ersteller = Person); `TestPlantWatchSetupState`; `TestMonitorWiringCreatesFault` (`PDH_TEST_DSN`: `max_minutes=1`, `grace_minutes=1`, Beginn vor 3 min → Störmeldung mit `plant_stopped`, Schweregrad `high`, Kategorie, Titel „Störung durch Rüsten: …“); `TestEasyDisabledInfraPageStillWorks`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS; `go build ./...`.
- [ ] **Step 5:** Im Browser (lokales Labor-PDH): Rüsttaste anlegen, zuordnen, im Easy-Modus starten, wechseln, „Fertig“; Höchstdauer 1 min + Karenz 1 min → Störmeldung erscheint; Fiori und Fluent, hell und dunkel, 390 und 1366 px.
- [ ] **Step 6:** Commit `Rüstzeiten: Rüsten an der Anlage, Status „Rüsten“ und „Störung durch Rüsten“, Überwachung`.

### Task 8: Handbuch, Änderungsprotokoll, Gesamtprüfung

**Files:** Modify `web/templates/help.gohtml` (Kapitel `ruestzeiten`, `data-pages="setup"`; Ergänzungen Easy-Modus, Anlage, Störungen), `widgets/help_figures.gohtml` (Skizze Rüsten → Karenz → Störung), `internal/web/help.go` (`helpChapterIDs`), `CHANGELOG.md`, `VERSION`.

- [ ] **Step 1:** Kapitel mit `.hb-howto`/`.hb-steps`: Rüsttasten anlegen und zuordnen; maximale Rüstzeit und Karenzzeit; Höchstdauer an der Anlage; an der Anlage rüsten; im PDH erfassen und korrigieren; Überschreitung; Rechte.
- [ ] **Step 2:** `git fetch origin main`; `VERSION` → nächste Korrekturversion über `main` (erwartet `0.80.3`); `CHANGELOG.md` `## [0.80.3] – <Datum>` mit `### Neu` und `### Sicherheit`.
- [ ] **Step 3:** `go vet ./...`, `go build ./...`, `go test ./...` (Git Bash, `PDH_TEST_DSN`) → grün.
- [ ] **Step 4:** Prüfung vor dem Merge nach `AGENTS.md` (Migrationsnummer, Version, Dopplungen: `grep` nach `setup_times`, `/setup`, `setup.view`), danach Step 3 erneut.
- [ ] **Step 5:** Commit `Handbuch und Änderungsprotokoll 0.80.3: Rüstzeiten`.
- [ ] **Step 6: Anhalten** – Push, Testverbund und Merge nur mit ausdrücklicher Freigabe; Befehle vollständig nennen (Rechner, erwartetes Ergebnis).
