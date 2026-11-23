package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Folie „Website“ (terminal_slides_store.go, terminal_slides.go, terminal.js).

func TestValidateWebsiteSlide(t *testing.T) {
	long := "https://example.org/" + strings.Repeat("a", 2048)
	cases := []struct {
		name   string
		url    string
		bad    bool
		reload int
	}{
		{"https", "https://example.org/anzeige?x=1#a", false, 0},
		{"http", "http://intranet.local:8080/tafel", false, 15},
		{"javascript", "javascript:alert(1)", true, 0},
		{"data", "data:text/html,<script>1</script>", true, 0},
		{"file", "file:///etc/passwd", true, 0},
		{"ftp", "ftp://example.org/x", true, 0},
		{"relativ", "/grafana/d/x", true, 0},
		{"protokollrelativ", "//example.org/x", true, 0},
		{"zugangsdaten", "https://user:pw@example.org/", true, 0},
		{"nur benutzer", "https://user@example.org/", true, 0},
		{"zu lang", long, true, 0},
		{"leer", "", true, 0},
		{"leerzeichen", "https://example.org/a b", true, 0},
		{"ohne rechner", "https:///x", true, 0},
		{"platzhalter", "https://*.example.org/", true, 0},
		{"strichpunkt im rechner", "https://a;b.example.org/", true, 0},
		{"neuladen 1440", "https://example.org/", false, 1440},
		{"neuladen 1441", "https://example.org/", true, 1441},
		{"neuladen negativ", "https://example.org/", true, -1},
	}
	for _, c := range cases {
		s := terminalSlide{Kind: "website", URL: c.url, ReloadMinutes: c.reload, DurationSeconds: 15}
		err := validateTerminalSlide(&s)
		if (err != nil) != c.bad {
			t.Errorf("%s: Fehler=%v, erwartet Fehler=%v", c.name, err, c.bad)
		}
	}
	// Genau 2048 Zeichen sind erlaubt.
	ok := "https://example.org/" + strings.Repeat("a", 2048-len("https://example.org/"))
	if s := (terminalSlide{Kind: "website", URL: ok, DurationSeconds: 15}); validateTerminalSlide(&s) != nil {
		t.Error("2048 Zeichen abgelehnt")
	}
	// Andere Arten tragen weder Adresse noch Neuladen.
	o := terminalSlide{Kind: "text", Body: "x", URL: "https://x", ReloadMinutes: 5, DurationSeconds: 15}
	if err := validateTerminalSlide(&o); err != nil || o.URL != "" || o.ReloadMinutes != 0 {
		t.Errorf("Textfolie: %+v %v", o, err)
	}
}

func TestWebsiteIsOwnHost(t *testing.T) {
	for _, c := range []struct {
		url, host string
		want      bool
	}{
		{"https://pdh.firma.de/x", "pdh.firma.de", true},
		{"https://PDH.firma.de:8443/x", "pdh.firma.de:443", true},
		{"https://example.org/", "pdh.firma.de", false},
		{"http://localhost:3000/", "localhost:8080", true},
	} {
		if got := websiteIsOwnHost(c.url, c.host); got != c.want {
			t.Errorf("%s gegen %s: %v", c.url, c.host, got)
		}
	}
}

// Die Seiten der Folienschau dürfen die Adressen aktiver Website-Folien einbetten, nur diese.
func TestFrameCSPWebsite(t *testing.T) {
	got := frameCSP([]terminalSlide{
		{Kind: "website", Active: true, URL: "https://tafel.example.org/a?b=c"},
		{Kind: "website", Active: true, URL: "http://intranet:8080/x"},
		{Kind: "website", Active: false, URL: "https://aus.example.org/"},
		{Kind: "website", Active: true, URL: "https://tafel.example.org/zwei"},
		{Kind: "website", Active: true, URL: "javascript:alert(1)"},
		{Kind: "grafana", Active: true, URL: "/grafana/d/x"},
		{Kind: "text", Active: true, URL: "https://text.example.org/"},
	})
	want := "frame-src 'self' http://intranet:8080 https://tafel.example.org"
	if got != want {
		t.Errorf("frameCSP = %q, erwartet %q", got, want)
	}
	if frameCSP(nil) != "frame-src 'self'" {
		t.Error("ohne Folien nur 'self'")
	}
}

// Ohne das Recht antwortet das Anlegen einer Website-Folie 403.
func TestTerminalSlidesWebsitePermRequired(t *testing.T) {
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p != permTerminalSlides }}
	r := slidesRouter(h)
	for _, c := range []struct{ method, path string }{
		{"GET", "/terminal-slides/new?kind=website"}, {"POST", "/terminal-slides"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("kind=website&web_url=https%3A%2F%2Fexample.org%2F")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s ohne Recht: %d", c.method, c.path, rec.Code)
		}
	}
}

