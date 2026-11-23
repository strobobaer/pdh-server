package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInputInUseText(t *testing.T) {
	if got := inputInUseText([]string{"Summe"}); got != "Nicht möglich: wird als Eingangswert gebraucht von „Summe“. Bitte zuerst dort die Formel ändern." {
		t.Errorf("ein Nutzer: %q", got)
	}
	got := inputInUseText([]string{"A", "B", "C", "D", "E", "F", "G"})
	if !strings.Contains(got, "„A“, „B“, „C“, „D“, „E“ und 2 weitere.") || strings.Contains(got, "„F“") {
		t.Errorf("sieben Nutzer: %q", got)
	}
}

// Zuordnungen verschwinden auf drei Wegen: von Hand, mit ihrer Verbindung
// (Fremdschlüssel) und mit einer gespeicherten Abfrage. Jeder Weg muss vorher
// fragen, ob ein berechneter Wert die Zuordnung braucht.
func TestMappingDeletesGuarded(t *testing.T) {
	allowed := map[string]bool{
		"internal/web/import_mappings.go":   true, // von Hand
		"internal/web/import_export.go":     true, // ganze Verbindung
		"internal/web/connection_detail.go": true, // gespeicherte Abfrage samt Zuordnungen
		"internal/modules/calc/store.go":    true, // berechneter Wert selbst (prüft UsersOfMapping)
	}
	seen := map[string]bool{}
	for _, root := range []string{"../../internal", "../../cmd", "../../pkg"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(b)
			if !strings.Contains(src, "DELETE FROM import_mappings") && !strings.Contains(src, "DELETE FROM import_export_connections") {
				return nil
			}
			rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
			seen[rel] = true
			switch {
			case !allowed[rel]:
				t.Errorf("%s löscht Zuordnungen oder Verbindungen – Zuordnungen nur mit Schutz der Eingangswerte löschen (calcInputUsers)", rel)
			case strings.HasPrefix(rel, "internal/web/") && !strings.Contains(src, "calcInputUsers(") && !strings.Contains(src, "calcBlockText("):
				t.Errorf("%s löscht ohne calcInputUsers – Zuordnungen nur mit Schutz der Eingangswerte löschen (calcInputUsers)", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for rel := range allowed {
		if !seen[rel] {
			t.Errorf("%s löscht nicht mehr – Liste im Test anpassen", rel)
		}
	}
}
