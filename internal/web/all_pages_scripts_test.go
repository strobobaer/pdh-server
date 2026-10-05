package web

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Jede Seite (mit leeren Daten gerendert) muss gueltiges JavaScript
// enthalten – ein Syntaxfehler legt sonst das ganze Skript einer Seite lahm
// (Formulare, Kontextmenues, Bearbeiten), ohne dass es serverseitig auffaellt.
func TestAllPagesScriptsValid(t *testing.T) {
	root := filepath.Join("..", "..", "web", "templates")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	base := loadTestTemplates(t)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".gohtml") {
			continue
		}
		page := strings.TrimSuffix(name, ".gohtml")
		switch page {
		case "base", "login", "setup", "labels", "global_dashboard":
			continue // eigene Seitenrahmen, eigene Tests
		}
		c, err := base.Clone()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ParseFiles(filepath.Join(root, name)); err != nil {
			t.Errorf("%s: %v", page, err)
			continue
		}
		var buf bytes.Buffer
		if err := c.ExecuteTemplate(&buf, "base.gohtml", map[string]any{}); err != nil {
			// leere Daten passen nicht zu jeder Vorlage – dann nur den gerenderten Teil pruefen
			t.Logf("%s: mit leeren Daten nur teilweise gerendert: %v", page, err)
		}
		checkScripts(t, page, buf.String())
		// Skript-Block zusaetzlich fuer sich – auch wenn der Inhalt oben abbrach
		if c.Lookup("scripts") != nil {
			var sb bytes.Buffer
			if err := c.ExecuteTemplate(&sb, "scripts", map[string]any{}); err != nil {
				t.Logf("%s: Skript-Block mit leeren Daten nur teilweise: %v", page, err)
			}
			checkScripts(t, page+"-scripts", sb.String())
		}
	}
}
