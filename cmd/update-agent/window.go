package main

// Wartungsfenster (docs/superpowers/specs/2026-10-30-verbund-update-design.md §3.1, §4):
// Der Agent prüft in jedem Takt der DR-Schleife, ob das Fenster fällig ist.
// Einzelserver: Einstellungen, letztes Ergebnis und fehlerhafte Versionen
// stehen in <drStateDir>/window.json, das Update führt der Agent selbst aus
// (job.go). Verbund: Einstellungen und fehlerhafte Versionen kommen von pdh-ha
// (GET /v1/status), der Agent des aktiven Servers übergibt das Fenster mit
// POST /v1/update/start an pdh-ha. Docker-Betrieb: kein Fenster.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

const (
	dockerNotAvailMsg = "im Docker-Betrieb nicht verfügbar"
	// windowHoldMax: Sicherheitsgrenze, falls das Ende des Fensters nie
	// gemeldet wird – danach läuft der Dateiabgleich wieder.
	windowHoldMax = 4 * time.Hour
)

// windowState: Zustand des Fensters (window.json). Window und Faulty gelten
// nur auf dem Einzelserver; im Verbund führt pdh-ha beides. HoldFiles
// überlebt den Neustart des Agenten am Ende von --finish.
type windowState struct {
	Window     ha.Window `json:"window"`
	LastRun    time.Time `json:"last_run"`
	LastResult string    `json:"last_result"`
	Faulty     []string  `json:"faulty"`
	HoldFiles  bool      `json:"hold_files"` // Beginn „vor dem Abgleich“: Abgleich wartet bis zum Ende des Fensters
	HoldSince  time.Time `json:"hold_since"`
}

type winRuntime struct {
	mu     sync.Mutex
	st     windowState
	saveMu sync.Mutex
}

func windowFile() string { return filepath.Join(drStateDir(), "window.json") }

func (a *updateAgent) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

func (a *updateAgent) windowDefaults() {
	a.win.mu.Lock()
	a.win.st = windowState{Window: ha.DefaultWindow()}
	a.win.mu.Unlock()
}

// windowLoad liest window.json; fehlt sie oder ist sie ungültig: Standard (aus).
func (a *updateAgent) windowLoad() {
	a.windowDefaults()
	b, err := os.ReadFile(windowFile())
	if err != nil {
		return
	}
	var st windowState
	if err := json.Unmarshal(b, &st); err != nil {
		log.Printf("wartungsfenster: window.json nicht lesbar: %v", err)
		return
	}
	if st.Window.Validate() != nil {
		st.Window = ha.DefaultWindow()
	}
	a.win.mu.Lock()
	a.win.st = st
	a.win.mu.Unlock()
}

// writeStateJSON schreibt eine Zustandsdatei atomar (0600).
func writeStateJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *updateAgent) windowSnapshot() windowState {
	a.win.mu.Lock()
	defer a.win.mu.Unlock()
	st := a.win.st
	st.Faulty = append([]string(nil), st.Faulty...)
	return st
}

// windowUpdate ändert den Zustand und speichert ihn.
func (a *updateAgent) windowUpdate(fn func(*windowState)) {
	a.win.saveMu.Lock()
	defer a.win.saveMu.Unlock()
	a.win.mu.Lock()
	fn(&a.win.st)
	st := a.win.st
	a.win.mu.Unlock()
	if err := writeStateJSON(windowFile(), st); err != nil {
		log.Printf("wartungsfenster: window.json nicht gespeichert: %v", err)
	}
}

// windowHoldsFiles: Dateiabgleich wartet, bis das Fenster beendet ist.
func (a *updateAgent) windowHoldsFiles() bool {
	a.win.mu.Lock()
	defer a.win.mu.Unlock()
	return a.win.st.HoldFiles
}

func windowReleaseFiles(s *windowState) { s.HoldFiles, s.HoldSince = false, time.Time{} }

// commitIn: Stand in der Liste (Kurzform ab 7 Zeichen in beide Richtungen).
func commitIn(list []string, sha string) bool {
	for _, f := range list {
		if sameCommit(f, sha) {
			return true
		}
	}
	return false
}

