# Abgleich mit Fremdsystemen (Paket 1) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Meldungen aus einer fremden SQL-Server-Datenbank entstehen im PDH als Ticket oder Störmeldung, Änderungen im PDH gehen in die fremde Zeile zurück – nach festen, im Cockpit gepflegten und getesteten Regeln, ohne je in der fremden Datenbank zu löschen.

**Architecture:** Neues Paket `internal/modules/extsync` mit drei getrennten Teilen: Speicher (PDH-Postgres, pgx), Dialekt (erzeugt die einzigen zwei erlaubten Schreibbefehle für die fremde Datenbank, `database/sql`) und Ablauf (hereinkommend im Takt, hinausgehend über eine dauerhafte Warteliste). Die Warteliste füllen Postgres-Auslöser an `tickets`, `faults`, `ticket_comments` und `infrastructure_comments`, nur für verknüpfte Datensätze; eigene Änderungen des Abgleichs kennzeichnet eine Sitzungsvariable. Die Oberfläche liegt in `internal/web/sync_*.go` und nutzt dieselben Funktionen, die später Paket 2 (KI-Einrichtung) aufruft.

**Tech Stack:** Go 1.24, chi, html/template, htmx, Postgres (pgx v5), `github.com/microsoft/go-mssqldb v1.7.2`, WireGuard-Teil des PDH (nur lesend).

**Spec:** `docs/superpowers/specs/2026-11-13-abgleich-fremdsysteme-design.md`

## Global Constraints

- In der fremden Datenbank **nie** `DELETE`, `TRUNCATE`, `DROP`, `MERGE` und kein `INSERT` in die Meldungstabelle. Schreibend gibt es nur `UPDATE` einer Zeile über ihren Schlüssel und `INSERT` in die Nebentabelle.
- Namen von Tabellen und Spalten nur aus dem gelesenen Aufbau, gequotet; Werte nur als Parameter. Kein freier SQL-Text beim Schreiben.
- Zusatzfunktion: ohne Profil ändert sich am PDH nichts; ein Fehler im Abgleich hält weder PDH noch Rollentausch auf.
- Nur am aktiven Server (Start in `cmd/server/main.go` im Block `if !mirrorOn`), in der Wartungssperre ruht alles (`h.MaintLocked`).
- Vorgaben: Obergrenze je Lauf 200; Zeitgrenze je Abfrage 15 s direkt, 60 s über VPN; Takt über VPN 5 min; Pause nach Fehlern höchstens 15 min; Warteliste alle 3 s.
- Rechte: `sync.view`, `sync.edit`, `sync.run`, `sync.admin` (Kategorie „Daten & Schnittstellen“); Datensatzprüfung mit `h.hasPermRec(r, recht, "sync_profile", id)`; Löschen nur `sync.admin`.
- Migration 158 (vor dem Merge gegen `origin/main` prüfen, sonst umnummerieren). Version 0.80.2 (vor dem Merge prüfen).
- Texte, Handbuch, Änderungsprotokoll, Commit-Nachrichten auf Deutsch in Anwendersprache; keine Attributionszeilen.
- Farben nur über CSS-Variablen; jede neue Oberfläche in Fiori und Fluent, hell und dunkel.
- Nie `git add -A` (unversioniertes `.claude/` bleibt draußen). Kein Push, kein Einspielen auf 10.10.0.14/.17, kein Merge ohne ausdrückliche Freigabe des Anwenders. 10.10.0.202 wird nie angefasst.
- Tests laufen in Git Bash: `go test ./internal/modules/extsync/...` (schnell), `go test ./internal/web/` (7–8 min). Tests mit PDH-Datenbank über `PDH_TEST_DSN`, Tests mit SQL Server über `PDH_TEST_MSSQL_DSN`; ohne Variable `t.Skip`.

## Review Focus

1. **Fremde Zeile mit zusammengesetztem oder nicht-ganzzahligem Schlüssel** (GUID, Text mit Leerzeichen, zwei Spalten): Verknüpfung und `UPDATE … WHERE` treffen genau eine Zeile. → Test in Task 5 und 6.
2. **Echo:** Was das PDH zurückschreibt, kommt im nächsten Lauf als „geändert“ zurück (neue `rowversion`). Es darf weder ein Verlaufseintrag noch ein neuer Wartelisteneintrag entstehen. → Test in Task 8.
3. **Leere und unpassende Werte:** `NULL`, leerer Titel, Text länger als das Zielfeld, Datum als Text, unbekannter Statuswert. Nur diese Zeile scheitert, mit Grund im Lauf; der Lauf geht weiter. → Test in Task 7 und 8.
4. **Abbruch mitten im Lauf** (Verbindung reißt, Prozess endet nach dem Anlegen des Tickets, vor dem Schreiben der Verknüpfung): kein doppeltes Ticket beim nächsten Lauf. → Verknüpfung zuerst reservieren, Herkunftseintrag direkt nach dem Anlegen; Test in Task 8.
5. **Kennwort beim Bearbeiten einer Verbindung leer gelassen:** das gespeicherte Kennwort bleibt; das Kennwort erscheint nirgends im HTML (auch nicht in `data-conn-config`). → Test in Task 3.

---

## Dateien

| Datei | Aufgabe |
|---|---|
| `deploy/lab/mssql/docker-compose.yml`, `seed.sql` | Labor: SQL Server mit Tabellen `Meldungen`, `MeldungKommentare`, ein Auslöser |
| `migrations/158_extsync.up.sql` | Tabellen, Rechte, Auslöser |
| `internal/web/conn_secrets.go` | Kennwörter der Verbindungen ver- und entschlüsseln |
| `internal/web/conn_route.go` | Weg, VPN-Prüfung, Zeitgrenzen, Fingerabdruck |
| `internal/modules/extsync/model.go` | Typen, Feldkatalog |
| `internal/modules/extsync/store.go` | Profile, Zuordnungen, Verknüpfungen, Warteliste, Läufe |
| `internal/modules/extsync/dialect.go`, `dialect_mssql.go` | Aufbau lesen, Befehle erzeugen |
| `internal/modules/extsync/mapping.go` | Übersetzen, Prüfwert, Widerspruch |
| `internal/modules/extsync/target.go` | Ticket und Störmeldung anlegen, ändern, lesen |
| `internal/modules/extsync/inbound.go`, `outbound.go`, `probe.go`, `runner.go` | Abläufe |
| `internal/web/sync_pages.go`, `sync_actions.go`, `sync_wire.go` | Seiten, Aktionen, Verdrahtung |
| `web/templates/sync.gohtml`, `sync_profile.gohtml`, `widgets/sync_*.gohtml` | Übersicht, Cockpit |
| `web/templates/help.gohtml`, `widgets/help_figures.gohtml`, `internal/web/help.go` | Handbuch |

---

### Task 1: Labor und Messungen

**Files:**
- Create: `deploy/lab/mssql/docker-compose.yml`, `deploy/lab/mssql/seed.sql`, `deploy/lab/mssql/LIESMICH.md`
- Modify: `docs/superpowers/specs/2026-11-13-abgleich-fremdsysteme-design.md` (Abschnitt „Offene Punkte“ → Messergebnisse)

**Interfaces:**
- Produces: SQL Server auf dem Windows-PC, Port 14330, Datenbank `Fremd`, Anmeldung `pdh_sync`. `PDH_TEST_MSSQL_DSN=sqlserver://pdh_sync:<kennwort>@127.0.0.1:14330?database=Fremd&encrypt=disable`.
  Tabellen: `dbo.Meldungen(MeldungNr INT IDENTITY PK, Titel NVARCHAR(200) NOT NULL, Beschreibung NVARCHAR(MAX) NULL, Prio NVARCHAR(10) NULL, Zustand NVARCHAR(20) NOT NULL, Faellig DATETIME2 NULL, Geaendert DATETIME2 NOT NULL, RV ROWVERSION)`, `dbo.MeldungKommentare(Id INT IDENTITY PK, MeldungNr INT NOT NULL, Text NVARCHAR(MAX), Verfasser NVARCHAR(100), Zeit DATETIME2)`, `dbo.MeldungenGuid(Id UNIQUEIDENTIFIER, Werk NVARCHAR(10), Titel NVARCHAR(200) NOT NULL, Zustand NVARCHAR(20) NOT NULL, PRIMARY KEY (Id, Werk))`, Auslöser `trg_Meldungen_Geaendert` (setzt `Geaendert`).
  Der Benutzer `pdh_sync` hat `SELECT` auf alle drei, `UPDATE` auf `Meldungen` und `MeldungenGuid`, `INSERT` auf `MeldungKommentare` – **kein** `DELETE`.

