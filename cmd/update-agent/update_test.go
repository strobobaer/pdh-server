package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readRepoFile(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestInstallScriptCommitGuard(t *testing.T) {
	s := readRepoFile(t, "../../install_update.sh")
	drBashSyntax(t, "install_update", s)
	anc := strings.Index(s, "merge-base --is-ancestor")
	merge := strings.Index(s, "merge --ff-only")
	if anc < 0 || merge < 0 || anc > merge {
		t.Fatalf("merge-base --is-ancestor (%d) muss vor merge --ff-only (%d) stehen", anc, merge)
	}
	if !strings.Contains(s, "Ziel nicht im Update-Zweig") {
		t.Error("Abbruchmeldung „Ziel nicht im Update-Zweig“ fehlt")
	}
	// Die Prüfung steht vor jeder Änderung (vor Bau und Neustart).
	if build := strings.Index(s, "go build"); build < anc {
		t.Error("Bau steht vor der Zielprüfung")
	}
	for _, flag := range []string{"--commit", "--rollback", "--no-restart", "--finish"} {
		if !strings.Contains(s, flag) {
			t.Errorf("Schalter %s fehlt", flag)
		}
	}
}

func TestInstallScriptBranch(t *testing.T) {
	s := readRepoFile(t, "../../install_update.sh")
	if !strings.Contains(s, `branch="${PDH_UPDATE_BRANCH:-main}"`) {
		t.Error("Update-Zweig nicht aus PDH_UPDATE_BRANCH")
	}
	if !strings.Contains(s, `fetch --prune origin "$branch"`) {
		t.Error("fetch nicht auf den Update-Zweig")
	}
	if strings.Contains(s, "fetch --prune origin main") {
		t.Error("fester Zweig main im fetch")
	}
}

func TestInstallScriptRollbackGuard(t *testing.T) {
	s := readRepoFile(t, "../../install_update.sh")
	cat := strings.Index(s, "cat-file -e")
	reset := strings.Index(s, "reset --hard")
	if cat < 0 || reset < 0 || cat > reset {
		t.Fatalf("cat-file -e (%d) muss vor reset --hard (%d) stehen", cat, reset)
	}
}

func TestInstallScriptNoRestartAndFinish(t *testing.T) {
	s := readRepoFile(t, "../../install_update.sh")
	if !strings.Contains(s, "--build-only") || !strings.Contains(s, "--install-built") {
		t.Error("pdh-ha wird nicht vorab gebaut / fertig eingesetzt")
	}
	h := readRepoFile(t, "../../scripts/install-pdh-ha.sh")
	drBashSyntax(t, "install-pdh-ha", h)
	for _, w := range []string{"--build-only", "--install-built", "bin/pdh-ha.neu"} {
		if !strings.Contains(h, w) {
			t.Errorf("install-pdh-ha.sh ohne %q", w)
		}
	}
}

func testUpdateAgent(t *testing.T) (*updateAgent, chan []string) {
	t.Helper()
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	ch := make(chan []string, 1)
	a := &updateAgent{mode: "systemd"}
	a.updateRun = func(args []string) { ch <- args }
	return a, ch
}

func postUpdate(a *updateAgent, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.updateHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/update", strings.NewReader(body)))
	return rec
}

func TestUpdateHandlerBody(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	cases := []struct {
		body string
		want string
	}{
		{``, ""},
		{`{}`, ""},
		{`{"commit":"` + sha + `"}`, "--commit " + sha},
		{`{"commit":"abc1234","no_restart":true}`, "--commit abc1234 --no-restart"},
		{`{"rollback":"` + sha + `"}`, "--rollback " + sha},
		{`{"rollback":"abc1234","no_restart":true}`, "--rollback abc1234 --no-restart"},
		{`{"finish":true}`, "--finish"},
	}
	for _, c := range cases {
		a, ch := testUpdateAgent(t)
		rec := postUpdate(a, c.body)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%q: Status %d %s", c.body, rec.Code, rec.Body.String())
		}
		select {
		case args := <-ch:
			if got := strings.Join(args, " "); got != c.want {
				t.Errorf("%q: Argumente %q, erwartet %q", c.body, got, c.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: runUpdate nicht aufgerufen", c.body)
		}
	}
	for _, bad := range []string{
		`{"commit":"xyz"}`, `{"commit":"abc12"}`, `{"commit":"ABCDEF1"}`, `{"commit":"abc1234; rm -rf /"}`,
		`{"rollback":"--hard"}`, `{"commit":"abc1234","rollback":"abc1234"}`,
		`{"finish":true,"commit":"abc1234"}`, `{"finish":true,"no_restart":true}`, `{"commit":`,
	} {
		a, ch := testUpdateAgent(t)
		if rec := postUpdate(a, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: Status %d, erwartet 400", bad, rec.Code)
		}
		select {
		case <-ch:
			t.Errorf("%q: Skript trotz ungültiger Eingabe gestartet", bad)
		default:
		}
	}
}

func TestUpdateHandlerRunningBeforeAnswerAndConflict(t *testing.T) {
	a, ch := testUpdateAgent(t)
	a.status = jobStatus{Success: true, Output: "alt"}
	rec := postUpdate(a, `{"commit":"abc1234"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code)
	}
	a.mu.RLock()
	st := a.status
	a.mu.RUnlock()
	if !st.Running || st.Commit != "abc1234" || st.Success || st.Output != "" || st.StartedAt.IsZero() {
		t.Fatalf("Status nach 202: %+v", st)
	}
	<-ch
	rec = postUpdate(a, `{"commit":"def5678"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("Status %d, erwartet 409", rec.Code)
	}
	var body struct{ Error, Commit string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "Update läuft bereits" || body.Commit != "abc1234" {
		t.Fatalf("409-Rumpf: %s", rec.Body.String())
	}
}

func TestUpdateHandlerDocker(t *testing.T) {
	a := &updateAgent{mode: "docker"}
	for _, b := range []string{`{"commit":"abc1234"}`, `{"rollback":"abc1234"}`, `{"finish":true}`, `{"no_restart":true}`} {
		if rec := postUpdate(a, b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: Docker-Modus Status %d, erwartet 400", b, rec.Code)
		}
	}
}

func TestUpdateStatusPersisted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PDH_DR_STATE_DIR", dir)
	a := &updateAgent{mode: "systemd"}
	a.status = jobStatus{Success: true, Output: "Updated to abc.", Commit: "abc1234", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	a.saveUpdateStatus()
	if _, err := os.Stat(filepath.Join(dir, "update-status.json")); err != nil {
		t.Fatal(err)
	}
	b := &updateAgent{mode: "systemd"}
	b.loadUpdateStatus()
	if b.status.Commit != "abc1234" || !b.status.Success || b.status.Running || b.status.Output != "Updated to abc." {
		t.Fatalf("geladen: %+v", b.status)
	}

	// Lief beim Absturz noch: nicht mehr „läuft“, nicht erfolgreich, mit Hinweis.
	a.status = jobStatus{Running: true, Commit: "abc1234", Output: "baue ...", StartedAt: time.Now().UTC()}
	a.saveUpdateStatus()
	c := &updateAgent{mode: "systemd"}
	c.loadUpdateStatus()
	if c.status.Running || c.status.Success || !strings.Contains(c.status.Output, "Agent neu gestartet") || c.status.FinishedAt.IsZero() {
		t.Fatalf("abgebrochener Lauf: %+v", c.status)
	}

	// Endzeile des Skripts vorhanden: Erfolg.
	a.status = jobStatus{Running: true, Output: "x\nUpdated to abc1234.\n"}
	a.saveUpdateStatus()
	d := &updateAgent{mode: "systemd"}
	d.loadUpdateStatus()
	if d.status.Running || !d.status.Success {
		t.Fatalf("fertiger Lauf: %+v", d.status)
	}

	// Fehlende oder kaputte Datei: leerer Status.
	_ = os.WriteFile(filepath.Join(dir, "update-status.json"), []byte("{kaputt"), 0o600)
	e := &updateAgent{mode: "systemd"}
	e.loadUpdateStatus()
	if e.status.Running || e.status.Output != "" {
		t.Fatalf("kaputte Datei: %+v", e.status)
	}
}
