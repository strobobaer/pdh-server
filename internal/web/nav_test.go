package web

import (
	"os"
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

// Der Nextcloud-Eintrag führt auf die Adresse aus den Server-Einstellungen; ohne
// (gültige) Adresse fehlt er – früher zeigte er fest auf eine fremde Nextcloud.
func TestBuildNavNextcloudLink(t *testing.T) {
	href := func() (string, bool) {
		for _, g := range buildNav(&BaseData{Lang: "de"}, nil) {
			for _, it := range g.Items {
				if it.Key == "nextcloud" {
					return it.Href, true
				}
			}
		}
		return "", false
	}
	t.Setenv("PDH_NEXTCLOUD_BASEURL", " https://nc.example/ ")
	if h, ok := href(); !ok || h != "https://nc.example/" {
		t.Errorf("Link %q sichtbar=%v, erwartet die eingetragene Adresse", h, ok)
	}
	for _, bad := range []string{"", "nc.example", "javascript:alert(1)", "ftp://nc.example"} {
		t.Setenv("PDH_NEXTCLOUD_BASEURL", bad)
		if h, ok := href(); ok {
			t.Errorf("Adresse %q: Eintrag sichtbar mit %q", bad, h)
		}
	}
	src, err := os.ReadFile("nav.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "strobl-home.net") {
		t.Error("nav.go enthält eine feste Nextcloud-Adresse")
	}
}

// Die Adresse aus Nextcloud wird nur übernommen, wenn andere sie erreichen.
func TestNCAddressFrom(t *testing.T) {
	for in, want := range map[string]string{
		"https://nc.example/":    "https://nc.example",
		" http://10.10.0.15 ":    "http://10.10.0.15",
		"http://localhost":       "",
		"https://127.0.0.1:8436": "",
		"http://[::1]":           "",
		"nc.example":             "",
		"":                       "",
	} {
		if got := ncAddressFrom(in); got != want {
			t.Errorf("ncAddressFrom(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

// Seiten muessen ihren Kopf ueber baseData bauen – sonst fehlen Navigation,
// Rechte und Sprache (die Stoerungs-Detailseite hatte eine leere linke Leiste).
func TestPagesUseBaseData(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "BaseData{") && !strings.Contains(line, "b := BaseData{") {
				t.Errorf("%s:%d: BaseData von Hand gebaut – h.baseData(...) verwenden", f, i+1)
			}
		}
	}
}
