# Energie-Messstellen: Fassungen der Formel und rückwirkendes Neurechnen (Paket 2b) – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Die Formel einer berechneten Energie-Messstelle bekommt Fassungen mit „gültig ab“; eine Fassung mit einem Datum bis heute rechnet die gespeicherten Minutenwerte der Messstelle und aller, die auf ihr aufbauen, im Hintergrund neu und erzeugt noch nicht verschickte Efficio-Dateien neu.

**Architecture:** Fassungen liegen in `energy_formula_versions`; `energy_meters.formula` bleibt die geltende Formel und wird vom Minutentakt je Minute auf die Fassung des Tages gestellt. Der Rechenweg einer Minute wird aus `Sampler.evalVirtual` in eine reine Funktion `evalMinute` gezogen, die Minutentakt und Neurechnung gemeinsam nutzen. Ein Lauf (`energy_recalc_runs`) rechnet Tag für Tag, jeder Tag in einer Transaktion; solange er läuft, lässt der Minutentakt die betroffenen Messstellen aus und der Efficio-Export setzt aus. Der Schlussschritt rechnet bis zur zuletzt abgelegten Minute unter derselben Sperre wie der Minutentakt, danach erzeugt der Export die nicht verschickten Dateien neu.

**Tech Stack:** Go (chi, html/template, pgx), htmx/Vanilla-JS, PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-11-24-energie-fassungen-design.md` (vom Anwender am 24.11.2026 bestätigt: „2b: 1 Vorschlag, 2a, 3–5 passt“)

## Global Constraints

- Version: nächste Korrekturversion über `origin/main` (jetzt 0.82.49 → **0.82.50**); vor dem Merge `git show origin/main:VERSION` prüfen.
- Migration: nächste freie Nummer über `origin/main` (jetzt **214**), Eintrag in `migrations/checksums.txt` mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`; neue Kennungen mit `pdh_uuid_v7()`.
- „Gültig ab einem Datum, 00:00 Uhr Ortszeit; keine Uhrzeit.“ Kalendertage im Go-Code als Mitternacht UTC (wie pgx DATE liefert), Tagesgrenzen über `Store.Loc` (Vorgabe `time.Local`, in Tests UTC bzw. Europe/Berlin). Nie `Truncate(24*time.Hour)` (`TestNoUTCDayTruncate`).
- Es gilt die Fassung mit dem spätesten `valid_from`, das nicht nach dem Tag liegt; am selben Tag die zuletzt angelegte. Vor der ersten Fassung gilt die erste Fassung.
- Je Datum höchstens eine **geplante** Fassung. Ändern und löschen lässt sich nur eine geplante Fassung.
- Rückwirkend höchstens bis zum Tag des ältesten gespeicherten Minutenwerts **dieser** Messstelle (Entscheidung 1).
- Efficio (Entscheidung 2 a): Dateien, von denen noch nichts verschickt ist, werden neu erzeugt; verschickte oder abgeholte bleiben und werden als „schon verschickt, nicht geändert“ gelistet. Bei „Zum Abholen im PDH“ zählt eine Datei als verschickt, sobald sie einmal heruntergeladen wurde.
- Rechte (Entscheidung 3), kein neues Recht: geplante Fassung anlegen, ändern, löschen mit `energy.edit` am Datensatz; eine Fassung mit Datum bis heute (Neurechnung) nur mit `energy.admin` am Datensatz. Die Formel in der Tabellenzeile ändern bleibt mit `energy.edit` möglich und gilt wie heute ab sofort (Fassung „ab heute“, ohne Neurechnung).
- Rechenumfang (Entscheidung 4): Funktionen, Vergleiche und Potenz nur bei der Art Momentanwert; Zählerstand und Verbrauch weiter nur + − × ÷, Klammern, Zahlen, `[ID]` (`ErrFormulaScope`).
- Copilot (Entscheidung 5): Der Copilot hat heute keine Werkzeuge für Messstellen; dieses Paket gibt ihm keine. Eine Fassung in der Vergangenheit legt nur eine Person an.
- Kein Lauf am Spiegel und in der Wartung (`MirrorMiddleware` sperrt POST; die Neurechnung startet nur am aktiven Server, `Skip: h.MaintLocked`). Höchstens ein Lauf gleichzeitig. Alarme bewerten die Vergangenheit nicht neu.
- Texte deutsch, für Anwender im Betrieb; keine Kennungen (UUIDs) in Oberfläche und Meldungen. Fiori und Fluent, hell und dunkel, 320–1366 px; Farben nur über die Variablen (`--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent`, `--shadow-card`).
- Bestehende Dateien haben CRLF – nur mit dem Edit-Werkzeug ändern.
- Tests in Git Bash: `go vet ./...`, `go test ./...`; Datenbanktests mit `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'` (Paket `energie` ohne Build-Tag, Paket `web` mit `-tags integration`).
- Commits deutsch, ohne Zusatzzeilen. Kein Push, kein Merge, kein Einspielen ohne ausdrückliche Freigabe des Anwenders.

