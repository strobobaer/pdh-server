package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// i18nKeys sammelt alle uebersetzbaren Texte: {{t "…"}} in den Vorlagen und
// die Seitentitel aus h.baseData(r, page, "Titel", "Kontext").
func i18nKeys(t *testing.T) map[string][]string {
	t.Helper()
	keys := map[string][]string{}
	add := func(k, where string) {
		if k != "" {
			keys[k] = append(keys[k], where)
		}
	}
	tplRe := regexp.MustCompile(`\{\{-?\s*th?\s+"((?:[^"\\]|\\.)*)"`)
	root := filepath.Join("..", "..", "web", "templates")
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".gohtml") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, m := range tplRe.FindAllStringSubmatch(string(b), -1) {
			add(strings.ReplaceAll(m[1], `\"`, `"`), filepath.Base(p))
		}
		return nil
	})
	// tr(…, "Text", …) mit woertlichem Text im Go-Code
	trRe := regexp.MustCompile(`\btr\([^,()]+(?:\([^()]*\))?, "((?:[^"\\]|\\.)*)"`)
	// uiError("Text"): Fehlermeldungen, die beim Ausgeben uebersetzt werden
	errRe := regexp.MustCompile(`\buiError\("((?:[^"\\]|\\.)*)"\)`)
	goRe := regexp.MustCompile(`h\.baseData\(r, "[^"]*", "((?:[^"\\]|\\.)*)", "((?:[^"\\]|\\.)*)"\)`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, m := range goRe.FindAllStringSubmatch(string(b), -1) {
			add(m[1], f)
			add(m[2], f)
		}
		for _, m := range trRe.FindAllStringSubmatch(string(b), -1) {
			add(strings.ReplaceAll(m[1], `\"`, `"`), f)
		}
		for _, m := range errRe.FindAllStringSubmatch(string(b), -1) {
			add(strings.ReplaceAll(m[1], `\"`, `"`), f)
		}
	}
	// zur Laufzeit uebersetzte Texte (tr mit Variable)
	for _, d := range widgetDefs {
		add(d.Name, "widgetDefs")
		add(d.Desc, "widgetDefs")
	}
	for _, c := range widgetCategories {
		add(c, "widgetCategories")
	}
	if qa, err := loadQuickActions(nil, context.Background(), "", WidgetInstance{}, func(string) bool { return true }); err == nil {
		for _, a := range qa.([]quickAction) {
			add(a.Name, "quickActions")
		}
	}
	for _, code := range []string{"open", "in_progress", "resolved", "closed", "detected", "analyzing", "pending", "archive", "done", "skipped", "planning", "active", "paused", "completed"} {
		add(statusLabel(code), "statusLabel")
	}
	for _, code := range []string{"low", "medium", "high", "critical"} {
		add(severityWord(code), "severityWord")
	}
	for _, k := range []string{"Ticket", "Störung", "Aufgabe", "Wartung"} { // Arten in "Mir zugewiesen"
		add(k, "listMine")
	}
	for _, g := range navDefaultGroups {
		add(g.Label, "navDefaultGroups")
	}
	for _, d := range navDefs {
		add(d.Label, "navDefs")
	}
	return keys
}

func TestI18nCatalogsComplete(t *testing.T) {
	keys := i18nKeys(t)
	if len(keys) < 50 {
		t.Fatalf("nur %d Texte gefunden – Extraktion kaputt?", len(keys))
	}
	for _, l := range supportedLangs {
		if l.Code == defaultLang {
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "i18n", l.Code+".json"))
		if err != nil {
			t.Fatalf("%s: %v", l.Code, err)
		}
		cat := map[string]string{}
		if err := json.Unmarshal(b, &cat); err != nil {
			t.Fatalf("%s.json: %v", l.Code, err)
		}
		var missing []string
		for k := range keys {
			if strings.TrimSpace(cat[k]) == "" {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: %d Übersetzung(en) fehlen, z. B. %q", l.Code, len(missing), missing[:min(5, len(missing))])
		}
		// Platzhalter muessen erhalten bleiben (%d, %s …)
		ph := regexp.MustCompile(`%[dsvq]`)
		for k, v := range cat {
			if _, used := keys[k]; used && len(ph.FindAllString(k, -1)) != len(ph.FindAllString(v, -1)) {
				t.Errorf("%s: Platzhalter in %q passen nicht zu %q", l.Code, k, v)
			}
		}
	}
}

