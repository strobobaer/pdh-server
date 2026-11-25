package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regeln von frame.css und pdh-frame.js aus der Schlussprüfung von 0.82.63. Die
// Zeichenketten stehen für das Verhalten; geprüft wird es zusätzlich im Browser.

func TestFrameCSSFixes(t *testing.T) {
	css := uiFile(t, "frame.css")
	// W1: Größe ≠ 100 % – die Höhe kommt aus dem Kopf (zoom-Ausgleich), nicht 100vh
	if strings.Contains(css, "height:100vh") {
		t.Error("frame.css setzt height:100vh und schaltet den Größenausgleich ab")
	}
	// W2: Statusleiste mit Warnfeld als vierte Spalte, unabhängig von der Klasse
	// (setGlobalStatus überschreibt className), nur am breiten Bildschirm
	if !strings.Contains(css, "@media (min-width:761px){html.frame-ui #global-status-footer{") || strings.Contains(css, ".frame-footer") {
		t.Error("Statusleiste: Raster für das Warnfeld fehlt oder hängt an .frame-footer")
	}
	// W3/K7: „Mehr“ zeigt alle Bereiche und die Favoriten
	for _, want := range []string{".rail:not(.open) .rail-area:not(.rail-top):not(.rail-more){display:none}", ".rail:not(.open) .rail-fav"} {
		if !strings.Contains(css, want) {
			t.Errorf("Handy-Leiste ohne %q", want)
		}
	}
	// W4: schmale Leiste nur am breiten Bildschirm
	if strings.Contains(css, "\nhtml.rail-narrow .frame-app") || !strings.Contains(css, "@media (min-width:761px){html.rail-narrow .frame-app") {
		t.Error("schmale Leiste wirkt auch am Handy")
	}
	// K5: 761–1180 px – rechte Leiste schwebt wie im klassischen Rahmen, keine leere Spalte
	if !strings.Contains(css, "@media (min-width:761px) and (max-width:1180px){.frame-app{grid-template-columns:64px minmax(0,1fr)}") {
		t.Error("leere rechte Spalte zwischen 761 und 1180 px")
	}
}

func TestFrameJSFixes(t *testing.T) {
	js := uiFile(t, "pdh-frame.js")
	// W6: nur aussagekräftige Meldungen in „Letzte Meldungen“
	if !strings.Contains(js, "function logWorthy(") || !strings.Contains(js, "'Keine Fehler'") {
		t.Error("Meldungsliste ohne Filter für „Bereit“/„Fertig“")
	}
	if !strings.Contains(js, "Array.isArray(") {
		t.Error("gespeicherte Meldungen ohne Typprüfung")
	}
	// C4: alte Treffer gelten nach einer Eingabe nicht mehr
	if !strings.Contains(js, "palItems = []; palSel = 0; palSeq++;") {
		t.Error("Enter kann einen Treffer der vorigen Suche öffnen")
	}
	// K8: Tab über das Suchfeld öffnet die Befehlszeile nicht
	if strings.Contains(js, "addEventListener('focusin'") {
		t.Error("Fokus auf dem Suchfeld öffnet die Befehlszeile (Tab-Falle)")
	}
	// W7: Favorit und Reihenfolge getrennt, Firmenvorgabe nur Reihenfolge
	for _, want := range []string{"{ fav: arg }", "{ order: order() }", "reset_order: true"} {
		if !strings.Contains(js, want) {
			t.Errorf("Speichern der Bereiche ohne %q", want)
		}
	}
	if strings.Contains(js, "dataset.favs") {
		t.Error("Favoriten werden aus der sichtbaren Leiste zurückgeschrieben")
	}
}

// K9: Der Rundgang durch PDH zeigt im neuen Rahmen auf Bereichsleiste und Menüband.
func TestTourKnowsNewFrame(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "widgets", "tour.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'.rail'", `'[data-ribbon-action="create"]'`, "frame-ui"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("Rundgang ohne %s", want)
		}
	}
}
