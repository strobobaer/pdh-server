package web

import (
	"strings"
	"testing"
)

func TestTerminalLocationOnUserDetail(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{
		User:   UserView{ID: "u9", Username: "terminal1", FirstName: "Terminal", LastName: "Halle 2", RoleValue: "worker", RoleLabel: "Werker", IsSystemUser: true},
		Master: userMaster{Language: "de", TerminalInfraID: "i-42", TerminalInfraPath: "Werk › Halle 2 › Linie 3"},
		CanEditMaster: true, CanBroker: true,
	}
	out := renderPage(t, tmpl, "user_detail", d)
	for _, want := range []string{`name="terminal_infrastructure_id" value="i-42"`, "Standort des Terminals", "Werk › Halle 2 › Linie 3", `href="/infrastructure/i-42"`, "Terminal-Standort"} {
		if !strings.Contains(out, want) {
			t.Errorf("Benutzerseite enthält %q nicht", want)
		}
	}
	// ohne Benutzerverwaltung: keine Auswahl (nur Anzeige)
	d.CanBroker = false
	if out := renderPage(t, tmpl, "user_detail", d); strings.Contains(out, `name="terminal_infrastructure_id"`) {
		t.Error("Terminal-Standort ohne Berechtigung änderbar")
	}
}

func TestTerminalInfraInLayout(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{User: UserView{ID: "u1"}}
	d.TerminalInfraID = "i-7"
	out := renderPage(t, tmpl, "user_detail", d)
	if !strings.Contains(out, `window.PDH_TERMINAL_INFRA = "i-7"`) || !strings.Contains(out, "findInfraPath") {
		t.Error("Terminal-Standort fehlt im Seitenkopf")
	}
}

func TestInfraEditControlsNeedPermission(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := InfraPageData{}
	if out := renderPage(t, tmpl, "infrastructure", d); !strings.Contains(out, "fehlt die Berechtigung") {
		t.Error("ohne Berechtigung: Hinweis statt Formular erwartet")
	}
	d.CanEditInfra = true
	d.Types = infraTypes
	out := renderPage(t, tmpl, "infrastructure", d)
	if !strings.Contains(out, "openNewNode(&#39;&#39;)") || !strings.Contains(out, `id="new-node-form"`) {
		t.Error("mit Berechtigung: Formular-Knopf erwartet")
	}
	// normales Formular: htmx wuerde die ganze Antwortseite in den Baum setzen
	if !strings.Contains(out, `<form class="rec-form" method="post" action="/infrastructure">`) || strings.Contains(out, `hx-post="/infrastructure"`) {
		t.Error("Anlegen soll per normalem POST mit Weiterleitung laufen")
	}
}
