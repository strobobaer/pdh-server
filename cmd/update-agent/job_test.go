package main

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

const (
	jobOldSHA = "1111111111111111111111111111111111111111"
	jobNewSHA = "2222222222222222222222222222222222222222"
)

// jobRec zeichnet die Befehlsfolge auf (Skript-Schalter und Host-Befehle).
type jobRec struct {
	mu   sync.Mutex
	list []string
	env  map[string]map[string]string // letzte Umgebung je Befehl
	fail map[string]bool              // Befehle, die scheitern
	on   map[string]func()            // Haken je Befehl (z. B. Abbild des Auftrags)
	head string                       // Stand des Checkouts (rev-parse HEAD)
	// noMove: --commit ändert den Checkout nicht (Abruf scheitert vorher)
	noMove bool
}

func (r *jobRec) headSHA() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.head == "" {
		return jobOldSHA
	}
	return r.head
}

// moveHead: Checkout nach einem Lauf von install_update.sh (vor Erfolg/Fehler).
func (r *jobRec) moveHead(args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i+1 < len(args); i++ {
		switch {
		case args[i] == "--commit" && !r.noMove:
			r.head = args[i+1]
		case args[i] == "--rollback":
			r.head = args[i+1]
		}
	}
}

func (r *jobRec) add(label string, env map[string]string) bool {
	r.mu.Lock()
	r.list = append(r.list, label)
	if r.env == nil {
		r.env = map[string]map[string]string{}
	}
	r.env[label] = env
	fail, hook := r.fail[label], r.on[label]
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return !fail
}

func (r *jobRec) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.list, " | ")
}

// jobHostLabel: Kurzname eines Host-Skripts.
func jobHostLabel(script string) string {
	switch script {
	case jobLockScript:
		return "psql-lock"
	case jobUnlockScript:
		return "psql-unlock"
	case jobDumpScript:
		return "pg_dump"
	case jobRbCreateScript:
		return "createdb"
	case jobRbRestoreScript:
		return "pg_restore"
	case jobRbTerminateScript:
		return "terminate"
	case jobRbRenameScript:
		return "rename"
	case jobStopScript:
		return "stop-pdh"
	case jobStartScript:
		return "start-pdh"
	case jobIsActiveScript:
		return "pdh-active"
	}
	return "unbekannt"
}

// jobClock: Uhr zum Weiterdrehen im Test.
type jobClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *jobClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *jobClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// jobHealth: Attrappe für /health des PDH.
type jobHealth struct {
	mu      sync.Mutex
	version string
}

func (h *jobHealth) set(v string) { h.mu.Lock(); h.version = v; h.mu.Unlock() }

// jobFakeGit: ls-remote, rev-parse, show <sha>:VERSION.
func jobFakeGit(remoteErr bool, remote string) func(context.Context, ...string) (string, error) {
	return func(_ context.Context, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "ls-remote origin refs/heads/main":
			if remoteErr {
				return "", io.ErrUnexpectedEOF
			}
			return remote + "\trefs/heads/main\n", nil
		case "rev-parse HEAD":
			return jobOldSHA + "\n", nil
		case "show " + jobNewSHA + ":VERSION":
			return "0.77.9\n", nil
		case "show " + jobOldSHA + ":VERSION":
			return "0.77.8\n", nil
		}
		return "", io.EOF
	}
}

// jobTestAgent: Einzelserver (systemd) mit Attrappen für Skript, psql,
// pg_dump/pg_restore, git und /health. Schritte laufen von Hand.
func jobTestAgent(t *testing.T) (*updateAgent, *jobRec, *jobClock, *jobHealth) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("PDH_DR_STATE_DIR", state)
	t.Setenv("PDH_HA_ETC", t.TempDir()) // ohne node.json: Einzelserver
	t.Setenv("PDH_UPDATE_BRANCH", "")
	health := &jobHealth{version: "0.77.8"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		health.mu.Lock()
		v := health.version
		health.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": v})
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	repo := t.TempDir()
	env := "PDH_SERVER_PORT=" + u.Port() + "\nPDH_DATABASE_HOST=localhost\nPDH_DATABASE_PORT=5432\n" +
		"PDH_DATABASE_USER=pdh\nPDH_DATABASE_PASSWORD='geheim'\nPDH_DATABASE_NAME=pdh\nPDH_DATABASE_SSLMODE=disable\n"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	// „pdh-active“ scheitert = systemctl is-active meldet „gestoppt“
	rec := &jobRec{fail: map[string]bool{"pdh-active": true}, on: map[string]func(){}}
	clk := &jobClock{t: time.Date(2026, 10, 30, 3, 0, 0, 0, time.Local)}
	a := &updateAgent{mode: "systemd", repoDir: repo}
	a.dr.Config = drDefaults()
	a.windowDefaults()
	a.wireJobFakes(rec, clk)
	return a, rec, clk, health
}

