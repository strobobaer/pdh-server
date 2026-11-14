package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
	"time"

	"github.com/google/uuid"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

// startTestOPCUAServer startet einen echten gopcua-Server (ohne Sicherheit, anonym) mit
// dem Namensraum „PDHTest“: Variablen Temperatur (Double, schreibbar) und Zaehler
// (Int32, nur lesbar). Liefert Endpunkt, Server und Namensraumindex.
func startTestOPCUAServer(t *testing.T) (endpoint string, srv *server.Server, ns int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	srv = server.New(
		server.EndPoint("127.0.0.1", port),
		server.EnableSecurity("None", ua.MessageSecurityModeNone),
		server.EnableAuthMode(ua.UserTokenTypeAnonymous),
	)
	root, _ := srv.Namespace(0)
	rootObjects := root.Objects()
	nodeNS := server.NewNodeNameSpace(srv, "PDHTest")
	folder := nodeNS.Objects()
	rootObjects.AddRef(folder, id.HasComponent, true)
	add := func(name string, value any, dataType uint32, level ua.AccessLevelType) {
		n := server.NewVariableNode(ua.NewStringNodeID(nodeNS.ID(), name), name, value)
		n.SetAttribute(ua.AttributeIDDataType, server.DataValueFromValue(ua.NewNumericNodeID(0, dataType)))
		n.SetAttribute(ua.AttributeIDAccessLevel, server.DataValueFromValue(byte(level)))
		n.SetAttribute(ua.AttributeIDUserAccessLevel, server.DataValueFromValue(byte(level)))
		nodeNS.AddNode(n)
		folder.AddRef(n, id.HasComponent, true)
	}
	add("Temperatur", float64(21.5), id.Double, ua.AccessLevelTypeCurrentRead|ua.AccessLevelTypeCurrentWrite)
	add("Zaehler", int32(7), id.Int32, ua.AccessLevelTypeCurrentRead)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("OPC-UA-Testserver startet nicht: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return fmt.Sprintf("opc.tcp://127.0.0.1:%d", port), srv, int(nodeNS.ID())
}

func testOPCUAClient(t *testing.T, endpoint string) *opcua.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := openOPCUAClient(ctx, map[string]string{"endpoint_url": endpoint})
	if err != nil {
		t.Fatalf("Verbindung zum Testserver: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func TestOPCUABrowseChildren(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	c := testOPCUAClient(t, endpoint)
	ctx := context.Background()
	top, truncated, err := opcuaBrowseChildren(ctx, c, "", opcuaExplorerMaxChildren)
	if err != nil || truncated {
		t.Fatalf("Objects: %v, abgeschnitten %v", err, truncated)
	}
	var folder *OPCUANodeInfo
	for i := range top {
		if top[i].Name == "PDHTest" {
			folder = &top[i]
		}
	}
	if folder == nil || !folder.HasChildren || folder.Class != "Object" {
		t.Fatalf("Ordner des Namensraums fehlt: %+v", top)
	}
	kids, _, err := opcuaBrowseChildren(ctx, c, folder.NodeID, opcuaExplorerMaxChildren)
	if err != nil {
		t.Fatal(err)
	}
	var temp *OPCUANodeInfo
	for i := range kids {
		if kids[i].Name == "Temperatur" {
			temp = &kids[i]
		}
	}
	if temp == nil || temp.Class != "Variable" || temp.DataType != "Double" || temp.Value != "21.5" || temp.Status != "Good" {
		t.Fatalf("Temperatur: %+v", kids)
	}
	if temp.NodeID != fmt.Sprintf("ns=%d;s=Temperatur", ns) {
		t.Errorf("NodeID = %q", temp.NodeID)
	}
	one, truncated, err := opcuaBrowseChildren(ctx, c, folder.NodeID, 1)
	if err != nil || !truncated || len(one) != 1 {
		t.Fatalf("limit 1: %d Einträge, abgeschnitten %v, %v", len(one), truncated, err)
	}
}

func TestOPCUAReadNodeAccess(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	c := testOPCUAClient(t, endpoint)
	z, err := opcuaReadNode(context.Background(), c, fmt.Sprintf("ns=%d;s=Zaehler", ns))
	if err != nil || z.Access != "lesen" || z.Writable || z.Value != "7" || z.DataType != "Int32" {
		t.Fatalf("Zaehler: %+v %v", z, err)
	}
	tmp, err := opcuaReadNode(context.Background(), c, fmt.Sprintf("ns=%d;s=Temperatur", ns))
	if err != nil || tmp.Access != "lesen/schreiben" || !tmp.Writable {
		t.Fatalf("Temperatur: %+v %v", tmp, err)
	}
	if _, err := opcuaReadNode(context.Background(), c, "kein-knoten"); err == nil {
		t.Error("ungültige NodeID ohne Fehler")
	}
}

func TestOPCUAWriteValue(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	c := testOPCUAClient(t, endpoint)
	ctx := context.Background()
	node := fmt.Sprintf("ns=%d;s=Temperatur", ns)
	if err := opcuaWriteValue(ctx, c, node, "22,75"); err != nil {
		t.Fatal(err)
	}
	got, err := opcuaReadNode(ctx, c, node)
	if err != nil || got.Value != "22.75" {
		t.Fatalf("nach Schreiben: %+v %v", got, err)
	}
	if err := opcuaWriteValue(ctx, c, fmt.Sprintf("ns=%d;s=Zaehler", ns), "8"); err == nil {
		t.Error("Schreiben auf nur lesbaren Knoten ohne Fehler")
	}
	if err := opcuaWriteValue(ctx, c, node, "abc"); err == nil {
		t.Error("ungültige Zahl ohne Fehler")
	}
}

func TestOPCUAWatch(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	c := testOPCUAClient(t, endpoint)
	node := fmt.Sprintf("ns=%d;s=Temperatur", ns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var seen []string
	got := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- opcuaWatch(ctx, c, []string{node}, 200*time.Millisecond, func(v OPCUAWatchValue) {
			mu.Lock()
			seen = append(seen, v.Value)
			mu.Unlock()
			if v.Value == "23.5" {
				select {
				case got <- struct{}{}:
				default:
				}
			}
		})
	}()
	time.Sleep(600 * time.Millisecond) // Anfangswert abwarten
	if err := opcuaWriteValue(context.Background(), c, node, "23.5"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("23.5 nicht gemeldet, gesehen: %v", seen)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("opcuaWatch nach Abbruch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opcuaWatch kehrt nach Abbruch nicht zurück")
	}
}

// Ein Server ohne Subscription-Unterstützung: Rückfall auf zyklisches Lesen.
func TestOPCUAWatchPollingFallback(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	c := testOPCUAClient(t, endpoint)
	node := fmt.Sprintf("ns=%d;s=Temperatur", ns)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int
	err := opcuaWatchPolling(ctx, c, []*ua.NodeID{ua.MustParseNodeID(node)}, 100*time.Millisecond, func(v OPCUAWatchValue) {
		if v.Value == "21.5" {
			n++
		}
	})
	if err != nil || n < 2 {
		t.Fatalf("Polling: %d Werte, %v", n, err)
	}
}

func TestOPCUAFormatValue(t *testing.T) {
	long := strings.Repeat("ä", 500)
	cases := []struct {
		in   any
		want string
	}{
		{float64(21.5), "21.5"}, {float32(1.25), "1.25"}, {int32(7), "7"}, {true, "true"},
		{"text", "text"}, {[]byte{1, 0xab}, "0x01ab"}, {[]int32{1, 2}, "[1 2]"},
	}
	for _, c := range cases {
		v, err := ua.NewVariant(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := opcuaFormatValue(v); got != c.want {
			t.Errorf("%T: %q, erwartet %q", c.in, got, c.want)
		}
	}
	nan, _ := ua.NewVariant(float64(0) / zeroF())
	if got := opcuaFormatValue(nan); got != "NaN" {
		t.Errorf("NaN: %q", got)
	}
	lv, _ := ua.NewVariant(long)
	if got := opcuaFormatValue(lv); len([]rune(got)) != opcuaExplorerMaxValue+1 {
		t.Errorf("Länge %d", len([]rune(got)))
	}
	bad, _ := ua.NewVariant("a\xffb\x00c")
	if got := opcuaFormatValue(bad); !utf8.ValidString(got) || strings.ContainsRune(got, 0) {
		t.Errorf("ungültige Zeichen: %q", got)
	}
	if opcuaFormatValue(nil) != "" {
		t.Error("nil")
	}
}

func zeroF() float64 { return 0 }

func opcuaTestHandler(endpoint string, allow map[string]bool) *Handler {
	return &Handler{
		hasPermHook: func(_ *http.Request, p string) bool { return allow[p] },
		explorerLoadHook: func(_ *http.Request, dir string) (explorerConn, error) {
			return explorerConn{Direction: dir, ID: uuid.NewString(), Name: "Maschine", Kind: "opcua",
				Config: map[string]string{"endpoint_url": endpoint}}, nil
		},
	}
}

func TestOPCUAExplorerHandlers(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	// ohne Leserecht
	h := opcuaTestHandler(endpoint, map[string]bool{})
	w := httptest.NewRecorder()
	h.OPCUAExplorerChildren("import")(w, explorerReq("GET", "/x?node="))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ohne Recht: %d", w.Code)
	}
	h = opcuaTestHandler(endpoint, map[string]bool{"import.execute": true})
	w = httptest.NewRecorder()
	h.OPCUAExplorerChildren("import")(w, explorerReq("GET", "/x?node="))
	if w.Code != http.StatusOK {
		t.Fatalf("mit Recht: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Nodes     []OPCUANodeInfo `json:"nodes"`
		Truncated bool            `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Nodes) == 0 {
		t.Fatalf("Antwort: %s", w.Body.String())
	}
	// Einzelknoten
	w = httptest.NewRecorder()
	h.OPCUAExplorerNode("import")(w, explorerReq("GET", "/x?node="+url.QueryEscape(fmt.Sprintf("ns=%d;s=Temperatur", ns))))
	var info OPCUANodeInfo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.Value != "21.5" {
		t.Fatalf("node: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.OPCUAExplorerNode("import")(w, explorerReq("GET", "/x?node=kaputt"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("ungültige NodeID: %d", w.Code)
	}
	// Verbindungsfehler: 502 JSON
	bad := opcuaTestHandler("opc.tcp://127.0.0.1:1", map[string]bool{"import.execute": true})
	w = httptest.NewRecorder()
	bad.OPCUAExplorerChildren("import")(w, explorerReq("GET", "/x?node="))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("Verbindungsfehler: %d %s", w.Code, w.Body.String())
	}
	// 21 Knoten: 400
	q := url.Values{}
	for i := 0; i < 21; i++ {
		q.Add("node", fmt.Sprintf("ns=%d;s=K%d", ns, i))
	}
	w = httptest.NewRecorder()
	h.OPCUAExplorerWatch("import")(w, explorerReq("GET", "/x?"+q.Encode()))
	if w.Code != http.StatusBadRequest {
		t.Errorf("21 Knoten: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.OPCUAExplorerWatch("import")(w, explorerReq("GET", "/x"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("0 Knoten: %d", w.Code)
	}
}

func TestOPCUAExplorerWatchStream(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	old := explorerMaxDuration
	explorerMaxDuration = 2 * time.Second
	t.Cleanup(func() { explorerMaxDuration = old })
	h := opcuaTestHandler(endpoint, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.OPCUAExplorerWatch("import")(w, explorerReq("GET", "/x?interval=250&node="+url.QueryEscape(fmt.Sprintf("ns=%d;s=Temperatur", ns))))
	body := w.Body.String()
	for _, want := range []string{"event: connected", "event: value", `"value":"21.5"`, "event: end"} {
		if !strings.Contains(body, want) {
			t.Errorf("Antwort enthält %q nicht: %s", want, body)
		}
	}
}

func opcuaWriteReq(node, value string, extra url.Values) *http.Request {
	form := url.Values{"node": {node}, "value": {value}, "confirm": {"1"}}
	for k, v := range extra {
		form[k] = v
	}
	r := httptest.NewRequest("POST", "/import/connections/x/explorer/opcua/write", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r.WithContext(explorerReq("POST", "/x").Context())
}

func TestOPCUAExplorerWriteGuards(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	node := fmt.Sprintf("ns=%d;s=Temperatur", ns)
	// nur Leserecht: 403
	h := opcuaTestHandler(endpoint, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.OPCUAExplorerWrite("import")(w, opcuaWriteReq(node, "1", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Schreibrecht: %d", w.Code)
	}
	// ohne Bestätigung: 400
	h = opcuaTestHandler(endpoint, map[string]bool{"import.execute": true, "import.write": true})
	w = httptest.NewRecorder()
	r := opcuaWriteReq(node, "1", url.Values{"confirm": {""}})
	h.OPCUAExplorerWrite("import")(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("ohne Bestätigung: %d", w.Code)
	}
	// zu langer Wert: 400
	w = httptest.NewRecorder()
	h.OPCUAExplorerWrite("import")(w, opcuaWriteReq(node, strings.Repeat("1", opcuaExplorerMaxWrite+1), nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("zu langer Wert: %d", w.Code)
	}
}

// Schreiben: Antwort mit neuem Wert, Verlaufseintrag auch bei Fehlschlag, mit Datentyp.
func TestOPCUAExplorerWriteLogsResult(t *testing.T) {
	pool := dataNetTestPool(t)
	endpoint, _, ns := startTestOPCUAServer(t)
	allow := map[string]bool{"import.execute": true, "import.write": true}
	h := opcuaTestHandler(endpoint, allow)
	h.db = pool
	id := uuid.NewString()
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1`, id) })
	h.explorerLoadHook = func(_ *http.Request, dir string) (explorerConn, error) {
		return explorerConn{Direction: dir, ID: id, Name: "Maschine", Kind: "opcua",
			Config: map[string]string{"endpoint_url": endpoint}}, nil
	}
	temp := fmt.Sprintf("ns=%d;s=Temperatur", ns)
	w := httptest.NewRecorder()
	h.OPCUAExplorerWrite("import")(w, noUserReq(opcuaWriteReq(temp, "19,5", nil)))
	var info OPCUANodeInfo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.Value != "19.5" {
		t.Fatalf("Schreiben: %d %s", w.Code, w.Body.String())
	}
	zaehler := fmt.Sprintf("ns=%d;s=Zaehler", ns)
	w = httptest.NewRecorder()
	h.OPCUAExplorerWrite("import")(w, noUserReq(opcuaWriteReq(zaehler, "9", nil)))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("Fehlschlag: %d %s", w.Code, w.Body.String())
	}
	hist := h.recordHistory(context.Background(), "connection", id)
	if len(hist) != 2 {
		t.Fatalf("Verlauf: %+v", hist)
	}
	var okSeen, failSeen bool
	for _, e := range hist {
		switch e.FieldName {
		case temp:
			okSeen = strings.Contains(e.NewValue, "19,5") && strings.Contains(e.NewValue, "Double") && !strings.HasPrefix(e.NewValue, "Fehlgeschlagen")
		case zaehler:
			failSeen = strings.HasPrefix(e.NewValue, "Fehlgeschlagen (") && strings.Contains(e.NewValue, "Int32")
		}
	}
	if !okSeen || !failSeen {
		t.Errorf("Einträge: %+v", hist)
	}
}

