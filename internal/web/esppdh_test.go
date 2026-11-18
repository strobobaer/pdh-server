package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/mqttbroker"
	"pdh/internal/core/mqttimport"
	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
)

// ESPPDH – Geräte (esppdh.go, esppdh_store.go): Liste, Detailseite, Rechte mit Abteilung,
// Gerätezugänge am eingebauten Broker. Die Datenbank-Tests brauchen die Labor-Datenbank.

// esppdhTestHost: Adresse der Anfrage – eine IP-Adresse, damit nichts aufgelöst werden muss.
const esppdhTestHost = "10.9.8.7:8090"

var esppdhUser = &users.User{ID: pdhSystemUserID, FirstName: "Ines", LastName: "Test", Role: "viewer"}

// esppdhDo ruft einen Handler auf (Formular bei POST, id im Pfad) als Benutzer u.
func esppdhDo(fn http.HandlerFunc, method, path, id string, u *users.User, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Host = esppdhTestHost
	rc := chi.NewRouteContext()
	if id != "" {
		rc.URLParams.Add("id", id)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	if u != nil {
		ctx = context.WithValue(ctx, "user", u)
	}
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

// esppdhRoutes: alle Routen der Seite mit dem Recht, das sie mindestens brauchen.
func esppdhRoutes(h *Handler) []struct {
	name, method string
	fn           http.HandlerFunc
	perm         string
} {
	return []struct {
		name, method string
		fn           http.HandlerFunc
		perm         string
	}{
		{"Liste", http.MethodGet, h.EsppdhPage, permESPView},
		{"Anlegen", http.MethodPost, h.EsppdhCreateWeb, permESPEdit},
		{"Detailseite", http.MethodGet, h.EsppdhDevicePage, permESPView},
		{"Speichern", http.MethodPost, h.EsppdhSaveWeb, permESPEdit},
		{"Sperren", http.MethodPost, h.EsppdhLockWeb, permESPEdit},
		{"Kennwort", http.MethodPost, h.EsppdhPasswordWeb, permESPEdit},
		{"Löschen", http.MethodPost, h.EsppdhDeleteWeb, permESPAdmin},
		{"Konfiguration", http.MethodGet, h.EsppdhConfigWeb, permESPView},
	}
}

func TestESPPDHGate(t *testing.T) {
	id := "/" + itNetTestID
	for path, want := range map[string]string{
		"/import/esppdh":                    permESPEdit,
		"/import/esppdh" + id:               permESPEdit,
		"/import/esppdh" + id + "/lock":     permESPEdit,
		"/import/esppdh" + id + "/password": permESPEdit,
		"/import/esppdh" + id + "/delete":   permESPAdmin,
	} {
		_, perm, ref, gotID, ok := matchGate("POST", path)
		if !ok || perm != want {
			t.Errorf("%s: Treffer %v Recht %q, erwartet %q", path, ok, perm, want)
		}
		if path != "/import/esppdh" && (ref != esppdhRef || gotID != itNetTestID) {
			t.Errorf("%s: Datensatz %q %q", path, ref, gotID)
		}
	}
	if _, _, _, _, ok := matchGate("GET", "/import/esppdh"+id+"/config.yaml"); ok {
		t.Error("Lesen braucht keine Schreib-Regel")
	}
	if areaAdminPerm[esppdhRef] != permESPAdmin {
		t.Error("Bereichsadministrator für ESPPDH fehlt")
	}
	if src, ok := recordDeptSource[esppdhRef]; !ok || src.table != "esppdh_devices" || src.group {
		t.Errorf("recordDeptSource[esppdh] = %+v", src)
	}
	if rt, got, ok := scopeTarget("/import/esppdh" + id); !ok || rt != esppdhRef || got != itNetTestID {
		t.Errorf("scopeTarget: %q %q %v", rt, got, ok)
	}
	if _, _, ok := scopeTarget("/import/esppdh"); ok {
		t.Error("die Liste braucht keine Datensatzprüfung")
	}
	if permESPView != "esppdh.view" || permESPEdit != "esppdh.edit" || permESPAdmin != "esppdh.admin" || esppdhRef != "esppdh_device" {
		t.Error("Schlüssel weichen von der Vorgabe ab")
	}

	// ohne Recht lehnt jede Route ab, bevor sie die Datenbank anfasst
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	for _, rt := range esppdhRoutes(h) {
		if w := esppdhDo(rt.fn, rt.method, "/import/esppdh", itNetTestID, esppdhUser, url.Values{}); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: %d", rt.name, w.Code)
		}
	}
	// Ansehen reicht nicht zum Ändern, Bearbeiten nicht zum Löschen
	for _, have := range []string{permESPView, permESPEdit} {
		h.hasPermHook = func(_ *http.Request, p string) bool { return p == have || p == permESPView }
		for _, rt := range esppdhRoutes(h) {
			if rt.method != http.MethodPost || (have == permESPEdit && rt.perm == permESPEdit) {
				continue
			}
			if w := esppdhDo(rt.fn, rt.method, "/x", itNetTestID, esppdhUser, url.Values{}); w.Code != http.StatusForbidden {
				t.Errorf("%s nur mit %s: %d", rt.name, have, w.Code)
			}
		}
	}

	// Navigation: Gruppe Daten, nur mit Recht
	var found bool
	for _, d := range navDefs {
		if d.Key != "esppdh" {
			continue
		}
		found = true
		if d.Href != "/import/esppdh" || d.Icon != "ti-cpu" || d.Label != "ESPPDH" || d.group != "data" || d.Page != "esppdh" {
			t.Errorf("Navigationseintrag: %+v in %q", d.navItem, d.group)
		}
		if d.show(&BaseData{}) {
			t.Error("ohne Recht darf der Eintrag nicht erscheinen")
		}
		if !d.show(&BaseData{CanESPPDH: true}) {
			t.Error("mit esppdh.view oder esppdh.edit muss der Eintrag erscheinen")
		}
	}
	if !found {
		t.Error("Navigationseintrag ESPPDH fehlt")
	}
}

// Der Hauptzugang eines eingebauten Brokers darf nicht wie ein Gerät heißen – der Broker
// hielte ihn sonst für ein Gerät und wiese ihn ab.
func TestESPPDHBrokerUserReserved(t *testing.T) {
	cfg := map[string]string{"broker_mode": "integrated", "int_listen_port": "1883", "int_broker_username": "esppdh-halle"}
	if err := mqttCheckSystem(cfg, "", ""); err == nil || !strings.Contains(err.Error(), "esppdh-") {
		t.Errorf("Benutzer esppdh-…: %v", err)
	}
	cfg["int_broker_username"] = "pdh"
	if err := mqttCheckSystem(cfg, "", ""); err != nil {
		t.Errorf("Benutzer pdh: %v", err)
	}
	// ein fremder Broker ist nicht betroffen
	if err := mqttCheckSystem(map[string]string{"broker_mode": "external", "ext_username": "esppdh-x"}, "", ""); err != nil {
		t.Errorf("fremder Broker: %v", err)
	}
}

// Die Import-Seite verweist auf ESPPDH, wenn man es sehen darf.
func TestESPPDHImportLink(t *testing.T) {
	tmpl := loadTestTemplates(t)
	if out := renderPage(t, tmpl, "import", ConnectionsPageData{BaseData: BaseData{CanESPPDH: true}, Direction: "import"}); !strings.Contains(out, `href="/import/esppdh"`) {
		t.Error("Verweis auf ESPPDH fehlt")
	}
	if out := renderPage(t, tmpl, "import", ConnectionsPageData{Direction: "import"}); strings.Contains(out, `href="/import/esppdh"`) {
		t.Error("Verweis ohne Recht")
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

type esppdhEnvT struct {
	h     *Handler
	pool  *pgxpool.Pool
	sfx   string
	infra string
	conn  string
	cfg   map[string]string
	wlan  string
	wlPW  string
	allow map[string]bool
}

// esppdhFreePort: freier TCP-Port für einen Broker im Test.
func esppdhFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

// esppdhBrokerCfg: eingebauter Broker mit Hauptzugang pdh/geheim; extra = Schlüssel, Wert, …
func esppdhBrokerCfg(port int, extra ...string) map[string]string {
	cfg := map[string]string{"broker_mode": "integrated", "int_listen_port": strconv.Itoa(port),
		"int_broker_username": "pdh", "int_broker_password": "geheim", "int_retain_enabled": "true", "int_allow_anonymous": "false"}
	for i := 0; i+1 < len(extra); i += 2 {
		cfg[extra[i]] = extra[i+1]
	}
	return cfg
}

// esppdhSetSwitch setzt den Schalter esppdh.enabled und stellt ihn nach dem Test wieder her.
func esppdhSetSwitch(t *testing.T, h *Handler, on string) {
	t.Helper()
	ctx := context.Background()
	var before string
	had := h.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, esppdhSetEnabled).Scan(&before) == nil
	t.Cleanup(func() {
		if had {
			_ = h.setAppSetting(context.Background(), esppdhSetEnabled, before)
		} else {
			_, _ = h.db.Exec(context.Background(), `DELETE FROM app_settings WHERE key=$1`, esppdhSetEnabled)
		}
	})
	if err := h.setAppSetting(ctx, esppdhSetEnabled, on); err != nil {
		t.Fatal(err)
	}
}

// newEsppdhEnv: Handler mit Labor-Datenbank, Vorlagen, Broker-Verwaltung, eingeschaltetem
// ESPPDH, einer Anlage, einem WLAN und einer Verbindung mit eingebautem Broker (cfg nil =
// Broker mit Namen, ohne TLS). Rechte über hasPermHook (alle drei an).
func newEsppdhEnv(t *testing.T, cfg map[string]string) *esppdhEnvT {
	t.Helper()
	esppdhTestEnv(t, false)
	h, pool, _ := itNetPageEnv(t)
	h.jwtSecret = "t"
	h.mqttBrokers, h.mqttImport = mqttbroker.NewManager(), mqttimport.NewManager()
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	e := &esppdhEnvT{h: h, pool: pool, sfx: sfx, allow: map[string]bool{permESPView: true, permESPEdit: true, permESPAdmin: true}}
	h.hasPermHook = func(_ *http.Request, p string) bool { return e.allow[p] }
	t.Cleanup(func() { h.mqttBrokers.StopAll() })
	esppdhSetSwitch(t, h, "1")
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "ESP-Anlage "+sfx).Scan(&e.infra); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM esppdh_devices WHERE infrastructure_id = $1::uuid`, e.infra)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1::uuid`, e.infra)
	})
	if cfg == nil {
		cfg = esppdhBrokerCfg(esppdhFreePort(t), mqttHostKey, "mqtt-"+sfx+".pdh-test.invalid")
	}
	e.cfg = cfg
	e.conn = e.addConn(t, "esp-test-"+sfx, cfg)
	e.wlPW = "WLAN-Geheim-" + sfx
	e.wlan = e.addWLAN(t, "pdh-esp-"+sfx, e.wlPW, "")
	return e
}

