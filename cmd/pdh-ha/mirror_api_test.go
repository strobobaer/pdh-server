package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

// stopNet: fakeNet mit zählendem stopMirrors (wie der echte Transport).
type stopNet struct {
	*fakeNet
	stops atomic.Int32
}

func (s *stopNet) stopMirrors() { s.stops.Add(1) }

type fnRunner func(argv []string) ([]byte, error)

func (f fnRunner) Run(_ context.Context, argv, _ []string, _ []byte) ([]byte, error) { return f(argv) }

// mdt: Daemon eines Servers a mit Partner b und Spiegelung auf Fake-Wegen.
type mdt struct {
	t   *testing.T
	d   *daemon
	fn  *stopNet
	w   *mirrorWiring
	rs  *fakeRsync
	ctx context.Context
	dir string
	clk *testClock
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newMirrorWiring(t *testing.T) (*mirrorWiring, *fakeRsync, *testClock, string) {
	t.Helper()
	dir := t.TempDir()
	rs := &fakeRsync{}
	clk := &testClock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local)}
	return &mirrorWiring{
		repo: filepath.ToSlash(dir) + "/repo", statePath: filepath.Join(dir, "state", "mirror.json"),
		exe: "pdh-ha", rsync: rs, run: rs, now: clk.now,
		fnEnv: func() string { return "" }, fnNC: func(context.Context, ha.Settings) string { return "" },
		scanEvery: 20 * time.Millisecond, loopEvery: 10 * time.Millisecond,
		tune: func(wd *mirrorDeps, _ *mirrorInboxDeps) {
			if wd != nil {
				wd.watchAdd = func(string) error { return nil }
			}
		},
	}, rs, clk, dir
}

// newMDT: persistierte Rolle role, Partner b mit Rolle partnerRole.
func newMDT(t *testing.T, role ha.Role, on bool, partner message) *mdt {
	t.Helper()
	cl := duoCluster()
	cl.Settings.MirrorOn = on
	fn := &stopNet{fakeNet: newFakeNet()}
	partner.Term, partner.Ver, partner.Sees = 1, "test", []string{"a"}
	fn.setView("b", seenView(partner))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, cl, ha.Persisted{Role: role, Term: 1}, fn.fakeNet)
	d.net = fn
	w, rs, clk, dir := newMirrorWiring(t)
	d.mirrorWire = w
	ctx, cancel := context.WithCancel(context.Background())
	m := &mdt{t: t, d: d, fn: fn, w: w, rs: rs, ctx: ctx, dir: dir, clk: clk}
	t.Cleanup(func() {
		cancel()
		if rt := m.rt(); rt != nil {
			rt.wg.Wait()
		}
	})
	return m
}

func (m *mdt) tick() { m.d.tick(m.ctx) }

func (m *mdt) rt() *mirrorRT {
	m.d.mu.Lock()
	defer m.d.mu.Unlock()
	return m.d.mir
}

// ready wartet, bis der Empfänger gebaut ist.
func (m *mdt) ready() *mirrorRT {
	m.t.Helper()
	rt := m.rt()
	if rt == nil {
		m.t.Fatal("keine Spiegelung angelegt")
	}
	waitFor(m.t, 3*time.Second, "Empfänger und Arbeiter gebaut", func() bool { return rt.inbox.Load() != nil && rt.worker.Load() != nil })
	return rt
}

func (m *mdt) setOn(on bool) {
	m.d.lock()
	m.d.cl.Settings.MirrorOn = on
	m.d.unlock()
}

func hasWatch(wk *mirrorWorker) bool {
	wk.mu.Lock()
	defer wk.mu.Unlock()
	return wk.watch != nil
}

