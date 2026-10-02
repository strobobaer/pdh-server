package web

import (
	"strings"
	"testing"
)

// Ausschnitt einer DuckDuckGo-Ergebnisseite (HTML-Version)
const ddgSample = `
<h2 class="result__title">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fftp.festo.com%2Fpublic%2FDataSheet%2F1376426.pdf&amp;rut=abc"><span class="result__type">PDF</span> ISO cylinder DSBC-32-100-PPVA-N3 - <b>Festo</b></a>
</h2>
<h2 class="result__title">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.festo.com%2Fde%2Fde%2Fa%2F1376426%2F&amp;rut=def">Produktseite DSBC</a>
</h2>
<h2 class="result__title">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.motionworld.com%2Fassets%2FFesto-DSBC-Datasheet.PDF&amp;rut=ghi">Festo DSBC &amp; Zubehör</a>
</h2>
<h2 class="result__title">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fftp.festo.com%2Fpublic%2FDataSheet%2F1376426.pdf&amp;rut=jkl"><span class="result__type">PDF</span> doppelt</a>
</h2>`

func TestParseDDGResults(t *testing.T) {
	hits := parseDDGResults(ddgSample, "datasheet")
	if len(hits) != 2 {
		t.Fatalf("2 PDF-Treffer erwartet (ohne HTML-Seite und Dublette), bekommen %d: %+v", len(hits), hits)
	}
	if hits[0].URL != "https://ftp.festo.com/public/DataSheet/1376426.pdf" || hits[0].Title != "ISO cylinder DSBC-32-100-PPVA-N3 - Festo" || hits[0].Source != "ftp.festo.com" || hits[0].Kind != "datasheet" {
		t.Errorf("erster Treffer falsch: %+v", hits[0])
	}
	if hits[1].Title != "Festo DSBC & Zubehör" || hits[1].Source != "motionworld.com" {
		t.Errorf("zweiter Treffer falsch: %+v", hits[1])
	}
}

func TestPartDocHelpers(t *testing.T) {
	if !isPDF([]byte("%PDF-1.7\n...")) || !isPDF([]byte("\xef\xbb\xbf%PDF-1.4")) || isPDF([]byte("<!DOCTYPE html>")) {
		t.Error("PDF-Erkennung falsch")
	}
	for _, c := range []struct{ base, title, kind, want string }{
		{"1376426.pdf", "egal", "datasheet", "1376426.pdf"},
		{"Betriebsanleitung%20DRN.PDF", "", "manual", "Betriebsanleitung DRN.PDF"},
		{"download", "SEW <DRN80> Handbuch", "manual", "SEW _DRN80_ Handbuch.pdf"},
		{"", "", "datasheet", "Datenblatt.pdf"},
	} {
		if got := partDocFilename(c.base, c.title, c.kind); got != c.want {
			t.Errorf("partDocFilename(%q, %q) = %q, erwartet %q", c.base, c.title, got, c.want)
		}
	}
	if q := partDocDefaultQuery(PartMaster{ManufacturerName: "Festo", ManufacturerPart: "DSBC-32", Name: "Zylinder"}); q != "Festo DSBC-32" {
		t.Errorf("Suchbegriff: %q", q)
	}
	if q := partDocDefaultQuery(PartMaster{ManufacturerLegacy: "SKF", Name: "Rillenkugellager 6205"}); q != "SKF Rillenkugellager 6205" {
		t.Errorf("Suchbegriff ohne Hersteller-Teilenummer: %q", q)
	}
}

// Seite: Knopf, Dialog und Skripte (gueltiges JavaScript); ohne Bearbeitungsrecht keine Suche.
func TestPartDocumentsUI(t *testing.T) {
	tmpl := loadTestTemplates(t)
	p := PartMaster{ID: "s1", Name: "Zylinder", ManufacturerName: "Festo", ManufacturerPart: "DSBC-32", Unit: "Stk", StatusKey: "ok", StatusClass: "b-green", StatusLabel: "OK"}
	page := renderPage(t, tmpl, "inventory_detail", PartDetailData{Part: p, CanEdit: true, Tab: "docs"})
	for _, want := range []string{"Datenblatt &amp; Handbuch suchen", `id="pd-modal"`, `value="Festo DSBC-32"`, "/doc-search?q=", "pdhRefreshDoc('s1','all',this)", "function pdhRefreshDoc", "attachment-badge doc"} {
		if !strings.Contains(page, want) {
			t.Errorf("Teileseite enthält %q nicht", want)
		}
	}
	checkScripts(t, "teil-dokumente", page)
	ro := renderPage(t, tmpl, "inventory_detail", PartDetailData{Part: p, Tab: "docs"})
	if strings.Contains(ro, `id="pd-modal"`) || strings.Contains(ro, "openDocPicker()") {
		t.Error("ohne Bearbeitungsrecht ist die Dokumentsuche sichtbar")
	}
}

// Seitenleisten in der Breite ziehbar: Griffe, Variablen, frueh geladene Breite.
func TestSidebarResizers(t *testing.T) {
	page := renderPage(t, loadTestTemplates(t), "tickets", TicketsPageData{})
	for _, want := range []string{`class="pdh-resizer left"`, `class="pdh-resizer right"`, `role="separator"`, "--left-open-w", "pdh_sidebar_left_w", "pdh_sidebar_right_w", "max(380px, var(--right-open-w))"} {
		if !strings.Contains(page, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	checkScripts(t, "resizer", page)
}
