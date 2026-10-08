package web

import (
	"strings"
	"testing"
	"time"
)

func TestObligationDueAndState(t *testing.T) {
	done := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if n := obligationNextDue(done, 12); n == nil || n.Format("2006-01-02") != "2027-01-31" {
		t.Errorf("12 Monate: %v", n)
	}
	if obligationNextDue(done, 0) != nil {
		t.Error("einmalig: keine neue Fälligkeit")
	}
	today := time.Date(2026, 10, 8, 15, 0, 0, 0, time.Local)
	at := func(s string) *time.Time { d, _ := time.Parse("2006-01-02", s); return &d }
	for _, c := range []struct {
		next  *time.Time
		state string
		days  int
	}{
		{at("2026-10-07"), "overdue", -1},
		{at("2026-10-08"), "soon", 0},
		{at("2026-11-07"), "soon", 30},
		{at("2026-11-08"), "ok", 31},
		{nil, "none", 0},
	} {
		if s, d := obligationState(c.next, 30, today); s != c.state || d != c.days {
			t.Errorf("%v: %s/%d, erwartet %s/%d", c.next, s, d, c.state, c.days)
		}
	}
	if l, cls := obligationStateLabel("overdue", -3); l != "seit 3 Tagen überfällig" || cls != "b-red" {
		t.Errorf("Label überfällig: %s %s", l, cls)
	}
}

func TestObligationReminderText(t *testing.T) {
	o := obligationView{InfraID: "i1", InfraName: "Presse 3", KindLabel: "Prüfpflicht", Title: "DGUV V3", Basis: "BetrSichV",
		Inspector: "Elektro Maier", NextDue: "07.11.2026", State: "soon", DaysLeft: 30}
	txt := obligationReminderText(o, "https://pdh")
	for _, want := range []string{"Prüfpflicht „DGUV V3“ an Presse 3", "in 30 Tagen fällig", "BetrSichV", "Elektro Maier", "https://pdh/infrastructure/i1?tab=checks"} {
		if !strings.Contains(txt, want) {
			t.Errorf("Erinnerung ohne %q:\n%s", want, txt)
		}
	}
	o.State, o.DaysLeft = "overdue", -2
	if !strings.Contains(obligationReminderText(o, ""), "seit 07.11.2026 überfällig") {
		t.Error("Überfällig-Text fehlt")
	}
}

func TestObligationPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	ob := obligationView{ID: "o1", InfraID: "n1", Kind: "inspection", KindLabel: "Prüfpflicht", Title: "DGUV V3", Basis: "BetrSichV",
		IntervalMonths: 12, RemindDays: 30, NextDue: "07.11.2026", NextDueISO: "2026-11-07", LastDone: "07.11.2025", LastDoneISO: "2025-11-07",
		ResponsibleID: "u1", State: "soon", StateLabel: "in 30 Tagen fällig", StateClass: "b-amber",
		Log: []obligationLog{{DoneOn: "07.11.2025", Result: "defects", Defects: true, Note: "Kabel getauscht"}}}
	gbu := obligationView{ID: "o2", InfraID: "n1", Kind: "risk_assessment", KindLabel: "Gefährdungsbeurteilung", Title: "GBU Presse", State: "none", StateLabel: "ohne Termin"}
	d := InfraDetailData{Node: InfraNodeView{ID: "n1", Name: "Presse 3"}, Types: infraTypes, Obligations: []obligationView{ob, gbu}, ObligationDue: 1,
		Users: []UserOption{{ID: "u1", Name: "Eva Maier"}}, Today: "2026-10-08"}
	d.CanEditInfra = true
	out := renderPage(t, tmpl, "infra_detail", d)
	checkScripts(t, "infra_detail", out)
	for _, want := range []string{`data-tab="checks"`, `data-pane="checks"`, "Prüfpflichten", "Gefährdungsbeurteilungen", "DGUV V3", "GBU Presse",
		`action="/infrastructure/n1/obligations/o1/done"`, `action="/infrastructure/n1/obligations/o1"`, `action="/infrastructure/n1/obligations"`,
		`<option value="u1" selected>Eva Maier</option>`, `value="2026-11-07"`, "Kabel getauscht", "mit Mängeln", `id="ob-new-risk_assessment"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Anlage ohne %q", want)
		}
	}
	// ohne Bearbeitungsrecht: lesen ja, Formulare nein
	d.CanEditInfra = false
	out = renderPage(t, tmpl, "infra_detail", d)
	if strings.Contains(out, "/obligations/o1/done") || strings.Contains(out, `id="ob-new-inspection"`) || !strings.Contains(out, "DGUV V3") {
		t.Error("ohne Recht: Liste sichtbar, Formulare nicht")
	}
	// Überblick auf der Infrastruktur-Seite
	list := renderPage(t, tmpl, "infrastructure", InfraPageData{DueObligations: []obligationView{ob}})
	if !strings.Contains(list, "Fällige Prüfungen &amp; GBU") || !strings.Contains(list, `data-rx-edit="/infrastructure/n1?tab=checks"`) {
		t.Error("Überblick fälliger Prüfungen fehlt")
	}
}