func TestEnvSum(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, ".env")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("PDH_PORT=8080\nSECRET=abc\n")
	want := envSum(base)
	if want == "" {
		t.Fatal("keine Summe")
	}
	same := map[string]string{
		"andere Reihenfolge, Kommentar, Leerzeile":        "# Kommentar\n\nSECRET=abc\r\nPDH_PORT=8080\n",
		"Anführungszeichen und export":                    "export SECRET=\"abc\"\nPDH_PORT='8080'\n",
		"Update-Zweig":                                    "PDH_PORT=8080\nSECRET=abc\nPDH_UPDATE_BRANCH=dev\n",
		"Update-Token":                                    "PDH_PORT=8080\nSECRET=abc\nPDH_UPDATE_TOKEN=xyz\n",
		"Serveradresse":                                   "PDH_PORT=8080\nSECRET=abc\nPDH_SERVER_HOST=10.0.0.5\n",
		"Datenbankadresse":                                "PDH_PORT=8080\nSECRET=abc\nPDH_DATABASE_HOST=10.0.0.6\n",
		"Ausfallschutz-Schlüssel PDH_HA_*":                "PDH_PORT=8080\nSECRET=abc\nPDH_HA_SOCKET=/x\nPDH_HA_ANDERES=1\n",
		"Wert eines ausgenommenen Schlüssels anders":      "PDH_PORT=8080\nSECRET=abc\nPDH_UPDATE_BRANCH=main\nPDH_SERVER_HOST=x\n",
		"Zeilen ohne Gleichheitszeichen werden ignoriert": "PDH_PORT=8080\nkaputt\nSECRET=abc\n",
		"Token des Update-Agenten (je Server)":            "PDH_PORT=8080\nSECRET=abc\nPDH_UPDATE_AGENT_TOKEN=t1\n",
	}
	for name, body := range same {
		if got := envSum(write(body)); got != want {
			t.Errorf("%s: Summe %s statt %s", name, got, want)
		}
	}
	for name, body := range map[string]string{
		"anderer Wert":        "PDH_PORT=8080\nSECRET=abd\n",
		"zusätzlicher Wert":   "PDH_PORT=8080\nSECRET=abc\nNEU=1\n",
		"fehlender Schlüssel": "PDH_PORT=8080\n",
	} {
		if got := envSum(write(body)); got == want {
			t.Errorf("%s: Summe unverändert", name)
		}
	}
	// unlesbar: leer (kein Hinweis auf der Gegenseite)
	if got := envSum(filepath.Join(dir, "gibt-es-nicht")); got != "" {
		t.Errorf("unlesbar: %q", got)
	}
}

// I2 (R69): /etc/pdh/pdh.env zählt mit; sie wird nach .env gelesen und gewinnt
// wie beim Dienst pdh. Fehlt sie, zählt nur die .env.
func TestEnvSumWithPdhEnv(t *testing.T) {
	dir := t.TempDir()
	put := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	env := put(".env", "PDH_PORT=8080\nSECRET=abc\n")
	only := envSum(env)
	missing := filepath.Join(dir, "fehlt.env")
	if got := envSum(env, missing); got != only {
		t.Errorf("fehlende pdh.env: %s statt %s", got, only)
	}
	pe := put("pdh.env", "PDH_NEXTCLOUD_URL=https://nc\n")
	withNC := envSum(env, pe)
	if withNC == only {
		t.Error("Wert aus pdh.env ändert die Summe nicht")
	}
	// Token des Update-Agenten in pdh.env ist serverbezogen und zählt nicht
	put("pdh.env", "PDH_NEXTCLOUD_URL=https://nc\nPDH_UPDATE_AGENT_TOKEN=anders\n")
	if got := envSum(env, pe); got != withNC {
		t.Error("PDH_UPDATE_AGENT_TOKEN in pdh.env ändert die Summe")
	}
	// pdh.env überschreibt .env: gleicher wirksamer Stand, gleiche Summe
	put("pdh.env", "SECRET=abc\nPDH_NEXTCLOUD_URL=https://nc\n")
	if got := envSum(env, pe); got != withNC {
		t.Error("gleicher wirksamer Wert in beiden Dateien ändert die Summe")
	}
	put("pdh.env", "SECRET=neu\nPDH_NEXTCLOUD_URL=https://nc\n")
	if got := envSum(env, pe); got == withNC {
		t.Error("pdh.env überschreibt .env nicht")
	}
	// Standardverdrahtung liest <repo>/.env und <PDH_HA_PDH_ETC>/pdh.env
	t.Setenv("PDH_HA_PDH_ETC", dir)
	w := mirrorWiring{repo: dir}.fill()
	if got := w.fnEnv(); got != envSum(env, pe) {
		t.Errorf("Standardverdrahtung ohne pdh.env: %s", got)
	}
}

