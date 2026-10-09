package web

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	coreusers "pdh/internal/core/users"
)

// Auswählbar: aktive Personen aus Instandhaltung/IT – keine Systemkonten, keine Betrachter.
func TestBoardActorEligible(t *testing.T) {
	ok := &coreusers.User{Active: true, Role: coreusers.RoleTechnician, Department: "Instandhaltung Mechanik"}
	if !boardActorEligible(ok) {
		t.Error("Instandhalter nicht auswählbar")
	}
	for name, u := range map[string]*coreusers.User{
		"nil":       nil,
		"inaktiv":   {Active: false, Role: coreusers.RoleTechnician, Department: "Instandhaltung"},
		"system":    {Active: true, IsSystemUser: true, Role: coreusers.RoleTechnician, Department: "Instandhaltung"},
		"viewer":    {Active: true, Role: coreusers.RoleViewer, Department: "IT"},
		"abteilung": {Active: true, Role: coreusers.RoleTechnician, Department: "Vertrieb"},
	} {
		if boardActorEligible(u) {
			t.Errorf("%s: darf nicht auswählbar sein", name)
		}
	}
	// ohne Einstellung (keine Datenbank): Auswahl überall erlaubt
	if (&Handler{}).boardPickTerminalOnly(t.Context()) {
		t.Error("Standard muss „überall“ sein")
	}
}

// Bearbeitungsfenster: ohne Info, Warten und Kommentar; Person statt Karte.
func TestGlobalDashboardActorPick(t *testing.T) {
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "global_dashboard.gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := c.ExecuteTemplate(&buf, "global_dashboard.gohtml", GlobalDashboardPageData{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	checkScripts(t, "leitstand-auswahl", out)
	for _, want := range []string{`id="actor-id"`, "Wer führt aus?", `id="rfid-field" class="hidden"`, "setActors(data.actors", "actorPayload()", `data-action="accept"`, `data-action="done"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Leitstand ohne %q", want)
		}
	}
	for _, bad := range []string{`id="edit-info-button"`, `data-action="wait"`, `id="comment"`, `id="follow-up-date"`, "Kommentar <span"} {
		if strings.Contains(out, bad) {
			t.Errorf("Leitstand enthält noch %q", bad)
		}
	}

	// Core-Einstellungen: Schalter „nur am Terminal“
	page := renderPage(t, loadTestTemplates(t), "core_settings", CoreSettingsPageData{BoardPickTerminalOnly: true})
	if !strings.Contains(page, `action="/core/settings/board-pick"`) || !strings.Contains(page, `name="terminal_only" checked`) {
		t.Error("Einstellung „Personenauswahl nur am Terminal“ fehlt")
	}
}