func TestTrAndLangDetection(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	if got := tr("en", "Störungen"); got == "Störungen" || got == "" {
		t.Errorf("en Störungen: %q", got)
	}
	if tr("de", "Störungen") != "Störungen" || tr("xx", "Störungen") != "Störungen" || tr("en", "gibt es nicht ×") != "gibt es nicht ×" {
		t.Error("Rückfall auf Deutsch")
	}
	for in, want := range map[string]string{"tr-TR,tr;q=0.9,en;q=0.8": "tr", "mk": "mk", "fr-FR,ro;q=0.5": "ro", "": "de", "fr": "de"} {
		if got := langFromAcceptLanguage(in); got != want {
			t.Errorf("Accept-Language %q -> %s, erwartet %s", in, got, want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en-GB")
	h := &Handler{}
	if h.requestLang(r) != "en" {
		t.Error("Browsersprache")
	}
}

func TestBaseRendersInLanguage(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	c = bindLang(c, "tr")
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "org_units.gohtml")); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	bd := BaseData{Lang: "tr"}
	bd.Nav = buildNav(&bd, nil)
	if err := c.ExecuteTemplate(&b, "base.gohtml", OrgUnitsData{BaseData: bd}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `<html lang="tr">`) || !strings.Contains(out, tr("tr", "Störungen")) || strings.Contains(out, ">Störungen<") {
		t.Error("Rahmen nicht auf Türkisch")
	}
	if !strings.Contains(out, `value="mk"`) || !strings.Contains(out, "Македонски") {
		t.Error("Sprachmenü fehlt")
	}
}

func TestNavFoldGroups(t *testing.T) {
	tmpl := loadTestTemplates(t)
	bd := BaseData{CanImport: true, CanExport: true, CanManageUsers: true, Page: "import"}
	bd.Nav = buildNav(&bd, nil)
	out := renderPage(t, tmpl, "org_units", OrgUnitsData{BaseData: bd})
	nav := out[strings.Index(out, `<nav id="pdh-nav">`):strings.Index(out, "</nav>")]
	for _, k := range []string{"work", "material", "people", "admin", "data", "links"} {
		if !strings.Contains(nav, `data-fold="`+k+`"`) || !strings.Contains(nav, `data-fold-group="`+k+`"`) {
			t.Errorf("Gruppe %s fehlt", k)
		}
	}
	if !strings.Contains(nav, `data-nav="import"`) || !strings.Contains(nav, `class="nav-item active" data-nav="import"`) {
		t.Error("aktiver Eintrag Import fehlt")
	}
	if strings.Contains(nav, `data-nav="roles"`) {
		t.Error("Rollen ohne Recht sichtbar")
	}
	if o, c := strings.Count(nav, "<div"), strings.Count(nav, "</div>"); o != c {
		t.Errorf("Navigation: %d <div> vs %d </div>", o, c)
	}
}

func TestDashboardWidgetsInEnglish(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	c = bindLang(c, "en")
	var b strings.Builder
	d, _ := widgetDef("chart_trend")
	if err := c.ExecuteTemplate(&b, "dw-body", widgetBody{Def: d, Lang: "en", Data: trendData{ShowTickets: true, ShowFaults: true, SumTickets: 3, SumFaults: 1, Days: []trendDay{{Label: "Mon 05.", Tickets: 3, Faults: 1}}}}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"Tickets (3)", "Faults (1)", "Mon 05.: 3 tickets, 1 faults"} {
		if !strings.Contains(out, want) {
			t.Errorf("englisches Widget enthält %q nicht: %s", want, out)
		}
	}
	b.Reset()
	d, _ = widgetDef("quick_links")
	_ = c.ExecuteTemplate(&b, "dw-body", widgetBody{Def: d, Lang: "en"})
	if !strings.Contains(b.String(), "<b>Customize dashboard</b>") {
		t.Errorf("HTML-Satz nicht als HTML: %s", b.String())
	}
	if longDate("de", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) != "Donnerstag, 1. Oktober 2026" || longDate("en", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) != "Thursday, 1 October 2026" {
		t.Error("longDate")
	}
}
