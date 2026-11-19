# Sortieren in Historie, Maßnahmen und Kommentaren (0.82.2) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Über jeder Verlaufs-, Maßnahmen-, Kommentar- und Protokoll-Liste steht dieselbe Sortier-Leiste; die Wahl merkt sich das PDH je Benutzer und Listenart.

**Architecture:** Die Sortier- und Zusammenfass-Logik liegt als reine Funktionen in einem neuen Paket `internal/core/listsort`. `internal/web/list_sort.go` kennt die Listenarten, liest und speichert die Wahl des Benutzers (neue JSONB-Spalte `users.list_sort`) und liefert die Daten für das gemeinsame Widget `sort-bar`. Listen mit wenigen Einträgen (Historie, Maßnahmen, Kommentare) werden vollständig geladen und in Go geordnet; Protokoll-Listen mit `LIMIT` sortiert die Datenbank über fest hinterlegte `ORDER BY`-Bausteine. Maßnahmen und Kommentare bekommen je einen gemeinsamen, vom Server gelieferten Baustein statt dreier Skripte.

**Tech Stack:** Go 1.27, pgx/PostgreSQL, Go-Templates + htmx, CSS-Variablen für Fiori/Fluent.

**Spec:** `docs/superpowers/specs/2026-11-19-sortierung-design.md` (vom Anwender am 19.11.2026 freigegeben; Festlegungen 1–4 dort).

**Branch:** `feature/sortierung` ab `main`, nachdem 0.82.1 auf `main` ist. Migration **182** (vor dem Bau gegen `origin/main` prüfen). Kein neues Recht.

## Global Constraints

- Sortierarten und ihre Kennungen: `new` Neueste zuerst · `old` Älteste zuerst · `freq` Häufigste zuerst · `same` Gleiches zusammen · `person` Nach Person · `kind` Nach Art · `due` Nach Termin (nur KVP-Maßnahmen).
- „Gleich“ heißt: gleicher Schlüssel `strings.Join(textmatch.Words(text), " ")` (Groß/Klein, Umlaute, Satz- und Leerzeichen egal). Ähnliche Texte werden **nicht** zusammengelegt.
- Datensatzhistorie: „gleich“ = gleiche `action` und gleiches `field_name`.
- `freq`: Gruppen nach Anzahl absteigend, bei Gleichstand die mit dem jüngsten Eintrag zuerst. `same`: Gruppen nach Schlüssel aufsteigend. Eine Gruppe mit einem Eintrag erscheint als gewöhnliche Zeile, ab zwei als „N× Text – zuletzt TT.MM.JJJJ“ mit aufklappbaren Einzeleinträgen (`<details>`).
- `person`: nach Name aufsteigend (ohne Groß/Klein), ohne Name ans Ende, darin neueste zuerst. `kind`: nach Art aufsteigend, darin neueste zuerst.
- Standard je Liste bleibt wie heute (siehe Tabelle „Listenarten“).
- Die Wahl gilt je Benutzer und Listenart, gespeichert in `users.list_sort` (JSONB, Schlüssel = Listenart, Wert = Kennung). Am Spiegel wird nicht gespeichert (kein Fehler, die Wahl gilt für den Aufruf).
- Ein Abfrageparameter für alle Listen: `ls_<listenart>=<kennung>`. Unbekannte oder für die Liste nicht erlaubte Werte zählen als nicht gesetzt.
- Gezählt und gezeigt wird nur, was der Benutzer sehen darf. Kein neuer Weg darf mehr zeigen als die Seite, auf der die Liste steht.
- `ORDER BY` wird nie aus Benutzereingaben gebaut – nur aus festen Bausteinen je Kennung.
- Aussehen nur über `--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent`, `--shadow-card`; beide Stile, hell/dunkel; bis 760 px Breite wird die Leiste zu einem Auswahlfeld.
- Texte der Leiste über `{{t "…"}}` mit Einträgen in `web/i18n/{en,mk,ro,tr}.json`.
- AGENTS.md gilt: Handbuch und Änderungsprotokoll im selben Schritt, Deutsch, kein Push/Merge ohne Freigabe.

