# PDH-App Paket 2 – App-Grundgerüst und Vorgänge online – Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eine Android-App (Flutter, iOS-Code mitgeführt) richtet sich per Firmen-Domain ein, fragt Zugang an, holt den gerätegebundenen Token ab und arbeitet **mit Verbindung** an den eigenen Vorgängen (ansehen, annehmen, Maßnahme, Kommentar, Checkliste, abschließen), scannt PDH-QR-Codes und meldet Störungen – der Server bekommt dafür die nötigen `/api/app/v1`-Wege.

**Architecture:** Teil S (pdh-server, Branch `feature/pdh-app-paket2`): neue Dateien `internal/web/app_work*.go` hinter `appAuth`; schreibende Wege durch einen eigenen Wächter (Spiegel, Wartung, Doppel-Absende-Schutz), Rechte selbst geprüft, weil die Rechte-Regeln (`gateRules`) für App-Tokens nicht greifen. Fachlogik nur aus den vorhandenen Modulen/Helfern (`completionFinish`, `assetOpenItems`, `loadListMine`-SQL, `faults.Service`), nichts doppelt. Teil A (neues Repository `C:\workplace\pdh-app`, lokal): Flutter-App mit dünner Schicht `core/` (Schlüssel, signierter Client, Speicher) und `features/` (Einrichtung, Zugang, Vorgänge, Scanner, Melden, Einstellungen).

**Tech Stack:** Go/chi/pgx (Server); Flutter 3.50.1 / Dart 3.14, Kotlin (Android Keystore), Swift (Secure Enclave), Pakete `http`, `flutter_secure_storage`, `mobile_scanner`, `app_links`, `package_info_plus`, `url_launcher`, `uuid` (v7).

**Spec:** `docs/superpowers/specs/2026-11-21-pdh-app-design.md` (Abschnitte 6, 8, 10, 11, 14, 14a; Paket 2 in 16). Grundlage Paket 1: `docs/superpowers/plans/2026-11-21-pdh-app-paket1.md`.

## Global Constraints

- Signatur exakt wie Server (`internal/web/app_keys.go`): `"PDHAPP1\n"+METHOD+"\n"+RequestURI+"\n"+ts+"\n"+nonce+"\n"+hex(sha256(body))`; ECDSA P-256/SHA-256, Signatur ASN.1-DER base64url ohne Auffüllung; Kopfzeilen `Authorization: PDHApp <token>`, `X-PDH-Ts` (Unix-Sekunden), `X-PDH-Nonce` (16–64 `[A-Za-z0-9_-]`), `X-PDH-Sig`, `X-PDH-App-Version`. Öffentlicher Schlüssel als SPKI-DER base64url.
- Fehler kommen als `{"error": "<Text>", "code": "<code>"}`; die App entscheidet nur nach `code` (`app_unauthorized`, `app_revoked`, `app_unavailable`, `app_outdated`, `app_no_permission`, `app_rate_limited`, `app_invalid_input`, `app_conflict`, `app_too_large`; neu: `app_not_found`, `app_read_only`, `app_maintenance`).
- **Nur `app_revoked` löscht Token und Daten** auf dem Telefon; `app_unavailable`, Netzfehler und 5xx sind vorübergehend.
- Nach Sperre oder „App zurücksetzen“ erzeugt die App einen **neuen** Geräteschlüssel (Paket-1-Ruling R6).
- Wirksames Recht = Benutzer ∩ Gerät (`allowed_types`, `may_create`); die App kann nie mehr als das Web.
- Schreibende App-Wege beachten Spiegelserver (nur lesen) und Wartungssperre; jeder schreibende Aufruf trägt `X-PDH-Once` (UUIDv7, auf dem Telefon erzeugt) und wird genau einmal ausgeführt.
- Server: keine Migration geplant; falls doch nötig, nur hinzufügend, UUIDv7, nächste freie Nummer auf `origin/main`. Version = nächste Korrekturversion über `origin/main` zum Zeitpunkt des Merges (heute 0.82.20 → 0.82.21). Handbuch und CHANGELOG mitpflegen.
- App: Paket-Kennung `de.pdh.app`, Name „PDH“, URL-Schema `pdh`; Texte deutsch (weitere Sprachen später); Mindest-Android 8.0 (API 26), Ziel/compile API 37.
- Fiori/Fluent und hell/dunkel aus `/api/app/v1/branding` (`style`, `theme_color`); Logo aus `logo` (relativ zur Serveradresse).
- Kein Push, kein neues GitHub-Repository ohne Freigabe des Anwenders.

## Review Focus

