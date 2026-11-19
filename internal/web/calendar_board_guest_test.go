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
// zählt, nie die Auswahl aus der Adresse (calendar_board.go). Welche Vorgänge und Projekte
// ein Leitstand zeigt, bestimmt boardAllowedIDs – für die Tafel und den Kalender gleich.

// boardWith legt einen Leitstand mit den Arten Projekt und Störung an.
func boardWith(t *testing.T, p *placesFx, slug string, dept, group any) {
	t.Helper()
	board := p.id(`INSERT INTO leitstaende (slug, name, department_id, group_id, types) VALUES ($1, $1, $2::uuid, $3::uuid, 'project,fault') RETURNING id::text`, slug, dept, group)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM leitstaende WHERE id = $1::uuid`, board)
	})
}

// boardFx: ein Leitstand der Abteilung A; dazu ein Projekt an einer Anlage der Abteilung B.
func boardFx(t *testing.T) (*placesFx, string) {
	t.Helper()
	p := newPlacesFx(t)
	slug := "cal" + p.sfx
	boardWith(t, p, slug, p.deptA, nil)
	p.named("CALFREMD", p.infraB, "")
	return p, slug
}

// groupIn: Gruppe in einer Abteilung.
func groupIn(t *testing.T, p *placesFx, label, dept string) string {
	t.Helper()
	id := p.id(`INSERT INTO user_groups (name, department_id) VALUES ($1, $2::uuid) RETURNING id::text`, label+p.sfx, dept)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM user_groups WHERE id = $1::uuid`, id)
	})
	return id
}

// guestCalendar: Kalenderdaten für einen Gast (kein Benutzer in der Anfrage).
func guestCalendar(p *placesFx, slug, query string) CalendarData {
	r := httptest.NewRequest("GET", "/global/calendar?b="+slug+query, nil)
	return p.h.buildCalendar(r, p.h.boardBySlug(r, slug))
}

func projectNames(d CalendarData) string {
	var names []string
	for _, row := range d.ProjectRows {
		names = append(names, row.Name)
	}
	return strings.Join(names, "|")
}

func TestCalendarGuestIgnoresQuery(t *testing.T) {
	p, slug := boardFx(t)
	foreign := "CALFREMD" + p.sfx
	q := url.Values{"set": {"1"}, "show": {"project", "ticket", "obligation"}, "dept": {p.deptB}, "group": {p.groupA}, "user": {p.none.ID}, "busy": {"0"}}
	d := guestCalendar(p, slug, "&"+q.Encode())
	if d.Dept != p.deptA || d.Group != "" || d.User != "" {
		t.Errorf("Abteilung/Gruppe/Person aus der Adresse übernommen: dept=%q group=%q user=%q", d.Dept, d.Group, d.User)
	}
	if d.Show["ticket"] || d.Show["obligation"] || !d.Show["project"] || !d.Show["fault"] {
		t.Errorf("Arten stammen nicht vom Leitstand: %v", d.Show)
	}
	got := projectNames(d)
	if strings.Contains(got, foreign) {
		t.Errorf("Projekt einer anderen Abteilung sichtbar: %q", got)
	}
	if !strings.Contains(got, p.mineName) {
		t.Errorf("Projekt der Leitstand-Abteilung fehlt: %q", got)
	}
}

// Ohne „b“ gilt für Gäste der Standard-Leitstand (es gibt immer einen gültigen); ist „b“ angegeben
// und unbekannt oder ungültig, gibt es 404.
func TestCalendarGuestWithoutValidBoard(t *testing.T) {
	p, slug := boardFx(t)
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
	p, slug := boardFx(t)
	foreign := "CALFREMD" + p.sfx
	r := p.reqFor(p.none, "/global/calendar?b="+slug)
	d := p.h.buildCalendar(r, p.h.boardBySlug(r, slug))
	// ohne Ansichtsrecht nur beteiligte Projekte – auch im Baustein
	if got := projectNames(d); strings.Contains(got, foreign) || strings.Contains(got, p.otherName) || !strings.Contains(got, p.mineName) {
		t.Errorf("ohne Recht: %q", got)
	}
}

