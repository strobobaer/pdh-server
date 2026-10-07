package web

import (
	"net/http"
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
	for _, k := range []string{"ticket", "fault"} {
		if _, ok := brokerKinds[k]; !ok {
			t.Errorf("Broker-Typ %s fehlt", k)
		}
	}
}

// Das Stammdaten-Formular speichert die Grunddaten ueber UserSaveWeb - es
// muss daher ALLE dort gelesenen Felder mitsenden, sonst wuerden sie geleert.
func TestUserMasterFormSendsAllCoreFields(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{
		User:   UserView{ID: "u1", Username: "eva", FirstName: "Eva", LastName: "Muster", RoleValue: "technician", RoleLabel: "Techniker"},
		Master: userMaster{Language: "de"}, CanEditMaster: true, CanChangeRole: false, IsSelf: true, CanPrivate: true,
	}
	out := renderPage(t, tmpl, "user_detail", d)
	for _, f := range []string{"core_submitted", "username", "first_name", "last_name", "email", "role", "password",
		"nextcloud_user_id", "rfid_uid", "is_system_user", "department", "phone", "manager_id",
		"on_call_duty", "shift_locksmith_1", "shift_locksmith_2", "sharpening", "heating_fill", "shift_leader"} {
		if !strings.Contains(out, `name="`+f+`"`) {
			t.Errorf("Stammdaten-Formular sendet %q nicht", f)
		}
	}
	if !strings.Contains(out, `enctype="multipart/form-data"`) {
		t.Error("UserSaveWeb erwartet multipart/form-data")
	}
	// eigenes Konto: Reiter "Konto & Dienste", Rolle ohne Aenderungsrecht nur als Hidden-Feld
	checkTabs(t, "user_detail(self)", out, "overview", "master", "private", "work", "account", "fields", "history")
	if !strings.Contains(out, `type="hidden" name="role" value="technician"`) {
		t.Error("Rolle ohne Änderungsrecht muss unverändert mitgesendet werden")
	}
}

func TestCaptureWriter(t *testing.T) {
	c := &captureWriter{header: http.Header{}}
	http.Error(c, "keine berechtigung", http.StatusForbidden)
	if c.status != http.StatusForbidden || !strings.Contains(c.body.String(), "keine berechtigung") {
		t.Errorf("captureWriter: %d %q", c.status, c.body.String())
	}
}

func TestUserQualificationsTab(t *testing.T) {
	tmpl := loadTestTemplates(t)
	quals := []qualGroup{
		{Key: "license", Label: "Führerschein", Icon: "ti-car", Items: []userQualView{{ID: "q1", Kind: "license", Title: "Führerschein", Classes: "B, BE", Number: "••••••", ValidLabel: "01.10.2026", State: "soon"}}},
		{Key: "qualification", Label: "Externe Qualifikationen", Icon: "ti-certificate"},
		{Key: "course", Label: "Lehrgänge", Icon: "ti-school", Items: []userQualView{{ID: "q2", Kind: "course", Title: "Ersthelfer", Hours: "9", ValidLabel: "01.01.2020", State: "expired"}}},
	}
	d := UserDetailData{User: UserView{ID: "u1", Username: "eva"}, Master: userMaster{Language: "de"},
		CanEditMaster: true, Quals: quals, QualWarn: 2, QualKinds: qualKinds}
	out := renderPage(t, tmpl, "user_detail", d)
	checkTabs(t, "user_detail(quals)", out, "quals")
	for _, want := range []string{"B, BE", "läuft ab am", "abgelaufen am", "/users/u1/qualifications/q1/delete", `name="hours"`, "2 Nachweis(e)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Nachweise-Reiter enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `name="number"`) {
		t.Error("Nummernfeld ohne Recht auf private Daten")
	}
	d.CanPrivate = true
	if out = renderPage(t, tmpl, "user_detail", d); !strings.Contains(out, `name="number"`) {
		t.Error("Nummernfeld mit Recht auf private Daten fehlt")
	}
}

func TestUserPermissionsTabAndAdminButton(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{User: UserView{ID: "u2", Username: "max", FullName: "Max", RoleLabel: "Techniker"},
		Master: userMaster{Language: "de"}, CanPermissions: true}
	out := renderPage(t, tmpl, "user_detail", d)
	checkTabs(t, "user_detail(perms)", out, "perms")
	if strings.Contains(out, "pdhMakeAdmin('u2'") {
		t.Error("'Admin machen' ohne Admin-Recht sichtbar")
	}
	d.CanMakeAdmin = true
	if out = renderPage(t, tmpl, "user_detail", d); !strings.Contains(out, "pdhMakeAdmin('u2'") {
		t.Error("'Admin machen' für Admins fehlt")
	}
	d.CanPermissions, d.CanMakeAdmin = false, false
	if out = renderPage(t, tmpl, "user_detail", d); strings.Contains(out, `data-pane="perms"`) {
		t.Error("Einzelrechte ohne Berechtigung sichtbar")
	}
}

func TestUsersListDotsOpenEditing(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "users", UsersPageData{
		Users: []UserView{{ID: "u3", FullName: "Eva", Username: "eva", CanEditProfile: true, CanManage: true, Active: true}},
	})
	if !strings.Contains(out, `href="/users/u3?tab=master" title="Bearbeiten"`) {
		t.Error("Drei-Punkte-Knopf führt nicht in die Bearbeitung")
	}
	if strings.Contains(out, `id="umenu-u3"`) || strings.Contains(out, "Admin machen") {
		t.Error("altes Menü bzw. 'Admin machen' noch in der Liste")
	}
}
