# Berechnete Werte (virtuelle Messwerte, Paket 2a) – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ein berechneter Wert ist eine Zuordnung an der eingebauten Import-Verbindung „Berechnete Werte“: Formel mit Eingangswerten (Kurznamen), Formelgenerator mit Prüfung und Probe, Fassungen mit „gültig ab“, laufende Rechnung mit Grund bei „kein Wert“, Vorschläge vom Copilot.

**Architecture:** Der Rechenkern zieht aus `internal/modules/energie/formula.go` nach `internal/core/formula` und wird erweitert (Namen ohne Klammern, Vergleiche, Potenz, Funktionen); die Energiedaten nutzen ihn über eine dünne Hülle und behalten ihren alten Umfang. Ein neues Modul `internal/modules/calc` hält Speicher (Wert, Fassungen, Eingangswerte, Prüfungen) und Rechenwerk (Lauf alle 10 s und kurz nach jedem Anstoß). Alle Stellen, die `import_mappings.last_value` schreiben, gehen über eine Funktion im Web-Paket, die das Rechenwerk anstößt. Die Seite der Verbindung zeigt für die Art `calc` Liste und Formular; der Copilot bekommt dort eigene Werkzeuge und legt Vorschläge in `copilot_proposals` ab.

**Tech Stack:** Go (chi, html/template, pgx), htmx/Vanilla-JS, PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-11-23-berechnete-werte-design.md` (vom Anwender am 23.11.2026 mit „passt“ bestätigt; damit entschieden: Schnitt 2a/2b, Schnellübersicht bleibt bei der heutigen Regel)

## Global Constraints

- Version: nächste Korrekturversion über `origin/main` (Stand jetzt 0.82.36 → **0.82.37**); vor dem Merge `git show origin/main:VERSION` prüfen.
- Migration: nächste freie Nummer über `origin/main` (jetzt **205**), in `migrations/checksums.txt` mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`; neue Kennungen mit `pdh_uuid_v7()`.
- **Still falsche Zahlen darf es nicht geben:** fehlender, nicht lesbarer oder zu alter Eingangswert → kein Wert (`last_value` NULL) mit Grund, nie 0 und nie der alte Stand.
- Keine Kennungen (UUIDs, `source_ref` der berechneten Werte) in Oberfläche, Meldungen oder Handbuch.
- Eine Formelsprache: nur `internal/core/formula`. An den Energie-Messstellen ändert sich nichts, auch nicht der Rechenumfang.
- Kein neues Recht: ansehen `import.read`, anlegen/ändern/löschen `import.write` an der Anlage des berechneten Werts (`assetGuardFor`, beim Wechsel der Anlage an alter und neuer). Eingangswerte: unternehmensweiter Pool wie bei den Export-Feldern.
- Nicht in der Wartung schreiben (`h.MaintLocked()` → 503 „Wartung läuft – bitte später übernehmen“, `errMaintApply`); am Spiegel sperrt `MirrorMiddleware` POST; das Rechenwerk startet nur am aktiven Server und ruht in der Wartung.
- Copilot: „Kein selbsttätiges Anlegen oder Löschen – jede Änderung braucht „Übernehmen“.“ Löschen nur nennen („Löschen bitte von Hand“). „Kennwörter, Schlüssel und Verbindungsgeheimnisse gehen nie an den Copilot.“ Kein Haken für die Internetsuche auf dieser Seite.
- Grenzen: Formel höchstens 2000 Zeichen, höchstens 50 Eingangswerte je Formel, Kurzname höchstens 20 Zeichen, Nachkommastellen 0–6 (Vorgabe 2), Höchstalter 1–44640 Minuten oder leer (Formular mit 10 vorbelegt), Einheit höchstens 20 Zeichen, Bezeichnung 2–150 Zeichen, Copilot-Vorschlag höchstens 50 Zeilen.
- Texte deutsch, für Anwender im Betrieb; Fiori und Fluent, hell und dunkel, 320–1366 px; Farben nur über `--bg`, `--bg2`, `--text`, `--muted`, `--border`, `--accent`, `--shadow-card`. Neue `{{t "…"}}`-Texte in `web/i18n/{en,ro,tr,mk}.json` (`TestI18nCatalogsComplete`).
- Tests in Git Bash: `go vet ./...`, `go test ./...`; Datenbanktests mit `-tags integration` und `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable'`.
- Commits deutsch, ohne Zusatzzeilen. Kein Push, kein Merge, kein Einspielen ohne ausdrückliche Freigabe des Anwenders.

## Review Focus

1. Dezimalkomma gegen Trennzeichen: `MIN(1,5; 2)` ist 1,5; `MIN(1, 2)` ist ein Schreibfehler mit Stelle und dem Hinweis auf „;“ – nie still 1 (Test in Task 2).
2. Eingangswerte, die keine reine Zahl sind (`"12,5 kW"`, `""`, `" 7 "`, `"TRUE"`, `"Ein"`): Text mit Einheit und leerer Text sind „keine Zahl“, Leerzeichen am Rand zählen nicht, Wahrheitswörter sind 1/0 (Test in Task 4).
3. Kette: B rechnet aus A. Wird A leer, wird B im selben Lauf leer und nennt A als Grund; kommt A zurück, ist B im selben Lauf wieder da (Test in Task 4).
4. Wertesturm: 1000 Anstöße kurz hintereinander blockieren keinen Schreiber und führen zu wenigen Läufen; ohne gestartetes Rechenwerk (Spiegel, Tests) ist der Anstoß wirkungslos statt eine Panik (Test in Task 5).
5. Person mit `import.write` nur in Abteilung A: verschiebt einen berechneten Wert nach B, ändert einen Wert in B oder löscht dort eine geplante Fassung – alles 403, nichts geändert (Test in Task 6).

---

### Task 1: Rechenkern nach `internal/core/formula` umziehen

**Files:**
- Create: `internal/core/formula/formula.go` (Inhalt von `internal/modules/energie/formula.go`), `internal/core/formula/formula_test.go`
- Modify: `internal/modules/energie/formula.go` (nur noch Hülle)
- Unverändert: `internal/modules/energie/formula_test.go`, alle übrigen Tests der Energiedaten

**Interfaces:**
- Produces (Paket `formula`):
  - `const MaxLen = 2000`
  - `type SyntaxError struct{ Pos int; Msg string }` – `Error()` = `"Formel, Zeichen %d: %s"` (wie bisher `FormulaError`)
  - `type UnknownRefError struct{ Key string }` – `Error()` = `"unbekannter Bezug [" + Key + "]"`
  - `type CycleError struct{ Path []string }` – `Error()` = `"Zirkelbezug: [A] → [B] → [A]"` (wie bisher)
  - `type Formula struct{…}`; `func Parse(src string) (*Formula, error)`; `func (f *Formula) Refs() []string`; `func (f *Formula) Eval(get func(key string) (float64, bool)) (float64, bool)`
  - `func Check(formulas map[string]string, known map[string]bool) error`; `func Order(formulas map[string]string) ([]string, error)`
  - `func OrderDeps(deps map[string][]string) ([]string, error)` – Reihenfolge „Bezüge vor Nutzern“ über beliebige Abhängigkeiten (Schlüssel → genutzte Schlüssel; unbekannte Ziele werden übergangen), bei Kreis `*CycleError`; `Order` nutzt sie.
  - Meldungen im Leser: „Messstellen-ID nicht erlaubt/fehlt/ist zu lang“ werden „Bezug nicht erlaubt“, „Bezug fehlt“, „Bezug ist zu lang“.
- Hülle in `energie` (damit die vorhandenen Tests unverändert bleiben):
  - `type Formula = formula.Formula`, `type FormulaError = formula.SyntaxError`, `type CycleError = formula.CycleError`
  - `type UnknownRefError struct{ Key string }` bleibt eigener Typ mit `"unbekannte Messstelle [" + Key + "]"`
  - `func ParseFormula(src string) (*Formula, error)` = `formula.Parse`; `func EvalOrder(...)` = `formula.Order`; `func CheckFormulas(...)` ruft `formula.Check` und wandelt `*formula.UnknownRefError` in `*energie.UnknownRefError`.

- [ ] **Step 1: Failing test** in `internal/core/formula/formula_test.go`:

```go
func TestParseEvalBase(t *testing.T) {
	get := func(k string) (float64, bool) { v, ok := map[string]float64{"G": 100, "A": 30, "B": 20}[k]; return v, ok }
	for src, want := range map[string]float64{"[G] − [A] − [B]": 50, "([A]+[B]) × 0,5": 25, "-[A] + 2*3": -24, "[A] ÷ 4": 7.5} {
		f, err := Parse(src)
		if err != nil { t.Fatalf("%q: %v", src, err) }
		if got, ok := f.Eval(get); !ok || got != want { t.Errorf("%q = %v (%v)", src, got, ok) }
	}
}
func TestOrderDeps(t *testing.T) {
	got, err := OrderDeps(map[string][]string{"c": {"b", "x"}, "b": {"a"}, "a": nil})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) { t.Fatalf("%v %v", got, err) }
	var ce *CycleError
	if _, err := OrderDeps(map[string][]string{"a": {"b"}, "b": {"a"}}); !errors.As(err, &ce) || ce.Path[0] != ce.Path[len(ce.Path)-1] { t.Fatalf("Kreis: %v", err) }
}
func TestUnknownRefMessage(t *testing.T) {
	if err := Check(map[string]string{"V": "[X]"}, map[string]bool{"V": true}); err == nil || err.Error() != "unbekannter Bezug [X]" { t.Fatal(err) }
}
```

