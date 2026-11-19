package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – ESPHome täglich prüfen, proben, umschalten (esppdh_update.go). Agent und PyPI sind
// httptest-Server, ESPHome eine Attrappe hinter esppdhProber; die installierte Version kommt über
// esppdhCheckEnv. ev schreibt in Reihenfolge mit, was Agent und Attrappe tun. Die Datenbank-Tests
// brauchen die Labor-Datenbank.

const (
	esppdhUpdOld = "2026.10.2" // läuft
	esppdhUpdNew = "2026.11.1" // Kandidat
)

var esppdhUpdNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// esppdhPyPIRel: Ausgabe für die PyPI-Antwort im Test.
type esppdhPyPIRel struct {
	v        string
	uploaded time.Time
}

// esppdhPyPIBody: Antwort von PyPI mit diesen Ausgaben (je eine Datei, Python ab 3.11).
func esppdhPyPIBody(rels ...esppdhPyPIRel) string {
	type file struct {
		Uploaded string `json:"upload_time_iso_8601"`
		Yanked   bool   `json:"yanked"`
		Python   string `json:"requires_python"`
	}
	doc := map[string]map[string][]file{"releases": {}}
	for _, r := range rels {
		doc["releases"][r.v] = []file{{Uploaded: r.uploaded.Format(time.RFC3339), Python: ">=3.11"}}
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// esppdhUpdT: Prüflauf im Test (siehe newEsppdhUpd).
type esppdhUpdT struct {
	*esppdhEnvT
	fb    *esppdhFakeBuilder
	admin string // Benutzer mit esppdh.admin – Empfänger der Meldung

	mu       sync.Mutex
	ev       []string
	pypiHits int
	pypiFn   func(w http.ResponseWriter, r *http.Request) // nil = pypiBody senden
	pypiBody string
	// Verhalten des Agenten
	tooOld      bool   // kennt /v1/esppdh/versions nicht (404)
	installErr  string // Einrichtung scheitert mit dieser Meldung
	otherTarget bool   // es läuft schon eine andere Einrichtung (Grundumgebung)
	noBin       bool   // Kandidat fehlt nach der Einrichtung (vom Agenten aufgeräumt)
	jobRunning  bool
	jobTarget   string
	jobPolls    int
	// Verhalten der Attrappe
	configFail  func(slug string, cand bool) bool
	buildFail   func(slug string) bool
	configPanic bool
}

func (u *esppdhUpdT) add(e string) {
	u.mu.Lock()
	u.ev = append(u.ev, e)
	u.mu.Unlock()
}

// events: alles bisher Mitgeschriebene.
func (u *esppdhUpdT) events() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.ev)
}

func (u *esppdhUpdT) hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pypiHits
}

// esppdhKeepSetting setzt einen Wert in app_settings und stellt ihn nach dem Test wieder her.
func esppdhKeepSetting(t *testing.T, h *Handler, key, value string) {
	t.Helper()
	ctx := context.Background()
	var before string
	had := h.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&before) == nil
	t.Cleanup(func() {
		if had {
			_ = h.setAppSetting(context.Background(), key, before)
		} else {
			_, _ = h.db.Exec(context.Background(), `DELETE FROM app_settings WHERE key=$1`, key)
		}
	})
	if err := h.setAppSetting(ctx, key, value); err != nil {
		t.Fatal(err)
	}
}

// esppdhFastUpdate verkürzt alle Wartezeiten des Ablaufs und stellt sie nach dem Test wieder her.
func esppdhFastUpdate(t *testing.T) {
	t.Helper()
	old := []time.Duration{esppdhInstallPoll, esppdhInstallLimit, esppdhIdleWait, esppdhIdleLimit, esppdhUpdTick, esppdhUpdDelay, esppdhPyPITimeout, esppdhRunPoll}
	t.Cleanup(func() {
		esppdhInstallPoll, esppdhInstallLimit, esppdhIdleWait, esppdhIdleLimit = old[0], old[1], old[2], old[3]
		esppdhUpdTick, esppdhUpdDelay, esppdhPyPITimeout, esppdhRunPoll = old[4], old[5], old[6], old[7]
	})
	esppdhInstallPoll, esppdhInstallLimit = time.Millisecond, 10*time.Second
	esppdhIdleWait, esppdhIdleLimit = 5*time.Millisecond, 10*time.Second
	esppdhUpdTick, esppdhUpdDelay, esppdhPyPITimeout, esppdhRunPoll = 5*time.Millisecond, time.Millisecond, 5*time.Second, 5*time.Millisecond
}

// set ändert das Verhalten der Attrappen unter u.mu (die Server lesen es in eigenen Goroutinen).
func (u *esppdhUpdT) set(f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f()
}

// loop lässt den Takt laufen, bis wait zurückkehrt, und wartet auf sein Ende.
func (u *esppdhUpdT) loop(t *testing.T, wait func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { u.h.esppdhUpdateLoop(ctx); close(done) }()
	wait()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Takt endet nicht")
	}
}

// esppdhFakeEnv: installierte Version oldVersion (statt „esphome version“ aufzurufen).
func esppdhFakeEnv(t *testing.T, version string) {
	t.Helper()
	old := esppdhCheckEnv
	t.Cleanup(func() { esppdhCheckEnv = old })
	esppdhCheckEnv = func(context.Context) esppdh.EnvState {
		return esppdh.EnvState{Installed: true, Version: version, Base: true, FreeMB: -1}
	}
}

