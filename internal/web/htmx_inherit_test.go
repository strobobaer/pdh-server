package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var htmlTagRe = regexp.MustCompile(`<(/?)([a-zA-Z][a-zA-Z0-9-]*)([^>]*)>`)

// htmx vererbt hx-target an Kind-Elemente: Ein Element, das beim Laden selbst
// nachlaedt (hx-get + hx-trigger="load"), landet sonst im Ziel des umgebenden
// Formulars statt in sich selbst (Gruppenauswahl blieb bei „Lädt…“).
func TestHtmxLoadInsideTargetedForm(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "web", "templates", "*.gohtml"))
	widgets, _ := filepath.Glob(filepath.Join("..", "..", "web", "templates", "widgets", "*.gohtml"))
	for _, f := range append(files, widgets...) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		formTarget := false
		for _, m := range htmlTagRe.FindAllStringSubmatchIndex(text, -1) {
			closing, tag, attrs := text[m[2]:m[3]] == "/", strings.ToLower(text[m[4]:m[5]]), text[m[6]:m[7]]
			if tag == "form" {
				formTarget = !closing && strings.Contains(attrs, "hx-target=")
				continue
			}
			if closing || !formTarget {
				continue
			}
			if strings.Contains(attrs, "hx-get=") && strings.Contains(attrs, `hx-trigger="load`) && !strings.Contains(attrs, "hx-target=") {
				line := strings.Count(text[:m[0]], "\n") + 1
				t.Errorf("%s:%d: <%s hx-get … load> im Formular mit hx-target – eigenes hx-target=\"this\" setzen", filepath.Base(f), line, tag)
			}
		}
	}
}
