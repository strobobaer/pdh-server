# Schwellwerte und Alarme an Messstellen (0.80.15) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Je Messstelle Schwellwerte (über/unter, Zeitraum, Rückschaltzeit 15 min); ein eigener Alarm-Wächter legt bei Überschreitung ein Ticket mit Tag an, benachrichtigt den Verantwortlichen und hängt bei erneuter Überschreitung nur einen Eintrag an ein noch offenes Ticket; Alarm-Tickets lassen sich nur mit ausgefüllter „Ursache“ erledigen/schließen.

**Architecture:** Neue Dateien im Paket `internal/modules/energie`: Schwellwerte speichern (`thresholds.go`), Zeitraum auswerten (`evaluate.go`, rein), Wächter mit Zustand je Schwellwert (`alarms.go`). Tickets und Nachrichten kommen über eine kleine Schnittstelle aus `internal/web` (`energy_alarms.go`), damit `energie` nicht von `tickets`/Chat abhängt. Die Ursachen-Pflicht sitzt zentral im Ticket-Modul (`tickets.ErrRootCauseRequired`).

**Tech Stack:** Go 1.27, chi, html/template, htmx, Postgres (pgx v5), zerolog.

**Spec:** `docs/superpowers/specs/2026-11-15-energie-schwellwerte-design.md`

## Global Constraints

- Version **0.80.15**: `VERSION` und neuer oberster Eintrag `## [0.80.15] – 2026-11-15` in `CHANGELOG.md` (nur in Task 7). Migration **163** (`migrations/163_energy_thresholds.up.sql`).
- Bedingung `above` („über“) / `below` („unter“); Zeitraum `window_minutes` 1–1440; Rückschaltzeit `reset_minutes` 1–1440, Vorgabe **15**; Priorität Vorgabe `medium`; Grenzwert in der Einheit der Messstelle.
- Auswertung: `instant` = Mittelwert, `counter` = Wert(Ende) − Wert(Anfang), `consumption` = Summe; nur wenn **jede Minute** des Zeitraums einen Wert hat; `counter` mit negativer Menge → nicht bewerten.
- Alarm endet erst nach `reset_minutes` Minuten am Stück ohne erfüllte Bedingung. Während eines Alarms kein weiteres Ticket. Neue Überschreitung bei noch offenem Ticket (Status `open`, `in_progress`, `pending`, nicht archiviert) → Kommentar am Ticket + Nachricht, kein neues Ticket.
- Ursachen-Pflicht nur für Alarm-Tickets (`tickets.requires_root_cause = true`), auf allen Wegen; Meldung exakt: „Bitte beschreiben, warum der Alarm kam (Feld Ursache).“
- Verantwortlich: Benutzer oder Gruppe je Schwellwert, sonst Verantwortlicher der Anlage (`infrastructure_responsible`), sonst Nachricht an Inhaber von `energy.admin`.
- Recht `energy.thresholds` „Schwellwerte pflegen“, Kategorie „Daten & Schnittstellen“, an Rollen mit `energy.edit` (Abteilung übernehmen, Muster Migration 161 Z. 136–148). Ansehen `energy.view`, Löschen `energy.admin`.
- Wächter nur am aktiven Server (Start neben `StartEnergySampler` in `cmd/server/main.go`), `Skip = h.MaintLocked`, nicht bei `h.Mirror().On`; Nachholen höchstens 24 h.
- Texte, Handbuch, Commits Deutsch; Farben nur über CSS-Variablen (Fiori/Fluent, hell/dunkel); keine Attributionszeilen; nie `git add -A`.
- Tests in Git Bash mit `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'`; DB-Tests ohne Variable `t.Skip`.

## Review Focus

