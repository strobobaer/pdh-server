package web

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// Meldetext gewählt: eigener Text freiwillig, Meldetext ist Titel und erste Zeile.
func TestEasyReportWithPresetText(t *testing.T) {
	in := easyReportIn{Type: "fault", TextID: "t1", Kind: "electrical", State: "stopped"}
	if err := easyValidate(&in); err != nil {
		t.Fatalf("Meldetext ohne eigenen Text abgelehnt: %v", err)
	}
	if err := easyValidate(&easyReportIn{Type: "fault", Text: "x", Kind: "electrical", State: "stopped"}); err == nil || err.Error() != "text" {
		t.Errorf("ohne Meldetext muss ein Text kommen: %v", err)
	}

	in.preset = "Band steht"
	title, desc := easyCompose(in, "", "Türkçe", false)
	if title != "Band steht" || !strings.HasPrefix(desc, "Band steht\n\nGemeldet per QR-Code") || strings.Contains(desc, "Original") || strings.Contains(desc, "Übersetzung nicht verfügbar") {
		t.Errorf("nur Meldetext falsch:\n%s\n%s", title, desc)
	}
	// mit Ergänzung (übersetzt): Meldetext oben, Ergänzung und Original darunter
	in.Text = "motor çok sıcak"
	title, desc = easyCompose(in, "Motor sehr heiß", "Türkçe", true)
	pre, de, orig := strings.Index(desc, "Band steht"), strings.Index(desc, "Motor sehr heiß"), strings.Index(desc, "motor çok sıcak")
	if title != "Band steht" || pre != 0 || de < pre || orig < de || !strings.Contains(desc, "Original (Türkçe)") {
		t.Errorf("Meldetext mit Ergänzung falsch:\n%s", desc)
	}
	// deutsch ergänzt: kein Original-Block
	in.Text = "Rolle 3 klemmt"
	if _, desc = easyCompose(in, "Rolle 3 klemmt", "Deutsch", true); strings.Contains(desc, "Original") || !strings.HasPrefix(desc, "Band steht\n\nRolle 3 klemmt") {
		t.Errorf("deutsche Ergänzung falsch:\n%s", desc)
	}
}

