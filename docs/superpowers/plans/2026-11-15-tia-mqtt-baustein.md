# TIA-Portal-Baustein „MQTT senden“ (0.80.13) – Umsetzungsplan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An einer MQTT-Import-Verbindung lädt man die Quelle eines eigenen TIA-Datenbausteins hoch und bekommt ein ZIP mit SCL-Quellen für S7-1500/S7-1200, die die Werte bei steigender Trigger-Flanke als JSON (`values.<Name>`, bei Bedarf in Teilnachrichten) und regelmäßig einen Heartbeat senden; auf Wunsch legt das PDH die passenden Zuordnungen an und meldet „Steuerung meldet sich nicht“.

**Architecture:** Neues Paket `internal/modules/tiamqtt` ohne Datenbank: Leser für die `.db`-Quelle (eigener Tokenizer und rekursiver Abstieg), Aufteilung der Variablen in Teile mit fester Byte-Obergrenze (Grundlage ist eine Liste von Schreibschritten, aus der sowohl der SCL-Code als auch ein Go-Gegenstück zum Prüfen entsteht), Erzeugung über `text/template` mit `embed` und Einsetzfunktionen, die jede Zeichenkette SCL-sicher machen, dazu ein kleiner MQTT-3.1.1-Kodierer in Go als Gegenstück zum SCL-Code. Die Web-Seite (`internal/web/tia_block.go`) belegt das Formular aus der Verbindung vor, prüft Rechte, liefert das ZIP und legt Zuordnungen an. Der Heartbeat wird über eine neue Spalte `import_mappings.heartbeat_seconds` überwacht.

**Tech Stack:** Go 1.27, chi, html/template, htmx, Postgres (pgx v5), `text/template` + `embed`, `archive/zip`, `golang.org/x/text/encoding/charmap` (schon direkte Abhängigkeit), `github.com/mochi-mqtt/server/v2/packets` (nur Tests), integrierter Broker `internal/core/mqttbroker`.

**Spec:** `docs/superpowers/specs/2026-11-15-tia-mqtt-baustein-design.md`

## Global Constraints