func TestNCVersion(t *testing.T) {
	dir := t.TempDir()
	php := fnRunner(func(argv []string) ([]byte, error) {
		if argv[0] != "php" {
			t.Errorf("Aufruf %v", argv)
		}
		return []byte("8.3\n"), nil
	})
	if got := ncVersion(context.Background(), dir, php); got != "" {
		t.Errorf("ohne version.php: %q", got)
	}
	os.WriteFile(filepath.Join(dir, "version.php"), []byte("<?php\n$OC_Version = array(30,0,2,2);\n$OC_VersionString = '30.0.2';\n"), 0o644)
	if got := ncVersion(context.Background(), dir, php); got != "30.0.2 / PHP 8.3" {
		t.Errorf("Fassung: %q", got)
	}
	bad := fnRunner(func([]string) ([]byte, error) { return nil, os.ErrNotExist })
	if got := ncVersion(context.Background(), dir, bad); got != "" {
		t.Errorf("PHP scheitert: %q", got)
	}
}

func TestStatusMirrorLine(t *testing.T) {
	line := func(m *apiMirror) string {
		var b bytes.Buffer
		printStatus(&b, apiStatus{Configured: true, Mode: ha.Duo, Self: apiNode{ID: "a", Kind: "node", Role: ha.Active}, Mirror: m})
		for _, l := range strings.Split(b.String(), "\n") {
			if strings.HasPrefix(l, "Spiegelung:") {
				return l
			}
		}
		return ""
	}
	if got := line(&apiMirror{On: true, Active: true, FirstDone: true, FirstPercent: 100, LastOKAgo: 12}); got != "Spiegelung: läuft, letzter Abgleich vor 12 s" {
		t.Errorf("läuft: %q", got)
	}
	if got := line(&apiMirror{On: false}); got != "Spiegelung: aus" {
		t.Errorf("aus: %q", got)
	}
	if got := line(&apiMirror{On: true, Active: true, FirstPercent: 40}); got != "Spiegelung: Erstabgleich 40 %" {
		t.Errorf("Erstabgleich: %q", got)
	}
	if got := line(nil); got != "" {
		t.Errorf("Zeuge: %q", got)
	}
	// T14 B4: Fehlt der Partner, steht das da – nicht „läuft“ und kein „Fehler:“
	var away bytes.Buffer
	printMirrorStatus(&away, &apiMirror{On: true, Active: true, FirstDone: true, LastOKAgo: 90, PartnerAway: true, Err: "uploads: rsync 12"})
	if s := away.String(); !strings.HasPrefix(s, "Spiegelung: wartet – Partner nicht erreichbar, letzter Abgleich vor 90 s\n") ||
		strings.Contains(s, "läuft") || strings.Contains(s, "  Fehler:") || !strings.Contains(s, "Letzte Meldung (Folge des fehlenden Partners): uploads: rsync 12") {
		t.Errorf("Partner fehlt: %q", s)
	}
	// Fehler und Papierkorb stehen eingerückt darunter
	var b bytes.Buffer
	printMirrorStatus(&b, &apiMirror{On: true, Active: true, FirstDone: true, LastOKAgo: 400, Err: "uploads: rsync 23", WatchErr: "chat_files: weg", TrashBytes: 2048, Orphans: 3})
	for _, sub := range []string{"letzter Abgleich vor 6 min", "Fehler: uploads: rsync 23", "Sofort-Übertragung: chat_files: weg", "3 Rückkehrer-Dateien"} {
		if !strings.Contains(b.String(), sub) {
			t.Errorf("%q fehlt in %q", sub, b.String())
		}
	}
}

func TestStatusMirrorFromDaemon(t *testing.T) {
	// Server: Feld da; Zeuge: Feld fehlt (auch im JSON)
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.tick()
	st := m.d.statusNow()
	if st.Mirror == nil || !st.Mirror.On || !st.Mirror.Active || st.Mirror.LastOKAgo != -1 {
		t.Fatalf("Server: %+v", st.Mirror)
	}
	fn := newFakeNet()
	wd := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, fn)
	wd.tick(m.ctx)
	ws := wd.statusNow()
	if ws.Mirror != nil {
		t.Fatalf("Zeuge: %+v", ws.Mirror)
	}
	b, _ := json.Marshal(ws)
	if strings.Contains(string(b), `"mirror"`) {
		t.Errorf("Zeuge meldet ein Feld mirror: %s", b)
	}
	wd.mu.Lock()
	mir := wd.mir
	wd.mu.Unlock()
	if mir != nil {
		t.Error("am Zeugen läuft eine Spiegelung")
	}
	// ausgeschaltet: Zeile „aus“
	off := newMDT(t, ha.Active, false, message{Role: "passiv"})
	off.tick()
	if mm := off.d.statusNow().Mirror; mm == nil || mm.On {
		t.Fatalf("aus: %+v", mm)
	}
}

