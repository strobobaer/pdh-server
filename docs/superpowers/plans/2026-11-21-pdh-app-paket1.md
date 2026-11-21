# PDH-App Paket 1 – Server-Grundlage – Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Der PDH-Server kann App-Geräte annehmen, über ein APP-Ticket freigeben, ihnen einen gerätegebundenen Token übergeben, jede Anfrage prüfen und Geräte sperren – noch ohne App, vollständig über Tests prüfbar.

**Architecture:** Neue Schnittstelle `/api/app/v1` neben `/api/terminal/v1` (öffentlich in `cmd/server/main.go` eingehängt, eigene Prüfung). Muster wie die Terminal-Kopplung (`internal/web/terminal_pair_api.go`, `terminal_devices.go`): nur Hashes speichern, `failLimiter`, `deviceCache`. Ein Gerät beweist sich mit einem P-256-Schlüssel (Signatur je Anfrage) plus Token; die Prüfung legt den zugeordneten PDH-Benutzer in den Request-Kontext (`"user"`), sodass `h.hasPerm`/`h.hasPermRec` unverändert gelten. Verwaltung (App-Konfiguration, App-Geräte, Einstellungen) als normale Web-Seiten.

**Tech Stack:** Go, chi, pgx/v5, PostgreSQL 16, `crypto/ecdsa` (P-256), `rsc.io/qr`, gohtml-Vorlagen mit htmx.

**Spec:** `docs/superpowers/specs/2026-11-21-pdh-app-design.md` (Abschnitte 6, 7, 10, 14, 14a, 15; Paket 1 in 16).

## Global Constraints

