# Wartung: Jahresplan im Kalender, Auftrag mit Vorlauf – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Geplante Wartungstermine für 12 Monate werden gespeichert, lassen sich im Kalender und im Planer verschieben und besetzen; der Durchführungsauftrag entsteht für den nächsten Termin erst einen einstellbaren Vorlauf in Arbeitstagen vorher (Vorgabe 5).

**Architecture:** Die Feiertage ziehen aus `internal/web/planner.go` in ein gemeinsames Paket `internal/core/workdays`, das Web und Wartungsmodul nutzen (eine Feiertagsliste). Eine neue Tabelle `maintenance_visits` hält die geplanten Termine (Weg A); das Wartungsmodul erzeugt, verlängert, rechnet neu und macht aus dem frühesten geplanten Termin – bzw. ohne Jahresplan aus `next_due_at` – den Auftrag, sobald der Vorlauftag erreicht ist. Der Auftrag speichert gerechneten Tag (`planned_for`) und „anstehend ab“ (`upcoming_from`), damit SQL-Leser ohne Feiertage auskommen. Kalender, Planer, Infobildschirm, Gantt, Wartungsseite und Outlook lesen die Tabelle als eigene Ebene; nichts davon zählt als offener Auftrag.

**Tech Stack:** Go (chi, html/template, pgx), htmx/Vanilla-JS, PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-11-23-wartung-jahresplan-design.md` (vom Anwender am 23.11.2026 bestätigt, „1a 2a 3a 4a“)

## Global Constraints

- Branch `feature/wartung-jahresplan` (ab `main` 0.82.45). Version **0.82.46**, Migration **212** – vor dem Merge `git show origin/main:VERSION` und `ls migrations` auf `origin/main` prüfen, sonst nächste Korrekturversion bzw. Nummer.
- Migration in `migrations/checksums.txt` mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`; neue Kennungen mit `pdh_uuid_v7()`.
- **Bestehende Dateien haben Windows-Zeilenenden (CRLF): nur per Edit ändern**, nie per Write oder `sed -i` neu schreiben. Neue Dateien dürfen LF haben.
- **Ein geplanter Termin zählt nirgends als offener Auftrag** (Startseite, Leitstand-Zähler, Kennzahlen, Erinnerungen, App, Übernahme-Liste „Wartungen“, Arbeitsbereich). Je Plan höchstens ein offener Auftrag; ein geplanter Termin wird höchstens einmal zum Auftrag.
- Eine von Hand verschobene oder besetzte Planung wird nie still überschrieben; eine fällige Wartung geht nie verloren (Termin in der Vergangenheit → sofort Auftrag).
- Vorlauf: Arbeitstage Mo–Fr ohne Feiertage (`planner_holiday_state`) und ohne Betriebsruhe (`planner_closures`), Vorgabe **5**, erlaubt **0 bis 60**, Einstellungsschlüssel `maintenance_lead_workdays` in `app_settings`; je Plan `lead_workdays` (NULL = Vorgabe). Altwert-Umrechnung `ceil(lead_days × 5 / 7)`, höchstens 60; 0 → NULL.
- Jahresplan: Termine ab `next_due_at` bis heute + 12 Monate; Verlängern, sobald der letzte geplante Termin weniger als einen Monat vor „heute + 12 Monate“ liegt.
- Kein neues Recht: ansehen `maintenance.view`; Jahresplan erzeugen, verschieben, besetzen `maintenance.edit` an der Anlage des Plans (`h.hasPermRec(r, "maintenance.edit", "maintenance_plan", planID)`); Wartungsregeln ändern `maintenance.admin` **überall** (`h.rbac.Scope(...)` → `all`). Beteiligte ohne Recht dürfen geplante Termine **nicht** verschieben (kein `recordInvolved`).
- Nicht am Spiegel schreiben (`MirrorMiddleware` sperrt POST), nicht in der Wartungssperre (`h.MaintLocked()` → 503 „Wartung läuft – bitte später erneut versuchen“); Hintergrundläufe nur am aktiven Server (`if !mirrorOn` in `cmd/server/main.go`, `skip = webHandler.MaintLocked`).
- Tage sind Kalendertage der Ortszeit: DATE-Spalten aus pgx (Mitternacht UTC) vor jeder Rechnung mit `time.Date(y, m, d, 0, 0, 0, 0, time.Local)` umsetzen; `due_date` des Auftrags = Mitternacht Ortszeit des Termintags.
- Texte deutsch für Anwender im Betrieb; Fiori und Fluent, hell und dunkel, 320–1366 px; Farben nur über `--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent` (Kind-Farbe über `--kc` wie im Kalender). Daten im JS nur über `textContent`/`value`. Neue `{{t "…"}}`- und `tr(…)`-Texte in `web/i18n/{en,ro,tr,mk}.json` (`TestI18nCatalogsComplete`).
- Tests in Git Bash: `go vet ./...`, `go test ./...`; Datenbanktests mit `//go:build integration` und `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'`. Testaufbau: im Wartungsmodul `testPool(t)`, `mustT(t)` (`internal/modules/maintenance/migration_integration_test.go`) und das Anlegen von Benutzer/Anlage wie in `TestServiceFlowIntegration`; im Web-Paket `newViewFx(t)` (`internal/web/view_rights_test.go`: zwei Abteilungen, Anlagen A/B, `role`, `user`, `req`) und `withLocal(t, loc)` (`internal/web/localday_test.go`).
- Commits deutsch, ohne Zusatzzeilen. Kein Push, kein Merge, kein Einspielen ohne ausdrückliche Freigabe des Anwenders.

## Review Focus

1. **Gespeicherte Kalender-Auswahl ohne die neue Ebene:** Wer schon eine Auswahl gemerkt hat (`app_settings user:<id>:calendar` ohne `maintenance_planned`), sieht „Wartungstermine“, wenn „Wartungen“ an ist – die Ebene ist nicht still aus (Test in Task 8).
2. **Termin an der Zeitumstellung bzw. kurz nach Mitternacht:** Ein geplanter Termin am So 28.03.2027 (Umstellung auf Sommerzeit) wird zum Auftrag mit Fälligkeit 28.03. Ortszeit und steht im Kalender in der Spalte 28.03., nicht am 27.03. (Test in Task 5).
3. **Zwei Termine vertauscht oder auf denselben Tag gezogen:** Der spätere Termin wird vor den früheren gezogen bzw. beide liegen am selben Tag – beauftragt wird zuerst der früheste Tag, der andere bleibt geplant, keiner doppelt, beide behalten ihren gerechneten Tag (Test in Task 5).
4. **Vorgabe in den Wartungsregeln geändert (5 → 10):** Gilt ab dem nächsten Lauf für Pläne ohne eigenen Vorlauf, Pläne mit eigenem Wert bleiben, bestehende Aufträge behalten ihr „anstehend ab“ (Test in Task 6).
5. **Ältere Schnittstellen-Aufrufe mit `lead_days`:** Ein `POST /api/v1/maintenance/plans` mit `lead_days: 14` und ohne `lead_workdays` bekommt 10 Arbeitstage wie die Migration – nicht still die Vorgabe (Test in Task 2).

---

### Task 1: Arbeitstage und Feiertage in `internal/core/workdays`

**Files:**
- Create: `internal/core/workdays/workdays.go`, `internal/core/workdays/workdays_test.go`
- Modify: `internal/web/planner.go` (`easterSunday`, `germanHolidays`, `freeDays` werden einzeilige Hüllen; `germanStates` bleibt)
- Unverändert: `internal/web/planner_test.go`, `internal/web/planner_models.go`

**Entscheidung (Begründung):** Die Feiertagsliste zieht in ein Kernpaket statt als Funktion ins Wartungsmodul gereicht zu werden. Das Wartungsmodul braucht die Arbeitstage im stündlichen Lauf, beim Abschluss und bei jedem Anlegen über die API – ohne Web-Handler. Ein Kernpaket ohne Abhängigkeit zu `internal/web` hält genau eine Liste und lässt sich wie `appsettings` direkt mit dem Pool nutzen.

**Interfaces:**
- Produces (Paket `workdays`):
  - `const HolidayStateKey = "planner_holiday_state"`
  - `func EasterSunday(y int) time.Time`; `func GermanHolidays(y int, state string) map[string]string` (Schlüssel `JJJJ-MM-TT`, Inhalt wie heute in `planner.go`)
  - `func FreeDays(ctx context.Context, db *pgxpool.Pool, from, to time.Time) map[string]string` – Feiertage aller Jahre von `from` bis `to` (Bundesland aus `app_settings`), dazu `planner_closures`; `db == nil` → nur bundesweite Feiertage; Lesefehler der Betriebsruhe → nur Feiertage (wie bisher).
  - `func IsWorkday(d time.Time, free map[string]string) bool` – Mo–Fr und nicht in `free`.
  - `func BackWorkdays(day time.Time, n int, free map[string]string) time.Time` – Start ist `day`, wenn Arbeitstag, sonst der letzte Arbeitstag davor; davon `n` Arbeitstage zurück; Ergebnis 0 Uhr Ortszeit; `n < 0` wie 0.
  - `func LeadDay(ctx context.Context, db *pgxpool.Pool, day time.Time, n int) time.Time` – lädt `FreeDays(day − (2n+30) Tage, day)` und ruft `BackWorkdays`.
- Web: `func easterSunday(y int) time.Time { return workdays.EasterSunday(y) }`, ebenso `germanHolidays`; `func (h *Handler) freeDays(ctx, from, to) map[string]string { return workdays.FreeDays(ctx, h.db, from, to) }`.

- [ ] **Step 1: Failing tests** in `internal/core/workdays/workdays_test.go` (`free` = `GermanHolidays(2026, "")` ∪ `GermanHolidays(2027, "")`):