func sameCommit(a, b string) bool {
	if len(a) < 7 || len(b) < 7 {
		return a == b && a != ""
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// drBackupReport: Stand der Ausfallschutz-Sicherung für „alles grün“.
func (a *updateAgent) drBackupReport() (configured, baseOK, filesOK bool) {
	a.dr.mu.Lock()
	defer a.dr.mu.Unlock()
	return a.dr.Config.Enabled,
		!a.dr.LastBase.IsZero() && a.dr.Errors["base"] == "",
		!a.dr.LastFileSync.IsZero() && a.dr.Errors["files"] == ""
}

// windowObs: Stand der Sicherungen des Tages aus dem DR-Zustand.
func (a *updateAgent) windowObs(now time.Time) ha.WindowState {
	a.dr.mu.Lock()
	defer a.dr.mu.Unlock()
	st := ha.WindowState{HasBackup: a.dr.Config.Enabled}
	if !st.HasBackup || a.dr.LastBase.IsZero() {
		return st
	}
	lb := a.dr.LastBase.In(now.Location())
	ly, lm, ld := lb.Date()
	ny, nm, nd := now.Date()
	st.BaseDoneToday = ly == ny && lm == nm && ld == nd && a.dr.Running != "base"
	st.FilesStartedAfterBase = a.dr.Running == "files" || a.dr.FilesTry.After(a.dr.LastBase) || a.dr.LastFileSync.After(a.dr.LastBase)
	st.FilesDoneAfterBase = a.dr.LastFileSync.After(a.dr.LastBase) && a.dr.Running != "files"
	return st
}

// windowTick: aus drTick, vor dem Start des Dateiabgleichs (auch bei
// ausgeschalteter Sicherung). Handelt nur auf dem Einzelserver bzw. auf dem
// aktiven Server des Verbunds.
func (a *updateAgent) windowTick(ctx context.Context, now time.Time) {
	if a.mode != "systemd" {
		return
	}
	hs, cluster, err := a.haCluster(ctx)
	if err != nil {
		return // pdh-ha eingerichtet, aber nicht erreichbar: kein Fenster
	}
	snap := a.windowSnapshot()
	w, faulty, jobActive, lastRun := snap.Window, snap.Faulty, a.singleJobActive(), snap.LastRun
	if cluster {
		if !hs.active() {
			if snap.HoldFiles {
				a.windowUpdate(windowReleaseFiles)
			}
			return
		}
		w, faulty, jobActive = hs.window(), hs.Faulty, hs.Update.Active
		// R18: Beginn im ganzen Verbund (pdh-ha) – nach einem Rollentausch
		// beginnt der neue aktive Server in derselben Nacht nicht noch einmal.
		// Die lokale Angabe bleibt für Auslassungen und abgelehnte Starts.
		if hs.WindowLastRun.After(lastRun) {
			lastRun = hs.WindowLastRun
		}
	}
	if snap.HoldFiles && (!jobActive || now.Sub(snap.HoldSince) >= windowHoldMax) {
		a.windowUpdate(windowReleaseFiles)
	}
	if jobActive {
		return
	}
	due, skip := ha.WindowDue(w, a.windowObs(now), lastRun, now)
	if skip != "" {
		a.windowUpdate(func(s *windowState) { s.LastRun, s.LastResult = now, skip })
		return
	}
	if !due {
		return
	}
	a.windowUpdate(func(s *windowState) { s.LastRun, s.LastResult = now, "Wartungsfenster begonnen" })
	var result string
	var started bool
	if cluster {
		result, started = a.windowToHA(ctx, w, faulty)
	} else {
		result, started = a.windowSingle(ctx, w)
	}
	if started && w.Begin == ha.WindowBeforeFiles {
		a.windowUpdate(func(s *windowState) { s.HoldFiles, s.HoldSince = true, now })
	}
	// Ein schon beendeter Auftrag hat sein Ergebnis selbst eingetragen.
	if cluster || !started || a.singleJobActive() {
		a.windowUpdate(func(s *windowState) { s.LastResult = result })
	}
}

// windowSingle: Einzelserver – das Update selbst ausführen.
func (a *updateAgent) windowSingle(ctx context.Context, w ha.Window) (string, bool) {
	if !w.Install {
		return "Nichts zu tun: „Neue Version einspielen“ ist ausgeschaltet", false
	}
	if _, err := a.startSingleJob(ctx, srcWindow, false); err != nil {
		return err.Error(), false
	}
	return "Update gestartet", true
}

// windowToHA: Verbund – Fenster an pdh-ha übergeben (Spec 4: ein Update
// „ohne Unterbrechung“ enthält schon einen Rollentausch).
func (a *updateAgent) windowToHA(ctx context.Context, w ha.Window, faulty []string) (string, bool) {
	configured, baseOK, filesOK := a.drBackupReport()
	body := map[string]any{"source": srcWindow, "backup_configured": configured, "last_base_ok": baseOK, "last_files_ok": filesOK}
	what, skipped := "", ""
	if w.Install {
		target, _, err := a.updateTarget(ctx)
		switch {
		case err != nil:
			skipped = err.Error()
		case commitIn(faulty, target):
			skipped = "Neue Version " + shortSHA(target) + " ist als fehlerhaft gemerkt"
		default:
			body["commit"], what = target, "Update auf "+shortSHA(target)
		}
	}
	if what == "" {
		if !w.Swap {
			if skipped == "" {
				skipped = "Nichts zu tun: Einspielen und Rollentausch sind ausgeschaltet"
			}
			return skipped, false
		}
		body["kind"], what = string(ha.UpdateSwap), "Rollentausch"
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	code, err := haDo(ctx, http.MethodPost, "/v1/update/start", body, &resp)
	if err != nil {
		return "pdh-ha nicht erreichbar: " + err.Error(), false
	}
	if code != http.StatusOK || !resp.OK {
		if resp.Error == "" {
			resp.Error = fmt.Sprintf("HTTP %d", code)
		}
		return what + " nicht gestartet: " + resp.Error, false
	}
	msg := what + " an pdh-ha übergeben"
	if skipped != "" {
		msg += " (" + skipped + ")"
	}
	return msg, true
}

// ── pdh-ha (Socket) ───────────────────────────────────────────────────────

// haDo: Anfrage an pdh-ha über den Unix-Socket. out wird bei jeder Antwort
// gefüllt, soweit lesbar (auch 409 mit Grund).
func haDo(ctx context.Context, method, path string, body, out any) (int, error) {
	sock := haSocketPath()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://pdh-ha"+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil && resp.StatusCode == http.StatusOK {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// haVerbund: was das Fenster aus GET /v1/status braucht.
type haVerbund struct {
	Configured bool       `json:"configured"`
	Error      string     `json:"error"`
	Mode       string     `json:"mode"`
	Self       haSelf     `json:"self"`
	Window     *ha.Window `json:"window"`
	Settings   struct {
		Window *ha.Window `json:"window"`
	} `json:"settings"`
	Update        ha.UpdateView `json:"update"`
	Faulty        []string      `json:"faulty"`
	WindowLastRun time.Time     `json:"window_last_run"` // Beginn des letzten Fensters im Verbund (R18)
}

// active: wie drHASelf – Rolle aktiv und Datenbank schreibbar.
func (s haVerbund) active() bool {
	writable := s.Self.PG == "" || s.Self.PG == "unknown" || s.Self.PG == "primary"
	return s.Self.Role == "aktiv" && writable
}

// window: Fenster der Verbund-Einstellungen (oben oder unter settings).
func (s haVerbund) window() ha.Window {
	for _, w := range []*ha.Window{s.Window, s.Settings.Window} {
		if w != nil && w.Validate() == nil {
			return *w
		}
	}
	return ha.DefaultWindow()
}

// haCluster: Verbund eingerichtet? Ohne node.json oder uneingerichtet gilt
// der Server als Einzelserver (ohne Aufruf). Fehler = pdh-ha eingerichtet,
// aber nicht erreichbar oder Konfiguration nicht lesbar.
func (a *updateAgent) haCluster(ctx context.Context) (haVerbund, bool, error) {
	var st haVerbund
	if _, err := os.Stat(filepath.Join(haEtcDir(), "node.json")); errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	code, err := haDo(ctx, http.MethodGet, "/v1/status", nil, &st)
	switch {
	case err != nil:
		return st, false, err
	case code != http.StatusOK:
		return st, false, fmt.Errorf("pdh-ha: HTTP %d", code)
	case st.Error != "":
		return st, false, errors.New(st.Error)
	}
	return st, st.Configured && st.Mode != string(ha.Single), nil
}

// ── Schnittstelle ─────────────────────────────────────────────────────────

func refuseDocker(a *updateAgent, w http.ResponseWriter) bool {
	if a.mode == "systemd" {
		return false
	}
	drRefuse(w, dockerNotAvailMsg)
	return true
}

// windowGetHandler: GET /v1/window → Einstellungen, letztes Ergebnis,
// fehlerhafte Versionen, nächster möglicher Beginn.
func (a *updateAgent) windowGetHandler(w http.ResponseWriter, r *http.Request) {
	if refuseDocker(a, w) {
		return
	}
	st := a.windowSnapshot()
	hs, cluster, err := a.haCluster(r.Context())
	if err != nil {
		drRefuse(w, "pdh-ha nicht erreichbar: "+err.Error())
		return
	}
	if cluster {
		st.Window, st.Faulty = hs.window(), hs.Faulty
	}
	a.dr.mu.Lock()
	cfg := a.dr.Config
	a.dr.mu.Unlock()
	if st.Faulty == nil {
		st.Faulty = []string{}
	}
	drJSON(w, map[string]any{"ok": true, "cluster": cluster, "window": st.Window,
		"last_run": st.LastRun, "last_result": st.LastResult, "faulty": st.Faulty,
		"next": ha.NextWindow(st.Window, cfg.BaseHour, cfg.Enabled, a.now())})
}

// windowPostHandler: POST /v1/window (ha.Window). Im Verbund an pdh-ha weitergereicht.
func (a *updateAgent) windowPostHandler(w http.ResponseWriter, r *http.Request) {
	if refuseDocker(a, w) {
		return
	}
	var win ha.Window
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&win); err != nil {
		drRefuse(w, "ungültige Anfrage")
		return
	}
	if err := win.Validate(); err != nil {
		drRefuse(w, err.Error())
		return
	}
	_, cluster, err := a.haCluster(r.Context())
	if err != nil {
		drRefuse(w, "pdh-ha nicht erreichbar: "+err.Error())
		return
	}
	if cluster {
		a.haForward(w, r.Context(), http.MethodPut, "/v1/window", win)
		return
	}
	a.windowUpdate(func(s *windowState) { s.Window = win })
	drJSON(w, map[string]any{"ok": true})
}

// windowFaultyDeleteHandler: DELETE /v1/window/faulty/{commit} – Stand
// vergessen (wird dann wieder eingespielt).
func (a *updateAgent) windowFaultyDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if refuseDocker(a, w) {
		return
	}
	commit := strings.TrimSpace(r.PathValue("commit"))
	if !updateSHA.MatchString(commit) {
		drRefuse(w, "ungültiger Commit")
		return
	}
	_, cluster, err := a.haCluster(r.Context())
	if err != nil {
		drRefuse(w, "pdh-ha nicht erreichbar: "+err.Error())
		return
	}
	if cluster {
		a.haForward(w, r.Context(), http.MethodDelete, "/v1/update/faulty/"+commit, nil)
		return
	}
	a.windowUpdate(func(s *windowState) {
		kept := s.Faulty[:0]
		for _, f := range s.Faulty {
			if !sameCommit(f, commit) {
				kept = append(kept, f)
			}
		}
		s.Faulty = kept
	})
	drJSON(w, map[string]any{"ok": true})
}

// haForward: Anfrage an pdh-ha weiterreichen, Antwort als {"ok",…}.
func (a *updateAgent) haForward(w http.ResponseWriter, ctx context.Context, method, path string, body any) {
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	code, err := haDo(ctx, method, path, body, &resp)
	switch {
	case err != nil:
		drRefuse(w, "pdh-ha nicht erreichbar: "+err.Error())
	case code != http.StatusOK || !resp.OK:
		if resp.Error == "" {
			resp.Error = fmt.Sprintf("pdh-ha: HTTP %d", code)
		}
		drRefuse(w, resp.Error)
	default:
		drJSON(w, map[string]any{"ok": true})
	}
}
