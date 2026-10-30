package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// updAPI: aktiver Server a im Duo, Partner b passiv und gesehen; die
// Socket-Schnittstelle läuft. Beide stehen auf dem Stand "x0".
func updAPI(t *testing.T) (*daemon, *fakeNet, string) {
	t.Helper()
	d, fn := upActive(t)
	d.commit = "x0"
	fn.setView("b", upPeerView("x0", ""))
	d.tick(context.Background()) // Sicht auf den Partner übernehmen
	dir, err := os.MkdirTemp("", "ha")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan error, 1)
	go func() {
		defer close(done)
		serveAPI(ctx, d, sock, ready)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-done })
	return d, fn, sock
}

// updPost schickt eine Anfrage und liefert Status und Antwort.
func updCall(t *testing.T, sock, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b := ""
	if body != nil {
		raw, _ := json.Marshal(body)
		b = string(raw)
	}
	resp, err := apiRawT(sock, 10*time.Second, method, path, b)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func TestAPIUpdateStartNotReady(t *testing.T) {
	d, fn, sock := updAPI(t)
	fn.setView("b", upPeerView("x9", "")) // Partner auf anderem Stand
	d.tick(context.Background())

	code, m := updCall(t, sock, http.MethodPost, "/v1/update/start", map[string]any{"commit": "x1", "source": "knopf"})
	if code != http.StatusConflict || m["ok"] != false || !strings.Contains(m["error"].(string), "unterschiedlichem Stand") {
		t.Fatalf("nicht grün: %d %v", code, m)
	}
	if _, ok := m["hints"]; !ok {
		t.Fatalf("hints fehlt: %v", m)
	}
	if d.up.job.ID != "" {
		t.Fatalf("Auftrag trotz Sperre angelegt: %+v", d.up.job)
	}
	// Zeitplan darf nie erzwingen
	code, m = updCall(t, sock, http.MethodPost, "/v1/update/start", map[string]any{"commit": "x1", "source": "zeitplan", "force": true})
	if code != http.StatusBadRequest || d.up.job.ID != "" {
		t.Fatalf("force vom Zeitplan: %d %v", code, m)
	}
	// Knopf mit force: „alle nacheinander“, trotz nicht grün
	code, m = updCall(t, sock, http.MethodPost, "/v1/update/start", map[string]any{"commit": "x1", "source": "knopf", "force": true, "kind": "rolling"})
	if code != http.StatusOK || m["ok"] != true {
		t.Fatalf("force vom Knopf: %d %v", code, m)
	}
	j := d.up.job
	if j.Kind != ha.UpdateSequential || j.First != "a" || j.Second != "b" || j.Target != "x1" || j.Phase != ha.UpPrepare ||
		j.Old["a"] != "x0" || j.Old["b"] != "x9" || j.Source != "knopf" {
		t.Fatalf("Auftrag: %+v", j)
	}
	// ein zweiter Start während des Auftrags
	code, _ = updCall(t, sock, http.MethodPost, "/v1/update/start", map[string]any{"commit": "x2", "source": "knopf", "force": true})
	if code == http.StatusOK {
		t.Fatal("zweiter Auftrag angelegt")
	}
}

func TestAPIUpdateStartFaulty(t *testing.T) {
	d, _, sock := updAPI(t)
	if err := d.st.saveFaulty([]string{"x1"}); err != nil {
		t.Fatal(err)
	}
	d.up.faulty = []string{"x1"}

	code, m := updCall(t, sock, http.MethodPost, "/v1/update/start", map[string]any{"commit": "x1", "source": "zeitplan"})
	if code != http.StatusConflict || !strings.Contains(m["error"].(string), "Version als fehlerhaft gemerkt") {
		t.Fatalf("fehlerhafte Version vom Zeitplan: %d %v", code, m)
	}
	if d.up.job.ID != "" {
		t.Fatal("Auftrag angelegt")
	}
	// der Knopf darf sie bewusst erneut versuchen (rolling: B zuerst)
	code, m = updCall(t, sock, http.MethodPost, "/v1/update/start", map[string]any{"commit": "x1", "source": "knopf", "version": "0.9.1"})
	if code != http.StatusOK {
		t.Fatalf("Knopf: %d %v", code, m)
	}
	j := d.up.job
	if j.Kind != ha.UpdateRolling || j.First != "b" || j.Second != "a" || j.TargetVersion != "0.9.1" || j.ID == "" {
		t.Fatalf("Auftrag: %+v", j)
	}

	// Eintrag löschen
	code, m = updCall(t, sock, http.MethodDelete, "/v1/update/faulty/x1", nil)
	if code != http.StatusOK || slices.Contains(d.up.faulty, "x1") {
		t.Fatalf("löschen: %d %v %v", code, m, d.up.faulty)
	}
	if f, _ := d.st.loadFaulty(); len(f) != 0 {
		t.Fatalf("Datei: %v", f)
	}
}

func TestAPIUpdateAbort(t *testing.T) {
	d, _, sock := updAPI(t)
	if code, _ := updCall(t, sock, http.MethodPost, "/v1/update/abort", map[string]string{"by": "admin"}); code == http.StatusOK {
		t.Fatal("Abbruch ohne Auftrag")
	}
	d.up.job = rollingJob(ha.UpCountdown, 4)
	var view ha.UpdateView
	if err := apiDo(sock, http.MethodGet, "/v1/update", nil, &view); err != nil {
		t.Fatal(err)
	}
	if !view.Active || view.Kind != "rolling" || view.Phase != string(ha.UpCountdown) {
		t.Fatalf("Ansicht: %+v", view)
	}
	if code, m := updCall(t, sock, http.MethodPost, "/v1/update/abort", map[string]string{"by": "admin"}); code != http.StatusOK {
		t.Fatalf("Abbruch: %d %v", code, m)
	}
	d.mu.Lock()
	by := d.up.abort
	d.mu.Unlock()
	if by != "admin" {
		t.Fatalf("Abbruch nicht vorgemerkt: %q", by)
	}
}

func TestAPIWindow(t *testing.T) {
	d, _, sock := updAPI(t)
	w := ha.DefaultWindow()
	w.Mode, w.CountdownMin, w.LockMaxMin = "daily", 5, 45
	if err := apiDo(sock, http.MethodPut, "/v1/window", w, nil); err != nil {
		t.Fatal(err)
	}
	if d.cl.Window != w {
		t.Fatalf("Fenster nicht gespeichert: %+v", d.cl.Window)
	}
	if cd, lm, _ := d.updateDurationsLocked(); cd != 5*time.Minute || lm != 45*time.Minute {
		t.Fatalf("Countdown/Höchstdauer: %v %v", cd, lm)
	}
	var st apiStatus
	if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &st); err != nil || st.Window != w {
		t.Fatalf("Status: %+v %v", st.Window, err)
	}
	w.CountdownMin = 99
	if err := apiDo(sock, http.MethodPut, "/v1/window", w, nil); err == nil {
		t.Fatal("ungültiges Fenster angenommen")
	}
}

