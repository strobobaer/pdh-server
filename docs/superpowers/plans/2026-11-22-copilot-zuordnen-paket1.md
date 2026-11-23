# Copilot beim Zuordnen (Paket 1) – Bauplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Zuordnungen von Import- und Export-Verbindungen werden vorgeschlagen – ohne Sprachmodell am Formular (A) und als Änderungsliste des Copilot (B) –, angelegt wird erst mit „Übernehmen“.

**Architecture:** Ein reiner Rechenkern `internal/core/mapsuggest` (Wörter, Ähnlichkeit, Bezeichnung, Einheit, Feldname). Das Web-Paket sammelt die Quellwerte je Verbindungsart, zeigt Vorschläge als Teilansicht im Reiter „Zuordnungen“ und legt sie über einen gemeinsamen Schreibweg an (derselbe wie von Hand, mit Abteilungsprüfung der Anlage). Der Copilot bekommt auf einer Verbindungsseite eigene Werkzeuge und ein Werkzeug „propose_mappings“, dessen Ergebnis als Vorschlag in `copilot_proposals` liegt (24 h) und in der Seitenleiste als Änderungsliste mit „Übernehmen“ erscheint. Websuche (Anthropic) liefert Vorlagen mit Links.

**Tech Stack:** Go (chi, html/template, pgx), htmx, anthropic-sdk-go v1.78.0, PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-11-22-copilot-zuordnen-design.md` (bestätigt „Sonst war alles ok“, 22.11.2026)

## Global Constraints

- Version: nächste Korrekturversion über `origin/main` (Stand jetzt 0.82.32 → **0.82.33**); vor dem Merge `git show origin/main:VERSION` prüfen.
- Migration: nächste freie Nummer über `origin/main` (jetzt **203**), in `migrations/checksums.txt` mit `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`; neue Kennungen mit `pdh_uuid_v7()`.
- „Kein selbsttätiges Anlegen oder Löschen – jede Änderung braucht „Übernehmen“. Löschen schlägt der Copilot nur vor, ausgeführt wird es wie bisher von Hand.“
- „Übernehmen: dieselben Rechte wie von Hand (`import.write` bzw. Export-/Abgleich-Recht), zusätzlich prüft das PDH je Zeile, ob die Person an der Anlage (Abteilung) arbeiten darf.“ – kein neues Recht.
- „Kennwörter, Schlüssel und Verbindungsgeheimnisse gehen nie an den Copilot.“ (`connSecretFields`)
- „Nicht am Spiegel, nicht in der Wartung (Übernehmen).“ – Spiegel sperrt POST schon in `MirrorMiddleware`; Wartung: `h.MaintLocked()`.
- Höchstens **500 Zeilen** je Vorschlag und je Sammel-Anlage.
- Vorschläge am Formular funktionieren ohne Internet und ohne Copilot. Assistent und Websuche nur mit Anthropic (`CanUseTools`, `CanSearchWeb`).
- Texte deutsch, für Anwender im Betrieb; Oberfläche in Fiori/Fluent, hell/dunkel über CSS-Variablen.
- Tests in Git Bash: `go vet ./...`, `go test ./...`; Datenbanktests mit `PDH_TEST_DSN` (Labor-Datenbank).

## Review Focus

1. Quellwert mit Umlauten/Nummern („Presse_3/Öl-Temp“) gegen Anlagen „Presse 3“ und „Presse 30“: darf nie „Presse 30“ treffen.
2. Person mit `import.write` nur in Abteilung A übernimmt eine Zeile mit Anlage aus Abteilung B: Zeile wird abgelehnt und gemeldet, die anderen werden angelegt.
3. Vorschlag einer anderen Person, abgelaufen (> 24 h) oder schon übernommen: „Übernehmen“ tut nichts Doppeltes und meldet den Grund.
4. Verbindung mit Kennwort: weder `connection_info` noch `source_values` noch der Systemtext enthalten das Kennwort.
5. MQTT-Broker nicht erreichbar oder OPC-UA-Endpunkt aus: Vorschlagsliste zeigt den Grund statt eines Fehlers 500, Abfragespalten erscheinen trotzdem.

---

### Task 1: Rechenkern `mapsuggest`

**Files:**
- Create: `internal/core/mapsuggest/mapsuggest.go`
- Test: `internal/core/mapsuggest/mapsuggest_test.go`

**Interfaces:**
- Produces:
  - `type Source struct{ Ref, Label, Sample string }` – Label ist der lesbare Quellname (Topic, „Abfrage › Spalte“, Knotenpfad).
  - `type Asset struct{ ID, Name, Path string }`
  - `type Suggestion struct{ Source; AssetID, AssetPath, Name, Note string; Score float64 }`
  - `func Tokens(s string) []string` – klein, ä→ae ö→oe ü→ue ß→ss, Trenner sind alle Nicht-Buchstaben/Ziffern, camelCase und Wechsel Buchstabe/Ziffer trennen („Presse3“ → `presse`,`3`).
  - `func Suggest(sources []Source, assets []Asset) []Suggestion` – je Quelle ein Eintrag in Eingabereihenfolge.
  - `func UnitHint(label, sample string) string`
  - `func ExportFieldName(assetName, name string) string`

- [ ] **Step 1: Failing tests** – `TestTokens`, `TestSuggestAsset`, `TestSuggestName`, `TestUnitHint`, `TestExportFieldName`:

```go
func TestTokens(t *testing.T) {
	want := map[string][]string{
		"Halle2/Presse_3/Öl-Temp": {"halle", "2", "presse", "3", "oel", "temp"},
		"motorStromL1":            {"motor", "strom", "l", "1"},
		"Abfrage › Spalte":        {"abfrage", "spalte"},
	}
	for in, w := range want { if got := Tokens(in); !reflect.DeepEqual(got, w) { t.Errorf("%q: %v", in, got) } }
}
func TestSuggestAsset(t *testing.T) {
	assets := []Asset{{"a3", "Presse 3", "Werk › Halle 2 › Presse 3"}, {"a30", "Presse 30", "Werk › Halle 2 › Presse 30"}, {"h2", "Halle 2", "Werk › Halle 2"}}
	got := Suggest([]Source{{Ref: "halle2/presse3/strom", Label: "halle2/presse3/strom", Sample: "12.5"}, {Ref: "x", Label: "lager/temp"}}, assets)
	if got[0].AssetID != "a3" { t.Errorf("Presse 3 erwartet: %+v", got[0]) }   // nie a30, tiefere Anlage vor Halle 2
	if got[1].AssetID != "" || got[1].Score != 0 { t.Errorf("ohne Treffer leer: %+v", got[1]) }
}
func TestSuggestName(t *testing.T) {
	got := Suggest([]Source{{Ref: "r", Label: "halle2/presse3/oel_temp", Sample: "41"}}, []Asset{{"a3", "Presse 3", "Werk › Halle 2 › Presse 3"}})
	if got[0].Name != "Oel Temp" || got[0].Note != "Einheit °C" { t.Errorf("%+v", got[0]) }
}
func TestUnitHint(t *testing.T) {
	for in, w := range map[[2]string]string{
		{"strom_l1", "3.2"}: "Einheit A", {"spannung", "230"}: "Einheit V", {"wirkleistung", "4"}: "Einheit kW",
		{"energie_kwh", "100"}: "Einheit kWh", {"temp", "20"}: "Einheit °C", {"druck", "6"}: "Einheit bar",
		{"drehzahl", "1500"}: "Einheit 1/min", {"feuchte", "40"}: "Einheit %", {"frequenz", "50"}: "Einheit Hz",
		{"laeuft", "1"}: "Zustand (0/1)", {"status", "true"}: "Zustand (ein/aus)", {"name", "abc"}: "",
	} { if got := UnitHint(in[0], in[1]); got != w { t.Errorf("%v: %q", in, got) } }
}
func TestExportFieldName(t *testing.T) {
	if got := ExportFieldName("Presse 3", "Öl-Temperatur"); got != "Presse_3_Oel_Temperatur" { t.Error(got) }
	if got := ExportFieldName("", "–"); got != "Wert" { t.Error(got) } // mindestens 2 Zeichen
}
```

- [ ] **Step 2: Run** `go test ./internal/core/mapsuggest/` – Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** Regeln (Entscheidung des Bauenden laut Entwurf):
  - Treffer einer Anlage: alle Zahl-Wörter des Anlagennamens müssen als eigenes Wort in der Quelle stehen (sonst 0); Wortanteil = gefundene Wörter des Anlagennamens / Wörter des Anlagennamens; Score = Wortanteil + 0,1 × gefundene Wörter des Pfads (ohne Namen), höchstens 1,5. Schwelle 0,6. Gleichstand: längerer Pfad (tiefere Anlage) gewinnt.
  - Bezeichnung: Wörter der Quelle ohne die Wörter der gefundenen Anlage und ihres Pfads, jedes mit großem Anfangsbuchstaben, mit Leerzeichen; ist das Ergebnis kürzer als 2 Zeichen, das letzte Segment des Labels (nach `/`, `›`, `.`) ungekürzt; höchstens 150 Zeichen.
  - Einheit nach Wortstamm (`strom|current|ampere`→A, `spannung|voltage`→V, `leistung|power`→kW, `energie|energy|kwh|zaehler`→kWh, `temp*`→°C, `druck|pressure`→bar, `drehzahl|rpm`→1/min, `feuchte|humidity`→%, `frequenz|frequency|hz`→Hz); sonst Wert `0`/`1` → „Zustand (0/1)“, `true`/`false` → „Zustand (ein/aus)“.
  - Feldname: Wörter mit Original-Großschreibung (Umlaute umgeschrieben), Nicht-Alphanumerisches → `_`, Mehrfach-`_` zusammen, ohne `_` am Rand, Anlage `_` Name, höchstens 150 Zeichen, mindestens 2 (sonst `Wert`).
- [ ] **Step 4: Run** `go test ./internal/core/mapsuggest/` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Zuordnen: Rechenkern für Vorschläge (Anlage, Bezeichnung, Einheit)"`

