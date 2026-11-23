# PDH-App Paket 3 – Arbeiten ohne Verbindung – Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Die App arbeitet ohne Verbindung weiter (Vorgänge ansehen und bearbeiten, Störung melden), merkt sich jede Handlung als Schritt mit Telefon-Kennung und führt beim Wiederverbinden nach den Regeln aus Spec 9.3 zusammen: Status im PDH gilt, nur Hinweise, alles andere wird angehängt, nie ein neuer Datensatz.

**Architecture:** Teil S (pdh-server, Branch `feature/pdh-app-paket3`): Migration 205 (ursprünglich 195, nach dem Merge mit main umnummeriert) mit `app_steps` (Schritt genau einmal, lange aufbewahrt, Ergebnis-Kennung) und `app_id_aliases` (Telefon-Kennung → bestehender Datensatz). Der Wächter `appWrite` schreibt in `app_steps` statt `request_once`. Störungen nehmen die Telefon-Kennung als `id`. Schreibende Wege kennen einen „Stand“ (`base`) im Körper; ist er da, gelten die Zusammenführ-Regeln, sonst bleibt alles wie in Paket 2 (alte App N-1). Teil A (`C:\workplace\pdh-app`): verschlüsselte lokale SQLite (`LocalDb`), Warteschlange (`Outbox`) und Abgleich (`SyncEngine`). Alle Bildschirme lesen aus dem lokalen Speicher und schreiben über die Warteschlange, auch mit Verbindung (ein Weg, kein Sonderfall).

**Tech Stack:** Go/chi/pgx/PostgreSQL 16; Flutter 3.50.1 / Dart 3.14, neu `sqflite_sqlcipher` (Gerät), `sqflite_common_ffi` (nur Tests), `connectivity_plus`.

**Spec:** `docs/superpowers/specs/2026-11-21-pdh-app-design.md` (Abschnitte 9.1–9.4, 11, 16 Paket 3). Vorgänger: `docs/superpowers/plans/2026-11-21-pdh-app-paket2.md`. Verschobene Punkte: `C:\workplace\pdh-app\docs\offene-punkte.md` („Paket 3“).

## Global Constraints

