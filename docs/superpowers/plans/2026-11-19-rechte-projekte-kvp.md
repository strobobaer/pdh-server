# Ansichtsrecht für Projekte und KVP (0.82.3) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Projekte und KVP-Vorschläge sieht nur noch, wer das neue Ansichtsrecht hat oder beteiligt ist – überall im PDH; dazu zwei Sicherheitslücken (Leitstand-Kalender für Gäste, Ordnerlisten unter `/uploads/`).

**Architecture:** Eine gemeinsame Prüfung in `internal/web/view_rights.go` („darf diesen Vorgang sehen“ = Recht für die Abteilung des Datensatzes oder beteiligt) und ein Listenfilter, den alle Stellen nutzen. Die vorhandenen Wege `canViewRecord`, `APIDepartmentScope` und `historyModules` fragen diese Prüfung für `project` und `kvp`. Migration 183 legt die Rechte an und gibt sie jeder Rolle.

**Tech Stack:** Go 1.27, pgx/PostgreSQL, Go-Templates + htmx, `internal/core/rbac` (Rechte mit Abteilungsebene).

**Spec:** `docs/superpowers/specs/2026-11-19-rechte-projekte-kvp-design.md` (vom Anwender am 19.11.2026 freigegeben, samt Festlegungen).

**Branch:** `feature/rechte-projekte-kvp` (ab `main` mit 0.82.2). Migration **183** (vor dem Bau gegen `origin/main` prüfen). Version **0.82.3**.

## Global Constraints

- Rechte: `projects.view` „Projekte: ansehen“ (Kategorie „Aufgaben & Projekte“), `kvp.view` „KVP: alle Vorschläge ansehen“ (Kategorie „KVP“).
- Migration 183 gibt beide Rechte **jeder bestehenden Rolle** (auch „viewer“), ohne Abteilung; `user_permissions` werden nicht angefasst; später angelegte Rollen bekommen sie nicht von selbst.
- Sehen darf **nur**, wer das Ansichtsrecht für die Abteilung des Datensatzes hat (Bereichsadministrator `projects.admin`/`kvp.admin` eingeschlossen, das erledigt `rbac.Scope`) **oder beteiligt ist**. `projects.edit` und `kvp.manage` schließen das Ansehen **nicht** ein; Bearbeiten und Steuern wirken nur an Vorgängen, die man sehen darf.
- Abteilung: Projekt = Abteilung der Anlage oder der zugewiesenen Gruppe; KVP = Abteilung der Anlage (`recordDepartments`). Datensätze ohne Abteilung sieht, wer das Recht irgendwo hat.
- Beteiligt (immer am aktuellen Stand geprüft): Projekt = `created_by`, `responsible_to`, `assigned_to`, Mitglied der `assigned_group_id`; KVP = `submitter_id`, `created_by`, `reviewer_id`, `responsible_to`, Eintrag in `kvp_members`, `responsible_id` einer `kvp_actions`-Zeile.
- KVP-Vorschläge **einreichen** bleibt für alle Angemeldeten.
- Bleibt sichtbar: Projektname an Aufgaben (Karte, Liste, Detail); Verweise in Chat-Nachrichten und Benachrichtigungen.
- Historie von Aufgaben und Wartungsaufträgen: wie die Seite (Anmeldung + Abteilungssperre).
- Schnittstelle (`/api/v1`): dieselben Regeln für den Benutzer der Anmeldung; einzeln → 403, Listen gefiltert.
- Gäste auf `/global/calendar`: genau die Einstellung des Leitstands; Abfrageparameter zählen für Gäste nicht; ohne gültigen Leitstand 404.
- `/uploads/`: keine Ordnerlisten (Ordner → 404), einzelne Dateien wie bisher.
- AGENTS.md gilt: Handbuch, CHANGELOG, VERSION im selben Schritt; Texte Deutsch; kein Push/Merge ohne Freigabe; Fehler mit `componentLog(…).Error()`.

## Review Focus