```go
func TestBackWorkdays(t *testing.T) {
	d := func(s string) time.Time { t, _ := time.ParseInLocation("2006-01-02", s, time.Local); return t }
	for _, c := range []struct{ day string; n int; extra, want string }{
		{"2026-11-30", 5, "", "2026-11-23"},          // Mo → Mo davor
		{"2026-11-30", 0, "", "2026-11-30"},          // Vorlauf 0 = am Termin
		{"2026-11-28", 0, "", "2026-11-27"},          // Samstag → letzter Arbeitstag davor
		{"2026-11-29", 5, "", "2026-11-20"},          // Sonntag: ab Fr 27.11. zurück
		{"2026-11-30", 5, "2026-11-26", "2026-11-20"}, // Betriebsruhe Do 26.11.
		{"2027-01-04", 5, "", "2026-12-24"},          // Jahreswechsel: Neujahr, 25./26.12.
		{"2027-03-29", 5, "", "2027-03-18"},          // Ostermontag, Karfreitag
	} { /* free kopieren, extra dazu; BackWorkdays(d(c.day), c.n, free) == d(c.want) */ }
}
func TestIsWorkday(t *testing.T) { /* 2026-12-24 ja, 2026-12-25 nein, 2026-11-28 nein */ }
func TestSingleHolidayList(t *testing.T) {
	// alle .go-Dateien ohne _test.go unter ../../../internal, ../../../cmd, ../../../pkg:
	// „Pfingstmontag“ nur in internal/core/workdays/workdays.go; Meldung „Feiertage nur in internal/core/workdays pflegen“
}
```

- [ ] **Step 2: Run** `go test ./internal/core/workdays/` – Expected: FAIL (Paket fehlt).
- [ ] **Step 3: Implement** – Rumpf von `easterSunday`/`germanHolidays` nach `workdays` verschieben (nur umbenennen, `dayISO` → `"2006-01-02"`), `FreeDays` aus `freeDays` (statt `h.appSetting` eine Abfrage auf `app_settings`), dann die Hüllen in `planner.go` per Edit.
- [ ] **Step 4: Run** `go test ./internal/core/workdays/ && go test ./internal/web -run 'Planner|Holiday|Easter|Calendar' && go vet ./internal/...` – Expected: PASS, `planner_test.go` unverändert grün.
- [ ] **Step 5: Commit** `git commit -m "Arbeitstage: Feiertage und Betriebsruhe in ein gemeinsames Paket"`

### Task 2: Migration 212, Felder an Plan und Auftrag, Vorlauf in Arbeitstagen

**Files:**
- Create: `migrations/212_maintenance_yearplan.up.sql`; Modify: `migrations/checksums.txt`
- Modify: `internal/modules/maintenance/model.go`, `internal/modules/maintenance/repo_plans.go` (`planSelect`, `scanPlan`, `normalizePlan`, `CreatePlan`, `UpdatePlan`, `DuplicatePlan`), `internal/modules/maintenance/repo_tasks.go` (`taskSelect`, `scanTask`), `pkg/appsettings/appsettings.go`
- Create: `internal/modules/maintenance/lead.go`
- Test: `internal/modules/maintenance/lead_test.go`, `internal/modules/maintenance/yearplan_migration_integration_test.go`; Modify: `internal/modules/maintenance/schedule_test.go:94` (`LeadDays: 400` → `LeadWorkdays: ptr(61)`, Meldung „Vorlauf: 0 bis 60 Arbeitstage“)

**Interfaces:**
- Migration:

```sql
CREATE TABLE maintenance_visits (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    plan_id           UUID NOT NULL REFERENCES maintenance_plans(id) ON DELETE CASCADE,
    calc_day          DATE NOT NULL,                 -- gerechneter Tag (Rhythmus)
    day               DATE NOT NULL,                 -- Tag (ggf. verschoben)
    assigned_to       UUID REFERENCES users(id) ON DELETE SET NULL,
    assigned_group_id UUID REFERENCES user_groups(id) ON DELETE SET NULL,
    date_manual       BOOLEAN NOT NULL DEFAULT false, -- Tag von Hand geändert
    staff_manual      BOOLEAN NOT NULL DEFAULT false, -- Person/Gruppe von Hand geändert
    off_plan          BOOLEAN NOT NULL DEFAULT false, -- „weicht vom Plan ab“
    state             TEXT NOT NULL DEFAULT 'planned' CHECK (state IN ('planned', 'ordered', 'dropped')),
    task_id           UUID REFERENCES maintenance_tasks(id) ON DELETE SET NULL,
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX uq_maintenance_visits_planned ON maintenance_visits (plan_id, calc_day) WHERE state = 'planned';
CREATE UNIQUE INDEX uq_maintenance_visits_task ON maintenance_visits (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX idx_maintenance_visits_day ON maintenance_visits (day) WHERE state <> 'ordered';
CREATE INDEX idx_maintenance_visits_plan ON maintenance_visits (plan_id, day);

ALTER TABLE maintenance_plans ADD COLUMN lead_workdays SMALLINT CHECK (lead_workdays BETWEEN 0 AND 60);
ALTER TABLE maintenance_plans ADD COLUMN year_plan_at TIMESTAMPTZ;   -- Jahresplan erzeugt (NULL = nein)
UPDATE maintenance_plans SET lead_workdays = LEAST(60, CEIL(lead_days * 5 / 7.0))::smallint WHERE lead_days > 0;

ALTER TABLE maintenance_tasks ADD COLUMN planned_for DATE;            -- gerechneter Tag (NULL = due_date)
ALTER TABLE maintenance_tasks ADD COLUMN upcoming_from DATE;          -- „anstehend ab“ (NULL = ab Fälligkeit)
ALTER TABLE maintenance_tasks ADD COLUMN due_manual BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE maintenance_tasks ADD COLUMN staff_manual BOOLEAN NOT NULL DEFAULT false;
UPDATE maintenance_tasks mt SET upcoming_from = mt.due_date::date - mp.lead_days
  FROM maintenance_plans mp WHERE mp.id = mt.plan_id AND mt.status IN ('open', 'in_progress', 'pending');

ALTER TABLE microsoft_calendar_links DROP CONSTRAINT IF EXISTS microsoft_calendar_links_source_type_check;
ALTER TABLE microsoft_calendar_links ADD CONSTRAINT microsoft_calendar_links_source_type_check
  CHECK (source_type IN ('shift', 'task', 'maintenance', 'maintenance_planned', 'ticket', 'fault'));
```

  Die Spalte `lead_days` bleibt (Rückweg), wird aber von keinem Go-Code mehr gelesen oder geschrieben.
- Produces:
  - `appsettings.KeyMaintenanceLeadWorkdays = "maintenance_lead_workdays"`, `appsettings.DefaultMaintenanceLeadWorkdays = 5`
  - `MaintenancePlan`: neu `LeadWorkdays *int` (`json:"lead_workdays"`), `YearPlanAt *time.Time` (`json:"year_plan_at,omitempty"`), `VisitCount int` (`json:"visit_count"`, geplante Termine im Zustand `planned`, Unterabfrage in `planSelect`). Das Feld `LeadDays` und sein Lesen von `lead_days` entfallen erst in Task 3 zusammen mit seinen Lesern (damit dieser Schritt baut).
  - `MaintenanceTask`: neu `PlannedFor *time.Time` (`json:"planned_for,omitempty"`), `UpcomingFrom *time.Time` (`json:"upcoming_from,omitempty"`), `DueManual`, `StaffManual bool` (in `taskSelect`/`scanTask`); `LeadDays` entfällt in Task 3.
  - `PlanInput`: neu `LeadWorkdays *int` (`json:"lead_workdays"`); `LeadDays int` (`json:"lead_days,omitempty"`) bleibt nur als **Altform**: ist `LeadWorkdays == nil` und `LeadDays > 0`, setzt `normalizePlan` `LeadWorkdays = min(60, ceil(LeadDays*5/7))`. `LeadWorkdays` außerhalb 0–60 → `inputErr("Vorlauf: 0 bis 60 Arbeitstage")`. `CreatePlan`/`UpdatePlan` schreiben `lead_workdays` statt `lead_days`; `DuplicatePlan` übernimmt `LeadWorkdays`, nicht `year_plan_at` und keine geplanten Termine.
  - `lead.go`: `func (r *Repository) DefaultLead(ctx context.Context) int` (Einstellung, Vorgabe 5, außerhalb 0–60 → 5); `func (r *Repository) EffectiveLead(ctx context.Context, planLead *int) int`; `func (r *Repository) OrderDay(ctx context.Context, day time.Time, planLead *int) time.Time` = `workdays.LeadDay(ctx, r.db, day, EffectiveLead(…))`; `func civil(d time.Time) time.Time` – Kalendertag einer DATE-Spalte als 0 Uhr Ortszeit.

- [ ] **Step 1: Failing tests.**
  - `lead_test.go` (ohne Datenbank): `TestNormalizeLeadAltform` – `PlanInput{LeadDays: 14}` → `*LeadWorkdays == 10`; `{LeadDays: 1}` → 1; `{LeadDays: 365}` → 60; `{LeadDays: 14, LeadWorkdays: ptr(3)}` → 3; `{LeadWorkdays: ptr(61)}` und `ptr(-1)` → `IsInputError`, Meldung „Vorlauf: 0 bis 60 Arbeitstage“; `{}` → `nil` (Vorgabe). `TestCivil` – `civil(time.Date(2027,3,28,0,0,0,0,time.UTC))` ist 28.03.2027 0 Uhr `time.Local`.
  - `yearplan_migration_integration_test.go` (Muster `migration_integration_test.go`: `copyMigrations(t, dir, 211)`, Daten anlegen, dann bis 212): Plan A `lead_days 0` → `lead_workdays IS NULL`; Plan B `lead_days 14` → 10; Plan C `lead_days 365` → 60; offener Auftrag von B mit `due_date` 2026-12-20 → `upcoming_from` 2026-12-06; erledigter Auftrag → `upcoming_from IS NULL`; `INSERT INTO microsoft_calendar_links … source_type 'maintenance_planned'` geht, `'foo'` scheitert; zweiter geplanter Termin mit gleichem `(plan_id, calc_day)` scheitert, derselbe im Zustand `dropped` geht.
  - `TestPlanAPIAcceptsLeadDays` (Review Focus 5, `service_integration_test.go`-Aufbau): `svc.CreatePlan(&PlanInput{…, LeadDays: 14})` → `GetPlan().LeadWorkdays == 10`; `UpdatePlan` mit `LeadWorkdays: nil, LeadDays: 0` → `nil`.