- [ ] **Step 1:** Compose-Datei mit `mcr.microsoft.com/mssql/server:2022-latest`, Projektname `pdh-lab-mssql` (die Behälter `pdh-ha-witness-*` nicht anfassen), `seed.sql` mit 5 Beispielzeilen. Start: `docker compose -p pdh-lab-mssql -f deploy/lab/mssql/docker-compose.yml up -d`.
- [ ] **Step 2:** Prüfen: `docker exec pdh-lab-mssql-db-1 /opt/mssql-tools18/bin/sqlcmd -C -U pdh_sync -P … -d Fremd -Q "SELECT COUNT(*) FROM dbo.Meldungen"` → 5; `DELETE FROM dbo.Meldungen` als `pdh_sync` → Fehler „permission was denied“.
- [ ] **Step 3: Messung A (Tunnel).** Nur lesend auf dem Testverbund: `ssh -o BatchMode=yes -o ConnectTimeout=8 michael@10.10.0.17 'ip -br addr | grep pdhout; ip route'`. Ergebnis festhalten: geht der Weg zu einer Adresse aus `routes` über `pdhoutN`? Gibt es keinen ausgehenden Tunnel am Testverbund, das festhalten; die Abnahme über VPN richtet dann der Anwender ein (Rückfrage, nichts selbst anlegen).
- [ ] **Step 4: Messung B (Tausch).** Aus dem Code beantworten und belegen (`internal/web/vpn.go`, Dienst `pdh-wireguard`): Auf welchem Server laufen ausgehende Tunnel, was passiert beim Rollentausch? Kein Rollentausch nur für die Messung.
- [ ] **Step 5: Messung C (Absender).** `ssh … 'ip route get 10.10.0.1'` auf beiden Servern: Welche Absenderadresse nutzt der aktive Server (eigene Adresse, nicht die mitwandernde)? Festhalten: Im Fremdnetz müssen beide Serveradressen freigegeben sein.
- [ ] **Step 6:** Ergebnisse in die Spec schreiben („Messergebnisse 4.7“). Commit: `Labor für den Abgleich: SQL Server mit Beispieltabellen; Messungen zu Tunnel, Rollentausch und Absenderadresse`.

### Task 2: Migration 158 – Tabellen, Rechte, Auslöser

**Files:**
- Create: `migrations/158_extsync.up.sql`
- Test: `internal/modules/extsync/migration_integration_test.go`

**Interfaces:**
- Produces (Tabellen, alle `id UUID PRIMARY KEY DEFAULT gen_random_uuid()`, Zeiten `TIMESTAMPTZ`):
  - `sync_profiles(name, connection_id → import_export_connections ON DELETE RESTRICT, department_id NULL → departments, target_type CHECK IN ('ticket','fault'), source_schema, source_table, key_columns TEXT[] NOT NULL, change_column NULL, change_kind CHECK IN ('none','timestamp','rowversion') DEFAULT 'none', filter_sql TEXT DEFAULT '', window_days INT DEFAULT 0, defaults JSONB DEFAULT '{}', comment_table JSONB NULL, interval_minutes INT DEFAULT 5, max_new INT DEFAULT 200, state CHECK IN ('entwurf','getestet','scharf','angehalten') DEFAULT 'entwurf', state_note TEXT DEFAULT '', test_dry_at, test_write_at, cursor TEXT DEFAULT '', fail_count INT DEFAULT 0, next_run_at, created_by, created_at, updated_at)`
  - `sync_field_maps(profile_id ON DELETE CASCADE, foreign_column, pdh_field, direction CHECK IN ('herein','hinaus','beides'), translate JSONB DEFAULT '{}', fixed_value TEXT NULL, position INT, UNIQUE(profile_id, pdh_field))`
  - `sync_links(profile_id ON DELETE CASCADE, foreign_key TEXT NOT NULL, ref_type, ref_id UUID NULL, seen_mark TEXT DEFAULT '', seen_values JSONB DEFAULT '{}', written_values JSONB DEFAULT '{}', last_error TEXT DEFAULT '', created_at, updated_at, UNIQUE(profile_id, foreign_key))`, Index `(ref_type, ref_id)`. `ref_id NULL` = reserviert, Datensatz noch nicht angelegt.
  - `sync_outbox(id BIGSERIAL PK, link_id ON DELETE CASCADE, kind CHECK IN ('update','comment'), payload JSONB NOT NULL, attempts INT DEFAULT 0, last_error TEXT DEFAULT '', next_try_at DEFAULT NOW(), created_at, done_at NULL)`, Index `(link_id, id) WHERE done_at IS NULL`.
  - `sync_runs(profile_id ON DELETE CASCADE, direction CHECK IN ('herein','hinaus','test'), started_at, finished_at, n_new, n_changed, n_written, n_failed INT DEFAULT 0, skipped_reason TEXT DEFAULT '', errors JSONB DEFAULT '[]')`
- Produces (Auslöser): Funktion `pdh_sync_enqueue()` an `tickets` und `faults` (AFTER UPDATE), `ticket_comments` und `infrastructure_comments` (AFTER INSERT). Sie schreibt in `sync_outbox` nur, wenn es eine Verknüpfung mit `ref_id` gibt **und** `current_setting('pdh.sync_origin', true)` nicht `'1'` ist. `payload` bei `update`: `{"fields": {"<spalte>": <neuer Wert>, …}}` nur für geänderte Spalten aus `title, description, priority, severity, status, due_date, resolution, root_cause`; ohne geänderte Spalte kein Eintrag. Bei `comment`: `{"text","user_id","created_at"}`; `infrastructure_comments` nur mit `ref_type IN ('ticket','fault')`.
- Produces (Rechte): `sync.view` „Abgleich: Profile, Läufe und Herkunft sehen“, `sync.edit` „Abgleich: Profile einrichten und testen“, `sync.run` „Abgleich: scharf schalten, anhalten, erneut versuchen“, `sync.admin` „Abgleich: Profile löschen (Bereichsadministrator)“, Kategorie `'Daten & Schnittstellen'`. Vergabe nach dem Muster aus `migrations/157_le_certs.up.sql`: Rollen mit `import.write` (ohne Abteilung) bekommen view/edit/run; Rolle `admin` und Rollen mit `import.write` **und** einem `%.admin`-Recht bekommen `sync.admin`.

- [ ] **Step 1: Test schreiben** (`PDH_TEST_DSN`, Hilfsfunktion `testPool` nach `internal/modules/maintenance/migration_integration_test.go:21`):
  - `TestOutboxOnlyForLinkedRecords`: Ticket ohne Verknüpfung ändern → 0 Einträge; mit Verknüpfung Titel ändern → 1 Eintrag, `payload->'fields'->>'title'` = neuer Titel.
  - `TestOutboxSkipsSyncOrigin`: in Transaktion `SET LOCAL pdh.sync_origin = '1'`, Titel ändern → 0 Einträge.
  - `TestOutboxIgnoresUnrelatedColumns`: nur `updated_at` ändern → 0 Einträge.
  - `TestOutboxComment`: Kommentar an verknüpftem Ticket → 1 Eintrag `kind='comment'`.
  - `TestSyncPermissionsGranted`: Rolle mit `import.write` hat `sync.view`, `sync.edit`, `sync.run`.
- [ ] **Step 2:** `go test ./internal/modules/extsync/ -run 'Outbox|SyncPermissions' -v` → FAIL (Tabelle fehlt).
- [ ] **Step 3:** Migration schreiben.
- [ ] **Step 4:** Test erneut → PASS. Zusätzlich `go test ./pkg/database/` → PASS.
- [ ] **Step 5:** Commit: `Abgleich: Tabellen, Rechte und Warteliste (Migration 158)`.

### Task 3: Kennwörter der Verbindungen verschlüsseln

**Files:**
- Create: `internal/web/conn_secrets.go`, `internal/web/conn_secrets_test.go`
- Modify: `internal/web/import_export.go` (`importExportConnections` :157, `createConnection` :283, `editConnection` :334), `internal/web/connection_queries.go` (`connectionRow` :663), `internal/web/export_preview.go` (:136, :199, :285), `internal/web/sql_preview.go` (:395, :467), `internal/web/connection_detail.go` (:216), `internal/web/web_preview.go` (`StartEnabledImportPolls` :347, `reconcileImportPoll`), `cmd/server/main.go` (Aufruf beim Start, Block `if !mirrorOn`)