- Migrationen nur hinzufügend, nie ändern; neue Datei mit der **nächsten freien Nummer auf `origin/main`** (heute 187); danach `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- Alle neuen `id`-Spalten `UUID PRIMARY KEY DEFAULT pdh_uuid_v7()`; `go test -tags integration ./pkg/database -run TestUUIDv7Policy` darf für die neuen Tabellen nicht fehlschlagen.
- Jede neue Seite/Aktion/Schnittstelle prüft ein Recht; Datensatz-Aktionen mit Abteilung über `hasPermRec`/`hasPermIn`; Rechte nur weitergeben, die man hat (`mayGrant`).
- Wirksames Recht = Rolle/Benutzer ∩ Gerät; Geräte-Einstellungen schränken nur ein.
- Token-Präfix `pdha_`; gespeichert wird nur SHA-256 (hex). Geräteschlüssel P-256, öffentlicher Schlüssel als SubjectPublicKeyInfo (DER, base64url ohne Auffüllung).
- Zeitfenster der Signatur ±5 Minuten; Nonce je Gerät höchstens einmal in 10 Minuten.
- Sperre wirkt nach höchstens 30 s (Cache wie `terminal_devices`).
- `/api/v1` bleibt unverändert und lehnt App-Tokens ab.
- Texte, Handbuch, CHANGELOG, Commits auf Deutsch; Version = nächste Korrekturversion über `origin/main` (heute 0.82.12 → **0.82.13**).
- Oberfläche in Fiori und Fluent, hell/dunkel, nur Farbvariablen (`--bg`, `--text`, `--accent` …).
- Branch `feature/pdh-app` (liegt auf `origin/main`); kein Push/Merge ohne Freigabe.

## Review Focus

1. **Massenhafte Anfragen ohne Anmeldung** – `POST /requests` ist öffentlich; ein Skript darf keine Ticketflut erzeugen: höchstens 3 neue Anfragen je Quelle und Stunde, danach 429 (Test in Task 4).
2. **Dieselbe App fragt zweimal an** (Doppeltipp, Neustart) – gleicher Schlüssel mit offener Anfrage liefert dieselbe Anfrage zurück, kein zweites Ticket (Test in Task 4).
3. **Antwort mit dem Token geht verloren** – die App fragt erneut ab; solange das Gerät den Token noch nie benutzt hat, gibt der Server einen neuen aus und der alte wird ungültig; danach nie wieder (Test in Task 4).
4. **Benutzer wird im PDH deaktiviert oder Gerät gesperrt, während die App läuft** – nächste Anfrage nach höchstens 30 s `401 {"error":"app_revoked"}` (Test in Task 5).
5. **Manager gibt ein Gerät für jemanden frei, den er nicht verwalten darf** (fremde Abteilung, höhere Rolle) oder setzt „Anlegen“, ohne dass der Benutzer `app.create` hat – 403 bzw. Haken wirkungslos (Test in Task 6).

---

### Task 1: Migration – Rechte, Tabellen, Systembenutzer, Gruppe

**Files:**
- Create: `migrations/187_app_grundlage.up.sql`
- Modify: `migrations/checksums.txt` (per Befehl), `internal/web/rbac_guard.go:71` (`permCategoryOrder`)
- Test: `internal/web/app_migration_integration_test.go`

**Interfaces:**
- Produces (Tabellen/Konstanten, die alle späteren Tasks nutzen):
  - `app_requests(id, name TEXT, department_id UUID NULL → departments, device_label TEXT, platform TEXT CHECK IN ('ios','android'), app_version TEXT, public_key TEXT, key_fingerprint TEXT, status TEXT CHECK IN ('pending','approved','rejected') DEFAULT 'pending', ticket_id UUID NULL → tickets ON DELETE SET NULL, device_id UUID NULL, created_at, decided_at, decided_by UUID NULL → users)`; Index eindeutig auf `key_fingerprint WHERE status = 'pending'`.
  - `app_devices(id, request_id UUID → app_requests, user_id UUID NOT NULL → users ON DELETE CASCADE, public_key TEXT, key_fingerprint TEXT UNIQUE, token_sha256 TEXT UNIQUE NULL, token_issued_at, first_used_at, allowed_types TEXT[] NOT NULL DEFAULT '{fault,ticket,task,maintenance}', may_create BOOLEAN NOT NULL DEFAULT false, platform, app_version, label TEXT, created_at, approved_by UUID → users, last_seen_at, revoked_at, revoked_by UUID → users)`.
  - `tickets.app_request_id UUID NULL REFERENCES app_requests(id) ON DELETE SET NULL`.
  - Systembenutzer `appSystemUserID = "00000000-0000-4000-8000-0000000a0001"` (username `pdh-app`, Name „PDH-App“, inaktiv, `password_hash '!'`, `is_bot true`, Rolle `worker`) – wie `pdh-system` in Migration 072.
  - Gruppe `appAdminGroupID = "00000000-0000-4000-8000-0000000a0002"` („App-Administratoren“) in `user_groups`.
  - Rechte (Kategorie **„App“**): `app.use` „App: nutzen“, `app.create` „App: Vorgänge anlegen“, `app.cockpit` „App: Cockpits verwalten“, `app.sensors.view` „App: Telefon-Sensoren ansehen“, `app.admin` „App: Geräte freigeben und sperren (Bereichsadministrator)“.
  - Vergabe: `app.use` an **alle** Rollen; `app.create` und `app.admin` an `admin`, `manager`; `app.cockpit`, `app.sensors.view` an `admin`.
  - Go-Konstanten in `internal/web/app_ids.go`: `appSystemUserID`, `appAdminGroupID`, `permAppUse = "app.use"`, `permAppCreate`, `permAppCockpit`, `permAppSensors`, `permAppAdmin`, `appRef = "app"`; Eintrag `appRef: permAppAdmin` in `areaAdminPerm` (`perm_gate.go`).

- [ ] **Step 1: Failing test** `TestAppMigration` (`//go:build integration`, nutzt `newATEnv`): nach `RunMigrations` existieren die fünf Rechte mit Kategorie `App`; `role_permissions` von `viewer` enthält `app.use`, nicht `app.admin`; `manager` hat `app.admin`; Benutzer `appSystemUserID` ist `active=false`; Gruppe `appAdminGroupID` existiert; `INSERT INTO app_requests (name, platform, app_version, public_key, key_fingerprint) … RETURNING id` liefert eine UUID mit Version 7 (`id::text` Zeichen 15 = `7`); zweite offene Anfrage mit gleichem `key_fingerprint` scheitert mit Unique-Verletzung.
- [ ] **Step 2:** `PDH_TEST_DSN=… go test -tags integration ./internal/web -run TestAppMigration` → FAIL (Tabelle fehlt).
- [ ] **Step 3:** Migration schreiben (Kopf-Kommentar wie 171/183: Zweck, „niemand wird ausgesperrt“), `"App"` in `permCategoryOrder` vor `"Daten & Schnittstellen"` einfügen, `app_ids.go` und `areaAdminPerm` ergänzen.
- [ ] **Step 4:** `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`; dann `TestAppMigration`, `TestMigrationsFrozen` und `go test -tags integration ./pkg/database -run TestUUIDv7Policy` → die neuen Tabellen erscheinen nicht unter den Verstößen.
- [ ] **Step 5:** Commit `feat(app): Rechte, Tabellen und Systembenutzer für die PDH-App`.