1. **Bearbeiten ohne Ansehen:** Rolle mit `projects.edit`, ohne `projects.view`, nicht beteiligt → `POST /api/v1/projects/{id}/edit` und `/records/project/{id}/people|group|parties` → 403, Projekt unverändert → Task 2 `TestProjectEditNeedsView`.
2. **Recht nur in Abteilung A:** Projekt an Anlage in A sichtbar, in B nicht, ohne Anlage sichtbar; Projekt mit Gruppe aus A sichtbar → Task 1 `TestCanViewRecDepartments`.
3. **Beteiligt ohne Recht:** Detail, Historie, Kommentare, KVP-Druck erreichbar; nach Entfernen aus dem Vorgang nicht mehr → Task 1 `TestCanViewRecInvolved`, Task 4 `TestKVPSubmitterWithoutView`.
4. **Gast auf dem Leitstand-Kalender** mit `?set=1&show=project&show=fault&dept=<andere>` → nur die Arten und die Abteilung des Leitstands; ohne `b`/gültigen Leitstand 404 → Task 5 `TestCalendarGuestIgnoresQuery`.
5. **Listen ohne Recht und ohne Beteiligung** (Projekte-Liste, KVP-Seite, Kategorien, Copilot, Kalender, Partner, Benutzerseite, Anlagen-Historie, Startseiten-Baustein) zeigen keinen Titel → Tests je Stelle in Task 2–4 (`…HidesWithoutView`).

---

### Task 1: Rechte anlegen und gemeinsame Prüfung

**Files:**
- Create: `migrations/183_view_projects_kvp.up.sql`, `internal/web/view_rights.go`, `internal/web/view_rights_test.go`
- Modify: `migrations/checksums.txt` (nur über `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`)

**Interfaces – Produces:**
- `const permProjectsView = "projects.view"`, `const permKVPView = "kvp.view"`
- `var viewRights = map[string]string{"project": permProjectsView, "kvp": permKVPView}`
- `func (h *Handler) involvedIn(ctx context.Context, uid, refType, id string) bool` – nur `project` und `kvp`; Projekt über `scopeTables["project"].people`, KVP über eine neue Bedingung `kvpPeopleCond` (Global Constraints).
- `func (h *Handler) involvedIDs(ctx context.Context, uid, refType string) map[string]bool`
- `func (h *Handler) canViewRec(r *http.Request, refType, id string) bool` – Typ nicht in `viewRights` → `true`; sonst `h.hasPermRec(r, viewRights[refType], refType, id) || h.involvedIn(...)`; ohne angemeldeten Benutzer `false`.
- `func (h *Handler) mayViewAny(r *http.Request, refType string) bool` – Recht irgendwo (`rbac.Scope`: all oder Abteilungen vorhanden).
- `func (h *Handler) viewFilter(r *http.Request, refType string) func(id string) bool` – eine Abfrage für die Abteilungen **aller** Datensätze des Typs (wie `recordDepartments`, als Menge je ID) plus `involvedIDs`; danach je ID `hasPermIn` oder beteiligt. Bei Recht „überall“ ohne Abfrage immer `true`.

- [ ] **Schritt 1: Tests.** `TestMigration183GrantsAllRoles` (nach den Migrationen hat jede Rolle in `roles` beide Rechte ohne Abteilung; Labels und Kategorien wie oben). `TestCanViewRecDepartments` (Rolle mit `projects.view` nur in Abteilung A: Projekt an Anlage in A → true, in B → false, ohne Anlage → true, Gruppe aus A → true; gleiches für KVP über die Anlage). `TestCanViewRecInvolved` (ohne Recht: Ersteller, Verantwortlicher, Zugewiesener, Gruppenmitglied → true; nach Entfernen der Verantwortung → false; KVP: Einreicher, Mitwirkender, Prüfer, Maßnahmen-Verantwortlicher → true). `TestCanViewRecEditIsNotView` (nur `projects.edit` bzw. `kvp.manage` → false). `TestCanViewRecAreaAdmin` (`projects.admin` → true). `TestViewFilterMatchesCanViewRec` (für 6 gemischte Datensätze gleiches Ergebnis wie `canViewRec`; bei Recht überall keine Datenbankabfrage nötig). `TestCanViewRecOtherTypes` (`ticket` → true).
- [ ] **Schritt 2:** `PDH_TEST_DSN=… go test ./internal/web -run 'Migration183|CanViewRec|ViewFilter'` scheitert.
- [ ] **Schritt 3: Umsetzen.** Migration nach dem Muster von `migrations/159_setup_times.up.sql` (Rechte anlegen `ON CONFLICT (key) DO NOTHING`; `INSERT INTO role_permissions (role_id, permission_id) SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE p.key IN (…) ON CONFLICT … DO NOTHING`), Kopfkommentar „niemand wird durch das Update ausgesperrt“. Prüfsumme eintragen.
- [ ] **Schritt 4:** Tests grün; `go test ./pkg/database` grün.
- [ ] **Schritt 5:** Commit „Rechte: Projekte und KVP ansehen – Recht und gemeinsame Prüfung“.

