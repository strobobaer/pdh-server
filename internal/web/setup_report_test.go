package web

import (
	"bytes"
	"fmt"
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
}

// Ein Kuchen: Stücke = Anlagen, Anteil an der gesamten Rüstdauer, eigene Farbe je Anlage, Gründe je Anlage.
func TestSetupPiesByPlant(t *testing.T) {
	paths := map[string]string{"p1": "Halle › B", "p2": "Halle › A", "p3": "Halle › C"}
	groups := setupGroupByPlant([]setupAgg{
		{"p1", "r1", "Reinigung", 1, 30}, {"p1", "r2", "Umstellung", 1, 30},
		{"p2", "r2", "Umstellung", 2, 90}, {"p2", "r3", "Hilfsmittel", 1, 7},
		{"p3", "r1", "Reinigung", 3, 13},
	}, func(id string) string { return paths[id] })
	pies := setupPies(groups)
	if len(pies) != 1 || pies[0].Key != "plants" || pies[0].Title != "Alle Anlagen" {
		t.Fatalf("Diagramme: %+v", pies)
	}
	sl := pies[0].Slices
	if len(sl) != 3 || sumPercent(sl) != 100 {
		t.Fatalf("Stücke: %+v", sl)
	}
	// nach Dauer, größte zuerst: A 97, B 60, C 13 von 170
	if sl[0].Label != "Halle › A" || sl[1].Label != "Halle › B" || sl[2].Label != "Halle › C" {
		t.Errorf("Reihenfolge: %+v", sl)
	}
	if sl[0].Key != "p2" || sl[0].Count != 3 || sl[0].Percent != 57 || sl[0].Duration != "1 Std. 37 Min." {
		t.Errorf("Anlage A: %+v", sl[0])
	}
	colors := map[string]bool{}
	for _, s := range sl {
		if !hexColor.MatchString(s.Color) || colors[s.Color] {
			t.Errorf("Farbe %q ungültig oder doppelt", s.Color)
		}
		colors[s.Color] = true
	}
	if sl[0].Color != plantColors([]string{"p1", "p2", "p3"})["p2"] {
		t.Error("Farbe stammt nicht aus plantColors")
	}
	// Gründe der Anlage (setupShares): Summe 100, längster zuerst
	if len(sl[0].Reasons) != 2 || sl[0].Reasons[0].Label != "Umstellung" || sumPercent(sl[0].Reasons) != 100 || sl[0].Reasons[0].Duration != "1 Std. 30 Min." {
		t.Errorf("Gründe: %+v", sl[0].Reasons)
	}
	// dieselben Gruppen ergeben dieselben Farben (Seite und Folie)
	again := setupPies(groups)
	for i := range sl {
		if again[0].Slices[i].Color != sl[i].Color {
			t.Error("Farben nicht stabil")
		}
	}
	// nur abgeschlossene Rüstzeiten mit Dauer 0: Anzahl entscheidet
	p := setupPies(setupGroupByPlant([]setupAgg{{"p1", "r1", "R", 3, 0}, {"p2", "r1", "R", 1, 0}}, func(id string) string { return id }))
	if len(p) != 1 || len(p[0].Slices) != 2 || p[0].Slices[0].Percent != 75 || p[0].Slices[1].Percent != 25 {
		t.Errorf("Dauer 0: %+v", p)
	}
	// unbekannte Anlage (ohne Pfad)
	p = setupPies(setupGroupByPlant([]setupAgg{{"zz", "r1", "R", 1, 5}}, func(string) string { return "" }))
	if p[0].Slices[0].Label != "Unbekannte Anlage" || p[0].Slices[0].Percent != 100 {
		t.Errorf("unbekannte Anlage: %+v", p)
	}
}

// Mehr als acht Anlagen: alle erscheinen (keine „Sonstige“), jede mit eigener Farbe, Summe 100.
func TestSetupPiesManyPlantsNoOther(t *testing.T) {
	var aggs []setupAgg
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("0190a000-0000-7000-8000-%012d", i)
		aggs = append(aggs, setupAgg{id, "r1", "Reinigung", 1, float64(10 + i)})
	}
	pies := setupPies(setupGroupByPlant(aggs, func(id string) string { return "A " + id }))
	sl := pies[0].Slices
	if len(sl) != 20 || sumPercent(sl) != 100 {
		t.Fatalf("Stücke %d, Summe %d", len(sl), sumPercent(sl))
	}
	seen := map[string]bool{}
	for _, s := range sl {
		if s.Label == "Sonstige" || seen[s.Color] {
			t.Errorf("Sonstige oder Farbe doppelt: %+v", s)
		}
		seen[s.Color] = true
	}
}

