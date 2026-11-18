package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh"
	"pdh/internal/core/users"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Firmware herunterladen und per USB flashen (esppdh_flash.go): Routen nur mit
// esppdh.edit am Gerät und nur für gelungene Läufe dieses Geräts, Manifest für ESP Web Tools,
// Knopf „Per USB flashen“ auf der Detailseite, ESP Web Tools lokal unter
// /static/vendor/esp-web-tools/.

const esppdhNoUSBText = "USB-Flash braucht Chrome oder Edge und eine https-Adresse des PDH – hier die Firmware herunterladen"

// esppdhDoF ruft eine Route eines Laufs auf (factory.bin, ota.bin, manifest.json).
func esppdhDoF(fn http.HandlerFunc, id, bid, file string, u *users.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/import/esppdh/"+id+"/firmware/"+bid+"/"+file, nil)
	req.Host = esppdhTestHost
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	rc.URLParams.Add("bid", bid)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	if u != nil {
		ctx = context.WithValue(ctx, "user", u)
	}
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

// esppdhFlashRoutes: die drei Routen eines Laufs.
func esppdhFlashRoutes(h *Handler) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{"factory.bin": h.EsppdhFactoryWeb, "ota.bin": h.EsppdhOTAWeb, "manifest.json": h.EsppdhManifestWeb}
}

// esppdhDoTools ruft eine Datei von ESP Web Tools ab (/static/vendor/esp-web-tools/<file>).
func esppdhDoTools(h *Handler, file string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/static/vendor/esp-web-tools/x", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("file", file)
	ctx := context.WithValue(context.WithValue(req.Context(), chi.RouteCtxKey, rc), "user", esppdhUser)
	w := httptest.NewRecorder()
	h.EsppdhWebToolsWeb(w, req.WithContext(ctx))
	return w
}

// buildableOn: Gerät auf einer Platine anlegen und mit einem S0-Baustein am Pin pin speichern.
func (e *esppdhEnvT) buildableOn(t *testing.T, slug, board, custom, pin string) string {
	t.Helper()
	extra := url.Values{"board_key": {board}}
	if custom != "" {
		extra.Set("custom_board", custom)
	}
	id := e.create(t, slug, extra)
	v := e.saveForm("Gerät " + slug)
	for k, vals := range extra {
		v[k] = vals
	}
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": pin, "pulses": "1000"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("Gerät %s speichern: %d %s", slug, w.Code, w.Body.String())
	}
	return id
}

// built: Bau einreihen und von der Attrappe gelingen lassen; liefert die Kennung des Laufs.
func (e *esppdhEnvT) built(t *testing.T, fb *esppdhFakeBuilder, id, tag string) string {
	t.Helper()
	bid := e.queue(t, id)
	if j := esppdhNextJob(t, fb); j.ID != bid {
		t.Fatalf("gebaut: %s, erwartet %s", j.ID, bid)
	}
	fb.gate(bid) <- esppdhBuildOK(tag)
	e.waitStatus(t, bid, "ok")
	return bid
}

// ── ohne Datenbank ───────────────────────────────────────────────────────