1. **Wert pendelt um die Grenze** (drüber 3 min, drunter 5 min, drüber …): genau ein Ticket, keine Rückschaltung vor 15 min am Stück. → Task 4 `TestWatchFlappingOneTicket`.
2. **Server war 2 h aus / Rollentausch**: verpasste Minuten werden in Reihenfolge nachgeholt, kein doppeltes Ticket, nichts älter als 24 h. → Task 4 `TestWatchCatchUp`, `TestWatchCatchUpLimit24h`.
3. **Gerät offline (Lücken in `energy_values`)**: weder Alarm noch Rückschaltung aus unvollständigen Zeiträumen. → Task 3 `TestEvaluateGapNotRated`, Task 4 `TestWatchGapKeepsState`.
4. **Alarm-Ticket über einen Nebenweg schließen** (Archivieren, „Verwerfen“ im globalen Dashboard, JSON-API, Status-Dropdown) ohne Ursache: abgewiesen mit der Meldung. → Task 2 `TestRootCauseRequiredAllPaths`.
5. **Messstelle oder Schwellwert gelöscht/deaktiviert während eines Alarms**: Wächter läuft weiter, kein Absturz, kein Ticket mehr. → Task 4 `TestWatchThresholdRemoved`.

---

## Dateien

| Datei | Aufgabe |
|---|---|
| `migrations/163_energy_thresholds.up.sql` | Tabellen `energy_thresholds`, `energy_threshold_state`; `tickets.requires_root_cause`; Recht |
| `internal/modules/tickets/tickets.go`, `resolve.go` | Gruppe + Ursachen-Pflicht beim Anlegen; zentrale Prüfung |
| `internal/modules/energie/thresholds.go` (+ Test) | Schwellwerte lesen/speichern/löschen |
| `internal/modules/energie/evaluate.go` (+ Test) | Zeitraum auswerten (rein) |
| `internal/modules/energie/alarms.go` (+ Test) | Wächter, Zustand, Nachholen |
| `internal/web/energy_alarms.go` (+ Test) | Verdrahtung, Ticket/Nachricht, Verantwortliche |
| `internal/web/energy_thresholds.go`, `web/templates/widgets/energy_thresholds.gohtml` | Oberfläche je Messstelle, Spalte/Filter |
| `cmd/server/main.go` | Start des Wächters |

---

### Task 1: Migration 163

**Files:** Create `migrations/163_energy_thresholds.up.sql`; Test `internal/modules/energie/thresholds_migration_test.go`

**Interfaces – Produces (Schema):**
- `energy_thresholds(id uuid pk (Erzeugung wie Migration 161), meter_id uuid not null references energy_meters on delete cascade, name text not null, condition text not null check in ('above','below'), limit_value double precision not null, window_minutes int not null check 1..1440, reset_minutes int not null default 15 check 1..1440, tag text not null default '', priority text not null default 'medium' check in ('low','medium','high','critical'), responsible_user_id uuid null references users on delete set null, responsible_group_id uuid null references user_groups on delete set null, active bool not null default true, created_at, updated_at, check not (responsible_user_id is not null and responsible_group_id is not null))`.
- `energy_threshold_state(threshold_id uuid pk references energy_thresholds on delete cascade, state text not null default 'normal' check in ('normal','alarm'), alarm_since timestamptz, clear_since timestamptz, checked_until timestamptz, ticket_id uuid null references tickets on delete set null, last_value double precision)`.
- `ALTER TABLE tickets ADD COLUMN IF NOT EXISTS requires_root_cause boolean NOT NULL DEFAULT false`.
- Recht `('energy.thresholds','Schwellwerte pflegen','Daten & Schnittstellen')` + Vergabe aus `energy.edit` (Muster 161).

- [ ] **Step 1:** Test `TestThresholdMigration` (DB): Schwellwert anlegen geht; `condition='x'`, `window_minutes=0`, Benutzer **und** Gruppe gleichzeitig → Fehler; Löschen der Messstelle löscht Schwellwert und Zustand; `tickets.requires_root_cause` Vorgabe false; Rolle mit `energy.edit` nur in Abteilung D hat `energy.thresholds` nur in D.
- [ ] **Step 2:** `go test ./internal/modules/energie/ -run TestThresholdMigration -v` → FAIL.
- [ ] **Step 3:** Migration schreiben.
- [ ] **Step 4:** → PASS; `go test ./pkg/database/` → PASS.
- [ ] **Step 5:** Commit `Schwellwerte: Tabellen, Alarm-Kennzeichen an Tickets und Recht (Migration 163)`.

