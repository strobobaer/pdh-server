package web

import (
	"strings"
	"testing"
)

func TestMSMarkdown(t *testing.T) {
	src := "## 1 Zweck\nDieser **Prozess** regelt\nzwei Zeilen.\n\n- eins\n- zwei\n\n1. Schritt\n2. Schritt\n\n| Rolle | Aufgabe |\n|---|---|\n| QMB | `prüfen` |\n\n[ISO](https://iso.org) [intern](/ms) [böse](javascript:alert(1))\n<script>alert(1)</script>"
	out := string(msMarkdown(src))
	for _, want := range []string{"<h3>1 Zweck</h3>", "<b>Prozess</b>", "regelt<br>zwei Zeilen.", "<ul><li>eins</li><li>zwei</li></ul>",
		"<ol><li>Schritt</li><li>Schritt</li></ol>", "<th>Rolle</th>", "<td><code>prüfen</code></td>", `<a href="https://iso.org" target="_blank" rel="noopener">ISO</a>`,
		`<a href="/ms">intern</a>`, "&lt;script&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("Markdown enthält %q nicht:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, `href="javascript`) {
		t.Errorf("unsicheres HTML durchgelassen: %s", out)
	}
	if strings.Contains(out, "---") {
		t.Error("Trennzeile der Tabelle darf nicht erscheinen")
	}
}

func TestMSTypesAndTemplates(t *testing.T) {
	seen := map[string]bool{}
	for _, ty := range msTypes {
		if seen[ty.Abbr] {
			t.Errorf("Kürzel %s doppelt", ty.Abbr)
		}
		seen[ty.Abbr] = true
		if ty.Key != "other" && msTemplates[ty.Key] == "" {
			t.Errorf("%s: keine Gliederung", ty.Key)
		}
	}
	if _, ok := msType("process"); !ok {
		t.Error("Prozessbeschreibung fehlt")
	}
}

func TestMSPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	doc := msDocRow{ID: "d1", No: "QM-PB-001", Title: "Lenkung dokumentierter Information", Type: "process", TypeLabel: "Prozessbeschreibung", Icon: "ti-route",
		SystemCode: "QM", SystemName: "Qualitätsmanagement", SystemColor: "#3B82F6", Version: 2, Released: "01.10.2026", Open: "review", OpenLabel: "in Prüfung", OpenClass: "b-amber",
		ReadPending: true, NextReview: "01.10.2027"}
	list := renderPage(t, tmpl, "ms_list", MSListData{Docs: []msDocRow{doc}, Types: msTypes, CanEdit: true, CanApprove: true,
		Systems: []msSystem{{ID: "s1", Code: "QM", Name: "Qualitätsmanagement", Active: true}}, CountReleased: 1, CountRead: 1})
	for _, want := range []string{"QM-PB-001", `href="/ms/d1"`, "in Prüfung", "Neues Dokument", `action="/ms"`, "lesen"} {
		if !strings.Contains(list, want) {
			t.Errorf("Liste enthält %q nicht", want)
		}
	}
	cur := &msVersion{ID: "v2", Version: 2, Status: "released", Title: doc.Title, HTML: msMarkdown("## Zweck\nText"), Released: "01.10.2026"}
	open := &msVersion{ID: "v3", Version: 3, Status: "review", StatusLabel: "in Prüfung", Title: doc.Title, Content: "## Zweck", ChangeNote: "ergänzt", AuthorID: "me", HTML: msMarkdown("## Zweck")}
	d := MSDocData{Doc: doc, DocTypeKey: "process", Current: cur, OpenV: open, Versions: []msVersion{*open, *cur}, ReadScope: "all", NeedRead: true,
		Reads: []msReadRow{{Name: "Anna", Read: true, ReadAt: "02.10.2026"}}, ReadDone: 1, ReadTotal: 3, CanEdit: true, CanApprove: true, OwnVersion: true,
		Types: msTypes, RelatedIDs: map[string]bool{}, Tab: "content"}
	page := renderPage(t, tmpl, "ms_doc", d)
	checkTabs(t, "ms_doc", page, "content", "edit", "versions", "reads", "files", "meta")
	for _, want := range []string{"/ms/d1/read", "Gelesen und verstanden", "<h3>Zweck</h3>", "/ms/d1/approve", "Vier-Augen-Prinzip", "disabled", "1 von 3", `data-attach-ref="ms_document:d1"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Dokumentseite enthält %q nicht", want)
		}
	}
	// Entwurf: Editor mit Vorschau
	open.Status = "draft"
	d.OpenV, d.OwnVersion = open, false
	page = renderPage(t, tmpl, "ms_doc", d)
	for _, want := range []string{`id="ms-content"`, `id="ms-preview"`, "/ms/d1/submit", "/ms/d1/save", "/ms/d1/discard"} {
		if !strings.Contains(page, want) {
			t.Errorf("Editor enthält %q nicht", want)
		}
	}
}