// Die Routen hängen am Router; ohne esppdh.edit lehnen sie ab, bevor sie die Datenbank fragen –
// esppdh.view allein reicht nicht (die Firmware enthält WLAN- und MQTT-Kennwort).
func TestFirmwareRoutes(t *testing.T) {
	want := map[string]bool{"/import/esppdh/{id}/firmware/{bid}/factory.bin": false, "/import/esppdh/{id}/firmware/{bid}/ota.bin": false,
		"/import/esppdh/{id}/firmware/{bid}/manifest.json": false, "/static/vendor/esp-web-tools/{file}": false}
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if _, ok := want[route]; ok && method == http.MethodGet {
			want[route] = true
		}
		return nil
	}
	if err := chi.Walk((&Handler{}).Routes(), walk); err != nil {
		t.Fatal(err)
	}
	for r, ok := range want {
		if !ok {
			t.Errorf("Route GET %s fehlt", r)
		}
	}
	b := "0198c0de-0000-7000-8000-000000000001"
	for _, have := range []string{"", permESPView} {
		h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p == have }}
		for name, fn := range esppdhFlashRoutes(h) {
			if w := esppdhDoF(fn, itNetTestID, b, name, esppdhUser); w.Code != http.StatusForbidden {
				t.Errorf("%s mit %q: %d", name, have, w.Code)
			}
		}
		if w := esppdhDoTools(h, "install-button.js"); w.Code != http.StatusForbidden {
			t.Errorf("ESP Web Tools mit %q: %d", have, w.Code)
		}
	}
	// ungültige Kennungen: 404
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p == permESPEdit }}
	for name, fn := range esppdhFlashRoutes(h) {
		for _, c := range [][2]string{{"x", b}, {itNetTestID, "x"}, {itNetTestID, "../" + b}} {
			if w := esppdhDoF(fn, c[0], c[1], name, esppdhUser); w.Code != http.StatusNotFound {
				t.Errorf("%s %v: %d", name, c, w.Code)
			}
		}
	}
}

// Chip-Familie für das Manifest je Platine; „Andere Platine“ hat keine (dort nur Herunterladen).
func TestEsppdhChipFamily(t *testing.T) {
	want := map[string]string{"esp32dev": "ESP32", "esp32-s3": "ESP32-S3", "esp32-c3": "ESP32-C3", "d1-mini": "ESP8266",
		"wt32-eth01": "ESP32", "olimex-poe": "ESP32", esppdh.BoardOther: "", "gibt-es-nicht": ""}
	for k, f := range want {
		if got := esppdhChipFamily(k); got != f {
			t.Errorf("%s: %q, erwartet %q", k, got, f)
		}
	}
	// jede Platine zur Auswahl (außer „Andere“) hat eine Familie, passend zur Plattform
	for _, b := range esppdh.Boards() {
		f := esppdhChipFamily(b.Key)
		switch {
		case b.Key == esppdh.BoardOther:
		case b.Platform == "esp8266" && f != "ESP8266", b.Platform == "esp32" && !strings.HasPrefix(f, "ESP32"):
			t.Errorf("Platine %s (%s): Familie %q", b.Key, b.Platform, f)
		}
	}
}