### Task 2: Tickets – Gruppe beim Anlegen und Ursachen-Pflicht

**Files:** Modify `internal/modules/tickets/tickets.go` (`CreateInput`, `insertTicket`, `UpdateStatus`, JSON-Handler `UpdateStatus` Z. 543), `resolve.go` (`Resolve`, `QuickResolve`); `internal/web/handler.go` (`TicketStatusWeb`, `TicketResolveWeb`, Archiv-Pfad Z. ~1544), `internal/web/global_dashboard_actions.go` (Z. ~235), `web/templates/ticket_detail.gohtml` (Hinweis); Tests `internal/modules/tickets/rootcause_test.go`, `internal/web/ticket_rootcause_test.go`

**Interfaces – Produces:**
```go
// tickets
type CreateInput struct { /* … vorhandene Felder … */ AssignedGroupID *string; RequiresRootCause bool }
var ErrRootCauseRequired = errors.New("Bitte beschreiben, warum der Alarm kam (Feld Ursache).")
// eine gemeinsame Prüfung, von UpdateStatus, Resolve, QuickResolve und JSON-Handler genutzt
// Ticket bekommt Feld RequiresRootCause bool (gelesen in GetByID/List)
```
- Prüfregel: Ziel `resolved` oder `closed` (bzw. Archivieren/QuickResolve) **und** `requires_root_cause` → Fehler, wenn weder die übergebene Ursache noch die gespeicherte `root_cause` nach `TrimSpace` Text enthält. Andere Ziele unverändert.
- Web: Fehler als Meldung auf der Ticketseite bzw. im htmx-Fragment, HTTP 400 (JSON: `{"error": …}` 400). Ticketseite: bei `RequiresRootCause` Hinweis „Alarm-Ticket – zum Erledigen oder Schließen bitte die Ursache beschreiben“ und Ursache-Feld als Pflichtfeld im Erledigen-Dialog.

- [ ] **Step 1:** Tests `TestCreateWithGroupAndFlag` (DB: `AssignedGroupID` und `requires_root_cause` landen in der Zeile), `TestRootCauseRequiredAllPaths` (DB: Alarm-Ticket ohne Ursache → `UpdateStatus(closed)`, `Resolve(…, rootCause="")` mit Maßnahme, `QuickResolve(…,"")` alle `ErrRootCauseRequired`; mit `Resolve(…, rootCause="Werkzeug stumpf")` geht es; danach `UpdateStatus(closed)` geht, weil gespeichert), `TestRootCauseNormalTicketUnchanged`, Web `TestTicketArchiveAlarmNeedsRootCause` und `TestDashboardDiscardAlarmNeedsRootCause` (Meldung im Antworttext, Ticket bleibt offen).
- [ ] **Step 2:** `go test ./internal/modules/tickets/ ./internal/web/ -run 'RootCause|CreateWithGroup' -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS; `go test ./internal/modules/tickets/` → PASS.
- [ ] **Step 5:** Commit `Tickets: Gruppe beim Anlegen, Alarm-Tickets nur mit Ursache erledigen oder schließen`.

### Task 3: Schwellwerte speichern und Zeitraum auswerten

**Files:** Create `internal/modules/energie/thresholds.go`, `thresholds_test.go`, `evaluate.go`, `evaluate_test.go`

**Interfaces – Produces:**
```go
const CondAbove, CondBelow = "above", "below"
type Threshold struct {
    ID, MeterID, Name, Condition, Tag, Priority string
    Limit float64; WindowMinutes, ResetMinutes int
    ResponsibleUserID, ResponsibleGroupID *string; Active bool
    // nur gelesen (Join): MeterName, MeterKey, Unit, Kind, InfrastructureID, State string
    AlarmSince, ClearSince *time.Time; TicketID *string; LastValue *float64
}
func (s *Store) Thresholds(ctx context.Context, meterID string) ([]Threshold, error)   // "" = alle aktiven
func (s *Store) Threshold(ctx context.Context, id string) (*Threshold, error)
func (s *Store) SaveThreshold(ctx context.Context, t *Threshold) error                // anlegen (ID leer) oder ändern; validiert
func (s *Store) DeleteThreshold(ctx context.Context, id string) error
type ThresholdCount struct{ Total, Alarms int }
func (s *Store) ThresholdCounts(ctx context.Context) (map[string]ThresholdCount, error) // je meter_id
// evaluate.go (rein, ohne DB)
type MinuteValue struct{ TS time.Time; Value float64 }
func WindowValue(kind string, vals []MinuteValue, end time.Time, window int) (v float64, ok bool)
func Breached(cond string, v, limit float64) bool
```
- Validierung (`SaveThreshold`, deutsche Fehlertexte): Name nicht leer (≤ 100), Bedingung gültig, Grenzwert endlich, Zeitraum/Rückschaltung 1–1440, Priorität gültig, höchstens Benutzer **oder** Gruppe, Tag ≤ 50 Zeichen.
- `WindowValue`: Minuten `(end−window, end]` (ts = Minutenende wie `energy_values`); `ok=false`, wenn eine Minute fehlt; `counter`: Wert(end) − Wert(end−window), dafür muss auch der Wert bei `end−window` vorliegen; negativ → `ok=false`.
- `Breached`: `above` → `v > limit`, `below` → `v < limit` (Gleichheit ist kein Alarm).

- [ ] **Step 1:** Tests `TestEvaluateInstantMean`, `TestEvaluateCounterDelta`, `TestEvaluateConsumptionSum`, `TestEvaluateGapNotRated`, `TestEvaluateCounterNegativeNotRated`, `TestBreachedEquality`; DB: `TestSaveThresholdValidation` (je Fehlerfall Meldung), `TestThresholdCounts`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Schwellwerte: speichern, prüfen und Zeitraum je Messart auswerten`.

