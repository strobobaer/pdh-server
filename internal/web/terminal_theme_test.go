package web

// Helles Farbschema der Terminalseiten: farbige Kacheln haben in web/templates/terminal.gohtml je Kachelart
// Variablen (--tile-<art>-bg/-bd/-fg) – dunkel eine feste dunkle Fläche mit heller Schrift, hell eine helle Fläche
// derselben Farbfamilie mit dunkler Schrift (Kontrast mindestens 4,5:1, WCAG AA). Die Regeln der Kacheln benutzen nur
// die Variablen; eine feste dunkle Fläche mit weißer Schrift ohne ausreichenden Kontrast darf es nirgends geben.

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func cssLuminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	ch := func(i int) float64 {
		n, _ := strconv.ParseUint(hex[i:i+2], 16, 8)
		c := float64(n) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(0) + 0.7152*ch(2) + 0.0722*ch(4)
}

func cssContrast(a, b string) float64 {
	la, lb := cssLuminance(a), cssLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

var (
	cssNoComments = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssBlockRe    = regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
	cssTileVar    = regexp.MustCompile(`--tile-([a-z]+)-(bg|bd|fg):(#[0-9a-fA-F]{3,6}|var\(--[a-z-]+\))`)
	tileHexRe     = regexp.MustCompile(`^#[0-9a-fA-F]{3}([0-9a-fA-F]{3})?$`)
	cssBgHexRe    = regexp.MustCompile(`(?:^|;)\s*background:(#[0-9a-fA-F]{3,6})\s*(?:;|$)`)
	cssFgWhiteR   = regexp.MustCompile(`(?:^|;)\s*color:(#fff|#ffffff)\s*(?:;|$)`)
)

func TestTerminalTileThemes(t *testing.T) {
	raw, err := os.ReadFile("../../web/templates/terminal.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	css := cssNoComments.ReplaceAllString(strings.ReplaceAll(string(raw), "\r\n", "\n"), "")
	dark, light := map[string]map[string]string{}, map[string]map[string]string{}
	for _, m := range cssBlockRe.FindAllStringSubmatch(css, -1) {
		sel := strings.TrimSpace(m[1])
		var into map[string]map[string]string
		switch {
		case strings.HasSuffix(sel, ":root"):
			into = dark
		case strings.HasSuffix(sel, `html[data-theme="light"]`):
			into = light
		default:
			continue
		}
		for _, v := range cssTileVar.FindAllStringSubmatch(m[2], -1) {
			if into[v[1]] == nil {
				into[v[1]] = map[string]string{}
			}
			into[v[1]][v[2]] = v[3]
		}
	}
	// jede Kachelart in beiden Farbschemata vollständig
	kinds := []string{"fault", "ticket", "rz", "v", "hmi", "m", "p", "z", "yr"}
	for _, k := range kinds {
		for name, set := range map[string]map[string]map[string]string{"dunkel": dark, "hell": light} {
			for _, part := range []string{"bg", "bd", "fg"} {
				if set[k][part] == "" {
					t.Errorf("Kachelart %q (%s): --tile-%s-%s fehlt", k, name, k, part)
				}
			}
		}
		bg, fg := light[k]["bg"], light[k]["fg"]
		if !tileHexRe.MatchString(bg) || !tileHexRe.MatchString(fg) {
			t.Errorf("Kachelart %q hell: Fläche und Schrift müssen feste Farben sein (%q, %q)", k, bg, fg)
			continue
		}
		if c := cssContrast(bg, fg); c < 4.5 {
			t.Errorf("Kachelart %q hell: Kontrast %.2f:1 (%s auf %s), erwartet mindestens 4,5:1", k, c, fg, bg)
		}
		// hell ist wirklich hell, dunkel bleibt dunkel (außer der Balken des Jahresbands, der mittelgrau ist)
		if cssLuminance(bg) < 0.5 {
			t.Errorf("Kachelart %q hell: Fläche %s ist nicht hell", k, bg)
		}
		if d := dark[k]["bg"]; k != "yr" && tileHexRe.MatchString(d) && cssLuminance(d) > 0.1 {
			t.Errorf("Kachelart %q dunkel: Fläche %s ist nicht dunkel", k, d)
		}
	}
	// die Regeln benutzen die Variablen (nie feste Farben)
	rules := map[string]string{
		".t-tilebtn.fault": "fault", ".t-tilebtn.ticket": "ticket", ".t-new .t-rzlink": "rz", ".t-new .t-vlink": "v",
		".t-new .t-hmilink": "hmi", ".t-new .t-mlink": "m", ".t-new .t-plink": "p", ".t-new .t-zlink": "z", ".g-yr-bar": "yr",
	}
	seen := map[string]bool{}
	for _, m := range cssBlockRe.FindAllStringSubmatch(css, -1) {
		sel := strings.TrimSpace(m[1])
		k, ok := rules[sel]
		if !ok {
			continue
		}
		seen[sel] = true
		if !strings.Contains(m[2], "background:var(--tile-"+k+"-bg)") || !strings.Contains(m[2], "color:var(--tile-"+k+"-fg)") {
			t.Errorf("Regel %s benutzt die Variablen --tile-%s-… nicht: %s", sel, k, m[2])
		}
		if strings.Contains(m[2], "#") {
			t.Errorf("Regel %s enthält eine feste Farbe: %s", sel, m[2])
		}
	}
	for sel := range rules {
		if !seen[sel] {
			t.Errorf("Regel %s nicht gefunden", sel)
		}
	}
	// nirgends eine feste Fläche mit weißer Schrift, die dafür zu hell ist (dunkle Flächen gehören in die Kachelvariablen)
	for _, f := range []string{"terminal.gohtml", "terminal_vorgang.gohtml"} {
		b, err := os.ReadFile("../../web/templates/" + f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range cssBlockRe.FindAllStringSubmatch(cssNoComments.ReplaceAllString(strings.ReplaceAll(string(b), "\r\n", "\n"), ""), -1) {
			bg := cssBgHexRe.FindStringSubmatch(m[2])
			if bg == nil || !cssFgWhiteR.MatchString(m[2]) {
				continue
			}
			if c := cssContrast(bg[1], "#ffffff"); c < 4.5 {
				t.Errorf("%s: %s hat Fläche %s mit weißer Schrift (Kontrast %.2f:1)", f, strings.TrimSpace(m[1]), bg[1], c)
			}
		}
	}
	// der grüne Knopf bleibt im hellen Schema lesbar
	if c := cssContrast("#047857", "#ffffff"); c < 4.5 {
		t.Errorf("Knopf ok hell: %.2f:1", c)
	}
	if !strings.Contains(css, `html[data-theme="light"] .t-btn.ok{background:#047857`) {
		t.Error("helle Regel für .t-btn.ok fehlt")
	}
}