**Interfaces:**
- Produces:
  - `func (h *Handler) connKey() []byte` – wie `vpnKey()` (`vpn.go:56`), Präfix `"pdh-connections:"`.
  - `func sealConnConfig(key []byte, cfg map[string]string) (map[string]string, error)` – für jedes Feld aus `connSecretFields` (`connection_detail.go:35`) mit nicht-leerem Wert: `<feld>_enc = encryptSecret(...)`, Klartextfeld entfernt.
  - `func openConnConfig(key []byte, cfg map[string]string) (map[string]string, error)` – Umkehrung; ein altes Klartextfeld ohne `_enc` bleibt gültig.
  - `func publicConnConfig(cfg map[string]string) map[string]string` – ohne Geheimfelder und ohne `_enc`-Felder; für `ConfigJSON` und alles, was ins HTML geht.
  - `func (h *Handler) EncryptConnectionSecrets(ctx context.Context) (int, error)` – verschlüsselt alle Zeilen mit Klartextgeheimnis, gibt die Zahl zurück, idempotent.
- Regel: **Jede** Stelle, die `config` aus `import_export_connections` liest und an `openSQLConnection`, MQTT oder einen Export weitergibt, ruft vorher `openConnConfig`. Beim Bearbeiten: leeres Geheimfeld im Formular = gespeicherten Wert behalten.

- [ ] **Step 1: Tests**
  - `TestSealOpenRoundTrip`: `{"host":"a","password":"g3h€im"}` → versiegelt enthält kein `password`, `password_enc` beginnt mit `v1:`; geöffnet wieder `g3h€im`.
  - `TestOpenKeepsLegacyPlaintext`: `{"password":"alt"}` → `alt`.
  - `TestPublicConfigHidesSecrets`: weder `password` noch `password_enc` im Ergebnis.
  - `TestEncryptConnectionSecretsIdempotent` (`PDH_TEST_DSN`): Zeile mit Klartext → 1, zweiter Aufruf → 0, Rohtext der Spalte enthält das Kennwort nicht mehr.
  - `TestEditKeepsPasswordWhenBlank` (`PDH_TEST_DSN`): `editConnection` mit leerem `f_mssql_password` → `openConnConfig` liefert das alte Kennwort.
  - `TestConnectionListHasNoSecret`: `web/templates/widgets/connection_list.gohtml` mit einer Verbindung rendern (`loadTestTemplates`, `partners_test.go:86`) → Ausgabe enthält weder das Kennwort noch `_enc`.
- [ ] **Step 2:** `go test ./internal/web/ -run 'Seal|Legacy|PublicConfig|EncryptConnection|EditKeeps|ConnectionListHasNo' -v` → FAIL.
- [ ] **Step 3:** Umsetzen. Alle Lesestellen mit `grep -rn "FROM import_export_connections" internal/ cmd/` gegenprüfen; keine darf `config` ungeöffnet an einen Treiber geben.
- [ ] **Step 4:** Tests → PASS; `go build ./...`.
- [ ] **Step 5:** Commit: `Verbindungen: Kennwörter werden verschlüsselt gespeichert und nicht mehr an den Browser gegeben`.

### Task 4: Verbindung – Weg, Abfragezeit über VPN, Verschlüsselung, „Schreiben erlaubt“

**Files:**
- Create: `internal/web/conn_route.go`, `internal/web/conn_route_test.go`
- Modify: `internal/web/import_export.go` (Feldlisten :74, :96 um `route`, `vpn_poll_minutes`, `vpn_timeout_seconds`, `allow_write`, `tls_fingerprint`), `internal/web/sql_preview.go` (`openMssqlDB` :152), `internal/web/connection_checks.go` (Prüfkette), `internal/web/connection_detail.go` (`connFieldLabels`), `web/templates/import.gohtml`, `web/templates/export.gohtml` (Formularfelder für `mssql`)

**Interfaces:**
- Config-Werte (Strings): `route` = `"direct"` (Vorgabe, auch bei fehlendem Feld) oder `"vpn:<vpn_peers.id>"`; `vpn_poll_minutes` Vorgabe `"5"`; `vpn_timeout_seconds` Vorgabe `"60"`; `allow_write` `"true"`/`"false"` (Vorgabe `"false"`); `tls_fingerprint` = SHA-256 des Serverzertifikats, hex, klein, oder leer.
- Produces:
  - `type connRoute struct { ViaVPN bool; PeerID string }`, `func parseConnRoute(cfg map[string]string) connRoute`
  - `func connQueryTimeout(cfg map[string]string) time.Duration` – 15 s direkt, sonst `vpn_timeout_seconds` (Grenzen 5–600 s).
  - `func connPollInterval(cfg map[string]string, profileMinutes int) time.Duration` – direkt: Profilwert; VPN: `vpn_poll_minutes` (Grenzen 1–1440).
  - `func routeDecision(p *vpnPeer, host net.IP) (bool, string)` – reine Entscheidung; Gründe wörtlich: `"Tunnel nicht gefunden"`, `"Tunnel ist kein ausgehender Tunnel"`, `"Tunnel steht nicht"`, `"Adresse liegt nicht in den Netzen des Tunnels"`.
  - `func (h *Handler) connRouteReady(ctx context.Context, cfg map[string]string) (ok bool, reason string)` – direkt: immer `true`. VPN: Peer laden, Status über `vpnReadService`/`vpnWithStatus` (`vpn.go:225`, `vpn_status.go:126`), Hostname vorher auflösen, dann `routeDecision`.
  - `func mssqlServerFingerprint(ctx context.Context, cfg map[string]string) (string, error)` – liest das Zertifikat, ohne sich anzumelden.
  - `openMssqlDB` behält seine Signatur; neu: `tls_fingerprint` gesetzt → Verbindung nur, wenn der Fingerabdruck passt; Verbindungs- und Anmeldezeit = `connQueryTimeout`.
  - `func connSourceAddr(host string, port string) string` – eigene Absenderadresse für diesen Weg (UDP-Dial ohne Senden).
- Prüfkette in `connection_checks.go`: Schritte in dieser Reihenfolge mit je `ok`/Text: Tunnel (nur VPN), Adresse (mit Absenderadresse im Text), Port, Anmeldung, Leserecht, Schreibrecht (nur bei `allow_write`), Hinweis bei `IS_SRVROLEMEMBER('sysadmin')=1` oder `IS_MEMBER('db_owner')=1`: „Der Datenbankbenutzer darf mehr als nötig.“
- Fingerabdruck über `go-mssqldb`: `msdsn.Parse` + `mssql.NewConnectorConfig`, `TLSConfig.InsecureSkipVerify=true` mit `VerifyPeerCertificate`, der den SHA-256 vergleicht. **Vor dem Bauen im Quelltext des Treibers v1.7.2 prüfen**, dass `msdsn.Config.TLSConfig` so greift; wenn nicht, den Befund festhalten und stattdessen den Treiber-Parameter `certificate` mit abgelegter PEM-Datei unter `config/connections/<id>.pem` nutzen.

- [ ] **Step 1: Tests** – `TestParseConnRoute` (leer → direkt; `vpn:abc` → VPN/abc), `TestConnQueryTimeout` (direkt 15 s; VPN ohne Wert 60 s; `"2"` → 5 s; `"9999"` → 600 s), `TestConnPollInterval` (direkt 10 → 10 min; VPN ohne Wert → 5 min, Profilwert zählt nicht), `TestRouteDecisionReasons` (Tabelle der vier Gründe und der Gutfall), `TestMssqlFingerprintMismatch` (`PDH_TEST_MSSQL_DSN`: falscher Fingerabdruck → Fehler mit „Fingerabdruck“; richtiger → Verbindung).
- [ ] **Step 2:** Tests laufen lassen → FAIL.
- [ ] **Step 3:** Umsetzen. Formular: Auswahl „Weg“ (Direkt / Liste der ausgehenden Tunnel), darunter nur bei VPN die Felder „Abfragetakt über VPN (Minuten)“ und „Zeitgrenze je Abfrage über VPN (Sekunden)“; Schalter „Schreiben erlaubt“; Knopf „Fingerabdruck lesen und bestätigen“.
- [ ] **Step 4:** Tests → PASS; Formular in Fiori und Fluent, hell und dunkel im Browser ansehen.
- [ ] **Step 5:** Commit: `Verbindungen: Weg direkt oder über VPN mit eigener Abfragezeit, Zertifikat mit Fingerabdruck, Schalter „Schreiben erlaubt“, Prüfkette mit Absenderadresse`.

### Task 5: Typen und Speicher

**Files:**
- Create: `internal/modules/extsync/model.go`, `store.go`, `store_integration_test.go`