- [ ] **Step 2: Run** `go test ./internal/modules/maintenance/ -run 'Lead|Civil|Normalize' && go test -tags integration ./internal/modules/maintenance/ -run 'YearplanMigration|PlanAPIAcceptsLeadDays'` – Expected: FAIL.
- [ ] **Step 3: Implement**; Migration eintragen: `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- [ ] **Step 4: Run** dieselben Befehle und `go build ./... && go test ./pkg/database -run TestMigrationsFrozen && go test -tags integration ./pkg/database -run TestUUIDv7Policy` – Expected: PASS (bei `TestUUIDv7Policy` nur die bekannten acht alten Tabellen aus AGENTS.md Punkt 14, keine `maintenance_visits`).
- [ ] **Step 5: Commit** `git commit -m "Wartung: geplante Termine (Tabelle), Vorlauf in Arbeitstagen, „anstehend ab“ am Auftrag (Migration 212)"`

### Task 3: „Anstehend“ aus `upcoming_from`, Fälligkeit von Hand merken

**Files:**
- Modify: `internal/web/handler.go:4718-4728` (`maintUpcoming`), `internal/web/work_board.go:385-388` (`workBaseSQL`, Wartung), `internal/web/testdata/work_base_maintenance.sql`, `internal/web/assignment_new.go:73` (`unassignedKinds`, Wartung), `internal/web/global_dashboard.go:433-445,467-470` (`leadDays` → `upcomingFrom`), `internal/web/work_board_test.go:181-198` (`TestDashboardCountersMatchLists` auf `UpcomingFrom`)
- Modify: `internal/modules/maintenance/repo_tasks.go` (`UpdateTask`, `UpdateDueDate`), `internal/web/calendar_board.go:642-672` (`CalendarMoveWeb`, Zweig Wartung), `internal/web/planner_maint.go:226-268` (`PlannerMaintSaveWeb`)
- Test: `internal/web/maint_upcoming_test.go`, `internal/modules/maintenance/due_manual_integration_test.go`

**Interfaces:**
- Consumes: `Repository.OrderDay`, `civil` (Task 2).
- Entfällt jetzt: `MaintenancePlan.LeadDays`, `MaintenanceTask.LeadDays`, `mp.lead_days` in `planSelect`/`scanPlan`/`taskSelect`/`scanTask`; `CreatePlan`/`UpdatePlan` schreiben `lead_days` nicht mehr (die Altform `PlanInput.LeadDays` bleibt, Task 2).
- Regel „anstehend ab“ überall gleich: `COALESCE(upcoming_from, due_date::date)`.
  - `maintUpcoming(m, now)`: Status `open`, Fälligkeit nach heute + 7 **und** `upFrom.After(today)` mit `upFrom = civil(*m.UpcomingFrom)` bzw. Fälligkeitstag ohne Wert.
  - `workBaseSQL` Wartung: `r.due_date::date - COALESCE((SELECT mp.lead_days …), 0) > CURRENT_DATE` → `COALESCE(r.upcoming_from, r.due_date::date) > CURRENT_DATE` (Goldendatei mitändern, sonst nichts in der Datei).
  - Übernahme-Liste: `… < NOW() + INTERVAL '1 day'` → `COALESCE(m.upcoming_from, m.due_date::date) <= CURRENT_DATE`.
  - Leitstand: Abfrage `SELECT mt.id::text, COALESCE(mt.upcoming_from, mt.due_date::date) FROM maintenance_tasks mt WHERE mt.status = 'open'`; `item.Upcoming = civil(upFrom).After(today)`.
- Produces: `func (r *Repository) AfterDueChange(ctx context.Context, taskID string) error` – setzt `due_manual = true` und `upcoming_from = OrderDay(due_date, lead des Plans)` (ohne Plan: `NULL`). Aufrufer: `UpdateDueDate` (und damit `Service.Wait`), `UpdateTask` (nur wenn sich `due_date` ändert), `CalendarMoveWeb` (nur `in.Kind == "maintenance"`, über `h.maint.Repo()`), `PlannerMaintSaveWeb` (wenn sich das Datum ändert). Besetzen im Planer setzt zusätzlich `staff_manual = true` im selben `UPDATE`.

- [ ] **Step 1: Failing tests.**
  - `maint_upcoming_test.go`: `TestNoPlanLeadDaysReaders` (Muster `TestNoUTCDayTruncate`): keine `.go`-Datei ohne `_test.go` unter `../../internal`, `../../cmd`, `../../pkg` enthält `mp.lead_days`, `p.lead_days` oder `.LeadDays` eines Wartungstyps (Regex `(mp|p)\.lead_days|maintenance\.\w+\{[^}]*LeadDays`), und `internal/web/testdata/work_base_maintenance.sql` enthält kein `lead_days`; Meldung „Vorlauf nur über upcoming_from lesen“. `TestDashboardCountersMatchLists` mit `mk(status, dueInDays, upInDays)` → `UpcomingFrom = now+upInDays`: `(open, 30, 20)` true; `(open, 30, 0)` false; `(open, 5, 3)` false; `(in_progress, 30, 20)` false; `(open, -2, -5)` false; `UpcomingFrom == nil` und Fälligkeit in 30 Tagen → true.
  - `due_manual_integration_test.go` (Wartungsmodul): Plan mit `LeadWorkdays: ptr(5)`, Auftrag direkt angelegt mit `due_date` Mo 30.11.2026; `UpdateDueDate` auf Mo 14.12.2026 → `due_manual`, `upcoming_from` 2026-12-07; `UpdateTask` mit gleichem Datum → `due_manual` bleibt wie war; Auftrag ohne Plan → `upcoming_from IS NULL`.
  - In `internal/web/calendar_board_move_integration_test.go` (neu, `newViewFx`): `CalendarMoveWeb` eines Wartungsauftrags → `due_manual = true`, `upcoming_from` neu; eines Tickets → unverändert wie bisher.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestNoPlanLeadDays|TestDashboardCounters|TestWorkKindsUnchanged' && go test -tags integration ./internal/modules/maintenance/ -run DueManual && go test -tags integration ./internal/web -run CalendarMove` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go build ./... && go test ./internal/web -run 'Work|Assign|Global|Maint'` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Wartung: „anstehend“ aus dem gespeicherten Vorlauftag; Fälligkeit von Hand wird gemerkt"`

### Task 4: Geplante Termine im Wartungsmodul

**Files:**
- Create: `internal/modules/maintenance/visits.go`
- Modify: `internal/modules/maintenance/repo_plans.go` (`UpdatePlan`: Neurechnen/Besetzung; `SetPlanActive`), `internal/modules/maintenance/service.go` (Hüllen)
- Test: `internal/modules/maintenance/visits_integration_test.go`

**Interfaces:**
- Consumes: Spalten aus Task 2, `PlannedDates`, `AddInterval`, `civil`.
- Produces:
  - `const VisitPlanned, VisitOrdered, VisitDropped = "planned", "ordered", "dropped"`
  - `type Visit struct{ ID, PlanID, PlanName, InfraID, InfraName string; CalcDay, Day time.Time; AssignedTo, GroupID, ResponsibleTo *string; AssigneeName, GroupName string; DateManual, StaffManual, OffPlan bool; State string; TaskID *string; EstimatedMin int }` (Tage als 0 Uhr Ortszeit).
  - `var ErrVisitNotFound = errors.New("Geplanter Termin nicht gefunden")`
  - `func (r *Repository) GenerateYearPlan(ctx context.Context, planID, userID string, now time.Time) (added int, err error)` – in einer Transaktion mit der Sperre je Plan (`pg_advisory_xact_lock(hashtext('maintenance_plan:' || id))`, wie `ensureTaskForPlan`); nur aktive Pläne (sonst `inputErr("Der Plan ruht – bitte zuerst aktivieren")`); Tage = `PlannedDates(unit, count, next_due_at, now + 12 Monate, 400)`; übersprungen wird ein Tag, für den es schon einen Termin gibt (beliebiger Zustand, gleicher `calc_day`) oder der `COALESCE(planned_for, due_date::date)` eines offenen Auftrags des Plans ist; neue Termine mit Person/Gruppe des Plans, `created_by`; setzt `year_plan_at = COALESCE(year_plan_at, NOW())`.
  - `func (r *Repository) ExtendYearPlans(ctx context.Context, now time.Time) (plans int, err error)` – aktive Pläne mit `year_plan_at`, deren spätester `calc_day` vor `now + 12 Monate − 1 Monat` liegt (oder ohne Termin), bekommen `GenerateYearPlan`.
  - `func (r *Repository) recalcVisits(ctx context.Context, tx pgx.Tx, planID string, now time.Time) error` – löscht geplante Termine ohne `date_manual` und ohne `staff_manual`, legt neu an wie `GenerateYearPlan` (ohne Sperre, gleiche Transaktion), setzt bei den übrigen geplanten `off_plan = calc_day NOT IN (neue Tage)`.
  - `func (r *Repository) MoveVisit(ctx context.Context, visitID string, day time.Time, userID string, now time.Time) error` – nur `planned` (sonst `inputErr("Aus diesem Termin ist schon ein Auftrag geworden")`), `day` vor heute → `inputErr("Ein geplanter Termin lässt sich nicht in die Vergangenheit legen")`; setzt `day`, `date_manual`, `updated_by/at`.
  - `func (r *Repository) StaffVisit(ctx context.Context, visitID string, assignedTo, groupID *string, userID string) error` – setzt beide (nil = niemand), `staff_manual`.
  - `func (r *Repository) ResetVisit(ctx context.Context, visitID, userID string, now time.Time) error` – „Zurück zum Plan“: Flags aus, dann `recalcVisits` des Plans (der Termin entsteht aus der Rechnung neu bzw. entfällt, wenn sein Tag nicht mehr im Rhythmus liegt).
  - `func (r *Repository) GetVisit(ctx context.Context, id string) (*Visit, error)`; `func (r *Repository) VisitsInRange(ctx context.Context, from, to time.Time, states ...string) ([]Visit, error)` (nach `day`, Plan-Name; mit Anlage, Verantwortlichem, Namen); `func (r *Repository) PlanVisits(ctx context.Context, planID string) ([]Visit, error)`.
  - `UpdatePlan`: alte Werte vor dem Ändern lesen. Haben sich Einheit, Anzahl, Modus oder `next_due_at` geändert und `year_plan_at IS NOT NULL` → `recalcVisits`; sonst bei geänderter Person/Gruppe `UPDATE maintenance_visits SET assigned_to, assigned_group_id … WHERE state = 'planned' AND NOT staff_manual`.
  - `SetPlanActive(false)`: zusätzlich `DELETE FROM maintenance_visits WHERE plan_id = $1 AND state = 'planned'` (`year_plan_at` bleibt). `SetPlanActive(true)`: bei `year_plan_at IS NOT NULL` `GenerateYearPlan` („Wieder einschalten: Jahresplan neu erzeugen“ geschieht von selbst).
  - Service-Hüllen mit `time.Now()`: `GenerateYearPlan(ctx, planID, userID)`, `MoveVisit`, `StaffVisit`, `ResetVisit`, `GetVisit`.

