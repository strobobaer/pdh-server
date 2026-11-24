package web

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func sumPercent(sl []setupPieSlice) (n int) {
	for _, s := range sl {
		n += s.Percent
	}
	return n
}

func TestSetupGroupByPlantSortsByPathAndGroups(t *testing.T) {
	paths := map[string]string{
		"b":  "Halle 1 › Linie 2 › Presse",
		"a1": "Halle 1 › Linie 1",
		"a":  "Halle 1",
		"c":  "halle 10 › Ofen",
		"d":  "Halle 2",
	}
	aggs := []setupAgg{
		{"d", "r1", "Reinigung", 1, 10},
		{"b", "r1", "Reinigung", 2, 20},
		{"c", "r2", "Umstellung", 1, 5},
		{"a1", "r2", "Umstellung", 1, 30},
		{"b", "r2", "Umstellung", 1, 60},
		{"a", "r1", "Reinigung", 1, 1},
		{"b", "r1", "Reinigung", 1, 10}, // gleicher Grund, wird addiert
	}
	g := setupGroupByPlant(aggs, func(id string) string { return paths[id] })
	var got []string
	for _, x := range g {
		got = append(got, x.Path)
	}
	want := []string{"Halle 1", "Halle 1 › Linie 1", "Halle 1 › Linie 2 › Presse", "halle 10 › Ofen", "Halle 2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Reihenfolge der Anlagen: %v, erwartet %v", got, want)
	}
	press := g[2]
	if press.Count != 4 || press.Minutes != 90 || len(press.Reasons) != 2 {
		t.Fatalf("Gruppe Presse: %+v", press)
	}
	if press.Reasons[0].Name != "Umstellung" || press.Reasons[0].Minutes != 60 || press.Reasons[1].Count != 3 || press.Reasons[1].Minutes != 30 {
		t.Errorf("Gründe der Presse nicht nach Dauer geordnet/addiert: %+v", press.Reasons)
	}
	tot := setupTotals(g)
	if len(tot) != 2 || tot[0].Name != "Umstellung" || tot[0].Minutes != 95 || tot[1].Minutes != 41 || tot[1].Count != 5 {
		t.Errorf("Summen: %+v", tot)
	}
	// unbekannte Anlage (kein Pfad) steht am Ende
	g = setupGroupByPlant([]setupAgg{{"x", "r", "G", 1, 1}, {"b", "r", "G", 1, 1}}, func(id string) string { return paths[id] })
	if g[0].InfraID != "b" || g[1].InfraID != "x" {
		t.Errorf("unbekannte Anlage nicht am Ende: %+v", g)
	}
}

func TestSetupPercentsSumTo100(t *testing.T) {
	cases := [][]float64{
		{1, 1, 1},                // 33,3 je
		{1, 1, 1, 1, 1, 1, 1},    // 14,28 je
		{2, 1},                   // 66,6 / 33,3
		{0.5, 0.5, 99},           // kleine Anteile
		{5, 0, 5},                // Null bleibt 0
		{1},                      // ein Stück
		{33.3, 33.3, 33.4},       //
		{1, 2, 3, 4, 5, 6, 7, 8}, //
	}
	for _, vals := range cases {
		p := setupPercents(vals)
		sum := 0
		for i, x := range p {
			sum += x
			if vals[i] <= 0 && x != 0 {
				t.Errorf("%v: Anteil bei Wert 0: %v", vals, p)
			}
		}
		if sum != 100 {
			t.Errorf("%v: Summe %d statt 100 (%v)", vals, sum, p)
		}
	}
	if p := setupPercents([]float64{0, 0}); p[0] != 0 || p[1] != 0 {
		t.Errorf("ohne Summe: %v", p)
	}
	if p := setupPercents(nil); len(p) != 0 {
		t.Errorf("leer: %v", p)
	}
}