func TestMirrorRunRefusedOnPassive(t *testing.T) {
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv"})
	m.tick()
	m.ready()
	if got := m.d.statusNow().Self.Role; got != ha.Passive {
		t.Fatalf("Rolle %s", got)
	}
	if err := m.d.mirrorRun(m.ctx); err == nil || err.Error() != "Nur auf dem aktiven Server möglich" {
		t.Fatalf("mirror run: %v", err)
	}
	if err := m.d.mirrorTrashEmptyReq(); err == nil || err.Error() != "Nur auf dem aktiven Server möglich" {
		t.Fatalf("trash-empty: %v", err)
	}
	if n := len(m.fn.messages("mirror_trash_empty")); n != 0 {
		t.Fatalf("%d Aufträge gesendet", n)
	}
	// auch über die Socket-Schnittstelle
	for _, p := range []string{"/v1/mirror/run", "/v1/mirror/trash-empty"} {
		rec := httptest.NewRecorder()
		apiHandler(m.d).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}")))
		var e apiErr
		json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != http.StatusBadRequest || e.Error != "Nur auf dem aktiven Server möglich" {
			t.Errorf("%s: %d %q", p, rec.Code, e.Error)
		}
	}
	// am Zeugen: keine Spiegelung
	fn := newFakeNet()
	wd := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, fn)
	if err := wd.mirrorRun(m.ctx); err != errMirrorWitness {
		t.Errorf("Zeuge: %v", err)
	}
}

func TestMirrorRunOnActive(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	os.MkdirAll(m.w.repo+"/uploads", 0o755)
	os.WriteFile(m.w.repo+"/uploads/a.txt", []byte("x"), 0o644)
	m.tick()
	m.ready()
	if err := m.d.mirrorRun(m.ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "rsync für uploads", func() bool {
		m.rs.mu.Lock()
		defer m.rs.mu.Unlock()
		for _, c := range m.rs.calls {
			if strings.HasSuffix(c[len(c)-1], ":uploads") {
				return true
			}
		}
		return false
	})
	// trash-empty: Auftrag geht an den Partner
	if err := m.d.mirrorTrashEmptyReq(); err != nil {
		t.Fatal(err)
	}
	if got := m.fn.messages("mirror_trash_empty"); len(got) != 1 || got[0].to != "b" {
		t.Fatalf("Aufträge: %+v", got)
	}
	// ausgeschaltet: kein Abgleich auf Zuruf
	m.setOn(false)
	if err := m.d.mirrorRun(m.ctx); err == nil || !strings.Contains(err.Error(), "ausgeschaltet") {
		t.Errorf("aus: %v", err)
	}
}

func TestDaemonStartsWorkerOnlyWhenOn(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	m.tick()
	rt := m.ready()
	time.Sleep(50 * time.Millisecond) // mehrere Durchgänge der Hintergrundschleife
	if rt.running.Load() {
		t.Fatal("Arbeiter läuft bei ausgeschalteter Spiegelung")
	}
	m.rs.mu.Lock()
	n := len(m.rs.calls)
	m.rs.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d rsync-Aufrufe ohne Spiegelung", n)
	}
	// der Empfänger lehnt ab (passiv, aber aus)
	pm := newMDT(t, ha.Passive, false, message{Role: "aktiv"})
	pm.tick()
	prt := pm.ready()
	_, _, refuse := prt.Accept(pm.ctx, "b", "node", mirrorRequest{Label: "uploads", Term: 1, Argv: []string{"rsync", "--server", "-a", ".", "x"}})
	if refuse != "Spiegelung ist aus" {
		t.Errorf("Empfänger: %q", refuse)
	}
	if prt.running.Load() {
		t.Error("Arbeiter am passiven Server ohne Spiegelung")
	}

	// Einschalten in den Einstellungen: der Arbeiter startet ohne Neustart, samt Watch
	m.setOn(true)
	m.tick()
	waitFor(t, 3*time.Second, "Arbeiter läuft", func() bool { return rt.running.Load() })
	wk := rt.worker.Load()
	waitFor(t, 3*time.Second, "Sofort-Übertragung läuft", func() bool { return hasWatch(wk) })
	if !wk.snapshot().On {
		t.Error("Arbeiter nicht eingeschaltet")
	}
	// Ausschalten: Arbeiter und Watch enden sauber
	m.setOn(false)
	m.tick()
	waitFor(t, 3*time.Second, "Arbeiter beendet", func() bool { return !rt.running.Load() })
	waitFor(t, 3*time.Second, "Watch beendet", func() bool { return !hasWatch(wk) })
	if wk.snapshot().On {
		t.Error("Arbeiter noch eingeschaltet")
	}
	// und wieder an: startet erneut (auch der Watch)
	m.setOn(true)
	m.tick()
	waitFor(t, 3*time.Second, "Arbeiter läuft wieder", func() bool { return rt.running.Load() })
	waitFor(t, 3*time.Second, "Watch läuft wieder", func() bool { return hasWatch(wk) })
}

