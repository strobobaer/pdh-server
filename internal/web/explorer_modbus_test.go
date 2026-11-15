package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/simonvetter/modbus"
)

func testModbusClient(t *testing.T, s *testModbusServer, unit byte) *modbus.ModbusClient {
	t.Helper()
	host, port, _ := net.SplitHostPort(s.Addr)
	c, err := openModbusClientUnit(map[string]string{"host": host, "port": port}, unit)
	if err != nil {
		t.Fatalf("Verbindung zum Testserver: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestParseModbusRange(t *testing.T) {
	cases := []struct {
		q    string
		want modbusRange
		bad  bool
	}{
		{q: "", want: modbusRange{Area: "holding", Start: 0, Count: 10, Unit: 7}},
		{q: "area=input&start=100&count=125&unit=3", want: modbusRange{Area: "input", Start: 100, Count: 125, Unit: 3}},
		{q: "area=holding&count=126", bad: true},
		{q: "area=input&count=126", bad: true},
		{q: "area=coil&count=2000", want: modbusRange{Area: "coil", Start: 0, Count: 2000, Unit: 7}},
		{q: "area=discrete&count=2001", bad: true},
		{q: "start=65530&count=10", bad: true},
		{q: "start=65530&count=6", want: modbusRange{Area: "holding", Start: 65530, Count: 6, Unit: 7}},
		{q: "start=65536", bad: true},
		{q: "start=-1", bad: true},
		{q: "count=0", bad: true},
		{q: "count=abc", bad: true},
		{q: "unit=256", bad: true},
		{q: "unit=0", want: modbusRange{Area: "holding", Count: 10, Unit: 0}},
		{q: "unit=255", want: modbusRange{Area: "holding", Count: 10, Unit: 255}},
		{q: "area=register", bad: true},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.q)
		got, err := parseModbusRange(q, 7)
		if c.bad {
			if err == nil {
				t.Errorf("%q: kein Fehler (%+v)", c.q, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q = %+v, %v; erwartet %+v", c.q, got, err, c.want)
		}
	}
}

func TestReadModbusRangeServer(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) {
		s.Holding[100], s.Holding[101] = 0x42F6, 0xE979
		s.Input[7] = 0xFFFF
		s.Coils[5] = true
		s.Discrete[2] = true
	})
	c := testModbusClient(t, s, 1)
	res, err := readModbusRange(c, modbusRange{Area: "holding", Start: 100, Count: 2, Unit: 1})
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("holding: %+v, %v", res, err)
	}
	var f32, i16 string
	for _, in := range res.Rows[0].Interps {
		if in.Type == "float32" && in.Order == "abcd" {
			f32 = in.Value
		}
	}
	for _, in := range res.Rows[0].Interps {
		if in.Type == "int16" && in.Order == "abcd" {
			i16 = in.Value
		}
	}
	if f32 != "123.456" || i16 != "17142" || res.Rows[0].Address != 100 {
		t.Errorf("Zeile 0: float32=%q int16=%q %+v", f32, i16, res.Rows[0])
	}
	res, err = readModbusRange(c, modbusRange{Area: "input", Start: 7, Count: 1})
	if err != nil || len(res.Rows) != 1 || res.Rows[0].Hex != "FFFF" {
		t.Errorf("input: %+v, %v", res, err)
	}
	res, err = readModbusRange(c, modbusRange{Area: "coil", Start: 0, Count: 10})
	if err != nil || len(res.Bits) != 10 || !res.Bits[5].On || res.Bits[4].On || res.Bits[5].Address != 5 || res.Bits[5].Ref != "coil:5:bool" {
		t.Errorf("coil: %+v, %v", res, err)
	}
	res, err = readModbusRange(c, modbusRange{Area: "discrete", Start: 0, Count: 4})
	if err != nil || len(res.Bits) != 4 || !res.Bits[2].On {
		t.Errorf("discrete: %+v, %v", res, err)
	}
}