// addConn legt eine MQTT-Import-Verbindung an (Kennwörter verschlüsselt wie im Formular).
func (e *esppdhEnvT) addConn(t *testing.T, name string, cfg map[string]string) string {
	t.Helper()
	sealed, err := sealConnConfig(e.h.connKey(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sealed)
	var id string
	if err := e.pool.QueryRow(context.Background(), `INSERT INTO import_export_connections (direction, kind, name, config, enabled)
		VALUES ('import', 'mqtt', $1, $2, true) RETURNING id::text`, name, b).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		e.h.mqttBrokers.Stop(id)
		_, _ = e.pool.Exec(c, `DELETE FROM esppdh_devices WHERE connection_id = $1::uuid`, id)
		_, _ = e.pool.Exec(c, `DELETE FROM import_export_connections WHERE id = $1::uuid`, id)
	})
	return id
}

// addWLAN legt ein WLAN der Seite „Netzwerk“ an (Kennwort verschlüsselt).
func (e *esppdhEnvT) addWLAN(t *testing.T, ssid, pw, segment string) string {
	t.Helper()
	enc, err := encryptSecret(e.h.networkKey(), pw)
	if err != nil {
		t.Fatal(err)
	}
	var seg *string
	if segment != "" {
		seg = &segment
	}
	var id string
	if err := e.pool.QueryRow(context.Background(), `INSERT INTO net_wlans (ssid, password_enc, segment_id) VALUES ($1, $2, $3::uuid) RETURNING id::text`,
		ssid, enc, seg).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM net_wlans WHERE id = $1::uuid`, id) })
	return id
}

// addSegment legt ein Netz der Seite „Netzwerk“ an.
func (e *esppdhEnvT) addSegment(t *testing.T, name, subnet string) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `INSERT INTO net_segments (name, subnet) VALUES ($1, $2) RETURNING id::text`, name, subnet).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM net_segments WHERE id = $1::uuid`, id) })
	return id
}

