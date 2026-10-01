package web

import (
	"strings"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestAddMonths(t *testing.T) {
	for in, want := range map[string]string{
		"2026-01-31|1":  "2026-02-28",
		"2028-01-31|1":  "2028-02-29",
		"2026-03-15|12": "2027-03-15",
		"2026-11-30|3":  "2027-02-28",
	} {
		parts := strings.Split(in, "|")
		n := map[string]int{"1": 1, "3": 3, "12": 12}[parts[1]]
		if got := addMonths(day(parts[0]), n).Format("2006-01-02"); got != want {
			t.Errorf("addMonths(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestTrainingState(t *testing.T) {
	today := day("2026-10-01")
	p := func(s string) *time.Time { d := day(s); return &d }
	cases := []struct {
		name              string
		required, planned bool
		last              *time.Time
		interval, lead    int
		want, wantUntil   string
	}{
		{"pflicht ohne nachweis", true, false, nil, 12, 30, "missing", ""},
		{"pflicht ohne nachweis, geplant", true, true, nil, 12, 30, "planned", ""},
		{"nicht pflicht, nichts", false, false, nil, 12, 30, "", ""},
		{"gültig", true, false, p("2026-03-01"), 12, 30, "ok", "2027-03-01"},
		{"bald fällig", true, false, p("2025-10-20"), 12, 30, "soon", "2026-10-20"},
		{"genau Vorlauf-Grenze", true, false, p("2025-10-31"), 12, 30, "soon", "2026-10-31"},
		{"abgelaufen", true, false, p("2025-09-01"), 12, 30, "expired", "2026-09-01"},
		{"abgelaufen, aber geplant", true, true, p("2025-09-01"), 12, 30, "planned", "2026-09-01"},
		{"einmalig", true, false, p("2010-01-01"), 0, 30, "ok", ""},
		{"vorhanden, nicht pflicht", false, false, p("2026-03-01"), 12, 30, "extra", "2027-03-01"},
		{"abgelaufen, nicht pflicht", false, false, p("2024-03-01"), 12, 30, "expired", "2025-03-01"},
	}
	for _, c := range cases {
		got, until := trainingState(c.required, c.last, c.interval, c.lead, c.planned, today)
		u := ""
		if until != nil {
			u = until.Format("2006-01-02")
		}
		if got != c.want || u != c.wantUntil {
			t.Errorf("%s: got %q/%q, want %q/%q", c.name, got, u, c.want, c.wantUntil)
		}
	}
}

func TestRequiredForAndDue(t *testing.T) {
	topic := trainingTopic{ID: "t1", Reqs: []trainingReq{{Kind: "role", RefID: "technician"}, {Kind: "group", RefID: "g1"}, {Kind: "department", RefID: "d1"}, {Kind: "user", RefID: "u9"}}}
	for _, c := range []struct {
		p    trainingPerson
		want bool
	}{
		{trainingPerson{ID: "u1", Role: "technician"}, true},
		{trainingPerson{ID: "u2", Role: "worker", Groups: map[string]bool{"g1": true}}, true},
		{trainingPerson{ID: "u3", Role: "worker", DepartmentID: "d1"}, true},
		{trainingPerson{ID: "u9", Role: "worker"}, true},
		{trainingPerson{ID: "u4", Role: "worker", DepartmentID: "d2", Groups: map[string]bool{"g2": true}}, false},
	} {
		if got := requiredFor(topic, c.p); got != c.want {
			t.Errorf("requiredFor(%s) = %v", c.p.ID, got)
		}
	}
	rows := []matrixRow{
		{UserID: "a", Cells: []matrixCell{{Required: true, State: "missing"}}},
		{UserID: "b", Cells: []matrixCell{{Required: true, State: "planned"}}},
		{UserID: "c", Cells: []matrixCell{{Required: false, State: "expired"}}},
		{UserID: "d", Cells: []matrixCell{{Required: true, State: "soon"}}},
		{UserID: "e", Cells: []matrixCell{{Required: true, State: "ok"}}},
	}
	if got := strings.Join(dueForTopic(rows, 0), ","); got != "a,d" {
		t.Errorf("dueForTopic = %s", got)
	}
}

func TestTrainingSessionCompleteness(t *testing.T) {
	s := trainingSession{Content: "- Lastdiagramm\n\n• Sichtprüfung  \n*  "}
	if b := s.Bullets(); len(b) != 2 || b[0] != "Lastdiagramm" || b[1] != "Sichtprüfung" {
		t.Fatalf("Bullets: %q", b)
	}
	m := strings.Join(s.Missing(), "|")
	for _, want := range []string{"Datum", "Schulende/r", "Teilnehmende", "Unterschrift Schulende/r"} {
		if !strings.Contains(m, want) {
			t.Errorf("Missing ohne %q: %s", want, m)
		}
	}
	s.DateISO, s.TrainerDisplay, s.TrainerSigned = "2026-10-01", "Eva", true
	s.Participants = []trainingParticipant{{UserID: "a", Signed: true}, {UserID: "b"}}
	s.SignedCount = 1
	if m := s.Missing(); len(m) != 1 || !strings.Contains(m[0], "1 Unterschrift") {
		t.Errorf("Missing: %v", m)
	}
	s.SignedCount = 2
	if m := s.Missing(); len(m) != 0 {
		t.Errorf("vollständig erwartet: %v", m)
	}
	if normalizeBullets(" a \r\n\r\n b ") != "a\nb" {
		t.Error("normalizeBullets")
	}
}

func TestTrainingPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	until := day("2027-03-01")
	topics := []trainingTopic{
		{ID: "t1", Name: "Stapler", Kind: "qualification", IntervalMonths: 12, LeadDays: 30, ResponsibleID: "u1", ResponsibleName: "Eva",
			Reqs: []trainingReq{{Kind: "role", RefID: "technician", Label: "Techniker"}, {Kind: "user", RefID: "u2", Label: "Max"}}},
		{ID: "t2", Name: "Erste Hilfe", Kind: "training", IntervalMonths: 24},
	}
	d := TrainingsData{CanManage: true, Tab: "matrix", Topics: topics,
		Rows: []matrixRow{{UserID: "u2", Name: "Max Muster", Department: "Elektro", Open: 1, Cells: []matrixCell{
			{TopicID: "t1", State: "ok", Required: true, Label: "03/27", ValidUntil: &until}, {TopicID: "t2", State: "missing", Required: true, Label: "fehlt"}}}},
		OpenSessions: []trainingSessionRow{{ID: "s1", TopicName: "Stapler", AutoCreated: true, Participants: 3, Signed: 1}},
		Users:        []UserOption{{ID: "u1", Name: "Eva"}, {ID: "u2", Name: "Max"}},
		Roles:        []UserOption{{ID: "technician", Name: "Techniker"}},
		MatrixStats:  map[string]int{"ok": 1, "missing": 1},
	}
	out := renderPage(t, tmpl, "trainings", d)
	for _, want := range []string{"Max Muster", "tm-c ok req", "03/27", "fehlt", `value="technician" checked`, `value="u2" selected`, "/trainings/sessions/s1", "1/3", "fällig"} {
		if !strings.Contains(out, want) {
			t.Errorf("Schulungsseite enthält %q nicht", want)
		}
	}
	// ohne Verwaltungsrecht: kein Katalog
	d.CanManage = false
	if out = renderPage(t, tmpl, "trainings", d); strings.Contains(out, `data-pane="topics"`) {
		t.Error("Katalog ohne Recht sichtbar")
	}

	s := &trainingSession{ID: "s1", TopicName: "Stapler", TopicKind: "qualification", Status: "open", DateISO: "2026-10-01", Date: "01.10.2026",
		Content: "Lastdiagramm\nSichtprüfung", TrainerID: "u1", TrainerDisplay: "Eva",
		Participants: []trainingParticipant{{UserID: "u2", Name: "Max", Signed: true, Signature: "data:image/png;base64,AAAA", SignedAt: "01.10.2026 10:00"}, {UserID: "u3", Name: "Ida"}},
		SignedCount:  1}
	sd := TrainingSessionData{Session: s, CanEdit: true, CanManage: true, Users: d.Users, MyUserID: "u1", LastContent: "Alt"}
	out = renderPage(t, tmpl, "training_session", sd)
	for _, want := range []string{"Qualifikationsnachweis", "<li>Lastdiagramm</li>", `tsSign('u3'`, "data:image/png;base64,AAAA", "1/2 unterschrieben", "Zum Abschließen fehlt noch", "vom letzten Termin", "/participants/u3/remove"} {
		if !strings.Contains(out, want) {
			t.Errorf("Nachweis enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "/participants/u2/remove") {
		t.Error("Unterschriebene Person darf nicht entfernbar sein")
	}
	// archiviert: nur Ansicht
	s.Status, s.ArchivedAt, s.SignedCount, s.TrainerSigned = "archived", "01.10.2026 11:00", 2, true
	s.Participants[1].Signed, s.Participants[1].Signature = true, "data:image/png;base64,BBBB"
	sd.CanEdit = false
	out = renderPage(t, tmpl, "training_session", sd)
	if strings.Contains(out, `name="content"`) || strings.Contains(out, `onclick="tsSign(`) || !strings.Contains(out, "archiviert") || !strings.Contains(out, "Wiedervorlage") {
		t.Error("archivierter Nachweis falsch dargestellt")
	}
	// Teilnehmer/in ohne Bearbeitungsrecht unterschreibt nur selbst
	s.Status, s.SignedCount = "open", 1
	s.Participants[1].Signed = false
	sd = TrainingSessionData{Session: s, MyUserID: "u3"}
	out = renderPage(t, tmpl, "training_session", sd)
	if !strings.Contains(out, `tsSign('u3'`) || strings.Contains(out, `tsSign('trainer'`) || strings.Contains(out, `name="content"`) {
		t.Error("Teilnehmer-Ansicht falsch")
	}
}

func TestTrainingUserFragmentRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	type item struct {
		Topic trainingTopic
		Cell  matrixCell
	}
	data := map[string]any{
		"Items":    []item{{trainingTopic{Name: "Stapler", IntervalMonths: 12}, matrixCell{State: "expired", Required: true, Title: "Stapler: gültig bis 01.09.2026"}}},
		"Sessions": []trainingSessionRow{{ID: "s1", TopicName: "Stapler", Date: "01.09.2025"}},
		"Open":     []trainingSessionRow{},
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "training-user", data); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"abgelaufen", "jährlich · Pflicht", "/trainings/sessions/s1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Reiter Schulungen enthält %q nicht", want)
		}
	}
}