### Task 2: Gemeinsamer Schreibweg und Sammel-Anlage

**Files:**
- Modify: `internal/web/import_mappings.go` (Prüfung und Einfügen herausziehen), `internal/web/export_mappings.go` (dito)
- Create: `internal/web/mapping_bulk.go`
- Modify: `internal/web/handler.go` (Routen), `internal/web/perm_gate.go` (Regeln)
- Test: `internal/web/mapping_bulk_test.go` (Einheit), `internal/web/mapping_bulk_integration_test.go` (`//go:build integration`)

**Interfaces:**
- Produces:
  - `func (in *importMappingCreateInput) normalize() error` – trimmt und prüft wie bisher (gleiche Meldungen).
  - `func (in *exportMappingCreateInput) normalize() error`
  - `func (h *Handler) insertImportMapping(ctx context.Context, connID, uid string, in importMappingCreateInput) (id string, dup bool, err error)` – `dup` = gleiche `source_ref` und Anlage gibt es an der Verbindung schon (dann kein Einfügen).
  - `func (h *Handler) insertExportMapping(ctx context.Context, connID, uid string, in exportMappingCreateInput) (id string, dup bool, err error)` – `dup` = gleicher `import_mapping_id` an der Verbindung.
  - `func (h *Handler) exportSourceAsset(ctx context.Context, importMappingID string) (string, error)` – Anlage des Import-Werts.
  - `type assetGuard func(infraID string) bool` und `func (h *Handler) assetGuardFor(r *http.Request, perm string) assetGuard` (= `h.hasPermRec(r, perm, "infrastructure", id)`).
  - `type bulkResult struct{ Created, Skipped int; Errors []bulkError }`, `type bulkError struct{ Index int; Error string }` (JSON `created`, `skipped`, `errors[{index,error}]`).
  - `func (h *Handler) bulkImportMappings(ctx context.Context, connID, uid string, items []importMappingCreateInput, allow assetGuard) bulkResult`
  - `func (h *Handler) bulkExportMappings(ctx context.Context, connID, uid string, items []exportMappingCreateInput, allow assetGuard) bulkResult`
  - Routen: `POST /import/connections/{id}/mappings/bulk`, `POST /export/connections/{id}/mappings/bulk` – JSON `{"items":[…]}`, höchstens 500 (sonst 400 „Höchstens 500 Zuordnungen auf einmal“), Körper bis 512 KiB; `canConnectionWrite`; Wartung → 503 „Wartung läuft – bitte später übernehmen“; danach einmal `reconcileMqttConsumer`.
  - Gate: `gr(wPost, "import.write", "", false, `^/import/connections`+gID+`/mappings/bulk$`)`, Export entsprechend.