// wireJobFakes setzt die Attrappen (auch für einen „neu gestarteten“ Agenten).
func (a *updateAgent) wireJobFakes(rec *jobRec, clk *jobClock) {
	git := jobFakeGit(false, jobNewSHA)
	a.gitRun = func(ctx context.Context, args ...string) (string, error) {
		if strings.Join(args, " ") == "rev-parse HEAD" {
			return rec.headSHA() + "\n", nil
		}
		return git(ctx, args...)
	}
	a.clock = clk.now
	a.sleep = func(time.Duration) {}
	a.jobManual = true
	a.freeSpace = func(string) (int64, bool) { return 10 << 30, true }
	a.haRun = func(_ context.Context, script string, env map[string]string, _ io.Writer) error {
		if !rec.add(jobHostLabel(script), env) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	a.updateRun = func(args []string) {
		rec.moveHead(args)
		ok := rec.add("script "+strings.Join(args, " "), nil)
		a.mu.Lock()
		a.status.Running, a.status.Success = false, ok
		a.mu.Unlock()
	}
}

// runJob führt Schritte aus, bis der Auftrag endet (Uhr je Schritt +1 min).
func runJob(t *testing.T, a *updateAgent, clk *jobClock, max int) {
	t.Helper()
	for i := 0; i < max; i++ {
		if a.jobStep(context.Background()) {
			return
		}
		clk.add(time.Minute)
	}
	t.Fatalf("Auftrag nach %d Schritten nicht beendet: %+v", max, a.jobView())
}

func TestSingleJobRollbackOnHealthFail(t *testing.T) {
	a, rec, clk, health := jobTestAgent(t)
	health.set("0.77.8") // die neue Version meldet sich nie
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	if v := a.jobView(); !v.Active || v.Phase != string(ha.UpPrepare) || v.Kind != string(ha.UpdateSequential) {
		t.Fatalf("Start: %+v", v)
	}
	runJob(t, a, clk, 40)
	want := "script --commit " + jobNewSHA + " --no-restart | psql-lock | stop-pdh | pdh-active | pg_dump | script --finish | " +
		"stop-pdh | pdh-active | createdb | pg_restore | terminate | rename | script --rollback " + jobOldSHA + " | psql-unlock"
	if got := rec.all(); got != want {
		t.Fatalf("Befehlsfolge\n got: %s\nwant: %s", got, want)
	}
	dump := filepath.Join(drStateDir(), "vor-update-"+jobNewSHA+".dump")
	if rec.env["pg_dump"]["PDH_UPD_DUMP"] != dump || rec.env["pg_restore"]["PDH_UPD_DUMP"] != dump {
		t.Fatalf("Sicherung: %v / %v", rec.env["pg_dump"], rec.env["pg_restore"])
	}
	notBefore := strconv.FormatInt(time.Date(2026, 10, 30, 3, 2, 0, 0, time.Local).Unix(), 10) // Sperre nach 2 min Countdown
	if e := rec.env["psql-lock"]; e["PGPASSWORD"] != "geheim" || e["PGDATABASE"] != "pdh" || e["PDH_UPD_COMMIT"] != jobNewSHA || e["PDH_UPD_NOT_BEFORE"] != notBefore {
		t.Fatalf("psql-Umgebung: %v (not_before %s)", e, notBefore)
	}
	// Rückweg: frische Datenbank als postgres, dann Tausch der Namen.
	for _, l := range []string{"createdb", "pg_restore", "terminate", "rename"} {
		e := rec.env[l]
		if e["PDH_UPD_DB"] != "pdh" || e["PDH_UPD_RB_DB"] != "pdh_rueckweg" || !strings.HasPrefix(e["PDH_UPD_FAILED_DB"], "pdh_fehlgeschlagen_2026103003") || e["PGPASSWORD"] != "" {
			t.Fatalf("Umgebung %s: %v", l, e)
		}
	}
	for _, part := range []string{"BEGIN;", `ALTER DATABASE :"db" RENAME TO :"failed";`, `ALTER DATABASE :"rb" RENAME TO :"db";`, "COMMIT;"} {
		if !strings.Contains(jobRbRenameScript, part) {
			t.Fatalf("Tausch ohne %q", part)
		}
	}
	if strings.Contains(jobRbRestoreScript, "--clean") || !strings.Contains(jobRbRestoreScript, "runuser -u postgres") {
		t.Fatal("Rückweg muss in eine frische Datenbank als postgres einspielen")
	}
	v := a.jobView()
	if v.Active || v.Phase != string(ha.UpFailed) || v.Locked || !strings.Contains(v.Result, "antwortet nicht") {
		t.Fatalf("Ende: %+v", v)
	}
	ws := a.windowSnapshot()
	if len(ws.Faulty) != 1 || ws.Faulty[0] != jobNewSHA || !strings.Contains(ws.LastResult, "zurückgesetzt") {
		t.Fatalf("Fenster-Zustand: %+v", ws)
	}
	// Ein fehlerhaft gemerkter Stand wird vom Zeitplan nicht erneut genommen.
	if _, err := a.startSingleJob(context.Background(), "zeitplan", false); err == nil || !strings.Contains(err.Error(), "fehlerhaft") {
		t.Fatalf("fehlerhafter Stand erneut gestartet: %v", err)
	}
}

func TestSingleJobPersistsAcrossRestart(t *testing.T) {
	a, rec, clk, health := jobTestAgent(t)
	health.set("0.77.9")
	// Der Agent wird am Ende von --finish neu gestartet: Abbild des Auftrags
	// in diesem Augenblick (der Schritt danach kommt nicht mehr zum Zug).
	var snap []byte
	rec.on["script --finish"] = func() { snap, _ = os.ReadFile(singleJobFile()) }
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10 && snap == nil; i++ {
		a.jobStep(context.Background())
		clk.add(time.Minute)
	}
	if snap == nil {
		t.Fatalf("--finish nicht erreicht: %s", rec.all())
	}
	if err := os.WriteFile(singleJobFile(), snap, 0o600); err != nil {
		t.Fatal(err)
	}

	b := &updateAgent{mode: "systemd", repoDir: a.repoDir}
	b.dr.Config = drDefaults()
	b.windowDefaults()
	b.wireJobFakes(rec, clk)
	b.loadSingleJob()
	if v := b.jobView(); !v.Active || !v.Locked || v.Phase != string(ha.UpLocked) {
		t.Fatalf("nach Neustart: %+v", v)
	}
	before := rec.all()
	runJob(t, b, clk, 10)
	if got := strings.TrimPrefix(rec.all(), before); got != " | psql-unlock" {
		t.Fatalf("nach Neustart doppelt ausgeführt: %q", got)
	}
	v := b.jobView()
	if v.Phase != string(ha.UpDone) || v.Locked || v.ToVersion != "0.77.9" || v.FromVersion != "0.77.8" {
		t.Fatalf("Ende: %+v", v)
	}
	if ws := b.windowSnapshot(); !strings.Contains(ws.LastResult, "abgeschlossen") || len(ws.Faulty) != 0 {
		t.Fatalf("Fenster-Zustand: %+v", ws)
	}
}

// Fehler beim Bauen: Checkout zurück (ohne Neustart), Stand fehlerhaft, keine Sperre.
func TestSingleJobPrepareFailure(t *testing.T) {
	a, rec, clk, _ := jobTestAgent(t)
	rec.fail["script --commit "+jobNewSHA+" --no-restart"] = true
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	runJob(t, a, clk, 5)
	want := "script --commit " + jobNewSHA + " --no-restart | script --rollback " + jobOldSHA + " --no-restart"
	if got := rec.all(); got != want {
		t.Fatalf("Befehlsfolge\n got: %s\nwant: %s", got, want)
	}
	if v := a.jobView(); v.Phase != string(ha.UpFailed) || !v.CountdownUntil.IsZero() {
		t.Fatalf("Ende: %+v", v)
	}
	if ws := a.windowSnapshot(); len(ws.Faulty) != 1 {
		t.Fatalf("nicht als fehlerhaft gemerkt: %+v", ws)
	}
}

// Abbruch durch den Administrator in der Sperre: zurücksetzen, nicht fehlerhaft.
func TestSingleJobAbortLocked(t *testing.T) {
	a, rec, clk, health := jobTestAgent(t)
	health.set("0.77.8")
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10 && !strings.Contains(rec.all(), "--finish"); i++ {
		a.jobStep(context.Background())
		clk.add(time.Minute)
	}
	if err := a.abortSingleJob("Anna Admin"); err != nil {
		t.Fatal(err)
	}
	runJob(t, a, clk, 10)
	v := a.jobView()
	if v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "Anna Admin") {
		t.Fatalf("Ende: %+v", v)
	}
	if !strings.Contains(rec.all(), "pg_restore") {
		t.Fatalf("nicht zurückgesetzt: %s", rec.all())
	}
	if ws := a.windowSnapshot(); len(ws.Faulty) != 0 {
		t.Fatalf("Abbruch darf nicht als fehlerhaft gelten: %+v", ws)
	}
}

