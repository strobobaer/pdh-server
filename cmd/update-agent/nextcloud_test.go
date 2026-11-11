package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNCOIDCScript(t *testing.T) {
	ok := ncOIDCRequest{Dir: "/var/www/nextcloud", TenantID: "00000000-0000-0000-0000-000000000001", ClientID: "00000000-0000-0000-0000-000000000002", ClientSecret: "geheim"}
	for name, bad := range map[string]func(r *ncOIDCRequest){
		"dir":      func(r *ncOIDCRequest) { r.Dir = "/var/www/nc; rm -rf /" },
		"tenant":   func(r *ncOIDCRequest) { r.TenantID = "x'; id; '" },
		"client":   func(r *ncOIDCRequest) { r.ClientID = "" },
		"provider": func(r *ncOIDCRequest) { r.Provider = "a'b" },
	} {
		r := ok
		bad(&r)
		if _, err := ncOIDCScript(r); err == nil {
			t.Errorf("%s: ungültige Eingabe akzeptiert", name)
		}
	}
	s, err := ncOIDCScript(ok)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "geheim") || !strings.Contains(s, `--clientsecret="$PDH_OIDC_SECRET"`) {
		t.Error("Secret gehört nicht in den Skripttext")
	}
	for _, want := range []string{"--mapping-uid=email", "auto_provision --value=false", "allow_multiple_user_backends --value=1", "login.microsoftonline.com/00000000-0000-0000-0000-000000000001/v2.0", "user_oidc:provider 'Microsoft'"} {
		if !strings.Contains(s, want) {
			t.Errorf("Skript enthält %q nicht", want)
		}
	}
	ok.OnlyMicrosoft, ok.AutoProvision = true, true
	if s, _ := ncOIDCScript(ok); !strings.Contains(s, "allow_multiple_user_backends --value=0") || !strings.Contains(s, "auto_provision --value=true") {
		t.Error("Optionen nicht übernommen")
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		f := filepath.Join(t.TempDir(), "oidc.sh")
		_ = os.WriteFile(f, []byte(s), 0o644)
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("Syntaxfehler %v %s", err, out)
		}
	}
}

func TestNCScript(t *testing.T) {
	for _, bad := range []string{"", "relativ", "/var/www/nc; rm -rf /", "/var/../etc", "/var/www/$(id)", "/a b"} {
		if _, err := ncScript(bad, "check"); err == nil {
			t.Errorf("%q akzeptiert", bad)
		}
	}
	if _, err := ncScript("/var/www/nextcloud", "rm"); err == nil {
		t.Error("unbekannte Aktion akzeptiert")
	}
	bash, haveBash := exec.LookPath("bash")
	for _, what := range []string{"check", "apps", "system", "all"} {
		s, err := ncScript("/var/www/nextcloud", what)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, `runuser -u "$U" -- php occ`) {
			t.Errorf("%s: occ nicht als Eigentümer", what)
		}
		if haveBash == nil {
			f := filepath.Join(t.TempDir(), what+".sh")
			_ = os.WriteFile(f, []byte(s), 0o644)
			if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("%s: Syntaxfehler %v %s", what, err, out)
			}
		}
	}
	if s, _ := ncScript("/srv/nc", "all"); !strings.Contains(s, "updater/updater.phar --no-interaction") || !strings.Contains(s, "app:update --all") {
		t.Error("System-Update ohne updater.phar/Apps")
	}
}

func TestNCServiceScript(t *testing.T) {
	for _, bad := range []ncServiceRequest{{Dir: "/var/www/nextcloud", User: "a b"}, {Dir: "/var/www/nextcloud", User: "x';id;'"}, {Dir: "rel", User: "pdhNCadmin"}} {
		if _, err := ncServiceScript(bad); err == nil {
			t.Errorf("%+v akzeptiert", bad)
		}
	}
	s, err := ncServiceScript(ncServiceRequest{Dir: "/var/www/nextcloud", User: "pdhNCadmin", Password: "geheim-geheim-123"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "geheim") || !strings.Contains(s, "user:resetpassword --password-from-env 'pdhNCadmin'") ||
		!strings.Contains(s, "user:add --password-from-env") || !strings.Contains(s, "group:adduser admin 'pdhNCadmin'") {
		t.Errorf("Skript:\n%s", s)
	}
	// Die Adresse von Nextcloud geht mit zurück (automatische Ersteinrichtung im PDH).
	if !strings.Contains(s, `echo "URL=$(occ config:system:get overwrite.cli.url`) {
		t.Error("Skript meldet die Nextcloud-Adresse nicht")
	}
	if got := ncURLFromOutput("### Konto anlegen\nSTATUS=created\n URL=https://nc.example \n### fertig"); got != "https://nc.example" {
		t.Errorf("Adresse aus der Ausgabe: %q", got)
	}
	if got := ncURLFromOutput("STATUS=updated\n### fertig"); got != "" {
		t.Errorf("ohne URL-Zeile: %q", got)
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		f := filepath.Join(t.TempDir(), "svc.sh")
		_ = os.WriteFile(f, []byte(s), 0o644)
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("Syntaxfehler %v %s", err, out)
		}
	}
}

