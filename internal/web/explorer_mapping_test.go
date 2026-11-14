package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/mqttbroker"
	"pdh/internal/core/mqttimport"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParseModbusReadingOrder(t *testing.T) {
	for ref, want := range map[string]modbusReading{
		"holding:100:float32":      {"holding", 100, "float32", "abcd"},
		"holding:100:float32:cdab": {"holding", 100, "float32", "cdab"},
		"input:7:int64:DCBA":       {"input", 7, "int64", "dcba"},
		"coil:5:bool":              {"coil", 5, "bool", "abcd"},
	} {
		got, err := parseModbusReading(ref)
		if err != nil || got != want {
			t.Errorf("%q → %+v, %v", ref, got, err)
		}
	}
	for _, bad := range []string{"holding:1:float32:wxyz", "holding:1:float32:abcd:x", "holding:1", "holding:x:float32", "holding:70000:uint16"} {
		if _, err := parseModbusReading(bad); err == nil {
			t.Errorf("%q angenommen", bad)
		}
	}
	if (modbusReading{"holding", 100, "float32", "abcd"}).SourceRef() != "holding:100:float32" {
		t.Error("abcd darf nicht ausgeschrieben werden")
	}
	if (modbusReading{RegisterType: "holding", Address: 100, DataType: "float32"}).SourceRef() != "holding:100:float32" {
		t.Error("leere Reihenfolge gilt als abcd")
	}
	if (modbusReading{"holding", 100, "float32", "cdab"}).SourceRef() != "holding:100:float32:cdab" {
		t.Error("Reihenfolge fehlt")
	}
}

func TestMqttMappingValue(t *testing.T) {
	p := []byte(`{"data":{"temp":21.5},"werte":[1,2]}`)
	for _, c := range []struct {
		ref, topic, want string
		ok               bool
	}{
		{"halle/1", "halle/1", string(p), true},
		{"halle/1#data.temp", "halle/1", "21.5", true},
		{"halle/1#werte[1]", "halle/1", "2", true},
		{"halle/1#fehlt", "halle/1", "", false},
		{"halle/2#data.temp", "halle/1", "", false},
		{"halle/2", "halle/1", "", false},
	} {
		got, ok := mqttMappingValue(c.ref, c.topic, p)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %q,%v", c.ref, got, ok)
		}
	}
	if _, ok := mqttMappingValue("halle/1#a", "halle/1", []byte("kein json")); ok {
		t.Error("Pfad auf Nicht-JSON")
	}
	// Ohne Pfad: wie bisher der Inhalt, auf 2000 Bytes gekürzt, auch wenn er kein JSON ist.
	long := strings.Repeat("x", 2500)
	if got, ok := mqttMappingValue("t", "t", []byte(long)); !ok || len(got) != 2000 {
		t.Errorf("Kürzung: %d, %v", len(got), ok)
	}
	if got, ok := mqttMappingValue("t", "t", []byte("kein json")); !ok || got != "kein json" {
		t.Errorf("Klartext: %q, %v", got, ok)
	}
	// Wert unter dem Pfad ist ein Objekt: als JSON-Text.
	if got, ok := mqttMappingValue("t#data", "t", p); !ok || got != `{"temp":21.5}` {
		t.Errorf("Objekt: %q, %v", got, ok)
	}
}

func TestMqttMappingTopics(t *testing.T) {
	got := mqttMappingTopics([]string{"halle/1", "halle/1#data.temp", "halle/1#data.druck", "halle/2", "q:abc:col", "halle/2"})
	want := []string{"halle/1", "halle/2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Themen %v, erwartet %v", got, want)
	}
}

// ── Modbus gegen den Testserver ──────────────────────────────

func TestReadModbusValueOrder(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) {
		s.Holding[20], s.Holding[21] = 0xE979, 0x42F6
		s.Holding[30], s.Holding[31] = 0x42F6, 0xE979
		// float64 1,5 = 3FF8 0000 0000 0000, in dcba: Register umgekehrt und Bytes getauscht
		s.Holding[40], s.Holding[41], s.Holding[42], s.Holding[43] = 0x0000, 0x0000, 0x0000, 0xF83F
		s.Input[5] = 0xFFFE
	})
	c := testModbusClient(t, s, 1)
	for _, tc := range []struct{ ref, want string }{
		{"holding:20:float32:cdab", "123.456"},
		{"holding:30:float32", "123.456"},        // alte Schreibweise: unverändert
		{"holding:30:float32:abcd", "123.456"},   // ausdrücklich abcd
		{"holding:20:uint16", "59769"},           // alte Schreibweise, 16 Bit
		{"holding:40:float64:dcba", "1.5"},       // 64 Bit, vier Register
		{"input:5:int16", "-2"},                  // alt
		{"holding:30:uint32:CDAB", "3917038326"}, // Reihenfolge klein/groß
	} {
		r, err := parseModbusReading(tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		got, err := readModbusValue(c, r)
		if err != nil || got != tc.want {
			t.Errorf("%s → %q, %v; erwartet %q", tc.ref, got, err, tc.want)
		}
	}
}