// ESP Web Tools liegt vollständig und unverändert im Programm (eingebettet aus web/static):
// Version, Lizenz, Prüfsummen; jede Datei, die eine andere nachlädt, findet sie im selben Ordner;
// nichts wird von einem fremden Rechner geladen. Ausgeliefert nur mit esppdh.edit, als
// JavaScript-Modul.
func TestESPWebToolsVendored(t *testing.T) {
	const dir = "web/static/vendor/esp-web-tools"
	read := func(name string) string {
		t.Helper()
		b, err := fs.ReadFile(pdh.Static, dir+"/"+name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return string(b)
	}
	version := read("VERSION")
	if !regexp.MustCompile(`^\d+\.\d+\.\d+\n$`).MatchString(version) {
		t.Errorf("VERSION: %q", version)
	}
	sums := read("SHA256SUMS")
	if !strings.Contains(sums, "esp-web-tools "+strings.TrimSpace(version)+",") || !strings.Contains(sums, "esp-web-tools-"+strings.TrimSpace(version)+".tgz") {
		t.Errorf("SHA256SUMS nennt eine andere Version als VERSION (%s)", strings.TrimSpace(version))
	}
	if !strings.Contains(read("LICENSE.txt"), "Apache License") || !strings.Contains(read("THIRD-PARTY-LICENSES.txt"), "BSD 3-Clause") {
		t.Error("Lizenzdateien fehlen oder sind unvollständig")
	}
	entries, err := fs.ReadDir(pdh.Static, dir)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, line := range strings.Split(sums, "\n") {
		if sum, name, ok := strings.Cut(line, "  "); ok && !strings.HasPrefix(line, "#") {
			listed[name] = sum
		}
	}
	var js []string
	for _, e := range entries {
		n := e.Name()
		if n == "SHA256SUMS" || n == "VERSION" || n == "THIRD-PARTY-LICENSES.txt" {
			continue
		}
		sum := sha256.Sum256([]byte(read(n)))
		if listed[n] != hex.EncodeToString(sum[:]) {
			t.Errorf("%s weicht vom npm-Paket ab (Prüfsumme %s, erwartet %q)", n, hex.EncodeToString(sum[:8]), listed[n])
		}
		delete(listed, n)
		if strings.HasSuffix(n, ".js") {
			js = append(js, n)
		}
	}
	for n := range listed {
		t.Errorf("%s steht in SHA256SUMS, fehlt aber", n)
	}
	if !slices.Contains(js, "install-button.js") || !strings.Contains(read("install-button.js"), `customElements.define("esp-web-install-button"`) {
		t.Fatal("install-button.js fehlt oder definiert esp-web-install-button nicht")
	}
	// Nachladen nur aus diesem Ordner; Verweise auf fremde Rechner sind nur Links und Texte
	importRe := regexp.MustCompile(`(?:\bimport\(|\bfrom|\bimport)\s*"([^"]+)"`)
	urlRe := regexp.MustCompile(`https?://([A-Za-z0-9.-]+)`)
	links := map[string]bool{
		"www.w3.org":           true, // Namensraum der SVG-Bilder
		"esphome.github.io":    true, // Text in der Browser-Konsole
		"my.home-assistant.io": true, // Link nur mit home_assistant_domain im Manifest – setzt das PDH nie
		"www.silabs.com":       true, // Treiber-Links im Fenster „kein Anschluss gewählt“
		"www.wch.cn":           true,
	}
	imports := 0
	for _, n := range js {
		src := read(n)
		for _, m := range importRe.FindAllStringSubmatch(src, -1) {
			imports++
			if !strings.HasPrefix(m[1], "./") || !slices.Contains(js, path.Base(m[1])) || strings.Contains(m[1][2:], "/") {
				t.Errorf("%s lädt %q – nicht im Ordner", n, m[1])
			}
		}
		for _, m := range urlRe.FindAllStringSubmatch(src, -1) {
			if !links[m[1]] {
				t.Errorf("%s nennt %s – unbekannter fremder Rechner", n, m[0])
			}
		}
		for _, bad := range []string{"@import", "url(http", "fonts.googleapis", "unpkg.com", "jsdelivr"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s enthält %q", n, bad)
			}
		}
	}
	if imports < len(js)-1 { // jede Datei außer install-button.js wird von einer anderen nachgeladen
		t.Errorf("nur %d Ladeanweisungen gefunden – Prüfung greift nicht", imports)
	}
	// Auslieferung: nur mit esppdh.edit, als Modul (MIME-Typ, nosniff), nur Skripte dieses Ordners
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p == permESPEdit }}
	for _, n := range js {
		w := esppdhDoTools(h, n)
		if w.Code != http.StatusOK || w.Body.String() != read(n) || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") ||
			w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d %q", n, w.Code, w.Header().Get("Content-Type"))
		}
	}
	for _, n := range []string{"VERSION", "LICENSE.txt", "SHA256SUMS", "../VERSION", "..", "gibt-es-nicht.js", "x/../install-button.js", ".js", "install-button.js.map"} {
		if w := esppdhDoTools(h, n); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", n, w.Code)
		}
	}
	// Bytes unverändert lassen: git darf die Zeilenenden nicht umwandeln
	ga, err := os.ReadFile(filepath.Join("..", "..", ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ga), "web/static/vendor/esp-web-tools/** -text") {
		t.Error(".gitattributes: Regel für web/static/vendor/esp-web-tools fehlt")
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

// Mit esppdh.view allein gibt es weder Firmware noch Manifest (403) und auf der Seite keinen
// Verweis darauf; mit esppdh.edit schon.
func TestFirmwareDownloadNeedsEdit(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	id := e.buildable(t, "dl-"+e.sfx)
	bid := e.built(t, fb, id, "dl")
	e.allow = map[string]bool{permESPView: true}
	for name, fn := range esppdhFlashRoutes(e.h) {
		if w := esppdhDoF(fn, id, bid, name, esppdhUser); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "KLARTEXT") {
			t.Errorf("%s nur mit esppdh.view: %d", name, w.Code)
		}
	}
	page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	if strings.Contains(page, "/firmware/") || strings.Contains(page, "esp-web-install-button") {
		t.Error("Seite verweist ohne esppdh.edit auf die Firmware")
	}
	e.allow = map[string]bool{permESPView: true, permESPEdit: true}
	for name, fn := range esppdhFlashRoutes(e.h) {
		if w := esppdhDoF(fn, id, bid, name, esppdhUser); w.Code != http.StatusOK {
			t.Errorf("%s mit esppdh.edit: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// Die Datei unter uploads/esppdh ist verschlüsselt und über /uploads nicht abrufbar; Klartext
// gibt es nur über die Route – als Download, nicht zwischengespeichert.
func TestFirmwareNotUnderUploads(t *testing.T) {
	e, fb, root := newEsppdhBuildEnv(t)
	e.start(t)
	slug := "nu-" + e.sfx
	id := e.buildable(t, slug)
	bid := e.built(t, fb, id, "nu")
	res := esppdhBuildOK("nu")
	uploads := UploadsHandler(filepath.Dir(root))
	for kind, want := range map[string][]byte{"factory": res.Factory, "ota": res.OTA} {
		name := bid + ".bin.enc"
		if kind == "ota" {
			name = bid + ".ota.bin.enc"
		}
		raw, err := os.ReadFile(filepath.Join(root, id, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "KLARTEXT") {
			t.Errorf("%s liegt im Klartext unter uploads/esppdh", name)
		}
		for _, p := range []string{"/uploads/esppdh/" + id + "/" + name, "/uploads/esppdh/" + id + "/"} {
			if w := uploadsGet(uploads, p); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "KLARTEXT") || strings.Contains(w.Body.String(), bid) {
				t.Errorf("%s: %d", p, w.Code)
			}
		}
		file, fn := "factory.bin", e.h.EsppdhFactoryWeb
		if kind == "ota" {
			file, fn = "ota.bin", e.h.EsppdhOTAWeb
		}
		w := esppdhDoF(fn, id, bid, file, esppdhUser)
		hd := w.Header()
		if w.Code != http.StatusOK || w.Body.String() != string(want) {
			t.Fatalf("%s: %d %q", file, w.Code, w.Body.String())
		}
		if hd.Get("Content-Type") != "application/octet-stream" || hd.Get("Content-Disposition") != `attachment; filename="esppdh-`+slug+`-`+kind+`.bin"` ||
			hd.Get("Cache-Control") != "no-store" || hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: Kopfzeilen %v", file, hd)
		}
	}
}

// Manifest für ESP Web Tools: Chip-Familie nach Platine, Pfad auf factory.bin desselben Laufs,
// ab Adresse 0. „Andere Platine“: kein Manifest, kein Knopf, nur Herunterladen. Ist die
// Konfiguration seit dem Bau geändert (z. B. andere Platine), passt die Firmware nicht mehr –
// dann kein Manifest und kein Knopf.
func TestManifest(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	for _, c := range []struct{ board, pin, family string }{
		{"esp32dev", "GPIO4", "ESP32"}, {"esp32-s3", "GPIO4", "ESP32-S3"}, {"esp32-c3", "GPIO4", "ESP32-C3"}, {"d1-mini", "D1", "ESP8266"},
	} {
		slug := "mf-" + strings.ReplaceAll(c.board, "-", "") + "-" + e.sfx
		id := e.buildableOn(t, slug, c.board, "", c.pin)
		bid := e.built(t, fb, id, c.board)
		w := esppdhDoF(e.h.EsppdhManifestWeb, id, bid, "manifest.json", esppdhUser)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.board, w.Code, w.Body.String())
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Kopfzeilen %v", c.board, w.Header())
		}
		var m struct {
			Name, Version string
			Improv        *int `json:"new_install_improv_wait_time"`
			Builds        []struct {
				ChipFamily string
				Parts      []struct {
					Path   string
					Offset *int
				}
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s: %v\n%s", c.board, err, w.Body.String())
		}
		if m.Name != "Gerät "+slug || m.Version == "" || m.Improv == nil || *m.Improv != 0 {
			t.Errorf("%s: name %q version %q improv %v", c.board, m.Name, m.Version, m.Improv)
		}
		if len(m.Builds) != 1 || m.Builds[0].ChipFamily != c.family || len(m.Builds[0].Parts) != 1 {
			t.Fatalf("%s: %s", c.board, w.Body.String())
		}
		p := m.Builds[0].Parts[0]
		if p.Path != "/import/esppdh/"+id+"/firmware/"+bid+"/factory.bin" || p.Offset == nil || *p.Offset != 0 {
			t.Errorf("%s: Teil %+v", c.board, p)
		}
		// derselbe Pfad liefert die Firmware dieses Laufs
		if fw := esppdhDoF(e.h.EsppdhFactoryWeb, id, bid, "factory.bin", esppdhUser); fw.Code != http.StatusOK || fw.Body.String() != "FACTORY-KLARTEXT-"+c.board {
			t.Errorf("%s: factory.bin %d %q", c.board, fw.Code, fw.Body.String())
		}
	}

	// Andere Platine: kein USB-Flash im Browser, Herunterladen geht
	other := e.buildableOn(t, "mf-other-"+e.sfx, esppdh.BoardOther, "nodemcu-32s", "GPIO4")
	ob := e.built(t, fb, other, "other")
	if w := esppdhDoF(e.h.EsppdhManifestWeb, other, ob, "manifest.json", esppdhUser); w.Code != http.StatusNotFound {
		t.Errorf("Andere Platine: Manifest %d", w.Code)
	}
	page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+other, other, esppdhUser, nil).Body.String()
	if strings.Contains(page, "<esp-web-install-button") || !strings.Contains(page, "/import/esppdh/"+other+"/firmware/"+ob+"/factory.bin") ||
		!strings.Contains(page, "kein USB-Flash im Browser") {
		t.Error("Andere Platine: Knopf statt nur Herunterladen")
	}

	// Konfiguration nach dem Bau geändert: kein Manifest (409), kein Knopf, Herunterladen bleibt
	id := e.buildable(t, "mf-stale-"+e.sfx)
	bid := e.built(t, fb, id, "stale")
	v := e.saveForm("Gerät mf-stale-" + e.sfx)
	v.Set("board_key", "esp32-s3")
	esppdhAddBlock(v, "0", e.blocks(t, id)[0].ID, "s0", map[string]string{"pin": "GPIO4", "pulses": "1000"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("Platine ändern: %d %s", w.Code, w.Body.String())
	}
	if w := esppdhDoF(e.h.EsppdhManifestWeb, id, bid, "manifest.json", esppdhUser); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "neu bauen") {
		t.Errorf("geänderte Konfiguration: Manifest %d %s", w.Code, w.Body.String())
	}
	page = esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	if strings.Contains(page, "<esp-web-install-button") || !strings.Contains(page, "/firmware/"+bid+"/factory.bin") {
		t.Error("geänderte Konfiguration: Knopf noch da oder Herunterladen fehlt")
	}
	if w := esppdhDoF(e.h.EsppdhFactoryWeb, id, bid, "factory.bin", esppdhUser); w.Code != http.StatusOK {
		t.Errorf("geänderte Konfiguration: Herunterladen %d", w.Code)
	}
}

// „Seit dem Bau geändert?“ hat eine Quelle für Karte „Firmware“, Knopf „Per USB flashen“ und
// Manifest. Lässt es sich nicht prüfen (hier: MQTT-Kennwort nicht lesbar), sagt die Karte das,
// statt still „passt“ zu zeigen, und Knopf und Manifest bleiben gesperrt.
func TestFirmwareCurrentOneSource(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	ctx := context.Background()
	id := e.buildable(t, "cs-"+e.sfx)
	bid := e.built(t, fb, id, "cs")
	page := func() string {
		t.Helper()
		return esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	}
	const changed, unknown = "Konfiguration seit dem letzten Bau geändert", "lässt sich gerade nicht prüfen"
	if p := page(); !strings.Contains(p, "<esp-web-install-button") || strings.Contains(p, changed) || strings.Contains(p, unknown) {
		t.Fatal("frisch gebaut: Knopf fehlt oder Hinweis falsch")
	}
	if w := esppdhDoF(e.h.EsppdhManifestWeb, id, bid, "manifest.json", esppdhUser); w.Code != http.StatusOK {
		t.Fatalf("frisch gebaut: Manifest %d", w.Code)
	}

	// MQTT-Kennwort nicht lesbar: nicht feststellbar → Karte sagt es, Knopf und Manifest gesperrt
	var enc string
	if err := e.pool.QueryRow(ctx, `SELECT mqtt_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE esppdh_devices SET mqtt_password_enc = 'kaputt' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	p := page()
	if strings.Contains(p, "<esp-web-install-button") || !strings.Contains(p, unknown) || strings.Contains(p, changed) {
		t.Error("Kennwort nicht lesbar: Knopf da oder Karte sagt nicht, dass es sich nicht prüfen lässt")
	}
	if w := esppdhDoF(e.h.EsppdhManifestWeb, id, bid, "manifest.json", esppdhUser); w.Code != http.StatusConflict {
		t.Errorf("Kennwort nicht lesbar: Manifest %d", w.Code)
	}
	d, err := e.h.esppdhLoad(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.h.esppdhFirmwareCurrent(ctx, d, "x", d.BuiltHash); ok || err == nil || !strings.Contains(err.Error(), "MQTT-Kennwort") {
		t.Errorf("esppdhFirmwareCurrent bei unlesbarem Kennwort: %v %v", ok, err)
	}
	if ok, err := e.h.esppdhFirmwareCurrent(ctx, d, "x", ""); ok || err == nil {
		t.Errorf("esppdhFirmwareCurrent ohne Prüfsumme: %v %v", ok, err)
	}

	// Kennwort wieder lesbar, Name geändert: Karte „geändert“, Knopf und Manifest gesperrt – ohne Fehler
	if _, err := e.pool.Exec(ctx, `UPDATE esppdh_devices SET mqtt_password_enc = $2, name = 'Anders ' || slug WHERE id = $1::uuid`, id, enc); err != nil {
		t.Fatal(err)
	}
	p = page()
	if strings.Contains(p, "<esp-web-install-button") || !strings.Contains(p, changed) || strings.Contains(p, unknown) {
		t.Error("geändert: Knopf da oder Hinweis falsch")
	}
	if w := esppdhDoF(e.h.EsppdhManifestWeb, id, bid, "manifest.json", esppdhUser); w.Code != http.StatusConflict {
		t.Errorf("geändert: Manifest %d", w.Code)
	}
	if d, err = e.h.esppdhLoad(ctx, id); err != nil {
		t.Fatal(err)
	}
	var host string
	_ = e.pool.QueryRow(ctx, `SELECT broker_host FROM esppdh_builds WHERE id = $1::uuid`, bid).Scan(&host)
	if ok, err := e.h.esppdhFirmwareCurrent(ctx, d, host, d.BuiltHash); ok || err != nil {
		t.Errorf("esppdhFirmwareCurrent nach Änderung: %v %v", ok, err)
	}
}

// Die Kennung eines Laufs gilt nur unter seinem Gerät; nur gelungene Läufe haben Firmware.
func TestFirmwareOfOtherDevice404(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	a := e.buildable(t, "oa-"+e.sfx)
	b := e.buildable(t, "ob-"+e.sfx)
	ba := e.built(t, fb, a, "a")
	for name, fn := range esppdhFlashRoutes(e.h) {
		if w := esppdhDoF(fn, b, ba, name, esppdhUser); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "KLARTEXT") {
			t.Errorf("%s unter fremdem Gerät: %d", name, w.Code)
		}
		if w := esppdhDoF(fn, a, "0198c0de-0000-7000-8000-00000000ffff", name, esppdhUser); w.Code != http.StatusNotFound {
			t.Errorf("%s eines unbekannten Laufs: %d", name, w.Code)
		}
		if w := esppdhDoF(fn, a, ba, name, esppdhUser); w.Code != http.StatusOK {
			t.Errorf("%s unter dem eigenen Gerät: %d", name, w.Code)
		}
	}
	// ein gescheiterter Lauf hat keine Firmware
	bf := e.queue(t, a)
	esppdhNextJob(t, fb)
	fb.gate(bf) <- esppdh.BuildResult{Log: "Fehler"}
	e.waitStatus(t, bf, "failed")
	for name, fn := range esppdhFlashRoutes(e.h) {
		if w := esppdhDoF(fn, a, bf, name, esppdhUser); w.Code != http.StatusNotFound {
			t.Errorf("%s eines gescheiterten Laufs: %d", name, w.Code)
		}
	}
}

// Detailseite: Knopf „Per USB flashen“ für den jüngsten gelungenen Lauf, versteckt, bis das
// Skript Web Serial in einem sicheren Kontext findet; der Hinweis fürs Herunterladen steht ohne
// JavaScript in der Seite. ESP Web Tools kommt nur aus /static/vendor/esp-web-tools/.
func TestFlashCard(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	id := e.buildable(t, "fc-"+e.sfx)
	if page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String(); strings.Contains(page, "esp-web-install-button") ||
		strings.Contains(page, "/firmware/") {
		t.Error("ohne gebaute Firmware: Knopf oder Herunterladen sichtbar")
	}
	old := e.built(t, fb, id, "alt")
	bid := e.built(t, fb, id, "neu")
	page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	base := "/import/esppdh/" + id + "/firmware/" + bid
	for _, s := range []string{`<esp-web-install-button manifest="` + base + `/manifest.json">`, `slot="activate"`, "Per USB flashen", esppdhNoUSBText,
		`href="` + base + `/factory.bin"`, `href="` + base + `/ota.bin"`, `/static/vendor/esp-web-tools/install-button.js`, "window.isSecureContext", `'serial' in navigator`} {
		if !strings.Contains(page, s) {
			t.Errorf("Seite: %q fehlt", s)
		}
	}
	if strings.Contains(page, "/firmware/"+old+"/") {
		t.Error("Seite bietet einen älteren Lauf an")
	}
	// ohne Skript bleibt der Knopf verborgen und der Hinweis sichtbar
	if !regexp.MustCompile(`<div class="esp-flash-usb"[^>]*\bhidden\b`).MatchString(page) || regexp.MustCompile(`<p[^>]*\bhidden\b[^>]*>`+regexp.QuoteMeta(esppdhNoUSBText)).MatchString(page) {
		t.Error("ohne JavaScript: Knopf sichtbar oder Hinweis versteckt")
	}
	// kein Skript, kein Stil, keine Schrift von außen
	for _, m := range regexp.MustCompile(`(?i)<script[^>]*\bsrc="([^"]*)"`).FindAllStringSubmatch(esppdhDeviceTemplate(t), -1) {
		t.Errorf("Vorlage lädt ein Skript per src: %s", m[1])
	}
	if strings.Contains(esppdhDeviceTemplate(t), "//unpkg") || strings.Contains(esppdhDeviceTemplate(t), "https://") {
		t.Error("Vorlage verweist auf einen fremden Rechner")
	}
}

// esppdhDeviceTemplate: Vorlage der Detailseite (die Datenbank-Tests laufen im Wurzelordner, itNetPageEnv).
func esppdhDeviceTemplate(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("web", "templates", "esppdh_device.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
