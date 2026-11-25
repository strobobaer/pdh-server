package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regeln von frame.css und pdh-frame.js aus der Schlussprüfung von 0.82.65 (gebaut als 0.82.63). Die
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

// Zweite Runde (Gegenprüfung und Browser).
func TestFrameCSSRound2(t *testing.T) {
	css := uiFile(t, "frame.css")
	for _, want := range []string{
		// Suchzeile nimmt nur den Restplatz – Uhr und Benutzermenü bleiben ganz sichtbar
		".frame-search{flex:1 1 0;min-width:34px;max-width:480px;",
		// Größe ≠ 100 %: vw/vh durch --ui-scale teilen wie pdh.css
		"width:min(640px,calc(100vw / var(--ui-scale, 1) - 24px))",
		"top:calc(12vh / var(--ui-scale, 1))",
		"max-height:calc(50vh / var(--ui-scale, 1))",
		"width:min(420px,calc(100vw / var(--ui-scale, 1) - 24px))",
		"width:min(420px,calc(100vw / var(--ui-scale, 1) - 32px))",
		// Knopfleiste im Dialog bricht um, „Wie Firmenvorgabe“ links
		".frame-dialog-bar{display:flex;flex-wrap:wrap;",
		".frame-dialog-bar .fd-firm{margin-right:auto}",
		// Leiste: lange Beschriftung ohne Bildlauf, Handy-Leiste verteilt die Breite
		".rail{display:flex;flex-direction:column;align-items:center;gap:var(--sp-1);padding:var(--sp-2) 0;background:var(--bg2);border-right:1px solid var(--border);overflow-y:auto;overflow-x:hidden}",
		".rail:not(.open) .rail-area{flex:1 1 0;min-width:0;width:auto}",
		// Statusleiste: Warnfeld bekommt Platz, wenn es sichtbar ist
		"#global-status-footer:has(#frame-warn:not([hidden]))",
		// eingebettet am Handy kein Platz für die ausgeblendete Leiste
		"html.pdh-embedded.frame-ui body.frame-body{padding-bottom:0}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("frame.css ohne %q", want)
		}
	}
	js := uiFile(t, "pdh-frame.js")
	for _, want := range []string{
		"e.stopPropagation(); palClose();",          // Esc schließt nicht den Assistenten dahinter
		"pal.contains(a)",                            // Fokus nur zurück, wenn er noch in der Befehlszeile war
		"typeof q === 'string' ? q : ''",             // pdhFrame.palette(event) sucht nicht nach „[object …]“
		"getModifierState('AltGraph')",               // AltGr-Zeichen öffnen die Befehlszeile
		"warn.title = warn.textContent",              // gekürzter Hinweis bleibt lesbar
		"'fd-firm'",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("pdh-frame.js ohne %q", want)
		}
	}
}

// Dritte Runde (Nachprüfung im Browser): Kopfleiste bei 768 px mit langem Titel und
// bei Größe 130–160 % – Uhr und Benutzermenü schrumpfen nie (AGENTS.md).
func TestFrameTopbarFits(t *testing.T) {
	css := uiFile(t, "frame.css")
	for _, want := range []string{
		"html.frame-ui .topbar > div:last-child{flex-shrink:0}",
		"html.frame-ui .topbar{container-type:inline-size}",
		"@container (max-width:640px){",
		"@container (max-width:900px){html.frame-ui .topbar-clock .clk-day,html.frame-ui .topbar-clock .clk-date{display:none}}",
		".frame-log{position:fixed;z-index:850;right:var(--sp-3);bottom:50px;",
		".frame-dialog-bar{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:var(--sp-2);margin-top:var(--sp-3);position:sticky;bottom:calc(-1 * var(--sp-4));",
		".frame-order li span{flex:1;min-width:0;overflow-wrap:anywhere}",
		// Statusleiste bei 768 px und Größe 130–160 %: Mindestbreiten als Anteil der Leiste
		"grid-template-columns:minmax(min(140px,25%),1fr) minmax(0,1fr) auto minmax(min(180px,30%),1.2fr)",
		"grid-template-columns:minmax(min(110px,20%),1fr) minmax(0,.8fr) minmax(min(150px,26%),auto) minmax(min(140px,26%),1.2fr)",
		// Texte kürzen sich mit „…“ (am Flex-Behälter greift text-overflow nicht)
		"@media (min-width:761px){html.frame-ui #footer-status-error{display:block;text-align:right}}",
		"html.frame-ui #footer-status-text{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}",
		"html.frame-ui .footer-progress{max-width:420px;justify-self:center;width:100%}",
		"html.frame-ui .footer-progress-label{display:block;line-height:22px;text-align:center}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("frame.css ohne %q", want)
		}
	}
	if !strings.Contains(uiFile(t, "pdh-frame.js"), "statusTitles()") {
		t.Error("gekürzte Status- und Fehlertexte ohne Tooltip")
	}
	for _, want := range []string{
	} {
		if !strings.Contains(css, want) {
			t.Errorf("frame.css ohne %q", want)
		}
	}
}

// K9: Der Rundgang durch PDH zeigt im neuen Rahmen auf Bereichsleiste und Menüband.
func TestTourKnowsNewFrame(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "widgets", "tour.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'.rail'", `'[data-ribbon-action="create"]'`, "frame-ui", `p: NEWF ? {{t "Hier findest du „Mein Konto“`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("Rundgang ohne %s", want)
		}
	}
}