- Einzel-Anlage von Hand (`ImportMappingCreateWeb`, `ExportMappingCreateWeb`) nutzt dieselben Funktionen und prüft zusätzlich die Abteilung der Anlage: ohne Recht 403 „Keine Berechtigung für diese Anlage“ (Sicherheit, siehe CHANGELOG).

- [ ] **Step 1: Failing tests**
  - `TestImportMappingNormalize` (Einheit): leere Quelle → „Ungültiger Quellwert“; Name „x“ → „Bezeichnung muss 2 bis 150 Zeichen lang sein“; Heartbeat 90000 → Fehler; gültig → nil und getrimmt.
  - `TestBulkImportMappings` (integration): Verbindung + zwei Anlagen anlegen; Items [gültig A, gültig B, doppelt zu A, Anlage verboten (Guard false), Name zu kurz] → `Created 2, Skipped 1`, Errors für Index 3 („Keine Berechtigung für diese Anlage“) und 4; Datenbank hat genau 2 Zeilen.
  - `TestBulkExportMappings` (integration): zwei Import-Werte, Items [W1, W1 nochmal, W2 mit Guard false] → `Created 1, Skipped 1`, ein Fehler.
  - `TestBulkRouteLimits` (Einheit, Handler mit `hasPermHook` true, ohne DB): 501 Items → 400 mit Meldung; `MaintLocked` → 503.
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestImportMappingNormalize|TestBulkRouteLimits'` und `go test -tags integration ./internal/web -run 'TestBulk'` – Expected: FAIL (undefined).
- [ ] **Step 3: Implement** wie in Interfaces; Einfügen und Dopplungsprüfung in einer Abfrage (`INSERT … SELECT … WHERE NOT EXISTS (…) RETURNING id`).
- [ ] **Step 4: Run** dieselben Befehle – Expected: PASS; zusätzlich `go test ./internal/web -run 'Gate|Mapping'` grün.
- [ ] **Step 5: Commit** `git commit -m "Zuordnen: gemeinsamer Schreibweg, Sammel-Anlage, Abteilung der Anlage"`

