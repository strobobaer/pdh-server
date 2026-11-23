package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/formula"
	"pdh/internal/core/users"
	"pdh/internal/modules/calc"
)

const (
	calcTestConn  = "11111111-1111-7111-8111-111111111111"
	calcTestValue = "22222222-2222-7222-8222-222222222222"
	calcTestInput = "33333333-3333-7333-8333-333333333333"
	calcTestVer   = "44444444-4444-7444-8444-444444444444"
)

func calcTestPage() CalcPageData {
	return CalcPageData{ConnectionID: calcTestConn, CanWrite: true, Functions: formula.Functions(),
		Values: []calcListRow{
			{ID: calcTestValue, Name: "Gesamtleistung Halle 2", InfraPath: "Halle 2", Value: "37,6", Unit: "kW", Age: "vor 3 s",
				Formula: "A + B + C", Since: "gilt seit 23.11.2026", Planned: 1},
			{ID: "55555555-5555-7555-8555-555555555555", Name: "Wirkungsgrad Presse 1", InfraPath: "Halle 2 › Presse 1",
				Formula: "RUNDEN(A / B * 100; 1)", Since: "gilt seit 23.11.2026",
				Reason: "B „Halle 2 › Presse 1 › Leistung Antrieb“ ist älter als 10 Minuten (letzter Wert 23.11. 14:02)"},
			{ID: "66666666-6666-7666-8666-666666666666", Name: "Tagesmenge", InfraPath: "Halle 2", Value: "12", Unit: "t", Age: "vor 2 h",
				Formula: "A", Since: "gilt seit 02.11.2026", NoAgeCheck: true},
		},
		Form: &calcForm{ID: calcTestValue, Name: "Gesamtleistung Halle 2", InfraID: "77777777-7777-7777-8777-777777777777", InfraPath: "Halle 2",
			Unit: "kW", Decimals: 1, MaxAge: "10", Formula: "A + B + C", ValidFrom: "2026-11-23", Today: "2026-11-23",
			Inputs: []calcFormInput{{Short: "A", MappingID: calcTestInput, Label: "Halle 2 › Presse 1 › Leistung", Value: "12,4", Age: "vor 2 s"}},
			Versions: []calcVersionRow{
				{ID: calcTestVer, Status: calc.StatusPlanned, ValidFrom: "01.12.2026", Formula: "A + B + C + D", By: "Anna Muster", At: "23.11.2026 09:14", Editable: true},
				{ID: "88888888-8888-7888-8888-888888888888", Status: calc.StatusCurrent, ValidFrom: "23.11.2026", Formula: "A + B + C", By: "Anna Muster", At: "23.11.2026 09:10"},
			},
			Usage: calc.Usage{ExportFields: []string{"Ablage › Summe_kW"}, Meters: []string{"Zähler Halle 2"}},
		}}
}