### Listenarten

| Listenart | Wo | Erlaubt | Standard |
|---|---|---|---|
| `history` | Datensatzhistorie, 13 Detailseiten | new, old, freq, same, person, kind | new |
| `timeline` | Anlagen-Historie | new, old, freq, same, person, kind | new |
| `actions` | Maßnahmen an Störung, Ticket, Aufgabe, Wartung | old, new, freq, same, person | old |
| `kvp_actions` | KVP-Maßnahmen | due, new, old, freq, same, person | due |
| `comments` | Kommentare an Ticket und KVP | old, new, freq, same, person | old |
| `moves` | Lagerbewegungen (Ersatzteil, Lagerort) | new, old, person, kind | new |
| `obligations` | Nachweise der Prüfpflichten (Anlage) | new, old, person, kind | new |
| `vpn` | VPN-Ereignisse | new, old, person, kind | new |
| `builds` | ESPPDH-Bauläufe | new, old, person, kind | new |
| `backups` | Sicherungen, Reiter „Protokoll“ | new, old, person, kind | new |
| `cleanup` | Bereinigung, Reiter „Verlauf“ (beide Listen) | new, old, person, kind | new |
| `sync` | Abgleich-Läufe | new, old, kind | new |
| `export` | Efficio-Export-Läufe | new, old, kind | new |
| `hmi` | HMI-Zugriffe (Anlage) | new, old, person, kind | new |
| `serverlog` | Server-Protokoll | new, old, person, kind | new |

**Bewusst unverändert** (Auszüge „die letzten N“, keine Listen zum Durchsehen): „Letzte Bewegungen“ (5) auf der Ersatzteilseite, „Letzte Buchungen“ (8), „Letzte Scans“ (25), Schreibprotokoll im Explorer (10), Ereignisse des Spiegelverbunds (kommen nicht aus der Datenbank).

## Abweichungen und Funde gegenüber dem Entwurf

- `textmatch.Normalize` gleicht nur Groß/Klein und Umlaute an; Satz- und Leerzeichen bleiben. Der Plan nimmt deshalb den Wortschlüssel, den der Copilot schon nutzt.
- Historie: „über alle Einträge rechnen“ heißt hier höchstens **5000** je Datensatz; darüber steht ein Hinweis „Nur die neuesten 5000 Einträge berücksichtigt“.
- Anlagen-Historie: `freq` und `same` rechnen über die gefilterte Menge, höchstens **2000** Einträge (statt 200), mit demselben Hinweis.
- Wartung zeigt Maßnahmen heute nur an erledigten Aufgaben; künftig steht der Baustein immer im Reiter „Übersicht“ (nur lesend, eingetragen wird weiter beim Abschließen).
- **Zwei Sicherheitslücken im Bestand**, die dieser Plan mit behebt (stehen im Änderungsprotokoll unter „Sicherheit“):
  1. Löschen einer Maßnahme über die Schnittstelle prüft nicht, ob die Maßnahme zum Datensatz im Pfad gehört (alle vier `DeleteAction`).
  2. `/records/history/{modul}/{id}` ist für Projekt, KVP, Anlage und Lagerort jedem angemeldeten Benutzer offen, weil diese Module kein Ansichtsrecht hinterlegt haben.

## Review Focus

