package web

// „Neues Projekt“ nur mit dem Recht, das die Rechte-Regel für
// POST /api/v1/projects verlangt (projects.edit) – sonst scheiterte erst das
// Speichern. Befund der Abnahme 0.82.3 am Testverbund (20.11.2026).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Migration 185: projects.edit heißt „Projekte bearbeiten“ (nicht mehr nur
// „Zusatzfelder“) – so finden Administratoren das Recht für „Neues Projekt“.
func TestProjectsEditLabel(t *testing.T) {
	pool := dataNetTestPool(t)
	var label string
	if err := pool.QueryRow(context.Background(), `SELECT label FROM permissions WHERE key = 'projects.edit'`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "Projekte bearbeiten" {
		t.Errorf("projects.edit = %q, erwartet „Projekte bearbeiten“", label)
	}
}

func TestProjectsNewButtonNeedsEdit(t *testing.T) {
	tmpl := loadTestTemplates(t)
	without := renderPage(t, tmpl, "projects", ProjectsPageData{Mine: true})
	if strings.Contains(without, "Neues Projekt") || strings.Contains(without, "new-project-form") {
		t.Error("ohne projects.edit: Knopf oder Formular „Neues Projekt“ sichtbar")
	}
	with := renderPage(t, tmpl, "projects", ProjectsPageData{CanCreate: true})
	if !strings.Contains(with, "Neues Projekt") || !strings.Contains(with, "new-project-form") {
		t.Error("mit projects.edit: Knopf oder Formular „Neues Projekt“ fehlt")
	}
}

func TestMayCreateProjectMatchesGate(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/projects", nil)
	for _, c := range []struct {
		perms []string
		want  bool
	}{
		{[]string{"projects.edit"}, true},
		{[]string{"projects.view"}, false},
		{nil, false},
	} {
		have := map[string]bool{}
		for _, p := range c.perms {
			have[p] = true
		}
		h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return have[p] }}
		if got := h.mayCreateProject(r); got != c.want {
			t.Errorf("Rechte %v: mayCreateProject = %v, erwartet %v", c.perms, got, c.want)
		}
		// Dieselbe Entscheidung wie die Rechte-Regel für POST /api/v1/projects.
		_, perm, _, _, ok := matchGate(http.MethodPost, "/api/v1/projects")
		if !ok || perm != "projects.edit" {
			t.Fatalf("Rechte-Regel für POST /api/v1/projects: gefunden=%v, Recht %q", ok, perm)
		}
		if gate := gatePermOK(perm, func(p string) bool { return have[p] }); gate != c.want {
			t.Errorf("Rechte %v: Gate = %v, Knopf = %v", c.perms, gate, c.want)
		}
	}
}
