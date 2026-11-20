package web

// Einstellungsdatei auf der Seite „Server-Einstellungen“: Grund nennen, wenn PDH
// nicht speichern kann (Besitzer, Ordner), und warnen, wenn andere sie lesen können.
// Anlass 20.11.2026: .env gehörte root:root mit 644, PDH lief als michael.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnvFileOwnerProblem(t *testing.T) {
	p, fix := envOwnerProblem("root", "michael", "michael", "/home/michael/pdh/.env", "644")
	for _, want := range []string{"gehört „root“", "PDH läuft als „michael“"} {
		if !strings.Contains(p, want) {
			t.Errorf("Problem %q ohne %q", p, want)
		}
	}
	if fix != "sudo chown michael:michael /home/michael/pdh/.env && sudo chmod 600 /home/michael/pdh/.env" {
		t.Errorf("Abhilfe = %q", fix)
	}
	// Gleicher Besitzer, aber keine Schreibrechte: Rechte nennen statt Besitzer.
	if p, _ := envOwnerProblem("michael", "michael", "michael", "/x/.env", "400"); !strings.Contains(p, "darf die Datei nicht schreiben (Rechte 400)") {
		t.Errorf("gleicher Besitzer: %q", p)
	}
	// Pfad mit Leerzeichen und Hochkomma wird für die Shell gequotet.
	if _, fix := envOwnerProblem("root", "pdh", "pdh", "/srv/mein pdh/it's.env", "644"); !strings.Contains(fix, `'/srv/mein pdh/it'\''s.env'`) {
		t.Errorf("Quoting: %q", fix)
	}
	// Dienstbenutzer unbekannt: kein Befehl, der falsch sein könnte.
	if _, fix := envOwnerProblem("root", "", "", "/x/.env", "644"); fix != "" {
		t.Errorf("ohne Dienstbenutzer: %q", fix)
	}
}

func TestCheckEnvFileWritable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := checkEnvFile(path)
	if !s.Writable || s.Problem != "" || s.Fix != "" || s.Exposed != "" {
		t.Fatalf("600: %+v", s)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".pdh-write-test-*")); len(left) != 0 {
		t.Errorf("Probedatei liegen geblieben: %v", left)
	}
	// Fehlt die Datei, zählt nur der Ordner.
	if s := checkEnvFile(filepath.Join(dir, "neu.env")); !s.Writable || s.Problem != "" {
		t.Fatalf("fehlende Datei: %+v", s)
	}
	if runtime.GOOS == "windows" {
		t.Skip("Dateirechte unter Windows nicht aussagekräftig")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	s = checkEnvFile(path)
	if !s.Writable || !strings.Contains(s.Exposed, "Rechte 644") || s.ExposedFix != "sudo chmod 600 "+path {
		t.Fatalf("644: %+v", s)
	}
}

func TestCheckEnvFileNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Dateirechte unter Windows nicht aussagekräftig")
	}
	if os.Geteuid() == 0 {
		t.Skip("root darf alles schreiben")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	s := checkEnvFile(path)
	if s.Writable || !strings.Contains(s.Problem, "darf die Datei nicht schreiben") || !strings.Contains(s.Fix, "sudo chmod 600") {
		t.Fatalf("Datei 400: %+v", s)
	}

	// Datei beschreibbar, Ordner nicht: Speichern scheitert an <datei>.tmp.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s = checkEnvFile(path)
	if s.Writable || !strings.Contains(s.Problem, "Ordner "+dir) {
		t.Fatalf("Ordner 500: %+v", s)
	}
}

func TestServerConfigShowsEnvFileReason(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{EnvFile: "/home/michael/pdh/.env", FileWritable: false,
		FileProblem:    "Die Datei gehört „root“, PDH läuft als „michael“.",
		FileFix:        "sudo chown michael:michael /home/michael/pdh/.env && sudo chmod 600 /home/michael/pdh/.env",
		FileExposed:    "Die Einstellungsdatei ist für andere Benutzer des Servers lesbar (Rechte 644).",
		FileExposedFix: "sudo chmod 600 /home/michael/pdh/.env"}
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{"nicht beschreibbar", "gehört „root“", "sudo chown michael:michael /home/michael/pdh/.env", "Rechte 644", "sudo chmod 600 /home/michael/pdh/.env"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite ohne %q", want)
		}
	}
	// Ohne Befund keine der beiden Meldungen.
	ok := renderPage(t, tmpl, "server_config", ServerConfigData{EnvFile: ".env", FileWritable: true})
	if strings.Contains(ok, "nicht beschreibbar") || strings.Contains(ok, "für andere Benutzer des Servers lesbar") {
		t.Error("Meldung ohne Befund")
	}
}