## Review Focus

1. **Zeitumstellung im Zeitraum** (25. Oktober 2026 hat 25 Stunden): ein Tag der Neurechnung umfasst genau seine Ortsminuten (1500 Werte), keine doppelte und keine fehlende Minute (Test in Task 4).
2. **Zählertausch oder Lücke eines Bezugs im Zeitraum:** dieselbe Regel wie der Minutentakt – in der Minute kein Wert, Basis rückt nach (Rückgang) bzw. bleibt (fehlender Wert), kein erfundener Verbrauch (Test in Task 4).
3. **Übergabe an den Minutentakt:** Läuft der Minutentakt direkt nach dem Schlussschritt, setzt er den Zählerstand ohne Sprung fort und hat keine Lücke an der Übergabe-Minute (Test in Task 4).
4. **Neustart mitten im Lauf:** Der Lauf setzt am ersten nicht geschriebenen Tag fort, kein Tag doppelt; nach 5 Fehlversuchen endet er als „abgebrochen“ und der Minutentakt rechnet die Messstellen wieder (Tests in Task 4).
5. **Änderungen während des Laufs:** Formel, Fassungen, Umbenennen oder Löschen einer Messstelle des Laufs werden mit „Es läuft gerade eine Neurechnung …“ abgewiesen; ein Export-Lauf während der Neurechnung schreibt nichts und nennt den Grund (Tests in Task 3 und Task 5).

---

### Task 1: Migration 214 – Fassungen, Läufe, Dateibereich

**Files:**
- Create: `migrations/214_energy_formula_versions.up.sql`
- Modify: `migrations/checksums.txt` (per Befehl)
- Test: `internal/modules/energie/versions_migration_integration_test.go`

**Interfaces:**
- Produces:
  - `energy_formula_versions (id UUID PK DEFAULT pdh_uuid_v7(), meter_id UUID NOT NULL → energy_meters ON DELETE CASCADE, valid_from DATE NOT NULL, formula VARCHAR(2000) NOT NULL CHECK (formula <> ''), created_by UUID → users ON DELETE SET NULL, created_by_name TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now())`, Index `(meter_id, valid_from, created_at)`.
  - `energy_recalc_runs (id UUID PK DEFAULT pdh_uuid_v7(), meter_id UUID → energy_meters ON DELETE SET NULL, meter_ids UUID[] NOT NULL, meter_keys TEXT[] NOT NULL, from_date DATE NOT NULL, done_until DATE, days_total INT NOT NULL, state TEXT NOT NULL CHECK (state IN ('running','exporting','done','failed')), attempts INT NOT NULL DEFAULT 0, message TEXT NOT NULL DEFAULT '', values_written BIGINT NOT NULL DEFAULT 0, missing BIGINT NOT NULL DEFAULT 0, files_rebuilt TEXT NOT NULL DEFAULT '', files_kept TEXT NOT NULL DEFAULT '', created_by UUID → users ON DELETE SET NULL, created_by_name TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), finished_at TIMESTAMPTZ)`, eindeutiger Teilindex `ON energy_recalc_runs ((true)) WHERE state IN ('running','exporting')`.
  - `energy_export_files.first_end`, `last_end`, `fetched_at` (TIMESTAMPTZ, NULL erlaubt).
  - Erste Fassung je berechneter Messstelle: `INSERT INTO energy_formula_versions (meter_id, valid_from, formula, created_by_name, created_at) SELECT id, created_at::date, formula, 'Stand beim Update', created_at FROM energy_meters m WHERE formula <> '' AND NOT EXISTS (SELECT 1 FROM energy_formula_versions v WHERE v.meter_id = m.id)`.