// R14: Was am Dateisystem hängen kann (.env, Nextcloud-Fassung, Papierkorb),
// läuft nie im Takt oder unter dem Schloss. Hängt es, laufen Takt, Herzschlag
// und Status weiter.
func TestMirrorFileWorkNeverBlocksTick(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	hang := make(chan struct{})
	entered := make(chan string, 8)
	t.Cleanup(func() { close(hang) })
	m.w.fnEnv = func() string { entered <- "env"; <-hang; return "x" }
	m.w.fnNC = func(context.Context, ha.Settings) string { entered <- "nc"; <-hang; return "x" }
	prevTune := m.w.tune
	m.w.tune = func(wd *mirrorDeps, id *mirrorInboxDeps) {
		prevTune(wd, id)
		if id != nil {
			id.sources = func(s ha.Settings) []ha.MirrorSource { entered <- "trash"; <-hang; return nil }
		}
		if wd != nil {
			wd.exists = func(string) bool { <-hang; return false } // auch ein hängender Quellordner
		}
	}
	m.d.lock()
	m.d.cl.Settings.MirrorNextcloud = true
	m.d.cl.Settings.NCDataDir, m.d.cl.Settings.NCAppDir = "/srv/nc/data", "/srv/nc/app"
	m.d.unlock()
	m.tick()
	m.ready()
	select {
	case <-entered: // die Dateiarbeit hängt jetzt in ihrer eigenen Goroutine
	case <-time.After(3 * time.Second):
		t.Fatal("Dateiarbeit nicht gestartet")
	}
	deadline := time.After(2 * time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 5 {
			m.tick()
			m.d.mu.Lock()
			hb := m.d.heartbeatLocked(false, m.d.views, m.d.wall())
			m.d.mu.Unlock()
			if hb.T != "hb" || hb.EnvSum != "" {
				t.Errorf("Herzschlag: %+v", hb)
			}
			if st := m.d.statusNow(); st.Mirror == nil {
				t.Error("kein Status")
			}
			if err := m.d.mirrorTrashEmptyReq(); err != nil {
				t.Error(err)
			}
		}
	}()
	select {
	case <-done:
	case <-deadline:
		t.Fatal("Takt, Herzschlag oder Status hängen an der Dateiarbeit")
	}
}