1. **Recht fehlt:** Aufruf eines Listen-Teils (Historie, Maßnahmen, Kommentare) ohne Ansichtsrecht oder außerhalb der eigenen Abteilung → 403/404, nie Inhalt → Task 3 `TestHistoryFragmentPerms`, Task 5 `TestActionsBlockPerms`, Task 6 `TestCommentsBlockPerms`.
2. **Manipulierter Parameter** (`ls_history=;DROP`, `ls_moves=freq`, Listenart, die es nicht gibt): Standard-Sortierung, nichts gespeichert → Task 2 `TestSortModeRejectsUnknown`, Task 7 `TestLogOrderSQLWhitelist`.
3. **Leere und entartete Texte** (nur Satzzeichen, nur Leerzeichen, 4000 Zeichen, `<script>`): eigene Gruppe „(ohne Text)“ statt Absturz, Ausgabe maskiert → Task 1 `TestGroupsEmptyKey`, Task 5 `TestActionsBlockEscapes`.
4. **Spiegel und Gast:** Sortieren am Spiegel und ohne gespeicherte Wahl funktioniert, speichert nichts und meldet keinen Fehler → Task 2 `TestSortModeMirror`.
5. **Gleichstand und Reihenfolge:** Einträge mit gleichem Zeitstempel behalten eine feste Reihenfolge (kein Springen beim Neuladen, „Weitere laden“ zeigt nichts doppelt) → Task 1 `TestOrderStable`, Task 3 `TestHistoryMoreNoDuplicates`.

---

### Task 1: Sortier- und Zusammenfass-Logik

**Files:**
- Create: `internal/core/listsort/listsort.go`, `internal/core/listsort/listsort_test.go`
- Modify: `internal/web/copilot_suggest.go` (`copSolutionKey` nutzt `listsort.Key`)

**Interfaces – Produces:**
- `type Mode string`; Konstanten `Newest="new"`, `Oldest="old"`, `Frequent="freq"`, `Same="same"`, `Person="person"`, `Kind="kind"`, `Due="due"`.
- `func (m Mode) Grouped() bool` – wahr für `freq` und `same`.
- `func Parse(s string, allowed []Mode, def Mode) Mode`
- `func Key(text string) string` – `strings.Join(textmatch.Words(text), " ")`.
- `type Item struct{ At time.Time; Key, Label, Person, Kind string; Due time.Time }`
- `func Order(items []Item, m Mode) []int` – Reihenfolge als Indexliste für `new`, `old`, `person`, `kind`, `due` (`due`: ohne Termin ans Ende); stabil, Gleichstand nach ursprünglichem Index.
- `type Group struct{ Key, Label string; Count int; Last time.Time; Idx []int }` – `Label` = `Label` des jüngsten Eintrags; `Idx` neueste zuerst; leerer Schlüssel → eine Gruppe mit `Label` „(ohne Text)“.
- `func Groups(items []Item, m Mode) []Group` – für `freq` und `same` nach den Regeln der Global Constraints.

- [ ] **Schritt 1: Tests.** `TestParse` (erlaubt, nicht erlaubt, leer, Müll → Standard). `TestKey` („Filter gereinigt.“ = „filter  GEREINIGT“; „Öl“ = „oel“; „…“ → `""`). `TestOrder` als Tabelle für alle fünf Arten, darunter Person ohne Name am Ende und `due` ohne Termin am Ende. `TestOrderStable` (drei Einträge mit gleichem Zeitstempel behalten die Reihenfolge, zweimal gleich). `TestGroupsFrequent` (3×, 2×, 1× → Reihenfolge, `Last`, `Label` des jüngsten). `TestGroupsFrequentTie` (gleiche Anzahl → jüngere Gruppe zuerst). `TestGroupsSame` (alphabetisch nach Schlüssel). `TestGroupsEmptyKey`.
- [ ] **Schritt 2:** `go test ./internal/core/listsort` scheitert.
- [ ] **Schritt 3: Umsetzen.**
- [ ] **Schritt 4:** Tests grün; `go test ./internal/web -run Copilot` grün.
- [ ] **Schritt 5:** Commit „Sortieren: gemeinsame Logik für Reihenfolge und Zusammenfassen“.

### Task 2: Gemerkte Wahl und Sortier-Leiste