// Die Folie bekommt dieselben Stücke und Farben, aber ohne Gründe (kein Aufklappen).
func TestSetupSlidePiesStripReasons(t *testing.T) {
	groups := setupGroupByPlant([]setupAgg{{"p1", "r1", "R", 1, 5}, {"p2", "r1", "R", 1, 5}}, func(id string) string { return id })
	full, slide := setupPies(groups), setupSlidePiesFrom(groups)
	if len(slide) != 1 || len(slide[0].Slices) != len(full[0].Slices) {
		t.Fatalf("Folie: %+v", slide)
	}
	for i, s := range slide[0].Slices {
		if s.Reasons != nil || s.Color != full[0].Slices[i].Color || s.Label != full[0].Slices[i].Label {
			t.Errorf("Folienstück %d: %+v", i, s)
		}
	}
	if full[0].Slices[0].Reasons == nil {
		t.Error("die Seite braucht die Gründe")
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
	names := []string{"pieGeometry", "pieText", "pieColor", "pieLine", "pieBindPick", "drawPie", "drawPieReasons", "drawPieSet"}
	body := `
var pieSeq = 0;
%FUNCS%
const out = [];
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) out.push(what + ': ' + g + ' statt ' + w); };
const slices = [
  { label: 'Halle › A', value: 60, count: 3, percent: 60, color: '#3b82f6', duration: '1 Std. 0 Min.' },
  { label: 'Halle › B', value: 39, count: 1, percent: 39, color: '#f59e0b', duration: '39 Min.' },
  { label: '<img src=x onerror=1>', value: 1, count: 2, percent: 1, color: 'url(javascript:1)' }];
const g = pieGeometry(slices, 150, 150, 140);
eq(g.length, 3, 'Stücke');
eq(g[0].d.startsWith('M 150 150 L 150 10 A 140 140 0 1 1'), true, 'erstes Stück beginnt oben, großer Bogen');
eq(g[1].d.includes('A 140 140 0 0 1'), true, 'zweites Stück kleiner Bogen');
eq(pieGeometry([{ value: 5 }], 150, 150, 140)[0].d.endsWith('Z'), true, 'ganzer Kreis');
eq(pieGeometry([{ value: 5 }], 150, 150, 140)[0].d.split('A').length, 3, 'ganzer Kreis aus zwei Bögen');
eq(pieGeometry([], 1, 1, 1), [], 'leer');
eq(pieGeometry(null, 1, 1, 1), [], 'null');
eq(pieGeometry([{ value: 0 }, { value: -1 }], 1, 1, 1), [], 'nur Nullen');
eq(pieText({ slices: slices.slice(0, 2) }), 'Halle › A 60 %, Halle › B 39 %', 'Text');
eq(pieColor('#aBc123'), '#aBc123', 'Farbe gültig');
eq(pieColor('red'), '#94a3b8', 'Farbe ungültig');
eq(pieLine(slices[0]), 'Halle › A – 1 Std. 0 Min. – 60 % – 3 Rüstvorgänge', 'Legendenzeile');
eq(pieLine(slices[1]), 'Halle › B – 39 Min. – 39 % – 1 Rüstvorgang', 'Legendenzeile Einzahl');
eq(pieLine({ label: 'X', percent: 5 }), 'X – 5 %', 'Legendenzeile ohne Dauer und Anzahl');
const box = drawPie({ title: 'Alle Anlagen', slices });
const svg = all(box, 'svg')[0];
eq(svg.attrs.role, 'img', 'role');
eq(all(svg, 'title')[0].textContent, 'Anteil der Anlagen an der Rüstdauer – Alle Anlagen', 'title');
eq(all(svg, 'desc')[0].textContent.startsWith('Halle › A 60 %, Halle › B 39 %'), true, 'desc');
eq(svg.attrs['aria-labelledby'], all(svg, 'title')[0].attrs.id + ' ' + all(svg, 'desc')[0].attrs.id, 'aria-labelledby');
eq(all(svg, 'path').length, 3, 'Pfade');
eq(all(svg, 'path')[2].attrs.fill, '#94a3b8', 'ungültige Farbe wird Grau');
eq(all(svg, 'text').map(x => x.textContent), ['60 %', '39 %'], 'Prozentangaben (1 % nur in der Legende)');
const lis = all(box, 'li');
eq(lis.length, 3, 'Legende');
eq(lis[0].textContent, 'Halle › A – 1 Std. 0 Min. – 60 % – 3 Rüstvorgänge', 'Legende mit Dauer, Anteil, Anzahl');
eq(lis[2].textContent, '<img src=x onerror=1> – 1 % – 2 Rüstvorgänge', 'Beschriftung bleibt Text');
eq(all(svg, 'path')[0].listeners.click, undefined, 'ohne onPick nicht antippbar');
eq(lis[0].attrs.role, undefined, 'ohne onPick keine Schaltfläche');
const empty = drawPie({ title: 'x', slices: [] });
eq(all(empty, 'svg').length, 0, 'leer ohne SVG');
eq(empty.textContent, 'Keine abgeschlossenen Rüstzeiten', 'leer Text');

// Folie: Stücke ohne Gründe, nichts zum Antippen, keine Hinweise
const slide = drawPieSet([{ key: 'plants', title: 'Alle Anlagen', slices }]);
eq(all(slide, 'path').length, 3, 'Folie zeigt alle Anlagen');
eq(all(slide, 'path')[0].listeners.click, undefined, 'Folie nicht antippbar');
eq(slide.children.length, 1, 'Folie ohne Hinweis und Einzelheiten');

// Seite: Stücke mit Gründen, Antippen zeigt die Gründe der Anlage
const withR = slices.map((s, i) => Object.assign({}, s, { key: 'p' + i, reasons: [
  { label: 'Umstellung', value: 30, count: 2, percent: 75, duration: '30 Min.' },
  { label: '<b>Reinigung</b>', value: 10, count: 1, percent: 25, duration: '10 Min.' }] }));
const set = drawPieSet([{ key: 'plants', title: 'Alle Anlagen', slices: withR }]);
eq(all(set, 'select').length, 0, 'keine Auswahl mehr (ein Diagramm für alle Anlagen)');
const detail = set.children[2];
eq(detail.className, 'pie-detail', 'Einzelheiten-Bereich');
eq(detail.children.length, 0, 'zunächst ohne Gründe');
const p0 = all(set, 'path')[0], l1 = all(set, 'li')[1];
eq(p0.attrs.role, 'button', 'Stück ist Schaltfläche');
eq(p0.attrs.tabindex, '0', 'Stück per Tastatur erreichbar');
eq(p0.attrs['aria-pressed'], 'false', 'nicht gewählt');
p0.listeners.click();
eq(detail.children.length, 1, 'Antippen zeigt die Gründe');
eq(detail.children[0].children[0].textContent, 'Rüstgründe – Halle › A', 'Überschrift nennt die Anlage');
const rl = all(detail, 'li');
eq(rl.map(x => x.textContent), ['Umstellung – 30 Min. – 75 % – 2 Rüstvorgänge', '<b>Reinigung</b> – 10 Min. – 25 % – 1 Rüstvorgang'], 'Gründe als Text');
eq(all(detail, 'span').filter(x => x.className === 'pie-rfill').map(x => x.style.width + '/' + x.style.backgroundColor), ['75%/#3b82f6', '25%/#3b82f6'], 'Balken in der Farbe der Anlage');
eq(all(set, 'path')[0].attrs['aria-pressed'], 'true', 'gewähltes Stück markiert');
eq(all(set, 'path')[0].attrs.class.includes('pie-on'), true, 'gewähltes Stück hat Klasse');
l1.listeners.click();
eq(all(detail, 'li').length === 2 && detail.children[0].children[0].textContent, 'Rüstgründe – Halle › B', 'Legendeneintrag wechselt die Anlage');
eq(all(set, 'path')[0].attrs['aria-pressed'], 'false', 'vorige Wahl aufgehoben');
l1.listeners.click();
eq(detail.children.length, 0, 'nochmal Antippen blendet aus');
let prevented = false;
p0.listeners.keydown({ key: 'Enter', preventDefault() { prevented = true; } });
eq(detail.children.length, 1, 'Enter wählt');
eq(prevented, true, 'Enter verhindert Standardaktion');
p0.listeners.keydown({ key: 'x' });
eq(detail.children.length, 1, 'andere Taste tut nichts');
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