func TestMirrorHeartbeatAndStatusFields(t *testing.T) {
	// ausgeschaltet: der Herzschlag trägt nichts ein, und ohne Felder steht nichts im JSON
	off := newMDT(t, ha.Active, false, message{Role: "passiv"})
	off.tick()
	off.d.mu.Lock()
	hb := off.d.heartbeatLocked(false, off.d.views, off.d.wall())
	off.d.mu.Unlock()
	b, _ := json.Marshal(hb)
	for _, k := range []string{"mirror_first", "mirror_at", "mirror_orphans", "mirror_trash_b", "env_sum", "nc_ver"} {
		if strings.Contains(string(b), k) {
			t.Errorf("Feld %s im ausgeschalteten Herzschlag: %s", k, b)
		}
	}

	m := newMDT(t, ha.Active, true, message{Role: "passiv", EnvSum: "bbbb", NCVer: "30 / PHP 8.3"})
	m.w.fnEnv = func() string { return "aaaa" }
	m.w.fnNC = func(context.Context, ha.Settings) string { return "30 / PHP 8.2" }
	m.d.lock()
	m.d.cl.Settings.MirrorNextcloud = true
	m.d.cl.Settings.NCDataDir, m.d.cl.Settings.NCAppDir = "/srv/nc/data", "/srv/nc/app"
	m.d.unlock()
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Zwischenspeicher", func() bool { return rt.cache.Load() != nil })
	m.d.mu.Lock()
	hb = m.d.heartbeatLocked(false, m.d.views, m.d.wall())
	m.d.mu.Unlock()
	if hb.EnvSum != "aaaa" || hb.NCVer != "30 / PHP 8.2" {
		t.Fatalf("Herzschlag: %+v", hb)
	}
	st := m.d.statusNow()
	if st.Mirror.EnvSame || st.Mirror.NCVerSame {
		t.Errorf("Unterschied nicht erkannt: %+v", st.Mirror)
	}
	warn := strings.Join(st.Warnings, "|")
	if !strings.Contains(warn, ".env") || !strings.Contains(warn, "Nextcloud") {
		t.Errorf("Hinweise: %v", st.Warnings)
	}
	// Partner meldet nichts (leer): gleich, kein Hinweis
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	st = m.d.statusNow()
	if !st.Mirror.EnvSame || !st.Mirror.NCVerSame {
		t.Errorf("leer ist gleich: %+v", st.Mirror)
	}
	for _, w := range st.Warnings {
		if strings.Contains(w, ".env") || strings.Contains(w, "Nextcloud") {
			t.Errorf("Hinweis trotz leerer Summe: %s", w)
		}
	}
	// ältere Gegenstelle ohne die Felder: Status und JSON sind lesbar
	var old message
	if err := json.Unmarshal([]byte(`{"t":"hb","id":"b","role":"passiv"}`), &old); err != nil || old.EnvSum != "" || !old.MirrorAt.IsZero() {
		t.Errorf("alter Herzschlag: %+v %v", old, err)
	}
}

