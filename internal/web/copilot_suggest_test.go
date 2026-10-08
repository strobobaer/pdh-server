package web

import (
	"strings"
	"testing"
)

func copCand(typ, id, title, desc, infra, resolution string, open bool, actions ...string) copCandidate {
	c := copCandidate{ref: copRef{Type: typ, ID: id, Title: title, URL: copKinds[typ].url + id}, desc: desc, infra: infra,
		resolution: resolution, open: open, actions: actions, status: "resolved"}
	if open {
		c.status = "in_progress"
	}
	c.tokens = copTokens(title, desc, "")
	return c
}

func TestCopilotRankAndGroup(t *testing.T) {
	me := copCand("fault", "me", "Scanner liefert keine Daten", "Microtech Scanner an der Bandsäge sendet nichts", "saege", "", true)
	cands := []copCandidate{
		// offen, gleiches Anliegen an gleicher Anlage → Dopplung
		copCand("ticket", "dup", "Scanner sendet keine Daten", "Bandsäge Scanner ohne Daten", "saege", "", true),
		// offen, ganz anderes Thema → keine Dopplung
		copCand("fault", "other", "Hydraulik undicht", "Öl tropft an der Presse", "presse", "", true),
		// gelöst, zweimal mit derselben Lösung (anders geschrieben) → eine Gruppe, 2×
		copCand("fault", "s1", "Scanner liefert keine Daten mehr", "Scanner an der Säge", "saege", "Netzwerkkabel neu gesteckt.", false),
		copCand("ticket", "s2", "Keine Daten vom Scanner", "Scanner Bandsäge", "", "netzwerkkabel NEU gesteckt", false),
		// gelöst, andere Lösung
		copCand("fault", "s3", "Scanner Daten fehlen", "Scanner liefert nichts", "", "Firmware aktualisiert", false, "Firmware 2.1 aufgespielt"),
		// gelöst, aber ohne Lösung und Maßnahmen → kein Vorschlag
		copCand("fault", "s4", "Scanner keine Daten", "", "", "", false),
		// gelöst, anderes Thema → zu unähnlich
		copCand("fault", "s5", "Hydraulikpumpe defekt", "Öl", "", "Pumpe getauscht", false),
	}
	dups, top := copRank(me, cands)
	if len(dups) != 1 || dups[0].ID != "dup" || !dups[0].SameAsset || dups[0].Type != "ticket" {
		t.Fatalf("Dopplungen falsch: %+v", dups)
	}
	for _, s := range top {
		if s.c.ref.ID == "s4" || s.c.ref.ID == "s5" || s.c.open {
			t.Errorf("%s gehört nicht zu den Lösungen", s.c.ref.ID)
		}
	}
	cases := copGroupCases(top, map[string][]string{"s1": {"Patchkabel 2 m"}})
	if len(cases) != 2 {
		t.Fatalf("2 Lösungsgruppen erwartet, erhalten %d: %+v", len(cases), cases)
	}
	var cable *copCase
	for i := range cases {
		if strings.HasPrefix(strings.ToLower(cases[i].Resolution), "netzwerkkabel") {
			cable = &cases[i]
		}
	}
	if cable == nil || cable.Count != 2 || len(cable.More) != 1 {
		t.Fatalf("gleiche Lösung nicht zusammengefasst: %+v", cases)
	}
	// die ähnlichste (gleiche Anlage) steht vorn und bringt ihre Teile mit
	if cable.ID != "s1" || !cable.SameAsset || len(cable.Parts) != 1 {
		t.Errorf("beste Fundstelle der Gruppe falsch: %+v", cable)
	}
	if cases[0].Percent < cases[1].Percent {
		t.Errorf("nicht nach Ähnlichkeit sortiert: %d < %d", cases[0].Percent, cases[1].Percent)
	}
}

func TestCopilotSolutionKey(t *testing.T) {
	a := copCandidate{resolution: "Netzwerkkabel neu gesteckt."}
	b := copCandidate{resolution: "netzwerkkabel  NEU gesteckt"}
	c := copCandidate{actions: []string{"Firmware aktualisiert"}}
	if copSolutionKey(&a) != copSolutionKey(&b) {
		t.Error("Schreibweise darf nicht zählen")
	}
	if copSolutionKey(&c) == "" || copSolutionKey(&c) == copSolutionKey(&a) {
		t.Error("ohne Lösungstext zählen die Maßnahmen")
	}
}

func TestCopilotSidebarRendersSuggestionHooks(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := FaultDetailData{}
	d.CopilotRef = "fault:f1"
	d.Page = "faults"
	out := renderPage(t, tmpl, "fault_detail", d)
	checkScripts(t, "fault_detail", out)
	for _, want := range []string{`id="cop-suggest"`, `const PAGE_REF = 'fault:f1'`, "/copilot/suggest?type=", "cop-alert", "window.pdhCopilotFor"} {
		if !strings.Contains(out, want) {
			t.Errorf("Störungsseite ohne %q", want)
		}
	}
}