### Task 2: Geräteschlüssel und Signaturprüfung

**Files:**
- Create: `internal/web/app_keys.go`
- Test: `internal/web/app_keys_test.go`

**Interfaces:**
- Produces:
  - `parseAppPublicKey(b64 string) (*ecdsa.PublicKey, error)` – base64url-SPKI, nur P-256, sonst Fehler.
  - `appKeyFingerprint(b64 string) string` – hex(SHA-256 der DER-Bytes).
  - `appConfirmCode(fingerprint string) string` – 6 Ziffern: erste 4 Byte des Fingerabdrucks als `uint32` big-endian `% 1_000_000`, mit Nullen links auf 6 aufgefüllt.
  - `appCanonical(method, pathWithQuery, ts, nonce string, body []byte) []byte` = `"PDHAPP1\n" + METHOD + "\n" + path + "\n" + ts + "\n" + nonce + "\n" + hex(sha256(body))`.
  - `verifyAppSignature(pub *ecdsa.PublicKey, r *http.Request, body []byte, now time.Time) error` – liest `X-PDH-Ts` (Unix-Sekunden), `X-PDH-Nonce` (16–64 Zeichen `[A-Za-z0-9_-]`), `X-PDH-Sig` (base64url ASN.1-DER); Fehler `errAppSigMissing`, `errAppSigStale` (|now−ts| > 5 min), `errAppSigBad`, `errAppNonceReused`.
  - `appNonces *nonceCache` mit `seen(key string, now time.Time) bool` (merkt Schlüssel `fingerprint+nonce` 10 Minuten, begrenzt auf 100 000 Einträge, Abgelaufenes zuerst entfernen).

- [ ] **Step 1: Failing tests** (`app_keys_test.go`, ohne Datenbank, Schlüssel per `ecdsa.GenerateKey(elliptic.P256(), rand.Reader)` und `x509.MarshalPKIXPublicKey`):
  - `TestAppConfirmCode`: Fingerabdruck `"00000001…"` → `"000001"`; `"ffffffff…"` → `"967295"`; Ergebnis immer 6 Ziffern.
  - `TestParseAppPublicKeyRejectsOtherCurves`: P-384- und RSA-Schlüssel → Fehler; P-256 → ok.
  - `TestVerifyAppSignature`: gültig → nil; geänderter Body → `errAppSigBad`; ts −6 min → `errAppSigStale`; gleiche Nonce zweimal → zweites Mal `errAppNonceReused`; fehlende Kopfzeile → `errAppSigMissing`; Pfad mit Query ist Teil der Signatur (anderer Query → `errAppSigBad`).
- [ ] **Step 2:** `go test ./internal/web -run 'TestAppConfirmCode|TestParseAppPublicKey|TestVerifyAppSignature'` → FAIL.
- [ ] **Step 3:** Implementieren (Nonce erst nach erfolgreicher Signatur merken, damit fremde Anfragen keine Nonces verbrauchen).
- [ ] **Step 4:** Tests → PASS; `go vet ./internal/web`.
- [ ] **Step 5:** Commit `feat(app): Geräteschlüssel, Bestätigungscode und Signaturprüfung`.

### Task 3: Öffentliche Einstiegs-Schnittstellen (Server, Erscheinungsbild, Abteilungen, Einrichtung)

**Files:**
- Create: `internal/web/app_public_api.go`, `web/templates/app_setup.gohtml`
- Modify: `cmd/server/main.go` (bei den Terminal-Routen, ~Zeile 507), `internal/web/app_ids.go` (Einstellungsschlüssel)
- Test: `internal/web/app_public_api_integration_test.go`