### Task 3: Quellwerte je Verbindungsart

**Files:**
- Create: `internal/web/mapping_sources.go`
- Test: `internal/web/mapping_sources_test.go`

**Interfaces:**
- Consumes: `mapsuggest.Source` (Task 1)
- Produces:
  - `func querySources(queries []connQuery) []mapsuggest.Source` – je Spalte des letzten Ergebnisses `Ref: queryRef(q.ID, col)`, `Label: q.Name + " › " + col`, `Sample` aus `LastResult.rowFor(q.Spec)`.
  - `func tabularSources(labels []string, lastRow []string) []mapsuggest.Source` – Excel/CSV-Spalten (Ref = Spaltenname wie in `ExcelRefreshValuesWeb`).
  - `func mqttStatSources(stats []MqttTopicStat) []mapsuggest.Source` – Thema als Wert (`Ref = mqttSourceRef(topic,"")`) und je JSON-Feld (`mqttSourceRef(topic, field.Path)`, Label `topic › path`).
  - `func sampleMqttTopics(ctx context.Context, config map[string]string, wait time.Duration, max int) ([]MqttTopicStat, error)` – kurzer Mitschnitt mit `mqttClientOptions(config, "suggest")`, Filter aus `ext_topic_filter`/`int_topic_filter` (sonst `#`), Tabelle `newMqttTopicTable(max, 1)`, Wartezeit 3 s.
  - `func opcuaSources(ctx context.Context, config map[string]string, max int) ([]mapsuggest.Source, error)` – `openOPCUAClient`, Breitensuche ab `i=85` mit `opcuaBrowseChildren(…, 200)`, Tiefe ≤ 4, nur `Class == "Variable"`, Label = Namen der Ebenen mit „ › “.
  - `func filterSources(src []mapsuggest.Source, mapped map[string]bool, max int) []mapsuggest.Source`
  - `type sourceSet struct{ Sources []mapsuggest.Source; Hints []string }`
  - `func (h *Handler) mappingSources(ctx context.Context, connID, kind string, config map[string]string) sourceSet` – Abfragespalten immer; dazu Excel/CSV-Datei, MQTT-Mitschnitt, OPC-UA-Knoten; Fehler einer Art werden Hinweis („Broker nicht erreichbar: …“), nie Abbruch; schon zugeordnete Refs der Verbindung fallen weg; Dopplungen nach Ref entfernt; höchstens 500.
  - `func (h *Handler) assetList(ctx context.Context) ([]mapsuggest.Asset, error)` – rekursiver Pfad wie `importMappings`, alle Anlagen.
- Modbus hat ohne Abfrage keine aufzählbaren Quellen (Hinweis „Für Modbus erst eine Abfrage anlegen und ausführen – oder den Copilot nach der Registertabelle fragen“).

- [ ] **Step 1: Failing tests** – `TestQuerySources` (zwei Abfragen, eine ohne Ergebnis → nur Spalten der ersten, Sample der letzten Zeile bei `row=last`), `TestMqttStatSources` (Thema mit JSON `{"a":{"b":1}}` → Refs `t` und `t#a.b`), `TestFilterSources` (zugeordnete Ref fehlt, Dopplung einmal, Grenze), `TestSampleMqttTopicsBroker` (mit `mqttbroker` wie in `explorer_mapping_test.go`: retained Nachricht auf `halle/1/temp` → Thema im Ergebnis; Broker-Port zu → Fehler, kein Hängen über 10 s).
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestQuerySources|TestMqttStatSources|TestFilterSources|TestSampleMqttTopicsBroker'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Zuordnen: Quellwerte je Verbindungsart sammeln"`

### Task 4: Vorschläge am Formular (A)

