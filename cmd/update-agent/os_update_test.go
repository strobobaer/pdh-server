package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"pdh/internal/ha"
)

// Betriebssystem-Updates, Teil A: Status, Prüfen, Haken, gemeinsame Sperre (os_update.go).

const osStatusJSON = `{"ok":true,"script_version":2,"system":"Debian GNU/Linux 13","kernel":"6.12.0","checked_at":"2026-11-24T10:00:00Z",` +
	`"upgradable":[{"name":"openssl","old":"3.0.1","new":"3.0.2","security":true},{"name":"nano","old":"7.1","new":"7.2","security":false}],` +
	`"security_count":1,"reboot_required":true,"reboot_pkgs":["linux-image"]}`

// osFake: Attrappe für das Betriebssystem-Skript.
type osFake struct {
	mu        sync.Mutex
	calls     []string
	statusOut string
	statusErr error
	checkErr  error
	installErr error
	installOut string
	onInstall  func() // läuft beim „install“ (z. B. Status ändern); ohne Sperre von f.mu
	rec        *jobRec // optional: Aufrufe als „os <aktion>“ ins gemeinsame Protokoll
}

func (f *osFake) run(_ context.Context, action string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, action)
	rec, hook := f.rec, f.onInstall
	f.mu.Unlock()
	if rec != nil {
		rec.add("os "+action, nil)
	}
	if action == "install" {
		if hook != nil {
			hook()
		}
		return []byte(f.installOut), f.installErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch action {
	case "status":
		return []byte(f.statusOut), f.statusErr
	case "check":
		return []byte("ok\n"), f.checkErr
	}
	return nil, nil
}

func (f *osFake) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func osTestAgent(t *testing.T) (*updateAgent, *osFake) {
	t.Helper()
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	t.Setenv("PDH_HA_ETC", t.TempDir())
	f := &osFake{statusOut: osStatusJSON}
	a := &updateAgent{mode: "systemd", repoDir: t.TempDir(), token: strings.Repeat("x", 32)}
	a.osRun = f.run
	a.osOutDir = t.TempDir()
	return a, f
}