func TestSingleJobHandlers(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	rec := httptest.NewRecorder()
	a.jobHandler(rec, httptest.NewRequest(http.MethodGet, "/v1/update/job", nil))
	if !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"active":false`) {
		t.Fatalf("GET ohne Auftrag: %s", rec.Body.String())
	}
	a.freeSpace = func(string) (int64, bool) { return 1 << 30, true }
	rec = httptest.NewRecorder()
	a.jobStartHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/update/job/start", strings.NewReader(`{"force":false}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "2 GB") || !strings.Contains(rec.Body.String(), `"hints"`) {
		t.Fatalf("nicht bereit: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.jobStartHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/update/job/start", strings.NewReader(`{"force":true}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("erzwungen: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.jobHandler(rec, httptest.NewRequest(http.MethodGet, "/v1/update/job", nil))
	if !strings.Contains(rec.Body.String(), `"active":true`) || !strings.Contains(rec.Body.String(), "Neustart") {
		t.Fatalf("GET mit Auftrag: %s", rec.Body.String())
	}
	// Während des Auftrags kein zweiter Lauf von install_update.sh über /v1/update.
	rec = httptest.NewRecorder()
	a.updateHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/update", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("/v1/update während des Auftrags: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.jobAbortHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/update/job/abort", strings.NewReader(`{"by":"Anna"}`)))
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("Abbruch: %s", rec.Body.String())
	}

	d := &updateAgent{mode: "docker"}
	for _, h := range []http.HandlerFunc{d.jobHandler, d.jobStartHandler, d.jobAbortHandler, d.windowGetHandler, d.windowPostHandler, d.windowFaultyDeleteHandler} {
		rec = httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
		if !strings.Contains(rec.Body.String(), "im Docker-Betrieb nicht verfügbar") || !strings.Contains(rec.Body.String(), `"ok":false`) {
			t.Fatalf("Docker: %s", rec.Body.String())
		}
	}
}

// rollbackAfterFinish: Auftrag bis nach --finish, dann prüfen lassen, bis er
// zurückgesetzt und beendet ist (die neue Version meldet sich nie).
func rollbackAfterFinish(t *testing.T, a *updateAgent, clk *jobClock, health *jobHealth) {
	t.Helper()
	health.set("0.77.8")
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	runJob(t, a, clk, 40)
}

// Einspielen in die frische Datenbank scheitert: kein Tausch, alte Datenbank unberührt.
func TestSingleJobRestoreFailureKeepsDatabase(t *testing.T) {
	a, rec, clk, health := jobTestAgent(t)
	rec.fail["pg_restore"] = true
	rollbackAfterFinish(t, a, clk, health)
	got := rec.all()
	if !strings.Contains(got, "createdb | pg_restore | script --rollback "+jobOldSHA) || strings.Contains(got, "terminate") || strings.Contains(got, "rename") {
		t.Fatalf("Befehlsfolge: %s", got)
	}
	if v := a.jobView(); v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "Datenbank nicht zurückgespielt") || !strings.Contains(v.Result, "unverändert") {
		t.Fatalf("Ende: %+v", v)
	}
}

// pdh lässt sich nicht stoppen: kein Zurückspielen, trotzdem alter Stand.
func TestSingleJobRollbackPdhStillRunning(t *testing.T) {
	a, rec, clk, health := jobTestAgent(t)
	// pdh läuft nach dem Einsetzen und lässt sich dann nicht mehr stoppen
	rec.on["script --finish"] = func() { rec.mu.Lock(); rec.fail["pdh-active"] = false; rec.mu.Unlock() }
	rollbackAfterFinish(t, a, clk, health)
	got := rec.all()
	if !strings.Contains(got, "stop-pdh | pdh-active | pdh-active | script --rollback "+jobOldSHA) || strings.Contains(got, "createdb") {
		t.Fatalf("Befehlsfolge: %s", got)
	}
	if v := a.jobView(); !strings.Contains(v.Result, "pdh ließ sich nicht stoppen – Datenbank nicht zurückgespielt") {
		t.Fatalf("Ende: %+v", v)
	}
}

// Abruf scheitert (Checkout unverändert) oder ein anderer Lauf: nicht fehlerhaft.
func TestSingleJobFetchFailureNotFaulty(t *testing.T) {
	a, rec, clk, _ := jobTestAgent(t)
	rec.noMove = true
	rec.fail["script --commit "+jobNewSHA+" --no-restart"] = true
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	runJob(t, a, clk, 5)
	if got := rec.all(); got != "script --commit "+jobNewSHA+" --no-restart" {
		t.Fatalf("Befehlsfolge: %s", got)
	}
	if v := a.jobView(); v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "nicht abgerufen") {
		t.Fatalf("Ende: %+v", v)
	}
	if ws := a.windowSnapshot(); len(ws.Faulty) != 0 {
		t.Fatalf("Abruffehler als fehlerhaft gemerkt: %+v", ws)
	}

	b, rec, clk, _ := jobTestAgent(t)
	if _, err := b.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.status.Running = true // ein anderer Lauf von install_update.sh
	b.mu.Unlock()
	runJob(t, b, clk, 5)
	if rec.all() != "" || len(b.windowSnapshot().Faulty) != 0 || !strings.Contains(b.jobView().Result, "anderes Update") {
		t.Fatalf("anderer Lauf: %s %+v", rec.all(), b.jobView())
	}
}

// Sicherung vor dem Update scheitert (z. B. Prüfung mit pg_restore --list):
// Abbruch vor jedem Neustart, Sperre weg, nicht fehlerhaft.
func TestSingleJobDumpFailureAbortsBeforeRestart(t *testing.T) {
	a, rec, clk, _ := jobTestAgent(t)
	rec.fail["pg_dump"] = true
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	runJob(t, a, clk, 10)
	want := "script --commit " + jobNewSHA + " --no-restart | psql-lock | stop-pdh | pdh-active | pg_dump | script --rollback " + jobOldSHA + " --no-restart | psql-unlock | start-pdh"
	if got := rec.all(); got != want {
		t.Fatalf("Befehlsfolge\n got: %s\nwant: %s", got, want)
	}
	if v := a.jobView(); v.Locked || v.Phase != string(ha.UpFailed) || len(a.windowSnapshot().Faulty) != 0 {
		t.Fatalf("Ende: %+v", v)
	}
	if !strings.Contains(jobDumpScript, `pg_restore --list "$PDH_UPD_DUMP.part"`) {
		t.Fatal("Sicherung wird nicht geprüft")
	}
}

// Zu wenig Platz für die Sicherung vor dem Update: nicht grün.
func TestSingleJobDumpSpace(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	a.freeSpace = func(p string) (int64, bool) {
		if p == drStateDir() {
			return 1 << 30, true
		}
		return 10 << 30, true
	}
	_, err := a.startSingleJob(context.Background(), "knopf", false)
	var nr jobNotReady
	if !errors.As(err, &nr) || !strings.Contains(nr.reason, "Sicherung vor dem Update") {
		t.Fatalf("Platz für die Sicherung: %v", err)
	}
}

// I4: pdh wird vor der Sicherung gestoppt; lässt es sich nicht stoppen,
// gibt es keine Sicherung und kein Einsetzen – alter Stand läuft weiter.
func TestSingleJobPDHNotStoppedBeforeDump(t *testing.T) {
	a, rec, clk, _ := jobTestAgent(t)
	rec.fail["pdh-active"] = false // systemctl is-active: läuft weiter
	if _, err := a.startSingleJob(context.Background(), "knopf", false); err != nil {
		t.Fatal(err)
	}
	runJob(t, a, clk, 10)
	want := "script --commit " + jobNewSHA + " --no-restart | psql-lock | stop-pdh | pdh-active | pdh-active | " +
		"script --rollback " + jobOldSHA + " --no-restart | psql-unlock | start-pdh"
	if got := rec.all(); got != want {
		t.Fatalf("Befehlsfolge\n got: %s\nwant: %s", got, want)
	}
	v := a.jobView()
	if v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "nicht gestoppt") {
		t.Fatalf("Ende: %+v", v)
	}
	if ws := a.windowSnapshot(); len(ws.Faulty) != 0 {
		t.Fatalf("als fehlerhaft gemerkt: %+v", ws)
	}
}