- Umfang **0.80.13**: `VERSION` → `0.80.13`, `CHANGELOG.md` neuer oberster Eintrag `## [0.80.13] – 2026-11-15`. Schwellwerte, Alarme, Tickets nicht hier (0.80.14). Migration **162** (`migrations/162_tia_mqtt.up.sql`; 161 ist `energy`).
- Steuerungen S7-1500 und S7-1200 (ab Firmware 4.x), SCL, optimierter Zugriff, Quellen für TIA Portal ab V17. Nicht: S7-300/400, Empfangen/Abonnieren, QoS 1/2 und TLS in der eigenständigen Variante.
- Varianten: `standalone` „Eigenständig (TCON/TSEND/TRCV, ohne TLS)“ und `lmqtt` „Mit Siemens-Bibliothek LMQTT (TLS möglich)“. Beide Bausteine haben dieselbe Schnittstelle.
- Eingänge `enable`, `trigger` (steigende Flanke = senden), `heartbeatInterval : Time := T#60s` (T#0s = aus), `hwId : HW_ANY := 64`, `connId : CONN_OUC := 1`; InOut `daten`; Ausgänge `connected`, `busy`, `done` (ein Zyklus), `error`, `status : Word`, `statusText : String[60]`, `sentCount : UDInt`, `heartbeatCount : UDInt`.
- Konfiguration in DB `"PDH_<DB>_Konfig"`: `brokerHost`, `brokerPort` (Vorgabe 1883, mit TLS 8883), `clientId`, `username`, `password`, `topic`, `heartbeatTopic` (= `<topic>/heartbeat`), `keepAlive` (Vorgabe 60 s), `retain` (Vorgabe FALSE), `useTls` (nur `lmqtt`).
- Nachricht: `{"ts":"2026-11-15T10:01:00Z","part":1,"parts":2,"values":{…}}`, `part`/`parts` immer dabei (auch `1`/`1`). `ts` aus der Steuerungsuhr (`RD_SYS_T`, UTC), alle Teile eines Triggers mit gleichem `ts` und aus demselben Abbild der Werte. Heartbeat auf `heartbeatTopic`: `{"ts":"…","count":N}`, **nie Retain**.
- Grenze je Nachricht (JSON-Inhalt) Vorgabe **4096 Byte**, erlaubt 1024–16384; höchstens 50 Teile, 2000 Einzelwerte, Datei höchstens 1 MiB. Strings zählen mit Höchstlänge (Vorgabe `String` = 254).
- JSON-Pfade der Zuordnungen: `values.<Name>`, Strukturen mit Punkt (`values.Motor.Drehzahl`), Felder als JSON-Array mit PDH-Index ab 0 (`values.Feld[0]` für SCL `Feld[1]`) – passend zu `jsonAtPath` (`connection_queries.go`). Quellverweis `mqttSourceRef(topic, path)`.
- Kennwort steht nur in der Datei, wenn „Kennwort mitliefern“ angekreuzt ist, sonst Platzhalter `PASSWORT_EINTRAGEN`.
- Rechte: neues `import.tia` „Steuerungsbaustein (TIA Portal) herunterladen“, Kategorie „Daten & Schnittstellen“, an alle Rollen mit `import.read` (Abteilung übernehmen wie Migration 161). Mit Kennwort oder „Zuordnungen anlegen“ zusätzlich `import.write`.
- „Steuerung meldet sich nicht“: Heartbeat-Zuordnung, deren letzter Wert (vor dem ersten Wert: Anlagezeitpunkt der Zuordnung) älter als **3 × Intervall** ist.
- Nur Zeichenketten über die Einsetzfunktionen in SCL; nichts vom Anwender roh in eine Vorlage.
- Texte, Handbuch, Commits auf Deutsch in Anwendersprache; Fiori/Fluent und hell/dunkel über Variablen; keine Attributionszeilen; nie `git add -A`.
- Tests in Git Bash: `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable' go test ./...`; Datenbanktests ohne Variable `t.Skip`.

## Review Focus

1. **Umlaute und Sonderzeichen** in Variablennamen (`"Zähler"`), String-Werten (`"`, `\`, Tabulator, `ä`), Kennwort (`'`, `$`) und Topic: SCL übersetzt, JSON bleibt gültiges UTF-8, PDH liest den Wert. → Task 3 `TestRenderEscapesStrings`, Task 4 `TestSCLStringEscapes`, `TestGenerateNoRawUserText`.
2. **Echte TIA-Exporte** mit BOM, CRLF, Windows-1252, `//`- und `(* *)`-Kommentaren, `{ … }`-Attributen, `VAR RETAIN`, `TITLE`/`VERSION`, `BEGIN`-Teil und mitexportierten PLC-Datentypen: werden gelesen, nicht abgewiesen. → Task 2 `TestParseTIAExport`, `TestParseEncodings`.
3. **Real/LReal ist NaN oder unendlich:** JSON enthält `null` statt `NaN`/`Inf`, die Nachricht bleibt lesbar. → Task 3 `TestRenderRealNaN`; Task 4 Golden prüft den `null`-Zweig.
4. **Heartbeat bleibt aus, aber ein alter Wert liegt noch auf dem Broker** bzw. Zuordnung frisch angelegt: kein Retain auf dem Heartbeat, vor dem ersten Wert zählt der Anlagezeitpunkt. → Task 4 `TestStandaloneHeartbeatNoRetain`, Task 7 `TestHeartbeatSilent`.
5. **Zweimal herunterladen mit „Zuordnungen anlegen“:** keine doppelten Zuordnungen, vorhandene bleiben unverändert. → Task 6 `TestTIADownloadMappingsNoDuplicates`.

---

## Dateien

| Datei | Aufgabe |
|---|---|
| `migrations/162_tia_mqtt.up.sql` | Spalte `heartbeat_seconds`, Recht `import.tia` |
| `internal/modules/tiamqtt/dbparse.go` | `.db`-Quelle lesen |
| `internal/modules/tiamqtt/layout.go`, `render.go` | Einzelwerte, Teile, Byte-Grenzen, Go-Gegenstück des JSON |
| `internal/modules/tiamqtt/sclquote.go`, `generate.go`, `templates/*.tmpl` | SCL-Erzeugung, ZIP |
| `internal/modules/tiamqtt/mqttref.go` | MQTT-3.1.1-Kodierer in Go (Gegenstück zum SCL, für Tests) |
| `internal/modules/tiamqtt/testdata/` | Beispielquellen, Golden-Dateien |
| `internal/web/tia_block.go`, `web/templates/tia_block.gohtml`, `widgets/tia_block_check.gohtml` | Seite, Prüfung, Download, Zuordnungen |
| `internal/web/import_mappings.go`, `connection_detail.go`, `mappings_overview.go` | Heartbeat-Anzeige |

---

### Task 1: Migration 162

**Files:** Create `migrations/162_tia_mqtt.up.sql`, `internal/modules/tiamqtt/migration_integration_test.go`

**Interfaces – Produces (Schema):**
- `ALTER TABLE import_mappings ADD COLUMN IF NOT EXISTS heartbeat_seconds int CHECK (heartbeat_seconds IS NULL OR heartbeat_seconds BETWEEN 1 AND 86400)` – gesetzt nur an Heartbeat-Zuordnungen.
- `permissions`: `('import.tia', 'Steuerungsbaustein (TIA Portal) herunterladen', 'Daten & Schnittstellen') ON CONFLICT (key) DO NOTHING`; `role_permissions` aus `import.read` mit übernommener Abteilung, `ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = NULL WHERE EXCLUDED.department_id IS NULL` (Muster Migration 161, Zeilen 141–148).

- [ ] **Step 1: Test** `TestTIAMigration` (`PDH_TEST_DSN`, `database.RunMigrations`): `heartbeat_seconds = 0` scheitert, `60` und NULL gehen; Rolle mit globalem `import.read` hat `import.tia` global; Rolle mit `import.read` nur in Abteilung D hat `import.tia` nur in D; Kategorie „Daten & Schnittstellen“.
- [ ] **Step 2:** `go test ./internal/modules/tiamqtt/ -run TestTIAMigration -v` → FAIL.
- [ ] **Step 3:** Migration schreiben.
- [ ] **Step 4:** → PASS; `go test ./pkg/database/` → PASS.
- [ ] **Step 5:** Commit `TIA-Baustein: Recht zum Herunterladen und Heartbeat-Kennzeichen an Zuordnungen (Migration 162)`.

### Task 2: Quelle des Datenbausteins lesen

**Files:** Create `internal/modules/tiamqtt/dbparse.go`, `dbparse_test.go`, `testdata/Daten.db` (Beispiel mit allen Typen, Struktur, Feld, Kommentaren, Attributen, `BEGIN`), `testdata/MitTyp.db` (TYPE + DATA_BLOCK), `testdata/Daten_cp1252.db`

**Interfaces – Produces:**
```go
type Kind int // KindBool, KindInt, KindDInt, KindUInt, KindUDInt, KindWord, KindDWord, KindReal, KindLReal, KindString, KindDTL, KindDT, KindStruct, KindArray
type Var struct {
    Name string; Kind Kind; Line int
    StrLen int          // String[n]; ohne Angabe 254
    Lo, Hi int          // Array-Grenzen (eindimensional)
    Elem *Var           // Array-Element (Name leer)
    Members []*Var      // Struct und aufgelöster PLC-Datentyp
    Init string         // Startwert wie in der Quelle (roh, für den Ersatz-DB)
}
type Skipped struct{ Path string; Line int; Reason string } // Path in SCL-Schreibweise
type DBSource struct {
    Name string          // ohne Anführungszeichen
    Vars []*Var; Skipped []Skipped
    Begin string         // roher BEGIN-Teil
    HasRetain bool       // VAR RETAIN oder Retain-Attribute gefunden
}
type ParseError struct{ Line int; Msg string } // Error(): "Zeile 12: „;“ erwartet"
func ParseDBSource(src []byte) (*DBSource, error)
```
- Vorlauf: BOM entfernen; kein gültiges UTF-8 → als Windows-1252 lesen (`charmap.Windows1252`); `\r\n` wie `\n`. Schlüsselwörter und Typnamen ohne Groß-/Kleinschreibung.
- Überspringen: `//…`, `(* … *)`, `{ … }` (verschachtelt), `TITLE = …`, `AUTHOR`, `FAMILY`, `NAME`, `VERSION : x.y`, `NON_RETAIN`, `RETAIN`.
- Grammatik: Datei = `{ TYPE "X" [VERSION…] STRUCT decls END_STRUCT; END_TYPE | DATA_BLOCK … END_DATA_BLOCK }`; `decl = name [{attr}] : type [:= init] ;`; `type = Elementartyp | String[n] | Struct decls END_STRUCT | Array[lo..hi] of type | "UDT"`. Namen bare oder in `"…"`.
- Abweisen (`*ParseError`): kein `DATA_BLOCK` („Die Datei enthält keinen Datenbaustein“); mehrere („Bitte nur einen Datenbaustein exportieren“); `DATA_BLOCK "X" "Y"` ohne VAR-Teil und `Y` nicht als TYPE in der Datei („Instanz- oder Typ-Datenbaustein ohne eigene Variablen – bitte einen Global-DB mit Variablenliste exportieren“); Syntaxfehler mit Zeile; Datei > 1 MiB; Verschachtelung > 8.
- Überspringen mit Grund (`Skipped`): andere Typen („Typ Char wird nicht unterstützt“, auch WString, LInt, Time, Byte …); `"UDT"` nicht in der Datei („PLC-Datentyp "X" fehlt – Quelle mit abhängigen Bausteinen exportieren“); mehrdimensionale Felder; Felder von Strukturen oder Feldern („Feld von Strukturen wird nicht unterstützt“); Feld mit mehr als 1000 Elementen; Namen mit `.`, `[`, `]`, `"`, `\`, `#`, Steuerzeichen oder länger als 128 Zeichen („Name enthält Zeichen, die im JSON-Pfad nicht gehen“). Ein leerer Struct nach dem Überspringen fällt selbst weg.

- [ ] **Step 1: Tests:** `TestParseTIAExport` (`testdata/Daten.db`: Name `Daten`; `Zaehler` Real, `Takt` Int mit `Init "5"`, `Motor` Struct mit `Drehzahl` Real und `Ein` Bool, `Feld` Array[1..10] of Int, `Text` String[20], `Name` String → `StrLen 254`, `Zeit` DTL; `HasRetain` true; `Begin` enthält `Feld[1] := 3;`); `TestParseUDTFromSameFile` (`testdata/MitTyp.db`: Member des Typs aufgelöst); `TestParseEncodings` (gleiche Datei mit BOM, CRLF und als Windows-1252 mit `"Zähler"` → Name `Zähler`); `TestParseSkipped` (Char, WString, `Array[1..2, 1..3]`, `Array[1..2] of Struct`, fehlender UDT, Name `"a.b"` → je ein `Skipped` mit Zeile und Grund, übrige Variablen da); `TestParseErrors` (leere Datei, nur `FUNCTION_BLOCK`, zwei DBs, Instanz-DB, `Zaehler : Real` ohne `;` → `*ParseError` mit Zeile).
- [ ] **Step 2:** `go test ./internal/modules/tiamqtt/ -run TestParse -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `TIA-Baustein: Quelle des eigenen Datenbausteins lesen, nicht unterstützte Variablen benennen`.

### Task 3: Einzelwerte, Teilnachrichten und JSON-Gegenstück

**Files:** Create `internal/modules/tiamqtt/layout.go`, `render.go`, `layout_test.go`, `render_test.go`

**Interfaces:**
- Consumes: `DBSource`, `Var`, `Kind`, `Skipped` (Task 2).
- Produces:
```go
type Family string // FamilyS71500 = "s7-1500", FamilyS71200 = "s7-1200"
const (DefaultLimit = 4096; MinLimit = 1024; MaxLimit = 16384; MaxParts = 50; MaxLeaves = 2000)
type Leaf struct {
    Path string     // SCL: Motor.Drehzahl, Feld[3]
    JSONPath string // values.Motor.Drehzahl, values.Feld[2]
    Kind Kind; StrLen int; Width int // größte Länge des JSON-Werts in Byte
}
type OpKind int // OpRaw (feste Zeichen, Kommas schon eingerechnet), OpValue
type Op struct{ Kind OpKind; Raw string; Leaf *Leaf }
type Part struct{ No int; Ops []Op; Leaves []Leaf; MaxBytes int }
type Layout struct{ Parts []Part; Leaves []Leaf; Skipped []Skipped; Limit int }
func Plan(db *DBSource, family Family, limit int) (*Layout, error)
func HeaderWidth(parts int) int                                         // Kopf {"ts":…,"part":…,"parts":…,"values":{ und Schluss }}
func ValueWidth(k Kind, strLen int) int
func Render(p Part, parts int, ts time.Time, get func(path string) any) []byte // macht genau, was der SCL-Code schreibt
func WorstCase(l Leaf) any
```
- Breiten: Bool 5, Int 6, DInt 11, UInt 5, UDInt 10, Word 5, DWord 10 (Word/DWord als Zahl), Real 15, LReal 25, `null` 4, String `2·n + 2`, DTL/DT 21 (`"JJJJ-MM-TTThh:mm:ss"`, ohne Zone). Die Tests entscheiden, ob die Breiten reichen.
- Zahlen: Real `strconv.FormatFloat(v, 'E', 6, 32)`, LReal `'E', 14, 64` – so schreibt es auch `PDH_J_Real`/`PDH_J_LReal` (Task 4); NaN/±Inf → `null`.
- Strings: `"` → `\"`, `\` → `\\`, Zeichen < 0x20 → Leerzeichen, Bytes 0x80–0xFF als Latin-1 nach UTF-8 (je höchstens 2 Byte, daher `2·n + 2`).
- Schlüssel und Namen: UTF-8 wie in der Quelle, geschrieben als feste Zeichen (`OpRaw`).
- Aufteilen: Einzelwerte in Quellreihenfolge, gierig, solange `HeaderWidth(parts) + Summe ≤ limit`; Strukturen dürfen auf mehrere Teile verteilt werden (in jedem Teil wieder geöffnet), ein Feld bleibt ganz in einem Teil. Passt ein Feld oder ein String allein nicht → `Skipped` („zu groß für eine Nachricht“). Die Teilezahl hängt vom Kopf ab: so lange wiederholen, bis sie sich nicht mehr ändert.
- `FamilyS71200`: DT → `Skipped` („Date_And_Time gibt es auf S7-1200 nicht – DTL verwenden“). Über `MaxParts` oder `MaxLeaves` → Fehler „Zu viele Werte: höchstens 50 Nachrichten bzw. 2000 Werte“. Keine Werte übrig → Fehler „Keine sendbaren Variablen gefunden“.

- [ ] **Step 1: Tests:** `TestPlanPaths` (`Daten.db`: `Feld[1]` → `values.Feld[0]`, `Motor.Ein` → `values.Motor.Ein`); `TestPlanSplitsAtLimit` (300 Real-Werte, Grenze 4096 → mehrere Teile, jeder `MaxBytes ≤ 4096`, alle Werte genau einmal); `TestPlanArrayStaysWhole`; `TestPlanTooBigSkipped` (String[2000] bei Grenze 1024); `TestPlanS71200SkipsDT`; `TestPlanLimits` (2001 Werte, 0 Werte, Grenze 512 → Fehler); `TestRenderWorstCaseFits` (jedes Teil mit `WorstCase` gerendert: `len ≤ MaxBytes ≤ Limit`, gültiges JSON, `part`/`parts` stimmen); `TestRenderMatchesPaths` (gerendertes JSON über einen Pfad-Leser mit Index ab 0 ergibt je `Leaf.JSONPath` den Wert); `TestRenderEscapesStrings` (`a"b\c`, Tab, `ä` als Byte 0xE4 → gültiges UTF-8 `a\"b\\c ä`); `TestRenderRealNaN` (NaN, +Inf → `null`, `json.Valid`); `TestRenderTimestamp` (`"ts":"2026-11-15T10:01:00Z"`).
- [ ] **Step 2:** `go test ./internal/modules/tiamqtt/ -run 'TestPlan|TestRender' -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `TIA-Baustein: Werte auf Nachrichten bis zur Größengrenze verteilen, JSON-Aufbau wie in der Steuerung`.

### Task 4: SCL-Erzeugung eigenständig und MQTT-Kodierer in Go

**Files:** Create `internal/modules/tiamqtt/sclquote.go`, `generate.go`, `mqttref.go`, `templates/common.tmpl` (Typ, JSON-Hilfsbausteine, Konfig-DB, Ersatz-DB), `templates/standalone.tmpl`, `generate_test.go`, `mqttref_test.go`, `testdata/golden/standalone/*`

**Interfaces:**
- Consumes: `DBSource`, `Layout`, `Part`, `Op`, `Leaf`, `Family` (Task 2, 3).
- Produces:
```go
type Variant string // VariantStandalone = "standalone", VariantLMQTT = "lmqtt"
type Options struct {
    Variant Variant; Family Family
    BrokerHost string; BrokerPort int; UseTLS bool
    ClientID, Username, Password string; IncludePassword bool
    Topic string; HeartbeatSeconds int /* 0–3600, Vorgabe 60 */; KeepAliveSeconds int /* 10–600, Vorgabe 60 */
    Retain bool; HwID, ConnID int /* Vorgabe 64, 1 */
    GeneratedBy string; GeneratedAt time.Time // im Dateikopf; Golden-Tests setzen feste Werte
}
type File struct{ Name string; Content []byte }
func Generate(db *DBSource, l *Layout, o Options) ([]File, error)
func Zip(files []File) ([]byte, error)
func ZipName(dbName string, v Variant) string               // "PDH_MQTT_Daten_eigenstaendig.zip" / "…_lmqtt.zip"
func SCLString(s string, max int) (string, error)           // '…' mit $$, $', $hh für < 0x20 und ≥ 0x80 (UTF-8-Bytes); Fehler bei > max Byte
func SCLName(s string) string                               // "…" für Bausteinnamen, nur [A-Za-z0-9_], sonst _
const PasswordPlaceholder = "PASSWORT_EINTRAGEN"
// mqttref.go – Gegenstück zum SCL-Kodierer
func RemainingLength(n int) []byte
func ConnectPacket(clientID, user, pass string, keepAlive uint16) []byte // Clean Session; Benutzer-/Kennwort-Flag nur wenn gesetzt, Kennwort nur mit Benutzer
func PublishPacket(topic string, payload []byte, retain bool) []byte   // QoS 0
func PingReq() []byte; func Disconnect() []byte                       // C0 00, E0 00
```
- Dateien (UTF-8 mit BOM, CRLF), in Import-Reihenfolge: `1_PDH_<DB>_Typ.udt` (TYPE mit den gesendeten Variablen), `2_PDH_MQTT_<DB>.scl` (Hilfs-FCs `"PDH_J_Raw"`, `"PDH_J_DInt"`, `"PDH_J_UDInt"`, `"PDH_J_Real"`, `"PDH_J_LReal"`, `"PDH_J_Str"`, `"PDH_J_DTL"` mit Überlaufschutz, dann FB `"PDH_MQTT_<DB>"`), `3_PDH_<DB>_Konfig.db` (Konfig-DB; bei `standalone` dazu `"PDH_<DB>_Puffer"` mit `S7_Optimized_Access := 'FALSE'`, damit `LEN` bei `TSEND`/`TRCV` sicher gilt), `4_<DB>_aus_Typ.db` (Ersatz für den eigenen DB: `DATA_BLOCK "<DB>" "PDH_<DB>_Typ"`, Startwerte aus `Init` und `Begin`), `LIESMICH.txt` (Schritte, Statuscodes, Teile mit Werten und JSON-Pfaden, übersprungene Variablen mit Zeile und Grund, Hinweis auf verlorene Retain-Einstellungen bei `HasRetain`).
- FB `standalone`: Ablauf `TCON` (`TCON_IP_v4`, `ConnectionType := 11`, `ActiveEstablished := TRUE`, IPv4 aus `brokerHost`) → CONNECT senden → CONNACK (`20 02 xx rc`) binnen 10 s → bereit; `TRCV` mit `ADHOC := TRUE` liest CONNACK/PINGRESP; PINGREQ nach `keepAlive/2` ohne Senden, keine PINGRESP binnen `keepAlive` → neu verbinden; Fehler → `TDISCON`, 5 s warten, neu verbinden; `enable` FALSE → DISCONNECT, `TDISCON`. Trigger: `R_TRIG`; bei Flanke Abbild `#snap := #daten`, `RD_SYS_T` einmal, Teile 1…n nacheinander senden (je Zyklus höchstens ein `TSEND`-Auftrag), danach `done` einen Zyklus, `sentCount + 1`. Flanke während `busy` → eine gemerkte Wiederholung danach; Flanke ohne Verbindung → `error` einen Zyklus, Status 16#8007, kein Puffern. Heartbeat über `TON`, nur zwischen zwei Triggern, `heartbeatCount + 1`.
- Statuscodes (in `LIESMICH.txt` und Handbuch): 16#7000 aus, 16#7001 verbinde, 16#7002 warte auf Broker, 16#7003 bereit, 16#7004 sende, 16#8001 TCON-Fehler, 16#8002 Sendefehler, 16#8003 Empfangsfehler, 16#81xx Broker lehnt ab (xx = Rückgabecode, 05 = Benutzer/Kennwort falsch), 16#8005 keine Antwort vom Broker, 16#8006 Verbindung eingeschlafen (keine PINGRESP), 16#8007 nicht verbunden beim Trigger, 16#8008 Nachricht zu lang, 16#8009 Uhrzeit nicht lesbar.
- Alle Anwenderwerte nur über `SCLString`/`SCLName` in die Vorlage (`template.FuncMap{"sclstr", "sclname"}`); Kennwort ohne `IncludePassword` → `PasswordPlaceholder`. `Generate` prüft: Topic per Aufrufer gültig, `standalone` mit `UseTLS` → Fehler „Die eigenständige Variante kann kein TLS – bitte LMQTT wählen“, `standalone` mit Host, der keine IPv4-Adresse ist → „Die eigenständige Variante braucht die IPv4-Adresse des Brokers“.

- [ ] **Step 1: Tests:** `TestSCLStringEscapes` (`it's` → `'it$'s'`, `$` → `'$$'`, `ä` → `'$C3$A4'`, Zeilenumbruch → `'$0A'`, 255 Byte bei max 254 → Fehler); `TestGenerateStandaloneGolden` (`Daten.db`, feste Optionen, Vergleich mit `testdata/golden/standalone/*`, `-update` schreibt neu); `TestGenerateNoRawUserText` (Topic `a'; END_FUNCTION_BLOCK`, Kennwort `x'y$z`, Client-ID mit `"` → erscheinen nur maskiert, Datei hat genau ein `END_FUNCTION_BLOCK` je FB); `TestGeneratePasswordPlaceholder` (ohne `IncludePassword` kein Kennwort im ZIP, Platzhalter da); `TestStandalonePacketConstants` (SCL enthält CONNECT-Kopf `16#10`, `'MQTT'` mit Level `16#04`, Flags `16#02`/`16#80`/`16#40`, PUBLISH `16#30` und Retain `16#01`, `16#C0`, `16#E0`, Restlänge mit `MOD 128` und `OR 16#80`); `TestStandaloneHeartbeatNoRetain` (Heartbeat-Senden nutzt festes `16#30` ohne Retain-Zweig); `TestGenerateRejects` (TLS bei `standalone`, Hostname bei `standalone`); `TestZipContents` (Dateinamen und Reihenfolge, BOM, CRLF); `TestConnectPacketDecodes` und `TestPublishPacketDecodes` (mit `github.com/mochi-mqtt/server/v2/packets` zurücklesen: Client-ID, Benutzer, Kennwort, Keep-Alive, Topic, Inhalt, Retain; Restlänge 127/128/16383/16384); `TestMqttRefAgainstBroker` (`mqttbroker.Manager` auf freiem Port mit Benutzer/Kennwort: rohe CONNECT-Bytes per `net.Dial` → CONNACK `20 02 00 00`; falsches Kennwort → Rückgabecode ≠ 0; PUBLISH von zwei mit `Render` erzeugten Teilen kommt über `SubscribeTopic` an; PINGREQ → `D0 00`).
- [ ] **Step 2:** `go test ./internal/modules/tiamqtt/ -run 'TestSCL|TestGenerate|TestStandalone|TestZip|Packet|MqttRef' -v` → FAIL.
- [ ] **Step 3:** Umsetzen; Golden-Dateien mit `-update` erzeugen und von Hand gegenlesen (SCL-Syntax, Reihenfolge der Bausteine).
- [ ] **Step 4:** → PASS; `go vet ./internal/modules/tiamqtt/`.
- [ ] **Step 5:** Commit `TIA-Baustein: SCL-Quellen für die eigenständige Variante mit MQTT 3.1.1, Trigger und Heartbeat`.

### Task 5: Variante mit Siemens-Bibliothek LMQTT

**Files:** Create `internal/modules/tiamqtt/templates/lmqtt.tmpl`, `testdata/golden/lmqtt/*`. Modify `generate.go`, `generate_test.go`

**Interfaces:**
- Consumes: `Generate`, `Options`, gemeinsame Vorlage `common.tmpl` (Task 4).
- Produces: `Generate` mit `VariantLMQTT`; `const LMQTTInfoURL = "https://support.industry.siemens.com/cs/ww/de/view/109748872"` (vor dem Einbau im Siemens-Portal prüfen, Beitrag „LMQTT“).
- Hülle um `LMQTT_Client` aus der Bibliothek (Namen und Parameter nach der Bibliotheksdoku der beim Anwender eingesetzten Version; im Kopf der Datei und in `LIESMICH.txt` genannt): gleiche Schnittstelle, gleiche Statuscodes (Bibliotheksfehler unter 16#82xx mit dem Bibliotheksstatus in `statusText`), gleicher JSON-Aufbau über dieselben `PDH_J_*`-FCs, Ablauf Trigger/Teile/Heartbeat wie Task 4. Hostname erlaubt; TLS über `useTls`, Port-Vorgabe 8883; kein Puffer-DB mit Standardzugriff.
- `LIESMICH.txt` beginnt mit „Vorher die Bibliothek LMQTT ins Projekt holen“ und `LMQTTInfoURL`.

- [ ] **Step 1: Tests:** `TestGenerateLMQTTGolden`; `TestLMQTTUsesLibrary` (FB ruft `LMQTT_Client` auf, kein `TCON`/`TSEND`; `useTls := TRUE` und Port 8883 bei `UseTLS`); `TestLMQTTReadmeLink`; `TestVariantsSameInterface` (VAR_INPUT/VAR_OUTPUT/VAR_IN_OUT beider FBs gleich).
- [ ] **Step 2:** → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Commit `TIA-Baustein: Variante mit Siemens-Bibliothek LMQTT und TLS`.

### Task 6: Seite „TIA-Baustein herunterladen“

**Files:** Create `internal/web/tia_block.go`, `tia_block_test.go`, `web/templates/tia_block.gohtml`, `widgets/tia_block_check.gohtml`. Modify `handler.go` (Routen), `connection_detail.go` (Werkzeug `connTool{base + "tia", "ti-cpu", "TIA-Baustein herunterladen", "SCL-Baustein für Siemens-Steuerungen: Werte per Trigger, Heartbeat"}` für `kind == "mqtt"` nur mit `import.tia`, in `connectionDetailPage` an `connectionTools` angehängt), `import_mappings.go` (`importMappingCreateInput` um `HeartbeatSeconds int` → Spalte `heartbeat_seconds`, 0 = NULL), `web/i18n/*.json` (neue Texte, falls über die Übersetzung geführt)

**Interfaces:**
- Consumes: `ParseDBSource`, `Plan`, `Generate`, `Zip`, `ZipName`, `Options`, `Layout`, Fehlertypen (Task 2–5); `h.connectionRow`, `h.openConnJSON`, `mqttValidPublishTopic`, `mqttSourceRef`, `h.canConnectionWrite`, `h.reconcileMqttConsumer`, `h.addHistory`.
- Produces:
```go
const permImportTIA = "import.tia"
type tiaForm struct {
    Variant, Family, BrokerHost, ClientID, Username, Topic, InfraID string
    BrokerPort, HeartbeatSeconds, KeepAliveSeconds, HwID, ConnID, Limit int
    UseTLS, Retain, IncludePassword, CreateMappings bool
}
func (h *Handler) tiaCan(r *http.Request, perm, id string) bool                 // wie efficioCan, Datensatztyp "import_connection"
func tiaDefaults(config map[string]string, connName, reqHost string) tiaForm
func tiaFormFromRequest(r *http.Request) (tiaForm, []string)                     // Fehler in Anwendersprache
func tiaMappingRows(l *tiamqtt.Layout, dbName, topic string, heartbeat int) []importMappingCreateInput // source_ref, name "<DB>.<Pfad>", heartbeat_seconds nur am Heartbeat
func (h *Handler) TIABlockPage(w http.ResponseWriter, r *http.Request)
func (h *Handler) TIABlockCheckWeb(w http.ResponseWriter, r *http.Request)
func (h *Handler) TIABlockDownload(w http.ResponseWriter, r *http.Request)
```

| Route | Recht |
|---|---|
| `GET /import/connections/{id}/tia` (`BaseData.Page` `"import"`) | `import.tia`; nur `kind = mqtt`, sonst 404 |
| `POST /import/connections/{id}/tia/check` (multipart, htmx-Fragment: erkannte Werte je Teil, übersprungene mit Zeile und Grund) | `import.tia` |
| `POST /import/connections/{id}/tia/download` (`application/zip`, `Content-Disposition` mit `ZipName`, `Cache-Control: no-store`, `nosniff`) | `import.tia`; mit Kennwort oder Zuordnungen zusätzlich `import.write` |

- Vorbelegung (`tiaDefaults`): integriert → Host aus `reqHost` (wie `MqttBrokerPage.SuggestedHost`, bei Namen die erste IPv4 aus `net.LookupIP`), Port `int_listen_port`, Benutzer `int_broker_username`, TLS wenn `int_tls_cert_path` und `int_tls_key_path` gesetzt (dann Variante `lmqtt` vorgewählt); extern → `ext_broker_host`, `ext_broker_port` (leer 1883), `ext_username`, `ext_use_tls`. Client-ID `pdh-sps-<DB>` (nach dem Hochladen), Topic `sps/<Verbindungsname klein, nur a-z0-9->/<DB>`, Heartbeat 60, Keep-Alive 60, `hwId` 64, `connId` 1, Grenze 4096, Familie S7-1500, Variante `standalone`.
- Kennwort mit `IncludePassword` aus `int_broker_password` bzw. `ext_password` (entschlüsselt über `openConnJSON`); Kennwort wird nie auf der Seite angezeigt. Download mit Kennwort und angelegte Zuordnungen kommen in den Verlauf (`addHistory(ctx, "connection", id, "tia_download", …, "TIA-Baustein heruntergeladen (mit Kennwort)")`).
- Zuordnungen (`CreateMappings`, Anlage Pflicht per `infra-tree-picker`): in einer Transaktion je Wert `INSERT … SELECT … WHERE NOT EXISTS (… connection_id = $1 AND source_ref = $2)`, Heartbeat-Zuordnung `mqttSourceRef(topic+"/heartbeat", "count")` mit `heartbeat_seconds` (bei Heartbeat 0 keine); danach `reconcileMqttConsumer`. Fehler beim Anlegen → kein ZIP, Meldung auf der Seite.
- Formularfehler (fehlende Datei, `*ParseError`, `Plan`-/`Generate`-Fehler, ungültiges Topic, Port, Grenze) → Seite erneut mit Meldung und Eingaben, HTTP 400, kein ZIP.

- [ ] **Step 1: Tests:** `TestTIARequiresPermission` (ohne `import.tia` GET → Weiterleitung, POST → 403; mit `import.tia`, ohne `import.write`: Download ohne Kennwort geht, mit `include_password` oder `create_mappings` → 403); `TestTIANotForOtherKinds` (OPC-UA-Verbindung → 404); `TestTIADefaultsIntegrated` / `TestTIADefaultsExternal` (Werte wie oben, Kennwort nie in `tiaForm`); `TestTIAOptionsRejects` (Topic mit `#`, Port 0, Grenze 512, `standalone` + TLS-Broker → Meldung „bitte LMQTT wählen“); `TestTIACheckFragment` (übersprungene Variable mit Zeile und Grund, Anzahl Teile); `TestTIADownloadZip` (`PDH_TEST_DSN`: ZIP öffnet sich, enthält `2_PDH_MQTT_Daten.scl`, kein Kennwort ohne Ankreuzen, Kennwort der Verbindung mit Ankreuzen, Verlaufseintrag); `TestTIADownloadCreatesMappings` (Zuordnungen `sps/x/Daten#values.Motor.Drehzahl` …, Heartbeat-Zuordnung mit `heartbeat_seconds = 60`, Name `Daten.Motor.Drehzahl`); `TestTIADownloadMappingsNoDuplicates` (zweimal → gleiche Anzahl, vorhandene Zuordnung mit anderem Namen unverändert); `TestTIAEndToEndIntegratedBroker` (`PDH_TEST_DSN`: integrierte Verbindung auf freiem Port, Zuordnungen angelegt, zwei mit `tiamqtt.Render` erzeugte Teile und ein Heartbeat per `tiamqtt.ConnectPacket`/`PublishPacket` über rohes TCP → `import_mappings.last_value` aller Werte aus beiden Teilen gesetzt, Heartbeat-Zuordnung hat `count`); dazu `TestHelpCoversAllPages`, `TestPagesUseBaseData`, `TestI18nCatalogsComplete`.
- [ ] **Step 2:** `go test ./internal/web/ -run 'TIA|Help|I18n|PagesUseBaseData' -v` → FAIL.
- [ ] **Step 3:** Umsetzen. Seite: Formular mit Abschnitten Steuerung (Variante, Familie, `hwId`, `connId`), Broker (Host, Port, TLS nur bei LMQTT, Client-ID, Benutzer, „Kennwort mitliefern“ nur mit `import.write`), Nachricht (Topic, Heartbeat, Keep-Alive, Retain, Grenze), Datenbaustein (Datei `.db`, Knopf „Prüfen“ per `hx-post` mit `hx-encoding="multipart/form-data"`), Zuordnungen (Ankreuzen + Anlage, nur mit `import.write`), Knopf „Herunterladen“ (normales Absenden). Farben nur über Variablen.
- [ ] **Step 4:** → PASS; `go build ./...`.
- [ ] **Step 5:** Im Browser (Labor-PDH): `.db` hochladen, prüfen, beide Varianten herunterladen, Zuordnungen anlegen; Fiori und Fluent, hell und dunkel, 390 und 1366 px.
- [ ] **Step 6:** Commit `TIA-Baustein: Seite zum Herunterladen an der MQTT-Verbindung, Zuordnungen auf Wunsch`.

### Task 7: Anzeige „Steuerung meldet sich nicht“

**Files:** Modify `internal/web/import_mappings.go` (`ImportMappingView` um `HeartbeatSeconds int`, `Silent bool`; Abfrage), `connection_detail.go` (`ConnectionDetailData.SilentPLCs []string`), `mappings_overview.go` (`ImportMappingOverviewView.Silent`), `web/templates/connection_detail.gohtml` (Hinweisleiste oben, Kennzeichen in der Zuordnungszeile), `web/templates/import_mappings_overview.gohtml` (Kennzeichen). Create `internal/web/heartbeat_test.go`

**Interfaces:**
- Consumes: Spalte `heartbeat_seconds` (Task 1), Heartbeat-Zuordnungen (Task 6).
- Produces: SQL-Ausdruck `heartbeatSilentSQL = "m.heartbeat_seconds IS NOT NULL AND GREATEST(COALESCE(m.last_received_at, m.created_at), m.created_at) < NOW() - make_interval(secs => 3 * m.heartbeat_seconds)"` (in beiden Abfragen genutzt); Text „Steuerung meldet sich nicht“ mit Zeitpunkt des letzten Heartbeats („seit 15.11.2026 10:04“ bzw. „noch kein Heartbeat“).

- [ ] **Step 1: Tests:** `TestHeartbeatSilent` (`PDH_TEST_DSN`, `mappingFixture`: Intervall 60, letzter Wert vor 170 s → nicht still; vor 181 s → still; nie empfangen, angelegt vor 10 s → nicht still; angelegt vor 200 s → still; ohne `heartbeat_seconds` nie still); `TestConnectionPageShowsSilentPLC` (Hinweisleiste mit Name der Zuordnung, Kennzeichen in der Zeile); `TestMappingsOverviewSilent`.
- [ ] **Step 2:** `go test ./internal/web/ -run 'Heartbeat|SilentPLC|MappingsOverviewSilent' -v` → FAIL.
- [ ] **Step 3:** Umsetzen.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5:** Im Browser: Heartbeat-Zuordnung ohne Werte, nach 3 Intervallen Hinweis sichtbar; Fiori/Fluent, hell/dunkel.
- [ ] **Step 6:** Commit `TIA-Baustein: Verbindung zeigt „Steuerung meldet sich nicht“, wenn der Heartbeat ausbleibt`.

### Task 8: Handbuch, Version, Prüfung vor dem Merge

**Files:** Modify `web/templates/help.gohtml` (neues Kapitel `<section class="hb-ch" id="tiabaustein" data-title="Steuerung per MQTT anbinden (TIA Portal)" data-pages="import">` nach `connections`, mit Bildschirmfoto-Bereich `(index $s "tiabaustein")`; Kapitel `connections`: Verweis; Kapitel `admin`: Recht `import.tia`), `widgets/help_figures.gohtml` (Skizze Steuerung → Broker → PDH-Zuordnungen, Heartbeat-Pfeil), `internal/web/help.go` (`helpChapterIDs`: `"tiabaustein"` nach `"connections"`), `CHANGELOG.md`, `VERSION`

- [ ] **Step 1:** Kapitel schreiben, `.hb-howto`/`.hb-steps`: Datenbaustein in TIA anlegen und Quelle erzeugen („Quelle aus Baustein generieren“, mit abhängigen Bausteinen bei PLC-Datentypen) → im PDH an der MQTT-Verbindung „TIA-Baustein herunterladen“, hochladen, prüfen → ZIP entpacken → in TIA unter „Externe Quellen“ die Dateien 1–4 in dieser Reihenfolge hinzufügen und „Bausteine aus Quelle generieren“ → eigenen DB durch Datei 4 ersetzen (Retain-Hinweis) → Kennwort im Konfig-DB eintragen, falls Platzhalter → FB in OB1 aufrufen, `daten := "<DB>"`, `hwId` der PROFINET-Schnittstelle aus der Gerätekonfiguration → laden → im PDH Zuordnungen und Heartbeat prüfen. Dazu: Unterschied der Varianten, LMQTT holen (Link), Statuscode-Tabelle, Teilnachrichten, Abschnitt „Steuerung meldet sich nicht“, PLCSIM-Hinweis aus dem Entwurf.
- [ ] **Step 2:** `VERSION` → `0.80.13`; `CHANGELOG.md` `## [0.80.13] – 2026-11-15`: `### Neu` (TIA-Baustein an MQTT-Verbindungen herunterladen, zwei Varianten; Werte aus eigenem Datenbaustein, große Datenmengen in mehreren Nachrichten; Zuordnungen auf Wunsch gleich anlegen; Anzeige „Steuerung meldet sich nicht“) und `### Sicherheit` (neues Recht `import.tia`; Kennwort nur auf ausdrücklichen Wunsch und mit Bearbeiten-Recht in der Datei, im Verlauf festgehalten).
- [ ] **Step 3:** `go test ./internal/web/ -run 'Help|TestChangelogMatchesVersion' -v` → PASS.
- [ ] **Step 4:** `go vet ./...`, `go build ./...`, `PDH_TEST_DSN='postgres://pdh:lab-pdh-test@127.0.0.1:54330/pdh_test?sslmode=disable' go test ./...` (Git Bash) → grün.
- [ ] **Step 5:** Prüfung vor dem Merge nach `AGENTS.md`: `git fetch origin main`, mergen; Migrationsnummer 162 frei; `VERSION` über `origin/main`; Dopplungen per `grep` nach `import.tia`, `/tia`, `heartbeat_seconds`, `tiamqtt`, `LMQTT`; danach Step 4 erneut.
- [ ] **Step 6:** Commit `TIA-Baustein: Handbuch, Änderungsprotokoll 0.80.13`.
- [ ] **Step 7: Anhalten** – Push, Testverbund und Merge nur mit ausdrücklicher Freigabe; Befehle vollständig nennen (Rechner, erwartetes Ergebnis).

---

## Abnahme durch den Anwender (keine Aufgabe)

1. Mit PLCSIM zuerst einen einfachen `TCON`-Test machen: Kommt aus der simulierten CPU eine TCP-Verbindung ins Netz? Wenn nicht, für die Netzprüfung PLCSIM Advanced oder eine echte CPU nehmen.
2. In jedem Fall in PLCSIM: Dateien 1–4 importieren, übersetzen ohne Fehler (S7-1500 und S7-1200), Aufruf in OB1, `statusText` lesen.
3. Mit Netz: CONNECT angenommen (Status 16#7003), Trigger → JSON im MQTT-Explorer des PDH, bei mehreren Teilen gleiches `ts`, `part`/`parts` richtig; Real-Format (`VAL_STRG`, Exponent, Punkt) und `LEN` bei `TSEND` prüfen; Heartbeat alle 60 s; Steuerung stoppen → nach 3 Minuten „Steuerung meldet sich nicht“.
4. Variante LMQTT mit TLS gegen den Broker der Verbindung.
