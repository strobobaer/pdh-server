package web

import (
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"pdh"
	"pdh/internal/web/tablericons"
)

var (
	iconRuleRe  = regexp.MustCompile(`(?m)^\.(ti-[a-z0-9-]+)\{--ti:`)
	iconWhereRe = regexp.MustCompile(`(?m)^:where\(\.(ti-[a-z0-9-]+)\)\{--ti:url\(/vendor/9\.9\.9/icons/(ti-[a-z0-9-]+)\.svg\)\}$`)
)

// Hauptdatei: genau die eingebetteten Namen als Daten-Regel, keine Adress-Regel.
// Rest-Datei: fuer jeden der Namen genau eine Adress-Regel mit :where (Spezifitaet 0).
func TestIconCSSSplitCoversEveryName(t *testing.T) {
	set := tablericons.Default()
	used := usedIconNames()
	mainCSS := string(buildIconCSS(set, used, "9.9.9"))
	restCSS := string(buildIconRestCSS(set, "9.9.9"))
	emb := map[string]bool{}
	for _, n := range used {
		emb[n] = true
	}
	got := map[string]int{}
	for _, m := range iconRuleRe.FindAllStringSubmatch(mainCSS, -1) {
		got[m[1]]++
		if !emb[m[1]] {
			t.Errorf("Hauptdatei: %s ist nicht eingebettet", m[1])
		}
	}
	for _, n := range used {
		if got[n] != 1 {
			t.Errorf("Hauptdatei: %s %d Regeln, erwartet 1", n, got[n])
		}
	}
	if strings.Contains(mainCSS, "/icons/ti-") || strings.Contains(mainCSS, ":where(") {
		t.Error("Hauptdatei enthaelt Adress-Regeln")
	}
	if strings.Contains(restCSS, "data:image") || strings.Contains(restCSS, ".ti::before") {
		t.Error("Rest-Datei enthaelt Grundregeln oder eingebettete Symbole")
	}
	rest := map[string]int{}
	for _, m := range iconWhereRe.FindAllStringSubmatch(restCSS, -1) {
		if m[1] != m[2] {
			t.Errorf("Rest-Datei: Regel %s zeigt auf %s", m[1], m[2])
		}
		rest[m[1]]++
	}
	if len(set.Names()) < 5000 {
		t.Fatalf("nur %d Symbole", len(set.Names()))
	}
	for _, n := range set.Names() {
		if rest[n] != 1 {
			t.Errorf("Rest-Datei: %s %d Regeln, erwartet 1", n, rest[n])
		}
	}
	if len(rest) != len(set.Names()) {
		t.Errorf("Rest-Datei: %d Namen, erwartet %d", len(rest), len(set.Names()))
	}
}

// Vorrang in CSS-Begriffen: jede Regel der Rest-Datei steht in :where( – damit
// hat sie Spezifitaet 0 und die eingebettete Regel .ti-x (0,1,0) gewinnt immer.
func TestIconRestRulesHaveZeroSpecificity(t *testing.T) {
	for i, ln := range strings.Split(strings.TrimSpace(string(buildIconRestCSS(tablericons.Default(), "9.9.9"))), "\n") {
		if !strings.HasPrefix(ln, ":where(.ti-") {
			t.Fatalf("Zeile %d nicht in :where(…): %q", i+1, ln)
		}
	}
}

func TestIconRestRoute(t *testing.T) {
	resetIconCSSCache()
	r := iconTestRouter()
	v := "/vendor/" + pdh.Version()
	href := iconCSSRestHref()
	if href != v+"/icons-rest.css" {
		t.Fatalf("iconCSSRestHref = %s", href)
	}
	rec := iconGet(r, href, map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") == "" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("Rest: %d %v", rec.Code, rec.Header())
	}
	raw := iconGet(r, href, nil).Body.String()
	if !strings.Contains(raw, ":where(.ti-") || strings.Contains(raw, ".ti::before") {
		t.Error("Rest-Route liefert nicht die Rest-Datei")
	}
	_, key := (&Handler{}).iconCSS(t.Context())
	mainBody := iconGet(r, v+"/icons-"+key+".css", nil).Body.String()
	if !strings.Contains(mainBody, ".ti::before") || strings.Contains(mainBody, "/icons/ti-") {
		t.Error("Haupt-Route liefert nicht die Hauptdatei")
	}
	if rec2 := iconGet(r, href, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": rec.Header().Get("ETag")}); rec2.Code != http.StatusNotModified {
		t.Errorf("ETag: %d", rec2.Code)
	}
	// fremde Version: Weiterleitung auf die aktuelle Rest-Adresse, nie zwischenspeichern
	rec = iconGet(r, "/vendor/0.0.1/icons-rest.css", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Location") != href {
		t.Errorf("fremde Version: %d %q %q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Location"))
	}
	// die alte Adresse mit Kennung gibt es nicht mehr als Rest-Datei
	rec = iconGet(r, v+"/icons-rest-0123456789.css", nil)
	if strings.Contains(rec.Body.String(), ":where(") {
		t.Error("icons-rest-<key>.css liefert noch die Rest-Datei")
	}
}

func TestIconCSSLinksFunc(t *testing.T) {
	f, ok := TemplateFuncs()["iconCSSLinks"].(func() template.HTML)
	if !ok {
		t.Fatal("iconCSSLinks fehlt in TemplateFuncs")
	}
	out := string(f())
	for _, want := range []string{
		`<link rel="stylesheet" href="` + iconCSSHref() + `">`,
		`<script>`, `readyState`, `"load"`, `"` + iconCSSRestHref() + `"`,
		`<noscript><link rel="stylesheet" href="` + iconCSSRestHref() + `"></noscript>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("iconCSSLinks ohne %q:\n%s", want, out)
		}
	}
	if strings.Index(out, `href="`+iconCSSHref()) > strings.Index(out, "<script>") {
		t.Error("Haupt-Link muss vor dem Skript stehen")
	}
}