func TestWriteModbusValueOrder(t *testing.T) {
	s := startTestModbusServer(t)
	c := testModbusClient(t, s, 1)
	for _, tc := range []struct {
		ref, value string
		want       []uint16
	}{
		{"holding:30:float64:cdab", "1,5", []uint16{0, 0, 0, 0x3FF8}},
		{"holding:40:float32", "123.456", []uint16{0x42F6, 0xE979}},      // alt: Standard abcd
		{"holding:50:float32:cdab", "123.456", []uint16{0xE979, 0x42F6}}, // neu
		{"holding:60:uint16", "65535", []uint16{0xFFFF}},
		{"holding:70:int64:abcd", "-2", []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFE}},
	} {
		r, err := parseModbusReading(tc.ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeModbusValue(c, r, tc.value); err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		a := int(r.Address)
		s.Set(func(s *testModbusServer) {
			if got := s.Holding[a : a+len(tc.want)]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s: Register %#x, erwartet %#x", tc.ref, got, tc.want)
			}
		})
	}
	// Zurücklesen mit derselben Zuordnung ergibt den geschriebenen Wert.
	r, _ := parseModbusReading("holding:30:float64:cdab")
	if got, err := readModbusValue(c, r); err != nil || got != "1.5" {
		t.Errorf("Rücklesen %q, %v", got, err)
	}
	if err := writeModbusValue(c, modbusReading{"holding", 1, "float32", "wxyz"}, "1"); err == nil {
		t.Error("unbekannte Reihenfolge geschrieben")
	}
	if err := writeModbusValue(c, modbusReading{"holding", 1, "uint16", "abcd"}, "70000"); err == nil {
		t.Error("Wert außerhalb des Bereichs geschrieben")
	}
}

func TestModbusRegisterRowsRef(t *testing.T) {
	rows := modbusRegisterRows("holding", []uint16{0x42F6, 0xE979, 0, 0}, 100)
	n := 0
	for _, row := range rows {
		for _, in := range row.Interps {
			r, err := parseModbusReading(in.Ref)
			if err != nil {
				t.Fatalf("%q: %v", in.Ref, err)
			}
			if r.SourceRef() != in.Ref || r.Address != row.Address || r.DataType != in.Type || r.order() != in.Order {
				t.Errorf("Ref %q passt nicht zu %+v", in.Ref, in)
			}
			n++
		}
	}
	if n == 0 {
		t.Fatal("keine Deutungen")
	}
}

func TestModbusDataTypesComplete(t *testing.T) {
	var got []string
	for _, o := range modbusDataTypes {
		got = append(got, o.Value)
		if strings.Contains(o.Label, "big-endian") {
			t.Errorf("Bezeichnung %q enthält big-endian", o.Label)
		}
	}
	want := append([]string(nil), modbusCodecTypes...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Datentypen %v, erwartet %v", got, want)
	}
}

// ── Alte Werkzeugseiten ──────────────────────────────────────

