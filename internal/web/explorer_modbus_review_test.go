package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func modbusCfg(s *testModbusServer) map[string]string {
	host, port, _ := net.SplitHostPort(s.Addr)
	return map[string]string{"host": host, "port": port}
}

// ── Export-Schreibpfad gegen den Testserver ──────────────────

func TestWriteModbusValueWire(t *testing.T) {
	s := startTestModbusServer(t)
	c := testModbusClient(t, s, 1)
	for _, tc := range []struct {
		reading modbusReading
		value   string
		want    []uint16
	}{
		{modbusReading{RegisterType: "holding", Address: 10, DataType: "uint32"}, "305419896", []uint16{0x1234, 0x5678}},
		{modbusReading{RegisterType: "holding", Address: 20, DataType: "int32"}, "-2", []uint16{0xFFFF, 0xFFFE}},
		{modbusReading{RegisterType: "holding", Address: 30, DataType: "float32"}, "123.456", []uint16{0x42F6, 0xE979}},
		{modbusReading{RegisterType: "holding", Address: 40, DataType: "uint16"}, "65535", []uint16{0xFFFF}},
		{modbusReading{RegisterType: "holding", Address: 50, DataType: "int16"}, "-2", []uint16{0xFFFE}},
	} {
		if err := writeModbusValue(c, tc.reading, tc.value); err != nil {
			t.Fatalf("%+v: %v", tc.reading, err)
		}
		a := int(tc.reading.Address)
		s.Set(func(s *testModbusServer) {
			if got := s.Holding[a : a+len(tc.want)]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s %s: Register %#x, erwartet %#x", tc.reading.DataType, tc.value, got, tc.want)
			}
			if s.Holding[a+len(tc.want)] != 0 {
				t.Errorf("%s: schreibt über das Ende hinaus", tc.reading.DataType)
			}
		})
	}
	if err := writeModbusValue(c, modbusReading{RegisterType: "coil", Address: 7}, "true"); err != nil {
		t.Fatal(err)
	}
	s.Set(func(s *testModbusServer) {
		if !s.Coils[7] {
			t.Error("Coil 7 nicht an")
		}
	})
	if err := writeModbusValue(c, modbusReading{RegisterType: "coil", Address: 7}, "false"); err != nil {
		t.Fatal(err)
	}
	s.Set(func(s *testModbusServer) {
		if s.Coils[7] {
			t.Error("Coil 7 nicht aus")
		}
	})
}