// Eine Störung an einer Anlage der Abteilung B, zugewiesen an eine Gruppe der Abteilung A, gehört zum
// Leitstand der Abteilung A (wie auf der Tafel) und bleibt im Kalender sichtbar.
func TestCalendarGuestKeepsBoardRecords(t *testing.T) {
	p, slug := boardFx(t)
	title := "CALSTOER" + p.sfx
	id := p.id(`INSERT INTO faults (title, created_by, infrastructure_id, assigned_group_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text`,
		title, p.creator, p.infraB, p.groupA)
	other := p.id(`INSERT INTO faults (title, created_by, infrastructure_id) VALUES ($1, $2::uuid, $3::uuid) RETURNING id::text`, "CALANDERE"+p.sfx, p.creator, p.infraB)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM faults WHERE id IN ($1::uuid, $2::uuid)`, id, other)
	})
	d := guestCalendar(p, slug, "")
	var titles []string
	for _, row := range d.Rows {
		for _, c := range row.Cells {
			for _, it := range c.Items {
				titles = append(titles, it.Title)
			}
		}
	}
	all := strings.Join(titles, "|")
	if !strings.Contains(all, title) {
		t.Errorf("Störung des Leitstands fehlt im Gast-Kalender: %q", all)
	}
	if strings.Contains(all, "CALANDERE") {
		t.Errorf("fremde Störung sichtbar: %q", all)
	}
}

// Projekte: Leitstand nur mit Gruppe, und Leitstand der Abteilung – dieselbe Regel wie bei Störungen.
func TestCalendarGuestProjectsFollowBoard(t *testing.T) {
	p := newPlacesFx(t)
	groupB := groupIn(t, p, "GrpB", p.deptB)
	p.named("CALEIGEN", "", p.groupA)
	p.named("CALGRPB", "", groupB)
	p.named("CALOHNE", "", "")
	p.named("CALANLB", p.infraB, "")

	// nur Gruppe A
	boardWith(t, p, "calg"+p.sfx, nil, p.groupA)
	got := projectNames(guestCalendar(p, "calg"+p.sfx, ""))
	if !strings.Contains(got, "CALEIGEN"+p.sfx) {
		t.Errorf("Gruppen-Leitstand: eigenes Projekt fehlt: %q", got)
	}
	for _, x := range []string{"CALGRPB", "CALOHNE", "CALANLB", "PLOTHER"} {
		if strings.Contains(got, x) {
			t.Errorf("Gruppen-Leitstand zeigt fremdes Projekt %s: %q", x, got)
		}
	}
	// Abteilung A: Projekt ohne Anlage, dessen Gruppe zu einer anderen Abteilung gehört, fehlt
	boardWith(t, p, "cald"+p.sfx, p.deptA, nil)
	got = projectNames(guestCalendar(p, "cald"+p.sfx, ""))
	if !strings.Contains(got, "CALEIGEN"+p.sfx) {
		t.Errorf("Abteilungs-Leitstand: eigenes Projekt fehlt: %q", got)
	}
	for _, x := range []string{"CALGRPB", "CALOHNE", "CALANLB"} {
		if strings.Contains(got, x) {
			t.Errorf("Abteilungs-Leitstand zeigt fremdes Projekt %s: %q", x, got)
		}
	}
}

// Gäste bekommen weder Filterlisten (Abteilungen, Gruppen, Personen) noch das Filterformular dazu.
func TestCalendarGuestNoNameLists(t *testing.T) {
	p, slug := boardFx(t)
	stranger := p.user("vf_calfremd", p.roleNone)
	p.exec(`UPDATE users SET last_name = $2, department_id = $3::uuid WHERE id = $1::uuid`, stranger, "Zzgast"+p.sfx, p.deptB)
	w := httptest.NewRecorder()
	p.h.CalendarBoardFragment(w, httptest.NewRequest("GET", "/global/calendar?b="+slug, nil))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("Status %d", w.Code)
	}
	for _, s := range []string{"Zzgast" + p.sfx, "VB " + p.sfx, "GrpA " + p.sfx, "Alle Abteilungen", "Alle Personen", p.none.ID} {
		if strings.Contains(body, s) {
			t.Errorf("Gast-Antwort enthält %q", s)
		}
	}
}
