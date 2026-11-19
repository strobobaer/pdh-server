package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Leitstand-Kalender (/global/calendar) ohne Anmeldung: nur die Einstellung des Leitstands
// zählt, nie die Auswahl aus der Adresse (calendar_board.go).

// boardFx: ein Leitstand der Abteilung A mit den Arten Projekt und Störung.
func boardFx(t *testing.T) (*placesFx, string, string, string) {
	t.Helper()
	p := newPlacesFx(t)
	slug := "cal" + p.sfx
	board := p.id(`INSERT INTO leitstaende (slug, name, department_id, types) VALUES ($1, $1, $2::uuid, 'project,fault') RETURNING id::text`, slug, p.deptA)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM leitstaende WHERE id = $1::uuid`, board)
	})
	// Projekt einer anderen Abteilung (Anlage B)
	otherDept := p.named("CALFREMD", p.infraB, "")
	return p, slug, p.otherName, otherDept
}

func TestCalendarGuestIgnoresQuery(t *testing.T) {
	p, slug, _, _ := boardFx(t)
	foreign := "CALFREMD" + p.sfx
	q := url.Values{"b": {slug}, "set": {"1"}, "show": {"project", "ticket", "obligation"}, "dept": {p.deptB}, "group": {p.groupA}, "user": {p.none.ID}, "busy": {"0"}}
	r := httptest.NewRequest("GET", "/global/calendar?"+q.Encode(), nil) // kein Benutzer = Gast
	b := p.h.boardBySlug(r, slug)
	if b == nil {
		t.Fatal("Leitstand nicht gefunden")
	}
	d := p.h.buildCalendar(r, b)
	if d.Dept != p.deptA || d.Group != "" || d.User != "" {
		t.Errorf("Abteilung/Gruppe/Person aus der Adresse übernommen: dept=%q group=%q user=%q", d.Dept, d.Group, d.User)
	}
	if d.Show["ticket"] || d.Show["obligation"] || !d.Show["project"] || !d.Show["fault"] {
		t.Errorf("Arten stammen nicht vom Leitstand: %v", d.Show)
	}
	var names []string
	for _, row := range d.ProjectRows {
		names = append(names, row.Name)
	}
	got := strings.Join(names, "|")
	if strings.Contains(got, foreign) {
		t.Errorf("Projekt einer anderen Abteilung sichtbar: %q", got)
	}
	if !strings.Contains(got, p.mineName) {
		t.Errorf("Projekt der Leitstand-Abteilung fehlt: %q", got)
	}
}

func TestCalendarGuestWithoutBoard(t *testing.T) {
	p, slug, _, _ := boardFx(t)
	get := func(target string) int {
		w := httptest.NewRecorder()
		p.h.CalendarBoardFragment(w, httptest.NewRequest("GET", target, nil))
		return w.Code
	}
	if c := get("/global/calendar?b=gibt-es-nicht" + p.sfx); c != http.StatusNotFound {
		t.Errorf("unbekannter Leitstand: %d, erwartet 404", c)
	}
	if c := get("/global/calendar?b=" + url.QueryEscape("../x")); c != http.StatusNotFound {
		t.Errorf("ungültige Adresse: %d, erwartet 404", c)
	}
	if c := get("/global/calendar?b=" + slug); c != http.StatusOK {
		t.Errorf("gültiger Leitstand: %d, erwartet 200", c)
	}
}

func TestCalendarBoardLoggedInRespectsRights(t *testing.T) {
	p, slug, _, _ := boardFx(t)
	foreign := "CALFREMD" + p.sfx
	rows := func(set string) string {
		r := p.reqFor(p.none, "/global/calendar?b="+slug+set)
		d := p.h.buildCalendar(r, p.h.boardBySlug(r, slug))
		var s []string
		for _, row := range d.ProjectRows {
			s = append(s, row.Name)
		}
		return strings.Join(s, "|")
	}
	// ohne Ansichtsrecht nur beteiligte Projekte – auch im Baustein
	if got := rows(""); strings.Contains(got, foreign) || strings.Contains(got, p.otherName) || !strings.Contains(got, p.mineName) {
		t.Errorf("ohne Recht: %q", got)
	}
}
