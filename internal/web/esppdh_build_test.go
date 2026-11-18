package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Bauen der Firmware (esppdh_build.go): Schlange mit einem Arbeiter, Abbruch,
// verschlüsselte Ablage, Verlauf auf der Geräteseite. Statt ESPHome baut eine Attrappe
// (esppdhFakeBuilder), die jeder Test einzeln freigibt.

// esppdhFakeBuilder: Attrappe des Runners. Jeder Bau meldet sich auf started und wartet, bis
// der Test ihn mit einem Ergebnis freigibt oder der Bau abgebrochen wird (dann canceled).
type esppdhFakeBuilder struct {
	mu       sync.Mutex
	started  chan esppdh.BuildJob
	canceled chan string
	gates    map[string]chan esppdh.BuildResult
}

func newEsppdhFakeBuilder() *esppdhFakeBuilder {
	return &esppdhFakeBuilder{started: make(chan esppdh.BuildJob, 16), canceled: make(chan string, 16), gates: map[string]chan esppdh.BuildResult{}}
}

func (f *esppdhFakeBuilder) gate(id string) chan esppdh.BuildResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.gates[id]
	if !ok {
		g = make(chan esppdh.BuildResult, 1)
		f.gates[id] = g
	}
	return g
}

func (f *esppdhFakeBuilder) Build(ctx context.Context, job esppdh.BuildJob, progress func(string)) esppdh.BuildResult {
	f.started <- job
	if progress != nil {
		progress("INFO ESPHome 2099.1.0\nINFO Compiling …\n")
	}
	select {
	case res := <-f.gate(job.ID):
		return res
	case <-ctx.Done():
		f.canceled <- job.ID
		return esppdh.BuildResult{Log: "Bau abgebrochen.\n"}
	}
}

// esppdhBuildOK: Ergebnis eines gelungenen Baus der Attrappe.
func esppdhBuildOK(tag string) esppdh.BuildResult {
	return esppdh.BuildResult{OK: true, Log: "INFO ESPHome 2099.1.0\nFirmware gebaut.\n", Version: "2099.1.0",
		Factory: []byte("FACTORY-KLARTEXT-" + tag), OTA: []byte("OTA-KLARTEXT-" + tag)}
}

func esppdhNextJob(t *testing.T, fb *esppdhFakeBuilder) esppdh.BuildJob {
	t.Helper()
	select {
	case j := <-fb.started:
		return j
	case <-time.After(10 * time.Second):
		t.Fatal("kein Bau gestartet")
	}
	return esppdh.BuildJob{}
}

// esppdhDoB: wie esppdhDo, zusätzlich mit der Kennung des Laufs im Pfad; hx = Abruf von htmx.
func esppdhDoB(fn http.HandlerFunc, method, id, bid string, u *users.User, hx bool) *httptest.ResponseRecorder {
	path := "/import/esppdh/" + id + "/build/" + bid
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path+"/cancel", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path+"?poll=1", nil)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
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

// newEsppdhBuildEnv: wie newEsppdhEnv, dazu „Umgebung eingerichtet“ (bin/esphome vorhanden),
// eigene Ablage der Firmware und die Attrappe als Runner. Wartende oder laufende Bauten aus
// früheren Läufen der Labor-Datenbank werden beendet, damit der Arbeiter nur eigene baut.
func newEsppdhBuildEnv(t *testing.T) (*esppdhEnvT, *esppdhFakeBuilder, string) {
	t.Helper()
	e := newEsppdhEnv(t, nil)
	e.envReady(t)
	old := esppdhFirmwareRoot
	if old != filepath.Join("uploads", "esppdh") {
		t.Errorf("Ablage der Firmware: %q, erwartet uploads/esppdh", old)
	}
	root := filepath.Join(t.TempDir(), "uploads", "esppdh")
	esppdhFirmwareRoot = root
	t.Cleanup(func() { esppdhFirmwareRoot = old })
	if _, err := e.pool.Exec(context.Background(), `UPDATE esppdh_builds SET status = 'canceled', finished_at = now() WHERE status IN ('queued', 'running')`); err != nil {
		t.Fatal(err)
	}
	fb := newEsppdhFakeBuilder()
	e.h.esppdhRunner = fb
	return e, fb, root
}

