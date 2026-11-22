# Herkunft und Kachelfarbe – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Jedes Ticket und jede Störung kennt ihre Herkunft; die Startseiten-Kacheln zeigen sie; jede Kachel bekommt eine helle Hintergrundfarbe zur Auswahl.

**Architecture:** Spalten `origin` und `origin_label` an `tickets` und `faults` (Migration 201, Nachtrag aus Abgleich, App, Alarm, Freigabe-Ticket, Terminal-/Easy-Textzeile). Jeder Anlege-Weg setzt sie über `CreateInput`/`CreateFaultInput`. Anzeige über eine Funktion `originText` (Abgleich-Profilname nur mit `sync.view`). Kachelfarbe als Schlüssel `bg` in `WidgetInstance.Config` (keine Migration).

**Tech Stack:** Go, pgx, html/template, CSS-Variablen (Fiori/Fluent, hell/dunkel).

**Spec:** `docs/superpowers/specs/2026-11-22-herkunft-kachelfarbe-design.md`

## Global Constraints

- Herkunftswerte: `''` (unbekannt), `web`, `easy`, `terminal`, `app`, `sync`, `alarm`, `system`; `origin_label` z. B. Name des Terminals, „Energiedaten“, „IT-Netz“, „Firewall-Freigabe“.
- „von Hand“ (`web`) und unbekannt werden nicht angezeigt.
- Abgleich: Profilname nur mit `sync.view`, sonst „Fremdsystem“.
- Kachelfarben: `blue mint green yellow peach rose lilac gray` (leer = Standard); hell als heller Ton, dunkel gedämpft; Text-Kontrast ≥ 4,5:1.
- Version: nächste Korrekturversion über `main` (bei Beginn 0.82.31), Migration 201.

## Review Focus

1. Abgleich-Profilname ohne `sync.view` sichtbar (Kachel, Liste, Detail).
2. Ein Anlege-Weg vergessen → Herkunft bleibt leer (Easy, Terminal, App, Alarm, Abgleich, Leitstand, Web).
3. Kacheln zählen außerhalb der eigenen Abteilung (Scope wie die Zahl selbst).
4. Unbekannter `bg`-Wert aus dem Browser landet in der Klasse (nur Liste).
5. Dunkler Modus: helle Fläche mit heller Schrift.

---

### Task 1: Kachelfarbe

**Files:** `internal/web/dashboard_widgets.go` (`sanitizeWidgets`), `web/templates/widgets/dashboard_widgets.gohtml` (`dw-card`, CSS, JS), Test `internal/web/dashboard_bg_test.go`
- [ ] Tests: `TestWidgetBgSanitize` (erlaubte bleiben, `"red; x"` fällt weg); `TestWidgetCardBgClass` (Klasse `dw-bg-mint` im gerenderten `dw-card`, ohne Farbe keine Klasse).
- [ ] Bauen: Auswahl „Hintergrund“ im Einstellungsbereich jeder Kachel (Farbfelder mit Namen), sofort sichtbar, gespeichert über `data-cfg="bg"`.

### Task 2: Herkunft speichern

**Files:** `migrations/201_record_origin.up.sql`, `internal/modules/tickets/tickets.go`, `internal/modules/faults/*` (Eingabe, Insert), alle Anlege-Wege (`handler.go`, `global_dashboard_create.go`, `easy_mode.go` `reportCreate`, `terminal_report.go`, `app_requests_api.go`, `app_scan.go`, `energy_alarms.go`, `it_netsync.go`, `esppdh_fw.go`, `setup_easy.go`, `extsync/target.go`)
- [ ] Tests: `TestRecordOriginBackfill` (Migration erneut ausführbar; Abgleich-, App-, Alarm-Ticket bekommen ihre Herkunft); `TestReportCreateSetsOrigin` (Easy → easy, Terminal → terminal + Name); `TestTicketCreateWebOrigin`.

### Task 3: Herkunft anzeigen

**Files:** neu `internal/web/record_origin.go` (`originText`, `originCounts`), `dashboard_widgets.go` (Stat-Kacheln, `list_faults`, `list_mine`), Listen und Detailseiten von Tickets und Störungen
- [ ] Tests: `TestOriginText` (Tabelle aller Werte, Abgleich mit/ohne Recht); `TestStatTicketsOriginLine` („davon importiert: 2 aus SAP PM · 1 aus der App“, Scope); Render-Test Schild in `list_faults`.

### Task 4: Handbuch, Änderungsprotokoll, Version
- [ ] Handbuch (Startseite: Kachelfarbe, Herkunft; Tickets/Störungen: Herkunft), CHANGELOG, VERSION, AGENTS; volle Tests.