// osServe ruft den Mux mit Bearer auf; Rumpf optional.
func osServe(t *testing.T, a *updateAgent, method, path, body string, bearer bool) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /v1/os/status", a.authorized(http.HandlerFunc(a.osStatusHandler)))
	mux.Handle("POST /v1/os/check", a.authorized(http.HandlerFunc(a.osCheckHandler)))
	mux.Handle("POST /v1/os/config", a.authorized(http.HandlerFunc(a.osConfigHandler)))
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer {
		r.Header.Set("Authorization", "Bearer "+a.token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestOSStatusHandler(t *testing.T) {
	a, f := osTestAgent(t)
	if code, _ := osServe(t, a, http.MethodGet, "/v1/os/status", "", false); code != http.StatusUnauthorized {
		t.Fatalf("ohne Bearer: %d", code)
	}
	code, m := osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if code != 200 || m["available"] != true || m["package_count"] != float64(2) || m["security_count"] != float64(1) ||
		m["reboot_required"] != true || m["with_pdh_update"] != false || m["system"] != "Debian GNU/Linux 13" {
		t.Fatalf("Antwort %d: %v", code, m)
	}
	if v, ok := m["job"]; !ok || v != nil {
		t.Fatalf("job soll null sein: %v", m["job"])
	}
	if pk, _ := m["packages"].([]any); len(pk) != 2 {
		t.Fatalf("Pakete: %v", m["packages"])
	}
	if !reflect.DeepEqual(f.all(), []string{"status"}) {
		t.Fatalf("Aufrufe: %v", f.all())
	}
}

// held_count wird durchgereicht; ein Skript mit script_version 2 (ohne held_count) ist nicht veraltet.
func TestOSStatusHeldCount(t *testing.T) {
	a, f := osTestAgent(t)
	f.statusOut = strings.Replace(osStatusJSON, `"script_version":2`, `"script_version":3,"held_count":55`, 1)
	code, m := osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if code != 200 || m["held_count"] != float64(55) || m["script_outdated"] != false || m["available"] != true {
		t.Fatalf("Antwort %d: %v", code, m)
	}
	a2, _ := osTestAgent(t) // osStatusJSON hat script_version 2
	code, m = osServe(t, a2, http.MethodGet, "/v1/os/status", "", true)
	if code != 200 || m["held_count"] != float64(0) || m["script_outdated"] != false || m["available"] != true {
		t.Fatalf("Skript Version 2: %d: %v", code, m)
	}
}

func TestOSStatusDocker(t *testing.T) {
	a, f := osTestAgent(t)
	a.mode = "docker"
	code, m := osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if code != 200 || m["available"] != false || m["docker"] != true || m["reason"] != dockerNotAvailMsg {
		t.Fatalf("Antwort %d: %v", code, m)
	}
	if len(f.all()) != 0 {
		t.Fatalf("osRun darf nicht laufen: %v", f.all())
	}
}

func TestOSStatusCluster(t *testing.T) {
	a, _ := osTestAgent(t)
	haInstalled(t)
	haFakeSocket(t, `{"ok":true,"configured":true,"mode":"duo","self":{"id":"a","role":"aktiv"}}`)
	code, m := osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if code != 200 || m["available"] != false || m["cluster"] != true || !strings.Contains(m["reason"].(string), "späteren Version") ||
		m["package_count"] != float64(2) {
		t.Fatalf("Antwort %d: %v", code, m)
	}
	code, m = osServe(t, a, http.MethodPost, "/v1/os/config", `{"with_pdh_update":true}`, true)
	if code != 400 || m["ok"] != false || !strings.Contains(m["error"].(string), "späteren Version") {
		t.Fatalf("config im Verbund: %d %v", code, m)
	}
	if code, _ := osServe(t, a, http.MethodPost, "/v1/os/check", "", true); code != 400 {
		t.Fatalf("check im Verbund: %d", code)
	}
}

func TestOSScriptOutdated(t *testing.T) {
	a, f := osTestAgent(t)
	f.statusOut = "Aufruf: pdh-os-update check|install\n"
	f.statusErr = errors.New("exit status 2")
	code, m := osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if code != 200 || m["script_outdated"] != true || m["available"] != false || !strings.Contains(m["reason"].(string), "nächsten PDH-Update") {
		t.Fatalf("Antwort %d: %v", code, m)
	}
}

func TestOSStatusFailureIsNotEmpty(t *testing.T) {
	a, f := osTestAgent(t)
	f.statusOut = `{"ok":false,"error":"Paketliste nicht lesbar"}`
	f.statusErr = errors.New("exit status 1")
	_, m := osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if m["available"] != false || m["script_outdated"] == true || !strings.Contains(m["reason"].(string), "Paketliste nicht lesbar") {
		t.Fatalf("ok:false muss ein Fehler sein: %v", m)
	}
	// auch ohne Fehlercode des Prozesses
	a2, f2 := osTestAgent(t)
	f2.statusOut = `{"ok":false,"error":"kaputt"}`
	if _, err := a2.osStatus(context.Background()); err == nil {
		t.Fatal("ok:false ohne Exit-Fehler muss Fehler liefern")
	}
}

func TestOSExecUsesSystemdRun(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	var name string
	var args []string
	n := 0
	a.osCmd = func(nm string, ar ...string) *exec.Cmd {
		n++
		name, args = nm, ar
		return exec.Command(os.Args[0], "-test.run=NONE")
	}
	_, _ = a.osExec(context.Background(), "install")
	if name != "systemd-run" || len(args) != 10 {
		t.Fatalf("Aufruf: %s %v", name, args)
	}
	want := []string{"--wait", "--collect", "--quiet"}
	if !reflect.DeepEqual(args[:3], want) || !strings.HasPrefix(args[3], "--unit=pdh-os-install-") ||
		strings.Trim(strings.TrimPrefix(args[3], "--unit=pdh-os-install-"), "0123456789-") != "" ||
		args[4] != "-p" || !strings.HasPrefix(args[5], "StandardOutput=append:") ||
		args[6] != "-p" || args[7] != "StandardError=inherit" ||
		args[8] != "/usr/local/sbin/pdh-os-update" || args[9] != "install" {
		t.Fatalf("Argumente: %v", args)
	}
	for _, bad := range []string{"full-upgrade", "install -y", ""} {
		if _, err := a.osExec(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "unbekannte Aktion") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if n != 1 {
		t.Fatalf("Hook wurde %d-mal gerufen", n)
	}
	if !reflect.DeepEqual(osActions, map[string]bool{"check": true, "install": true, "status": true}) {
		t.Fatalf("osActions: %v", osActions)
	}
}

func TestOSCheckHandler(t *testing.T) {
	a, f := osTestAgent(t)
	code, m := osServe(t, a, http.MethodPost, "/v1/os/check", "", true)
	if code != 200 || m["available"] != true || m["package_count"] != float64(2) {
		t.Fatalf("check: %d %v", code, m)
	}
	if !reflect.DeepEqual(f.all(), []string{"check", "status"}) {
		t.Fatalf("Aufrufe: %v", f.all())
	}
	osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	osServe(t, a, http.MethodGet, "/v1/os/status", "", true)
	if len(f.all()) != 2 {
		t.Fatalf("Zwischenspeicher: %v", f.all())
	}
	// die Sperre ist nach dem Prüfen wieder frei
	if rel, busy := a.lockAcquire(lockPDH); busy != "" {
		t.Fatalf("Sperre noch belegt von %s", busy)
	} else {
		rel()
	}
	rel, _ := a.lockAcquire(lockPDH)
	defer rel()
	before := len(f.all())
	code, m = osServe(t, a, http.MethodPost, "/v1/os/check", "", true)
	if code != http.StatusConflict || m["ok"] != false || !strings.Contains(m["error"].(string), "PDH-Update") {
		t.Fatalf("belegt: %d %v", code, m)
	}
	if len(f.all()) != before {
		t.Fatalf("Aufrufe bei belegter Sperre: %v", f.all())
	}
}

func TestOSConfigPersists(t *testing.T) {
	a, _ := osTestAgent(t)
	if code, m := osServe(t, a, http.MethodPost, "/v1/os/config", `{"with_pdh_update":true}`, true); code != 200 || m["ok"] != true {
		t.Fatalf("config: %d %v", code, m)
	}
	if _, m := osServe(t, a, http.MethodGet, "/v1/os/status", "", true); m["with_pdh_update"] != true {
		t.Fatalf("GET: %v", m)
	}
	b := &updateAgent{mode: "systemd"}
	b.loadOSState()
	if !b.osWithUpdate() {
		t.Fatal("nach dem Neustart verloren")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(drStateDir(), "os-state.json"))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("Datei: %v %v", st, err)
		}
	}
	if code, _ := osServe(t, a, http.MethodPost, "/v1/os/config", `kaputt`, true); code != 400 {
		t.Fatalf("ungültiger Rumpf: %d", code)
	}
}

func TestLockExclusive(t *testing.T) {
	a := &updateAgent{}
	r1, busy := a.lockAcquire(lockOS)
	if busy != "" {
		t.Fatal(busy)
	}
	if _, busy := a.lockAcquire(lockPDH); busy != lockOS {
		t.Fatalf("busyBy=%q", busy)
	}
	r2, busy := a.lockAcquire(lockOS)
	if busy != "" {
		t.Fatalf("gleicher Besitzer: %q", busy)
	}
	r1()
	r1() // doppelt freigeben darf nicht zählen
	if _, busy := a.lockAcquire(lockPDH); busy != lockOS {
		t.Fatalf("nach r1 noch r2: %q", busy)
	}
	r2()
	r3, busy := a.lockAcquire(lockPDH)
	if busy != "" {
		t.Fatalf("frei erwartet: %q", busy)
	}
	r3()
}

func TestStartUpdateRefusedWhileOS(t *testing.T) {
	a, _ := osTestAgent(t)
	rel, _ := a.lockAcquire(lockOS)
	if _, ok := a.startUpdate("abc", "install"); ok {
		t.Fatal("Update darf nicht starten")
	}
	rel()
	if _, ok := a.startUpdate("abc", "install"); !ok {
		t.Fatal("nach Freigabe muss es gehen")
	}
	if _, busy := a.lockAcquire(lockOS); busy != lockPDH {
		t.Fatalf("PDH-Update hält die Sperre: %q", busy)
	}
	// Ende des Laufs gibt die Sperre frei
	a.updateRun = func([]string) {
		a.mu.Lock()
		a.status.Running = false
		a.mu.Unlock()
	}
	a.runUpdateHook(nil)
	if r, busy := a.lockAcquire(lockOS); busy != "" {
		t.Fatalf("Sperre nach Lauf: %q", busy)
	} else {
		r()
	}
}

func TestStartUpdateRunningReleasesLock(t *testing.T) {
	a, _ := osTestAgent(t)
	if _, ok := a.startUpdate("abc", "install"); !ok {
		t.Fatal("start")
	}
	if _, ok := a.startUpdate("def", "install"); ok {
		t.Fatal("zweiter Start muss scheitern")
	}
	a.updateRun = func([]string) {
		a.mu.Lock()
		a.status.Running = false
		a.mu.Unlock()
	}
	a.runUpdateHook(nil)
	if r, busy := a.lockAcquire(lockOS); busy != "" {
		t.Fatalf("Sperre leckt: %q", busy)
	} else {
		r()
	}
}

func TestStartSingleJobRefusedWhileOS(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	rel, _ := a.lockAcquire(lockOS)
	defer rel()
	_, err := a.startSingleJob(context.Background(), srcButton, false)
	if err == nil || !strings.Contains(err.Error(), "Betriebssystem-Update") {
		t.Fatalf("Fehler: %v", err)
	}
}

func TestStartSingleJobHoldsLockUntilFinish(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	if _, busy := a.lockAcquire(lockOS); busy != lockPDH {
		t.Fatalf("Auftrag soll die Sperre halten: %q", busy)
	}
	a.sj.mu.Lock()
	s := a.sj.job
	a.sj.mu.Unlock()
	a.jobFinish(&s, ha.UpFailed, "Test", false)
	if r, busy := a.lockAcquire(lockOS); busy != "" {
		t.Fatalf("nach jobFinish frei erwartet: %q", busy)
	} else {
		r()
	}
}

func TestStartSingleJobErrorReleasesLock(t *testing.T) {
	a, _, _, _ := jobTestAgent(t)
	a.mu.Lock()
	a.status.Running = true // „Ein Update läuft bereits“
	a.mu.Unlock()
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if r, busy := a.lockAcquire(lockOS); busy != "" {
		t.Fatalf("Sperre leckt bei Fehler: %q", busy)
	} else {
		r()
	}
}

func TestNetApplyRefusedWhileOS(t *testing.T) {
	rec := &netRec{}
	a := netTestAgent(t, rec, nil)
	rel, _ := a.lockAcquire(lockOS)
	ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`)
	if ans.OK || !strings.Contains(ans.Error, "Betriebssystem-Update läuft") || len(rec.all()) != 0 {
		t.Fatalf("Antwort: %+v, Aufrufe %d", ans, len(rec.all()))
	}
	rel()
	ans = netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`)
	if !ans.OK {
		t.Fatalf("nach Freigabe: %+v", ans)
	}
	netWaitIdle(t, a)
	if r, busy := a.lockAcquire(lockOS); busy != "" {
		t.Fatalf("Netz-Sperre leckt: %q", busy)
	} else {
		r()
	}
}

func TestEsppdhInstallRefusedWhileOS(t *testing.T) {
	a := &updateAgent{mode: "systemd", repoDir: "/srv/pdh"}
	calls := 0
	a.esppdhRun = func(context.Context, string, map[string]string) (string, error) {
		calls++
		return "Version: 2026.11.1\n", nil
	}
	rel, _ := a.lockAcquire(lockOS)
	_, ans := esppdhCall(t, a, http.MethodPost)
	if ans.OK || !strings.Contains(ans.Error, "Betriebssystem-Update läuft") || calls != 0 {
		t.Fatalf("Antwort: %+v, Aufrufe %d", ans, calls)
	}
	rel()
	if _, ans = esppdhCall(t, a, http.MethodPost); !ans.OK {
		t.Fatalf("nach Freigabe: %+v", ans)
	}
	esppdhWaitDone(t, a)
	if r, busy := a.lockAcquire(lockOS); busy != "" {
		t.Fatalf("ESPHome-Sperre leckt: %q", busy)
	} else {
		r()
	}
}
