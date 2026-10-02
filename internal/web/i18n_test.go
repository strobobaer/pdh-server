package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
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
	tplRe := regexp.MustCompile(`\{\{-?\s*t\s+"((?:[^"\\]|\\.)*)"`)
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
	if err := c.ExecuteTemplate(&b, "base.gohtml", OrgUnitsData{BaseData: BaseData{Lang: "tr"}}); err != nil {
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
