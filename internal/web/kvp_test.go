package web

import (
	"strings"
	"testing"
	"time"
)

func TestKVPQuadrantAndMoney(t *testing.T) {
	cases := []struct {
		b, e int
		want string
	}{{5, 1, "Quick Win"}, {4, 4, "Großprojekt"}, {2, 1, "Lückenfüller"}, {1, 5, "Fragwürdig"}, {0, 3, "nicht bewertet"}}
	for _, c := range cases {
		if _, l, _ := kvpQuadrant(c.b, c.e); l != c.want {
			t.Errorf("kvpQuadrant(%d,%d) = %q, will %q", c.b, c.e, l, c.want)
		}
	}
	for in, want := range map[float64]string{0: "0 €", 12.5: "12,50 €", 1234567: "1.234.567 €", 999.99: "999,99 €"} {
		if got := eur(in); got != want {
			t.Errorf("eur(%v) = %q, will %q", in, got, want)
		}
	}
	if parseMoney("1.234,50 €") != 1234.5 || parseMoney("99.5") != 99.5 || parseMoney("-3") != 0 {
		t.Error("parseMoney falsch")
	}
	if payback(1200, 2400) != "6 Monate" || payback(0, 100) != "sofort" || payback(100, 0) != "–" {
		t.Errorf("payback falsch: %s", payback(1200, 2400))
	}
}

// Die KVP-Regeln: Annehmen nur mit Bewertung und Verantwortlichem, Do nur mit vollstaendigem Plan.
func TestKVPRules(t *testing.T) {
	k := &kvpIdea{Status: "review"}
	if m := kvpMissingAccept(k); len(m) != 2 {
		t.Errorf("Annehmen ohne Bewertung/Verantwortliche: %v", m)
	}
	k.BenefitScore, k.EffortScore, k.ResponsibleID = 4, 2, "u1"
	if m := kvpMissingAccept(k); len(m) != 0 {
		t.Errorf("Annehmen sollte gehen: %v", m)
	}
	if m := kvpMissingPlan(k); len(m) != 4 {
		t.Errorf("Plan unvollstaendig erwartet: %v", m)
	}
	due := time.Now().AddDate(0, 0, 7)
	k.RootCause, k.Target, k.DueDate, k.ActionsTotal = "x", "y", &due, 1
	if m := kvpMissingPlan(k); len(m) != 0 {
		t.Errorf("Plan sollte vollstaendig sein: %v", m)
	}
	past := time.Now().AddDate(0, 0, -2)
	k.Status, k.FeedbackDue = "submitted", &past
	if !k.FeedbackOverdue() {
		t.Error("Rückmeldefrist sollte überschritten sein")
	}
	if (kvpIdea{Number: 7}).Code() != "KVP-0007" {
		t.Error("Code falsch")
	}
}

func TestKVPPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	due := time.Now()
	k := &kvpIdea{ID: "00000000-0000-0000-0000-000000000001", Number: 1, Title: "Regal sichern", Status: "plan", Category: "safety",
		Problem: "wackelt", Proposal: "verschrauben", DueDate: &due, CreatedAt: time.Now()}
	out := renderPage(t, tmpl, "kvp_detail", KVPDetailData{Idea: k, CanWork: true, Scores: []int{1, 2, 3, 4, 5},
		Next: []kvpTransition{{To: "do", Label: "Umsetzung starten (Do)", Missing: []string{"mindestens eine Maßnahme"}}}})
	checkTabs(t, "kvp_detail", out, "overview", "assess", "pdca", "comments", "fields", "docs", "links", "history")
	for _, want := range []string{"KVP-0001", "Es fehlt: mindestens eine Maßnahme", `data-attach-ref="kvp:`, "Maßnahme"} {
		if !strings.Contains(out, want) {
			t.Errorf("kvp_detail enthält %q nicht", want)
		}
	}
	list := renderPage(t, tmpl, "kvp", KVPPageData{Ideas: []kvpIdea{*k}, Statuses: kvpStatuses, Categories: kvpCategories,
		BoardCols: kvpStatuses[2:6], Dash: kvpDashboard{Board: map[string][]kvpIdea{"plan": {*k}}}})
	checkTabs(t, "kvp", list, "dashboard", "ideas", "board", "rules")
	if !strings.Contains(list, "Regal sichern") {
		t.Error("Vorschlag fehlt in Liste/Board")
	}
}