1. **Netz bricht mitten in „Abschließen“ ab** – Wiederholung mit derselben `X-PDH-Once` darf nicht doppelt abschließen; der Server antwortet beim zweiten Mal mit dem aktuellen Stand (Test in S1/S4, A3).
2. **Server ist Spiegel oder in Wartung** – App zeigt „Nur lesen“ bzw. „Wartung“ statt Fehler, Aktionen sind gesperrt, nichts wird gelöscht (Test in S1, A3).
3. **Gerät darf Vorgangsart nicht** (`allowed_types` ohne `ticket`) – Tickets erscheinen weder in der Liste noch per Scan, Detail → 403 (Test in S2, S3, S5).
4. **QR-Code einer fremden Domain** (`pdh://open?d=andere.de`) oder eines anderen Servers – App öffnet nichts und sagt warum (Test in A4).
5. **Telefon mit falscher Uhr** – App meldet „Bitte Uhrzeit des Telefons prüfen“, statt endlos 401 zu bekommen; der Server liefert dafür seine Zeit in `/server` (`time`) (Test in S5, A3).

---

## Teil S – Server (pdh-server, Branch `feature/pdh-app-paket2`)

### Task S1: Wächter für schreibende App-Wege

**Files:**
- Create: `internal/web/app_write.go`, Test `internal/web/app_write_integration_test.go`
- Modify: `internal/web/app_public_api.go` (`/server` liefert zusätzlich `"time"`: Unix-Sekunden)

**Interfaces:**
- Consumes: `appAuth`, `appDevice(r)`, `appJSONErr`, `mirror.go` (Spiegel-Erkennung wie `MirrorMiddleware`/`mirrorGate`), `maint_lock.go` (Wartungssperre), Tabelle `request_once` (Migration 122, Muster `request_once.go`).
- Produces: `func (h *Handler) appWrite(next http.Handler) http.Handler` (nach `appAuth` zu hängen): Spiegel → 409 `app_read_only`; Wartungssperre aktiv → 503 `app_maintenance` mit `retry_after`; fehlende/ungültige `X-PDH-Once` (UUID) → 422 `app_invalid_input`; bekannte Once → Handler läuft mit Kontextwert `appReplay=true` und antwortet nur mit dem aktuellen Stand (keine gespeicherten Antworten, keine neue Tabelle); sonst in `request_once` eintragen und weiterreichen; Status ≥ 400 gibt die Once wieder frei.
  - `func appOnce(r *http.Request) string`.

- [ ] **Step 1: Failing tests** `TestAppWriteGuard`: Spiegelmodus (wie andere Tests den Spiegel simulieren) → 409 `app_read_only`; Wartungssperre gesetzt → 503 `app_maintenance`; ohne `X-PDH-Once` → 422; gleiche Once zweimal → nächster Handler läuft genau einmal (Zähler), zweite Antwort trägt Kopfzeile `X-PDH-Once-Replay: 1`; erste Antwort 500 → dieselbe Once darf erneut laufen. `/server` enthält `time` (±5 s zur Testzeit).
- [ ] **Step 2:** `go test -tags integration ./internal/web -run TestAppWriteGuard` → FAIL.
- [ ] **Step 3:** Implementieren. Bei bekannter Once ruft `appWrite` den Handler mit Kontextwert `appReplay=true` auf; schreibende Handler (S4) prüfen das und antworten dann nur mit dem aktuellen Stand, ohne zu schreiben.
- [ ] **Step 4:** Tests → PASS; `go vet ./internal/web`.
- [ ] **Step 5:** Commit `feat(app): Wächter für schreibende App-Wege (Spiegel, Wartung, einmalige Ausführung)`.

### Task S2: „Meine Vorgänge“

**Files:**
- Create: `internal/web/app_work.go`, Test `internal/web/app_work_integration_test.go`
- Modify: `internal/web/dashboard_widgets.go:567` (`loadListMine`): SQL in `func (h *Handler) myOpenWork(ctx context.Context, uid string, kinds []string, limit int) ([]myWorkRow, error)` herauslösen; das Widget ruft es mit `limit 8` auf (Verhalten unverändert).

**Interfaces:**
- Produces:
  - `type myWorkRow struct { Type, ID, Number, Title, Status, Priority, AssetID, AssetName string; Due *time.Time; UpdatedAt time.Time }`
  - `GET /api/app/v1/work` → `{"items":[{"type","id","number","title","status","priority","asset":{"id","name"},"due","updated_at"}]}`; Typen = `allowed_types` des Geräts ∩ Ansichtsrecht je Art (wie `loadListMine`: `faults.view`, `tickets.view`, `maintenance.view`, Aufgaben ohne Recht) ∩ Abteilungsbereich (`scopeSQL`); nur offene Status; höchstens 100; sortiert nach Fälligkeit (leer zuletzt), dann Priorität, dann `updated_at` absteigend.