- [ ] **Step 1: Failing tests** (`visits_integration_test.go`, Hilfsaufbau `jpEnv(t)`: `testPool`, Benutzer zwei Personen, Gruppe, Anlage; `mkPlan(unit string, count int, mode string, next time.Time, lead *int) string` über `svc.CreatePlan`; `now` fest auf 23.11.2026 10:00; Aufräumen per `t.Cleanup`):
  - `TestGenerateYearPlan`: monatlich ab 01.12.2026 → 12 Termine 01.12.2026 … 01.11.2027, Person aus dem Plan, `year_plan_at` gesetzt, `VisitCount == 12`; zweiter Aufruf → `added == 0`; Plan mit offenem Auftrag am 01.12. → 11 Termine (kein 01.12.).
  - `TestExtendYearPlans`: erzeugt am 23.11.2026; `ExtendYearPlans(now = 05.11.2027)` → neuer Termin 01.12.2027 (und nur dieser), Plan ohne `year_plan_at` unberührt; `now = 01.12.2026` → nichts.
  - `TestRecalcKeepsManual`: wöchentlich, Jahresplan; Termin 2 per `MoveVisit` verschoben, Termin 3 per `StaffVisit` besetzt; `UpdatePlan` auf 14-täglich → Termine 2 und 3 stehen unverändert, `off_plan` bei dem, dessen `calc_day` nicht mehr im 14-Tage-Raster liegt; übrige im neuen Raster; keine Doppelten.
  - `TestStaffOnlyChange`: Person am Plan ändern → nicht besetzte Termine übernehmen sie, Termin 3 behält seine.
  - `TestMoveVisitRules`: in die Vergangenheit → Meldung; beauftragter Termin → Meldung; ok → `date_manual`, `updated_by`.
  - `TestResetVisit`: verschobener Termin → nach `ResetVisit` wieder am gerechneten Tag, Flags aus; Termin mit `off_plan` → entfällt, kein Termin außerhalb des Rasters.
  - `TestDeactivateRemovesPlanned`: `SetPlanActive(false)` → keine geplanten Termine, `ordered` bleiben; `SetPlanActive(true)` → wieder 12; Plan löschen (`DELETE FROM maintenance_plans`) → Termine weg (Fremdschlüssel).
- [ ] **Step 2: Run** `go test -tags integration ./internal/modules/maintenance/ -run 'TestGenerateYearPlan|TestExtendYearPlans|TestRecalc|TestStaffOnly|TestMoveVisit|TestResetVisit|TestDeactivate'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** derselbe Befehl und `go test -tags integration ./internal/modules/maintenance/` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Wartung: Jahresplan erzeugen, verlängern, neu rechnen; Termine verschieben und besetzen"`

### Task 5: Auftrag erst mit dem Vorlauf – alle Wege

**Files:**
- Modify: `internal/modules/maintenance/schedule.go` (`ensureTaskForPlan`, `EnsurePlanTasks`, `ScheduleNext`, `NextDueForTask` → `NextTermForTask`, `StartScheduler`, Kopfkommentar „Grundsatz“), `internal/modules/maintenance/service.go` (`CreatePlan`, `UpdatePlan`, `DuplicatePlan`, `EnsurePlanTask`, `GenerateTasks`, `NextDueForTask` → `NextTermForTask`, neuer Hook), `internal/modules/maintenance/repo_plans.go` (`UpdatePlan`: Übertrag auf den offenen Auftrag), `internal/modules/maintenance/repo_tasks.go` (`CreateTask`), `internal/modules/maintenance/model.go` (Paketkommentar „genau einen offenen Auftrag“ berichtigen)
- Modify: `internal/modules/maintenance/service_integration_test.go` (Plan bekommt `LeadWorkdays: ptr(5)`; sonst unverändert)
- Test: `internal/modules/maintenance/order_integration_test.go`

**Interfaces:**
- Consumes: Task 2–4 (`OrderDay`, `civil`, Termine, `recalcVisits`).
- Produces:
  - `type candidate struct{ VisitID string; CalcDay, Day time.Time; AssignedTo, GroupID *string; DateManual, StaffManual bool }` – der früheste geplante Termin des Plans nach `day` (bei Gleichstand früherer `calc_day`), ohne Termin `next_due_at` mit Person/Gruppe des Plans.
  - `func (r *Repository) ensureTaskForPlan(ctx context.Context, planID, createdBy string, now time.Time) (taskID string, err error)` – leer = nichts angelegt. Ablauf in einer Transaktion mit der Sperre je Plan: offener Auftrag → nichts; Kandidat ermitteln; `OrderDay(Kandidat.Day, lead_workdays)` nach dem Tag von `now` → nichts; sonst Auftrag mit `due_date = Kandidat.Day` (0 Uhr Ortszeit), `planned_for = CalcDay`, `upcoming_from = Vorlauftag`, Person/Gruppe des Kandidaten, `due_manual`/`staff_manual` vom Termin; Termin `state = 'ordered'`, `task_id`. Nach dem Commit `runPlanTaskCreatedHooks(ctx, taskID, createdBy)`.
  - `func OnPlanTaskCreated(fn func(ctx context.Context, taskID, actorID string))` – Muster `OnTaskCompleted` (Sperre, Kopie der Liste, `recover` je Hook, `context.WithoutCancel`).
  - `func (r *Repository) EnsurePlanTasksAt(ctx context.Context, now time.Time) (int, error)`; `EnsurePlanTasks(ctx)` ruft sie mit `time.Now()`.
  - `ScheduleNext`: Grundlage im Modus „fest“ ist `COALESCE(planned_for, due_date::date)` des Auftrags, nicht der verschobene Tag; nach dem Setzen von `next_due_at`: geplante Termine mit `day` vor heute → `dropped` (beide Modi – im Modus „ab Durchführung“ sind sie nach einem Abschluss ebenso überholt); im Modus „ab Durchführung“ mit Jahresplan `recalcVisits`; dann sofort `ensureTaskForPlan(…, time.Now())`.
  - `UpdatePlan` (Übertrag auf den offenen, nicht begonnenen Auftrag): Fälligkeit nur, wenn `next_due_at` sich geändert hat und `NOT mt.due_manual`; Person/Gruppe nur, wenn sie sich am Plan geändert haben und `NOT mt.staff_manual`; Titel, Beschreibung, Art, Anlage, Priorität, Kostenstelle wie bisher. Bei geänderter Fälligkeit `upcoming_from` neu.
  - `CreateTask` mit `PlanID`: in einer Transaktion mit der Sperre je Plan; gibt es einen geplanten Termin mit `day` = Fälligkeitstag, wird der früheste davon verknüpft (`ordered`, `task_id`, `planned_for = calc_day`); `upcoming_from = OrderDay(Fälligkeit, Vorlauf des Plans)`. Ohne Plan: `upcoming_from` NULL.
  - Alle Wege über `ensureTaskForPlan(…, time.Now())`: `Service.CreatePlan`, `UpdatePlan`, `DuplicatePlan`, `EnsurePlanTask`, `GenerateTasks` (= `EnsurePlanTasks`), `Repository.SetPlanActive(true)`, `ScheduleNext`.
  - `type NextTerm struct{ Day time.Time; TaskID string; OrderOn time.Time }` – `TaskID` leer = noch kein Auftrag, `OrderOn` = Vorlauftag (frühestens heute). `func (r *Repository) NextTermForTask(ctx context.Context, taskID string, now time.Time) *NextTerm` (nil ohne aktiven Plan); offener anderer Auftrag → dessen Tag und Kennung; sonst Kandidat. `Service.NextTermForTask(ctx, taskID)` ersetzt `NextDueForTask`.
  - `StartScheduler(ctx, skip)`: jeder Lauf (Start, stündlich) `EnsurePlanTasksAt(now)`; einmal je Kalendertag zuerst `ExtendYearPlans(now)` (Merker im Lauf, beim Start sofort). `skip()` wahr → nichts, auch kein Verlängern.