func TestSetupSharesAndPies(t *testing.T) {
	color := func(string) string { return "#3b82f6" }
	// mehr als acht Gründe: der Rest wird „Sonstige“, Summe 100
	var sums []setupReasonSum
	for i := 0; i < 11; i++ {
		sums = append(sums, setupReasonSum{Key: string(rune('a' + i)), Name: string(rune('A' + i)), Count: 1, Minutes: float64(10 + i)})
	}
	sl := setupShares(sums, color)
	if len(sl) != setupPieMax+1 || sl[len(sl)-1].Label != "Sonstige" || sl[len(sl)-1].Color != setupPieOther || sl[len(sl)-1].Count != 3 {
		t.Fatalf("Sonstige: %+v", sl)
	}
	if sumPercent(sl) != 100 {
		t.Errorf("Summe der Prozente %d", sumPercent(sl))
	}
	// Farben innerhalb eines Diagramms nie doppelt (nil = Rang im Diagramm)
	seen := map[string]bool{}
	for _, s := range setupShares(sums, nil) {
		if seen[s.Color] {
			t.Errorf("Farbe doppelt: %s", s.Color)
		}
		seen[s.Color] = true
	}
	// ohne Dauer zählt die Anzahl
	sl = setupShares([]setupReasonSum{{Key: "a", Name: "A", Count: 3}, {Key: "b", Name: "B", Count: 1}}, color)
	if len(sl) != 2 || sl[0].Percent != 75 || sl[1].Percent != 25 {
		t.Errorf("Anzahl statt Dauer: %+v", sl)
	}
	// leer
	if setupShares(nil, color) != nil || setupShares([]setupReasonSum{{Key: "a", Name: "A"}}, color) != nil {
		t.Error("ohne Daten kein Kreisdiagramm")
	}
	if setupPies(nil) != nil {
		t.Error("setupPies ohne Gruppen muss nil sein")
	}

	// Gesamt zuerst, dann je Anlage nach Pfad; Farben je Grund in allen Diagrammen gleich
	paths := map[string]string{"p1": "Halle › B", "p2": "Halle › A"}
	groups := setupGroupByPlant([]setupAgg{
		{"p1", "r1", "Reinigung", 1, 30}, {"p1", "r2", "Umstellung", 1, 30},
		{"p2", "r2", "Umstellung", 2, 90}, {"p2", "r3", "Hilfsmittel", 1, 7},
	}, func(id string) string { return paths[id] })
	pies := setupPies(groups)
	if len(pies) != 3 || pies[0].Key != "all" || pies[0].Title != "Alle Anlagen" || pies[1].Title != "Halle › A" || pies[2].Title != "Halle › B" {
		t.Fatalf("Diagramme: %+v", pies)
	}
	col := map[string]string{}
	for _, p := range pies {
		if sumPercent(p.Slices) != 100 {
			t.Errorf("%s: Summe %d", p.Title, sumPercent(p.Slices))
		}
		for _, s := range p.Slices {
			if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(s.Color) {
				t.Errorf("Farbe %q", s.Color)
			}
			if old, ok := col[s.Label]; ok && old != s.Color {
				t.Errorf("Grund %s hat zwei Farben", s.Label)
			}
			col[s.Label] = s.Color
		}
	}
	// nur abgeschlossene Rüstzeiten mit Dauer 0: Anzahl entscheidet, kein Fehler
	if p := setupPies(setupGroupByPlant([]setupAgg{{"p1", "r1", "R", 2, 0}}, func(string) string { return "X" })); len(p) != 2 || p[0].Slices[0].Percent != 100 {
		t.Errorf("Dauer 0: %+v", p)
	}
}

func TestSetupReportRows(t *testing.T) {
	plants, total, dur := setupReportRows(nil)
	if plants != nil || total != 0 || dur != "" {
		t.Errorf("leer: %v %d %q", plants, total, dur)
	}
	g := setupGroupByPlant([]setupAgg{{"p", "a", "A", 1, 30}, {"p", "b", "B", 1, 15}, {"p", "c", "C", 1, 15}}, func(string) string { return "Halle" })
	plants, total, dur = setupReportRows(g)
	if len(plants) != 1 || total != 3 || dur != "1 Std. 0 Min." {
		t.Fatalf("Zeilen: %+v %d %q", plants, total, dur)
	}
	sum := 0
	for _, r := range plants[0].Reasons {
		sum += r.Pct
	}
	if sum != 100 || plants[0].Reasons[0].Pct != 50 {
		t.Errorf("Anteile: %+v", plants[0].Reasons)
	}
}

// ── JavaScript: Ziffernfeld und Kreisdiagramm (terminal.js und Seite) ──

func extractJSFuncs(t *testing.T, src []byte, names ...string) string {
	t.Helper()
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	var fns []string
	for _, name := range names {
		m := regexp.MustCompile(`(?s)\n  function ` + name + `\(.*?\n  \}\n`).Find(src)
		if m == nil {
			t.Fatalf("Funktion %s nicht gefunden", name)
		}
		fns = append(fns, string(m))
	}
	return strings.Join(fns, "\n")
}

func runNode(t *testing.T, js string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node fehlt")
	}
	cmd := exec.Command(node, "-e", js)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