func TestWriteModbusExportWire(t *testing.T) {
	s := startTestModbusServer(t)
	err := writeModbusExport(modbusCfg(s), []ExportMappingView{
		{FieldName: "holding:1:uint32", SourceValue: "305419896"},
		{FieldName: "holding:3:float32", SourceValue: "123.456"},
		{FieldName: "coil:2:bool", SourceValue: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Set(func(s *testModbusServer) {
		if got := s.Holding[1:5]; !reflect.DeepEqual(got, []uint16{0x1234, 0x5678, 0x42F6, 0xE979}) || !s.Coils[2] {
			t.Errorf("Register %#x Coil %v", got, s.Coils[2])
		}
	})
	err = writeModbusExport(modbusCfg(s), []ExportMappingView{{FieldName: "holding:5000:uint16", SourceValue: "1"}})
	if err == nil || !strings.Contains(err.Error(), "holding:5000:uint16") {
		t.Errorf("Gerätefehler: %v", err)
	}
	if err := writeModbusExport(modbusCfg(s), []ExportMappingView{{FieldName: "kaputt", SourceValue: "1"}}); err == nil {
		t.Error("ungültiges Ziel ohne Fehler")
	}
}

func TestRunModbusQueryUint64(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.Holding[3], s.Holding[7] = 1, 2 })
	cfg := modbusCfg(s)
	res, err := runModbusQuery(cfg, map[string]string{"area": "holding", "start": "0", "count": "2", "type": "uint64"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Columns, []string{"holding:0:uint64", "holding:4:uint64"}) || !reflect.DeepEqual(res.Rows, [][]string{{"1", "2"}}) {
		t.Errorf("%+v", res)
	}
	s.Set(func(s *testModbusServer) { s.Coils[1] = true })
	res, err = runModbusQuery(cfg, map[string]string{"area": "coil", "start": "0", "count": "3"})
	if err != nil || !reflect.DeepEqual(res.Rows, [][]string{{"false", "true", "false"}}) {
		t.Errorf("coil: %+v %v", res, err)
	}
	if _, err := runModbusQuery(map[string]string{"host": "127.0.0.1", "port": "1"}, map[string]string{"count": "1"}); err == nil {
		t.Error("unerreichbar ohne Fehler")
	}
}

func TestCheckProtocolModbus(t *testing.T) {
	s := startTestModbusServer(t)
	h := &Handler{}
	rep := &checkReport{}
	h.checkProtocol(context.Background(), rep, "import", "modbus", modbusCfg(s), "x")
	if rep.OK != 1 || rep.Fail != 0 {
		t.Errorf("erreichbar: %+v", rep)
	}
	rep = &checkReport{}
	h.checkProtocol(context.Background(), rep, "import", "modbus", map[string]string{"host": "127.0.0.1", "port": "1"}, "x")
	if rep.Fail != 1 {
		t.Errorf("unerreichbar: %+v", rep)
	}
}

func TestModbusRefreshValuesWeb(t *testing.T) {
	pool := connTestPool(t)
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) {
		s.Holding[100], s.Holding[101] = 0x42F6, 0xE979
		s.Coils[5] = true
	})
	ctx := context.Background()
	cfg, _ := json.Marshal(modbusCfg(s))
	var connID, infra string
	if err := pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name, config)
		VALUES ('import', 'modbus', $1, $2) RETURNING id::text`, "zz-test-modbus-"+uuid.NewString(), cfg).Scan(&connID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('zz-modbus '||$1::text, 'plant') RETURNING id::text`, connID).Scan(&infra); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM import_mappings WHERE connection_id=$1`, connID)
		pool.Exec(ctx, `DELETE FROM import_export_connections WHERE id=$1`, connID)
		pool.Exec(ctx, `DELETE FROM infrastructure WHERE id=$1`, infra)
	})
	for i, ref := range []string{"holding:100:float32", "coil:5:bool", "holding:5000:uint16"} {
		if _, err := pool.Exec(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name) VALUES ($1,$2,$3,$4)`,
			connID, ref, infra, "m"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{db: pool, jwtSecret: "t", hasPermHook: func(_ *http.Request, p string) bool { return p == "import.write" }}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", connID)
	r := httptest.NewRequest("POST", "/import/connections/"+connID+"/read/refresh", nil)
	r = r.WithContext(context.WithValue(explorerReq("POST", "/x").Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.ModbusRefreshValuesWeb(w, r)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "2+von+3+Wert") {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Location"))
	}
	got := map[string]string{}
	rows, _ := pool.Query(ctx, `SELECT source_ref, COALESCE(last_value,'') FROM import_mappings WHERE connection_id=$1`, connID)
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		got[k] = v
	}
	rows.Close()
	if got["holding:100:float32"] != "123.456" || got["coil:5:bool"] != "true" || got["holding:5000:uint16"] != "" {
		t.Errorf("Werte: %v", got)
	}
}

// ── Datenmenge ───────────────────────────────────────────────

func TestModbusRangeOrderParam(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.Holding[100], s.Holding[101] = 0x42F6, 0xE979 })
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	get := func(q string) (int, ModbusRangeResult, string) {
		w := httptest.NewRecorder()
		h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?start=100&count=2&"+q))
		var res ModbusRangeResult
		json.Unmarshal(w.Body.Bytes(), &res)
		return w.Code, res, w.Body.String()
	}
	orders := func(res ModbusRangeResult) map[string]bool {
		m := map[string]bool{}
		for _, r := range res.Rows {
			for _, in := range r.Interps {
				m[in.Order] = true
				if in.Ref != "" {
					t.Errorf("Quellwert in der Antwort: %+v", in)
				}
			}
		}
		return m
	}
	code, res, _ := get("")
	if code != 200 || !reflect.DeepEqual(orders(res), map[string]bool{"abcd": true}) {
		t.Errorf("Standard: %d %v", code, orders(res))
	}
	code, res, _ = get("order=cdab")
	if code != 200 || !reflect.DeepEqual(orders(res), map[string]bool{"abcd": true, "cdab": true}) { // 16 Bit gilt als abcd
		t.Errorf("cdab: %d %v", code, orders(res))
	}
	for _, in := range res.Rows[0].Interps {
		if in.Type == "float32" && (in.Order != "cdab" || in.Value == "123.456") {
			t.Errorf("float32 cdab: %+v", in)
		}
	}
	code, res, _ = get("order=all")
	if code != 200 || len(orders(res)) != 4 {
		t.Errorf("all: %d %v", code, orders(res))
	}
	if code, _, _ = get("order=zzz"); code != http.StatusBadRequest {
		t.Errorf("zzz: %d", code)
	}
	// Einzelzeile für die Matrix: kurzer Aufruf, läuft über Guard und Gate
	hn := modbusTestHandler(s.Addr, map[string]bool{})
	w := httptest.NewRecorder()
	hn.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?start=100&count=2&order=all"))
	if w.Code != http.StatusForbidden {
		t.Errorf("Matrix ohne Recht: %d", w.Code)
	}
}