**Files:**
- Create: `internal/web/mapping_suggest_web.go`, `web/templates/widgets/mapping_suggestions.gohtml`
- Modify: `web/templates/connection_detail.gohtml` (Knopf im Reiter „Zuordnungen“/„Felder“, Auswahl-Dialog für die Anlage), `internal/web/handler.go` (Routen), `internal/web/perm_gate.go`
- Test: `internal/web/mapping_suggest_web_test.go`

**Interfaces:**
- Consumes: Task 1–3.
- Produces:
  - `type suggestRow struct{ Ref, Label, Sample, AssetID, AssetPath, Name, Note, FieldName, ImportMappingID string; Checked bool }`
  - `type SuggestionsView struct{ Direction, ConnectionID string; Rows []suggestRow; Hints []string }`
  - `func buildImportSuggestions(src sourceSet, assets []mapsuggest.Asset) SuggestionsView` – `Checked` nur mit Anlage.
  - `func buildExportSuggestions(sources []ImportMappingView, exported map[string]bool) SuggestionsView` – nicht exportierte Werte, Feldname über `mapsuggest.ExportFieldName(letztes Pfadsegment, Name)`, alle `Checked=false`.
  - `GET /import/connections/{id}/suggestions` und `GET /export/connections/{id}/suggestions` → Teilansicht `mapping-suggestions` (nur mit `canConnectionWrite`, sonst 403; Efficio-Export → 404); Zeitlimit 20 s.
  - Gate: `gr("GET", "import.write", "", false, `^/import/connections`+gID+`/suggestions$`)`, Export entsprechend.
- Oberfläche: Knopf „Zuordnungen vorschlagen“ (`ti-wand`) bzw. „Felder vorschlagen“ lädt per `hx-get` in `#cd-suggest`. Tabelle: Haken, Quelle (+ Beispielwert), Anlage (Pfad, Knopf „Ändern“ öffnet Dialog mit einem `infra-tree-picker`), Bezeichnung/Feldname (Eingabe), Notiz (Eingabe); Kopf mit „Alle“, Filterfeld; „Ausgewählte übernehmen“ → `POST …/mappings/bulk` → Seite neu mit Meldung „N angelegt, M schon vorhanden“ bzw. Fehlerliste im Kasten. Hinweise (`Hints`) als gelbe Zeile. Leer: „Keine offenen Quellwerte – alles ist zugeordnet oder es kam noch nichts an.“

- [ ] **Step 1: Failing tests** – `TestBuildImportSuggestions` (Quelle mit Treffer → Checked, ohne → unchecked mit leerem AssetID), `TestBuildExportSuggestions` (exportierter Wert fehlt, Feldname `Presse_3_Strom`), `TestSuggestionsRender` (`loadTestTemplates` + `ExecuteTemplate(… "mapping-suggestions" …)`: enthält `data-ref="halle/1/temp"`, „Ausgewählte übernehmen“, Hinweistext), `TestConnectionDetailHasSuggestButton` (Render `connection_detail` mit `CanWrite: true` enthält `/suggestions`; mit `CanWrite: false` nicht).
- [ ] **Step 2: Run** `go test ./internal/web -run 'Suggest'` – Expected: FAIL.
- [ ] **Step 3: Implement** (CSS nur mit `--bg`, `--bg2`, `--border`, `--text`, `--muted`, `--accent`; Tabelle in `overflow-x:auto`; am Handy Eingaben volle Breite).
- [ ] **Step 4: Run** – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Zuordnen: Vorschläge zum Abhaken im Reiter Zuordnungen"`

### Task 5: Vorschlag speichern und übernehmen

**Files:**
- Create: `migrations/203_copilot_proposals.up.sql`, `internal/web/copilot_proposals.go`
- Modify: `migrations/checksums.txt`, `internal/web/import_mappings.go`/`export_mappings.go` (Ändern), `internal/web/handler.go` (Route)
- Test: `internal/web/copilot_proposals_test.go`, `internal/web/copilot_proposals_integration_test.go`

**Interfaces:**
- Migration:

```sql
CREATE TABLE copilot_proposals (
    id            UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connection_id UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE CASCADE,
    direction     TEXT NOT NULL CHECK (direction IN ('import', 'export')),
    summary       TEXT NOT NULL DEFAULT '',
    rows          JSONB NOT NULL,
    links         JSONB NOT NULL DEFAULT '[]',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours',
    applied_at    TIMESTAMPTZ
);
CREATE INDEX idx_copilot_proposals_user ON copilot_proposals (user_id, created_at DESC);
```

