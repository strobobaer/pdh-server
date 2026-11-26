package web

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Abgleich-Profil (0.82.71): Test, Schreibversuch und andere Formulare zeigen ihr Ergebnis
// unter der Adresse des Formulars (z. B. /sync/<id>/test-dry-web). Ein relativer Verweis
// „?tab=maps“ führte von dort auf eine Adresse, die nur Formulare annimmt – „Seite nicht
// erreichbar“. Alle Reiter und Verweise zeigen deshalb fest auf /sync/<id>?tab=….
func TestSyncLinksAbsolute(t *testing.T) {
	rel := regexp.MustCompile(`href="\?tab=`)
	files, err := filepath.Glob(filepath.Join("..", "..", "web", "templates", "*sync*.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	more, _ := filepath.Glob(filepath.Join("..", "..", "web", "templates", "widgets", "sync*.gohtml"))
	files = append(files, more...)
	if len(files) < 4 {
		t.Fatalf("Vorlagen des Abgleichs nicht gefunden: %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(rel.FindAll(b, -1)); n > 0 {
			t.Errorf("%s: %d relative Verweise href=\"?tab=…\" – fest auf /sync/{{$p.ID}}?tab=… setzen", filepath.Base(f), n)
		}
	}
}
