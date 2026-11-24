package main

// Betriebssystem-Updates (Teil A, Einzelserver): Status, Prüfen, Haken „beim PDH-Update
// mit aktualisieren“ und die gemeinsame Sperre aller Aufträge des Agenten.
//
// apt, das Skript und der Neustart laufen nie im Agenten selbst, sondern über
// „systemd-run --wait --pipe --collect --quiet“ außerhalb der Sandbox des Agenten
// (/usr/local/sbin/pdh-os-update, scripts/pdh-os-update, von install_update.sh installiert).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

// Besitzer der gemeinsamen Sperre; die Meldung an den Anwender nennt den Besitzer.
const (
	lockOS      = "Betriebssystem-Update"
	lockPDH     = "PDH-Update"
	lockESPHome = "ESPHome-Einrichtung"
	lockNet     = "Netzwerk-Änderung"
)

// osActions: einzige Aktionen, die der Agent an das Skript weitergibt.
var osActions = map[string]bool{"check": true, "install": true, "status": true}

const (
	osScriptPath      = "/usr/local/sbin/pdh-os-update"
	osCheckTimeout    = 10 * time.Minute
	osStatusTimeout   = 30 * time.Second
	osInstallTimeout  = 45 * time.Minute
	osStatusCacheTTL  = 20 * time.Second
	osMaxPackages     = 200
	osClusterMsg      = "Im Verbund folgt das Betriebssystem-Update mit Teil B"
	osOutdatedMsg     = "Das Betriebssystem-Skript ist veraltet oder fehlt – es wird mit dem nächsten PDH-Update erneuert"
	osBusyOSMsg       = "Ein Betriebssystem-Update läuft – bitte danach erneut versuchen"
	osStateFileName   = "os-state.json"
	osDefaultBootPath = "/proc/sys/kernel/random/boot_id"
)

// ErrScriptOutdated: das Skript auf dem Server kennt „status“ nicht oder fehlt.
var ErrScriptOutdated = errors.New(osOutdatedMsg)

type osPackage struct {
	Name     string `json:"name"`
	Old      string `json:"old"`
	New      string `json:"new"`
	Security bool   `json:"security"`
}

// osScriptStatus: Ausgabe von „pdh-os-update status“.
type osScriptStatus struct {
	OK             bool        `json:"ok"`
	Error          string      `json:"error,omitempty"`
	ScriptVersion  int         `json:"script_version"`
	System         string      `json:"system"`
	Kernel         string      `json:"kernel"`
	CheckedAt      string      `json:"checked_at"`
	Upgradable     []osPackage `json:"upgradable"`
	SecurityCount  int         `json:"security_count"`
	RebootRequired bool        `json:"reboot_required"`
	RebootPkgs     []string    `json:"reboot_pkgs"`
}