- [ ] **Step 1: Failing tests** (`order_integration_test.go`, `jpEnv` aus Task 4; Pläne mit eigenem Vorlauf, damit die geteilte Einstellung nicht stört):
  - `TestOrderOnLeadDay`: Plan ab Mo 15.02.2027, Vorlauf 5 → `EnsurePlanTasksAt(07.02.2027 23:00)` legt nichts an; `(08.02.2027 00:30)` legt genau einen an mit `due_date` 15.02., `upcoming_from` 08.02.; dritter Lauf → kein zweiter.
  - `TestOrderVorlaufZeroAndFreeDay`: Vorlauf 0, Termin Sa 27.02.2027 → Auftrag ab Fr 26.02.
  - `TestOrderMissedDay`: Termin vorgestern (relativ zu heute), kein Auftrag → ein Lauf legt ihn sofort an (überfällig), kein Termin geht verloren.
  - `TestOrderFromVisit`: Jahresplan; zweiter Termin per `StaffVisit` an Person B, erster per `MoveVisit` auf +2 Tage → erster Auftrag hat den verschobenen Tag, `planned_for` = gerechneter Tag, `due_manual`; Termin `ordered`, nicht mehr `planned`.
  - `TestOrderSwappedVisits` (Review Focus 3): Termine T1 (01.03.2027) und T2 (05.04.2027); T2 auf 22.02.2027 gezogen, danach T1 auf 22.02.2027 → zuerst wird **ein** Auftrag am 22.02. angelegt (der mit dem früheren `calc_day`, T1); der andere bleibt `planned`; beide `calc_day` unverändert; kein zweiter offener Auftrag.
  - `TestOrderDSTDay` (Review Focus 2, `time.Local = Europe/Berlin` über eine Testhilfe `withLocal` im Paket, gebaut wie in `internal/web/localday_test.go`): Termin So 28.03.2027, Vorlauf 0 → `due_date.Local()` = 28.03.2027 00:00 Ortszeit, `due_date::date` in der Sitzungszone `Europe/Berlin` = 2027-03-28; Termin Mo 29.03. (Ostermontag), Vorlauf 5 → `upcoming_from` 2027-03-18.
  - `TestFixedRhythmFromCalcDay` (Entscheidung 1): fest, monatlich, Termin 01.03. auf 10.03. verschoben und beauftragt, erledigt → `next_due_at` = 01.04., nicht 10.04.
  - `TestLateCompletionDrops` (Entscheidung 2): fest, wöchentlich, Jahresplan; Auftrag von vor 3 Wochen erst heute erledigt → die zwei vergangenen geplanten Termine `dropped`, weiter mit dem nächsten künftigen; kein Rückstau.
  - `TestCompletionModeRecalc`: „ab Durchführung“, Jahresplan; verspätet erledigt → nicht von Hand geänderte Termine ab dem neuen `next_due_at` neu, von Hand verschobener künftiger Termin bleibt mit `off_plan`.
  - `TestShortRhythmOneOpen`: täglich, Vorlauf 5 → nach dem Erledigen genau ein neuer offener Auftrag, nie zwei.
  - `TestAllPathsWaitForLead`: Plan mit Termin in 30 Tagen, Vorlauf 5 → `CreatePlan`, `UpdatePlan`, `DuplicatePlan`, `SetPlanActive(false/true)`, `EnsurePlanTask`, `GenerateTasks` legen **keinen** Auftrag an; mit Termin morgen legt jeder Weg genau einen an.
  - `TestUpdatePlanKeepsManual`: Auftrag aus verschobenem und besetztem Termin; `UpdatePlan` mit neuem Namen, neuer Person und neuer Fälligkeit → Titel neu, Fälligkeit und Person bleiben; Auftrag ohne Handänderung → übernimmt beides.
  - `TestCreateTaskLinksVisit`: `CreateTask` mit `PlanID` am Tag des nächsten Termins → Termin `ordered`, `task_id` gesetzt; an einem anderen Tag → kein Termin verknüpft.
  - `TestPlanTaskCreatedHook`: Hook zählt; `EnsurePlanTasksAt` mit erreichtem Vorlauf → einmal mit `taskID` und leerem `actorID`; kein Auftrag → kein Aufruf.
  - `TestSchedulerSkip`: `skip` wahr → nach dem Lauf kein Auftrag; danach `skip` falsch → nachgeholt (über `EnsurePlanTasksAt`, der Lauf selbst mit kleinem Testtakt nicht nötig: `StartScheduler`-Rumpf in `func (s *Service) schedulerRun(ctx context.Context, skip func() bool, now time.Time, lastExtend *time.Time)` herausziehen und den testen).
  - `TestNextTermForTask`: nach dem Abschluss ohne Auftrag → `Day` = nächster geplanter (verschobener) Tag, `TaskID` leer, `OrderOn` = Vorlauftag; mit Auftrag → `TaskID` gesetzt.
- [ ] **Step 2: Run** `go test -tags integration ./internal/modules/maintenance/ -run 'TestOrder|TestFixedRhythm|TestLateCompletion|TestCompletionMode|TestShortRhythm|TestAllPaths|TestUpdatePlanKeeps|TestCreateTaskLinks|TestPlanTaskCreated|TestSchedulerSkip|TestNextTerm'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** derselbe Befehl und `go test -tags integration ./internal/modules/maintenance/ && go build ./...` – Expected: PASS, auch `TestServiceFlowIntegration` (Wochenrhythmus mit 5 Arbeitstagen ⇒ Folgeauftrag sofort).
- [ ] **Step 5: Commit** `git commit -m "Wartung: Auftrag entsteht erst mit dem Vorlauf, aus dem nächsten geplanten Termin"`

### Task 6: Wartungsseite – Jahresplan-Knöpfe, Wartungsregeln, Plan-Editor, Jahresplan-Ansicht

**Files:**
- Create: `internal/web/maintenance_visits.go`
- Modify: `internal/web/maintenance_web.go` (`maintenanceRoutes`, `MaintenancePageData`, `MaintenancePage` Fall `area`/Reiter `rules`, `maintYearRows`, `MaintenanceGenerate`, `MaintenancePlanPageData`, `MaintenancePlanPage`), `internal/web/perm_gate.go` (Regeln), `web/templates/maintenance.gohtml` (Sammelknopf, Tooltip „Fehlende Aufträge anlegen“, Legende „entfallen“, Block `mt-lead-rules`), `web/templates/maintenance_plan.gohtml:13,59,245` (Knopf, Zahl, Feld Vorlauf)
- Unverändert: `web/templates/widgets/work_board.gohtml` (`wa-rules`) und die Golddateien `internal/web/testdata/work_render_*`
- Test: `internal/web/maintenance_visits_test.go`, `internal/web/maintenance_visits_integration_test.go`

**Interfaces:**
- Consumes: `Service.GenerateYearPlan`, `Repository.PlanVisits/VisitsInRange/DefaultLead` (Task 2–4).
- Routen (`maintenanceRoutes`) und Gate:
  - `POST /maintenance/plans/{id}/yearplan` → `MaintenanceYearPlanWeb` – Antwort htmx-Fragment „✓ 12 Termine geplant“ bzw. „Jahresplan ist aktuell“; Gate `gr(wPost, "maintenance.edit", "maintenance_plan", false, `^/maintenance/plans`+gID+`/yearplan$`)`.
  - `POST /maintenance/yearplan` → `MaintenanceYearPlanAllWeb` – je aktivem Plan `h.hasPermRec(r, "maintenance.edit", "maintenance_plan", id)`, sonst übersprungen und gezählt; Antwort „Jahresplan für N Pläne erzeugt (M Termine) · K Pläne anderer Abteilungen übersprungen“; Gate `gr(wPost, "maintenance.edit", "", false, `^/maintenance/yearplan$`)`.
  - `POST /maintenance/rules` → `MaintenanceRulesWeb` (Formular `lead_workdays`, 0–60, sonst Rückmeldung „Der Vorlauf muss zwischen 0 und 60 Arbeitstagen liegen“; zurück auf `/maintenance?tab=rules&notice=…`); nur mit `maintenance.admin` überall (`func (h *Handler) canMaintRules(r *http.Request) bool` über `h.rbac.Scope`), sonst 403; Gate `gr(wPost, "maintenance.admin", "", false, `^/maintenance/rules$`)`; schreibt über `h.setUpdateSetting(ctx, appsettings.KeyMaintenanceLeadWorkdays, …)` wie `WorkSettingsWeb`.
  - Alle drei: `h.MaintLocked()` → 503 „Wartung läuft – bitte später erneut versuchen“ (`const errMaintLater` in `maintenance_visits.go`).
- Produces:
  - `MaintenancePageData` zusätzlich `LeadWorkdays int`, `CanMaintRules, CanYearPlan bool`; Block `{{define "mt-lead-rules"}}` in `maintenance.gohtml`, eingebunden **nach** `wa-pane`, nur bei `eq .Area.Tab "rules"`: Karte „Wartungsregeln“ – „Vorlauf für den Durchführungsauftrag: [5] Arbeitstage“ (Mo–Fr ohne Feiertage und Betriebsruhe), Hinweis „Gilt für alle Pläne ohne eigenen Vorlauf. Der Auftrag entsteht so viele Arbeitstage vor dem Termin.“, Speichern nur mit `.CanMaintRules`, sonst Text.
  - Reiter „Pläne“: Knopf „Jahresplan für alle aktiven Pläne erzeugen“ (`hx-post="/maintenance/yearplan"`, nur mit `.CanYearPlan` = `hasPerm("maintenance.edit")`); Tooltip von „Fehlende Aufträge anlegen“: „Legt Aufträge an, deren Vorlauf erreicht ist – normalerweise unnötig, PDH macht das selbst“; Karte je Plan zeigt „· N geplant“, wenn `VisitCount > 0`.
  - `maintYearRows`: für Pläne mit `YearPlanAt != nil` Marken aus `PlanVisits` statt `PlannedDates` (geplant am `day`, Zustand `dropped` → neue Marke `dropped` „entfallen“, `off_plan` → Titel „… (weicht vom Plan ab)“, `ordered` nicht, der Auftrag steht schon da); ohne Jahresplan wie bisher. `yearMark.Kind` um `dropped`; Legende ⊘ entfallen (CSS `.mk.dropped` gestrichelter Rand `var(--muted)`).
  - `MaintenancePlanPageData` zusätzlich `CanEdit bool`, `VisitCount int`, `YearPlanDone bool`, `DefaultLead int`. Plan-Editor: Knopf „Jahresplan erzeugen“ bzw. „Jahresplan aktualisieren“ (`hx-post` auf `/maintenance/plans/{id}/yearplan`, nur aktiver, gespeicherter Plan mit `.CanEdit`), daneben „N geplante Termine“; Feld „Vorlauf (Arbeitstage)“ (`id="mp-lead"`, `min 0 max 60`, leer erlaubt, Platzhalter „Vorgabe: {{.DefaultLead}}“, `title` „So viele Arbeitstage vor dem Termin entsteht der Auftrag – leer = Vorgabe aus den Wartungsregeln“); Speichern sendet `lead_workdays: wert === '' ? null : +wert`, kein `lead_days` mehr.

- [ ] **Step 1: Failing tests.**
  - `maintenance_visits_test.go` (ohne Datenbank, `loadTestTemplates`): `TestMaintRulesRender` – `maintenance` mit `Area.Tab "rules"`, `CanMaintRules` → enthält „Wartungsregeln“, `name="lead_workdays"`, `action="/maintenance/rules"`; ohne Recht kein Formular, aber „5 Arbeitstage“. `TestPlanEditorLeadField` – `maintenance_plan` enthält `id="mp-lead"`, `max="60"`, „Vorgabe: 5“, „Jahresplan erzeugen“ (bzw. „aktualisieren“ mit `YearPlanDone`), nicht mehr „Vorlauf (Tage)“. `TestYearplanGateRules` – `matchGate` liefert für `POST /maintenance/plans/<uuid>/yearplan` `maintenance.edit`/`maintenance_plan`/`<uuid>`, für `/maintenance/yearplan` `maintenance.edit`, für `/maintenance/rules` `maintenance.admin`. `TestMaintRulesLocked` – `hasPermHook` wahr, Sperre → 503.
  - `maintenance_visits_integration_test.go` (`newViewFx`; Rolle A mit `maintenance.edit` nur in Abteilung A, Rolle Admin mit `maintenance.admin` nur in A):
    - `TestYearPlanWebDept`: Plan an Anlage A und B; Einzelknopf auf B → 403; Sammelknopf → A erzeugt, Antwort nennt „1 Plan anderer Abteilungen übersprungen“; B ohne Termine.
    - `TestMaintRulesWeb` (Review Focus 4): Admin nur in A → 403; Admin überall → gespeichert; danach Plan ohne eigenen Vorlauf: `OrderDay` rechnet mit 10, Plan mit `lead_workdays 3` weiter mit 3; vorhandener offener Auftrag behält `upcoming_from`. Einstellung im `t.Cleanup` zurücksetzen.
    - `TestYearRowsFromVisits`: Plan mit Jahresplan, ein Termin verschoben, einer `dropped` → Zeile hat die Marke am neuen Tag und eine `dropped`-Marke; Plan ohne Jahresplan rechnet wie bisher.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestMaintRules|TestPlanEditorLead|TestYearplanGate' && go test -tags integration ./internal/web -run 'TestYearPlanWeb|TestMaintRulesWeb|TestYearRowsFromVisits'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go test ./internal/web -run 'Work|Maint|Gate'` – Expected: PASS, Golddateien `work_render_*` unverändert.
