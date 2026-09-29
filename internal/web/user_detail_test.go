package web

import (
	"strings"
	"testing"
)

func TestUserDetailPrivacy(t *testing.T) {
	tmpl := loadTestTemplates(t)
	base := UserDetailData{
		User:    UserView{ID: "u1", FullName: "Eva Muster", Username: "eva", RoleLabel: "Techniker"},
		Master:  userMaster{PersonnelNo: "4711", BrokerFaults: true, Language: "de"},
		Private: userPrivate{Street: "Geheimweg 7", EmergencyName: "Max Muster"},
	}

	// ohne Berechtigung: kein Privat-Reiter und keine privaten Werte im HTML
	noPriv := base
	noPriv.CanEditMaster = true
	out := renderPage(t, tmpl, "user_detail", noPriv)
	checkTabs(t, "user_detail", out, "overview", "master", "work", "fields", "history")
	if strings.Contains(out, `data-pane="private"`) || strings.Contains(out, "Geheimweg") || strings.Contains(out, "Max Muster") {
		t.Error("private Daten ohne Berechtigung sichtbar")
	}
	if !strings.Contains(out, "Broker Störungen") || !strings.Contains(out, "4711") {
		t.Error("Stammdaten/Broker-Kennzeichen fehlen")
	}
	if strings.Contains(out, `name="broker_faults"`) {
		t.Error("Broker-Rollen ohne Benutzerverwaltung änderbar")
	}

	// mit Berechtigung: Privat-Reiter mit Werten, Broker-Checkboxen nur mit Benutzerverwaltung
	priv := base
	priv.CanPrivate, priv.CanEditMaster, priv.CanBroker = true, true, true
	out = renderPage(t, tmpl, "user_detail", priv)
	checkTabs(t, "user_detail", out, "overview", "master", "private", "work", "fields", "history")
	if !strings.Contains(out, "Geheimweg 7") || !strings.Contains(out, `name="broker_faults" checked`) {
		t.Error("Privat-Reiter bzw. Broker-Checkbox fehlt")
	}

	// reine Ansicht (z. B. Person selbst ohne Bearbeitungsrecht): kein Stammdaten-Formular
	view := base
	view.IsSelf, view.CanPrivate = true, true
	out = renderPage(t, tmpl, "user_detail", view)
	if strings.Contains(out, `action="/users/u1/master"`) {
		t.Error("Stammdaten-Formular ohne Bearbeitungsrecht")
	}
	if strings.Contains(out, "pdhOpenDirectChat('u1')") {
		t.Error("Chat mit sich selbst angeboten")
	}
}

func TestBrokerInboxTab(t *testing.T) {
	tab := brokerInboxTab(true)
	if tab.Query != "unassigned=1" || !tab.Active || !strings.Contains(tab.Cond, "assigned_to IS NULL") {
		t.Errorf("brokerInboxTab: %+v", tab)
	}
	for _, k := range []string{"ticket", "fault"} {
		if _, ok := brokerKinds[k]; !ok {
			t.Errorf("Broker-Typ %s fehlt", k)
		}
	}
}
