package web

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-chi/chi/v5"
	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/google/uuid"

	"pdh/internal/core/users"
)

func TestMqttSourceRef(t *testing.T) {
	if mqttSourceRef("halle/1/temp", "") != "halle/1/temp" || mqttSourceRef("halle/1", "data.temp") != "halle/1#data.temp" {
		t.Fatal("Format")
	}
	for ref, want := range map[string][2]string{
		"halle/1/temp": {"halle/1/temp", ""}, "halle/1#data.temp": {"halle/1", "data.temp"},
		"sensors/#": {"sensors/#", ""}, "a/##werte[0]": {"a/#", "werte[0]"},
	} {
		if tp, p := parseMqttSourceRef(ref); tp != want[0] || p != want[1] {
			t.Errorf("%q → %q,%q", ref, tp, p)
		}
	}
}

func TestMqttValidFilter(t *testing.T) {
	for f, ok := range map[string]bool{"": true, "#": true, "a/+/c": true, "a/#": true, "a/#/b": false, "a+/b": false, "a/b#": false} {
		if (mqttValidFilter(f) == nil) != ok {
			t.Errorf("%q", f)
		}
	}
	if mqttValidFilter(strings.Repeat("a", 501)) == nil {
		t.Error("501 Zeichen zugelassen")
	}
}

func TestMqttTopicTable(t *testing.T) {
	tb := newMqttTopicTable(10, 3)
	now := time.Now()
	tb.add("a/temp", []byte("21.5"), true, 1, now)
	for _, v := range []string{"22", "23", "24"} {
		tb.add("a/temp", []byte(v), false, 0, now)
	}
	st, _ := tb.add("a/json", []byte(`{"temp":21.5,"arr":[1,2]}`), false, 0, now)
	got, _ := tb.add("a/temp", []byte("25"), false, 0, now)
	if got.Count != 5 || !got.Numeric || len(got.History) != 3 || got.History[2] != 25 || got.Last != "25" {
		t.Fatalf("%+v", got)
	}
	if len(st.Fields) != 3 || st.Fields[0].Path != "arr[0]" || st.Fields[2].Path != "temp" || st.Numeric {
		t.Fatalf("%+v", st.Fields)
	}
	if got.Ref != "a/temp" {
		t.Errorf("Ref = %q", got.Ref)
	}
	if ch := tb.takeChanged(); len(ch) != 2 {
		t.Errorf("geändert: %d", len(ch))
	}
	if ch := tb.takeChanged(); len(ch) != 0 {
		t.Errorf("zweiter Abruf: %d", len(ch))
	}
}

func TestMqttTopicTableCap(t *testing.T) {
	tb := newMqttTopicTable(2000, 60)
	now := time.Now()
	accepted := 0
	for i := 0; i < 5000; i++ {
		if _, ok := tb.add(fmt.Sprintf("t/%d", i), []byte("1"), false, 0, now); ok {
			accepted++
		}
	}
	if accepted != 2000 || tb.Dropped() != 3000 {
		t.Fatalf("angenommen %d, verworfen %d", accepted, tb.Dropped())
	}
	st, ok := tb.add("t/5", []byte("7"), false, 0, now)
	if !ok || st.Count != 2 || st.Last != "7" {
		t.Errorf("bekanntes Thema nicht aktualisiert: %+v %v", st, ok)
	}
	big, _ := tb.add("t/6", []byte(strings.Repeat("x", 5000)), false, 0, now)
	if !big.Truncated || len(big.Last) != 4000 {
		t.Errorf("Kürzung: %v %d", big.Truncated, len(big.Last))
	}
}