- [ ] **Step 5: Commit** `git commit -m "Wartung: Jahresplan erzeugen, Wartungsregeln mit Vorlauf in Arbeitstagen"`

### Task 7: Nach dem Abschluss und beim automatischen Auftrag – Anzeige und Nachricht

**Files:**
- Modify: `internal/web/maintenance_web.go:636-641` (`MaintenanceTaskDetail`), `web/templates/maintenance_detail.gohtml:67`, `internal/web/completion.go:414-417,597-603` (`maintNextDue` → `maintNextTerm`), `internal/web/maintenance_protocol.go:128-172,625-628` (`maintDoneMessage`, `RegisterMaintenanceProtocol`), `internal/web/planner_maint.go:265` (Text aus gemeinsamer Funktion)
- Create: `internal/web/maintenance_notify.go`
- Test: `internal/web/maintenance_notify_test.go`, `internal/web/maintenance_notify_integration_test.go`

**Interfaces:**
- Consumes: `maintenance.NextTerm`, `Service.NextTermForTask`, `maintenance.OnPlanTaskCreated` (Task 5).
- Produces:
  - `func (h *Handler) maintNextTerm(ctx context.Context, taskID string) *maintenance.NextTerm` (nil ohne Wartungsdienst).
  - `func nextTermText(lang string, n *maintenance.NextTerm) string` – „Nächster Termin: 12.03.2027“ und, ohne Auftrag, „ – Auftrag entsteht am 05.03.2027“ (`tr`).
  - Meldung im Assistenten: `msg += " " + nextTermText(lang, next)`. Mail/Chat zum Abschluss: `maintDoneMessage(p, taskID, pdfURL string, next *maintenance.NextTerm)` – Zeile „📅 Nächster Termin: 12.03.2027 (Auftrag entsteht am 05.03.2027)“, mit Auftrag ohne Klammer.
  - Detailseite: `MaintenanceTaskDetailData` zusätzlich `NextDay, NextOrderOn string`; Anzeige „nächster Termin <Link> 12.03.2027“ mit Auftrag wie bisher, sonst „nächster Termin 12.03.2027 (geplant) · Auftrag entsteht am 05.03.2027“.
  - `func maintAssignedText(title string, due time.Time, taskID string) string` = heutiger Text aus `planner_maint.go:265` („🔧 Dir wurde die Wartung **%s** am %s zugewiesen. [Öffnen](/maintenance/tasks/%s)“); der Planer nutzt ihn.
  - `func (h *Handler) planTaskRecipients(ctx context.Context, taskID, actorID string) []string` – zugewiesene Person und Mitglieder der zugewiesenen Gruppe, aktiv, kein Bot, kein Systembenutzer, ohne `actorID`.
  - `func (h *Handler) notifyPlanTaskCreated(ctx context.Context, taskID, actorID string)` – Titel/Fälligkeit lesen, `h.systemNotify(ctx, planTaskRecipients(…), maintAssignedText(…))`; ohne Empfänger nichts. `RegisterMaintenanceProtocol` meldet ihn zusätzlich mit `maintenance.OnPlanTaskCreated` an. Der Hinweis beim Anlegen (`record_created.go`, `fromPlan`) bleibt ausgenommen – keine doppelte Nachricht.

- [ ] **Step 1: Failing tests.**
  - `maintenance_notify_test.go`: `TestNextTermText` – mit `TaskID` „Nächster Termin: 12.03.2027“; ohne „Nächster Termin: 12.03.2027 – Auftrag entsteht am 05.03.2027“; nil → „“. `TestMaintDoneMessageNext` – Zeile mit und ohne Klammer. `TestMaintAssignedText` – genau der bisherige Text.
  - `maintenance_notify_integration_test.go` (`newViewFx`): `TestPlanTaskRecipients` – Plan mit Person P und Gruppe G (Mitglieder P, Q, inaktiver R): Empfänger {P, Q}; mit `actorID = Q` → {P}; ohne Person und Gruppe → leer. `TestNotifyPlanTaskCreatedOnce` – automatischer Auftrag → genau eine Direktnachricht von PDH-System an P mit dem Text aus `maintAssignedText`; die Warteschlange `record_change_queue` (`processChangeQueue`) erzeugt für denselben Auftrag keine weitere. `TestTaskDetailNextTerm` – erledigter Auftrag ohne Folgeauftrag zeigt „Auftrag entsteht am“.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestNextTermText|TestMaintDoneMessageNext|TestMaintAssignedText' && go test -tags integration ./internal/web -run 'TestPlanTaskRecipients|TestNotifyPlanTaskCreatedOnce|TestTaskDetailNextTerm'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go test -tags integration ./internal/web -run 'BoardMaintenanceFlow|Completion'` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Wartung: nächster geplanter Termin nach dem Abschluss; Nachricht beim automatischen Auftrag"`

### Task 8: Kalender – Ebene „Wartungstermine“, Ziehen, Besetzen, Leitstand, „Mein Kalender“

**Files:**
- Modify: `internal/web/calendar_board.go` (`calKinds`, `calChip`, `buildCalendar`), `internal/web/maintenance_visits.go` (Handler für Ziehen/Besetzen/Zurück), `internal/web/maintenance_web.go` (`maintenanceRoutes`), `internal/web/perm_gate.go`, `internal/web/leitstand_boards.go:366-420` (`boardAllowedIDs`: Art `maintenance_planned`), `internal/web/dashboard_widgets_calendar.go:88-125` (`loadMyCalendar`), `web/templates/widgets/calendar_board.gohtml` (Chip, Fenster, `pdhCalInit`), `web/templates/calendar.gohtml` (CSS)
- Test: `internal/web/calendar_visits_test.go`, `internal/web/calendar_visits_integration_test.go`

**Interfaces:**
- Consumes: `Repository.VisitsInRange`, `Service.MoveVisit/StaffVisit/ResetVisit/GetVisit` (Task 4), `ensureTaskForPlan` läuft nach Ziehen/Besetzen über `Service.EnsurePlanTask` (Vorlauf vom neuen Tag).
- `calKinds`: nach „Wartungen“ `{"maintenance_planned", "Wartungstermine", "ti-calendar-repeat", "#C99A3C"}`.
- `calChip` zusätzlich `Planned, OffPlan, Dropped bool`, `AssigneeID, GroupID, PlanURL string`; `Drag = "visit"` für ziehbare geplante Termine.
- `buildCalendar`, Ebene `maintenance_planned` (nur mit `maintenance.view` bzw. als Gast, wenn der Leitstand `maintenance` zeigt):
  - Termine `planned` und `dropped` mit `day` im Zeitraum, Abteilung über `scopeSQL(scope, "maintenance_plan", "mp", …)`; Gäste zusätzlich `boardIDs["maintenance_planned"]`.
  - Person: `assigned_to`, sonst `responsible_to` des Plans, sonst „Ohne Person“; Gruppe im Tooltip wie bei Aufträgen.
  - Text `geplant · <Plan>`; Tooltip „Wartungstermin (geplant): <Plan> · <Anlage> · Gruppe … · weicht vom Plan ab“; `dropped` → „entfallen · <Plan>“, nicht ziehbar. Geplante Termine zählen bei „fällige(r) Auftrag/Aufträge“ der Überschneidungs- und Schicht-Hinweise mit (Personalplanung), `d.Counts["maintenance_planned"]`.
  - Ziehbar (`Drag "visit"`), wenn `me.ID != ""` und `hasPermRec(maintenance.edit, maintenance_plan, planID)` – je Plan einmal geprüft (Merker-Map).
  - Gemerkte Auswahl (Review Focus 1): enthält die gespeicherte Auswahl `show=maintenance`, aber keinen Eintrag `pv=2`, wird `maintenance_planned` ergänzt; beim Merken wird `pv=2` mitgeschrieben. Leitstand-Baustein: zeigt der Leitstand `maintenance`, kommt `maintenance_planned` in `q["show"]`.
