package main

import (
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
	if bash, err := exec.LookPath("bash"); err == nil {
		f := filepath.Join(t.TempDir(), "svc.sh")
		_ = os.WriteFile(f, []byte(s), 0o644)
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("Syntaxfehler %v %s", err, out)
		}
	}
}