var (
	scriptRe = regexp.MustCompile(`(?is)<script\b.*?</script>|<style\b.*?</style>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
)

// visibleText: was die Person liest – ohne Skripte, Stile und Attribute.
func visibleText(html string) string {
	return tagRe.ReplaceAllString(scriptRe.ReplaceAllString(html, " "), " ")
}

func TestCalcPageRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	raw := renderPage(t, tmpl, "calc_values", calcTestPage())
	page := html.UnescapeString(raw) // die Vorlage schreibt + als Zeichenfolge
	for _, want := range []string{
		"Berechnete Werte", "Berechneter Wert", "Gesamtleistung Halle 2", "37,6", "= A + B + C", "gilt seit 23.11.2026", "1 geplant",
		"kein Wert", "ist älter als 10 Minuten", "Alter der Eingangswerte wird nicht geprüft",
		"Eingangswerte höchstens", "leer = Alter wird nicht geprüft", "Wert suchen", `data-insert="WENN(; ; )"`, `data-insert="MITTELWERT(; )"`,
		"/import/calc/check", "/import/calc/inputs", "/import/calc/values", "Gültig ab", "heute = gilt sofort", "Fassungen", "geplant", "gilt",
		"Anna Muster, 23.11.2026 09:14", "?edit=" + calcTestValue + "&version=" + calcTestVer, "/versions/" + calcTestVer + "/delete",
		"Ablage › Summe_kW", "Zähler Halle 2", "Speichern",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("%q fehlt", want)
		}
	}
	// keine Kennungen in dem, was die Person liest
	text := visibleText(page)
	for _, bad := range []string{"calc:", calcTestValue, calcTestInput, calcTestVer, calcTestConn} {
		if strings.Contains(text, bad) {
			t.Errorf("Kennung %q steht im sichtbaren Text", bad)
		}
	}
	checkScripts(t, "calc_values", raw)

	// ohne Schreibrecht: kein Anlegen, kein Speichern, kein Löschen
	ro := calcTestPage()
	ro.CanWrite = false
	page = renderPage(t, tmpl, "calc_values", ro)
	for _, bad := range []string{"?new=1", `id="cv-save"`, "/delete"} {
		if strings.Contains(page, bad) {
			t.Errorf("ohne Schreibrecht steht %q auf der Seite", bad)
		}
	}
	// ohne Formular: nur die Liste
	list := calcTestPage()
	list.Form = nil
	page = renderPage(t, tmpl, "calc_values", list)
	if strings.Contains(page, `id="cv-form"`) || !strings.Contains(page, "?new=1") {
		t.Error("ohne Formular: nur Liste und Knopf zum Anlegen")
	}
	// leere Liste
	if page := renderPage(t, tmpl, "calc_values", CalcPageData{ConnectionID: calcTestConn, CanWrite: true}); !strings.Contains(page, "Noch kein berechneter Wert") {
		t.Error("Hinweis bei leerer Liste fehlt")
	}
}

func TestCalcRoutesGate(t *testing.T) {
	for path, want := range map[string]string{
		"/import/calc/values":                              "import.write",
		"/import/calc/values/" + calcTestValue + "/delete": "import.write",
		"/import/calc/values/" + calcTestValue + "/versions/" + calcTestVer + "/delete": "import.write",
		"/import/calc/check": "import.read",
	} {
		if _, perm, _, _, ok := matchGate("POST", path); !ok || perm != want {
			t.Errorf("POST %s: Treffer %v, Recht %q, erwartet %q", path, ok, perm, want)
		}
	}
	for _, path := range []string{"/import/calc", "/import/calc/inputs"} {
		if _, perm, _, _, ok := matchGate("GET", path); !ok || perm != "import.read" {
			t.Errorf("GET %s: Treffer %v, Recht %q", path, ok, perm)
		}
	}
}

func calcJSONReq(path, body string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: "99999999-9999-7999-8999-999999999999", Role: "admin"}))
}

func TestCalcSaveGuards(t *testing.T) {
	denied := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	w := httptest.NewRecorder()
	denied.CalcSaveWeb(w, calcJSONReq("/import/calc/values", `{}`))
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return true }}
	h.mlock.locked = true
	w = httptest.NewRecorder()
	h.CalcSaveWeb(w, calcJSONReq("/import/calc/values", `{}`))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Wartung läuft") {
		t.Errorf("Wartung: %d %s", w.Code, w.Body.String())
	}
	h.mlock.locked = false
	w = httptest.NewRecorder()
	h.CalcSaveWeb(w, calcJSONReq("/import/calc/values", `{"valid_from":"morgen"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("ungültiges Datum: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.CalcSaveWeb(w, calcJSONReq("/import/calc/values", `kein json`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("ungültige Eingabe: %d", w.Code)
	}
	w = httptest.NewRecorder()
	denied.CalcCheckWeb(w, calcJSONReq("/import/calc/check", `{}`))
	if w.Code != http.StatusForbidden {
		t.Errorf("Prüfung ohne Leserecht: %d", w.Code)
	}
}

func TestConnectionKindLabelCalc(t *testing.T) {
	if got := connectionKindLabel("import", calc.Kind); got != "Berechnete Werte" {
		t.Errorf("Art calc: %q", got)
	}
	for _, opt := range importKindOptions {
		if opt.Value == calc.Kind {
			t.Error("die Art calc darf in „Neue Verbindung“ nicht zur Wahl stehen")
		}
	}
	if _, ok := connectionKindFields("import", calc.Kind); ok {
		t.Error("die Art calc hat keine Einstellungen")
	}
}

func TestCalcAgeText(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	for want, got := range map[string]string{
		"noch nie":    calcAgeText(nil, now),
		"vor 3 s":     calcAgeText(at(3*time.Second), now),
		"vor 5 min":   calcAgeText(at(5*time.Minute+10*time.Second), now),
		"vor 2 h":     calcAgeText(at(2*time.Hour+5*time.Minute), now),
		"vor 3 Tagen": calcAgeText(at(75*time.Hour), now),
		"vor 0 s":     calcAgeText(at(-2*time.Second), now), // Uhr des Senders geht vor
	} {
		if got != want {
			t.Errorf("Alter: %q, erwartet %q", got, want)
		}
	}
	if got := calcDisplayNumber("37.60"); got != "37,60" {
		t.Errorf("Dezimalkomma: %q", got)
	}
}

func TestCalcListRows(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	at := now.Add(-3 * time.Second)
	rows := calcListRows([]calc.Value{
		{ID: calcTestValue, Name: "Summe", InfraPath: "Halle 2", Unit: "kW", Decimals: 1, MaxAgeMinutes: nil, LastValue: "37.6", LastAt: &at,
			Versions: []calc.Version{
				{ID: "p", Status: calc.StatusPlanned, ValidFrom: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), Formula: "A * 2"},
				{ID: "c", Status: calc.StatusCurrent, ValidFrom: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), Formula: "A + B"},
			}},
		{ID: "x", Name: "Später", Reason: "Die erste Fassung gilt ab 01.12.2026", LastAt: &at,
			Versions: []calc.Version{{ID: "p2", Status: calc.StatusPlanned, ValidFrom: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), Formula: "A"}}},
	}, now)
	if len(rows) != 2 {
		t.Fatalf("Zeilen: %d", len(rows))
	}
	if r := rows[0]; r.Value != "37,6" || r.Age != "vor 3 s" || r.Formula != "A + B" || r.Since != "gilt seit 23.11.2026" || r.Planned != 1 || !r.NoAgeCheck || r.Reason != "" {
		t.Errorf("Zeile 1: %+v", r)
	}
	// noch keine geltende Fassung: die erste geplante steht bei „gilt ab“ und zählt nicht noch einmal
	if r := rows[1]; r.Value != "" || r.Formula != "A" || r.Since != "gilt ab 01.12.2026" || r.Planned != 0 || r.Reason == "" {
		t.Errorf("Zeile 2: %+v", r)
	}
}