### Task 2: Projekte – Seiten, Schnittstelle, Datensatz-Bausteine; Historie Aufgabe/Wartung

**Files:**
- Modify: `internal/web/nav.go` (Eintrag `projects` → `needs(permProjectsView)`), `internal/web/projects_handler.go` (Liste, Detail), `internal/web/dept_scope.go` (`APIDepartmentScope`), `internal/web/record_links.go` (`canViewRecord`, `historyModules`), `internal/web/fieldsets.go` (`ViewPerm` für `project`/`kvp`), `internal/web/categories.go` (Seite „Kategorien“ und `/categories/filter/{module}`), `internal/web/perm_gate.go` bzw. die Projekt-Schreibwege, `web/templates/projects.gohtml`, Tests (`history_test.go`, `nav_test.go`, neue `projects_view_test.go`)

**Interfaces – Consumes:** Task 1 (`canViewRec`, `mayViewAny`, `viewFilter`, `permProjectsView`).

**Produces:** `canViewRecord(r, m, id)` liefert für Module in `viewRights` `h.canViewRec(r, m.Key, id)` (damit gelten `/records/fields|links|meta` und die Historie automatisch). `fieldModules`: `ViewPerm` von `project` = `permProjectsView`, von `kvp` = `permKVPView`. `historyModules`: `project`, `kvp` → `historyViewRight(...)`; `task`, `maintenance_task` → `historyLogin`.

- [ ] **Schritt 1: Tests.** `TestProjectsNavNeedsView`. `TestProjectsListFiltered` (ohne Recht: nur beteiligte Projekte, Überschrift „Meine Projekte“; mit Recht in A: nur A und ohne Abteilung). `TestProjectDetailNeedsView` (403/„Keine Berechtigung“ ohne Recht und unbeteiligt; beteiligt 200). `TestProjectAPIViewRights` (GET Liste gefiltert, GET einzeln 403 – auch für einen Benutzer **ohne** Abteilungssperre). `TestProjectEditNeedsView` (Review Focus 1). `TestRecordsBlocksProjectView` (`/records/fields|links|meta|history/project/{id}` ohne Recht 403). `TestCategoriesHidesWithoutView` (Projekt- und KVP-Titel fehlen ohne Recht). `TestHistoryLikeThePage` angepasst: Aufgabe und Wartung mit nur Anmeldung → 200; `TestHistoryFragmentPerms` für `project`/`kvp` ohne Recht → 403.
- [ ] **Schritt 2:** `go test ./internal/web -run 'Project|RecordsBlocks|Categories|History|Nav'` scheitert.
- [ ] **Schritt 3: Umsetzen.** `APIDepartmentScope`: für `project` zusätzlich (auch wenn `deptScopeFor` nil ist) einzeln `canViewRec` → 403 `{"success":false,"error":"kein zugriff"}`, Liste über `viewFilter` im selben Puffer-Filter. Schreibwege eines Projekts (API-Bearbeiten, Beteiligte/Gruppe/Partner) lehnen ab, wenn `!canViewRec`.
- [ ] **Schritt 4:** gezielte Tests grün, dann `go test ./internal/web` (ganzes Paket) grün.
- [ ] **Schritt 5:** Commit „Rechte: Projekte nur mit Ansichtsrecht oder beteiligt; Historie Aufgabe/Wartung wie die Seite“.

### Task 3: Projekte – übrige Stellen

**Files:**
- Modify: `internal/web/tasks_handler.go` (Projektauswahl im Aufgaben-Formular), `internal/web/partners.go` (`partnerRecords`, `partnerRecordOptions`), `internal/web/user_detail.go` (Zuständigkeiten), `internal/web/infra_history.go` (Projekt-Einträge), `internal/web/copilot_data.go` (Art `project`: `Perm` und Datensatzfilter), `internal/web/calendar_board.go` (Projektzeilen, Seite und Leitstand mit Anmeldung) + Tests
- Vorher: `grep -rn "FROM projects\|JOIN projects" internal/` – jede weitere Stelle, an der Projekttitel oder -inhalte bei einem Benutzer ankommen, bekommt denselben Filter (Ausnahmen: Global Constraints „Bleibt sichtbar“). Liste der Fundstellen mit Entscheidung in den Bericht.