- Migrationen nur hinzufügend, nie bestehende ändern; nächste freie Nummer auf `origin/main` (heute **205**); `migrations/checksums.txt` mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen` fortschreiben.
- Jede Datensatz-`id` ist `UUID … DEFAULT pdh_uuid_v7()`. Kennungen vom Telefon sind UUIDv7: Versions-Nibble `7`, Variante `8–b`. Prüfung serverseitig mit `appUUIDv7Re = ^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`.
- Rückwärtskompatibel: Eine App von Paket 2 (ohne `base`, ohne `id` im Meldekörper) verhält sich exakt wie bisher. Neue Antwortfelder sind nur Ergänzungen.
- Zusammenführ-Regeln (Spec 9.3, verbindlich):
  1. Status im PDH gilt. Ein Statusschritt (`start`, `finish`), dessen `base.status` ≠ aktueller Status, entfällt ohne Rückfrage.
  2. Dann gibt es nur einen Hinweis in der Antwort (`hint`), der zugleich im Verlauf des Vorgangs steht.
  3. Kommentare und Maßnahmen werden angehängt, auch an geschlossene Vorgänge.
  4. Nie ein neuer Datensatz als Ersatz.
  5. Eine offline gemeldete Störung wird an eine beim Eintreffen offene Störung derselben Anlage gehängt, mit Alias.
  6. Ist ein Recht entzogen, wird der Schritt abgelehnt und mit Grund angezeigt.
- Hinweistexte wörtlich:
  - Status geändert: `„<Titel>“ wurde inzwischen von <Name> auf „<Status>“ gesetzt.`, ohne bekannten Namen `„<Titel>“ wurde inzwischen auf „<Status>“ gesetzt.`
  - Text angehängt: Zusatz ` Dein Eintrag wurde angehängt.`
  - Zusammengeführte Meldung: `An der Anlage ist bereits die Störung „<Titel>“ offen. Deine Meldung wurde dort angehängt.`
- Lagerbuchungen und Kanban nur mit Verbindung. Offline zeigt die App wörtlich „Nur mit Verbindung möglich“.
- Balken oben (wörtlich):
  - ohne Verbindung: `Keine Verbindung – n Änderungen warten`, bei n = 0 `Keine Verbindung`
  - mit Verbindung und n > 0: `n Änderungen werden übertragen`
  - Er verschwindet erst, wenn alles übertragen ist.
  - Bei n = 1 heißt es „Änderung“, bei n = 1 wartend „1 Änderung wartet“.
- Lokale Datenbank verschlüsselt. Schlüssel 32 Zufallsbytes in `SecureStore`. Bei `app_revoked` und „App zurücksetzen“ werden Datenbank und Schlüssel gelöscht.
- Nur `app_revoked` löscht Daten. Netzfehler, 5xx, `app_unavailable`, `app_maintenance`, `app_read_only` und `app_rate_limited` sind vorübergehend: Der Schritt bleibt in der Warteschlange.
- Version Server: nächste Korrekturversion über `origin/main` zum Zeitpunkt des Merges (heute 0.82.24 → 0.82.25). App: `1.1.0+2`. Handbuch (Kapitel `pdh-app`) und CHANGELOG mitpflegen. Texte deutsch.
- Kein Push, kein Merge, kein Einspielen ohne ausdrückliche Freigabe des Anwenders.

## Review Focus

1. **Verbindung reißt nach dem Senden ab, die Antwort kommt nie an.** Wiederholt die App denselben Schritt nach Tagen (> 2 Tage), wird nichts doppelt angelegt. Die Antwort enthält den aktuellen Stand bzw. dieselbe Störungs-`id` (Test in S1, S2).
2. **Zwei Leute am selben Vorgang.** Das Telefon schließt offline ab, im Web wurde inzwischen geschlossen. Ergebnis: kein zweiter Abschluss, der Text hängt am Vorgang, der Hinweis steht im Verlauf und in der App (Test in S3, A3).
3. **Offline gemeldete Störung, dann sofort ein Foto bzw. eine Maßnahme dazu** (Schritt zeigt auf die Telefon-Kennung). Der Server führt die Meldung mit einer bestehenden Störung zusammen. Die Maßnahme landet bei der bestehenden Störung, die App zeigt deren Kennung (Test in S2, S3, A4).
4. **Ein alter Schritt (Paket-2-App) mit einer Once aus `request_once`** trifft nach dem Update ein. Er wird als Wiederholung erkannt, nicht neu ausgeführt (Test in S1).
5. **Rechte während der Offline-Zeit entzogen oder Gerät gesperrt.** Abgelehnte Schritte stehen mit Grund in „Hinweise“, die Warteschlange läuft mit dem nächsten weiter. Eine Sperre löscht alles lokal (Test in A2).

---

## Teil S – Server (pdh-server, Branch `feature/pdh-app-paket3`, Worktree `C:/workplace/pdh-server-app`)

DB für Integrationstests: `pdh_test_app` (wie Paket 2). `go test -tags integration ./internal/web -run App`.

### Task S1: Schritte genau einmal – `app_steps` und `app_id_aliases`

**Files:**
- Create: `migrations/205_app_offline.up.sql`
- Modify: `migrations/checksums.txt`, `internal/web/app_write.go`
- Test: `internal/web/app_write_test.go` / `app_write_integration_test.go` (vorhandene erweitern)

**Interfaces:**
- Produces (Migration):
  ```sql
  CREATE TABLE IF NOT EXISTS app_steps (
      id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(), -- Kennung des Schritts vom Telefon (X-PDH-Once)
      user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      device_id   UUID REFERENCES app_devices(id) ON DELETE SET NULL,
      path        TEXT NOT NULL,
      result_type TEXT,
      result_id   UUID,
      created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
  );
  CREATE INDEX IF NOT EXISTS app_steps_created ON app_steps (created_at);
  CREATE TABLE IF NOT EXISTS app_id_aliases (
      id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(), -- Kennung vom Telefon
      target_type TEXT NOT NULL,
      target_id   UUID NOT NULL,
      created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
      created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
  );
  CREATE INDEX IF NOT EXISTS app_id_aliases_target ON app_id_aliases (target_type, target_id);
  ```
  Dazu Kommentare in Deutsch, die Zweck und Aufbewahrung nennen.
- Produces (Go):
  - `appWrite` trägt die Once in `app_steps` ein (`id`, `user_id`, `device_id = dev.ID`, `path`), statt in `request_once`.
  - Eine Once ist **bekannt**, wenn sie in `app_steps` steht **oder** in `request_once` (Übergang von Paket 2). Dabei gelten Besitzer- und Pfadprüfung wie bisher.
  - Aufräumen: `app_steps` älter als **90 Tage**. Die Aufräumzeile für `request_once` (2 Tage) bleibt für die Web-Wege unverändert, wenn sie dort hingehört. Sonst entfällt sie in `appWrite`.
  - Fehlschlag (Status ≥ 400 und nicht `appKeepOnce`) löscht die Zeile aus `app_steps`.
  - Neu `func appSetResult(r *http.Request, typ, id string)`: Der Handler merkt sich das Ergebnis. `appWrite` schreibt nach erfolgreichem Lauf `result_type`/`result_id`.
  - Neu `func appStepResult(r *http.Request) (typ, id string)`: liefert bei einem Replay das gespeicherte Ergebnis.
  - `appOnceLookup` liest beide Tabellen. Der Test-Haken bleibt austauschbar.

- [ ] **Step 1: Failing tests**
  - `TestMigration205` / `TestUUIDv7Policy` deckt die neuen Tabellen ab (die Policy-Prüfung soll für 205 grün sein).
  - `TestAppWriteStepsTable`:
    - Eine Once landet in `app_steps` mit `device_id`.
    - Eine Wiederholung nach künstlich 3 Tagen (`UPDATE app_steps SET created_at = NOW() - INTERVAL '3 days'`) ist ein Replay.
    - Eine Once, die nur in `request_once` steht (gleicher Benutzer und Pfad), ist ein Replay (Review Focus 4).
    - Ein fremder Benutzer bekommt 409 ohne Retry-After.
    - Bei 422 wird die Zeile gelöscht.
    - `appSetResult` landet in der Zeile, und `appStepResult` gibt das Ergebnis beim Replay zurück.
- [ ] **Step 2:** Run `go test -tags integration ./internal/web -run 'AppWrite|Migration'` → FAIL.
- [ ] **Step 3:** Migration schreiben, Checksumme fortschreiben, `appWrite` umbauen.
- [ ] **Step 4:** Run the same tests plus `go test ./pkg/database -run 'TestMigrationsFrozen'` → PASS. `TestUUIDv7Policy` meldet für 205 nichts. Die vorbestehenden Befunde auf main sind bekannt und bleiben unberührt.
- [ ] **Step 5:** Commit `feat(app): Schritte aus der App genau einmal, 90 Tage aufbewahrt (Migration 205)`.

### Task S2: Störung mit Telefon-Kennung und Zusammenführen an offener Störung

**Files:**
- Modify: `internal/modules/faults/model.go` (`CreateFaultInput.ID *string`, `json:"-"`), `internal/modules/faults/repository.go` (`insertFault`: `COALESCE($11::uuid, pdh_uuid_v7())`), `internal/modules/faults/service_handler.go` (`newFault` übernimmt `in.ID`), `internal/web/app_scan.go`
- Test: `internal/web/app_scan_integration_test.go`, `internal/modules/faults/*_test.go` falls dort Repository-Tests liegen

**Interfaces:**
- Consumes: S1 `appSetResult`, `appStepResult`, Tabelle `app_id_aliases`.
- Produces: `POST /api/app/v1/faults` akzeptiert zusätzlich:
  - `"id"`: UUIDv7 vom Telefon, optional.
  - `"offline": bool`, optional. Die Meldung entstand ohne Verbindung.
- Mit `id`:
  - Ungültiges Format → 422 „Kennung ist ungültig.“
  - Die neue Störung bekommt genau diese `id`.
  - `appSetResult(r, "fault", id)`.
  - Ein Replay antwortet mit `appStepResult` (200, gleiche Form wie beim ersten Mal, inkl. `merged`/`hint`, wenn damals zusammengeführt). `appFaultByOnce` bleibt nur für Aufrufe ohne `id`.
  - Existiert die `id` schon als Störung oder Alias, ohne dass es ein Replay ist → 409 `app_conflict` ohne Retry-After („Diese Kennung gehört zu einer anderen Aktion.“).
- Mit `offline: true` und `id`, Zusammenführen (Spec 9.3.5):
  - Gesucht wird die zuletzt gemeldete **offene** Störung derselben Anlage. Offen = Status nicht in `closedStatus` und nicht archiviert, so wie `appWorkCan` „geschlossen“ bestimmt.
  - Der Benutzer muss sie sehen dürfen (`appWorkCan(r, "fault", id).Access == appWorkOK`).
  - Gefunden:
    - Den Meldetext als Maßnahme anhängen, wenn `can.action`. Sonst als Verlaufseintrag `Meldung aus der App: <Titel> – <Text>`.
    - Historie mit dem Hinweistext.
    - `INSERT INTO app_id_aliases (id, target_type, target_id, created_by) VALUES (<Telefon-id>, 'fault', <bestehende>, uid)`.
    - Bei `plant_stopped` zusätzlich `markPlantStopped` auf der bestehenden.
    - `appSetResult(r, "fault", bestehende)`.
    - Antwort 200 `{"type":"fault","id":<bestehende>,"merged":true,"hint":"An der Anlage ist bereits die Störung „<Titel>“ offen. Deine Meldung wurde dort angehängt."}`.
  - Nicht gefunden (oder nicht sichtbar) → normal anlegen (201).
- Ohne `offline` wird immer neu angelegt, wie in Paket 2.

- [ ] **Step 1: Failing tests** `TestAppReportFaultOffline`:
  - Mit `id` gemeldet → Störung hat genau diese `id`.
  - Dieselbe Once nach 3 Tagen → 200 mit derselben `id`, keine zweite Störung (Review Focus 1).
  - `id` kein v7 → 422.
  - `id` einer fremden vorhandenen Störung → 409.
  - `offline:true` bei offener Störung an der Anlage → 200, `merged:true`, `id` der bestehenden, Maßnahme angehängt, Alias gespeichert, keine neue Störung.
  - Dasselbe bei geschlossener Störung an der Anlage → neue Störung mit der Telefon-`id`.
  - `offline:false` bei offener Störung → neue Störung.
  - Replay der zusammengeführten Meldung → wieder `merged:true` mit derselben `id`.
- [ ] **Step 2:** FAIL → **Step 3:** implementieren → **Step 4:** `go test ./internal/modules/faults/... && go test -tags integration ./internal/web -run 'AppReport|AppScan'` PASS.
- [ ] **Step 5:** Commit `feat(app): Störungen mit Kennung vom Telefon, Offline-Meldung an offene Störung anhängen`.

### Task S3: Zusammenführen an Vorgängen – Stand, Hinweis, Alias

**Files:**
- Modify: `internal/web/app_work.go`, `internal/web/app_work_actions.go`
- Test: `internal/web/app_work_integration_test.go`, `internal/web/app_work_actions_integration_test.go`

**Interfaces:**
- Consumes: S1, S2 (Aliase).
- Produces:
  - `func (h *Handler) appResolveID(ctx context.Context, kind, id string) (string, error)`. Gibt es keinen Datensatz `id` der Art, aber einen Alias mit `target_type = kind`, dann gilt `target_id`. Sonst bleibt `id`. Angewendet in `AppWorkDetailAPI` und am Anfang von `appWorkWrite`. Danach wird mit der aufgelösten `id` gearbeitet, und die Antwort trägt die bestehende `id` (Review Focus 3).
  - Detail- und Listenantwort tragen `"updated_at"` (RFC 3339, wie `appTime`). In der Liste prüfen, ob es schon da ist.
  - Körper der schreibenden Wege `start`, `actions`, `comments`, `steps/{stepID}` und `finish` kennt optional `"base": {"status": "<Status>", "updated_at": "<Zeit>"}`. Fehlt `base`, gilt alles wie in Paket 2.
  - Mit `base` gelten die Regeln aus Spec 9.3:
    - `start`/`finish`: Ist der aktuelle Status ≠ `base.status`, entfällt der Statuswechsel. Bei `finish` wird der Text angehängt (siehe unten). Ist der Text leer, wird nichts angehängt. Historie und Antwort bekommen `hint`: Status-Hinweis + bei angehängtem Text ` Dein Eintrag wurde angehängt.`
    - Der `<Name>` stammt aus dem jüngsten Verlaufseintrag, der den Status geändert hat, sofern ermittelbar. Sonst wird die Variante ohne Namen verwendet.
    - `finish`: Bei gleichem Status gilt Paket-2-Verhalten, inkl. 422 aus dem Modul.
    - `actions`/`comments` auf einem **geschlossenen** Vorgang (Paket 2: 403): Ist der Benutzer sichtbar beteiligt (`Access == appWorkOK`) und kein Betrachter, wird der Text als Verlaufseintrag `Nachtrag aus der App: <Text>` angehängt. Antwort 200 mit `hint` `„<Titel>“ ist inzwischen abgeschlossen. Dein Eintrag wurde im Verlauf angehängt.`
    - `steps` auf geschlossenem Vorgang: 200 mit `hint` `„<Titel>“ ist inzwischen abgeschlossen. Der Prüfpunkt wurde nicht mehr gespeichert.` (kein Schreiben).
    - Fehlendes Recht bei offenem Vorgang bleibt 403 (Regel 6).
  - Replay: Die Antwort trägt beim Replay denselben `hint` wie beim ersten Mal. Den `hint` als `result_type = "hint"` ist nicht möglich, weil `result_id` UUID ist. Deshalb wird der Hinweis beim Replay aus dem jüngsten Verlaufseintrag dieses Benutzers mit Hinweistext am Vorgang neu ermittelt. Die Implementierung entscheidet, ob das sauber geht. Sonst entfällt `hint` beim Replay und wird im Bericht vermerkt.

- [ ] **Step 1: Failing tests** `TestAppWorkMerge`:
  - Detail über Alias-`id` liefert die bestehende `id`. Eine Maßnahme über die Alias-`id` landet bei der bestehenden.
  - `updated_at` in Detail und Liste.
  - `finish` mit `base.status = in_progress`, Vorgang inzwischen im Web geschlossen → 200, nicht doppelt abgeschlossen, Text als Nachtrag im Verlauf, `hint` mit Namen des Schließenden (Review Focus 2).
  - `start` mit veraltetem `base` → kein Statuswechsel, `hint`.
  - `start`/`finish` mit passendem `base` → wie Paket 2.
  - `actions` mit `base` auf geschlossenem Vorgang → 200, Nachtrag im Verlauf.
  - Dasselbe ohne `base` → 403 wie bisher (Rückwärtskompatibilität).
  - Betrachter mit `base` → 403.
- [ ] **Step 2:** FAIL → **Step 3:** implementieren → **Step 4:** `go test -tags integration ./internal/web -run 'AppWork'` PASS.
- [ ] **Step 5:** Commit `feat(app): Schritte aus der App zusammenführen – Status im PDH gilt, Hinweise, Nachträge, Aliase`.

### Task S4: Anlagen für den Offline-Speicher, Handbuch, Änderungsprotokoll

**Files:**
- Create: `internal/web/app_assets.go`, Test `internal/web/app_assets_integration_test.go`
- Modify: `cmd/server/main.go` (Route), Handbuch-Kapitel `pdh-app` (wo `internal/web/help.go` es einbindet), `CHANGELOG.md`, `VERSION`, `AGENTS.md` (offener Punkt 22)

**Interfaces:**
- Produces: `GET /api/app/v1/assets` (hinter `AppAuth`).
  - Antwort: `{"items":[{"id","name","path"}],"truncated":bool}`.
  - Inhalt: Anlagen (`infrastructure`, nicht `hidden_at`), die im Bereich des Benutzers liegen (`appInfraInScope`-Logik als eine SQL-Abfrage, nicht je Zeile). Ohne `may_report` des Geräts bleibt die Liste leer.
  - Höchstens 5000, sortiert nach `path, name`. `path` wie in `/scan`.
- Handbuch („Mit der App ohne Verbindung arbeiten“, `.hb-howto`):
  - Balken
  - Was offline geht und was nur mit Verbindung
  - Was beim Zusammenführen passiert (Status im PDH gilt, Hinweise, Nachtrag, angehängte Meldung)
  - Abgelehnte Schritte
- CHANGELOG: nächste Korrekturversion über `origin/main`, Abschnitte `### Neu` und `### Sicherheit` (Schritte genau einmal 90 Tage, Rechte beim Eintreffen geprüft).

- [ ] **Step 1: Failing test** `TestAppAssets`:
  - Anlagen im Bereich, keine ausgeblendeten.
  - Abteilungsfremde fehlen.
  - Gerät ohne `may_create` → leere Liste.
  - `truncated` bei Grenze (Grenze im Test über Variable `appAssetsMax` klein setzen).
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Ganzer Lauf: `go vet ./...`, `go test ./...`, `go test -tags integration ./internal/web -run 'App|Help'`, gofmt der geänderten Dateien.
- [ ] **Step 6:** Commit `feat(app): Anlagenliste für den Offline-Speicher; Handbuch und Änderungsprotokoll`.

## Teil A – App (`C:\workplace\pdh-app`, Branch `feature/paket3`)

Werkzeuge wie Paket 2: `C:\Users\michael\dev\flutter\bin\flutter`, `JAVA_HOME=C:\Users\michael\dev\jdk-17`, `ANDROID_HOME=C:\Users\michael\dev\android-sdk`, AVD `pdh_test`. Jede Aufgabe endet mit `flutter analyze` (0 Befunde) und `flutter test` (alles grün).

### Task A1: Verschlüsselter lokaler Speicher

**Files:**
- Create: `lib/core/local_db.dart`, `test/core/local_db_test.dart`
- Modify: `pubspec.yaml`, `lib/core/secure_store.dart` (DB-Schlüssel), `lib/core/session.dart` (Löschen bei Sperre/Zurücksetzen)

**Interfaces:**
- Produces:
  ```dart
  abstract class LocalDb {
    static Future<LocalDb> open(SecureStore store);          // Gerät: sqflite_sqlcipher, Datei pdh.db
    static Future<LocalDb> openForTest();                     // sqflite_common_ffi, im Speicher, unverschlüsselt
    // Vorgänge
    Future<void> putItems(List<Map<String, dynamic>> items);  // ersetzt die Liste „Meine Vorgänge“ ganz
    Future<List<Map<String, dynamic>>> items();
    Future<void> putDetail(String type, String id, Map<String, dynamic> json);
    Future<Map<String, dynamic>?> detail(String type, String id);
    Future<String?> detailUpdatedAt(String type, String id);
    // Anlagen
    Future<void> putAssets(List<Map<String, dynamic>> assets, {required bool truncated});
    Future<Map<String, dynamic>?> asset(String id);
    // Warteschlange
    Future<int> enqueue(OutboxEntry e);                       // Rückgabe: seq
    Future<List<OutboxEntry>> pending();                      // state = pending, nach seq
    Future<void> markDone(String stepId);
    Future<void> markRejected(String stepId, String code, String message);
    Future<int> pendingCount();
    // Kennungen und Hinweise
    Future<void> mapId(String phoneId, String realId);
    Future<String> resolveId(String id);                      // phoneId → realId, sonst id
    Future<void> addHint(Hint h);
    Future<List<Hint>> hints();
    Future<void> markHintsRead();
    Future<void> setMeta(String key, String value);
    Future<String?> meta(String key);
    Future<void> wipe();                                      // schließt, löscht Datei
  }
  class OutboxEntry { String stepId; String method; String path; Map<String,dynamic> body;
    String targetType; String targetId; String label; DateTime createdAt; String state; String? errorCode; String? errorMessage; }
  class Hint { String id; String text; String? targetType; String? targetId; DateTime at; bool read; bool rejected; }
  ```
- Tabellen: `work_items`, `work_details(type,id,json,updated_at)`, `assets(id,name,path)`, `outbox(seq INTEGER PRIMARY KEY AUTOINCREMENT, step_id TEXT UNIQUE, …)`, `id_map(phone_id PRIMARY KEY, real_id)`, `hints`, `meta`. Schemaversion 1 über `onCreate`/`onUpgrade`.
- Schlüssel: `SecureStore.readDbKey()`/`writeDbKey()`. Er wird beim ersten Öffnen erzeugt (32 Bytes `Random.secure`, hex) und als SQLCipher-Passwort übergeben. `SecureStore.wipe` löscht ihn mit.
- Löschen: Wo `session.dart` heute bei `app_revoked` bzw. „App zurücksetzen“ Token/Daten löscht, wird zusätzlich `LocalDb.wipe()` aufgerufen.
- Rückfall, nur wenn `sqflite_sqlcipher` mit Flutter 3.50.1/AGP des Projekts nicht baut: `sqflite` und AES-GCM je JSON-Spalte (Paket `cryptography`). Die Schnittstelle bleibt gleich. Der Bericht muss den Rückfall begründen.

- [ ] **Step 1: Failing tests** `local_db_test.dart`:
  - `putItems` ersetzt ganz.
  - `detailUpdatedAt`.
  - `enqueue` → `pending` in seq-Reihenfolge.
  - `markRejected` → nicht mehr pending, `pendingCount` sinkt.
  - Doppelte `stepId` → Fehler.
  - `mapId`/`resolveId`.
  - `wipe` → danach leer.
- [ ] **Step 2–4:** FAIL → implementieren → PASS. Zusätzlich `flutter build apk --debug` muss bauen (native Bibliothek).
- [ ] **Step 5:** Commit `feat: verschlüsselter lokaler Speicher`.

### Task A2: Warteschlange und Abgleich

**Files:**
- Create: `lib/core/sync_engine.dart`, `test/core/sync_engine_test.dart`
- Modify: `lib/core/api_errors.dart` (falls Retry-After/Code-Zuordnung fehlt), `test/support/fake_app_server.dart`

**Interfaces:**
- Consumes: A1 `LocalDb`, `ApiClient.get/post(…, once:)`, Fehlerklassen aus `api_errors.dart`.
- Produces:
  ```dart
  class SyncEngine extends ChangeNotifier {
    SyncEngine({required LocalDb db, required ApiClient? Function() client, Duration interval = const Duration(minutes: 2)});
    bool get online;              // letzte Server-Antwort erfolgreich
    int get pending;              // db.pendingCount, gecacht
    Future<String> submit({required String method, required String path, required Map<String,dynamic> body,
        required String targetType, required String targetId, required String label}); // erzeugt stepId (UUIDv7), enqueue, stößt flush an, Rückgabe stepId
    Future<void> flush();         // FIFO, einer nach dem anderen, nie parallel (Sperre)
    Future<void> refresh();       // GET /work → putItems; Details nur, wenn updated_at abweicht; /assets höchstens 1× je 24 h
    Future<void> syncNow();       // flush dann refresh
    void start(); void stop();    // Zeitgeber; bei App im Vordergrund
  }
  ```
- Regeln für `flush` je Antwort:
  - **2xx:**
    - `markDone`
    - `putDetail` mit der Antwort (Arbeitswege)
    - Antwort-`id` ≠ `targetId` → `mapId(targetId, id)`
    - Antwort mit `hint` → `addHint`
    - Störung: Antwort `{"type","id","merged","hint"}` → `mapId`, ggf. `addHint`.
  - **Vorübergehend** (Netzfehler, Timeout, 5xx, `app_unavailable`, `app_maintenance`, `app_read_only`, `app_rate_limited`, 409 `app_conflict` **mit** Retry-After): abbrechen, Eintrag bleibt, `online=false` nur bei Netzfehler.
  - **Endgültig** (`app_no_permission`, `app_not_found`, `app_invalid_input`, `app_too_large`, 409 **ohne** Retry-After):
    - `markRejected`
    - `addHint(rejected: true, text: "<label>: <Server-Text>")`
    - weiter mit dem nächsten Eintrag (Review Focus 5).
  - `app_revoked`: Ablauf wie Paket 2 (Sperre), danach `db.wipe()`.
  - `app_outdated`: anhalten, Update-Bildschirm wie bisher.
- Pfade mit Telefon-Kennung bleiben, wie sie sind (der Server löst Aliase auf). Für die Anzeige liefert `db.resolveId` die echte Kennung.
- Abgleich bei App-Start, alle `interval` im Vordergrund, nach jedem `submit` und wenn `connectivity_plus` eine Verbindung meldet. Ohne Verbindung prüft ein leichter `GET /server` alle 15 s, ob der Server wieder erreichbar ist.

- [ ] **Step 1: Failing tests** `sync_engine_test.dart` gegen `fake_app_server.dart`:
  - Drei Schritte offline eingereiht → Server aus → `flush` sendet nichts, `pending == 3`.
  - Server an → in Reihenfolge gesendet, jede `X-PDH-Once` = `stepId`.
  - Abbruch nach dem Senden (Fake schreibt, antwortet aber mit Netzfehler) → zweiter `flush` sendet dieselbe Once.
  - 403 beim zweiten von drei → zweiter rejected mit Hinweis, dritter gesendet.
  - 409 mit Retry-After → bleibt pending.
  - Antwort mit anderer `id` → `resolveId` liefert sie.
  - `hint` → in `hints()`.
  - `app_revoked` → `wipe` aufgerufen.
  - `refresh` holt Details nur bei geändertem `updated_at`.
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Commit `feat: Warteschlange und Abgleich für Arbeiten ohne Verbindung`.

### Task A3: Vorgänge offline – Bildschirme, Balken, Hinweise

**Files:**
- Modify: `lib/features/work/work_api.dart`, `work_list_screen.dart`, `work_detail_screen.dart`, `lib/core/connectivity_banner.dart`, `lib/features/home/home_screen.dart`, `lib/app.dart`/`session.dart` (SyncEngine erzeugen, starten, stoppen)
- Create: `lib/features/hints/hints_screen.dart`
- Test: `test/features/work_test.dart`, `test/features/work_offline_test.dart`, `test/core/connectivity_banner_test.dart`

**Interfaces:**
- Consumes: A1, A2.
- Produces:
  - `WorkApi.list()`/`detail()` lesen aus `LocalDb`. `syncNow` stößt das Neuladen an; „Ziehen zum Aktualisieren“ ruft `syncNow`.
  - `WorkApi.act(...)` → `SyncEngine.submit` mit `body + {"base": {"status": <lokal gesehener Status>, "updated_at": <lokal gesehenes updated_at>}}`.
  - Lokal sofort sichtbar:
    - Der Eintrag erscheint mit Kennzeichen „wartet“ unter dem Vorgang (Liste „Noch nicht übertragen“ aus `db.pending()` mit `targetId`).
    - Der Status wird **nicht** lokal geändert (Status im PDH gilt).
    - Nach Übertragung ersetzt die Server-Antwort das Detail.
  - Balken (`ConnectivityBanner`) liest `SyncEngine.online`/`pending` mit den Texten aus den Global Constraints. Er steht fest oben auf allen Bildschirmen nach der Einrichtung.
  - Kacheln/Liste zeigen `Stand: <HH:mm>` aus `meta('work_refreshed_at')`, wenn offline.
  - „Hinweise“: Eintrag auf der Startseite mit Anzahl ungelesener. `HintsScreen` listet Hinweise (abgelehnte mit Grund rot), Tippen öffnet den Vorgang (`resolveId`). Beim Öffnen gelten sie als gelesen.
  - Abgelehnte Schritte stehen zusätzlich im Detail des Vorgangs mit Grund, nie stumm verworfen.
  - Ein Vorgang, der nicht im Speicher liegt und offline geöffnet wird (z. B. Link): „Ohne Verbindung nicht verfügbar“.

- [ ] **Step 1: Failing widget tests**
  - Offline: Liste aus Speicher mit `Stand`, Balken „Keine Verbindung – 2 Änderungen warten“.
  - Maßnahme offline → erscheint als „wartet“, Balken zählt hoch.
  - Wieder online → Balken „2 Änderungen werden übertragen“, danach weg.
  - `hint` aus Antwort → Startseite zeigt „Hinweise (1)“.
  - Abgelehnter Schritt → im Detail und in Hinweisen mit Grund.
  - `act` sendet `base` mit lokal gesehenem Status (Review Focus 2).
  - Singular „1 Änderung wartet“.
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Commit `feat: Vorgänge ohne Verbindung bearbeiten, Hinweisbalken und Hinweise`.

### Task A4: Scannen und Melden ohne Verbindung

**Files:**
- Modify: `lib/features/scan/scan_logic.dart`, `scan_screen.dart`, `lib/features/report/report_screen.dart`
- Test: `test/features/scan_logic_test.dart`, `test/features/report_test.dart`

**Interfaces:**
- Consumes: A1 `asset()`, A2 `submit`, die Code-Erkennung aus Paket 2 (`links.dart`, `scan_logic.dart`).
- Produces:
  - Offline-Auflösung eines Codes: dieselben Formen wie der Server (`/a/<uuid>`, `/e/<uuid>`, bare UUID; Host muss eine der eigenen Serveradressen sein bzw. `pdh://open` mit eigener Domain).
    - Mit Treffer in `assets` → Anlagenansicht aus dem Speicher: Name, Pfad, offene Vorgänge aus `items()` mit `asset.id`. „Störung melden“ ist verfügbar, wenn die Anlage im Speicher liegt (die Liste ist für Geräte ohne Melderecht leer).
    - Lager-/Kanban-Codes (`/inventory/…`, `/k/…`) → „Nur mit Verbindung möglich“.
    - Unbekannt → „Ohne Verbindung nicht erkannt“.
  - Meldung: `submit(method: 'POST', path: '/faults', body: {"id": <UUIDv7>, "asset_id", "title", "text", "plant_stopped", "offline": <!engine.online beim Absenden>}, targetType: 'fault', targetId: <id>, label: 'Störung „<Titel>“')`.
    - Online wie offline derselbe Weg.
    - Lokal erscheint die Meldung sofort in „Meine Vorgänge“ als „wartet“.
    - Antwort `merged` → `mapId`, Hinweis, der lokale Eintrag verschwindet zugunsten der bestehenden Störung.
  - Mit Verbindung bleibt die Server-Auflösung `/scan` wie Paket 2. Der Speicher wird nur ohne Verbindung genutzt.

- [ ] **Step 1: Failing tests**
  - Offline-Scan `https://<eigene>/a/<id>` mit Anlage im Speicher → Anlagenansicht.
  - Fremder Host → nicht erkannt.
  - `/inventory/x` → „Nur mit Verbindung möglich“.
  - Offline-Meldung → Warteschlange mit `offline: true` und v7-`id`.
  - Antwort `merged` mit anderer `id` → Hinweis und `resolveId` (Review Focus 3).
  - Maßnahme auf die Telefon-Kennung vor der Übertragung → Pfad mit Telefon-Kennung, nach Übertragung im Detail der bestehenden Störung.
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Commit `feat: Anlagen scannen und Störungen melden ohne Verbindung`.

### Task A5: Ende-zu-Ende-Abnahme mit Flugmodus, Version 1.1.0

**Files:**
- Create: `docs/abnahme-paket3.md`, `docs/abnahme-paket3/` (Bildschirmfotos)
- Modify: `integration_test/` (neuer Ablauf `offline_test.dart`, wenn automatisierbar), `pubspec.yaml` (`version: 1.1.0+2`), `docs/offene-punkte.md`

- [ ] **Step 1:** Server vorbereiten wie Paket 2 (`docs/abnahme-paket2.md`): `go run ./cmd/server` aus `feature/pdh-app-paket3`, Labor-DB `pdh_test_app`, Port 8090, Adresse `http://10.0.2.2:8090`. Prüfkonto-Kennwörter nur im Scratchpad.
- [ ] **Step 2:** Ablauf am Emulator `pdh_test`. Flugmodus per `adb shell cmd connectivity airplane-mode enable|disable`.
  1. Online Liste laden.
  2. Flugmodus an → Balken „Keine Verbindung“.
  3. Maßnahme an Vorgang A und Abschluss von Vorgang B.
  4. Anlage per Code öffnen und Störung melden.
  5. Im Web am Rechner Vorgang B schließen und an derselben Anlage eine Störung melden.
  6. Flugmodus aus → Balken „… werden übertragen“, dann weg.
  7. Prüfen: Maßnahme an A; B nicht doppelt geschlossen, Nachtrag im Verlauf, Hinweis in der App; Meldung an der Web-Störung angehängt, Hinweis, keine zweite Störung.
  8. Im Web sperren → App löscht lokalen Speicher (Datei `pdh.db` weg: `adb shell run-as de.pdh.app ls databases`).
- [ ] **Step 3:** Bildschirmfotos und Ergebnis in `docs/abnahme-paket3.md`. `offene-punkte.md`: erledigte Paket-3-Punkte abhaken, neue verschobene eintragen. Die Wartungs-Checkliste, die beim Lesen schreibt, bleibt offen, falls nicht gelöst.
- [ ] **Step 4:** Labor-Prüfdaten entfernen. Commit `docs: Abnahme Paket 3 ohne Verbindung; Version 1.1.0`.

## Nicht in Paket 3

- Fotos, Zeitbuchung und Messwerte als Schritte: kommen mit den Funktionen selbst. Die Warteschlange ist dafür allgemein gebaut.
- Abgleich „seit Stand X“ als eigener Server-Weg: heute volle Liste plus Details nur bei geändertem `updated_at`.
- Cockpit-Kacheln mit Stand-Zeit (Paket 4), Push nach Abgleich (Paket 5), Lager offline.
