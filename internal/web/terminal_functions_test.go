package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseTerminalFnBody(t *testing.T) {
	ok := `{"functions":{"ticket":{"enabled":false,"updated_at":"2026-11-22T11:00:00.123Z"},"zukunft":{"enabled":"egal"},"hmi":{"enabled":true,"updated_at":"2026-11-22T13:00:00+02:00"}}}`
	in, err := parseTerminalFnBody([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 2 || in["ticket"].Enabled || !in["hmi"].Enabled {
		t.Fatalf("falsch gelesen: %+v", in)
	}
	if !in["hmi"].At.Equal(time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("Offset nicht in UTC umgerechnet: %v", in["hmi"].At)
	}
	if !in["ticket"].At.Equal(time.Date(2026, 11, 22, 11, 0, 0, 123000000, time.UTC)) {
		t.Errorf("Millisekunden: %v", in["ticket"].At)
	}
	for _, bad := range []string{
		``, `[]`, `{}`, `{"functions":null}`, `{"functions":[]}`, `{"functions":"x"}`,
		`{"functions":{"ticket":{"enabled":"ja","updated_at":"2026-11-22T11:00:00Z"}}}`,
		`{"functions":{"ticket":{"enabled":true}}}`,
		`{"functions":{"ticket":{"enabled":true,"updated_at":"gestern"}}}`,
		`{"functions":{"ticket":{"enabled":true,"updated_at":5}}}`,
		`{"functions":{"ticket":null}}`,
	} {
		if _, err := parseTerminalFnBody([]byte(bad)); err == nil {
			t.Errorf("%q hätte abgelehnt werden müssen", bad)
		}
	}
}

func TestTerminalFnJSONDefaults(t *testing.T) {
	m := terminalFnJSON(defaultTerminalFns())
	if len(m) != len(terminalFnDefs) {
		t.Fatalf("alle Schlüssel erwartet: %v", m)
	}
	for _, k := range []string{"stoerung", "ticket", "ruesten", "stillstand", "hmi", "wartungen", "vorgaenge", "schichtplan", "saver_slides"} {
		e, ok := m[k].(map[string]any)
		if !ok {
			t.Fatalf("%s fehlt", k)
		}
		if e["updated_at"] != "1970-01-01T00:00:00.000Z" {
			t.Errorf("%s: nie geändert = Epoche, war %v", k, e["updated_at"])
		}
		if want := k != "saver_slides"; e["enabled"] != want {
			t.Errorf("%s: Grundwert %v", k, e["enabled"])
		}
	}
	if got := fnWire(time.Date(2026, 11, 22, 12, 15, 30, 123456789, time.FixedZone("x", 3600))); got != "2026-11-22T11:15:30.123Z" {
		t.Errorf("Leitungsformat: %s", got)
	}
}

// Die Farben des Vertrags sind die von base.gohtml (dunkel :root, hell html[data-theme="light"]).
func TestTerminalThemeMatchesBase(t *testing.T) {
	b, err := os.ReadFile("../../web/templates/base.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.ReplaceAll(string(b), "\r", "")
	i := strings.Index(base, `html[data-theme="light"]{`)
	if i < 0 {
		t.Fatal("hell-Block fehlt in base.gohtml")
	}
	dark, light := base[:i], base[i:]
	for scheme, src := range map[string]string{"dark": dark, "light": light} {
		for key, tok := range map[string]string{"bg": "--bg:", "bg2": "--bg2:", "bg3": "--bg3:", "text": "--text:", "muted": "--muted:", "border": "--border:"} {
			if want := tok + terminalThemes[scheme][key]; !strings.Contains(src, want) {
				t.Errorf("%s/%s: %q nicht in base.gohtml", scheme, key, want)
			}
		}
	}
	th := terminalThemeJSON("light")
	if th["scheme"] != "light" || th["bg"] != "#f3f3f3" || terminalThemeJSON("")["scheme"] != "dark" {
		t.Errorf("theme: %v", th)
	}
	// Die Terminalseite nutzt dieselben Werte (kein eigener fester Hintergrund mehr).
	tg, err := os.ReadFile("../../web/templates/terminal.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(tg)
	for _, want := range []string{"--bg:#202020", "--bg2:#2b2b2b", `html[data-theme="light"]{--bg:#f3f3f3`, `data-theme="{{if eq .Scheme "light"}}`} {
		if !strings.Contains(s, want) {
			t.Errorf("terminal.gohtml ohne %q", want)
		}
	}
	if strings.Contains(s, "#0f1117") {
		t.Error("terminal.gohtml hat noch den festen dunklen Hintergrund")
	}
}

func TestTerminalClientVersion(t *testing.T) {
	req := func(k, v string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if k != "" {
			r.Header.Set(k, v)
		}
		return r
	}
	for _, c := range []struct{ k, v, want string }{
		{"", "", ""}, {"X-Terminal-Version", "0.4.3", "0.4.3"}, {"User-Agent", "pdh-terminal/0.4.3 (linux)", "0.4.3"},
		{"User-Agent", "Mozilla/5.0", ""}, {"X-Terminal-Version", "a b", ""}, {"X-Terminal-Version", strings.Repeat("9", 50), ""},
	} {
		if got := terminalClientVersion(req(c.k, c.v)); got != c.want {
			t.Errorf("%s=%q: %q, erwartet %q", c.k, c.v, got, c.want)
		}
	}
}