**Interfaces:**
- Produces (`model.go`):
  ```go
  type Profile struct { ID, Name, ConnectionID string; DepartmentID *string; TargetType string
      SourceSchema, SourceTable string; KeyColumns []string; ChangeColumn string; ChangeKind string
      FilterSQL string; WindowDays int; Defaults map[string]string; CommentTable *CommentTable
      IntervalMinutes, MaxNew int; State, StateNote string; TestDryAt, TestWriteAt, NextRunAt *time.Time
      Cursor string; FailCount int }
  type CommentTable struct { Schema, Table, FKColumn, TextColumn, AuthorColumn, TimeColumn string }
  type FieldMap struct { ID, ProfileID, ForeignColumn, PDHField, Direction string; Translate map[string]string; FixedValue *string; Position int }
  type Link struct { ID, ProfileID, ForeignKey, RefType string; RefID *string; SeenMark string; SeenValues, WrittenValues map[string]string; LastError string }
  type OutboxItem struct { ID int64; LinkID, Kind string; Payload json.RawMessage; Attempts int; LastError string }
  type Run struct { ID, ProfileID, Direction string; StartedAt time.Time; FinishedAt *time.Time; New, Changed, Written, Failed int; SkippedReason string; Errors []RunError }
  type RunError struct { ForeignKey, Field, Message string }
  type PDHField struct { Key, Label string; Required bool; MaxLen int; Kind string; Values []string } // Kind: text|enum|time
  func Fields(targetType string) []PDHField
  func EncodeKey(values []string) string   // eindeutig auch bei mehreren Spalten und Sonderzeichen (JSON-Feld)
  func DecodeKey(key string) ([]string, error)
  ```
  Feldkatalog Ticket: `title` (Pflicht), `description`, `priority` (low|medium|high|critical), `status` (open|in_progress|pending|resolved|closed), `due_date`, `resolution`, `root_cause`. Störmeldung: dasselbe mit `severity` statt `priority` und `status` (detected|analyzing|in_progress|pending|resolved|closed). `MaxLen` an den Spalten in den Migrationen ablesen und übernehmen.
- Produces (`store.go`): `type Store struct{ db *pgxpool.Pool }`, `func NewStore(db *pgxpool.Pool) *Store`, Methoden
  `ListProfiles(ctx) ([]Profile, error)`, `GetProfile(ctx, id) (*Profile, error)`, `SaveProfile(ctx, *Profile, userID string) error` (setzt bei Änderung an Quelle/Ziel `state='entwurf'`, Testzeiten leer), `SetState(ctx, id, state, note string) error`, `DeleteProfile(ctx, id) error`,
  `ListMaps(ctx, profileID) ([]FieldMap, error)`, `ReplaceMaps(ctx, profileID string, maps []FieldMap) error` (setzt `entwurf`),
  `ReserveLink(ctx, profileID, foreignKey, refType string) (link *Link, created bool, err error)` (`INSERT … ON CONFLICT DO NOTHING`), `AttachLink(ctx, linkID, refID, seenMark string, seen map[string]string) error`, `UpdateLinkSeen(ctx, linkID, seenMark string, seen map[string]string) error`, `UpdateLinkWritten(ctx, linkID string, written map[string]string) error`, `SetLinkError(ctx, linkID, msg string) error`, `LinkByRef(ctx, refType, refID string) (*Link, *Profile, error)`, `LinkByKey(ctx, profileID, foreignKey string) (*Link, error)`, `OrphanLinks(ctx, profileID) ([]Link, error)` (`ref_id IS NULL`),
  `Enqueue(ctx, linkID, kind string, payload any) error`, `NextOutbox(ctx, limit int) ([]OutboxItem, error)` (je Verknüpfung nur der älteste offene Eintrag, `next_try_at <= NOW()`, nur Profile mit `state='scharf'`), `OutboxDone(ctx, id int64) error`, `OutboxFailed(ctx, id int64, msg string, retryIn time.Duration) error`, `OutboxRetryNow(ctx, id int64) error`, `OpenOutboxCount(ctx, profileID) (int, error)`,
  `StartRun(ctx, profileID, direction string) (string, error)`, `FinishRun(ctx, *Run) error`, `ListRuns(ctx, profileID string, limit int) ([]Run, error)`, `DueProfiles(ctx) ([]Profile, error)`, `SetSchedule(ctx, id string, failCount int, next time.Time, cursor string) error`,
  `DemoteConnection(ctx, connectionID string) (int, error)` – setzt `scharf` → `getestet` mit Hinweis „Weg der Verbindung geändert“, Testzeiten leer.

- [ ] **Step 1: Tests** – `TestEncodeKeyRoundTrip` (`["a b","x|y"]`, GUID, ein Wert `"17"`), `TestReserveLinkOnce` (zweimal dieselbe → `created` einmal `true`), `TestNextOutboxKeepsOrderPerLink` (zwei Einträge einer Verknüpfung, erster fehlgeschlagen mit `next_try_at` in der Zukunft → keiner der beiden kommt; Eintrag einer anderen Verknüpfung kommt), `TestNextOutboxOnlyArmed`, `TestSaveProfileResetsState`, `TestDemoteConnection`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit: `Abgleich: Profile, Zuordnungen, Verknüpfungen und Warteliste speichern`.

### Task 6: Dialekt SQL Server – lesen und die zwei Schreibbefehle

**Files:**
- Create: `internal/modules/extsync/dialect.go`, `dialect_mssql.go`, `dialect_mssql_test.go`, `dialect_mssql_integration_test.go`

**Interfaces:**
- Produces:
  ```go
  type Column struct { Name, Type string; Nullable bool; MaxLen int; IsKey, IsRowVersion, IsIdentity, IsComputed bool }
  type TableInfo struct { Schema, Name string; Columns []Column; Triggers []string }
  type Stmt struct { SQL string; Args []any }
  type Dialect interface {
      Tables(ctx context.Context, db *sql.DB) ([]TableInfo, error)            // nur Namen
      Describe(ctx context.Context, db *sql.DB, schema, table string) (*TableInfo, error)
      SelectRows(t *TableInfo, cols, keyCols []string, changeCol, changeKind, cursor, filter string, windowDays, limit int) (Stmt, error)
      SelectOne(t *TableInfo, cols, keyCols, keyVals []string) (Stmt, error)
      UpdateRow(t *TableInfo, keyCols, keyVals []string, set map[string]any) (Stmt, error)
      InsertChild(source, child *TableInfo, values map[string]any) (Stmt, error)
      Quote(name string) string
  }
  func DialectFor(kind string) (Dialect, error) // nur "mssql"; sonst Fehler "Datenbankart wird noch nicht unterstützt"
  var ErrInsertIntoSource = errors.New("in die Meldungstabelle wird nichts eingefügt")
  ```
- Regeln: `UpdateRow` und `InsertChild` lehnen jeden Spalten- oder Tabellennamen ab, der nicht in `Columns` steht; `UpdateRow` verlangt alle Schlüsselspalten, lehnt Schlüssel-, Identitäts-, berechnete und `rowversion`-Spalten im `SET` ab und erzeugt `UPDATE TOP (1) [s].[t] SET [c]=@p1 … WHERE [k]=@pn`. `InsertChild` gibt `ErrInsertIntoSource`, wenn `child` dieselbe Tabelle ist wie `source`. `filter` läuft durch `validateFilter` (keine `;`, kein Kommentar, keine Wörter `DELETE|UPDATE|INSERT|MERGE|DROP|TRUNCATE|EXEC|EXECUTE|ALTER|CREATE|GRANT|INTO`) und wird in Klammern an `WHERE` gehängt. `windowDays > 0` nur mit Änderungsspalte der Art `timestamp`.
- `Quote`: `[` + Name mit `]` → `]]` + `]`.

- [ ] **Step 1: Tests (ohne Datenbank)**
  - `TestUpdateRowShape`: Ergebnis genau `UPDATE TOP (1) [dbo].[Meldungen] SET [Titel]=@p1, [Zustand]=@p2 WHERE [MeldungNr]=@p3`, Args in dieser Reihenfolge (Spalten sortiert).
  - `TestUpdateRowCompositeKey`: zwei Schlüsselspalten → beide im `WHERE`; fehlt eine → Fehler.
  - `TestUpdateRowRejectsUnknownAndKeyColumns`: Spalte `x]; DROP TABLE y--`, Schlüsselspalte, `RV` → jeweils Fehler.
  - `TestInsertChildShape`, `TestInsertChildRejectsSource`.
  - `TestNoDestructiveStatementPossible`: Für eine Tabelle mit bösartigen Werten in **allen** Eingaben (Werte, Filter, Schlüsselwerte) über 200 erzeugte Kombinationen: Jeder erzeugte `Stmt.SQL` beginnt mit `SELECT`, `UPDATE TOP (1)` oder `INSERT INTO` und enthält außerhalb von `[...]` keines der Wörter `DELETE`, `TRUNCATE`, `DROP`, `MERGE`; `validateFilter` weist `1=1; DELETE FROM x` zurück.
  - `TestQuote`.