- [ ] **Step 1: Failing tests** `TestAppWorkList`: Benutzer mit zugewiesener Störung, Ticket über seine Gruppe, Aufgabe als Beteiligter, Wartung zugewiesen → alle vier; Gerät `allowed_types={fault}` → nur die Störung; Benutzer ohne `tickets.view` → kein Ticket; geschlossene Vorgänge fehlen; Vorgang außerhalb der Abteilung des abteilungsbegrenzten Benutzers fehlt; vorhandener Widget-Test `loadListMine` bleibt grün.
- [ ] **Step 2:** → FAIL. **Step 3:** Implementieren, Route `r.With(AppAuth).Get("/api/app/v1/work", …)` in `cmd/server/main.go`. **Step 4:** → PASS.
- [ ] **Step 5:** Commit `feat(app): Meine Vorgänge für die App`.

### Task S3: Vorgang im Detail

**Files:**
- Modify: `internal/web/app_work.go`, Test `internal/web/app_work_integration_test.go`

**Interfaces:**
- Consumes: Modul-Lesefunktionen (`faults.Service.GetByID`, `GetActions`; `tickets.Service.GetByID`, `ListComments`, `GetActions`; `tasks.Service.GetByID` + Maßnahmen; `maintenance.Service.GetTaskByID`, `Steps`), `recordInScope`, `recordInvolved`, `completionKinds`, `closedStatus`, `canWriteAction` (record_actions.go:97), `canFinish` (completion.go:549).
- Produces: `GET /api/app/v1/work/{type}/{id}` → `{"type","id","number","title","description","status","priority","asset":{…},"due","actions":[{"text","by","at"}],"comments":[…] (nur ticket),"steps":[{"id","label","kind","value","done","required","min","max"}] (nur maintenance),"history":[letzte 20 {"action","by","at","message"}],"can":{"start","action","comment","step","finish"}}`.
  - Zugriff: Art in `allowed_types` **und** Ansichtsrecht oder Beteiligung **und** `recordInScope` – sonst 403 `app_no_permission`; unbekannt → 404 `app_not_found`; unbekannter Typ → 404.
  - `can.*` exakt nach den Regeln des Webs: start = `hasPermRec(EditPerm)` oder `completionInvolved`, nicht geschlossen; action = `canWriteAction`; comment = nur ticket, Ticket nicht geschlossen, wie `RecordCommentsAddWeb`; step = maintenance, `maintenance.edit` (Rec) oder beteiligt, nicht abgeschlossen; finish = `canFinish`.

- [ ] **Step 1: Failing tests** `TestAppWorkDetail`: je Art ein Vorgang → Felder vorhanden, `steps` nur bei Wartung, `comments` nur bei Ticket; Gerät ohne `ticket` → Ticket 403; fremde Abteilung → 403; Betrachter → `can.finish=false`, `can.start=false`; beteiligter Werker ohne `faults.edit` → `can.action=true` (wie Web); geschlossener Vorgang → alle `can` false.
- [ ] **Step 2–4:** FAIL → implementieren (Route in `main.go`) → PASS.
- [ ] **Step 5:** Commit `feat(app): Vorgang im Detail mit erlaubten Aktionen`.

### Task S4: Aktionen an Vorgängen

**Files:**
- Modify: `internal/web/app_work.go` (oder `app_work_actions.go`, wenn `app_work.go` > 400 Zeilen), Test `internal/web/app_work_actions_integration_test.go`

**Interfaces:**
- Consumes: S1 `appWrite`, S3 Zugriffsprüfung und `can.*` (dieselbe Funktion `func (h *Handler) appWorkCan(r *http.Request, kind, id string) (appWorkPerms, error)`), `completionFinish`, Modul-`AddAction`, `tickets.Service.AddComment`, `maintenance.Service.SaveStep`, `StartTask`/Status „in_progress“ wie `CompletionWeb` mit `Finish=false`, `h.addHistory`.
- Produces (alle `POST`, hinter `AppAuth` + `appWrite`, Antwort = aktuelles Detail wie S3):
  - `/api/app/v1/work/{type}/{id}/start` – Vorgang annehmen/beginnen (übernimmt Zuweisung an sich, wenn unzugewiesen – wie `completionInvolved` „übernehmen“; Status `in_progress`; Historie „work“).
  - `/actions` `{"text"}` (1–4000 Zeichen).
  - `/comments` `{"text"}` (nur ticket).
  - `/steps/{stepID}` `{"value","done"}`.
  - `/finish` `{"text","no_parts":bool,"root_cause"?}` → `completionFinish` mit `completionRequest{Comment:text, NoParts, RootCause}`; Fehler des Moduls (z. B. „Ursache fehlt“, „Pflichtschritt offen“) → 422 `app_invalid_input` mit Modultext.
  - Jede Aktion prüft ihr `can.*`; sonst 403. Bei `appReplay` nur das Detail zurückgeben.