**Interfaces:**
- Consumes: `h.branding(ctx)` (`branding.go`), `publicBaseURL` (`labels.go:79`), `terminalJSON`/`terminalJSONErr`.
- Produces:
  - Konstanten `appAPILevel = 1`; Einstellungsschlüssel in `app_settings`: `app.addresses` (eine Adresse je Zeile), `app.allow_previous` (`"1"`/`"0"`, Grundwert `"1"`), `app.latest_version.ios`, `app.latest_version.android`, `app.previous_version.ios`, `app.previous_version.android`, `app.store_url.ios`, `app.store_url.android`.
  - `func (h *Handler) appMinVersion(ctx, platform string) string` = `allow_previous=="1" && previous!="" ? previous : latest`.
  - `GET /api/app/v1/server` → `{"name","api_level":1,"addresses":[…],"latest":{"ios","android"},"min":{"ios","android"},"branding_rev"}`; `addresses` beginnt mit `publicBaseURL`, dann `app.addresses` ohne Dubletten.
  - `GET /api/app/v1/branding` → `{"name","logo","theme","theme_color","style","font","rev"}`; `rev` = erste 16 hex von SHA-256 über die übrigen Felder.
  - `GET /api/app/v1/departments` → `[{"id","name"}]` nur aktive Abteilungen, nur diese zwei Felder.
  - `GET /app/setup` (ohne Anmeldung) → Seite mit Firmenlogo, Text „PDH-App installieren“, Knöpfen zu den Store-Adressen (nur gesetzte), Knopf „In der App öffnen“ → `pdh://setup?d=<host von publicBaseURL>`.
  - Alle vier ohne Anmeldung, `Cache-Control: no-store`.

- [ ] **Step 1: Failing test** `TestAppPublicAPI`: mit `e.setting("app.latest_version.android","1.2.0")`, `previous` `1.1.0`, `allow_previous` `1` → `min.android == "1.1.0"`; mit `allow_previous` `0` → `"1.2.0"`; ohne `previous` → `"1.2.0"`; `addresses` enthält die gesetzte Zusatzadresse genau einmal; `/branding` liefert dieselbe `rev` zweimal hintereinander und eine andere nach Änderung von `branding.theme_color`; `/departments` liefert keine inaktive Abteilung und kein Feld außer `id`/`name`; `/app/setup` → 200 mit `pdh://setup?d=`.
- [ ] **Step 2:** Test → FAIL.
- [ ] **Step 3:** Handler `AppServerAPI`, `AppBrandingAPI`, `AppDepartmentsAPI`, `AppSetupPage` implementieren; in `main.go` vor `r.Mount("/", …)` einhängen: `r.Get("/api/app/v1/server", …)`, `…/branding`, `…/departments`, `r.Get("/app/setup", …)`. Die Seite setzt `document.documentElement.dataset.style` (eigener Seitenkopf, siehe `AGENTS.md`).
- [ ] **Step 3b:** Kapitel-Gerüst anlegen: `<section class="hb-ch" id="pdh-app" data-pages="app_setup">` mit Überschrift „PDH-App“ und einem Satz in `web/templates/help.gohtml`, `"pdh-app"` in `helpChapterIDs` (`internal/web/help.go`). Tasks 6 und 7 hängen ihre Page-Kennungen an `data-pages` an; den Text schreibt Task 8.
- [ ] **Step 4:** Test → PASS; `go test ./internal/web -run TestHelp` grün.
- [ ] **Step 5:** Commit `feat(app): Server-Info, Erscheinungsbild und Einrichtungsseite für die App`.

### Task 4: Zugang anfragen, APP-Ticket, Token abholen

**Files:**
- Create: `internal/web/app_requests_api.go`
- Modify: `cmd/server/main.go`
- Test: `internal/web/app_requests_api_integration_test.go`