- [ ] **Step 2: Run** `go test ./internal/core/formula/` – Expected: FAIL (Paket fehlt).
- [ ] **Step 3: Implement** – Datei verschieben (`git mv`), umbenennen wie oben, Hülle schreiben. Kein Verhalten ändern.
- [ ] **Step 4: Run** `go test ./internal/core/formula/ ./internal/modules/energie/ && go test ./internal/web -run 'Meter|Energy' && go vet ./internal/...` – Expected: PASS, kein vorhandener Test geändert.
- [ ] **Step 5: Commit** `git commit -m "Rechenkern: Formeln in ein gemeinsames Paket (Energiedaten unverändert)"`

### Task 2: Rechenkern erweitern, Energie-Messstellen beim alten Umfang halten

**Files:**
- Modify: `internal/core/formula/formula.go`; Create: `internal/core/formula/funcs.go`
- Test: `internal/core/formula/extended_test.go`
- Modify: `internal/modules/energie/model.go` (`ErrFormulaScope`), `internal/modules/energie/store.go` (`Save`), `internal/web/energy_wire.go` (`meterUserError`)
- Test: `internal/modules/energie/formula_scope_test.go`

**Interfaces:**
- Schreibweise (bindet von schwach nach stark):

```
vergleich  := summe [ (< | <= | > | >= | = | <> | ≤ | ≥ | ≠) summe ]   höchstens ein Vergleich je Ebene
summe      := produkt { (+ | - | −) produkt }
produkt    := vorzeichen { (* | × | / | ÷) vorzeichen }
vorzeichen := (- | −) vorzeichen | potenz
potenz     := primär [ ^ vorzeichen ]                                  rechts gebunden
primär     := Zahl | [Bezug] | Name | Name ( vergleich { ; vergleich } ) | ( vergleich )
Name       := Buchstabe { Buchstabe | Ziffer | _ }                      unicode.IsLetter / IsDigit
```

  Ein `Name` ohne folgende „(“ ist ein Bezug (`A` ≡ `[A]`), mit „(“ eine Funktion (Groß-/Kleinschreibung egal). Bezüge selbst bleiben im Kern groß/klein-genau. `A < B < C` → `SyntaxError` „Vergleiche bitte klammern“. Vergleich liefert 1 oder 0.
- Funktionen (deutsch · gleichwertig englisch · Angaben):
  `MIN`, `MAX` · – · 1–50; `SUMME` · `SUM` · 1–50; `MITTELWERT` · `AVG`, `AVERAGE` · 1–50; `BETRAG` · `ABS` · 1; `RUNDEN` · `ROUND` · 1–2 (zweite Angabe muss eine Zahl 0–6 ohne Nachkommastellen sein, fehlt sie: 0; kaufmännisch, von der Null weg); `WURZEL` · `SQRT` · 1 (negativ → kein Wert); `WENN` · `IF` · genau 3, nur der gewählte Zweig wird gerechnet; `UND` · `AND`, `ODER` · `OR` · 1–50 (alle Angaben werden gerechnet, ≠ 0 ist wahr); `NICHT` · `NOT` · 1.
  Fehlermeldungen (`SyntaxError`, Stelle = Anfang des Funktionsnamens): „unbekannte Funktion „X““; „WENN braucht 3 Angaben (Bedingung; dann; sonst)“; „X braucht N Angabe(n)“ bzw. „X braucht mindestens eine Angabe“; „RUNDEN: Stellen als Zahl von 0 bis 6 angeben“. Folgt auf „1,“ keine Ziffer: „Ziffer nach dem Komma fehlt – Angaben einer Funktion trennt „;““.
- Produces:
  - `type NoValueKind int` mit `NoValueRef`, `NoValueDivZero`, `NoValueNotFinite`; `type NoValue struct{ Kind NoValueKind; Ref string }`
  - `func (f *Formula) EvalWhy(get func(key string) (float64, bool)) (float64, *NoValue)` – `nil` = Wert da; `Eval` ruft sie.
  - `func (f *Formula) Extended() bool` – wahr, wenn die Formel einen Namen ohne Klammern, einen Vergleich, `^` oder eine Funktion enthält.
  - `func IsFunction(name string) bool`; `func ValidIdent(s string) bool` (Buchstabe, dann Buchstaben/Ziffern/`_`, höchstens 20 Zeichen, kein Funktionsname)
  - `type FuncInfo struct{ Name, Insert, Help string }`; `func Functions() []FuncInfo` – die elf deutschen Namen in obiger Reihenfolge, `Insert` z. B. `"WENN(; ; )"`, `Help` ein Satz mit Beispiel (für Knöpfe und Handbuch).
  - `energie.ErrFormulaScope = errors.New("in der Formel einer Messstelle sind nur + − × ÷, Klammern und [Messstellen-ID] erlaubt – für Funktionen und Vergleiche einen berechneten Wert anlegen und als Quelle wählen")`; `Store.Save` gibt ihn zurück, wenn `f.Extended()`; `meterUserError` kennt ihn (Status 400).

- [ ] **Step 1: Failing tests** (`extended_test.go`, `get` liefert `A=30, B=20, Z=0`; `X` und `a` fehlen):

```go
func TestExtendedEval(t *testing.T) {
	for src, want := range map[string]float64{
		"A + B": 50, "[A] + B": 50,
		"A > B": 1, "A <= B": 0, "A = 30": 1, "A <> 30": 0, "A ≥ 30": 1, "A + 1 > B * 2": 0,
		"2 ^ 3": 8, "2 ^ 3 ^ 2": 512, "-2 ^ 2": -4, "2 ^ -1": 0.5, "2 * 3 ^ 2": 18,
		"MIN(A; B; 25)": 20, "max(A; B)": 30, "SUMME(A; B; 1,5)": 51.5, "MITTELWERT(A; B)": 25, "avg(A; B)": 25,
		"BETRAG(B - A)": 10, "RUNDEN(A / 7; 2)": 4.29, "RUNDEN(2,5)": 3, "RUNDEN(-2,5)": -3, "WURZEL(B + 5)": 5,
		"WENN(Z > 0; A / Z; -1)": -1, "WENN(A; 1; X)": 1, "UND(A > B; B > 0)": 1, "ODER(Z; 0)": 0, "NICHT(Z)": 1,
		"MIN(1,5; 2)": 1.5,
	} {
		f, err := Parse(src)
		if err != nil { t.Errorf("%q: %v", src, err); continue }
		if got, why := f.EvalWhy(get); why != nil || math.Abs(got-want) > 1e-9 { t.Errorf("%q = %v (%+v), erwartet %v", src, got, why, want) }
	}
}
func TestExtendedNoValue(t *testing.T) {
	for src, want := range map[string]NoValue{
		"A / Z": {Kind: NoValueDivZero}, "X + 1": {Kind: NoValueRef, Ref: "X"}, "a + B": {Kind: NoValueRef, Ref: "a"},
		"WURZEL(0 - A)": {Kind: NoValueNotFinite}, "WENN(X; 1; 2)": {Kind: NoValueRef, Ref: "X"},
	} { /* Parse ohne Fehler; EvalWhy liefert genau want */ }
	// "WENN(Z; X; 2)" → Wert 2, why == nil (X wird nicht angefasst)
}
func TestExtendedSyntax(t *testing.T) {
	// *SyntaxError mit Pos ≥ 1 für: "A < B < C", "FOO(1)", "WENN(A; 1)", "MIN()", "RUNDEN(A; 7)", "RUNDEN(A; B)",
	// "MIN(1, 2)" (Msg enthält „;“), "A ;", "1 2", "A B", "(A", "A ^"
}
func TestExtendedFlag(t *testing.T) {
	// Extended() false: "[A] + [B] * 2", "-([A] / 3,5)"; true: "A + [B]", "[A] > 1", "[A] ^ 2", "MIN([A]; [B])"
}
func TestValidIdent(t *testing.T) {
	// true: "A", "B2", "Öl_1", "x"; false: "", "1A", "A-B", "MIN", "wenn", 21 Zeichen, "A B"
}
func TestRefsIncludeAllBranches(t *testing.T) { /* Parse("WENN(A; B; [C])").Refs() == [A B C] */ }
```

  `formula_scope_test.go` (ohne Datenbank, prüft die Regel über eine kleine Hilfsfunktion `formulaScopeErr(src string) error` in `store.go`, die `Save` nutzt): `"[A] + [B]"` → nil; `"MIN([A]; [B])"`, `"[A] > 1"`, `"A + B"` → `ErrFormulaScope`. In `internal/web/energy_meters_test.go` der vorhandenen Tabelle eine Zeile anfügen: `{energie.ErrFormulaScope, "In der Formel einer Messstelle sind nur", 400}`.
- [ ] **Step 2: Run** `go test ./internal/core/formula/ ./internal/modules/energie/ -run 'Extended|ValidIdent|RefsInclude|Scope'` – Expected: FAIL.
- [ ] **Step 3: Implement.** Bezug-Knoten merkt sich „ohne Klammern“; `WENN` wertet erst die Bedingung, dann genau einen Zweig; Ergebnis jeder Stufe auf NaN/Inf prüfen.
- [ ] **Step 4: Run** `go test ./internal/core/formula/ ./internal/modules/energie/ && go test ./internal/web -run 'Meter|Energy'` – Expected: PASS (auch alle alten Tests).
- [ ] **Step 5: Commit** `git commit -m "Rechenkern: Namen ohne Klammern, Vergleiche, Potenz und Funktionen; Messstellen bleiben beim alten Umfang"`

### Task 3: Migration 205 und Speicher der berechneten Werte

**Files:**
- Create: `migrations/205_calc_values.up.sql`; Modify: `migrations/checksums.txt`
- Create: `internal/modules/calc/model.go`, `internal/modules/calc/store.go`
- Test: `internal/modules/calc/store_integration_test.go` (`//go:build integration`), `internal/modules/calc/model_test.go`

**Interfaces:**
- Consumes: `formula.Parse`, `Refs`, `ValidIdent`, `OrderDeps`, `EvalWhy` (Task 1–2).
- Migration:

```sql
INSERT INTO import_export_connections (direction, kind, name, config, enabled)
SELECT 'import', 'calc', 'Berechnete Werte', '{}'::jsonb, true
WHERE NOT EXISTS (SELECT 1 FROM import_export_connections WHERE kind = 'calc');
CREATE UNIQUE INDEX IF NOT EXISTS uq_import_export_connections_calc ON import_export_connections (kind) WHERE kind = 'calc';

CREATE TABLE calc_values (
    mapping_id      UUID PRIMARY KEY REFERENCES import_mappings(id) ON DELETE CASCADE,
    unit            VARCHAR(20) NOT NULL DEFAULT '',
    decimals        SMALLINT NOT NULL DEFAULT 2 CHECK (decimals BETWEEN 0 AND 6),
    max_age_minutes INTEGER CHECK (max_age_minutes BETWEEN 1 AND 44640),
    reason          TEXT NOT NULL DEFAULT '',
    calculated_at   TIMESTAMPTZ
);
CREATE TABLE calc_versions (
    id              UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    mapping_id      UUID NOT NULL REFERENCES calc_values(mapping_id) ON DELETE CASCADE,
    valid_from      DATE NOT NULL,
    formula         VARCHAR(2000) NOT NULL,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by_name TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_calc_versions_value ON calc_versions (mapping_id, valid_from, created_at);
CREATE TABLE calc_inputs (
    id               UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    version_id       UUID NOT NULL REFERENCES calc_versions(id) ON DELETE CASCADE,
    short_name       VARCHAR(20) NOT NULL,
    input_mapping_id UUID REFERENCES import_mappings(id) ON DELETE SET NULL,
    label            TEXT NOT NULL
);
CREATE UNIQUE INDEX uq_calc_inputs_short ON calc_inputs (version_id, lower(short_name));
CREATE INDEX idx_calc_inputs_mapping ON calc_inputs (input_mapping_id) WHERE input_mapping_id IS NOT NULL;
```

- Produces (Paket `calc`):
  - `const Kind = "calc"`, `const MaxInputs = 50`, `const StatusPlanned, StatusCurrent, StatusSuperseded = "geplant", "gilt", "abgelöst"`
  - `type InputRef struct{ Short, MappingID string }` (JSON `short`, `mapping_id`)
  - `type SaveInput struct{ ID, Name, InfraID, Unit string; Decimals int; MaxAgeMinutes *int; Formula string; Inputs []InputRef; ValidFrom time.Time; VersionID string }` – `ID` leer = neu; `ValidFrom` Null = heute; `VersionID` = diese geplante Fassung ändern.
  - `type SaveResult struct{ ID, VersionID string; Created bool; Version string }` – `Version` ∈ `"created"`, `"updated"`, `"unchanged"`.
  - `type Input struct{ Short, MappingID, Label, Value string; ReceivedAt *time.Time; Gone bool }`
  - `type Version struct{ ID string; ValidFrom time.Time; Formula string; Inputs []Input; CreatedBy string; CreatedAt time.Time; Status string }`
  - `type Value struct{ ID, Name, InfraID, InfraPath, Unit string; Decimals int; MaxAgeMinutes *int; LastValue string; LastAt *time.Time; Reason string; Versions []Version }` – Fassungen neueste zuerst; `func (v *Value) Current() *Version`.
  - `type Candidate struct{ ID, Name, InfraPath, Connection, Value string; ReceivedAt *time.Time; Calc bool }`
  - `type Usage struct{ Values, ExportFields, Meters []string }` – Namen: berechnete Werte, „Verbindung › Feld“, Messstellen-Bezeichnungen.
  - `type Store struct{ DB *pgxpool.Pool; Now func() time.Time; Today func() time.Time }`; `func NewStore(db *pgxpool.Pool) *Store` (`Today` Vorgabe: Kalendertag der Ortszeit als Mitternacht UTC, wie `localDate`).
  - `func (s *Store) ConnectionID(ctx context.Context) (string, error)`
  - `func (s *Store) List(ctx context.Context) ([]Value, error)` (nach Anlagenpfad, Bezeichnung); `func (s *Store) Get(ctx context.Context, id string) (*Value, error)`
  - `func (s *Store) CheckFormula(ctx context.Context, valueID, formulaSrc string, inputs []InputRef) error` – Schreibweise, Kurznamen, unbekannter Kurzname, fehlende Zuordnung, Kreisbezug (`valueID` leer = neuer Wert); Regeln 2 und 6 unten. Für die Prüfung beim Tippen.
  - `func (s *Store) Validate(ctx context.Context, in *SaveInput) error` – trimmt und prüft alles, was `Save` prüft (samt `CheckFormula`), ohne zu schreiben.
  - `func (s *Store) Save(ctx context.Context, in SaveInput, userID string) (SaveResult, error)`
  - `func (s *Store) Delete(ctx context.Context, id string) error`; `func (s *Store) DeleteVersion(ctx context.Context, valueID, versionID string) error`
  - `func (s *Store) Candidates(ctx context.Context, query string, limit int, excludeID string) ([]Candidate, error)` – alle Zuordnungen (wie `availableExportSources`) samt berechneter Werte, ohne `excludeID`; Wörter der Suche gegen Bezeichnung, Anlagenpfad und Verbindung.
  - `func (s *Store) Usage(ctx context.Context, id string) (Usage, error)`
  - `func (s *Store) UsersOfMapping(ctx context.Context, mappingID string) ([]string, error)`, `UsersOfConnection(ctx, connID string)`, `UsersOfSourcePrefix(ctx, connID, prefix string)` – Bezeichnungen der berechneten Werte, die eine der Zuordnungen in einer **geltenden oder geplanten** Fassung als Eingangswert haben (sortiert, ohne Doppelte).
  - `func (s *Store) Formulas(ctx context.Context) (map[string]string, error)` – Zuordnung → geltende Formel (ohne geltende: die früheste geplante).
  - Fehler: `ErrNotFound`; `ErrBadName` („Bezeichnung muss 2 bis 150 Zeichen lang sein“); `ErrNoInfra` („Bitte eine Anlage wählen“); `ErrBadUnit` („Einheit: höchstens 20 Zeichen“); `ErrBadDecimals` („Nachkommastellen: 0 bis 6“); `ErrBadMaxAge` („Höchstalter: 1 bis 44640 Minuten oder leer“); `ErrPastDate` („„Gültig ab“ muss heute oder später sein – berechnete Werte haben keinen Verlauf, rückwirkend gäbe es nichts neu zu rechnen“); `ErrVersionFixed` („Nur geplante Fassungen lassen sich ändern oder löschen“); `ErrTooManyInputs` („Höchstens 50 Eingangswerte je Formel“); `type PlannedExistsError struct{ Date time.Time }` („Für den TT.MM.JJJJ gibt es schon eine geplante Fassung – bitte diese ändern“); `type ShortNameError struct{ Short string }` („Kurzname „X“: Buchstabe, dann Buchstaben, Ziffern oder _, höchstens 20 Zeichen, kein Funktionsname, je Formel nur einmal“); `type UnknownInputError struct{ Short string }` („Die Formel nennt „X“ – das ist kein Eingangswert“); `type InputGoneError struct{ Short string }` („Den Eingangswert „X“ gibt es nicht mehr“); `type CycleError struct{ Names []string }` („Kreisbezug: A → B → A“, Bezeichnungen); `type InUseError struct{ Users []string }` („wird als Eingangswert gebraucht von „A“, „B““). `func UserError(err error) bool` (alle obigen und `*formula.SyntaxError`).
- Regeln von `Save` (eine Transaktion, vorher `SELECT pg_advisory_xact_lock(hashtext('calc_values'))`):
  1. Neuer Wert: Zuordnung an der eingebauten Verbindung mit `source_ref = 'calc:' || pdh_uuid_v7()::text`, `created_by`; dazu `calc_values`. Vorhandener Wert: Bezeichnung, Anlage, Einheit, Nachkommastellen, Höchstalter sofort ändern.
  2. Kurznamen prüfen (`formula.ValidIdent`, ohne Groß/klein eindeutig, höchstens 50); jede Zuordnung muss es geben; jeder Bezug der Formel muss ohne Groß/klein ein Kurzname sein (`[A]` und `A` gleich). Nicht benutzte Eingangswerte sind erlaubt.
  3. `ValidFrom` vor `Today()` → `ErrPastDate`.
  4. Mit `VersionID`: Fassung muss zum Wert gehören und geplant sein (sonst `ErrVersionFixed`); Formel, Eingangswerte, Datum ersetzen, `created_by`/`created_at` auf jetzt; liegt am neuen Datum schon eine **andere** geplante Fassung → `PlannedExistsError`. Ergebnis `"updated"`.
  5. Ohne `VersionID`: Sind Formel (getrimmt) und Eingangswerte (Kurzname ohne Groß/klein → Zuordnung) gleich der Fassung, die an `ValidFrom` gelten würde → keine neue Fassung, `"unchanged"`. Sonst: `ValidFrom` in der Zukunft und dort schon eine geplante Fassung → `PlannedExistsError`; sonst neue Fassung, `"created"`. `created_by_name` = „Vorname Nachname“ aus `users`. `label` je Eingangswert = „Anlagenpfad › Bezeichnung“.
  6. Kreisprüfung über `formula.OrderDeps`: Knoten = berechnete Werte, Kanten = Eingangswerte, die selbst berechnete Werte sind, je Wert vereinigt über alle nicht abgelösten Fassungen (geltend und geplant, nach dem Speichern). Kreis → `*CycleError` mit Bezeichnungen, nichts gespeichert.
- „Gilt“: Fassung mit dem spätesten `valid_from <= Today()`, bei Gleichstand die mit dem spätesten `created_at`. „Geplant“: `valid_from > Today()`. Alles andere „abgelöst“.
- `Delete`: `InUseError`, wenn `UsersOfMapping` nicht leer; sonst Zuordnung löschen (Fassungen gehen über den Fremdschlüssel mit). `DeleteVersion`: nur geplante, sonst `ErrVersionFixed`.

- [ ] **Step 1: Failing tests.** Hilfsaufbau `calcEnv(t)` (Muster `newMappingEnv` in `internal/web/mapping_bulk_integration_test.go`: Pool, `database.RunMigrations(ctx, pool, "../../../migrations")`, Benutzer „Anna Muster“, zwei Anlagen, CSV-Import-Verbindung, `src(name, value string, at time.Time) string` legt eine Zuordnung mit Wert an; `Store.Today` fest auf 2026-11-23; Aufräumen per `t.Cleanup`). Tests:
  - `TestMigrationBuiltinConnection`: genau eine Zeile `kind='calc'`, `direction='import'`, `enabled`; zweites `INSERT` mit `kind='calc'` scheitert am Index.
  - `TestSaveCreateAndGet`: zwei Quellen A, B; `Save{Name:"Summe", Formula:"A + b", Inputs:[A,B], Decimals:1, MaxAgeMinutes:&10}` → `Created`, `Version "created"`; `Get`: eine Fassung `StatusCurrent`, `CreatedBy "Anna Muster"`, Label „<Anlage> › <Name>“; Zuordnung liegt an `ConnectionID()` mit `source_ref` `calc:…`.
  - `TestSaveValidation` (Tabelle): Name „x“ → `ErrBadName`; `Decimals 7` → `ErrBadDecimals`; `MaxAgeMinutes 0` → `ErrBadMaxAge`; Kurzname „MIN“, „1A“, doppelt „a“/„A“ → `*ShortNameError`; Formel `A + C` → `*UnknownInputError{C}`; unbekannte Zuordnung → `*InputGoneError`; 51 Eingangswerte → `ErrTooManyInputs`; Formel `A +` → `*formula.SyntaxError`; `ValidFrom` gestern → `ErrPastDate`. Nach jedem Fall: keine neue Zeile in `calc_values`.
  - `TestVersions`: gleiche Formel noch einmal → `"unchanged"`, weiter eine Fassung; andere Formel heute → zweite Fassung gilt, erste „abgelöst“; Formel für 01.12. → „geplant“; noch eine für 01.12. ohne `VersionID` → `*PlannedExistsError`; mit `VersionID` ändern → `"updated"`; `VersionID` der geltenden → `ErrVersionFixed`; `DeleteVersion` geplant ok, geltend `ErrVersionFixed`. Mit `Today` = 01.12.: die geplante „gilt“.
  - `TestCycle`: V1 = `A`, V2 = `V1 * 2` (Eingang V1); V1 neu als `V2 + 1` → `*CycleError` mit Namen beider Werte; auch wenn V2s Bezug nur in einer **geplanten** Fassung steht.
  - `TestUsersAndDelete`: `UsersOfMapping(A)` = [Summe]; `UsersOfConnection(csv)` = [Summe]; nach einer neuen geltenden Fassung ohne A: leer; `Delete(V1)` mit Nutzer V2 → `*InUseError`; `Delete(V2)` ok, Fassungen weg; gelöschte Quelle einer abgelösten Fassung: Eingangswert `Gone` mit erhaltenem `Label`.
  - `TestCandidates`: Suche „presse a“ findet die Quelle an Anlage A, `excludeID` fehlt, berechnete Werte tragen `Calc`.
  - `model_test.go` (ohne Datenbank): Fehlertexte von `PlannedExistsError` (Datum `01.12.2026`), `CycleError`, `InUseError`; `UserError`.
- [ ] **Step 2: Run** `go test ./internal/modules/calc/ && go test -tags integration ./internal/modules/calc/ -run 'TestMigration|TestSave|TestVersions|TestCycle|TestUsers|TestCandidates'` – Expected: FAIL (undefined).
- [ ] **Step 3: Implement**; Migration eintragen: `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- [ ] **Step 4: Run** dieselben Befehle, dazu `go test ./pkg/database -run TestMigrationsFrozen && go test -tags integration ./pkg/database -run TestUUIDv7Policy` – Expected: PASS (bei `TestUUIDv7Policy` nur die bekannten acht alten Tabellen aus AGENTS.md Punkt 14, keine `calc_…`).
- [ ] **Step 5: Commit** `git commit -m "Berechnete Werte: Tabellen, eingebaute Verbindung und Speicher mit Fassungen (Migration 205)"`

### Task 4: Rechenwerk

**Files:**
- Create: `internal/modules/calc/input.go`, `internal/modules/calc/engine.go`
- Test: `internal/modules/calc/input_test.go`, `internal/modules/calc/engine_integration_test.go`

**Interfaces:**
- Consumes: `Store` (Task 3), `formula.EvalWhy`, `formula.OrderDeps`.
- Produces:
  - `func ParseInput(raw string) (float64, bool)` – getrimmt; eine Zahl mit Punkt oder Komma wie `energie.ParseValue` (Vorzeichen, Ziffern, höchstens ein Dezimalzeichen, kein Exponent, keine Tausendertrennung); `true/on/ein/ja` → 1, `false/off/aus/nein` → 0 (Groß/klein egal); alles andere falsch.
  - `func FormatValue(v float64, decimals int) string` – kaufmännisch gerundet, `strconv.FormatFloat(…, 'f', decimals, 64)`, „-0.00“ wird „0.00“.
  - `type Engine struct{…}`; `func NewEngine(s *Store, skip func() bool) *Engine`
  - `func (e *Engine) Pass(ctx context.Context) (changed int, err error)`
  - `func (e *Engine) Nudge()` – blockiert nie; auf `nil` aufrufbar.
  - `func (e *Engine) Start(ctx context.Context)` – Lauf beim Start, dann alle 10 s und 500 ms nach einem Anstoß (weitere Anstöße in dieser Zeit fallen zusammen).
  - `func (s *Store) Probe(ctx context.Context, formulaSrc string, inputs []InputRef, decimals int, maxAge *int) (value, reason string, err error)` – rechnet mit den Werten von jetzt nach denselben Regeln, speichert nichts (`err` nur bei Schreibfehler der Formel/unbekanntem Kurznamen).
- Lauf (`Pass`), `skip()` wahr → nichts:
  1. Alle berechneten Werte mit geltender Fassung (nach `Store.Today()`), deren Eingangswerte und den Stand aller beteiligten Zuordnungen laden (eine Abfrage je Tabelle, kein N+1).
  2. Reihenfolge über `formula.OrderDeps`; Werte in einem Kreis bekommen „Kreisbezug“ (kann nur durch einen Fehler entstehen).
  3. Je Wert `EvalWhy`; der Abruf eines Kurznamens (ohne Groß/klein) liefert „kein Wert“, wenn der Eingangswert fehlt, nie geliefert hat, keine Zahl ist oder älter als das Höchstalter ist; Ergebnisse dieses Laufs gelten sofort für nachfolgende Werte.
  4. Wert da: `last_value = FormatValue(…)`, `last_received_at` = jüngster `last_received_at` aller Eingangswerte der Fassung (ohne Eingangswerte: bleibt, wenn der Wert gleich ist, sonst `Now()`); Grund leer.
  5. Kein Wert: `last_value = NULL`; `last_received_at` = `Now()`, wenn vorher ein Wert da war oder noch nie etwas stand, sonst unverändert; Grund setzen.
  6. Geschrieben wird nur bei Änderung (`IS DISTINCT FROM`); `changed` zählt geänderte Zuordnungen.
- Gründe (genau diese Texte; K = Kurzname, N = Label des Eingangswerts):
  - `K „N“ hat noch nie einen Wert geliefert`
  - `K „N“ ist keine Zahl („<Wert, höchstens 30 Zeichen>“)`
  - `K „N“ ist älter als <M> Minuten (letzter Wert <TT.MM. HH:MM>)`
  - `K „N“ hat selbst keinen Wert` (Eingangswert ist ein berechneter Wert ohne Wert)
  - `K: den Eingangswert „N“ gibt es nicht mehr`
  - `Teilen durch 0` · `Ergebnis ist keine Zahl` · `Kreisbezug` · `Die erste Fassung gilt ab <TT.MM.JJJJ>` (noch keine geltende Fassung)

- [ ] **Step 1: Failing tests.**
  - `input_test.go`: `TestParseInput` – wahr: `"12.5"`→12.5, `"12,5"`→12.5, `" 7 "`→7, `"-3"`→-3, `"TRUE"`→1, `"Ein"`→1, `"off"`→0, `"nein"`→0; falsch: `""`, `"12,5 kW"`, `"1.234,5"`, `"abc"`, `"1e3"`, `"NaN"`. `TestFormatValue` – `(37.649, 1)`→`"37.6"`, `(2.5, 0)`→`"3"`, `(-0.001, 2)`→`"0.00"`, `(1e6, 0)`→`"1000000"`.
  - `engine_integration_test.go` (mit `calcEnv`, `Store.Now` gestellt):
    - `TestPassFollowsInput`: Summe = `A + B`, A=`12,4`, B=`11.9` → `last_value "24.30"` (2 Stellen), `last_received_at` = jüngerer der beiden; zweiter Lauf `changed == 0`; A neu → `changed == 1`.
    - `TestPassReasons` (je Grund ein Fall, prüft `last_value IS NULL` und den genauen Text): Quelle ohne Wert; Wert `"12,5 kW"`; Wert 11 Minuten alt bei Höchstalter 10; Quelle gelöscht (Zeile in `calc_inputs` bleibt mit Label); `A / B` mit B=0; `WURZEL(0 - A)`; erste Fassung erst ab morgen.
    - `TestPassMaxAgeOff`: Höchstalter leer, Wert 40 Tage alt → Wert da.
    - `TestPassLazyBranch`: `WENN(B > 0; A / B; 0)` mit B=0 → `"0.00"`; `WENN(A > 0; A; C)` mit veraltetem C → Wert da.
    - `TestPassChain`: V1 = `A`, V2 = `V1 * 2`; ein Lauf rechnet beide; A wird zu `"x"` → ein Lauf: V1 leer, V2 leer mit Grund `V1 „… › V1“ hat selbst keinen Wert`; A wieder Zahl → ein Lauf: beide da.
    - `TestPassEmptyKeepsTimestamp`: Wert wird leer → `last_received_at` = gestellte Uhr; weiterer Lauf ohne Änderung → unverändert, `changed == 0`.
    - `TestPassVersionSwitch`: geltende Formel `A`, geplante `A * 2` ab 24.11.; `Today` 23.11. → Wert von A; `Today` 24.11. → doppelt.
    - `TestPassSkip`: `skip` wahr → `changed == 0`, nichts geschrieben.
    - `TestProbe`: liefert `"24.30"` ohne Zeile zu ändern; mit veraltetem Eingangswert den Grund.
- [ ] **Step 2: Run** `go test ./internal/modules/calc/ -run 'TestParseInput|TestFormatValue' && go test -tags integration ./internal/modules/calc/ -run 'TestPass|TestProbe'` – Expected: FAIL.
- [ ] **Step 3: Implement.** `Probe` und `Pass` teilen sich eine Funktion für die Auswertung eines Werts.
- [ ] **Step 4: Run** dieselben Befehle – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Berechnete Werte: Rechenwerk mit Grund bei „kein Wert“, Höchstalter und Fassungswechsel"`

### Task 5: Anstoß bei jedem neuen Wert, Start des Rechenwerks, Energiedaten

**Files:**
- Create: `internal/web/mapping_value.go`, `internal/web/calc_wire.go`
- Modify (Schreibstellen): `internal/web/connection_queries.go:655`, `excel_preview.go:330`, `modbus_preview.go:198`, `mqtt_broker.go:204`, `opcua_preview.go:102`, `sql_preview.go:568`, `web_preview.go:311`
- Modify: `internal/web/handler.go` (Feld `calcEngine *calc.Engine`), `cmd/server/main.go` (nach `StartEnergySampler`: `webHandler.StartCalcEngine(context.Background())`, im Block `if !mirrorOn`)
- Modify: `internal/modules/energie/sampler.go` (kein Rückfall bei leerer Quelle), `internal/modules/energie/store.go`, `model.go` (`ErrCalcConsumption`), `internal/web/energy_wire.go`
- Test: `internal/web/mapping_value_test.go`, `internal/web/calc_wire_test.go`, `internal/modules/energie/sampler_integration_test.go` (ein Test dazu), `internal/modules/energie/store_rules_integration_test.go` (ein Test dazu)

**Interfaces:**
- Consumes: `calc.NewStore`, `calc.NewEngine`, `Engine.Nudge`, `Engine.Start` (Task 3–4).
- Produces:
  - `func (h *Handler) storeMappingValue(ctx context.Context, mappingID, value string) error` – `UPDATE import_mappings SET last_value = $1, last_received_at = NOW() WHERE id = $2::uuid`, danach `h.calcNudge()`. Die sieben Stellen rufen nur noch sie (Fehlerbehandlung der Stellen bleibt).
  - `func (h *Handler) calcStore() *calc.Store` (`nil` ohne Datenbank; `Today` = `localDate(time.Now())`); `func (h *Handler) calcNudge()` (ohne Rechenwerk wirkungslos); `func (h *Handler) StartCalcEngine(ctx context.Context)` – `calc.NewEngine(store, h.MaintLocked)`, `go engine.Start(ctx)`.
  - `energie.ErrCalcConsumption = errors.New("ein berechneter Wert kann nicht Quelle einer Messstelle der Art Verbrauch sein – er wird zusammengefasst geschrieben, die Summe wäre falsch. Bitte Momentanwert oder Zählerstand wählen")`; `Store.Save` gibt ihn zurück, wenn `m.Kind == KindConsumption` und die Quelle an einer Verbindung mit `kind = 'calc'` liegt; `meterUserError` kennt ihn (400).
  - Sampler, echte Messstellen (nicht Verbrauch): steht an der Quelle ein Zeitpunkt vor dem Minutenende, aber kein Wert (`x.receivedAt != nil && x.raw == nil && x.receivedAt.Before(end)`), gibt es für die Minute keinen Wert – kein Rückfall auf `latest`.

- [ ] **Step 1: Failing tests.**
  - `TestMappingValueSingleWriter` (`mapping_value_test.go`, Muster `TestNoUTCDayTruncate`): geht über alle `.go`-Dateien ohne `_test.go` unter `../../internal`, `../../cmd`, `../../pkg`; ein SQL-Text der Form `UPDATE import_mappings SET … last_value = …` (ohne Groß/klein, über Zeilen hinweg, `last_value` vor dem nächsten `WHERE`) darf nur in `internal/web/mapping_value.go` und `internal/modules/calc/engine.go` vorkommen; Fehlermeldung nennt Datei und den Satz „Werte nur über storeMappingValue schreiben, sonst rechnen berechnete Werte nicht nach“.
  - `TestCalcNudgeNeverBlocks` (`calc_wire_test.go`): `(&Handler{}).calcNudge()` ohne Panik; `calc.NewEngine(nil, nil)` nicht gestartet, 1000 × `Nudge()` in unter 100 ms; `var e *calc.Engine; e.Nudge()` ohne Panik.
  - `TestSamplerSourceEmptyNoFallback` (energie, integration): Momentanwert-Messstelle mit Quelle; Minute 1 Wert `5` → abgelegt; dann an der Quelle `last_value = NULL, last_received_at = <in Minute 2>`; Lauf für Minute 2 → `wantNoValue`.
  - `TestSaveCalcSourceNotConsumption` (energie, integration): Zuordnung an der eingebauten Verbindung als Quelle, Art Verbrauch → `ErrCalcConsumption`; Art Momentanwert und Zählerstand → gespeichert.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestMappingValueSingleWriter|TestCalcNudge' && go test -tags integration ./internal/modules/energie -run 'TestSamplerSourceEmpty|TestSaveCalcSource'` – Expected: FAIL (sieben Fundstellen bzw. undefined).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go build ./... && go test ./internal/web -run 'Mqtt|Excel|Modbus|OPCUA|Query|Meter' && go test -tags integration ./internal/modules/energie` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Berechnete Werte: Rechnung bei jedem neuen Eingangswert; Messstelle ohne Rückfall bei leerer Quelle"`

### Task 6: Seite „Berechnete Werte“ mit Formular

**Files:**
- Create: `internal/web/calc_values.go`, `web/templates/calc_values.gohtml`
- Modify: `internal/web/connection_detail.go` (`connectionDetailPage`: Art `calc` → `h.calcPage`), `internal/web/handler.go` (Routen), `internal/web/perm_gate.go` (Regeln), `internal/web/import_export.go` (`connectionKindLabel`: `calc` → „Berechnete Werte“; `ConnectionView.Builtin`), `web/templates/import.gohtml` (Zeile der eingebauten Verbindung: Schild „eingebaut“, ohne Bearbeiten/Löschen/Schalter)
- Test: `internal/web/calc_values_test.go`, `internal/web/calc_values_integration_test.go`

**Interfaces:**
- Consumes: `calc.Store` (Task 3–4), `h.calcStore()`, `h.calcNudge()` (Task 5), `assetGuardFor`, `canConnectionRead/Write`, `formula.Functions()`.
- Routen:
  - `GET /import/calc` → 302 auf `/import/connections/<Kennung der eingebauten Verbindung>` (`import.read`).
  - `GET /import/connections/{id}` mit Art `calc` → Seite; `?new=1` leeres Formular, `?edit=<Wert>` Formular mit der geltenden Fassung, `?edit=<Wert>&version=<Fassung>` mit der geplanten Fassung (zum Ändern).
  - `GET /import/calc/inputs?q=&exclude=` → JSON `[{id, name, path, connection, value, age, calc}]`, höchstens 50 (`import.read`); `age` als Text („vor 3 s“, „vor 2 h“, „noch nie“).
  - `POST /import/calc/check` → JSON `{ok, error, pos, hints[], probe, probe_reason}`; Körper `{id, formula, inputs[{short, mapping_id}], decimals, max_age_minutes}` (`import.read`). Prüft mit `Store.CheckFormula` (auch Kreisbezug, mit Bezeichnungen) und rechnet mit `Store.Probe`; `pos` nur bei `*formula.SyntaxError`. `hints`: „X wird in der Formel nicht benutzt“. `probe` ist der rohe Wert; Dezimalkomma und Einheit setzt die Oberfläche.
  - `POST /import/calc/values` → JSON `{status:"ok", id, version}`; Körper `{id, name, infrastructure_id, unit, decimals, max_age_minutes (null = nicht prüfen), formula, inputs[], valid_from "JJJJ-MM-TT", version_id}`; höchstens 64 KiB.
  - `POST /import/calc/values/{id}/delete` und `POST /import/calc/values/{id}/versions/{vid}/delete` → Formular, zurück auf die Seite mit `notice`/`err`.
  - Gate: `gr("GET", "import.read", "", false, "^/import/calc(?:/inputs)?$")`, `gr(wPost, "import.read", "", false, "^/import/calc/check$")`, `gr(wPost, "import.write", "", false, "^/import/calc/values(?:/.*)?$")`.
- Schreibende Routen, in dieser Reihenfolge: `canConnectionWrite(r, "import")` sonst 403; `h.MaintLocked()` → 503 `errMaintApply`; Anlage des Werts: bei vorhandenem Wert die **alte** Anlage und bei Speichern die **neue** über `h.assetGuardFor(r, "import.write")`, sonst 403 `errAssetDenied`; Fehler mit `calc.UserError` → 400 mit dem Text (erster Buchstabe groß), `ErrNotFound` → 404, sonst 500 „Der berechnete Wert konnte nicht gespeichert werden“ (Fehler ins Protokoll). Nach Erfolg `h.calcNudge()`.
- Produces:
  - `type CalcPageData struct{ BaseData; ConnectionID string; CanWrite bool; Values []calcListRow; Form *calcForm; Functions []formula.FuncInfo; Notice, Err string }`
  - `type calcListRow struct{ ID, Name, InfraPath, Value, Unit, Age, Formula, Since, Reason string; Planned int; NoAgeCheck bool }` – `Value` mit Dezimalkomma; `Since` „gilt seit TT.MM.JJJJ“ bzw. „gilt ab TT.MM.JJJJ“.
  - `type calcForm struct{ ID, VersionID, Name, InfraID, InfraPath, Unit string; Decimals int; MaxAge string; Formula, ValidFrom string; Inputs []calcFormInput; Versions []calcVersionRow; Usage calc.Usage }` – neues Formular: `Decimals 2`, `MaxAge "10"`, `ValidFrom` heute; `calcFormInput{Short, MappingID, Label, Value, Age string; Gone bool}`; `calcVersionRow{ID, Status, ValidFrom, Formula, By, At string; Editable bool}`.
  - `func (h *Handler) calcPage(w http.ResponseWriter, r *http.Request, connID string)`; Handler `CalcRedirectWeb`, `CalcInputsWeb`, `CalcCheckWeb`, `CalcSaveWeb`, `CalcDeleteWeb`, `CalcVersionDeleteWeb`.
  - `BaseData`-Seite bleibt `import` (Kontexthilfe Kapitel `connections`).
- Oberfläche (`calc_values.gohtml`, Skizze im Entwurf, Abschnitt „So sieht es aus“):
  - Kopf „Berechnete Werte“, Knopf „Berechneter Wert“ (`?new=1`, nur mit Schreibrecht). Liste als Karten: Bezeichnung, Anlagenpfad, Wert mit Einheit und Alter oder „kein Wert“; darunter `= Formel`, „gilt seit …“, „· N geplant“; bei Grund eine Zeile mit `ti-alert-triangle` und dem Grund; bei leerem Höchstalter „Alter der Eingangswerte wird nicht geprüft“. Klick öffnet `?edit=`.
  - Formular: Bezeichnung, Anlage (`infra-tree-picker` wie im Reiter „Zuordnungen“), Einheit, Nachkommastellen, „Eingangswerte höchstens [10] Minuten alt (leer = Alter wird nicht geprüft)“; Liste der Eingangswerte (Kurzname als Eingabefeld, Pfad › Bezeichnung, Wert, Alter, „entfernen“); Suchfeld „Wert suchen …“ (`/import/calc/inputs`, 250 ms verzögert), Auswahl bekommt den nächsten freien Buchstaben (A…Z, dann A1…); Klick auf eine Zeile setzt den Kurznamen an der Schreibmarke in die Formel; Formelfeld (`maxlength 2000`); Knöpfe `+ − × ÷ ( ) < > =` und je Funktion aus `.Functions` (setzt `Insert` ein, `title` = `Help`); Prüfzeile (`/import/calc/check`, 400 ms nach dem Tippen): „✓ Formel ist in Ordnung · Probe mit den Werten von jetzt: 37,6 kW“ bzw. Fehler mit „Zeichen N“ bzw. „kein Wert: <Grund>“, Hinweise darunter; „Gültig ab“ (`type=date`, `min` = heute) mit „heute = gilt sofort“; „Speichern“ (`fetch` auf `/import/calc/values`, bei Erfolg Seite neu mit `notice`, bei Fehler Meldung im Formular).
  - „Fassungen“: Status, Datum, Formel, „Anna Muster, 23.11.2026 09:14“; bei geplanten „Ändern“ (`?edit=…&version=…`) und „Löschen“ (Formular mit Rückfrage).
  - „Löschen“ des Werts: Rückfrage nennt aus `Usage` Export-Felder und Messstellen („Das Feld … im Export … und die Messstelle … verlieren ihre Quelle“); wird er als Eingangswert gebraucht, ist der Knopf gesperrt mit dem Satz aus `InUseError`.
  - Daten nur über `textContent`/`value`, kein `innerHTML` mit Daten. Am Handy (≤ 900 px) Felder, Eingangswerte und Knöpfe untereinander, volle Breite; Formeln und Pfade brechen um (`overflow-wrap:anywhere`).

- [ ] **Step 1: Failing tests.**
  - `calc_values_test.go` (ohne Datenbank, `loadTestTemplates`): `TestCalcPageRender` – `ExecuteTemplate` mit zwei Listenzeilen (eine mit Grund, eine mit `NoAgeCheck`) und einem Formular: enthält „Berechneter Wert“, „Eingangswerte höchstens“, „Alter der Eingangswerte wird nicht geprüft“, den Grundtext, `data-insert="WENN(; ; )"`, `/import/calc/check`, „Gültig ab“, „geplant“; der sichtbare Text (nach Entfernen der Tags und Skripte) enthält weder `calc:` noch eine der Test-UUIDs. `TestCalcRoutesGate` – `gateRules` treffen `POST /import/calc/values`, `POST /import/calc/values/<uuid>/delete`, `POST /import/calc/values/<uuid>/versions/<uuid>/delete` mit `import.write` und `POST /import/calc/check` mit `import.read`. `TestCalcSaveMaintLocked` – Handler mit `hasPermHook` wahr und Wartungssperre → 503. `TestConnectionKindLabelCalc` → „Berechnete Werte“.
  - `calc_values_integration_test.go` (`newMappingEnv`, `h.assetGuardHook` gestellt):
    - `TestCalcSaveWeb`: Anlegen → 200, `version "created"`; ohne Schreibrecht → 403; Formel `A +` → 400 mit „Formel, Zeichen“; `valid_from` gestern → 400 mit „heute oder später“.
    - `TestCalcSaveAssetGuard` (Review Focus 5): Guard erlaubt nur Anlage A. Wert an A anlegen ok; Wert nach B verschieben → 403, Anlage unverändert; Wert an B (direkt in der Datenbank angelegt) ändern → 403; dessen geplante Fassung löschen → 403, Fassung noch da; Wert an B löschen → 403.
    - `TestCalcCheckWeb`: `A + B` → `ok`, `probe` gesetzt, nichts gespeichert; `A + C` → `ok false`, Fehler nennt „C“; `A +` → `pos` gesetzt; nicht benutzter Eingangswert → Hinweis; Formel, die einen Kreis schließt → Fehler „Kreisbezug: …“ mit den Bezeichnungen, ohne Kennung.
    - `TestCalcInputsWeb`: Suche liefert Quelle mit Pfad und Wert; ohne `import.read` 403.
    - `TestCalcPageWeb`: `GET /import/connections/<calc>` → 200 mit der Bezeichnung des Werts; `GET /import/calc` → 302 dorthin.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestCalc|TestConnectionKindLabelCalc' && go test -tags integration ./internal/web -run 'TestCalc'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go test ./internal/web -run 'Gate|Connection|Import'` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Berechnete Werte: Seite mit Formelgenerator, Prüfung, Probe und Fassungen"`

### Task 7: Schutz der Eingangswerte, eingebaute Verbindung, Anzeige „berechnet“

**Files:**
- Create: `internal/web/calc_guard.go`
- Modify: `internal/web/import_mappings.go` (`ImportMappingCreateWeb`, `ImportMappingDeleteWeb`), `internal/web/mapping_bulk.go` (`bulkPreamble`), `internal/web/mapping_suggest_web.go` (`ImportSuggestionsWeb`), `internal/web/import_export.go` (`editConnection`, `deleteConnection`, `toggleConnection`), `internal/web/connection_detail.go` (`ConnectionQueryDeleteWeb`), `internal/web/export_mappings.go` (`availableExportSources`), `internal/web/mappings_overview.go` (`allImportMappings`), `internal/web/copilot_data.go` (Zuordnungsliste um Zeile 776), `web/templates/import_mappings_overview.gohtml` (Knopf „Berechneter Wert“ → `/import/calc`, nur mit `import.read`)
- Test: `internal/web/calc_guard_test.go`, `internal/web/calc_guard_integration_test.go`

**Interfaces:**
- Consumes: `calc.Store.UsersOfMapping/UsersOfConnection/UsersOfSourcePrefix`, `Formulas` (Task 3).
- Produces:
  - `func inputInUseText(users []string) string` – „Nicht möglich: wird als Eingangswert gebraucht von „A“, „B“. Bitte zuerst dort die Formel ändern.“ (mehr als fünf: die ersten fünf und „und N weitere“).
  - `func (h *Handler) calcInputUsers(ctx context.Context, by string, args ...string) []string` – `by` ∈ `"mapping"`, `"connection"`, `"source-prefix"`; ohne Datenbank leer.
  - `func (h *Handler) isCalcConnection(ctx context.Context, connID string) bool`
  - `const errCalcBuiltin = "Die eingebaute Verbindung „Berechnete Werte“ lässt sich nicht ändern, abschalten oder löschen"`; `const errCalcOwnPage = "Berechnete Werte legst du auf der Seite „Berechnete Werte“ an"`
  - `func (h *Handler) calcSourceLabels(ctx context.Context) map[string]string` – Zuordnung → „berechnet: <Formel, höchstens 120 Zeichen>“.
- Verhalten:
  - `ImportMappingDeleteWeb`: Nutzer vorhanden → nichts löschen, zurück mit `err = inputInUseText(…)` (bzw. 409 ohne `back`).
  - `deleteConnection`: Art `calc` → `errCalcBuiltin`; Nutzer von Zuordnungen der Verbindung → Meldung „Die Verbindung liefert Eingangswerte für … – bitte zuerst dort die Formel ändern.“, nichts gelöscht.
  - `ConnectionQueryDeleteWeb` mit `with_mappings=1`: Nutzer vorhanden → nichts löschen (auch die Abfrage nicht), Meldung; der bisher verschluckte Fehler des Löschens wird gemeldet.
  - `editConnection`, `toggleConnection`: Art `calc` → 400 `errCalcBuiltin`; die `UPDATE`/`DELETE` bekommen zusätzlich `AND kind <> 'calc'`.
  - `ImportMappingCreateWeb`, `bulkPreamble` (Richtung Import), `ImportSuggestionsWeb`: Verbindung der Art `calc` → 400 `errCalcOwnPage`.
  - `availableExportSources` und `allImportMappings` ersetzen bei Zuordnungen der eingebauten Verbindung `SourceRef` durch `calcSourceLabels`; ebenso die Zuordnungsliste des Copilot. Nirgends erscheint `calc:<uuid>`.

- [ ] **Step 1: Failing tests.**
  - `calc_guard_test.go`: `TestInputInUseText` (ein Name, sieben Namen → „und 2 weitere“); `TestMappingDeletesGuarded` (Muster `TestNoUTCDayTruncate`): jede `.go`-Datei ohne `_test.go` unter `../../internal`, `../../cmd`, `../../pkg` mit `DELETE FROM import_mappings` oder `DELETE FROM import_export_connections` muss in der Liste {`internal/web/import_mappings.go`, `internal/web/import_export.go`, `internal/web/connection_detail.go`, `internal/modules/calc/store.go`} stehen und – im Web-Paket – `calcInputUsers(` aufrufen; Fehlermeldung: „Zuordnungen nur mit Schutz der Eingangswerte löschen (calcInputUsers)“.
  - `calc_guard_integration_test.go` (`newMappingEnv` + berechneter Wert „Summe“ mit Eingangswert an der CSV-Verbindung):
    - `TestUsedInputNotDeletableByHand`: `ImportMappingDeleteWeb` → Meldung nennt „Summe“, Zuordnung noch da.
    - `TestUsedInputNotDeletableWithConnection`: `deleteConnection` → Meldung nennt „Summe“, Verbindung und Zuordnung noch da.
    - `TestUsedInputNotDeletableWithQuery`: Zuordnung mit `source_ref` einer Abfrage als Eingangswert; `ConnectionQueryDeleteWeb` mit `with_mappings=1` → Abfrage und Zuordnung noch da.
    - `TestSupersededInputDeletable`: nach neuer geltender Fassung ohne den Eingangswert lässt er sich löschen; die abgelöste Fassung zeigt sein Label.
    - `TestBuiltinConnectionProtected`: edit, toggle, delete auf der eingebauten Verbindung → Meldung, Zeile unverändert `enabled`; `createConnection` mit `kind=calc` → „Unbekannter Verbindungstyp“; `ImportMappingCreateWeb` und Sammel-Anlage an ihr → 400 `errCalcOwnPage`.
    - `TestCalcSourceLabel`: `availableExportSources` und `allImportMappings` liefern für „Summe“ `SourceRef == "berechnet: A + B"`; kein Eintrag enthält `calc:`.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestInputInUseText|TestMappingDeletesGuarded' && go test -tags integration ./internal/web -run 'TestUsedInput|TestSupersededInput|TestBuiltinConnection|TestCalcSourceLabel'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dieselben Befehle und `go test ./internal/web && go test -tags integration ./internal/web -run 'TestBulk|TestProposal|TestApplyProposal|Mapping'` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Berechnete Werte: genutzte Eingangswerte sind vor dem Löschen geschützt; eingebaute Verbindung fest"`

### Task 8: Copilot auf der Seite „Berechnete Werte“

**Files:**
- Create: `internal/web/calc_copilot.go`
- Modify: `internal/web/copilot_proposals.go` (`proposalRow` um Felder, `checkProposalRows` und `applyProposal` verzweigen bei `p.Kind == calc.Kind`), `internal/web/copilot_mapping.go` (`run` reicht bei Art `calc` an `runCalc`; `proposalView` um Felder), `internal/web/copilot_data.go` (Werkzeuge, Systemtext und Bezeichnungen bei Art `calc`; `web` dort nie), `internal/web/handler.go` (`BaseData.CopilotNoWeb bool`; `CopilotAskWeb` ignoriert `web=1` bei Art `calc`), `internal/web/calc_values.go` (setzt `CopilotConn` über `copilotConnRef` und `CopilotNoWeb = true`), `web/templates/base.gohtml` (Haken nur `{{if and .CopilotConn (not .CopilotNoWeb)}}`; `copRenderProposal` zeigt Formel, Probe, „gültig ab“)
- Test: `internal/web/calc_copilot_test.go`, `internal/web/calc_copilot_integration_test.go`

**Interfaces:**
- Consumes: `calc.Store.List/Candidates/Validate/Save/Probe` (Task 3–4), `copilotConn`, `mappingProposal`, `saveProposal`, `loadProposal`, `CopilotProposalApplyWeb` (Bestand), `h.calcNudge()`.
- Produces:
  - `proposalRow` zusätzlich (alle `omitempty`): `Unit string`, `Decimals *int`, `MaxAgeMinutes *int` (0 = nicht prüfen), `Formula string`, `ValidFrom string` (`JJJJ-MM-TT`, leer = heute beim Übernehmen), `Inputs []calcProposalInput`, `Probe string`; `type calcProposalInput struct{ Short, MappingID, Label string }` (JSON `short`, `mapping_id`, `label`). `Op`: `add` = anlegen, `change` = neue Fassung an `MappingID` (im Werkzeug `value_id`), `delete` = nur nennen.
  - `func calcCopilotTools(canWrite bool) []faults.CopilotTool` – `calc_values` (vorhandene Werte: Kennung, Bezeichnung, Anlage, Einheit, geltende Formel mit Eingangswerten, Wert oder Grund, geplante Fassungen), `calc_inputs` (`query`, `limit` ≤ 200: Kennung, Bezeichnung, Anlagenpfad, Verbindung, Wert, Alter), `asset_tree` (wie Bestand), nur mit `canWrite` `propose_calc` (`summary`, `rows[{op, value_id, name, infrastructure_id, unit, decimals, max_age_minutes, inputs[{short, mapping_id}], formula, valid_from, reason}]`, höchstens 50 Zeilen; Beschreibung nennt die Funktionen aus `formula.Functions()` und „;“ als Trennzeichen).
  - `func (c *copilotConn) runCalc(ctx context.Context, d *copilotData, name string, in map[string]any) (string, bool, error)`
  - `func calcCopilotPrompt(c *copilotConn) string` – Regeln: erst `calc_values` und `calc_inputs` lesen, nichts erfinden; Änderungen nur über `propose_calc`, danach kurz zusammenfassen und auf „Übernehmen“ verweisen; Löschen nur nennen; `valid_from` leer lassen, wenn die Person kein Datum nennt; bei Mehrdeutigkeit (zwei passende Werte, unklare Einheit) zurückfragen; Kennwörter nie; kein Internetzugang.
  - `func (h *Handler) checkCalcRows(ctx context.Context, p *mappingProposal, allow assetGuard) error` – je Zeile: `delete` → `Valid=false`, `Problem = errProposalDelete`; `change` ohne Angabe übernimmt Bezeichnung, Anlage, Einheit, Nachkommastellen, Höchstalter des vorhandenen Werts; `calc.Store.Validate` → `Problem` = Fehlertext; Anlage (bei `change` alte und neue) über `allow`, sonst `errAssetDenied`; `Label` = Bezeichnung, `AssetPath`, Labels der Eingangswerte und `Probe` („37,6 kW“ oder „kein Wert: <Grund>“) füllen.
  - `func (h *Handler) applyCalcRow(ctx context.Context, row *proposalRow, uid string) (created bool, err error)` – `calc.Store.Save`; `applyProposal` zählt `Created`/`Changed`, Text bei Fehler = Fehlertext des Speichers (bei `calc.UserError`) bzw. „Der berechnete Wert konnte nicht gespeichert werden“; danach einmal `h.calcNudge()`.
  - `proposalView` liefert für diese Zeilen zusätzlich `formula`, `probe`, `valid_from` (TT.MM.JJJJ oder leer), `unit`; `label` = Bezeichnung.
  - Bezeichnungen der Werkzeuge: `calc_values` „Berechnete Werte“, `calc_inputs` „Eingangswerte“, `propose_calc` „Vorschlag“.

- [ ] **Step 1: Failing tests.**
  - `calc_copilot_test.go`: `TestCalcCopilotToolsWrite` (ohne Schreibrecht kein `propose_calc`); `TestCalcCopilotPromptRules` (enthält „propose_calc“, „Übernehmen“, „Kennw“, „;“; enthält nicht „Im Internet“); `TestCopilotNoWebOnCalcPage` (Render `calc_values` mit `CopilotConn` gesetzt und `CopilotNoWeb` → kein `id="cop-web"`; `connection_detail` mit `CopilotConn` → weiter vorhanden); `TestProposalViewCalc` (Zeile mit Formel → Ansicht hat `formula`, `probe`, kein `calc:`).
  - `calc_copilot_integration_test.go` (`newMappingEnv`):
    - `TestCalcProposeAndApply`: `runCalc("propose_calc", …)` mit [anlegen gültig, anlegen mit Formel `A +`, löschen] → Vorschlag gespeichert, Zeile 0 `Valid` mit `Probe`, Zeile 1 `Problem` mit „Formel, Zeichen“, Zeile 2 `Problem == errProposalDelete`; **nichts** in `calc_values`. `applyProposal` mit Zeilen 0,1,2 → `Created 1`, zwei Fehler; noch einmal Zeile 0 → `Skipped 1`, weiter ein Wert.
    - `TestCalcProposalForeignUser`: `loadProposal` als andere Person → `errProposalGone`.
    - `TestCalcProposalUncheckedStay`: Vorschlag mit zwei gültigen Zeilen, nur Zeile 1 übernehmen → genau ein Wert, Zeile 0 offen.
    - `TestCalcProposalAssetGuard`: Guard verbietet Anlage B → Zeile mit Anlage B `Problem == errAssetDenied`, wird nicht angelegt.
    - `TestCalcProposeNewVersion`: `change` mit neuer Formel ab 01.12. → nach dem Übernehmen eine geplante Fassung; Bezeichnung und Einheit unverändert.
    - `TestCalcToolsNoRawRefs`: Ausgabe von `calc_values` und `calc_inputs` enthält kein `calc:`.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestCalcCopilot|TestCopilotNoWeb|TestProposalViewCalc' && go test -tags integration ./internal/web -run 'TestCalcPropos|TestCalcTools'` – Expected: FAIL.
- [ ] **Step 3: Implement** (Seitenleiste: nur `textContent`; Zeile zeigt Bezeichnung → Anlage, darunter `= Formel`, „Probe: …“, bei Datum „gültig ab …“, Begründung klein).
- [ ] **Step 4: Run** dieselben Befehle und `go test ./internal/web -run 'Copilot|Proposal' && go test -tags integration ./internal/web -run 'TestProposal|TestApplyProposal|TestCopilotConn'` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Copilot: berechnete Werte und Formeln vorschlagen, anlegen erst mit „Übernehmen“"`

### Task 9: Handbuch, Skizze, Änderungsprotokoll, Version, Übersetzungen, offene Punkte

**Files:**
- Modify: `web/templates/help.gohtml` – Kapitel `connections`: Abschnitt „Berechnete Werte“ mit „So geht's: Berechneten Wert anlegen“ (`.hb-howto`/`.hb-steps`), „So geht's: Formel zu einem Stichtag ändern“, Tabelle der Funktionen mit je einem Beispiel (aus `formula.Functions()` über `internal/web/help.go`, damit sie von selbst aktuell bleibt), „Warum steht dort „kein Wert“?“ (die Gründe, Höchstalter), Rechte und Abteilung, Copilot („Übernehmen“, Löschen von Hand, was an den Anbieter geht), Hinweis Schnellübersicht (zeigt nur Werte, deren Bezeichnung nach einem Energiewert klingt) und „Im Zweifel Klammern setzen“ (`-2 ^ 2` ist −4). Kapitel `energiedaten`: berechneter Wert als Quelle (nicht für Verbrauch); Formeln der Messstellen bleiben bei + − × ÷.
- Modify: `web/templates/widgets/help_figures.gohtml` (Skizze des Formulars: Eingangswerte A/B/C, Formelzeile, Knöpfe, Prüfzeile, Fassungen; Farben über Variablen), `internal/web/help.go` (Funktionsliste für das Handbuch)
- Modify: `CHANGELOG.md` (`## [0.82.37] – 2026-11-23`, `### Neu`: berechnete Werte mit Formelgenerator; Fassungen mit „gültig ab“; Copilot schlägt berechnete Werte vor. `### Geändert`: ein genutzter Eingangswert lässt sich nicht mehr löschen; Messstelle legt keinen Wert ab, wenn ihre Quelle leer ist. `### Behoben`: Löschen einer Abfrage „samt Zuordnungen“ meldet einen Fehler statt ihn zu verschlucken), `VERSION` (0.82.37), `web/i18n/{en,ro,tr,mk}.json`, `AGENTS.md` (Punkt 24: Stand von Paket 2a, Stand und Datum)

- [ ] **Step 1:** Handbuch, Skizze, Änderungsprotokoll, Version, Übersetzungen, AGENTS.md schreiben.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestHelp|TestI18nCatalogsComplete' && go test . -run TestChangelogMatchesVersion` – Expected: PASS.
- [ ] **Step 3: Volle Prüfung** `go vet ./... && go test ./... && go test -tags integration ./internal/core/... ./internal/modules/calc/ ./internal/modules/energie/ && go test -tags integration ./internal/web -run 'TestCalc|TestUsedInput|TestSupersededInput|TestBuiltinConnection|TestBulk|TestProposal|TestApplyProposal'` – Expected: PASS (bekannte Wackler der geteilten Labor-Datenbank einzeln wiederholen und im Ledger nennen).
- [ ] **Step 4: Prüfung vor dem Merge** nach AGENTS.md: `git fetch origin main`, auf `origin/main` bringen, Dopplungen suchen (`git diff origin/main...HEAD`; Migration 205 und Version 0.82.37 noch frei?), Tests erneut.
- [ ] **Step 5: Commit** `git commit -m "Berechnete Werte: Handbuch, Änderungsprotokoll (0.82.37)"`

Danach, nur mit ausdrücklicher Freigabe des Anwenders: Push und Merge nach `main`, Verbund-Update, Browserprüfung am Testverbund mit dem Prüfkonto (Fiori/Fluent, hell/dunkel, 320–1366 px; Werte über den eingebauten Broker stellen; Testdaten entfernen, Konto wieder sperren).