### Task 4: Alarm-Wächter

**Files:** Create `internal/modules/energie/alarms.go`, `alarms_test.go`

**Interfaces:**
- Consumes: Task 3 (`Thresholds`, `WindowValue`, `Breached`, `MinuteValue`).
- Produces:
```go
type AlarmEvent struct {
    Threshold Threshold; Value float64; At time.Time
    TicketID string // gesetzt bei erneuter Überschreitung am offenen Ticket
}
type AlarmSink interface {
    OpenTicket(ctx context.Context, tx pgx.Tx, ev AlarmEvent) (ticketID string, after func(), err error) // after nach Commit (Nachricht)
    TicketOpen(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error)                           // open/in_progress/pending, nicht archiviert
    Repeat(ctx context.Context, tx pgx.Tx, ev AlarmEvent) (after func(), err error)                      // Eintrag + Nachricht
}
type AlarmWatch struct { Store *Store; Sink AlarmSink; Skip func() bool; Now func() time.Time }
func (w *AlarmWatch) Pass(ctx context.Context) error
func (w *AlarmWatch) Start(ctx context.Context) // jede Minute, kurz nach dem Sampler (samplerOffset + 10 s)
```
- Ablauf je aktivem Schwellwert in einer Transaktion mit `SELECT … FOR UPDATE` auf die Zustandszeile (anlegen, falls fehlt; erstes `checked_until` = letzte volle Minute, kein Rückblick). Jede Minute `t` von `checked_until+1min` bis zur letzten erfassten Minute in `energy_values` (nicht vor `jetzt−24h`):
  - nicht bewertbar → nur `checked_until = t`;
  - `normal` + erfüllt → `alarm`, `alarm_since=t`, `clear_since=nil`; ist `ticket_id` gesetzt und `TicketOpen` → `Repeat`, sonst `OpenTicket` und `ticket_id` setzen;
  - `alarm` + erfüllt → `clear_since=nil`;
  - `alarm` + nicht erfüllt → `clear_since` setzen falls leer; sobald `reset_minutes` Minuten am Stück nicht erfüllt → `normal`, `alarm_since=nil`, `clear_since=nil` (Ticket-ID bleibt für die Offen-Prüfung).
  - `last_value` aktualisieren; `after`-Funktionen nach Commit. Fehler eines Schwellwerts loggen, Rest läuft weiter; Panik abfangen (`safePass` wie Sampler).