**Interfaces:**
- Consumes: Task 1 (Tabellen, `appSystemUserID`, `appAdminGroupID`), Task 2 (`parseAppPublicKey`, `appKeyFingerprint`, `appConfirmCode`, `verifyAppSignature`), `tickets.Repository.CreateTx` (`internal/modules/tickets/tickets.go:121`), `validDeviceName`, `limiterKey`, `clientIP`, `authLog`.
- Produces:
  - `POST /api/app/v1/requests` Body `{"name","department_id","device_label","platform","app_version","public_key"}` → 201 `{"request_id","confirm_code","status":"pending"}`.
  - `GET /api/app/v1/requests/{id}` (signiert mit dem Schlüssel der Anfrage, ohne Token) → `{"status":"pending"}` | `{"status":"rejected"}` | `{"status":"approved","token":"pdha_…","device_id"}`.
  - `var appRequestLimit = newFailLimiter(3, time.Hour)` (zählt **jede** neue Anfrage je `limiterKey`), `var appFailLimit = newFailLimiter(10, 5*time.Minute)` (Fehlversuche der Signatur/des Tokens).
  - `func newAppToken() (string, error)` (`pdha_` + 32 Zufallsbytes base64url) und `hashAppToken(string) string` (hex SHA-256).

- [ ] **Step 1: Failing tests** `TestAppRequestCreate`, `TestAppRequestPickup`:
  - Neue Anfrage → 201, 6-stelliger `confirm_code` = `appConfirmCode(fingerprint)`; genau ein Ticket mit `app_request_id`, Titel `App-Zugang: <Name> (<Abteilung>)`, `created_by = appSystemUserID`, `assigned_group_id = appAdminGroupID`, Status `open`.
  - Zweite Anfrage mit demselben Schlüssel, solange offen → 200 mit **derselben** `request_id`, weiterhin genau ein Ticket.
  - Vierte neue Anfrage (andere Schlüssel) aus derselben Quelle innerhalb einer Stunde → 429.
  - Ungültig → 422: leerer Name, Name > 120 Zeichen, unbekannte `department_id`, `platform` ≠ ios/android, P-384-Schlüssel, unbekanntes Feld im JSON, Body > 8 KiB.
  - Abholen offen → `pending`; ohne/mit falscher Signatur → 401 und zählt in `appFailLimit`; mit Signatur eines **anderen** Schlüssels → 401.
  - Nach Freigabe (Test setzt `app_devices` + `status='approved'` direkt per SQL wie Task 6 es tut) → `approved` mit Token; `token_sha256` = Hash davon. Zweites Abholen, solange `first_used_at IS NULL` → **neuer** Token, alter Hash ersetzt. Nach gesetztem `first_used_at` → `{"status":"approved"}` ohne Token.
  - Abgelehnt → `rejected`; danach mit **neuem** Schlüssel eine neue Anfrage möglich.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren: Anfrage + Ticket in **einer** Transaktion (`CreateTx`, danach `UPDATE tickets SET app_request_id`); Abteilungsname für den Titel aus `departments`; ohne Abteilung Titel `App-Zugang: <Name>`; Ticket-Beschreibung nennt Gerät, System, App-Version, Bestätigungscode. Token-Ausgabe beim Abholen in einer Transaktion mit `SELECT … FOR UPDATE` auf `app_devices`. Routen in `main.go` einhängen.
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(app): Zugang anfragen mit APP-Ticket und Token-Abholung`.

### Task 5: Prüfung jeder App-Anfrage (Token + Signatur + Sperre)

**Files:**
- Create: `internal/web/app_auth.go`
- Modify: `cmd/server/main.go`
- Test: `internal/web/app_auth_integration_test.go`

**Interfaces:**
- Consumes: Task 2, Task 4 (`hashAppToken`, `appFailLimit`), `newDeviceCache(30*time.Second)` (`terminal_devices.go:182`), `users.User`.
- Produces:
  - `func (h *Handler) appAuth(next http.Handler) http.Handler` – erwartet `Authorization: PDHApp <token>` + Signaturkopfzeilen; lädt Gerät über `token_sha256`, prüft Signatur mit `app_devices.public_key`, aktiv = `d.revoked_at IS NULL AND u.active` (gecacht 30 s, `appDeviceCache`), setzt `first_used_at` beim ersten Mal, `last_seen_at` höchstens einmal je Minute; legt den Benutzer (`*users.User` mit Rolle) unter `"user"` und das Gerät unter `appDeviceKey{}` in den Kontext.
  - `func appDevice(r *http.Request) *appDeviceCtx` mit Feldern `ID, UserID string; AllowedTypes []string; MayCreate bool`.
  - Fehlerantworten: 401 `{"error":"app_unauthorized"}` (unbekannt/Signatur falsch, zählt in `appFailLimit`), 401 `{"error":"app_revoked"}` (gesperrt oder Benutzer inaktiv), 426 `{"error":"app_outdated","min":"x.y.z"}` wenn Kopfzeile `X-PDH-App-Version` unter `appMinVersion(platform)` liegt (Vergleich numerisch je Stelle).
  - `GET /api/app/v1/me` (hinter `appAuth`) → `{"user":{"id","name","department"},"device_id","allowed_types":[…],"may_create":bool,"perms":{"app.use","app.create","app.cockpit","app.sensors.view","app.admin":bool}}`; `may_create` = `device.may_create && hasPerm(app.create)`; ohne `app.use` → 403 `{"error":"app_no_permission"}`.
  - Kein App-Token gilt an `/api/v1` (bestehende Prüfung in `pkg/middleware` erkennt `PDHApp` nicht als Bearer → 401; nur Test, keine Änderung).

- [ ] **Step 1: Failing tests** `TestAppAuth`:
  - Gültige Anfrage an `/me` → 200, `user.id` = zugeordneter Benutzer; `first_used_at` gesetzt.
  - Token richtig, Signatur mit fremdem Schlüssel → 401 `app_unauthorized`.
  - Gerät gesperrt (`revoked_at = NOW()`) und `appDeviceCache.forget(id)` → 401 `app_revoked`; ohne `forget` spätestens nach Ablauf des Caches (Test mit Cache-TTL 0 über `appDeviceCache = newDeviceCache(0)`).
  - Benutzer `active=false` → 401 `app_revoked`.
  - `X-PDH-App-Version: 1.0.0` bei `min.android = 1.1.0` → 426 mit `"min":"1.1.0"`; `1.10.0` > `1.9.0` gilt als neuer.
  - Benutzer ohne `app.use` (Rolle mit entzogenem Recht via `e.grant(uid,"app.use","",false)`) → 403.
  - `may_create`: Gerät `true`, Benutzer ohne `app.create` → `false`.
  - App-Token an `GET /api/v1/users/me` (über den echten Router aus `main.go` oder `middleware.Auth`) → 401.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren; in `main.go`: `r.With(webHandler.AppAuth).Get("/api/app/v1/me", webHandler.AppMeAPI)` (exportierter Wrapper `AppAuth` um `appAuth`).
- [ ] **Step 4:** Tests → PASS.
- [ ] **Step 5:** Commit `feat(app): Anmeldung der App mit gerätegebundenem Token, Sperre und Mindestversion`.

### Task 6: Seite „App-Konfiguration“ (aus dem APP-Ticket)

**Files:**
- Create: `internal/web/app_admin.go`, `web/templates/app_request.gohtml`
- Modify: `internal/web/handler.go` (`Routes()`, bei `/users/{id}/terminal-code` ~Zeile 915), `web/templates/ticket_detail.gohtml` (Knopf), `internal/web/perm_gate.go` (`recordDepartments` kennt `appRef` → `app_requests.department_id`)
- Test: `internal/web/app_admin_integration_test.go`

**Interfaces:**
- Consumes: Task 1–4, `h.hasPermRec`, `h.rbac.Outranks`, `getUser`, `h.addHistory`, `tickets.Service.UpdateStatus` (`tickets.go:500`), `withUser` (Test).
- Produces:
  - `GET /app/requests/{id}` – Seite mit Name, Abteilung, Gerät, System, Version, **Bestätigungscode-Eingabe**, Benutzersuche (aktive, nicht-System-Benutzer; Vorschlag: gleicher Nachname/Abteilung zuerst), Haken je Vorgangsart (`fault`, `ticket`, `task`, `maintenance`), Haken „Vorgänge aus der App anlegen“ (nur sichtbar, wenn der gewählte Benutzer `app.create` hat – Prüfung auch beim Speichern), Knöpfe „Freigeben“ / „Ablehnen“.
  - `POST /app/requests/{id}/approve` Felder `confirm_code`, `user_id`, `types[]`, `may_create` → legt `app_devices` an (Schlüssel/Fingerabdruck aus der Anfrage, `token_sha256 NULL`), Anfrage `approved`, `decided_*`, Ticket `closed`, Historie am Benutzer `app_device_approved`; Weiterleitung auf `/app/devices`.
  - `POST /app/requests/{id}/reject` → Anfrage `rejected`, Ticket `closed`, Historie.
  - Recht: `hasPermRec(r, permAppAdmin, appRef, requestID)` (Abteilung der Anfrage; ohne Abteilung nur globales `app.admin`) **und** `h.rbac.Outranks(actor.Role, target.Role)` **und** der Zielbenutzer liegt in einer Abteilung, für die der Handelnde `app.admin` hat (`hasPermIn` mit Abteilung des Benutzers).
  - Falscher Bestätigungscode → Seite mit Meldung „Der Bestätigungscode stimmt nicht mit dem Telefon überein.“, nichts gespeichert.
  - Ticket-Detail zeigt bei `app_request_id IS NOT NULL` den Knopf „App-Konfiguration öffnen“ → `/app/requests/{id}`.

- [ ] **Step 1: Failing tests** `TestAppApprove`:
  - Admin, richtiger Code → 303; `app_devices` mit `user_id`, `allowed_types` wie gewählt; Anfrage `approved`; Ticket `closed`.
  - Falscher Code → 200 mit der Meldung, keine Zeile in `app_devices`.
  - Manager mit `app.admin` nur in Abteilung A: Anfrage aus Abteilung B → 403; Zielbenutzer in B → 403; Zielbenutzer mit Rolle `admin` → 403 (Outranks).
  - `may_create=on` für Benutzer ohne `app.create` → gespeichert `false`.
  - Bereits entschiedene Anfrage erneut freigeben → 409, nichts verändert.
  - Viewer ohne `app.admin` → `GET /app/requests/{id}` 403.
  - `TestAppReject`: Anfrage `rejected`, Ticket `closed`, Abholen (Task 4) liefert `rejected`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren; Vorlage nach dem Muster vorhandener Detailseiten (`base.gohtml`), Page-Kennung `app_request` (an `data-pages` des Kapitels `pdh-app` anhängen). Rechte des Benutzers werden **nicht** pro Gerät vergeben: die Seite zeigt die wirksamen `app.*`-Rechte des gewählten Benutzers und verlinkt auf `/users/{id}/permissions` (Einzelrechte, vorhandene Seite). Cockpit-Auswahl folgt mit Paket 4.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web -run TestHelp` grün.
