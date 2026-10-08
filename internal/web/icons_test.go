package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIconDefaultsExistLocally(t *testing.T) {
	loadIconNames()
	if len(iconNames) < 4000 {
		t.Fatalf("lokale Bibliothek unvollständig: %d Symbole", len(iconNames))
	}
	seen := map[string]bool{}
	for _, g := range iconGroups() {
		for _, it := range g.Items {
			if seen[it.Key] {
				t.Errorf("Schlüssel doppelt: %s", it.Key)
			}
			seen[it.Key] = true
			if !iconExists(it.Default) {
				t.Errorf("%s: Vorgabe %q gibt es lokal nicht", it.Key, it.Default)
			}
		}
	}
	for _, key := range []string{"nav.tickets", "record.fault", "infra.plant", "storage.regal", "it.printer", "action.save"} {
		if !seen[key] {
			t.Errorf("Schlüssel %s fehlt", key)
		}
	}
}

func TestIconOverride(t *testing.T) {
	iconMu.Lock()
	old := iconOverrides
	iconOverrides = map[string]string{"infra.plant": "ti-engine"}
	iconMu.Unlock()
	defer func() { iconMu.Lock(); iconOverrides = old; iconMu.Unlock() }()

	if iconClass("infra.plant") != "ti-engine" || infraTypeIcon("plant") != "ti-engine" {
		t.Error("Änderung greift nicht")
	}
	if iconClass("infra.line") != "ti-route" {
		t.Error("ohne Änderung: Vorgabe")
	}
	if iconClass("gibt.es.nicht") != "ti-point" || infraTypeIcon("unbekannt") != "ti-hierarchy-2" {
		t.Error("Rückfall für unbekannte Schlüssel")
	}
	if recordIcon("maintenance") != "ti-tool" || recordIcon("business_partner") != "ti-building-store" {
		t.Error("recordIcon")
	}
	if linkRecordKey("/maintenance/tasks/1") != "maintenance_task" || linkRecordKey("/x") != "" {
		t.Error("linkRecordKey")
	}
	if m := iconMapAll(); m["infra.plant"] != "ti-engine" || m["nav.tickets"] == "" {
		t.Error("iconMap für Skripte")
	}
}

// Keine Symbol-Bibliothek mehr aus dem Internet; Knöpfe ohne OpenUI5.
func TestIconsServedLocally(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "web", "templates", "*.gohtml"))
	widgets, _ := filepath.Glob(filepath.Join("..", "..", "web", "templates", "widgets", "*.gohtml"))
	for _, f := range append(files, widgets...) {
		b, _ := os.ReadFile(f)
		s := string(b)
		for _, bad := range []string{"cdn.jsdelivr.net/npm/@tabler", "esm.sh/@ui5", "<ui5-icon"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: %s", filepath.Base(f), bad)
			}
		}
	}
	tmpl := loadTestTemplates(t)
	d := IconsPageData{Groups: []iconRowGroup{{Key: "infra", Label: "Anlagentypen", Rows: []iconRow{{iconDef: iconDef{"infra.plant", "Anlage", "ti-settings"}, Current: "ti-engine", Changed: true}}}}, Changed: 1}
	out := renderPage(t, tmpl, "icons", d)
	checkScripts(t, "icons", out)
	for _, want := range []string{`/vendor/tabler-icons/tabler-icons.min.css`, `name="icon.infra.plant" value="ti-engine"`, `data-default="ti-settings"`,
		`<i class="ti ti-engine">`, "/admin/icons/names", "Alles zurücksetzen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Symbol-Seite ohne %q", want)
		}
	}
	// Aktionsknöpfe (icon-button) zeigen das Symbol der Tabelle
	infra := renderPage(t, tmpl, "infrastructure", InfraPageData{})
	if !strings.Contains(infra, `<i class="ti ti-arrows-maximize" aria-hidden="true"></i>`) {
		t.Error("Knopf „Alle aufklappen“ ohne Symbol aus der Tabelle")
	}
	// Schrift steckt in der CSS-Datei (keine separate Schriftdatei, die ein
	// Zwischenspeicher wie Cloudflare falsch vorhalten kann)
	css, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "vendor", "tabler-icons", "tabler-icons.min.css"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "src:url(data:font/woff2;base64,") || strings.Contains(string(css), "./fonts/") {
		t.Error("Symbol-Schrift muss in die CSS-Datei eingebettet sein")
	}
	// Adresse mit Version: neue Version = neue Adresse (kein alter Zwischenstand)
	if !strings.Contains(out, "/vendor/tabler-icons/tabler-icons.min.css?v=") {
		t.Error("CSS-Adresse ohne Version")
	}
}
