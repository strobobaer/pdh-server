package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// haFakeVerbund: pdh-ha auf einem Unix-Socket mit Status und Update-Start;
// zeichnet die Rümpfe von POST /v1/update/start auf.
type haFakeVerbund struct {
	mu     sync.Mutex
	starts []map[string]any
	code   int    // Antwort auf den Start (Standard 200)
	answer string // Rumpf dazu
}

func (f *haFakeVerbund) got() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.starts...)
}

func haFakeVerbundSocket(t *testing.T, status string) *haFakeVerbund {
	t.Helper()
	f := &haFakeVerbund{code: http.StatusOK, answer: `{"ok":true,"hints":[]}`}
	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("Unix-Socket nicht möglich: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/status":
			_, _ = io.WriteString(w, status)
		case "POST /v1/update/start":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.mu.Lock()
			f.starts = append(f.starts, m)
			code, answer := f.code, f.answer
			f.mu.Unlock()
			w.WriteHeader(code)
			_, _ = io.WriteString(w, answer)
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Setenv("PDH_HA_SOCKET", sock)
	return f
}

// verbundStatus: aktiver Server im Verbund mit Wartungsfenster.
func verbundStatus(install, swap bool, faulty string) string {
	w := ha.DefaultWindow()
	w.Mode, w.Install, w.Swap = ha.WindowDaily, install, swap
	wb, _ := json.Marshal(w)
	return `{"ok":true,"configured":true,"mode":"duo","self":{"id":"a","role":"aktiv","pg":"primary"},` +
		`"window":` + string(wb) + `,"update":{"active":false},"faulty":[` + faulty + `]}`
}

// 03:00 Ortszeit: nach 02:00 (Uhrzeit ohne Sicherung), vor 05:00.
var windowNow = time.Date(2026, 10, 30, 3, 0, 0, 0, time.Local)

func TestWindowTickHandsToHA(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	haInstalled(t)
	f := haFakeVerbundSocket(t, verbundStatus(true, false, ""))
	a.windowTick(context.Background(), windowNow)
	starts := f.got()
	if len(starts) != 1 {
		t.Fatalf("Starts: %v", starts)
	}
	s := starts[0]
	if s["source"] != "zeitplan" || s["commit"] != jobNewSHA || s["backup_configured"] != false || s["kind"] != nil || s["version"] != "0.77.9" { // C1: Zielversion mitgesendet
		t.Fatalf("Start-Rumpf: %v", s)
	}
	if a.jobView().Active {
		t.Fatal("im Verbund darf der Agent nicht selbst einspielen")
	}
	ws := a.windowSnapshot()
	if !ws.LastRun.Equal(windowNow) || !strings.Contains(ws.LastResult, "pdh-ha") {
		t.Fatalf("Fenster-Zustand: %+v", ws)
	}
	// Am selben Tag nicht noch einmal.
	a.windowTick(context.Background(), windowNow.Add(time.Minute))
	if len(f.got()) != 1 {
		t.Fatal("Fenster am selben Tag erneut gestartet")
	}

	t.Run("nur Rollentausch", func(t *testing.T) {
		a, _, _, _ := jobTestAgent(t)
		haInstalled(t)
		f := haFakeVerbundSocket(t, verbundStatus(false, true, ""))
		a.dr.Config.Enabled = true
		a.dr.LastBase = windowNow.Add(-30 * time.Minute)
		a.dr.LastFileSync = windowNow.Add(-10 * time.Minute)
		a.windowTick(context.Background(), windowNow)
		s := f.got()
		if len(s) != 1 || s[0]["kind"] != "swap" || s[0]["commit"] != nil {
			t.Fatalf("Rollentausch: %v", s)
		}
		if s[0]["backup_configured"] != true || s[0]["last_base_ok"] != true || s[0]["last_files_ok"] != true {
			t.Fatalf("Stand der Sicherung fehlt: %v", s[0])
		}
	})
	t.Run("nicht grün", func(t *testing.T) {
		a, _, _, _ := jobTestAgent(t)
		haInstalled(t)
		f := haFakeVerbundSocket(t, verbundStatus(true, false, ""))
		f.code, f.answer = http.StatusConflict, `{"ok":false,"error":"Spiegel nicht synchron","hints":[]}`
		a.windowTick(context.Background(), windowNow)
		if ws := a.windowSnapshot(); !strings.Contains(ws.LastResult, "Spiegel nicht synchron") {
			t.Fatalf("Grund fehlt: %+v", ws)
		}
	})
	t.Run("passiver Server", func(t *testing.T) {
		a, _, _, _ := jobTestAgent(t)
		haInstalled(t)
		f := haFakeVerbundSocket(t, strings.Replace(verbundStatus(true, false, ""), `"role":"aktiv"`, `"role":"passiv"`, 1))
		a.windowTick(context.Background(), windowNow)
		if len(f.got()) != 0 || !a.windowSnapshot().LastRun.IsZero() {
			t.Fatal("passiver Server darf das Fenster nicht starten")
		}
	})
	t.Run("schon begonnen", func(t *testing.T) {
		a, _, _, _ := jobTestAgent(t)
		haInstalled(t)
		// Vor dem Rollentausch hat der frühere aktive Server das Fenster begonnen.
		earlier := windowNow.Add(-40 * time.Minute).UTC().Format(time.RFC3339)
		st := strings.Replace(verbundStatus(true, false, ""), `"faulty":`, `"window_last_run":"`+earlier+`","faulty":`, 1)
		f := haFakeVerbundSocket(t, st)
		a.windowTick(context.Background(), windowNow)
		if len(f.got()) != 0 || !a.windowSnapshot().LastRun.IsZero() {
			t.Fatalf("zweites Fenster in derselben Nacht: %v %+v", f.got(), a.windowSnapshot())
		}
	})
	t.Run("fehlerhaft im Verbund", func(t *testing.T) {
		a, _, _, _ := jobTestAgent(t)
		haInstalled(t)
		f := haFakeVerbundSocket(t, verbundStatus(true, false, `"`+jobNewSHA[:7]+`"`))
		a.windowTick(context.Background(), windowNow)
		if len(f.got()) != 0 || !strings.Contains(a.windowSnapshot().LastResult, "fehlerhaft") {
			t.Fatalf("fehlerhafter Stand übergeben: %v %+v", f.got(), a.windowSnapshot())
		}
	})
}

// windowSingleOn: Wartungsfenster täglich auf dem Einzelserver.
func windowSingleOn(a *updateAgent, begin string) {
	a.win.mu.Lock()
	a.win.st.Window.Mode, a.win.st.Window.Begin = ha.WindowDaily, begin
	a.win.mu.Unlock()
}

func TestWindowSkipsWhenNoTarget(t *testing.T) {
	a, rec, _, _ := jobTestAgent(t)
	windowSingleOn(a, ha.WindowAfterFiles)
	a.gitRun = jobFakeGit(true, "")
	a.windowTick(context.Background(), windowNow)
	if a.jobView().Active || rec.all() != "" {
		t.Fatalf("ohne Ziel gestartet: %s", rec.all())
	}
	if ws := a.windowSnapshot(); ws.LastResult != "Update-Zweig nicht erreichbar" || !ws.LastRun.Equal(windowNow) {
		t.Fatalf("Fenster-Zustand: %+v", ws)
	}

	b, rec, _, _ := jobTestAgent(t)
	windowSingleOn(b, ha.WindowAfterFiles)
	b.gitRun = jobFakeGit(false, jobOldSHA) // Zweig = installierter Stand
	b.windowTick(context.Background(), windowNow)
	if b.jobView().Active || rec.all() != "" || !strings.Contains(b.windowSnapshot().LastResult, "Keine neue Version") {
		t.Fatalf("gleicher Stand: %+v %s", b.windowSnapshot(), rec.all())
	}
}

func TestWindowSkipsFaulty(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	windowSingleOn(a, ha.WindowAfterFiles)
	a.win.mu.Lock()
	a.win.st.Faulty = []string{jobNewSHA}
	a.win.mu.Unlock()
	a.windowTick(context.Background(), windowNow)
	if a.jobView().Active || !strings.Contains(a.windowSnapshot().LastResult, "fehlerhaft") {
		t.Fatalf("fehlerhafter Stand gestartet: %+v", a.windowSnapshot())
	}
	// Vergessen über die Schnittstelle: danach wieder möglich.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/window/faulty/"+jobNewSHA, nil)
	req.SetPathValue("commit", jobNewSHA)
	a.windowFaultyDeleteHandler(rec, req)
	if !strings.Contains(rec.Body.String(), `"ok":true`) || len(a.windowSnapshot().Faulty) != 0 {
		t.Fatalf("vergessen: %s %+v", rec.Body.String(), a.windowSnapshot())
	}
}

func TestWindowHoldsFileSync(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	windowSingleOn(a, ha.WindowBeforeFiles)
	a.dr.Config.Enabled = true
	a.dr.LastBase = windowNow.Add(-30 * time.Minute)  // Grundsicherung heute 02:30
	a.dr.LastFileSync = windowNow.Add(-2 * time.Hour) // Abgleich fällig, aber noch nicht nach der Grundsicherung
	a.dr.FilesTry = a.dr.LastFileSync
	a.windowTick(context.Background(), windowNow)
	if !a.jobView().Active {
		t.Fatalf("Fenster nicht gestartet: %+v", a.windowSnapshot())
	}
	if !a.windowHoldsFiles() {
		t.Fatal("Beginn „vor dem Abgleich“: Abgleich muss warten")
	}
	a.drStartDue(a.dr.Config, windowNow)
	if a.dr.Running != "" || !a.dr.FilesTry.Equal(a.dr.LastFileSync) {
		t.Fatalf("Abgleich trotz Fenster gestartet: running=%q", a.dr.Running)
	}
	// Neustart des Agenten (Ende von --finish): Sperre des Abgleichs bleibt.
	b := &updateAgent{mode: "systemd", repoDir: a.repoDir}
	b.dr.Config = a.dr.Config
	b.dr.LastBase, b.dr.LastFileSync, b.dr.FilesTry = a.dr.LastBase, a.dr.LastFileSync, a.dr.FilesTry
	b.jobManual = true
	b.windowLoad()
	b.loadSingleJob()
	if !b.jobView().Active || !b.windowHoldsFiles() {
		t.Fatalf("nach Neustart: aktiv=%v hold=%v", b.jobView().Active, b.windowHoldsFiles())
	}
	b.wireJobFakes(&jobRec{fail: map[string]bool{}, on: map[string]func(){}}, &jobClock{t: windowNow})
	b.windowTick(context.Background(), windowNow.Add(time.Minute))
	b.drStartDue(b.dr.Config, windowNow.Add(time.Minute))
	if !b.windowHoldsFiles() || b.dr.Running != "" || !b.dr.FilesTry.Equal(b.dr.LastFileSync) {
		t.Fatalf("Abgleich nach Neustart gestartet: hold=%v running=%q", b.windowHoldsFiles(), b.dr.Running)
	}
	// Fenster beendet: Abgleich wieder frei (auch dauerhaft).
	b.sj.mu.Lock()
	b.sj.job.Job.Phase = ha.UpDone
	b.sj.mu.Unlock()
	b.windowTick(context.Background(), windowNow.Add(2*time.Minute))
	if b.windowHoldsFiles() {
		t.Fatal("Abgleich nach dem Fenster weiter angehalten")
	}
	c := &updateAgent{mode: "systemd", repoDir: a.repoDir}
	c.windowLoad()
	if c.windowHoldsFiles() {
		t.Fatal("Freigabe des Abgleichs nicht gespeichert")
	}
}

func TestWindowEndpoints(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	rec := httptest.NewRecorder()
	a.windowPostHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/window", strings.NewReader(`{"mode":"weekly","weekday":9}`)))
	if !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "Wochentag") {
		t.Fatalf("ungültig: %s", rec.Body.String())
	}
	w := ha.DefaultWindow()
	w.Mode, w.Weekday = ha.WindowWeekly, 6
	b, _ := json.Marshal(w)
	rec = httptest.NewRecorder()
	a.windowPostHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/window", strings.NewReader(string(b))))
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("speichern: %s", rec.Body.String())
	}
	c := &updateAgent{mode: "systemd", repoDir: a.repoDir}
	c.windowLoad()
	if got := c.windowSnapshot().Window; got.Mode != ha.WindowWeekly || got.Weekday != 6 {
		t.Fatalf("nicht dauerhaft gespeichert: %+v", got)
	}
	rec = httptest.NewRecorder()
	a.windowGetHandler(rec, httptest.NewRequest(http.MethodGet, "/v1/window", nil))
	var got struct {
		OK     bool      `json:"ok"`
		Window ha.Window `json:"window"`
		Next   time.Time `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.OK || got.Window.Weekday != 6 || got.Next.IsZero() {
		t.Fatalf("lesen: %s", rec.Body.String())
	}
}