- [ ] **Step 2: Tests (mit `PDH_TEST_MSSQL_DSN`)** – `TestDescribeLab` (Schlüssel `MeldungNr`, `RV` als `IsRowVersion`, Auslöser `trg_Meldungen_Geaendert`), `TestSelectRowsCursor` (nach `rowversion`-Cursor kommen nur geänderte Zeilen), `TestUpdateRowHitsOneRow` (in Transaktion, `RowsAffected()==1`, Rollback), `TestGuidCompositeKey`.
- [ ] **Step 3:** → FAIL. **Step 4:** Umsetzen. **Step 5:** → PASS.
- [ ] **Step 6:** Commit: `Abgleich: SQL-Server-Aufbau lesen; schreibend nur „eine Zeile ändern“ und „Kommentar anhängen“`.

### Task 7: Zuordnen, Übersetzen, Widerspruch

**Files:**
- Create: `internal/modules/extsync/mapping.go`, `mapping_test.go`

**Interfaces:**
- Produces:
  ```go
  func ToPDH(maps []FieldMap, fields []PDHField, row map[string]any, defaults map[string]string) (values map[string]string, errs []RunError)
  func ToForeign(maps []FieldMap, pdhValues map[string]string) (set map[string]any, errs []RunError) // nur hinaus|beides
  func RowMark(changeKind string, changeVal any, mapped map[string]string) string // Änderungswert, sonst SHA-256 der zugeordneten Werte
  type Decision struct { Apply map[string]string; KeepPDH map[string]string; ForeignLost map[string]string }
  func Decide(maps []FieldMap, seen, written, foreignNow, pdhNow map[string]string) Decision
  ```
- Regeln `ToPDH`: Werte als Text, Leerzeichen am Rand weg; `Translate` wird ohne Beachtung von Groß/Klein angewandt, Schlüssel `"*"` ist der Auffangwert; Aufzählungsfeld ohne Treffer → Fehler „Wert ‚X‘ für Feld ‚Priorität‘ ist nicht zugeordnet“; Pflichtfeld leer → Fehler; Text länger als `MaxLen` → gekürzt, **kein** Fehler; Zeit: `time.Time` oder Text in `2006-01-02`, `2006-01-02 15:04:05`, RFC 3339, sonst Fehler; `FixedValue` schlägt die Spalte; `defaults` füllen leere Felder.
- Regeln `ToForeign`: Umkehrung der Übersetzung (erster Fremdwert, der auf den PDH-Wert zeigt; `"*"` zählt nicht); PDH-Wert ohne Gegenstück bei Aufzählungsfeld → Fehler; leere Zeit → `nil`.
- Regeln `Decide` je Feld mit Richtung `herein`/`beides`, alle Werte in PDH-Form:
  - `foreignNow == seen` → nichts.
  - `foreignNow == written` → nichts (eigenes Zurückgeschriebenes).
  - sonst, `pdhNow == seen` oder `pdhNow == written` oder `pdhNow == foreignNow` → `Apply`.
  - sonst (beide geändert) → `KeepPDH[feld]=pdhNow`, `ForeignLost[feld]=foreignNow`.
  - Feld nur `hinaus`: nie `Apply`.

- [ ] **Step 1: Tests** – Tabellen für jede Regel oben, dazu `TestToPDHNull` (`nil` → leer → Vorgabewert), `TestToPDHTruncates`, `TestToForeignReverse`, `TestRowMarkStableOrder`, `TestDecideEcho`, `TestDecideConflict`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit: `Abgleich: Werte übersetzen und Widersprüche entscheiden (PDH gewinnt)`.

### Task 8: Ziel im PDH und hereinkommender Lauf

**Files:**
- Create: `internal/modules/extsync/target.go`, `inbound.go`, `inbound_integration_test.go`, `fake_foreign_test.go`

**Interfaces:**
- Consumes: `tickets.Service.Create(ctx, *tickets.CreateInput, createdBy) (*tickets.Ticket, error)` (`tickets.go:337`), `faults.Service.Create(ctx, *faults.CreateFaultInput, userID) (*faults.Fault, error)` (`service_handler.go:68`), `QuickResolve` beider Dienste, Tabelle `record_history` (Spalten wie in `internal/web/handler.go:1376`).
- Produces:
  ```go
  type Target interface {
      Create(ctx context.Context, values map[string]string, systemUser string) (id string, err error)
      Read(ctx context.Context, id string) (map[string]string, error)        // nur Katalogfelder
      Apply(ctx context.Context, id string, values map[string]string, systemUser string) error
      Note(ctx context.Context, id, action, field, oldV, newV, message, systemUser string) error
      FindOrigin(ctx context.Context, profileID, foreignKey string) (id string, found bool, err error)
  }
  func NewTicketTarget(db *pgxpool.Pool, svc *tickets.Service) Target
  func NewFaultTarget(db *pgxpool.Pool, svc *faults.Service) Target
  type Foreign interface { // im Betrieb *sql.DB + Dialect, im Test eine Attrappe
      Describe(ctx context.Context, schema, table string) (*TableInfo, error)
      Query(ctx context.Context, st Stmt) ([]map[string]any, error)
      Exec(ctx context.Context, st Stmt) (affected int64, err error)
  }
  func NewSQLForeign(db *sql.DB, d Dialect, timeout time.Duration) Foreign
  type Inbound struct { Store *Store; Targets map[string]Target; SystemUser string }
  func (in *Inbound) Run(ctx context.Context, p *Profile, maps []FieldMap, f Foreign, d Dialect, dry bool) (*Run, []Preview, error)
  type Preview struct { ForeignKey string; Values map[string]string; Errors []RunError; IsNew bool }
  ```
- Regeln:
  - `Apply` und der Statuswechsel beim Anlegen laufen in einer Transaktion mit `SET LOCAL pdh.sync_origin = '1'`; Titel, Beschreibung, Priorität/Schweregrad, Fälligkeit per `UPDATE` auf `tickets`/`faults`, je Feld ein Eintrag in `record_history` (`action='sync'`). Status `resolved`/`closed` über `QuickResolve` mit Lösungstext aus dem Feld `resolution`, sonst „Im Fremdsystem erledigt“; andere Status per `UPDATE`. Weil `QuickResolve` eine eigene Verbindung nutzt, löst es den Auslöser aus: Der dabei entstandene Wartelisteneintrag ist unschädlich, weil `Decide`/`UpdateLinkWritten` den Stand danach als gesehen führen – im Test `TestInboundFaultTarget` belegen, dass der Fremdwert nicht erneut geschrieben wird (Eintrag wird ohne Befehl erledigt, wenn PDH-Wert und zuletzt gesehener Fremdwert gleich sind; diese Prüfung gehört in `Outbound.Process`, Task 9).
  - Neue Zeile: `ReserveLink` → war die Verknüpfung schon da und hat `ref_id`, ist die Zeile nicht neu. `created` → `Create`, sofort `Note(…, "sync_origin", …)` mit `message` = `"<Profil-ID>|<Schlüssel>|Aus <Profilname>, Nummer <Schlüssel>"`, dann `AttachLink`.
  - Zu Beginn jedes Laufs `OrphanLinks`: je Eintrag `FindOrigin` → gefunden: `AttachLink`; nicht gefunden: Zeile gilt wieder als neu.
  - Geänderte Zeile: `Decide`; `Apply` für `Apply`; für jedes Feld in `ForeignLost` ein Verlaufseintrag „Wert im Fremdsystem ‚X‘ nicht übernommen, PDH-Wert bleibt“ und ein Wartelisteneintrag `update` (`Store.Enqueue`) mit dem Feld; danach `UpdateLinkSeen`.
  - Obergrenze: Mehr als `MaxNew` neue Zeilen in einem Lauf → nichts anlegen, `SetState(…, "angehalten", "Zu viele neue Meldungen: N (Obergrenze M)")`, Lauf mit `SkippedReason`. Mit `dry=true` wird nie angehalten und nie geschrieben (auch kein `sync_runs`-Eintrag, keine Verknüpfung).
  - Cursor: nach dem Lauf höchster Änderungswert der fehlerfreien Zeilen **vor** der ersten fehlerhaften Zeile (sortiert nach Änderungswert), damit eine fehlerhafte Zeile beim nächsten Lauf wiederkommt.