- `boardAllowedIDs`: zusätzlich `UNION ALL SELECT 'maintenance_planned', w.id::text FROM (SELECT v.id, mp.infrastructure_id, v.assigned_to, v.assigned_group_id FROM maintenance_visits v JOIN maintenance_plans mp ON mp.id = v.plan_id WHERE v.state <> 'ordered') w WHERE ` + cond("w", false)`; `out` bekommt den Schlüssel.
- Routen (JSON, höchstens 4 KiB) und Gate:
  - `POST /maintenance/visits/{id}/move` `{to_date}` → `MaintenanceVisitMoveWeb`; `POST /maintenance/visits/{id}/staff` `{assigned_to, group_id}` (leer = niemand) → `MaintenanceVisitStaffWeb`; `POST /maintenance/visits/{id}/reset` → `MaintenanceVisitResetWeb`. Antwort `{success:true}` bzw. `{error}` mit 400/403/404/503.
  - Gate `gr(wPost, "maintenance.edit", "", false, `^/maintenance/visits`+gID+`/(?:move|staff|reset)$`)`; im Handler gemeinsam `func (h *Handler) visitGuard(w http.ResponseWriter, r *http.Request) (*maintenance.Visit, bool)`: Kennung prüfen, `MaintLocked` → 503, Termin laden (404), `hasPermRec(r, "maintenance.edit", "maintenance_plan", v.PlanID)` sonst 403 „Keine Berechtigung, diesen Termin zu ändern.“ – **kein** `recordInvolved`. Verlauf am Plan (`h.addHistory(ctx, "maintenance_plan", planID, "update", …, "Wartungstermin im Kalender verschoben/besetzt/zurückgesetzt", uid)`).
- Oberfläche: Chip `cc it plan` gestrichelt (`border:1px dashed var(--kc); border-left-width:3px; background:transparent`), `off` mit `ti-alert-triangle`, `drop` durchgestrichen und `var(--muted)`. `pdhCalInit`: `canDrop` erlaubt `visit` auf jede Zelle; `onMove` für `visit` → `pdhGridPost('/maintenance/visits/' + id + '/move', {to_date})`. Klick auf einen ziehbaren Termin öffnet `<dialog id="cal-visit-dlg">` statt der Plan-Seite: Person (`.Users`), Gruppe (`.Groups`), vorbelegt aus `data-assignee`/`data-group`, Knöpfe „Speichern“, „Zurück zum Plan“, Link „Plan öffnen“; Felder nur über `value`/`textContent` füllen; am Handy volle Breite (`width:min(420px, calc(100vw - 32px))`). Nicht ziehbare Termine verlinken auf den Plan. Legende: „gestrichelt = geplanter Wartungstermin (noch kein Auftrag)“.
- `loadMyCalendar`: eigene geplante Termine (`assigned_to = uid`, `state = 'planned'`, Tag bis Ende der 7 Tage, nicht vor heute), Kind-Farbe von `maintenance_planned`, Titel „geplant · <Plan>“, Link Plan, nur mit `maintenance.view`.

- [ ] **Step 1: Failing tests.**
  - `calendar_visits_test.go` (ohne Datenbank): `TestCalKindsPlanned` – Schlüssel `maintenance_planned` direkt nach `maintenance`, gleiche Farbe. `TestCalendarRenderVisit` – `calendar-board` mit einem ziehbaren, einem `off_plan` und einem `dropped` Chip enthält `data-drag="visit"`, Klasse `plan`, „weicht vom Plan ab“, „entfallen“, `id="cal-visit-dlg"`, `/maintenance/visits/`. `TestVisitGateRules` – `matchGate` für die drei Routen liefert `maintenance.edit`. `TestVisitRoutesLocked` – Sperre → 503.
  - `calendar_visits_integration_test.go` (`newViewFx`; Rolle „edit A“ mit `maintenance.view` + `maintenance.edit` in A, Rolle „view“ nur `maintenance.view` überall; Plan PA an Anlage A mit Jahresplan, Plan PB an B):
    - `TestCalendarShowsVisits`: Ebene zeigt PA-Termine bei der Plan-Person, ohne Person beim Verantwortlichen; „edit A“ sieht nur PA (Abteilung), Chips ziehbar; „view“ sieht beide, nicht ziehbar; beauftragter Termin fehlt in der Ebene, der Auftrag steht unter „Wartungen“; mit `withLocal(t, Europe/Berlin)` steht ein Termin vom So 28.03.2027 in der Zelle `Date == "2027-03-28"` (Review Focus 2).
    - `TestVisitMoveWeb`: „edit A“ zieht PA-Termin → 200, `date_manual`; PB-Termin → 403, unverändert; in die Vergangenheit → 400 „nicht in die Vergangenheit“; als Person, die nur am Termin **beteiligt** ist (zugewiesen, ohne Recht) → 403; Ziehen in den Vorlauf (morgen) → Auftrag entsteht sofort.
    - `TestVisitStaffAndReset`: Besetzen setzt Person/Gruppe und `staff_manual`; „Zurück zum Plan“ stellt Tag und Besetzung aus der Rechnung her.
    - `TestCalendarSavedPrefGetsPlanned` (Review Focus 1): gespeicherte Auswahl `show=maintenance&show=ticket` → `d.Show["maintenance_planned"]`; mit `pv=2` und ohne `maintenance_planned` → aus.
    - `TestCalendarGuestVisits`: Leitstand mit Abteilung A und Art `maintenance` → Gast sieht PA-Termine, nicht PB; Leitstand ohne `maintenance` → keine.
    - `TestMyCalendarVisits`: Widget zeigt eigenen geplanten Termin mit „geplant“, fremden nicht.
    - `TestVisitsNeverCountAsOpen`: mit 12 geplanten Terminen und keinem offenen Auftrag: `h.workOpenCards(r, workKinds["maintenance"])` leer, Übernahme-Liste (`unassignedKinds` Wartung) ohne Eintrag, `DashStats.MaintenanceDue` 0, Leitstand-Daten (`GlobalDashboardData`) ohne Wartung, App-Liste (`app_work.go`) ohne Wartung.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestCalKindsPlanned|TestCalendarRenderVisit|TestVisitGate|TestVisitRoutesLocked' && go test -tags integration ./internal/web -run 'TestCalendarShowsVisits|TestVisitMoveWeb|TestVisitStaffAndReset|TestCalendarSavedPref|TestCalendarGuestVisits|TestMyCalendarVisits|TestVisitsNeverCountAsOpen'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go test ./internal/web -run 'Calendar|Widget|Board'` – Expected: PASS (auch `calendar_board_guest_test.go`).
- [ ] **Step 5: Commit** `git commit -m "Kalender: Ebene „Wartungstermine“ – verschieben, besetzen, zurück zum Plan"`

### Task 9: Planer-Reiter „Wartung“, Planungsfolie, Gantt

**Files:**
- Modify: `internal/web/planner_maint.go` (`maintItem`, `maintTab`, neuer Handler), `internal/web/handler.go` (Route neben `/planner/maintenance/{id}`; `GanttItem`; `buildDashboardGantt`), `web/templates/planner.gohtml:402-452` (Chip, Formularziel), `internal/web/terminal_plan.go:294-334` (`planMaintRows`), `internal/web/global_dashboard.go:414-420` (`boardShows` für `maintenance_planned`), `web/templates/global_dashboard.gohtml:1324`, `web/templates/dashboard.gohtml:54` (geplant gestrichelt, nicht ziehbar)
- Test: `internal/web/planner_visits_integration_test.go`, `internal/web/terminal_plan_integration_test.go` (ein Test dazu), `internal/web/gantt_visits_test.go`

**Interfaces:**
- Consumes: `Repository.VisitsInRange`, `Service.MoveVisit/StaffVisit` (Task 4), `visitGuard`-Logik (Task 8, für Formulare als `func (h *Handler) visitAllowed(r *http.Request, v *maintenance.Visit) bool`).
- Planer: `maintItem` zusätzlich `Planned, OffPlan bool`; geplante Termine (`state = 'planned'`, Abteilung über `scopeSQL(…, "maintenance_plan", "mp", …)`) als `Kind "planned"`, `URL` Plan-Seite, Person wie im Kalender, dieselben Warnungen (`check`), Minuten aus `estimated_min`, zählen in `Hours` mit; vergangene geplante Termine nicht unter „Überfällig“ (sie werden im nächsten Lauf beauftragt). Route `POST /planner/maintenance-visits/{id}` → `PlannerVisitSaveWeb` (Formular wie `PlannerMaintSaveWeb`: `due_date`, `assigned_to` leer = unverändert, „-“ = niemand; Rechte wie Task 8, Sperre → `plannerBack` mit Fehler; Rückmeldung „„<Plan>“ geplant am TT.MM.JJJJ“). JS: Formularziel `'/planner/maintenance-visits/' + id`, wenn `data-kind === 'planned'`. Chip `mt-item plan` gestrichelt, Text „geplant“.
- Planungsfolie: `planMaintRows` liest zusätzlich geplante Termine im Zeitraum (Anlage des Plans in den erlaubten Abteilungen, gleiche Bedingung wie für Aufträge) und gibt sie als Gruppe `add(tag, "planned", "Geplant", "", "<Plan> · <Anlage>")` aus; keine „Überfällig“-Einträge für Termine.
- Gantt: `GanttItem` zusätzlich `IsPlanned bool` (`json:"is_planned"`); `buildDashboardGantt` hängt geplante Termine der nächsten 12 Monate an: `RefType "maintenance_planned"`, `ID` Termin, Start = Ende = Tag, `DetailURL "/maintenance/plans/<plan>"` (damit `scopeGantt` die Abteilung des Plans prüft), `DueDateEndpoint ""`, `Color "#c99a3c"`. Leitstand: `boardShows` prüft für `maintenance_planned` `board.HasType("maintenance")` und `boardIDs["maintenance_planned"]`. JS: `planned: !!t.is_planned` → Balken gestrichelt, nicht ziehbar, Text „geplant“.