- [ ] **Step 5:** Commit `feat(app): App-Konfiguration – Gerät bestätigen, Benutzer zuordnen, freigeben`.

### Task 7: „App-Geräte“, Sperren, Einstellungen und Benutzermenü

**Files:**
- Modify: `internal/web/app_admin.go`, `internal/web/handler.go` (`Routes()`, `BaseData` + `baseData` ~Zeile 1459), `web/templates/base.gohtml:1077` (Benutzermenü), `web/templates/user_detail.gohtml` (Abschnitt „App-Geräte“), `internal/web/nav.go` (Eintrag unter Verwaltung, Recht `app.admin`)
- Create: `web/templates/app_devices.gohtml`, `web/templates/app_install.gohtml`
- Test: `internal/web/app_admin_integration_test.go` (ergänzen)

**Interfaces:**
- Consumes: Task 1–6, `appDeviceCache.forget`, `qrSVG` (`labels.go:51`).
- Produces:
  - `GET /app/devices` (Recht `app.admin` irgendwo; Liste gefiltert auf Geräte, deren Benutzer in den eigenen `app.admin`-Abteilungen liegt): Benutzer, Gerät, System, App-Version, zuletzt gesehen, Status; offene Anfragen mit Link zur App-Konfiguration.
  - `POST /app/devices/{id}/revoke` → `revoked_at`, `revoked_by`, `appDeviceCache.forget(id)`, Historie am Benutzer `app_device_revoked`. Gleiche Rechteprüfung wie Task 6 (Abteilung + Outranks des Gerätebenutzers).
  - Einstellungs-Formular auf `/app/devices` (nur mit **globalem** `app.admin`): „Adressen für die App“ (Textfeld, eine je Zeile, nur `http(s)://`), „Ältere App-Version erlauben“ (0/1), neueste Version iOS/Android (beim Ändern wandert der alte Wert nach `app.previous_version.*`), Store-Adressen iOS/Android. `POST /app/settings`.
  - `GET /app/install` (Recht `app.use`): QR-Code (`qrSVG`) mit `publicBaseURL + "/app/setup"`, darunter die Store-Knöpfe und zwei „So geht's“-Schritte.
  - `BaseData.CanAppUse bool` (`hasPermissionForUser(…, "app.use")`); Benutzermenü-Eintrag `<a href="/app/install" class="menu-item"><i class="ti ti-device-mobile"></i>{{t "App installieren"}}</a>` unter „Mein Konto“, nur `{{if .CanAppUse}}`.
  - `user_detail.gohtml`: Abschnitt „App-Geräte“ mit Sperren-Knopf (sichtbar nur mit Recht).