func TestExplorerReplacesOldTools(t *testing.T) {
	for _, kind := range []string{"mqtt", "modbus", "opcua"} {
		found := false
		for _, tl := range connectionTools("import", kind, "x", map[string]string{"broker_mode": "integrated"}) {
			if tl.Href == "/import/connections/x?tab=explorer" {
				found = true
			}
			for _, old := range []string{"/read", "/nodes", "/sniffer"} {
				if strings.HasSuffix(tl.Href, old) {
					t.Errorf("%s: altes Werkzeug %q noch da", kind, tl.Href)
				}
			}
		}
		if !found {
			t.Errorf("%s: Eintrag Explorer fehlt", kind)
		}
	}
	for _, tl := range connectionTools("import", "excel", "x", nil) {
		if strings.Contains(tl.Href, "explorer") {
			t.Error("Excel hat keinen Explorer")
		}
	}
	for _, kind := range []string{"modbus", "opcua"} {
		found := false
		for _, tl := range connectionTools("export", kind, "x", nil) {
			found = found || tl.Href == "/export/connections/x?tab=explorer"
		}
		if !found {
			t.Errorf("Export %s: Eintrag Explorer fehlt", kind)
		}
	}
	for _, tl := range connectionTools("export", "mqtt", "x", nil) {
		if strings.Contains(tl.Href, "explorer") {
			t.Error("Export-MQTT hat keinen Explorer")
		}
	}

	id := uuid.NewString()
	h := &Handler{}
	r := chi.NewRouter()
	h.registerLegacyExplorerRedirects(r)
	for _, p := range []string{"sniffer", "nodes", "read"} {
		req := httptest.NewRequest(http.MethodGet, "/import/connections/"+id+"/"+p, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/import/connections/"+id+"?tab=explorer" {
			t.Errorf("/%s: %d → %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/import/connections/kaputt/read", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("ungültige ID: %d", w.Code)
	}
}

func TestImportConnectionDetailPathExplorer(t *testing.T) {
	for kind, want := range map[string]string{"mqtt": "?tab=explorer", "modbus": "?tab=explorer", "opcua": "?tab=explorer", "excel": "/preview", "csv": "/preview"} {
		if got := importConnectionDetailPath(kind); got != want {
			t.Errorf("%s: %q, erwartet %q", kind, got, want)
		}
	}
}

func TestConnectionDetailExplorerMapping(t *testing.T) {
	tmpl := loadTestTemplates(t)
	const id = "11111111-1111-1111-1111-111111111111"
	imp := ConnectionDetailData{
		Direction: "import", ID: id, Name: "Broker", Kind: "mqtt", KindLabel: "MQTT",
		Enabled: true, CanWrite: true, CanExecute: true, Tab: "explorer", ExplorerTopic: "#",
		Explorer: true, ExplorerWrite: true,
	}
	page := renderPage(t, tmpl, "connection_detail", imp)
	for _, want := range []string{"importMarkOpen", "pdh:mapping-created", `id="mark-panel"`, "Als Zuordnung übernehmen", "Zuordnung angelegt"} {
		if !strings.Contains(page, want) {
			t.Errorf("Import-Explorer enthält %q nicht", want)
		}
	}
	checkScripts(t, "explorer-mapping-import", page)

	imp.ExplorerWrite = false
	page = renderPage(t, tmpl, "connection_detail", imp)
	if strings.Contains(page, `id="mark-panel"`) {
		t.Error("Zuordnungs-Panel ohne Schreibrecht sichtbar")
	}
	checkScripts(t, "explorer-mapping-import-lesen", page)

	exp := ConnectionDetailData{
		Direction: "export", ID: id, Name: "SPS", Kind: "modbus", KindLabel: "Modbus TCP",
		Enabled: true, CanWrite: true, CanExecute: true, Tab: "explorer", ExplorerUnit: 1,
		Explorer: true, ExplorerWrite: true,
		ExportSources: []ImportMappingView{{ID: "m1", Name: "Öltemperatur", InfrastructureName: "Presse 3"}},
	}
	page = renderPage(t, tmpl, "connection_detail", exp)
	for _, want := range []string{"Als Zielfeld übernehmen", "Öltemperatur", "Presse 3", `id="ex-target-panel"`, "/export/connections/"} {
		if !strings.Contains(page, want) {
			t.Errorf("Export-Explorer enthält %q nicht", want)
		}
	}
	checkScripts(t, "explorer-mapping-export", page)
}

// ── MQTT-Übernahme: bestehende Zuordnungen laufen unverändert weiter ─────

func mappingTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// mappingFixture legt Verbindung, Anlage und Zuordnungen an (refs → Zuordnungs-IDs).
func mappingFixture(t *testing.T, pool *pgxpool.Pool, config string, refs ...string) (conn string, ids map[string]string) {
	t.Helper()
	ctx := context.Background()
	sfx := uuid.NewString()[:8]
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var infra string
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'line') RETURNING id::text`, "Zuordnung "+sfx).Scan(&infra))
	must(pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('import', 'mqtt', $1, $2::jsonb) RETURNING id::text`,
		"MQTT "+sfx, config).Scan(&conn))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM import_export_connections WHERE id = $1::uuid`, conn)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
	})
	ids = map[string]string{}
	for i, ref := range refs {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name) VALUES ($1::uuid, $2, $3::uuid, $4) RETURNING id::text`,
			conn, ref, infra, "Wert "+sfx+"-"+string(rune('a'+i))).Scan(&id))
		ids[ref] = id
	}
	return conn, ids
}