// form: Formular „Neues Gerät“ mit Vorgaben; extra überschreibt.
func (e *esppdhEnvT) form(slug string, extra url.Values) url.Values {
	v := url.Values{"name": {"ESP-Gerät " + slug}, "slug": {slug}, "infrastructure_id": {e.infra},
		"connection_id": {e.conn}, "board_key": {"esp32dev"}, "net": {e.wlan}}
	for k, vals := range extra {
		v[k] = vals
	}
	return v
}

// create legt ein Gerät über das Formular an und liefert seine ID.
func (e *esppdhEnvT) create(t *testing.T, slug string, extra url.Values) string {
	t.Helper()
	w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form(slug, extra))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Gerät %s anlegen: %d %s", slug, w.Code, w.Body.String())
	}
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT id::text FROM esppdh_devices WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/import/esppdh/"+id) {
		t.Errorf("Weiterleitung nach dem Anlegen: %s", loc)
	}
	return id
}

// mqttPW: entschlüsseltes MQTT-Kennwort eines Geräts (nur im Test).
func (e *esppdhEnvT) mqttPW(t *testing.T, id string) string {
	t.Helper()
	var enc string
	if err := e.pool.QueryRow(context.Background(), `SELECT mqtt_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	pw, err := decryptSecret(e.h.esppdhKey(), enc)
	if err != nil || pw == "" {
		t.Fatalf("MQTT-Kennwort: %v", err)
	}
	return pw
}

// esppdhAddBlock hängt einen Baustein an das Formular (id "" = neu).
func esppdhAddBlock(v url.Values, idx, id, kind string, params map[string]string) {
	v.Add("blk", idx)
	v.Set("blk_id_"+idx, id)
	v.Set("blk_kind_"+idx, kind)
	for k, p := range params {
		v.Set("blk_"+idx+"_"+k, p)
	}
}

// saveForm: Formular der Detailseite mit den gespeicherten Werten des Geräts.
func (e *esppdhEnvT) saveForm(name string) url.Values {
	return url.Values{"name": {name}, "infrastructure_id": {e.infra}, "connection_id": {e.conn}, "board_key": {"esp32dev"}, "net": {e.wlan}}
}

type esppdhBlockRow struct {
	ID, Kind string
	Nr, Pos  int
	Params   map[string]string
}

func (e *esppdhEnvT) blocks(t *testing.T, id string) []esppdhBlockRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT id::text, kind, nr, position, params FROM esppdh_blocks WHERE device_id = $1::uuid ORDER BY position`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []esppdhBlockRow
	for rows.Next() {
		var b esppdhBlockRow
		var raw []byte
		if err := rows.Scan(&b.ID, &b.Kind, &b.Nr, &b.Pos, &raw); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &b.Params)
		out = append(out, b)
	}
	return out
}