func TestMqttClientOptions(t *testing.T) {
	opts, broker, err := mqttClientOptions(map[string]string{"broker_mode": "integrated", "int_listen_port": "1884", "int_broker_username": "u", "int_broker_password": "p"}, "explorer")
	if err != nil || broker != "tcp://127.0.0.1:1884" || opts.Username != "u" || opts.Password != "p" {
		t.Fatalf("integriert: %v %q", err, broker)
	}
	if !regexp.MustCompile(`^pdh-explorer-[0-9a-f]{8}$`).MatchString(opts.ClientID) || opts.AutoReconnect {
		t.Errorf("ClientID %q, AutoReconnect %v", opts.ClientID, opts.AutoReconnect)
	}
	opts2, broker, err := mqttClientOptions(map[string]string{"ext_broker_host": "b.example", "ext_use_tls": "true", "ext_client_id": "werk"}, "explorer")
	if err != nil || broker != "ssl://b.example:1883" || !strings.HasPrefix(opts2.ClientID, "werk-explorer-") || opts2.TLSConfig == nil {
		t.Fatalf("extern: %v %q %q", err, broker, opts2.ClientID)
	}
	if opts2.ClientID == opts.ClientID {
		t.Error("Client-ID nicht zufällig")
	}
	if _, _, err := mqttClientOptions(map[string]string{}, "explorer"); err == nil {
		t.Error("ohne Host kein Fehler")
	}
	if _, _, err := mqttClientOptions(map[string]string{"broker_mode": "integrated"}, "explorer"); err == nil {
		t.Error("ohne Port kein Fehler")
	}
}

// startTestBroker startet einen eingebetteten Broker auf einem freien Port.
func startTestBroker(t *testing.T) (*mqttserver.Server, string, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	srv := mqttserver.New(&mqttserver.Options{InlineClient: true})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{Type: "tcp", ID: "t", Address: addr})); err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	host, port, _ := net.SplitHostPort(addr)
	return srv, host, port
}

func mqttTestHandler(host, port string, allow map[string]bool) *Handler {
	return &Handler{
		hasPermHook: func(_ *http.Request, p string) bool { return allow[p] },
		explorerLoadHook: func(_ *http.Request, dir string) (explorerConn, error) {
			return explorerConn{Direction: dir, ID: uuid.NewString(), Name: "Broker", Kind: "mqtt",
				Config: map[string]string{"broker_mode": "external", "ext_broker_host": host, "ext_broker_port": port}}, nil
		},
	}
}