// newEsppdhUpd: Labor-Datenbank mit eingeschaltetem ESPPDH (newEsppdhBuildEnv: Umgebung
// „eingerichtet“, keine wartenden Bauten), Schalter „selbst aktuell halten“ an, installiert
// 2026.10.2, auf PyPI 2026.10.2 und 2026.11.1 (seit 9 Tagen), Versionen in einem Temp-Ordner,
// Agent und PyPI als httptest, Attrappe als Prober und ein Benutzer mit esppdh.admin.
func newEsppdhUpd(t *testing.T) *esppdhUpdT {
	t.Helper()
	e, fb, _ := newEsppdhBuildEnv(t)
	u := &esppdhUpdT{esppdhEnvT: e, fb: fb, pypiBody: esppdhPyPIBody(
		esppdhPyPIRel{esppdhUpdOld, esppdhUpdNow.Add(-30 * 24 * time.Hour)},
		esppdhPyPIRel{esppdhUpdNew, esppdhUpdNow.Add(-9 * 24 * time.Hour)})}
	t.Setenv("PDH_ESPHOME_VERSIONS_DIR", t.TempDir())
	esppdhKeepSetting(t, e.h, esppdhSetAuto, "1")
	esppdhFakeEnv(t, esppdhUpdOld)
	esppdhFastUpdate(t)

	pypi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.pypiHits++
		fn, body := u.pypiFn, u.pypiBody
		u.mu.Unlock()
		if r.Header.Get("User-Agent") != "pdh-server" || r.URL.Path != "/pypi/esphome/json" {
			t.Errorf("PyPI-Anfrage: %s %q", r.URL.Path, r.Header.Get("User-Agent"))
		}
		if fn != nil {
			fn(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(pypi.Close)
	agent := httptest.NewServer(http.HandlerFunc(u.agentServe(t)))
	t.Cleanup(agent.Close)

	e.h.updateAgentURL, e.h.updateAgentToken = agent.URL, "tok"
	e.h.esppdhPyPIURL = pypi.URL + "/pypi/esphome/json"
	e.h.esppdhNow = func() time.Time { return esppdhUpdNow }
	e.h.esppdhProber = func(bin string) esppdhProbeRunner { return &esppdhFakeProber{u: u, bin: bin} }

	ctx := context.Background()
	if err := e.pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Esp', $1::text, 'technician', '', '') RETURNING id::text`, "espupd"+e.sfx).Scan(&u.admin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, u.admin) })
	if _, err := e.pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted)
		SELECT $1::uuid, id, true FROM permissions WHERE key = $2`, u.admin, permESPAdmin); err != nil {
		t.Fatal(err)
	}
	return u
}

// agentServe: Update-Agent im Test – Einrichtung (zwei Abfragen „läuft“, dann fertig, legt
// bin/esphome des Kandidaten an), Versionen (activate, discard).
func (u *esppdhUpdT) agentServe(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Agent ohne Schlüssel: %s %s", r.Method, r.URL.Path)
		}
		var req struct{ Action, Version string }
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			_ = json.Unmarshal(body, &req)
		}
		w.Header().Set("Content-Type", "application/json")
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.tooOld && strings.HasPrefix(r.URL.Path, "/v1/esppdh/versions") {
			http.NotFound(w, r)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/esppdh/versions":
			u.ev = append(u.ev, "versions")
			io.WriteString(w, `{"ok":true,"active":"","previous":"","installed":[]}`)
		case "POST /v1/esppdh/install":
			u.ev = append(u.ev, "install "+req.Version)
			if !u.otherTarget {
				u.jobRunning, u.jobTarget, u.jobPolls = true, req.Version, 0
				if !u.noBin {
					bin := esppdh.VersionBin(req.Version)
					if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
						t.Error(err)
					}
					if err := os.WriteFile(bin, []byte("attrappe"), 0o755); err != nil {
						t.Error(err)
					}
				}
			}
			io.WriteString(w, `{"ok":true,"running":true}`)
		case "GET /v1/esppdh/install":
			if u.otherTarget {
				io.WriteString(w, `{"ok":true,"running":true,"done":false}`)
				return
			}
			if u.jobRunning {
				if u.jobPolls++; u.jobPolls < 2 {
					json.NewEncoder(w).Encode(map[string]any{"ok": true, "running": true, "target": u.jobTarget})
					return
				}
				u.jobRunning = false
			}
			st := map[string]any{"ok": true, "running": false, "done": true, "target": u.jobTarget}
			if u.installErr != "" {
				st["error"] = u.installErr
			} else {
				st["version"] = u.jobTarget
			}
			json.NewEncoder(w).Encode(st)
		case "POST /v1/esppdh/versions":
			u.ev = append(u.ev, req.Action+" "+req.Version)
			io.WriteString(w, `{"ok":true,"active":"","previous":"","installed":[]}`)
		default:
			t.Errorf("unerwartete Anfrage an den Agenten: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

// esppdhFakeProber: Attrappe von ESPHome. „neu“ = Programm des Kandidaten (im Versionen-Ordner),
// „alt“ = laufende Version. Ein Fehler liefert einen Verlauf mit den Kennwörtern im Klartext.
type esppdhFakeProber struct {
	u   *esppdhUpdT
	bin string
}

func (p *esppdhFakeProber) cand() bool {
	return strings.HasPrefix(p.bin, esppdh.VersionsDirPath()+string(filepath.Separator))
}

func (p *esppdhFakeProber) tag() string {
	if p.cand() {
		return " neu"
	}
	return " alt"
}

func (p *esppdhFakeProber) check(job esppdh.BuildJob) {
	if !meterUUIDRe.MatchString(job.ID) || len(job.Config) == 0 || len(job.Secrets) == 0 || len(job.SecretValues) == 0 {
		p.u.add("ungültiger Auftrag " + job.Slug)
	}
}

func (p *esppdhFakeProber) Config(_ context.Context, job esppdh.BuildJob) (string, error) {
	p.check(job)
	p.u.add("config " + job.Slug + p.tag())
	p.u.mu.Lock()
	fail := p.u.configFail != nil && p.u.configFail(job.Slug, p.cand())
	crash := p.u.configPanic
	p.u.mu.Unlock()
	if crash {
		panic("Attrappe stürzt ab")
	}
	if fail {
		return "INFO Reading configuration ./esppdh.yaml...\nERROR Ungültig: " + strings.Join(job.SecretValues, " ") + "\n",
			errors.New("ESPHome hat mit einem Fehler beendet (Exit-Code 2).")
	}
	return "INFO Configuration is valid!\n", nil
}

func (p *esppdhFakeProber) Build(_ context.Context, job esppdh.BuildJob, _ func(string)) esppdh.BuildResult {
	p.check(job)
	p.u.add("build " + job.Slug + p.tag())
	p.u.mu.Lock()
	fail := p.u.buildFail != nil && p.u.buildFail(job.Slug)
	p.u.mu.Unlock()
	if fail {
		return esppdh.BuildResult{Log: "INFO ESPHome " + esppdhUpdNew + "\nerror: " + strings.Join(job.SecretValues, " ") + " passt nicht\n"}
	}
	return esppdh.BuildResult{OK: true, Log: "Firmware gebaut.\n", Factory: []byte("F"), OTA: []byte("O"), Version: esppdhUpdNew}
}

// device legt ein baubares Gerät an (Platine board, ein S0-Zähler an GPIO4). wlan "" = Test-WLAN.
func (u *esppdhUpdT) device(t *testing.T, slug, name, board, wlan string) string {
	t.Helper()
	if wlan == "" {
		wlan = u.wlan
	}
	id := u.create(t, slug, url.Values{"name": {name}, "board_key": {board}, "net": {wlan}})
	v := url.Values{"name": {name}, "infrastructure_id": {u.infra}, "connection_id": {u.conn}, "board_key": {board}, "net": {wlan}}
	esppdhAddBlock(v, "0", "", "s0", map[string]string{"pin": "GPIO4", "pulses": "1000"})
	if w := esppdhDo(u.h.EsppdhSaveWeb, "POST", "/import/esppdh/"+id, id, esppdhUser, v); w.Code != http.StatusSeeOther {
		t.Fatalf("Gerät %s speichern: %d %s", slug, w.Code, w.Body.String())
	}
	return id
}

// mine: Ereignisse zu Geräten dieses Tests (Kennung endet auf sfx) und alle des Agenten.
func (u *esppdhUpdT) mine() []string {
	var out []string
	for _, e := range u.events() {
		if !strings.HasPrefix(e, "config ") && !strings.HasPrefix(e, "build ") {
			out = append(out, e)
			continue
		}
		if f := strings.Fields(e); strings.HasSuffix(f[1], u.sfx) || esppdhSampleSlug(f[1]) {
			out = append(out, e)
		}
	}
	return out
}

func esppdhSampleSlug(slug string) bool {
	for _, s := range esppdh.Samples() {
		if s.Device.Slug == slug {
			return true
		}
	}
	return false
}

// notes: Meldungen von PDH-System an den Testbenutzer mit esppdh.admin.
func (u *esppdhUpdT) notes(t *testing.T) int {
	t.Helper()
	var n int
	if err := u.pool.QueryRow(context.Background(), `SELECT count(*) FROM chat_messages m
		JOIN chat_members cm ON cm.conversation_id = m.conversation_id AND cm.user_id = $1::uuid
		WHERE m.user_id = $2::uuid AND m.body LIKE '%ESPHome%'`, u.admin, pdhSystemUserID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// run: ein Lauf; Fehler beenden den Test.
func (u *esppdhUpdT) run(t *testing.T, force bool) (string, esppdh.UpdateState) {
	t.Helper()
	msg, err := u.h.esppdhUpdateRun(context.Background(), force)
	if err != nil {
		t.Fatalf("Lauf: %v (%s)", err, msg)
	}
	return msg, esppdh.LoadUpdateState()
}

func esppdhIndex(ev []string, prefix string) []int {
	var out []int
	for i, e := range ev {
		if strings.HasPrefix(e, prefix) {
			out = append(out, i)
		}
	}
	return out
}

func esppdhCount(ev []string, prefix string) int { return len(esppdhIndex(ev, prefix)) }

// ── ohne Datenbank ───────────────────────────────────────────────────────

// Docker-Betrieb und ausgeschaltetes ESPPDH: nichts tun, kein Fehler – weder PyPI noch Agent.
func TestUpdateDocker(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	t.Cleanup(srv.Close)
	h := &Handler{updateAgentURL: srv.URL, updateAgentToken: "tok", esppdhPyPIURL: srv.URL}
	esppdhTestEnv(t, true)
	if msg, err := h.esppdhUpdateRun(context.Background(), true); err != nil || msg != "ESPPDH gibt es im Docker-Betrieb nicht." {
		t.Errorf("Docker: %q %v", msg, err)
	}
	esppdhDockerHook = func() bool { return false }
	if msg, err := h.esppdhUpdateRun(context.Background(), true); err != nil || msg != esppdhMsgOff {
		t.Errorf("ohne Datenbank (ESPPDH aus): %q %v", msg, err)
	}
	if calls != 0 {
		t.Errorf("%d Anfragen", calls)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("PDH_ESPHOME_WORKDIR"), "update-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Zustand geschrieben: %v", err)
	}
	if h.esppdhUpd.running {
		t.Error("Lauf gilt noch als laufend")
	}
}

func TestEsppdhPythonOf(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "esphome")
	if got := esppdhPythonOf(bin); got != [2]int{} {
		t.Errorf("ohne pyvenv.cfg: %v", got)
	}
	for cfg, want := range map[string][2]int{
		"home = /usr/bin\ninclude-system-site-packages = false\nversion = 3.12.3\n": {3, 12},
		"home = /usr/bin\nversion_info = 3.11.2.final.0\n":                          {3, 11},
		"home = /usr/bin\nversion = drei\n":                                         {0, 0},
	} {
		if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := esppdhPythonOf(bin); got != want {
			t.Errorf("%q: %v, erwartet %v", cfg, got, want)
		}
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

// Keine neuere, ausgereifte stabile Version: „aktuell“, der Agent wird nicht gefragt.
func TestUpdateNoCandidate(t *testing.T) {
	u := newEsppdhUpd(t)
	body := esppdhPyPIBody(
		esppdhPyPIRel{esppdhUpdOld, esppdhUpdNow.Add(-30 * 24 * time.Hour)},
		esppdhPyPIRel{"2026.11.2", esppdhUpdNow.Add(-24 * time.Hour)},       // zu jung
		esppdhPyPIRel{"2026.12.0b1", esppdhUpdNow.Add(-9 * 24 * time.Hour)}) // Vorabversion
	u.set(func() { u.pypiBody = body })
	msg, st := u.run(t, false)
	if want := "ESPHome " + esppdhUpdOld + " ist aktuell"; msg != want || st.Result != want {
		t.Errorf("Ergebnis %q / %q", msg, st.Result)
	}
	if !st.CheckedAt.Equal(esppdhUpdNow) || st.Installed != esppdhUpdOld || st.FailedVersion != "" {
		t.Errorf("Zustand: %+v", st)
	}
	if ev := u.events(); len(ev) != 0 || u.hits() != 1 {
		t.Errorf("Agent gefragt oder PyPI nicht genau einmal: %v, %d", ev, u.hits())
	}
}

// Probe bestanden: einrichten → Konfiguration aller Muster und aller nicht gesperrten,
// auflösbaren Geräte → je Chip-Familie ein Probebau (erstes Gerät) → umschalten.
func TestUpdateSwitches(t *testing.T) {
	u := newEsppdhUpd(t)
	a := "ua-" + u.sfx
	b := "ub-" + u.sfx
	c := "uc-" + u.sfx
	locked := "ud-" + u.sfx
	gone := "ue-" + u.sfx
	u.device(t, a, "0upd A "+u.sfx, "esp32dev", "")
	u.device(t, b, "0upd B "+u.sfx, "esp32dev", "")
	u.device(t, c, "0upd C "+u.sfx, "esp32-s3", "")
	lid := u.device(t, locked, "0upd D "+u.sfx, "esp32dev", "")
	if _, err := u.pool.Exec(context.Background(), `UPDATE esppdh_devices SET locked = true WHERE id = $1::uuid`, lid); err != nil {
		t.Fatal(err)
	}
	office := u.addWLAN(t, "pdh-upd-"+u.sfx, "Buero-Geheim-"+u.sfx, "")
	u.device(t, gone, "0upd E "+u.sfx, "esp32dev", office)
	u.esppdhReleaseWLAN(t, office, false) // WLAN nicht mehr freigegeben: lässt sich nicht auflösen

	msg, st := u.run(t, false)
	want := "Auf ESPHome " + esppdhUpdNew + " umgestellt"
	if msg != want || st.Result != want {
		t.Errorf("Ergebnis %q / %q", msg, st.Result)
	}
	if st.Installed != esppdhUpdNew || st.FailedVersion != "" || !st.CheckedAt.Equal(esppdhUpdNow) {
		t.Errorf("Zustand: %+v", st)
	}
	ev := u.mine()
	for _, s := range esppdh.Samples() {
		if !slices.Contains(ev, "config "+s.Device.Slug+" neu") {
			t.Errorf("Muster %s nicht geprüft: %v", s.Name, ev)
		}
	}
	for _, slug := range []string{a, b, c} {
		if !slices.Contains(ev, "config "+slug+" neu") {
			t.Errorf("Gerät %s nicht geprüft: %v", slug, ev)
		}
	}
	for _, slug := range []string{locked, gone} {
		if esppdhCount(ev, "config "+slug) > 0 || esppdhCount(ev, "build "+slug) > 0 {
			t.Errorf("gesperrtes oder nicht auflösbares Gerät %s geprüft: %v", slug, ev)
		}
	}
	builds := esppdhIndex(ev, "build ")
	if len(builds) != 2 || ev[builds[0]] != "build "+a+" neu" || ev[builds[1]] != "build "+c+" neu" {
		t.Errorf("Probebau je Chip-Familie (erstes Gerät): %v", ev)
	}
	install, activate := esppdhIndex(ev, "install "+esppdhUpdNew), esppdhIndex(ev, "activate "+esppdhUpdNew)
	configs := esppdhIndex(ev, "config ")
	if len(install) != 1 || len(activate) != 1 || len(configs) == 0 || len(builds) == 0 || install[0] > configs[0] ||
		configs[len(configs)-1] > builds[0] || builds[len(builds)-1] > activate[0] || esppdhCount(ev, "discard") > 0 {
		t.Errorf("Reihenfolge install → config → build → activate: %v", ev)
	}
	if esppdhCount(u.events(), "ungültiger Auftrag") > 0 {
		t.Errorf("Auftrag ohne Kennung, Konfiguration oder Kennwörter: %v", ev)
	}
	// keine Kennwörter in der Zustandsdatei
	raw, err := os.ReadFile(filepath.Join(os.Getenv("PDH_ESPHOME_WORKDIR"), "update-state.json"))
	if err != nil || strings.Contains(string(raw), u.wlPW) || strings.Contains(string(raw), u.mqttPW(t, lid)) {
		t.Errorf("Zustandsdatei: %v\n%s", err, raw)
	}
	if u.notes(t) != 0 {
		t.Error("Meldung trotz bestandener Probe")
	}
}

// Probe gescheitert: Kandidat verwerfen, Zustand mit geschwärztem Verlauf, genau eine Meldung;
// der nächste Lauf ohne „Jetzt prüfen“ fragt den Agenten nicht mehr; ein erneuter Fehlschlag
// über den Knopf meldet nicht noch einmal.
func TestUpdateProbeFails(t *testing.T) {
	u := newEsppdhUpd(t)
	a := "fa-" + u.sfx
	u.device(t, a, "0upd Fehler "+u.sfx, "esp32dev", "")
	u.set(func() { u.configFail = func(slug string, cand bool) bool { return slug == a && cand } })

	msg, st := u.run(t, false)
	want := "ESPHome " + esppdhUpdNew + " hat die Probe nicht bestanden – es bleibt bei " + esppdhUpdOld
	if msg != want || st.Result != want {
		t.Errorf("Ergebnis %q / %q", msg, st.Result)
	}
	if st.FailedVersion != esppdhUpdNew || st.FailedPDH != pdh.Version() || st.NotifiedVersion != esppdhUpdNew || st.Installed != esppdhUpdOld {
		t.Errorf("Zustand: %+v", st)
	}
	if !strings.Contains(st.FailedLog, "0upd Fehler "+u.sfx) || !strings.Contains(st.FailedLog, "•••") ||
		strings.Contains(st.FailedLog, u.wlPW) || strings.Count(st.FailedLog, "\n") > 40 {
		t.Errorf("Verlauf:\n%s", st.FailedLog)
	}
	ev := u.mine()
	if !slices.Contains(ev, "config "+a+" neu") || !slices.Contains(ev, "config "+a+" alt") || !slices.Contains(ev, "discard "+esppdhUpdNew) ||
		esppdhCount(ev, "activate") > 0 || esppdhCount(ev, "build ") > 0 {
		t.Errorf("Ablauf: %v", ev)
	}
	if n := u.notes(t); n != 1 {
		t.Errorf("%d Meldungen, erwartet 1", n)
	}

	// nächster Tag, gleiche PDH-Version: nicht erneut versuchen
	before := len(u.events())
	msg, st = u.run(t, false)
	if ev := u.events()[before:]; len(ev) != 0 || msg != "ESPHome "+esppdhUpdOld+" ist aktuell" || st.FailedVersion != esppdhUpdNew {
		t.Errorf("zweiter Lauf: %q %v %+v", msg, ev, st)
	}
	// „Jetzt prüfen“: erneuter Versuch, scheitert wieder – keine zweite Meldung
	msg, st = u.run(t, true)
	if !strings.HasPrefix(msg, "ESPHome "+esppdhUpdNew+" hat die Probe nicht bestanden") || st.FailedVersion != esppdhUpdNew {
		t.Errorf("mit Knopf: %q %+v", msg, st)
	}
	if n := u.notes(t); n != 1 {
		t.Errorf("%d Meldungen nach dem zweiten Fehlschlag, erwartet 1", n)
	}
}

// Nach einem PDH-Update (andere PDH-Version) wird eine gescheiterte Version erneut versucht.
func TestUpdateRetryAfterPDHUpdate(t *testing.T) {
	u := newEsppdhUpd(t)
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{FailedVersion: esppdhUpdNew, FailedPDH: "0.0.1", NotifiedVersion: esppdhUpdNew, FailedLog: "alt"}); err != nil {
		t.Fatal(err)
	}
	msg, st := u.run(t, false)
	if msg != "Auf ESPHome "+esppdhUpdNew+" umgestellt" || st.FailedVersion != "" || st.FailedLog != "" || st.FailedPDH != "" {
		t.Errorf("%q %+v", msg, st)
	}
	if !slices.Contains(u.events(), "install "+esppdhUpdNew) {
		t.Errorf("nicht erneut eingerichtet: %v", u.events())
	}
}

// Gleiche PDH-Version: nur „Jetzt prüfen“ (force) versucht die gescheiterte Version erneut.
func TestUpdateForceRetries(t *testing.T) {
	u := newEsppdhUpd(t)
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{FailedVersion: esppdhUpdNew, FailedPDH: pdh.Version(), NotifiedVersion: esppdhUpdNew}); err != nil {
		t.Fatal(err)
	}
	if msg, _ := u.run(t, false); msg != "ESPHome "+esppdhUpdOld+" ist aktuell" || len(u.events()) != 0 {
		t.Errorf("ohne Knopf: %q %v", msg, u.events())
	}
	msg, st := u.run(t, true)
	if msg != "Auf ESPHome "+esppdhUpdNew+" umgestellt" || st.FailedVersion != "" || !slices.Contains(u.events(), "activate "+esppdhUpdNew) {
		t.Errorf("mit Knopf: %q %+v %v", msg, st, u.events())
	}
}

// Ein Gerät, das schon mit der laufenden Version nicht durch die Prüfung kommt, lässt den
// Kandidaten nicht scheitern; es wird im Ergebnis als übersprungen genannt und nicht gebaut.
func TestProbeIgnoresAlreadyBroken(t *testing.T) {
	u := newEsppdhUpd(t)
	ok, broken := "pa-"+u.sfx, "pb-"+u.sfx
	u.device(t, broken, "0upd Kaputt "+u.sfx, "esp32dev", "")
	u.device(t, ok, "0upd Gut "+u.sfx, "esp32dev", "")
	u.set(func() { u.configFail = func(slug string, _ bool) bool { return slug == broken } })
	msg, st := u.run(t, false)
	if !strings.HasPrefix(msg, "Auf ESPHome "+esppdhUpdNew+" umgestellt") || !strings.Contains(msg, "übersprungen") ||
		!strings.Contains(msg, "0upd Kaputt "+u.sfx) || st.FailedVersion != "" {
		t.Errorf("%q %+v", msg, st)
	}
	ev := u.mine()
	if !slices.Contains(ev, "config "+broken+" neu") || !slices.Contains(ev, "config "+broken+" alt") ||
		esppdhCount(ev, "build "+broken) > 0 || !slices.Contains(ev, "build "+ok+" neu") {
		t.Errorf("Ablauf: %v", ev)
	}
}

// Ein laufender Bau hält runMu: Die Probe (Konfiguration wie Probebau) startet erst danach.
func TestProbeWaitsForBuild(t *testing.T) {
	u := newEsppdhUpd(t)
	u.start(t)
	a := "wa-" + u.sfx
	id := u.device(t, a, "0upd Warten "+u.sfx, "esp32dev", "")
	bid := u.queue(t, id)
	esppdhNextJob(t, u.fb) // der echte Bau läuft
	done := make(chan string, 1)
	go func() {
		msg, _ := u.h.esppdhUpdateRun(context.Background(), false)
		done <- msg
	}()
	time.Sleep(300 * time.Millisecond)
	if ev := u.events(); esppdhCount(ev, "config ") > 0 || esppdhCount(ev, "build ") > 0 {
		t.Errorf("Probe läuft während eines Baus: %v", ev)
	}
	u.add("bau fertig")
	u.fb.gate(bid) <- esppdhBuildOK("x")
	var msg string
	select {
	case msg = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Probe endet nicht")
	}
	u.waitStatus(t, bid, "ok")
	ev := u.mine()
	fin, cfg := esppdhIndex(ev, "bau fertig"), esppdhIndex(ev, "config ")
	if msg != "Auf ESPHome "+esppdhUpdNew+" umgestellt" || len(fin) != 1 || len(cfg) == 0 || cfg[0] < fin[0] || !slices.Contains(ev, "build "+a+" neu") {
		t.Errorf("%q %v", msg, ev)
	}
}

// Wartet ein Bau länger als die Frist, wird die Probe vertagt – kein Fehlschlag, Kandidat weg.
func TestProbeQueueBusy(t *testing.T) {
	u := newEsppdhUpd(t)
	esppdhIdleLimit = 50 * time.Millisecond
	id := u.device(t, "qb-"+u.sfx, "0upd Schlange "+u.sfx, "esp32dev", "")
	u.queue(t, id) // kein Arbeiter: der Auftrag wartet
	msg, st := u.run(t, false)
	if msg != "Probe vertagt – Bau-Schlange belegt" || st.FailedVersion != "" || u.notes(t) != 0 {
		t.Errorf("%q %+v", msg, st)
	}
	ev := u.mine()
	if esppdhCount(ev, "build ") > 0 || !slices.Contains(ev, "discard "+esppdhUpdNew) || esppdhCount(ev, "activate") > 0 {
		t.Errorf("Ablauf: %v", ev)
	}
}

// Fehlt der Kandidat zur Probe (vom Agenten aufgeräumt), ist das kein Fehlschlag der Version.
func TestProbeCandidateGone(t *testing.T) {
	u := newEsppdhUpd(t)
	u.set(func() { u.noBin = true })
	msg, st := u.run(t, false)
	if !strings.Contains(msg, "nicht mehr vorhanden") || st.FailedVersion != "" || u.notes(t) != 0 {
		t.Errorf("%q %+v", msg, st)
	}
	if ev := u.mine(); esppdhCount(ev, "config ") > 0 || esppdhCount(ev, "activate") > 0 {
		t.Errorf("Ablauf: %v", ev)
	}
}

// Am Spiegel: nur Konfigurationsprüfung, kein Probebau; eine gescheiterte Probe wird vermerkt,
// aber nicht gemeldet (Datenbank nur lesbar).
func TestUpdateMirrorNoBuild(t *testing.T) {
	u := newEsppdhUpd(t)
	a := "ma-" + u.sfx
	u.device(t, a, "0upd Spiegel "+u.sfx, "esp32dev", "")
	u.h.SetMirror(MirrorInfo{On: true})
	msg, _ := u.run(t, false)
	ev := u.mine()
	if msg != "Auf ESPHome "+esppdhUpdNew+" umgestellt" || !slices.Contains(ev, "config "+a+" neu") || esppdhCount(ev, "build ") > 0 ||
		!slices.Contains(ev, "activate "+esppdhUpdNew) {
		t.Errorf("%q %v", msg, ev)
	}
	u.set(func() { u.configFail = func(slug string, cand bool) bool { return slug == a && cand } })
	msg, st := u.run(t, false)
	if !strings.HasPrefix(msg, "ESPHome "+esppdhUpdNew+" hat die Probe nicht bestanden") || st.FailedVersion != esppdhUpdNew || st.NotifiedVersion != "" {
		t.Errorf("gescheitert am Spiegel: %q %+v", msg, st)
	}
	if n := u.notes(t); n != 0 {
		t.Errorf("%d Meldungen am Spiegel", n)
	}
}

// PyPI liefert Unbrauchbares: „Prüfung nicht möglich“ mit Zeitpunkt, der Agent wird nicht gefragt.
func TestUpdateCheckBadPyPI(t *testing.T) {
	u := newEsppdhUpd(t)
	big := `{"releases":{"x":[` + strings.Repeat(`{"yanked":false},`, 5<<20/17) + `{}]}}`
	for _, c := range []struct {
		name, want string
		fn         func(w http.ResponseWriter, r *http.Request)
	}{
		{"HTML", "nicht lesbar", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><body>Wartung</body></html>")
		}},
		{"500", "HTTP 500", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "kaputt", http.StatusInternalServerError) }},
		{"Zeitlimit", "antwortet nicht", func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}},
		{"5 MB", "größer als 4 MB", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, big) }},
		{"leer", "keine Ausgaben", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"releases":{}}`) }},
	} {
		u.set(func() { u.pypiFn = c.fn })
		esppdhPyPITimeout = 5 * time.Second // nur „Zeitlimit“ kurz – sonst wackelt es auf langsamen Rechnern
		if c.name == "Zeitlimit" {
			esppdhPyPITimeout = 200 * time.Millisecond
		}
		msg, st := u.run(t, false)
		if !strings.HasPrefix(msg, "Prüfung nicht möglich: ") || !strings.Contains(msg, c.want) || st.Result != msg ||
			!st.CheckedAt.Equal(esppdhUpdNow) || st.Installed != esppdhUpdOld {
			t.Errorf("%s: %q %+v", c.name, msg, st)
		}
	}
	if ev := u.events(); len(ev) != 0 {
		t.Errorf("Agent gefragt: %v", ev)
	}
}

