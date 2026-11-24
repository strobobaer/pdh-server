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

var iconRuleRe = regexp.MustCompile(`(?m)^\.(ti-[a-z0-9-]+)\{--ti:`)

// Hauptdatei und Rest-Datei zusammen: genau eine Regel je Name; die Hauptdatei
// hat keine Adress-Regel, die Rest-Datei keinen eingebetteten Namen.
func TestIconCSSSplitCoversEveryName(t *testing.T) {
	set := tablericons.Default()
	used := usedIconNames()
	main, rest := buildIconCSS(set, used, "9.9.9")
	emb := map[string]bool{}
	for _, n := range used {
		emb[n] = true
	}
	count := map[string]int{}
	for _, m := range iconRuleRe.FindAllStringSubmatch(string(main), -1) {
		count[m[1]]++
		if !emb[m[1]] {
			t.Errorf("Hauptdatei: %s ist nicht eingebettet", m[1])
		}
	}
	if strings.Contains(string(main), "/icons/ti-") {
		t.Error("Hauptdatei enthaelt eine Adress-Regel")
	}
	for _, m := range iconRuleRe.FindAllStringSubmatch(string(rest), -1) {
		count[m[1]]++
		if emb[m[1]] {
			t.Errorf("Rest-Datei: %s ist eingebettet", m[1])
		}
	}
	if strings.Contains(string(rest), "data:image") || strings.Contains(string(rest), ".ti::before") {
		t.Error("Rest-Datei enthaelt Grundregeln oder eingebettete Symbole")
	}
	for _, n := range set.Names() {
		if count[n] != 1 {
			t.Errorf("%s: %d Regeln, erwartet 1", n, count[n])
		}
	}
	if !strings.Contains(string(rest), "\n.ti-ad-2{--ti:url(/vendor/9.9.9/icons/ti-ad-2.svg)}") && !strings.HasPrefix(string(rest), ".ti-ad-2{") {
		t.Error("ti-ad-2 soll in der Rest-Datei stehen")
	}
}

func TestIconRestRoute(t *testing.T) {
	resetIconCSSCache()
	r := iconTestRouter()
	v := "/vendor/" + pdh.Version()
	_, key := (&Handler{}).iconCSS(t.Context())
	href := iconCSSRestHref()
	if href != v+"/icons-rest-"+key+".css" {
		t.Fatalf("iconCSSRestHref = %s", href)
	}
	rec := iconGet(r, href, map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") == "" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("Rest: %d %v", rec.Code, rec.Header())
	}
	raw := iconGet(r, href, nil).Body.String()
	if !strings.Contains(raw, "/icons/ti-") || strings.Contains(raw, ".ti::before") {
		t.Error("Rest-Route liefert nicht die Rest-Datei")
	}
	main := iconGet(r, v+"/icons-"+key+".css", nil).Body.String()
	if !strings.Contains(main, ".ti::before") || strings.Contains(main, "/icons/ti-") {
		t.Error("Haupt-Route liefert nicht die Hauptdatei")
	}
	if rec2 := iconGet(r, href, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": rec.Header().Get("ETag")}); rec2.Code != http.StatusNotModified {
		t.Errorf("ETag: %d", rec2.Code)
	}
	// fremde Kennung der laufenden Version: aktuelle Datei, nicht zwischenspeichern
	rec = iconGet(r, v+"/icons-rest-0000000000.css", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Body.String() != raw {
		t.Errorf("fremde Kennung: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	// fremde Version: Weiterleitung auf die aktuelle Rest-Adresse
	rec = iconGet(r, "/vendor/0.0.1/icons-rest-0000000000.css", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Location") != href {
		t.Errorf("fremde Version: %d %q %q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Location"))
	}
	// keine gueltige Kennung: 404
	for _, p := range []string{v + "/icons-rest-xyz.css", v + "/icons-rest-.css"} {
		rec = iconGet(r, p, nil)
		if rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
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
		`<script>`, `readyState`, `"load"`, iconCSSRestHref(),
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