**Files:**
- Create: `migrations/182_users_list_sort.up.sql` (+ `.down.sql`), `internal/web/list_sort.go`, `internal/web/list_sort_test.go`, `web/templates/widgets/sort_bar.gohtml`
- Modify: `migrations/checksums.txt` (über `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`), `internal/web/user_row.go` (Spalte lesen), `web/templates/base.gohtml` (CSS der Leiste, beide Stile, mobil), `web/i18n/*.json`

**Migration:** `ALTER TABLE users ADD COLUMN IF NOT EXISTS list_sort JSONB;`

**Interfaces – Consumes:** `listsort.Mode`, `listsort.Parse` (Task 1).

**Interfaces – Produces:**
- `type sortList struct{ Modes []listsort.Mode; Default listsort.Mode }`; `var sortLists = map[string]sortList{…}` genau nach der Tabelle „Listenarten“.
- `userRow.ListSort map[string]string`.
- `func (h *Handler) sortMode(r *http.Request, list string) listsort.Mode` – Parameter `ls_<list>` gültig → nehmen und, wenn anders als gespeichert und nicht am Spiegel und Benutzer angemeldet, speichern (`UPDATE users SET list_sort = COALESCE(list_sort,'{}'::jsonb) || jsonb_build_object($1::text,$2::text) WHERE id=$3`; Fehler nur protokollieren); sonst gespeicherter Wert, wenn erlaubt; sonst Standard. Unbekannte Listenart → `listsort.Newest`.
- `type SortOption struct{ Mode listsort.Mode; Label string; On bool }`
- `type SortBar struct{ List string; Options []SortOption; URL, Target string; Htmx bool; Lang string }`; `func (b SortBar) Language() string`.
- `func (h *Handler) sortBar(r *http.Request, list string, cur listsort.Mode, url, target string) SortBar` – `target == ""` heißt Seitenmodus (gewöhnliche Links, `URL` behält alle übrigen Abfrageparameter und setzt `ls_<list>`), sonst htmx (`hx-get`, `hx-target`, `hx-swap="outerHTML"`).
- Beschriftungen: „Neueste zuerst“, „Älteste zuerst“, „Häufigste zuerst“, „Gleiches zusammen“, „Nach Person“, „Nach Art“, „Nach Termin“; davor „Sortieren:“.
- Template `sort-bar`: `<div class="sortbar" role="group" aria-label="Sortieren">` mit einem Knopf je Option (`aria-pressed`), dazu ein `<select class="sortbar-sel">` mit denselben Optionen, das ab 760 px Breite statt der Knöpfe erscheint (reines CSS; das Auswahlfeld löst denselben Aufruf aus).

- [ ] **Schritt 1: Tests.** `TestSortListsComplete` (jede Listenart der Tabelle da, Standard in `Modes`). `TestSortModeDefault`, `TestSortModeStored`, `TestSortModeParamSaves` (Wert steht danach in `users.list_sort`, andere Listenarten bleiben). `TestSortModeRejectsUnknown` (`;DROP`, für die Liste nicht erlaubte Kennung, unbekannte Listenart → Standard, nichts gespeichert). `TestSortModeMirror` (Spiegel: Wahl gilt, nichts gespeichert, kein Fehler). `TestSortBarRenders` (htmx- und Seitenmodus, gewählte Option `aria-pressed="true"`, übrige Parameter bleiben in der URL, `<script>` im Parameter maskiert). `TestI18nCatalogsComplete` grün.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.**
- [ ] **Schritt 4:** `go test ./internal/web -run 'Sort|I18n|UserRow' ./pkg/database` grün.
- [ ] **Schritt 5:** Commit „Sortieren: Sortier-Leiste und gemerkte Wahl je Benutzer“.

### Task 3: Datensatzhistorie