// Die Einrichtung scheitert (z. B. Platz, pip): Version vermerken, nicht täglich neu versuchen.
func TestUpdateInstallFails(t *testing.T) {
	u := newEsppdhUpd(t)
	u.set(func() {
		u.installErr = "Einrichtung fehlgeschlagen: Zu wenig Platz unter /opt (frei 900 MB, nötig 3072 MB).\n\nLetzte Ausgabe:\nzeile"
	})
	msg, st := u.run(t, false)
	want := "ESPHome " + esppdhUpdNew + " ließ sich nicht einrichten – es bleibt bei " + esppdhUpdOld
	if msg != want || st.FailedVersion != esppdhUpdNew || st.FailedPDH != pdh.Version() || !strings.Contains(st.FailedLog, "Zu wenig Platz") {
		t.Errorf("%q %+v", msg, st)
	}
	ev := u.mine()
	if esppdhCount(ev, "config ") > 0 || esppdhCount(ev, "activate") > 0 || !slices.Contains(ev, "discard "+esppdhUpdNew) {
		t.Errorf("Ablauf: %v", ev)
	}
	before := len(u.events())
	if msg, _ := u.run(t, false); len(u.events()) != before {
		t.Errorf("zweiter Lauf richtet erneut ein: %q %v", msg, u.events()[before:])
	}
}