- [ ] **Step 1: Failing tests.**
  - `planner_visits_integration_test.go` (`newViewFx`): `TestPlannerTabVisits` – Monat mit einem geplanten Termin von Person P an einem Tag, an dem P abwesend ist → Eintrag `Planned`, Warnung „ist abwesend“; ohne Schicht (P hat sonst Schichten) → „hat keine Schicht“; Abteilung B unsichtbar für Rolle A. `TestPlannerVisitSave` – Verschieben und Zuweisen ändern den Termin (`date_manual`, `staff_manual`), fremde Abteilung → 403, Termin unverändert.
  - `terminal_plan_integration_test.go`: `TestTerminalPlanMaintenanceVisits` (Muster `TestTerminalPlanMaintenance`) – Folie zeigt „Geplant“ mit „<Plan> · <Anlage>“ am Termintag; Termin an Anlage einer anderen Abteilung fehlt; beauftragter Termin erscheint nur als „Fällig“.
  - `gantt_visits_test.go` (integration): `TestGanttVisits` – `buildDashboardGantt` enthält den Termin mit `IsPlanned`, `DueDateEndpoint == ""`; `scopeGantt` mit Rolle A entfernt Termin an B; Leitstand ohne Art `maintenance` zeigt ihn nicht.
- [ ] **Step 2: Run** `go test -tags integration ./internal/web -run 'TestPlannerTabVisits|TestPlannerVisitSave|TestTerminalPlanMaintenanceVisits|TestGanttVisits'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** derselbe Befehl und `go test -tags integration ./internal/web -run 'TerminalPlan|Planner|Gantt|Global'` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Planer, Infobildschirm und Gantt zeigen geplante Wartungstermine"`

### Task 10: Outlook – geplante Termine, kein doppelter Eintrag

**Files:**
- Modify: `internal/web/microsoft_calendar.go` (`microsoftCalendarSources`, `syncPDHEventsToMicrosoft`, `removeStaleMicrosoftEvents`)
- Test: `internal/web/microsoft_calendar_visits_test.go`, `internal/web/microsoft_calendar_visits_integration_test.go`

**Interfaces:**
- Consumes: `maintenance_visits` (Task 2), Migration-Regel `maintenance_planned` (Task 2).
- Produces:
  - In `microsoftCalendarSources` bei `preferences.Maintenance`: geplante Termine mit `assigned_to = $1`, `state = 'planned'`, `day BETWEEN CURRENT_DATE - 30 AND CURRENT_DATE + 365` als ganztägiger Termin `Type "maintenance_planned"`, `ID` Termin, Titel „PDH · Wartung (geplant) · <Plan>“ (`func plannedCalendarSource(id, plan string, day time.Time) microsoftCalendarSource`). Termine nur mit Gruppe gehen nicht nach Outlook.
  - `func microsoftSourceSelected(sourceType string, p microsoftCalendarPreferences) bool` – aus der Bedingung in `removeStaleMicrosoftEvents`, `maintenance_planned` zählt zu `Maintenance`.
  - `func (h *Handler) adoptPlannedCalendarLink(ctx context.Context, userID, kind, taskID string) error` – vor dem Nachschlagen der Verknüpfung eines Wartungsauftrags: hat der Auftrag einen Termin (`maintenance_visits.task_id`) mit Verknüpfung `maintenance_planned` und gibt es noch keine für den Auftrag, wird die Verknüpfung auf `('maintenance', taskID)` umgeschrieben (ein `UPDATE … FROM maintenance_visits … AND NOT EXISTS (…)`); der Outlook-Termin wird dann per PATCH zum Auftrag statt gelöscht und neu angelegt.

- [ ] **Step 1: Failing tests.**
  - `microsoft_calendar_visits_test.go`: `TestMicrosoftSourceSelected` – `maintenance_planned` nur mit `Maintenance`; die alten Arten wie bisher. `TestPlannedCalendarSource` – ganztägig, Titel „PDH · Wartung (geplant) · Filter wechseln“, Ende = Tag + 1.
  - `microsoft_calendar_visits_integration_test.go` (`newViewFx`): `TestOutlookSourcesVisits` – Person P mit Termin (besetzt mit P) und Termin nur mit Gruppe: Quellen enthalten genau den ersten als `maintenance_planned`; nach dem Beauftragen enthalten sie den Auftrag (`maintenance`) und nicht mehr den Termin. `TestAdoptPlannedLink` – Verknüpfung `maintenance_planned` mit Ereignis `ev1`; nach dem Beauftragen und `adoptPlannedCalendarLink` gibt es genau eine Verknüpfung `('maintenance', task) → ev1` und keine für den Termin; besteht schon eine für den Auftrag, bleibt alles unverändert (kein Verlust, kein Doppel).
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestMicrosoftSourceSelected|TestPlannedCalendarSource' && go test -tags integration ./internal/web -run 'TestOutlookSourcesVisits|TestAdoptPlannedLink'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go test ./internal/web -run Microsoft` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Outlook: geplante Wartungstermine, beim Beauftragen ohne doppelten Eintrag"`

### Task 11: Handbuch, Skizze, Änderungsprotokoll, Version, Übersetzungen, offene Punkte

**Files:**
- Modify: `web/templates/help.gohtml` – Kapitel `maintenance` (Zeile 588: Aussage „genau einen offenen Auftrag … sofort nach jedem Abschluss“ berichtigen: der Auftrag entsteht den Vorlauf in Arbeitstagen vor dem Termin; „So geht's: Jahresplan erzeugen“ (Knopf am Plan, Sammelknopf, Verlängern, Zahl der Termine), „So geht's: Vorlauf einstellen“ (Wartungsregeln, Vorgabe 5, je Plan eigener Wert, Arbeitstage ohne Feiertage und Betriebsruhe, Termin auf freiem Tag), Abschnitt „Was mit geplanten Terminen passiert“ (Neu rechnen, „weicht vom Plan ab“, „entfallen“ nach verspätetem Abschluss, Plan ruhen/löschen, Rhythmus bleibt am gerechneten Tag, ein von Hand geänderter Auftrag wird bei Planänderung nicht überschrieben); Schritt „Fertig“ (Zeile 607: „legt den Folgeauftrag an“ → „zeigt den nächsten Termin und wann der Auftrag entsteht“); Schritt „Wann“ im Plan-Editor (Zeile 625: „Vorlauf (Tage)“ → „Vorlauf (Arbeitstage)“); Zeile 627 „Der erste Auftrag ist sofort da“ berichtigen; Jahresplan-Ansicht mit ⊘ entfallen. Kapitel `calendar`: Ebene „Wartungstermine“ (gestrichelt, „geplant“), „So geht's: Termin verschieben und besetzen“ (Ziehen, Fenster, „Zurück zum Plan“, Rechte: nur mit „Wartung bearbeiten“ an der Anlage, nicht als Beteiligte), „weicht vom Plan ab“, Leitstand-Baustein. Kapitel `planner`: geplante Termine im Reiter Wartung. Outlook-Abschnitt (Zeile 1631): geplante Termine, die dir zugewiesen sind, als „PDH · Wartung (geplant) · …“, werden beim Beauftragen zum Auftrag (kein doppelter Termin), Gruppen nicht.
- Modify: `web/templates/widgets/help_figures.gohtml` (neue Skizze `hb-fig-jahresplan`: Kalenderzeile mit gestricheltem „geplant“-Termin, einem Auftrag, Fenster „Person/Gruppe · Speichern · Zurück zum Plan“; Farben über Variablen; im Kapitel `calendar` eingebunden), `CHANGELOG.md` (`## [0.82.46] – 2026-11-23`; `### Neu`: Jahresplan erzeugen mit geplanten Terminen für 12 Monate; Ebene „Wartungstermine“ im Kalender zum Verschieben und Besetzen; Wartungsregeln mit Vorlauf in Arbeitstagen; geplante Termine im Planer, auf dem Infobildschirm, im Gantt und in Outlook. `### Geändert`: Der Folgeauftrag einer Wartung entsteht erst mit dem Vorlauf, nicht mehr sofort nach dem Abschluss; der Vorlauf zählt Arbeitstage statt Kalendertage, alte Werte wurden umgerechnet; nach dem Abschluss steht der nächste Termin mit „Auftrag entsteht am …“), `VERSION` (0.82.46), `web/i18n/{en,ro,tr,mk}.json`, `AGENTS.md` (neuer Punkt 25 „0.82.46 Wartung: Jahresplan im Kalender, Auftrag mit Vorlauf“ mit Entwurf, Plan, Branch, Migration 212, Stand; Kopf „Stand“ und Datum; MEMORY nicht anfassen)

- [ ] **Step 1:** Handbuch, Skizze, Änderungsprotokoll, Version, Übersetzungen, AGENTS.md schreiben (alles per Edit, CRLF).
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestHelp|TestI18nCatalogsComplete' && go test . -run TestChangelogMatchesVersion` – Expected: PASS.
- [ ] **Step 3: Volle Prüfung** `go vet ./... && go test ./... && go test -tags integration ./internal/core/workdays/ ./internal/modules/maintenance/ && go test -tags integration ./internal/web -run 'Visit|YearPlan|YearRows|MaintRules|PlanTask|NextTerm|Calendar|Planner|TerminalPlan|Gantt|Outlook|AdoptPlanned|BoardMaintenanceFlow|Work'` – Expected: PASS (bekannte Wackler der geteilten Labor-Datenbank einzeln wiederholen und im Ledger nennen).
- [ ] **Step 4: Prüfung vor dem Merge** nach AGENTS.md: `git fetch origin main`, auf `origin/main` bringen, Dopplungen suchen (`git diff origin/main...HEAD`; Migration 212 und Version 0.82.46 noch frei? `freeDays`/`germanHolidays` auf `main` inzwischen geändert? neue Leser von `lead_days`?), Tests erneut.
- [ ] **Step 5: Commit** `git commit -m "Wartung Jahresplan: Handbuch, Änderungsprotokoll (0.82.46)"`

Danach, nur mit ausdrücklicher Freigabe des Anwenders: Push und Merge nach `main`, Verbund-Update, Browserprüfung am Testverbund mit dem Prüfkonto (Wartungsseite mit Jahresplan-Knöpfen und Wartungsregeln, Kalender mit Ziehen und Besetzen-Fenster, Planer-Reiter, Leitstand-Kalender und Gantt; Fiori/Fluent, hell/dunkel, 320–1366 px; Testdaten entfernen, Konto wieder sperren).