// Größere Bereiche gehen in Blöcken zu je höchstens 125 Registern bzw. 2000 Bits.
func TestReadModbusRangeBlocks(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) {
		for i := range s.Holding {
			s.Holding[i] = uint16(i)
		}
	})
	c := testModbusClient(t, s, 1)
	res, err := readModbusRange(c, modbusRange{Area: "holding", Start: 0, Count: 300})
	if err != nil || len(res.Rows) != 300 {
		t.Fatalf("300 Register: %d Zeilen, %v", len(res.Rows), err)
	}
	for _, i := range []int{0, 124, 125, 250, 299} {
		if res.Rows[i].Address != uint16(i) {
			t.Errorf("Zeile %d hat Adresse %d", i, res.Rows[i].Address)
		}
	}
	if s.Requests != 3 {
		t.Errorf("%d Aufträge statt 3 Blöcke", s.Requests)
	}
}

func TestWriteModbusExplorer(t *testing.T) {
	s := startTestModbusServer(t)
	c := testModbusClient(t, s, 1)
	if err := writeModbusExplorer(c, modbusWriteInput{Area: "holding", Address: 10, Type: "float32", Order: "cdab", Value: "21,5", Unit: 1}); err != nil {
		t.Fatal(err)
	}
	want, _ := encodeModbusValue("21.5", "float32", "cdab")
	s.Set(func(s *testModbusServer) {
		if got := []uint16{s.Holding[10], s.Holding[11]}; !reflect.DeepEqual(got, want) {
			t.Errorf("Register %v, erwartet %v", got, want)
		}
	})
	// ein Register: Funktion 6
	if err := writeModbusExplorer(c, modbusWriteInput{Area: "holding", Address: 20, Type: "int16", Order: "abcd", Value: "-2", Unit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeModbusExplorer(c, modbusWriteInput{Area: "holding", Address: 30, Type: "uint64", Order: "dcba", Value: "72623859790382856", Unit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeModbusExplorer(c, modbusWriteInput{Area: "coil", Address: 3, Value: "true", Unit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeModbusExplorer(c, modbusWriteInput{Area: "coil", Address: 4, Value: "1", Unit: 1}); err != nil {
		t.Fatal(err)
	}
	s.Set(func(s *testModbusServer) {
		if s.Holding[20] != 0xFFFE {
			t.Errorf("int16 -2: %#x", s.Holding[20])
		}
		if got := s.Holding[30:34]; !reflect.DeepEqual(got, []uint16{0x0807, 0x0605, 0x0403, 0x0201}) {
			t.Errorf("uint64 dcba: %#x", got)
		}
		if !s.Coils[3] || !s.Coils[4] {
			t.Error("Coils nicht gesetzt")
		}
	})
	if err := writeModbusExplorer(c, modbusWriteInput{Area: "coil", Address: 3, Value: "false", Unit: 1}); err != nil {
		t.Fatal(err)
	}
	s.Set(func(s *testModbusServer) {
		if s.Coils[3] {
			t.Error("Coil 3 nicht gelöscht")
		}
	})
	// abgelehnt: kein Zugriff auf das Gerät nötig
	for _, in := range []modbusWriteInput{
		{Area: "input", Address: 1, Type: "uint16", Order: "abcd", Value: "1"},
		{Area: "discrete", Address: 1, Value: "1"},
		{Area: "holding", Address: 1, Type: "uint16", Order: "abcd", Value: "70000"},
		{Area: "holding", Address: 1, Type: "float32", Order: "abcd", Value: "NaN"},
		{Area: "holding", Address: 1, Type: "float64", Order: "abcd", Value: "-Inf"},
		{Area: "holding", Address: 1, Type: "int16", Order: "abcd", Value: "1,5"},
		{Area: "holding", Address: 65535, Type: "uint32", Order: "abcd", Value: "1"},
		{Area: "coil", Address: 1, Value: "vielleicht"},
	} {
		err := writeModbusExplorer(c, in)
		var rej *opcuaRejected
		if err == nil || !asRejected(err, &rej) {
			t.Errorf("%+v: %v statt Ablehnung", in, err)
		}
	}
	if err := writeModbusExplorer(c, modbusWriteInput{Area: "input", Address: 1, Value: "1"}); err == nil || !strings.Contains(err.Error(), "Nur Coils und Holding Register sind beschreibbar") {
		t.Errorf("Meldung: %v", err)
	}
	// Adresse außerhalb des Geräts: Gerätefehler, keine Ablehnung
	err := writeModbusExplorer(c, modbusWriteInput{Area: "holding", Address: 5000, Type: "uint16", Order: "abcd", Value: "1", Unit: 1})
	var rej *opcuaRejected
	if err == nil || asRejected(err, &rej) {
		t.Errorf("Gerätefehler erwartet: %v", err)
	}
}

func asRejected(err error, target **opcuaRejected) bool {
	r, ok := err.(*opcuaRejected)
	*target = r
	return ok
}

func modbusTestHandler(addr string, allow map[string]bool) *Handler {
	host, port, _ := net.SplitHostPort(addr)
	id := "22222222-2222-2222-2222-222222222222"
	return &Handler{
		hasPermHook: func(_ *http.Request, p string) bool { return allow[p] },
		explorerLoadHook: func(_ *http.Request, dir string) (explorerConn, error) {
			return explorerConn{Direction: dir, ID: id, Name: "Presse", Kind: "modbus",
				Config: map[string]string{"host": host, "port": port, "unit_id": "5"}}, nil
		},
	}
}

func TestModbusRangeHandler(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.Holding[100], s.Holding[101] = 0x42F6, 0xE979 })
	// ohne Recht
	h := modbusTestHandler(s.Addr, map[string]bool{})
	w := httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?start=100&count=2"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ohne Recht: %d", w.Code)
	}
	h = modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	w = httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?start=100&count=2"))
	var res ModbusRangeResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || len(res.Rows) != 2 || res.Rows[0].Address != 100 {
		t.Fatalf("Bereich: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"ref"`) || strings.Contains(w.Body.String(), `"order":"cdab"`) {
		t.Errorf("Quellwert bzw. fremde Reihenfolge in der Antwort: %s", w.Body.String())
	}
	s.Set(func(s *testModbusServer) {
		if s.LastUnit != 5 {
			t.Errorf("Einheit %d statt der Vorgabe 5", s.LastUnit)
		}
	})
	w = httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?start=100&count=2&unit=9"))
	s.Set(func(s *testModbusServer) {
		if s.LastUnit != 9 {
			t.Errorf("Einheit %d statt 9", s.LastUnit)
		}
	})
	for _, q := range []string{"count=126", "start=65530&count=10", "unit=256", "area=x"} {
		w = httptest.NewRecorder()
		h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?"+q))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	// falscher Verbindungstyp: 404
	h.explorerLoadHook = func(_ *http.Request, dir string) (explorerConn, error) {
		return explorerConn{Direction: dir, ID: "x", Kind: "opcua"}, nil
	}
	w = httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x"))
	if w.Code != http.StatusNotFound {
		t.Errorf("Typ opcua: %d", w.Code)
	}
}

// Review Focus 3: nicht erreichbares Gerät – 502 mit Text, schnell.
func TestModbusRangeUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	h := modbusTestHandler(addr, map[string]bool{"import.execute": true})
	start := time.Now()
	w := httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x"))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "Verbindung fehlgeschlagen") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("dauerte %v", d)
	}
}

// Review Focus 4: der Gerätefehler nennt den gelesenen Bereich.
func TestModbusRangeHandlerDeviceError(t *testing.T) {
	s := startTestModbusServer(t)
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x?start=990&count=20"))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "990–1009") || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

// Höchstens zwei gleichzeitige Zugriffe je Verbindung.
func TestModbusRangeHandlerBusy(t *testing.T) {
	s := startTestModbusServer(t)
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	id := "22222222-2222-2222-2222-222222222222"
	r1, ok1 := opcuaConnGate.acquire(id)
	r2, ok2 := opcuaConnGate.acquire(id)
	if !ok1 || !ok2 {
		t.Fatal("Plätze nicht frei")
	}
	w := httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x"))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "Gerät ist gerade beschäftigt") {
		t.Errorf("belegt: %d %s", w.Code, w.Body.String())
	}
	r1()
	r2()
	w = httptest.NewRecorder()
	h.ModbusExplorerRange("import")(w, explorerReq("GET", "/x"))
	if w.Code != http.StatusOK {
		t.Errorf("frei: %d %s", w.Code, w.Body.String())
	}
}

func TestModbusWatchStream(t *testing.T) {
	s := startTestModbusServer(t)
	s.Set(func(s *testModbusServer) { s.Holding[100] = 0x1234 })
	old := explorerMaxDuration
	explorerMaxDuration = 1500 * time.Millisecond
	t.Cleanup(func() { explorerMaxDuration = old })
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.ModbusExplorerWatch("import")(w, explorerReq("GET", "/x?start=100&count=1&interval=0"))
	body := w.Body.String()
	for _, want := range []string{"event: connected", `"interval":1`, "event: range", `"hex":"1234"`, "event: end"} {
		if !strings.Contains(body, want) {
			t.Errorf("Antwort enthält %q nicht: %s", want, body)
		}
	}
	// Lesefehler: Warnung, Sitzung läuft weiter und liest im nächsten Takt wieder
	w = httptest.NewRecorder()
	h.ModbusExplorerWatch("import")(w, explorerReq("GET", "/x?start=2000&count=1&interval=1"))
	body = w.Body.String()
	if !strings.Contains(body, "event: warn") || !strings.Contains(body, "2000–2000") || strings.Count(body, "event: warn") < 2 || !strings.Contains(body, "event: end") {
		t.Errorf("Warnungen: %s", body)
	}
	// Eingabefehler vor dem Strom
	w = httptest.NewRecorder()
	h.ModbusExplorerWatch("import")(w, explorerReq("GET", "/x?count=500"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("count=500: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ModbusExplorerWatch("import")(w, explorerReq("GET", "/x?interval=abc"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("interval=abc: %d", w.Code)
	}
}

func modbusWriteReq(fields map[string]string) *http.Request {
	form := url.Values{"confirm": {"1"}}
	for k, v := range fields {
		form.Set(k, v)
	}
	r := httptest.NewRequest("POST", "/import/connections/x/explorer/modbus/write", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r.WithContext(explorerReq("POST", "/x").Context())
}

func TestModbusWriteHandlerGuards(t *testing.T) {
	pool := dataNetTestPool(t)
	s := startTestModbusServer(t)
	fields := map[string]string{"area": "holding", "address": "10", "type": "uint16", "order": "abcd", "value": "7"}
	// nur Leserecht: 403
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.ModbusExplorerWrite("import")(w, modbusWriteReq(fields))
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Schreibrecht: %d", w.Code)
	}
	h = modbusTestHandler(s.Addr, map[string]bool{"import.execute": true, "import.write": true})
	h.db = pool
	id := uuid.NewString()
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1`, id) })
	base := h.explorerLoadHook
	h.explorerLoadHook = func(r *http.Request, dir string) (explorerConn, error) {
		c, err := base(r, dir)
		c.ID = id
		return c, err
	}
	// ohne confirm: 400
	w = httptest.NewRecorder()
	noConfirm := map[string]string{"confirm": ""}
	for k, v := range fields {
		noConfirm[k] = v
	}
	h.ModbusExplorerWrite("import")(w, modbusWriteReq(noConfirm))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "bestätigen") {
		t.Errorf("ohne confirm: %d %s", w.Code, w.Body.String())
	}
	// Spiegel: 409
	h.mirror = MirrorInfo{On: true}
	w = httptest.NewRecorder()
	h.ModbusExplorerWrite("import")(w, modbusWriteReq(fields))
	if w.Code != http.StatusConflict {
		t.Errorf("Spiegel: %d", w.Code)
	}
	h.mirror = MirrorInfo{}
	// Eingabefehler: 400 und „Abgelehnt“ im Verlauf, ohne dass das Gerät etwas sieht
	cases := 0
	for name, f := range map[string]map[string]string{
		"Bereich input":    {"area": "input"},
		"Bereich discrete": {"area": "discrete"},
		"Adresse":          {"address": "70000"},
		"Adresse negativ":  {"address": "-1"},
		"Datentyp":         {"type": "int8"},
		"Reihenfolge":      {"order": "xyzw"},
		"Einheit":          {"unit": "300"},
		"Wert zu lang":     {"value": strings.Repeat("1", modbusExplorerMaxWrite+1)},
		"Wert ungültig":    {"value": "abc"},
		"Wert zu groß":     {"value": "70000"},
		"NaN":              {"type": "float32", "value": "NaN"},
		"Coil-Wert":        {"area": "coil", "value": "vielleicht"},
	} {
		f2 := map[string]string{}
		for k, v := range fields {
			f2[k] = v
		}
		for k, v := range f {
			f2[k] = v
		}
		w = httptest.NewRecorder()
		h.ModbusExplorerWrite("import")(w, noUserReq(modbusWriteReq(f2)))
		cases++
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	hist := h.recordHistory(context.Background(), "connection", id)
	if len(hist) != cases {
		t.Errorf("Verlauf: %d Einträge für %d Ablehnungen", len(hist), cases)
	}
	for _, e := range hist {
		if !strings.HasPrefix(e.NewValue, "Abgelehnt (") {
			t.Errorf("kein „Abgelehnt“: %+v", e)
		}
	}
	// Gerät beschäftigt: 429, nicht im Verlauf
	r1, _ := opcuaConnGate.acquire(id)
	r2, _ := opcuaConnGate.acquire(id)
	w = httptest.NewRecorder()
	h.ModbusExplorerWrite("import")(w, noUserReq(modbusWriteReq(fields)))
	r1()
	r2()
	if w.Code != http.StatusTooManyRequests || len(h.recordHistory(context.Background(), "connection", id)) != cases {
		t.Errorf("429: %d, Verlauf %d", w.Code, len(h.recordHistory(context.Background(), "connection", id)))
	}
	s.Set(func(s *testModbusServer) {
		if s.Requests != 0 {
			t.Errorf("Gerät wurde %d-mal angesprochen", s.Requests)
		}
	})
}

// Schreiben: Register gesetzt, Antwort mit Rücklesewert, Verlaufseintrag auch bei Ablehnung
// und Fehlschlag, mit Ergebnis, Datentyp und Reihenfolge.
func TestModbusWriteHandlerLogsResult(t *testing.T) {
	pool := dataNetTestPool(t)
	s := startTestModbusServer(t)
	h := modbusTestHandler(s.Addr, map[string]bool{"import.execute": true, "import.write": true})
	h.db = pool
	id := uuid.NewString()
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1`, id) })
	base := h.explorerLoadHook
	h.explorerLoadHook = func(r *http.Request, dir string) (explorerConn, error) {
		c, err := base(r, dir)
		c.ID = id
		return c, err
	}
	post := func(f map[string]string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ModbusExplorerWrite("import")(w, noUserReq(modbusWriteReq(f)))
		return w
	}
	w := post(map[string]string{"area": "holding", "address": "10", "type": "float32", "order": "cdab", "value": "21,5", "unit": "5"})
	var out struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.OK || out.Value != "21.5" {
		t.Fatalf("Schreiben: %d %s", w.Code, w.Body.String())
	}
	want, _ := encodeModbusValue("21.5", "float32", "cdab")
	s.Set(func(s *testModbusServer) {
		if got := []uint16{s.Holding[10], s.Holding[11]}; !reflect.DeepEqual(got, want) || s.LastUnit != 5 {
			t.Errorf("Register %v Einheit %d", got, s.LastUnit)
		}
	})
	if w = post(map[string]string{"area": "coil", "address": "4", "value": "true"}); w.Code != http.StatusOK {
		t.Fatalf("Coil: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"NaN", "70000", "1,5"} {
		typ := "uint16"
		if bad == "NaN" {
			typ = "float32"
		}
		if w = post(map[string]string{"area": "holding", "address": "10", "type": typ, "value": bad}); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	// Gerät lehnt ab: 502
	if w = post(map[string]string{"area": "holding", "address": "5000", "type": "uint16", "value": "1"}); w.Code != http.StatusBadGateway {
		t.Fatalf("Adresse 5000: %d %s", w.Code, w.Body.String())
	}
	// Andere Einheit als die der Verbindung (5) und Rundruf (0): 400, ohne dass das Gerät etwas sieht
	s.Set(func(s *testModbusServer) { s.Requests = 0 })
	w = post(map[string]string{"area": "holding", "address": "10", "type": "uint16", "value": "1", "unit": "3"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Einheit 3 statt der Einheit 5 der Verbindung") {
		t.Fatalf("andere Einheit: %d %s", w.Code, w.Body.String())
	}
	w = post(map[string]string{"area": "coil", "address": "4", "value": "true", "unit": "0"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Rundruf") {
		t.Fatalf("Einheit 0: %d %s", w.Code, w.Body.String())
	}
	s.Set(func(s *testModbusServer) {
		if s.Requests != 0 {
			t.Errorf("Gerät wurde %d-mal angesprochen", s.Requests)
		}
	})
	hist := h.recordHistory(context.Background(), "connection", id)
	if len(hist) != 8 {
		t.Fatalf("Verlauf: %+v", hist)
	}
	var okSeen, coilSeen, rejSeen, failSeen, unitSeen bool
	for _, e := range hist {
		switch {
		case e.FieldName == "holding:10:float32:cdab (Einheit 5)":
			okSeen = e.NewValue == "21,5 (float32, CDAB)"
		case e.FieldName == "coil:4:bool (Einheit 5)":
			coilSeen = e.NewValue == "true"
		case strings.HasPrefix(e.NewValue, "Abgelehnt (") && strings.Contains(e.NewValue, "70000") && strings.Contains(e.NewValue, "(uint16)"):
			rejSeen = true
		case strings.HasPrefix(e.NewValue, "Abgelehnt (Einheit 3 statt der Einheit 5 der Verbindung)"):
			unitSeen = true
		case e.FieldName == "holding:5000:uint16 (Einheit 5)":
			failSeen = strings.HasPrefix(e.NewValue, "Fehlgeschlagen (Das Gerät kennt diese Adresse nicht") && strings.Contains(e.NewValue, "– 1 (uint16, ABCD)")
		}
	}
	if !okSeen || !coilSeen || !rejSeen || !failSeen || !unitSeen {
		t.Errorf("Einträge ok=%v coil=%v abgelehnt=%v fehlgeschlagen=%v: %+v", okSeen, coilSeen, rejSeen, failSeen, hist)
	}
}

func TestConnectionDetailModbusExplorer(t *testing.T) {
	tmpl := loadTestTemplates(t)
	for _, dir := range []string{"import", "export"} {
		d := ConnectionDetailData{
			Direction: dir, ID: "11111111-1111-1111-1111-111111111111", Name: "Presse", Kind: "modbus", KindLabel: "Modbus TCP",
			Enabled: true, CanWrite: true, CanExecute: true, Tab: "explorer",
			Explorer: true, ExplorerWrite: true, ExplorerUnit: 7,
			Tools: connectionTools(dir, "modbus", "x", nil),
		}
		page := renderPage(t, tmpl, "connection_detail", d)
		for _, want := range []string{`id="xm-root"`, `id="xm-table"`, `const canWrite = true`, `value="7"`, "Das ändert die Maschine.", "ABCD", "CDAB", "BADC", "DCBA", "/explorer/modbus/"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: %q fehlt", dir, want)
			}
		}
		d.ExplorerWrite = false
		page = renderPage(t, tmpl, "connection_detail", d)
		if strings.Contains(page, `id="xm-write"`) || !strings.Contains(page, `const canWrite = false`) {
			t.Errorf("%s: Schreibformular ohne Recht", dir)
		}
	}
}

// Reiter „Zuordnungen“: „Werte jetzt aktualisieren“ nur mit Bearbeiten-Recht, nur bei Modbus/OPC UA
// im Import und nur, wenn es Zuordnungen gibt; jeder Typ ruft seine eigene Route.
func TestConnectionDetailRefreshButton(t *testing.T) {
	tmpl := loadTestTemplates(t)
	const id = "11111111-1111-1111-1111-111111111111"
	mk := func(dir, kind string, write bool, maps int) string {
		d := ConnectionDetailData{
			Direction: dir, ID: id, Name: "Presse", Kind: kind, KindLabel: kind, Enabled: true,
			CanWrite: write, CanExecute: true, Tab: "mappings", Tools: connectionTools(dir, kind, "x", nil),
		}
		for i := 0; i < maps; i++ {
			d.Mappings = append(d.Mappings, connMappingRow{})
		}
		return renderPage(t, tmpl, "connection_detail", d)
	}
	const label = "Werte jetzt aktualisieren"
	if p := mk("import", "modbus", true, 1); !strings.Contains(p, label) || !strings.Contains(p, `/import/connections/`+id+`/read/refresh`) {
		t.Error("Modbus: Knopf oder Route fehlt")
	}
	if p := mk("import", "opcua", true, 1); !strings.Contains(p, label) || !strings.Contains(p, `/import/connections/`+id+`/nodes/refresh`) {
		t.Error("OPC UA: Knopf oder Route fehlt")
	}
	for name, p := range map[string]string{
		"ohne Recht": mk("import", "modbus", false, 1), "MQTT": mk("import", "mqtt", true, 1), "ohne Zuordnung": mk("import", "opcua", true, 0),
	} {
		if strings.Contains(p, label) {
			t.Errorf("%s: Knopf darf nicht erscheinen", name)
		}
	}
}

// OPC UA mit Benutzer/Passwort: sichtbarer Hinweis auf fehlende Verschlüsselung; Modbus sperrt das Schreibformular bei abweichender Einheit.
func TestExplorerPlainAuthAndWriteLockMarkup(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ConnectionDetailData{
		Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "SPS", Kind: "opcua", KindLabel: "OPC UA",
		Enabled: true, CanWrite: true, CanExecute: true, Tab: "explorer", Explorer: true, ExplorerWrite: true,
		Tools: connectionTools("import", "opcua", "x", nil),
	}
	if p := renderPage(t, tmpl, "connection_detail", d); strings.Contains(p, "Sicherheitsrichtlinie None") && !strings.Contains(p, "id=\"xo-plain\"") {
		t.Error("Hinweis ohne Passwortanmeldung")
	}
	d.ExplorerPlainAuth = true
	if p := renderPage(t, tmpl, "connection_detail", d); !strings.Contains(p, "ohne Verschlüsselung (Sicherheitsrichtlinie None)") || !strings.Contains(p, "im Klartext übers Netz") {
		t.Error("OPC UA: Hinweis zur Verschlüsselung fehlt")
	}
	d.Kind, d.KindLabel, d.ExplorerUnit = "modbus", "Modbus TCP", 7
	d.Tools = connectionTools("import", "modbus", "x", nil)
	p := renderPage(t, tmpl, "connection_detail", d)
	for _, want := range []string{`id="xm-w-hint"`, "lockWrite", "auf Einheit ' + wantUnit"} {
		if !strings.Contains(p, want) {
			t.Errorf("Modbus: %q fehlt", want)
		}
	}
}