**Files:**
- Modify: `internal/web/handler.go` (`recordHistory` bleibt für den Explorer unverändert; neu `recordHistoryAll`), `internal/web/record_links.go`, `web/templates/widgets/record_history.gohtml`, `web/templates/widgets/record_links.gohtml`, die sechs Seiten mit eingebetteter Historie (`fault_detail`, `ticket_detail`, `kvp_detail`, `inventory_detail`, `partner_detail`, `purchase_detail`) und ihre Handler (Feld `History` und Abfrage entfallen), `internal/web/maintenance_web.go` (tote Abfrage entfernen), Tests

**Interfaces – Consumes:** Task 1, Task 2.

**Interfaces – Produces:**
- `HistoryView` bekommt `At time.Time` (roh) zusätzlich zu `CreatedAt`.
- `func (h *Handler) recordHistoryAll(ctx context.Context, refType, id string) (rows []HistoryView, capped bool)` – alle Einträge, neueste zuerst, `ORDER BY created_at DESC, id DESC LIMIT 5001`.
- `GET /records/history/{module}/{id}?ls_history=…&n=…` liefert das ganze Kärtchen (Leiste + Liste + „Weitere laden“); `n` = Anzahl gezeigter Zeilen bzw. Gruppen, Schritt 30, Standard 30, höchstens 5000.
- Zugriff: `historyModules` – eigene Tabelle für diesen Weg mit Prüf-Funktion je Modul: die bisherigen `fieldModules`-Einträge, aber für `project`, `kvp`, `infrastructure`, `storage` die Prüfung der jeweiligen Detailseite (`canViewInfra`, `inventory.view|edit`, Projekt und KVP wie ihre Seiten); neu `partner` (`canViewPartners`) und `purchase_order` (`hasPermRec(r, "procurement.view", "purchase_order", id)`).
- Alle 13 Seiten nutzen `record-history-slot`; `kind` in der Historie = Beschriftung des Vorgangs (`action` + Feld), dieselbe Beschriftung wie in der Zeile.

- [ ] **Schritt 1: Tests.** `TestHistoryFragmentSorts` (je Sortierart die erwartete Reihenfolge; `freq` zeigt „3× Status geändert“). `TestHistoryMore` (31 Einträge: erst 30 und Knopf, mit `n=60` alle, kein Knopf). `TestHistoryMoreNoDuplicates` (gleiche Zeitstempel). `TestHistoryCapped` (Hinweistext bei mehr als 5000 – mit herabgesetzter Grenze über eine Paketvariable). `TestHistoryFragmentPerms` (ohne Ansichtsrecht: Projekt, KVP, Anlage, Lagerort, Partner, Bestellung → 403; fremde Abteilung → 403/404). `TestHistoryAllPagesUseSlot` (alle 13 Vorlagen enthalten `record-history-slot`, keine mehr `{{template "record-history" .History}}`). Bestehende `TestDetailPagesTabs`, Explorer-Tests grün.
- [ ] **Schritt 2:** Tests scheitern.
- [ ] **Schritt 3: Umsetzen.**
- [ ] **Schritt 4:** `go test ./internal/web -run 'History|DetailPages|Explorer|Scope'` grün.
- [ ] **Schritt 5:** Commit „Sortieren: Datensatzhistorie mit Sortier-Leiste und ‚Weitere laden‘“.

### Task 4: Anlagen-Historie

**Files:**
- Modify: `internal/web/infra_history.go`, `web/templates/widgets/infra_history.gohtml`, `web/templates/infra_detail.gohtml` (Leiste im Filterformular als verstecktes Feld + `sort-bar`), `internal/web/infra_history_test.go`

**Interfaces – Consumes:** Task 1, Task 2.

**Produces:** `infraHistoryFilter.Sort listsort.Mode` (aus `h.sortMode(r, "timeline")`). Person = `Author`, Art = `ModuleLabel`, Schlüssel = `listsort.Key(Title + " " + Body)` für Maßnahmen, Kommentare und Lagerbewegungen, für die übrigen Arten `listsort.Key(Title)`. `old` sortiert in der Datenbank (`ORDER BY occurred_at ASC`), damit die ältesten überhaupt erscheinen; `freq`/`same` laden bis 2000. `InfraHistoryResult` bekommt `Groups` und `Capped bool`. Die CSV-Ausgabe bleibt nach Datum.

