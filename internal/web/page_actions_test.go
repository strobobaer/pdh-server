package web

import (
	"strings"
	"testing"
)

// Seiteneigene Aktionen im Menüband (Entwurf 10.1, 11.10).

func TestDashboardActions(t *testing.T) {
	b := &BaseData{Page: "dashboard", navCan: func(p string) bool { return p == permDefaults }}
	a := pageActions(b)
	if got := keysWhen(a.Band, "editing"); got != "addw,save-edit,cancel-edit" {
		t.Errorf("Bearbeiten: %s", got)
	}
	if got := keysWhen(a.Band, "idle"); !strings.Contains(got, "dashboard-edit") {
		t.Errorf("ohne Bearbeitung: %s", got)
	}
	if !hasAction(a.Band, "dash-standard") {
		t.Error("„Als Standard für alle setzen“ fehlt")
	}
	start := []ribbonGroup{{Items: a.Start}}
	if keysWhen(start, "editing") != "save-edit,cancel-edit" || keysWhen(start, "idle") != "dashboard-edit" {
		t.Errorf("Band Start: %+v", a.Start)
	}
	b.navCan = func(string) bool { return false }
	if hasAction(pageActions(b).Band, "dash-standard") {
		t.Error("„Als Standard“ ohne Recht")
	}
}

func TestDashboardHidesOwnEditButtonsInNewFrame(t *testing.T) {
	tmpl := loadTestTemplates(t)
	classic := renderPage(t, tmpl, "dashboard", DashboardData{})
	if !strings.Contains(classic, `id="dw-save-btn"`) {
		t.Fatal("klassisch: „Fertig“ fehlt")
	}
	d := DashboardData{BaseData: frameData(t, nil).BaseData}
	out := renderPage(t, tmpl, "dashboard", d)
	if strings.Contains(out, `id="dw-save-btn"`) || strings.Contains(out, `id="dw-cancel-btn"`) {
		t.Error("neuer Rahmen: eigene Knöpfe im Inhalt statt im Menüband")
	}
	js := between(out, "window.dwEdit = function", "window.dwSave")
	if !strings.Contains(js, "pdhEdit.begin(") {
		t.Error("Bearbeiten meldet sich nicht bei pdhEdit an")
	}
	if !strings.Contains(out, "pdhEdit.step(") {
		t.Error("Schritte werden nicht gemeldet")
	}
}
