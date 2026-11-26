package web

import (
	"testing"
	"time"
)

// Seitenvorlagen (Entwurf 12.1/12.2): jede Seite der Bereiche hat genau eine Vorlage;
// den Hinweis darauf sehen nur Gestaltungs-Administratoren (11.10).

func TestEveryAreaPageHasTemplate(t *testing.T) {
	for _, a := range navAreaDefs {
		for _, k := range a.Items {
			if _, ok := pageTemplateOf[k]; !ok {
				t.Errorf("Seite %q (Bereich %s) hat keine Vorlage", k, a.Key)
			}
		}
	}
}

func TestPageTemplateValues(t *testing.T) {
	for k, want := range map[string]pageTemplate{"dashboard": tplCockpit, "leitstand": tplCockpit, "infrastructure": tplBaum,
		"calendar": tplPlanung, "import": tplTabelle, "core": tplEinst, "nextcloud": tplDienst, "faults": tplListe, "time": tplListe} {
		if got := pageTemplateOf[k]; got != want {
			t.Errorf("%s: Vorlage %q, erwartet %q", k, got, want)
		}
	}
}

func TestTemplateTipText(t *testing.T) {
	at := time.Date(2026, 11, 24, 13, 12, 0, 0, time.UTC) // 14:12 in Berlin
	if got := templateTip(tplCockpit, uiMeta{By: "K. Muster", At: at}, true); got != "Vorlage: Cockpit · zuletzt angepasst von K. Muster am 24.11.2026, 14:12" {
		t.Errorf("%q", got)
	}
	if got := templateTip(tplListe, uiMeta{}, false); got != "Vorlage: Liste" {
		t.Errorf("%q", got)
	}
	if got := templateTip(tplTabelle, uiMeta{}, false); got != "Vorlage: Tabelle/Zuordnung" {
		t.Errorf("%q", got)
	}
}

// frameData: Testdaten des neuen Rahmens mit Bereichen und Menüband wie in baseData().
func frameData(t *testing.T, mod func(*BaseData)) ServerConfigData {
	t.Helper()
	d := frameTestData(true)
	d.Page = "dashboard"
	d.navCan = func(string) bool { return true }
	if mod != nil {
		mod(&d.BaseData)
	}
	d.Rail = buildAreas(&d.BaseData, uiAreaPrefs{})
	d.Ribbon = ribbonFor(&d.BaseData)
	return d
}