- Produces:
  - `type proposalRow struct{ Op, SourceRef, ImportMappingID, MappingID, InfrastructureID, AssetPath, Name, Note, FieldName, Reason, SourceURL, Problem string; Valid, Applied bool }` – `Op` ∈ `add`, `change`, `keep`, `delete`; nur `add`/`change` mit `Valid` sind übernehmbar.
  - `type copilotLink struct{ Title, URL string }`
  - `type mappingProposal struct{ ID, UserID, ConnectionID, Direction, Kind, Summary string; Rows []proposalRow; Links []copilotLink; ExpiresAt time.Time; AppliedAt *time.Time }`
  - `func validProposalSource(kind, ref string, queries map[string]bool, columns map[string]bool) string` (Einheit, Problemtext oder leer): Abfrage-Ref mit bekannter Abfrage, MQTT-Thema ohne `+`/`#`, Modbus `parseModbusReading`, OPC UA `opcuaNodeIDRe`, Excel/CSV bekannter Spaltenname.
  - `func (h *Handler) checkProposalRows(ctx context.Context, p *mappingProposal, allow assetGuard)` – setzt `Valid`/`Problem`/`AssetPath`: Anlage gibt es und darf bearbeitet werden; Quelle über `validProposalSource`; Export: Import-Wert existiert; Dopplung (gibt es schon / doppelt im Vorschlag); `change`: Zuordnung gehört zur Verbindung, alte und neue Anlage erlaubt; `delete`: `Valid=false`, Problem „Löschen bitte von Hand“; `keep`: `Valid=false` ohne Problem; Namen/Notiz/Feldname mit `normalize()` geprüft; mehr als 500 Zeilen → Fehler.
  - `func (h *Handler) saveProposal(ctx context.Context, p *mappingProposal) error` – löscht vorher Vorschläge mit `expires_at < NOW() - INTERVAL '7 days'`.
  - `func (h *Handler) loadProposal(ctx context.Context, id, uid string) (*mappingProposal, error)` – nur eigene; `errProposalGone` bei fremd/unbekannt, `errProposalExpired`.
  - `func (h *Handler) updateImportMapping(ctx context.Context, connID, mappingID string, in importMappingCreateInput) error`, `func (h *Handler) updateExportMapping(ctx context.Context, connID, mappingID string, in exportMappingCreateInput) error` (Name/Notiz/Anlage bzw. Feldname/Notiz, `updated_at = NOW()`).
  - `type applyResult struct{ Created, Changed, Skipped int; Errors []bulkError }`
  - `func (h *Handler) applyProposal(ctx context.Context, p *mappingProposal, rows []int, uid string, allow assetGuard) applyResult` – prüft jede gewählte Zeile erneut (`checkProposalRows`), legt über `insertImportMapping`/`insertExportMapping` an bzw. ändert, setzt `Applied`, speichert `rows` zurück, `applied_at`, sobald keine übernehmbare Zeile offen ist; schon übernommene Zeile → `Skipped`.
  - `POST /copilot/proposals/{id}/apply` (Formular `rows=0,2,5`) → JSON `applyResult`; 404 fremd/unbekannt, 410 „Der Vorschlag ist abgelaufen – bitte neu fragen“, 403 ohne `canConnectionWrite(direction)`, 503 in der Wartung; danach `reconcileMqttConsumer` bei Import.

- [ ] **Step 1: Failing tests** – `TestValidProposalSource` (Einheit: MQTT `a/+/b` ungültig, `a/b` gültig, Modbus `holding:1:float32` gültig, `holding:x` ungültig, OPC UA `ns=2;s=X` gültig, Abfrage-Ref mit unbekannter Abfrage ungültig), `TestProposalRoundTrip` (integration: speichern, laden als Besitzer ok, als andere Person `errProposalGone`, `expires_at` in die Vergangenheit → `errProposalExpired`), `TestApplyProposal` (integration: 1× add, 1× change, 1× add mit verbotener Anlage, 1× delete → `Created 1, Changed 1`, ein Fehler, delete nicht ausgeführt; zweites Übernehmen derselben Zeilen → `Skipped 2`, nichts doppelt).
- [ ] **Step 2: Run** `go test ./internal/web -run TestValidProposalSource` und `go test -tags integration ./internal/web -run 'TestProposalRoundTrip|TestApplyProposal'` – Expected: FAIL.
- [ ] **Step 3: Implement**; Migration eintragen: `PDH_MIGRATIONS_APPEND=1 go test ./pkg/database -run TestMigrationsFrozen`.
- [ ] **Step 4: Run** dieselben Befehle und `go test ./pkg/database -run TestMigrationsFrozen` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Copilot: Vorschläge speichern und übernehmen (Migration 203)"`

### Task 6: Copilot-Werkzeuge auf der Verbindungsseite

