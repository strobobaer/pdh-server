package web

// Einstellungsdatei auf der Seite „Server-Einstellungen“: Kann PDH speichern?
// config.WriteEnvFile schreibt <datei>.tmp in denselben Ordner und benennt um –
// deshalb müssen Datei UND Ordner beschreibbar sein. Kann es nicht, nennt die
// Seite den Grund (Besitzer und Dienstbenutzer) und den Befehl zur Abhilfe; ist
// die Datei für andere Benutzer lesbar, warnt sie (Kennwörter und Schlüssel).

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
)

type envFileState struct {
	Writable   bool
	Problem    string // warum PDH nicht speichern kann; "" = kein Problem
	Fix        string // Befehl zur Abhilfe am Server; "" = keiner
	Exposed    string // für andere Benutzer lesbar; "" = nein
	ExposedFix string
}

func checkEnvFile(path string) envFileState {
	var s envFileState
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	dir := filepath.Dir(abs)
	svc, svcGroup := serviceAccount()
	info, statErr := os.Stat(abs)
	exists := statErr == nil
	fileOK := !exists
	if exists {
		if f, err := os.OpenFile(abs, os.O_WRONLY|os.O_APPEND, 0); err == nil {
			f.Close()
			fileOK = true
		}
	}
	dirOK := dirWritable(dir)
	s.Writable = fileOK && dirOK
	switch {
	case exists && !fileOK:
		s.Problem, s.Fix = envOwnerProblem(fileOwnerName(info), svc, svcGroup, abs, permText(info))
	case !dirOK:
		who := "PDH"
		if svc != "" {
			who = "PDH (Benutzer „" + svc + "“)"
		}
		s.Problem = "Der Ordner " + dir + " ist für " + who + " nicht beschreibbar – beim Speichern legt PDH dort kurz eine Hilfsdatei an."
	}
	if runtime.GOOS == "windows" {
		s.Fix = "" // Befehle gelten für Linux-Server
		return s
	}
	if exists && info.Mode().Perm()&0o077 != 0 {
		s.Exposed = "Die Einstellungsdatei ist für andere Benutzer des Servers lesbar (Rechte " + permText(info) + "). Sie enthält Kennwörter und Schlüssel."
		s.ExposedFix = "sudo chmod 600 " + envShellArg(abs)
	}
	return s
}

// envOwnerProblem: Grund in Anwendersprache und Befehl zur Abhilfe. Ohne
// bekannten Dienstbenutzer kein Befehl – er könnte falsch sein.
func envOwnerProblem(owner, svc, svcGroup, path, perm string) (problem, fix string) {
	if owner != "" && svc != "" && owner != svc {
		problem = "Die Datei gehört „" + owner + "“, PDH läuft als „" + svc + "“."
	} else {
		who := "PDH"
		if svc != "" {
			who = "PDH (Benutzer „" + svc + "“)"
		}
		problem = who + " darf die Datei nicht schreiben (Rechte " + perm + ")."
	}
	if svc != "" {
		if svcGroup == "" {
			svcGroup = svc
		}
		q := envShellArg(path)
		fix = "sudo chown " + svc + ":" + svcGroup + " " + q + " && sudo chmod 600 " + q
	}
	return problem, fix
}

var envPlainArg = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=-]+$`)

// envShellArg: Pfad so, dass er sich in die Shell kopieren lässt.
func envShellArg(p string) string {
	if envPlainArg.MatchString(p) {
		return p
	}
	return shellQuote(p)
}

// serviceAccount: Benutzer und Hauptgruppe, unter denen PDH läuft ("" = unbekannt).
func serviceAccount() (name, group string) {
	u, err := user.Current()
	if err != nil {
		return "", ""
	}
	group = u.Gid
	if g, err := user.LookupGroupId(u.Gid); err == nil {
		group = g.Name
	}
	return u.Username, group
}

func dirWritable(dir string) bool {
	tmp, err := os.CreateTemp(dir, ".pdh-write-test-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	return true
}

func permText(info os.FileInfo) string { return fmt.Sprintf("%o", info.Mode().Perm()) }