func readStatic(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// Der Rahmen (Player und Vorschau) trägt genau die vorgesehenen Einschränkungen.
func TestWebsiteFrameAttributes(t *testing.T) {
	for _, f := range []string{"../../web/static/terminal.js", "../../web/templates/terminal_slide_form.gohtml"} {
		src := readStatic(t, f)
		for _, want := range []string{
			`setAttribute('sandbox', 'allow-scripts allow-same-origin')`,
			`setAttribute('referrerpolicy', 'no-referrer')`,
			`setAttribute('allow', '')`,
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%s enthält %q nicht", f, want)
			}
		}
		for _, bad := range []string{"allow-top-navigation", "allow-popups", "allow-forms", "allow-downloads", "allow-modals", "allow-pointer-lock", "allow-presentation", "allow-orientation-lock"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s enthält verbotenes Token %q", f, bad)
			}
		}
		for _, bad := range []string{"camera", "microphone", "geolocation"} {
			if regexp.MustCompile(`setAttribute\('allow', '[^']*` + bad).MatchString(src) {
				t.Errorf("%s: allow erlaubt %s", f, bad)
			}
		}
	}
	js := readStatic(t, "../../web/static/terminal.js")
	// Der Rahmen entsteht beim Zeigen und verschwindet beim Wechsel (draw leert die Bühne), Berührungen fängt eine Schicht.
	for _, want := range []string{"el('iframe', 'g-frame g-web')", "el('div', 'g-shield')", "stage.textContent = '';", "clearInterval(reloadTimer)"} {
		if !strings.Contains(js, want) {
			t.Errorf("terminal.js enthält %q nicht", want)
		}
	}
	css := readStatic(t, "../../web/templates/terminal.gohtml")
	if !strings.Contains(css, ".g-shield{position:absolute;inset:0") {
		t.Error("Schicht über dem Rahmen fehlt im Stil")
	}
	form := readStatic(t, "../../web/templates/terminal_slide_form.gohtml")
	for _, want := range []string{`name="web_url"`, `name="reload_minutes"`, "Vorschau", "verbieten das Einbetten"} {
		if !strings.Contains(form, want) {
			t.Errorf("Formular enthält %q nicht", want)
		}
	}
}

// Reine Funktionen des Players: Adresse prüfen, Website-Folien ohne Verbindung überspringen, Zwischenspeicher ohne Website.
func TestTerminalScriptWebsiteFunctions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node fehlt")
	}
	src := []byte(readStatic(t, "../../web/static/terminal.js"))
	var fns []string
	for _, name := range []string{"websiteFrameUrl", "showableIndex", "cachePayload"} {
		m := regexp.MustCompile(`(?s)\n  function ` + name + `\(.*?\n  \}\n`).Find(src)
		if m == nil {
			t.Fatalf("Funktion %s nicht gefunden", name)
		}
		fns = append(fns, string(m))
	}
	kinds := regexp.MustCompile(`var CACHEABLE_KINDS = [^\n]*`).Find(src)
	if kinds == nil {
		t.Fatal("CACHEABLE_KINDS nicht gefunden")
	}
	harness := string(kinds) + "\n" + strings.Join(fns, "\n") + `
const out = [];
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) out.push(what + ': ' + g + ' statt ' + w); };
const own = 'https://pdh.firma.de';
eq(websiteFrameUrl('https://example.org/a?b=1', own), 'https://example.org/a?b=1', 'https');
eq(websiteFrameUrl('http://example.org', own), 'http://example.org/', 'http');
eq(websiteFrameUrl('javascript:alert(1)', own), '', 'javascript');
eq(websiteFrameUrl('data:text/html,x', own), '', 'data');
eq(websiteFrameUrl('file:///etc/passwd', own), '', 'file');
eq(websiteFrameUrl('https://u:p@example.org/', own), '', 'zugangsdaten');
eq(websiteFrameUrl('https://pdh.firma.de/terminal', own), '', 'eigener Ursprung');
eq(websiteFrameUrl('/grafana/x', own), '', 'relativ');
eq(websiteFrameUrl('https://example.org/' + 'a'.repeat(2100), own), '', 'zu lang');
eq(websiteFrameUrl(null, own), '', 'null');
const web = {id: 'w', kind: 'website'}, txt = {id: 't', kind: 'text'};
eq(showableIndex([txt, web, txt], 1, false), 1, 'online zeigt Website');
eq(showableIndex([txt, web, txt], 1, true), 2, 'offline überspringt Website');
eq(showableIndex([web, txt], 2, true), 1, 'hinter dem Ende, offline');
eq(showableIndex([web, web], 0, true), -1, 'nur Websites, offline');
eq(showableIndex([web, web], 0, false), 0, 'nur Websites, online');
eq(showableIndex([], 0, true), -1, 'leer');
const p = cachePayload({version: 'v', slides: [txt, web, {id: 'g', kind: 'grafana'}, {id: 'i', kind: 'image'}]}, {});
eq(p.slides.map(s => s.id), ['t', 'i'], 'Zwischenspeicher ohne Website und Grafana');
console.log(out.join('\n'));
`
	cmd := exec.Command(node, "-e", harness)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, e.String())
	}
	if s := strings.TrimSpace(o.String()); s != "" {
		t.Error(s)
	}
}
