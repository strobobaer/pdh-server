package web

import (
	"os"
	"strings"
	"testing"
)

// Die Easy-Mode-Seite folgt der Bildschirmgröße (Handy, Terminal 1280×800 und 1920×1080, Hochformat):
// kein fester Handy-Streifen, Schrift und Breite über Variablen, große Tasten für Berührung.
func TestEasyPageResponsiveRules(t *testing.T) {
	b, err := os.ReadFile("../../web/templates/easy.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"main{max-width:var(--ez-max)}",
		"html,body{font-size:var(--ez-fs)}",
		"@media (min-width:768px)",
		"@media (min-width:1600px)",
		"@media (orientation:portrait)",
		"@media (pointer:coarse)",
		"main .rz-btns{grid-template-columns:repeat(auto-fill",
		".ez-big{font-size:1.35em;min-height:clamp(",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("easy.gohtml ohne Regel %q", want)
		}
	}
	if strings.Contains(s, "max-width:560px;margin") {
		t.Error("fester Handy-Streifen (max-width:560px) auf main")
	}
}

// Das Terminal rendert Rüsten, Nachtragen und die Kachel mit Raster statt fester Breite.
func TestTerminalSetupResponsiveRules(t *testing.T) {
	b, err := os.ReadFile("../../web/templates/terminal.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		".t-rzgrid{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,13em),1fr))",
		".t-rz{flex:1;min-height:0;overflow-y:auto",
		`eq .Mode "rusten"`,
		`action="/terminal/rusten/start"`,
		`action="/terminal/rusten/add"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("terminal.gohtml ohne %q", want)
		}
	}
}
