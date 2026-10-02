package web

import (
	"path/filepath"
	"strings"
	"testing"
)

func navKeys(groups []navGroup) string {
	var parts []string
	for _, g := range groups {
		var items []string
		for _, it := range g.Items {
			items = append(items, it.Key)
		}
		parts = append(parts, g.Key+"("+g.Label+"):"+strings.Join(items, ","))
	}
	return strings.Join(parts, " | ")
}

func TestBuildNavDefaultAndPermissions(t *testing.T) {
	b := &BaseData{Lang: "de"}
	got := navKeys(buildNav(b, nil))
	if strings.Contains(got, "users") || strings.Contains(got, "import") || strings.Contains(got, "chat") {
		t.Errorf("ohne Rechte sichtbar: %s", got)
	}
	if strings.Contains(got, "data(") {
		t.Error("leere Vorgabe-Gruppe Daten sichtbar")
	}
	if !strings.HasPrefix(got, "top():dashboard,leitstand,assign | work(Instandhaltung):tickets,faults") {
		t.Errorf("Vorgabe: %s", got)
	}
	b.CanTrainings = false
	for _, g := range buildNav(b, nil) {
		for _, it := range g.Items {
			if it.Key == "trainings" && it.Label != "Meine Schulungen" {
				t.Errorf("Schulungen-Label: %s", it.Label)
			}
		}
	}
}

func TestBuildNavCustomLayout(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	b := &BaseData{Lang: "en", CanImport: true}
	layout, _ := sanitizeNavLayout([]navLayoutGroup{
		{Key: "top", Label: "ignoriert", Items: []string{"faults", "dashboard"}},
		{Key: "g1", Label: "Meine Werkstatt", Items: []string{"tickets", "tickets", "gibtsnicht", "inventory"}},
		{Key: "work", Label: "Instandhaltung", Items: []string{"maintenance"}},
		{Key: "g2", Label: "Leer", Items: nil},
	})
	got := navKeys(buildNav(b, layout))
	for _, want := range []string{
		"top():faults,dashboard,",                // eigene Reihenfolge oben, Ueberschrift entfernt
		"g1(Meine Werkstatt):tickets,inventory",  // Doppelte/unbekannte entfernt, eigener Name unuebersetzt
		"work(Maintenance & repair):maintenance", // Vorgabe-Name uebersetzt; fehlende Eintraege angehaengt
		"g2(Leer):",                              // leere eigene Gruppe bleibt (zum Befuellen)
		"data(Data):import",                      // fehlende Gruppe mit neuem Eintrag ergaenzt
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fehlt %q in %s", want, got)
		}
	}
	if strings.Count(got, "tickets") != 1 {
		t.Errorf("tickets mehrfach: %s", got)
	}
	if _, err := sanitizeNavLayout(make([]navLayoutGroup, 31)); err == nil {
		t.Error("zu viele Gruppen")
	}
}