- [ ] **Step 1: Failing tests** `TestAppRevokeAndSettings`:
  - Sperren → nächste `/api/app/v1/me` → 401 `app_revoked` ohne Wartezeit (Cache vergessen).
  - Manager aus Abteilung A sieht in `/app/devices` kein Gerät eines Benutzers aus B; Sperren eines solchen Geräts → 403.
  - Einstellungen mit Abteilungs-`app.admin` → 403; mit globalem → gespeichert; neue neueste Version `1.3.0` bei bisher `1.2.0` → `app.previous_version.android == "1.2.0"`; Adresse `ftp://x` → abgelehnt mit Meldung.
  - `/app/install` ohne `app.use` → 403; mit → enthält `<svg` und `/app/setup`.
  - Benutzermenü: `baseData` für Benutzer ohne `app.use` → `CanAppUse == false`.
- [ ] **Step 2:** Tests → FAIL.
- [ ] **Step 3:** Implementieren; Page-Kennungen `app_devices` und `app_install` an `data-pages` des Kapitels `pdh-app` anhängen.
- [ ] **Step 4:** Tests → PASS; `go test ./internal/web -run TestHelp` grün.
- [ ] **Step 5:** Commit `feat(app): App-Geräte sperren, App-Einstellungen, „App installieren“ im Benutzermenü`.