func TestTerminalKeypadState(t *testing.T) {
	src, err := os.ReadFile("../../web/static/terminal.js")
	if err != nil {
		t.Fatal(err)
	}
	harness := extractJSFuncs(t, src, "keypadState") + `
const out = [];
const eq = (got, want, what) => { if (got !== want) out.push(what + ': ' + got + ' statt ' + want); };
eq(keypadState(false, 'open'), true, 'Antippen klappt auf');
eq(keypadState(true, 'open'), true, 'nochmal Antippen bleibt offen');
eq(keypadState(true, 'ok'), false, 'OK klappt zu');
eq(keypadState(true, 'cancel'), false, 'Schließen klappt zu');
eq(keypadState(true, 'login'), false, 'Anmeldung klappt zu');
eq(keypadState(true, 'idle'), false, 'Rückkehr zum Schoner klappt zu');
eq(keypadState(false, 'ok'), false, 'zu bleibt zu');
eq(keypadState(true, 'unbekannt'), true, 'Unbekanntes lässt offen');
eq(keypadState(false, 'unbekannt'), false, 'Unbekanntes lässt zu');
eq(keypadState(undefined, 'idle'), false, 'ohne Zustand');
eq(keypadState(undefined, undefined), false, 'ohne alles');
if (out.length) console.log(out.join('\n'));
`
	runNode(t, harness)
	js := strings.ReplaceAll(string(src), "\r\n", "\n")
	// Ausklappen nur durch Antippen (click), nicht durch Fokus; Schließen, OK, Anmeldung und Schoner klappen zu.
	for _, want := range []string{
		"cardIn.addEventListener('click', function () { activeIn = cardIn; padEvent('open'); render(); });",
		"padEvent('ok')", "padEvent('cancel')", "padEvent('login')", "padEvent('idle')",
		"document.dispatchEvent(new CustomEvent('pdh-idle'))",
		"window.pdhKbGesture = kbUserGesture;", // Tastaturlogik unverändert vorhanden
	} {
		if !strings.Contains(js, want) {
			t.Errorf("terminal.js: %q fehlt", want)
		}
	}
	if strings.Contains(js, "cardIn.addEventListener('focus', function () { padEvent('open')") {
		t.Error("Fokus allein darf das Ziffernfeld nicht aufklappen")
	}
}

func TestTerminalLoginPanelCollapsed(t *testing.T) {
	h, _ := terminalTestHandler(t)
	rec := httptest.NewRecorder()
	h.renderTerminal(rec, terminalPage{Mode: "info", Idle: 45, CardEvents: true, CanFaults: true, CanTickets: true,
		InfraID: "33333333-3333-3333-3333-333333333333", Name: "Presse", ManualLogin: "card_pin", PasswordButton: true})
	body := rec.Body.String()
	for _, want := range []string{`placeholder="Kartennummer eingeben"`, `id="login-pad" data-login-pad hidden`, `data-login-collapse`, `id="login-password-btn"`, `aria-expanded="false"`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s fehlt", want)
		}
	}
	// Ziffernfeld und PIN-Feld liegen im eingeklappten Bereich, das Kartenfeld davor
	pad := strings.Index(body, `data-login-pad`)
	for _, in := range []string{`id="login-card"`, `id="login-pin"`, `id="login-keypad"`} {
		i := strings.Index(body, in)
		if i < 0 {
			t.Fatalf("%s fehlt", in)
		}
		if (in == `id="login-card"`) == (i > pad) {
			t.Errorf("%s liegt im falschen Bereich", in)
		}
	}
}

// pieHarness: ein kleiner DOM-Ersatz, damit drawPie/drawPieSet ohne Browser laufen.
const pieHarness = `
let ids = 0;
function node(tag) {
  const n = { tag, attrs: {}, children: [], style: {}, listeners: {}, _text: '' };
  n.setAttribute = (k, v) => { n.attrs[k] = String(v); };
  n.appendChild = (c) => { n.children.push(c); c.parent = n; return c; };
  n.removeChild = (c) => { n.children = n.children.filter(x => x !== c); };
  Object.defineProperty(n, 'firstChild', { get() { return n.children[0] || null; } });
  n.addEventListener = (ev, f) => { n.listeners[ev] = f; };
  Object.defineProperty(n, 'textContent', { get() { return n._text + n.children.map(c => c.textContent).join(''); }, set(v) { n._text = String(v); n.children = []; } });
  Object.defineProperty(n, 'innerHTML', { set() { throw new Error('innerHTML verboten'); } });
  return n;
}
const document = { createElement: node, createElementNS: (ns, tag) => node(tag), createTextNode: (t) => { const n = node('#text'); n._text = String(t); return n; } };
const all = (n, tag, acc = []) => { if (n.tag === tag) acc.push(n); n.children.forEach(c => all(c, tag, acc)); return acc; };
`