// osLastRun: Ergebnis des letzten Laufs (os-state.json).
type osLastRun struct {
	At       string `json:"at"`
	Packages int    `json:"packages"`
	Rebooted bool   `json:"rebooted"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
}

// osState: <drStateDir>/os-state.json (0600).
type osState struct {
	WithPDHUpdate bool       `json:"with_pdh_update"`
	LastRun       *osLastRun `json:"last_run,omitempty"`
}

// osRuntime: Zustand des Betriebssystem-Teils im Agenten und die gemeinsame Sperre.
type osRuntime struct {
	lockMu    sync.Mutex
	lockOwner string
	lockN     int

	mu      sync.Mutex // Zwischenspeicher und Zustand
	cache   osScriptStatus
	cacheAt time.Time
	cacheOK bool
	state   osState
	saveMu  sync.Mutex

	unitSeq atomic.Uint64 // Zähler für eindeutige Einheitsnamen
}

func osStateFile() string { return filepath.Join(drStateDir(), osStateFileName) }

// ── gemeinsame Sperre ─────────────────────────────────────────────────────

// lockAcquire nimmt die gemeinsame Sperre. Derselbe Besitzer darf mehrfach (Zähler); ein
// anderer bekommt busyBy = Name des Besitzers. release ist mehrfach rufbar, zählt aber einmal.
func (a *updateAgent) lockAcquire(owner string) (release func(), busyBy string) {
	return a.lockTake(owner, false)
}

func (a *updateAgent) lockTake(owner string, exclusive bool) (release func(), busyBy string) {
	r := &a.os
	r.lockMu.Lock()
	defer r.lockMu.Unlock()
	if r.lockN > 0 && (exclusive || r.lockOwner != owner) {
		return func() {}, r.lockOwner
	}
	r.lockOwner = owner
	r.lockN++
	var once sync.Once
	return func() {
		once.Do(func() {
			r.lockMu.Lock()
			defer r.lockMu.Unlock()
			if r.lockN > 0 {
				r.lockN--
			}
			if r.lockN == 0 {
				r.lockOwner = ""
			}
		})
	}, ""
}

// lockTryExclusive nimmt die Sperre nur, wenn sie ganz frei ist (auch derselbe Besitzer
// bekommt sie nicht ein zweites Mal); atomar unter demselben Mutex.
func (a *updateAgent) lockTryExclusive(owner string) (release func(), busyBy string) {
	return a.lockTake(owner, true)
}

// osLastLines: die letzten n nicht leeren Zeilen, durch „ / “ verbunden.
func osLastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}

// lockHolder: aktueller Besitzer der Sperre, leer = frei.
func (a *updateAgent) lockHolder() string {
	a.os.lockMu.Lock()
	defer a.os.lockMu.Unlock()
	if a.os.lockN == 0 {
		return ""
	}
	return a.os.lockOwner
}

// lockBusyMsg:Meldung an den Anwender, wer die Sperre hält.
func lockBusyMsg(owner string) string {
	switch owner {
	case lockPDH:
		return "Ein PDH-Update läuft – bitte danach erneut versuchen"
	case lockOS:
		return osBusyOSMsg
	case lockESPHome:
		return "Die ESPHome-Einrichtung läuft – bitte danach erneut versuchen"
	case lockNet:
		return "Eine Netzwerk-Änderung läuft – bitte danach erneut versuchen"
	}
	return "Ein anderer Auftrag läuft – bitte danach erneut versuchen"
}

// ── Zustand ───────────────────────────────────────────────────────────────

// loadOSState: beim Start des Agenten; fehlt die Datei, gelten die Standardwerte.
func (a *updateAgent) loadOSState() {
	b, err := os.ReadFile(osStateFile())
	if err != nil {
		return
	}
	var st osState
	if err := json.Unmarshal(b, &st); err != nil {
		log.Printf("betriebssystem: %s nicht lesbar: %v", osStateFileName, err)
		return
	}
	a.os.mu.Lock()
	a.os.state = st
	a.os.mu.Unlock()
}

// osUpdateState ändert den Zustand und speichert ihn.
func (a *updateAgent) osUpdateState(fn func(*osState)) {
	a.os.saveMu.Lock()
	defer a.os.saveMu.Unlock()
	a.os.mu.Lock()
	fn(&a.os.state)
	st := a.os.state
	a.os.mu.Unlock()
	if err := writeStateJSON(osStateFile(), st); err != nil {
		log.Printf("betriebssystem: %s nicht gespeichert: %v", osStateFileName, err)
	}
}

func (a *updateAgent) osState() osState {
	a.os.mu.Lock()
	defer a.os.mu.Unlock()
	st := a.os.state
	if st.LastRun != nil {
		lr := *st.LastRun
		st.LastRun = &lr
	}
	return st
}

func (a *updateAgent) osWithUpdate() bool {
	a.os.mu.Lock()
	defer a.os.mu.Unlock()
	return a.os.state.WithPDHUpdate
}

// osBootID: Kennung des laufenden Starts des Rechners (für die Erkennung eines Neustarts).
func (a *updateAgent) osBootID() string {
	if a.bootID != nil {
		return a.bootID()
	}
	b, err := os.ReadFile(osDefaultBootPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ── Ausführung ────────────────────────────────────────────────────────────

func (a *updateAgent) osCommand(name string, args ...string) *exec.Cmd {
	if a.osCmd != nil {
		return a.osCmd(name, args...)
	}
	return exec.Command(name, args...)
}

// osExec führt das Skript aus (nur check, install, status) – über systemd-run außerhalb der
// Sandbox des Agenten. Rückgabe: Ausgabe (stdout und stderr) und Fehler.
func (a *updateAgent) osExec(ctx context.Context, action string) ([]byte, error) {
	return a.osExecUnit(ctx, action, "")
}

// osNewUnit: eindeutiger Name je Aufruf (Sekunde und Zähler): zwei Aufrufe in derselben Sekunde kollidieren nicht.
func (a *updateAgent) osNewUnit(action string) string {
	return fmt.Sprintf("pdh-os-%s-%d-%d", action, time.Now().Unix(), a.os.unitSeq.Add(1))
}

// osExecUnit wie osExec, aber mit vorgegebenem Namen der Einheit (leer = neuer Name); die
// Schrittmaschine merkt sich den Namen, um nach einem Neustart des Agenten zu warten.
func (a *updateAgent) osExecUnit(ctx context.Context, action, unit string) ([]byte, error) {
	if !osActions[action] {
		return nil, fmt.Errorf("unbekannte Aktion %q", action)
	}
	timeout := map[string]time.Duration{"check": osCheckTimeout, "status": osStatusTimeout, "install": osInstallTimeout}[action]
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if a.osRun != nil {
		return a.osRun(ctx, action)
	}
	if unit == "" {
		unit = a.osNewUnit(action)
	}
	cmd := a.osCommand("systemd-run", "--wait", "--pipe", "--collect", "--quiet",
		"--unit="+unit, osScriptPath, action)
	out := &limitedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.Bytes(), err
	case <-ctx.Done():
		// nur der systemd-run-Aufruf endet; die Einheit läuft zu Ende (apt nie halb abbrechen)
		_ = cmd.Process.Kill()
		<-done
		actx, acancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer acancel()
		if a.osUnitIsActive(actx, unit) {
			return out.Bytes(), &osUnitRunning{Unit: unit, Action: action}
		}
		return out.Bytes(), fmt.Errorf("Zeitüberschreitung: %w", ctx.Err())
	}
}

// osUnitRunning: der Aufruf wurde abgebrochen (Zeit, Abbruch), die Einheit läuft aber noch.
// Wer die Sperre hält, gibt sie erst frei, wenn die Einheit beendet ist (osWaitUnit).
type osUnitRunning struct{ Unit, Action string }

func (e *osUnitRunning) Error() string {
	return "Die Einheit " + e.Unit + " läuft noch – bitte später erneut versuchen"
}

func (a *updateAgent) osUnitIsActive(ctx context.Context, unit string) bool {
	if a.osUnitActive != nil {
		return a.osUnitActive(ctx, unit)
	}
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

// osWaitUnit gibt die Sperre erst frei, wenn die Einheit nicht mehr läuft (im Hintergrund).
func (a *updateAgent) osWaitUnit(unit string, release func()) {
	go func() {
		defer release()
		for {
			a.sleepFn()(10 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			active := a.osUnitIsActive(ctx, unit)
			cancel()
			if !active {
				return
			}
		}
	}()
}

var osExitRe = regexp.MustCompile(`exit status (\d+)`)

func osExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		if m := osExitRe.FindStringSubmatch(err.Error()); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return -1
}

// osOutdated: ist das Skript veraltet oder fehlt es? Nur (a) Aufrufhinweis mit Exit 2 oder
// (b) die Skriptdatei selbst fehlt (Exit 127 oder „No such file“ mit dem Skriptpfad).
// Alles andere (z. B. „Release file … not found“ von apt) ist ein echter Fehler.
func osOutdated(out []byte, err error) bool {
	text := string(out)
	if osExitCode(err) == 2 && strings.Contains(text, "Aufruf:") {
		return true
	}
	return strings.Contains(text, osScriptPath) && (osExitCode(err) == 127 || strings.Contains(text, "No such file"))
}

// osStatus liest den Stand des Skripts (Zwischenspeicher 20 s, nach „check“ geleert).
func (a *updateAgent) osStatus(ctx context.Context) (osScriptStatus, error) {
	r := &a.os
	r.mu.Lock()
	if r.cacheOK && time.Since(r.cacheAt) < osStatusCacheTTL {
		st := r.cache
		r.mu.Unlock()
		return st, nil
	}
	r.mu.Unlock()
	out, err := a.osExec(ctx, "status")
	var st osScriptStatus
	if jerr := json.Unmarshal(out, &st); jerr == nil && !st.OK {
		// ok:false ist ein Fehler, nie „nichts zu aktualisieren“
		msg := strings.TrimSpace(st.Error)
		if msg == "" {
			msg = "Status nicht lesbar"
		}
		return osScriptStatus{}, errors.New(msg)
	} else if jerr != nil || err != nil {
		text := string(out)
		var ur *osUnitRunning
		if errors.As(err, &ur) {
			return osScriptStatus{}, err
		}
		if osOutdated(out, err) {
			return osScriptStatus{}, ErrScriptOutdated
		}
		if err != nil {
			if msg := lastLine(text); msg != "" {
				return osScriptStatus{}, fmt.Errorf("%s (%w)", msg, err)
			}
			return osScriptStatus{}, err
		}
		return osScriptStatus{}, errors.New("Status des Betriebssystem-Skripts nicht lesbar")
	}
	r.mu.Lock()
	r.cache, r.cacheAt, r.cacheOK = st, time.Now(), true
	r.mu.Unlock()
	return st, nil
}

func (a *updateAgent) osClearCache() {
	a.os.mu.Lock()
	a.os.cacheOK = false
	a.os.mu.Unlock()
}

// osAvailable: dürfen in Teil A Aufträge laufen? Nicht im Docker-Betrieb und nicht im Verbund.
func (a *updateAgent) osAvailable(ctx context.Context) (ok bool, reason string, cluster bool) {
	if a.mode != "systemd" {
		return false, dockerNotAvailMsg, false
	}
	_, cl, err := a.haCluster(ctx)
	switch {
	case err != nil:
		return false, "pdh-ha nicht erreichbar: " + err.Error(), false
	case cl:
		return false, osClusterMsg, true
	}
	return true, "", false
}

// ── Schnittstelle ─────────────────────────────────────────────────────────

type osStatusResp struct {
	OK             bool           `json:"ok"`
	Available      bool           `json:"available"`
	Reason         string         `json:"reason"`
	Cluster        bool           `json:"cluster"`
	Docker         bool           `json:"docker"`
	ScriptOutdated bool           `json:"script_outdated"`
	System         string         `json:"system"`
	Kernel         string         `json:"kernel"`
	PackageCount   int            `json:"package_count"`
	SecurityCount  int            `json:"security_count"`
	Packages       []osPackage    `json:"packages"`
	RebootRequired bool           `json:"reboot_required"`
	RebootPkgs     []string       `json:"reboot_pkgs"`
	CheckedAt      string         `json:"checked_at"`
	WithPDHUpdate  bool           `json:"with_pdh_update"`
	Job            *ha.UpdateView `json:"job"`
	LastRun        *osLastRun     `json:"last_run"`
}

// osBuildStatus stellt die Antwort von GET /v1/os/status zusammen.
func (a *updateAgent) osBuildStatus(ctx context.Context) osStatusResp {
	resp := osStatusResp{OK: true, Packages: []osPackage{}, RebootPkgs: []string{}}
	st := a.osState()
	resp.WithPDHUpdate, resp.LastRun = st.WithPDHUpdate, st.LastRun
	if a.mode != "systemd" {
		resp.Reason, resp.Docker = dockerNotAvailMsg, true
		return resp
	}
	ok, reason, cluster := a.osAvailable(ctx)
	resp.Available, resp.Reason, resp.Cluster = ok, reason, cluster
	if !ok && !cluster {
		return resp // pdh-ha nicht erreichbar: keine Paketdaten
	}
	if a.singleJobActive() {
		v := a.jobView()
		resp.Job = &v
	}
	ss, err := a.osStatus(ctx)
	switch {
	case errors.Is(err, ErrScriptOutdated):
		resp.Available, resp.ScriptOutdated, resp.Reason = false, true, osOutdatedMsg
		return resp
	case err != nil:
		resp.Available, resp.Reason = false, err.Error()
		return resp
	}
	resp.System, resp.Kernel, resp.CheckedAt = ss.System, ss.Kernel, ss.CheckedAt
	resp.PackageCount, resp.SecurityCount = len(ss.Upgradable), ss.SecurityCount
	resp.Packages = ss.Upgradable
	if len(resp.Packages) > osMaxPackages {
		resp.Packages = resp.Packages[:osMaxPackages]
	}
	if resp.Packages == nil {
		resp.Packages = []osPackage{}
	}
	resp.RebootRequired = ss.RebootRequired
	if ss.RebootPkgs != nil {
		resp.RebootPkgs = ss.RebootPkgs
	}
	return resp
}

// osStatusHandler: GET /v1/os/status (immer 200)
func (a *updateAgent) osStatusHandler(w http.ResponseWriter, r *http.Request) {
	writeUpdateJSON(w, http.StatusOK, a.osBuildStatus(r.Context()))
}

func osRefuse(w http.ResponseWriter, code int, msg string) {
	writeUpdateJSON(w, code, map[string]any{"ok": false, "error": msg})
}

// osCheckHandler: POST /v1/os/check → „check“, dann „status“; Antwort wie GET.
func (a *updateAgent) osCheckHandler(w http.ResponseWriter, r *http.Request) {
	if ok, reason, _ := a.osAvailable(r.Context()); !ok {
		osRefuse(w, http.StatusBadRequest, reason)
		return
	}
	// nicht wiederholbar: ein zweites Prüfen (oder ein Prüfen neben dem Betriebssystem-Auftrag)
	// darf nie parallel zu apt laufen
	release, busy := a.lockTryExclusive(lockOS)
	if busy != "" {
		osRefuse(w, http.StatusConflict, lockBusyMsg(busy))
		return
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	// unabhängig vom Browser: ein geschlossener Tab darf apt nicht abbrechen
	out, err := a.osExec(context.Background(), "check")
	a.osClearCache()
	if err != nil {
		var ur *osUnitRunning
		if errors.As(err, &ur) {
			keep = true
			a.osWaitUnit(ur.Unit, release)
			osRefuse(w, http.StatusBadGateway, err.Error())
			return
		}
		if osOutdated(out, err) {
			writeUpdateJSON(w, http.StatusOK, a.osBuildStatus(r.Context()))
			return
		}
		msg := osLastLines(string(out), 3)
		if msg == "" {
			msg = err.Error()
		}
		log.Printf("betriebssystem: Prüfen fehlgeschlagen: %v", err)
		osRefuse(w, http.StatusBadGateway, "Prüfen fehlgeschlagen: "+msg)
		return
	}
	writeUpdateJSON(w, http.StatusOK, a.osBuildStatus(r.Context()))
}

// osConfigHandler: POST /v1/os/config {"with_pdh_update":bool}
func (a *updateAgent) osConfigHandler(w http.ResponseWriter, r *http.Request) {
	if ok, reason, _ := a.osAvailable(r.Context()); !ok {
		osRefuse(w, http.StatusBadRequest, reason)
		return
	}
	var req struct {
		With *bool `json:"with_pdh_update"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.With == nil {
		osRefuse(w, http.StatusBadRequest, "ungültige Anfrage")
		return
	}
	a.osUpdateState(func(s *osState) { s.WithPDHUpdate = *req.With })
	writeUpdateJSON(w, http.StatusOK, map[string]any{"ok": true})
}