- [ ] **Step 1: Failing tests** `TestAppWorkActions`: start setzt `in_progress` und weist zu; action legt genau eine Maßnahme an, auch bei zweimal gleicher Once (Review Focus 1); comment nur bei Ticket (Störung → 404 oder 403 `app_no_permission`); step mit Zahl außerhalb des Bereichs → 422 mit Modultext; finish ohne Maßnahme → 422; finish mit Maßnahme und `no_parts` → Status geschlossen/erledigt, `can.*` danach false; Spiegel → 409 (über S1); Betrachter → 403 für alle.
- [ ] **Step 2–4:** FAIL → implementieren (Routen in `main.go`) → PASS.
- [ ] **Step 5:** Commit `feat(app): Vorgänge in der App annehmen, Maßnahme, Kommentar, Checkliste, abschließen`.

### Task S5: Scannen und Störung melden

**Files:**
- Create: `internal/web/app_scan.go`, Test `internal/web/app_scan_integration_test.go`

**Interfaces:**
- Consumes: Auflösung wie `/scan?code=` (`internal/web/scan.go`), `assetOpenItems` (asset_info.go:263) mit `copilotKindByKey`, `faults.Service.Create`, `h.markPlantStopped` (push.go:673), `appDevice(r)`.
- Produces:
  - `GET /api/app/v1/scan?code=<Text>` → `{"kind":"asset","asset":{"id","name","path"},"items":[wie S2-Zeilen],"may_report":bool}` für `/a/<uuid>`, `/e/<uuid>`, bare UUID einer Anlage; `{"kind":"web","url":"<absolute PDH-URL>"}` für `/inventory/…`, `/k/…` (Lager und Kanban folgen später, Paket-1-Ruling „nur mit Verbindung“); unbekannt → 404 `app_not_found`. Codes mit fremdem Host (nicht in den Server-Adressen) → 404 `app_not_found`. `items` gefiltert wie S2 (Gerät, Rechte, Bereich).
  - `may_report` = `appDevice.MayCreate && hasPerm(app.create)`.
  - `POST /api/app/v1/faults` (hinter `appWrite`) `{"asset_id","title","text","plant_stopped":bool}` → 201 `{"type":"fault","id"}`; ohne `may_report` → 403; Titel 1–200, Text ≤ 4000, Anlage muss existieren und im Bereich liegen; Schwere „high“ bei `plant_stopped`, sonst „medium“ (wie Easy-Mode); `plant_stopped` → `markPlantStopped`; Historie „create“.
  - Hinweis: die Regel „offene Störung an derselben Anlage → anhängen“ (Spec 9.3.5) gilt nur für Offline-Meldungen und kommt mit Paket 3.

- [ ] **Step 1: Failing tests** `TestAppScan`: `https://<publicBase>/a/<id>` → asset mit offener Störung; `/e/<id>` und bare UUID → gleich; `/inventory/x/booking` → kind web mit absoluter URL; fremder Host → 404; Gerät ohne `fault` → items ohne Störungen. `TestAppReportFault`: mit `may_create` und `app.create` → 201, Störung mit Schwere medium; `plant_stopped` → high und Anlage als stehend markiert; ohne Recht → 403; Titel leer → 422; doppelte Once → eine Störung.
- [ ] **Step 2–4:** FAIL → implementieren (Routen) → PASS.
- [ ] **Step 5:** Commit `feat(app): QR-Codes auflösen und Störung aus der App melden`.

### Task S6: Handbuch, Änderungsprotokoll, Prüfung

**Files:** `web/templates/help.gohtml` (Kapitel `pdh-app`: „Mit der App arbeiten“ – Vorgänge, Scannen, Melden; Hinweis Spiegel/Wartung „nur lesen“), `CHANGELOG.md`, `VERSION`, `AGENTS.md` (Punkt 22)

