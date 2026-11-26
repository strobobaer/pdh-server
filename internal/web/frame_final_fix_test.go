package web

import (
	"strings"
	"testing"
)

// Korrekturen aus der Schlussprüfung von 0.82.68 (Ledger .superpowers/sdd/2026-11-26-rahmen-start).

// C1/W5: eingebettet bleibt der Kopf im Dokument (das Skript des Leitstands braucht seine
// Kennungen), wird nur ausgeblendet; Verweise öffnen im ganzen Fenster.
func TestGlobalEmbeddedKeepsHeaderHidden(t *testing.T) {
	out := renderGlobal(t, GlobalDashboardPageData{LoggedIn: true, Embedded: true})
	for _, s := range []string{`id="fullscreen-button"`, `id="updated-at"`, `html.gd-embedded header{display:none}`, `<base target="_top">`} {
		if !strings.Contains(out, s) {
			t.Errorf("eingebettet ohne %q", s)
		}
	}
}

// W1/W2/W6/K2: Benutzerknopf immer sichtbar, am Handy letzter Platz der unteren Leiste,
// Höhen mit der Größe der Oberfläche, Menüeinträge am Handy vollständig.
func TestFrameCSSFinalFixes(t *testing.T) {
	css := strings.ReplaceAll(uiFile(t, "frame.css"), " ", "")
	for _, s := range []string{".ft-user{flex:none;position:sticky;bottom:0", ".rail-fill{display:none}", "max-height:calc(80vh/var(--ui-scale,1))",
		".ribbon-menu{position:fixed;z-index:60;min-width:220px;max-height:calc(70vh/var(--ui-scale,1));overflow:auto", ".ribbon-items>.ribbon-item:nth-child(n+4)"} {
		if !strings.Contains(css, s) {
			t.Errorf("frame.css ohne %q", s)
		}
	}
}

// W3/Codex3: Speichern und Verwerfen über die Nachfrage laden nicht selbst neu.
func TestDashboardSaveForGuardNoReload(t *testing.T) {
	out := renderPage(t, loadTestTemplates(t), "dashboard", DashboardData{BaseData: frameData(t, nil).BaseData})
	begin := between(out, "window.pdhEdit.begin(", "});")
	if !strings.Contains(begin, "save: dwSaveOnly") || strings.Contains(begin, "reload") {
		t.Errorf("Bearbeitung lädt beim Speichern/Verwerfen neu: %s", begin)
	}
	if !strings.Contains(out, "async function dwSaveOnly(") {
		t.Error("dwSaveOnly fehlt")
	}
}

// W4/Codex1/Codex5: Aktionen mit Neuladen fragen erst nach; eine Weiterleitung zur
// Anmeldung gilt nicht als Erfolg.
func TestFrameReloadsGuarded(t *testing.T) {
	js := uiFile(t, "pdh-frame.js")
	for _, s := range []string{"function withGuard(", "withGuard(function () { formPost(", `r.redirected && /\/login/.test(r.url)`} {
		if !strings.Contains(js, s) {
			t.Errorf("pdh-frame.js ohne %q", s)
		}
	}
}

// Codex2/Codex4/K8: ein Speichervorgang zur Zeit; Formulare werden mit dem gedrückten
// Knopf fortgesetzt; Dialog-Formulare sind ausgenommen.
func TestEditSaveAndSubmit(t *testing.T) {
	js := uiFile(t, "pdh-edit.js")
	for _, s := range []string{"if (saving) return saving;", "e.submitter", "requestSubmit(", "=== 'dialog'", "'focusin'"} {
		if !strings.Contains(js, s) {
			t.Errorf("pdh-edit.js ohne %q", s)
		}
	}
}

// W7: der Bereichsknopf öffnet die erste eigene Unterseite (Start → Dashboard), nicht die
// in mehreren Bereichen geteilte Zeiterfassung.
func TestAreaButtonHome(t *testing.T) {
	v := buildAreas(&BaseData{Page: "faults", navCan: func(string) bool { return true }}, uiAreaPrefs{})
	for _, a := range v.Areas {
		if a.Key == "start" && a.Home().Key != "dashboard" {
			t.Errorf("Start öffnet %q", a.Home().Key)
		}
		if a.Key == "planung" && a.Home().Key == "time" {
			t.Error("Planung öffnet die Zeiterfassung")
		}
	}
	out := renderFrameForTest(t, "server_config", frameData(t, nil))
	if !strings.Contains(out, `href="/" data-area="start"`) {
		t.Error("Bereichsknopf Start zeigt nicht aufs Dashboard")
	}
}