- [ ] **Step 1: Tests** (`PDH_TEST_DSN`, `Foreign`-Attrappe mit Zeilen im Speicher)
  - `TestInboundCreatesOnce`: zwei Läufe über dieselben 3 Zeilen → 3 Tickets, 3 Verknüpfungen.
  - `TestInboundChangeApplied`: fremder Titel geändert → Ticket-Titel neu, Verlaufseintrag `sync`, **kein** Wartelisteneintrag.
  - `TestInboundEchoIgnored`: `written_values` = neuer Fremdwert → kein Verlauf, keine Warteliste.
  - `TestInboundConflictPDHWins`: beide Seiten geändert → PDH-Wert bleibt, Verlauf nennt Fremdwert, 1 Wartelisteneintrag.
  - `TestInboundBadRowDoesNotStopRun`: Zeile 2 mit unbekannter Priorität → Zeilen 1 und 3 angelegt, `Failed==1`, Fehlertext nennt Feld und Wert; nächster Lauf versucht Zeile 2 erneut.
  - `TestInboundMaxNewPauses`: `MaxNew=2`, 3 neue → 0 Tickets, Zustand `angehalten`.
  - `TestInboundCrashBetweenCreateAndAttach`: Verknüpfung reserviert, Ticket mit Herkunftseintrag vorhanden, `ref_id` leer → nächster Lauf hängt an, kein zweites Ticket.
  - `TestInboundDryWritesNothing`: Zahl der Tickets, Verknüpfungen, Läufe unverändert.
  - `TestInboundFaultTarget`: Störmeldung statt Ticket, Status `resolved` über `QuickResolve`.
  - `TestInboundCompositeKey`: Schlüssel aus zwei Spalten, eine davon GUID.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit: `Abgleich: neue und geänderte Meldungen aus dem Fremdsystem übernehmen`.

### Task 9: Hinausgehend – Warteliste abarbeiten

**Files:**
- Create: `internal/modules/extsync/outbound.go`, `outbound_integration_test.go`

**Interfaces:**
- Consumes: `Store.NextOutbox`, `OutboxDone`, `OutboxFailed`, `UpdateLinkWritten`, `SetLinkError`, `Dialect.UpdateRow`, `InsertChild`, `ToForeign`, `Target.Read`, `Target.Note`, `Foreign`.
- Produces:
  ```go
  type Outbound struct { Store *Store; Targets map[string]Target; SystemUser string; UserName func(ctx context.Context, id string) string }
  func (o *Outbound) Process(ctx context.Context, item OutboxItem, link *Link, p *Profile, maps []FieldMap, f Foreign, d Dialect, allowWrite bool) error
  func Backoff(attempts int) time.Duration // 5 s, 15 s, 1 min, 5 min, dann 15 min
  var ErrRowGone = errors.New("Meldung im Fremdsystem nicht mehr vorhanden")
  var ErrWriteNotAllowed = errors.New("Schreiben ist an der Verbindung nicht erlaubt")
  ```
- Regeln: `update` → aus `payload.fields` nur Felder mit Richtung `hinaus`/`beides`; ist danach nichts übrig, Eintrag als erledigt. Wert immer der **aktuelle** PDH-Wert aus `Target.Read` (nicht der aus dem Eintrag), damit ältere Einträge keinen neueren Stand überschreiben. Felder, deren aktueller PDH-Wert gleich `link.SeenValues` oder `link.WrittenValues` ist, werden ausgelassen (nichts zu tun). `affected == 0` → `ErrRowGone`: Eintrag erledigt, `SetLinkError`, Verlaufseintrag am Datensatz, Verknüpfung bleibt. Erfolg → `UpdateLinkWritten` mit den geschriebenen Werten in PDH-Form, `SetLinkError("")`. `comment` → `InsertChild` in `CommentTable` (Fremdschlüssel = erster Schlüsselwert, Text, Verfassername, Zeit); ohne `CommentTable` als erledigt. `allowWrite == false` → `ErrWriteNotAllowed`, Eintrag bleibt.

- [ ] **Step 1: Tests** – `TestBackoffCap` (ab dem 5. Versuch 15 min), `TestOutboundUpdateUsesCurrentValue`, `TestOutboundOnlyMappedOutFields` (Feld `herein` wird nie geschrieben), `TestOutboundSkipsUnchangedAgainstSeen`, `TestOutboundRowGone` (Attrappe liefert 0 → Eintrag erledigt, Verknüpfung bleibt, kein `INSERT`), `TestOutboundFailureBlocksLaterOfSameLink`, `TestOutboundComment`, `TestOutboundWriteNotAllowed`, mit `PDH_TEST_MSSQL_DSN`: `TestOutboundAgainstLab` (Titel im Labor geändert, `MeldungKommentare` hat neue Zeile, Zeilenzahl `Meldungen` unverändert).
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit: `Abgleich: Änderungen und Kommentare ins Fremdsystem zurückschreiben`.

### Task 10: Test im Cockpit – Trockenlauf und Schreibversuch mit Rücknahme

**Files:**
- Create: `internal/modules/extsync/probe.go`, `probe_integration_test.go`

**Interfaces:**
- Produces:
  ```go
  type DryOut struct { Stmt Stmt; Errors []RunError }
  func DryOutbound(p *Profile, maps []FieldMap, t *TableInfo, d Dialect, foreignKey string, sample map[string]string) DryOut
  type WriteProbe struct { OK bool; Triggers []string; Statement string; ReadBack map[string]any; Error string }
  func ProbeTriggers(ctx context.Context, db *sql.DB, d Dialect, p *Profile) ([]string, error)
  func ProbeWrite(ctx context.Context, db *sql.DB, d Dialect, p *Profile, maps []FieldMap, foreignKey string, sample map[string]string) WriteProbe
  func (s *Store) MarkTest(ctx context.Context, profileID, stage string, ok bool) error // stage: "dry" | "write"; alle nötigen Stufen bestanden → state 'getestet'
  func NeedsWriteTest(maps []FieldMap, p *Profile) bool // hinausgehende Zuordnung oder CommentTable vorhanden
  ```
- Regeln `ProbeWrite`: `BeginTx` → `UpdateRow` → Zeile mit `SelectOne` lesen → bei `CommentTable` zusätzlich `InsertChild` mit Text „PDH-Test“ → **immer** `Rollback` (per `defer`, auch bei Panik); nie `Commit`. Der Trockenlauf hereinkommend ist `Inbound.Run(…, dry=true)`. `MarkTest("dry", ok)` nur `true`, wenn keine `Preview` Fehler trägt.

- [ ] **Step 1: Tests** – `TestDryOutboundShowsStatement`, `TestNeedsWriteTest`, `TestMarkTestBothStages`, mit `PDH_TEST_MSSQL_DSN`: `TestProbeWriteRollsBack` (Zeile nach dem Versuch unverändert, `RV` unverändert, Zeilenzahl `MeldungKommentare` unverändert), `TestProbeWriteReportsTriggers`, `TestProbeWriteReportsPermissionError` (Zuordnung auf eine Tabelle, für die `pdh_sync` kein `UPDATE` hat → `OK=false`, Fehlertext der Datenbank).
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Commit: `Abgleich: Trockenlauf und Schreibversuch mit Rücknahme`.

### Task 11: Betrieb – Takt, aktiver Server, Pausen

**Files:**
- Create: `internal/modules/extsync/runner.go`, `runner_test.go`, `internal/web/sync_wire.go`, `internal/web/sync_wire_test.go`
- Modify: `internal/web/handler.go` (Felder `syncStore *extsync.Store`, `syncRunner *extsync.Runner`, Aufbau bei :347), `cmd/server/main.go` (Start im Block `if !mirrorOn`, nach `StartChangeNotifier` :344), `internal/web/import_export.go` (`editConnection`: bei geändertem `route` `DemoteConnection`)

**Interfaces:**
- Produces:
  ```go
  type ConnOpener func(ctx context.Context, connectionID string) (db *sql.DB, kind string, cfg map[string]string, err error)
  type RouteCheck func(ctx context.Context, cfg map[string]string) (ok bool, reason string)
  type RunnerConfig struct { Store *Store; In *Inbound; Out *Outbound; Open ConnOpener; Route RouteCheck; Skip func() bool
      Interval func(cfg map[string]string, profileMinutes int) time.Duration; Timeout func(cfg map[string]string) time.Duration }
  func NewRunner(c RunnerConfig) *Runner
  func (r *Runner) Start(ctx context.Context)          // zwei Schleifen: hereinkommend alle 30 s fällige Profile, hinausgehend alle 3 s
  func (r *Runner) RunNow(ctx context.Context, profileID string) (*Run, error)
  func (r *Runner) Kick()                               // Warteliste sofort prüfen
  ```