- [ ] **Step 1:** Handbuch-Abschnitt in Anwendersprache mit `.hb-howto`.
- [ ] **Step 2:** CHANGELOG-Eintrag (nächste Korrekturversion über `origin/main`), `### Neu` (App-Schnittstellen für Vorgänge, Scannen, Melden), `### Sicherheit` (Rechte wie im Web, einmalige Ausführung, Spiegel/Wartung).
- [ ] **Step 3:** `go vet ./...`, `go test ./...`, `go test -tags integration ./internal/web -run 'App|Help|Widget'` (DB `pdh_test_app`), gofmt der committeten Dateien.
- [ ] **Step 4:** Commit `docs(app): Handbuch und Änderungsprotokoll für die App-Schnittstellen`.

## Teil A – App (neues Repository `C:\workplace\pdh-app`)

Werkzeuge: `C:\Users\michael\dev\flutter\bin\flutter`, `JAVA_HOME=C:\Users\michael\dev\jdk-17`, `ANDROID_HOME=C:\Users\michael\dev\android-sdk` (NDK 28.2.13676358, Plattform 37, Emulator-AVD `pdh_test`). Tests: `flutter test`; Analyse `flutter analyze` ohne Befunde.

### Task A1: Projekt anlegen

**Files:** `C:\workplace\pdh-app\` (`flutter create --org de.pdh --project-name pdh_app --platforms android,ios`), `pubspec.yaml`, `analysis_options.yaml` (`flutter_lints`), `README.md` (Bauen, Testen, Werkzeugpfade), `lib/main.dart`, `lib/app.dart`, Ordner `lib/core/`, `lib/features/`, `test/`.

**Interfaces:**
- Produces: `applicationId`/Bundle-ID `de.pdh.app`, App-Name „PDH“, `minSdk 26`, URL-Schema `pdh` (Android `intent-filter` scheme `pdh`, iOS `CFBundleURLTypes`), Kameraberechtigung mit Begründung „Zum Scannen der PDH-QR-Codes“ (Android-Manifest, iOS `NSCameraUsageDescription`), `INTERNET`. Abhängigkeiten in genau diesen Paketen (aktuelle stabile Versionen, in `pubspec.lock` festgehalten): `http`, `flutter_secure_storage`, `mobile_scanner`, `app_links`, `package_info_plus`, `url_launcher`, `uuid`; dev: `flutter_lints`, `pointycastle` (nur für Tests).
- `git init`, erster Commit. **Kein** GitHub-Repository anlegen.

- [ ] **Step 1:** Projekt erzeugen, Pakete eintragen, `flutter pub get`.
- [ ] **Step 2:** Test `test/smoke_test.dart`: App startet und zeigt „PDH“.
- [ ] **Step 3:** `flutter analyze` → 0 Befunde, `flutter test` → PASS, `flutter build apk --debug` → APK gebaut.
- [ ] **Step 4:** Commit `chore: Flutter-Projekt PDH-App angelegt`.

### Task A2: Geräteschlüssel (Android Keystore, iOS Secure Enclave)

**Files:**
- Create: `lib/core/device_key.dart`, `android/app/src/main/kotlin/de/pdh/app/DeviceKeyPlugin.kt` (in `MainActivity` registriert), `ios/Runner/DeviceKeyPlugin.swift` (in `AppDelegate` registriert), `test/core/device_key_test.dart`, `test/support/fake_device_key.dart`

**Interfaces:**
- Produces:
  - `abstract class DeviceKey { Future<String> ensureKey(); Future<String> publicKeySpki(); Future<Uint8List> sign(Uint8List data); Future<void> reset(); }` – `ensureKey` legt den Schlüssel an, falls keiner da ist, und gibt die SPKI (base64url) zurück; `sign` liefert ASN.1-DER.
  - `class PlatformDeviceKey implements DeviceKey` über `MethodChannel('de.pdh.app/device_key')`, Methoden `ensureKey`, `publicKey`, `sign` (Argument `Uint8List`), `reset`.
  - Android: `KeyPairGenerator` `EC` in `AndroidKeyStore`, Alias `pdh_device`, `secp256r1`, `PURPOSE_SIGN`, Digest `SHA256`, StrongBox versuchen, sonst ohne; Signatur `SHA256withECDSA` (liefert DER); SPKI = `publicKey.encoded`.
  - iOS: `SecKeyCreateRandomKey` mit `kSecAttrTokenIDSecureEnclave` (Simulator ohne Enclave: Rückfall Schlüsselbund), Tag `de.pdh.app.device`, `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`; Signatur `ecdsaSignatureMessageX962SHA256` (DER); SPKI = festes P-256-Präfix `3059301306072a8648ce3d020106082a8648ce3d030107034200` + X9.63-Punkt. (iOS wird hier nicht gebaut – kein Mac; Code muss kompilierbar aussehen, Prüfung folgt auf einem Mac.)
  - `FakeDeviceKey` (Test): P-256 mit `pointycastle`, DER-Signatur.
- [ ] **Step 1: Failing test** `device_key_test.dart`: Fake erzeugt SPKI, die mit dem festen Präfix beginnt (hex) und 91 Byte lang ist; Signatur über Bytes wird mit dem öffentlichen Schlüssel (pointycastle) verifiziert; `reset` → neue SPKI ≠ alte.
- [ ] **Step 2–4:** FAIL → implementieren → PASS; `flutter build apk --debug` baut mit dem Kotlin-Teil.
- [ ] **Step 5:** Commit `feat: Geräteschlüssel im Android Keystore (iOS Secure Enclave vorbereitet)`.

### Task A3: Signierter Server-Client

**Files:**
- Create: `lib/core/api_client.dart`, `lib/core/api_errors.dart`, `lib/core/secure_store.dart`, `test/core/api_client_test.dart`, `test/core/canonical_vector_test.dart`
- Modify (pdh-server, Branch `feature/pdh-app-paket2`): `internal/web/app_keys_test.go` – Test `TestAppCanonicalVector` mit festem Vektor (Methode `POST`, URI `/api/app/v1/work/fault/0190.../actions?x=1`, ts `1767225600`, Nonce `AAAAAAAAAAAAAAAAAAAAAA`, Body `{"text":"ä"}`) → erwartete kanonische Bytes als hex in der Testdatei; derselbe hex-Wert steht im Dart-Test.

**Interfaces:**
- Produces:
  - `Uint8List appCanonical(String method, String requestUri, int ts, String nonce, List<int> body)` – byte-gleich zum Server.
  - `class ApiClient { ApiClient({required ServerConfig server, required DeviceKey key, required SecureStore store, http.Client? http, DateTime Function()? now}); Future<Map<String,dynamic>> get(String path, {bool signed = true}); Future<Map<String,dynamic>> post(String path, Object body, {String? once}); }` – probiert die Adressen von `server.addresses` der Reihe nach, merkt die zuletzt erfolgreiche; Zeitabweichung: nutzt den Versatz aus `/server.time`; schreibende `post` setzen `X-PDH-Once` (UUIDv7, vom Aufrufer übergeben oder neu).
  - `sealed class AppError` mit Unterklassen je `code` (Global Constraints) plus `NetworkError`, `ClockSkewError`; `AppError.fromResponse(int status, Map body)`.
  - `class SecureStore` (über `flutter_secure_storage`): `server`, `token`, `requestId`, `brandingRev`; `wipe()` löscht alle gespeicherten Werte.
- [ ] **Step 1: Failing tests** `canonical_vector_test.dart` (hex gleich Server-Vektor); `api_client_test.dart` mit `MockClient`: Kopfzeilen vorhanden und Signatur verifizierbar (FakeDeviceKey); erste Adresse nicht erreichbar → zweite benutzt; Antwort `{"code":"app_revoked"}` → `RevokedError`; `app_unavailable`/503/Netzfehler → vorübergehend, `wipe` nicht aufgerufen; zwei `post` mit derselben `once` senden dieselbe `X-PDH-Once`; Telefonuhr 10 min falsch, `/server.time` korrekt → signiert mit korrigierter Zeit; 401 `app_unauthorized` direkt nach Zeitabgleich mit Abweichung > 5 min → `ClockSkewError` (Review Focus 5).
- [ ] **Step 2–4:** FAIL → implementieren → PASS (Dart und `go test ./internal/web -run TestAppCanonicalVector`).
- [ ] **Step 5:** Commit (App) `feat: signierter Server-Client mit Fehlercodes und Adresswechsel`; Commit (Server) `test(app): fester Signatur-Vektor für die App`.

### Task A4: Einrichtung und Erscheinungsbild

**Files:**
- Create: `lib/features/setup/setup_screen.dart`, `lib/features/setup/setup_logic.dart`, `lib/core/branding.dart`, `lib/core/links.dart`, Tests `test/features/setup_logic_test.dart`, `test/core/links_test.dart`

**Interfaces:**
- Consumes: A3.
- Produces:
  - `ServerConfig { String domain; List<String> addresses; String name; }`.
  - `Future<ServerConfig> probeDomain(String input, http.Client c)` – nimmt `pdh.firma.de`, `https://pdh.firma.de`, `https://pdh.firma.de/app/setup` oder `pdh://setup?d=pdh.firma.de`; ruft `https://<domain>/api/app/v1/server` (ohne Schema: `https`), erwartet `api_level ≥ 1`; sonst Fehlertext „Unter dieser Adresse antwortet kein PDH.“.
  - `Branding { String name; String? logoUrl; String style /*fiori|fluent*/; Color accent; String rev; }`, `ThemeData buildTheme(Branding b, Brightness br)` (Fluent: eckiger, Fiori: abgerundet wie Web-Stile), gespeichert mit `rev`; neu geladen, wenn `rev` vom Server abweicht.
  - `AppLink parseLink(Uri u, ServerConfig? cfg)` → `SetupLink(domain)` | `OpenLink(path)` | `ForeignLink` | `Unknown`; `pdh://open` mit `d` ≠ eigener Domain → `ForeignLink` (Review Focus 4).
  - Bildschirm: Feld „Firmen-Domain“, Knopf „QR-Code scannen“ (A7-Scanner im Modus „Einrichtung“), Bestätigung mit Firmenname und Logo, „Mit diesem Server verbinden“.