- [ ] **Step 1: Failing test** `TestMigration214Versions`: Tabellen und die drei Spalten sind da; der Seed-Satz der Migration (im Test als Konstante wiederholt) ergibt für eine mit `virtual(…)` angelegte Messstelle genau eine Fassung mit ihrer Formel, ein zweites Ausführen keine weitere; ein zweiter Lauf mit `state = 'running'` neben einem ersten scheitert mit SQLSTATE `23505`.
- [ ] **Step 2:** `go test ./internal/modules/energie -run TestMigration214Versions` → FAIL (Tabelle fehlt).
- [ ] **Step 3:** Migration schreiben; Labor-Datenbank migrieren; `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- [ ] **Step 4:** Test → PASS; `go test ./pkg/database -run TestMigrationsFrozen` → PASS.
- [ ] **Step 5:** Commit `Energie-Fassungen: Migration 214 (Fassungen, Neurechnungen, Bereich der Export-Dateien)`.

### Task 2: Ein Rechenweg für Minutentakt und Neurechnung

**Files:**
- Create: `internal/modules/energie/virtual.go`, `internal/modules/energie/virtual_test.go`, `internal/modules/energie/sampler_pause_integration_test.go`
- Modify: `internal/modules/energie/sampler.go` (`evalVirtual`, `sampleMinute`)
- Test: `internal/modules/energie/sampler_integration_test.go` (bestehend, muss grün bleiben)

**Interfaces:**
- Produces:
  - `type refValue struct { Kind string; Now float64; HasNow bool; Base float64; HasBase bool }`
  - `func evalMinute(f *Formula, kind string, ref func(key string) (refValue, bool), hasBase bool, last float64) (v float64, ok, rebase bool)` – genau die Regeln aus dem Kommentar von `evalVirtual`: Zählerbezug trägt seinen Zuwachs seit der Basis bei, Verbrauch und Momentanwert ihren Minutenwert; unbekannter oder fehlender Bezug → kein Wert, Basis bleibt; Rückgang oder Zählerbezug ohne Basiswert → kein Wert, `rebase`; ohne Basis tragen Zähler 0 bei; Zähler < 0 → kein Wert; Zähler: `v += last`.
  - `const minuteLock = "energy_minute"` – `pg_advisory_xact_lock(hashtext(minuteLock))`, genommen am Anfang jeder Minuten-Transaktion.
  - `func pausedMeters(ctx context.Context, q querier) (map[string]bool, error)` – `meter_ids` des Laufs mit `state = 'running'`.
- `sampleMinute` öffnet die Transaktion **zuerst**, nimmt `minuteLock`, liest dann `pausedMeters` und lässt diese Messstellen ganz weg; alle Lesezugriffe (vorhandene Werte, Vorwerte, Lesungen, Basiswerte) laufen über die Transaktion. `evalVirtual` liest Basiswerte über einen übergebenen `querier` und ruft `evalMinute`.

- [ ] **Step 1: Failing tests** in `virtual_test.go` (ohne Datenbank): `TestEvalMinuteCounterIncrement` (Formel `[A]+[B]`, A Zähler now 110 base 100, B Zähler now 52 base 50, last 1000, Art Zähler → 1012, ok), `TestEvalMinuteMissingRef` (B ohne Wert → ok=false, rebase=false), `TestEvalMinuteCounterReset` (A now 5 base 100 → ok=false, rebase=true), `TestEvalMinuteNoBase` (hasBase=false, Zählerbezüge → Ergebnis = last), `TestEvalMinuteInstantExtended` (Momentanwert `MAX([A]; [B])` mit 3 und 7 → 7), `TestEvalMinuteNegativeCounter` (Zuwachs −1 → ok=false).
- [ ] **Step 2: Failing test** `TestSamplerSkipsPausedMeters` (Integration): V = `[R]`; ein Lauf `state='running'` mit `meter_ids = {V}` per INSERT; `run(t, sm, m0)` legt R ab, V nicht; nach `UPDATE … SET state='done'` legt der nächste `run` V wieder ab.
- [ ] **Step 3:** `go test ./internal/modules/energie -run 'TestEvalMinute|TestSamplerSkipsPausedMeters'` → FAIL (nicht definiert bzw. V abgelegt).
- [ ] **Step 4:** `evalMinute` in `virtual.go`; `evalVirtual` und `sampleMinute` umstellen wie oben.
- [ ] **Step 5:** `go test ./internal/modules/energie` → PASS (alle bestehenden Sampler-Tests unverändert grün).
- [ ] **Step 6:** Commit `Energie-Fassungen: ein Rechenweg je Minute, Minutentakt lässt Messstellen einer Neurechnung aus`.

### Task 3: Fassungen speichern und anwenden

**Files:**
- Create: `internal/modules/energie/versions.go`, `internal/modules/energie/versions_integration_test.go`
- Modify: `internal/modules/energie/store.go` (`Store.Loc`, `formulaScopeErr`, `lockAndLoad`, `usersOf`, `Save`, `Delete`), `internal/modules/energie/model.go` (Fehler), `internal/modules/energie/sampler.go` (`Pass`)

**Interfaces:**
- Consumes: `pausedMeters` (Task 2).
- Produces (in `versions.go`, sofern nicht anders genannt):
  - `Store.Loc *time.Location`, `func (s *Store) loc() *time.Location` (nil → `time.Local`), `func (s *Store) today() time.Time` (Ortstag als Mitternacht UTC), `func dayStart(day time.Time, loc *time.Location) time.Time` (Ortsmitternacht des Kalendertags).
  - `const (StatusPlanned = "planned"; StatusCurrent = "current"; StatusSuperseded = "superseded")`
  - `type Version struct { ID, MeterID, Formula, CreatedBy, Status string; ValidFrom, CreatedAt time.Time }`
  - `type VersionInput struct { MeterID, VersionID, Formula string; ValidFrom time.Time; Recalc bool }`
  - `type VersionResult struct { Version Version; RunID string; Unchanged bool }`
  - `func (s *Store) Versions(ctx context.Context, meterID string) ([]Version, error)` – neueste zuerst, Status nach `s.today()`.
  - `func (s *Store) SaveVersion(ctx context.Context, in VersionInput, userID string) (VersionResult, error)` – in einer Transaktion mit `lockAndLoad`: legt an oder ändert (nur geplante, über `VersionID`); mit `Recalc` legt sie zusätzlich den Lauf an (`meter_ids`/`meter_keys` = Messstelle, dann alle Nutzer in Rechenreihenfolge; `days_total` = Tage von `ValidFrom` bis heute).
  - `func (s *Store) DeleteVersion(ctx context.Context, meterID, versionID string) error`
  - `func (s *Store) ApplyVersions(ctx context.Context, minute time.Time) (int, error)` – ein UPDATE: setzt bei jeder Messstelle mit Formel (und ohne Quelle) `formula` auf die Fassung, die am Ortstag von `minute` gilt, wo sie abweicht; **ohne** Basis-Verschiebung.
  - `func (s *Store) EarliestDay(ctx context.Context, meterID string) (time.Time, bool, error)` – Ortstag des Minutenbeginns des ältesten Werts.
  - `func affected(rows []meterRow, key string, from time.Time) []meterRow` – die Messstelle und alle, die sie über eine Fassung mit Geltung ab `from` nutzen, in Rechenreihenfolge.
  - Fehler in `model.go`: `ErrPastDate` („Ein Datum bis heute geht nur mit „Neu rechnen“ – ab sofort ohne Neurechnung ändert der Stift in der Zeile die Formel“), `ErrVersionFixed` („Nur eine geplante Fassung lässt sich ändern oder löschen“), `ErrNotCalc` („Nur berechnete Messstellen haben Fassungen“), `ErrRecalcBusy` („Es läuft gerade eine Neurechnung – bitte warten, bis sie fertig ist“), `type PlannedExistsError struct{ Date time.Time }` („Für den TT.MM.JJJJ ist schon eine Fassung geplant“), `type TooEarlyError struct{ First time.Time }` („Werte dieser Messstelle gibt es erst ab dem TT.MM.JJJJ“).
- Regeln (alle in `SaveVersion`): Formel nicht leer; Schreibweise; `formulaScopeErr(src, kind)` (neu mit Art: `Extended()` nur bei `KindInstant`); unbekannte Bezüge und Kreis gegen die Vereinigung aller Fassungen, die ab `ValidFrom` gelten (je Messstelle zu einem Prüfausdruck `(f1)+(f2)…` verbunden, nur für die Prüfung); `ErrKindMix` je Bezug; `ValidFrom > today` → geplant, `Recalc` wird ignoriert; `ValidFrom ≤ today` ohne `Recalc` → `ErrPastDate`; mit `Recalc`: `ValidFrom ≥ EarliestDay`, sonst `*TooEarlyError`; ein laufender Lauf → `ErrRecalcBusy`; gleiche Formel wie die an `ValidFrom` geltende und kein `Recalc` → `Unchanged`.
- `Store.Save`: Formel neu oder geändert → Fassung mit `valid_from` = heute (bestehendes Verhalten samt `rebaseDependents` bleibt); Umstellen auf Quelle löscht die Fassungen; Artwechsel prüft die geltende und alle geplanten Fassungen mit `formulaScopeErr`; `usersOf` berücksichtigt geplante Fassungen; `Save` und `Delete` einer Messstelle eines laufenden Laufs → `ErrRecalcBusy`.
- `Sampler.Pass` ruft vor dem Laden jeder Minute `ApplyVersions(ctx, m)`.

- [ ] **Step 1: Failing tests** (`versions_integration_test.go`, `s.Loc = time.UTC`, `s.Now` fest auf `m0`):
  - `TestVersionPlannedTakesEffectAtMidnight`: V = `[A]` (Zähler), geplante Fassung `[A]+[B]` ab 16.11.2026; `ApplyVersions(ctx, 2026-11-15 23:59)` → Formel bleibt `[A]`; `ApplyVersions(ctx, 2026-11-16 00:00)` → `[A]+[B]`; `base_at` unverändert.
  - `TestVersionOnePlannedPerDay`: zweite geplante Fassung am selben Tag → `*PlannedExistsError`; Ändern der ersten über `VersionID` geht.
  - `TestVersionOnlyPlannedEditable`: die geltende Fassung ändern oder löschen → `ErrVersionFixed`.
  - `TestVersionPastNeedsRecalc`: `ValidFrom` = gestern ohne `Recalc` → `ErrPastDate`; mit `Recalc`, aber vor dem ältesten Wert → `*TooEarlyError` mit dessen Tag; mit gültigem Tag → `RunID` gesetzt, Lauf `running` mit `meter_keys` = [V, W] (W = `[V]*2`).
  - `TestVersionBusy`: bei laufendem Lauf: `SaveVersion` mit `Recalc` → `ErrRecalcBusy`; `Save` von W mit geänderter Formel und `Delete` von W → `ErrRecalcBusy`.
  - `TestFormulaScopeByKind`: `MAX([P1]; [P2])` bei Momentanwert speicherbar, bei Zählerstand `ErrFormulaScope`; Artwechsel einer Momentanwert-Messstelle mit geplanter `MAX`-Fassung auf Zählerstand → `ErrFormulaScope`.
  - `TestVersionPlannedRefBlocksDelete`: geplante Fassung von V nutzt `[X]` → `Delete(X)` → `*InUseError` mit V.
  - `TestSaveFormulaCreatesTodayVersion`: Formel über `Save` ändern → neue Fassung mit `valid_from` = heute, Status `current`; Umstellen auf Quelle → keine Fassungen mehr.
- [ ] **Step 2:** `go test ./internal/modules/energie -run 'TestVersion|TestFormulaScopeByKind|TestSaveFormulaCreatesTodayVersion'` → FAIL.
- [ ] **Step 3:** Umsetzen wie in Interfaces.
- [ ] **Step 4:** `go test ./internal/modules/energie` → PASS.
- [ ] **Step 5:** Commit `Energie-Fassungen: Fassungen mit „gültig ab“, Momentanwerte mit Funktionen`.

### Task 4: Neurechnung

**Files:**
- Create: `internal/modules/energie/recalc.go`, `internal/modules/energie/recalc_integration_test.go`
- Modify: `internal/modules/energie/model.go` (Typen)

**Interfaces:**
- Consumes: `evalMinute`, `minuteLock`, `pausedMeters` (Task 2); `Versions`, `affected`, `dayStart`, `Store.loc` (Task 3); `Exporter.Rebuild` (Task 5 – bis dahin ist `Recalculator.Exporter` nil und ein Lauf geht von `exporting` direkt nach `done`).
- Produces:
  - `type RecalcRun struct { ID, MeterID, State, Message, CreatedBy string; MeterKeys, FilesRebuilt, FilesKept []string; From time.Time; DoneUntil *time.Time; DaysTotal, Progress int; ValuesWritten, Missing int64; CreatedAt time.Time; FinishedAt *time.Time }` (`Progress` in Prozent).
  - `type RecalcPreview struct { From, To time.Time; Days int; Meters []string; Rebuild, Keep []FileImpact }`, `type FileImpact struct { Connection, Name, Reason string }`.
  - `type Recalculator struct { Store *Store; Exporter *Exporter; Skip func() bool; MaxAttempts int /* 0 = 5 */; nudge chan struct{} }`, `func NewRecalculator(s *Store, ex *Exporter, skip func() bool) *Recalculator`, `func (rc *Recalculator) Nudge()` (nil-sicher, blockiert nie), `func (rc *Recalculator) Start(ctx context.Context)` (sofort, dann alle 30 s und nach jedem Anstoß; Panik wird protokolliert), `func (rc *Recalculator) Pass(ctx context.Context) error`, `func (rc *Recalculator) Preview(ctx context.Context, in VersionInput) (RecalcPreview, error)` (prüft wie `SaveVersion` mit `Recalc`, schreibt nichts).
  - `func (s *Store) RunningRecalc(ctx context.Context) (*RecalcRun, error)` (nil = keiner), `func (s *Store) RecalcRuns(ctx context.Context, meterID string, limit int) ([]RecalcRun, error)` (`$1 = ANY(meter_ids)`, neueste zuerst).
- Ablauf von `Pass` (der Lauf mit `state = 'running'`):
  - Je Tag `d` von `done_until + 1` (sonst `from_date`) bis gestern: in **einer** Transaktion die Werte der Laufmessstellen mit `ts ∈ (dayStart(d), dayStart(d+1)]` löschen und neu schreiben (`INSERT … SELECT unnest(…)`), `done_until = d`, Zähler fortschreiben. Zwischen den Tagen `ctx` und `Skip` prüfen.
  - Ausgangslage jedes Tages aus der Datenbank: je Laufmessstelle letzter Wert mit `ts ≤ dayStart(d)` (Wert; sein Zeitpunkt ist die Basis) und die Werte ihrer Zählerbezüge zu diesem Zeitpunkt; die Werte aller Bezüge im Tag in einem Zug laden. Formel je Messstelle = die am Tag `d` geltende Fassung; Reihenfolge über `EvalOrder` der Tagesformeln. Je Minute `evalMinute`; die Basis rückt bei einem Wert und bei `rebase` auf das Minutenende.
  - Heute (Schlussschritt) in einer Transaktion mit `minuteLock`: Ende `E` = jüngster `ts` in `energy_values` aller aktiven Messstellen außerhalb des Laufs (ohne solche: letzte volle Minute); Minuten bis `E` rechnen und schreiben; `energy_meters.last_value/last_at/base_at` der Laufmessstellen auf den letzten Stand; `state = 'exporting'`. Danach je Efficio-Export `Exporter.Rebuild(…, dry = false)`; Listen in `files_rebuilt`/`files_kept`; `state = 'done'`, `finished_at`. Ein Lauf in `exporting` (nach Neustart) macht nur diesen Teil.
  - Fehler: `attempts + 1`, `message`; ab `MaxAttempts` `state = 'failed'` (der Minutentakt rechnet die Messstellen wieder).

- [ ] **Step 1: Failing tests** (`s.Loc = time.UTC`, `s.Now` = 15.11.2026 10:17; Werte mit `fillSeries`/`putValue`; Zähler R1, R2 mit Minutenwerten vom 12.11. bis 15.11. 10:16; V = `[R1]` mit alten Werten; W = `[V]*2` mit alten Werten):
  - `TestRecalcCounterMatchesRule`: Fassung `[R1]+[R2]` ab 13.11. mit `Recalc`; `Pass` bis `done`; Stichproben am 13., 14. und 15.11.: V = V(13.11. 00:00) + Zuwachs R1 + Zuwachs R2 seit 13.11. 00:00; W = W(13.11. 00:00) + 2 × Zuwachs V; V und W am 12.11. unverändert; R1, R2 unverändert; `last_at` von V = 10:16; `values_written` > 0.
  - `TestRecalcResetAndGap`: R2 springt am 14.11. 08:00 zurück (Zählertausch) und hat 09:00–09:04 keine Werte → V hat um 08:00 und 09:00–09:04 keinen Wert und rechnet danach ohne erfundenen Zuwachs weiter.
  - `TestRecalcDSTDay`: `s.Loc` = Europe/Berlin, Fassung ab 25.10.2026 → V hat für diesen Ortstag genau 1500 Minutenwerte.
  - `TestRecalcResume`: `Pass` mit einem Kontext, der nach dem ersten Tag endet → `done_until` = 13.11.; der zweite `Pass` macht ab 14.11. weiter; die Werte des 13.11. stehen genau einmal da.
  - `TestRecalcHandoverNoJump`: nach `done` legt `Sampler.Pass` für 10:16–10:17 V mit `V(10:16) + Zuwachs` ab – kein Sprung, keine Lücke.
  - `TestRecalcFailsAfterAttempts`: ein vorübergehender BEFORE-INSERT-Trigger auf `energy_values` lehnt Werte von V ab, `MaxAttempts = 2` → nach zwei `Pass` `state = 'failed'`, `message` gesetzt, `pausedMeters` leer.
  - `TestRecalcPreview`: `Preview` nennt Zeitraum 13.11.–15.11., `Days = 3`, `Meters = [V, W]` und schreibt nichts.
- [ ] **Step 2:** `go test ./internal/modules/energie -run TestRecalc` → FAIL.
- [ ] **Step 3:** `recalc.go` umsetzen wie oben.
- [ ] **Step 4:** `go test ./internal/modules/energie` → PASS.
- [ ] **Step 5:** Commit `Energie-Fassungen: rückwirkend neu rechnen, Tag für Tag, mit Übergabe an den Minutentakt`.

### Task 5: Efficio – nicht verschickte Dateien neu erzeugen

**Files:**
- Create: `internal/modules/energie/rebuild.go`, `internal/modules/energie/rebuild_integration_test.go`
- Modify: `internal/modules/energie/exporter.go` (`block`, `run`), `internal/modules/energie/recalc.go` (Rebuild aufrufen, Vorschau), `internal/web/export_efficio.go` (`EfficioDownload`)

**Interfaces:**
- Consumes: `FileImpact`, `RunningRecalc` (Task 4).
- Produces:
  - `block` setzt beim Anlegen `first_end = steps[0].End`, `last_end = steps[n-1].End`, beim Anhängen `last_end = steps[n-1].End`.
  - `func (e *Exporter) Rebuild(ctx context.Context, c ExportConfig, from time.Time, keys map[string]bool, dry bool) (rebuilt, kept []FileImpact, err error)` – nur wenn `e.meters(ctx, c)` einen Schlüssel aus `keys` enthält; unter `SELECT … FROM energy_export_state … FOR UPDATE`; je Datei mit `last_end > from`: verschickt (Abholen: `fetched_at` gesetzt; sonst `delivered_version > 0` oder `fetched_at` gesetzt) → `kept` „schon verschickt, nicht geändert“; `first_end` leer → `kept` „vor dem Update erzeugt, nicht geändert“; anderer Zeitbezug oder Bereich nicht im Raster `c.Interval` → `kept` „Einstellung des Exports geändert, nicht geändert“; sonst Inhalt aus `RangeValues(first_end … last_end)` mit Kopfzeile wie bei einer neuen Datei, `version = version + 1`, `last_error = ''` → `rebuilt`.
  - `Exporter.run`: läuft eine Neurechnung (`running` oder `exporting`), keine Datei anfassen, Notiz „Neurechnung läuft – der Export folgt, sobald sie fertig ist.“, Ergebnis `nothing`.
  - `EfficioDownload`: `UPDATE energy_export_files SET fetched_at = COALESCE(fetched_at, now())` vor dem Senden.
  - `Recalculator.Preview` füllt `Rebuild`/`Keep` mit `dry = true`.

- [ ] **Step 1: Failing tests** (`exportEnv`, Zähler-Messstelle mit „exportieren“):
  - `TestExporterRecordsFileRange`: nach zwei Läufen hat die Tagesdatei `first_end` = erstes und `last_end` = letztes Intervallende.
  - `TestRebuildUnsentFile`: Ziel `failTarget` (nichts zugestellt) → nach geänderten Werten und `Rebuild` enthält die Datei die neuen Werte, `version` + 1, Datei in `rebuilt`.
  - `TestRebuildKeepsDelivered`: zugestellte Datei (`probeTarget`) → Inhalt unverändert, in `kept` mit „schon verschickt“.
  - `TestRebuildPickupFetched`: Abholen mit `fetched_at` → `kept`; ohne `fetched_at` → `rebuilt`.
  - `TestExporterPausesDuringRecalc`: Lauf `running` → `Run` legt keine Datei an, Notiz enthält „Neurechnung läuft“.
  - `TestRecalcRebuildsExport` (in `recalc_integration_test.go`): Lauf über einen exportierten Tag → `files_rebuilt` nennt die Datei.
- [ ] **Step 2:** `go test ./internal/modules/energie -run 'TestExporterRecordsFileRange|TestRebuild|TestExporterPausesDuringRecalc|TestRecalcRebuildsExport'` → FAIL.
- [ ] **Step 3:** Umsetzen; `EfficioDownload` ändern.
- [ ] **Step 4:** `go test ./internal/modules/energie` → PASS; `go test -tags integration ./internal/web -run Efficio` → PASS.
- [ ] **Step 5:** Commit `Energie-Fassungen: nicht verschickte Efficio-Dateien nach der Neurechnung neu erzeugen`.

### Task 6: Oberfläche „Fassungen“ an der Messstelle

**Files:**
- Create: `internal/web/energy_versions.go`, `web/templates/widgets/energy_meter_versions.gohtml`, `internal/web/energy_versions_test.go`, `internal/web/energy_versions_integration_test.go`
- Modify: `internal/web/handler.go` (Feld `energyRecalc atomic.Pointer[energie.Recalculator]`, Routen bei den Energiedaten), `internal/web/energy_wire.go` (`StartEnergyRecalc`, `energyRecalcNudge`, neue Fehler in `meterUserError`/`meterSaveStatus`), `internal/web/energy_meters.go` (`MeterRow.Recalculating`), `internal/web/perm_gate.go`, `cmd/server/main.go` (Start nach `StartEnergyExporter`), `web/templates/widgets/energy_meter_table.gohtml` (Knopf, Zeile, Schild, Hinweis zur Formel), `web/templates/energy_meters.gohtml` (Skript, Stil)

**Interfaces:**
- Consumes: `Versions`, `SaveVersion`, `DeleteVersion`, `EarliestDay`, `RecalcRuns`, `RunningRecalc`, `Recalculator.Preview`, `Recalculator.Nudge`.
- Produces:
  - Routen: `GET /energy/meters/{id}/versions` (`EnergyMeterVersionsWeb`, Fragment), `POST /energy/meters/{id}/versions/preview` (`EnergyMeterVersionPreviewWeb`, JSON → Vorschau in Anwendersprache), `POST /energy/meters/{id}/versions/save-web` (`EnergyMeterVersionSaveWeb`, JSON `{valid_from, formula, version_id, recalc}` → `{status, run_id}`), `POST /energy/meters/{id}/versions/{vid}/delete-web` (`EnergyMeterVersionDeleteWeb`).
  - Rechte: Fragment `energy.view` am Datensatz; geplant `energy.edit` am Datensatz; `valid_from ≤ heute` `energy.admin` am Datensatz (auch für die Vorschau); Gate-Regel `gr(wPost, permEnergyEdit, meterRef, false, ^/energy/meters{gID}/versions/(?:preview|save-web|{gID}/delete-web)$)`; Neurechnung in der Wartung → 503 mit `errMaintApply`.
  - `StartEnergyRecalc(ctx)`: nicht am Spiegel; `energie.NewRecalculator(st, &energie.Exporter{Store: st, Configs: h.efficioConfigs}, h.MaintLocked)` in `h.energyRecalc`.
  - `type EnergyVersionsData struct { Meter energie.Meter; Versions []energie.Version; Runs []energie.RecalcRun; Running *energie.RecalcRun; CanEdit, CanAdmin bool; Today, Tomorrow, Earliest string; Err string }`
  - Zeile berechneter Messstellen: Knopf „Fassungen“ (`ti-history`, `hx-get` wie Schwellwerte, ausklappbare Zeile `em-ver-row-{ID}`); Schild „wird neu gerechnet“, solange die Messstelle in einem Lauf ist. Fragment: Liste (Schild geplant · gilt · abgelöst, „gültig ab“, Formel, „angelegt von … am …“, Löschen nur bei geplant); Formular „Neue Fassung“ (Datum mit `min` = morgen bei `energy.edit`, = `Earliest` bei `energy.admin`; Formel; bei Datum bis heute Knopf „Prüfen“ → Vorschau mit Zeitraum, Tagen, Messstellen, Efficio-Dateien „wird neu erzeugt“/„bleibt“ und dem Hinweis „Alarme werden für die Vergangenheit nicht neu bewertet. Während der Neurechnung setzen die Messstellen und der Efficio-Export kurz aus.“, danach „Neu rechnen“); laufender Lauf „wird neu gerechnet (… %)“ mit `hx-trigger="every 5s"`; letzte Läufe mit Zeitraum, Person und Ergebnis.
  - Hinweis unter der Formel im Bearbeiten-Formular: „Ändern gilt ab sofort. Für ein anderes Datum: Fassungen. Bei Momentanwerten auch Funktionen und Vergleiche (z. B. MAX, WENN).“

- [ ] **Step 1: Failing tests**
  - `TestEnergyVersionRoutesGated` (ohne DB): die drei POST-Wege brauchen laut `gateRules` `energy.edit` am Datensatz `meter`.
  - `TestEnergyMeterTableVersionsButton` (ohne DB, Vorlage rendern): eine berechnete Zeile hat den Knopf „Fassungen“, eine Zeile mit Quelle nicht; `Recalculating` zeigt „wird neu gerechnet“.
  - `TestEnergyVersionSaveRights` (integration, `hasPermHook`): nur `energy.edit` → geplante Fassung 200, Datum heute mit `recalc` 403; mit `energy.admin` 200 und `run_id`.
  - `TestEnergyVersionSaveMaint` (integration): `MaintLocked` → Neurechnung 503 mit „Wartung läuft“.
  - `TestEnergyMeterVersionsFragment` (integration): Fragment zeigt „geplant“, „gilt“, „abgelöst“, keinen Löschen-Knopf an der geltenden Fassung und keine UUID.
- [ ] **Step 2:** `go test ./internal/web -run 'TestEnergyVersionRoutesGated|TestEnergyMeterTableVersionsButton'` und `go test -tags integration ./internal/web -run 'TestEnergyVersion|TestEnergyMeterVersionsFragment'` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** Tests → PASS; lokal gerenderte Seite in Fiori und Fluent, hell und dunkel, 320–1366 px ansehen (kein Überlauf, Fragment am Handy einspaltig).
- [ ] **Step 5:** Commit `Energie-Fassungen: Fassungen an der Messstelle mit Vorschau und Fortschritt`.

### Task 7: Handbuch, Änderungsprotokoll, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `energiedaten`), `CHANGELOG.md`, `VERSION`

**Interfaces:** keine.

- [ ] **Step 1:** Kapitel „Energiedaten & Messstellen“: So-geht's-Kasten „So planst du eine neue Formel“ (Fassungen → Datum ab morgen → Formel → Speichern; gilt ab 00:00 Uhr dieses Tages) und „So rechnest du rückwirkend neu“ (Bereichsadministrator; Datum bis heute → Prüfen → Vorschau lesen → Neu rechnen; Fortschritt; was mit Efficio-Dateien und Alarmen geschieht; frühestens ab dem ältesten Wert; Messstellen und Export setzen kurz aus); Absatz zu Funktionen und Vergleichen auf „nur bei Momentanwerten“ umstellen; Rechte-Liste ergänzen (`energy.edit`: geplante Fassungen; `energy.admin`: rückwirkend neu rechnen); Sätze „Für die Zeit vor dem Update kennt das PDH nur die damals geltende Formel (Fassung „Stand beim Update“).“ und „Wer eine berechnete Messstelle auf eine Quelle umstellt, verwirft ihre Fassungen.“
- [ ] **Step 2:** `CHANGELOG.md` neuer Eintrag `## [0.82.50] – <Datum>` mit `### Neu` (Fassungen der Formel; rückwirkend neu rechnen; Efficio-Dateien neu erzeugt), `### Geändert` (Momentanwerte mit Funktionen und Vergleichen), `### Sicherheit` (rückwirkend nur Bereichsadministrator); `VERSION` = `0.82.50`.
- [ ] **Step 3:** `go test ./internal/web -run 'TestHelp|TestChangelogMatchesVersion'` → PASS.
- [ ] **Step 4:** Commit `Energie-Fassungen: Handbuch, Änderungsprotokoll (0.82.50)`.

### Task 8: Gesamtprüfung

**Files:** keine neuen.

- [ ] **Step 1:** `go vet ./...` → keine Meldung.
- [ ] **Step 2:** `go test ./...` → alle Pakete ok.
- [ ] **Step 3:** `go test -tags integration ./internal/web ./internal/modules/energie ./pkg/database` → ok (bekannt rot nur `TestTerminalGlobalSlidesJSON` wegen Resten in der Labor-Datenbank – im Bericht namentlich nennen).
- [ ] **Step 4:** Schlussprüfung des ganzen Branches durch einen frischen Prüfer; Befunde beheben oder im Ledger `.superpowers/sdd/2026-11-24-energie-fassungen/` zurückstellen.
- [ ] **Step 5:** Liste der offenen Punkte in `AGENTS.md` nachziehen; Commit. Freigabe für Push, Merge und Verbund-Update beim Anwender einholen.
