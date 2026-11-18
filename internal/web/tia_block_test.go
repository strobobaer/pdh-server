package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/mqttbroker"
	"pdh/internal/core/mqttimport"
	"pdh/internal/core/rbac"
	"pdh/internal/modules/tiamqtt"
	"pdh/pkg/database"
)

func tiaTestSource(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "modules", "tiamqtt", "testdata", "Daten.db"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tiaBigSource: Daten.db mit einem zusätzlichen Feld, das bei 1024 Byte
// auf mehrere Nachrichten verteilt wird.
func tiaBigSource(t *testing.T) []byte {
	t.Helper()
	var extra strings.Builder
	for i := 1; i <= 40; i++ {
		extra.WriteString("      Wert" + strconv.Itoa(i) + " : DInt;\n")
	}
	s := strings.Replace(string(tiaTestSource(t)), "      Genau : LReal", extra.String()+"      Genau : LReal", 1)
	if !strings.Contains(s, "Wert40") {
		t.Fatal("Testquelle passt nicht")
	}
	return []byte(s)
}

// tiaValues: gültiges Formular wie von der Seite (Vorgaben einer externen Verbindung).
func tiaValues(mod func(url.Values)) url.Values {
	v := url.Values{
		"variant": {"standalone"}, "family": {"s7-1500"}, "broker_host": {"10.0.0.5"}, "broker_port": {"1883"},
		"client_id": {""}, "username": {"sps"}, "topic": {"sps/x/"}, "heartbeat_seconds": {"60"},
		"keepalive_seconds": {"60"}, "hw_id": {"64"}, "conn_id": {"1"}, "limit": {"4096"},
	}
	if mod != nil {
		mod(v)
	}
	return v
}

// tiaReq baut eine multipart-Anfrage mit Formularfeldern und (src != nil) Datei.
func tiaReq(t *testing.T, path, id string, v url.Values, src []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, vals := range v {
		for _, s := range vals {
			_ = mw.WriteField(k, s)
		}
	}
	if src != nil {
		fw, err := mw.CreateFormFile("db_file", "Daten.db")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(src)
	}
	_ = mw.Close()
	r := httptest.NewRequest("POST", path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}

func tiaFormReq(v url.Values) *http.Request {
	r := httptest.NewRequest("POST", "/x", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestTIARequiresPermission(t *testing.T) {
	allow := map[string]bool{}
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return allow[p] }}
	id := "11111111-1111-4111-8111-111111111111"
	src := tiaTestSource(t)

	// ohne import.tia: Seite leitet weiter, POST 403
	w := httptest.NewRecorder()
	h.TIABlockPage(w, efficioReq("GET", "/import/connections/"+id+"/tia", id, "", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("Seite ohne Recht: %d %s", w.Code, w.Header().Get("Location"))
	}
	for name, fn := range map[string]http.HandlerFunc{"prüfen": h.TIABlockCheckWeb, "herunterladen": h.TIABlockDownload} {
		asked = nil
		w = httptest.NewRecorder()
		fn(w, tiaReq(t, "/x", id, tiaValues(nil), src))
		if w.Code != http.StatusForbidden || len(asked) == 0 || asked[0] != permImportTIA {
			t.Errorf("%s ohne Recht: %d %v", name, w.Code, asked)
		}
	}
	// mit import.tia, ohne import.write: Kennwort oder Zuordnungen → 403
	allow[permImportTIA] = true
	for name, mod := range map[string]func(url.Values){
		"Kennwort":    func(v url.Values) { v.Set("include_password", "on") },
		"Zuordnungen": func(v url.Values) { v.Set("create_mappings", "on"); v.Set("infrastructure_id", id) },
	} {
		w = httptest.NewRecorder()
		h.TIABlockDownload(w, tiaReq(t, "/x", id, tiaValues(mod), src))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne import.write: %d", name, w.Code)
		}
	}
	// ohne Kennwort und Zuordnungen kein 403 (ohne Datenbank: Verbindung nicht gefunden)
	w = httptest.NewRecorder()
	h.TIABlockDownload(w, tiaReq(t, "/x", id, tiaValues(nil), src))
	if w.Code == http.StatusForbidden {
		t.Errorf("Download ohne Kennwort abgewiesen: %d", w.Code)
	}
}

func TestTIAToolEntry(t *testing.T) {
	tool := tiaTool("X")
	if tool.Href != "/import/connections/X/tia" || tool.Icon != "ti-cpu" || tool.Label != "TIA-Baustein herunterladen" ||
		tool.Help != "SCL-Baustein für Siemens-Steuerungen: Werte per Trigger, Heartbeat" {
		t.Errorf("Werkzeug: %+v", tool)
	}
}

func TestTIASlugUmlauts(t *testing.T) {
	for in, want := range map[string]string{
		"TIA-Prüfung (wird gelöscht)": "tia-pruefung-wird-geloescht",
		"Größe Ärger Übel":            "groesse-aerger-uebel",
		"Halle 3":                     "halle-3",
		"ÄÖÜ":                         "aeoeue",
		"!!!":                         "pdh",
	} {
		if got := tiaSlug(in); got != want {
			t.Errorf("tiaSlug(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

func TestTIADefaultsIntegrated(t *testing.T) {
	cfg := map[string]string{"broker_mode": "integrated", "int_listen_port": "1884", "int_broker_username": "plc",
		"int_broker_password": "Geheim123", "ext_broker_host": "nicht.nehmen"}
	f := tiaDefaults(cfg, "Linie 1 Öfen", "10.1.2.3:8443")
	want := tiaForm{Variant: "standalone", Family: "s7-1500", BrokerHost: "10.1.2.3", BrokerPort: 1884, Username: "plc",
		Topic: "sps/linie-1-oefen/", HeartbeatSeconds: 60, KeepAliveSeconds: 60, HwID: 64, ConnID: 1, Limit: 4096}
	if f != want {
		t.Errorf("Vorgaben:\n got %+v\nwant %+v", f, want)
	}
	if strings.Contains(strings.ToLower(jsonString(t, f)), "geheim") {
		t.Error("Kennwort in tiaForm")
	}
	// mit Zertifikat: TLS und LMQTT vorgewählt
	cfg["int_tls_cert_path"], cfg["int_tls_key_path"] = "/c.pem", "/k.pem"
	if f = tiaDefaults(cfg, "x", "[::1]:80"); !f.UseTLS || f.Variant != "lmqtt" || f.BrokerHost != "::1" {
		t.Errorf("TLS: %+v", f)
	}
	// Name: die erste IPv4-Adresse
	if f = tiaDefaults(map[string]string{"broker_mode": "integrated"}, "", "localhost:8080"); f.BrokerHost != "127.0.0.1" || f.BrokerPort != 1883 || f.Topic != "sps/pdh/" {
		t.Errorf("Name statt Adresse: %+v", f)
	}
}

func TestTIADefaultsExternal(t *testing.T) {
	cfg := map[string]string{"broker_mode": "external", "ext_broker_host": " broker.werk ", "ext_username": "u1", "ext_password": "Geheim123"}
	f := tiaDefaults(cfg, "SPS-Halle_2", "pdh:443")
	if f.BrokerHost != "broker.werk" || f.BrokerPort != 1883 || f.Username != "u1" || f.UseTLS || f.Variant != "standalone" || f.Topic != "sps/sps-halle-2/" {
		t.Errorf("extern: %+v", f)
	}
	if strings.Contains(jsonString(t, f), "Geheim123") {
		t.Error("Kennwort in tiaForm")
	}
	cfg["ext_broker_port"], cfg["ext_use_tls"] = "8883", "true"
	if f = tiaDefaults(cfg, "x", ""); f.BrokerPort != 8883 || !f.UseTLS || f.Variant != "lmqtt" {
		t.Errorf("extern TLS: %+v", f)
	}
}

func jsonString(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTIAFormFromRequest(t *testing.T) {
	f, errs := tiaFormFromRequest(tiaFormReq(tiaValues(func(v url.Values) {
		v.Set("retain", "on")
		v.Set("use_tls", "on")
		v.Set("variant", "lmqtt")
		v.Set("heartbeat_seconds", "0")
	})))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !f.Retain || !f.UseTLS || f.Variant != "lmqtt" || f.HeartbeatSeconds != 0 || f.BrokerPort != 1883 || f.Topic != "sps/x/" || f.IncludePassword || f.CreateMappings {
		t.Errorf("gelesen: %+v", f)
	}
}

func TestTIAOptionsRejects(t *testing.T) {
	for name, c := range map[string]struct {
		mod  func(url.Values)
		want string
	}{
		"Topic mit #":         {func(v url.Values) { v.Set("topic", "sps/#") }, "Platzhalter"},
		"Port 0":              {func(v url.Values) { v.Set("broker_port", "0") }, "Port"},
		"Port Text":           {func(v url.Values) { v.Set("broker_port", "abc") }, "ganze Zahl"},
		"Grenze 512":          {func(v url.Values) { v.Set("limit", "512") }, "1024 und 16384"},
		"Variante":            {func(v url.Values) { v.Set("variant", "s7-300") }, "Variante"},
		"Anlage fehlt":        {func(v url.Values) { v.Set("create_mappings", "on") }, "Anlage"},
		"Broker-Adresse leer": {func(v url.Values) { v.Set("broker_host", " ") }, "Adresse"},
	} {
		_, errs := tiaFormFromRequest(tiaFormReq(tiaValues(c.mod)))
		if len(errs) == 0 || !strings.Contains(strings.Join(errs, "|"), c.want) {
			t.Errorf("%s: %v", name, errs)
		}
	}
	// eigenständig mit TLS: die Meldung von Generate
	f, errs := tiaFormFromRequest(tiaFormReq(tiaValues(func(v url.Values) { v.Set("use_tls", "on") })))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := tiaBuild(f, tiaTestSource(t), "", ""); err == nil || !strings.Contains(err.Error(), "bitte LMQTT wählen") {
		t.Errorf("standalone + TLS: %v", err)
	}
	// Fehler der Quelle mit Zeile
	_, err := tiaBuild(f, []byte("DATA_BLOCK \"X\"\nVAR\n  a : Int\nEND_VAR\n"), "", "")
	var pe *tiamqtt.ParseError
	if err == nil || !errors.As(err, &pe) || pe.Line == 0 {
		t.Errorf("Parserfehler: %v", err)
	}
	// keine Datei
	if v := tiaCheck(f, nil, []string{"Bitte die Quelle des Datenbausteins (.db) hochladen"}); len(v.Errors) != 1 {
		t.Errorf("ohne Datei: %+v", v)
	}
}

func TestTIAReadRequestLimits(t *testing.T) {
	for name, c := range map[string]struct {
		src  []byte
		want string
	}{
		"zu groß":     {bytes.Repeat([]byte("x"), tiaMaxUpload+10), "größer als 1 MiB"},
		"keine Datei": {nil, "hochladen"},
		"leer":        {[]byte{}, "leer"},
	} {
		_, _, errs := tiaReadRequest(httptest.NewRecorder(), tiaReq(t, "/x", "id", tiaValues(nil), c.src))
		if !strings.Contains(strings.Join(errs, "|"), c.want) {
			t.Errorf("%s: %v", name, errs)
		}
	}
	f, src, errs := tiaReadRequest(httptest.NewRecorder(), tiaReq(t, "/x", "id", tiaValues(nil), tiaTestSource(t)))
	if len(errs) > 0 || len(src) == 0 || f.Topic != "sps/x/" {
		t.Errorf("gültig: %v %d %+v", errs, len(src), f)
	}
}

func TestTIATopicCompletion(t *testing.T) {
	if got := tiaTopic("sps/x/", "Daten"); got != "sps/x/Daten" {
		t.Errorf("ergänzt: %q", got)
	}
	if got := tiaTopic("sps/x/eigen", "Daten"); got != "sps/x/eigen" {
		t.Errorf("unverändert: %q", got)
	}
	if got := tiaTopic("a/", "Linie 1/#+"); got != "a/Linie_1___" {
		t.Errorf("Sonderzeichen: %q", got)
	}
}

func TestTIAMappingRows(t *testing.T) {
	db, err := tiamqtt.ParseDBSource(tiaTestSource(t))
	if err != nil {
		t.Fatal(err)
	}
	l, err := tiamqtt.Plan(db, tiamqtt.FamilyS71500, 4096)
	if err != nil {
		t.Fatal(err)
	}
	rows := tiaMappingRows(l, "Daten", "sps/x/Daten", 60)
	if len(rows) != len(l.Leaves)+1 {
		t.Fatalf("%d Zeilen für %d Werte", len(rows), len(l.Leaves))
	}
	byRef := map[string]importMappingCreateInput{}
	for _, r := range rows {
		byRef[r.SourceRef] = r
		if r.HeartbeatSeconds != 0 && r.SourceRef != "sps/x/Daten/heartbeat#count" {
			t.Errorf("heartbeat_seconds an %s", r.SourceRef)
		}
	}
	if r, ok := byRef["sps/x/Daten#values.Motor.Drehzahl"]; !ok || r.Name != "Daten.Motor.Drehzahl" {
		t.Errorf("Motor.Drehzahl: %+v", r)
	}
	if r, ok := byRef["sps/x/Daten#values.Feld[0]"]; !ok || r.Name != "Daten.Feld[1]" {
		t.Errorf("Feld[1]: %+v", r)
	}
	if r := byRef["sps/x/Daten/heartbeat#count"]; r.HeartbeatSeconds != 60 || r.Name != "Daten.Heartbeat" {
		t.Errorf("Heartbeat: %+v", r)
	}
	if rows := tiaMappingRows(l, "Daten", "sps/x/Daten", 0); len(rows) != len(l.Leaves) {
		t.Errorf("Heartbeat aus: %d Zeilen", len(rows))
	}
	long := tiaMappingRows(l, strings.Repeat("ä", 200), "t", 0)
	if n := len([]rune(long[0].Name)); n != tiaNameMax {
		t.Errorf("Name nicht gekürzt: %d", n)
	}
}

func TestTIACheckFragment(t *testing.T) {
	src := strings.Replace(string(tiaBigSource(t)), "      Takt : Int := 5;", "      Takt : Int := 5;\n      Dauer : Time;", 1)
	f, errs := tiaFormFromRequest(tiaFormReq(tiaValues(func(v url.Values) { v.Set("limit", "1024") })))
	v := tiaCheck(f, []byte(src), errs)
	if len(v.Errors) > 0 {
		t.Fatal(v.Errors)
	}
	if v.DBName != "Daten" || v.Topic != "sps/x/Daten" || len(v.Parts) < 2 || v.Mappings != v.Values+1 {
		t.Errorf("Prüfung: %d Teile, %+v", len(v.Parts), v)
	}
	var dauer *tiamqtt.Skipped
	for i := range v.Skipped {
		if v.Skipped[i].Path == "Dauer" {
			dauer = &v.Skipped[i]
		}
	}
	if dauer == nil || dauer.Line == 0 || !strings.Contains(dauer.Reason, "Time") {
		t.Fatalf("übersprungen: %+v", v.Skipped)
	}
	var buf bytes.Buffer
	if err := loadTestTemplates(t).ExecuteTemplate(&buf, "tia-block-check", v); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Datenbaustein „Daten“", strconv.Itoa(len(v.Parts)) + " Nachricht(en)", "Nachricht 2:",
		"values.Motor.Drehzahl", "Dauer", "<td>" + strconv.Itoa(dauer.Line) + "</td>", dauer.Reason, "sps/x/Daten"} {
		if !strings.Contains(out, want) {
			t.Errorf("Fragment enthält %q nicht", want)
		}
	}
	// Fehlerfall
	buf.Reset()
	if err := loadTestTemplates(t).ExecuteTemplate(&buf, "tia-block-check", tiaCheckView{Errors: []string{"Zeile 3: kaputt"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Zeile 3: kaputt") || !strings.Contains(buf.String(), `role="alert"`) {
		t.Error("Fehler nicht im Fragment")
	}
}

func TestTIAPageRenders(t *testing.T) {
	d := TIABlockPageData{ID: "abc", Name: "Linie 1", Form: tiaDefaults(map[string]string{"ext_broker_host": "10.0.0.5"}, "Linie 1", ""),
		HasPassword: true, InfoURL: tiamqtt.LMQTTInfoURL, MinLimit: tiamqtt.MinLimit, MaxLimit: tiamqtt.MaxLimit}
	out := renderPage(t, loadTestTemplates(t), "tia_block", d)
	for _, want := range []string{`action="/import/connections/abc/tia/download"`, `enctype="multipart/form-data"`, `hx-post="/import/connections/abc/tia/check"`,
		`hx-encoding="multipart/form-data"`, `name="db_file"`, `value="sps/linie-1/"`, `value="10.0.0.5"`, `value="standalone" checked`} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	// ohne import.write: kein Kennwort, keine Zuordnungen
	for _, bad := range []string{`name="include_password"`, `name="create_mappings"`} {
		if strings.Contains(out, bad) {
			t.Errorf("ohne Schreibrecht sichtbar: %s", bad)
		}
	}
	d.CanWrite, d.Errors = true, []string{"Der Port muss zwischen 1 und 65535 liegen"}
	out = renderPage(t, loadTestTemplates(t), "tia_block", d)
	for _, want := range []string{`name="include_password"`, `name="create_mappings"`, "tia-infra-id", "Der Port muss zwischen 1 und 65535 liegen"} {
		if !strings.Contains(out, want) {
			t.Errorf("mit Schreibrecht fehlt %q", want)
		}
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

type tiaEnv struct {
	pool  *pgxpool.Pool
	h     *Handler
	id    string
	infra string
	allow map[string]bool
}

func newTIAEnv(t *testing.T, kind string, cfg map[string]string) *tiaEnv {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("Labor-Datenbank nicht erreichbar: ", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skip("Labor-Datenbank nicht erreichbar: ", err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	env := &tiaEnv{pool: pool, allow: map[string]bool{permImportTIA: true, "import.write": true}}
	rb := rbac.NewService(rbac.NewRepository(pool))
	if err := rb.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	env.h = &Handler{db: pool, rbac: rb, jwtSecret: "t", mqttBrokers: mqttbroker.NewManager(), mqttImport: mqttimport.NewManager(),
		hasPermHook: func(_ *http.Request, p string) bool { return env.allow[p] }}
	sealed, err := sealConnConfig(env.h.connKey(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sealed)
	sfx := time.Now().Format("150405.000000")
	if err := pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name, config)
		VALUES ('import', $1, $2, $3) RETURNING id::text`, kind, "tia-test-"+sfx, b).Scan(&env.id); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "TIA-Anlage "+sfx).Scan(&env.infra); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		env.h.mqttImport.Stop(env.id)
		env.h.mqttBrokers.Stop(env.id)
		_, _ = pool.Exec(ctx, `DELETE FROM record_history WHERE ref_type = 'connection' AND ref_id = $1`, env.id)
		_, _ = pool.Exec(ctx, `DELETE FROM import_export_connections WHERE id = $1::uuid`, env.id)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = $1::uuid`, env.infra)
	})
	return env
}

func (e *tiaEnv) download(t *testing.T, mod func(url.Values)) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	e.h.TIABlockDownload(w, tiaReq(t, "/x", e.id, tiaValues(mod), tiaTestSource(t)))
	return w
}

func zipFiles(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("ZIP öffnet sich nicht: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		c, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(c)
	}
	return out
}

func TestTIANotForOtherKinds(t *testing.T) {
	env := newTIAEnv(t, "opcua", map[string]string{"endpoint_url": "opc.tcp://127.0.0.1:4840"})
	w := httptest.NewRecorder()
	env.h.TIABlockPage(w, efficioReq("GET", "/x", env.id, "", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("Seite an OPC UA: %d", w.Code)
	}
	if w = env.download(t, nil); w.Code != http.StatusNotFound {
		t.Errorf("Download an OPC UA: %d", w.Code)
	}
}

func TestTIADownloadZip(t *testing.T) {
	env := newTIAEnv(t, "mqtt", map[string]string{"broker_mode": "external", "ext_broker_host": "10.0.0.5", "ext_username": "sps", "ext_password": "Geh'eim$1"})
	// nur import.tia: ohne Kennwort geht es
	env.allow["import.write"] = false
	w := env.download(t, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Download: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, tiamqtt.ZipName("Daten", tiamqtt.VariantStandalone)) {
		t.Errorf("Content-Disposition %q", cd)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("Kopfzeilen: %v", w.Header())
	}
	files := zipFiles(t, w.Body.Bytes())
	if _, ok := files["3_PDH_MQTT_Daten.scl"]; !ok {
		t.Fatalf("Dateien: %v", files)
	}
	all := strings.Join(mapValues(files), "\n")
	if strings.Contains(all, "Geh$'eim$$1") || !strings.Contains(all, "password : String[254] := '"+tiamqtt.PasswordPlaceholder+"'") {
		t.Error("Kennwort ohne Ankreuzen in der Datei")
	}
	if !strings.Contains(all, "sps/x/Daten") {
		t.Error("Topic nicht um den Datenbaustein ergänzt")
	}
	// mit Kennwort (import.write): Kennwort der Verbindung, Verlaufseintrag
	env.allow["import.write"] = true
	w = env.download(t, func(v url.Values) { v.Set("include_password", "on") })
	if w.Code != http.StatusOK {
		t.Fatalf("mit Kennwort: %d %s", w.Code, w.Body.String())
	}
	all = strings.Join(mapValues(zipFiles(t, w.Body.Bytes())), "\n")
	if !strings.Contains(all, "password : String[254] := 'Geh$'eim$$1'") {
		t.Error("Kennwort der Verbindung fehlt")
	}
	var n int
	_ = env.pool.QueryRow(context.Background(), `SELECT count(*) FROM record_history WHERE ref_type = 'connection' AND ref_id = $1
		AND action = 'tia_download' AND message LIKE '%mit Kennwort%'`, env.id).Scan(&n)
	if n != 1 {
		t.Errorf("Verlaufseinträge: %d", n)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func tiaMappingsOf(t *testing.T, env *tiaEnv) map[string]struct {
	Name string
	HB   *int
} {
	t.Helper()
	rows, err := env.pool.Query(context.Background(), `SELECT source_ref, name, heartbeat_seconds FROM import_mappings WHERE connection_id = $1::uuid`, env.id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]struct {
		Name string
		HB   *int
	}{}
	for rows.Next() {
		var ref, name string
		var hb *int
		if err := rows.Scan(&ref, &name, &hb); err != nil {
			t.Fatal(err)
		}
		out[ref] = struct {
			Name string
			HB   *int
		}{name, hb}
	}
	return out
}

func TestTIADownloadCreatesMappings(t *testing.T) {
	env := newTIAEnv(t, "mqtt", map[string]string{"broker_mode": "external", "ext_broker_host": "10.0.0.5"})
	w := env.download(t, func(v url.Values) { v.Set("create_mappings", "on"); v.Set("infrastructure_id", env.infra) })
	if w.Code != http.StatusOK {
		t.Fatalf("Download: %d %s", w.Code, w.Body.String())
	}
	m := tiaMappingsOf(t, env)
	if r, ok := m["sps/x/Daten#values.Motor.Drehzahl"]; !ok || r.Name != "Daten.Motor.Drehzahl" || r.HB != nil {
		t.Errorf("Motor.Drehzahl: %+v", r)
	}
	if r, ok := m["sps/x/Daten#values.Feld[0]"]; !ok || r.Name != "Daten.Feld[1]" {
		t.Errorf("Feld: %+v", r)
	}
	if r, ok := m["sps/x/Daten/heartbeat#count"]; !ok || r.HB == nil || *r.HB != 60 {
		t.Errorf("Heartbeat: %+v", r)
	}
	// Heartbeat aus: keine Heartbeat-Zuordnung (neue Verbindung)
	env2 := newTIAEnv(t, "mqtt", map[string]string{"broker_mode": "external", "ext_broker_host": "10.0.0.5"})
	env2.download(t, func(v url.Values) {
		v.Set("create_mappings", "on")
		v.Set("infrastructure_id", env2.infra)
		v.Set("heartbeat_seconds", "0")
	})
	if _, ok := tiaMappingsOf(t, env2)["sps/x/Daten/heartbeat#count"]; ok {
		t.Error("Heartbeat-Zuordnung trotz Heartbeat 0")
	}
	// Anlage gibt es nicht: kein ZIP, Seite mit Meldung (400)
	env.h.tmpl = loadTestTemplates(t)
	w = env.download(t, func(v url.Values) {
		v.Set("create_mappings", "on")
		v.Set("infrastructure_id", "99999999-9999-4999-8999-999999999999")
		v.Set("topic", "sps/y/")
	})
	if w.Code != http.StatusBadRequest || w.Header().Get("Content-Type") == "application/zip" {
		t.Errorf("unbekannte Anlage: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestTIADownloadMappingsNoDuplicates(t *testing.T) {
	env := newTIAEnv(t, "mqtt", map[string]string{"broker_mode": "external", "ext_broker_host": "10.0.0.5"})
	ctx := context.Background()
	// vorhandene Zuordnung mit anderem Namen
	if _, err := env.pool.Exec(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name)
		VALUES ($1::uuid, 'sps/x/Daten#values.Takt', $2::uuid, 'Meine Taktzeit')`, env.id, env.infra); err != nil {
		t.Fatal(err)
	}
	mod := func(v url.Values) { v.Set("create_mappings", "on"); v.Set("infrastructure_id", env.infra) }
	if w := env.download(t, mod); w.Code != http.StatusOK {
		t.Fatalf("erster Download: %d", w.Code)
	}
	first := len(tiaMappingsOf(t, env))
	const hbRef = "sps/x/Daten/heartbeat#count"
	// Heartbeat-Zuordnung umbenennen: Name und Anlage müssen beim Nachziehen bleiben
	if _, err := env.pool.Exec(ctx, `UPDATE import_mappings SET name = 'Mein Heartbeat' WHERE connection_id = $1::uuid AND source_ref = $2`, env.id, hbRef); err != nil {
		t.Fatal(err)
	}
	// zweiter Download mit anderem Heartbeat-Intervall
	if w := env.download(t, func(v url.Values) { mod(v); v.Set("heartbeat_seconds", "300") }); w.Code != http.StatusOK {
		t.Fatalf("zweiter Download: %d", w.Code)
	}
	m := tiaMappingsOf(t, env)
	if len(m) != first {
		t.Errorf("doppelt angelegt: %d → %d", first, len(m))
	}
	if m["sps/x/Daten#values.Takt"].Name != "Meine Taktzeit" {
		t.Errorf("vorhandene Zuordnung geändert: %+v", m["sps/x/Daten#values.Takt"])
	}
	if hb := m[hbRef]; hb.HB == nil || *hb.HB != 300 || hb.Name != "Mein Heartbeat" {
		t.Errorf("Heartbeat nach Intervallwechsel: %+v", hb)
	}
	var infra string
	_ = env.pool.QueryRow(ctx, `SELECT infrastructure_id::text FROM import_mappings WHERE connection_id = $1::uuid AND source_ref = $2`, env.id, hbRef).Scan(&infra)
	if infra != env.infra {
		t.Errorf("Anlage der Heartbeat-Zuordnung geändert: %s", infra)
	}
	// dritter Download mit Heartbeat 0: Überwachung der vorhandenen Heartbeat-Zuordnung aus (NULL), sonst unberührt
	if w := env.download(t, func(v url.Values) { mod(v); v.Set("heartbeat_seconds", "0") }); w.Code != http.StatusOK {
		t.Fatalf("dritter Download: %d", w.Code)
	}
	if hb := tiaMappingsOf(t, env)[hbRef]; hb.HB != nil || hb.Name != "Mein Heartbeat" {
		t.Errorf("Heartbeat 0 soll die Überwachung ausschalten (NULL), Name bleibt: %+v", hb)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// tiaSampleValue: ein Wert passend zum Typ, wie ihn die Steuerung liefert.
func tiaSampleValue(k tiamqtt.Kind) any {
	switch k {
	case tiamqtt.KindBool:
		return true
	case tiamqtt.KindReal:
		return float32(1.5)
	case tiamqtt.KindLReal:
		return float64(2.25)
	case tiamqtt.KindString:
		return "abc"
	case tiamqtt.KindDTL, tiamqtt.KindDT:
		return time.Date(2026, 11, 15, 10, 1, 0, 0, time.UTC)
	}
	return int64(7)
}

func TestTIAEndToEndIntegratedBroker(t *testing.T) {
	port := freeTCPPort(t)
	cfg := map[string]string{"broker_mode": "integrated", "int_listen_port": strconv.Itoa(port), "int_allow_anonymous": "true"}
	env := newTIAEnv(t, "mqtt", cfg)
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `UPDATE import_export_connections SET enabled = true WHERE id = $1::uuid`, env.id); err != nil {
		t.Fatal(err)
	}
	env.h.reconcileMqttBroker(env.id, "import", "mqtt", true, cfg)
	if !env.h.mqttBrokers.IsRunning(env.id) {
		t.Fatalf("Broker läuft nicht: %s", env.h.mqttBrokers.LastError(env.id))
	}
	src := tiaBigSource(t)
	w := httptest.NewRecorder()
	env.h.TIABlockDownload(w, tiaReq(t, "/x", env.id, tiaValues(func(v url.Values) {
		v.Set("broker_host", "127.0.0.1")
		v.Set("broker_port", strconv.Itoa(port))
		v.Set("limit", "1024")
		v.Set("create_mappings", "on")
		v.Set("infrastructure_id", env.infra)
	}), src))
	if w.Code != http.StatusOK {
		t.Fatalf("Download: %d %s", w.Code, w.Body.String())
	}
	db, err := tiamqtt.ParseDBSource(src)
	if err != nil {
		t.Fatal(err)
	}
	l, err := tiamqtt.Plan(db, tiamqtt.FamilyS71500, 1024)
	if err != nil || len(l.Parts) < 2 {
		t.Fatalf("Aufteilung: %v %+v", err, l)
	}
	kinds := map[string]tiamqtt.Kind{}
	for _, lf := range l.Leaves {
		kinds[lf.Path] = lf.Kind
	}
	get := func(p string) any { return tiaSampleValue(kinds[p]) }

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(tiamqtt.ConnectPacket("pdh-sps-Daten", "", "", 60)); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil || ack[0] != 0x20 || ack[3] != 0 {
		t.Fatalf("CONNACK: % x %v", ack, err)
	}
	ts := time.Now()
	for _, p := range l.Parts[:2] {
		if _, err := conn.Write(tiamqtt.PublishPacket("sps/x/Daten", tiamqtt.Render(p, len(l.Parts), ts, get), false)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Write(tiamqtt.PublishPacket("sps/x/Daten/heartbeat", []byte(`{"ts":"2026-11-15T10:01:00Z","count":7}`), false)); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"sps/x/Daten/heartbeat#count": true}
	for _, p := range l.Parts[:2] {
		for _, lf := range p.Leaves {
			want[mqttSourceRef("sps/x/Daten", lf.JSONPath)] = true
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	var missing []string
	for {
		missing = missing[:0]
		rows, err := env.pool.Query(ctx, `SELECT source_ref, COALESCE(last_value, '') FROM import_mappings WHERE connection_id = $1::uuid`, env.id)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for rows.Next() {
			var ref, v string
			_ = rows.Scan(&ref, &v)
			got[ref] = v
		}
		rows.Close()
		for ref := range want {
			if got[ref] == "" {
				missing = append(missing, ref)
			}
		}
		if len(missing) == 0 {
			if got["sps/x/Daten/heartbeat#count"] != "7" {
				t.Errorf("Heartbeat-Wert %q", got["sps/x/Daten/heartbeat#count"])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ohne Wert: %v", missing)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