**Files:**
- Create: `internal/web/copilot_mapping.go`
- Modify: `internal/web/handler.go` (`BaseData.CopilotConn`, `CopilotAskWeb`), `internal/web/connection_detail.go` (setzt `CopilotConn`), `internal/web/copilot_data.go` (`copilotData.conn`, `exec` reicht unbekannte Namen an die Verbindungswerkzeuge weiter, Labels)
- Test: `internal/web/copilot_mapping_test.go`

**Interfaces:**
- Consumes: Task 2, 3, 5.
- Produces:
  - `BaseData.CopilotConn string` – `"import:<id>"`/`"export:<id>"`.
  - `type copilotConn struct{ Direction, ID, Kind, Name string; Config map[string]string; CanWrite bool; Allow assetGuard; Proposal *mappingProposal }`
  - `func copilotConnTools(direction string, canWrite bool) []faults.CopilotTool` – `connection_info`, `source_values` (query, limit ≤ 500), `asset_tree` (query, limit ≤ 300), `mappings`, bei Export `export_sources`, nur mit `canWrite` `propose_mappings` (Eingabe `summary`, `rows[{op, source_ref, import_mapping_id, mapping_id, infrastructure_id, name, note, field_name, reason, source_url}]`).
  - `func (c *copilotConn) exec(ctx context.Context, d *copilotData, name string, in map[string]any) (string, bool, error)` – zweiter Wert: Werkzeug gehört hierher.
  - `func connectionInfoText(name, kind string, enabled bool, config map[string]string, check *checkReport, queries []connQuery) string` – ohne `connSecretFields`, Kennwortfelder als „gesetzt“/„leer“.
  - `func copilotConnPrompt(c *copilotConn, web bool) string` – Regeln: Änderungen nur über `propose_mappings`, danach kurz zusammenfassen und auf „Übernehmen“ in der Änderungsliste verweisen; Löschen nur nennen; bei Mehrdeutigkeit zurückfragen; nie Kennwörter erfragen oder nennen; mit `web` Websuche für Vorlagen erlaubt, Quelle als Link und in `source_url`.
  - `CopilotAskWeb`: Feld `conn` (`^(import|export):<uuid>$`, sonst 400), `canConnectionRead` sonst 403; Antwort zusätzlich `proposal` (Ansicht: `id`, `summary`, `rows` mit `op`, `label`, `asset`, `name`, `note`, `field`, `reason`, `url`, `valid`, `problem`) und `links`.
  - Labels: `connection_info` „Verbindung“, `source_values` „Quellwerte“, `asset_tree` „Anlagenbaum“, `mappings` „Zuordnungen“, `export_sources` „Importwerte“, `propose_mappings` „Vorschlag“.

- [ ] **Step 1: Failing tests** – `TestConnectionInfoNoSecrets` (Konfig mit `password: geheim123`, `ext_password`, `bearer_token` → Text enthält „geheim123“ nicht, enthält „Passwort: gesetzt“), `TestCopilotConnToolsWrite` (ohne Schreibrecht kein `propose_mappings`; Export hat `export_sources`), `TestCopilotConnPromptRules` (enthält „propose_mappings“, „Übernehmen“, „Kennw“; mit `web=false` kein „Websuche“), `TestCopilotAskConnParam` (Handler ohne Copilot: `conn=import:kaputt` → 400; gültiges Format ohne Leserecht → 403).
- [ ] **Step 2: Run** `go test ./internal/web -run 'TestConnectionInfoNoSecrets|TestCopilotConn|TestCopilotAskConnParam'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Copilot: Werkzeuge und Vorschlag auf der Verbindungsseite"`

### Task 7: Websuche nach Vorlagen

**Files:**
- Modify: `internal/modules/faults/copilot_tools.go`
- Test: `internal/modules/faults/copilot_tools_test.go`
- Modify: `internal/web/copilot_data.go` (`askCopilot` mit Optionen), `internal/web/copilot_mapping.go`

**Interfaces:**
- Produces:
  - `type AskOptions struct{ Web bool }`, `type Link struct{ Title, URL string }`
  - `func (c *Copilot) AskWithToolsOpts(ctx context.Context, system, question string, tools []CopilotTool, exec ToolExecutor, opts AskOptions) (answer string, used []string, links []Link, err error)`; `AskWithTools` ruft sie mit `AskOptions{}` auf (Verhalten unverändert).
  - `func toolParams(model, system, question string, tools []CopilotTool, opts AskOptions) anthropic.BetaMessageNewParams` – mit `Web` zusätzlich `BetaWebSearchTool20260209Param{MaxUses: 5}` und `BetaWebFetchTool20260209Param{MaxUses: 4, MaxContentTokens: 20000}`.
  - `func collectLinks(content []anthropic.BetaContentBlockUnion, seen map[string]bool, links []Link) []Link` – zuerst Zitate der Textblöcke (`web_search_result_location`), dann Treffer der Suche; doppelte URLs einmal, nur `http(s)://`, höchstens 8.
  - Schleife: `pause_turn` → Antwort anhängen und weiter (zählt als Runde).
  - `askCopilot(ctx, d, userName, question, faultID string)` liefert zusätzlich `links []faults.Link`; Websuche an, wenn `d.conn != nil && cp.CanSearchWeb()`.