func TestMirrorWatchErrorVisible(t *testing.T) {
	f := newMirrorFix(t)
	f.w.d.lexists = func(string) bool { return true }
	f.w.set(true, ha.Active, 1, mirrorSettings)
	mw := newMirrorWatch(nil, time.Hour, func(string, []string) {}, func() {})
	fail := true
	f.rs.fn = func(context.Context, []string) error {
		if fail {
			return errTest("Verbindung weg")
		}
		return nil
	}
	f.w.onWatch(context.Background(), mw, "uploads", []string{"a.txt"})
	if got := f.w.snapshot().WatchErr; !strings.Contains(got, "Verbindung weg") {
		t.Fatalf("WatchErr: %q", got)
	}
	// Besetzt ist kein Fehler und löscht ihn nicht
	f.w.sem <- struct{}{}
	f.w.onWatch(context.Background(), mw, "uploads", []string{"a.txt"})
	<-f.w.sem
	if f.w.snapshot().WatchErr == "" {
		t.Error("Besetzt hat den Fehler gelöscht")
	}
	fail = false
	f.w.onWatch(context.Background(), mw, "uploads", []string{"a.txt"})
	if got := f.w.snapshot().WatchErr; got != "" {
		t.Errorf("nach Erfolg: %q", got)
	}

	// im Status
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Arbeiter", func() bool { return rt.worker.Load() != nil })
	wk := rt.worker.Load()
	wk.mu.Lock()
	wk.st.WatchErr = "uploads: Verbindung weg"
	wk.mu.Unlock()
	if got := m.d.statusNow().Mirror.WatchErr; got != "uploads: Verbindung weg" {
		t.Errorf("Status: %q", got)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

// R9: Wechsel weg von passiv bricht die laufenden Ströme ab.
func TestMirrorStopStreamsOnRoleChange(t *testing.T) {
	w, _, _, _ := newMirrorWiring(t)
	var stops atomic.Int32
	rt := newMirrorRT(*w, testEvents(t), func() { stops.Add(1) })
	pub := func(before, after ha.Role) {
		rt.publish(before, &mirrorWant{On: true, Role: after, Term: 1, Partner: "b", Settings: ha.DefaultSettings()})
	}
	pub(ha.Passive, ha.Passive)
	pub(ha.Passive, ha.Passive)
	if stops.Load() != 0 {
		t.Fatalf("passiv bleibt passiv: %d", stops.Load())
	}
	pub(ha.Passive, ha.Active) // wird aktiv
	if stops.Load() != 1 {
		t.Fatalf("passiv -> aktiv: %d", stops.Load())
	}
	pub(ha.Active, ha.Active)
	pub(ha.Active, ha.Passive) // zurück: es gibt keinen Strom abzubrechen
	if stops.Load() != 1 {
		t.Fatalf("aktiv -> passiv: %d", stops.Load())
	}
	pub(ha.Passive, ha.Fenced) // abgeschaltet zählt auch
	if stops.Load() != 2 {
		t.Fatalf("passiv -> abgeschaltet: %d", stops.Load())
	}
	// Neustart: der Stand vor dem Schritt war passiv, jetzt aktiv
	rt2 := newMirrorRT(*w, testEvents(t), func() { stops.Add(1) })
	rt2.publish(ha.Passive, &mirrorWant{On: true, Role: ha.Active, Partner: "b", Settings: ha.DefaultSettings()})
	if stops.Load() != 3 {
		t.Fatalf("erster Takt: %d", stops.Load())
	}
	// die neue Rolle ist schon sichtbar, wenn die Ströme gestoppt werden
	var seen ha.Role
	rt3 := newMirrorRT(*w, testEvents(t), nil)
	rt3.stopMir = func() { seen = rt3.view().Role }
	rt3.publish(ha.Passive, &mirrorWant{On: true, Role: ha.Active, Partner: "b", Settings: ha.DefaultSettings()})
	if seen != ha.Active {
		t.Errorf("Ansicht beim Stoppen: %q", seen)
	}

	// im Daemon: der Transport wird erreicht
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv"})
	m.d.mirrorStopStreams()
	if m.fn.stops.Load() != 1 {
		t.Errorf("Daemon erreicht stopMirrors nicht: %d", m.fn.stops.Load())
	}
}

// Ein Absturz im aktiven Zustand: nach dem Neustart als passiver Server zählt
// das als Rückkehr (ReturnedAt), auch ohne dass der Prozess den Wechsel sah.
func TestMirrorReturnedAtSurvivesCrash(t *testing.T) {
	w, _, clk, _ := newMirrorWiring(t)
	s := ha.DefaultSettings()
	s.MirrorOn = true
	ctx1, stop1 := context.WithCancel(context.Background())
	rt1 := newMirrorRT(*w, testEvents(t), nil)
	rt1.publish(ha.Active, &mirrorWant{On: true, Role: ha.Active, Term: 1, Partner: "b", Settings: s})
	rt1.start(ctx1)
	waitFor(t, 3*time.Second, "Rolle gemerkt", func() bool {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(w.statePath), mirrorInboxFile))
		return err == nil && strings.Contains(string(b), `"last_role":"aktiv"`)
	})
	if !rt1.view().ReturnedAt.IsZero() {
		t.Fatal("ReturnedAt ohne Wechsel")
	}
	// harter Absturz: der Prozess endet, nichts wird geordnet beendet (hier: nur die Goroutinen)
	stop1()
	rt1.wg.Wait()

	clk.add(time.Minute)
	ctx2, stop2 := context.WithCancel(context.Background())
	t.Cleanup(func() { stop2() })
	rt2 := newMirrorRT(*w, testEvents(t), nil)
	// neuer Prozess: der Stand vor dem Schritt ist schon „passiv“ (Neustart-Regel), kein Wechsel zu sehen
	rt2.publish(ha.Passive, &mirrorWant{On: true, Role: ha.Passive, Term: 2, Partner: "b", Settings: s})
	rt2.start(ctx2)
	waitFor(t, 3*time.Second, "Rückkehr erkannt", func() bool { return !rt2.view().ReturnedAt.IsZero() })
	if got := rt2.view().ReturnedAt; got.Before(clk.now().Add(-time.Second)) {
		t.Errorf("ReturnedAt %s", got)
	}
	// einmalig: nach dem Merken der neuen Rolle zählt ein dritter Start nicht erneut
	waitFor(t, 3*time.Second, "passiv gemerkt", func() bool {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(w.statePath), mirrorInboxFile))
		return strings.Contains(string(b), `"last_role":"passiv"`)
	})
	stop2()
	rt2.wg.Wait()
	ctx3, stop3 := context.WithCancel(context.Background())
	t.Cleanup(func() { stop3() })
	rt3 := newMirrorRT(*w, testEvents(t), nil)
	rt3.publish(ha.Passive, &mirrorWant{On: true, Role: ha.Passive, Term: 2, Partner: "b", Settings: s})
	rt3.start(ctx3)
	waitFor(t, 3*time.Second, "Empfänger gebaut", func() bool { return rt3.inbox.Load() != nil })
	if got := rt3.view().ReturnedAt; !got.IsZero() {
		t.Errorf("zweiter Neustart zählt erneut: %s", got)
	}
	stop3()
	rt3.wg.Wait()
}