**Interfaces – Consumes:** Task 1 (`viewFilter`, `mayViewAny`).

- [ ] **Schritt 1: Tests** je Stelle `…HidesWithoutView`: `TestTaskFormProjectOptionsHidesWithoutView`, `TestPartnerRecordsHidesWithoutView` (Liste und Auswahl), `TestUserDetailProjectsHidesWithoutView` (eigene Zuständigkeiten bleiben sichtbar), `TestInfraHistoryProjectsHidesWithoutView`, `TestCopilotProjectsHidesWithoutView`, `TestCalendarProjectsHidesWithoutView`; dazu je ein Gegenfall mit Recht.
- [ ] **Schritt 2–4:** scheitern, umsetzen, `go test ./internal/web` grün.
- [ ] **Schritt 5:** Commit „Rechte: Projekte auch in Aufgaben, Partnern, Kalender, Copilot und Anlagen-Historie nur mit Ansichtsrecht“.

### Task 4: KVP

**Files:**
- Modify: `internal/web/kvp.go` (`KVPPage`, `listKVP`, Zählung, `kvpDashboard`, `KVPDetailPage`, `KVPPrintPage`, Schreibwege), `internal/web/record_comments.go` bzw. `record_blocks.go` (Kommentare `kvp` → `canViewRec`), `internal/web/dashboard_widgets.go` (`stat_kvp`: `Perm: permKVPView`), `web/templates/kvp.gohtml` (Satz „Alle sehen alle Vorschläge“ ersetzen; ohne Recht nur „Meine Vorschläge“ + „Idee einreichen“), `web/templates/kvp_detail.gohtml` falls nötig, Tests (`kvp_test.go`, `record_comments_test.go`, `perm_gate_test.go`)
- Vorher: `grep -rn "kvp_ideas\|kvp_" internal/web` – weitere Stellen wie in Task 3 behandeln, Liste in den Bericht.

**Interfaces – Consumes:** Task 1, Task 2 (`canViewRecord` für `kvp`).

- [ ] **Schritt 1: Tests.** `TestKVPPageWithoutView` (nur eigene/beteiligte Vorschläge, keine Kennzahlen Einsparung/Prämie/Zählung je Person, keine Tafel; Knopf „Idee einreichen“ da). `TestKVPPageWithView` (wie bisher). `TestKVPSubmitterWithoutView` (einreichen 200; danach Detail, Druck, Kommentare lesen und schreiben 200). `TestKVPDetailNeedsView` (unbeteiligt ohne Recht: Detail, Druck, `/records/comments/kvp/{id}` GET und POST → 403). `TestKVPManageWithoutView` (`kvp.manage` ohne `kvp.view`: fremder Vorschlag 403, Bewerten/Status 403). `TestStatKVPWidgetNeedsView`. `TestCommentsBlockPerms` und `TestKVPCommentOldRoute` an die neue Regel angepasst; `perm_gate_test.go` unverändert offen (die Prüfung sitzt im Handler).
- [ ] **Schritt 2–4:** scheitern, umsetzen, `go test ./internal/web` grün.
- [ ] **Schritt 5:** Commit „Rechte: KVP-Vorschläge nur mit Ansichtsrecht oder beteiligt, Einreichen für alle“.

### Task 5: Sicherheit – Leitstand-Kalender für Gäste, keine Ordnerlisten

**Files:**
- Modify: `internal/web/calendar_board.go` (`CalendarBoardFragment`, `buildCalendar`), `internal/web/uploads.go` (`UploadsHandler`) + Tests (`calendar_board_test.go` o. ä., `uploads_test.go`)
- Vorher: prüfen, dass keine Seite und kein Skript des PDH Ordnerlisten unter `/uploads/` lädt (`grep -rn "uploads/" web/ internal/`); Ergebnis in den Bericht.