func TestMqttExplorerStreamBroker(t *testing.T) {
	srv, host, port := startTestBroker(t)
	if err := srv.Publish("halle/1/temp", []byte("21.5"), true, 0); err != nil {
		t.Fatal(err)
	}
	old := explorerMaxDuration
	explorerMaxDuration = 2 * time.Second
	t.Cleanup(func() { explorerMaxDuration = old })
	h := mqttTestHandler(host, port, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.MqttExplorerStream(w, explorerReq("GET", "/import/connections/x/explorer/mqtt/stream?filter="+url.QueryEscape("halle/#")))
	body := w.Body.String()
	for _, want := range []string{"event: connected", "event: topics", `"topic":"halle/1/temp"`, `"retained":true`, `"filter":"halle/#"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Antwort enthält %q nicht: %s", want, body)
		}
	}
}

func TestMqttExplorerStreamBadFilter(t *testing.T) {
	h := mqttTestHandler("127.0.0.1", "1", map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.MqttExplorerStream(w, explorerReq("GET", "/x?filter="+url.QueryEscape("a/#/b")))
	if w.Code != http.StatusBadRequest {
		t.Errorf("Filter a/#/b: %d", w.Code)
	}
}

func publishReq(topic string, extra url.Values) *http.Request {
	form := url.Values{"topic": {topic}, "payload": {"42"}, "qos": {"0"}, "confirm": {"1"}}
	for k, v := range extra {
		form[k] = v
	}
	r := httptest.NewRequest("POST", "/import/connections/x/explorer/mqtt/publish", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded") // wie pdhExplorer.post
	return r.WithContext(explorerReq("POST", "/x").Context())
}

func TestMqttExplorerPublish(t *testing.T) {
	_, host, port := startTestBroker(t)
	got := make(chan string, 4)
	o := mqtt.NewClientOptions().AddBroker("tcp://" + host + ":" + port).SetClientID("test-sub")
	sub := mqtt.NewClient(o)
	if tok := sub.Connect(); !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
		t.Fatal("Test-Client verbindet nicht")
	}
	defer sub.Disconnect(100)
	if tok := sub.Subscribe("halle/test", 0, func(_ mqtt.Client, m mqtt.Message) { got <- string(m.Payload()) }); !tok.WaitTimeout(3*time.Second) || tok.Error() != nil {
		t.Fatal("Abo")
	}
	allow := map[string]bool{"import.execute": true, "import.write": true}
	h := mqttTestHandler(host, port, allow)

	w := httptest.NewRecorder()
	h.MqttExplorerPublish(w, publishReq("halle/test", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("Senden: %d %s", w.Code, w.Body.String())
	}
	select {
	case v := <-got:
		if v != "42" {
			t.Errorf("empfangen %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Nachricht nicht empfangen")
	}

	// Rückfrage nicht bestätigt: 400
	w = httptest.NewRecorder()
	r := publishReq("halle/test", url.Values{"confirm": {""}})
	h.MqttExplorerPublish(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("ohne Bestätigung: %d", w.Code)
	}
	// Platzhalter: 400
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, publishReq("halle/#", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("Platzhalter: %d", w.Code)
	}
	// zu großer Inhalt: 400
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, publishReq("halle/test", url.Values{"payload": {strings.Repeat("x", mqttExplorerMaxPublish+1)}}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("zu groß: %d", w.Code)
	}
	// ungültige QoS: 400
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, publishReq("halle/test", url.Values{"qos": {"3"}}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("QoS 3: %d", w.Code)
	}
	// ohne Schreibrecht: 403
	allow["import.write"] = false
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, publishReq("halle/test", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Schreibrecht: %d", w.Code)
	}
	// ohne Erkunden-Recht: 403
	allow["import.write"], allow["import.execute"] = true, false
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, publishReq("halle/test", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Erkunden-Recht: %d", w.Code)
	}
}

// noUserReq: Benutzer ohne Kennung, damit created_by in der Datenbank leer bleibt.
func noUserReq(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), "user", &users.User{}))
}

// Der Verlaufseintrag entsteht auch bei fehlgeschlagenem Senden, mit dem Ergebnis darin.
func TestMqttExplorerPublishLogsResult(t *testing.T) {
	pool := dataNetTestPool(t)
	srv, host, port := startTestBroker(t)
	_ = srv
	allow := map[string]bool{"import.execute": true, "import.write": true}
	h := mqttTestHandler(host, port, allow)
	h.db = pool
	id := uuid.NewString()
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1`, id) })
	load := h.explorerLoadHook
	h.explorerLoadHook = func(r *http.Request, dir string) (explorerConn, error) {
		c, err := load(r, dir)
		c.ID = id
		return c, err
	}
	w := httptest.NewRecorder()
	h.MqttExplorerPublish(w, noUserReq(publishReq("halle/log", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("Senden: %d %s", w.Code, w.Body.String())
	}
	// Broker nicht erreichbar
	h.explorerLoadHook = func(r *http.Request, dir string) (explorerConn, error) {
		return explorerConn{Direction: dir, ID: id, Name: "Broker", Kind: "mqtt",
			Config: map[string]string{"ext_broker_host": "127.0.0.1", "ext_broker_port": "1"}}, nil
	}
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, noUserReq(publishReq("halle/fehler", nil)))
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
		case "mqtt:halle/log":
			okSeen = e.NewValue == "QoS 0, ohne Retain: 42"
		case "mqtt:halle/fehler":
			failSeen = strings.HasPrefix(e.NewValue, "Fehlgeschlagen (") && strings.HasSuffix(e.NewValue, "QoS 0, ohne Retain: 42")
		}
	}
	if !okSeen || !failSeen {
		t.Errorf("Einträge: %+v", hist)
	}
}

// Jede Explorer-Route ohne Recht liefert 403. Neue Routen erscheinen hier von selbst.
func TestExplorerRoutesRequirePermission(t *testing.T) {
	h := &Handler{
		hasPermHook: func(*http.Request, string) bool { return false },
		explorerLoadHook: func(_ *http.Request, dir string) (explorerConn, error) {
			return explorerConn{Direction: dir, ID: "c1", Name: "X", Kind: "mqtt"}, nil
		},
	}
	re := regexp.MustCompile(`^/(import|export)/connections/\{id\}/explorer/`)
	n := 0
	walk := func(method, route string, handler http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !re.MatchString(route) {
			return nil
		}
		n++
		path := strings.Replace(route, "{id}", "11111111-1111-1111-1111-111111111111", 1)
		w := httptest.NewRecorder()
		r := explorerReq(method, path)
		if method == http.MethodPost {
			r = httptest.NewRequest(method, path, strings.NewReader("confirm=1&topic=a&payload=1"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r = r.WithContext(explorerReq(method, path).Context())
		}
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s ohne Recht: %d (erwartet 403)", method, route, w.Code)
		}
		return nil
	}
	if err := chi.Walk(h.Routes(), walk); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Errorf("nur %d Explorer-Routen gefunden", n)
	}
}