// Ein Update-Agent ohne ESPHome-Versionen (404): Hinweis, die Version gilt nicht als gescheitert.
func TestUpdateAgentTooOld(t *testing.T) {
	u := newEsppdhUpd(t)
	u.set(func() { u.tooOld = true })
	msg, st := u.run(t, false)
	if msg != "Der Update-Agent ist zu alt – zuerst das PDH aktualisieren" || st.Result != msg || st.FailedVersion != "" || !st.CheckedAt.Equal(esppdhUpdNow) {
		t.Errorf("%q %+v", msg, st)
	}
	if ev := u.events(); esppdhCount(ev, "install") > 0 {
		t.Errorf("trotzdem eingerichtet: %v", ev)
	}
}

// Läuft beim Agenten schon eine andere Einrichtung, endet der Lauf mit Hinweis – ohne Vermerk.
func TestUpdateInstallOtherTarget(t *testing.T) {
	u := newEsppdhUpd(t)
	u.set(func() { u.otherTarget = true })
	msg, st := u.run(t, false)
	if !strings.Contains(msg, "andere ESPHome-Umgebung") || st.FailedVersion != "" || u.notes(t) != 0 {
		t.Errorf("%q %+v", msg, st)
	}
	if ev := u.mine(); esppdhCount(ev, "config ") > 0 || esppdhCount(ev, "discard") > 0 || esppdhCount(ev, "activate") > 0 {
		t.Errorf("Ablauf: %v", ev)
	}
}

