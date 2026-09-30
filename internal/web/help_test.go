package web

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Wird eine neue Seite (BaseData.Page) eingefuehrt, muss das Handbuch ein
// Kapitel mit passendem data-pages-Eintrag bekommen - sonst schlaegt dieser
// Test fehl. So bleibt das Handbuch bei Aenderungen vollstaendig.
func TestHelpCoversAllPages(t *testing.T) {
	pageRe := regexp.MustCompile(`baseData\(r, "([a-z-]+)"`)
	pages := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pageRe.FindAllStringSubmatch(string(src), -1) {
			pages[m[1]] = true
		}
	}
	delete(pages, "help")
	if len(pages) < 10 {
		t.Fatalf("zu wenige Seiten gefunden (%d) - Regex veraltet?", len(pages))
	}
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "help.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, m := range regexp.MustCompile(`data-pages="([^"]*)"`).FindAllStringSubmatch(string(tmpl), -1) {
		for _, p := range strings.Fields(m[1]) {
			covered[p] = true
		}
	}
	var missing []string
	for p := range pages {
		if !covered[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("Handbuch: kein Kapitel für die Seite(n) %v - in web/templates/help.gohtml ergänzen (data-pages)", missing)
	}
}

func TestHelpChaptersMatch(t *testing.T) {
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "help.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range regexp.MustCompile(`<section class="hb-ch" id="([a-z]+)"`).FindAllStringSubmatch(string(tmpl), -1) {
		ids = append(ids, m[1])
	}
	if strings.Join(ids, ",") != strings.Join(helpChapterIDs, ",") {
		t.Errorf("Kapitel im Template %v ≠ helpChapterIDs %v", ids, helpChapterIDs)
	}
	for _, id := range helpChapterIDs {
		if !strings.Contains(string(tmpl), `(index $s "`+id+`")`) {
			t.Errorf("Kapitel %s ohne Bildschirmfoto-Bereich", id)
		}
	}
	if a, b := helpChapterRef("faults"), helpChapterRef("faults"); a != b || len(a) != 36 || helpChapterRef("tickets") == a {
		t.Errorf("helpChapterRef nicht stabil/eindeutig: %s", a)
	}
}

func TestHelpPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	shots := map[string]string{}
	for _, id := range helpChapterIDs {
		shots[id] = helpChapterRef(id)
	}
	qr, _ := qrSVG("https://pdh/inventory/beispiel")
	d := HelpPageData{
		FieldModules: fieldModules, LabelSizes: labelSizes, FieldTypes: fieldTypeOrder, TypeLabels: fieldTypeLabels,
		Shots: shots, CanShots: true, SampleQR: qr, Version: "abc1234",
		PermGroups: []helpPermGroup{{Category: "Chat", Items: []helpPerm{{"chat.use", "Chat & Teams nutzen"}}}},
	}
	out := renderPage(t, tmpl, "help", d)
	for _, want := range []string{"PDH-Handbuch", "Stand Version abc1234", `id="faults"`, "chat.use", labelSizes[0].Label,
		"<svg", "hb-arrow", "Bildschirmfoto zu diesem Kapitel", `data-attach-ref="help:` + shots["faults"] + `"`, "4711-A"} {
		if !strings.Contains(out, want) {
			t.Errorf("Handbuch enthält %q nicht", want)
		}
	}
	// Kontexthilfe (Fragment) ohne Upload-Recht: keine Upload-Knoepfe
	c, _ := tmpl.Clone()
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "help.gohtml")); err != nil {
		t.Fatal(err)
	}
	d.CanShots = false
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "help-chapters", d); err != nil {
		t.Fatalf("help-chapters: %v", err)
	}
	if strings.Contains(buf.String(), "Bildschirmfoto zu diesem Kapitel") {
		t.Error("Upload ohne Berechtigung angeboten")
	}
}