- [ ] **Schritt 1: Tests.** `TestInfraHistorySortOld` (ältester Eintrag zuerst, auch wenn es mehr als 200 gibt). `TestInfraHistoryFrequent` (gleiche Maßnahme an zwei Störungen → „2× …“). `TestInfraHistoryByKind`, `TestInfraHistoryByPerson`. `TestInfraHistorySortKeepsFilter` (Text- und Zeitraumfilter bleiben beim Umschalten). `TestInfraHistoryScopeCounts` (abteilungsbeschränkter Benutzer: Gruppen zählen nur Sichtbares). `TestInfraHistoryTemplateRenders` grün.
- [ ] **Schritt 2–4:** scheitern, umsetzen, `go test ./internal/web -run InfraHistory` grün.
- [ ] **Schritt 5:** Commit „Sortieren: Anlagen-Historie“.

### Task 5: Maßnahmen an Störung, Ticket, Aufgabe, Wartung

**Files:**
- Create: `internal/web/record_actions.go`, `internal/web/record_actions_test.go`, `web/templates/widgets/record_actions.gohtml`
- Modify: `internal/web/handler.go` (Routen), `internal/web/dept_scope.go` (`scopePaths` um `actions|comments` erweitern) + `dept_scope_test.go`, `internal/web/perm_gate.go` (+ Test), `fault_detail.gohtml`, `ticket_detail.gohtml`, `task_detail.gohtml` (Skripte `loadFaultActions`/`addFaultAction`, `loadTicketActions`…, `loadTaskActions`… entfernen), `maintenance_detail.gohtml`, die vier `DeleteAction` in `internal/modules/{faults,tickets,tasks,maintenance}` samt Tests

**Interfaces – Consumes:** Task 1, Task 2.

**Produces:**
- `GET /records/actions/{module}/{id}?ls_actions=…` → Template `record-actions` (Leiste, Liste, Eingabezeile wenn erlaubt); `module` ∈ `fault`, `ticket`, `task`, `maintenance_task`.
- `POST /records/actions/{module}/{id}` (`description`) → legt über den vorhandenen Dienst des Moduls an und liefert den Baustein neu. Leer oder über 4000 Zeichen → 400 mit Klartext.
- Lesen: Ansichtsprüfung wie die Detailseite des Moduls plus Abteilungsebene. Schreiben: dieselben Rechte wie heute die Schnittstelle (`faults.edit`, `tickets.edit`, `tasks.edit`, `maintenance.edit` über `hasPermRec`, Beteiligte erlaubt) – als `gateRules`-Einträge.
- Eingabezeile nur, wo sie heute steht: Störung immer bei Schreibrecht, Ticket nicht bei gelöstem Ticket, Aufgabe nur bei `CanResolve`, Wartung nie.
- `DeleteAction(ctx, parentID, actionID)` in allen vier Modulen: `WHERE id=$1 AND <fk>=$2`; nichts gelöscht → „nicht gefunden“ (404).
- Die JSON-Schnittstellen unter `/api/v1/…/actions` bleiben.

- [ ] **Schritt 1: Tests.** `TestActionsBlockSorts` (alle fünf Arten; `freq` „2× Filter gereinigt“ bei unterschiedlicher Schreibweise). `TestActionsBlockAdd` (je Modul; Historie und bisherige Nebenwirkungen bleiben). `TestActionsBlockPerms` (ohne Ansicht 403; ohne Schreibrecht keine Eingabezeile und POST 403; fremde Abteilung). `TestActionsBlockEscapes`. `TestActionsBlockInputRules` (gelöstes Ticket, Aufgabe ohne `CanResolve`, Wartung). `TestDeleteActionOtherParent` (je Modul: Maßnahme einer anderen Störung über den Pfad dieser Störung → 404, Maßnahme noch da). `TestScopeTarget`, `TestPermissionGateRules`, `TestAllPagesScriptsValid`, `TestDetailPagesTabs` grün.
- [ ] **Schritt 2–4:** scheitern, umsetzen, `go test ./internal/web ./internal/modules/...` grün.
- [ ] **Schritt 5:** Commit „Sortieren: Maßnahmen als gemeinsamer Baustein, Löschen prüft den Datensatz“.