func TestConnectionDetailMqttExplorer(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ConnectionDetailData{
		Direction: "import", ID: "11111111-1111-1111-1111-111111111111", Name: "Broker", Kind: "mqtt", KindLabel: "MQTT",
		Enabled: true, CanWrite: true, CanExecute: true, Tab: "explorer", ExplorerTopic: "werk/#",
		Explorer: true, ExplorerWrite: true,
		Tools: connectionTools("import", "mqtt", "x", nil),
	}
	page := renderPage(t, tmpl, "connection_detail", d)
	for _, want := range []string{`id="xm-filter"`, `value="werk/#"`, `id="xm-tree"`, `data-explorer-write`, `id="xm-send"`, "Retain bleibt auf dem Broker gespeichert"} {
		if !strings.Contains(page, want) {
			t.Errorf("MQTT-Explorer enthält %q nicht", want)
		}
	}
	checkScripts(t, "verbindung-explorer-mqtt", page)
	d.ExplorerWrite = false
	page = renderPage(t, tmpl, "connection_detail", d)
	if strings.Contains(page, `id="xm-send"`) || strings.Contains(page, "data-explorer-write") {
		t.Error("Senden-Formular ohne Schreibrecht sichtbar")
	}
	checkScripts(t, "verbindung-explorer-mqtt-lesen", page)
}

func TestMqttTopicTableNonFinite(t *testing.T) {
	tb := newMqttTopicTable(10, 5)
	now := time.Now()
	for _, v := range []string{"nan", "NaN", "inf", "+Inf", "-Infinity"} {
		st, _ := tb.add("a/x", []byte(v), false, 0, now)
		if st.Numeric || len(st.History) != 0 {
			t.Errorf("%q gilt als Zahl: %+v", v, st)
		}
		if _, err := json.Marshal(st); err != nil {
			t.Errorf("%q nicht ausgebbar: %v", v, err)
		}
	}
	if st, _ := tb.add("a/x", []byte("1,5"), false, 0, now); !st.Numeric || st.History[0] != 1.5 {
		t.Errorf("normale Zahl: %+v", st)
	}
}

func TestMqttSafeStatsSkipsBroken(t *testing.T) {
	in := []MqttTopicStat{{Topic: "ok"}, {Topic: "kaputt", History: []float64{math.NaN()}}, {Topic: "ok2"}}
	out := mqttSafeStats(in)
	if len(out) != 2 || out[0].Topic != "ok" || out[1].Topic != "ok2" {
		t.Errorf("%+v", out)
	}
}