func TestConnectionDetailOPCUAExplorer(t *testing.T) {
	tmpl := loadTestTemplates(t)
	for _, dir := range []string{"import", "export"} {
		d := ConnectionDetailData{
			Direction: dir, ID: "11111111-1111-1111-1111-111111111111", Name: "Maschine", Kind: "opcua", KindLabel: "OPC UA",
			Enabled: true, CanWrite: true, CanExecute: true, Tab: "explorer",
			Explorer: true, ExplorerWrite: true,
			Tools: connectionTools(dir, "opcua", "x", nil),
		}
		page := renderPage(t, tmpl, "connection_detail", d)
		for _, want := range []string{`id="xo-tree"`, `id="xo-watch-box"`, `const canWrite = true`, "Das ändert die Maschine."} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: OPC-UA-Explorer enthält %q nicht", dir, want)
			}
		}
		checkScripts(t, "verbindung-explorer-opcua-"+dir, page)
		d.ExplorerWrite = false
		page = renderPage(t, tmpl, "connection_detail", d)
		if !strings.Contains(page, `const canWrite = false`) {
			t.Errorf("%s: Schreibschalter ohne Recht nicht aus", dir)
		}
		checkScripts(t, "verbindung-explorer-opcua-lesen-"+dir, page)
	}
}
