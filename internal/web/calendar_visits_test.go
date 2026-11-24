package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Kalender-Ebene „Wartungstermine“: geplante Termine aus dem Jahresplan.
func TestCalKindsPlanned(t *testing.T) {
	for i, k := range calKinds {
		if k.Key != "maintenance" {
			continue
		}
		if i+1 >= len(calKinds) || calKinds[i+1].Key != "maintenance_planned" {
			t.Fatal("„Wartungstermine“ fehlt direkt nach „Wartungen“")
		}
		if p := calKinds[i+1]; p.Label != "Wartungstermine" || p.Color != k.Color || p.Icon != "ti-calendar-repeat" {
			t.Errorf("Ebene: %+v", p)
		}
		return
	}
	t.Fatal("„Wartungen“ fehlt")
}

func TestCalendarRenderVisit(t *testing.T) {
	tmpl := loadTestTemplates(t)
	k := calKindByKey("maintenance_planned")
	chip := func(title string, drag bool, off, dropped bool) calChip {
		c := calChipFor(k)
		c.Title, c.URL, c.Planned, c.OffPlan, c.Dropped, c.PlanURL = title, "/maintenance/plans/p1", true, off, dropped, "/maintenance/plans/p1"
		c.Tip = "Wartungstermin (geplant): " + title
		if off {
			c.Tip += " · weicht vom Plan ab"
		}
		if dropped {
			c.Title, c.Tip = "entfallen · "+title, "Wartungstermin entfallen: "+title
		}
		if drag {
			c.Drag, c.ID, c.AssigneeID, c.GroupID = "visit", "v1", "u1", "g1"
		}
		return c
	}
	d := CalendarData{Show: map[string]bool{}, Counts: map[string]int{},
		Days: []plannerDay{{Date: "2027-03-01"}},
		Rows: []calRow{{UserID: "u1", Name: "Anna", Cells: []calCell{{Date: "2027-03-01", Items: []calChip{
			chip("geplant · Kompressor", true, false, false), chip("geplant · Pumpe", true, true, false), chip("Lüfter", false, false, true)}}}}},
		Users: []UserOption{{ID: "u1", Name: "Anna"}}, Groups: []partnerOption{{Value: "g1", Label: "Schlosser"}}}
	var b bytes.Buffer
	if err := tmpl.ExecuteTemplate(&b, "calendar-board", d); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`data-drag="visit"`, `data-id="v1"`, `data-assignee="u1"`, `data-group="g1"`, "cc it plan", "weicht vom Plan ab",
		"entfallen · Lüfter", `id="cal-visit-dlg"`, "/maintenance/visits/", "gestrichelt = geplanter Wartungstermin"} {
		if !strings.Contains(out, want) {
			t.Errorf("Kalender ohne %q", want)
		}
	}
	if strings.Count(out, `data-drag="visit"`) != 2 {
		t.Error("entfallener Termin darf nicht ziehbar sein")
	}
	d.Guest = true
	b.Reset()
	if err := tmpl.ExecuteTemplate(&b, "calendar-board", d); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `id="cal-visit-dlg"`) {
		t.Error("Gäste bekommen kein Fenster zum Besetzen")
	}
}

// Fenster zum Besetzen: Titel ohne den Zieh-Hinweis des Chips, mittig, und
// nach einem Fehler bleibt es offen (pdhGridPost meldet den Fehlschlag).
func TestCalendarVisitDialogDetails(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c := calChipFor(calKindByKey("maintenance_planned"))
	c.Title, c.Tip, c.URL, c.Planned, c.Drag, c.ID = "geplant · Kompressor", "Wartungstermin (geplant): Kompressor · Halle 1", "/maintenance/plans/p1", true, "visit", "v1"
	d := CalendarData{Show: map[string]bool{}, Counts: map[string]int{}, Days: []plannerDay{{Date: "2027-03-01"}},
		Rows: []calRow{{UserID: "u1", Name: "Anna", Cells: []calCell{{Date: "2027-03-01", Items: []calChip{c}}}}}}
	render := func(name string, data any) string {
		var b bytes.Buffer
		if err := tmpl.ExecuteTemplate(&b, name, data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if out := render("calendar-board", d); !strings.Contains(out, `data-tip="Wartungstermin (geplant): Kompressor · Halle 1"`) {
		t.Error("Chip ohne data-tip – das Fenster hätte nur den Tooltip samt Zieh-Hinweis")
	}
	js := render("calendar-js", nil)
	if !strings.Contains(js, "ch.dataset.tip") || strings.Contains(js, "ch.getAttribute('title')") {
		t.Error("Fenstertitel muss aus data-tip kommen, nicht aus dem Tooltip")
	}
	if !strings.Contains(render("cal-plan-style", nil), ".cal-visit-dlg{margin:auto;") {
		t.Error("Fenster ohne margin:auto – base.gohtml setzt alle Ränder auf 0, es stünde links oben")
	}
	post := render("grid-drag", nil)
	post = post[strings.Index(post, "window.pdhGridPost"):]
	if !strings.Contains(post, "return false") || !strings.Contains(post, "return true") {
		t.Error("pdhGridPost muss Erfolg (true) und Fehlschlag (false) melden")
	}
}

func TestVisitGateRules(t *testing.T) {
	id := "01a1f566-b4d2-783f-b0f7-e6e42ee3deae"
	for _, a := range []string{"move", "staff", "reset"} {
		_, perm, _, _, ok := matchGate(http.MethodPost, "/maintenance/visits/"+id+"/"+a)
		if !ok || perm != "maintenance.edit" {
			t.Errorf("%s: %v %q", a, ok, perm)
		}
	}
}

func TestVisitRoutesLocked(t *testing.T) {
	h := &Handler{tmpl: loadTestTemplates(t)}
	h.hasPermHook = func(*http.Request, string) bool { return true }
	h.mlock.locked = true
	id := "01a1f566-b4d2-783f-b0f7-e6e42ee3deae"
	for name, fn := range map[string]http.HandlerFunc{"move": h.MaintenanceVisitMoveWeb, "staff": h.MaintenanceVisitStaffWeb, "reset": h.MaintenanceVisitResetWeb} {
		r := httptest.NewRequest(http.MethodPost, "/maintenance/visits/"+id+"/"+name, strings.NewReader(`{"to_date":"2027-03-01"}`))
		r = withChiParam(withTestUser(r), "id", id)
		w := httptest.NewRecorder()
		fn(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s während der Wartung: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// withChiParam setzt einen Pfad-Parameter wie der Router.
func withChiParam(r *http.Request, key, value string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}