- **Ergänzt nach der Prüfung von Task 4 (19.11.2026):** Pfadabschnitte, die eine UUID in **anderer** Schreibweise sind (32 Hex-Zeichen ohne Bindestriche, `{…}`, `urn:uuid:…`), werden zentral abgelehnt (404), bevor Abteilungssperre, Gate und Handler laufen – sonst greifen `scopePaths`/`gateRules` (kennen nur die Normalform) nicht, während Postgres die Schreibweise annimmt (z. B. `GET /api/v1/projects/<32 hex>` lieferte das Projekt ungeprüft). Test `TestNonCanonicalUUIDPathRejected` (Web- und `/api/v1`-Pfade; Normalform weiter 200).

- [ ] **Schritt 1: Tests.** `TestCalendarGuestIgnoresQuery` (Gast mit `?b=<slug>&set=1&show=project&show=fault&dept=<andere>&group=<andere>&user=<x>` → nur Arten, Abteilung und Gruppe des Leitstands; Projekt einer anderen Abteilung fehlt). `TestCalendarGuestWithoutBoard` (Gast ohne bzw. mit ungültigem `b` → 404). `TestCalendarBoardLoggedInRespectsRights` (angemeldet ohne `projects.view`: keine Projektzeilen). `TestUploadsNoDirectoryListing` (`/uploads/`, `/uploads/ticket/`, `/uploads/ticket` → 404; eine vorhandene Datei → 200; `uploads/esppdh` weiterhin 404).
- [ ] **Schritt 2–4:** scheitern, umsetzen (Ordner erkennen über `os.Stat` des bereinigten Pfads im Upload-Verzeichnis → 404), `go test ./internal/web` grün.
- [ ] **Schritt 5:** Commit „Sicherheit: Leitstand-Kalender zeigt Gästen nur seine Einstellung; keine Ordnerlisten unter /uploads/“.

### Task 6: Handbuch, Version, Änderungsprotokoll, Gesamtlauf

**Files:** `web/templates/help.gohtml`, ggf. `web/templates/widgets/help_figures.gohtml`, `web/i18n/{en,mk,ro,tr}.json` (neue Oberflächentexte), `VERSION`, `CHANGELOG.md`, `AGENTS.md`

- [ ] **Schritt 1: Handbuch.** Kapitel `tasks` (Projekte): wer Projekte sieht, „Meine Projekte“. Kapitel `kvp`: „Alle sehen alle Vorschläge“ ersetzen – einreichen dürfen alle, alle Vorschläge sieht, wer „KVP: alle Vorschläge ansehen“ hat, sonst „Meine Vorschläge“. Kapitel `admin`: die zwei Rechte, „bearbeiten/steuern ohne ansehen wirkt nur an eigenen Vorgängen“, Beteiligte sehen immer, Historie von Aufgaben und Wartung wie die Seite (Satz „Historie“ dort anpassen).
- [ ] **Schritt 2:** `VERSION` → `0.82.3`; `CHANGELOG.md` `## [0.82.3] – <Datum>`: `### Neu` (zwei Rechte; Beteiligte sehen immer), `### Geändert` (KVP-Seite ohne Recht zeigt nur eigene Vorschläge; Historie von Aufgaben und Wartung wie die Seite), `### Sicherheit` (Leitstand-Kalender für Gäste; keine Ordnerlisten unter `/uploads/`) – ein Satz je Punkt, Anwendersprache. `AGENTS.md` Punkt 6 (a): „gebaut, Freigabe für Merge offen“.
- [ ] **Schritt 3:** `go build ./... && go vet ./... && GOOS=linux go vet ./...`; `go test ./...` mit Labor-Datenbank (`PDH_TEST_DSN`, `PDH_TEST_MSSQL_DSN`, `PDH_TEST_SNMP`).
- [ ] **Schritt 4:** Commit „Version 0.82.3: Ansichtsrecht für Projekte und KVP“.
- [ ] **Schritt 5 (Controller, nach Freigabe des Anwenders):** Prüfung vor dem Merge laut AGENTS.md, Merge, Push, Verbund-Update, Browserprüfung (Rolle ohne Rechte: Menü, Projekte-Liste, KVP-Seite; Leitstand-Kalender als Gast).

## Nur im Browser bzw. am Testverbund prüfbar

- Menü und KVP-Seite einer Rolle ohne die neuen Rechte (Fiori/Fluent, hell/dunkel).
- Leitstand-Kalender als Gast mit manipulierter Adresse.
- `GET /uploads/` am Testverbund → 404.
