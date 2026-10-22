package web

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultLabelLayoutFits(t *testing.T) {
	for _, s := range labelSizes {
		l := defaultLabelLayout(s)
		for _, e := range l.Elements {
			if e.X < 0 || e.Y < 0 || e.X+e.W > s.W+0.05 || e.Y+e.H > s.H+0.05 {
				t.Errorf("%s: Feld %s ragt über das Etikett (%.1f/%.1f %.1f×%.1f)", s.Key, e.Key, e.X, e.Y, e.W, e.H)
			}
		}
		if len(l.Elements) != len(labelFields) {
			t.Errorf("%s: %d Felder, erwartet %d", s.Key, len(l.Elements), len(labelFields))
		}
	}
}

func TestLabelLayoutSanitize(t *testing.T) {
	s := labelSizeByKey("62x29")
	l := labelLayout{Elements: []labelElement{
		{Key: "pn", X: 80, Y: -3, W: 500, H: 0, Size: 99, Align: "quer", Lines: 9, Max: -1, Visible: true},
		{Key: "pn", Visible: true},    // doppelt
		{Key: "boese", Visible: true}, // unbekannt
	}}
	l.sanitize(s)
	pn := l.Elements[0]
	if pn.Key != "pn" || pn.X > s.W || pn.Y != 0 || pn.Size != 40 || pn.Align != "left" || pn.Lines != 1 || pn.Max != 0 || pn.H < 1 {
		t.Errorf("nicht begrenzt: %+v", pn)
	}
	if len(l.Elements) != len(labelFields) {
		t.Errorf("fehlende Felder nicht ergänzt: %d", len(l.Elements))
	}
	for _, e := range l.Elements[1:] {
		if e.Visible {
			t.Errorf("ergänztes Feld %s darf nicht sichtbar sein", e.Key)
		}
	}
}

func TestLabelElementText(t *testing.T) {
	it := testLabel
	it.Manufacturer, it.EAN = "SKF", "7316571234567"
	if got := (labelElement{Key: "pn", Prefix: "Nr. "}).text(it); got != "Nr. 4711-A" {
		t.Errorf("Präfix: %q", got)
	}
	if got := (labelElement{Key: "name", Max: 10}).text(it); got != "Rillenkug…" {
		t.Errorf("Kürzung: %q", got)
	}
	if got := (labelElement{Key: "min", Prefix: "Min "}).text(it); got != "Min 4 Stk" {
		t.Errorf("Mindestmenge: %q", got)
	}
	if got := (labelElement{Key: "mf"}).text(labelItem{}); got != "" {
		t.Errorf("leeres Feld mit Präfix: %q", got)
	}
}

func TestLayoutRenderersUseLayout(t *testing.T) {
	s := labelSizeByKey("62x29")
	l := defaultLabelLayout(s)
	for i := range l.Elements {
		if l.Elements[i].Key == "mf" {
			l.Elements[i].Prefix = "Herst.: "
		}
	}
	it := testLabel
	it.Manufacturer = "SKF"
	// Browser
	parts := l.parts(it, "/logo.png")
	var html strings.Builder
	for _, p := range parts {
		html.WriteString(string(p.Style) + p.Text + "|")
	}
	if !strings.Contains(html.String(), "Herst.: SKF") || !strings.Contains(html.String(), "left:") {
		t.Errorf("Browser-Teile: %s", html.String())
	}
	// Zebra
	z := zebraSettingsFrom(map[string]string{"dpi": "203", "label_width_mm": "62", "label_height_mm": "29"})
	zpl := zplLayoutLabel(&l, it, s, z, 2)
	if !strings.Contains(zpl, "^BQN") || !strings.Contains(zpl, "Herst.: SKF") || !strings.HasSuffix(zpl, "^XZ\n") {
		t.Errorf("ZPL: %s", zpl)
	}
	// Dymo
	xml := dymoLayoutLabel(&l, it, s, dymoLabelTypeByKey("11354"), "")
	if !strings.Contains(xml, "Herst.: SKF") || !strings.Contains(xml, "<Type>QRCode</Type>") || !strings.HasSuffix(xml, "</DieCutLabel>") {
		t.Errorf("Dymo: %s", xml)
	}
	// PDF
	pdf, err := labelsPDF([]labelItem{it}, s, 1, 0, true, &l)
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Errorf("PDF mit Layout: %v", err)
	}
}

func TestLabelDesignerAndLabelsPageRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	s := labelSizeByKey("62x29")
	d := LabelDesignerData{Size: s, Sizes: labelSizes, Layout: defaultLabelLayout(s), Fields: labelFields, Sample: map[string]string{"pn": "4711-A"}}
	out := renderPage(t, tmpl, "label_designer", d)
	for _, want := range []string{"Etiketten-Designer", "ld-canvas", "/inventory/labels/designer", `"key":"pn"`, "Speichern"} {
		if !strings.Contains(out, want) {
			t.Errorf("Designer enthält %q nicht", want)
		}
	}

	c, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "labels.gohtml")); err != nil {
		t.Fatal(err)
	}
	l := defaultLabelLayout(s)
	it := testLabel
	it.Parts = l.parts(it, "")
	data := LabelsPageData{Size: s, Sizes: labelSizes, Labels: []labelItem{it}, Pages: [][]*labelItem{{&it}}, Copies: 1, Layout: &l, CanDesign: true,
		Tree: []storageTreeOption{{ID: "n1", Name: "Halle 1", Type: "Lager", Path: "Halle 1", Parts: 12, Selected: true}, {ID: "n2", Name: "Regal A", Type: "Regal", Path: "Halle 1 › Regal A", Depth: 1, Parts: 7}}}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "labels-page", data); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	for _, want := range []string{`class="label lx"`, "lx-qr", "4711-A", `name="node" value="n1" checked`, "Regal A", "eigenes Layout", "/inventory/labels/designer?size=62x29", "padding-left:20px"} {
		if !strings.Contains(page, want) {
			t.Errorf("Etikettenseite enthält %q nicht", want)
		}
	}
	// ohne Auswahl: Hinweis statt Fehler
	data = LabelsPageData{Size: s, Sizes: labelSizes, Copies: 1, NeedSelect: true}
	buf.Reset()
	if err := c.ExecuteTemplate(&buf, "labels-page", data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Lagerort wählen") || !strings.Contains(buf.String(), "<details class=\"lsel\" open>") {
		t.Error("ohne Auswahl soll die Lagerort-Auswahl offen sein")
	}
}