- `sync_wire.go`: `func (h *Handler) StartSync(ctx context.Context)` baut den `Runner` mit `connectionRow` + `openConnConfig` + `openSQLConnection`, `h.connRouteReady`, `h.MaintLocked`, `connPollInterval`, `connQueryTimeout`, `pdhSystemUserID`.
- Regeln: nur Profile mit `state='scharf'`; je Verbindung ein Schloss (höchstens ein Lauf gleichzeitig, hereinkommend und hinausgehend teilen es); jeder Aufruf an die fremde Datenbank mit `context.WithTimeout(Timeout(cfg))`; Weg nicht bereit oder Verbindung scheitert → Lauf mit `SkippedReason` (Grund aus `Route` bzw. `"Datenbank nicht erreichbar: …"`), `fail_count+1`, `next_run_at = jetzt + min(Takt·2^fail, 15 min)`; Erfolg → `fail_count=0`, `next_run_at = jetzt + Takt`. Gleiche übersprungene Gründe hintereinander ergeben **einen** Lauf-Eintrag (letzten aktualisieren), damit die Liste nicht vollläuft. `Skip()` wahr → nichts tun. Eine Panik in einem Lauf wird abgefangen und als Fehler des Laufs gespeichert. Fehlgeschlagene Wartelisteneinträge: `OutboxFailed(…, Backoff(attempts))`.

- [ ] **Step 1: Tests** (Attrappen, `PDH_TEST_DSN`) – `TestRunnerOnlyArmedProfiles`, `TestRunnerSkipsWhenRouteDown` (Grund im Lauf, Warteliste unverändert), `TestRunnerBackoffCapsAt15Min`, `TestRunnerOneRunPerConnection` (zwei Profile, Attrappe blockiert → nie zwei gleichzeitig), `TestRunnerSkipInMaintenance`, `TestRunnerVPNIntervalOverridesProfile`, `TestRunnerRecoversPanic`, `TestRunnerRepeatedSkipOneRunRow`, `TestRouteChangeDemotesArmedProfiles` (`internal/web`).
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS; `go build ./...`.
- [ ] **Step 5:** Commit: `Abgleich: läuft im Takt am aktiven Server, pausiert bei Störungen und in der Wartungssperre`.

### Task 12: Oberfläche – Übersicht und Cockpit Schritte 1 bis 3

**Files:**
- Create: `internal/web/sync_pages.go`, `sync_actions.go`, `sync_pages_test.go`, `web/templates/sync.gohtml`, `web/templates/sync_profile.gohtml`, `web/templates/widgets/sync_source.gohtml`, `sync_target.gohtml`, `sync_maps.gohtml`
- Modify: `internal/web/handler.go` (`Routes` :359; `BaseData` um `CanSync`, gesetzt bei :1168 mit `"sync.view"`), `internal/web/nav.go` (`navDefs` nach :111), `internal/web/perm_gate.go` (`areaAdminPerm`, `gateRules`), `internal/core/rbac/scope.go` (Abteilung eines Profils), `web/templates/help.gohtml` (vorläufiger Abschnitt, damit `TestHelpCoversAllPages` grün bleibt; Text kommt in Task 14), `internal/web/help.go` (`helpChapterIDs`: `abgleich` nach `connections`)

**Interfaces:**
- Routen (alle unter `/sync`), Seitenkennung `baseData(r, "sync", …)`:

  | Route | Recht |
  |---|---|
  | `GET /sync` | `sync.view` |
  | `GET /sync/{id}` | `sync.view` (Datensatz) |
  | `POST /sync/new-web` | `sync.edit` |
  | `POST /sync/{id}/source-web`, `/target-web`, `/maps-web` | `sync.edit` (Datensatz) |
  | `GET /sync/{id}/tables`, `/columns`, `/sample` (htmx) | `sync.edit` (Datensatz) |
  | `POST /sync/{id}/delete-web` | `sync.admin` |

- `gateRules`: Regeln für `sync.edit` (Refs `sync_profile`) auf `^/sync`+gID+`/(?:source|target|maps)-web$`, für `sync.admin` auf `^/sync`+gID+`/delete-web$`, für `sync.edit` ohne Datensatz auf `^/sync/new-web$`; `areaAdminPerm["sync_profile"] = "sync.admin"`.
- Navigation: `{navItem{"sync", "/sync", "ti-arrows-exchange", "Abgleich", "sync", false}, "data", func(b *BaseData) bool { return b.CanSync }}`.
- Die Aktionen rufen **nur** `Store.SaveProfile`, `Store.ReplaceMaps` und `Dialect.Tables/Describe` – keine eigene Logik in den Handlern (Paket 2 nutzt dieselben Funktionen). Eingaben prüfen: Tabelle, Schlüssel- und Änderungsspalte müssen in `Describe` vorkommen; Filter durch `validateFilter` (aus `extsync` exportieren als `ValidateFilter`).
- `/sample` zeigt höchstens 20 Zeilen und nur mit `sync.edit`; Abfrage über `SelectRows` mit `limit=20`.
- Zuordnungstabelle: je PDH-Feld eine Zeile mit fremder Spalte (Auswahl aus `Describe`), Richtung, fester Wert, Übersetzung als Paare „Fremdwert → PDH-Wert“ (bei Aufzählungsfeldern Auswahl der PDH-Werte). Pflichtfeld ohne Zuordnung und ohne Vorgabewert: markierte Zeile mit Text „Pflichtfeld ohne Zuordnung“. Richtung `hinaus`/`beides` ist gesperrt mit Hinweis, wenn die Verbindung `allow_write != "true"` hat oder die Spalte Schlüssel, berechnet oder `rowversion` ist.
- Oberfläche nur mit vorhandenen Klassen und Variablen (`--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent`, `--shadow-card`); Schritte als Reiterleiste wie in der vorhandenen Verbindungsseite (`connection_detail`), bei schmaler Breite untereinander.

- [ ] **Step 1: Tests** – `TestSyncPagesRequirePermission` (ohne `sync.view` Umleitung; `POST …/delete-web` ohne `sync.admin` abgelehnt; `hasPermHook`), `TestSyncGateRules` (Pfade treffen die Regeln), `TestSyncSourceRejectsUnknownTable`, `TestSyncMapsMarksMissingRequired` (Render enthält „Pflichtfeld ohne Zuordnung“ für `title`), `TestSyncMapsLocksOutboundWithoutWrite`, `TestSyncTemplatesRender` (alle neuen Vorlagen mit leerem und gefülltem Profil), dazu `TestHelpCoversAllPages`, `TestHelpChaptersMatch`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Im Browser prüfen: Fiori und Fluent, hell und dunkel, 390 px und 1366 px. Bildschirmfotos im Scratchpad ablegen.
- [ ] **Step 6:** Commit: `Abgleich: Übersicht und Cockpit für Quelle, Ziel und Zuordnungen`.

### Task 13: Oberfläche – Test, Betrieb, Herkunft am Datensatz

**Files:**
- Create: `web/templates/widgets/sync_test.gohtml`, `sync_ops.gohtml`, `sync_origin.gohtml`, `internal/web/sync_ops_test.go`
- Modify: `internal/web/sync_actions.go`, `internal/web/perm_gate.go`, `internal/web/handler.go` (Routen; Ticket- und Störungsdetail geben `SyncOrigin` an die Vorlage), Vorlagen der Ticket- und Störungsdetailseite (Baustein `sync-origin` einbinden)

**Interfaces:**
- Routen:

  | Route | Recht |
  |---|---|
  | `POST /sync/{id}/test-dry-web` | `sync.edit` |
  | `POST /sync/{id}/test-out-web` (Befehl zeigen), `/test-write-web` (Schreibversuch) | `sync.edit` |
  | `POST /sync/{id}/arm-web`, `/pause-web`, `/run-web` | `sync.run` |
  | `POST /sync/{id}/outbox/{n}/retry-web` | `sync.run` |
  | `GET /sync/{id}/runs` (htmx) | `sync.view` |

  Alle `POST`-Routen zusätzlich in `gateRules`.