func (e *esppdhEnvT) count(t *testing.T, slug string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM esppdh_devices WHERE slug = $1`, slug).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Ablauf: anlegen (Kennwörter verschlüsselt, nie auf einer Seite), Bausteine speichern mit
// Prüfung (falscher Pin → Meldung, nichts gespeichert), feste Bausteinnummern, gleiche
// Kennung zweimal, sperren, neues Kennwort, löschen.
func TestESPPDHFlow(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	ctx := context.Background()
	slug := "flow-" + e.sfx
	page := esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", esppdhUser, nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "esp-test-"+e.sfx) || !strings.Contains(page.Body.String(), "pdh-esp-"+e.sfx) {
		t.Fatalf("Liste: %d – Verbindung oder WLAN fehlt zur Auswahl", page.Code)
	}

	id := e.create(t, slug, nil)
	var mqttEnc, apEnc string
	var wired, locked, useTLS bool
	if err := e.pool.QueryRow(ctx, `SELECT mqtt_password_enc, ap_password_enc, wired, locked, use_tls FROM esppdh_devices WHERE id = $1::uuid`, id).
		Scan(&mqttEnc, &apEnc, &wired, &locked, &useTLS); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(mqttEnc, "v1:") || !strings.HasPrefix(apEnc, "v1:") || wired || locked || !useTLS {
		t.Fatalf("Zeile: mqtt %q ap %q wired %v locked %v tls %v", mqttEnc[:min(4, len(mqttEnc))], apEnc[:min(4, len(apEnc))], wired, locked, useTLS)
	}
	pw := e.mqttPW(t, id)
	apPW, _ := decryptSecret(e.h.esppdhKey(), apEnc)
	if len(pw) < 24 || len(apPW) < 24 || pw == apPW {
		t.Errorf("Kennwörter zu kurz oder gleich: %d %d", len(pw), len(apPW))
	}

	detail := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil)
	body := detail.Body.String()
	if detail.Code != http.StatusOK {
		t.Fatalf("Detailseite: %d %s", detail.Code, body)
	}
	for _, want := range []string{slug, "esppdh-" + slug, "esppdh/" + slug + "/", "ESPPDH baut auf ESPHome auf", apPW,
		"mqtt-" + e.sfx + ".pdh-test.invalid", "/import/esppdh/" + id + "/config.yaml", "/import/esppdh/" + id + "/delete"} {
		if !strings.Contains(body, want) {
			t.Errorf("Detailseite ohne %q", want)
		}
	}
	list := esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", esppdhUser, nil).Body.String()
	if !strings.Contains(list, "ESP-Gerät "+slug) || !strings.Contains(list, "/import/esppdh/"+id) {
		t.Error("Gerät fehlt in der Liste")
	}
	for name, s := range map[string]string{"Liste": list, "Detailseite": body} {
		for _, secret := range []string{pw, mqttEnc, apEnc, e.wlPW} {
			if strings.Contains(s, secret) {
				t.Fatalf("%s zeigt ein Kennwort oder seinen Schlüsseltext", name)
			}
		}
	}
	// nur Ansehen: kein Notzugang-Kennwort, keine Knöpfe zum Ändern
	e.allow = map[string]bool{permESPView: true}
	viewOnly := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	if strings.Contains(viewOnly, apPW) || strings.Contains(viewOnly, `action="/import/esppdh/`+id+`/lock"`) || strings.Contains(viewOnly, "/import/esppdh/"+id+"/delete") {
		t.Error("ohne esppdh.edit: Notzugang-Kennwort oder Knöpfe sichtbar")
	}
	e.allow = map[string]bool{permESPView: true, permESPEdit: true}
	if b := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String(); strings.Contains(b, "/import/esppdh/"+id+"/delete") {
		t.Error("Löschknopf ohne esppdh.admin")
	}
	e.allow[permESPAdmin] = true

	// falscher Pin: Meldung von ValidateBlocks, nichts gespeichert (auch nicht der neue Name)
	v := e.saveForm("Umbenannt " + e.sfx)
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO0", "pulses": "1000"})
	w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "GPIO0 ist auf dieser Platine nicht nutzbar") {
		t.Fatalf("falscher Pin: %d – Meldung fehlt", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Umbenannt "+e.sfx) {
		t.Error("das Formular verliert die Eingaben")
	}
	var name string
	_ = e.pool.QueryRow(ctx, `SELECT name FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&name)
	if name != "ESP-Gerät "+slug || len(e.blocks(t, id)) != 0 {
		t.Fatalf("trotz Fehler gespeichert: %q, %d Bausteine", name, len(e.blocks(t, id)))
	}

	// zwei S0-Zähler: Nummern 1 und 2
	v = e.saveForm("Presse " + e.sfx)
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4", "pulses": "1000"})
	esppdhAddBlock(v, "1", "", "s0", map[string]string{"pin": "GPIO5", "pulses": "500"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("speichern: %d %s", w.Code, w.Body.String())
	}
	bl := e.blocks(t, id)
	if len(bl) != 2 || bl[0].Nr != 1 || bl[1].Nr != 2 || bl[1].Params["pulses"] != "500" {
		t.Fatalf("Bausteine: %+v", bl)
	}
	// den ersten löschen: der zweite behält Nummer 2; ein neuer bekommt die nächste freie
	v = e.saveForm("Presse " + e.sfx)
	esppdhAddBlock(v, "7", bl[1].ID, "s0", map[string]string{"pin": "GPIO5", "pulses": "250"})
	esppdhAddBlock(v, "8", "", "counter", map[string]string{"pin": "GPIO13"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("speichern: %d %s", w.Code, w.Body.String())
	}
	bl2 := e.blocks(t, id)
	if len(bl2) != 2 || bl2[0].ID != bl[1].ID || bl2[0].Nr != 2 || bl2[0].Params["pulses"] != "250" || bl2[1].Kind != "counter" || bl2[1].Nr != 1 {
		t.Fatalf("nach dem Löschen: %+v", bl2)
	}
	// umsortieren behält die Nummern; die Art eines bestehenden Bausteins bleibt
	v = e.saveForm("Presse " + e.sfx)
	esppdhAddBlock(v, "0", bl2[1].ID, "s0", map[string]string{"pin": "GPIO13"})
	esppdhAddBlock(v, "1", bl2[0].ID, "s0", map[string]string{"pin": "GPIO5", "pulses": "250"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("umsortieren: %d %s", w.Code, w.Body.String())
	}
	if bl3 := e.blocks(t, id); len(bl3) != 2 || bl3[0].ID != bl2[1].ID || bl3[0].Kind != "counter" || bl3[0].Nr != 1 || bl3[1].Nr != 2 {
		t.Fatalf("nach dem Umsortieren: %+v", bl3)
	}

	// gleiche Kennung zweimal: Meldung, keine zweite Zeile
	w = esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form(slug, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "schon vergeben") || e.count(t, slug) != 1 {
		t.Errorf("doppelte Kennung: %d, %d Zeilen", w.Code, e.count(t, slug))
	}
	// ungültige Kennung
	w = esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form("Halle 1", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "3 bis 32 Zeichen") {
		t.Errorf("ungültige Kennung: %d", w.Code)
	}

	// sperren und entsperren
	for _, c := range []struct {
		val  string
		want bool
	}{{"1", true}, {"0", false}} {
		if w := esppdhDo(e.h.EsppdhLockWeb, "POST", "/x", id, esppdhUser, url.Values{"locked": {c.val}}); w.Code != http.StatusSeeOther {
			t.Fatalf("sperren=%s: %d", c.val, w.Code)
		}
		_ = e.pool.QueryRow(ctx, `SELECT locked FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&locked)
		if locked != c.want {
			t.Errorf("sperren=%s: gesperrt %v", c.val, locked)
		}
	}
	// neues MQTT-Kennwort
	if w := esppdhDo(e.h.EsppdhPasswordWeb, "POST", "/x", id, esppdhUser, url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("neues Kennwort: %d", w.Code)
	}
	if pw2 := e.mqttPW(t, id); pw2 == pw {
		t.Error("Kennwort unverändert")
	}
	// löschen
	if w := esppdhDo(e.h.EsppdhDeleteWeb, "POST", "/x", id, esppdhUser, url.Values{}); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/import/esppdh") {
		t.Fatalf("löschen: %d %s", w.Code, w.Header().Get("Location"))
	}
	if e.count(t, slug) != 0 || len(e.blocks(t, id)) != 0 {
		t.Error("Gerät oder Bausteine nach dem Löschen noch da")
	}
	if w := esppdhDo(e.h.EsppdhDevicePage, "GET", "/x", id, esppdhUser, nil); w.Code != http.StatusNotFound {
		t.Errorf("gelöschtes Gerät: %d", w.Code)
	}
}

// Die Konfiguration enthält Kennwörter nur als Verweis.
func TestESPPDHConfigDownload(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	slug := "cfg-" + e.sfx
	id := e.create(t, slug, nil)
	// ohne Baustein lässt sie sich nicht erzeugen
	if w := esppdhDo(e.h.EsppdhConfigWeb, "GET", "/x", id, esppdhUser, nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Baustein") {
		t.Errorf("ohne Baustein: %d %s", w.Code, w.Body.String())
	}
	v := e.saveForm("Zähler " + e.sfx)
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/x", id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("speichern: %d %s", w.Code, w.Body.String())
	}
	w := esppdhDo(e.h.EsppdhConfigWeb, "GET", "/x", id, esppdhUser, nil)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("Konfiguration: %d %s", w.Code, body)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "esppdh-"+slug+".yaml") {
		t.Errorf("Content-Disposition %q", cd)
	}
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Type"), "yaml") {
		t.Errorf("Kopfzeilen: %v", w.Header())
	}
	for _, want := range []string{"!secret mqtt_password", "!secret wifi_password", "!secret ap_password", "esppdh/" + slug + "/s0-1/"} {
		if !strings.Contains(body, want) {
			t.Errorf("Konfiguration ohne %q", want)
		}
	}
	var apEnc string
	_ = e.pool.QueryRow(context.Background(), `SELECT ap_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&apEnc)
	apPW, _ := decryptSecret(e.h.esppdhKey(), apEnc)
	for _, secret := range []string{e.mqttPW(t, id), apPW, e.wlPW} {
		if secret == "" || strings.Contains(body, secret) {
			t.Fatal("Kennwort in der Konfiguration")
		}
	}
}

// Rechte mit Abteilung: esppdh.view und esppdh.edit nur in Abteilung A – ein Gerät an einer
// Anlage der Abteilung B ist weder zu sehen noch zu ändern.
func TestESPPDHDepartment(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	e.h.hasPermHook = nil // echter Rechtedienst
	ctx := context.Background()
	pool := e.pool
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, q, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	depA := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ESP-A "+e.sfx)
	depB := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ESP-B "+e.sfx)
	infraA := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'plant', $2::uuid) RETURNING id::text`, "ESP-Anlage A "+e.sfx, depA)
	infraB := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'plant', $2::uuid) RETURNING id::text`, "ESP-Anlage B "+e.sfx, depB)
	role := "esp" + e.sfx
	if _, err := pool.Exec(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'ESPPDH', 40)`, role); err != nil {
		t.Fatal(err)
	}
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'E', 'E', $2, '', '') RETURNING id::text`, "esp"+e.sfx, role)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM esppdh_devices WHERE infrastructure_id IN ($1::uuid, $2::uuid)`, infraA, infraB)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, user)
		_, _ = pool.Exec(c, `DELETE FROM roles WHERE key = $1`, role)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, infraA, infraB)
		_, _ = pool.Exec(c, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid)`, depA, depB)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
		SELECT r.id, p.id, $2::uuid FROM roles r, permissions p WHERE r.key = $1 AND p.key IN ('esppdh.view', 'esppdh.edit')`, role, depA); err != nil {
		t.Fatal(err)
	}
	rb := rbac.NewService(rbac.NewRepository(pool))
	if err := rb.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	e.h.rbac = rb
	// Geräte ohne Ersteller (niemand ist daran beteiligt)
	insert := func(slug, infra string) string {
		t.Helper()
		devID, err := e.h.esppdhInsert(ctx, esppdhDevice{Slug: slug, Name: "ESP-Gerät " + slug, InfraID: infra, ConnID: e.conn,
			BoardKey: "esp32dev", WLANID: e.wlan, UseTLS: true}, "mqtt-pw-"+slug, "ap-pw-"+slug+"-lang", "")
		if err != nil {
			t.Fatal(err)
		}
		return devID
	}
	devA, devB := insert("a-"+e.sfx, infraA), insert("b-"+e.sfx, infraB)
	u := &users.User{ID: user, FirstName: "E", LastName: "E", Role: users.Role(role)}

	list := esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", u, nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "ESP-Gerät a-"+e.sfx) {
		t.Fatalf("Liste: %d – Gerät der eigenen Abteilung fehlt", list.Code)
	}
	if strings.Contains(list.Body.String(), "ESP-Gerät b-"+e.sfx) {
		t.Error("Liste zeigt ein Gerät der fremden Abteilung")
	}
	if w := esppdhDo(e.h.EsppdhDevicePage, "GET", "/x", devA, u, nil); w.Code != http.StatusOK {
		t.Errorf("eigene Abteilung: %d", w.Code)
	}
	for _, rt := range esppdhRoutes(e.h) {
		if rt.name == "Liste" || rt.name == "Anlegen" {
			continue
		}
		if w := esppdhDo(rt.fn, rt.method, "/x", devB, u, url.Values{"name": {"x"}, "locked": {"1"}}); w.Code != http.StatusForbidden {
			t.Errorf("%s an fremder Abteilung: %d", rt.name, w.Code)
		}
	}
	// löschen nur Bereichsadministrator – auch in der eigenen Abteilung nicht
	if w := esppdhDo(e.h.EsppdhDeleteWeb, "POST", "/x", devA, u, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("löschen ohne esppdh.admin: %d", w.Code)
	}
	// anlegen an einer Anlage der fremden Abteilung: verboten; in der eigenen: erlaubt
	f := e.form("c-"+e.sfx, url.Values{"infrastructure_id": {infraB}})
	if w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", u, f); w.Code != http.StatusForbidden || e.count(t, "c-"+e.sfx) != 0 {
		t.Errorf("anlegen in fremder Abteilung: %d", w.Code)
	}
	f.Set("infrastructure_id", infraA)
	if w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", u, f); w.Code != http.StatusSeeOther {
		t.Errorf("anlegen in eigener Abteilung: %d %s", w.Code, w.Body.String())
	}
	// ein Gerät der eigenen Abteilung lässt sich nicht in die fremde verschieben
	v := url.Values{"name": {"verschoben"}, "infrastructure_id": {infraB}, "connection_id": {e.conn}, "board_key": {"esp32dev"}, "net": {e.wlan}}
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/x", devA, u, v); w.Code != http.StatusForbidden {
		t.Errorf("in fremde Abteilung verschieben: %d", w.Code)
	}
	var infra string
	_ = pool.QueryRow(ctx, `SELECT infrastructure_id::text FROM esppdh_devices WHERE id = $1::uuid`, devA).Scan(&infra)
	if infra != infraA {
		t.Error("Gerät wurde verschoben")
	}
}

// esppdhDial verbindet einen MQTT-Client (3.1.1, ohne Wiederverbinden) wie ein Gerät: Client-ID
// gleich Benutzername. Der Kanal meldet das Ende der Verbindung.
func esppdhDial(t *testing.T, port int, user, pass string) (paho.Client, <-chan struct{}, error) {
	t.Helper()
	lost := make(chan struct{}, 1)
	o := paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).SetClientID(user).
		SetUsername(user).SetPassword(pass).SetProtocolVersion(4).
		SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(3 * time.Second).
		SetConnectionLostHandler(func(paho.Client, error) { lost <- struct{}{} })
	c := paho.NewClient(o)
	tok := c.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		return nil, nil, errors.New("Zeitüberschreitung beim Verbinden")
	}
	if err := tok.Error(); err != nil {
		return nil, nil, err
	}
	t.Cleanup(func() { c.Disconnect(50) })
	return c, lost, nil
}

func esppdhExpectLost(t *testing.T, lost <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: Verbindung endet nicht binnen 2 s", what)
	}
}

// Gerätezugänge am echten Broker: anlegen → Anmeldung geht; sperren, neues Kennwort und
// löschen trennen sofort; nach einem Neustart des PDH (neue Broker-Verwaltung) gehen die
// Zugänge aus der Datenbank wieder.
func TestESPPDHBrokerSync(t *testing.T) {
	port := esppdhFreePort(t)
	e := newEsppdhEnv(t, esppdhBrokerCfg(port))
	ctx := context.Background()
	e.h.reconcileMqttBroker(e.conn, "import", "mqtt", true, e.cfg)
	if !e.h.mqttBrokers.IsRunning(e.conn) {
		t.Fatalf("Broker läuft nicht: %s", e.h.mqttBrokers.LastError(e.conn))
	}
	slug := "sync-" + e.sfx
	id := e.create(t, slug, nil)
	user, pw := "esppdh-"+slug, e.mqttPW(t, id)
	_, lost, err := esppdhDial(t, port, user, pw)
	if err != nil {
		t.Fatalf("Gerät meldet sich nach dem Anlegen nicht an: %v", err)
	}
	if !e.h.mqttBrokers.DeviceOnline(e.conn, user) {
		t.Error("DeviceOnline meldet das Gerät nicht")
	}
	if _, _, err := esppdhDial(t, port, user, "falsch"); err == nil {
		t.Error("falsches Kennwort angenommen")
	}

	// sperren trennt; Wiederanmeldung scheitert; entsperren lässt es wieder zu
	esppdhDo(e.h.EsppdhLockWeb, "POST", "/x", id, esppdhUser, url.Values{"locked": {"1"}})
	esppdhExpectLost(t, lost, "gesperrt")
	if _, _, err := esppdhDial(t, port, user, pw); err == nil {
		t.Fatal("gesperrtes Gerät meldet sich an")
	}
	esppdhDo(e.h.EsppdhLockWeb, "POST", "/x", id, esppdhUser, url.Values{"locked": {"0"}})
	if _, _, err := esppdhDial(t, port, user, pw); err != nil {
		t.Fatalf("entsperrt: %v", err)
	}

	// Neustart des PDH: neue Broker-Verwaltung ohne Zugänge im Speicher
	e.h.mqttBrokers.StopAll()
	e.h.mqttBrokers = mqttbroker.NewManager()
	e.h.reconcileMqttBroker(e.conn, "import", "mqtt", true, e.cfg)
	if _, _, err := esppdhDial(t, port, user, pw); err != nil {
		t.Fatalf("nach reconcileMqttBroker: %v", err)
	}
	e.h.mqttBrokers.StopAll()
	e.h.mqttBrokers = mqttbroker.NewManager()
	e.h.StartEnabledMqttBrokers(ctx)
	_, lost, err = esppdhDial(t, port, user, pw)
	if err != nil {
		t.Fatalf("nach StartEnabledMqttBrokers: %v", err)
	}

	// neues Kennwort: trennt; das alte gilt nicht mehr, das neue schon
	esppdhDo(e.h.EsppdhPasswordWeb, "POST", "/x", id, esppdhUser, url.Values{})
	esppdhExpectLost(t, lost, "neues Kennwort")
	if _, _, err := esppdhDial(t, port, user, pw); err == nil {
		t.Fatal("altes Kennwort gilt noch")
	}
	pw = e.mqttPW(t, id)
	_, lost, err = esppdhDial(t, port, user, pw)
	if err != nil {
		t.Fatalf("neues Kennwort: %v", err)
	}

	// löschen: trennt; Anmeldung scheitert
	esppdhDo(e.h.EsppdhDeleteWeb, "POST", "/x", id, esppdhUser, url.Values{})
	esppdhExpectLost(t, lost, "gelöscht")
	if _, _, err := esppdhDial(t, port, user, pw); err == nil {
		t.Fatal("gelöschtes Gerät meldet sich an")
	}
}

// Rechte aus der Migration: Rollen und Einzelrechte mit import.write bekommen esppdh.view und
// esppdh.edit (mit deren Abteilung); esppdh.admin nur Rollen, die dazu system.server_config
// überall haben. Geprüft am Bestand und – mit einem zweiten Lauf der Migration – an neuen Rollen.
func TestESPPDHMigrationGrants(t *testing.T) {
	_, pool, _ := itNetPageEnv(t)
	ctx := context.Background()
	var perms int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key IN ('esppdh.view', 'esppdh.edit', 'esppdh.admin') AND category = 'Daten & Schnittstellen'`).Scan(&perms)
	if perms != 3 {
		t.Fatalf("Rechte: %d (3 erwartet)", perms)
	}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var dep string
	if err := pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ESP-Mig "+sfx).Scan(&dep); err != nil {
		t.Fatal(err)
	}
	roleW, roleWS := "espw"+sfx, "espws"+sfx
	var user string
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM users WHERE username = $1`, "espmig"+sfx)
		_, _ = pool.Exec(c, `DELETE FROM roles WHERE key IN ($1, $2)`, roleW, roleWS)
		_, _ = pool.Exec(c, `DELETE FROM departments WHERE id = $1::uuid`, dep)
	})
	grant := func(role, perm string, dept any) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
			SELECT r.id, p.id, $3::uuid FROM roles r, permissions p WHERE r.key = $1 AND p.key = $2`, role, perm, dept); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []string{roleW, roleWS} {
		if _, err := pool.Exec(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'ESP-Migration', 40)`, r); err != nil {
			t.Fatal(err)
		}
	}
	grant(roleW, "import.write", dep)
	grant(roleWS, "import.write", nil)
	grant(roleWS, "system.server_config", nil)
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'E', 'E', 'viewer', '', '') RETURNING id::text`, "espmig"+sfx).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
		SELECT $1::uuid, p.id, true, $2::uuid FROM permissions p WHERE p.key = 'import.write'`, user, dep); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("migrations", "179_esppdh.up.sql")) // itNetPageEnv steht im Wurzelordner
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(src)); err != nil {
		t.Fatalf("Migration noch einmal: %v", err)
	}
	has := func(role, perm string) (bool, string) {
		var d *string
		err := pool.QueryRow(ctx, `SELECT rp.department_id::text FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
			JOIN permissions p ON p.id = rp.permission_id WHERE r.key = $1 AND p.key = $2`, role, perm).Scan(&d)
		if err != nil {
			return false, ""
		}
		if d == nil {
			return true, ""
		}
		return true, *d
	}
	for _, perm := range []string{permESPView, permESPEdit} {
		if ok, d := has(roleW, perm); !ok || d != dep {
			t.Errorf("Rolle mit import.write in einer Abteilung: %s %v in %q", perm, ok, d)
		}
		if ok, d := has(roleWS, perm); !ok || d != "" {
			t.Errorf("Rolle mit import.write überall: %s %v in %q", perm, ok, d)
		}
		var granted bool
		var d *string
		if err := pool.QueryRow(ctx, `SELECT up.granted, up.department_id::text FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
			WHERE up.user_id = $1::uuid AND p.key = $2`, user, perm).Scan(&granted, &d); err != nil || !granted || d == nil || *d != dep {
			t.Errorf("Einzelrecht %s: %v %v %v", perm, granted, d, err)
		}
	}
	if ok, _ := has(roleW, permESPAdmin); ok {
		t.Error("esppdh.admin ohne system.server_config vergeben")
	}
	if ok, d := has(roleWS, permESPAdmin); !ok || d != "" {
		t.Errorf("esppdh.admin mit system.server_config: %v %q", ok, d)
	}
	// Bestand: keine Rolle mit import.write ohne die neuen Rechte, kein esppdh.admin ohne beide Rechte überall
	var missing, wrong int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id AND o.key = 'import.write'
		CROSS JOIN (VALUES ('esppdh.view'), ('esppdh.edit')) k(key)
		WHERE NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions p ON p.id = x.permission_id AND p.key = k.key WHERE x.role_id = rp.role_id)`).Scan(&missing)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id AND p.key = 'esppdh.admin'
		WHERE NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions o ON o.id = x.permission_id AND o.key = 'import.write' WHERE x.role_id = rp.role_id AND x.department_id IS NULL)
		   OR NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions o ON o.id = x.permission_id AND o.key = 'system.server_config' WHERE x.role_id = rp.role_id AND x.department_id IS NULL)`).Scan(&wrong)
	if missing != 0 || wrong != 0 {
		t.Errorf("Rollen mit import.write ohne esppdh.view/edit: %d, esppdh.admin ohne import.write und system.server_config: %d", missing, wrong)
	}
}

// Schalter aus: Hinweis mit Verweis auf die Server-Einstellungen, Anlegen 409. Im
// Docker-Betrieb ein eigener Hinweis.
func TestESPPDHDisabled(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	slug := "off-" + e.sfx
	esppdhSetSwitch(t, e.h, "0")
	page := esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", esppdhUser, nil).Body.String()
	if !strings.Contains(page, "ESPPDH ist ausgeschaltet") || !strings.Contains(page, `href="/admin/server-config?tab=esppdh"`) {
		t.Error("Hinweis „ausgeschaltet“ mit Verweis fehlt")
	}
	if w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form(slug, nil)); w.Code != http.StatusConflict || e.count(t, slug) != 0 {
		t.Errorf("anlegen bei ausgeschaltetem ESPPDH: %d", w.Code)
	}
	esppdhSetSwitch(t, e.h, "1")
	esppdhDockerHook = func() bool { return true } // esppdhTestEnv stellt ihn zurück
	page = esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", esppdhUser, nil).Body.String()
	if !strings.Contains(page, "Im Docker-Betrieb gibt es ESPPDH nicht") || strings.Contains(page, "ESPPDH ist ausgeschaltet") {
		t.Error("Docker-Hinweis fehlt")
	}
	if w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form(slug, nil)); w.Code != http.StatusConflict || e.count(t, slug) != 0 {
		t.Errorf("anlegen im Docker-Betrieb: %d", w.Code)
	}
	// eingeschaltet, aber ohne Umgebung: Hinweis, Anlegen geht
	esppdhDockerHook = func() bool { return false }
	page = esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", esppdhUser, nil).Body.String()
	if !strings.Contains(page, "noch nicht eingerichtet") {
		t.Error("Hinweis auf die fehlende Umgebung fehlt")
	}
	e.create(t, slug, nil)
}

// Nur Verbindungen mit eingebautem Broker stehen zur Wahl.
func TestESPPDHOnlyIntegratedBroker(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	ext := e.addConn(t, "esp-fremd-"+e.sfx, map[string]string{"broker_mode": "external", "ext_broker_host": "10.0.0.5", "ext_broker_port": "1883"})
	page := esppdhDo(e.h.EsppdhPage, "GET", "/import/esppdh", "", esppdhUser, nil).Body.String()
	if strings.Contains(page, "esp-fremd-"+e.sfx) || !strings.Contains(page, "esp-test-"+e.sfx) {
		t.Error("Auswahl der Verbindungen: fremder Broker steht zur Wahl oder eingebauter fehlt")
	}
	slug := "ext-" + e.sfx
	w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form(slug, url.Values{"connection_id": {ext}}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "eingebauten Broker") || e.count(t, slug) != 0 {
		t.Errorf("fremder Broker: %d, %d Zeilen", w.Code, e.count(t, slug))
	}
	// beim Speichern ebenso
	id := e.create(t, "int-"+e.sfx, nil)
	v := url.Values{"name": {"x"}, "infrastructure_id": {e.infra}, "connection_id": {ext}, "board_key": {"esp32dev"}, "net": {e.wlan}}
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/x", id, esppdhUser, v); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "eingebauten Broker") {
		t.Errorf("speichern mit fremdem Broker: %d", w.Code)
	}
}

// Broker mit TLS-Port: ESP32 verbindet über TLS mit dem Namen; ESP8266 kann kein TLS – die
// Seite sagt warum und bietet „ohne TLS“ an, das Gerät nutzt dann den Port ohne TLS.
func TestESPPDHPlainForESP8266(t *testing.T) {
	name := "mqtts-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "") + ".pdh-test.invalid"
	e := newEsppdhEnv(t, esppdhBrokerCfg(1883, mqttHostKey, name, mqttTLSPortKey, "8883", "int_websocket_port", "8083"))
	ctx := context.Background()
	idTLS := e.create(t, "tls-"+e.sfx, nil)
	d, err := e.h.esppdhBuildInput(ctx, idTLS, esppdhTestHost)
	if err != nil || !d.BrokerTLS || d.BrokerPort != 8883 || d.BrokerHost != name || d.MQTTUser != "esppdh-tls-"+e.sfx {
		t.Fatalf("ESP32 mit TLS: %+v %v", d, err)
	}
	slug := "d1-" + e.sfx
	w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form(slug, url.Values{"board_key": {"d1-mini"}}))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "ESP8266") || !strings.Contains(body, "ohne TLS (Port 1883)") || !strings.Contains(body, `name="plain"`) || e.count(t, slug) != 0 {
		t.Fatalf("ESP8266 mit TLS-Broker: %d, %d Zeilen", w.Code, e.count(t, slug))
	}
	id := e.create(t, slug, url.Values{"board_key": {"d1-mini"}, "plain": {"on"}})
	d, err = e.h.esppdhBuildInput(ctx, id, esppdhTestHost)
	if err != nil || d.BrokerTLS || d.BrokerPort != 1883 {
		t.Fatalf("ESP8266 ohne TLS: %+v %v", d, err)
	}
	if page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/x", id, esppdhUser, nil).Body.String(); !strings.Contains(page, "sendet unverschlüsselt") {
		t.Error("Hinweis „sendet unverschlüsselt“ fehlt")
	}
	// ESP32 kann TLS: „ohne TLS“ wird nicht übernommen
	idPlain32 := e.create(t, "p32-"+e.sfx, url.Values{"plain": {"on"}})
	if d, err := e.h.esppdhBuildInput(ctx, idPlain32, esppdhTestHost); err != nil || !d.BrokerTLS {
		t.Errorf("ESP32 mit „ohne TLS“: %+v %v", d, err)
	}
}

// Hinweise der Detailseite: Broker ohne TLS-Port, „Retained Messages“ aus, Gerät in einem
// fremden Netz, Lizenz.
func TestESPPDHHints(t *testing.T) {
	e := newEsppdhEnv(t, esppdhBrokerCfg(1883, "int_retain_enabled", "false"))
	seg := e.addSegment(t, "ESP-Netz "+e.sfx, "192.0.2.0/24") // Dokumentationsnetz: der Server liegt nicht darin
	id := e.create(t, "eth-"+e.sfx, url.Values{"board_key": {"wt32-eth01"}, "net": {"wired"}, "segment_id": {seg}})
	page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/x", id, esppdhUser, nil).Body.String()
	for _, want := range []string{
		"Geräte senden unverschlüsselt",
		"Retained Messages erlauben", "abgewiesen", `href="/import/connections/` + e.conn + `"`,
		"Freigabe für 10.9.8.7 Port 1883 in der Firewall des Netzes „ESP-Netz " + e.sfx + "“ nötig",
		"ESPPDH baut auf ESPHome auf (GPLv3/MIT)",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Detailseite ohne %q", want)
		}
	}
	var wired bool
	var seg2 *string
	_ = e.pool.QueryRow(context.Background(), `SELECT wired, segment_id::text FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&wired, &seg2)
	if !wired || seg2 == nil || *seg2 != seg {
		t.Errorf("Kabel: %v %v", wired, seg2)
	}
	// Kabel nur bei Ethernet-Platine
	slug := "nocable-" + e.sfx
	w := esppdhDo(e.h.EsppdhCreateWeb, "POST", "/import/esppdh", "", esppdhUser, e.form(slug, url.Values{"net": {"wired"}}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "kein Ethernet") || e.count(t, slug) != 0 {
		t.Errorf("Kabel an ESP32 DevKit: %d", w.Code)
	}
}

// Eine Verbindung, an der noch Geräte hängen, lässt sich nicht löschen – mit verständlicher Meldung.
func TestESPPDHConnectionInUse(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	e.create(t, "use-"+e.sfx, nil)
	admin := &users.User{ID: pdhSystemUserID, FirstName: "Ines", LastName: "Test", Role: users.RoleAdmin} // import.write über die Rolle
	w := esppdhDo(e.h.ImportConnectionDeleteWeb, "POST", "/x", e.conn, admin, url.Values{})
	loc, _ := url.QueryUnescape(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || !strings.Contains(loc, "ESPPDH") {
		t.Errorf("löschen: %d %s", w.Code, loc)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM import_export_connections WHERE id = $1::uuid`, e.conn).Scan(&n)
	if n != 1 {
		t.Error("Verbindung trotz Geräten gelöscht")
	}
}