### Task 6: KVP-Maßnahmen und Kommentare

**Files:**
- Create: `internal/web/record_comments.go`, `internal/web/record_comments_test.go`, `web/templates/widgets/record_comments.gohtml`
- Modify: `internal/web/kvp.go` (`kvpActionView` um `CreatedAt time.Time`, `CreatedByName`; `kvpActions` sortiert nach `sortMode(r, "kvp_actions")`), `kvp_detail.gohtml` (Leiste im Seitenmodus über der Karte „Do“; Kommentar-Baustein), `ticket_detail.gohtml`, `internal/web/handler.go` (`TicketAddComment` und sein zusammengesetztes HTML entfallen, Route `/tickets/{id}/comment` leitet auf den neuen Weg), Tests

**Produces:**
- KVP-Maßnahmen: Person = Verantwortlicher; `freq`/`same` nach Beschreibung; die Formulare zum Abhaken und Löschen bleiben in jeder Zeile, auch in aufgeklappten Gruppen. Assistent, Abhak-Ansicht und A3-Druck bleiben nach Termin.
- `GET /records/comments/{module}/{id}?ls_comments=…`, `POST /records/comments/{module}/{id}` (`text`, 1–4000 Zeichen) für `ticket` und `kvp` → Template `record-comments`. Lesen wie die Detailseite; Schreiben wie heute (Ticket: wer das Ticket sieht; KVP: wer die Idee sieht).
- Anfangsbuchstabe des Namens über Runen, nicht Bytes.

- [ ] **Schritt 1: Tests.** `TestKVPActionsSort` (alle sechs Arten, Abhaken/Löschen in Gruppen vorhanden). `TestCommentsBlockSorts`. `TestCommentsBlockAdd` (Ticket, KVP; leer und zu lang → 400). `TestCommentsBlockPerms` (KVP-Idee, die der Benutzer nicht sieht → 403). `TestCommentInitialUmlaut` („Özlem“ → „Ö“). `TestTicketCommentOldRoute` (alter Weg funktioniert weiter).
- [ ] **Schritt 2–4:** scheitern, umsetzen, `go test ./internal/web -run 'KVP|Comment|Ticket'` grün.
- [ ] **Schritt 5:** Commit „Sortieren: KVP-Maßnahmen und Kommentare“.

### Task 7: Übrige Protokoll-Listen

**Files:**
- Modify: `internal/web/list_sort.go` (+ Test), `part_booking.go`/`parts.go` + `inventory_detail.gohtml`, `storage_detail.go` + `.gohtml`, `infra_obligations.go` + `infra_detail.gohtml`, `vpn.go` + `widgets/vpn.gohtml`, `esppdh_build.go` + `esppdh_device.gohtml`, `backup.go` + `backup.gohtml`, `record_state.go` + `cleanup.gohtml`, `sync_ops.go` + `internal/modules/extsync/store.go` + `widgets/sync_ops.gohtml`, `export_efficio.go` + `.gohtml`, `hmi.go` + Skript in `infra_detail.gohtml`, `server_logs.go` + `server_logs.gohtml`