// Netzlaufwerke in Nextcloud: nur Einträge unter /PDH-Laufwerke/ gehören dem PDH,
// passende bleiben, alles andere dort wird ersetzt; fremde Speicher bleiben.
func TestNCDrivePlan(t *testing.T) {
	local := `\OC\Files\Storage\Local`
	have := []ncExtMount{
		{ID: 1, Point: "/Fremd", Storage: local, Config: map[string]any{"datadir": "/srv/fremd"}},
		{ID: 2, Point: "/PDH-Laufwerke/NAS", Storage: local, Config: map[string]any{"datadir": "/mnt/pdh/nas"}, Options: map[string]any{"readonly": true}, Groups: []string{"pdh-laufwerke"}},
		{ID: 3, Point: "/PDH-Laufwerke/NAS", Storage: local, Config: map[string]any{"datadir": "/mnt/pdh/nas"}, Options: map[string]any{"readonly": true}, Groups: []string{"pdh-laufwerke-schreiben"}},
		{ID: 4, Point: "/PDH-Laufwerke/Alt", Storage: local, Config: map[string]any{"datadir": "/mnt/pdh/alt"}, Groups: []string{"pdh-laufwerke"}},
		{ID: 5, Point: "/PDH-Laufwerke/Offen", Storage: local, Config: map[string]any{"datadir": "/mnt/pdh/offen"}},
	}
	want := []ncDriveMount{
		{Name: "NAS", Path: "/mnt/pdh/nas", ReadOnly: true, Groups: []string{"pdh-laufwerke"}},
		{Name: "NAS", Path: "/mnt/pdh/nas", ReadOnly: false, Groups: []string{"pdh-laufwerke-schreiben"}},
		{Name: "Offen", Path: "/mnt/pdh/offen", Groups: []string{"pdh-laufwerke-schreiben", "pdh-laufwerke"}},
	}
	del, add := ncDrivePlan(have, want)
	if fmt.Sprint(del) != "[3 4 5]" {
		t.Errorf("zu löschen: %v", del)
	}
	if len(add) != 2 || add[0].ReadOnly || add[0].Name != "NAS" || add[1].Name != "Offen" {
		t.Errorf("anzulegen: %+v", add)
	}
	if del, add := ncDrivePlan(have[:2], want[:1]); len(del)+len(add) != 0 {
		t.Errorf("passender Stand wird angefasst: %v %v", del, add)
	}

	for name, bad := range map[string]ncDrivesRequest{
		"name":       {Dir: "/var/www/nextcloud", Mounts: []ncDriveMount{{Name: "a'b", Path: "/mnt/x", Groups: []string{"g"}}}},
		"pfad":       {Dir: "/var/www/nextcloud", Mounts: []ncDriveMount{{Name: "A", Path: "/mnt/x y", Groups: []string{"g"}}}},
		"punkte":     {Dir: "/var/www/nextcloud", Mounts: []ncDriveMount{{Name: "A", Path: "/mnt/../etc", Groups: []string{"g"}}}},
		"ohneGruppe": {Dir: "/var/www/nextcloud", Mounts: []ncDriveMount{{Name: "A", Path: "/mnt/x"}}},
		"gruppe":     {Dir: "/var/www/nextcloud", Mounts: []ncDriveMount{{Name: "A", Path: "/mnt/x", Groups: []string{"g'; id"}}}},
		"dir":        {Dir: "/var/www/nc; rm -rf /"},
	} {
		if err := validateNCDrives(&bad); err == nil {
			t.Errorf("%s: ungültige Anfrage akzeptiert", name)
		}
	}
	req := ncDrivesRequest{Dir: "/var/www/nextcloud", Mounts: want}
	if err := validateNCDrives(&req); err != nil {
		t.Fatal(err)
	}
	s := ncDrivesApplyScript(req.Dir, del, add)
	// Erst Platzhalter und Gruppen, dann der echte Ordner – nie ein Eintrag für alle.
	i, j, k := strings.Index(s, "datadir=/nonexistent-pdh"), strings.Index(s, "--add-group='pdh-laufwerke-schreiben'"), strings.Index(s, "datadir '/mnt/pdh/nas'")
	if i < 0 || j < i || k < j {
		t.Errorf("Reihenfolge Platzhalter → Gruppen → Ordner stimmt nicht:\n%s", s)
	}
	for _, want := range []string{"files_external:delete 3 --yes", "'/PDH-Laufwerke/Offen'", "readonly false", "--add-group='pdh-laufwerke' --add-group='pdh-laufwerke-schreiben'"} {
		if !strings.Contains(s, want) {
			t.Errorf("Skript enthält %q nicht:\n%s", want, s)
		}
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		for n, src := range map[string]string{"apply": s, "list": ncDrivesListScript(req.Dir, true), "list0": ncDrivesListScript(req.Dir, false)} {
			f := filepath.Join(t.TempDir(), n+".sh")
			_ = os.WriteFile(f, []byte(src), 0o644)
			if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("%s: Syntaxfehler %v %s", n, err, out)
			}
		}
	}
}

// Benötigte Apps: einschalten oder installieren, nie abbrechen, nur gültige Kennungen.
func TestNCAppsScript(t *testing.T) {
	s, err := ncAppsScript(ncAppsRequest{Dir: "/var/www/nextcloud", Apps: []string{"calendar", "files_external"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"occ app:enable 'calendar'", "occ app:install 'files_external'", `echo "APP calendar ok"`, `echo "APP files_external fehlt"`} {
		if !strings.Contains(s, want) {
			t.Errorf("Skript enthält %q nicht", want)
		}
	}
	if strings.Contains(s, "app:disable") || strings.Contains(s, "app:remove") {
		t.Error("das Skript darf keine App abschalten oder entfernen")
	}
	for _, bad := range []ncAppsRequest{{Dir: "/var/www/nextcloud", Apps: []string{"a'; id"}}, {Dir: "/var/www/nextcloud", Apps: []string{"Deck"}}, {Dir: "rel", Apps: []string{"deck"}}} {
		if _, err := ncAppsScript(bad); err == nil {
			t.Errorf("%+v akzeptiert", bad)
		}
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		f := filepath.Join(t.TempDir(), "apps.sh")
		_ = os.WriteFile(f, []byte(s), 0o644)
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("Syntaxfehler %v %s", err, out)
		}
	}
}