// Schalter „selbst aktuell halten“ aus: der tägliche Lauf tut nichts, „Jetzt prüfen“ läuft.
func TestUpdateSwitchOff(t *testing.T) {
	u := newEsppdhUpd(t)
	esppdhKeepSetting(t, u.h, esppdhSetAuto, "0")
	if u.h.esppdhAutoUpdate(context.Background()) {
		t.Error("Schalter aus wird nicht erkannt")
	}
	msg, err := u.h.esppdhUpdateRun(context.Background(), false)
	if err != nil || msg != "„ESPHome selbst aktuell halten“ ist ausgeschaltet." || u.hits() != 0 || len(u.events()) != 0 {
		t.Errorf("ohne Knopf: %q %v %d %v", msg, err, u.hits(), u.events())
	}
	if st := esppdh.LoadUpdateState(); !st.CheckedAt.IsZero() {
		t.Errorf("Zustand geschrieben: %+v", st)
	}
	if msg, _ := u.run(t, true); msg != "Auf ESPHome "+esppdhUpdNew+" umgestellt" {
		t.Errorf("mit Knopf: %q", msg)
	}
	esppdhKeepSetting(t, u.h, esppdhSetAuto, "irgendwas")
	if !u.h.esppdhAutoUpdate(context.Background()) {
		t.Error("nur „0“ heißt aus")
	}
}