func TestPieDrawing(t *testing.T) {
	src, err := os.ReadFile("../../web/static/terminal.js")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := os.ReadFile("../../web/templates/setup_report.gohtml")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"pieGeometry", "pieText", "pieColor", "drawPie", "drawPieSet"}
	body := `
var pieSeq = 0;
%FUNCS%
const out = [];
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) out.push(what + ': ' + g + ' statt ' + w); };
const slices = [
  { label: 'Umstellung', value: 60, percent: 60, color: '#3b82f6' },
  { label: 'Reinigung', value: 39, percent: 39, color: '#f59e0b' },
  { label: '<img src=x onerror=1>', value: 1, percent: 1, color: 'url(javascript:1)' }];
const g = pieGeometry(slices, 150, 150, 140);
eq(g.length, 3, 'Stücke');
eq(g[0].d.startsWith('M 150 150 L 150 10 A 140 140 0 1 1'), true, 'erstes Stück beginnt oben, großer Bogen');
eq(g[1].d.includes('A 140 140 0 0 1'), true, 'zweites Stück kleiner Bogen');
eq(pieGeometry([{ value: 5 }], 150, 150, 140)[0].d.endsWith('Z'), true, 'ganzer Kreis');
eq(pieGeometry([{ value: 5 }], 150, 150, 140)[0].d.split('A').length, 3, 'ganzer Kreis aus zwei Bögen');
eq(pieGeometry([], 1, 1, 1), [], 'leer');
eq(pieGeometry(null, 1, 1, 1), [], 'null');
eq(pieGeometry([{ value: 0 }, { value: -1 }], 1, 1, 1), [], 'nur Nullen');
eq(pieText({ slices: slices.slice(0, 2) }), 'Umstellung 60 %, Reinigung 39 %', 'Text');
eq(pieColor('#aBc123'), '#aBc123', 'Farbe gültig');
eq(pieColor('red'), '#94a3b8', 'Farbe ungültig');
const box = drawPie({ title: 'Halle A', slices });
const svg = all(box, 'svg')[0];
eq(svg.attrs.role, 'img', 'role');
eq(all(svg, 'title')[0].textContent, 'Anteil der Rüstgründe – Halle A', 'title');
eq(all(svg, 'desc')[0].textContent.startsWith('Umstellung 60 %, Reinigung 39 %'), true, 'desc');
eq(svg.attrs['aria-labelledby'], all(svg, 'title')[0].attrs.id + ' ' + all(svg, 'desc')[0].attrs.id, 'aria-labelledby');
eq(all(svg, 'path').length, 3, 'Pfade');
eq(all(svg, 'path')[2].attrs.fill, '#94a3b8', 'ungültige Farbe wird Grau');
eq(all(svg, 'text').map(x => x.textContent), ['60 %', '39 %'], 'Prozentangaben (1 % nur in der Legende)');
const lis = all(box, 'li');
eq(lis.length, 3, 'Legende');
eq(lis[2].textContent, '<img src=x onerror=1> – 1 %', 'Beschriftung bleibt Text');
const empty = drawPie({ title: 'x', slices: [] });
eq(all(empty, 'svg').length, 0, 'leer ohne SVG');
eq(empty.textContent, 'Keine abgeschlossenen Rüstzeiten', 'leer Text');
const set = drawPieSet([{ title: 'Alle Anlagen', slices }, { title: 'Halle A', slices: [{ label: 'X', value: 1, percent: 100, color: '#10b981' }] }]);
const sel = all(set, 'select')[0];
eq(all(sel, 'option').map(o => o.textContent), ['Alle Anlagen', 'Halle A'], 'Auswahl');
eq(!!sel.attrs['aria-label'], true, 'Auswahl beschriftet');
sel.value = '1'; sel.listeners.change();
eq(all(set, 'path').length, 1, 'Wechsel zeigt die gewählte Anlage');
eq(all(set, 'li').length, 1, 'Wechsel ersetzt die Legende');
eq(drawPieSet([]).children.length, 0, 'ohne Diagramme leer');
if (out.length) console.log(out.join('\n'));
`
	for name, text := range map[string][]byte{"terminal.js": src, "setup_report.gohtml": tpl} {
		funcs := extractJSFuncs(t, text, names...)
		if !strings.Contains(strings.ReplaceAll(string(text), "\r\n", "\n"), "  var pieSeq = 0;\n") {
			t.Fatalf("%s: pieSeq fehlt", name)
		}
		t.Run(name, func(t *testing.T) {
			runNode(t, pieHarness+strings.Replace(body, "%FUNCS%", funcs, 1))
		})
	}
	// beide Fassungen sind gleich
	a := extractJSFuncs(t, src, names...)
	b := extractJSFuncs(t, tpl, names...)
	if a != b {
		t.Error("Kreisdiagramm-Code in terminal.js und setup_report.gohtml ist nicht identisch")
	}
	if strings.Contains(string(tpl), "innerHTML") || strings.Contains(a, "innerHTML") {
		t.Error("innerHTML ist für Daten verboten")
	}
}