**Produces:**
- `func logOrderSQL(m listsort.Mode, at, person, kind string) string` – feste Bausteine: `new` → `<at> DESC`, `old` → `<at> ASC`, `person` → `lower(<person>) ASC NULLS LAST, <at> DESC`, `kind` → `<kind> ASC, <at> DESC`; `person`/`kind` leer oder andere Kennung → wie `new`. Die Spaltenausdrücke kommen nur aus dem Programm.
- Jede Liste der Tabelle „Listenarten“ ab `moves`: Leiste im Seitenmodus über der Liste (bei nachgeladenen Teilen htmx), Abfrage mit `logOrderSQL`, `LIMIT` bleibt.
- HMI-Zugriffe: der JSON-Endpunkt nimmt `ls_hmi`; die Leiste steht als Vorlage auf der Seite, das Skript lädt mit dem gewählten Wert neu.
- Server-Protokoll: der Dateispeicher sortiert nicht selbst – nach dem Lesen der gefilterten Zeilen in Go über `listsort.Order`.

- [ ] **Schritt 1: Tests.** `TestLogOrderSQLWhitelist` (jede Kennung → erwarteter Text; Müll → `new`; Ergebnis enthält nie den Parameterwert). Je Liste ein Test `Test<Liste>Sort` mit drei Zeilen: `old` kehrt um, `person` und `kind` ordnen wie verlangt, Leiste ist im HTML. `TestListsUseExpandableRows`, `TestAllPagesScriptsValid` grün.
- [ ] **Schritt 2–4:** scheitern, umsetzen, `go test ./internal/web ./internal/modules/extsync` grün.
- [ ] **Schritt 5:** Commit „Sortieren: Protokoll-Listen“.

### Task 8: Handbuch, Version, Änderungsprotokoll, Gesamtlauf

**Files:** `web/templates/help.gohtml`, `web/templates/widgets/help_figures.gohtml`, `VERSION`, `CHANGELOG.md`, `AGENTS.md`

- [ ] **Schritt 1: Handbuch.** Im Kapitel `records` neuer Unterabschnitt „Listen sortieren“ mit „So geht's“ (Leiste, was jede Art bedeutet, „12× … – zuletzt …“ aufklappen, dass sich das PDH die Wahl je Listenart merkt, „Weitere laden“ in der Historie, Auswahlfeld auf dem Handy) und einer Skizze der Leiste. Kurzer Verweis in `faults`, `tickets`, `tasks`, `kvp`, `maintenance`, `infrastructure`, `inventory`, `storage`, `cleanup`, `backup`, `esppdh`, `abgleich`, `vpn`. Kapitel „Administration“: Historie eines Datensatzes sieht nur, wer den Datensatz sehen darf.
- [ ] **Schritt 2:** `VERSION` → `0.82.2`; `CHANGELOG.md` `## [0.82.2] – <Datum>`: `### Neu` (Sortier-Leiste überall; Zusammenfassen mit Zähler; gemerkte Wahl; „Weitere laden“), `### Geändert` (Maßnahmen an der Wartung immer sichtbar; Maßnahmen und Kommentare laden ohne Skript), `### Sicherheit` (beide Lücken in Anwendersprache). `AGENTS.md`: Punkt Sortierung auf „gebaut, Freigabe offen“.
- [ ] **Schritt 3:** `go build ./... && go vet ./... && go test ./...` (Labor-DB), `GOOS=linux go vet ./...`.
- [ ] **Schritt 4:** Commit „Version 0.82.2: Listen sortieren“.
- [ ] **Schritt 5 (Controller, nach Freigabe des Anwenders):** Prüfung vor dem Merge laut AGENTS.md, Push, Merge, Verbund-Update; Browserprüfung der Leiste in Fiori/Fluent, hell/dunkel, 320–1366 px an Störung, Ticket, KVP, Anlage und einer Protokoll-Liste.

## Was nur im Browser geprüft werden kann

- Umbruch der Leiste bei 320–768 px und der Wechsel zum Auswahlfeld.
- Aufklappen der Gruppen mit Tastatur und Vorleseprogramm.
- Verhalten mit vielen Einträgen (Historie einer alten Anlage) – Ladezeit.