- Regeln:
  - „Scharf schalten“ nur, wenn `test_dry_at` gesetzt und (`!NeedsWriteTest` oder `test_write_at` gesetzt) und Zustand `getestet`; sonst Meldung, welcher Schritt fehlt. Hat das Profil hinausgehende Zuordnungen und die Verbindung `allow_write != "true"`, abgelehnt mit „Schreiben ist an der Verbindung nicht erlaubt“. Vor dem ersten Scharfschalten zeigt ein Bestätigungsschritt die Zahl der Meldungen, die der erste Lauf anlegen würde (Trockenlauf-Zahl), bei Überschreitung von `MaxNew` mit Hinweis, das Zeitfenster oder den Filter enger zu setzen.
  - Schreibversuch: erst `ProbeTriggers`; gibt es Auslöser, Seite mit deren Namen und Knopf „Trotzdem versuchen“; ohne Auslöser direkt.
  - Betrieb zeigt: Zustand mit Hinweistext, wirksamen Takt (bei VPN „über VPN: alle N Minuten“), offene Einträge, letzte 20 Läufe, Fehler mit Schlüssel/Feld/Text, je offenem fehlerhaften Eintrag „Erneut versuchen“.
  - Herkunft am Datensatz (nur mit `sync.view`): „Aus <Profil>, Nummer <Schlüssel> · Abgleich: aktuell / N offen / Fehler: <Text>“.

- [ ] **Step 1: Tests** – `TestArmRequiresTests` (ohne Trockenlauf abgelehnt, Text nennt den Schritt), `TestArmSkipsWriteTestWithoutOutbound`, `TestArmRefusedWithoutAllowWrite`, `TestArmNeedsRunPermission`, `TestWriteTestShowsTriggersFirst`, `TestOriginShownOnTicket`, `TestOriginHiddenWithoutPermission`, `TestRetryMovesItemToNow`.
- [ ] **Step 2:** → FAIL. **Step 3:** Umsetzen. **Step 4:** → PASS.
- [ ] **Step 5:** Im Browser gegen das Labor aus Task 1 durchspielen (lokaler PDH-Start): Profil anlegen, zuordnen, Trockenlauf, Schreibversuch, scharf schalten, Zeile im Labor einfügen → Ticket erscheint; Titel im PDH ändern → `SELECT Titel FROM dbo.Meldungen` zeigt den neuen Titel; Kommentar → Zeile in `MeldungKommentare`. Beide Stile, hell und dunkel.
- [ ] **Step 6:** Commit: `Abgleich: Test, Scharfschalten, Läufe und Herkunft am Datensatz`.

### Task 14: Handbuch, Änderungsprotokoll, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `abgleich`, `data-pages="sync"`; Ergänzungen in `connections`, `vpn`, `admin`), `web/templates/widgets/help_figures.gohtml` (Skizze Cockpit mit fünf Schritten), `CHANGELOG.md`, `VERSION`

- [ ] **Step 1:** Kapitel „Abgleich mit Fremdsystemen“ in Anwendersprache mit `.hb-howto`/`.hb-steps`: Was der Abgleich tut und was er nie tut (löschen, Meldungen anlegen); Datenbankbenutzer im Fremdsystem mit Mindestrechten (Lesen auf Meldungen, Ändern auf Meldungen, Anfügen auf Kommentare); Verbindung einrichten (Weg direkt im anderen VLAN: beide Serveradressen in der Firewall freigeben; VPN nur zum Test, eigene Abfragezeit; Fingerabdruck; „Schreiben erlaubt“); Profil in fünf Schritten; Test lesen; Scharfschalten; Fehler beheben (Tabelle aus Spec Abschnitt 6 in Anwendersprache); Verhalten bei Rollentausch (Messergebnis aus Task 1).
- [ ] **Step 2:** `connections`: Kennwörter verschlüsselt, werden nicht mehr angezeigt, leer lassen = behalten. `vpn`: Tunnel als Weg einer Verbindung. `admin`: neue Rechte kommen automatisch aus `help.go`; nur der Hinweis auf den Bereichsadministrator.
- [ ] **Step 3:** `git fetch origin main` und `git show origin/main:VERSION` prüfen. `VERSION` → nächste Korrekturversion über `main` (erwartet `0.80.2`), `CHANGELOG.md` neuer Eintrag `## [0.80.2] – <Datum>` mit `### Neu` (Abgleich mit Fremdsystemen; Weg über VPN mit eigener Abfragezeit), `### Geändert` (Kennwort beim Bearbeiten leer lassen behält es), `### Sicherheit` (Kennwörter von Verbindungen verschlüsselt; Rechte für den Abgleich; Schreiben in fremde Datenbanken nur nach Freigabe an der Verbindung).
- [ ] **Step 4:** `go test ./internal/web/ -run 'Help' -v` und `go test ./... -run Changelog` → PASS.
- [ ] **Step 5:** Commit: `Handbuch und Änderungsprotokoll 0.80.2: Abgleich mit Fremdsystemen`.

### Task 15: Gesamtprüfung, Labor, Abnahme

- [ ] **Step 1:** `go vet ./...`, `go build ./...`, `go test ./...` in Git Bash mit `PDH_TEST_DSN` und `PDH_TEST_MSSQL_DSN` → alles grün. `go test -race ./internal/modules/extsync/...` → grün.
- [ ] **Step 2: Labor-Ablauf** lokal gegen `pdh-lab-mssql`, Ergebnis je Punkt festhalten: (a) 5 Zeilen → 5 Tickets, zweiter Lauf 0 neue; (b) fremde Änderung kommt an; (c) PDH-Änderung steht binnen 5 s im Labor; (d) Kommentar angehängt; (e) Widerspruch → PDH gewinnt, Verlauf; (f) SQL Server gestoppt → Läufe übersprungen, Warteliste wächst, nach Start nachgeholt in Reihenfolge; (g) fremde Zeile von Hand als `sa` gelöscht → Meldung am Ticket, nichts angelegt; (h) 250 Zeilen eingefügt → Profil hält an; (i) die Zeilenzahl der Meldungstabelle wurde durch das PDH nie kleiner, und unter den vom Benutzer `pdh_sync` ausgeführten Befehlen (Erweiterte Ereignisse oder `sys.dm_exec_query_stats`) ist kein `DELETE`.
- [ ] **Step 3:** Prüfung vor dem Merge nach `AGENTS.md`: `git fetch origin main`, `git merge origin/main`, Migrationsnummer und Version gegen `main`, Dopplungen (`grep` nach `sync_`, `/sync`, `sync.view`, `conn_secrets`), danach Schritt 1 wiederholen. Ergebnis für den Pull-Request-Text notieren.
- [ ] **Step 4: Anhalten und den Anwender fragen** – Freigabe für Push des Zweigs und Einspielen auf den Testverbund (10.10.0.14/.17). Befehle dafür vollständig nennen (Rechner, erwartetes Ergebnis).
- [ ] **Step 5 (nach Freigabe): Abnahme auf dem Testverbund:** dieselbe Beispieldatenbank direkt (Windows-PC, Port 14330, Freigabe für beide Serveradressen) und – wenn ein ausgehender Tunnel vorhanden ist oder der Anwender einen anlegt – über VPN mit eigenem Takt und eigener Zeitgrenze; Wechsel des Wegs: Profil fällt auf „getestet“, Verknüpfungen bleiben; Rollentausch mit gefüllter Warteliste: neuer aktiver Server arbeitet weiter, kein doppeltes Ticket, kein doppelter Kommentar; Oberfläche in Fiori und Fluent, hell und dunkel, schmal und breit.
- [ ] **Step 6: Anhalten** – Merge nach `main` nur mit ausdrücklicher Freigabe.

---

## Selbstprüfung gegen die Spec

| Spec | Task |
|---|---|
| 4.1 Daten | 2, 5 |
| 4.2 Hereinkommend | 7, 8, 11 |
| 4.3 Hinausgehend | 2 (Auslöser), 9, 11 |
| 4.4 Schutz | 4 („Schreiben erlaubt“, Rollenhinweis), 6, 9, 13 |
| 4.5 Cockpit | 12, 13 |
| 4.6 Test | 10, 13 |
| 4.7 Verbindung, VPN, Abfragezeit, Wechsel des Wegs, Zugangsdaten | 1, 3, 4, 11 |
| 4.8 Verbund | 11, 15 |
| 5 Rechte | 2, 12, 13 |
| 6 Fehlerbilder | 8, 9, 11, 15 |
| 7 Prüfung | 1, 15 |
| 8 Handbuch, Version | 14 |

Bewusste Festlegungen, die die Spec offen lässt: Die Warteliste füllen Datenbank-Auslöser (erfasst auch die Stellen in `internal/web`, die Tickets direkt per SQL ändern). Ein Statuswechsel auf „erledigt“ aus dem Fremdsystem nutzt `QuickResolve`. Zusammengesetzte Schlüssel werden als ein Textwert gespeichert (`EncodeKey`). Kommentare an Störmeldungen kommen aus `infrastructure_comments`. Beim Zurückschreiben zählt immer der aktuelle PDH-Wert, nicht der Wert aus dem Wartelisteneintrag.