func TestModbusWatchOnlyOnChange(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.Holding[100] = 0x1234 })
	oldD, oldA := explorerMaxDuration, modbusAliveEvery
	// Lebenszeichen-Abstand deutlich unter dem Takt (1 s): lastSent wird nach dem Lesen gesetzt
	// (erstes Lesen samt Verbindungsaufbau), bei genau 1 s wäre „alive“ beim nächsten Takt Zufall.
	explorerMaxDuration, modbusAliveEvery = 3500*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { explorerMaxDuration, modbusAliveEvery = oldD, oldA })
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(1500 * time.Millisecond)
		s.Set(func(s *testModbusServer) { s.Holding[100] = 0x4321 })
	}()
	w := httptest.NewRecorder()
	h.ModbusExplorerWatch("import")(w, explorerReq("GET", "/x?start=100&count=1&interval=1&order=badc"))
	wg.Wait()
	body := w.Body.String()
	if n := strings.Count(body, "event: range"); n != 2 {
		t.Errorf("%d Takte „range“ statt 2 (erster und nach der Änderung): %s", n, body)
	}
	if !strings.Contains(body, "event: alive") || !strings.Contains(body, `"hex":"4321"`) || strings.Contains(body, `"ref"`) {
		t.Errorf("Lebenszeichen/Änderung: %s", body)
	}
	if w2 := httptest.NewRecorder(); true {
		h.ModbusExplorerWatch("import")(w2, explorerReq("GET", "/x?order=all"))
		if w2.Code != http.StatusBadRequest {
			t.Errorf("order=all im Watch: %d", w2.Code)
		}
	}
}

// ── Störungen ────────────────────────────────────────────────

// Abbruch mitten im Lesen: der Platz an der Verbindung wird frei (Wartezeit bis 5 s).
func TestModbusRangeCancelDuringRead(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.Stall = true })
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	ctx, cancel := context.WithCancel(explorerReq("GET", "/x").Context())
	req := httptest.NewRequest("GET", "/x?count=2", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ModbusExplorerRange("import")(w, req)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Handler hängt nach Abbruch")
	}
	id := "22222222-2222-2222-2222-222222222222"
	r1, ok1 := opcuaConnGate.acquire(id)
	r2, ok2 := opcuaConnGate.acquire(id)
	if !ok1 || !ok2 {
		t.Error("Platz an der Verbindung nicht freigegeben")
	}
	if ok1 {
		r1()
	}
	if ok2 {
		r2()
	}
	if w.Code != http.StatusBadGateway {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

// Der Server schließt die Verbindung mitten im Lesen: verständlicher Fehler, kein Hängen.
func TestModbusRangeServerClosesConnection(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.DropNext = 5 })
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	start := time.Now()
	w := httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?count=2"))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "Lesen von holding 0–1 fehlgeschlagen") ||
		!strings.Contains(w.Body.String(), "Das Gerät hat die Verbindung beendet") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("dauerte %v", d)
	}
}

// Wiederaufbau im Watch: nach einem Verbindungsabbruch kommt eine Warnung, dann wieder Werte.
func TestModbusWatchReconnects(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.Holding[100] = 0x1234; s.DropNext = 1 })
	old := explorerMaxDuration
	explorerMaxDuration = 2500 * time.Millisecond
	t.Cleanup(func() { explorerMaxDuration = old })
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.ModbusExplorerWatch("import")(w, explorerReq("GET", "/x?start=100&count=1&interval=1"))
	body := w.Body.String()
	warn, rng := strings.Index(body, "event: warn"), strings.Index(body, "event: range")
	if warn < 0 || rng < warn || !strings.Contains(body, `"hex":"1234"`) {
		t.Errorf("erst Warnung, dann Werte erwartet: %s", body)
	}
	s.Set(func(s *testModbusServer) {
		if s.Requests < 2 {
			t.Errorf("nur %d Anfragen", s.Requests)
		}
	})
}