// envReady: Python-Umgebung „eingerichtet“ (EnvPresent prüft nur, ob bin/esphome da ist).
func (e *esppdhEnvT) envReady(t *testing.T) {
	t.Helper()
	bin := filepath.Join(os.Getenv("PDH_ESPHOME_DIR"), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "esphome"), []byte("attrappe"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (e *esppdhEnvT) start(t *testing.T) {
	t.Helper()
	e.h.StartESPPDH(context.Background())
	t.Cleanup(e.h.StopESPPDH)
}

// buildable: Gerät anlegen und einen Baustein speichern – dann lässt es sich bauen.
func (e *esppdhEnvT) buildable(t *testing.T, slug string) string {
	t.Helper()
	id := e.create(t, slug, nil)
	v := e.saveForm("Gerät " + slug)
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4", "pulses": "1000"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("Baustein speichern: %d %s", w.Code, w.Body.String())
	}
	return id
}

// queue reiht einen Bau über den Knopf ein und liefert die Kennung des Laufs.
func (e *esppdhEnvT) queue(t *testing.T, id string) string {
	t.Helper()
	w := esppdhDo(e.h.EsppdhBuildWeb, "POST", "/import/esppdh/"+id+"/build", id, esppdhUser, url.Values{})
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/import/esppdh/"+id) {
		t.Fatalf("Bau einreihen: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	var bid string
	if err := e.pool.QueryRow(context.Background(), `SELECT id::text FROM esppdh_builds WHERE device_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, id).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	return bid
}

func (e *esppdhEnvT) build(t *testing.T, bid string) (status, log string) {
	t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT status, log FROM esppdh_builds WHERE id = $1::uuid`, bid).Scan(&status, &log); err != nil {
		t.Fatal(err)
	}
	return status, log
}

func (e *esppdhEnvT) waitStatus(t *testing.T, bid, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, log := e.build(t, bid)
		if st == want {
			return log
		}
		if time.Now().After(deadline) {
			t.Fatalf("Lauf %s: Zustand %q, erwartet %q\n%s", bid, st, want, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitLog wartet, bis der Zwischenstand der Attrappe im Verlauf steht.
func (e *esppdhEnvT) waitLog(t *testing.T, bid, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, log := e.build(t, bid); strings.Contains(log, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Zwischenstand %q nicht gespeichert", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *esppdhEnvT) buildCount(t *testing.T, id string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM esppdh_builds WHERE device_id = $1::uuid`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ── ohne Datenbank ───────────────────────────────────────────────────────

func TestBuildGate(t *testing.T) {
	id, bid := "/"+itNetTestID, "/0198c0de-0000-7000-8000-000000000001"
	for _, path := range []string{"/import/esppdh" + id + "/build", "/import/esppdh" + id + "/build" + bid + "/cancel"} {
		_, perm, ref, gotID, ok := matchGate("POST", path)
		if !ok || perm != permESPEdit || ref != esppdhRef || gotID != itNetTestID {
			t.Errorf("%s: %v %q %q %q", path, ok, perm, ref, gotID)
		}
	}
	if _, _, _, _, ok := matchGate("GET", "/import/esppdh"+id+"/build"+bid); ok {
		t.Error("Verlauf lesen braucht keine Schreib-Regel")
	}
	// ohne Recht: 403 vor jedem Datenbankzugriff; nur Ansehen reicht nicht zum Bauen und Abbrechen
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	b := "0198c0de-0000-7000-8000-000000000001"
	if w := esppdhDo(h.EsppdhBuildWeb, "POST", "/x", itNetTestID, esppdhUser, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Bauen ohne Recht: %d", w.Code)
	}
	if w := esppdhDoB(h.EsppdhBuildPartWeb, http.MethodGet, itNetTestID, b, esppdhUser, true); w.Code != http.StatusForbidden {
		t.Errorf("Verlauf ohne Recht: %d", w.Code)
	}
	if w := esppdhDoB(h.EsppdhBuildCancelWeb, http.MethodPost, itNetTestID, b, esppdhUser, false); w.Code != http.StatusForbidden {
		t.Errorf("Abbrechen ohne Recht: %d", w.Code)
	}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permESPView }
	if w := esppdhDo(h.EsppdhBuildWeb, "POST", "/x", itNetTestID, esppdhUser, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Bauen nur mit esppdh.view: %d", w.Code)
	}
	if w := esppdhDoB(h.EsppdhBuildCancelWeb, http.MethodPost, itNetTestID, b, esppdhUser, false); w.Code != http.StatusForbidden {
		t.Errorf("Abbrechen nur mit esppdh.view: %d", w.Code)
	}
	// ungültige Kennung des Laufs: 404
	if w := esppdhDoB(h.EsppdhBuildPartWeb, http.MethodGet, itNetTestID, "x", esppdhUser, true); w.Code != http.StatusNotFound {
		t.Errorf("ungültige Lauf-Kennung: %d", w.Code)
	}
}

func TestEsppdhLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1": true, "127.1.2.3": true, "::1": true, "localhost": true, "LOCALHOST": true,
		"mqtt.localhost": true, "0.0.0.0": true, "": true, "10.0.0.5": false, "mqtt.firma.de": false, "localhost.firma.de": false} {
		if got := esppdhLoopbackHost(host); got != want {
			t.Errorf("%q: %v, erwartet %v", host, got, want)
		}
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

// Zwei Aufträge: Es baut immer nur einer; der zweite wartet, bis der erste fertig ist.
func TestBuildQueueOneAtATime(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	a := e.buildable(t, "qa-"+e.sfx)
	b := e.buildable(t, "qb-"+e.sfx)
	ba := e.queue(t, a)
	bb := e.queue(t, b)
	if j := esppdhNextJob(t, fb); j.ID != ba {
		t.Fatalf("zuerst gebaut: %s, erwartet %s", j.ID, ba)
	}
	time.Sleep(300 * time.Millisecond)
	if st, _ := e.build(t, bb); st != "queued" {
		t.Errorf("zweiter Auftrag: %q, erwartet queued", st)
	}
	if st, _ := e.build(t, ba); st != "running" {
		t.Errorf("erster Auftrag: %q, erwartet running", st)
	}
	select {
	case j := <-fb.started:
		t.Fatalf("zweiter Bau läuft gleichzeitig: %s", j.ID)
	default:
	}
	// derselbe Knopf noch einmal: kein zweiter Auftrag für das Gerät
	if w := esppdhDo(e.h.EsppdhBuildWeb, "POST", "/import/esppdh/"+a+"/build", a, esppdhUser, url.Values{}); w.Code != http.StatusConflict {
		t.Errorf("zweiter Auftrag für dasselbe Gerät: %d", w.Code)
	}
	if n := e.buildCount(t, a); n != 1 {
		t.Errorf("%d Aufträge für das Gerät, erwartet 1", n)
	}
	fb.gate(ba) <- esppdhBuildOK("a")
	if j := esppdhNextJob(t, fb); j.ID != bb {
		t.Fatalf("danach gebaut: %s, erwartet %s", j.ID, bb)
	}
	e.waitStatus(t, ba, "ok")
	fb.gate(bb) <- esppdhBuildOK("b")
	e.waitStatus(t, bb, "ok")
}

// Gebaute Firmware liegt verschlüsselt unter uploads/esppdh/<gerät>/<lauf>.bin.enc (und
// .ota.bin.enc); esppdhFirmware entschlüsselt sie; built_hash ist gesetzt. Der Auftrag
// enthält die Konfiguration für die beim Einreihen festgelegte Adresse und alle Kennwörter
// zum Schwärzen.
func TestBuildStoresEncryptedFirmware(t *testing.T) {
	e, fb, root := newEsppdhBuildEnv(t)
	e.start(t)
	slug := "fw-" + e.sfx
	id := e.buildable(t, slug)
	bid := e.queue(t, id)
	var host string
	var port int
	var tls bool
	if err := e.pool.QueryRow(context.Background(), `SELECT broker_host, broker_port, broker_tls FROM esppdh_builds WHERE id = $1::uuid`, bid).Scan(&host, &port, &tls); err != nil {
		t.Fatal(err)
	}
	if host != "mqtt-"+e.sfx+".pdh-test.invalid" || port == 0 || tls {
		t.Errorf("Broker beim Einreihen: %s:%d tls=%v", host, port, tls)
	}

	job := esppdhNextJob(t, fb)
	apPW := ""
	{
		var apEnc string
		_ = e.pool.QueryRow(context.Background(), `SELECT ap_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&apEnc)
		apPW, _ = decryptSecret(e.h.esppdhKey(), apEnc)
	}
	mqttPW := e.mqttPW(t, id)
	if job.ID != bid || job.Slug != slug || !strings.Contains(string(job.Config), host) || !strings.Contains(string(job.Secrets), e.wlPW) {
		t.Errorf("Auftrag: %s %s\n%s", job.ID, job.Slug, job.Config)
	}
	for _, s := range []string{e.wlPW, mqttPW, apPW} {
		var found bool
		for _, v := range job.SecretValues {
			found = found || v == s
		}
		if s == "" || !found {
			t.Errorf("Kennwort fehlt in SecretValues (Länge %d)", len(s))
		}
	}
	for _, s := range []string{e.wlPW, mqttPW, apPW} {
		if strings.Contains(string(job.Config), s) {
			t.Error("Kennwort in der Konfiguration statt in secrets.yaml")
		}
	}

	e.waitLog(t, bid, "Compiling") // Zwischenstand landet im Verlauf
	res := esppdhBuildOK("fw")
	fb.gate(bid) <- res
	e.waitStatus(t, bid, "ok")

	dir := filepath.Join(root, id)
	for _, name := range []string{bid + ".bin.enc", bid + ".ota.bin.enc"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(b), "KLARTEXT") {
			t.Errorf("%s ist nicht verschlüsselt", name)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if !strings.HasSuffix(en.Name(), ".bin.enc") {
			t.Errorf("fremde Datei in der Ablage: %s", en.Name())
		}
	}
	if fw, err := e.h.esppdhFirmware(bid); err != nil || string(fw) != string(res.Factory) {
		t.Errorf("esppdhFirmware: %q %v", fw, err)
	}
	if fw, err := e.h.esppdhFirmwareOTA(bid); err != nil || string(fw) != string(res.OTA) {
		t.Errorf("esppdhFirmwareOTA: %q %v", fw, err)
	}
	var built, cfgHash, sha, ver string
	var size int64
	if err := e.pool.QueryRow(context.Background(), `SELECT d.built_hash, b.config_hash, b.firmware_sha256, b.firmware_size, b.esphome_version
		FROM esppdh_builds b JOIN esppdh_devices d ON d.id = b.device_id WHERE b.id = $1::uuid`, bid).Scan(&built, &cfgHash, &sha, &size, &ver); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(res.Factory)
	if built == "" || built != cfgHash || sha != hex.EncodeToString(sum[:]) || size != int64(len(res.Factory)) || ver != "2099.1.0" {
		t.Errorf("Zeile: built %q hash %q sha %q size %d version %q", built, cfgHash, sha, size, ver)
	}

	// vertauschte Dateien lassen sich nicht entschlüsseln (an Lauf und Art gebunden)
	f, o := filepath.Join(dir, bid+".bin.enc"), filepath.Join(dir, bid+".ota.bin.enc")
	fb1, _ := os.ReadFile(f)
	ob1, _ := os.ReadFile(o)
	_ = os.WriteFile(f, ob1, 0o600)
	if _, err := e.h.esppdhFirmware(bid); err == nil {
		t.Error("vertauschte Datei angenommen")
	}
	_ = os.WriteFile(f, fb1, 0o600)

	// Seite: Lauf fertig, für welche Adresse; nach einer Änderung der Hinweis „geändert“
	page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	if !strings.Contains(page, "fertig") || !strings.Contains(page, host+":") || strings.Contains(page, "seit dem letzten Bau geändert") {
		t.Errorf("Seite nach dem Bau: Lauf, Adresse oder falscher Hinweis")
	}
	v := e.saveForm("Neuer Name " + e.sfx)
	esppdhAddBlock(v, "0", e.blocks(t, id)[0].ID, "s0", map[string]string{"pin": "GPIO4", "pulses": "1000"})
	if w := esppdhDo(e.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("speichern: %d", w.Code)
	}
	page = esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	if !strings.Contains(page, "Konfiguration seit dem letzten Bau geändert") {
		t.Error("Hinweis „Konfiguration seit dem letzten Bau geändert“ fehlt")
	}
	for _, s := range []string{e.wlPW, mqttPW} {
		if strings.Contains(page, s) {
			t.Error("Kennwort auf der Seite")
		}
	}
}

// Je Gerät bleiben die Dateien der letzten 3 gelungenen Läufe; Löschen des Geräts entfernt alle.
func TestBuildKeepsLastThreeAndDeleteRemoves(t *testing.T) {
	e, fb, root := newEsppdhBuildEnv(t)
	e.start(t)
	id := e.buildable(t, "keep-"+e.sfx)
	var bids []string
	for i := 0; i < 4; i++ {
		bid := e.queue(t, id)
		esppdhNextJob(t, fb)
		fb.gate(bid) <- esppdhBuildOK(bid)
		e.waitStatus(t, bid, "ok")
		bids = append(bids, bid)
	}
	entries, _ := os.ReadDir(filepath.Join(root, id))
	if len(entries) != 6 {
		t.Errorf("%d Dateien, erwartet 6 (3 Läufe)", len(entries))
	}
	if _, err := e.h.esppdhFirmware(bids[0]); err == nil {
		t.Error("Firmware des ältesten Laufs noch da")
	}
	if _, err := e.h.esppdhFirmware(bids[3]); err != nil {
		t.Errorf("jüngste Firmware: %v", err)
	}
	if w := esppdhDo(e.h.EsppdhDeleteWeb, "POST", "/import/esppdh/"+id+"/delete", id, esppdhUser, url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("löschen: %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
		t.Errorf("Firmware des gelöschten Geräts noch da: %v", err)
	}
}

func TestBuildRefusedOnMirrorAndWhenDisabled(t *testing.T) {
	e, _, _ := newEsppdhBuildEnv(t)
	id := e.buildable(t, "refuse-"+e.sfx)
	post := func() *httptest.ResponseRecorder {
		return esppdhDo(e.h.EsppdhBuildWeb, "POST", "/import/esppdh/"+id+"/build", id, esppdhUser, url.Values{})
	}
	e.h.SetMirror(MirrorInfo{On: true})
	if w := post(); w.Code != http.StatusConflict {
		t.Errorf("Spiegel: %d", w.Code)
	}
	e.h.SetMirror(MirrorInfo{})
	esppdhSetSwitch(t, e.h, "0")
	if w := post(); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "ausgeschaltet") {
		t.Errorf("Schalter aus: %d %s", w.Code, w.Body.String())
	}
	esppdhSetSwitch(t, e.h, "1")
	bin := filepath.Join(os.Getenv("PDH_ESPHOME_DIR"), "bin", "esphome")
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if w := post(); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Umgebung") {
		t.Errorf("Umgebung fehlt: %d %s", w.Code, w.Body.String())
	}
	e.envReady(t)
	esppdhDockerHook = func() bool { return true }
	if w := post(); w.Code != http.StatusConflict {
		t.Errorf("Docker: %d", w.Code)
	}
	if n := e.buildCount(t, id); n != 0 {
		t.Errorf("%d Aufträge trotz Ablehnung", n)
	}
	// kein Arbeiter im Docker-Betrieb und am Spiegel
	h2 := &Handler{db: e.pool}
	h2.StartESPPDH(context.Background())
	if h2.esppdhQ.done != nil {
		t.Error("Arbeiter im Docker-Betrieb gestartet")
	}
	esppdhDockerHook = func() bool { return false }
	h2.SetMirror(MirrorInfo{On: true})
	h2.StartESPPDH(context.Background())
	if h2.esppdhQ.done != nil {
		t.Error("Arbeiter am Spiegel gestartet")
	}
	// am Spiegel und ohne Recht kein Knopf „Firmware bauen“
	e.h.SetMirror(MirrorInfo{On: true})
	if page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String(); strings.Contains(page, `action="/import/esppdh/`+id+`/build"`) {
		t.Error("Knopf „Firmware bauen“ am Spiegel")
	}
	e.h.SetMirror(MirrorInfo{})
	if page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String(); !strings.Contains(page, `action="/import/esppdh/`+id+`/build"`) {
		t.Error("Knopf „Firmware bauen“ fehlt")
	}
	e.allow = map[string]bool{permESPView: true}
	if page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String(); strings.Contains(page, `action="/import/esppdh/`+id+`/build"`) {
		t.Error("Knopf „Firmware bauen“ ohne esppdh.edit")
	}
}

// Broker-Adresse, unter der das Gerät den Broker nicht erreicht (Loopback): Bau abgelehnt,
// die Seite sagt, was zu tun ist.
func TestBuildLoopbackRejected(t *testing.T) {
	e := newEsppdhEnv(t, esppdhBrokerCfg(esppdhFreePort(t), mqttHostKey, "mqtt.localhost"))
	e.envReady(t)
	id := e.buildable(t, "loop-"+e.sfx)
	w := esppdhDo(e.h.EsppdhBuildWeb, "POST", "/import/esppdh/"+id+"/build", id, esppdhUser, url.Values{})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "unter dieser Adresse erreicht das Gerät den Broker nicht") {
		t.Errorf("Loopback: %d %s", w.Code, w.Body.String())
	}
	if n := e.buildCount(t, id); n != 0 {
		t.Errorf("%d Aufträge", n)
	}
	page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	if !strings.Contains(page, "unter dieser Adresse erreicht das Gerät den Broker nicht") || strings.Contains(page, `action="/import/esppdh/`+id+`/build"`) {
		t.Error("Seite: Hinweis fehlt oder Knopf sichtbar")
	}
}

// Ein „running“-Lauf von vor dem Neustart wird beim Start „failed“; Reste von Arbeitsordnern
// verschwinden; die Schlange läuft danach normal.
func TestBuildStaleRunningRecovered(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	id := e.buildable(t, "stale-"+e.sfx)
	var bid string
	if err := e.pool.QueryRow(context.Background(), `INSERT INTO esppdh_builds (device_id, status, config_hash, log, started_at)
		VALUES ($1::uuid, 'running', 'x', 'INFO Compiling …', now()) RETURNING id::text`, id).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(os.Getenv("PDH_ESPHOME_WORKDIR"), "build", bid)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "secrets.yaml"), []byte("mqtt_password: x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Kommt der Server nach dem Absturz als Spiegel (oder im Docker-Betrieb) zurück, baut er
	// nicht und schreibt nichts in die Datenbank – die Kennwörter im alten Arbeitsordner
	// räumt er trotzdem weg.
	for _, c := range []struct {
		name           string
		mirror, docker bool
	}{{"Spiegel", true, false}, {"Docker", false, true}} {
		if err := os.MkdirAll(old, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(old, "secrets.yaml"), []byte("mqtt_password: x"), 0o600); err != nil {
			t.Fatal(err)
		}
		h2 := &Handler{db: e.pool}
		h2.SetMirror(MirrorInfo{On: c.mirror})
		esppdhDockerHook = func() bool { return c.docker }
		h2.StartESPPDH(context.Background())
		esppdhDockerHook = func() bool { return false }
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Errorf("%s: alter Arbeitsordner mit Kennwörtern noch da: %v", c.name, err)
		}
		if h2.esppdhQ.done != nil {
			t.Errorf("%s: Arbeiter gestartet", c.name)
		}
		if st, _ := e.build(t, bid); st != "running" {
			t.Errorf("%s: Lauf %q – am Spiegel wird nichts eingetragen", c.name, st)
		}
	}
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "secrets.yaml"), []byte("mqtt_password: x"), 0o600); err != nil {
		t.Fatal(err)
	}

	e.start(t)
	st, log := e.build(t, bid)
	if st != "failed" || !strings.Contains(log, "INFO Compiling") || !strings.Contains(log, "PDH wurde während des Baus neu gestartet") {
		t.Errorf("nach dem Start: %q\n%s", st, log)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("alter Arbeitsordner noch da: %v", err)
	}
	next := e.queue(t, id)
	if j := esppdhNextJob(t, fb); j.ID != next {
		t.Fatalf("nächster Lauf: %s", j.ID)
	}
	fb.gate(next) <- esppdhBuildOK("n")
	e.waitStatus(t, next, "ok")
}

// Abbrechen: ein wartender Auftrag sofort, ein laufender über den Arbeiter (Prozess beendet);
// fremde Läufe gibt es unter einem anderen Gerät nicht.
func TestBuildCancel(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	a := e.buildable(t, "ca-"+e.sfx)
	b := e.buildable(t, "cb-"+e.sfx)
	ba := e.queue(t, a)
	esppdhNextJob(t, fb)
	bb := e.queue(t, b)

	if w := esppdhDoB(e.h.EsppdhBuildCancelWeb, http.MethodPost, b, ba, esppdhUser, false); w.Code != http.StatusNotFound {
		t.Errorf("Lauf eines anderen Geräts: %d", w.Code)
	}
	if w := esppdhDoB(e.h.EsppdhBuildCancelWeb, http.MethodPost, b, bb, esppdhUser, false); w.Code != http.StatusSeeOther {
		t.Fatalf("wartenden abbrechen: %d %s", w.Code, w.Body.String())
	}
	if st, _ := e.build(t, bb); st != "canceled" {
		t.Errorf("wartender Auftrag: %q", st)
	}
	if w := esppdhDoB(e.h.EsppdhBuildCancelWeb, http.MethodPost, a, ba, esppdhUser, false); w.Code != http.StatusSeeOther {
		t.Fatalf("laufenden abbrechen: %d %s", w.Code, w.Body.String())
	}
	select {
	case got := <-fb.canceled:
		if got != ba {
			t.Errorf("abgebrochen: %s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("der laufende Bau wurde nicht beendet")
	}
	if log := e.waitStatus(t, ba, "canceled"); !strings.Contains(log, "abgebrochen") {
		t.Errorf("Verlauf: %s", log)
	}
	select {
	case j := <-fb.started:
		t.Errorf("abgebrochener Auftrag wurde doch gebaut: %s", j.ID)
	case <-time.After(200 * time.Millisecond):
	}
	// der Arbeiter macht weiter
	next := e.queue(t, a)
	if j := esppdhNextJob(t, fb); j.ID != next {
		t.Fatalf("nächster Lauf: %s", j.ID)
	}
	fb.gate(next) <- esppdhBuildOK("c")
	e.waitStatus(t, next, "ok")
}

// Vor jedem Auftrag prüft der Arbeiter erneut: Ist ESPPDH inzwischen aus oder fehlt die
// Umgebung, startet er wartende Aufträge nicht, sondern bricht sie mit Hinweis ab.
func TestBuildWorkerRechecks(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	a := e.buildable(t, "ra-"+e.sfx)
	b := e.buildable(t, "rb-"+e.sfx)
	c := e.buildable(t, "rc-"+e.sfx)
	ba := e.queue(t, a)
	esppdhNextJob(t, fb)
	bb := e.queue(t, b)
	esppdhSetSwitch(t, e.h, "0") // ausgeschaltet, während a baut und b wartet
	fb.gate(ba) <- esppdhBuildOK("a")
	e.waitStatus(t, ba, "ok")
	if log := e.waitStatus(t, bb, "canceled"); !strings.Contains(log, "Nicht gebaut") || !strings.Contains(log, "ausgeschaltet") {
		t.Errorf("Verlauf des wartenden Auftrags: %s", log)
	}
	// Umgebung weg (eingereiht, solange sie noch da war; Arbeiter kurz angehalten): ebenso
	esppdhSetSwitch(t, e.h, "1")
	e.h.StopESPPDH()
	bc := e.queue(t, c)
	if err := os.Remove(filepath.Join(os.Getenv("PDH_ESPHOME_DIR"), "bin", "esphome")); err != nil {
		t.Fatal(err)
	}
	e.h.StartESPPDH(context.Background())
	if log := e.waitStatus(t, bc, "canceled"); !strings.Contains(log, "Umgebung") {
		t.Errorf("Verlauf ohne Umgebung: %s", log)
	}
	select {
	case j := <-fb.started:
		t.Errorf("trotzdem gebaut: %s", j.ID)
	default:
	}
}

// Herunterfahren: der laufende Bau endet als „failed“ mit Hinweis, der Arbeiter ist beendet.
func TestBuildShutdownMarksFailed(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.h.StartESPPDH(context.Background())
	id := e.buildable(t, "stop-"+e.sfx)
	bid := e.queue(t, id)
	esppdhNextJob(t, fb)
	e.h.esppdhQ.mu.Lock()
	done := e.h.esppdhQ.done
	e.h.esppdhQ.mu.Unlock()
	e.h.StopESPPDH()
	select {
	case <-done:
	default:
		t.Fatal("Arbeiter läuft nach StopESPPDH weiter")
	}
	st, log := e.build(t, bid)
	if st != "failed" || !strings.Contains(log, "PDH wurde während des Baus beendet") {
		t.Errorf("nach dem Herunterfahren: %q\n%s", st, log)
	}
	e.h.StopESPPDH() // zweimal schadet nicht
}

// Verlauf als htmx-Teil: fragt alle 2 s nur, solange der Lauf wartet oder läuft; prüft das Recht
// bei jeder Abfrage und dass der Lauf zum Gerät gehört.
func TestBuildFragment(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	a := e.buildable(t, "fa-"+e.sfx)
	b := e.buildable(t, "fb-"+e.sfx)
	ba := e.queue(t, a)
	esppdhNextJob(t, fb)
	e.waitLog(t, ba, "Compiling")
	w := esppdhDoB(e.h.EsppdhBuildPartWeb, http.MethodGet, a, ba, esppdhUser, true)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, "/build/"+ba+"/cancel") || !strings.Contains(body, "Compiling") {
		t.Errorf("laufender Lauf: %d\n%s", w.Code, body)
	}
	if w := esppdhDoB(e.h.EsppdhBuildPartWeb, http.MethodGet, b, ba, esppdhUser, true); w.Code != http.StatusNotFound {
		t.Errorf("Lauf unter fremdem Gerät: %d", w.Code)
	}
	e.allow = map[string]bool{permESPView: true}
	if body := esppdhDoB(e.h.EsppdhBuildPartWeb, http.MethodGet, a, ba, esppdhUser, true).Body.String(); strings.Contains(body, "/cancel") {
		t.Error("Abbrechen ohne esppdh.edit")
	}
	e.allow = map[string]bool{}
	if w := esppdhDoB(e.h.EsppdhBuildPartWeb, http.MethodGet, a, ba, esppdhUser, true); w.Code != http.StatusForbidden {
		t.Errorf("Abfrage ohne Recht: %d", w.Code)
	}
	e.allow = map[string]bool{permESPView: true, permESPEdit: true, permESPAdmin: true}
	fb.gate(ba) <- esppdhBuildOK("f")
	e.waitStatus(t, ba, "ok")
	w = esppdhDoB(e.h.EsppdhBuildPartWeb, http.MethodGet, a, ba, esppdhUser, true)
	if strings.Contains(w.Body.String(), "hx-trigger") || w.Header().Get("HX-Refresh") != "true" {
		t.Errorf("fertiger Lauf fragt weiter oder lädt die Seite nicht neu: %q\n%s", w.Header().Get("HX-Refresh"), w.Body.String())
	}
}

// Den Verlauf eines älteren Laufs öffnen ersetzt nie den Teil des laufenden Baus, der alle 2 s
// nachfragt: ältere Läufe kommen in einen eigenen Bereich, jeder Teil hat eine eigene Kennung.
func TestBuildOlderLogKeepsPolling(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	id := e.buildable(t, "poll-"+e.sfx)
	old := e.queue(t, id)
	esppdhNextJob(t, fb)
	fb.gate(old) <- esppdhBuildOK("alt")
	e.waitStatus(t, old, "ok")
	cur := e.queue(t, id)
	esppdhNextJob(t, fb)
	page := esppdhDo(e.h.EsppdhDevicePage, "GET", "/import/esppdh/"+id, id, esppdhUser, nil).Body.String()
	if !strings.Contains(page, `id="esp-build-`+cur+`"`) || !strings.Contains(page, `hx-trigger="every 2s"`) {
		t.Fatal("laufender Bau fragt auf der Seite nicht nach")
	}
	if strings.Contains(page, `hx-target="#esp-build-`+cur+`"`) || strings.Contains(page, `hx-target="#esp-build"`) {
		t.Error("ein Knopf „Verlauf“ würde den nachfragenden Teil ersetzen")
	}
	if !strings.Contains(page, `hx-get="/import/esppdh/`+id+`/build/`+old+`" hx-target="#esp-build-other"`) || !strings.Contains(page, `id="esp-build-other"`) {
		t.Error("älterer Lauf öffnet nicht im eigenen Bereich")
	}
	if strings.Contains(page, `hx-get="/import/esppdh/`+id+`/build/`+cur+`" hx-target`) {
		t.Error("Knopf „Verlauf“ für den Lauf, der schon oben steht")
	}
	part := esppdhDoB(e.h.EsppdhBuildPartWeb, http.MethodGet, id, old, esppdhUser, true).Body.String()
	if !strings.Contains(part, `id="esp-build-`+old+`"`) || strings.Contains(part, "hx-trigger") {
		t.Errorf("Teil des älteren Laufs: %s", part)
	}
	fb.gate(cur) <- esppdhBuildOK("neu")
	e.waitStatus(t, cur, "ok")
}

// uploads/esppdh ist über /uploads/* nie abrufbar – in keiner Schreibweise; andere Dateien wie bisher.
func TestUploadsHandlerHidesEsppdh(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "esppdh", "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "esppdh", "dev", "x.bin.enc"), []byte("GEHEIM"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "avatars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "avatars", "a.txt"), []byte("BILD"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := UploadsHandler(dir)
	get := func(p string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path, r.URL.RawPath = p, ""
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, p := range []string{"/uploads/esppdh/dev/x.bin.enc", "/uploads/esppdh/", "/uploads/esppdh", "/uploads/esppdh/dev/",
		"/uploads/ESPPDH/dev/x.bin.enc", "/uploads/EspPdh/", "/uploads/./esppdh/dev/x.bin.enc", "/uploads//esppdh/dev/x.bin.enc",
		"/uploads/x/../esppdh/dev/x.bin.enc", "/uploads/avatars/../esppdh/", "/uploads/esppdh/./dev/x.bin.enc", "/uploads/esppdh/dev/../dev/x.bin.enc"} {
		if w := get(p); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "GEHEIM") || strings.Contains(w.Body.String(), "x.bin.enc") {
			t.Errorf("%s: %d %q", p, w.Code, w.Body.String())
		}
	}
	if w := get("/uploads/avatars/a.txt"); w.Code != http.StatusOK || w.Body.String() != "BILD" {
		t.Errorf("andere Datei: %d %q", w.Code, w.Body.String())
	}
	// in main.go eingehängt
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "server", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `r.Handle("/uploads/*", web.UploadsHandler(`) || strings.Contains(string(src), `http.FileServer(http.Dir("uploads"))`) {
		t.Error("main.go liefert uploads nicht über UploadsHandler aus")
	}
}