func TestMqttTopicTableFieldLimits(t *testing.T) {
	tb := newMqttTopicTable(10, 5)
	now := time.Now()
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < 300; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"k%03d":"v"`, i)
	}
	sb.WriteString("}")
	st, _ := tb.add("a/many", []byte(sb.String()), false, 0, now)
	if len(st.Fields) != 200 {
		t.Errorf("Felder: %d", len(st.Fields))
	}
	long, _ := tb.add("a/long", []byte(`{"v":"`+strings.Repeat("ä", 500)+`"}`), false, 0, now)
	if len(long.Fields) != 1 || len([]rune(long.Fields[0].Value)) > 201 {
		t.Errorf("Feldwert nicht gekürzt: %d", len([]rune(long.Fields[0].Value)))
	}
	// über 16 KiB: keine Zerlegung
	big, _ := tb.add("a/big", []byte(`{"v":"`+strings.Repeat("x", 17<<10)+`"}`), false, 0, now)
	if len(big.Fields) != 0 {
		t.Errorf("zu großes JSON zerlegt: %d", len(big.Fields))
	}
	// unveränderte Felder werden nicht erneut gesendet
	tb.takeChanged()
	tb.add("a/many", []byte(sb.String()), false, 0, now)
	ch := tb.takeChanged()
	if len(ch) != 1 || !ch[0].FieldsKept || ch[0].Fields != nil {
		t.Errorf("FieldsKept: %+v", ch)
	}
	tb.add("a/many", []byte(`{"neu":1}`), false, 0, now)
	if ch = tb.takeChanged(); len(ch) != 1 || ch[0].FieldsKept || len(ch[0].Fields) != 1 {
		t.Errorf("geänderte Felder: %+v", ch)
	}
}

func TestMqttTopicTableInvalidUTF8(t *testing.T) {
	tb := newMqttTopicTable(10, 5)
	st, _ := tb.add("a/bin", []byte{0xff, 0xfe, 'o', 'k'}, false, 0, time.Now())
	if !utf8.ValidString(st.Last) {
		t.Errorf("Last kein gültiges UTF-8: %q", st.Last)
	}
	if _, err := json.Marshal(st); err != nil {
		t.Error(err)
	}
}

func TestMqttExplorerPublishRejects(t *testing.T) {
	allow := map[string]bool{"import.execute": true, "import.write": true}
	h := mqttTestHandler("127.0.0.1", "1", allow)
	for _, topic := range []string{"halle/+/x", "$SYS/broker", "halle/#", ""} {
		w := httptest.NewRecorder()
		h.MqttExplorerPublish(w, publishReq(topic, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("Thema %q: %d", topic, w.Code)
		}
	}
	// Spiegel: 409
	h.mirror = MirrorInfo{On: true}
	w := httptest.NewRecorder()
	h.MqttExplorerPublish(w, publishReq("halle/test", nil))
	if w.Code != http.StatusConflict {
		t.Errorf("Spiegel: %d", w.Code)
	}
	h.mirror = MirrorInfo{}
	// kaputter Formular-Körper: 400, nicht 413
	r := httptest.NewRequest("POST", "/x", strings.NewReader("a=%zz"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, r.WithContext(explorerReq("POST", "/x").Context()))
	if w.Code != http.StatusBadRequest {
		t.Errorf("kaputtes Formular: %d", w.Code)
	}
	// wirklich zu groß: 413
	r = httptest.NewRequest("POST", "/x", strings.NewReader("payload="+strings.Repeat("x", 3*mqttExplorerMaxPublish)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.MqttExplorerPublish(w, r.WithContext(explorerReq("POST", "/x").Context()))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("zu groß: %d", w.Code)
	}
}

func TestMqttDialHonoursContext(t *testing.T) {
	// 192.0.2.1 (TEST-NET) antwortet nie: der Abbruch des Kontexts muss die Wartezeit beenden.
	opts, _, _ := mqttClientOptions(map[string]string{"ext_broker_host": "192.0.2.1"}, "explorer")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := mqttExplorerDial(ctx, mqtt.NewClient(opts))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("Dial: %v nach %v", err, time.Since(start))
	}
}

// Der Strom endet beim Abbruch der Anfrage und gibt den Sitzungsplatz frei.
func TestMqttExplorerStreamCancelReleasesSlot(t *testing.T) {
	_, host, port := startTestBroker(t)
	old := explorerLimiter
	explorerLimiter = newExplorerSessions(1, 1)
	t.Cleanup(func() { explorerLimiter = old })
	h := mqttTestHandler(host, port, map[string]bool{"import.execute": true})
	ctx, cancel := context.WithCancel(explorerReq("GET", "/x").Context())
	r := explorerReq("GET", "/x").WithContext(ctx)
	w := &syncRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() { defer close(done); h.MqttExplorerStream(w, r) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(w.String(), "event: connected") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Strom endet nicht nach Abbruch")
	}
	rel, err := explorerLimiter.acquire("u1")
	if err != nil {
		t.Fatalf("Sitzungsplatz nicht frei: %v", err)
	}
	rel()
}

// syncRecorder: ResponseRecorder, dessen Inhalt während des Schreibens sicher gelesen werden kann.
type syncRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (s *syncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Write(b)
}

func (s *syncRecorder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Body.String()
}
