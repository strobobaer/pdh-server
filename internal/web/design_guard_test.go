package web

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Gestaltungsgrundlage (0.90.0 Teil 1): Werte stehen in web/static/ui/tokens.css,
// feste Farbwerte in Vorlagen und Stildateien werden nur noch weniger.

var cssVarDefRe = regexp.MustCompile(`(?:^|[;{\s])(--[A-Za-z0-9-]+)\s*:`)

func uiFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "ui", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r", "")
}

func cssVarDefs(css string) map[string]bool {
	out := map[string]bool{}
	for _, m := range cssVarDefRe.FindAllStringSubmatch(css, -1) {
		out[m[1]] = true
	}
	return out
}

func TestTokensComplete(t *testing.T) {
	tokens, main := cssVarDefs(uiFile(t, "tokens.css")), cssVarDefs(uiFile(t, "pdh.css"))
	for _, v := range []string{"--bg", "--bg2", "--bg3", "--text", "--muted", "--border", "--accent", "--shadow-card",
		"--stroke", "--fill-hover", "--font", "--sp-1", "--sp-2", "--sp-3", "--sp-4", "--sp-6",
		"--fs-xs", "--fs-sm", "--fs-md", "--fs-lg", "--fs-xl", "--r-s", "--r-m", "--shadow-float",
		"--ok", "--warn", "--err", "--info", "--ok-soft", "--warn-soft", "--err-soft", "--info-soft"} {
		if !tokens[v] {
			t.Errorf("tokens.css definiert %s nicht", v)
		}
	}
	for v := range tokens {
		if main[v] {
			t.Errorf("pdh.css definiert %s, das zu tokens.css gehört", v)
		}
	}
	out := renderBaseForTest(t)
	i, j := strings.Index(out, `href="`+uiURL("tokens.css")+`"`), strings.Index(out, `href="`+uiURL("pdh.css")+`"`)
	if i < 0 || j < 0 || i > j {
		t.Errorf("tokens.css muss vor pdh.css eingebunden sein (%d, %d)", i, j)
	}
}

// colorLiteralRe: #rgb, #rgba, #rrggbb, #rrggbbaa sowie rgb(, rgba(, hsl(, hsla(.
var colorLiteralRe = regexp.MustCompile(`#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})\b|\b(?:rgba?|hsla?)\(`)

// colorExempt: Vorlagen, die feste Farben brauchen – E-Mails (Mailprogramme kennen
// keine Variablen), Druckblätter und die Skizzen im Handbuch.
var colorExempt = map[string]bool{
	"web/templates/widgets/purchase_report_mail.gohtml": true,
	"web/templates/kanban_print.gohtml":                 true,
	"web/templates/labels.gohtml":                       true,
	"web/templates/widgets/kvp.gohtml":                  true,
	"web/templates/widgets/help_figures.gohtml":         true,
}

const colorBaselineFile = "testdata/color_literals.txt"

func countColorLiterals(t *testing.T) map[string]int {
	t.Helper()
	root := filepath.Join("..", "..")
	var files []string
	for _, pat := range []string{"web/templates/*.gohtml", "web/templates/widgets/*.gohtml", "web/static/ui/*.css"} {
		m, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pat)))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	out := map[string]int{}
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if colorExempt[rel] || rel == "web/static/ui/tokens.css" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = len(colorLiteralRe.FindAllIndex(b, -1))
	}
	return out
}

func TestColorLiteralsRatchet(t *testing.T) {
	got := countColorLiterals(t)
	if os.Getenv("PDH_COLOR_BASELINE_UPDATE") == "1" {
		var lines []string
		for f, n := range got {
			if n > 0 {
				lines = append(lines, fmt.Sprintf("%s %d", f, n))
			}
		}
		sort.Strings(lines)
		head := "# Feste Farbwerte je Datei (TestColorLiteralsRatchet). Darf nur sinken; neu schreiben mit\n# PDH_COLOR_BASELINE_UPDATE=1 go test ./internal/web -run TestColorLiteralsRatchet\n"
		if err := os.WriteFile(colorBaselineFile, []byte(head+strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(colorBaselineFile)
	if err != nil {
		t.Fatalf("Ausgangswerte fehlen (%v) – mit PDH_COLOR_BASELINE_UPDATE=1 anlegen", err)
	}
	base := map[string]int{}
	for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f, n, ok := strings.Cut(l, " ")
		v, err := strconv.Atoi(n)
		if !ok || err != nil {
			t.Fatalf("Zeile %q unlesbar", l)
		}
		base[f] = v
	}
	for f, n := range got {
		switch b := base[f]; {
		case n > b:
			t.Errorf("%s: %d feste Farbwerte, erlaubt %d – Werte aus web/static/ui/tokens.css nehmen", f, n, b)
		case n < b:
			t.Errorf("%s: nur noch %d feste Farbwerte (vorher %d) – Ausgangswert senken: PDH_COLOR_BASELINE_UPDATE=1", f, n, b)
		}
	}
	for f := range base {
		if _, ok := got[f]; !ok {
			t.Errorf("%s steht in %s, gibt es aber nicht mehr – Ausgangswerte neu schreiben", f, colorBaselineFile)
		}
	}
}