- [ ] **Step 1: Failing tests:** alle vier Eingabeformen → gleiche Domain; Server ohne `api_level` → Fehlertext; `parseLink` für eigene/fremde Domain; `buildTheme` setzt `colorScheme.primary` = `accent`.
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Commit `feat: Einrichtung per Firmen-Domain, Logo und Farben vom PDH`.

### Task A5: Zugang anfragen, Freigabe abwarten, Sperre

**Files:**
- Create: `lib/features/access/lock_screen.dart`, `lib/features/access/request_screen.dart`, `lib/features/access/access_logic.dart`, `lib/core/session.dart`, `lib/features/update/update_screen.dart`, Tests `test/features/access_logic_test.dart`

**Interfaces:**
- Consumes: A2, A3, A4; Server `/api/app/v1/departments`, `/requests`, `/requests/{id}`, `/me`.
- Produces:
  - `enum SessionState { setup, locked, waiting, ready, outdated, revoked }`; `class Session extends ChangeNotifier` mit `state`, `me`, `start()`, `requestAccess(name, departmentId)`, `poll()`, `resetApp()`.
  - Warten: Abfrage `/requests/{id}` mit Abstand 3 s, verdoppelnd bis höchstens 60 s; `approved`+Token → speichern → `/me` → `ready`; `rejected` → Hinweis „Die Anfrage wurde abgelehnt.“ und neuer Schlüssel vor der nächsten Anfrage.
  - `RevokedError` an beliebiger Stelle → `wipe()`, `DeviceKey.reset()`, Zustand `locked` mit Hinweis „Diese App wurde gesperrt. Bitte den Zugang neu anfragen.“
  - `OutdatedError` (426) → `update_screen` blockiert mit Knopf zum Store (Store-Adressen als Konstanten `kStoreUrlAndroid`/`kStoreUrlIos` in `lib/core/constants.dart`, vorerst leer → Text „Bitte die App aktualisieren.“).
  - Sperrbildschirm zeigt nur Logo, Firmenname und „Zugang anfragen“; Warteschirm zeigt den 6-stelligen Code groß.