func TestCLIUpdateStatus(t *testing.T) {
	d, _, sock := updAPI(t)
	t.Setenv("PDH_HA_SOCKET", sock)
	d.up.job = rollingJob(ha.UpLocked, 6)
	d.up.job.LockedAt = d.wall()
	d.up.faulty = []string{"deadbeef"}

	var out, errb bytes.Buffer
	if rc := runCLI([]string{"update", "status"}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d %s", rc, errb.String())
	}
	s := out.String()
	for _, want := range []string{"rolling", string(ha.UpLocked), "0.9.1", "deadbeef"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q fehlt in:\n%s", want, s)
		}
	}
	out.Reset()
	if rc := runCLI([]string{"update", "abort"}, &out, &errb); rc != 0 {
		t.Fatalf("abort rc %d %s", rc, errb.String())
	}
	out.Reset()
	errb.Reset()
	if rc := runCLI([]string{"update", "start"}, &out, &errb); rc != 2 {
		t.Fatalf("start ohne Commit: rc %d", rc)
	}
	d.up.job = ha.UpdateJob{}
	if rc := runCLI([]string{"update", "start", "x1", "--kind", "sequential"}, &out, &errb); rc != 0 {
		t.Fatalf("start rc %d %s", rc, errb.String())
	}
	if d.up.job.Kind != ha.UpdateSequential || d.up.job.Target != "x1" || d.up.job.Source != "knopf" {
		t.Fatalf("Auftrag: %+v", d.up.job)
	}
}