func TestEasyTextForTypeAndForm(t *testing.T) {
	if !(easyText{Type: "both"}).ForType("ticket") || (easyText{Type: "fault"}).ForType("ticket") || !(easyText{Type: "ticket"}).ForType("ticket") {
		t.Error("ForType")
	}
	form := func(v url.Values) error {
		r := httptest.NewRequest("POST", "/infrastructure/easy-texts", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ParseForm()
		text, _, _, err := readEasyTextForm(r)
		if err == nil && strings.Contains(text, "  ") {
			t.Errorf("Leerraum nicht zusammengefasst: %q", text)
		}
		return err
	}
	if err := form(url.Values{"text": {"  Band   steht "}, "report_type": {"both"}, "kind": {""}}); err != nil {
		t.Errorf("gültiger Text abgelehnt: %v", err)
	}
	for name, v := range map[string]url.Values{
		"kurz": {"text": {"ab"}, "report_type": {"both"}},
		"typ":  {"text": {"Band steht"}, "report_type": {"task"}},
		"art":  {"text": {"Band steht"}, "report_type": {"fault"}, "kind": {"hydraulic"}},
	} {
		if form(v) == nil {
			t.Errorf("%s: Fehler erwartet", name)
		}
	}
}

func TestEasyTextMove(t *testing.T) {
	ids := []string{"a", "b", "c"}
	for _, c := range []struct {
		id   string
		dir  int
		want string
	}{{"b", -1, "b,a,c"}, {"b", 1, "a,c,b"}, {"a", -1, "a,b,c"}, {"c", 1, "a,b,c"}, {"x", 1, "a,b,c"}} {
		if got := strings.Join(easyTextMove(ids, c.id, c.dir), ","); got != c.want {
			t.Errorf("%s %+d: %s, erwartet %s", c.id, c.dir, got, c.want)
		}
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Error("Eingabe verändert")
	}
}

// Rücksprung nur auf eigene Infrastruktur-Seiten, Anker bleibt am Ende.
func TestEasyTextBack(t *testing.T) {
	back := func(b string) string {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(url.Values{"back": {b}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ParseForm()
		return easyTextBack(r, "ok", nil)
	}
	if got := back("/infrastructure#easy-texts"); got != "/infrastructure?msg=ok#easy-texts" {
		t.Errorf("Katalog: %s", got)
	}
	if got := back("/infrastructure/n1?tab=easy"); got != "/infrastructure/n1?tab=easy&msg=ok" {
		t.Errorf("Anlage: %s", got)
	}
	r := httptest.NewRequest("POST", "/x", strings.NewReader("back=%2Finfrastructure%23easy-texts"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ParseForm()
	if got := easyTextBack(r, "", nil); got != "/infrastructure#easy-texts" {
		t.Errorf("ohne Hinweis: %s", got)
	}
	for _, bad := range []string{"https://evil.example", "//evil.example", "/tickets"} {
		if got := back(bad); !strings.HasPrefix(got, "/infrastructure?") {
			t.Errorf("%s → %s", bad, got)
		}
	}
}

func TestEasyTextPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	texts := []easyText{
		{ID: "t1", Text: "Band steht", Type: "fault", Kind: "mechanical", Assigned: true},
		{ID: "t2", Text: "Material fehlt", Type: "ticket", InheritedFrom: "Halle A"},
		{ID: "t3", Text: "Öl tritt aus", Type: "both"},
	}
	// Reiter an der Anlage
	d := InfraDetailData{Node: InfraNodeView{ID: "n1", Name: "Presse 3"}, Types: infraTypes, EasyTexts: texts, EasyTextCount: 2}
	d.CanEditInfra = true
	out := renderPage(t, tmpl, "infra_detail", d)
	for _, want := range []string{`data-tab="easy"`, `data-pane="easy"`, `action="/infrastructure/n1/easy-texts"`, `value="t1" checked`, "über Halle A",
		`name="assign" value="n1"`, `value="/infrastructure/n1?tab=easy"`, "Anlegen und zuordnen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Anlage ohne %q", want)
		}
	}
	if strings.Contains(out, `value="t2" checked`) {
		t.Error("geerbter Text als eigene Zuordnung angehakt")
	}
	// Katalog auf der Infrastruktur-Seite
	texts[0].Uses = 3
	texts[2].Last = true
	list := renderPage(t, tmpl, "infrastructure", InfraPageData{EasyTexts: texts, BaseData: BaseData{CanEditInfra: true}})
	for _, want := range []string{`id="easy-texts"`, "Band steht", "3 Anlagen", "nicht zugeordnet", `action="/infrastructure/easy-texts/t1"`,
		`action="/infrastructure/easy-texts/t1/delete"`, `action="/infrastructure/easy-texts"`, "immer mechanisch",
		`action="/infrastructure/easy-texts/t2/move"`, `name="dir" value="up"`, "location.hash === '#easy-texts'"} {
		if !strings.Contains(list, want) {
			t.Errorf("Katalog ohne %q", want)
		}
	}
	// erster Text: ▲ aus, letzter: ▼ aus
	if n := strings.Count(list, "disabled aria-label"); n != 2 {
		t.Errorf("%d gesperrte Pfeile, erwartet 2", n)
	}

	// Easy-Mode: Tasten mit Art und Vorbelegung
	c, _ := tmpl.Clone()
	c = bindLang(c, "de")
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "easy.gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	ed := EasyPageData{AssetID: "11111111-1111-4111-8111-111111111111", AssetName: "Presse 3",
		Texts: []easyTextOption{{ID: "t1", Label: "Belt stopped", Type: "fault", Kind: "mechanical"}}}
	if err := c.ExecuteTemplate(&buf, "easy.gohtml", ed); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	checkScripts(t, "easy-meldetexte", page)
	for _, want := range []string{`data-text-id="t1"`, `data-type="fault"`, `data-kind="mechanical"`, "Belt stopped", "text_id: S.textId"} {
		if !strings.Contains(page, want) {
			t.Errorf("Easy-Mode ohne %q", want)
		}
	}
}