- [ ] **Step 1:** Tests (DB, `Now` fest, Fake-`AlarmSink` zählt Aufrufe): `TestWatchOpensTicketOnce`, `TestWatchFlappingOneTicket` (über 3 / unter 5 / über 3 → 1× OpenTicket, 0× Repeat, Zustand bleibt alarm), `TestWatchResetAfter15` (14 min unter → alarm; 15 min → normal), `TestWatchRepeatWhileTicketOpen` (nach Rückschaltung erneute Überschreitung, Ticket offen → Repeat; Ticket zu → neues OpenTicket), `TestWatchGapKeepsState`, `TestWatchCatchUp` (120 verpasste Minuten, ein Ticket), `TestWatchCatchUpLimit24h`, `TestWatchThresholdRemoved`, `TestWatchInactiveIgnored`, `TestWatchSkip`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS; `go test ./internal/modules/energie/` → PASS.
- [ ] **Step 5:** Commit `Schwellwerte: Alarm-Wächter mit Rückschaltzeit, Nachholen und einem Ticket je Alarm`.

### Task 5: Verdrahtung, Ticket und Nachricht

**Files:** Create `internal/web/energy_alarms.go`, `energy_alarms_test.go`; Modify `cmd/server/main.go` (neben `StartEnergySampler`)

**Interfaces:**
- Consumes: Task 2 (`CreateInput.AssignedGroupID`, `RequiresRootCause`, `CreateInTx`, `AfterCreate`), Task 4 (`AlarmSink`, `AlarmWatch`).
- Produces: `func (h *Handler) StartEnergyAlarms(ctx context.Context)`; `type energyAlarmSink struct{ h *Handler }` (implementiert `energie.AlarmSink`); `func (h *Handler) alarmRecipients(ctx context.Context, t energie.Threshold) (userIDs []string, assignUser, assignGroup *string, err error)`.
- Ticket: Titel „Schwellwert „<Name>“ überschritten: <Messstelle>“ bzw. „unterschritten“; Beschreibung mit Wert und Grenze (Einheit, deutsches Zahlenformat), Zeitraum („Mittel über 1 min“, „Menge in 15 min“, „Summe in 15 min“), Zeitpunkt `TT.MM.JJJJ hh:mm`, Link `/energy/meters?q=<meter_key>`; `InfrastructureID` der Messstelle; `Tags` = [Tag] wenn gesetzt; Priorität; `RequiresRootCause=true`; Ersteller System-Benutzer (wie Rüst-Überwachung).
- Verantwortliche: Benutzer → `ResponsibleTo`+`AssignedTo`; Gruppe → `AssignedGroupID`, Empfänger über `pushRecipients`; leer → `infrastructure_responsible`; sonst Inhaber von `energy.admin`. Nachricht über `systemNotify` mit Ticket-Link. Doppelte Nachricht durch `notifyRecordCreated`/`StartPushNotifier` prüfen und für Alarm-Tickets vermeiden.
- Repeat: Ticket-Kommentar vom System-Benutzer „Erneut überschritten am TT.MM.JJJJ hh:mm – Wert …, Grenze …“ + Nachricht an dieselben Empfänger.
- Start: nicht bei `h.Mirror().On`, `Skip = h.MaintLocked`.

- [ ] **Step 1:** Tests: `TestAlarmRecipients` (DB: Benutzer / Gruppe / Anlage geerbt / niemand → energy.admin-Inhaber), `TestAlarmTicketContent` (DB: Titel, Tag, Anlage, Priorität, Gruppe, `requires_root_cause`), `TestAlarmNoDoubleNotify`, `TestAlarmRepeatComment`, `TestStartEnergyAlarmsMirror`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS; `go build ./...`.
- [ ] **Step 5:** Commit `Schwellwerte: Alarm-Tickets mit Tag und Nachricht an Verantwortliche, Wächter nur am aktiven Server`.