### Task 8: Handbuch, Änderungsprotokoll, Version, Gesamtprüfung

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `pdh-app` aus Task 3 ausformulieren), `web/templates/widgets/help_figures.gohtml` (Skizze: Telefon „Zugang anfragen“ → APP-Ticket → App-Konfiguration → freigegeben), Kapitel „Administration“ (Rechte `app.*`, Gruppe „App-Administratoren“), `CHANGELOG.md`, `VERSION`, `AGENTS.md` (offener Punkt 20: Stand Paket 1)

- [ ] **Step 1:** Kapitel „PDH-App“ in Anwendersprache mit `.hb-howto`-Schritten: App installieren, Zugang anfragen (Mitarbeiter), freigeben mit Bestätigungscode (Administrator), App sperren, Einstellungen „Adressen“/„Versionen“. Hinweis: Die App selbst folgt mit den nächsten Paketen.
- [ ] **Step 2:** `CHANGELOG.md` `## [0.82.13] – <Datum>` mit `### Neu` (App-Zugang anfragen und freigeben, App-Geräte, „App installieren“) und `### Sicherheit` (neue Rechte `app.*`, gerätegebundener Token, Sperre); `VERSION` = `0.82.13` (vorher `git show origin/main:VERSION` prüfen, ggf. nächste Korrekturversion).
- [ ] **Step 3:** `go vet ./...` und `go test ./...` (Git Bash) sowie `PDH_TEST_DSN=… go test -tags integration ./internal/web -run 'App|Help' ./pkg/database` → alles grün, inkl. `TestHelpCoversAllPages`, `TestHelpChaptersMatch`, `TestChangelogMatchesVersion`, `TestMigrationsFrozen`.
- [ ] **Step 4:** Prüfung vor Merge nach `AGENTS.md` (Abschnitt „Prüfung vor jedem Merge“): `git fetch origin main`, mergen, Migrationsnummer und Version gegen `origin/main` prüfen, Dopplungen (`grep -rn "app\.use\|/api/app/v1"`) suchen; Ergebnis im Commit-Text festhalten.
- [ ] **Step 5:** Commit `docs(app): Handbuch, Änderungsprotokoll und Version 0.82.13 für die App-Grundlage`. Kein Push ohne Freigabe.
