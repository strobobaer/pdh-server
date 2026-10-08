package web

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEasyValidate(t *testing.T) {
	ok := easyReportIn{Type: "fault", Name: "  Ayşe Yılmaz ", Text: " Band steht ", Kind: "electrical", State: "stopped"}
	if err := easyValidate(&ok); err != nil || ok.Name != "Ayşe Yılmaz" || ok.Text != "Band steht" {
		t.Fatalf("gültige Meldung abgelehnt: %v %+v", err, ok)
	}
	for field, bad := range map[string]easyReportIn{
		"type":  {Type: "task", Name: "Max", Text: "abc", Kind: "electrical", State: "running"},
		"name":  {Type: "fault", Name: "M", Text: "abc", Kind: "electrical", State: "running"},
		"text":  {Type: "ticket", Name: "Max", Text: "x", Kind: "mechanical", State: "running"},
		"kind":  {Type: "fault", Name: "Max", Text: "abc", Kind: "hydraulic", State: "running"},
		"state": {Type: "fault", Name: "Max", Text: "abc", Kind: "mechanical", State: "broken"},
	} {
		if err := easyValidate(&bad); err == nil || err.Error() != field {
			t.Errorf("%s: Fehler erwartet, erhalten %v", field, err)
		}
	}
}

func TestEasyCompose(t *testing.T) {
	in := easyReportIn{Name: "Ayşe", Text: "Bant durdu, motor çok ısındı", Kind: "electrical", State: "stopped"}
	title, desc := easyCompose(in, "Band steht, Motor sehr heiß geworden", "Türkçe", true)
	if title != "Band steht, Motor sehr heiß geworden" {
		t.Errorf("Titel = %q", title)
	}
	de, orig := strings.Index(desc, "Band steht"), strings.Index(desc, "Bant durdu")
	if de < 0 || orig < 0 || de > orig {
		t.Errorf("deutscher Text muss über dem Original stehen:\n%s", desc)
	}
	for _, want := range []string{"Original (Türkçe)", "von: Ayşe", "elektrisch", "Anlage steht"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Beschreibung ohne %q:\n%s", want, desc)
		}
	}
	// ohne Übersetzung: Original bleibt, mit Hinweis
	_, desc = easyCompose(in, "", "Türkçe", false)
	if !strings.HasPrefix(desc, "Bant durdu") || !strings.Contains(desc, "Übersetzung nicht verfügbar") {
		t.Errorf("Rückfall ohne Übersetzung falsch:\n%s", desc)
	}
	// deutsch eingegeben: kein Original-Block
	_, desc = easyCompose(easyReportIn{Name: "Max", Text: "Kette gerissen", Kind: "mechanical", State: "running"}, "Kette gerissen", "Deutsch", true)
	if strings.Contains(desc, "Original") || !strings.Contains(desc, "mechanisch · Anlage läuft") {
		t.Errorf("deutsche Meldung falsch:\n%s", desc)
	}
	// langer Text: Titel gekürzt
	long := strings.Repeat("Sehr lange Beschreibung ", 10)
	if title, _ := easyCompose(easyReportIn{Text: long, Kind: "mechanical", State: "running"}, long, "Deutsch", true); len([]rune(title)) > 80 {
		t.Errorf("Titel zu lang: %d", len([]rune(title)))
	}
}

func TestEasyLimiter(t *testing.T) {
	l := &easyLimiter{hits: map[string][]time.Time{}}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.allow("ip:1", 3, time.Minute, now) {
			t.Fatalf("Meldung %d abgelehnt", i+1)
		}
	}
	if l.allow("ip:1", 3, time.Minute, now) {
		t.Error("vierte Meldung in der Minute hätte gebremst werden müssen")
	}
	if !l.allow("ip:2", 3, time.Minute, now) {
		t.Error("andere Adresse darf nicht gebremst sein")
	}
	if !l.allow("ip:1", 3, time.Minute, now.Add(61*time.Second)) {
		t.Error("nach Ablauf wieder erlaubt")
	}
	r := httptest.NewRequest("POST", "/e/x/report", nil)
	r.Header.Set("X-Forwarded-For", "10.0.0.7, 172.16.0.1")
	if easyClient(r) != "10.0.0.7" {
		t.Errorf("Absender hinter Proxy = %q", easyClient(r))
	}
}

func TestEasyPageRenders(t *testing.T) {
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	tmpl := loadTestTemplates(t)
	for _, lang := range []string{"de", "tr"} {
		c, _ := tmpl.Clone()
		c = bindLang(c, lang)
		if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "easy.gohtml")); err != nil {
			t.Fatal(err)
		}
		d := EasyPageData{AssetID: "11111111-1111-4111-8111-111111111111", AssetName: "Bandsäge 2", AssetPath: "Halle A › Linie 1", TypeIcon: "⚙️",
			LoginURL: "/login?next=/a/11111111-1111-4111-8111-111111111111", Recent: []easyRecent{{Type: "Störung", Title: "Kette gerissen", When: "08.10. 09:12", Status: "Offen"}}}
		d.Lang = lang
		var buf bytes.Buffer
		if err := c.ExecuteTemplate(&buf, "easy.gohtml", d); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		out := buf.String()
		checkScripts(t, "easy", out)
		for _, want := range []string{"Bandsäge 2", "Kette gerissen", `data-start="fault"`, `data-start="ticket"`, `href="/login?next=/a/11111111-1111-4111-8111-111111111111"`,
			`'X-PDH-Easy': '1'`, `data-value="electrical"`, `data-value="stopped"`, `id="ez-website"`} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: Seite ohne %q", lang, want)
			}
		}
		if lang == "tr" && (!strings.Contains(out, "Makine durdu") || !strings.Contains(out, `lang="tr"`)) {
			t.Error("türkische Oberfläche nicht übersetzt")
		}
		// kein Zugang zu internen Seiten: keine Navigation, kein Chat
		if strings.Contains(out, `id="pdh-nav"`) || strings.Contains(out, "/chat") {
			t.Error("Easy-Mode zeigt interne Navigation")
		}
	}
}