// Im Daemon: gespeichert „aktiv“, Neustart über „abgeschaltet“ zu passiv:
// sieht schon der erste Takt die Rückkehr.
func TestMirrorReturnedAtViaFenced(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"}) // Partner sieht uns nicht: Neustart-Regel sperrt zuerst
	m.fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test"}))
	m.tick()
	rt := m.ready()
	if got := m.d.statusNow().Self.Role; got != ha.Fenced {
		t.Fatalf("erste Rolle %s", got)
	}
	if !rt.view().ReturnedAt.IsZero() {
		t.Fatal("Rückkehr ohne Wechsel")
	}
	// der Partner hat übernommen: dieser Server wird passiv, der Weg über „abgeschaltet“ zählt
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 5, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	m.tick()
	if got := m.d.statusNow().Self.Role; got != ha.Passive {
		t.Fatalf("zweite Rolle %s", got)
	}
	if rt.view().ReturnedAt.IsZero() {
		t.Error("Rückkehr nicht erkannt")
	}
}

func TestMirrorPruneTrashDaily(t *testing.T) {
	w, _, clk, _ := newMirrorWiring(t)
	s := ha.DefaultSettings() // Spiegelung aus: aufgeräumt wird trotzdem
	rt := newMirrorRT(*w, testEvents(t), nil)
	rt.want.Store(&mirrorWant{Role: ha.Passive, Term: 1, Partner: "b", Settings: s})
	rt.build()
	trash := w.repo + "/.pdh-papierkorb"
	mk := func(day string) string {
		p := trash + "/" + day + "/uploads"
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(p+"/f", []byte("x"), 0o644)
		return trash + "/" + day
	}
	old, fresh := mk("2026-10-01"), mk("2026-11-01")
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	rt.scan(context.Background())
	if exists(old) || !exists(fresh) {
		t.Fatalf("erster Lauf: alt %v neu %v", exists(old), exists(fresh))
	}
	// am selben Tag nicht noch einmal
	old = mk("2026-10-01")
	rt.scan(context.Background())
	if !exists(old) {
		t.Fatal("zweimal am Tag aufgeräumt")
	}
	// nächster Tag (mit der eingespritzten Uhr): wieder
	clk.add(24 * time.Hour)
	rt.scan(context.Background())
	if exists(old) {
		t.Fatal("am nächsten Tag nicht aufgeräumt")
	}
	if !exists(fresh) {
		t.Fatal("frischer Tag gelöscht")
	}
}

func TestMirrorTrashEmptyFromPeer(t *testing.T) {
	m := newMDT(t, ha.Passive, true, message{Role: "aktiv"})
	m.tick()
	rt := m.ready()
	day := m.w.repo + "/.pdh-papierkorb/2026-11-02/uploads"
	os.MkdirAll(day, 0o755)
	os.WriteFile(day+"/f", []byte("x"), 0o644)
	exists := func() bool { _, err := os.Stat(day); return err == nil }
	// ein Unbekannter (nicht im Cluster) darf es nicht
	m.d.handle(m.ctx, inbound{From: "zz", Msg: message{T: "mirror_trash_empty", ID: "zz", Seq: 1}})
	time.Sleep(100 * time.Millisecond)
	if !exists() {
		t.Fatal("Unbekannter leert den Papierkorb")
	}
	// der Partner darf es: ohne das Schloss des Daemons (der Aufruf kehrt sofort zurück)
	m.d.handle(m.ctx, inbound{From: "b", Msg: message{T: "mirror_trash_empty", ID: "b", Seq: 2, Term: 1}})
	waitFor(t, 3*time.Second, "Papierkorb geleert", func() bool { return !exists() })
	waitFor(t, 3*time.Second, "Ereignis", func() bool { return hasEvent(m.d, "Papierkorb der Spiegelung geleert") })
	_ = rt
	// am Zeugen: nie
	fn := newFakeNet()
	wd := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, fn)
	wd.handle(m.ctx, inbound{From: "a", Msg: message{T: "mirror_trash_empty", ID: "a", Seq: 3}})
	time.Sleep(50 * time.Millisecond)
	if hasEvent(wd, "Papierkorb") {
		t.Error("Zeuge leert einen Papierkorb")
	}
}

func TestMirrorTrashEmptyIsDeliveredType(t *testing.T) {
	if !deliverTypes["mirror_trash_empty"] {
		t.Fatal("mirror_trash_empty wird nicht zugestellt")
	}
}