- [ ] **Step 1: Failing tests** – `TestToolParamsWeb` (ohne Web 1 Werkzeug, mit Web 3, Namen `web_search`/`web_fetch` in `json.Marshal`), `TestCollectLinks` (Inhalt aus JSON: Textblock mit Zitat `https://a.example/x`, Suchergebnis mit `https://a.example/x` und `https://b.example/y` und `ftp://c` → `[a, b]`).
- [ ] **Step 2: Run** `go test ./internal/modules/faults -run 'TestToolParamsWeb|TestCollectLinks'` – Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** dasselbe und `go test ./internal/modules/faults ./internal/web -run Copilot` – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Copilot: Websuche nach Vorlagen mit Quellenangabe"`

### Task 8: Änderungsliste in der Copilot-Seitenleiste

**Files:**
- Modify: `web/templates/base.gohtml` (`sendCop` schickt `conn`, zeichnet `proposal` und `links`, Hinweis zum Datenschutz), CSS dort
- Test: `internal/web/copilot_mapping_test.go`

**Interfaces:**
- Consumes: Antwortfelder aus Task 6/7, Route aus Task 5.
- Oberfläche: Karte `.cop-diff` mit Kopf (Zusammenfassung, Zähler „+N neu · ~M geändert“), je Zeile Zeichen `+` (grün), `~` (Akzent), `=` (grau, unverändert), `–` (grau, „wird nicht angefasst“: ungültig ohne Haken) bzw. `✕` „löschen – bitte von Hand“, Quelle, Pfeil, Anlage, Bezeichnung/Feld, Begründung klein, Link „Quelle“ bei `url`; Haken nur bei übernehmbaren Zeilen (vorausgewählt); ungültige Zeilen mit Grund in Rot; Knopf „Übernehmen“ → `POST /copilot/proposals/{id}/apply` → Ergebnis „3 angelegt, 1 geändert“ + Fehlerliste + Link „Seite neu laden“. Links aus `links` als „Quellen aus dem Internet“. Auf einer Verbindungsseite steht unter dem Eingabefeld: „Auf dieser Seite gehen Quellwerte und Anlagennamen an Anthropic – Kennwörter nie.“

- [ ] **Step 1: Failing test** – `TestCopilotSidebarConn` (Render `connection_detail` mit `BaseData.CopilotConn = "import:…"`: enthält die Kennung im Aufruf, `copRenderProposal`, `/copilot/proposals/`, Hinweistext; ohne `CopilotConn` kein Hinweistext).
- [ ] **Step 2: Run** `go test ./internal/web -run TestCopilotSidebarConn` – Expected: FAIL.
- [ ] **Step 3: Implement** (nur `textContent`, keine `innerHTML` mit Daten; Farben über Variablen).
- [ ] **Step 4: Run** – Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "Copilot: Änderungsliste mit Übernehmen in der Seitenleiste"`

### Task 9: Handbuch, Änderungsprotokoll, Version

**Files:**
- Modify: `web/templates/help.gohtml` (Kapitel `connections`: „So geht's: Zuordnungen vorschlagen lassen“, „So geht's: Mit dem Copilot zuordnen“, Rechte/Abteilung, Datenschutz), ggf. `web/templates/widgets/help_figures.gohtml` (Skizze des Reiters „Zuordnungen“ mit Knopf), `CHANGELOG.md` (`## [0.82.33] – 2026-11-22`: Neu – Vorschläge am Formular, Copilot-Änderungsliste, Websuche nach Vorlagen; Sicherheit – Zuordnung nur an Anlagen der eigenen Abteilung), `VERSION`, `AGENTS.md` (Punkt 24)

- [ ] **Step 1: Run** `go test ./internal/web -run 'TestHelp' && go test . -run TestChangelogMatchesVersion` nach den Änderungen – Expected: PASS.
- [ ] **Step 2: Volle Prüfung** `go vet ./... && go test ./... && go test -tags integration ./internal/web -run 'TestBulk|TestProposal|TestApplyProposal'` – Expected: PASS (bekannte Wackler der geteilten Labor-Datenbank einzeln wiederholen).
- [ ] **Step 3: Commit** `git commit -m "Copilot beim Zuordnen: Handbuch, Änderungsprotokoll (0.82.33)"`
