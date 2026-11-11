package web

import (
	"context"
	"sort"
	"strings"
	"testing"

	"pdh/internal/integrations/nextcloud"
)

// fakeNC: Nextcloud im Speicher für den Abgleich.
type fakeNC struct {
	users  map[string]*nextcloud.RemoteUser
	groups map[string]string // id → Anzeigename
	log    []string
}

func newFakeNC() *fakeNC {
	return &fakeNC{users: map[string]*nextcloud.RemoteUser{}, groups: map[string]string{"admin": "admin"}}
}

func (f *fakeNC) add(id, name, email string, enabled bool, groups ...string) {
	f.users[id] = &nextcloud.RemoteUser{ID: id, DisplayName: name, Email: email, Language: "de", Enabled: enabled, Groups: groups}
	for _, g := range groups {
		if _, ok := f.groups[g]; !ok {
			f.groups[g] = g
		}
	}
}

func (f *fakeNC) Users(context.Context) ([]string, error) {
	var out []string
	for id := range f.users {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeNC) UserInfo(_ context.Context, id string) (*nextcloud.RemoteUser, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, nextcloud.ErrNotFound
	}
	c := *u
	c.Groups = append([]string(nil), u.Groups...)
	return &c, nil
}

func (f *fakeNC) CreateUser(_ context.Context, u nextcloud.NewUser) (string, error) {
	if _, ok := f.users[u.UserID]; ok {
		return "", nextcloud.ErrUserExists
	}
	f.users[u.UserID] = &nextcloud.RemoteUser{ID: u.UserID, DisplayName: u.DisplayName, Email: u.Email, Language: u.Language, Enabled: true}
	f.log = append(f.log, "create "+u.UserID)
	return "start", nil
}

func (f *fakeNC) EditUser(_ context.Context, id, key, value string) error {
	u := f.users[id]
	switch key {
	case "displayname":
		u.DisplayName = value
	case "email":
		u.Email = value
	case "language":
		u.Language = value
	}
	f.log = append(f.log, "edit "+id+" "+key)
	return nil
}

func (f *fakeNC) SetEnabled(_ context.Context, id string, on bool) error {
	f.users[id].Enabled = on
	f.log = append(f.log, "enabled "+id)
	return nil
}