- [ ] **Step 1: Failing tests** (Fake-API): Anfrage → `waiting` mit Code; Abfrage-Abstände 3, 6, 12 … 60; approved → `ready`; rejected → Schlüssel neu; `app_revoked` aus `/me` → Daten gelöscht, Schlüssel neu, `locked`; `app_unavailable` → Zustand bleibt, nichts gelöscht; 426 → `outdated`.
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Commit `feat: Zugang anfragen, Freigabe abwarten, Sperre und Pflicht-Update`.

### Task A6: Startseite und Vorgänge

**Files:**
- Create: `lib/features/home/home_screen.dart` (Leiste unten: „Vorgänge“, „Scannen“, „Melden“ nur bei `may_create`, „Mehr“), `lib/features/work/work_list_screen.dart`, `lib/features/work/work_detail_screen.dart`, `lib/features/work/work_api.dart`, `lib/core/connectivity_banner.dart`, `lib/features/settings/settings_screen.dart`, Tests `test/features/work_test.dart`

**Interfaces:**
- Consumes: A3, A5; Server S2–S4.
- Produces:
  - `class WorkApi { Future<List<WorkItem>> list(); Future<WorkDetail> detail(String type, String id); Future<WorkDetail> act(String type, String id, String action, Map body, {required String once}); }`.
  - Liste mit Ziehen-zum-Aktualisieren, Symbol je Art, Fälligkeit rot wenn überfällig. Detail zeigt nur Knöpfe, deren `can.*` true ist; „Abschließen“ fragt Text und „Keine Ersatzteile benötigt“; Modulfehler (422) als Text anzeigen.
  - Eine `once` je Benutzeraktion (beim Antippen erzeugt, bei Wiederholung nach Netzfehler gleich).
  - Balken oben „Keine Verbindung“ bei Netzfehler, „Nur lesen (Spiegelserver)“ bei `app_read_only`, „Wartung – bitte später erneut versuchen“ bei `app_maintenance`; Aktionsknöpfe dann aus. (Arbeiten ohne Verbindung folgt mit Paket 3.)
  - „Mehr“: Server, Benutzer, App-Version, „App zurücksetzen“ (mit Rückfrage; löscht Daten und Schlüssel).
