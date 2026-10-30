package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Update im Verbund: install_update.sh kann einen bestimmten Stand einspielen
// (--commit), zurückdrehen (--rollback), nur bauen (--no-restart) und später
// einsetzen (--finish). Der Auftrag kommt von pdh-ha (cmd/pdh-ha/agent_client.go).

var updateSHA = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

type updateRequest struct {
	Commit    string `json:"commit"`
	Rollback  string `json:"rollback"`
	NoRestart bool   `json:"no_restart"`
	Finish    bool   `json:"finish"`
}

// args prüft die Anfrage und liefert die Schalter für install_update.sh.
// Leere Anfrage = bisheriges Verhalten (kein Schalter).
func (r updateRequest) args() ([]string, error) {
	if r.Commit != "" && r.Rollback != "" {
		return nil, fmt.Errorf("commit und rollback schließen sich aus")
	}
	if r.Finish && (r.Commit != "" || r.Rollback != "" || r.NoRestart) {
		return nil, fmt.Errorf("finish gilt nur allein")
	}
	var args []string
	for _, f := range []struct{ flag, sha string }{{"--commit", r.Commit}, {"--rollback", r.Rollback}} {
		if f.sha == "" {
			continue
		}
		if !updateSHA.MatchString(f.sha) {
			return nil, fmt.Errorf("ungültiger Commit (7 bis 40 Hex-Zeichen, klein)")
		}
		args = append(args, f.flag, f.sha)
	}
	if r.NoRestart {
		args = append(args, "--no-restart")
	}
	if r.Finish {
		args = append(args, "--finish")
	}
	return args, nil
}

// op: Art des Laufs für /v1/status (pdh-ha ordnet das Ergebnis damit zu).
func (r updateRequest) op() string {
	return argsOp(r.Commit != "", r.Rollback != "", r.NoRestart, r.Finish)
}

// target: Ziel des Laufs (--commit bzw. --rollback).
func (r updateRequest) target() string {
	if r.Commit != "" {
		return r.Commit
	}
	return r.Rollback
}

func argsOp(commit, rollback, noRestart, finish bool) string {
	suffix := ""
	if noRestart {
		suffix = "_norestart"
	}
	switch {
	case finish:
		return "finish"
	case rollback:
		return "rollback" + suffix
	case commit:
		return "install" + suffix
	}
	return ""
}

// opOfArgs: Art und Ziel eines Laufs aus den Schaltern von install_update.sh.
func opOfArgs(args []string) (op, commit string) {
	var c, rb, nr, fin bool
	for i, a := range args {
		switch a {
		case "--commit", "--rollback":
			if i+1 < len(args) {
				commit = args[i+1]
			}
			c, rb = c || a == "--commit", rb || a == "--rollback"
		case "--no-restart":
			nr = true
		case "--finish":
			fin = true
		}
	}
	return argsOp(c, rb, nr, fin), commit
}

func (r updateRequest) isEmpty() bool {
	return r.Commit == "" && r.Rollback == "" && !r.NoRestart && !r.Finish
}

func updateStatusFile() string { return filepath.Join(drStateDir(), "update-status.json") }

// saveUpdateStatus schreibt den Status atomar; der Aufrufer hält a.mu oder
// ist allein (Start/Test). Fehler werden nur protokolliert.
func (a *updateAgent) saveUpdateStatus() {
	b, err := json.Marshal(a.status)
	if err == nil {
		err = os.MkdirAll(drStateDir(), 0o700)
	}
	if err == nil {
		tmp := updateStatusFile() + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, updateStatusFile())
		}
	}
	if err != nil {
		log.Printf("update: Status nicht gespeichert: %v", err)
	}
}

// loadUpdateStatus lädt den Status des letzten Laufs (überlebt den Neustart
// des Agenten am Ende von Update/Finish). Stand „läuft“ stammt von einem
// beendeten Prozess: ohne Endzeile des Skripts („Updated to <sha>.“) gilt der
// Lauf als abgebrochen, mit Endzeile als erfolgreich.
func (a *updateAgent) loadUpdateStatus() {
	b, err := os.ReadFile(updateStatusFile())
	if err != nil {
		return
	}
	var st jobStatus
	if json.Unmarshal(b, &st) != nil {
		return
	}
	if st.Running {
		st.Running = false
		st.Success = strings.Contains(st.Output, "Updated to ")
		if !st.Success {
			st.Output = strings.TrimRight(st.Output, "\n") + "\nAgent neu gestartet – der Lauf wurde unterbrochen.\n"
		}
		if st.FinishedAt.IsZero() {
			st.FinishedAt = time.Now().UTC()
		}
	}
	a.mu.Lock()
	a.status = st
	a.mu.Unlock()
}

func writeUpdateJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// startUpdate setzt den Status auf „läuft“, bevor der Aufrufer 202 bekommt
// (die erste Statusabfrage darf nie das alte Ergebnis sehen). false = läuft
// schon; dann ist running der laufende Stand.
func (a *updateAgent) startUpdate(commit, op string) (running jobStatus, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status.Running {
		return a.status, false
	}
	a.status = jobStatus{Running: true, Commit: commit, Op: op, StartedAt: time.Now().UTC()}
	a.saveUpdateStatus()
	return a.status, true
}

func (a *updateAgent) runUpdateHook(args []string) {
	if a.updateRun != nil {
		a.updateRun(args)
		return
	}
	a.runUpdate(args)
}

func (a *updateAgent) runUpdate(args []string) {
	output := &limitedBuffer{}
	script := filepath.Join(a.repoDir, "install_update.sh")
	command := exec.Command("bash", append([]string{script}, args...)...)
	command.Dir = a.repoDir
	command.Env = append(os.Environ(), "PDH_UPDATE_MODE="+a.mode)
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if err != nil {
		fmt.Fprintf(output, "\nUpdater failed: %v\n", err)
	}

	a.mu.Lock()
	a.status.Running = false
	a.status.Success = err == nil
	a.status.Output = output.String()
	a.status.FinishedAt = time.Now().UTC()
	a.saveUpdateStatus()
	a.mu.Unlock()
}