// Höchstens ein Lauf zur Zeit – auch zwischen Takt und Knopf.
func TestUpdateSingleFlight(t *testing.T) {
	u := newEsppdhUpd(t)
	release := make(chan struct{})
	u.set(func() {
		u.pypiFn = func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			io.WriteString(w, `{"releases":{}}`)
		}
	})
	done := make(chan string, 1)
	go func() {
		msg, _ := u.h.esppdhUpdateRun(context.Background(), false)
		done <- msg
	}()
	deadline := time.Now().Add(5 * time.Second)
	for u.hits() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if msg, err := u.h.esppdhUpdateRun(context.Background(), true); err != nil || msg != "Eine Prüfung läuft schon" {
		t.Errorf("zweiter Lauf: %q %v", msg, err)
	}
	running := func() bool {
		u.h.esppdhUpd.mu.Lock()
		defer u.h.esppdhUpd.mu.Unlock()
		return u.h.esppdhUpd.running
	}
	if !running() {
		t.Error("laufender Lauf nicht vermerkt")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("erster Lauf endet nicht")
	}
	if u.hits() != 1 || running() {
		t.Errorf("PyPI %d-mal gefragt, läuft noch: %v", u.hits(), running())
	}
}

// Takt: fällig erst 24 h nach der letzten Prüfung.
func TestUpdateScheduler(t *testing.T) {
	u := newEsppdhUpd(t)
	u.set(func() { u.pypiBody = `{"releases":{}}` })
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{CheckedAt: esppdhUpdNow.Add(-23 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	u.loop(t, func() { time.Sleep(100 * time.Millisecond) })
	if u.hits() != 0 {
		t.Errorf("vor Ablauf von 24 h geprüft: %d", u.hits())
	}
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{CheckedAt: esppdhUpdNow.Add(-25 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	u.loop(t, func() {
		deadline := time.Now().Add(5 * time.Second)
		for u.hits() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond) // weitere Takte: nicht mehr fällig
	})
	if st := esppdh.LoadUpdateState(); u.hits() != 1 || !st.CheckedAt.Equal(esppdhUpdNow) {
		t.Errorf("fällig: %d Abfragen, Zustand %+v", u.hits(), st)
	}
}

// Lässt sich der Zustand nicht speichern (Arbeitsordner kaputt, Platte voll), meldet der Lauf den
// Fehler, wiederholt sich aber nicht im 5-Minuten-Takt und meldet dieselbe Version nicht noch
// einmal – auch nicht über „Jetzt prüfen“.
func TestUpdateSaveFailsBacksOff(t *testing.T) {
	u := newEsppdhUpd(t)
	a := "sf-" + u.sfx
	u.device(t, a, "0upd Speicher "+u.sfx, "esp32dev", "")
	u.set(func() { u.configFail = func(slug string, cand bool) bool { return slug == a && cand } })
	blocker := filepath.Join(t.TempDir(), "datei-statt-ordner")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_ESPHOME_WORKDIR", blocker) // eine Datei statt des Arbeitsordners: nichts lässt sich speichern
	failed := "ESPHome " + esppdhUpdNew + " hat die Probe nicht bestanden"

	msg, err := u.h.esppdhUpdateRun(context.Background(), false)
	if err == nil || !strings.HasPrefix(msg, failed) {
		t.Fatalf("erster Lauf: %q %v", msg, err)
	}
	if u.h.esppdhUpdateDue() {
		t.Error("ohne gespeicherten Zustand gleich wieder fällig")
	}
	hits := u.hits()
	u.loop(t, func() { time.Sleep(100 * time.Millisecond) })
	if u.hits() != hits {
		t.Errorf("Takt wiederholt die Prüfung: %d Abfragen, vorher %d", u.hits(), hits)
	}
	if n := u.notes(t); n != 1 {
		t.Errorf("%d Meldungen, erwartet 1", n)
	}
	msg, err = u.h.esppdhUpdateRun(context.Background(), true)
	if err == nil || !strings.HasPrefix(msg, failed) {
		t.Errorf("mit Knopf: %q %v", msg, err)
	}
	if n := u.notes(t); n != 1 {
		t.Errorf("%d Meldungen nach dem zweiten Fehlschlag, erwartet 1", n)
	}
}

// Stürzt die Prüfung ab: Fehler statt Absturz des PDH, runMu wieder frei, Lauf vermerkt (Datei
// und Speicher) – der Takt wiederholt ihn nicht gleich.
func TestUpdatePanicBacksOff(t *testing.T) {
	u := newEsppdhUpd(t)
	u.set(func() { u.configPanic = true })
	msg, err := u.h.esppdhUpdateRun(context.Background(), false)
	if err == nil || !strings.HasPrefix(msg, "Interner Fehler bei der Prüfung") {
		t.Fatalf("%q %v", msg, err)
	}
	if !u.h.esppdhQ.runMu.TryLock() {
		t.Fatal("runMu nach dem Absturz noch belegt")
	}
	u.h.esppdhQ.runMu.Unlock()
	if st := esppdh.LoadUpdateState(); !st.CheckedAt.Equal(esppdhUpdNow) || st.Result != msg {
		t.Errorf("Zustand: %+v", st)
	}
	if u.h.esppdhUpdateDue() || u.h.esppdhUpd.running {
		t.Errorf("fällig %v, läuft %v", u.h.esppdhUpdateDue(), u.h.esppdhUpd.running)
	}
}

// Baut gerade eine Probe (runMu belegt), übernimmt der Arbeiter keinen Auftrag: Er bleibt
// „wartet“ und lässt sich sofort abbrechen – mit „Von Hand abgebrochen.“, nicht als Fehlschlag.
func TestProbeCancelWaitingBuild(t *testing.T) {
	u := newEsppdhUpd(t)
	u.start(t)
	id := u.device(t, "cw-"+u.sfx, "0upd Abbrechen "+u.sfx, "esp32dev", "")
	u.h.esppdhQ.runMu.Lock() // wie ein laufender Probebau
	locked := true
	t.Cleanup(func() { // vor StopESPPDH (später angemeldet, früher ausgeführt)
		if locked {
			u.h.esppdhQ.runMu.Unlock()
		}
	})
	bid := u.queue(t, id)
	time.Sleep(200 * time.Millisecond)
	if st, _ := u.build(t, bid); st != "queued" {
		t.Errorf("Auftrag während der Probe: %q, erwartet queued", st)
	}
	if w := esppdhDoB(u.h.EsppdhBuildCancelWeb, http.MethodPost, id, bid, esppdhUser, false); w.Code != http.StatusSeeOther {
		t.Fatalf("Abbrechen: %d %s", w.Code, w.Body.String())
	}
	if log := u.waitStatus(t, bid, "canceled"); !strings.Contains(log, "Von Hand abgebrochen.") {
		t.Errorf("Verlauf: %q", log)
	}
	u.h.esppdhQ.runMu.Unlock()
	locked = false
	time.Sleep(200 * time.Millisecond)
	select {
	case j := <-u.fb.started:
		t.Errorf("abgebrochener Auftrag gebaut: %s", j.ID)
	default:
	}
	if st, _ := u.build(t, bid); st != "canceled" {
		t.Errorf("nach der Probe: %q", st)
	}
}