- [ ] **Step 1: Failing widget tests:** Liste zeigt Einträge; Detail ohne `can.finish` zeigt keinen Abschließen-Knopf; Abschließen nach Netzfehler wiederholt mit derselben `once`; `app_read_only` → Balken und Knöpfe aus; „App zurücksetzen“ → `setup`.
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Commit `feat: Startseite, Meine Vorgänge und Aktionen`.

### Task A7: Scanner und Störung melden

**Files:**
- Create: `lib/features/scan/scan_screen.dart` (Modi `setup` und `work`), `lib/features/scan/scan_logic.dart`, `lib/features/report/report_screen.dart`, Tests `test/features/scan_logic_test.dart`, `test/features/report_test.dart`

**Interfaces:**
- Consumes: A3, A4 (`parseLink`), A6; Server S5.
- Produces:
  - Scan im Modus `work` → `/api/app/v1/scan?code=` → Anlagenseite (Name, Pfad, offene Vorgänge → Detail, „Störung melden“ bei `may_report`) | `kind=web` → im Browser öffnen (`url_launcher`) | 404 → „Dieser Code gehört nicht zu diesem PDH.“
  - Eingehender Link `pdh://open?p=/a/<id>` landet auf derselben Anlagenseite.
  - Melden: Titel, Beschreibung, Schalter „Anlage steht“, Anlage aus Scan (oder per Scan wählen); `POST /api/app/v1/faults` mit `once`; danach Detail der neuen Störung.
- [ ] **Step 1: Failing tests:** `scan_logic`: Anlagen-Antwort → AssetView; web → öffnet URL; 404 → Text; `report`: leerer Titel → Knopf aus; Erfolg → Navigation zur Störung; doppeltes Absenden → eine `once`.
- [ ] **Step 2–4:** FAIL → implementieren → PASS.
- [ ] **Step 5:** Commit `feat: QR-Codes scannen und Störung melden`.

### Task A8: Ende-zu-Ende-Abnahme am Emulator

**Files:** `docs/abnahme-paket2.md` im App-Repository (Schritte und Ergebnis), ggf. kleine Korrekturen.

- [ ] **Step 1:** PDH-Server lokal starten (`go run ./cmd/server` aus `feature/pdh-app-paket2` gegen Labor-DB `pdh_test_app`, Port 8090), Prüf-Administrator nur in dieser Labor-DB anlegen (Kennwort nur im Scratchpad), in „Adressen für die App“ `http://10.0.2.2:8090` freigeben (Emulator → Rechner).
- [ ] **Step 2:** Emulator `pdh_test` starten, Debug-APK installieren; Ablauf: Domain eingeben → Bestätigen → Zugang anfragen → im Browser am Rechner App-Konfiguration mit Code freigeben → App wird bereit → Vorgang aus „Meine Vorgänge“ annehmen, Maßnahme, abschließen → Anlage per QR (Bild in der Emulator-Kamera oder Link `pdh://open`) → Störung melden → im Web sperren → App zeigt Sperrhinweis.
- [ ] **Step 3:** Bildschirmfotos der Hauptschritte in `docs/abnahme-paket2/`; Ergebnis und Abweichungen in `docs/abnahme-paket2.md`.
- [ ] **Step 4:** Labor-Prüfdaten entfernen; Commit `docs: Abnahme Paket 2 am Emulator`.

## Nicht in Paket 2 (spätere Pakete)

Arbeiten ohne Verbindung und Zusammenführen (3), automatische Cockpits (4), Push (5), Widgets und Telefon-Sensoren (6), Store-Einträge, Vorführ-Server, iOS-Bau auf einem Mac (7), weitere Sprachen der App.