func lastValue(t *testing.T, pool *pgxpool.Pool, id string) (string, bool) {
	t.Helper()
	var v *string
	if err := pool.QueryRow(context.Background(), `SELECT last_value FROM import_mappings WHERE id = $1::uuid`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return "", false
	}
	return *v, true
}

func TestRecordMqttValueOldAndNew(t *testing.T) {
	pool := mappingTestPool(t)
	h := &Handler{db: pool}
	conn, ids := mappingFixture(t, pool, `{}`, "halle/1", "halle/1#data.temp", "halle/1#fehlt", "halle/10", "halle/10#data.temp", "halle/2#data.temp")

	h.recordMqttValue(conn, "halle/1", []byte(`{"data":{"temp":21.5}}`))
	for ref, want := range map[string]string{
		"halle/1":           `{"data":{"temp":21.5}}`, // alte Zuordnung: Inhalt wie bisher
		"halle/1#data.temp": "21.5",
	} {
		if got, ok := lastValue(t, pool, ids[ref]); !ok || got != want {
			t.Errorf("%s: %q, %v; erwartet %q", ref, got, ok, want)
		}
	}
	// Nicht betroffen: Pfad fehlt (Wert bleibt leer), anderes Thema mit gleichem Anfang, anderes Thema.
	for _, ref := range []string{"halle/1#fehlt", "halle/10", "halle/10#data.temp", "halle/2#data.temp"} {
		if got, ok := lastValue(t, pool, ids[ref]); ok {
			t.Errorf("%s: unerwartet %q", ref, got)
		}
	}
	// Fehlender Pfad bei späterer Nachricht lässt den alten Wert stehen.
	h.recordMqttValue(conn, "halle/1", []byte(`{"data":{"other":1}}`))
	if got, _ := lastValue(t, pool, ids["halle/1#data.temp"]); got != "21.5" {
		t.Errorf("Wert überschrieben: %q", got)
	}
	// Alte Zuordnung ohne JSON: Klartext wird übernommen.
	h.recordMqttValue(conn, "halle/10", []byte("42"))
	if got, _ := lastValue(t, pool, ids["halle/10"]); got != "42" {
		t.Errorf("Klartext: %q", got)
	}
	if got, ok := lastValue(t, pool, ids["halle/10#data.temp"]); ok {
		t.Errorf("Pfad auf Klartext hat geschrieben: %q", got)
	}
}

// Ganzer Weg über den eingebetteten Broker: reconcileMqttConsumer abonniert, der Broker liefert,
// die Zuordnungen (alt und neu) bekommen ihre Werte.
func TestReconcileMqttConsumerOldAndNew(t *testing.T) {
	pool := mappingTestPool(t)
	srv, host, port := startTestBroker(t)
	cfg := `{"broker_mode":"external","ext_broker_host":"` + host + `","ext_broker_port":"` + port + `"}`
	conn, ids := mappingFixture(t, pool, cfg, "halle/1", "halle/1#data.temp", "halle/1#werte[1]", "halle/3")
	h := &Handler{db: pool, mqttImport: mqttimport.NewManager(), mqttBrokers: mqttbroker.NewManager()}
	t.Cleanup(func() { h.mqttImport.Stop(conn) })

	h.reconcileMqttConsumer(conn)
	deadline := time.Now().Add(5 * time.Second)
	for !h.mqttImport.IsRunning(conn) {
		if time.Now().After(deadline) {
			t.Fatal("Abonnement läuft nicht")
		}
		time.Sleep(50 * time.Millisecond)
	}
	payload := `{"data":{"temp":21.5},"werte":[1,2]}`
	if err := srv.Publish("halle/1", []byte(payload), false, 0); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{"halle/1": payload, "halle/1#data.temp": "21.5", "halle/1#werte[1]": "2"} {
		var got string
		var ok bool
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if got, ok = lastValue(t, pool, ids[ref]); ok {
				break
			}
		}
		if !ok || got != want {
			t.Errorf("%s: %q, %v; erwartet %q", ref, got, ok, want)
		}
	}
	if got, ok := lastValue(t, pool, ids["halle/3"]); ok {
		t.Errorf("anderes Thema bekam %q", got)
	}
}
