package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Werte der Zuordnungen schreibt nur storeMappingValue (und das Rechenwerk für
// seine eigenen Ergebnisse) – sonst rechnen berechnete Werte nicht nach.
func TestMappingValueSingleWriter(t *testing.T) {
	// UPDATE import_mappings SET … last_value = … innerhalb eines SQL-Texts
	re := regexp.MustCompile("(?is)UPDATE\\s+import_mappings\\s+SET\\b[^;\x60\"]*?\\blast_value\\s*=")
	allowed := map[string]bool{
		"internal/web/mapping_value.go":   true,
		"internal/modules/calc/engine.go": true,
	}
	seen := 0
	for _, root := range []string{"../../internal", "../../cmd", "../../pkg"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !re.Match(b) {
				return nil
			}
			rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
			if allowed[rel] {
				seen++
				return nil
			}
			t.Errorf("%s schreibt import_mappings.last_value selbst – Werte nur über storeMappingValue schreiben, sonst rechnen berechnete Werte nicht nach", rel)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen != len(allowed) {
		t.Errorf("erwartet die Schreibstelle in %d Dateien, gefunden in %d – stimmt das Suchmuster noch?", len(allowed), seen)
	}
}