func (f *fakeNC) Groups(context.Context) ([]nextcloud.RemoteGroup, error) {
	var out []nextcloud.RemoteGroup
	for id, d := range f.groups {
		out = append(out, nextcloud.RemoteGroup{ID: id, DisplayName: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeNC) CreateGroup(_ context.Context, id, d string) error {
	f.groups[id] = d
	return nil
}

func (f *fakeNC) SetGroupDisplayName(_ context.Context, id, d string) error {
	f.groups[id] = d
	f.log = append(f.log, "rename "+id)
	return nil
}

func (f *fakeNC) DeleteGroup(_ context.Context, id string) error {
	delete(f.groups, id)
	for _, u := range f.users {
		u.Groups = without(u.Groups, id)
	}
	return nil
}

func (f *fakeNC) GroupMembers(_ context.Context, id string) ([]string, error) {
	if _, ok := f.groups[id]; !ok {
		return nil, nextcloud.ErrNotFound
	}
	var out []string
	for uid, u := range f.users {
		if hasFold(u.Groups, id) {
			out = append(out, uid)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeNC) AddToGroup(_ context.Context, uid, gid string) error {
	f.users[uid].Groups = append(f.users[uid].Groups, gid)
	return nil
}

func (f *fakeNC) RemoveFromGroup(_ context.Context, uid, gid string) error {
	f.users[uid].Groups = without(f.users[uid].Groups, gid)
	f.log = append(f.log, "remove "+uid+" "+gid)
	return nil
}

func without(list []string, s string) []string {
	var out []string
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func groupsOf(f *fakeNC, id string) string {
	g := append([]string(nil), f.users[id].Groups...)
	sort.Strings(g)
	return strings.Join(g, ",")
}

// Der Abgleich legt Konten an, setzt die Stammdaten aus dem PDH, baut die Gruppen
// und nimmt weg, was das PDH nicht (mehr) sagt.
func TestNCSyncDirectory(t *testing.T) {
	nc := newFakeNC()
	nc.add("pdhNCadmin", "PDH (Dienstkonto)", "", true, "admin")
	nc.add("anna", "Anna Alt", "alt@example", false, "admin", "pdh", "pdh-rolle-weg", "eigene")
	nc.add("gast", "Gast", "", true, "pdh", "pdh-abt-d1") // kein PDH-Benutzer
	nc.add("chef", "Chef", "chef@example", true, "admin") // gleichnamig, dort Administrator
	nc.add("weg", "Weg Gegangen", "", true, "pdh")
	nc.groups["pdh-abt-d1"] = "Abteilung: Alt"

	people := []ncPerson{
		{ID: "1", Username: "anna", First: "Anna", Last: "Neu", Email: "anna@example", Lang: "de", Role: "techniker", DeptID: "d1", Teams: []string{"g1"}, NCID: "anna", Active: true, Drives: true},
		{ID: "2", Username: "bert", First: "Bert", Last: "Boss", Email: "bert@example", Lang: "en", Role: "admin", WantID: "bert", Active: true, NCAdmin: true, Drives: true, DrivesWrite: true},
		{ID: "3", Username: "chef", First: "Der", Last: "Chef", Email: "chef@example", Role: "admin", WantID: "chef", Active: true},
		{ID: "4", Username: "weg", NCID: "weg", Role: "techniker"},
		{ID: "5", Username: "dora", First: "PDH", Last: "(Dienstkonto)", Role: "admin", NCID: "pdhNCadmin", Active: true},
	}
	groups := []ncGroupDef{
		{ncGroupAll, "PDH"}, {ncGroupDrives, "PDH-Netzlaufwerke"}, {ncGroupDrivesWrite, "PDH-Netzlaufwerke (bearbeiten)"},
		{"pdh-rolle-techniker", "PDH-Rolle: Techniker"}, {"pdh-rolle-admin", "PDH-Rolle: Administrator"},
		{"pdh-abt-d1", "Abteilung: Instandhaltung"}, {"pdh-gruppe-g1", "Gruppe: Schlosser"},
	}
	linked := map[string]string{}
	res := ncSyncDirectory(context.Background(), nc, people, groups, "pdhNCadmin", func(p ncPerson, id string) error {
		linked[p.Username] = id
		return nil
	})
	if len(res.Errors) > 0 {
		t.Fatalf("Fehler: %v", res.Errors)
	}

	// anna: Stammdaten aus dem PDH, wieder freigegeben, kein Administrator mehr, eigene Gruppe bleibt.
	a := nc.users["anna"]
	if a.DisplayName != "Anna Neu" || a.Email != "anna@example" || !a.Enabled {
		t.Errorf("anna: %+v", a)
	}
	if got := groupsOf(nc, "anna"); got != "eigene,pdh,pdh-abt-d1,pdh-gruppe-g1,pdh-laufwerke,pdh-rolle-techniker" {
		t.Errorf("anna Gruppen: %s", got)
	}
	// bert: neu angelegt, verknüpft, Administrator (Rolle + Kennzeichen), Laufwerke nur in der Schreib-Gruppe.
	if linked["bert"] != "bert" || nc.users["bert"] == nil {
		t.Fatalf("bert nicht angelegt/verknüpft: %v", linked)
	}
	if got := groupsOf(nc, "bert"); got != "admin,pdh,pdh-laufwerke-schreiben,pdh-rolle-admin" {
		t.Errorf("bert Gruppen: %s", got)
	}
	// chef: gleichnamiges Administrator-Konto wird ohne Kennzeichen nicht übernommen.
	if _, ok := linked["chef"]; ok || !hasFold(nc.users["chef"].Groups, "admin") {
		t.Errorf("chef wurde übernommen bzw. hat die Rechte verloren: %v", nc.users["chef"].Groups)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "chef") {
		t.Errorf("Hinweis zu chef fehlt: %v", res.Notes)
	}
	// weg: deaktiviert im PDH → gesperrt, nicht gelöscht.
	if nc.users["weg"] == nil || nc.users["weg"].Enabled {
		t.Errorf("weg: %+v", nc.users["weg"])
	}
	// Dienstkonto bleibt Administrator, auch ohne Kennzeichen an der verknüpften Person.
	if !hasFold(nc.users["pdhNCadmin"].Groups, "admin") {
		t.Error("das Dienstkonto hat die Administrator-Rechte verloren")
	}
	// gast: nicht vom PDH → aus den PDH-Gruppen entfernt, Konto bleibt.
	if got := groupsOf(nc, "gast"); got != "" || nc.users["gast"] == nil {
		t.Errorf("gast Gruppen: %q", got)
	}
	// Gruppen: umbenannt, verwaiste Rollen-Gruppe gelöscht, fremde bleibt.
	if nc.groups["pdh-abt-d1"] != "Abteilung: Instandhaltung" {
		t.Errorf("Abteilung nicht umbenannt: %q", nc.groups["pdh-abt-d1"])
	}
	if _, ok := nc.groups["pdh-rolle-weg"]; ok {
		t.Error("verwaiste Rollen-Gruppe nicht gelöscht")
	}
	if _, ok := nc.groups["eigene"]; !ok {
		t.Error("fremde Gruppe gelöscht")
	}
	if res.Created != 1 || res.Disabled != 1 || res.Changed < 1 {
		t.Errorf("Zähler: %+v", res)
	}

	// Zweiter Lauf: nichts mehr zu tun.
	people[1].NCID, people[1].WantID = "bert", ""
	nc.log = nil
	res = ncSyncDirectory(context.Background(), nc, people, groups, "pdhNCadmin", func(ncPerson, string) error { return nil })
	if len(nc.log) != 0 || res.Created+res.Changed+res.Disabled != 0 || len(res.Errors) != 0 {
		t.Errorf("zweiter Lauf ändert noch: %v %+v", nc.log, res)
	}

	// Kennzeichen weg → Administrator-Rechte weg.
	people[1].NCAdmin = false
	res = ncSyncDirectory(context.Background(), nc, people, groups, "pdhNCadmin", func(ncPerson, string) error { return nil })
	if hasFold(nc.users["bert"].Groups, "admin") || len(res.Errors) != 0 {
		t.Errorf("bert ist ohne Kennzeichen noch Administrator: %v %v", nc.users["bert"].Groups, res.Errors)
	}
}

// Beim Einschalten behalten nur die PDH-Administratoren das Kennzeichen, deren
// Nextcloud-Konto dort wirklich Administrator ist.
func TestNCAdminsInNextcloud(t *testing.T) {
	nc := newFakeNC()
	nc.add("anna", "Anna", "", true, "Admin", "pdh")
	nc.add("bert", "Bert", "", true, "pdh")
	got := ncAdminsInNextcloud(context.Background(), nc, []ncAdminCand{
		{ID: "1", Name: "anna", NCID: "anna"},
		{ID: "2", Name: "bert", NCID: "bert"},
		{ID: "3", Name: "fort", NCID: "gibt-es-nicht"},
	})
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("übernommen: %+v", got)
	}
}

func TestNCWantedGroups(t *testing.T) {
	p := ncPerson{Role: "Team Lead", NCAdmin: true}
	if got := strings.Join(p.wantedGroups(), ","); got != "pdh,pdh-rolle-team-lead" {
		t.Errorf("Kennzeichen ohne Rolle Administrator darf nicht reichen: %s", got)
	}
	p = ncPerson{Role: "admin", Drives: true}
	if got := strings.Join(p.wantedGroups(), ","); got != "pdh,pdh-rolle-admin,pdh-laufwerke" {
		t.Errorf("Administrator ohne Kennzeichen: %s", got)
	}
}

// Laufwerke mit PDH-Aufgabe sind für die Lese-Gruppe nur lesbar und für die
// Schreib-Gruppe beschreibbar; andere sind für beide gleich.
func TestNCDriveMounts(t *testing.T) {
	got := ncDriveMounts([]networkDrive{
		{Key: "nas", Name: "NAS Halle 1", Kind: "smb", NextcloudShow: true, UseBackup: true},
		{Key: "frei", Name: "Austausch/Team", Kind: "local", Source: "/srv/austausch", NextcloudShow: true},
		{Key: "ro", Name: "Archiv", Kind: "local", Source: "/srv/archiv", NextcloudShow: true, ReadOnly: true},
		{Key: "aus", Name: "Nicht zeigen", Kind: "local", Source: "/srv/x", UseData: true},
	})
	if len(got) != 4 {
		t.Fatalf("Einträge: %+v", got)
	}
	if got[0].Name != "NAS Halle 1" || !got[0].ReadOnly || strings.Join(got[0].Groups, ",") != ncGroupDrives {
		t.Errorf("Lese-Sicht: %+v", got[0])
	}
	if got[1].ReadOnly || strings.Join(got[1].Groups, ",") != ncGroupDrivesWrite || got[1].Path != got[0].Path {
		t.Errorf("Schreib-Sicht: %+v", got[1])
	}
	if got[2].Name != "Austausch_Team" || got[2].ReadOnly || len(got[2].Groups) != 2 || got[2].Path != "/srv/austausch" {
		t.Errorf("Laufwerk ohne Aufgabe: %+v", got[2])
	}
	if !got[3].ReadOnly || len(got[3].Groups) != 2 {
		t.Errorf("schreibgeschütztes Laufwerk: %+v", got[3])
	}
	if ncMountName("../..", "key1") != "key1" || ncMountName("  ", "key2") != "key2" {
		t.Error("ungültiger Name fällt nicht auf den Schlüssel zurück")
	}
}

// Die Karte „Abgleich mit Nextcloud“ zeigt Stand, Ergebnis und die Knöpfe nur mit Recht.
func TestServerConfigNextcloudSyncCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Groups: []envGroupView{{Key: "nextcloud"}}}
	d.Tab = "nextcloud"
	d.NCSync = ncSyncView{On: true, Ready: true, CanManage: true, Last: &ncSyncResult{At: "11.11.2026 17:00", People: 12, Created: 2, Errors: []string{"anna: Fehler"}, Notes: []string{"chef: Hinweis"}}}
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{"Abgleich mit Nextcloud", `action="/admin/server-config/nextcloud/sync"`, `value="run"`, `value="off"`, "Letzter Lauf 11.11.2026 17:00: 12 Personen, 2 Konten angelegt", "anna: Fehler", "chef: Hinweis"} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte enthält %q nicht", want)
		}
	}
	d.NCSync = ncSyncView{Ready: true}
	out = renderPage(t, tmpl, "server_config", d)
	if strings.Contains(out, `action="/admin/server-config/nextcloud/sync"`) || !strings.Contains(out, "Nextcloud-Abgleich einstellen und starten") {
		t.Error("ohne Recht dürfen die Knöpfe nicht erscheinen")
	}
}

// Apps nur für das, was im PDH eingeschaltet ist; Kalender und Kontakte immer.
func TestNCNeededApps(t *testing.T) {
	if got := strings.Join(ncNeededApps(false, false, false), ","); got != "calendar,contacts" {
		t.Errorf("Grundausstattung: %s", got)
	}
	if got := strings.Join(ncNeededApps(true, true, true), ","); got != "calendar,contacts,deck,files_external,user_oidc" {
		t.Errorf("alles eingeschaltet: %s", got)
	}
}