### Task 6: Oberfläche

**Files:** Create `internal/web/energy_thresholds.go`, `energy_thresholds_test.go`, `web/templates/widgets/energy_thresholds.gohtml`; Modify `internal/web/handler.go` (Routen), `energy_meters.go` + `web/templates/widgets/energy_meter_table.gohtml` (Spalte, Filter `alarm=1`), `internal/web/energy_meters_test.go:295` (Test, der „Schwellwert“ verbietet, anpassen), `perm_gate.go` (Löschroute), `web/i18n/*.json` falls nötig

**Interfaces:**
- Consumes: Task 3 (`Thresholds`, `SaveThreshold`, `DeleteThreshold`, `ThresholdCounts`).

| Route | Recht |
|---|---|
| `GET /energy/meters/{id}/thresholds` (htmx-Fragment nach vorhandenem Muster) | `energy.view` (Datensatz: Anlage der Messstelle) |
| `POST /energy/meters/{id}/thresholds/save-web` | `energy.thresholds` |
| `POST /energy/thresholds/{tid}/delete-web` | `energy.admin` (in `gateRules`) |

- Liste je Messstelle: Bezeichnung, Bedingung + Grenze + Einheit, Zeitraum, Rückschaltung, Tag, Verantwortlich, aktiv, Zustand („normal“, „Alarm seit TT.MM. hh:mm“, „zurückschaltend seit …“), Link zum Ticket. Formular mit Benutzer- oder Gruppenauswahl (vorhandene Auswahlfelder), Tag aus vorhandenen Ticket-Tags oder frei.
- Übersicht: Spalte „Schwellwerte“ (z. B. „2 · 1 Alarm“), Filter „mit offenem Alarm“.

- [ ] **Step 1:** Tests: `TestThresholdsPermissions` (ohne `energy.view` 403/Weiterleitung, Speichern ohne `energy.thresholds` 403, Löschen ohne `energy.admin` 403), `TestThresholdSaveWeb` (Fehlermeldung bei ungültigem Zeitraum, Erfolg legt an), `TestMeterTableThresholdColumn` (Spalte und Filter), `TestPagesUseBaseData`, `TestHelpCoversAllPages`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit `Schwellwerte: Pflege je Messstelle, Spalte und Filter in der Übersicht`.

### Task 7: Handbuch, Version, Prüfung vor dem Merge

**Files:** Modify `web/templates/help.gohtml` (Kapitel Energiedaten: „Schwellwerte und Alarme“ mit `.hb-howto`; Kapitel Tickets: Alarm-Tickets und Ursache; Kapitel Administration: `energy.thresholds`), ggf. `widgets/help_figures.gohtml`, `CHANGELOG.md`, `VERSION`

- [ ] **Step 1:** Handbuch: So geht's „Schwellwert anlegen“, Ablauf eines Alarms mit Beispiel (Stromspitze über 45 kW, 1 min; Rückschaltung 15 min; erneute Überschreitung bei offenem Ticket), Auswertung je Messart, Lücken, Alarm-Ticket mit Ursache schließen.
- [ ] **Step 2:** `VERSION` → `0.80.15`; `CHANGELOG.md` `## [0.80.15] – 2026-11-15` mit `### Neu` und `### Sicherheit` (Recht `energy.thresholds`; Alarm-Tickets nur mit Ursache).
- [ ] **Step 3:** `go vet ./...`, `go build ./...`, `go test ./...` mit `PDH_TEST_DSN` → grün.
- [ ] **Step 4:** Prüfung vor dem Merge nach `AGENTS.md` (fetch, Migration 163 frei, VERSION über main, Dopplungen `energy_thresholds`, `requires_root_cause`, `energy.thresholds`).
- [ ] **Step 5:** Commit `Schwellwerte: Handbuch, Änderungsprotokoll 0.80.15`.
- [ ] **Step 6: Anhalten** – Push, Merge, Testverbund nur mit Freigabe; Browserprüfung im Testverbund nach `AGENTS.md`.
